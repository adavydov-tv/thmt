package api

// Дашборд «абьюзеров»: люди без активности или с низкой активностью за период.
// Метрики — из ComparePeople; дни низкой активности — из ShallowDays (та же
// логика, что матрица и правило low_activity_days).

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

type abuserRow struct {
	Person            models.Person `json:"person"`
	Cluster           string        `json:"cluster,omitempty"`
	TotalEvents       int           `json:"total_events"`
	WorkingDays       int           `json:"working_days"`
	ActiveWorkingDays int           `json:"active_working_days"`
	IdleWorkingDays   int           `json:"idle_working_days"`
	LowActivityDays   int           `json:"low_activity_days"`
	ActiveRatio       float64       `json:"active_ratio"`
	// Reason — почему попал в список: no_activity | idle | low_activity.
	Reason string `json:"reason"`
}

func (s *Server) handleAbusers(w http.ResponseWriter, r *http.Request) {
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

	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	scope := s.requestScope(r)
	exEmails := map[string]bool{}
	if s.hrdb.Current() != nil {
		if ex, err := s.hrdb.Current().ExEmployeeEmails(r.Context()); err == nil {
			exEmails = ex
		}
	}
	filtered := people[:0]
	for _, p := range people {
		if scope != nil && !scope.persons[p.Key] {
			continue
		}
		if exEmails[strings.ToLower(strings.TrimSpace(p.Email))] {
			continue
		}
		filtered = append(filtered, p)
	}

	metrics, _, err := s.store.ComparePeople(r.Context(), filtered, from, to, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	shallowCfg := s.loadShallowConfig(r.Context())
	out := []abuserRow{}
	for _, m := range metrics {
		p := m.Person
		// Дни низкой активности (рабочие) — как в правиле/матрице.
		low := 0
		fmts, _ := s.store.ListWorkFormatHistory(r.Context(), p.Key)
		rdates := remoteDates(fmts, from, to, loc)
		if days, derr := s.store.ShallowDays(r.Context(), p.Key, from, to, loc,
			shallowCfg.typesFor(p.Area), rdates); derr == nil {
			for _, ds := range days {
				t, _ := time.Parse("2006-01-02", ds)
				if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
					continue
				}
				low++
			}
		}

		row := abuserRow{
			Person:            p,
			TotalEvents:       m.TotalEvents,
			WorkingDays:       m.WorkingDays,
			ActiveWorkingDays: m.ActiveWorkingDays,
			IdleWorkingDays:   m.IdleWorkingDays,
			LowActivityDays:   low,
		}
		if m.WorkingDays > 0 {
			row.ActiveRatio = float64(m.ActiveWorkingDays) / float64(m.WorkingDays)
		}

		// Классификация и отбор: показываем только проблемных.
		switch {
		case m.WorkingDays > 0 && m.ActiveWorkingDays == 0:
			row.Reason = "no_activity"
		case m.IdleWorkingDays >= 3:
			row.Reason = "idle"
		case low >= 3:
			row.Reason = "low_activity"
		default:
			continue // без заметных проблем — не абьюзер
		}
		out = append(out, row)
	}

	// Сортировка: сначала без активности, затем по «тяжести» (простои + низкие дни).
	sort.Slice(out, func(i, j int) bool {
		wi := out[i].IdleWorkingDays + out[i].LowActivityDays
		wj := out[j].IdleWorkingDays + out[j].LowActivityDays
		ai := out[i].Reason == "no_activity"
		aj := out[j].Reason == "no_activity"
		if ai != aj {
			return ai
		}
		return wi > wj
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"from":    from,
		"to":      to,
		"abusers": out,
	})
}
