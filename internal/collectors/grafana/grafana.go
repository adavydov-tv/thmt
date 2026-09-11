// Package grafana собирает активность в Grafana: правки дашбордов (новые
// версии, /api/dashboards/uid/{uid}/versions) и аннотации на графиках
// (/api/annotations). Атрибуция — по createdBy/login/userId, сопоставленному
// с человеком через /api/org/users.
//
// Несколько сред (prod/staging) через список инстансов. Групповой сбор: обход
// дашбордов и аннотаций делается один раз на среду для всей группы.
//
// Аутентификация — service account token (Bearer). Публичные хосты за
// OAuth-прокси: нужен VPN (сервер ходит через внутренний DNS).
package grafana

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

// one — клиент одного инстанса Grafana.
type one struct {
	inst    config.GrafanaInstance
	log     *slog.Logger
	client  *http.Client
	maxDash int
	verPer  int
}

// Collector перебирает все среды Grafana.
type Collector struct {
	insts   []*one
	log     *slog.Logger
	maxDash int
	verPer  int
}

// New создаёт коллектор по конфигурации.
func New(cfg config.GrafanaConfig, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.Instances) == 0 {
		return nil, fmt.Errorf("grafana: не задан ни один инстанс")
	}
	maxDash, verPer := cfg.MaxDashboards, cfg.VersionsPer
	if maxDash <= 0 {
		maxDash = 500
	}
	if verPer <= 0 {
		verPer = 20
	}
	c := &Collector{log: log.With("collector", "grafana"), maxDash: maxDash, verPer: verPer}
	for _, inst := range cfg.Instances {
		if inst.BaseURL == "" || inst.Token == "" {
			return nil, fmt.Errorf("grafana[%s]: нужны URL и TOKEN", inst.Env)
		}
		tr := &http.Transport{}
		if inst.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		}
		c.insts = append(c.insts, &one{inst: inst,
			log:     log.With("collector", "grafana", "env", inst.Env),
			client:  &http.Client{Timeout: timeout, Transport: tr},
			maxDash: maxDash, verPer: verPer})
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceGrafana }

func (o *one) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.inst.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.inst.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("grafana: доступ отклонён (%d) — токен недействителен или запрос перехвачен OAuth-прокси (нужен VPN)", resp.StatusCode)
		}
		return fmt.Errorf("grafana: %s → %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// userMaps строит login→personKey и userID→personKey по людям группы.
func (o *one) userMaps(ctx context.Context, people []models.Person) (map[string]string, map[int64]string, error) {
	want := map[string]string{}
	for _, p := range people {
		add := func(v string) {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				want[v] = p.Key
				if i := strings.Index(v, "@"); i > 0 {
					want[v[:i]] = p.Key
				}
			}
		}
		add(p.Email)
		add(p.GoogleEmail)
		add(p.GitLabUser)
	}
	byLogin := map[string]string{}
	byID := map[int64]string{}
	var users []struct {
		UserID int64  `json:"userId"`
		Login  string `json:"login"`
		Email  string `json:"email"`
	}
	if err := o.getJSON(ctx, "/api/org/users?perpage=1000", &users); err != nil {
		return nil, nil, err
	}
	for _, u := range users {
		pk := ""
		if v, ok := want[strings.ToLower(u.Login)]; ok {
			pk = v
		} else if v, ok := want[strings.ToLower(u.Email)]; ok {
			pk = v
		}
		if pk != "" {
			byLogin[strings.ToLower(u.Login)] = pk
			byID[u.UserID] = pk
		}
	}
	// «Сырые» совпадения want → byLogin намеренно НЕ добавляются: createdBy
	// сервисного аккаунта, случайно совпавший с чьим-то алиасом (локальной
	// частью email, GitLab-логином), приписывал бы человеку чужие правки.
	// Матчим только пользователей, реально существующих в /api/org/users.
	return byLogin, byID, nil
}

// collectGroup собирает правки дашбордов и аннотации всех людей группы.
func (o *one) collectGroup(ctx context.Context, people []models.Person, from, to time.Time, out map[string][]models.Event) error {
	byLogin, byID, err := o.userMaps(ctx, people)
	if err != nil {
		return err
	}
	if len(byLogin) == 0 && len(byID) == 0 {
		return nil
	}

	// 1) Аннотации — один запрос по диапазону, атрибуция по userId/login.
	var anns []struct {
		ID      int64  `json:"id"`
		UserID  int64  `json:"userId"`
		Login   string `json:"login"`
		Time    int64  `json:"time"` // мс
		Text    string `json:"text"`
		DashUID string `json:"dashboardUID"`
	}
	annPath := fmt.Sprintf("/api/annotations?from=%d&to=%d&limit=5000", from.UnixMilli(), to.UnixMilli())
	if err := o.getJSON(ctx, annPath, &anns); err != nil {
		o.log.Warn("grafana: аннотации недоступны", "err", err)
	}
	for _, a := range anns {
		pk := byID[a.UserID]
		if pk == "" {
			pk = byLogin[strings.ToLower(a.Login)]
		}
		if pk == "" {
			continue
		}
		at := time.UnixMilli(a.Time)
		if at.Before(from) || !at.Before(to) {
			continue
		}
		ev := models.Event{
			PersonKey: pk, Source: models.SourceGrafana, Type: models.TypeGrafanaAnnotation,
			ExternalID: fmt.Sprintf("grafana-ann:%s:%d", o.inst.Env, a.ID),
			OccurredAt: at, Title: "Аннотация: " + oneLine(a.Text, 80) + " (" + o.inst.Env + ")",
			URL:  o.inst.BaseURL + "/d/" + a.DashUID,
			Meta: map[string]any{"env": o.inst.Env, "text": a.Text, "dashboard_uid": a.DashUID},
		}
		ev.Normalize()
		out[pk] = append(out[pk], ev)
	}

	// 2) Правки дашбордов — обход дашбордов и их версий.
	var dashes []struct {
		UID   string `json:"uid"`
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	if err := o.getJSON(ctx, fmt.Sprintf("/api/search?type=dash-db&limit=%d", o.maxDash), &dashes); err != nil {
		return err
	}
	for _, d := range dashes {
		if d.UID == "" {
			continue
		}
		var versions []struct {
			ID        int64  `json:"id"`
			Version   int    `json:"version"`
			Created   string `json:"created"` // RFC3339
			CreatedBy string `json:"createdBy"`
			Message   string `json:"message"`
		}
		vp := fmt.Sprintf("/api/dashboards/uid/%s/versions?limit=%d", d.UID, o.verPer)
		if err := o.getJSON(ctx, vp, &versions); err != nil {
			o.log.Debug("grafana: версии дашборда пропущены", "uid", d.UID, "err", err.Error())
			continue
		}
		for _, v := range versions {
			pk := byLogin[strings.ToLower(strings.TrimSpace(v.CreatedBy))]
			if pk == "" {
				continue // API-key/сервисные правки или не наши
			}
			at, err := time.Parse(time.RFC3339, v.Created)
			if err != nil || at.Before(from) || !at.Before(to) {
				continue
			}
			ev := models.Event{
				PersonKey: pk, Source: models.SourceGrafana, Type: models.TypeGrafanaDashboard,
				ExternalID: fmt.Sprintf("grafana-dash:%s:%s:%d", o.inst.Env, d.UID, v.Version),
				OccurredAt: at,
				Title:      fmt.Sprintf("Правка дашборда «%s» v%d (%s)", d.Title, v.Version, o.inst.Env),
				URL:        o.inst.BaseURL + "/d/" + d.UID,
				Project:    d.Title, ProjectName: d.Title, RefID: d.UID,
				Meta: map[string]any{"env": o.inst.Env, "dashboard": d.Title, "dashboard_uid": d.UID,
					"version": v.Version, "message": v.Message},
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
		}
	}
	return nil
}

// CollectGroup — один обход Grafana на среду для всей группы.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	out := map[string][]models.Event{}
	for _, o := range c.insts {
		if err := o.collectGroup(ctx, people, from.UTC(), to.UTC(), out); err != nil {
			c.log.Warn("инстанс недоступен", "env", o.inst.Env, "err", err)
		}
	}
	total := 0
	for _, evs := range out {
		total += len(evs)
	}
	return out, fmt.Sprintf("Grafana: сред %d, событий %d", len(c.insts), total), nil
}

// Collect — одиночный человек (через групповой путь).
func (c *Collector) Collect(ctx context.Context, r collectors.Request) (collectors.Result, error) {
	var res collectors.Result
	byPerson, note, err := c.CollectGroup(ctx, []models.Person{r.Person}, r.From, r.To)
	if err != nil {
		return res, err
	}
	res.Events = byPerson[r.Person.Key]
	res.Note = note
	return res, nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
