// Package figma собирает org-wide активность в Figma через Activity Logs API
// (GET /v1/activity_logs) — доступно на тарифе Enterprise. Один запрос по
// диапазону времени возвращает действия всех пользователей (правки файлов,
// комментарии, публикации библиотек) с атрибуцией по actor.email, поэтому
// сбор устроен групповым: один обход на всю группу, раскладка по авторам.
//
// Аутентификация — personal access token org-админа со скоупом
// org:activity_log_read (заголовок X-Figma-Token). Токен обычного пользователя
// к этому эндпоинту доступа не имеет.
package figma

import (
	"context"
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

// Collector — клиент Figma Activity Logs (единственный SaaS-инстанс).
type Collector struct {
	cfg    config.FigmaConfig
	log    *slog.Logger
	client *http.Client
}

// New создаёт коллектор Figma.
func New(cfg config.FigmaConfig, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("figma: не задан FIGMA_TOKEN")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.figma.com"
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 50
	}
	return &Collector{cfg: cfg, log: log.With("collector", "figma"),
		client: &http.Client{Timeout: timeout}}, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceFigma }

// activityLog — одна запись Activity Logs.
type activityLog struct {
	ID        string  `json:"id"`
	Timestamp float64 `json:"timestamp"` // unix-секунды (может быть дробным)
	Actor     struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"actor"`
	Action struct {
		Type    string          `json:"type"`
		Details json.RawMessage `json:"details"`
	} `json:"action"`
	Entity struct {
		Type string `json:"type"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"entity"`
}

// activityResp — ответ эндпоинта. Пагинация у Figma отдаётся ссылкой
// pagination.next_page; парсим её при наличии.
type activityResp struct {
	ActivityLogs []activityLog `json:"activity_logs"`
	Pagination   struct {
		NextPage string `json:"next_page"`
	} `json:"pagination"`
}

func (c *Collector) get(ctx context.Context, rawURL string, out *activityResp) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Figma-Token", c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("figma: доступ отклонён (%d) — нужен токен org-админа со скоупом org:activity_log_read (Activity Logs — только Enterprise)", resp.StatusCode)
		case http.StatusTooManyRequests:
			return fmt.Errorf("figma: rate limit (429)")
		}
		return fmt.Errorf("figma: activity_logs → %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// classify превращает action.type Figma в наш тип события. Незначимые действия
// (просмотры, логины, изменения прав) отбрасываются — трекаем созидательную
// активность.
func classify(actionType, entityType string) (models.EventType, bool) {
	a := strings.ToLower(actionType)
	switch {
	case strings.Contains(a, "comment"):
		return models.TypeFigmaComment, true
	// Только publish: голое "library" ловило и потребление библиотек
	// (подключение к файлу, использование ассета) как «публикацию».
	case strings.Contains(a, "publish"):
		return models.TypeFigmaPublish, true
	case strings.Contains(a, "view") || strings.Contains(a, "login") ||
		strings.Contains(a, "logout") || strings.Contains(a, "permission") ||
		strings.Contains(a, "access") || strings.Contains(a, "delete"):
		return "", false
	case strings.Contains(a, "update") || strings.Contains(a, "edit") ||
		strings.Contains(a, "create") || strings.Contains(a, "version") ||
		strings.Contains(a, "move") || strings.Contains(a, "rename"):
		// Созидательное действие строго над файлом/проектом. Пустой entityType
		// НЕ пропускаем: через эту дыру любые административные *_update
		// (org/plugin/настройки) становились «правкой файла».
		if strings.Contains(strings.ToLower(entityType), "file") ||
			strings.Contains(strings.ToLower(entityType), "project") {
			return models.TypeFigmaEdit, true
		}
	}
	return "", false
}

// CollectGroup — один обход Activity Logs для всей группы людей.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	ids := idMap(people)
	out := map[string][]models.Event{}
	if len(ids) == 0 {
		return out, "Figma: группа пуста", nil
	}
	from, to = from.UTC(), to.UTC()

	q := url.Values{}
	q.Set("start_time", strconv.FormatInt(from.Unix(), 10))
	q.Set("end_time", strconv.FormatInt(to.Unix(), 10))
	q.Set("order", "asc")
	q.Set("limit", "1000")
	next := c.cfg.BaseURL + "/v1/activity_logs?" + q.Encode()

	total, pages := 0, 0
	for next != "" && pages < c.cfg.MaxPages {
		var resp activityResp
		if err := c.get(ctx, next, &resp); err != nil {
			if pages == 0 {
				return nil, "", err
			}
			c.log.Warn("figma: пагинация прервана", "err", err, "page", pages)
			break
		}
		pages++
		for _, l := range resp.ActivityLogs {
			pk, ok := ids[strings.ToLower(strings.TrimSpace(l.Actor.Email))]
			if !ok {
				continue
			}
			typ, keep := classify(l.Action.Type, l.Entity.Type)
			if !keep {
				continue
			}
			at := time.Unix(int64(l.Timestamp), 0).UTC()
			if at.Before(from) || !at.Before(to) {
				continue
			}
			name := l.Entity.Name
			if name == "" {
				name = l.Entity.Type
			}
			ev := models.Event{
				PersonKey:   pk,
				Source:      models.SourceFigma,
				Type:        typ,
				ExternalID:  "figma:" + l.ID,
				OccurredAt:  at,
				Title:       figmaTitle(typ, name),
				Project:     name,
				ProjectName: name,
				RefID:       l.Entity.Key,
				Meta: map[string]any{
					"action": l.Action.Type, "entity_type": l.Entity.Type,
					"entity": name, "entity_key": l.Entity.Key,
				},
			}
			if l.Entity.Type == "file" && l.Entity.Key != "" {
				ev.URL = "https://www.figma.com/file/" + l.Entity.Key
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
			total++
		}
		next = resp.Pagination.NextPage
	}
	if pages >= c.cfg.MaxPages && next != "" {
		c.log.Warn("figma: достигнут потолок страниц, часть событий не выгружена", "max_pages", c.cfg.MaxPages)
	}
	return out, fmt.Sprintf("Figma: страниц %d, событий %d", pages, total), nil
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

func figmaTitle(typ models.EventType, name string) string {
	switch typ {
	case models.TypeFigmaComment:
		return "Комментарий в «" + name + "»"
	case models.TypeFigmaPublish:
		return "Публикация библиотеки «" + name + "»"
	default:
		return "Правка «" + name + "»"
	}
}

// idMap строит actor.email → person key для группы.
func idMap(people []models.Person) map[string]string {
	m := map[string]string{}
	for _, p := range people {
		add := func(v string) {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				m[v] = p.Key
			}
		}
		add(p.Email)
		add(p.GoogleEmail)
	}
	return m
}
