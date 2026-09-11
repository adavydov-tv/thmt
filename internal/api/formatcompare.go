package api

// Сравнение продуктивности по формату работы (office / hybrid / remote)
// в разрезе направления (Area), кластера или грейда. Метрики берутся из
// ComparePeople, формат/грейд/кластер — из HRDB (кэш + оргструктура).

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

// lowProdRules — отклонения «низкой продуктивности»: простои, спад активности,
// низкая/только-Slack активность, нулевая активность в remote/WFH.
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
	People        int     `json:"people"`
	AvgEventsDay  float64 `json:"avg_events_day"` // события на рабочий день
	ActiveRatio   float64 `json:"active_ratio"`   // доля активных рабочих дней
	Deviations    int     `json:"deviations"`     // отклонения низкой продуктивности
	events        int
	workingDays   int
	activeWorking int
}

type formatRow struct {
	Key   string                 `json:"key"`
	Cells map[string]*formatCell `json:"cells"`
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
	wantCluster := strings.TrimSpace(q.Get("cluster")) // фильтр по кластеру
	wantGrade := strings.TrimSpace(q.Get("grade"))     // фильтр по грейду

	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	scope := s.requestScope(r)

	// HRDB: формат работы, грейд (по e-mail) и оргструктура (для кластера).
	format := map[string]string{} // email → office|hybrid|remote
	grade := map[string]string{}  // email → грейд
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
				// Грейд: отдельный атрибут HRDB, а если его нет — первое слово
				// должности («Senior Backend Development» → «Senior»).
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

	// Фильтруем людей: доступ (scope) + кластер + грейд. Заодно собираем
	// все кластеры/грейды доступных людей — для выпадашек фильтров.
	clusterSet, gradeSet := map[string]bool{}, map[string]bool{}
	filtered := people[:0]
	for _, p := range people {
		if scope != nil && !scope.persons[p.Key] {
			continue
		}
		em := strings.ToLower(strings.TrimSpace(p.Email))
		cl := hrdb.TeamChain(structure, p.Team).Cluster
		if cl != "" {
			clusterSet[cl] = true
		}
		if g := grade[em]; g != "" {
			gradeSet[g] = true
		}
		if wantCluster != "" && !strings.EqualFold(cl, wantCluster) {
			continue
		}
		if wantGrade != "" && !strings.EqualFold(grade[em], wantGrade) {
			continue
		}
		filtered = append(filtered, p)
	}
	clusters, grades := sortedKeys(clusterSet), sortedKeys(gradeSet)

	// Отклонения низкой продуктивности за период — по person_key.
	lowDevByPerson := map[string]int{}
	if items, _, derr := s.deviations(r, deviationParams{from: from, to: to, loc: loc}); derr == nil {
		for _, v := range items {
			if lowProdRules[v.Rule] {
				lowDevByPerson[v.PersonKey]++
			}
		}
	}

	metrics, _, err := s.store.ComparePeople(r.Context(), filtered, from, to, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	rows := map[string]*formatRow{}
	totals := map[string]*formatCell{}
	for _, f := range workFormats {
		totals[f] = &formatCell{}
	}
	for _, m := range metrics {
		em := strings.ToLower(strings.TrimSpace(m.Person.Email))
		f := format[em]
		if f == "" {
			continue // неизвестный формат — в сравнение форматов не берём
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
				row.Cells[wf] = &formatCell{}
			}
			rows[key] = row
		}
		dev := lowDevByPerson[m.Person.Key]
		accum(row.Cells[f], m, dev)
		accum(totals[f], m, dev)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"dim":      dim,
		"formats":  workFormats,
		"rows":     out,
		"totals":   totals,
		"clusters": clusters,
		"grades":   grades,
		"from":     from,
		"to":       to,
	})
}

func accum(c *formatCell, m models.PersonMetrics, deviations int) {
	c.People++
	c.events += m.TotalEvents
	c.workingDays += m.WorkingDays
	c.activeWorking += m.ActiveWorkingDays
	c.Deviations += deviations
}

func finalizeCell(c *formatCell) {
	if c.workingDays > 0 {
		c.AvgEventsDay = float64(c.events) / float64(c.workingDays)
		c.ActiveRatio = float64(c.activeWorking) / float64(c.workingDays)
	}
}
