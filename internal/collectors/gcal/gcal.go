// Package gcal собирает участие сотрудника во встречах Google Calendar.
//
// Авторизация общая с коллектором gdocs (см. gdocs.NewHTTPClientWithScopes),
// но scope свой — calendar.readonly. Читается календарь самого сотрудника
// (calendarId = его google_email), поэтому нужен один из вариантов доступа:
//   - service_account + domain-wide delegation со scope calendar.readonly;
//   - oauth-токен пользователя, чей календарь видит календари коллег
//     (внутри организации календари обычно открыты на чтение);
//   - GCAL_CALENDAR_ID, явно указывающий общий календарь.
//
// Встречей считается событие со временем (не целодневное), в котором, кроме
// самого сотрудника, участвует хотя бы один человек, и участие в котором
// сотрудник подтвердил (принял приглашение либо сам организатор). Отклонённые,
// неотвеченные и «возможно» не учитываются. Типы событий:
//   - gcal.interview — собеседование: в названии есть одна из подстрок
//     GCAL_INTERVIEW_MARKERS (по умолчанию «interview» и «собеседование»);
//   - gcal.recurring — экземпляр регулярной (повторяющейся) встречи; все
//     экземпляры серии связаны общим RefID и группируются в UI;
//   - gcal.meeting — разовая встреча.
package gcal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gdocs"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Scope — единственное право, необходимое коллектору.
const Scope = "https://www.googleapis.com/auth/calendar.readonly"

const (
	// maxPages — предохранитель от бесконечной пагинации.
	maxPages = 50
	// defaultPageSize используется, если размер страницы не задан в конфиге.
	defaultPageSize = 250
	// maxPageSize — потолок Calendar API.
	maxPageSize = 2500
)

// Collector собирает события календаря одного человека.
type Collector struct {
	cfg             config.GoogleConfig
	log             *slog.Logger
	svc             *calendar.Service
	markers         []string
	vacationMarkers []string
	sickMarkers     []string
	progress        collectors.Progress
}

// New создаёт коллектор: поднимает HTTP-клиент Google со scope календаря.
func New(ctx context.Context, cfg config.GoogleConfig, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	httpClient, err := gdocs.NewHTTPClientWithScopes(ctx, cfg, []string{Scope})
	if err != nil {
		return nil, fmt.Errorf("gcal: %w", err)
	}
	svc, err := calendar.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("gcal: не удалось создать клиент Calendar API: %w", err)
	}
	return &Collector{
		cfg:             cfg,
		log:             log,
		svc:             svc,
		markers:         lowerAll(cfg.InterviewMarkers),
		vacationMarkers: lowerAll(cfg.VacationMarkers),
		sickMarkers:     lowerAll(cfg.SickMarkers),
	}, nil
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceGCal }

// SetProgress подключает колбэк прогресса.
func (c *Collector) SetProgress(p collectors.Progress) { c.progress = p }

func (c *Collector) report(stage string, done, total int) {
	if c.progress != nil {
		c.progress(stage, done, total)
	}
}

// Collect выгружает встречи за период.
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	email := strings.ToLower(strings.TrimSpace(req.Person.GoogleEmail))
	if email == "" {
		return collectors.Result{}, errors.New("gcal: у пользователя не задан google_email")
	}
	calendarID := strings.TrimSpace(c.cfg.CalendarID)
	if calendarID == "" {
		calendarID = email
	}

	pageSize := c.cfg.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	var (
		events      []models.Event
		days        []models.PersonDay
		notMeetings int
		notAccepted int
		pageToken   string
	)
	for page := 1; ; page++ {
		call := c.svc.Events.List(calendarID).
			TimeMin(req.From.Format(time.RFC3339)).
			TimeMax(req.To.Format(time.RFC3339)).
			SingleEvents(true). // повторяющиеся встречи разворачиваются в экземпляры
			OrderBy("startTime").
			MaxResults(int64(pageSize)).
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return collectors.Result{}, explainAPIError(err, calendarID)
		}

		for _, item := range resp.Items {
			// Целодневные события с пометкой отпуска («… - Vacation») или
			// больничного («… - Sick leave») — не встречи, а особые дни
			// для матрицы активности.
			if abs := c.absenceDays(item, req); len(abs) > 0 {
				days = append(days, abs...)
				continue
			}
			ev, skip := c.toEvent(item, email, req.Person.Key)
			switch skip {
			case skipNone:
				events = append(events, ev)
			case skipNotAccepted:
				notAccepted++
			default:
				notMeetings++
			}
		}
		c.report("страницы календаря", page, 0)

		pageToken = resp.NextPageToken
		if pageToken == "" || page >= maxPages {
			break
		}
	}

	groupRepeated(events)

	var parts []string
	vacN, sickN := 0, 0
	for _, d := range days {
		switch d.Kind {
		case models.DayVacation:
			vacN++
		case models.DaySick:
			sickN++
		}
	}
	if vacN > 0 {
		parts = append(parts, fmt.Sprintf("дней отпуска: %d", vacN))
	}
	if sickN > 0 {
		parts = append(parts, fmt.Sprintf("дней больничного: %d", sickN))
	}

	// Праздники по офису из HRDB: читаются из публичных праздничных
	// календарей Google той страны, где сидит человек.
	country := models.CountryForOffice(req.Person.Office)
	switch {
	case req.Person.Office == "":
		parts = append(parts, "офис не известен — праздники не размечены")
	case country == "":
		parts = append(parts, fmt.Sprintf("офис %q не сопоставлен со страной — праздники не размечены", req.Person.Office))
	default:
		holidays, err := c.fetchHolidays(ctx, country, req)
		if err != nil {
			c.log.Warn("не удалось получить праздничный календарь", "country", country, "err", err)
			parts = append(parts, "праздничный календарь недоступен: "+err.Error())
		} else {
			days = append(days, holidays...)
			parts = append(parts, fmt.Sprintf("праздников (%s): %d", country, len(holidays)))
		}
	}

	if notAccepted > 0 {
		parts = append(parts, fmt.Sprintf("пропущено %d без подтверждённого участия", notAccepted))
	}
	if notMeetings > 0 {
		parts = append(parts, fmt.Sprintf("пропущено %d не встреч", notMeetings))
	}
	return collectors.Result{Events: events, Days: days, Note: strings.Join(parts, "; ")}, nil
}

// absenceDays распознаёт целодневное событие-отсутствие (отпуск или
// больничный) и разворачивает его в дни, обрезая по границам периода.
func (c *Collector) absenceDays(item *calendar.Event, req collectors.Request) []models.PersonDay {
	if item == nil || item.Status == "cancelled" || item.Start == nil || item.Start.Date == "" {
		return nil
	}
	title := strings.TrimSpace(collapseSpace(item.Summary))
	lower := strings.ToLower(title)
	kind := ""
	switch {
	case matchAny(lower, c.sickMarkers):
		kind = models.DaySick
	case matchAny(lower, c.vacationMarkers):
		kind = models.DayVacation
	}
	if kind == "" {
		return nil
	}
	start, err := time.Parse("2006-01-02", item.Start.Date)
	if err != nil {
		return nil
	}
	// End.Date у целодневных событий эксклюзивный; без него — один день.
	end := start.AddDate(0, 0, 1)
	if item.End != nil && item.End.Date != "" {
		if e, err := time.Parse("2006-01-02", item.End.Date); err == nil && e.After(start) {
			end = e
		}
	}
	lo := time.Date(req.From.Year(), req.From.Month(), req.From.Day(), 0, 0, 0, 0, time.UTC)
	var out []models.PersonDay
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		if d.Before(lo) || !d.Before(req.To) {
			continue
		}
		out = append(out, models.PersonDay{
			PersonKey: req.Person.Key,
			Day:       d,
			Kind:      kind,
			Label:     title,
		})
	}
	return out
}

// holidayCalendars — публичные праздничные календари Google по странам.
var holidayCalendars = map[string]string{
	"RU": "ru.russian#holiday@group.v.calendar.google.com",
	"CY": "en.cy#holiday@group.v.calendar.google.com",
	"GB": "en.uk#holiday@group.v.calendar.google.com",
	"ES": "es.spain#holiday@group.v.calendar.google.com",
	"GE": "en.ge#holiday@group.v.calendar.google.com",
}

// fetchHolidays читает государственные праздники страны за период сбора.
func (c *Collector) fetchHolidays(ctx context.Context, country string, req collectors.Request) ([]models.PersonDay, error) {
	days, err := c.HolidaysRange(ctx, country, req.From, req.To)
	if err != nil {
		return nil, err
	}
	for i := range days {
		days[i].PersonKey = req.Person.Key
	}
	return days, nil
}

// HolidaysRange возвращает государственные праздники страны за период из
// публичного календаря Google — используется сборщиком и импортом в
// корпоративный календарь (PersonKey у результатов пустой).
func (c *Collector) HolidaysRange(ctx context.Context, country string, from, to time.Time) ([]models.PersonDay, error) {
	calID, ok := holidayCalendars[country]
	if !ok {
		return nil, fmt.Errorf("нет праздничного календаря для страны %s", country)
	}
	var out []models.PersonDay
	seen := map[string]bool{}
	pageToken := ""
	for page := 1; page <= maxPages; page++ {
		call := c.svc.Events.List(calID).
			TimeMin(from.Format(time.RFC3339)).
			TimeMax(to.Format(time.RFC3339)).
			SingleEvents(true).
			MaxResults(250).
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, err
		}
		for _, item := range resp.Items {
			if item == nil || item.Start == nil || item.Start.Date == "" {
				continue
			}
			if !isPublicHoliday(item.Description) {
				continue
			}
			d, err := time.Parse("2006-01-02", item.Start.Date)
			if err != nil || seen[item.Start.Date] {
				continue
			}
			seen[item.Start.Date] = true
			out = append(out, models.PersonDay{
				Day:   d,
				Kind:  models.DayHoliday,
				Label: strings.TrimSpace(item.Summary),
			})
		}
		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return out, nil
}

// skipReason — почему событие календаря не стало событием активности.
type skipReason int

const (
	skipNone skipReason = iota
	// skipNotMeeting — целодневное, служебное, отменённое или без участников.
	skipNotMeeting
	// skipNotAccepted — встреча есть, но участие не подтверждено.
	skipNotAccepted
)

// toEvent превращает событие календаря в событие активности.
func (c *Collector) toEvent(item *calendar.Event, email, personKey string) (models.Event, skipReason) {
	if item == nil || item.Status == "cancelled" {
		return models.Event{}, skipNotMeeting
	}
	// Служебные записи календаря встречами не являются.
	switch item.EventType {
	case "outOfOffice", "focusTime", "workingLocation", "birthday":
		return models.Event{}, skipNotMeeting
	}
	// Целодневные события (отпуска, праздники) — не встречи.
	if item.Start == nil || item.Start.DateTime == "" || item.End == nil || item.End.DateTime == "" {
		return models.Event{}, skipNotMeeting
	}
	start, err := time.Parse(time.RFC3339, item.Start.DateTime)
	if err != nil {
		return models.Event{}, skipNotMeeting
	}
	end, err := time.Parse(time.RFC3339, item.End.DateTime)
	if err != nil {
		end = start
	}

	// Кроме самого сотрудника во встрече должны быть живые участники
	// (переговорки приходят как attendee с resource=true). Первый «не сам» —
	// кандидат в собеседники для встреч 1:1.
	humans := 0
	selfStatus := ""
	peer, peerName := "", ""
	for _, a := range item.Attendees {
		if a == nil || a.Resource {
			continue
		}
		humans++
		// ВАЖНО: только по email. a.Self у Google означает «владелец учётки,
		// от которой идёт API-запрос» (общий subject/token на всех людей),
		// а не собираемый человек — с a.Self ответ учётки API перетирал бы
		// ответ человека (declined превращался в accepted).
		if strings.EqualFold(a.Email, email) {
			selfStatus = a.ResponseStatus
			continue
		}
		if peer == "" {
			peer = strings.ToLower(a.Email)
			peerName = a.DisplayName
			if peerName == "" {
				peerName = peer
			}
		}
	}
	if humans < 2 {
		return models.Event{}, skipNotMeeting
	}

	// Участие должно быть подтверждено: сотрудник принял приглашение, либо он
	// организатор и не отклонил собственную встречу. «Отклонил», «возможно» и
	// «не ответил» для приглашённого — не участие. Organizer.Self не используем
	// по той же причине, что и a.Self выше: это учётка API, не человек.
	// Известное ограничение без решения на уровне Calendar API: у прошедших
	// инстансов регулярных серий Google подставляет ТЕКУЩИЙ список участников
	// с их текущими ответами, поэтому добавление человека в старую серию
	// порождает фантомные «участия» задним числом. Частично гасится отсечкой
	// по дате найма в persist и пометкой maybe_offline при сверке с Meet.
	isOrganizer := item.Organizer != nil && strings.EqualFold(item.Organizer.Email, email)
	if isOrganizer {
		if selfStatus == "declined" {
			return models.Event{}, skipNotAccepted
		}
	} else if selfStatus != "accepted" {
		return models.Event{}, skipNotAccepted
	}

	title := strings.TrimSpace(collapseSpace(item.Summary))
	lower := strings.ToLower(title)

	// Классификация: собеседование > регулярная встреча > 1:1 > разовая.
	// Категория встречи пишется в Project/ProjectName: у регулярных это название
	// серии (одинаковые названия склеиваются без учёта регистра), у остальных —
	// фиксированные группы. Так разбивка «по проектам» даёт группировку встреч.
	typ := models.TypeMeeting
	project, projectName := "разовые встречи", "Разовые встречи"
	switch {
	case matchAny(lower, c.markers):
		typ = models.TypeInterview
		project, projectName = "собеседования", "Собеседования"
	case item.RecurringEventId != "":
		typ = models.TypeRecurring
		if title != "" {
			project, projectName = lower, title
		} else {
			project, projectName = "регулярные встречи", "Регулярные встречи"
		}
	case humans == 2 || matchAny(lower, oneOnOneMarkers):
		typ = models.TypeOneOnOne
		project, projectName = "1:1", "1:1"
	}

	if title == "" {
		title = "(встреча без названия)"
	}

	organizer, organizerName := "", ""
	if item.Organizer != nil {
		organizer = strings.ToLower(item.Organizer.Email)
		organizerName = item.Organizer.DisplayName
		if organizerName == "" {
			organizerName = organizer
		}
	}
	// Для 1:1 с более чем двумя приглашёнными (пометка в названии) собеседником
	// считаем организатора, если встречу организовал не сам сотрудник.
	if typ == models.TypeOneOnOne && humans > 2 && !isOrganizer && organizer != "" {
		peer, peerName = organizer, organizerName
	}
	if typ != models.TypeOneOnOne {
		peer, peerName = "", ""
	}

	refID := item.RecurringEventId
	if refID == "" {
		refID = item.Id
	}

	ev := models.Event{
		PersonKey:   personKey,
		Source:      models.SourceGCal,
		Type:        typ,
		ExternalID:  item.Id,
		OccurredAt:  start,
		Title:       title,
		URL:         item.HtmlLink,
		Project:     project,
		ProjectName: projectName,
		RefID:       refID,
		// Effort — длительность встречи в секундах; в сводке агрегируется
		// в «часы во встречах» отдельно от ворклогов Jira.
		Effort:     end.Sub(start).Seconds(),
		EffortUnit: "seconds",
		Meta: map[string]any{
			"attendees":      humans,
			"organizer":      organizer,
			"organizer_name": organizerName,
			"organizer_self": isOrganizer,
			"response":       selfStatus,
			"recurring":      item.RecurringEventId != "",
			"event_type":     item.EventType,
		},
	}
	if peer != "" {
		ev.Meta["peer"] = peer
		ev.Meta["peer_name"] = peerName
	}
	ev.Normalize()
	return ev, skipNone
}

// groupRepeated объединяет в группу любые встречи с одинаковым названием,
// даже если формально они не повторяющиеся: пересозданные серии, «ручные»
// повторы. Такие встречи получают группу по названию и тип «регулярная».
// Собеседования и 1:1 не трогаем — у них свои категории.
func groupRepeated(events []models.Event) {
	counts := map[string]int{}
	for _, e := range events {
		if e.Type == models.TypeMeeting || e.Type == models.TypeRecurring {
			counts[strings.ToLower(e.Title)]++
		}
	}
	for i := range events {
		e := &events[i]
		if e.Type != models.TypeMeeting && e.Type != models.TypeRecurring {
			continue
		}
		if counts[strings.ToLower(e.Title)] < 2 {
			continue
		}
		e.Project = strings.ToLower(e.Title)
		e.ProjectName = e.Title
		if e.Type == models.TypeMeeting {
			e.Type = models.TypeRecurring
			// ID детерминированно зависит от типа — пересчитываем.
			e.ID = ""
			e.ComputeID()
		}
	}
}

// isPublicHoliday отделяет государственные праздники от памятных дат:
// календари Google содержат и «observances» (Pancake Day, канун Рождества),
// которые выходными не являются. Тип дня приходит в description на языке
// календаря.
func isPublicHoliday(description string) bool {
	d := strings.ToLower(description)
	return strings.Contains(d, "public holiday") ||
		strings.Contains(d, "государственный праздник") ||
		strings.Contains(d, "día festivo")
}

// oneOnOneMarkers — пометки «один на один» в названии встречи: по ним 1:1
// распознаётся, даже если приглашённых больше двух (например, добавлен ассистент).
var oneOnOneMarkers = []string{"1:1", "1-1", "1x1", "1on1", "1 on 1", "one-on-one", "один на один"}

func matchAny(lower string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// explainAPIError дополняет типовые ошибки Calendar API подсказкой.
func explainAPIError(err error, calendarID string) error {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 404:
			return fmt.Errorf("gcal: календарь %q не найден или не расшарен на учётку, "+
				"которой идут запросы: откройте доступ на чтение или задайте GCAL_CALENDAR_ID: %w", calendarID, err)
		case 403:
			return fmt.Errorf("gcal: нет доступа к календарю %q: для service_account добавьте scope %s "+
				"в domain-wide delegation; для oauth перевыпустите refresh token (go run ./cmd/googleauth): %w",
				calendarID, Scope, err)
		}
	}
	return fmt.Errorf("gcal: запрос событий календаря %q: %w", calendarID, err)
}
