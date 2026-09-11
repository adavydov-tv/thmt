// Package confluence собирает активность в Confluence Cloud: созданные и
// изменённые страницы, комментарии и посты блога.
//
// Живёт в том же Atlassian-тенанте, что и Jira: авторизация — Basic
// (email + API-токен), человек идентифицируется тем же accountId, что и в
// Jira (models.Person.JiraAccount). Поиск — через CQL:
//
//   - creator = <id> AND created в периоде   → создание страниц/постов/комментариев;
//   - contributor = <id> AND lastmodified в периоде → правки страниц; CQL
//     отдаёт страницы, где человек «когда-либо контрибьютор», поэтому автор
//     последней версии дополнительно сверяется с accountId.
package confluence

import (
	"context"
	"encoding/base64"
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
	// maxPages — предохранитель пагинации на один CQL-запрос.
	maxPages = 60
)

// Collector собирает активность одного человека в Confluence.
type Collector struct {
	cfg      config.ConfluenceConfig
	log      *slog.Logger
	cl       *httpx.Client
	progress collectors.Progress
}

// New создаёт коллектор с Basic-авторизацией Atlassian.
func New(cfg config.ConfluenceConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.BaseURL == "" || cfg.Email == "" || cfg.APIToken == "" {
		return nil, errors.New("confluence: не заданы CONFLUENCE_BASE_URL / EMAIL / API_TOKEN")
	}
	cl, err := httpx.New(httpx.Options{
		BaseURL:    cfg.BaseURL,
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers: map[string]string{
			"Authorization": "Basic " + basicAuth(cfg.Email, cfg.APIToken),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("confluence: %w", err)
	}
	return &Collector{cfg: cfg, log: log, cl: cl}, nil
}

func basicAuth(email, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceConfluence }

// SetProgress подключает колбэк прогресса.
func (c *Collector) SetProgress(p collectors.Progress) { c.progress = p }

func (c *Collector) report(stage string, done, total int) {
	if c.progress != nil {
		c.progress(stage, done, total)
	}
}

// ---- модели ответов Confluence ----

type searchPage struct {
	Results []content `json:"results"`
	Size    int       `json:"size"`
	Limit   int       `json:"limit"`
}

type content struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // page | blogpost | comment
	Title  string `json:"title"`
	Status string `json:"status"`
	Space  struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	History struct {
		CreatedDate time.Time `json:"createdDate"`
		CreatedBy   struct {
			AccountID string `json:"accountId"`
		} `json:"createdBy"`
	} `json:"history"`
	Version struct {
		When time.Time `json:"when"`
		By   struct {
			AccountID string `json:"accountId"`
		} `json:"by"`
		Number int `json:"number"`
	} `json:"version"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
	Container struct {
		Title string `json:"title"`
	} `json:"container"`
}

// Collect выгружает активность за период.
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	accountID := sanitizeAccountID(req.Person.JiraAccount)
	if accountID == "" {
		return collectors.Result{}, errors.New("confluence: у пользователя не задан jira_account_id (Atlassian accountId общий)")
	}

	fromCQL := req.From.Format("2006/01/02 15:04")
	toCQL := req.To.Format("2006/01/02 15:04")
	spaceClause := ""
	if len(c.cfg.Spaces) > 0 {
		spaceClause = fmt.Sprintf(` and space in ("%s")`, strings.Join(c.cfg.Spaces, `","`))
	}

	var events []models.Event
	seen := map[string]bool{}

	// 1. Созданное человеком: страницы, посты, комментарии.
	createdCQL := fmt.Sprintf(
		`creator = "%s" and created >= "%s" and created < "%s"%s`,
		accountID, fromCQL, toCQL, spaceClause)
	err := c.search(ctx, createdCQL, func(item content) {
		ev, ok := c.createdEvent(item, req.Person.Key)
		if ok && !seen[ev.ExternalID] {
			seen[ev.ExternalID] = true
			events = append(events, ev)
		}
	})
	if err != nil {
		return collectors.Result{}, fmt.Errorf("confluence: созданный контент: %w", err)
	}
	c.report("создано", len(events), 0)

	// 2. Правки страниц: contributor отдаёт и чужие свежие правки страниц,
	// куда человек когда-то писал, — сверяем автора последней версии.
	editedCQL := fmt.Sprintf(
		`type in (page, blogpost) and contributor = "%s" and lastmodified >= "%s" and lastmodified < "%s"%s`,
		accountID, fromCQL, toCQL, spaceClause)
	edits := 0
	err = c.search(ctx, editedCQL, func(item content) {
		if item.Version.By.AccountID != accountID || item.Version.Number <= 1 {
			return // правил не он, либо это создание — уже учтено выше
		}
		if item.Version.When.Before(req.From) || !item.Version.When.Before(req.To) {
			return
		}
		ev := c.baseEvent(item, req.Person.Key)
		ev.Type = models.TypeConfluencePageEdited
		ev.OccurredAt = item.Version.When.UTC()
		ev.ExternalID = fmt.Sprintf("%s:edit:%d", item.ID, item.Version.Number)
		if !seen[ev.ExternalID] {
			seen[ev.ExternalID] = true
			events = append(events, ev)
			edits++
		}
	})
	if err != nil {
		return collectors.Result{}, fmt.Errorf("confluence: правки страниц: %w", err)
	}
	c.report("правки", edits, 0)

	return collectors.Result{
		Events: events,
		Note:   fmt.Sprintf("создано: %d, правок: %d", len(events)-edits, edits),
	}, nil
}

// createdEvent строит событие «создано» по типу контента.
func (c *Collector) createdEvent(item content, personKey string) (models.Event, bool) {
	ev := c.baseEvent(item, personKey)
	ev.OccurredAt = item.History.CreatedDate.UTC()
	ev.ExternalID = item.ID + ":created"
	switch item.Type {
	case "page":
		ev.Type = models.TypeConfluencePageCreated
	case "blogpost":
		ev.Type = models.TypeConfluenceBlogpost
	case "comment":
		ev.Type = models.TypeConfluenceComment
		if item.Container.Title != "" {
			ev.Title = item.Container.Title
		}
	default:
		return models.Event{}, false
	}
	return ev, true
}

func (c *Collector) baseEvent(item content, personKey string) models.Event {
	title := item.Title
	if title == "" {
		title = "(без названия)"
	}
	return models.Event{
		PersonKey:   personKey,
		Source:      models.SourceConfluence,
		Title:       title,
		URL:         c.cfg.BaseURL + item.Links.WebUI,
		Project:     strings.ToLower(item.Space.Key),
		ProjectName: nonEmpty(item.Space.Name, item.Space.Key),
		RefID:       "confluence-" + item.ID,
		Meta: map[string]any{
			"space":   item.Space.Key,
			"version": item.Version.Number,
		},
	}
}

// search листает результаты CQL-запроса.
func (c *Collector) search(ctx context.Context, cql string, visit func(content)) error {
	limit := c.cfg.PageSize
	if limit <= 0 || limit > 200 {
		limit = defaultPageSize
	}
	for page := 0; page < maxPages; page++ {
		q := url.Values{
			"cql":    {cql},
			"start":  {fmt.Sprint(page * limit)},
			"limit":  {fmt.Sprint(limit)},
			"expand": {"history.createdBy,version,space,container"},
		}
		var res searchPage
		if _, err := c.cl.GetJSON(ctx, "/rest/api/content/search", q, &res); err != nil {
			return err
		}
		for _, item := range res.Results {
			visit(item)
		}
		if res.Size < limit {
			return nil
		}
	}
	return nil
}

// sanitizeAccountID отрезает служебные хвосты вида «?cloudId=…», которые
// встречаются в сохранённых accountId.
func sanitizeAccountID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexAny(id, "?&"); i >= 0 {
		id = id[:i]
	}
	return id
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
