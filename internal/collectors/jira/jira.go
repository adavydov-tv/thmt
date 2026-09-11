// Package jira собирает активность пользователя в Jira Cloud через REST API v3.
//
// Логика сбора устроена «от задач»: одним JQL-запросом выбираются задачи, где
// пользователь мог что-то делать в интересующем периоде, а затем из каждой
// задачи вытаскиваются конкретные действия (создание, назначение, переходы
// статусов, правки полей, комментарии, ворклоги). Такой подход дешевле по
// количеству запросов, чем обход всех проектов, и при этом даёт полную картину:
// JQL умеет искать по comment ~ и worklogAuthor, то есть находит и те задачи,
// где человек только комментировал.
//
// Побочная, но важная функция коллектора — поиск ссылок на Google-документы
// (TSD) внутри задач: именно так связываются постановки в Jira и документы,
// активность в которых потом дособирает Google-коллектор.
package jira

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Поля задачи, которых достаточно для построения всех типов событий.
// Просим ровно их, а не *all: ответ на сотню задач с описаниями и комментариями
// и так весит мегабайты.
var issueFieldList = []string{
	"summary", "status", "issuetype", "project", "created", "updated",
	"resolutiondate", "assignee", "reporter", "creator", "priority", "labels",
	"parent", "description", "comment", "worklog",
}

const (
	// Верхний предел страницы в Jira Cloud — 100 задач.
	maxPageSize = 100
	// Предохранитель от бесконечной пагинации при неожиданном ответе API.
	maxPages = 500
)

// Collector реализует collectors.Collector поверх Jira Cloud REST API v3.
type Collector struct {
	cfg config.JiraConfig
	cl  *httpx.Client
	log *slog.Logger

	progress collectors.Progress

	// legacySearch взводится, когда современный /search/jql недоступен
	// (старые Data Center-подобные инсталляции и часть тенантов ещё на нём).
	// Флаг «липкий» на время одного Collect, чтобы не долбиться в 404 на каждой
	// странице пагинации.
	legacySearch bool
}

// Проверки контракта на этапе компиляции.
var (
	_ collectors.Collector     = (*Collector)(nil)
	_ collectors.ProgressAware = (*Collector)(nil)
)

// New создаёт коллектор Jira. Авторизация — Basic auth (email + API token),
// как того требует Jira Cloud: OAuth здесь избыточен, токен выпускается на
// стороне пользователя и живёт в конфиге. Логгер может быть nil.
func New(cfg config.JiraConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Collector, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("jira: не задан BaseURL")
	}
	if cfg.Email == "" || cfg.APIToken == "" {
		return nil, errors.New("jira: нужны email и API-токен для Basic auth")
	}
	if log == nil {
		log = slog.Default()
	}

	cred := base64.StdEncoding.EncodeToString([]byte(cfg.Email + ":" + cfg.APIToken))
	cl, err := httpx.New(httpx.Options{
		BaseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers: map[string]string{
			"Authorization": "Basic " + cred,
			"Content-Type":  "application/json",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jira: %w", err)
	}

	return &Collector{cfg: cfg, cl: cl, log: log.With("source", "jira")}, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceJira }

// SetProgress подключает колбэк прогресса (collectors.ProgressAware).
func (c *Collector) SetProgress(p collectors.Progress) { c.progress = p }

func (c *Collector) report(stage string, done, total int) {
	if c.progress != nil {
		c.progress(stage, done, total)
	}
}

// baseURL — сайт без хвостового слеша, нужен для сборки ссылок вида /browse/KEY.
func (c *Collector) baseURL() string { return strings.TrimRight(c.cfg.BaseURL, "/") }

func (c *Collector) pageSize() int {
	n := c.cfg.PageSize
	if n <= 0 {
		n = maxPageSize
	}
	if n > maxPageSize {
		n = maxPageSize
	}
	return n
}

// ---------------------------------------------------------------------------
// Идентификация пользователя
// ---------------------------------------------------------------------------

// actor — то, по чему мы узнаём «нашего» человека в ответах Jira.
//
// В Jira Cloud единственный надёжный идентификатор — accountId: email в ответах
// API скрыт настройками приватности у большинства тенантов. Email оставлен как
// запасной вариант для инсталляций, где приватность отключена.
type actor struct {
	accountID string
	email     string
}

func actorOf(p models.Person) actor {
	return actor{
		accountID: strings.TrimSpace(p.JiraAccount),
		email:     strings.TrimSpace(p.Email),
	}
}

// matches сравнивает пользователя из ответа API с нашим. Если у нас есть
// accountId и он есть в ответе — сверяем только его. Иначе падаем на email
// без учёта регистра.
func (a actor) matches(u *jiraUser) bool {
	if u == nil {
		return false
	}
	if a.accountID != "" && u.AccountID != "" {
		return a.accountID == u.AccountID
	}
	if a.email != "" && u.EmailAddress != "" {
		return strings.EqualFold(a.email, u.EmailAddress)
	}
	// Server/DC-стиль: accountId отсутствует, зато есть name/key = логин.
	if a.email != "" {
		login, _, _ := strings.Cut(a.email, "@")
		if login != "" && (strings.EqualFold(login, u.Name) || strings.EqualFold(login, u.Key)) {
			return true
		}
	}
	return false
}

// operand — значение, которое подставляется в JQL для поиска задач пользователя.
func (a actor) operand() string {
	if a.accountID != "" {
		return a.accountID
	}
	return a.email
}

// ---------------------------------------------------------------------------
// Collect
// ---------------------------------------------------------------------------

// Collect выгружает активность пользователя за период [req.From, req.To).
//
// Ошибки по отдельным задачам (403/404 — задача в закрытом проекте или удалена)
// не прерывают сбор: они логируются, а результат помечается как частичный через
// Result.Note.
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result

	act := actorOf(req.Person)
	if act.operand() == "" {
		return res, fmt.Errorf("jira: у пользователя %q не задан ни accountId, ни email", req.Person.Key)
	}

	from, to := req.From.UTC(), req.To.UTC()
	if !to.After(from) {
		return res, fmt.Errorf("jira: пустой период %s..%s", from, to)
	}

	jql := c.buildJQL(act, from, to)
	c.log.Debug("jira jql", "person", req.Person.Key, "jql", jql)

	issues, err := c.searchIssues(ctx, jql)
	if err != nil {
		return res, err
	}

	var notes []string
	if c.legacySearch {
		notes = append(notes, "использован устаревший /rest/api/3/search")
	}

	// Дедупликация doc-ссылок: один и тот же документ может быть упомянут и в
	// описании, и в комментариях — в отчёте он должен быть один раз на задачу.
	seenDocs := make(map[string]bool)
	failed := 0

	c.report("issues", 0, len(issues))
	for i := range issues {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		iss := &issues[i]

		events, docs, err := c.processIssue(ctx, req, act, iss, from, to)
		if err != nil {
			// Одна нечитаемая задача не должна ронять весь сбор.
			failed++
			c.log.Warn("jira: задача пропущена", "issue", iss.Key, "err", err)
		}
		res.Events = append(res.Events, events...)
		for _, d := range docs {
			k := d.IssueKey + "|" + d.DocID
			if seenDocs[k] {
				continue
			}
			seenDocs[k] = true
			res.DocLinks = append(res.DocLinks, d)
		}
		c.report("issues", i+1, len(issues))
	}

	if failed > 0 {
		notes = append(notes, fmt.Sprintf("пропущено задач из-за ошибок доступа: %d", failed))
	}
	res.Note = strings.Join(notes, "; ")

	// Хронологический порядок удобнее и для записи в БД, и для отладки.
	sort.SliceStable(res.Events, func(i, j int) bool {
		return res.Events[i].OccurredAt.Before(res.Events[j].OccurredAt)
	})
	return res, nil
}

// buildJQL собирает запрос «задачи, где пользователь как-то фигурировал и
// которые менялись в периоде».
//
// Фильтр по updated, а не по конкретным датам действий: у Jira нет способа
// отобрать «задачи, где человек комментировал именно в этом окне». Точную
// фильтрацию по времени делаем уже локально, при разборе changelog/комментариев.
func (c *Collector) buildJQL(a actor, from, to time.Time) string {
	op := jqlQuote(a.operand())

	// worklogAuthor доступен не во всех тенантах (нужен модуль учёта времени),
	// но Jira просто вернёт 400 на неизвестное поле — а оно стандартное, поэтому
	// оставляем. comment ~ ищет по автору комментария в Cloud.
	participants := []string{
		"assignee = " + op,
		"reporter = " + op,
		"creator = " + op,
		"comment ~ " + op,
		"worklogAuthor = " + op,
	}

	parts := []string{"(" + strings.Join(participants, " OR ") + ")"}

	if len(c.cfg.Projects) > 0 {
		quoted := make([]string, 0, len(c.cfg.Projects))
		for _, p := range c.cfg.Projects {
			if p = strings.TrimSpace(p); p != "" {
				quoted = append(quoted, jqlQuote(p))
			}
		}
		if len(quoted) > 0 {
			parts = append(parts, "project in ("+strings.Join(quoted, ", ")+")")
		}
	}

	// Границы даём в UTC: сервер интерпретирует их в таймзоне пользователя
	// токена, поэтому берём период с запасом в сутки с каждой стороны, а точную
	// отсечку делаем локально по реальным меткам времени событий.
	parts = append(parts,
		fmt.Sprintf("updated >= %s", jqlQuote(from.Add(-24*time.Hour).Format("2006-01-02 15:04"))),
		fmt.Sprintf("updated <= %s", jqlQuote(to.Add(24*time.Hour).Format("2006-01-02 15:04"))),
	)

	return strings.Join(parts, " AND ") + " ORDER BY updated DESC"
}

// jqlQuote оборачивает значение в кавычки, экранируя спецсимволы JQL.
func jqlQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// ---------------------------------------------------------------------------
// Поиск задач: новый /search/jql с nextPageToken и фолбэк на /search
// ---------------------------------------------------------------------------

// searchIssues выгружает все страницы результатов JQL.
//
// Сначала пробуем современный /rest/api/3/search/jql (курсорная пагинация по
// nextPageToken). Если тенант его ещё не отдаёт (404) или, наоборот, эндпоинт
// уже удалён/переехал (410) — переключаемся на устаревший /rest/api/3/search с
// startAt/maxResults.
func (c *Collector) searchIssues(ctx context.Context, jql string) ([]jiraIssue, error) {
	issues, err := c.searchViaJQL(ctx, jql)
	if err == nil {
		return issues, nil
	}
	if !isStatus(err, http.StatusNotFound, http.StatusGone) {
		return nil, err
	}
	c.log.Info("jira: /search/jql недоступен, переключаюсь на устаревший /search", "err", err)
	c.legacySearch = true
	return c.searchLegacy(ctx, jql)
}

// searchViaJQL — курсорная пагинация нового Cloud-эндпоинта.
func (c *Collector) searchViaJQL(ctx context.Context, jql string) ([]jiraIssue, error) {
	var (
		out   []jiraIssue
		token string
	)
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("jql", jql)
		q.Set("maxResults", strconv.Itoa(c.pageSize()))
		q.Set("fields", strings.Join(issueFieldList, ","))
		if c.cfg.FetchChangelog {
			q.Set("expand", "changelog")
		}
		if token != "" {
			q.Set("nextPageToken", token)
		}

		var resp searchResponse
		if _, err := c.cl.GetJSON(ctx, "/rest/api/3/search/jql", q, &resp); err != nil {
			return nil, fmt.Errorf("jira: поиск задач: %w", err)
		}
		out = append(out, resp.Issues...)

		// Признак конца: явный isLast, пустой токен или пустая страница.
		if resp.NextPageToken == "" || len(resp.Issues) == 0 || (resp.IsLast != nil && *resp.IsLast) {
			break
		}
		token = resp.NextPageToken
	}
	return dedupIssues(out), nil
}

// searchLegacy — офсетная пагинация устаревшего эндпоинта.
func (c *Collector) searchLegacy(ctx context.Context, jql string) ([]jiraIssue, error) {
	var (
		out     []jiraIssue
		startAt int
	)
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("jql", jql)
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(c.pageSize()))
		q.Set("fields", strings.Join(issueFieldList, ","))
		if c.cfg.FetchChangelog {
			q.Set("expand", "changelog")
		}

		var resp searchResponse
		if _, err := c.cl.GetJSON(ctx, "/rest/api/3/search", q, &resp); err != nil {
			return nil, fmt.Errorf("jira: поиск задач (legacy): %w", err)
		}
		out = append(out, resp.Issues...)

		startAt += len(resp.Issues)
		if len(resp.Issues) == 0 || startAt >= resp.Total {
			break
		}
	}
	return dedupIssues(out), nil
}

// dedupIssues страхует от повторов: при офсетной пагинации задача может попасть
// на две страницы, если её обновили прямо во время обхода.
func dedupIssues(in []jiraIssue) []jiraIssue {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, iss := range in {
		if iss.Key == "" || seen[iss.Key] {
			continue
		}
		seen[iss.Key] = true
		out = append(out, iss)
	}
	return out
}

// ---------------------------------------------------------------------------
// Разбор одной задачи
// ---------------------------------------------------------------------------

// processIssue превращает задачу в события пользователя и найденные doc-ссылки.
//
// Возвращаемая ошибка не фатальна: часть данных (например, дочитывание истории)
// может быть недоступна, при этом уже собранные события остаются валидными.
func (c *Collector) processIssue(
	ctx context.Context,
	req collectors.Request,
	act actor,
	iss *jiraIssue,
	from, to time.Time,
) ([]models.Event, []models.DocLink, error) {
	f := iss.Fields
	var events []models.Event
	var softErr error

	isAssignee := act.matches(f.Assignee)
	isReporter := act.matches(f.Reporter)
	isCreator := act.matches(f.Creator)

	// --- история изменений ---
	histories := iss.changelogHistories()
	if c.cfg.FetchChangelog && iss.changelogTruncated() {
		c.report("changelog", 0, 1)
		full, err := c.fetchChangelog(ctx, iss.Key)
		if err != nil {
			softErr = err
			c.log.Warn("jira: не удалось дочитать changelog", "issue", iss.Key, "err", err)
		} else {
			histories = full
		}
		c.report("changelog", 1, 1)
	}

	// --- комментарии ---
	comments := f.commentList()
	if f.commentsTruncated() {
		c.report("comments", 0, 1)
		full, err := c.fetchComments(ctx, iss.Key)
		if err != nil {
			softErr = err
			c.log.Warn("jira: не удалось дочитать комментарии", "issue", iss.Key, "err", err)
		} else {
			comments = full
		}
		c.report("comments", 1, 1)
	}

	// --- ворклоги ---
	var worklogs []jiraWorklog
	if c.cfg.FetchWorklogs {
		worklogs = f.worklogList()
		if f.worklogsTruncated() {
			full, err := c.fetchWorklogs(ctx, iss.Key)
			if err != nil {
				softErr = err
				c.log.Warn("jira: не удалось дочитать ворклоги", "issue", iss.Key, "err", err)
			} else {
				worklogs = full
			}
		}
	}

	// 1. Создание задачи.
	if (isCreator || isReporter) && inPeriod(f.Created.Time, from, to) {
		ev := c.newEvent(req, iss, models.TypeIssueCreated,
			"issue_created:"+iss.Key, f.Created.Time)
		ev.Title = iss.Key + ": " + f.Summary
		ev.Body = adfToText(f.Description)
		ev.Meta["as_creator"] = isCreator
		ev.Meta["as_reporter"] = isReporter
		events = append(events, finish(ev))
	}

	// 2. Назначение на пользователя. Действием человека считается ТОЛЬКО
	// самоназначение: автор changelog-записи о смене assignee должен совпадать
	// с самим человеком. Назначение тимлидом/автораспределением — действие НАД
	// человеком, событием не становится. Событие датируется временем назначения
	// (а не updated задачи — иначе оно всплывало бы в каждом периоде заново).
	// Без changelog самоназначение недоказуемо — событие не создаётся.
	if isAssignee {
		if assignedAt, ok := lastSelfAssignment(histories, act); ok && inPeriod(assignedAt, from, to) {
			ev := c.newEvent(req, iss, models.TypeIssueAssigned,
				"issue_assigned:"+iss.Key, assignedAt)
			ev.Title = iss.Key + ": " + f.Summary
			ev.Meta["inferred"] = false
			ev.Meta["self_assigned"] = true
			ev.Meta["assigned_at"] = assignedAt.UTC().Format(time.RFC3339)
			events = append(events, finish(ev))
		}
		// Иначе: changelog включён, но назначения на нас в нём нет — значит,
		// задачу выдали до начала периода, и события быть не должно.
	}

	// 3. Решение задачи. Автора закрытия достоверно знает только changelog;
	// без него считаем закрывшим исполнителя.
	if inPeriod(f.ResolutionDate.Time, from, to) {
		resolvedByUs := false
		var byStatus string
		if c.cfg.FetchChangelog {
			if st, ok := resolvingTransitionBy(histories, act, f.statusName(), f.statusCategoryKey()); ok {
				resolvedByUs, byStatus = true, st
			}
		} else if isAssignee {
			resolvedByUs = true
		}
		if resolvedByUs {
			ev := c.newEvent(req, iss, models.TypeIssueResolved,
				"issue_resolved:"+iss.Key, f.ResolutionDate.Time)
			ev.Title = iss.Key + ": " + f.Summary
			ev.Meta["inferred"] = !c.cfg.FetchChangelog
			if byStatus != "" {
				ev.Meta["resolved_status"] = byStatus
			}
			events = append(events, finish(ev))
		}
	}

	// 4. Переходы статусов и правки полей из истории.
	for _, h := range histories {
		if !act.matches(h.Author) || !inPeriod(h.Created.Time, from, to) {
			continue
		}
		for idx, it := range h.Items {
			field := strings.TrimSpace(it.Field)
			if field == "" {
				field = it.FieldID
			}
			// Ключ поля в ExternalID нормализуем: он попадает в детерминированный
			// хеш события, а Jira шлёт поля с пробелами и разным регистром.
			slug := fieldSlug(field)
			if slug == "" {
				slug = strconv.Itoa(idx)
			}
			extID := "changelog:" + h.ID + ":" + slug

			if strings.EqualFold(field, "status") {
				ev := c.newEvent(req, iss, models.TypeStatusChanged, extID, h.Created.Time)
				ev.Title = fmt.Sprintf("%s: %s → %s", iss.Key, dash(it.FromString), dash(it.ToString))
				ev.Meta["from"] = it.FromString
				ev.Meta["to"] = it.ToString
				events = append(events, finish(ev))
				continue
			}

			ev := c.newEvent(req, iss, models.TypeFieldChanged, extID, h.Created.Time)
			ev.Title = fmt.Sprintf("%s: %s: %s → %s", iss.Key, field, dash(it.FromString), dash(it.ToString))
			ev.Meta["field"] = field
			ev.Meta["from"] = it.FromString
			ev.Meta["to"] = it.ToString
			events = append(events, finish(ev))
		}
	}

	// 5. Комментарии.
	for _, cm := range comments {
		if !act.matches(cm.Author) || !inPeriod(cm.Created.Time, from, to) {
			continue
		}
		ev := c.newEvent(req, iss, models.TypeJiraComment, "comment:"+cm.ID, cm.Created.Time)
		ev.Title = iss.Key + ": " + f.Summary
		ev.Body = adfToText(cm.Body)
		ev.URL = c.issueURL(iss.Key) + "?focusedCommentId=" + url.QueryEscape(cm.ID)
		ev.Meta["comment_id"] = cm.ID
		if !cm.Updated.Time.IsZero() && cm.Updated.Time.After(cm.Created.Time) {
			ev.Meta["edited"] = true
		}
		events = append(events, finish(ev))
	}

	// 6. Ворклоги.
	for _, w := range worklogs {
		if !act.matches(w.Author) || !inPeriod(w.Started.Time, from, to) {
			continue
		}
		ev := c.newEvent(req, iss, models.TypeWorklog, "worklog:"+w.ID, w.Started.Time)
		ev.Title = iss.Key + ": " + f.Summary
		ev.Body = adfToText(w.Comment)
		ev.Effort = float64(w.TimeSpentSeconds)
		ev.EffortUnit = "seconds"
		ev.Meta["time_spent_seconds"] = w.TimeSpentSeconds
		if w.TimeSpent != "" {
			ev.Meta["time_spent"] = w.TimeSpent
		}
		ev.Meta["worklog_id"] = w.ID
		events = append(events, finish(ev))
	}

	// Doc-ссылки собираем только для «своих» задач: иначе в отчёт попадут
	// документы из задач, которые просто нашлись по совпадению текста.
	if len(events) == 0 && !isAssignee && !isReporter {
		return events, nil, softErr
	}

	docs := c.collectDocLinks(ctx, req, iss, comments)
	return events, docs, softErr
}

// finish доводит событие до консистентного вида (нормализация + хеш-ID).
func finish(ev models.Event) models.Event {
	ev.Normalize()
	return ev
}

// newEvent заполняет общую для всех типов часть события.
func (c *Collector) newEvent(req collectors.Request, iss *jiraIssue, typ models.EventType, extID string, at time.Time) models.Event {
	f := iss.Fields
	meta := map[string]any{}
	if n := f.issueTypeName(); n != "" {
		meta["issue_type"] = n
	}
	if n := f.statusName(); n != "" {
		meta["status"] = n
	}
	if n := f.statusCategoryKey(); n != "" {
		meta["status_category"] = n
	}
	if f.Priority != nil && f.Priority.Name != "" {
		meta["priority"] = f.Priority.Name
	}
	if len(f.Labels) > 0 {
		meta["labels"] = f.Labels
	}
	if f.Parent != nil && f.Parent.Key != "" {
		meta["parent_key"] = f.Parent.Key
	}
	if f.Assignee != nil && f.Assignee.DisplayName != "" {
		meta["assignee"] = f.Assignee.DisplayName
	}

	return models.Event{
		PersonKey:   req.Person.Key,
		Source:      models.SourceJira,
		Type:        typ,
		ExternalID:  extID,
		OccurredAt:  at.UTC(),
		Project:     f.projectKey(),
		ProjectName: f.projectName(),
		RefID:       iss.Key,
		URL:         c.issueURL(iss.Key),
		Meta:        meta,
	}
}

func (c *Collector) issueURL(key string) string {
	return c.baseURL() + "/browse/" + key
}

// lastSelfAssignment ищет в истории самое позднее САМОназначение задачи:
// запись, где assignee стал нашим человеком И автор записи — он же.
// Назначение чужой рукой действием человека не является и не учитывается.
func lastSelfAssignment(histories []jiraHistory, act actor) (time.Time, bool) {
	var (
		best time.Time
		ok   bool
	)
	for _, h := range histories {
		if !act.matches(h.Author) {
			continue
		}
		for _, it := range h.Items {
			if !strings.EqualFold(it.Field, "assignee") {
				continue
			}
			// В Cloud в `to` лежит accountId, в `toString` — отображаемое имя;
			// в Server/DC в `to` — логин. Сверяем оба варианта.
			if !act.matchesRaw(it.To, it.ToString) {
				continue
			}
			if h.Created.Time.After(best) {
				best, ok = h.Created.Time, true
			}
		}
	}
	return best, ok
}

// matchesRaw сверяет «сырые» значения changelog-элемента с нашим пользователем.
func (a actor) matchesRaw(raw, display string) bool {
	raw, display = strings.TrimSpace(raw), strings.TrimSpace(display)
	if a.accountID != "" && raw != "" && a.accountID == raw {
		return true
	}
	if a.email != "" {
		if strings.EqualFold(a.email, raw) || strings.EqualFold(a.email, display) {
			return true
		}
		if login, _, _ := strings.Cut(a.email, "@"); login != "" && strings.EqualFold(login, raw) {
			return true
		}
	}
	return false
}

// resolvingTransitionBy проверяет, что переход в «разрешённый» статус сделал наш
// пользователь, и возвращает название целевого статуса.
//
// Категорию статуса changelog не отдаёт, поэтому опираемся на два признака:
// переход в текущий статус задачи, если сама задача в категории done, и на
// узнаваемые названия финальных статусов.
func resolvingTransitionBy(histories []jiraHistory, act actor, currentStatus, currentCategory string) (string, bool) {
	doneNow := strings.EqualFold(currentCategory, "done")
	for _, h := range histories {
		if !act.matches(h.Author) {
			continue
		}
		for _, it := range h.Items {
			if !strings.EqualFold(it.Field, "status") {
				continue
			}
			to := strings.TrimSpace(it.ToString)
			if to == "" {
				continue
			}
			if (doneNow && strings.EqualFold(to, currentStatus)) || looksResolved(to) {
				return to, true
			}
		}
	}
	return "", false
}

var resolvedStatusHints = []string{
	"done", "closed", "resolved", "complete", "released", "shipped",
	"готов", "закры", "выполнен", "решен", "решён", "заверш",
}

func looksResolved(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, h := range resolvedStatusHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return false
}

func fieldSlug(field string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(field)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '-':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// inPeriod — полуинтервал [from, to), чтобы соседние периоды не пересекались.
func inPeriod(t, from, to time.Time) bool {
	if t.IsZero() {
		return false
	}
	u := t.UTC()
	return !u.Before(from) && u.Before(to)
}

// ---------------------------------------------------------------------------
// Дочитывание усечённых коллекций
// ---------------------------------------------------------------------------

func (c *Collector) fetchChangelog(ctx context.Context, key string) ([]jiraHistory, error) {
	var (
		out     []jiraHistory
		startAt int
	)
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(c.pageSize()))

		var resp changelogPage
		_, err := c.cl.GetJSON(ctx, "/rest/api/3/issue/"+url.PathEscape(key)+"/changelog", q, &resp)
		if err != nil {
			return out, err
		}
		out = append(out, resp.Values...)
		startAt += len(resp.Values)
		if len(resp.Values) == 0 || (resp.IsLast != nil && *resp.IsLast) || startAt >= resp.Total {
			break
		}
	}
	return out, nil
}

func (c *Collector) fetchComments(ctx context.Context, key string) ([]jiraComment, error) {
	var (
		out     []jiraComment
		startAt int
	)
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(c.pageSize()))

		var resp commentPage
		_, err := c.cl.GetJSON(ctx, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", q, &resp)
		if err != nil {
			return out, err
		}
		out = append(out, resp.Comments...)
		startAt += len(resp.Comments)
		if len(resp.Comments) == 0 || startAt >= resp.Total {
			break
		}
	}
	return out, nil
}

func (c *Collector) fetchWorklogs(ctx context.Context, key string) ([]jiraWorklog, error) {
	var (
		out     []jiraWorklog
		startAt int
	)
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(c.pageSize()))

		var resp worklogPage
		_, err := c.cl.GetJSON(ctx, "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", q, &resp)
		if err != nil {
			return out, err
		}
		out = append(out, resp.Worklogs...)
		startAt += len(resp.Worklogs)
		if len(resp.Worklogs) == 0 || startAt >= resp.Total {
			break
		}
	}
	return out, nil
}

func (c *Collector) fetchRemoteLinks(ctx context.Context, key string) ([]remoteLink, error) {
	var out []remoteLink
	_, err := c.cl.GetJSON(ctx, "/rest/api/3/issue/"+url.PathEscape(key)+"/remotelink", nil, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Ссылки на Google-документы
// ---------------------------------------------------------------------------

// DocRef — найденная в тексте ссылка на документ Google.
type DocRef struct {
	// ID — идентификатор файла в Drive.
	ID string
	// URL — ссылка ровно в том виде, в каком она встретилась в тексте.
	URL string
}

// Регулярки специально не пытаются валидировать весь URL: важно поймать ID,
// а хвост (query, #heading, /edit) нам не нужен. Ограничение {15,} на длину
// отсекает короткие мусорные совпадения вроде /file/d/1.
var (
	docPathRe = regexp.MustCompile(`(?i)https?://(?:docs|drive|sheets|slides)\.google\.com/(?:a/[^/\s"'<>]+/)?(?:document|spreadsheets|presentation|file|drawings|forms)/d/([a-zA-Z0-9_-]{15,})`)
	docOpenRe = regexp.MustCompile(`(?i)https?://drive\.google\.com/open\?(?:[^\s"'<>]*&)?id=([a-zA-Z0-9_-]{15,})`)
)

// ExtractGoogleDocIDs находит в произвольном тексте ссылки на файлы Google
// (Docs, Sheets, Slides, Drive) и возвращает их без дублей, в порядке появления.
func ExtractGoogleDocIDs(text string) []DocRef {
	if text == "" {
		return nil
	}
	var (
		out  []DocRef
		seen = map[string]bool{}
	)
	add := func(matches [][]string) {
		for _, m := range matches {
			id := m[1]
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, DocRef{ID: id, URL: m[0]})
		}
	}
	add(docPathRe.FindAllStringSubmatch(text, -1))
	add(docOpenRe.FindAllStringSubmatch(text, -1))
	return out
}

// collectDocLinks сканирует задачу на ссылки на Google-документы.
//
// Порядок источников важен: description считается более «авторитетным» местом
// постановки, чем комментарий, а FoundIn первой находки закрепляется за
// документом (дальше дубли отбрасываются).
func (c *Collector) collectDocLinks(ctx context.Context, req collectors.Request, iss *jiraIssue, comments []jiraComment) []models.DocLink {
	f := iss.Fields
	var (
		out  []models.DocLink
		seen = map[string]bool{}
	)
	now := time.Now().UTC()

	appendFrom := func(text, foundIn string) {
		for _, ref := range ExtractGoogleDocIDs(text) {
			if seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			out = append(out, models.DocLink{
				DocID:        ref.ID,
				DocURL:       ref.URL,
				IssueKey:     iss.Key,
				IssueTitle:   f.Summary,
				PersonKey:    req.Person.Key,
				FoundIn:      foundIn,
				DiscoveredAt: now,
			})
		}
	}

	appendFrom(f.Summary, "summary")
	appendFrom(adfToText(f.Description), "description")
	// Родитель (эпик/стори) часто и держит ссылку на общий TSD.
	if f.Parent != nil {
		appendFrom(f.Parent.Fields.Summary, "summary")
	}
	for _, cm := range comments {
		appendFrom(adfToText(cm.Body), "comment")
	}

	// Remote links — отдельный запрос, поэтому делаем его последним и только для
	// релевантных задач (вызывающий уже отфильтровал чужие).
	links, err := c.fetchRemoteLinks(ctx, iss.Key)
	if err != nil {
		c.log.Warn("jira: remote links недоступны", "issue", iss.Key, "err", err)
		return out
	}
	for _, l := range links {
		appendFrom(l.Object.URL, "remotelink")
		appendFrom(l.Object.Title, "remotelink")
	}
	return out
}

// ---------------------------------------------------------------------------
// ADF → текст
// ---------------------------------------------------------------------------

// adfToText разворачивает Atlassian Document Format в плоский текст.
//
// Нужен и для показа в ленте, и для поиска ссылок: в ADF URL живёт не в тексте,
// а в атрибутах узлов (marks[].attrs.href, inlineCard.attrs.url), поэтому просто
// склеить text-узлы недостаточно. Также поддерживается обычная строка — так тело
// приходит из Jira Server/DC и из полей wiki-разметки.
func adfToText(v any) string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	writeADF(v, &b)
	return normalizeText(b.String())
}

func writeADF(v any, b *strings.Builder) {
	switch n := v.(type) {
	case string:
		b.WriteString(n)
	case []any:
		for _, item := range n {
			writeADF(item, b)
		}
	case map[string]any:
		typ, _ := n["type"].(string)

		// Листовые узлы: у них нет content, всё содержимое в атрибутах.
		switch typ {
		case "text":
			txt, _ := n["text"].(string)
			b.WriteString(txt)
			// Ссылка живёт в marks, а видимый текст может быть подписью —
			// дописываем href, иначе URL потеряется для ExtractGoogleDocIDs.
			if href := linkHref(n["marks"]); href != "" && !strings.Contains(txt, href) {
				b.WriteString(" (" + href + ")")
			}
			return
		case "hardBreak":
			b.WriteString("\n")
			return
		case "rule":
			b.WriteString("\n")
			return
		case "inlineCard", "blockCard", "embedCard":
			attrs := n["attrs"]
			if u := attrString(attrs, "url"); u != "" {
				b.WriteString(u)
			} else if u := attrString(attrs, "href"); u != "" {
				b.WriteString(u)
			}
			if typ != "inlineCard" {
				b.WriteString("\n")
			}
			return
		case "mention":
			if t := attrString(n["attrs"], "text"); t != "" {
				b.WriteString(t)
			}
			return
		case "emoji":
			if t := attrString(n["attrs"], "text"); t != "" {
				b.WriteString(t)
			} else if t := attrString(n["attrs"], "shortName"); t != "" {
				b.WriteString(t)
			}
			return
		case "media":
			// У вложений текста нет, но иногда есть осмысленное имя файла.
			if t := attrString(n["attrs"], "alt"); t != "" {
				b.WriteString(t)
			}
			return
		case "listItem", "taskItem":
			b.WriteString("- ")
		case "tableCell", "tableHeader":
			defer b.WriteString(" | ")
		}

		if content, ok := n["content"]; ok {
			writeADF(content, b)
		}

		// Блочные узлы завершаем переводом строки, чтобы абзацы не слипались.
		switch typ {
		case "paragraph", "heading", "blockquote", "codeBlock", "panel",
			"listItem", "taskItem", "tableRow", "mediaSingle", "mediaGroup", "expand":
			b.WriteString("\n")
		}
	}
}

// linkHref достаёт href из marks узла text.
func linkHref(marks any) string {
	list, ok := marks.([]any)
	if !ok {
		return ""
	}
	for _, m := range list {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := mm["type"].(string); t != "link" {
			continue
		}
		if href := attrString(mm["attrs"], "href"); href != "" {
			return href
		}
	}
	return ""
}

func attrString(attrs any, key string) string {
	m, ok := attrs.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// normalizeText схлопывает лишние пустые строки и хвостовые пробелы.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := true
	for _, l := range lines {
		l = strings.TrimRight(l, " \t|")
		if strings.TrimSpace(l) == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
			out = append(out, "")
			continue
		}
		prevBlank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ---------------------------------------------------------------------------
// Модель ответов Jira
// ---------------------------------------------------------------------------

type searchResponse struct {
	Issues        []jiraIssue `json:"issues"`
	NextPageToken string      `json:"nextPageToken"`
	IsLast        *bool       `json:"isLast"`
	StartAt       int         `json:"startAt"`
	MaxResults    int         `json:"maxResults"`
	Total         int         `json:"total"`
}

type jiraIssue struct {
	ID        string         `json:"id"`
	Key       string         `json:"key"`
	Fields    issueFields    `json:"fields"`
	Changelog *changelogPage `json:"changelog"`
}

func (i *jiraIssue) changelogHistories() []jiraHistory {
	if i.Changelog == nil {
		return nil
	}
	if len(i.Changelog.Histories) > 0 {
		return i.Changelog.Histories
	}
	return i.Changelog.Values
}

// changelogTruncated сообщает, что в ответе поиска история пришла обрезанной:
// expand=changelog отдаёт максимум ~100 последних записей.
func (i *jiraIssue) changelogTruncated() bool {
	if i.Changelog == nil {
		return true
	}
	return i.Changelog.Total > len(i.changelogHistories())
}

type issueFields struct {
	Summary        string    `json:"summary"`
	Description    any       `json:"description"`
	Created        jiraTime  `json:"created"`
	Updated        jiraTime  `json:"updated"`
	ResolutionDate jiraTime  `json:"resolutiondate"`
	Assignee       *jiraUser `json:"assignee"`
	Reporter       *jiraUser `json:"reporter"`
	Creator        *jiraUser `json:"creator"`
	Labels         []string  `json:"labels"`

	Status *struct {
		Name           string `json:"name"`
		StatusCategory *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"statusCategory"`
	} `json:"status"`

	IssueType *struct {
		Name    string `json:"name"`
		Subtask bool   `json:"subtask"`
	} `json:"issuetype"`

	Project *struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"project"`

	Priority *struct {
		Name string `json:"name"`
	} `json:"priority"`

	Parent *struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	} `json:"parent"`

	Comment *commentPage `json:"comment"`
	Worklog *worklogPage `json:"worklog"`
}

func (f issueFields) projectKey() string {
	if f.Project == nil {
		return ""
	}
	return f.Project.Key
}

func (f issueFields) projectName() string {
	if f.Project == nil {
		return ""
	}
	return f.Project.Name
}

func (f issueFields) statusName() string {
	if f.Status == nil {
		return ""
	}
	return f.Status.Name
}

func (f issueFields) statusCategoryKey() string {
	if f.Status == nil || f.Status.StatusCategory == nil {
		return ""
	}
	return f.Status.StatusCategory.Key
}

func (f issueFields) issueTypeName() string {
	if f.IssueType == nil {
		return ""
	}
	return f.IssueType.Name
}

func (f issueFields) commentList() []jiraComment {
	if f.Comment == nil {
		return nil
	}
	return f.Comment.Comments
}

// commentsTruncated: в выдаче поиска Jira отдаёт последние ~20 комментариев.
func (f issueFields) commentsTruncated() bool {
	if f.Comment == nil {
		return false
	}
	return f.Comment.Total > len(f.Comment.Comments)
}

func (f issueFields) worklogList() []jiraWorklog {
	if f.Worklog == nil {
		return nil
	}
	return f.Worklog.Worklogs
}

func (f issueFields) worklogsTruncated() bool {
	if f.Worklog == nil {
		return false
	}
	return f.Worklog.Total > len(f.Worklog.Worklogs)
}

type jiraUser struct {
	AccountID    string `json:"accountId"`
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
	// name/key приходят только от Server/DC.
	Name string `json:"name"`
	Key  string `json:"key"`
}

// changelogPage покрывает оба формата: вложенный в issue (histories) и ответ
// эндпоинта /issue/{key}/changelog (values).
type changelogPage struct {
	StartAt       int           `json:"startAt"`
	MaxResults    int           `json:"maxResults"`
	Total         int           `json:"total"`
	IsLast        *bool         `json:"isLast"`
	NextPageToken string        `json:"nextPageToken"`
	Histories     []jiraHistory `json:"histories"`
	Values        []jiraHistory `json:"values"`
}

type jiraHistory struct {
	ID      string        `json:"id"`
	Author  *jiraUser     `json:"author"`
	Created jiraTime      `json:"created"`
	Items   []historyItem `json:"items"`
}

type historyItem struct {
	Field      string `json:"field"`
	FieldType  string `json:"fieldtype"`
	FieldID    string `json:"fieldId"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

type commentPage struct {
	Comments   []jiraComment `json:"comments"`
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
}

type jiraComment struct {
	ID      string    `json:"id"`
	Author  *jiraUser `json:"author"`
	Body    any       `json:"body"`
	Created jiraTime  `json:"created"`
	Updated jiraTime  `json:"updated"`
}

type worklogPage struct {
	Worklogs   []jiraWorklog `json:"worklogs"`
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
}

type jiraWorklog struct {
	ID               string    `json:"id"`
	Author           *jiraUser `json:"author"`
	Comment          any       `json:"comment"`
	Started          jiraTime  `json:"started"`
	Created          jiraTime  `json:"created"`
	TimeSpent        string    `json:"timeSpent"`
	TimeSpentSeconds int       `json:"timeSpentSeconds"`
}

type remoteLink struct {
	ID     int64 `json:"id"`
	Object struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"object"`
}

// jiraTime разбирает метки времени Jira: формат `2006-01-02T15:04:05.000-0700`
// не является валидным RFC3339 (нет двоеточия в смещении), поэтому нужен
// собственный разбор.
type jiraTime struct{ time.Time }

var jiraTimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05-0700",
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02",
}

// UnmarshalJSON намеренно не возвращает ошибку на неразобранном значении:
// одно кривое поле не должно ронять разбор всей страницы задач — событие просто
// не будет создано (нулевое время не попадает ни в один период).
func (t *jiraTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range jiraTimeLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			t.Time = ts
			return nil
		}
	}
	return nil
}

// isStatus сообщает, что ошибка — это ответ API с одним из указанных кодов.
func isStatus(err error, codes ...int) bool {
	var ae *httpx.APIError
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if ae.Status == c {
			return true
		}
	}
	return false
}
