package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// filterSQL собирает WHERE-условие и аргументы для выборок по событиям.
type filterSQL struct {
	where []string
	args  []any
}

func newFilterSQL(f models.EventFilter) *filterSQL {
	q := &filterSQL{}
	q.add("person_key = ", f.PersonKey)
	q.add("occurred_at >= ", f.From)
	q.add("occurred_at < ", f.To)
	if len(f.Sources) > 0 {
		q.addAny("source", f.Sources)
	}
	if len(f.Types) > 0 {
		q.addAny("type", f.Types)
	}
	if len(f.Projects) > 0 {
		q.addAny("project", f.Projects)
	}
	if f.MinAIScore > 0 {
		q.args = append(q.args, f.MinAIScore)
		q.where = append(q.where, fmt.Sprintf(
			"(source <> 'slack' OR COALESCE((meta->>'ai_score')::numeric, 10) >= $%d)", len(q.args)))
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		q.args = append(q.args, "%"+strings.ToLower(s)+"%")
		n := len(q.args)
		q.where = append(q.where, fmt.Sprintf("(lower(title) LIKE $%d OR lower(body) LIKE $%d OR lower(ref_id) LIKE $%d)", n, n, n))
	}
	return q
}

func (q *filterSQL) add(expr string, val any) {
	q.args = append(q.args, val)
	q.where = append(q.where, fmt.Sprintf("%s$%d", expr, len(q.args)))
}

func (q *filterSQL) addAny(col string, vals []string) {
	q.args = append(q.args, vals)
	q.where = append(q.where, fmt.Sprintf("%s = ANY($%d)", col, len(q.args)))
}

func (q *filterSQL) clause() string {
	if len(q.where) == 0 {
		return "TRUE"
	}
	return strings.Join(q.where, " AND ")
}

// next возвращает номер следующего плейсхолдера при добавлении аргумента.
func (q *filterSQL) next(val any) int {
	q.args = append(q.args, val)
	return len(q.args)
}

// CountEvents возвращает общее число событий по фильтру.
func (s *Store) CountEvents(ctx context.Context, f models.EventFilter) (int, error) {
	q := newFilterSQL(f)
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE "+q.clause(), q.args...).Scan(&n)
	return n, err
}

// ListEvents возвращает страницу ленты событий.
func (s *Store) ListEvents(ctx context.Context, f models.EventFilter) (models.EventPage, error) {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	if f.PerPage > 500 {
		f.PerPage = 500
	}
	if f.Page < 1 {
		f.Page = 1
	}

	total, err := s.CountEvents(ctx, f)
	if err != nil {
		return models.EventPage{}, err
	}

	q := newFilterSQL(f)
	order := "DESC"
	if !f.SortDesc {
		order = "ASC"
	}
	limitPos := q.next(f.PerPage)
	offsetPos := q.next((f.Page - 1) * f.PerPage)

	sql := fmt.Sprintf(`
SELECT id, person_key, source, type, external_id, occurred_at, title, body, url,
       project, project_name, ref_id, parent_ref_id, effort, effort_unit, meta
FROM events WHERE %s
ORDER BY occurred_at %s, id
LIMIT $%d OFFSET $%d`, q.clause(), order, limitPos, offsetPos)

	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return models.EventPage{}, err
	}
	defer rows.Close()

	items, err := scanEvents(rows)
	if err != nil {
		return models.EventPage{}, err
	}
	return models.EventPage{Items: items, Total: total, Page: f.Page, PerPage: f.PerPage}, nil
}

// GetEvent возвращает одно событие по id.
func (s *Store) GetEvent(ctx context.Context, id string) (models.Event, error) {
	const sql = `
SELECT id, person_key, source, type, external_id, occurred_at, title, body, url,
       project, project_name, ref_id, parent_ref_id, effort, effort_unit, meta
FROM events WHERE id = $1`
	rows, err := s.pool.Query(ctx, sql, id)
	if err != nil {
		return models.Event{}, err
	}
	defer rows.Close()
	items, err := scanEvents(rows)
	if err != nil {
		return models.Event{}, err
	}
	if len(items) == 0 {
		return models.Event{}, ErrNotFound
	}
	return items[0], nil
}

// RelatedEvents возвращает события, привязанные к той же сущности (ref_id) —
// используется при проваливании в конкретную задачу, MR, тред или документ.
func (s *Store) RelatedEvents(ctx context.Context, personKey, refID string, limit int) ([]models.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	const sql = `
SELECT id, person_key, source, type, external_id, occurred_at, title, body, url,
       project, project_name, ref_id, parent_ref_id, effort, effort_unit, meta
FROM events
WHERE person_key = $1 AND (ref_id = $2 OR parent_ref_id = $2)
ORDER BY occurred_at ASC
LIMIT $3`
	rows, err := s.pool.Query(ctx, sql, personKey, refID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func scanEvents(rows pgx.Rows) ([]models.Event, error) {
	items := make([]models.Event, 0, 64)
	for rows.Next() {
		var e models.Event
		var src, typ string
		var meta []byte
		if err := rows.Scan(&e.ID, &e.PersonKey, &src, &typ, &e.ExternalID, &e.OccurredAt, &e.Title, &e.Body,
			&e.URL, &e.Project, &e.ProjectName, &e.RefID, &e.ParentRefID, &e.Effort, &e.EffortUnit, &meta); err != nil {
			return nil, err
		}
		e.Source = models.Source(src)
		e.Type = models.EventType(typ)
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &e.Meta)
		}
		items = append(items, e)
	}
	return items, rows.Err()
}

// Timeline агрегирует события по временным корзинам с разбивкой по источникам.
// granularity: hour | day | week | month.
func (s *Store) Timeline(ctx context.Context, f models.EventFilter, granularity string, loc *time.Location) ([]models.TimelinePoint, error) {
	trunc := map[string]string{"hour": "hour", "day": "day", "week": "week", "month": "month"}[granularity]
	if trunc == "" {
		trunc = "day"
	}
	q := newFilterSQL(f)
	tzPos := q.next(loc.String())

	sql := fmt.Sprintf(`
SELECT date_trunc('%s', occurred_at AT TIME ZONE $%d) AS bucket, source, count(*)
FROM events WHERE %s
GROUP BY 1, 2 ORDER BY 1`, trunc, tzPos, q.clause())

	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	idx := map[time.Time]int{}
	var out []models.TimelinePoint
	for rows.Next() {
		var bucket time.Time
		var source string
		var n int
		if err := rows.Scan(&bucket, &source, &n); err != nil {
			return nil, err
		}
		i, ok := idx[bucket]
		if !ok {
			out = append(out, models.TimelinePoint{Bucket: bucket, Counts: map[string]int{}})
			i = len(out) - 1
			idx[bucket] = i
		}
		out[i].Counts[source] += n
		out[i].Total += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fillGaps(out, trunc, f.From, f.To, loc), nil
}

// fillGaps добавляет нулевые корзины, чтобы линия не «склеивала» простои.
func fillGaps(points []models.TimelinePoint, trunc string, from, to time.Time, loc *time.Location) []models.TimelinePoint {
	if trunc == "month" || trunc == "week" || len(points) == 0 {
		return points
	}
	step := 24 * time.Hour
	if trunc == "hour" {
		step = time.Hour
	}
	have := make(map[time.Time]models.TimelinePoint, len(points))
	for _, p := range points {
		have[p.Bucket] = p
	}
	start := from.In(loc).Truncate(step)
	if trunc == "day" {
		start = time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, time.UTC)
	}
	var out []models.TimelinePoint
	for t := start; t.Before(to.In(loc)); t = t.Add(step) {
		key := t
		if p, ok := have[key]; ok {
			out = append(out, p)
		} else {
			out = append(out, models.TimelinePoint{Bucket: key, Counts: map[string]int{}})
		}
		if len(out) > 5000 {
			break
		}
	}
	if len(out) == 0 {
		return points
	}
	return out
}

// Breakdown агрегирует события по измерению: source | type | project | ref | peer.
// «ref» группирует по ref_id — так экземпляры одной регулярной встречи (или
// события одной задачи/MR) складываются в серию; «peer» — по собеседнику
// встречи 1:1 (meta.peer, заполняет коллектор gcal).
func (s *Store) Breakdown(ctx context.Context, f models.EventFilter, dimension string) ([]models.CountItem, error) {
	col := map[string]string{
		"source":  "source",
		"type":    "type",
		"project": "project",
		"ref":     "ref_id",
		"peer":    "meta->>'peer'",
	}[dimension]
	if col == "" {
		col = "source"
	}
	// Человекочитаемая подпись группы, если она есть в самих событиях.
	labelExpr := "''"
	switch dimension {
	case "ref":
		labelExpr = "COALESCE(max(title), '')"
	case "project":
		labelExpr = "COALESCE(max(project_name), '')"
	case "peer":
		labelExpr = "COALESCE(max(meta->>'peer_name'), '')"
	}
	q := newFilterSQL(f)
	sql := fmt.Sprintf(`
SELECT COALESCE(NULLIF(%s, ''), '—') AS k,
       %s AS lbl,
       count(*) AS c,
       COALESCE(sum(effort), 0) AS eff
FROM events WHERE %s
GROUP BY 1 ORDER BY c DESC, k`, col, labelExpr, q.clause())

	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.CountItem
	for rows.Next() {
		var it models.CountItem
		var lbl string
		if err := rows.Scan(&it.Key, &lbl, &it.Count, &it.Value); err != nil {
			return nil, err
		}
		it.Label = lbl
		if it.Label == "" {
			it.Label = labelFor(dimension, it.Key)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func labelFor(dimension, key string) string {
	switch dimension {
	case "type":
		return models.EventType(key).Label()
	case "source":
		return map[string]string{
			"jira": "Jira", "gitlab": "GitLab", "slack": "Slack", "gdocs": "Google Docs",
			"gcal": "Календарь", "allure": "Allure", "confluence": "Confluence",
		}[key]
	default:
		return key
	}
}

// DefaultShallowTypes — «поверхностная» активность по умолчанию: presence-сигналы
// и чат. День, где нет НИЧЕГО кроме них, на матрице подсвечивается отдельно.
// Набор переопределяется в настройках для каждой Area of Responsibility.
var DefaultShallowTypes = []string{
	"gwork.login", "gwork.device_sync",
	"slack.message", "slack.thread_reply", "slack.reaction",
}

// meetingEventTypes — календарные встречи: сами по себе они лишь принятое
// приглашение, а не факт работы.
var meetingEventTypes = []string{
	"gcal.meeting", "gcal.recurring", "gcal.one_on_one", "gcal.interview",
}

// ShallowDays — даты (в таймзоне loc), где вся активность человека — из набора
// shallowTypes (что считать низкой активностью, задаётся в настройках по Area).
// Календарная встреча БЕЗ подтверждённого присутствия (нет gwork.meet_attended)
// день не спасает — но только в период, где Meet-данные у человека в принципе
// есть (иначе вся история до ретеншна стала бы «низкой»).
//
// remoteDates — даты (YYYY-MM-DD в loc), когда у человека формат работы Remote:
// ТОЛЬКО в эти дни непосещённая встреча не считается работой (оффлайн-встреча
// маловероятна). В офис/гибрид-дни встреча без Meet может быть офлайн — считаем
// её работой (день не помечаем низкой активностью). Ретеншн-гард сохранён:
// до первого meet-события человека встречи не дисконтируем (данных нет).
func (s *Store) ShallowDays(ctx context.Context, personKey string, from, to time.Time, loc *time.Location, shallowTypes, remoteDates []string) ([]string, error) {
	if len(shallowTypes) == 0 {
		return nil, nil // пустой набор — низкую активность не выделяем
	}
	const q = `
SELECT (occurred_at AT TIME ZONE $4)::date AS d
FROM events
WHERE person_key = $1 AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1
HAVING count(*) FILTER (
	WHERE type <> ALL($5)
	  AND NOT (
		type = ANY($6)
		AND ((occurred_at AT TIME ZONE $4)::date::text = ANY($7))
		AND occurred_at >= COALESCE(
			(SELECT min(occurred_at) FROM events
			 WHERE person_key = $1 AND type = 'gwork.meet_attended'),
			'infinity'::timestamptz)
	  )
) = 0
ORDER BY 1`
	rows, err := s.pool.Query(ctx, q, personKey, from, to, loc.String(), shallowTypes, meetingEventTypes, remoteDates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d.Format("2006-01-02"))
	}
	return out, rows.Err()
}

// Heatmap строит матрицу «день недели × час» в указанной таймзоне.
func (s *Store) Heatmap(ctx context.Context, f models.EventFilter, loc *time.Location) ([]models.HeatCell, error) {
	q := newFilterSQL(f)
	tzPos := q.next(loc.String())
	sql := fmt.Sprintf(`
SELECT EXTRACT(ISODOW FROM occurred_at AT TIME ZONE $%d)::int - 1 AS wd,
       EXTRACT(HOUR   FROM occurred_at AT TIME ZONE $%d)::int      AS hr,
       count(*)
FROM events WHERE %s
GROUP BY 1, 2`, tzPos, tzPos, q.clause())

	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.HeatCell
	for rows.Next() {
		var c models.HeatCell
		if err := rows.Scan(&c.Weekday, &c.Hour, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// aggregates — сырые агрегаты по периоду, из которых собирается Summary.
type aggregates struct {
	total        int
	activeDays   int
	worklogSec   float64
	meetingSec   float64
	linesChanged float64
	first, last  *time.Time
}

func (s *Store) aggregate(ctx context.Context, f models.EventFilter, loc *time.Location) (aggregates, error) {
	q := newFilterSQL(f)
	tzPos := q.next(loc.String())
	// Секунды хранят и ворклоги Jira, и встречи календаря — разводим по источнику,
	// чтобы встречи не попадали в «списано часов» и наоборот.
	sql := fmt.Sprintf(`
SELECT count(*)                                                                      AS total,
       count(DISTINCT date_trunc('day', occurred_at AT TIME ZONE $%d))                AS active_days,
       COALESCE(sum(effort) FILTER (WHERE effort_unit = 'seconds' AND source = 'jira'), 0) AS worklog_sec,
       COALESCE(sum(effort) FILTER (WHERE effort_unit = 'seconds' AND source = 'gcal'), 0) AS meeting_sec,
       COALESCE(sum(effort) FILTER (WHERE effort_unit = 'lines'), 0)                  AS lines_changed,
       min(occurred_at), max(occurred_at)
FROM events WHERE %s`, tzPos, q.clause())

	var a aggregates
	err := s.pool.QueryRow(ctx, sql, q.args...).Scan(&a.total, &a.activeDays, &a.worklogSec, &a.meetingSec, &a.linesChanged, &a.first, &a.last)
	return a, err
}

// Summary собирает сводку за период вместе со сравнением с предыдущим равным периодом.
func (s *Store) Summary(ctx context.Context, f models.EventFilter, loc *time.Location) (models.Summary, error) {
	agg, err := s.aggregate(ctx, f, loc)
	if err != nil {
		return models.Summary{}, err
	}
	bySource, err := s.Breakdown(ctx, f, "source")
	if err != nil {
		return models.Summary{}, err
	}
	projects, err := s.Breakdown(ctx, f, "project")
	if err != nil {
		return models.Summary{}, err
	}
	if len(projects) > 8 {
		projects = projects[:8]
	}

	// Предыдущий период той же длительности — база для дельт.
	span := f.To.Sub(f.From)
	prevFilter := f
	prevFilter.To = f.From
	prevFilter.From = f.From.Add(-span)
	prevAgg, err := s.aggregate(ctx, prevFilter, loc)
	if err != nil {
		return models.Summary{}, err
	}
	prevBySource, err := s.Breakdown(ctx, prevFilter, "source")
	if err != nil {
		return models.Summary{}, err
	}

	byType, err := s.Breakdown(ctx, f, "type")
	if err != nil {
		return models.Summary{}, err
	}
	prevByType, err := s.Breakdown(ctx, prevFilter, "type")
	if err != nil {
		return models.Summary{}, err
	}

	dayAgg, err := s.dayStats(ctx, f, loc)
	if err != nil {
		return models.Summary{}, err
	}

	sum := models.Summary{
		PersonKey:    f.PersonKey,
		From:         f.From,
		To:           f.To,
		TotalEvents:  agg.total,
		ActiveDays:   agg.activeDays,
		SpanDays:     int(span.Hours()/24 + 0.5),
		BySource:     bySource,
		TopProjects:  projects,
		WorklogHours: round1(agg.worklogSec / 3600),
		MeetingHours: round1(agg.meetingSec / 3600),
		LinesChanged: int64(agg.linesChanged),
		FirstEventAt: agg.first,
		LastEventAt:  agg.last,
		PrevTotal:    prevAgg.total,
		PrevBySource: prevBySource,

		WorkingDays:       dayAgg.working,
		ActiveWorkingDays: dayAgg.activeWorking,
		ActiveDayBuckets: []models.CountItem{
			{Key: "1", Label: "1 событие за день", Count: dayAgg.buckets[0]},
			{Key: "2-5", Label: "2–5 событий за день", Count: dayAgg.buckets[1]},
			{Key: "5+", Label: "более 5 событий за день", Count: dayAgg.buckets[2]},
		},
		WeekendDays:     dayAgg.weekend,
		HolidayDays:     dayAgg.holiday,
		VacationDays:    dayAgg.vacation,
		SickDays:        dayAgg.sick,
		IdleWorkingDays: dayAgg.idleWorking,
		Office:          dayAgg.office,
		HolidayCountry:  models.CountryForOffice(dayAgg.office),
	}
	sum.Highlights = buildHighlights(sum, byType, prevByType, prevAgg)
	return sum, nil
}

// dayAggregates — классификация дней периода.
type dayAggregates struct {
	working, activeWorking, idleWorking int
	weekend, holiday, vacation, sick    int
	// buckets — рабочие дни с активностью по её объёму: 1 событие,
	// 2–5 событий, более 5 событий.
	buckets [3]int
	office  string
}

// dayStats раскладывает дни периода по категориям с приоритетом
// отпуск > праздник > выходной > рабочий; для рабочих дней считается,
// была ли в них активность (в часовом поясе пользователя).
func (s *Store) dayStats(ctx context.Context, f models.EventFilter, loc *time.Location) (dayAggregates, error) {
	var out dayAggregates

	q := newFilterSQL(f)
	tzPos := q.next(loc.String())
	sql := fmt.Sprintf(`SELECT (occurred_at AT TIME ZONE $%d)::date AS d, count(*)
FROM events WHERE %s GROUP BY 1`, tzPos, q.clause())
	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	active := map[string]int{}
	for rows.Next() {
		var d time.Time
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return out, err
		}
		active[d.Format("2006-01-02")] = n
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	var hire *time.Time
	person := models.Person{Key: f.PersonKey}
	if p, err := s.GetPerson(ctx, f.PersonKey); err == nil {
		person = p
		out.office = p.Office
		hire = p.HireDate
	}

	// Особые дни с учётом корпоративного календаря праздников.
	special, err := s.EffectiveSpecialDays(ctx, person, f.From, f.To)
	if err != nil {
		return out, err
	}
	// Приоритет пересечений: отпуск > больничный > праздник.
	rank := map[string]int{models.DayVacation: 3, models.DaySick: 2, models.DayHoliday: 1}
	kinds := map[string]string{}
	for _, d := range special {
		key := d.Day.Format("2006-01-02")
		if rank[d.Kind] > rank[kinds[key]] {
			kinds[key] = d.Kind
		}
	}

	fromLoc := f.From.In(loc)
	start := time.Date(fromLoc.Year(), fromLoc.Month(), fromLoc.Day(), 0, 0, 0, 0, loc)
	// Дни до даты найма не считаются вовсе: у сотрудника не может быть ни
	// рабочих, ни выходных дней до прихода в компанию.
	if hire != nil {
		hireLoc := time.Date(hire.Year(), hire.Month(), hire.Day(), 0, 0, 0, 0, loc)
		if start.Before(hireLoc) {
			start = hireLoc
		}
	}
	end := f.To.In(loc)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		switch {
		case kinds[key] == models.DayVacation:
			out.vacation++
		case kinds[key] == models.DaySick:
			out.sick++
		case kinds[key] == models.DayHoliday:
			out.holiday++
		case d.Weekday() == time.Saturday || d.Weekday() == time.Sunday:
			out.weekend++
		default:
			out.working++
			switch n := active[key]; {
			case n == 0:
				out.idleWorking++
			case n == 1:
				out.activeWorking++
				out.buckets[0]++
			case n <= 5:
				out.activeWorking++
				out.buckets[1]++
			default:
				out.activeWorking++
				out.buckets[2]++
			}
		}
	}
	return out, nil
}

func buildHighlights(sum models.Summary, byType, prevByType []models.CountItem, prev aggregates) []models.Highlight {
	cur := map[string]int{}
	for _, it := range byType {
		cur[it.Key] = it.Count
	}
	old := map[string]int{}
	for _, it := range prevByType {
		old[it.Key] = it.Count
	}
	sumOf := func(m map[string]int, keys ...string) int {
		n := 0
		for _, k := range keys {
			n += m[k]
		}
		return n
	}

	type spec struct {
		key, label, unit, source string
		curV, prevV              float64
	}
	specs := []spec{
		{"total", "Всего событий", "", "", float64(sum.TotalEvents), float64(sum.PrevTotal)},
		{"active_days", "Активных дней", "дн.", "", float64(sum.ActiveDays), 0},
		{"jira_issues", "Задач затронуто", "", "jira",
			float64(sumOf(cur, string(models.TypeIssueCreated), string(models.TypeIssueAssigned), string(models.TypeIssueResolved))),
			float64(sumOf(old, string(models.TypeIssueCreated), string(models.TypeIssueAssigned), string(models.TypeIssueResolved)))},
		{"worklog", "Списано часов", "ч", "jira", sum.WorklogHours, prev.worklogSec / 3600},
		{"commits", "Коммитов", "", "gitlab", float64(cur[string(models.TypeCommit)]), float64(old[string(models.TypeCommit)])},
		{"mrs", "MR открыто / смёржено", "", "gitlab",
			float64(sumOf(cur, string(models.TypeMROpened), string(models.TypeMRMerged))),
			float64(sumOf(old, string(models.TypeMROpened), string(models.TypeMRMerged)))},
		{"reviews", "Ревью-комментариев", "", "gitlab",
			float64(cur[string(models.TypeReviewComment)]), float64(old[string(models.TypeReviewComment)])},
		{"slack", "Сообщений в Slack", "", "slack",
			float64(sumOf(cur, string(models.TypeSlackMessage), string(models.TypeSlackReply))),
			float64(sumOf(old, string(models.TypeSlackMessage), string(models.TypeSlackReply)))},
		{"docs", "Правок документов", "", "gdocs",
			float64(sumOf(cur, string(models.TypeDocEdit), string(models.TypeDocComment), string(models.TypeDocSuggest))),
			float64(sumOf(old, string(models.TypeDocEdit), string(models.TypeDocComment), string(models.TypeDocSuggest)))},
		{"meetings", "Встреч", "", "gcal",
			float64(sumOf(cur, string(models.TypeMeeting), string(models.TypeRecurring),
				string(models.TypeOneOnOne), string(models.TypeInterview))),
			float64(sumOf(old, string(models.TypeMeeting), string(models.TypeRecurring),
				string(models.TypeOneOnOne), string(models.TypeInterview)))},
		{"meeting_hours", "Часов во встречах", "ч", "gcal", sum.MeetingHours, prev.meetingSec / 3600},
		{"one_on_ones", "Встреч 1:1", "", "gcal",
			float64(cur[string(models.TypeOneOnOne)]), float64(old[string(models.TypeOneOnOne)])},
		{"interviews", "Собеседований", "", "gcal",
			float64(cur[string(models.TypeInterview)]), float64(old[string(models.TypeInterview)])},
		{"confluence", "Страниц Confluence создано / правок", "", "confluence",
			float64(sumOf(cur, string(models.TypeConfluencePageCreated), string(models.TypeConfluencePageEdited))),
			float64(sumOf(old, string(models.TypeConfluencePageCreated), string(models.TypeConfluencePageEdited)))},
		{"test_launches", "Запусков тестов", "", "allure",
			float64(cur[string(models.TypeAllureLaunch)]), float64(old[string(models.TypeAllureLaunch)])},
		{"testcases", "Тест-кейсов создано / изменено", "", "allure",
			float64(sumOf(cur, string(models.TypeAllureCaseCreated), string(models.TypeAllureCaseUpdated))),
			float64(sumOf(old, string(models.TypeAllureCaseCreated), string(models.TypeAllureCaseUpdated)))},
	}

	out := make([]models.Highlight, 0, len(specs))
	for _, sp := range specs {
		h := models.Highlight{Key: sp.key, Label: sp.label, Value: round1(sp.curV), Unit: sp.unit, Source: sp.source}
		if sp.prevV > 0 {
			h.Delta = round1((sp.curV - sp.prevV) / sp.prevV * 100)
			h.HasDelta = true
		} else if sp.curV > 0 && sp.key != "active_days" {
			h.Delta = 100
			h.HasDelta = false
		}
		out = append(out, h)
	}
	return out
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5*sign(f))) / 10
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// ComparePeople считает метрики продуктивности для набора людей за период —
// для страницы сравнения. Разбивка по типам одним запросом на всех; дни
// (рабочие/активные/отпуск) — по человеку, в его часовом поясе не различаем,
// используем общий loc запроса. Вторым значением возвращается шаг таймлайна
// спарклайнов: day для коротких периодов, week для длинных.
func (s *Store) ComparePeople(ctx context.Context, people []models.Person, from, to time.Time, loc *time.Location) ([]models.PersonMetrics, string, error) {
	granularity := "day"
	if to.Sub(from).Hours()/24 > 120 {
		granularity = "week"
	}
	if len(people) == 0 {
		return []models.PersonMetrics{}, granularity, nil
	}
	byKey := make(map[string]*models.PersonMetrics, len(people))
	out := make([]models.PersonMetrics, 0, len(people))
	keys := make([]string, 0, len(people))
	for _, p := range people {
		keys = append(keys, p.Key)
		out = append(out, models.PersonMetrics{
			Person:   p,
			BySource: map[string]int{},
			ByType:   map[string]int{},
		})
	}
	for i := range out {
		byKey[out[i].Person.Key] = &out[i]
	}

	const q = `
SELECT person_key, source, type, count(*), COALESCE(sum(effort), 0)
FROM events
WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2, 3`
	rows, err := s.pool.Query(ctx, q, keys, from, to)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var key, source, typ string
		var count int
		var effort float64
		if err := rows.Scan(&key, &source, &typ, &count, &effort); err != nil {
			return nil, "", err
		}
		m := byKey[key]
		if m == nil {
			continue
		}
		m.TotalEvents += count
		m.BySource[source] += count
		m.ByType[typ] += count
		switch {
		case typ == string(models.TypeWorklog):
			m.WorklogHours += effort / 3600
		case source == "gcal":
			m.MeetingHours += effort / 3600
		case source == "gitlab":
			m.LinesChanged += int64(effort)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	for i := range out {
		m := &out[i]
		agg, err := s.dayStats(ctx, models.EventFilter{PersonKey: m.Person.Key, From: from, To: to}, loc)
		if err != nil {
			return nil, "", err
		}
		m.WorkingDays = agg.working
		m.ActiveWorkingDays = agg.activeWorking
		m.IdleWorkingDays = agg.idleWorking
		m.VacationDays = agg.vacation
		m.SickDays = agg.sick
		m.WorklogHours = round1(m.WorklogHours)
		m.MeetingHours = round1(m.MeetingHours)
	}

	// Таймлайны для спарклайнов «волн активности» — одним запросом на всех.
	tq := fmt.Sprintf(`
SELECT person_key, date_trunc('%s', occurred_at AT TIME ZONE $4) AS bucket, count(*)
FROM events
WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2 ORDER BY 2`, granularity)
	tr, err := s.pool.Query(ctx, tq, keys, from, to, loc.String())
	if err != nil {
		return nil, "", err
	}
	defer tr.Close()
	for tr.Next() {
		var key string
		var bucket time.Time
		var n int
		if err := tr.Scan(&key, &bucket, &n); err != nil {
			return nil, "", err
		}
		if m := byKey[key]; m != nil {
			m.Timeline = append(m.Timeline, models.SparkPoint{Bucket: bucket, Count: n})
		}
	}
	if err := tr.Err(); err != nil {
		return nil, "", err
	}

	// Активные дни (включая выходные) — отдельным запросом на всех сразу.
	const qDays = `
SELECT person_key, count(DISTINCT date_trunc('day', occurred_at AT TIME ZONE $4))
FROM events
WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1`
	dr, err := s.pool.Query(ctx, qDays, keys, from, to, loc.String())
	if err != nil {
		return nil, "", err
	}
	defer dr.Close()
	for dr.Next() {
		var key string
		var n int
		if err := dr.Scan(&key, &n); err != nil {
			return nil, "", err
		}
		if m := byKey[key]; m != nil {
			m.ActiveDays = n
		}
	}
	return out, granularity, dr.Err()
}

// ActivityByDay возвращает число событий по датам (в часовом поясе loc).
func (s *Store) ActivityByDay(ctx context.Context, f models.EventFilter, loc *time.Location) (map[string]int, error) {
	q := newFilterSQL(f)
	tzPos := q.next(loc.String())
	sql := fmt.Sprintf(`SELECT (occurred_at AT TIME ZONE $%d)::date AS d, count(*)
FROM events WHERE %s GROUP BY 1`, tzPos, q.clause())
	rows, err := s.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d time.Time
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d.Format("2006-01-02")] = n
	}
	return out, rows.Err()
}

// ActivityByDayAll — число событий по датам для набора людей одним запросом.
func (s *Store) ActivityByDayAll(ctx context.Context, keys []string, from, to time.Time, loc *time.Location) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT person_key, (occurred_at AT TIME ZONE $4)::date AS d, count(*)
FROM events WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2`
	rows, err := s.pool.Query(ctx, q, keys, from, to, loc.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var d time.Time
		var n int
		if err := rows.Scan(&key, &d, &n); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = map[string]int{}
		}
		out[key][d.Format("2006-01-02")] = n
	}
	return out, rows.Err()
}

// SlackOnlyDaysAll — даты, где ВСЯ активность человека — события Slack
// (ни входов, ни задач, ни кода), для набора людей одним запросом.
// Используется правилом отклонений slack_only_days.
func (s *Store) SlackOnlyDaysAll(ctx context.Context, keys []string, from, to time.Time, loc *time.Location) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT person_key, (occurred_at AT TIME ZONE $4)::date AS d
FROM events WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2
HAVING count(*) FILTER (WHERE source <> 'slack') = 0`
	rows, err := s.pool.Query(ctx, q, keys, from, to, loc.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var d time.Time
		if err := rows.Scan(&key, &d); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][d.Format("2006-01-02")] = true
	}
	return out, rows.Err()
}

// DaySpan — границы активности одного дня (в часовом поясе запроса).
type DaySpan struct {
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
	Events int       `json:"events"`
}

// DaySpansAll — первое/последнее событие каждого дня для набора людей:
// из них считается длина рабочего дня.
func (s *Store) DaySpansAll(ctx context.Context, keys []string, from, to time.Time, loc *time.Location) (map[string]map[string]DaySpan, error) {
	out := map[string]map[string]DaySpan{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT person_key, (occurred_at AT TIME ZONE $4)::date AS d,
       min(occurred_at AT TIME ZONE $4), max(occurred_at AT TIME ZONE $4), count(*)
FROM events WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2`
	rows, err := s.pool.Query(ctx, q, keys, from, to, loc.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var d time.Time
		var span DaySpan
		if err := rows.Scan(&key, &d, &span.First, &span.Last, &span.Events); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = map[string]DaySpan{}
		}
		out[key][d.Format("2006-01-02")] = span
	}
	return out, rows.Err()
}

// TypeCountsAll — счётчики событий по типам для набора людей.
func (s *Store) TypeCountsAll(ctx context.Context, keys []string, from, to time.Time) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT person_key, type, count(*)
FROM events WHERE person_key = ANY($1) AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1, 2`
	rows, err := s.pool.Query(ctx, q, keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, typ string
		var n int
		if err := rows.Scan(&key, &typ, &n); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = map[string]int{}
		}
		out[key][typ] = n
	}
	return out, rows.Err()
}

// MROpenReview — открытый MR и момент первого отклика ревью (комментарий,
// апрув или мёрж любым человеком). FirstReview nil — отклика ещё не было.
type MROpenReview struct {
	PersonKey   string
	RefID       string
	Title       string
	Opened      time.Time
	FirstReview *time.Time
}

// MRReviewDelays — MR, открытые людьми за период, с временем первого отклика.
func (s *Store) MRReviewDelays(ctx context.Context, keys []string, from, to time.Time) ([]MROpenReview, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	const q = `
SELECT o.person_key, o.ref_id, o.title, o.occurred_at,
       (SELECT min(r.occurred_at) FROM events r
        WHERE r.ref_id = o.ref_id AND r.occurred_at > o.occurred_at
          AND r.type IN ('gitlab.review_comment', 'gitlab.mr_approved', 'gitlab.mr_merged'))
FROM events o
WHERE o.type = 'gitlab.mr_opened' AND o.person_key = ANY($1)
  AND o.occurred_at >= $2 AND o.occurred_at < $3`
	rows, err := s.pool.Query(ctx, q, keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MROpenReview
	for rows.Next() {
		var m MROpenReview
		if err := rows.Scan(&m.PersonKey, &m.RefID, &m.Title, &m.Opened, &m.FirstReview); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OneOnOnePeerCounts — число разных собеседников встреч 1:1 за период.
func (s *Store) OneOnOnePeerCounts(ctx context.Context, keys []string, from, to time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `
SELECT person_key, count(DISTINCT meta->>'peer')
FROM events
WHERE person_key = ANY($1) AND type = 'gcal.one_on_one'
  AND meta->>'peer' IS NOT NULL AND occurred_at >= $2 AND occurred_at < $3
GROUP BY 1`
	rows, err := s.pool.Query(ctx, q, keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, rows.Err()
}

// ListPersonDaysAll — особые дни набора людей одним запросом.
func (s *Store) ListPersonDaysAll(ctx context.Context, keys []string, from, to time.Time) (map[string][]models.PersonDay, error) {
	out := map[string][]models.PersonDay{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `SELECT person_key, day, kind, label FROM person_days
	           WHERE person_key = ANY($1) AND day >= $2::date AND day < $3::date ORDER BY day`
	rows, err := s.pool.Query(ctx, q, keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d models.PersonDay
		if err := rows.Scan(&d.PersonKey, &d.Day, &d.Kind, &d.Label); err != nil {
			return nil, err
		}
		out[d.PersonKey] = append(out[d.PersonKey], d)
	}
	return out, rows.Err()
}

// LastVacationDays — последний день отпуска (не позже before) для набора людей.
func (s *Store) LastVacationDays(ctx context.Context, keys []string, before time.Time) (map[string]*time.Time, error) {
	out := map[string]*time.Time{}
	if len(keys) == 0 {
		return out, nil
	}
	const q = `SELECT person_key, max(day) FROM person_days
	           WHERE person_key = ANY($1) AND kind = 'vacation' AND day <= $2::date GROUP BY 1`
	rows, err := s.pool.Query(ctx, q, keys, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var d *time.Time
		if err := rows.Scan(&key, &d); err != nil {
			return nil, err
		}
		out[key] = d
	}
	return out, rows.Err()
}

// MergeCompanyHolidays накладывает корпоративный календарь страны на особые
// дни человека (см. EffectiveSpecialDays); covered и comp — предзагруженные
// данные по стране, чтобы батч-обработка не ходила в БД по каждому человеку.
func MergeCompanyHolidays(base []models.PersonDay, personKey string, covered map[int]bool, comp []CompanyHoliday) []models.PersonDay {
	if len(covered) == 0 {
		return base
	}
	out := make([]models.PersonDay, 0, len(base)+len(comp))
	for _, d := range base {
		if d.Kind == models.DayHoliday && covered[d.Day.Year()] {
			continue
		}
		out = append(out, d)
	}
	for _, h := range comp {
		out = append(out, models.PersonDay{
			PersonKey: personKey, Day: h.Day, Kind: models.DayHoliday, Label: h.Label,
		})
	}
	return out
}

// EffectiveSpecialDays — особые дни человека с учётом корпоративного
// календаря праздников: отпуска и больничные всегда из person_days, а
// праздники для годов, покрытых корпоративным календарём страны сотрудника,
// берутся из него (записи Google за эти годы игнорируются).
func (s *Store) EffectiveSpecialDays(ctx context.Context, p models.Person, from, to time.Time) ([]models.PersonDay, error) {
	base, err := s.ListPersonDays(ctx, p.Key, from, to)
	if err != nil {
		return nil, err
	}
	country := models.CountryForOffice(p.Office)
	if country == "" {
		return base, nil
	}
	covered, err := s.CompanyHolidayYears(ctx, country)
	if err != nil {
		return nil, err
	}
	if len(covered) == 0 {
		return base, nil
	}
	out := make([]models.PersonDay, 0, len(base))
	for _, d := range base {
		if d.Kind == models.DayHoliday && covered[d.Day.Year()] {
			continue
		}
		out = append(out, d)
	}
	comp, err := s.ListCompanyHolidays(ctx, country, from, to)
	if err != nil {
		return nil, err
	}
	for _, h := range comp {
		out = append(out, models.PersonDay{
			PersonKey: p.Key, Day: h.Day, Kind: models.DayHoliday, Label: h.Label,
		})
	}
	return out, nil
}

// presenceDays возвращает даты присутствия человека: рабочие дни без
// отпуска, больничного и праздников, начиная с даты найма. Ключ — YYYY-MM-DD
// в часовом поясе loc.
func (s *Store) presenceDays(ctx context.Context, p models.Person, from, to time.Time, loc *time.Location) (map[string]bool, error) {
	special, err := s.EffectiveSpecialDays(ctx, p, from, to)
	if err != nil {
		return nil, err
	}
	off := map[string]bool{}
	for _, d := range special {
		off[d.Day.Format("2006-01-02")] = true
	}

	fromLoc := from.In(loc)
	start := time.Date(fromLoc.Year(), fromLoc.Month(), fromLoc.Day(), 0, 0, 0, 0, loc)
	if p.HireDate != nil {
		hire := time.Date(p.HireDate.Year(), p.HireDate.Month(), p.HireDate.Day(), 0, 0, 0, 0, loc)
		if start.Before(hire) {
			start = hire
		}
	}
	out := map[string]bool{}
	end := to.In(loc)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		if key := d.Format("2006-01-02"); !off[key] {
			out[key] = true
		}
	}
	return out, nil
}

// CompareTeams считает метрики команд: абсолютные события, разбивку по
// источникам, таймлайн и средневзвешенный ряд «событий на присутствующего
// сотрудника» (присутствие — по-дневно, с учётом отпусков, больничных,
// праздников и даты найма). Фильтры источников/типов/AI-оценки применяются
// из f; человек в f не используется.
func (s *Store) CompareTeams(ctx context.Context, teams map[string][]models.Person, f models.EventFilter, loc *time.Location) ([]models.TeamMetrics, string, error) {
	granularity := "day"
	if f.To.Sub(f.From).Hours()/24 > 120 {
		granularity = "week"
	}
	// Корзина даты: для недель — понедельник её недели.
	bucketOf := func(day string) string {
		if granularity == "day" {
			return day
		}
		t, err := time.ParseInLocation("2006-01-02", day, loc)
		if err != nil {
			return day
		}
		monday := t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
		return monday.Format("2006-01-02")
	}

	teamNames := make([]string, 0, len(teams))
	allKeys := []string{}
	teamByKey := map[string]string{}
	for name, members := range teams {
		teamNames = append(teamNames, name)
		for _, p := range members {
			allKeys = append(allKeys, p.Key)
			teamByKey[p.Key] = name
		}
	}
	sort.Strings(teamNames)

	type dayAgg struct {
		events   map[string]int // date → события команды
		presence map[string]int // date → присутствующих
		bySource map[string]int
		total    int
	}
	agg := map[string]*dayAgg{}
	for _, name := range teamNames {
		agg[name] = &dayAgg{events: map[string]int{}, presence: map[string]int{}, bySource: map[string]int{}}
	}

	if len(allKeys) > 0 {
		// События по людям и дням одним запросом, с фильтрами.
		where := []string{"person_key = ANY($1)", "occurred_at >= $2", "occurred_at < $3"}
		args := []any{allKeys, f.From, f.To}
		next := func(v any) int { args = append(args, v); return len(args) }
		tzPos := next(loc.String())
		if len(f.Sources) > 0 {
			where = append(where, fmt.Sprintf("source = ANY($%d)", next(f.Sources)))
		}
		if len(f.Types) > 0 {
			where = append(where, fmt.Sprintf("type = ANY($%d)", next(f.Types)))
		}
		if f.MinAIScore > 0 {
			where = append(where, fmt.Sprintf(
				"(source <> 'slack' OR COALESCE((meta->>'ai_score')::numeric, 10) >= $%d)", next(f.MinAIScore)))
		}
		sql := fmt.Sprintf(`
SELECT person_key, source, (occurred_at AT TIME ZONE $%d)::date AS d, count(*)
FROM events WHERE %s
GROUP BY 1, 2, 3`, tzPos, strings.Join(where, " AND "))
		rows, err := s.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, "", err
		}
		defer rows.Close()
		for rows.Next() {
			var key, source string
			var d time.Time
			var n int
			if err := rows.Scan(&key, &source, &d, &n); err != nil {
				return nil, "", err
			}
			a := agg[teamByKey[key]]
			if a == nil {
				continue
			}
			a.events[d.Format("2006-01-02")] += n
			a.bySource[source] += n
			a.total += n
		}
		if err := rows.Err(); err != nil {
			return nil, "", err
		}
	}

	// Присутствие — по каждому человеку.
	for name, members := range teams {
		for _, p := range members {
			pres, err := s.presenceDays(ctx, p, f.From, f.To, loc)
			if err != nil {
				return nil, "", err
			}
			for day := range pres {
				agg[name].presence[day]++
			}
		}
	}

	out := make([]models.TeamMetrics, 0, len(teamNames))
	for _, name := range teamNames {
		a := agg[name]
		// Сводим дни в корзины (день или неделя).
		type bucketAgg struct{ events, presence int }
		buckets := map[string]*bucketAgg{}
		add := func(day string) *bucketAgg {
			key := bucketOf(day)
			b := buckets[key]
			if b == nil {
				b = &bucketAgg{}
				buckets[key] = b
			}
			return b
		}
		for day, n := range a.events {
			add(day).events += n
		}
		for day, n := range a.presence {
			add(day).presence += n
		}
		keys := make([]string, 0, len(buckets))
		for k := range buckets {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		tm := models.TeamMetrics{
			Team:        name,
			Members:     len(teams[name]),
			TotalEvents: a.total,
			BySource:    a.bySource,
		}
		for _, k := range keys {
			b := buckets[k]
			t, _ := time.ParseInLocation("2006-01-02", k, time.UTC)
			tm.Timeline = append(tm.Timeline, models.SparkPoint{Bucket: t, Count: b.events})
			wp := models.WeightedPoint{Bucket: t, Events: b.events, Presence: b.presence}
			if b.presence > 0 {
				wp.Value = round2(float64(b.events) / float64(b.presence))
			}
			tm.Weighted = append(tm.Weighted, wp)
		}
		for _, n := range a.presence {
			tm.PersonDays += n
		}
		if tm.PersonDays > 0 {
			tm.EventsPerPersonDay = round2(float64(tm.TotalEvents) / float64(tm.PersonDays))
		}
		out = append(out, tm)
	}
	return out, granularity, nil
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5*sign(f))) / 100
}

// ---------- doc_links ----------

// SaveDocLinks сохраняет найденные в Jira ссылки на Google-документы.
func (s *Store) SaveDocLinks(ctx context.Context, links []models.DocLink) error {
	if len(links) == 0 {
		return nil
	}
	const q = `
INSERT INTO doc_links (doc_id, person_key, issue_key, doc_url, title, issue_title, found_in, discovered_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (doc_id, person_key, issue_key) DO UPDATE SET
    doc_url = EXCLUDED.doc_url,
    title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE doc_links.title END,
    issue_title = CASE WHEN EXCLUDED.issue_title <> '' THEN EXCLUDED.issue_title ELSE doc_links.issue_title END,
    found_in = EXCLUDED.found_in`
	batch := &pgx.Batch{}
	for _, l := range links {
		if l.DiscoveredAt.IsZero() {
			l.DiscoveredAt = time.Now().UTC()
		}
		batch.Queue(q, l.DocID, l.PersonKey, l.IssueKey, l.DocURL, l.Title, l.IssueTitle, l.FoundIn, l.DiscoveredAt)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range links {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("запись doc_links: %w", err)
		}
	}
	return nil
}

// EnrichDocLink дописывает метаданные документа после похода в Google API.
func (s *Store) EnrichDocLink(ctx context.Context, personKey, docID, title string, lastModified *time.Time, edits, comments int) error {
	const q = `
UPDATE doc_links SET
    enriched = true,
    title = CASE WHEN $3 <> '' THEN $3 ELSE title END,
    last_modified = $4,
    edit_count = $5,
    comment_count = $6
WHERE person_key = $1 AND doc_id = $2`
	_, err := s.pool.Exec(ctx, q, personKey, docID, title, lastModified, edits, comments)
	return err
}

// ListDocLinks возвращает документы, связанные с задачами пользователя.
func (s *Store) ListDocLinks(ctx context.Context, personKey string) ([]models.DocLink, error) {
	const q = `
SELECT doc_id, person_key, issue_key, doc_url, title, issue_title, found_in, discovered_at,
       enriched, last_modified, edit_count, comment_count
FROM doc_links WHERE person_key = $1
ORDER BY COALESCE(last_modified, discovered_at) DESC`
	rows, err := s.pool.Query(ctx, q, personKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.DocLink, 0, 32)
	for rows.Next() {
		var l models.DocLink
		if err := rows.Scan(&l.DocID, &l.PersonKey, &l.IssueKey, &l.DocURL, &l.Title, &l.IssueTitle,
			&l.FoundIn, &l.DiscoveredAt, &l.Enriched, &l.LastModified, &l.EditCount, &l.CommentCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PendingDocLinks возвращает id документов, которые ещё не обогащены.
func (s *Store) PendingDocLinks(ctx context.Context, personKey string) ([]string, error) {
	const q = `SELECT DISTINCT doc_id FROM doc_links WHERE person_key = $1`
	rows, err := s.pool.Query(ctx, q, personKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------- sync_runs ----------

// SaveSyncRun сохраняет состояние прогона.
func (s *Store) SaveSyncRun(ctx context.Context, r models.SyncRun) error {
	sources, err := json.Marshal(r.Sources)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO sync_runs (id, person_key, range_from, range_to, status, started_at, finished_at, sources, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (id) DO UPDATE SET
    status = EXCLUDED.status, finished_at = EXCLUDED.finished_at,
    sources = EXCLUDED.sources, error = EXCLUDED.error`
	_, err = s.pool.Exec(ctx, q, r.ID, r.PersonKey, r.From, r.To, string(r.Status), r.StartedAt, r.FinishedAt, sources, r.Error)
	return err
}

// ListSyncRuns возвращает историю прогонов.
func (s *Store) ListSyncRuns(ctx context.Context, personKey string, limit int) ([]models.SyncRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := `SELECT id, person_key, range_from, range_to, status, started_at, finished_at, sources, error
	      FROM sync_runs`
	args := []any{}
	if personKey != "" {
		q += ` WHERE person_key = $1`
		args = append(args, personKey)
	}
	q += fmt.Sprintf(` ORDER BY started_at DESC LIMIT %d`, limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.SyncRun, 0, limit)
	for rows.Next() {
		var r models.SyncRun
		var status string
		var sources []byte
		if err := rows.Scan(&r.ID, &r.PersonKey, &r.From, &r.To, &status, &r.StartedAt, &r.FinishedAt, &sources, &r.Error); err != nil {
			return nil, err
		}
		r.Status = models.SyncStatus(status)
		_ = json.Unmarshal(sources, &r.Sources)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSyncRun возвращает прогон по id.
func (s *Store) GetSyncRun(ctx context.Context, id string) (models.SyncRun, error) {
	const q = `SELECT id, person_key, range_from, range_to, status, started_at, finished_at, sources, error
	           FROM sync_runs WHERE id = $1`
	var r models.SyncRun
	var status string
	var sources []byte
	err := s.pool.QueryRow(ctx, q, id).Scan(&r.ID, &r.PersonKey, &r.From, &r.To, &status, &r.StartedAt, &r.FinishedAt, &sources, &r.Error)
	if err != nil {
		if err == pgx.ErrNoRows {
			return models.SyncRun{}, ErrNotFound
		}
		return models.SyncRun{}, err
	}
	r.Status = models.SyncStatus(status)
	_ = json.Unmarshal(sources, &r.Sources)
	return r, nil
}

// ---------- удаление собранных данных ----------

// PurgeResult — что удалено (или будет удалено) по запросу очистки.
type PurgeResult struct {
	Events    int                `json:"events"`
	BySource  []models.CountItem `json:"by_source"`
	SyncRuns  int                `json:"sync_runs"`
	DocLinks  int                `json:"doc_links_reset"`
	Preview   bool               `json:"preview"`
	PersonKey string             `json:"person_key"`
	From      time.Time          `json:"from"`
	To        time.Time          `json:"to"`
}

// PurgePreview считает, сколько данных попадёт под удаление, ничего не меняя.
// Отдельный шаг нужен, чтобы удаление в UI подтверждалось осознанно.
func (s *Store) PurgePreview(ctx context.Context, f models.EventFilter, withRuns bool) (PurgeResult, error) {
	res := PurgeResult{Preview: true, PersonKey: f.PersonKey, From: f.From, To: f.To}

	total, err := s.CountEvents(ctx, f)
	if err != nil {
		return res, err
	}
	res.Events = total

	bySource, err := s.Breakdown(ctx, f, "source")
	if err != nil {
		return res, err
	}
	res.BySource = bySource

	if withRuns {
		const q = `SELECT count(*) FROM sync_runs
		           WHERE person_key = $1 AND started_at >= $2 AND started_at < $3`
		if err := s.pool.QueryRow(ctx, q, f.PersonKey, f.From, f.To).Scan(&res.SyncRuns); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Purge удаляет собранные события за период. Опционально — историю прогонов за
// тот же период и агрегаты по документам.
//
// Пустой PersonKey или нулевой период не принимаются: «удалить всё» должно быть
// осознанным действием, а не следствием незаполненного фильтра.
func (s *Store) Purge(ctx context.Context, f models.EventFilter, withRuns, resetDocs bool) (PurgeResult, error) {
	res := PurgeResult{PersonKey: f.PersonKey, From: f.From, To: f.To}
	if f.PersonKey == "" {
		return res, fmt.Errorf("удаление: не указан пользователь")
	}
	if !f.From.Before(f.To) {
		return res, fmt.Errorf("удаление: некорректный период")
	}

	// Разбивку считаем до удаления — после неё уже не по чему.
	bySource, err := s.Breakdown(ctx, f, "source")
	if err != nil {
		return res, err
	}
	res.BySource = bySource

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := newFilterSQL(f)
	tag, err := tx.Exec(ctx, "DELETE FROM events WHERE "+q.clause(), q.args...)
	if err != nil {
		return res, fmt.Errorf("удаление событий: %w", err)
	}
	res.Events = int(tag.RowsAffected())

	if withRuns {
		tag, err := tx.Exec(ctx,
			`DELETE FROM sync_runs WHERE person_key = $1 AND started_at >= $2 AND started_at < $3`,
			f.PersonKey, f.From, f.To)
		if err != nil {
			return res, fmt.Errorf("удаление истории прогонов: %w", err)
		}
		res.SyncRuns = int(tag.RowsAffected())
	}

	if resetDocs {
		// Счётчики правок и комментариев считались из удалённых событий,
		// поэтому оставлять их нельзя. Сами связки «задача ↔ документ» приходят
		// из Jira и переживают очистку.
		tag, err := tx.Exec(ctx,
			`UPDATE doc_links SET enriched = false, edit_count = 0, comment_count = 0, last_modified = NULL
			 WHERE person_key = $1`, f.PersonKey)
		if err != nil {
			return res, fmt.Errorf("сброс агрегатов по документам: %w", err)
		}
		res.DocLinks = int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// DistinctProjects возвращает список проектов, встречавшихся в событиях —
// для наполнения фильтров в UI.
func (s *Store) DistinctProjects(ctx context.Context, personKey string) ([]models.CountItem, error) {
	const q = `
SELECT project, COALESCE(NULLIF(max(project_name), ''), project) AS name, count(*)
FROM events WHERE person_key = $1 AND project <> ''
GROUP BY project ORDER BY count(*) DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q, personKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CountItem
	for rows.Next() {
		var it models.CountItem
		if err := rows.Scan(&it.Key, &it.Label, &it.Count); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SourceMonthCell — активность человека в одной системе за один месяц.
type SourceMonthCell struct {
	Events int
	Effort float64
}

// SourceMonthly агрегирует события одной системы по людям и календарным
// месяцам (границы месяцев — в loc). Возвращает ячейки по ключу человека и
// месяца «YYYY-MM» и единицу «усилия» источника: самая частая непустая
// effort_unit в выборке (у Claude — requests, у GitLab — lines, у Jira —
// seconds ворклогов). Effort суммируется только по строкам с этой единицей,
// чтобы не складывать минуты с письмами у смешанных источников (gwork).
func (s *Store) SourceMonthly(ctx context.Context, people []models.Person, source string, from, to time.Time, loc *time.Location) (map[string]map[string]SourceMonthCell, string, error) {
	out := make(map[string]map[string]SourceMonthCell, len(people))
	if len(people) == 0 {
		return out, "", nil
	}
	keys := make([]string, 0, len(people))
	for _, p := range people {
		keys = append(keys, p.Key)
	}

	var unit string
	const unitQ = `
SELECT effort_unit
FROM events
WHERE source = $1 AND person_key = ANY($2) AND occurred_at >= $3 AND occurred_at < $4 AND effort_unit <> ''
GROUP BY 1
ORDER BY count(*) DESC
LIMIT 1`
	if err := s.pool.QueryRow(ctx, unitQ, source, keys, from, to).Scan(&unit); err != nil && err != pgx.ErrNoRows {
		return nil, "", fmt.Errorf("source monthly: единица усилия: %w", err)
	}

	const q = `
SELECT person_key,
       to_char(date_trunc('month', occurred_at AT TIME ZONE $5), 'YYYY-MM') AS month,
       count(*),
       COALESCE(sum(effort) FILTER (WHERE effort_unit = $6), 0)
FROM events
WHERE source = $1 AND person_key = ANY($2) AND occurred_at >= $3 AND occurred_at < $4
GROUP BY 1, 2`
	rows, err := s.pool.Query(ctx, q, source, keys, from, to, loc.String(), unit)
	if err != nil {
		return nil, "", fmt.Errorf("source monthly: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, month string
		var cell SourceMonthCell
		if err := rows.Scan(&key, &month, &cell.Events, &cell.Effort); err != nil {
			return nil, "", err
		}
		m := out[key]
		if m == nil {
			m = map[string]SourceMonthCell{}
			out[key] = m
		}
		m[month] = cell
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, unit, nil
}
