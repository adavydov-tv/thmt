// Package gitlab реализует collectors.Collector для self-hosted GitLab.
//
// Используется REST API v4 с авторизацией через заголовок PRIVATE-TOKEN
// (Personal Access Token). Внешних зависимостей нет — только stdlib и
// internal/*. Собираются три пласта данных:
//
//   - /users/:id/events — лента действий пользователя (push, MR, issue, заметки);
//   - /projects/:id/repository/commits — отдельные коммиты по проектам, где были
//     push-события (лента событий отдаёт только агрегированные push'и);
//   - /merge_requests?scope=all&author_id=... — обогащение MR точными датами
//     создания/мержа и метаданными (ветки, changes_count, upvotes).
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// maxPages — жёсткий предел числа страниц в одном пагинированном обходе.
// Защита от бесконечного цикла, если сервер отдаёт некорректный X-Next-Page.
const maxPages = 200

// Этапы, о которых коллектор сообщает через collectors.Progress.
const (
	stageEvents  = "events"
	stageCommits = "commits"
	stageMRs     = "merge_requests"
)

// Collector собирает активность одного пользователя из GitLab.
type Collector struct {
	cfg  config.GitLabConfig
	http *httpx.Client
	log  *slog.Logger

	mu       sync.Mutex
	progress collectors.Progress
	// projectPaths — кэш «project_id → path_with_namespace», чтобы не дёргать
	// GET /projects/:id повторно. Пустая строка означает «резолв не удался».
	projectPaths map[int]string
}

// New создаёт коллектор GitLab. Возвращает ошибку, если не заданы базовый URL
// или токен доступа.
func New(cfg config.GitLabConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, errors.New("gitlab: не задан BaseURL")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("gitlab: не задан токен доступа (PRIVATE-TOKEN)")
	}
	cli, err := httpx.New(httpx.Options{
		BaseURL:       base + "/api/v4",
		Timeout:       timeout,
		MaxRetries:    maxRetries,
		SkipTLSVerify: cfg.SkipTLSVerify,
		Headers:       map[string]string{"PRIVATE-TOKEN": cfg.Token},
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	cfg.BaseURL = base
	return &Collector{
		cfg:          cfg,
		http:         cli,
		log:          log.With("collector", "gitlab"),
		projectPaths: make(map[int]string),
	}, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceGitLab }

// SetProgress устанавливает колбэк прогресса (интерфейс collectors.ProgressAware).
func (c *Collector) SetProgress(p collectors.Progress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.progress = p
}

func (c *Collector) report(stage string, done, total int) {
	c.mu.Lock()
	p := c.progress
	c.mu.Unlock()
	if p != nil {
		p(stage, done, total)
	}
}

// perPage возвращает размер страницы в допустимых для GitLab пределах.
func (c *Collector) perPage() string {
	n := c.cfg.PageSize
	if n <= 0 || n > 100 {
		n = 100
	}
	return strconv.Itoa(n)
}

// maxProjects — сколько проектов максимум обходить при доборе коммитов.
func (c *Collector) maxProjects() int {
	if c.cfg.MaxProjects <= 0 {
		return 50
	}
	return c.cfg.MaxProjects
}

// ---- Модели ответов GitLab ----

type glUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	State    string `json:"state"`
}

type glPushData struct {
	CommitCount int    `json:"commit_count"`
	Action      string `json:"action"`
	RefType     string `json:"ref_type"`
	Ref         string `json:"ref"`
	CommitTitle string `json:"commit_title"`
	CommitFrom  string `json:"commit_from"`
	CommitTo    string `json:"commit_to"`
}

type glNote struct {
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	NoteableID   int64  `json:"noteable_id"`
	NoteableIID  int64  `json:"noteable_iid"`
	NoteableType string `json:"noteable_type"`
}

type glEvent struct {
	ID          int64       `json:"id"`
	ProjectID   int         `json:"project_id"`
	ActionName  string      `json:"action_name"`
	TargetID    int64       `json:"target_id"`
	TargetIID   int64       `json:"target_iid"`
	TargetType  string      `json:"target_type"`
	TargetTitle string      `json:"target_title"`
	Title       string      `json:"title"`
	CreatedAt   time.Time   `json:"created_at"`
	Author      glUser      `json:"author"`
	PushData    *glPushData `json:"push_data"`
	Note        *glNote     `json:"note"`
}

type glCommitStats struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Total     int `json:"total"`
}

type glCommit struct {
	ID             string         `json:"id"`
	ShortID        string         `json:"short_id"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	AuthorName     string         `json:"author_name"`
	AuthorEmail    string         `json:"author_email"`
	CommitterName  string         `json:"committer_name"`
	CommitterEmail string         `json:"committer_email"`
	CreatedAt      time.Time      `json:"created_at"`
	AuthoredDate   time.Time      `json:"authored_date"`
	CommittedDate  time.Time      `json:"committed_date"`
	WebURL         string         `json:"web_url"`
	Stats          *glCommitStats `json:"stats"`
}

type glMergeRequest struct {
	ID           int64      `json:"id"`
	IID          int64      `json:"iid"`
	ProjectID    int        `json:"project_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	MergedAt     *time.Time `json:"merged_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	WebURL       string     `json:"web_url"`
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	Upvotes      int        `json:"upvotes"`
	// ChangesCount в API может прийти и строкой ("12+"), и числом — читаем сырым.
	ChangesCount json.RawMessage `json:"changes_count"`
	Author       glUser          `json:"author"`
	// Кто фактически мержил/закрывал: merge_user — актуальное поле,
	// merged_by — его устаревший синоним в старых GitLab.
	MergeUser *glUser `json:"merge_user"`
	MergedBy  *glUser `json:"merged_by"`
	ClosedBy  *glUser `json:"closed_by"`
}

type glProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

// ---- Сбор ----

// Collect выгружает активность пользователя за период [req.From, req.To].
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result

	user, err := c.resolveUser(ctx, req.Person)
	if err != nil {
		return res, err
	}
	c.log.Info("пользователь GitLab найден", "id", user.ID, "username", user.Username, "person", req.Person.Key)

	from := req.From.UTC()
	to := req.To.UTC()

	// seen защищает от дублей по ExternalID, seenMR — логическая дедупликация
	// MR-действий между лентой событий и глобальным эндпоинтом /merge_requests.
	seen := make(map[string]bool)
	seenMR := make(map[string]bool)
	var notes []string

	// 1. Лента событий — основной источник.
	events, pushProjects, skipped, err := c.collectEvents(ctx, user.ID, req, from, to, seen, seenMR)
	if err != nil {
		return res, err
	}
	res.Events = append(res.Events, events...)
	if s := formatSkipped(skipped); s != "" {
		notes = append(notes, s)
	}

	// 2. Отдельные коммиты по проектам, где были push-события.
	commits, note, err := c.collectCommits(ctx, req, from, to, pushProjects, seen)
	if err != nil {
		return res, err
	}
	res.Events = append(res.Events, commits...)
	if note != "" {
		notes = append(notes, note)
	}

	// 3. Обогащение по MR, где пользователь автор.
	mrs, err := c.collectMergeRequests(ctx, user.ID, req, from, to, seen, seenMR)
	if err != nil {
		return res, err
	}
	res.Events = append(res.Events, mrs...)

	// Дозаполняем имена проектов и нормализуем события.
	for i := range res.Events {
		e := &res.Events[i]
		if pid, err := strconv.Atoi(e.Project); err == nil && e.ProjectName == "" {
			e.ProjectName = c.projectPath(ctx, pid)
		}
		e.Normalize()
	}
	sort.Slice(res.Events, func(i, j int) bool {
		return res.Events[i].OccurredAt.Before(res.Events[j].OccurredAt)
	})

	res.Note = strings.Join(notes, "; ")
	return res, nil
}

// resolveUser находит числовой ID пользователя: сначала по username, затем по email.
func (c *Collector) resolveUser(ctx context.Context, p models.Person) (*glUser, error) {
	if u := strings.TrimSpace(p.GitLabUser); u != "" {
		var out []glUser
		q := url.Values{"username": {u}}
		if _, err := c.http.GetJSON(ctx, "/users", q, &out); err != nil {
			return nil, fmt.Errorf("gitlab: поиск пользователя по username %q: %w", u, err)
		}
		if len(out) > 0 {
			return &out[0], nil
		}
		c.log.Warn("пользователь GitLab по username не найден, пробуем email", "username", u)
	}

	if e := strings.TrimSpace(p.Email); e != "" {
		var out []glUser
		q := url.Values{"search": {e}}
		if _, err := c.http.GetJSON(ctx, "/users", q, &out); err != nil {
			return nil, fmt.Errorf("gitlab: поиск пользователя по email %q: %w", e, err)
		}
		// Точное совпадение по email приоритетнее первого результата поиска.
		for i := range out {
			if strings.EqualFold(strings.TrimSpace(out[i].Email), e) {
				return &out[i], nil
			}
		}
		if len(out) > 0 {
			return &out[0], nil
		}
	}

	return nil, fmt.Errorf(
		"gitlab: пользователь не найден ни по username %q, ни по email %q (person=%s); "+
			"проверьте gitlab_username в конфигурации людей",
		p.GitLabUser, p.Email, p.Key)
}

// collectEvents выгружает /users/:id/events и мапит их в модель.
//
// Возвращает также множество project_id, где были push-события (для добора
// коммитов), и статистику пропущенных action_name.
func (c *Collector) collectEvents(
	ctx context.Context,
	userID int,
	req collectors.Request,
	from, to time.Time,
	seen map[string]bool,
	seenMR map[string]bool,
) ([]models.Event, []int, map[string]int, error) {
	// after/before в GitLab эксклюзивные и работают по датам, поэтому берём
	// границы с запасом в сутки, а точную отсечку делаем по created_at ниже.
	q := url.Values{
		"after":    {from.AddDate(0, 0, -1).Format("2006-01-02")},
		"before":   {to.AddDate(0, 0, 1).Format("2006-01-02")},
		"per_page": {c.perPage()},
	}

	var (
		out       []models.Event
		skipped   = map[string]int{}
		pushSet   = map[int]bool{}
		pushOrder []int
	)

	err := paginate(ctx, c.http, c.log, fmt.Sprintf("/users/%d/events", userID), q,
		func(batch []glEvent) error {
			for i := range batch {
				ev := &batch[i]
				at := ev.CreatedAt.UTC()
				if at.Before(from) || at.After(to) {
					continue
				}
				e, ok := c.mapEvent(ctx, ev, req, at)
				if !ok {
					key := strings.TrimSpace(ev.ActionName)
					if key == "" {
						key = "(пусто)"
					}
					if ev.TargetType != "" {
						key += "/" + ev.TargetType
					}
					skipped[key]++
					continue
				}
				if seen[e.ExternalID] {
					continue
				}
				seen[e.ExternalID] = true
				if k := mrDedupKey(ev.ProjectID, ev.TargetIID, e.Type); k != "" {
					seenMR[k] = true
				}
				if ev.PushData != nil && ev.ProjectID > 0 && !pushSet[ev.ProjectID] {
					pushSet[ev.ProjectID] = true
					pushOrder = append(pushOrder, ev.ProjectID)
				}
				out = append(out, e)
			}
			c.report(stageEvents, len(out), 0)
			return nil
		})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gitlab: лента событий пользователя %d: %w", userID, err)
	}
	c.report(stageEvents, len(out), len(out))
	return out, pushOrder, skipped, nil
}

// mapEvent превращает событие GitLab в models.Event.
// Второй результат false — событие неинтересно и должно быть пропущено.
func (c *Collector) mapEvent(ctx context.Context, ev *glEvent, req collectors.Request, at time.Time) (models.Event, bool) {
	action := strings.ToLower(strings.TrimSpace(ev.ActionName))
	target := ev.TargetType

	e := models.Event{
		PersonKey:  req.Person.Key,
		Source:     models.SourceGitLab,
		ExternalID: fmt.Sprintf("event:%d", ev.ID),
		OccurredAt: at,
		Meta:       map[string]any{"action_name": ev.ActionName},
	}
	if ev.ProjectID > 0 {
		e.Project = strconv.Itoa(ev.ProjectID)
	}

	switch {
	// Push: "pushed to", "pushed new" (а также ветки/теги).
	case strings.HasPrefix(action, "pushed") && ev.PushData != nil:
		pd := ev.PushData
		e.Type = models.TypePush
		e.Effort = float64(pd.CommitCount)
		e.EffortUnit = "" // это количество коммитов, а не строк
		e.RefID = pd.Ref
		e.Meta["commit_count"] = pd.CommitCount
		e.Meta["ref"] = pd.Ref
		e.Meta["ref_type"] = pd.RefType
		if pd.CommitTitle != "" {
			e.Meta["commit_title"] = pd.CommitTitle
		}
		title := fmt.Sprintf("Push %d коммит(ов) в %s", pd.CommitCount, pd.Ref)
		if pd.CommitTitle != "" {
			title += ": " + pd.CommitTitle
		}
		e.Title = title
		if p := c.projectPath(ctx, ev.ProjectID); p != "" && pd.Ref != "" {
			e.ProjectName = p
			e.URL = fmt.Sprintf("%s/%s/-/commits/%s", c.cfg.BaseURL, p, pd.Ref)
		}

	case target == "MergeRequest":
		switch action {
		case "opened", "created":
			e.Type = models.TypeMROpened
		case "merged", "accepted":
			e.Type = models.TypeMRMerged
		case "closed":
			e.Type = models.TypeMRClosed
		case "approved":
			e.Type = models.TypeMRApproved
		default:
			return e, false
		}
		e.RefID = fmt.Sprintf("!%d", ev.TargetIID)
		e.Title = strings.TrimSpace(e.RefID + " " + ev.TargetTitle)
		e.Meta["iid"] = ev.TargetIID
		if p := c.projectPath(ctx, ev.ProjectID); p != "" {
			e.ProjectName = p
			e.URL = fmt.Sprintf("%s/%s/-/merge_requests/%d", c.cfg.BaseURL, p, ev.TargetIID)
		}

	case target == "Issue" || target == "WorkItem":
		e.Type = models.TypeGitLabIssue
		e.RefID = fmt.Sprintf("#%d", ev.TargetIID)
		e.Title = strings.TrimSpace(e.RefID + " " + ev.TargetTitle)
		e.Meta["iid"] = ev.TargetIID
		if p := c.projectPath(ctx, ev.ProjectID); p != "" {
			e.ProjectName = p
			e.URL = fmt.Sprintf("%s/%s/-/issues/%d", c.cfg.BaseURL, p, ev.TargetIID)
		}

	case target == "Note" || target == "DiffNote" || target == "DiscussionNote":
		n := ev.Note
		if n == nil {
			return e, false
		}
		e.Body = n.Body
		e.Meta["noteable_type"] = n.NoteableType
		e.Meta["note_id"] = n.ID
		var path, kind string
		if p := c.projectPath(ctx, ev.ProjectID); p != "" {
			e.ProjectName = p
			path = p
		}
		if n.NoteableType == "MergeRequest" {
			e.Type = models.TypeReviewComment
			kind = "merge_requests"
			if n.NoteableIID > 0 {
				e.RefID = fmt.Sprintf("!%d", n.NoteableIID)
			}
		} else {
			e.Type = models.TypeGitLabNote
			kind = "issues"
			if n.NoteableIID > 0 {
				e.RefID = fmt.Sprintf("#%d", n.NoteableIID)
			}
		}
		if path != "" && n.NoteableIID > 0 && n.NoteableType != "Commit" && n.NoteableType != "Snippet" {
			e.URL = fmt.Sprintf("%s/%s/-/%s/%d#note_%d", c.cfg.BaseURL, path, kind, n.NoteableIID, n.ID)
		}
		title := strings.TrimSpace(e.RefID + " " + ev.TargetTitle)
		if title == "" {
			title = firstLine(n.Body)
		}
		e.Title = "Комментарий " + title

	default:
		return e, false
	}

	return e, true
}

// collectCommits добирает отдельные коммиты по проектам, где были push-события.
// Недоступные репозитории (403/404) пропускаются с записью в лог.
func (c *Collector) collectCommits(
	ctx context.Context,
	req collectors.Request,
	from, to time.Time,
	projects []int,
	seen map[string]bool,
) ([]models.Event, string, error) {
	email := strings.ToLower(strings.TrimSpace(req.Person.Email))
	name := strings.TrimSpace(req.Person.DisplayName)
	if email == "" && name == "" {
		return nil, "коммиты пропущены: у человека не заданы ни email, ни display_name", nil
	}

	limit := c.maxProjects()
	truncated := 0
	if len(projects) > limit {
		truncated = len(projects) - limit
		projects = projects[:limit]
	}

	author := email
	if author == "" {
		author = name
	}

	var (
		out     []models.Event
		skipped int
	)
	c.report(stageCommits, 0, len(projects))

	for idx, pid := range projects {
		q := url.Values{
			"author":     {author},
			"since":      {from.Format(time.RFC3339)},
			"until":      {to.Format(time.RFC3339)},
			"with_stats": {"true"},
			"per_page":   {c.perPage()},
		}
		path := c.projectPath(ctx, pid)

		err := paginate(ctx, c.http, c.log, fmt.Sprintf("/projects/%d/repository/commits", pid), q,
			func(batch []glCommit) error {
				for i := range batch {
					cm := &batch[i]
					if !matchesAuthor(cm, email, name) {
						continue
					}
					at := commitTime(cm)
					if at.Before(from) || at.After(to) {
						continue
					}
					sha := shortSHA(cm)
					ext := fmt.Sprintf("commit:%d:%s", pid, sha)
					if seen[ext] {
						continue
					}
					seen[ext] = true

					e := models.Event{
						PersonKey:   req.Person.Key,
						Source:      models.SourceGitLab,
						Type:        models.TypeCommit,
						ExternalID:  ext,
						OccurredAt:  at,
						Title:       strings.TrimSpace(sha + " " + cm.Title),
						Body:        cm.Message,
						URL:         cm.WebURL,
						Project:     strconv.Itoa(pid),
						ProjectName: path,
						RefID:       sha,
						EffortUnit:  "lines",
						Meta: map[string]any{
							"sha":          sha,
							"author_email": cm.AuthorEmail,
							"author_name":  cm.AuthorName,
						},
					}
					if cm.WebURL != "" {
						e.Meta["web_url"] = cm.WebURL
					}
					if cm.Stats != nil {
						e.Effort = float64(cm.Stats.Additions + cm.Stats.Deletions)
						e.Meta["additions"] = cm.Stats.Additions
						e.Meta["deletions"] = cm.Stats.Deletions
						e.Meta["total"] = cm.Stats.Total
					}
					out = append(out, e)
				}
				return nil
			})
		if err != nil {
			var apiErr *httpx.APIError
			if errors.As(err, &apiErr) && apiErr.IsNotFound() {
				skipped++
				c.log.Warn("репозиторий недоступен, пропускаем", "project_id", pid, "status", apiErr.Status)
				c.report(stageCommits, idx+1, len(projects))
				continue
			}
			return nil, "", fmt.Errorf("gitlab: коммиты проекта %d: %w", pid, err)
		}
		c.report(stageCommits, idx+1, len(projects))
	}

	var notes []string
	if truncated > 0 {
		notes = append(notes, fmt.Sprintf("добор коммитов ограничен %d проектами (пропущено %d)", limit, truncated))
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("недоступных репозиториев: %d", skipped))
	}
	return out, strings.Join(notes, ", "), nil
}

// collectMergeRequests обогащает картину точными датами MR, где пользователь автор.
func (c *Collector) collectMergeRequests(
	ctx context.Context,
	userID int,
	req collectors.Request,
	from, to time.Time,
	seen map[string]bool,
	seenMR map[string]bool,
) ([]models.Event, error) {
	q := url.Values{
		"scope":          {"all"},
		"author_id":      {strconv.Itoa(userID)},
		"updated_after":  {from.Format(time.RFC3339)},
		"updated_before": {to.Format(time.RFC3339)},
		"per_page":       {c.perPage()},
	}

	var out []models.Event
	err := paginate(ctx, c.http, c.log, "/merge_requests", q, func(batch []glMergeRequest) error {
		for i := range batch {
			mr := &batch[i]
			// Каждое состояние MR даёт отдельное событие в свою дату.
			add := func(t models.EventType, at time.Time, state string) {
				at = at.UTC()
				if at.IsZero() || at.Before(from) || at.After(to) {
					return
				}
				if k := mrDedupKey(mr.ProjectID, mr.IID, t); k != "" && seenMR[k] {
					return
				}
				ext := fmt.Sprintf("mr:%d:%d:%s", mr.ProjectID, mr.IID, state)
				if seen[ext] {
					return
				}
				seen[ext] = true
				if k := mrDedupKey(mr.ProjectID, mr.IID, t); k != "" {
					seenMR[k] = true
				}
				out = append(out, c.mrEvent(mr, t, ext, at, req))
			}

			add(models.TypeMROpened, mr.CreatedAt, "opened")
			// Мерж/закрытие — действие того, кто его выполнил, а не автора MR.
			// Этот путь идёт по author_id, поэтому засчитываем мерж только если
			// мержил сам автор; чужой мерж (ревьюер, merge train) — не его действие
			// (свой мерж чужого MR приходит из ленты /users/{id}/events).
			if mr.MergedAt != nil && actedBy(mr.MergeUser, mr.MergedBy, userID) {
				add(models.TypeMRMerged, *mr.MergedAt, "merged")
			}
			if mr.ClosedAt != nil && mr.MergedAt == nil && actedBy(mr.ClosedBy, nil, userID) {
				add(models.TypeMRClosed, *mr.ClosedAt, "closed")
			}
		}
		c.report(stageMRs, len(out), 0)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab: merge requests автора %d: %w", userID, err)
	}
	c.report(stageMRs, len(out), len(out))
	return out, nil
}

// actedBy сообщает, что действие над MR выполнил пользователь userID.
// Поле может отсутствовать в ответе (старый GitLab) — тогда доказательства
// нет и действие не засчитывается (fail-closed).
func actedBy(u, alt *glUser, userID int) bool {
	if u != nil && u.ID == userID {
		return true
	}
	return alt != nil && alt.ID == userID
}

// mrEvent собирает событие по merge request.
func (c *Collector) mrEvent(mr *glMergeRequest, t models.EventType, ext string, at time.Time, req collectors.Request) models.Event {
	ref := fmt.Sprintf("!%d", mr.IID)
	e := models.Event{
		PersonKey:  req.Person.Key,
		Source:     models.SourceGitLab,
		Type:       t,
		ExternalID: ext,
		OccurredAt: at,
		Title:      strings.TrimSpace(ref + " " + mr.Title),
		Body:       mr.Description,
		URL:        mr.WebURL,
		Project:    strconv.Itoa(mr.ProjectID),
		RefID:      ref,
		Meta: map[string]any{
			"state":         mr.State,
			"iid":           mr.IID,
			"source_branch": mr.SourceBranch,
			"target_branch": mr.TargetBranch,
			"upvotes":       mr.Upvotes,
			"project_id":    mr.ProjectID,
		},
	}
	if mr.WebURL != "" {
		e.Meta["web_url"] = mr.WebURL
	}
	if v := rawScalar(mr.ChangesCount); v != "" {
		e.Meta["changes_count"] = v
	}
	// path_with_namespace достаём из web_url, чтобы не тратить лишний запрос.
	if p := projectPathFromURL(c.cfg.BaseURL, mr.WebURL); p != "" {
		e.ProjectName = p
		c.rememberProjectPath(mr.ProjectID, p)
	}
	return e
}

// ---- Вспомогательное ----

// paginate обходит все страницы коллекции GitLab, следуя заголовку X-Next-Page.
// Число страниц ограничено maxPages — защита от бесконечного цикла.
func paginate[T any](
	ctx context.Context,
	cli *httpx.Client,
	log *slog.Logger,
	path string,
	q url.Values,
	onPage func(items []T) error,
) error {
	page := "1"
	for i := 0; i < maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := url.Values{}
		for k, v := range q {
			query[k] = v
		}
		query.Set("page", page)

		var batch []T
		hdr, err := cli.GetJSON(ctx, path, query, &batch)
		if err != nil {
			return err
		}
		if len(batch) > 0 {
			if err := onPage(batch); err != nil {
				return err
			}
		}
		next := ""
		if hdr != nil {
			next = strings.TrimSpace(hdr.Get("X-Next-Page"))
		}
		// Пустой X-Next-Page — страниц больше нет; повтор номера страницы
		// означает некорректный ответ сервера, тоже останавливаемся.
		if next == "" || next == "0" || next == page || len(batch) == 0 {
			return nil
		}
		page = next
	}
	log.Warn("достигнут лимит страниц пагинации", "path", path, "max_pages", maxPages)
	return nil
}

// projectPath лениво резолвит path_with_namespace по project_id с кэшированием.
// При ошибке возвращает пустую строку и запоминает неудачу, чтобы не повторять запрос.
func (c *Collector) projectPath(ctx context.Context, pid int) string {
	if pid <= 0 {
		return ""
	}
	c.mu.Lock()
	p, ok := c.projectPaths[pid]
	c.mu.Unlock()
	if ok {
		return p
	}

	var pr glProject
	if _, err := c.http.GetJSON(ctx, fmt.Sprintf("/projects/%d", pid), nil, &pr); err != nil {
		c.log.Warn("не удалось получить проект", "project_id", pid, "err", err)
		c.rememberProjectPath(pid, "")
		return ""
	}
	c.rememberProjectPath(pid, pr.PathWithNamespace)
	return pr.PathWithNamespace
}

func (c *Collector) rememberProjectPath(pid int, path string) {
	c.mu.Lock()
	// Непустое значение не затираем пустым.
	if old, ok := c.projectPaths[pid]; !ok || old == "" {
		c.projectPaths[pid] = path
	}
	c.mu.Unlock()
}

// mrDedupKey — логический ключ действия над MR: проект + iid + тип события.
// Нужен, чтобы одно и то же действие не пришло дважды (из ленты и из /merge_requests).
func mrDedupKey(projectID int, iid int64, t models.EventType) string {
	switch t {
	case models.TypeMROpened, models.TypeMRMerged, models.TypeMRClosed:
		if projectID <= 0 || iid <= 0 {
			return ""
		}
		return fmt.Sprintf("%d:%d:%s", projectID, iid, t)
	default:
		return ""
	}
}

// matchesAuthor проверяет, что коммит НАПИСАН этим человеком. Сверяется только
// author_email: совпадение по committer_email намеренно НЕ засчитывается —
// при rebase/cherry-pick/squash чужой ветки committer это тот, кто пере-применял
// коммиты, и чужой код записывался бы человеку. Матч по одному display_name
// допускается лишь когда у человека вообще нет email (иначе тёзки и «Administrator»
// приписывали бы чужие коммиты).
func matchesAuthor(cm *glCommit, email, name string) bool {
	if email != "" {
		return strings.EqualFold(strings.TrimSpace(cm.AuthorEmail), email)
	}
	return name != "" && strings.EqualFold(strings.TrimSpace(cm.AuthorName), name)
}

// commitTime выбирает наиболее осмысленную дату коммита.
func commitTime(cm *glCommit) time.Time {
	switch {
	case !cm.CommittedDate.IsZero():
		return cm.CommittedDate.UTC()
	case !cm.AuthoredDate.IsZero():
		return cm.AuthoredDate.UTC()
	default:
		return cm.CreatedAt.UTC()
	}
}

// shortSHA возвращает короткий хеш коммита.
func shortSHA(cm *glCommit) string {
	if cm.ShortID != "" {
		return cm.ShortID
	}
	if len(cm.ID) > 8 {
		return cm.ID[:8]
	}
	return cm.ID
}

// projectPathFromURL вытаскивает path_with_namespace из web_url merge request'а
// вида https://gitlab.example.com/group/proj/-/merge_requests/42.
func projectPathFromURL(base, webURL string) string {
	if base == "" || webURL == "" || !strings.HasPrefix(webURL, base) {
		return ""
	}
	rest := strings.Trim(strings.TrimPrefix(webURL, base), "/")
	if i := strings.Index(rest, "/-/"); i > 0 {
		return rest[:i]
	}
	return ""
}

// rawScalar приводит сырое JSON-значение к строке (число или строка).
func rawScalar(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	return s
}

// firstLine возвращает первую непустую строку текста (для заголовков заметок).
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// formatSkipped кратко описывает, какие action_name были пропущены.
func formatSkipped(skipped map[string]int) string {
	if len(skipped) == 0 {
		return ""
	}
	keys := make([]string, 0, len(skipped))
	total := 0
	for k, v := range skipped {
		keys = append(keys, k)
		total += v
	}
	sort.Slice(keys, func(i, j int) bool {
		if skipped[keys[i]] != skipped[keys[j]] {
			return skipped[keys[i]] > skipped[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > 5 {
		keys = keys[:5]
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, skipped[k]))
	}
	return fmt.Sprintf("пропущено событий: %d (%s)", total, strings.Join(parts, ", "))
}
