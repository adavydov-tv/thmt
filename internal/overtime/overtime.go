// Package overtime читает заявки на оплачиваемые овертаймы из Jira
// Server/DC (jira.xtools.tv): тикеты вида VAC-* с Request type «Paid:
// Overtime» и полем Employee. Используется вкладкой «Нарушения», чтобы
// сверять заявленные овертаймы с фактической активностью.
package overtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
)

// Entry — одна заявка на овертайм. Овертайм может длиться несколько дней:
// Start/End — включительные даты из полей заявки (Start Date / End Date).
type Entry struct {
	Key      string    `json:"key"`
	Summary  string    `json:"summary"`
	Employee string    `json:"employee"` // как в Jira: имя/логин/e-mail
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	URL      string    `json:"url"`
	// Kind — тип заявки, распознанный по summary (Request Type скрыт от нашего
	// токена JSM-политикой): overtime | vacation | sick | remote | leave | "".
	Kind string `json:"kind"`
}

// Виды заявок VAC для классификации абсансов.
const (
	KindOvertime = "overtime"
	KindVacation = "vacation" // отпуск и приравненные дни отсутствия
	KindSick     = "sick"     // больничные всех видов
	KindRemote   = "remote"   // Paid: Work From Home
	KindLeave    = "leave"    // прочие оплачиваемые/неоплачиваемые отгулы
)

// classify распознаёт тип заявки по summary вида «Paid: <Type> request …».
func classify(summary string) string {
	s := strings.ToLower(summary)
	switch {
	case strings.Contains(s, "overtime"):
		return KindOvertime
	case strings.Contains(s, "work from home"):
		return KindRemote
	case strings.Contains(s, "sick") || strings.Contains(s, "family sickness"):
		return KindSick
	case strings.Contains(s, "vacation"):
		return KindVacation
	case strings.Contains(s, "bereavement") || strings.Contains(s, "marriage") ||
		strings.Contains(s, "moving day") || strings.Contains(s, "caregiving") ||
		strings.Contains(s, "unpaid leave") || strings.Contains(s, "parental"):
		return KindLeave
	case strings.Contains(s, "paperwork"):
		return "" // не абсанс
	}
	return ""
}

// Days возвращает даты овертайма (YYYY-MM-DD), обрезанные по [from, to).
func (e Entry) Days(from, to time.Time) []string {
	if e.Start.IsZero() {
		return nil
	}
	end := e.End
	if end.Before(e.Start) || end.IsZero() {
		end = e.Start
	}
	var out []string
	for d := e.Start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if d.Before(from) || !d.Before(to) {
			continue
		}
		out = append(out, d.Format("2006-01-02"))
		if len(out) > 62 {
			break // предохранитель от кривых дат в заявке
		}
	}
	return out
}

// Client — клиент Jira DC для чтения заявок.
type Client struct {
	cfg config.OvertimeConfig
	cl  *httpx.Client
	log *slog.Logger

	// id кастомных полей; резолвятся по именам один раз через /rest/api/2/field
	// (см. fieldIDs) — чтобы в search запрашивать только нужные поля, а не *all.
	fieldsResolved  bool
	employeeFieldID string
	startFieldID    string
	endFieldID      string

	// fieldMap — кэш «имя поля → id» инстанса Jira (один запрос на клиента).
	fieldsMu sync.Mutex
	fieldMap map[string]string

	// Кэш заявок широким окном: Jira DC за прокси отвечает секундами, поэтому
	// сеть вынесена из пути запроса — WarmUp обновляет окно фоном, а Fetch
	// нарезает из него нужный период.
	cacheMu   sync.Mutex
	cacheFrom time.Time
	cacheTo   time.Time
	cacheAt   time.Time
	cached    []Entry
	// refreshing — идёт фоновое обновление протухшего окна (защита от шквала).
	refreshing bool
	// persist, если задан, хранит снапшот кэша между рестартами процесса.
	persist Persist

	// Кэш заявок HCM на смену гибридных дней (см. hcm.go).
	hcmMu     sync.Mutex
	hcmAt     time.Time
	hcmCached []HybridTicket
}

// Persist — долговременное хранилище снапшота кэша (см. SetPersist).
type Persist interface {
	Load(ctx context.Context, kind string) (payload []byte, at time.Time, err error)
	Save(ctx context.Context, kind string, payload []byte) error
}

type cacheSnapshot struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	At      time.Time `json:"at"`
	Entries []Entry   `json:"entries"`
}

// SetPersist подключает хранилище снапшота и загружает его в кэш: после
// рестарта заявки отдаются мгновенно (пусть слегка устаревшие), а свежесть
// догоняет фоновое обновление.
func (c *Client) SetPersist(ctx context.Context, p Persist) {
	c.cacheMu.Lock()
	c.persist = p
	c.cacheMu.Unlock()
	payload, _, err := p.Load(ctx, "entries")
	if err != nil || payload == nil {
		return
	}
	var snap cacheSnapshot
	if json.Unmarshal(payload, &snap) != nil || len(snap.Entries) == 0 {
		return
	}
	c.cacheMu.Lock()
	if c.cacheAt.IsZero() {
		c.cacheFrom, c.cacheTo, c.cacheAt, c.cached = snap.From, snap.To, snap.At, snap.Entries
	}
	c.cacheMu.Unlock()
}

// snapshot сохраняет текущее окно кэша в долговременное хранилище.
func (c *Client) snapshot() {
	c.cacheMu.Lock()
	p := c.persist
	snap := cacheSnapshot{From: c.cacheFrom, To: c.cacheTo, At: c.cacheAt, Entries: c.cached}
	c.cacheMu.Unlock()
	if p == nil || snap.At.IsZero() {
		return
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.Save(bg, "entries", payload); err != nil {
			c.log.Warn("не удалось сохранить снапшот кэша овертаймов", "err", err)
		}
	}()
}

// refreshWide фоном перечитывает текущее широкое окно после протухания.
func (c *Client) refreshWide() {
	bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c.cacheMu.Lock()
	from, to := c.cacheFrom, c.cacheTo
	c.cacheMu.Unlock()
	entries, err := c.fetchRange(bg, from, to)
	c.cacheMu.Lock()
	c.refreshing = false
	if err == nil {
		c.cacheAt, c.cached = time.Now(), entries
	}
	c.cacheMu.Unlock()
	if err != nil {
		c.log.Warn("фоновое обновление кэша овертаймов не удалось", "err", err)
		return
	}
	c.snapshot()
}

const cacheTTL = 10 * time.Minute

// New создаёт клиент. Авторизация — Bearer PAT (Personal Access Token Jira DC).
func New(cfg config.OvertimeConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.BaseURL == "" || cfg.Token == "" {
		return nil, errors.New("overtime: не заданы OVERTIME_JIRA_URL / OVERTIME_JIRA_TOKEN")
	}
	cl, err := httpx.New(httpx.Options{
		BaseURL:    cfg.BaseURL,
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers:    map[string]string{"Authorization": "Bearer " + cfg.Token},
	})
	if err != nil {
		return nil, fmt.Errorf("overtime: %w", err)
	}
	return &Client{cfg: cfg, cl: cl, log: log}, nil
}

type searchResp struct {
	Total  int `json:"total"`
	Issues []struct {
		Key    string         `json:"key"`
		Fields map[string]any `json:"fields"`
	} `json:"issues"`
	Names map[string]string `json:"names"`
}

// Fetch возвращает заявки на овертайм, пересекающиеся с периодом. Диапазон
// в JQL — по полю даты начала с двухнедельным запасом назад (многодневный
// овертайм мог начаться до периода); точные дни обрезает Entry.Days.
func (c *Client) Fetch(ctx context.Context, from, to time.Time) ([]Entry, error) {
	all, err := c.fetchWindow(ctx, from, to)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.Kind == KindOvertime {
			out = append(out, e)
		}
	}
	return out, nil
}

// Absences возвращает заявки об отсутствии (без овертаймов) за период.
func (c *Client) Absences(ctx context.Context, from, to time.Time) ([]Entry, error) {
	all, err := c.fetchWindow(ctx, from, to)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.Kind != "" && e.Kind != KindOvertime {
			out = append(out, e)
		}
	}
	return out, nil
}

// fetchWindow отдаёт ВСЕ заявки периода из кэша (нарезка широкого окна).
func (c *Client) fetchWindow(ctx context.Context, from, to time.Time) ([]Entry, error) {
	needLo := from.AddDate(0, 0, -14)
	// Окно покрывает запрос — нарезаем без похода в сеть; протухшее окно
	// отдаём как есть и обновляем фоном: страница не должна ждать Jira.
	c.cacheMu.Lock()
	if !c.cacheAt.IsZero() && !c.cacheFrom.After(needLo) && !c.cacheTo.Before(to) {
		var out []Entry
		for _, e := range c.cached {
			if !e.Start.Before(needLo) && e.Start.Before(to) {
				out = append(out, e)
			}
		}
		if time.Since(c.cacheAt) >= cacheTTL && !c.refreshing {
			c.refreshing = true
			go c.refreshWide()
		}
		c.cacheMu.Unlock()
		return out, nil
	}
	c.cacheMu.Unlock()

	entries, err := c.fetchRange(ctx, needLo, to)
	if err != nil {
		return nil, err
	}
	c.cacheMu.Lock()
	c.cacheFrom, c.cacheTo, c.cacheAt, c.cached = needLo, to, time.Now(), entries
	c.cacheMu.Unlock()
	c.snapshot()
	return entries, nil
}

// WarmUp фоном держит кэш заявок широким окном (год назад — месяц вперёд),
// чтобы вкладка отклонений никогда не ждала Jira. Блокирует — запускать в
// горутине; останавливается по отмене контекста.
func (c *Client) WarmUp(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	for {
		from := time.Now().AddDate(-1, 0, 0)
		to := time.Now().AddDate(0, 1, 0)
		entries, err := c.fetchRange(ctx, from, to)
		next := cacheTTL - time.Minute
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("прогрев кэша овертаймов не удался", "err", err)
			next = time.Minute
		} else {
			c.cacheMu.Lock()
			c.cacheFrom, c.cacheTo, c.cacheAt, c.cached = from, to, time.Now(), entries
			c.cacheMu.Unlock()
			c.snapshot()
			log.Info("кэш овертаймов прогрет", "entries", len(entries))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// fetchRange выкачивает заявки с датой начала в [from, to) из Jira.
func (c *Client) fetchRange(ctx context.Context, from, to time.Time) ([]Entry, error) {
	// Сортировка по дате начала DESC: если тикетов больше лимита, в кэш
	// попадают самые свежие (отсутствия недавних недель важнее старых).
	jql := fmt.Sprintf(`%s AND "%s" >= "%s" AND "%s" < "%s" ORDER BY "%s" DESC`,
		c.cfg.JQL,
		c.cfg.StartDateField, from.Format("2006-01-02"),
		c.cfg.StartDateField, to.Format("2006-01-02"),
		c.cfg.StartDateField)

	if err := c.resolveFields(ctx); err != nil {
		return nil, fmt.Errorf("overtime: список полей Jira: %w", err)
	}
	fields := fieldsParam([]string{"summary", "reporter", "created"},
		c.employeeFieldID, c.startFieldID, c.endFieldID)

	var out []Entry
	for start := 0; start < 60000; start += c.pageSize() {
		q := url.Values{
			"jql":        {jql},
			"startAt":    {fmt.Sprint(start)},
			"maxResults": {fmt.Sprint(c.pageSize())},
			"fields":     {fields},
		}
		var res searchResp
		if _, err := c.cl.GetJSON(ctx, "/rest/api/2/search", q, &res); err != nil {
			return nil, fmt.Errorf("overtime: поиск заявок: %w", err)
		}
		for _, issue := range res.Issues {
			e := Entry{
				Key:     issue.Key,
				URL:     c.cfg.BaseURL + "/browse/" + issue.Key,
				Summary: str(issue.Fields["summary"]),
			}
			e.Employee = cleanEmployee(userString(issue.Fields[c.employeeFieldID]))
			if e.Employee == "" {
				// Заявку мог завести сам сотрудник — падаем на репортёра.
				e.Employee = userString(issue.Fields["reporter"])
			}
			e.Kind = classify(e.Summary)
			e.Start = parseDate(str(issue.Fields[c.startFieldID]))
			e.End = parseDate(str(issue.Fields[c.endFieldID]))
			if e.Start.IsZero() {
				e.Start = parseDate(str(issue.Fields["created"]))
			}
			if e.End.IsZero() {
				e.End = e.Start
			}
			if !e.Start.IsZero() {
				out = append(out, e)
			}
		}
		if start+len(res.Issues) >= res.Total || len(res.Issues) == 0 {
			break
		}
	}
	return out, nil
}

// InvalidateCache сбрасывает кэш заявок (например, после завершения сбора).
func (c *Client) InvalidateCache() {
	c.cacheMu.Lock()
	c.cacheAt = time.Time{}
	c.cacheMu.Unlock()
}

// fieldIDs лениво загружает карту «имя поля → id» из /rest/api/2/field —
// один лёгкий запрос на инстанс. Позволяет запрашивать в search только нужные
// поля (fields=id1,id2,...) вместо тяжёлого fields=*all + expand=names.
func (c *Client) fieldIDs(ctx context.Context) (map[string]string, error) {
	c.fieldsMu.Lock()
	defer c.fieldsMu.Unlock()
	if c.fieldMap != nil {
		return c.fieldMap, nil
	}
	var fields []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := c.cl.GetJSON(ctx, "/rest/api/2/field", nil, &fields); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		if f.Name != "" {
			m[strings.ToLower(strings.TrimSpace(f.Name))] = f.ID
		}
	}
	c.fieldMap = m
	return m, nil
}

// resolveFields находит id кастомных полей VAC по их именам (один раз).
func (c *Client) resolveFields(ctx context.Context) error {
	if c.fieldsResolved {
		return nil
	}
	m, err := c.fieldIDs(ctx)
	if err != nil {
		return err
	}
	c.employeeFieldID = m[strings.ToLower(strings.TrimSpace(c.cfg.EmployeeField))]
	c.startFieldID = m[strings.ToLower(strings.TrimSpace(c.cfg.StartDateField))]
	c.endFieldID = m[strings.ToLower(strings.TrimSpace(c.cfg.EndDateField))]
	c.fieldsResolved = true
	if c.employeeFieldID == "" {
		c.log.Warn("overtime: поле Employee не найдено по имени, используется reporter",
			"field", c.cfg.EmployeeField)
	}
	if c.startFieldID == "" {
		c.log.Warn("overtime: поле даты начала не найдено, используется created",
			"field", c.cfg.StartDateField)
	}
	return nil
}

// fieldsParam собирает значение параметра fields из встроенных и кастомных
// полей (пустые id-опускаются).
func fieldsParam(builtin []string, custom ...string) string {
	fields := append([]string{}, builtin...)
	for _, id := range custom {
		if id != "" {
			fields = append(fields, id)
		}
	}
	return strings.Join(fields, ",")
}

func (c *Client) pageSize() int {
	if c.cfg.PageSize > 0 && c.cfg.PageSize <= 200 {
		return c.cfg.PageSize
	}
	return 100
}

// userString достаёт человекочитаемый идентификатор из значения поля Jira:
// объект пользователя, строка или список.
func userString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		for _, k := range []string{"emailAddress", "name", "displayName"} {
			if s := str(t[k]); s != "" {
				return s
			}
		}
	case []any:
		if len(t) > 0 {
			return userString(t[0])
		}
	}
	return ""
}

// cleanEmployee убирает служебный хвост «(HRDB-…)» из значения поля Employee:
// «Ivan Klubkov (HRDB-69735)» → «Ivan Klubkov», чтобы матчиться по имени.
func cleanEmployee(s string) string {
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	// REST отдаёт date-поля ISO-строкой, но подстрахуемся и локальным
	// отображением dd.MM.yyyy (03.03.2026).
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2006-01-02T15:04:05.000-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		}
	}
	return time.Time{}
}

// KindToDayKind переводит вид заявки в вид особого дня (models.Day*).
// Пустая строка — заявка не создаёт особый день (овертайм, paperwork).
func KindToDayKind(kind string) string {
	switch kind {
	case KindVacation, KindLeave:
		return "vacation"
	case KindSick:
		return "sick"
	case KindRemote:
		return "remote"
	}
	return ""
}
