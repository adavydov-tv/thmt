// Package allure собирает активность в Allure TestOps: запуски тестов,
// созданные и изменённые тест-кейсы, заведённые дефекты.
//
// Авторизация — API-токен пользователя (профиль → API tokens). Новые версии
// TestOps принимают его напрямую в заголовке «Authorization: Api-Token …»;
// если инстанс отвечает 401, коллектор обменивает токен на JWT через
// /api/uaa/oauth/token и ходит с Bearer.
//
// У TestOps нет фильтра «по автору» в листингах, поэтому коллектор обходит
// проекты, листает сущности по убыванию даты до нижней границы периода и
// отбирает записи, где createdBy/lastModifiedBy совпадает с идентификаторами
// человека (e-mail, его локальная часть или логин GitLab — под SSO логины
// совпадают).
package allure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

const (
	defaultPageSize = 100
	defaultMaxPages = 50
)

// Collector собирает активность одного человека в Allure TestOps.
type Collector struct {
	cfg        config.AllureConfig
	log        *slog.Logger
	timeout    time.Duration
	maxRetries int
	cl         *httpx.Client
	progress   collectors.Progress
}

// New создаёт коллектор с авторизацией по Api-Token.
func New(cfg config.AllureConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.BaseURL == "" || cfg.Token == "" {
		return nil, errors.New("allure: не заданы ALLURE_BASE_URL / ALLURE_TOKEN")
	}
	c := &Collector{cfg: cfg, log: log, timeout: timeout, maxRetries: maxRetries}
	cl, err := c.newClient("Api-Token " + cfg.Token)
	if err != nil {
		return nil, err
	}
	c.cl = cl
	return c, nil
}

func (c *Collector) newClient(authorization string) (*httpx.Client, error) {
	cl, err := httpx.New(httpx.Options{
		BaseURL:    c.cfg.BaseURL,
		Timeout:    c.timeout,
		MaxRetries: c.maxRetries,
		Headers:    map[string]string{"Authorization": authorization},
	})
	if err != nil {
		return nil, fmt.Errorf("allure: %w", err)
	}
	return cl, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceAllure }

// SetProgress подключает колбэк прогресса.
func (c *Collector) SetProgress(p collectors.Progress) { c.progress = p }

func (c *Collector) report(stage string, done, total int) {
	if c.progress != nil {
		c.progress(stage, done, total)
	}
}

// ---- модели ответов TestOps (Spring pageable) ----

type page[T any] struct {
	Content    []T  `json:"content"`
	Last       bool `json:"last"`
	TotalPages int  `json:"totalPages"`
}

type project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type launch struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CreatedBy   string `json:"createdBy"`
	CreatedDate int64  `json:"createdDate"`
	Closed      bool   `json:"closed"`
}

type testCase struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	CreatedBy        string `json:"createdBy"`
	CreatedDate      int64  `json:"createdDate"`
	LastModifiedBy   string `json:"lastModifiedBy"`
	LastModifiedDate int64  `json:"lastModifiedDate"`
}

type defect struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CreatedBy   string `json:"createdBy"`
	CreatedDate int64  `json:"createdDate"`
}

// Collect выгружает активность за период.
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	ids := identities(req.Person)
	if len(ids) == 0 {
		return collectors.Result{}, errors.New("allure: у пользователя нет ни e-mail, ни логина для сопоставления")
	}

	if err := c.ensureAuth(ctx); err != nil {
		return collectors.Result{}, err
	}

	projects, err := c.fetchProjects(ctx)
	if err != nil {
		return collectors.Result{}, err
	}
	if len(projects) == 0 {
		return collectors.Result{Note: "нет доступных проектов"}, nil
	}

	var events []models.Event
	for i, p := range projects {
		c.report("проекты", i+1, len(projects))
		evs, err := c.collectProject(ctx, req, p, ids)
		if err != nil {
			// Один недоступный проект не должен ронять весь сбор.
			c.log.Warn("allure: проект пропущен", "project", p.Name, "err", err)
			continue
		}
		events = append(events, evs...)
	}
	return collectors.Result{
		Events: events,
		Note:   fmt.Sprintf("обойдено проектов: %d", len(projects)),
	}, nil
}

// ensureAuth проверяет доступ и при 401 обменивает Api-Token на JWT (старые
// версии TestOps не принимают токен напрямую).
func (c *Collector) ensureAuth(ctx context.Context) error {
	var probe page[project]
	_, err := c.cl.GetJSON(ctx, "/api/rs/project", url.Values{"page": {"0"}, "size": {"1"}}, &probe)
	if err == nil {
		return nil
	}
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		return fmt.Errorf("allure: проверка доступа: %w", err)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := c.cl.PostForm(ctx, "/api/uaa/oauth/token", url.Values{
		"grant_type": {"apitoken"},
		"scope":      {"openid"},
		"token":      {c.cfg.Token},
	}, &tok); err != nil {
		return fmt.Errorf("allure: обмен API-токена на JWT не удался (проверьте ALLURE_TOKEN): %w", err)
	}
	if tok.AccessToken == "" {
		return errors.New("allure: /api/uaa/oauth/token не вернул access_token")
	}
	cl, err := c.newClient("Bearer " + tok.AccessToken)
	if err != nil {
		return err
	}
	c.cl = cl
	return nil
}

// fetchProjects возвращает проекты, отфильтрованные по cfg.Projects (id или имя).
func (c *Collector) fetchProjects(ctx context.Context) ([]project, error) {
	var out []project
	for pageNum := 0; pageNum < defaultMaxPages; pageNum++ {
		var res page[project]
		q := url.Values{"page": {fmt.Sprint(pageNum)}, "size": {"100"}}
		if _, err := c.cl.GetJSON(ctx, "/api/rs/project", q, &res); err != nil {
			return nil, fmt.Errorf("allure: список проектов: %w", err)
		}
		out = append(out, res.Content...)
		if res.Last || len(res.Content) == 0 {
			break
		}
	}
	if len(c.cfg.Projects) == 0 {
		return out, nil
	}
	want := map[string]bool{}
	for _, p := range c.cfg.Projects {
		want[strings.ToLower(strings.TrimSpace(p))] = true
	}
	var filtered []project
	for _, p := range out {
		if want[strings.ToLower(p.Name)] || want[fmt.Sprint(p.ID)] {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

// collectProject собирает запуски, тест-кейсы и дефекты одного проекта.
func (c *Collector) collectProject(ctx context.Context, req collectors.Request, p project, ids map[string]bool) ([]models.Event, error) {
	var events []models.Event
	projKey := strings.ToLower(p.Name)

	base := func(typ models.EventType, extID, title, urlPath, refID string, at time.Time, actor string) models.Event {
		return models.Event{
			PersonKey:   req.Person.Key,
			Source:      models.SourceAllure,
			Type:        typ,
			ExternalID:  extID,
			OccurredAt:  at,
			Title:       title,
			URL:         c.cfg.BaseURL + urlPath,
			Project:     projKey,
			ProjectName: p.Name,
			RefID:       refID,
			// actor — сырой createdBy/lastModifiedBy из TestOps: по нему
			// атрибуцию можно проверить постфактум (CI-токены и т.п.).
			Meta: map[string]any{"allure_project_id": p.ID, "actor": actor},
		}
	}

	// Запуски тестов.
	err := paginate(ctx, c, "/api/rs/launch", p.ID, "createdDate,DESC", func(l launch) (bool, error) {
		at := time.UnixMilli(l.CreatedDate).UTC()
		if at.Before(req.From) {
			return false, nil // дальше только старее — останавливаем пагинацию
		}
		if !at.Before(req.To) || !ids[strings.ToLower(l.CreatedBy)] {
			return true, nil
		}
		events = append(events, base(models.TypeAllureLaunch,
			fmt.Sprintf("launch:%d", l.ID), l.Name,
			fmt.Sprintf("/launch/%d", l.ID),
			fmt.Sprintf("allure-launch-%d", l.ID), at, l.CreatedBy))
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("запуски: %w", err)
	}

	// Тест-кейсы: листаем по дате изменения, создание и правка — разные события.
	err = paginate(ctx, c, "/api/rs/testcase", p.ID, "lastModifiedDate,DESC", func(tc testCase) (bool, error) {
		modified := time.UnixMilli(tc.LastModifiedDate).UTC()
		if modified.Before(req.From) {
			return false, nil
		}
		created := time.UnixMilli(tc.CreatedDate).UTC()
		ref := fmt.Sprintf("allure-tc-%d", tc.ID)
		urlPath := fmt.Sprintf("/project/%d/test-cases/%d", p.ID, tc.ID)
		if !created.Before(req.From) && created.Before(req.To) && ids[strings.ToLower(tc.CreatedBy)] {
			events = append(events, base(models.TypeAllureCaseCreated,
				fmt.Sprintf("tc:%d:created", tc.ID), tc.Name, urlPath, ref, created, tc.CreatedBy))
		}
		if tc.LastModifiedDate != tc.CreatedDate && modified.Before(req.To) &&
			ids[strings.ToLower(tc.LastModifiedBy)] {
			events = append(events, base(models.TypeAllureCaseUpdated,
				fmt.Sprintf("tc:%d:upd:%d", tc.ID, tc.LastModifiedDate), tc.Name, urlPath, ref, modified, tc.LastModifiedBy))
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("тест-кейсы: %w", err)
	}

	// Дефекты.
	err = paginate(ctx, c, "/api/rs/defect", p.ID, "createdDate,DESC", func(d defect) (bool, error) {
		at := time.UnixMilli(d.CreatedDate).UTC()
		if at.Before(req.From) {
			return false, nil
		}
		if !at.Before(req.To) || !ids[strings.ToLower(d.CreatedBy)] {
			return true, nil
		}
		events = append(events, base(models.TypeAllureDefect,
			fmt.Sprintf("defect:%d", d.ID), d.Name,
			fmt.Sprintf("/project/%d/defects/%d", p.ID, d.ID),
			fmt.Sprintf("allure-defect-%d", d.ID), at, d.CreatedBy))
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("дефекты: %w", err)
	}

	return events, nil
}

// paginate листает pageable-эндпоинт TestOps по убыванию даты; visit
// возвращает false, когда записи стали старее нижней границы периода.
func paginate[T any](ctx context.Context, c *Collector, path string, projectID int64, sort string, visit func(T) (bool, error)) error {
	pageSize := c.cfg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxPages := c.cfg.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	for pageNum := 0; pageNum < maxPages; pageNum++ {
		var res page[T]
		q := url.Values{
			"projectId": {fmt.Sprint(projectID)},
			"page":      {fmt.Sprint(pageNum)},
			"size":      {fmt.Sprint(pageSize)},
			"sort":      {sort},
		}
		if _, err := c.cl.GetJSON(ctx, path, q, &res); err != nil {
			return err
		}
		for _, item := range res.Content {
			cont, err := visit(item)
			if err != nil {
				return err
			}
			if !cont {
				return nil
			}
		}
		if res.Last || len(res.Content) == 0 {
			return nil
		}
	}
	return nil
}

// identities — идентификаторы человека для сопоставления с createdBy:
// e-mail, google e-mail, их локальные части и логин GitLab (под SSO совпадают).
func identities(p models.Person) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out[s] = true
			if i := strings.Index(s, "@"); i > 0 {
				out[s[:i]] = true
			}
		}
	}
	add(p.Email)
	add(p.GoogleEmail)
	add(p.GitLabUser)
	return out
}
