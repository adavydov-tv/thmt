package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
)

// Вкладка «По системе» сравнения продуктивности: активность людей в одной
// системе (источнике) по календарным месяцам периода — таблица «человек ×
// месяц» с итогом. Ячейка несёт число событий и «усилие» в единице источника
// (запросы Claude, строки GitLab, секунды ворклогов Jira), чтобы фронт мог
// переключать метрику без повторного запроса.

// monthBucket — календарный месяц, обрезанный границами периода.
type monthBucket struct {
	Key  string    `json:"key"` // YYYY-MM
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Partial — период покрывает месяц не целиком (первый/последний месяц
	// окна): сравнивать такие столбцы с полными месяцами надо с оговоркой.
	Partial bool `json:"partial"`
}

// monthBuckets разбивает [from, to) на календарные месяцы в loc.
func monthBuckets(from, to time.Time, loc *time.Location) []monthBucket {
	var out []monthBucket
	if !from.Before(to) {
		return out
	}
	f := from.In(loc)
	cur := time.Date(f.Year(), f.Month(), 1, 0, 0, 0, 0, loc)
	for cur.Before(to) {
		next := cur.AddDate(0, 1, 0)
		b := monthBucket{Key: cur.Format("2006-01"), From: cur, To: next}
		if b.From.Before(from) {
			b.From, b.Partial = from, true
		}
		if b.To.After(to) {
			b.To, b.Partial = to, true
		}
		out = append(out, b)
		cur = next
	}
	return out
}

type bySourceCell struct {
	Events int     `json:"events"`
	Effort float64 `json:"effort"`
}

type bySourcePerson struct {
	Person      models.Person  `json:"person"`
	Cells       []bySourceCell `json:"cells"` // по порядку buckets
	TotalEvents int            `json:"total_events"`
	TotalEffort float64        `json:"total_effort"`
	// HR — стаж, оценки Performance Review и PIP (см. hrfacts.go).
	HR hrFacts `json:"hr"`
}

// comparePeople — общий отбор людей для страниц сравнения: область видимости
// роли, мультивыбор команд, направление, без бывших сотрудников (HRDB).
// Недоступность HRDB не ломает страницу — просто без фильтра «бывших».
func (s *Server) comparePeople(r *http.Request, wantTeams map[string]bool, area string) ([]models.Person, error) {
	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		return nil, err
	}
	exEmails := map[string]bool{}
	if s.hrdb != nil {
		if ex, err := s.hrdb.Current().ExEmployeeEmails(r.Context()); err != nil {
			s.log.Warn("не удалось получить список бывших сотрудников", "err", err)
		} else {
			exEmails = ex
		}
	}
	scope := s.requestScope(r)
	filtered := make([]models.Person, 0, len(people))
	for _, p := range people {
		if scope != nil && !scope.persons[p.Key] {
			continue
		}
		if len(wantTeams) > 0 && !wantTeams[strings.ToLower(p.Team)] {
			continue
		}
		if area != "" && !strings.EqualFold(p.Area, area) {
			continue
		}
		if exEmails[strings.ToLower(strings.TrimSpace(p.Email))] {
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered, nil
}

// enrichTitles дописывает людям должность и грейд из HRDB-кэша по e-mail
// (в БД они не хранятся). Без HRDB — тихо ничего не делает.
func (s *Server) enrichTitles(r *http.Request, people []models.Person) {
	if s.hrdb == nil {
		return
	}
	hc := s.hrdb.Current()
	if hc == nil {
		return
	}
	emps, err := hc.ListEmployees(r.Context())
	if err != nil {
		return
	}
	type hr struct{ title, grade string }
	byEmail := make(map[string]hr, len(emps))
	for _, e := range emps {
		if e.Email != "" {
			byEmail[strings.ToLower(strings.TrimSpace(e.Email))] = hr{e.Title, e.Grade}
		}
	}
	for i := range people {
		if h, ok := byEmail[strings.ToLower(strings.TrimSpace(people[i].Email))]; ok {
			people[i].Title, people[i].Grade = h.title, h.grade
		}
	}
}

// handleCompareBySource — GET /api/stats/by-source?source=claude&from&to&tz&team&area.
func (s *Server) handleCompareBySource(w http.ResponseWriter, r *http.Request) {
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
	source := strings.ToLower(strings.TrimSpace(q.Get("source")))
	if source == "" {
		writeErr(w, http.StatusBadRequest, errors.New("укажите source"))
		return
	}
	known := false
	for _, k := range models.AllSources {
		if string(k) == source {
			known = true
			break
		}
	}
	if !known {
		writeErr(w, http.StatusBadRequest, errors.New("неизвестный source: "+source))
		return
	}
	wantTeams := map[string]bool{}
	for _, t := range multi(q["team"]) {
		wantTeams[strings.ToLower(t)] = true
	}

	people, err := s.comparePeople(r, wantTeams, strings.TrimSpace(q.Get("area")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.enrichTitles(r, people)

	cells, unit, err := s.store.SourceMonthly(r.Context(), people, source, from, to, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	buckets := monthBuckets(from, to, loc)
	cycles, facts := s.hrFacts(r, people)
	rows := make([]bySourcePerson, 0, len(people))
	for _, p := range people {
		row := bySourcePerson{Person: p, Cells: make([]bySourceCell, len(buckets)), HR: facts[p.Key]}
		for i, b := range buckets {
			var c storage.SourceMonthCell
			if m := cells[p.Key]; m != nil {
				c = m[b.Key]
			}
			row.Cells[i] = bySourceCell{Events: c.Events, Effort: c.Effort}
			row.TotalEvents += c.Events
			row.TotalEffort += c.Effort
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":          from,
		"to":            to,
		"source":        source,
		"effort_unit":   unit,
		"buckets":       orEmpty(buckets),
		"review_cycles": orEmpty(cycles),
		"people":        rows,
	})
}
