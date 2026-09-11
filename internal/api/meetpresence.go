package api

// Сверка календарных встреч (gcal) с фактом присутствия в Meet (gwork).
// На чтении лента приводится к виду «одна встреча — одна строка»:
//   - к прошедшей встрече добавляется meta.meet_minutes — сколько человек
//     реально был в конференции (сумма его подключений);
//   - встреча без единого подключения получает meta.maybe_offline
//     («не участвовал или встреча была оффлайн»);
//   - события «присутствие на встрече», совпавшие с календарной встречей,
//     помечаются как поглощённые и в общей ленте не показываются — их минуты
//     уже на самой встрече. Подключения БЕЗ календарной встречи (ad-hoc
//     звонки) остаются отдельными строками: это реальная активность.
// В БД ничего не пишется; при ошибках лента отдаётся без пометок.

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

var meetingTypes = map[models.EventType]bool{
	models.TypeMeeting:   true,
	models.TypeRecurring: true,
	models.TypeOneOnOne:  true,
	models.TypeInterview: true,
}

var meetingTypeFilter = []string{
	string(models.TypeMeeting),
	string(models.TypeRecurring),
	string(models.TypeOneOnOne),
	string(models.TypeInterview),
}

// enrichMeetPresence аннотирует встречи в ленте и возвращает индексы
// поглощённых meet-событий (их строки — дубли календарных встреч).
func (s *Server) enrichMeetPresence(ctx context.Context, events []models.Event) map[int]bool {
	type refs struct {
		meetings []*models.Event
		calls    []int
	}
	byPerson := map[string]*refs{}
	get := func(k string) *refs {
		r := byPerson[k]
		if r == nil {
			r = &refs{}
			byPerson[k] = r
		}
		return r
	}
	now := time.Now()
	for i := range events {
		ev := &events[i]
		switch {
		case meetingTypes[ev.Type]:
			// Будущие и идущие встречи не размечаем — присутствие не финально.
			if meetingEnd(ev).After(now) {
				continue
			}
			get(ev.PersonKey).meetings = append(get(ev.PersonKey).meetings, ev)
		case ev.Type == models.TypeMeetAttended:
			get(ev.PersonKey).calls = append(get(ev.PersonKey).calls, i)
		}
	}
	absorbed := map[int]bool{}
	for person, r := range byPerson {
		if len(r.meetings) > 0 {
			s.annotateMeetings(ctx, person, r.meetings)
		}
		if len(r.calls) > 0 {
			s.absorbCalls(ctx, person, events, r.calls, absorbed)
		}
	}
	return absorbed
}

func (s *Server) annotateMeetings(ctx context.Context, person string, meetings []*models.Event) {
	dataStart, ok := s.meetDataStart(ctx, person)
	if !ok {
		return // у человека вообще нет Meet-данных — судить не о чем
	}
	// История формата работы: у remote-сотрудника «встреча была оффлайн» —
	// не объяснение, отсутствие подключений помечается жёстче.
	formats, _ := s.store.ListWorkFormatHistory(ctx, person)

	var from, to time.Time
	for _, m := range meetings {
		if from.IsZero() || m.OccurredAt.Before(from) {
			from = m.OccurredAt
		}
		if end := meetingEnd(m); end.After(to) {
			to = end
		}
	}
	// Запас: ранний вход в звонок и овертайм после конца встречи.
	const margin = 2 * time.Hour
	calls, err := s.eventsOfTypes(ctx, person, from.Add(-margin), to.Add(margin),
		[]string{string(models.TypeMeetAttended)})
	if err != nil {
		return
	}

	for _, m := range meetings {
		var sec int
		for i := range calls {
			if callMatchesMeeting(&calls[i], m) {
				sec += metaInt(calls[i].Meta, "duration_seconds")
			}
		}
		if m.Meta == nil {
			m.Meta = map[string]any{}
		}
		if sec > 0 {
			m.Meta["meet_minutes"] = int(math.Round(float64(sec) / 60))
			continue
		}
		// Отсутствие в Meet значимо, только если по этому дню Meet-данные
		// в принципе есть (Reports API хранит ~6 мес) и человек встречу не отклонял.
		if resp, _ := m.Meta["response"].(string); resp == "declined" {
			continue
		}
		if !m.OccurredAt.Before(dataStart) {
			if models.NormalizeWorkFormat(models.HybridDaysAt(formats, "", m.OccurredAt)) == "remote" {
				// Формат работы на дату встречи — удалённый: встреча не могла
				// пройти оффлайн, но подключения в Meet нет — присутствие
				// подтвердить нельзя (отдельный статус, отдельный цвет).
				m.Meta["presence_unclear"] = true
			} else {
				m.Meta["maybe_offline"] = true
			}
		}
	}
}

// absorbCalls помечает meet-события, совпавшие с какой-либо календарной
// встречей человека (не обязательно попавшей на эту же страницу ленты).
func (s *Server) absorbCalls(ctx context.Context, person string, events []models.Event, callIdx []int, absorbed map[int]bool) {
	var from, to time.Time
	for _, i := range callIdx {
		start, end := callInterval(&events[i])
		if from.IsZero() || start.Before(from) {
			from = start
		}
		if end.After(to) {
			to = end
		}
	}
	const margin = 2 * time.Hour
	meetings, err := s.eventsOfTypes(ctx, person, from.Add(-margin), to.Add(margin), meetingTypeFilter)
	if err != nil {
		return
	}
	for _, i := range callIdx {
		for j := range meetings {
			if callMatchesMeeting(&events[i], &meetings[j]) {
				absorbed[i] = true
				break
			}
		}
	}
}

// callMatchesMeeting — единые правила матчинга подключения и встречи:
// по calendar_event_id (у инстансов серии он общий, поэтому дополнительно
// требуется попадание в окно инстанса ±30 мин), для старых meet-событий
// без calendar_event_id — по пересечению интервалов.
func callMatchesMeeting(c, m *models.Event) bool {
	if metaInt(c.Meta, "duration_seconds") <= 0 {
		return false
	}
	callStart, callEnd := callInterval(c)
	schedStart, schedEnd := m.OccurredAt, meetingEnd(m)
	calID, _ := c.Meta["calendar_event_id"].(string)
	switch {
	case calID != "":
		// У инстанса регулярной серии Meet-аудит отдаёт id С суффиксом
		// («…_20260907T140000Z»), у разового события — без. Сравниваем базы
		// с обеих сторон; окно инстанса ±30 мин отделяет соседние инстансы
		// одной серии (у них база общая).
		return baseCalendarID(calID) == baseCalendarID(m.ExternalID) &&
			overlaps(callStart, callEnd, schedStart.Add(-30*time.Minute), schedEnd.Add(30*time.Minute))
	default:
		return overlaps(callStart, callEnd, schedStart, schedEnd)
	}
}

// callInterval — интервал участия в звонке: OccurredAt это конец (call_ended).
func callInterval(c *models.Event) (start, end time.Time) {
	end = c.OccurredAt
	start = end.Add(-time.Duration(metaInt(c.Meta, "duration_seconds")) * time.Second)
	if ts := metaInt(c.Meta, "start_ts"); ts > 0 {
		start = time.Unix(int64(ts), 0)
	}
	return start, end
}

// meetDataStart — дата самого раннего meet-события человека.
func (s *Server) meetDataStart(ctx context.Context, person string) (time.Time, bool) {
	// From/To обязательны: фильтр хранилища подставляет границы безусловно,
	// и нулевой To дал бы «occurred_at < 0001-01-01» — пустую выборку.
	page, err := s.store.ListEvents(ctx, models.EventFilter{
		PersonKey: person,
		From:      time.Unix(0, 0),
		To:        time.Now().Add(24 * time.Hour),
		Types:     []string{string(models.TypeMeetAttended)},
		PerPage:   1,
		SortDesc:  false,
	})
	if err != nil || len(page.Items) == 0 {
		return time.Time{}, false
	}
	return page.Items[0].OccurredAt, true
}

// eventsOfTypes выгружает события человека нужных типов за окно (с пагинацией).
func (s *Server) eventsOfTypes(ctx context.Context, person string, from, to time.Time, types []string) ([]models.Event, error) {
	var out []models.Event
	for page := 1; page <= 10; page++ {
		p, err := s.store.ListEvents(ctx, models.EventFilter{
			PersonKey: person,
			From:      from,
			To:        to,
			Types:     types,
			Page:      page,
			PerPage:   500,
			SortDesc:  false,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if len(p.Items) < 500 {
			break
		}
	}
	return out, nil
}

func meetingEnd(m *models.Event) time.Time {
	return m.OccurredAt.Add(time.Duration(m.Effort) * time.Second)
}

// baseCalendarID отрезает суффикс инстанса регулярной встречи
// («…_20260907T120000Z») — Meet-аудит хранит id серии без суффикса.
func baseCalendarID(externalID string) string {
	if i := strings.IndexByte(externalID, '_'); i > 0 {
		return externalID[:i]
	}
	return externalID
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// metaInt достаёт число из meta: после JSON-раунда это float64, из свежего
// коллектора — int, из старых записей бывает строка.
func metaInt(meta map[string]any, key string) int {
	switch v := meta[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
