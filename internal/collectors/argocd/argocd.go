// Package argocd собирает деплои из Argo CD: кто вручную синкал приложение и
// когда. Источник надёжной атрибуции — поле initiatedBy в истории деплоев
// приложения (status.history[].initiatedBy.username), доступное с Argo CD 2.5+.
//
// Автоматические синки (initiatedBy.automated) — не действие человека и
// пропускаются. История в CRD ограничена revisionHistoryLimit (по умолчанию
// ~10 на приложение), поэтому глубокий бэкфилл невозможен — свежие деплои
// копятся ежедневным сбором.
//
// Прод Argo CD за SSO-прокси (нужен VPN/внутренний DNS); staging обычно
// доступен напрямую. Авторизация — Bearer-токен (argocd account generate-token).
package argocd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// one — клиент одного инстанса Argo CD.
type one struct {
	inst    config.ArgoInstance
	log     *slog.Logger
	client  *http.Client
	apps    []application
	fetched bool
}

// Collector перебирает все сконфигурированные инстансы (среды) Argo CD.
type Collector struct {
	insts []*one
	log   *slog.Logger
}

// New создаёт коллектор по списку инстансов Argo CD.
func New(insts []config.ArgoInstance, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(insts) == 0 {
		return nil, fmt.Errorf("argocd: не задан ни один инстанс")
	}
	c := &Collector{log: log.With("collector", "argocd")}
	for _, inst := range insts {
		if inst.BaseURL == "" || inst.Token == "" {
			return nil, fmt.Errorf("argocd[%s]: нужны URL и TOKEN", inst.Env)
		}
		tr := &http.Transport{}
		if inst.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		}
		c.insts = append(c.insts, &one{inst: inst,
			log:    log.With("collector", "argocd", "env", inst.Env),
			client: &http.Client{Timeout: timeout, Transport: tr}})
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceArgoCD }

type application struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
	} `json:"spec"`
	Status struct {
		History []struct {
			ID          int64     `json:"id"`
			Revision    string    `json:"revision"`
			DeployedAt  time.Time `json:"deployedAt"`
			InitiatedBy struct {
				Username  string `json:"username"`
				Automated bool   `json:"automated"`
			} `json:"initiatedBy"`
			Source struct {
				RepoURL string `json:"repoURL"`
			} `json:"source"`
		} `json:"history"`
	} `json:"status"`
}

type appListResp struct {
	Items []application `json:"items"`
}

// loadApps выкачивает список приложений с историей (один раз за прогон).
func (c *one) loadApps(ctx context.Context) error {
	if c.fetched {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.inst.BaseURL+"/api/v1/applications", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.inst.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("argocd: запрос приложений: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Прокси перед прод-Argo CD отдаёт HTML/302 вместо JSON.
		if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("argocd: доступ отклонён (%d) — токен недействителен или запрос перехвачен SSO-прокси (нужен VPN)", resp.StatusCode)
		}
		return fmt.Errorf("argocd: приложения вернули %d", resp.StatusCode)
	}
	var list appListResp
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return fmt.Errorf("argocd: разбор ответа (возможно, HTML от прокси): %w", err)
	}
	c.apps = list.Items
	c.fetched = true
	return nil
}

// CollectGroup — деплои всей группы за один запрос приложений на инстанс.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	ids := idMap(people)
	out := map[string][]models.Event{}
	for _, o := range c.insts {
		if err := o.collectGroup(ctx, ids, from.UTC(), to.UTC(), out); err != nil {
			c.log.Warn("инстанс недоступен", "env", o.inst.Env, "err", err)
		}
	}
	total := 0
	for _, evs := range out {
		total += len(evs)
	}
	return out, fmt.Sprintf("Argo CD: инстансов %d, деплоев %d", len(c.insts), total), nil
}

// Collect — одиночный человек (через групповой путь).
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result
	byPerson, note, err := c.CollectGroup(ctx, []models.Person{req.Person}, req.From, req.To)
	if err != nil {
		return res, err
	}
	res.Events = byPerson[req.Person.Key]
	res.Note = note
	return res, nil
}

// collectGroup собирает деплои всех людей группы за один запрос приложений.
// ids — username Argo CD (обычно e-mail) → person key.
func (o *one) collectGroup(ctx context.Context, ids map[string]string, from, to time.Time, out map[string][]models.Event) error {
	if err := o.loadApps(ctx); err != nil {
		return err
	}
	for _, app := range o.apps {
		for _, h := range app.Status.History {
			if h.InitiatedBy.Automated || h.InitiatedBy.Username == "" {
				continue // автосинк — не действие человека
			}
			pk, ok := ids[strings.ToLower(strings.TrimSpace(h.InitiatedBy.Username))]
			if !ok {
				continue
			}
			if h.DeployedAt.Before(from) || !h.DeployedAt.Before(to) {
				continue
			}
			rev := h.Revision
			if len(rev) > 8 {
				rev = rev[:8]
			}
			ev := models.Event{
				PersonKey:   pk,
				Source:      models.SourceArgoCD,
				Type:        models.TypeDeploySync,
				ExternalID:  fmt.Sprintf("argocd:%s:%s:%d", o.inst.Env, app.Metadata.Name, h.ID),
				OccurredAt:  h.DeployedAt,
				Title:       fmt.Sprintf("Деплой %s · %s (%s)", app.Metadata.Name, rev, o.inst.Env),
				URL:         o.inst.BaseURL + "/applications/" + app.Metadata.Name,
				Project:     app.Metadata.Name,
				ProjectName: app.Metadata.Name,
				RefID:       h.Revision,
				Meta: map[string]any{
					"env": o.inst.Env, "app": app.Metadata.Name, "project": app.Spec.Project,
					"revision": h.Revision, "repo_url": h.Source.RepoURL, "username": h.InitiatedBy.Username,
				},
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
		}
	}
	return nil
}

// idMap строит username Argo CD → person key для группы.
func idMap(people []models.Person) map[string]string {
	m := map[string]string{}
	for _, p := range people {
		add := func(v string) {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				m[v] = p.Key
				if i := strings.Index(v, "@"); i > 0 {
					m[v[:i]] = p.Key
				}
			}
		}
		add(p.Email)
		add(p.GoogleEmail)
		add(p.GitLabUser)
	}
	return m
}
