// Package gwork собирает аудит Google Workspace через Admin SDK Reports API:
// реальное присутствие на встречах Meet, просмотры/скачивания Drive, логины
// (с определением офис/вне офиса по IP), счётчики отправленных писем Gmail и
// активность устройств.
//
// Требует АДМИН-доступа: сервис-аккаунт с domain-wide delegation на
// reports-скоупы (admin.reports.audit.readonly, admin.reports.usage.readonly)
// либо OAuth-токен администратора. Скоупы отдельные от gdocs/gcal, поэтому
// источник выключен по умолчанию и включается после выдачи нового токена.
//
// Reports API отдаёт активность ПО ДНЯМ и хранит её ~6 месяцев — годовой
// бэкфилл невозможен, данные копятся с момента включения.
package gwork

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gdocs"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Scopes — права Reports API: аудит-события и usage-отчёты (только чтение).
var Scopes = []string{
	"https://www.googleapis.com/auth/admin.reports.audit.readonly",
	"https://www.googleapis.com/auth/admin.reports.usage.readonly",
}

const (
	activitiesURL = "https://admin.googleapis.com/admin/reports/v1/activity/users/%s/applications/%s"
	userUsageURL  = "https://admin.googleapis.com/admin/reports/v1/usage/users/%s/dates/%s"
)

// Collector — аудит Workspace одного человека.
type Collector struct {
	cfg    config.GWorkConfig
	log    *slog.Logger
	client *http.Client
	office []*net.IPNet // офисные подсети
}

// New создаёт коллектор. Требуются те же учётные данные Google, что у gdocs,
// но с reports-скоупами (см. Scopes) и админ-доступом.
func New(ctx context.Context, gcfg config.GoogleConfig, cfg config.GWorkConfig, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.AuthMode != "" {
		gcfg.Mode = cfg.AuthMode
	}
	client, err := gdocs.NewHTTPClientWithScopes(ctx, gcfg, Scopes)
	if err != nil {
		return nil, fmt.Errorf("gwork: %w", err)
	}
	c := &Collector{cfg: cfg, log: log.With("collector", "gwork"), client: client}
	for _, raw := range cfg.OfficeCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ipnet, perr := net.ParseCIDR(raw); perr == nil {
			c.office = append(c.office, ipnet)
		} else if ip := net.ParseIP(raw); ip != nil {
			// одиночный IP → /32 или /128
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			_, ipnet, _ := net.ParseCIDR(fmt.Sprintf("%s/%d", raw, bits))
			c.office = append(c.office, ipnet)
		} else {
			c.log.Warn("gwork: не удалось разобрать офисную подсеть", "cidr", raw)
		}
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceGWork }

// fromOffice — попал ли IP в одну из офисных подсетей.
func (c *Collector) fromOffice(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	for _, n := range c.office {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---- разбор ответа activities.list ----

type activitiesResp struct {
	Items []struct {
		ID struct {
			Time            string `json:"time"`
			ApplicationName string `json:"applicationName"`
		} `json:"id"`
		Actor struct {
			Email string `json:"email"`
		} `json:"actor"`
		IPAddress string `json:"ipAddress"`
		Events    []struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			Parameters []struct {
				Name       string   `json:"name"`
				Value      string   `json:"value"`
				IntValue   string   `json:"intValue"`
				BoolValue  bool     `json:"boolValue"`
				MultiValue []string `json:"multiValue"`
			} `json:"parameters"`
		} `json:"events"`
	} `json:"items"`
	NextPageToken string `json:"nextPageToken"`
}

func paramStr(params []struct {
	Name       string   `json:"name"`
	Value      string   `json:"value"`
	IntValue   string   `json:"intValue"`
	BoolValue  bool     `json:"boolValue"`
	MultiValue []string `json:"multiValue"`
}, name string) string {
	for _, p := range params {
		if p.Name == name {
			if p.Value != "" {
				return p.Value
			}
			return p.IntValue
		}
	}
	return ""
}

// Collect собирает аудит-активность человека за период.
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result
	email := strings.TrimSpace(req.Person.GoogleEmail)
	if email == "" {
		email = strings.TrimSpace(req.Person.Email)
	}
	if email == "" {
		return res, fmt.Errorf("gwork: у %q нет email — некого запрашивать", req.Person.Key)
	}
	from, to := req.From.UTC(), req.To.UTC()

	var events []models.Event
	add := func(evs ...models.Event) { events = append(events, evs...) }

	if c.cfg.Meet {
		evs, err := c.collectApp(ctx, email, "meet", from, to, req.Person.Key, c.meetEvent)
		if err != nil {
			return res, err
		}
		add(evs...)
	}
	if c.cfg.Drive {
		evs, err := c.collectApp(ctx, email, "drive", from, to, req.Person.Key, c.driveEvent)
		if err != nil {
			return res, err
		}
		add(evs...)
	}
	if c.cfg.Calendar {
		evs, err := c.collectApp(ctx, email, "calendar", from, to, req.Person.Key, c.calendarEvent)
		if err != nil {
			return res, err
		}
		add(evs...)
	}
	if c.cfg.Login {
		evs, err := c.collectApp(ctx, email, "login", from, to, req.Person.Key, c.loginEvent)
		if err != nil {
			return res, err
		}
		c.annotateLoginDevices(ctx, email, evs)
		add(evs...)
	}
	if c.cfg.Gmail {
		evs, err := c.collectUsage(ctx, email, from, to, req.Person.Key)
		if err != nil {
			c.log.Warn("gwork: usage-отчёт недоступен", "err", err)
		} else {
			add(evs...)
		}
	}
	if c.cfg.Device {
		evs, err := c.collectApp(ctx, email, "mobile", from, to, req.Person.Key, c.mobileEvent)
		if err != nil {
			// mobile-аудит может быть недоступен (нет MDM) — не валим весь сбор.
			c.log.Warn("gwork: mobile-аудит недоступен", "err", err)
		} else {
			seen := map[string]bool{}
			for _, ev := range evs {
				if !seen[ev.ExternalID] {
					seen[ev.ExternalID] = true
					add(ev)
				}
			}
		}
	}

	res.Events = events
	res.Note = fmt.Sprintf("Google Workspace audit: %d событий (Reports API хранит ~6 мес.)", len(events))
	return res, nil
}

// collectApp обходит activities.list одного applicationName с пагинацией.
func (c *Collector) collectApp(ctx context.Context, email, app string, from, to time.Time,
	personKey string, build func(item activityItem, personKey string) *models.Event) ([]models.Event, error) {

	var out []models.Event
	pageToken := ""
	pageSize := c.cfg.PageSize
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 1000
	}
	for page := 0; page < 200; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		q := url.Values{
			"startTime":  {from.Format(time.RFC3339)},
			"endTime":    {to.Format(time.RFC3339)},
			"maxResults": {strconv.Itoa(pageSize)},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u := fmt.Sprintf(activitiesURL, url.PathEscape(email), app) + "?" + q.Encode()
		var resp activitiesResp
		if err := c.getJSON(ctx, u, &resp); err != nil {
			return out, fmt.Errorf("gwork %s: %w", app, err)
		}
		for _, it := range resp.Items {
			// Сверка актора: activity/users/{email} и так фильтрует по человеку,
			// но полагаться только на URL нельзя — item с чужим/пустым актором
			// (события анонимных участников у организатора Meet и т.п.) отбрасываем.
			if a := strings.TrimSpace(it.Actor.Email); a != "" && !strings.EqualFold(a, email) {
				continue
			}
			ai := activityItem{time: it.ID.Time, ip: it.IPAddress}
			for _, e := range it.Events {
				ai.name = e.Name
				ai.params = e.Parameters
				if ev := build(ai, personKey); ev != nil {
					out = append(out, *ev)
				}
			}
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return out, nil
}

// activityItem — плоское представление одного события Reports API.
type activityItem struct {
	time   string
	ip     string
	name   string
	params []struct {
		Name       string   `json:"name"`
		Value      string   `json:"value"`
		IntValue   string   `json:"intValue"`
		BoolValue  bool     `json:"boolValue"`
		MultiValue []string `json:"multiValue"`
	}
}

func parseAuditTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// meetEvent — присутствие на звонке: событие call_ended с длительностью.
func (c *Collector) meetEvent(it activityItem, personKey string) *models.Event {
	if it.name != "call_ended" {
		return nil
	}
	durSec, _ := strconv.Atoi(paramStr(it.params, "duration_seconds"))
	confID := paramStr(it.params, "conference_id")
	ts := parseAuditTime(it.time)
	ev := models.Event{
		PersonKey:  personKey,
		Source:     models.SourceGWork,
		Type:       models.TypeMeetAttended,
		ExternalID: "meet:" + confID + ":" + it.time,
		OccurredAt: ts,
		Title:      fmt.Sprintf("Присутствие на встрече · %d мин", durSec/60),
		Effort:     float64(durSec) / 60,
		EffortUnit: "мин",
		Meta: map[string]any{
			"conference_id":    confID,
			"duration_seconds": durSec,
			"identifier":       paramStr(it.params, "identifier"),
			// Для сверки с календарём: id события серии (без суффикса
			// инстанса), код встречи и unix-время входа в конференцию.
			"calendar_event_id": paramStr(it.params, "calendar_event_id"),
			"meeting_code":      paramStr(it.params, "meeting_code"),
			"start_ts":          atoi(paramStr(it.params, "start_timestamp_seconds")),
		},
	}
	ev.Normalize()
	return &ev
}

// calendarActions — авторские действия с календарём (создание/правка/удаление
// встреч, управление гостями). change_event_guest_response (ответ на приглашение)
// и notification_triggered (система) НЕ берём — участие покрывает gcal.
var calendarActions = map[string]bool{
	"create_event":             true,
	"delete_event":             true,
	"change_event":             true,
	"change_event_title":       true,
	"change_event_start_time":  true,
	"change_event_end_time":    true,
	"change_event_description": true,
	"change_event_location":    true,
	"add_event_guest":          true,
	"remove_event_guest":       true,
}

// calendarEvent — действие с календарём из аудита воркспейса.
func (c *Collector) calendarEvent(it activityItem, personKey string) *models.Event {
	if !calendarActions[it.name] {
		return nil
	}
	eventID := paramStr(it.params, "event_id")
	title := paramStr(it.params, "event_title")
	ts := parseAuditTime(it.time)
	ev := models.Event{
		PersonKey:  personKey,
		Source:     models.SourceGWork,
		Type:       models.TypeCalendarEdit,
		ExternalID: "cal-edit:" + it.name + ":" + eventID + ":" + it.time,
		OccurredAt: ts,
		Title:      strings.TrimSpace(calendarActionLabel(it.name) + ": " + title),
		Meta: map[string]any{
			"action":      it.name,
			"event_id":    eventID,
			"event_title": title,
			"calendar_id": paramStr(it.params, "calendar_id"),
			"recurring":   paramStr(it.params, "recurring") == "yes",
		},
	}
	ev.Normalize()
	return &ev
}

func calendarActionLabel(name string) string {
	switch name {
	case "create_event":
		return "Создана встреча"
	case "delete_event":
		return "Удалена встреча"
	case "add_event_guest", "remove_event_guest":
		return "Изменён состав встречи"
	default:
		return "Изменена встреча"
	}
}

// driveEvent — действие с документом из Drive-аудита воркспейса: просмотр,
// скачивание или правка/создание (edit/create — авторская активность).
func (c *Collector) driveEvent(it activityItem, personKey string) *models.Event {
	var typ models.EventType
	switch it.name {
	case "view":
		typ = models.TypeDriveView
	case "download":
		typ = models.TypeDriveDownload
	case "edit", "create":
		typ = models.TypeDriveEdit
	default:
		return nil
	}
	docID := paramStr(it.params, "doc_id")
	docTitle := paramStr(it.params, "doc_title")
	ts := parseAuditTime(it.time)
	ev := models.Event{
		PersonKey:  personKey,
		Source:     models.SourceGWork,
		Type:       typ,
		ExternalID: string(typ) + ":" + docID + ":" + it.time,
		OccurredAt: ts,
		Title:      strings.TrimSpace(typ.Label() + ": " + docTitle),
		Project:    docID,
		Meta: map[string]any{
			"doc_id":    docID,
			"doc_title": docTitle,
			"doc_type":  paramStr(it.params, "doc_type"),
			"action":    it.name,
		},
	}
	ev.Normalize()
	return &ev
}

// loginEvent — вход в аккаунт с пометкой офис/вне офиса.
// interactiveLoginTypes — типы входа, за которыми стоит осознанное действие
// человека. Остальные — фон: reauth (тихая пере-выдача сессии браузером,
// ~98% всех login_success по факту), exchange/app_password (поллинг почтовых
// клиентов). Пустой login_type пропускаем как неопределённый.
var interactiveLoginTypes = map[string]bool{
	"google_password": true,
	"google_otp":      true,
	"saml":            true,
	"":                true,
}

func (c *Collector) loginEvent(it activityItem, personKey string) *models.Event {
	if it.name != "login_success" {
		return nil
	}
	if !interactiveLoginTypes[paramStr(it.params, "login_type")] {
		return nil
	}
	ts := parseAuditTime(it.time)
	office := c.fromOffice(it.ip)
	where := "вне офиса"
	if office {
		where = "из офиса"
	}
	ev := models.Event{
		PersonKey:  personKey,
		Source:     models.SourceGWork,
		Type:       models.TypeLogin,
		ExternalID: "login:" + personKey + ":" + it.time,
		OccurredAt: ts,
		Title:      "Вход в аккаунт · " + where,
		Meta: map[string]any{
			"ip":          it.ip,
			"from_office": office,
			"login_type":  paramStr(it.params, "login_type"),
		},
	}
	ev.Normalize()
	return &ev
}

// mobileEvent — синхронизация мобильного устройства (DEVICE_SYNC_EVENT из
// mobile-аудита). ExternalID суточный: все синки за день схлопываются в одно
// событие «в этот день устройство было активно» (дедуп в Collect + upsert по id).
func (c *Collector) mobileEvent(it activityItem, personKey string) *models.Event {
	if it.name != "DEVICE_SYNC_EVENT" {
		return nil
	}
	ts := parseAuditTime(it.time)
	ev := models.Event{
		PersonKey:  personKey,
		Source:     models.SourceGWork,
		Type:       models.TypeDeviceSync,
		ExternalID: "device:" + personKey + ":" + ts.Format("2006-01-02"),
		OccurredAt: ts,
		Title:      "Активность устройства",
		Meta: map[string]any{
			"device_model": paramStr(it.params, "DEVICE_MODEL"),
			"device_type":  paramStr(it.params, "DEVICE_TYPE"),
		},
	}
	ev.Normalize()
	return &ev
}

// ---- определение устройства входа (компьютер/телефон) ----
//
// Login-аудит не отдаёт ни user-agent, ни устройство. Зато в token-аудите
// ровно в момент интерактивного входа появляется обращение клиента, который
// его выполнял (например «Google Chrome» / NATIVE_DESKTOP), и у него тот же
// item.ipAddress, что у входа. Сверка по IP обязательна: без неё окно забито
// фоновым поллингом других устройств (а у людей за VPN типа WARP логин-IP
// вообще не совпадает с фоновым — тогда честно оставляем «неизвестно»).

// annotateLoginDevices — best-effort: ошибки token-аудита не валят сбор.
func (c *Collector) annotateLoginDevices(ctx context.Context, email string, logins []models.Event) {
	var warned bool
	for i := range logins {
		ev := &logins[i]
		ip, _ := ev.Meta["ip"].(string)
		if ip == "" {
			continue
		}
		kind, app, err := c.loginDeviceKind(ctx, email, ev.OccurredAt, ip)
		if err != nil {
			if !warned {
				c.log.Warn("gwork: token-аудит для определения устройства входа недоступен", "err", err)
				warned = true
			}
			return
		}
		if kind == "" {
			continue
		}
		ev.Meta["device_kind"] = kind
		if app != "" {
			ev.Meta["client_app"] = app
		}
		label := "с компьютера"
		if kind == "phone" {
			label = "с телефона"
		}
		ev.Title += " · " + label
	}
}

// loginDeviceKind ищет в token-аудите [-2м; +5м] вокруг входа обращения
// клиентов с того же IP и классифицирует их в computer/phone.
func (c *Collector) loginDeviceKind(ctx context.Context, email string, at time.Time, ip string) (kind, app string, err error) {
	q := url.Values{
		"startTime":  {at.Add(-2 * time.Minute).Format(time.RFC3339)},
		"endTime":    {at.Add(5 * time.Minute).Format(time.RFC3339)},
		"maxResults": {"500"},
	}
	u := fmt.Sprintf(activitiesURL, url.PathEscape(email), "token") + "?" + q.Encode()
	var resp activitiesResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return "", "", err
	}
	votes := map[string]int{}
	apps := map[string]string{}
	for _, it := range resp.Items {
		if it.IPAddress != ip {
			continue
		}
		for _, e := range it.Events {
			name := paramStr(e.Parameters, "app_name")
			if k := classifyClient(name, paramStr(e.Parameters, "client_type")); k != "" {
				votes[k]++
				if apps[k] == "" {
					apps[k] = name
				}
			}
		}
	}
	switch {
	case votes["computer"] > 0 && votes["phone"] > 0:
		return "", "", nil // противоречивые сигналы — не гадаем
	case votes["computer"] > 0:
		return "computer", apps["computer"], nil
	case votes["phone"] > 0:
		return "phone", apps["phone"], nil
	}
	return "", "", nil
}

// classifyClient — app_name надёжнее client_type: у системного клиента
// «macOS» client_type почему-то NATIVE_IOS. WEB не классифицируем: это
// сторонние веб-приложения (Calendly и т.п.), а не устройство входа.
func classifyClient(appName, clientType string) string {
	switch appName {
	case "iOS", "Android":
		return "phone"
	case "macOS", "Windows", "Google Chrome", "Mozilla Firefox", "Mozilla Thunderbird", "Microsoft Edge":
		return "computer"
	}
	switch clientType {
	case "NATIVE_DESKTOP":
		return "computer"
	case "NATIVE_IOS", "NATIVE_ANDROID":
		return "phone"
	}
	return ""
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// ---- usage report (Gmail counters) ----

type usageResp struct {
	UsageReports []struct {
		Date       string `json:"date"`
		Parameters []struct {
			Name      string `json:"name"`
			IntValue  string `json:"intValue"`
			BoolValue bool   `json:"boolValue"`
			StringVal string `json:"stringValue"`
		} `json:"parameters"`
	} `json:"usageReports"`
}

// collectUsage — суточные счётчики per-user: отправленные письма Gmail.
// Usage-отчёт доступен за завершённые дни (обычно с задержкой ~1–2 дня),
// поэтому запрашиваем по каждой дате периода. ВАЖНО: категории параметров
// ограничены (accounts|gmail|drive|device_management|…) — невалидный параметр
// роняет весь запрос 400; активность устройств берётся из mobile-аудита.
func (c *Collector) collectUsage(ctx context.Context, email string, from, to time.Time, personKey string) ([]models.Event, error) {
	var out []models.Event
	for d := from.Truncate(24 * time.Hour); d.Before(to); d = d.AddDate(0, 0, 1) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		date := d.Format("2006-01-02")
		u := fmt.Sprintf(userUsageURL, url.PathEscape(email), date) +
			"?parameters=gmail:num_emails_sent"
		var resp usageResp
		if err := c.getJSON(ctx, u, &resp); err != nil {
			// Пропущенный день (нет отчёта) не должен рушить весь сбор.
			continue
		}
		for _, r := range resp.UsageReports {
			var sent int
			for _, p := range r.Parameters {
				if p.Name == "gmail:num_emails_sent" {
					sent, _ = strconv.Atoi(p.IntValue)
				}
			}
			day, _ := time.Parse("2006-01-02", r.Date)
			at := day.Add(12 * time.Hour) // середина дня — счётчик суточный
			if sent > 0 {
				ev := models.Event{
					PersonKey:  personKey,
					Source:     models.SourceGWork,
					Type:       models.TypeGmailSent,
					ExternalID: "gmail:" + personKey + ":" + r.Date,
					OccurredAt: at,
					Title:      fmt.Sprintf("Отправлено писем: %d", sent),
					Effort:     float64(sent),
					EffortUnit: "писем",
					Meta:       map[string]any{"emails_sent": sent, "date": r.Date},
				}
				ev.Normalize()
				out = append(out, ev)
			}
		}
	}
	return out, nil
}

func (c *Collector) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		msg := errBody.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("нет админ-доступа к Reports API (%d): %s — нужны reports-скоупы и права администратора", resp.StatusCode, msg)
		}
		return fmt.Errorf("Reports API %d: %s", resp.StatusCode, msg)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
