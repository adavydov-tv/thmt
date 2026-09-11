// Package jenkins собирает запуски сборок/деплоев Jenkins: кто запустил job,
// когда, с каким результатом. Атрибуция — actions[].causes[].userId сборки.
// Автозапуски (SCM/таймер) без userId пропускаются.
//
// Поддерживает несколько сред (prod/staging). Групповой сбор: дерево job'ов
// обходится один раз на среду, сборки раскладываются по авторам — так
// N людей × M job'ов не превращаются в N обходов.
//
// Аутентификация — Basic (user + API token). Прод за прокси (нужен VPN).
package jenkins

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// one — клиент одного инстанса Jenkins.
type one struct {
	inst         config.JenkinsInstance
	log          *slog.Logger
	client       *http.Client
	maxJobs      int
	buildsPerJob int
	seen         int
}

// Collector перебирает все среды Jenkins.
type Collector struct {
	insts        []*one
	log          *slog.Logger
	maxJobs      int
	buildsPerJob int
}

// New создаёт коллектор по списку инстансов.
func New(cfg config.JenkinsConfig, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.Instances) == 0 {
		return nil, fmt.Errorf("jenkins: не задан ни один инстанс")
	}
	maxJobs, bpj := cfg.MaxJobs, cfg.BuildsPerJob
	if maxJobs <= 0 {
		maxJobs = 300
	}
	if bpj <= 0 {
		bpj = 50
	}
	c := &Collector{log: log.With("collector", "jenkins"), maxJobs: maxJobs, buildsPerJob: bpj}
	for _, inst := range cfg.Instances {
		if inst.BaseURL == "" || inst.User == "" || inst.Token == "" {
			return nil, fmt.Errorf("jenkins[%s]: нужны URL, USER, TOKEN", inst.Env)
		}
		tr := &http.Transport{}
		if inst.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		}
		c.insts = append(c.insts, &one{inst: inst,
			log:     log.With("collector", "jenkins", "env", inst.Env),
			client:  &http.Client{Timeout: timeout, Transport: tr},
			maxJobs: maxJobs, buildsPerJob: bpj})
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceJenkins }

func (o *one) getJSON(ctx context.Context, apiURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(o.inst.User, o.inst.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("jenkins: доступ отклонён (%d) — проверьте USER/TOKEN или VPN", resp.StatusCode)
		}
		return fmt.Errorf("jenkins: %s → %d", apiURL, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type jobsResp struct {
	Jobs []struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Class string `json:"_class"`
	} `json:"jobs"`
}

type buildsResp struct {
	Builds []struct {
		Number    int64  `json:"number"`
		URL       string `json:"url"`
		Result    string `json:"result"`
		Building  bool   `json:"building"`
		Timestamp int64  `json:"timestamp"`
		Duration  int64  `json:"duration"`
		Actions   []struct {
			Causes []struct {
				UserID   string `json:"userId"`
				UserName string `json:"userName"`
			} `json:"causes"`
		} `json:"actions"`
	} `json:"builds"`
}

func isFolder(class string) bool {
	return strings.Contains(class, "Folder") ||
		strings.Contains(class, "WorkflowMultiBranchProject") ||
		strings.Contains(class, "OrganizationFolder")
}

// walk рекурсивно обходит дерево job'ов, раскладывая сборки по авторам группы.
func (o *one) walk(ctx context.Context, base string, ids map[string]string, from, to time.Time, out map[string][]models.Event) error {
	if o.seen >= o.maxJobs {
		return nil
	}
	var jr jobsResp
	if err := o.getJSON(ctx, base+"/api/json?tree=jobs[name,url,_class]", &jr); err != nil {
		return err
	}
	for _, j := range jr.Jobs {
		if o.seen >= o.maxJobs {
			return nil
		}
		if isFolder(j.Class) {
			if err := o.walk(ctx, strings.TrimRight(j.URL, "/"), ids, from, to, out); err != nil {
				o.log.Debug("папка пропущена", "url", j.URL, "err", err.Error())
			}
			continue
		}
		o.seen++
		var br buildsResp
		tree := fmt.Sprintf("builds[number,url,result,building,timestamp,duration,actions[causes[userId,userName]]]{0,%d}", o.buildsPerJob)
		u := strings.TrimRight(j.URL, "/") + "/api/json?tree=" + url.QueryEscape(tree)
		if err := o.getJSON(ctx, u, &br); err != nil {
			o.log.Debug("сборки job пропущены", "job", j.Name, "err", err.Error())
			continue
		}
		for _, b := range br.Builds {
			at := time.UnixMilli(b.Timestamp)
			if at.Before(from) || !at.Before(to) {
				continue
			}
			var uid, uname string
			for _, act := range b.Actions {
				for _, cs := range act.Causes {
					if cs.UserID != "" {
						uid, uname = cs.UserID, cs.UserName
						break
					}
				}
				if uid != "" {
					break
				}
			}
			pk, ok := ids[strings.ToLower(uid)]
			if !ok {
				continue // автозапуск или не из нашей группы
			}
			res := b.Result
			if b.Building {
				res = "RUNNING"
			}
			ev := models.Event{
				PersonKey:   pk,
				Source:      models.SourceJenkins,
				Type:        models.TypeJenkinsBuild,
				ExternalID:  "jenkins:" + o.inst.Env + ":" + j.URL + strconv.FormatInt(b.Number, 10),
				OccurredAt:  at,
				Title:       fmt.Sprintf("Сборка %s #%d · %s (%s)", j.Name, b.Number, res, o.inst.Env),
				URL:         b.URL,
				Project:     j.Name,
				ProjectName: j.Name,
				Effort:      float64(b.Duration) / 60000,
				EffortUnit:  "мин",
				Meta:        map[string]any{"env": o.inst.Env, "job": j.Name, "build": b.Number, "result": res, "user_id": uid, "user_name": uname},
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
		}
	}
	return nil
}

// idMap строит userId → person key для группы (Jenkins userId = SSO-логин).
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

// CollectGroup — один обход дерева на среду для всей группы людей.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	ids := idMap(people)
	out := map[string][]models.Event{}
	total, jobs := 0, 0
	for _, o := range c.insts {
		o.seen = 0
		if err := o.walk(ctx, o.inst.BaseURL, ids, from.UTC(), to.UTC(), out); err != nil {
			c.log.Warn("инстанс недоступен", "env", o.inst.Env, "err", err)
		}
		jobs += o.seen
	}
	for _, evs := range out {
		total += len(evs)
	}
	return out, fmt.Sprintf("Jenkins: сред %d, обойдено job'ов %d, сборок %d", len(c.insts), jobs, total), nil
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
