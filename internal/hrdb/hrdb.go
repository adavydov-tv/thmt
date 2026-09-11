// Package hrdb читает список сотрудников из HRDB — Atlassian Assets
// (JSM Insight). Объектный тип «Employee» хранит отображаемое имя и рабочий
// e-mail; id атрибутов различаются между схемами, поэтому они находятся по
// имени атрибута с возможностью явного переопределения через ENV.
//
// Авторизация — Basic под теми же JIRA_EMAIL / JIRA_API_TOKEN, что и
// Jira-коллектор: Assets живёт в том же Atlassian-тенанте.
package hrdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Employee — сотрудник из HRDB. Key — objectKey Assets (стабильный id).
type Employee struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	// Office — офис сотрудника (Limassol, London, Remote work, …); по нему
	// выбирается праздничный календарь.
	Office string `json:"office,omitempty"`
	// Team — команда сотрудника: по ней запускается массовый сбор.
	Team string `json:"team,omitempty"`
	// Area — направление (Area of Responsibility): по нему сравнивается
	// продуктивность сотрудников одного профиля.
	Area string `json:"area,omitempty"`
	// HireDate — дата найма, как её отдал Assets (сырой строкой).
	HireDate string `json:"hire_date,omitempty"`
	// HybridDays — дни недели работы из дома («Wednesday, Friday»).
	HybridDays string `json:"hybrid_days,omitempty"`
	// WorkFormat — формат работы (Office | Hybrid | Remote), как в HRDB.
	WorkFormat string `json:"work_format,omitempty"`
	// Title — должность (атрибут «Title», напр. «Staff Backend Development»).
	Title string `json:"title,omitempty"`
	// Grade — грейд (атрибут «Grade», напр. «Staff», «Senior»).
	Grade string `json:"grade,omitempty"`
}

// HireTime разбирает дату найма. Assets отдаёт даты в нескольких форматах —
// пробуем по очереди; nil, если поле пустое или формат неизвестен.
func (e Employee) HireTime() *time.Time {
	s := strings.TrimSpace(e.HireDate)
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "02.01.2006", "02/Jan/06", "2006-01-02T15:04:05.000Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			return &d
		}
	}
	return nil
}

// Client — клиент HRDB с кэшем списка сотрудников в памяти.
type Client struct {
	cfg config.HRDBConfig
	cl  *httpx.Client

	mu       sync.Mutex
	cached   []Employee
	cachedAt time.Time
	attrs    *attrIDs

	// exCached — e-mail бывших сотрудников (объекты типа Ex-employee).
	exCached   map[string]bool
	exCachedAt time.Time

	// structCached — оргструктура: юниты типа Team с Entity Type
	// (Team | Cluster | Department) и родительскими связями.
	structCached   map[string]StructureUnit
	structCachedAt time.Time

	// fetchMu сериализует полные выкачки списка: HRDB выкачивается ~30 секунд,
	// и параллельные промахи кэша не должны устраивать шквал одинаковых обходов.
	fetchMu sync.Mutex

	// persist, если задан, хранит снапшоты кэшей между рестартами процесса.
	persist Persist
}

// Persist — долговременное хранилище снапшотов кэшей HRDB (см. SetPersist).
type Persist interface {
	Load(ctx context.Context, kind string) (payload []byte, at time.Time, err error)
	Save(ctx context.Context, kind string, payload []byte) error
}

// SetPersist подключает хранилище снапшотов и сразу загружает их в кэши с
// исходными временными метками: протухший снапшот отдаётся мгновенно, а
// свежесть догоняется фоновым обновлением — рестарт перестаёт давать
// минутное «холодное окно».
func (c *Client) SetPersist(ctx context.Context, p Persist) {
	c.mu.Lock()
	c.persist = p
	c.mu.Unlock()
	if payload, at, err := p.Load(ctx, "employees"); err == nil && payload != nil {
		var emps []Employee
		if json.Unmarshal(payload, &emps) == nil && len(emps) > 0 {
			c.mu.Lock()
			if c.cached == nil {
				c.cached, c.cachedAt = emps, at
			}
			c.mu.Unlock()
		}
	}
	if payload, at, err := p.Load(ctx, "structure"); err == nil && payload != nil {
		var st map[string]StructureUnit
		if json.Unmarshal(payload, &st) == nil && len(st) > 0 {
			c.mu.Lock()
			if c.structCached == nil {
				c.structCached, c.structCachedAt = st, at
			}
			c.mu.Unlock()
		}
	}
	if payload, at, err := p.Load(ctx, "ex"); err == nil && payload != nil {
		var ex map[string]bool
		if json.Unmarshal(payload, &ex) == nil && len(ex) > 0 {
			c.mu.Lock()
			if c.exCached == nil {
				c.exCached, c.exCachedAt = ex, at
			}
			c.mu.Unlock()
		}
	}
}

// snapshot сохраняет свежевыкачанный кэш в долговременное хранилище.
func (c *Client) snapshot(kind string, data any) {
	c.mu.Lock()
	p := c.persist
	c.mu.Unlock()
	if p == nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.Save(bg, kind, payload); err != nil {
			slog.Warn("не удалось сохранить снапшот кэша HRDB", "kind", kind, "err", err)
		}
	}()
}

// New создаёт клиент новой HRDB (Atlassian Cloud Assets).
// jiraEmail/jiraToken — учётные данные Atlassian.
func New(cfg config.HRDBConfig, jiraEmail, jiraToken string, timeout time.Duration, maxRetries int) (*Client, error) {
	if cfg.WorkspaceID == "" {
		return nil, fmt.Errorf("hrdb: не задан HRDB_WORKSPACE_ID")
	}
	if cfg.EmployeeTypeID == "" {
		return nil, fmt.Errorf("hrdb: не задан HRDB_EMPLOYEE_TYPE_ID")
	}
	cred := base64.StdEncoding.EncodeToString([]byte(jiraEmail + ":" + jiraToken))
	cl, err := httpx.New(httpx.Options{
		BaseURL:    "https://api.atlassian.com/jsm/assets/workspace/" + cfg.WorkspaceID + "/v1",
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers:    map[string]string{"Authorization": "Basic " + cred},
	})
	if err != nil {
		return nil, fmt.Errorf("hrdb: %w", err)
	}
	return &Client{cfg: cfg, cl: cl}, nil
}

// NewDC создаёт клиент старой HRDB — Insight на Jira Data Center
// (jira.xtools.tv). Схема и имена атрибутов там те же, различается транспорт:
// базовый URL, Bearer-авторизация и пагинация без hasMoreResults.
func NewDC(cfg config.HRDBConfig, timeout time.Duration, maxRetries int) (*Client, error) {
	if cfg.DCURL == "" || cfg.DCToken == "" {
		return nil, fmt.Errorf("hrdb: для старой HRDB нужны HRDB_DC_URL и HRDB_DC_TOKEN")
	}
	cl, err := httpx.New(httpx.Options{
		BaseURL:    strings.TrimRight(cfg.DCURL, "/") + "/rest/insight/1.0",
		Timeout:    timeout,
		MaxRetries: maxRetries,
		Headers:    map[string]string{"Authorization": "Bearer " + cfg.DCToken},
	})
	if err != nil {
		return nil, fmt.Errorf("hrdb dc: %w", err)
	}
	// Тот же код работает с обеими HRDB: подменяем только id типа Employee,
	// атрибуты дальше резолвятся по именам.
	cfg.EmployeeTypeID = cfg.DCEmployeeTypeID
	return &Client{cfg: cfg, cl: cl}, nil
}

// ---- разбор объектов Assets ----

type aqlPage struct {
	Values         []assetObject `json:"values"`
	ObjectEntries  []assetObject `json:"objectEntries"` // старые эндпоинты
	HasMoreResults bool          `json:"hasMoreResults"`
	// PageSize у Insight DC — общее число страниц (hasMoreResults там нет).
	PageSize int `json:"pageSize"`
}

// hasMore — есть ли страницы после page: cloud отвечает hasMoreResults,
// Insight DC вместо этого отдаёт общее число страниц в pageSize.
func (p aqlPage) hasMore(page int) bool {
	return p.HasMoreResults || page < p.PageSize
}

type assetObject struct {
	ObjectKey  string `json:"objectKey"`
	Label      string `json:"label"`
	Name       string `json:"name"`
	ObjectType struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	} `json:"objectType"`
	Attributes []assetAttr `json:"attributes"`
}

type assetAttr struct {
	ObjectTypeAttributeID any          `json:"objectTypeAttributeId"`
	Values                []assetValue `json:"objectAttributeValues"`
}

type assetValue struct {
	Value        any `json:"value"`
	DisplayValue any `json:"displayValue"`
}

type typeAttr struct {
	ID   any    `json:"id"`
	Name string `json:"name"`
}

func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// quoteList превращает список имён в аргумент AQL-оператора in: `"A", "B"`.
func quoteList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			quoted = append(quoted, `"`+n+`"`)
		}
	}
	return strings.Join(quoted, ", ")
}

// attrAll собирает все значения атрибута (для мультизначных полей вроде
// «Hybrid remote days»).
func (o assetObject) attrAll(attrID string) []string {
	for _, a := range o.Attributes {
		if asStr(a.ObjectTypeAttributeID) != attrID {
			continue
		}
		var out []string
		for _, v := range a.Values {
			s := asStr(v.Value)
			if s == "" {
				s = asStr(v.DisplayValue)
			}
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (o assetObject) attr(attrID string) string {
	for _, a := range o.Attributes {
		if asStr(a.ObjectTypeAttributeID) != attrID {
			continue
		}
		for _, v := range a.Values {
			if s := asStr(v.Value); s != "" {
				return s
			}
			if s := asStr(v.DisplayValue); s != "" {
				return s
			}
		}
		return ""
	}
	return ""
}

var (
	emailAttrRe  = regexp.MustCompile(`(?i)mail`)
	nameAttrRe   = regexp.MustCompile(`(?i)name`)
	officeAttrRe = regexp.MustCompile(`(?i)office|location|city`)
	teamAttrRe   = regexp.MustCompile(`(?i)team|squad`)
	areaAttrRe   = regexp.MustCompile(`(?i)area|responsibilit`)
	hireAttrRe   = regexp.MustCompile(`(?i)hire|employment date|start date`)
)

// attrIDs — id атрибутов типа Employee. Обязателен только e-mail; остальные
// атрибуты необязательные: без них соответствующие поля остаются пустыми.
type attrIDs struct {
	name, email, office, team, area, hire, hybrid, workFormat, title, grade string
}

// resolveAttrs находит id атрибутов на типе Employee: сначала точное имя,
// затем первое нечёткое совпадение. Результат липкий на время жизни
// процесса — схему HRDB не меняют между запросами.
func (c *Client) resolveAttrs(ctx context.Context) (attrIDs, error) {
	c.mu.Lock()
	cached := c.attrs
	c.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}

	var attrs []typeAttr
	if _, err := c.cl.GetJSON(ctx, "/objecttype/"+url.PathEscape(c.cfg.EmployeeTypeID)+"/attributes", nil, &attrs); err != nil {
		return attrIDs{}, fmt.Errorf("hrdb: атрибуты типа Employee: %w", err)
	}
	pick := func(override, exact string, fuzzy *regexp.Regexp) string {
		if override != "" {
			return override
		}
		for _, a := range attrs {
			if strings.EqualFold(strings.TrimSpace(a.Name), exact) {
				return asStr(a.ID)
			}
		}
		for _, a := range attrs {
			if fuzzy.MatchString(a.Name) {
				return asStr(a.ID)
			}
		}
		return ""
	}
	ids := attrIDs{
		name:       pick(c.cfg.NameAttrID, "display name", nameAttrRe),
		email:      pick(c.cfg.EmailAttrID, "work email", emailAttrRe),
		office:     pick(c.cfg.OfficeAttrID, "office", officeAttrRe),
		team:       pick(c.cfg.TeamAttrID, "team", teamAttrRe),
		area:       pick(c.cfg.AreaAttrID, "area of responsibility", areaAttrRe),
		hire:       pick(c.cfg.HireAttrID, "hire date", hireAttrRe),
		hybrid:     pick("", "hybrid remote days", regexp.MustCompile(`(?i)hybrid`)),
		workFormat: pick("", "work format", regexp.MustCompile(`(?i)work.?format|формат.?работы`)),
		// «Title» — должность (напр. «Staff Backend Development»); имя атрибута
		// сверяем точно, чтобы не поймать другие поля со словом title.
		title: pick("", "title", regexp.MustCompile(`(?i)^title$|job.?title|должность`)),
		grade: pick("", "grade", regexp.MustCompile(`(?i)^grade$|грейд`)),
	}
	if ids.email == "" {
		return attrIDs{}, fmt.Errorf("hrdb: на типе Employee не найден атрибут e-mail — задайте HRDB_EMPLOYEE_EMAIL_ATTR")
	}
	c.mu.Lock()
	c.attrs = &ids
	c.mu.Unlock()
	return ids, nil
}

// isPlaceholderEmail отбрасывает адреса-заглушки (Vacant/системные учётки).
func isPlaceholderEmail(email string) bool {
	lc := strings.ToLower(email)
	return lc == "" || strings.Contains(lc, "noreply") || strings.Contains(lc, "no-reply")
}

// ListEmployees возвращает сотрудников из HRDB, кэш — на CacheTTL.
func (c *Client) ListEmployees(ctx context.Context) ([]Employee, error) {
	if out, ok := c.fromCache(); ok {
		return out, nil
	}
	// Протухший кэш отдаём сразу, свежий строим в фоне: полная выкачка идёт
	// десятки секунд, и блокировать ею страницу нельзя — иначе каждые
	// CacheTTL кто-то из пользователей ловил бы минутное зависание.
	if stale := c.staleCache(); stale != nil {
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			c.fetchMu.Lock()
			defer c.fetchMu.Unlock()
			if _, ok := c.fromCache(); ok {
				return
			}
			if _, err := c.fetch(bg); err != nil {
				slog.Warn("фоновое обновление списка сотрудников HRDB не удалось", "err", err)
			}
		}()
		return stale, nil
	}
	// Кэша нет вовсе (первый запрос до прогрева) — выкачка блокирующая;
	// параллельные не пускаем: кто дождался чужой, забирает готовый кэш.
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	if out, ok := c.fromCache(); ok {
		return out, nil
	}
	return c.fetch(ctx)
}

func (c *Client) fromCache() ([]Employee, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cfg.CacheTTL {
		return c.cached, true
	}
	return nil, false
}

// staleCache — кэш любой свежести (nil, если выкачки ещё не было).
func (c *Client) staleCache() []Employee {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached
}

// HasData — есть ли у клиента хоть какие-то данные (кэш или снапшот из БД).
func (c *Client) HasData() bool {
	return c.staleCache() != nil
}

// fetch выкачивает список сотрудников (штатные + контракторы) в кэш.
func (c *Client) fetch(ctx context.Context) ([]Employee, error) {
	ids, err := c.resolveAttrs(ctx)
	if err != nil {
		return nil, err
	}

	var out []Employee
	pageSize := c.cfg.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	// Предохранитель от бесконечной пагинации: 500 страниц по 100 — 50 000
	// сотрудников, больше в одной HRDB не бывает.
	for page := 1; page <= 500; page++ {
		q := url.Values{
			"qlQuery":           {"objectType in (" + quoteList(c.cfg.EmployeeTypes) + ")"},
			"page":              {fmt.Sprint(page)},
			"resultsPerPage":    {fmt.Sprint(pageSize)},
			"resultPerPage":     {fmt.Sprint(pageSize)},
			"includeAttributes": {"true"},
		}
		var res aqlPage
		if _, err := c.cl.GetJSON(ctx, "/aql/objects", q, &res); err != nil {
			return nil, fmt.Errorf("hrdb: страница %d: %w", page, err)
		}
		items := res.Values
		if len(items) == 0 {
			items = res.ObjectEntries
		}
		for _, obj := range items {
			email := strings.ToLower(strings.TrimSpace(obj.attr(ids.email)))
			if isPlaceholderEmail(email) {
				continue
			}
			name := strings.TrimSpace(obj.attr(ids.name))
			if name == "" {
				name = obj.Label
			}
			if name == "" {
				name = obj.Name
			}
			optional := func(attrID string) string {
				if attrID == "" {
					return ""
				}
				return strings.TrimSpace(obj.attr(attrID))
			}
			out = append(out, Employee{
				Key:         obj.ObjectKey,
				DisplayName: name,
				Email:       email,
				Office:      optional(ids.office),
				Team:        optional(ids.team),
				Area:        optional(ids.area),
				HireDate:    optional(ids.hire),
				HybridDays:  strings.Join(obj.attrAll(ids.hybrid), ", "),
				WorkFormat:  optional(ids.workFormat),
				Title:       optional(ids.title),
				Grade:       optional(ids.grade),
			})
		}
		if !res.hasMore(page) {
			break
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })

	c.mu.Lock()
	c.cached, c.cachedAt = out, time.Now()
	c.mu.Unlock()
	c.snapshot("employees", out)
	return out, nil
}

// WarmUp — фоновый прогрев кэша: список сотрудников выкачивается сразу при
// старте и обновляется незадолго до истечения TTL, так что пользователь
// никогда не ждёт полной загрузки HRDB (~30 секунд на холодную). Блокирует —
// запускать в отдельной горутине; останавливается по отмене контекста.
func (c *Client) WarmUp(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	for {
		started := time.Now()
		employees, err := c.ListEmployees(ctx)
		if err == nil {
			// Список бывших и оргструктура греются тем же циклом: они нужны
			// сравнению и отклонениям, а собирать их в пути запроса долго.
			if _, exErr := c.refreshExEmails(ctx); exErr != nil && ctx.Err() == nil {
				log.Warn("прогрев списка бывших сотрудников не удался", "err", exErr)
			}
			if _, stErr := c.Structure(ctx); stErr != nil && ctx.Err() == nil {
				log.Warn("прогрев оргструктуры не удался", "err", stErr)
			}
		}

		// Обновляемся за минуту до протухания; после ошибки — через минуту.
		next := c.cfg.CacheTTL - time.Minute
		if next < time.Minute {
			next = time.Minute
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("прогрев кэша HRDB не удался", "err", err)
			next = time.Minute
		} else if time.Since(started) > time.Second {
			// Логируем только реальные выкачки, попадания в кэш не шумят.
			log.Info("кэш HRDB прогрет", "employees", len(employees),
				"took", time.Since(started).Round(time.Second).String())
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// ExEmployeeEmails возвращает e-mail бывших сотрудников из кэша, НЕ блокируя
// запрос: объектов типа Ex-employee тысячи, полная выкачка занимает десятки
// секунд, и делать её в пути HTTP-запроса нельзя — страница сравнения висла
// до таймаута браузера. Протухший кэш отдаётся как есть, свежий строится
// фоном (WarmUp обновляет его вместе со списком сотрудников). Пустая карта —
// кэш ещё не готов, фильтр в этот раз просто не применяется.
func (c *Client) ExEmployeeEmails(_ context.Context) (map[string]bool, error) {
	c.mu.Lock()
	cached, at := c.exCached, c.exCachedAt
	c.mu.Unlock()
	if cached != nil && time.Since(at) < c.cfg.CacheTTL {
		return cached, nil
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := c.refreshExEmails(bg); err != nil {
			slog.Warn("обновление списка бывших сотрудников не удалось", "err", err)
		}
	}()
	if cached != nil {
		return cached, nil
	}
	return map[string]bool{}, nil
}

// refreshExEmails выкачивает e-mail всех объектов типа Ex-employee и кладёт
// их в кэш. Параллельные выкачки сериализуются, как и у списка сотрудников.
func (c *Client) refreshExEmails(ctx context.Context) (map[string]bool, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	c.mu.Lock()
	if c.exCached != nil && time.Since(c.exCachedAt) < c.cfg.CacheTTL {
		out := c.exCached
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	ids, err := c.resolveAttrs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	pageSize := c.cfg.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	for page := 1; page <= 500; page++ {
		q := url.Values{
			"qlQuery":           {`objectType in (` + quoteList(c.cfg.ExEmployeeTypes) + `)`},
			"page":              {fmt.Sprint(page)},
			"resultsPerPage":    {fmt.Sprint(pageSize)},
			"resultPerPage":     {fmt.Sprint(pageSize)},
			"includeAttributes": {"true"},
		}
		var res aqlPage
		if _, err := c.cl.GetJSON(ctx, "/aql/objects", q, &res); err != nil {
			return nil, fmt.Errorf("hrdb: ex-employees, страница %d: %w", page, err)
		}
		items := res.Values
		if len(items) == 0 {
			items = res.ObjectEntries
		}
		for _, obj := range items {
			if email := strings.ToLower(strings.TrimSpace(obj.attr(ids.email))); email != "" {
				out[email] = true
			}
		}
		if !res.hasMore(page) {
			break
		}
	}

	c.mu.Lock()
	c.exCached, c.exCachedAt = out, time.Now()
	c.mu.Unlock()
	c.snapshot("ex", out)
	return out, nil
}

// StructureUnit — юнит оргструктуры (объект типа Team в Assets): команда,
// кластер или департамент; вид различается атрибутом Entity Type.
type StructureUnit struct {
	Name       string `json:"name"`
	EntityType string `json:"entity_type"` // Team | Cluster | Department | …
	Parent     string `json:"parent"`      // Parent team
	Department string `json:"department"`  // прямой атрибут Department, если задан
	// Lead — руководитель юнита (атрибут Team lead): по нему строится
	// HRDB-скоуп роли lead в дашборде.
	Lead string `json:"lead,omitempty"`
}

// Structure возвращает оргструктуру: имя юнита (в нижнем регистре) → юнит.
// Кэш — на CacheTTL.
func (c *Client) Structure(ctx context.Context) (map[string]StructureUnit, error) {
	c.mu.Lock()
	fresh := c.structCached != nil && time.Since(c.structCachedAt) < c.cfg.CacheTTL
	stale := c.structCached
	c.mu.Unlock()
	if fresh {
		return stale, nil
	}
	// Протухшую структуру отдаём сразу, обновляем в фоне — как и сотрудников:
	// страница не должна ждать выкачку.
	if stale != nil {
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if _, err := c.refreshStructure(bg); err != nil {
				slog.Warn("фоновое обновление оргструктуры HRDB не удалось", "err", err)
			}
		}()
		return stale, nil
	}
	return c.refreshStructure(ctx)
}

// refreshStructure выкачивает оргструктуру в кэш (сериализовано с другими
// полными выкачками).
func (c *Client) refreshStructure(ctx context.Context) (map[string]StructureUnit, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	c.mu.Lock()
	if c.structCached != nil && time.Since(c.structCachedAt) < c.cfg.CacheTTL {
		out := c.structCached
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	// Id атрибутов типа Team резолвим по именам единожды.
	var typeAttrIDs struct{ name, entity, parent, department, lead string }
	// Тип юнитов ищем по первому объекту с Entity Type.
	var probe aqlPage
	if _, err := c.cl.GetJSON(ctx, "/aql/objects", url.Values{
		"qlQuery": {`objectType = "Team"`}, "resultsPerPage": {"1"},
	}, &probe); err != nil {
		return nil, fmt.Errorf("hrdb: структура: %w", err)
	}
	items := probe.Values
	if len(items) == 0 {
		items = probe.ObjectEntries
	}
	if len(items) == 0 {
		return map[string]StructureUnit{}, nil
	}
	var attrs []typeAttr
	if _, err := c.cl.GetJSON(ctx,
		"/objecttype/"+url.PathEscape(asStr(items[0].ObjectType.ID))+"/attributes", nil, &attrs); err != nil {
		return nil, fmt.Errorf("hrdb: атрибуты типа Team: %w", err)
	}
	for _, a := range attrs {
		switch strings.ToLower(strings.TrimSpace(a.Name)) {
		case "name":
			typeAttrIDs.name = asStr(a.ID)
		case "entity type":
			typeAttrIDs.entity = asStr(a.ID)
		case "parent team":
			typeAttrIDs.parent = asStr(a.ID)
		case "department":
			typeAttrIDs.department = asStr(a.ID)
		case "team lead":
			typeAttrIDs.lead = asStr(a.ID)
		}
	}

	out := map[string]StructureUnit{}
	for page := 1; page <= 200; page++ {
		q := url.Values{
			"qlQuery":           {`objectType = "Team"`},
			"page":              {fmt.Sprint(page)},
			"resultsPerPage":    {"100"},
			"resultPerPage":     {"100"},
			"includeAttributes": {"true"},
		}
		var res aqlPage
		if _, err := c.cl.GetJSON(ctx, "/aql/objects", q, &res); err != nil {
			return nil, fmt.Errorf("hrdb: структура, страница %d: %w", page, err)
		}
		items := res.Values
		if len(items) == 0 {
			items = res.ObjectEntries
		}
		for _, obj := range items {
			u := StructureUnit{
				Name:       strings.TrimSpace(obj.attr(typeAttrIDs.name)),
				EntityType: strings.TrimSpace(obj.attr(typeAttrIDs.entity)),
				Parent:     strings.TrimSpace(obj.attr(typeAttrIDs.parent)),
				Department: strings.TrimSpace(obj.attr(typeAttrIDs.department)),
				Lead:       strings.TrimSpace(obj.attr(typeAttrIDs.lead)),
			}
			if u.Name == "" {
				u.Name = obj.Label
			}
			if u.Name != "" {
				out[strings.ToLower(u.Name)] = u
			}
		}
		if !res.hasMore(page) {
			break
		}
	}

	c.mu.Lock()
	c.structCached, c.structCachedAt = out, time.Now()
	c.mu.Unlock()
	c.snapshot("structure", out)
	return out, nil
}

// TeamChainInfo — положение команды в оргструктуре.
type TeamChainInfo struct {
	// Chain — путь снизу вверх: команда → … → вершина.
	Chain []string `json:"chain"`
	// Unit, Cluster и Department — ближайшие юниты соответствующих Entity
	// Type (полная иерархия: департамент → кластер → юнит → команда).
	Unit       string `json:"unit"`
	Cluster    string `json:"cluster"`
	Department string `json:"department"`
}

// TeamChain строит иерархическую цепочку команды по оргструктуре.
func TeamChain(structure map[string]StructureUnit, team string) TeamChainInfo {
	info := TeamChainInfo{}
	name := strings.TrimSpace(team)
	seen := map[string]bool{}
	for i := 0; i < 10 && name != "" && !seen[strings.ToLower(name)]; i++ {
		seen[strings.ToLower(name)] = true
		info.Chain = append(info.Chain, name)
		u, ok := structure[strings.ToLower(name)]
		if !ok {
			break
		}
		switch strings.ToLower(u.EntityType) {
		case "unit":
			if info.Unit == "" {
				info.Unit = u.Name
			}
		case "cluster":
			if info.Cluster == "" {
				info.Cluster = u.Name
			}
		case "department":
			if info.Department == "" {
				info.Department = u.Name
			}
		}
		if info.Department == "" && u.Department != "" {
			info.Department = u.Department
		}
		name = u.Parent
	}
	return info
}

// Team — команда из HRDB с числом сотрудников.
type Team struct {
	Name    string `json:"name"`
	Members int    `json:"members"`
}

// Teams возвращает список команд и направлений (Area of Responsibility),
// встречающихся у сотрудников.
func (c *Client) Teams(ctx context.Context) ([]Team, []string, error) {
	all, err := c.ListEmployees(ctx)
	if err != nil {
		return nil, nil, err
	}
	teamCount := map[string]int{}
	areaSet := map[string]bool{}
	for _, e := range all {
		if e.Team != "" {
			teamCount[e.Team]++
		}
		if e.Area != "" {
			areaSet[e.Area] = true
		}
	}
	teams := make([]Team, 0, len(teamCount))
	for name, n := range teamCount {
		teams = append(teams, Team{Name: name, Members: n})
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })
	areas := make([]string, 0, len(areaSet))
	for a := range areaSet {
		areas = append(areas, a)
	}
	sort.Strings(areas)
	return teams, areas, nil
}

// TeamMembers возвращает сотрудников команды (совпадение без учёта регистра).
func (c *Client) TeamMembers(ctx context.Context, team string) ([]Employee, error) {
	all, err := c.ListEmployees(ctx)
	if err != nil {
		return nil, err
	}
	var out []Employee
	for _, e := range all {
		if strings.EqualFold(strings.TrimSpace(e.Team), strings.TrimSpace(team)) {
			out = append(out, e)
		}
	}
	return out, nil
}

// FindByEmail возвращает сотрудника по точному e-mail (без учёта регистра).
func (c *Client) FindByEmail(ctx context.Context, email string) (Employee, bool, error) {
	all, err := c.ListEmployees(ctx)
	if err != nil {
		return Employee{}, false, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, e := range all {
		if e.Email == email {
			return e, true, nil
		}
	}
	return Employee{}, false, nil
}

// HybridHistory возвращает историю изменений атрибута «Hybrid remote days»
// сотрудника: когда и с какого на какой шаблон менялись дни из дома.
func (c *Client) HybridHistory(ctx context.Context, objectKey string) ([]models.HybridChange, error) {
	return c.attrHistory(ctx, objectKey, "hybrid remote days")
}

// WorkFormatHistory — история изменений атрибута «Work format»
// (Office | Hybrid | Remote).
func (c *Client) WorkFormatHistory(ctx context.Context, objectKey string) ([]models.HybridChange, error) {
	return c.attrHistory(ctx, objectKey, "work format")
}

// attrHistory возвращает изменения атрибута из журнала объекта Assets по
// objectKey (HRDB-1234); числовой id объекта совпадает с суффиксом objectKey
// (в каждом инстансе — свой). Работает и в Cloud (/jsm/assets/.../object/{id}/
// history), и в DC (/rest/insight/1.0/object/{id}/history) — путь одинаковый.
// Имя атрибута сверяется ТОЧНО: в DC живут атрибуты-дубли миграции
// («Hybrid remote days1») с мусорными правками — подстрочный матч их ловил.
func (c *Client) attrHistory(ctx context.Context, objectKey, attrName string) ([]models.HybridChange, error) {
	id := objectKey
	if i := strings.LastIndex(objectKey, "-"); i >= 0 {
		id = objectKey[i+1:]
	}
	var entries []struct {
		Created           string `json:"created"`
		AffectedAttribute string `json:"affectedAttribute"`
		OldValue          string `json:"oldValue"`
		NewValue          string `json:"newValue"`
	}
	if _, err := c.cl.GetJSON(ctx, "/object/"+url.PathEscape(id)+"/history", nil, &entries); err != nil {
		return nil, fmt.Errorf("hrdb: история объекта %s: %w", objectKey, err)
	}
	var out []models.HybridChange
	for _, e := range entries {
		if !strings.EqualFold(strings.TrimSpace(e.AffectedAttribute), attrName) {
			continue
		}
		// Очистки значения («Wednesday» → «») — не смена шаблона: они приходят
		// вместе со сменой формата на Office/Remote, а там шаблон и так не
		// действует (гейт по формату); в DC встречаются и мусорные очистки.
		if strings.TrimSpace(e.NewValue) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Created)
		if err != nil {
			continue
		}
		out = append(out, models.HybridChange{At: t, Old: e.OldValue, New: e.NewValue})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// Search фильтрует сотрудников по подстроке имени или e-mail.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Employee, error) {
	all, err := c.ListEmployees(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 50
	}
	out := make([]Employee, 0, limit)
	for _, e := range all {
		if query == "" ||
			strings.Contains(strings.ToLower(e.DisplayName), query) ||
			strings.Contains(e.Email, query) {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
