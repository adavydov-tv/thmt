package api

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/perfreview"
)

// HR-факты о человеке для отчётных таблиц: стаж от даты найма (HRDB), оценки
// Performance Review по последним циклам и план PIP (Jira DC, проект PR).

// reviewCyclesShown — сколько последних циклов оценки показывать в таблице.
const reviewCyclesShown = 3

type hrFacts struct {
	// TenureYears — стаж в годах от даты найма с одним знаком; nil — даты нет.
	TenureYears *float64 `json:"tenure_years,omitempty"`
	// Reviews — оценки по порядку review_cycles ответа; nil в позиции — нет оценки.
	Reviews []*perfreview.Rating `json:"reviews,omitempty"`
	// PIP — самый свежий план PIP (активный приоритетнее закрытого); nil — нет.
	PIP *perfreview.Plan `json:"pip,omitempty"`
}

// tenureYears — полных лет с десятыми от hire до now (2.1 = два года и месяц).
func tenureYears(hire *time.Time, now time.Time) *float64 {
	if hire == nil || hire.IsZero() || hire.After(now) {
		return nil
	}
	y := now.Sub(*hire).Hours() / 24 / 365.25
	v := math.Round(y*10) / 10
	return &v
}

// hrFacts собирает факты по людям. Циклы — последние reviewCyclesShown по
// хронологии. Сопоставление с проектом PR: HRDB-ключ сотрудника (через e-mail
// в HRDB-кэше), запасной путь — по имени. Без Jira DC возвращает только стаж.
func (s *Server) hrFacts(r *http.Request, people []models.Person) ([]perfreview.Cycle, map[string]hrFacts) {
	now := time.Now()
	out := make(map[string]hrFacts, len(people))
	for _, p := range people {
		out[p.Key] = hrFacts{TenureYears: tenureYears(p.HireDate, now)}
	}
	if s.perf == nil {
		return nil, out
	}
	snap, err := s.perf.Snapshot(r.Context())
	if err != nil {
		s.log.Warn("performance review: снапшот недоступен", "err", err)
		return nil, out
	}
	cycles := snap.Cycles
	if len(cycles) > reviewCyclesShown {
		cycles = cycles[len(cycles)-reviewCyclesShown:]
	}

	// e-mail → HRDB-ключ из кэша HRDB (Employee.Key — ключ объекта Assets).
	hrdbByEmail := map[string]string{}
	if s.hrdb != nil {
		if hc := s.hrdb.Current(); hc != nil {
			if emps, err := hc.ListEmployees(r.Context()); err == nil {
				for _, e := range emps {
					if e.Email != "" {
						hrdbByEmail[strings.ToLower(strings.TrimSpace(e.Email))] = e.Key
					}
				}
			}
		}
	}

	for _, p := range people {
		var emp *perfreview.Employee
		if key := hrdbByEmail[strings.ToLower(strings.TrimSpace(p.Email))]; key != "" {
			emp = snap.ByHRDB[key]
		}
		if emp == nil {
			emp = snap.ByName[perfreview.NormalizeName(p.DisplayName)]
		}
		if emp == nil {
			continue
		}
		f := out[p.Key]
		f.Reviews = make([]*perfreview.Rating, len(cycles))
		for i, c := range cycles {
			if rt, ok := emp.Ratings[c.Key]; ok {
				rt := rt
				f.Reviews[i] = &rt
			}
		}
		for i := range emp.Plans {
			pl := emp.Plans[i]
			if pl.Type != "PIP" {
				continue
			}
			if f.PIP == nil || (pl.Active && !f.PIP.Active) {
				f.PIP = &pl
			}
		}
		out[p.Key] = f
	}
	return cycles, out
}
