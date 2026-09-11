package api

// Сравнение по формату работы (office / hybrid / remote): анализ продуктивности
// и ОТКЛОНЕНИЙ в разрезе Направления / кластера / грейда. Один эндпоинт отдаёт
// данные сразу для всех представлений фронта:
//   - матрица (строки = измерение, столбцы = форматы);
//   - тепловая карта (формат × группа отклонений);
//   - скаттер (точка на человека: продуктивность ↔ отклонения);
//   - тренд по бакетам (линия событий/чел на формат).
// Отклонения нормируются НА ЧЕЛОВЕКА (сырые счётчики отражают лишь численность).
// Метрики — из ComparePeople; формат/грейд/кластер/Area — из HRDB и карточек.

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

var workFormats = []string{"office", "hybrid", "remote"}

// deviationGroups — порядок групп отклонений (совпадает с фронтовым ruleHelp).
var deviationGroups = []string{"sick", "rest", "overtime", "activity", "team"}

// ruleGroup — правило → группа (зеркало web/src/lib/ruleHelp.ts). Нужен для
// разбивки отклонений по группам на стороне сервера.
var ruleGroup = map[string]string{
	"sick_adjacent": "sick", "sick_monthly": "sick",
	"no_vacation": "rest", "work_on_vacation": "rest", "vacation_recovery": "rest",
	"role_vacation_overlap": "rest",
	"overtime_idle":         "overtime", "overtime_low": "overtime",
	"offday_activity": "overtime", "long_workday": "overtime",
	"idle_streak": "activity", "slack_only_days": "activity", "low_activity_days": "activity",
	"activity_drop": "activity", "remote_zero": "activity", "remote_drop": "activity",
	"wfh_zero": "activity", "steady_rhythm": "activity",
	"onboarding_rise": "activity", "onboarding_flat": "activity", "role_inactive": "activity",
	"bus_factor": "team", "no_reviewers": "team", "slow_review": "team",
	"review_champion": "team", "mentor_one_on_ones": "team",
}

// lowProdRules — отклонения «низкой продуктивности»: набор по умолчанию, когда
// пользователь не выбрал конкретные правила (негативные, про выработку).
var lowProdRules = map[string]bool{
	"idle_streak":       true,
	"activity_drop":     true,
	"low_activity_days": true,
	"slack_only_days":   true,
	"remote_zero":       true,
	"remote_drop":       true,
	"wfh_zero":          true,
	"role_inactive":     true,
}

type formatCell struct {
	People          int                `json:"people"`
	AvgEventsDay    float64            `json:"avg_events_day"`    // все события / рабочий день
	UsefulEventsDay float64            `json:"useful_events_day"` // не-Slack события / рабочий день
	ActiveRatio     float64            `json:"active_ratio"`      // доля активных рабочих дней
	DevPerPerson    float64            `json:"dev_per_person"`    // выбранные отклонения на человека
	DevByRule       map[string]float64 `json:"dev_by_rule"`       // правило → отклонений на человека (группы фронт складывает сам)

	events        int
	usefulEvents  int
	workingDays   int
	activeWorking int
	devTotal      int
	devRuleRaw    map[string]int
}

type formatRow struct {
	Key   string                 `json:"key"`
	Cells map[string]*formatCell `json:"cells"`
}

type scatterPoint struct {
	Name      string  `json:"name"`
	Format    string  `json:"format"`
	Area      string  `json:"area"`
	Grade     string  `json:"grade"`
	EventsDay float64 `json:"events_day"`
	UsefulDay float64 `json:"useful_day"`
	Dev       int     `json:"dev"`
}

type formatTrend struct {
	Granularity string               `json:"granularity"`
	Buckets     []time.Time          `json:"buckets"`
	Series      map[string][]float64 `json:"series"` // формат → события/чел по бакетам
}

func (s *Server) handleFormatCompare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	loc := time.UTC
	if tz := q.Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	to := parseTimeOr(q.Get("to"), time.Now().UTC())
	from := parseTimeOr(q.Get("from"), to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}
	dim := strings.ToLower(strings.TrimSpace(q.Get("dim")))
	if dim != "area" && dim != "cluster" && dim != "grade" {
		dim = "area"
	}
	wantAreas := csvSet(q.Get("areas"))
	wantClusters := csvSet(q.Get("clusters"))
	wantGrades := csvSet(q.Get("grades"))
	selRules := csvSet(q.Get("rules")) // выбранные правила; пусто → lowProd по умолчанию

	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	scope := s.requestScope(r)

	// HRDB: формат работы и грейд по e-mail, оргструктура для кластера.
	format := map[string]string{}
	grade := map[string]string{}
	var structure map[string]hrdb.StructureUnit
	if hc := s.hrdb.Current(); hc != nil {
		if emps, err := hc.ListEmployees(r.Context()); err == nil {
			for _, e := range emps {
				em := strings.ToLower(strings.TrimSpace(e.Email))
				if em == "" {
					continue
				}
				if f := models.NormalizeWorkFormat(e.WorkFormat); f != "" {
					format[em] = f
				}
				g := strings.TrimSpace(e.Grade)
				if g == "" {
					if fields := strings.Fields(e.Title); len(fields) > 0 {
						g = strings.Trim(fields[0], ",;.:")
					}
				}
				grade[em] = g
			}
		}
		if st, err := hc.Structure(r.Context()); err == nil {
			structure = st
		}
	}

	// Фильтрация людей (scope + мульти Area/Cluster/Grade) + сбор доступных
	// значений для выпадашек.
	areaSet, clusterSet, gradeSet := map[string]bool{}, map[string]bool{}, map[string]bool{}
	filtered := people[:0]
	for _, p := range people {
		if scope != nil && !scope.persons[p.Key] {
			continue
		}
		em := strings.ToLower(strings.TrimSpace(p.Email))
		cl := hrdb.TeamChain(structure, p.Team).Cluster
		if p.Area != "" {
			areaSet[p.Area] = true
		}
		if cl != "" {
			clusterSet[cl] = true
		}
		if g := grade[em]; g != "" {
			gradeSet[g] = true
		}
		if len(wantAreas) > 0 && !wantAreas[strings.ToLower(p.Area)] {
			continue
		}
		if len(wantClusters) > 0 && !wantClusters[strings.ToLower(cl)] {
			continue
		}
		if len(wantGrades) > 0 && !wantGrades[strings.ToLower(grade[em])] {
			continue
		}
		filtered = append(filtered, p)
	}
	areas, clusters, grades := sortedKeys(areaSet), sortedKeys(clusterSet), sortedKeys(gradeSet)

	// Отклонения за период по человеку и правилу.
	devByPerson := map[string]map[string]int{}
	if items, _, derr := s.deviations(r, deviationParams{from: from, to: to, loc: loc}); derr == nil {
		for _, v := range items {
			m := devByPerson[v.PersonKey]
			if m == nil {
				m = map[string]int{}
				devByPerson[v.PersonKey] = m
			}
			m[v.Rule]++
		}
	}
	selected := func(rule string) bool {
		if len(selRules) > 0 {
			return selRules[rule]
		}
		return lowProdRules[rule]
	}
	countsSelected := func(rules map[string]int) (total int, byRule map[string]int) {
		byRule = map[string]int{}
		for rule, n := range rules {
			if !selected(rule) {
				continue
			}
			total += n
			byRule[rule] += n
		}
		return
	}

	metrics, gran, err := s.store.ComparePeople(r.Context(), filtered, from, to, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	rows := map[string]*formatRow{}
	totals := map[string]*formatCell{}
	for _, f := range workFormats {
		totals[f] = newCell()
	}
	scatter := make([]scatterPoint, 0, len(metrics))
	// Тренд: события/чел по бакетам на формат. Собираем из per-person Timeline.
	trendSum := map[string]map[time.Time]int{} // формат → бакет → сумма событий
	bucketSet := map[time.Time]bool{}
	peoplePerFormat := map[string]int{}
	for _, f := range workFormats {
		trendSum[f] = map[time.Time]int{}
	}

	for _, m := range metrics {
		em := strings.ToLower(strings.TrimSpace(m.Person.Email))
		f := format[em]
		if f == "" {
			continue // неизвестный формат — в сравнение не берём
		}
		var key string
		switch dim {
		case "cluster":
			key = hrdb.TeamChain(structure, m.Person.Team).Cluster
		case "grade":
			key = grade[em]
		default:
			key = m.Person.Area
		}
		if strings.TrimSpace(key) == "" {
			key = "—"
		}
		row := rows[key]
		if row == nil {
			row = &formatRow{Key: key, Cells: map[string]*formatCell{}}
			for _, wf := range workFormats {
				row.Cells[wf] = newCell()
			}
			rows[key] = row
		}
		devTotal, devByRule := countsSelected(devByPerson[m.Person.Key])
		accum(row.Cells[f], m, devTotal, devByRule)
		accum(totals[f], m, devTotal, devByRule)

		useful := m.TotalEvents - m.BySource["slack"]
		var evDay, usDay float64
		if m.WorkingDays > 0 {
			evDay = float64(m.TotalEvents) / float64(m.WorkingDays)
			usDay = float64(useful) / float64(m.WorkingDays)
		}
		scatter = append(scatter, scatterPoint{
			Name: m.Person.DisplayName, Format: f, Area: m.Person.Area, Grade: grade[em],
			EventsDay: round1(evDay), UsefulDay: round1(usDay), Dev: devTotal,
		})

		peoplePerFormat[f]++
		for _, sp := range m.Timeline {
			trendSum[f][sp.Bucket] += sp.Count
			bucketSet[sp.Bucket] = true
		}
	}

	for _, row := range rows {
		for _, c := range row.Cells {
			finalizeCell(c)
		}
	}
	for _, c := range totals {
		finalizeCell(c)
	}

	out := make([]*formatRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })

	// Ось бакетов тренда + нормировка на человека формата.
	buckets := make([]time.Time, 0, len(bucketSet))
	for b := range bucketSet {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Before(buckets[j]) })
	series := map[string][]float64{}
	for _, f := range workFormats {
		vals := make([]float64, len(buckets))
		n := peoplePerFormat[f]
		for i, b := range buckets {
			if n > 0 {
				vals[i] = round1(float64(trendSum[f][b]) / float64(n))
			}
		}
		series[f] = vals
	}

	// Эффективно учитываемые правила (для колонок карты и раскрытия таблицы).
	effRules := []string{}
	for rule := range ruleGroup {
		if selected(rule) {
			effRules = append(effRules, rule)
		}
	}
	sort.Strings(effRules)

	writeJSON(w, http.StatusOK, map[string]any{
		"dim":      dim,
		"formats":  workFormats,
		"rows":     out,
		"totals":   totals,
		"scatter":  scatter,
		"trend":    formatTrend{Granularity: gran, Buckets: buckets, Series: series},
		"groups":   deviationGroups,
		"rules":    effRules,
		"areas":    areas,
		"clusters": clusters,
		"grades":   grades,
		"from":     from,
		"to":       to,
	})
}

func newCell() *formatCell {
	return &formatCell{DevByRule: map[string]float64{}, devRuleRaw: map[string]int{}}
}

func accum(c *formatCell, m models.PersonMetrics, devTotal int, devByRule map[string]int) {
	c.People++
	c.events += m.TotalEvents
	c.usefulEvents += m.TotalEvents - m.BySource["slack"]
	c.workingDays += m.WorkingDays
	c.activeWorking += m.ActiveWorkingDays
	c.devTotal += devTotal
	for rule, n := range devByRule {
		c.devRuleRaw[rule] += n
	}
}

func finalizeCell(c *formatCell) {
	if c.workingDays > 0 {
		c.AvgEventsDay = round1(float64(c.events) / float64(c.workingDays))
		c.UsefulEventsDay = round1(float64(c.usefulEvents) / float64(c.workingDays))
		c.ActiveRatio = float64(c.activeWorking) / float64(c.workingDays)
	}
	if c.People > 0 {
		c.DevPerPerson = round2(float64(c.devTotal) / float64(c.People))
		for rule, n := range c.devRuleRaw {
			c.DevByRule[rule] = round2(float64(n) / float64(c.People))
		}
	}
}

// csvSet разбирает CSV-параметр в множество (в нижнем регистре); пусто → nil.
func csvSet(v string) map[string]bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
