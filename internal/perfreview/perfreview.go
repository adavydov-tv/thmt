// Package perfreview читает из Jira DC (проект PR) итоги Performance Review и
// планы развития сотрудников:
//
//   - оценка за цикл — тикеты «Final review» (приоритет), «Line manager
//     review», «Functional manager review»: поле «Rate overall impact»
//     (Exceeds / Meets / Partially meets / Does not meet expectations);
//     цикл — префикс summary до « | » («2026Q2 - OKR Quarterly Check»,
//     «Annual Performance Review 2025/2026»);
//   - PIP — тикеты «Personal Development Plan» с полем Type
//     «PIP: Performance Improvement Plan» (или summary с префиксом «PIP:»).
//
// Сотрудник привязан полем Employee вида «Имя Фамилия (HRDB-222448)» — ключ
// объекта HRDB (Atlassian Assets), по нему и сопоставляем с нашими людьми
// (через e-mail из HRDB-кэша), запасной путь — по имени.
package perfreview

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
)

// cacheTTL — как долго снапшот считается свежим; фон обновляет его заранее.
const cacheTTL = 30 * time.Minute

// Коды оценок — короткие бейджи для таблиц.
const (
	GradeExceeds   = "E" // Exceeds expectations
	GradeMeets     = "M" // Meets expectations
	GradePartially = "P" // Partially meets expectations
	GradeNotMeet   = "D" // Does not meet expectations
)

// Cycle — цикл оценки (квартальный OKR-чек или годовое ревью).
type Cycle struct {
	Key   string    `json:"key"`   // как в summary: «2026Q2 - OKR Quarterly Check»
	Start time.Time `json:"start"` // дата создания самого раннего тикета цикла
}

// Rating — оценка сотрудника за цикл.
type Rating struct {
	Cycle    string `json:"cycle"`
	Grade    string `json:"grade"`     // код: E / M / P / D
	Text     string `json:"text"`      // как в Jira
	IssueKey string `json:"issue_key"` // PR-26118
	URL      string `json:"url"`       //
	Source   string `json:"source"`    // тип тикета, из которого взята оценка
	priority int    // Final > Line manager > Functional manager
}

// Plan — план развития (PIP или PDP).
type Plan struct {
	IssueKey string    `json:"issue_key"`
	URL      string    `json:"url"`
	Type     string    `json:"type"` // PIP | PDP
	Status   string    `json:"status"`
	Active   bool      `json:"active"` // статус не Closed
	Created  time.Time `json:"created"`
}

// Employee — всё, что известно о сотруднике из проекта PR.
type Employee struct {
	HRDBKey string
	Name    string
	Ratings map[string]Rating // по ключу цикла
	Plans   []Plan
}

// Snapshot — разобранное состояние проекта PR.
type Snapshot struct {
	At     time.Time
	Cycles []Cycle // по хронологии
	// ByHRDB — сотрудники по ключу HRDB; ByName — по нормализованному имени
	// (запасной путь, когда HRDB-ключ не совпал).
	ByHRDB map[string]*Employee
	ByName map[string]*Employee
}

// Client — клиент Jira DC с кэшем снапшота.
type Client struct {
	cfg config.PerfReviewConfig
	cl  *httpx.Client
	log *slog.Logger

	fieldsMu sync.Mutex
	fieldMap map[string]string

	mu         sync.Mutex
	snap       *Snapshot
	refreshing bool
}

// New создаёт клиент. Ошибка — только при отсутствии адреса/токена.
func New(cfg config.PerfReviewConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Client, error) {
	if cfg.BaseURL == "" || cfg.Token == "" {
		return nil, fmt.Errorf("perfreview: нужны PERF_REVIEW_JIRA_URL и PERF_REVIEW_JIRA_TOKEN (или OVERTIME_JIRA_*)")
	}
	if log == nil {
		log = slog.Default()
	}
	cl, err := httpx.New(httpx.Options{
		BaseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers:    map[string]string{"Authorization": "Bearer " + cfg.Token},
	})
	if err != nil {
		return nil, fmt.Errorf("perfreview: %w", err)
	}
	return &Client{cfg: cfg, cl: cl, log: log.With("source", "perfreview")}, nil
}

// Snapshot возвращает кэшированный снапшот; протухший отдаётся как есть и
// обновляется фоном — страница не должна ждать Jira DC за прокси. Первый
// вызов без кэша ходит в сеть синхронно.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	c.mu.Lock()
	if c.snap != nil {
		snap := c.snap
		if time.Since(snap.At) >= cacheTTL && !c.refreshing {
			c.refreshing = true
			go c.refresh()
		}
		c.mu.Unlock()
		return snap, nil
	}
	c.mu.Unlock()

	snap, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.snap = snap
	c.mu.Unlock()
	return snap, nil
}

func (c *Client) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	snap, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	if err != nil {
		c.log.Warn("perfreview: обновление снапшота не удалось", "err", err)
		return
	}
	c.snap = snap
}

// WarmUp фоном держит снапшот свежим. Блокирует — запускать в горутине.
func (c *Client) WarmUp(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = c.log
	}
	for {
		snap, err := c.fetch(ctx)
		next := cacheTTL - time.Minute
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("прогрев кэша performance review не удался", "err", err)
			next = time.Minute
		} else {
			c.mu.Lock()
			c.snap = snap
			c.mu.Unlock()
			log.Info("кэш performance review прогрет", "employees", len(snap.ByHRDB), "cycles", len(snap.Cycles))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// ---------------------------------------------------------------------------
// Загрузка
// ---------------------------------------------------------------------------

type searchResp struct {
	Total  int `json:"total"`
	Issues []struct {
		Key    string         `json:"key"`
		Fields map[string]any `json:"fields"`
	} `json:"issues"`
}

// rawIssue — тикет PR в том виде, в каком его разбирает parse (без сети).
type rawIssue struct {
	Key      string
	Type     string
	Status   string
	Summary  string
	Created  time.Time
	Employee string // «Имя (HRDB-123)» или пусто
	Rate     string // Rate overall impact
	PlanType string // Type у Personal Development Plan
	BaseURL  string
}

func (c *Client) fetch(ctx context.Context) (*Snapshot, error) {
	ids, err := c.fieldIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("perfreview: список полей Jira: %w", err)
	}
	employeeID, rateID, typeID := ids["employee"], ids["rate overall impact"], ids["type"]
	fields := []string{"summary", "issuetype", "status", "created"}
	for _, id := range []string{employeeID, rateID, typeID} {
		if id != "" {
			fields = append(fields, id)
		}
	}
	var raws []rawIssue
	for start := 0; start < 20000; start += c.pageSize() {
		q := url.Values{
			"jql":        {c.cfg.JQL},
			"startAt":    {fmt.Sprint(start)},
			"maxResults": {fmt.Sprint(c.pageSize())},
			"fields":     {strings.Join(fields, ",")},
		}
		var res searchResp
		if _, err := c.cl.GetJSON(ctx, "/rest/api/2/search", q, &res); err != nil {
			return nil, fmt.Errorf("perfreview: поиск тикетов PR: %w", err)
		}
		raws = append(raws, convertIssues(res, employeeID, rateID, typeID, strings.TrimRight(c.cfg.BaseURL, "/"))...)
		if start+len(res.Issues) >= res.Total || len(res.Issues) == 0 {
			break
		}
	}
	snap := parse(raws)
	snap.At = time.Now()
	return snap, nil
}

// convertIssues переводит страницу ответа /search в rawIssue по известным id
// кастомных полей (Employee, Rate overall impact, Type).
func convertIssues(res searchResp, employeeID, rateID, typeID, baseURL string) []rawIssue {
	out := make([]rawIssue, 0, len(res.Issues))
	for _, is := range res.Issues {
		f := is.Fields
		out = append(out, rawIssue{
			Key:      is.Key,
			Type:     nested(f["issuetype"], "name"),
			Status:   nested(f["status"], "name"),
			Summary:  str(f["summary"]),
			Created:  parseTime(str(f["created"])),
			Employee: firstString(f[employeeID]),
			Rate:     optionValue(f[rateID]),
			PlanType: optionValue(f[typeID]),
			BaseURL:  baseURL,
		})
	}
	return out
}

// fieldIDs — «имя поля в нижнем регистре → id» (один запрос на клиента).
func (c *Client) fieldIDs(ctx context.Context) (map[string]string, error) {
	c.fieldsMu.Lock()
	defer c.fieldsMu.Unlock()
	if c.fieldMap != nil {
		return c.fieldMap, nil
	}
	var fields []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Custom bool   `json:"custom"`
	}
	if _, err := c.cl.GetJSON(ctx, "/rest/api/2/field", nil, &fields); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		// Системное поле «Type» (issuetype) не должно затирать кастомное «Type»
		// планов развития: берём кастомное, если оно есть.
		if prev, ok := m[name]; ok && !f.Custom && strings.HasPrefix(prev, "customfield_") {
			continue
		}
		m[name] = f.ID
	}
	c.fieldMap = m
	return m, nil
}

func (c *Client) pageSize() int {
	if c.cfg.PageSize > 0 && c.cfg.PageSize <= 200 {
		return c.cfg.PageSize
	}
	return 100
}

// ---------------------------------------------------------------------------
// Разбор
// ---------------------------------------------------------------------------

var employeeRe = regexp.MustCompile(`^(.*?)\s*\((HRDB-\d+)\)\s*$`)

// ratingPriority — из какого тикета брать оценку, если их несколько за цикл.
var ratingPriority = map[string]int{
	"final review":              3,
	"line manager review":       2,
	"functional manager review": 1,
}

// gradeCode переводит текст оценки в код бейджа; пусто — не оценка.
func gradeCode(text string) string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "exceeds expectations":
		return GradeExceeds
	case "meets expectations":
		return GradeMeets
	case "partially meets expectations":
		return GradePartially
	case "does not meet expectations":
		return GradeNotMeet
	}
	return ""
}

// cycleOf — цикл из summary: всё до первого « | ».
func cycleOf(summary string) string {
	if i := strings.Index(summary, "|"); i >= 0 {
		return strings.TrimSpace(summary[:i])
	}
	return strings.TrimSpace(summary)
}

// employeeOf разбирает «Имя Фамилия (HRDB-123)» → имя, ключ. Без ключа в
// скобках возвращает имя как есть (ключ пустой).
func employeeOf(v string) (name, key string) {
	v = strings.TrimSpace(v)
	if m := employeeRe.FindStringSubmatch(v); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	return v, ""
}

// employeeFromSummary — имя из summary вида «<цикл> | <Имя> | <тип>».
func employeeFromSummary(summary string) string {
	parts := strings.Split(summary, "|")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// planTypeOf — PIP или PDP по полю Type, иначе по префиксу summary.
func planTypeOf(typeField, summary string) string {
	t := strings.ToUpper(typeField + " " + summary)
	switch {
	case strings.Contains(t, "PIP"):
		return "PIP"
	case strings.Contains(t, "PDP"):
		return "PDP"
	}
	return ""
}

// NormalizeName — ключ для сопоставления по имени: нижний регистр, один пробел.
func NormalizeName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func parse(raws []rawIssue) *Snapshot {
	snap := &Snapshot{ByHRDB: map[string]*Employee{}, ByName: map[string]*Employee{}}
	cycleStart := map[string]time.Time{}

	get := func(name, key string) *Employee {
		if key != "" {
			if e := snap.ByHRDB[key]; e != nil {
				return e
			}
		}
		nn := NormalizeName(name)
		if key == "" && nn != "" {
			if e := snap.ByName[nn]; e != nil {
				return e
			}
		}
		e := &Employee{HRDBKey: key, Name: name, Ratings: map[string]Rating{}}
		if key != "" {
			snap.ByHRDB[key] = e
		}
		if nn != "" {
			if prev := snap.ByName[nn]; prev == nil || prev.HRDBKey == "" {
				snap.ByName[nn] = e
			}
		}
		return e
	}

	for _, r := range raws {
		name, key := employeeOf(r.Employee)
		if name == "" {
			name = employeeFromSummary(r.Summary)
		}
		typ := strings.ToLower(r.Type)
		switch {
		case typ == "personal development plan":
			pt := planTypeOf(r.PlanType, r.Summary)
			if pt == "" {
				continue
			}
			if name == "" {
				// «PIP: Performance Improvement Plan - Имя Фамилия»
				if i := strings.LastIndex(r.Summary, " - "); i >= 0 {
					name = strings.TrimSpace(r.Summary[i+3:])
				}
			}
			if name == "" && key == "" {
				continue
			}
			e := get(name, key)
			e.Plans = append(e.Plans, Plan{
				IssueKey: r.Key, URL: r.BaseURL + "/browse/" + r.Key, Type: pt,
				Status: r.Status, Active: !strings.EqualFold(r.Status, "Closed"), Created: r.Created,
			})
		default:
			prio, ok := ratingPriority[typ]
			if !ok {
				continue
			}
			code := gradeCode(r.Rate)
			if code == "" || (name == "" && key == "") {
				continue
			}
			cycle := cycleOf(r.Summary)
			if cycle == "" {
				continue
			}
			if st, seen := cycleStart[cycle]; !seen || (!r.Created.IsZero() && r.Created.Before(st)) {
				cycleStart[cycle] = r.Created
			}
			e := get(name, key)
			cur, exists := e.Ratings[cycle]
			if !exists || prio > cur.priority {
				e.Ratings[cycle] = Rating{
					Cycle: cycle, Grade: code, Text: strings.TrimSpace(r.Rate),
					IssueKey: r.Key, URL: r.BaseURL + "/browse/" + r.Key, Source: r.Type, priority: prio,
				}
			}
		}
	}
	for k, st := range cycleStart {
		snap.Cycles = append(snap.Cycles, Cycle{Key: k, Start: st})
	}
	sort.Slice(snap.Cycles, func(i, j int) bool {
		if snap.Cycles[i].Start.Equal(snap.Cycles[j].Start) {
			return snap.Cycles[i].Key < snap.Cycles[j].Key
		}
		return snap.Cycles[i].Start.Before(snap.Cycles[j].Start)
	})
	for _, e := range snap.ByHRDB {
		sort.Slice(e.Plans, func(i, j int) bool { return e.Plans[i].Created.After(e.Plans[j].Created) })
	}
	return snap
}

// ---------------------------------------------------------------------------
// Значения полей Jira
// ---------------------------------------------------------------------------

func str(v any) string {
	s, _ := v.(string)
	return s
}

func nested(v any, key string) string {
	if m, ok := v.(map[string]any); ok {
		return str(m[key])
	}
	return ""
}

// optionValue — значение select-поля ({"value": …}) или строка.
func optionValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s := str(t["value"]); s != "" {
			return s
		}
		return str(t["name"])
	case []any:
		if len(t) > 0 {
			return optionValue(t[0])
		}
	}
	return ""
}

// firstString — первая строка из строки/списка/объекта (поле Employee —
// список ссылок на объекты Assets в виде строк).
func firstString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			return firstString(t[0])
		}
	case map[string]any:
		for _, k := range []string{"label", "displayName", "name", "value"} {
			if s := str(t[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

func parseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
