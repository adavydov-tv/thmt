package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/overtime"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
)

// devCacheTTL — время жизни рассчитанных отклонений: правила детерминированы
// по собранным данным, поэтому кэш инвалидируется завершением сбора, а TTL —
// подстраховка.
const devCacheTTL = 10 * time.Minute

type devCacheEntry struct {
	items []violationItem
	note  string
	at    time.Time
}

func (p deviationParams) cacheKey() string {
	return strings.Join([]string{
		p.from.Format(time.RFC3339), p.to.Format(time.RFC3339), p.loc.String(),
		strings.ToLower(p.team), strings.ToLower(p.cluster), strings.ToLower(p.dept), p.personKey,
	}, "|")
}

// deviations — кэширующая обёртка над collectDeviations.
func (s *Server) deviations(r *http.Request, p deviationParams) ([]violationItem, string, error) {
	key := p.cacheKey()
	s.devMu.Lock()
	if e, ok := s.devCache[key]; ok && time.Since(e.at) < devCacheTTL {
		s.devMu.Unlock()
		return e.items, e.note, nil
	}
	s.devMu.Unlock()

	items, note, err := s.collectDeviations(r, p)
	if err != nil {
		return nil, "", err
	}
	// Выключенные в настройках правила отбрасываются после расчёта: сам
	// расчёт дешёвый, зато включение правила обратно не требует пересборки.
	if disabled := s.disabledRules(r.Context()); len(disabled) > 0 {
		kept := items[:0]
		for _, v := range items {
			if !disabled[v.Rule] {
				kept = append(kept, v)
			}
		}
		items = kept
	}
	s.devMu.Lock()
	s.devCache[key] = devCacheEntry{items: items, note: note, at: time.Now()}
	s.devMu.Unlock()
	return items, note, nil
}

// disabledRules — множество выключенных в настройках правил отклонений.
func (s *Server) disabledRules(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	v, err := s.store.GetSetting(ctx, "disabled_rules")
	if err != nil || v == "" {
		return out
	}
	for _, r := range strings.Split(v, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out[r] = true
		}
	}
	return out
}

func (s *Server) handleGetDisabledRules(w http.ResponseWriter, r *http.Request) {
	disabled := s.disabledRules(r.Context())
	list := make([]string, 0, len(disabled))
	for k := range disabled {
		list = append(list, k)
	}
	sort.Strings(list)
	writeJSON(w, http.StatusOK, map[string]any{"disabled": list})
}

func (s *Server) handleSetDisabledRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disabled []string `json:"disabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	clean := make([]string, 0, len(req.Disabled))
	for _, x := range req.Disabled {
		if x = strings.TrimSpace(x); x != "" {
			clean = append(clean, x)
		}
	}
	sort.Strings(clean)
	if err := s.store.SetSetting(r.Context(), "disabled_rules", strings.Join(clean, ",")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.InvalidateDeviations()
	s.log.Info("правила отклонений обновлены", "disabled", clean)
	writeJSON(w, http.StatusOK, map[string]any{"disabled": clean})
}

// scopeViolations режет находки по HRDB-скоупу роли lead (кэш отклонений
// общий, поэтому фильтр применяется на выдаче, а не при расчёте).
func (s *Server) scopeViolations(r *http.Request, items []violationItem) []violationItem {
	scope := s.requestScope(r)
	if scope == nil {
		return items
	}
	kept := make([]violationItem, 0, len(items))
	for _, v := range items {
		if scope.persons[v.PersonKey] {
			kept = append(kept, v)
		}
	}
	return kept
}

// InvalidateDeviations сбрасывает кэш отклонений — вызывается после
// завершения любого сбора данных.
func (s *Server) InvalidateDeviations() {
	s.devMu.Lock()
	s.devCache = map[string]devCacheEntry{}
	s.devMu.Unlock()
	// Кэш овертаймов не трогаем: заявки в Jira VAC не зависят от наших
	// сборов, у них свой TTL и фоновый прогрев — сброс здесь заставлял бы
	// первую загрузку отклонений после каждого сбора ходить в сеть.
}

// ---------- отклонения ----------

// violationItem — одна находка вкладки «Отклонения».
type violationItem struct {
	PersonKey  string   `json:"person_key"`
	Person     string   `json:"person"`
	Team       string   `json:"team,omitempty"`
	Cluster    string   `json:"cluster,omitempty"`
	Department string   `json:"department,omitempty"`
	Rule       string   `json:"rule"`
	Severity   string   `json:"severity"` // warn | info
	Dates      []string `json:"dates"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	URL        string   `json:"url,omitempty"`
}

// deviationParams — фильтры выборки отклонений.
type deviationParams struct {
	from, to  time.Time
	loc       *time.Location
	team      string
	cluster   string
	dept      string
	personKey string
}

func (s *Server) deviationParams(r *http.Request) (deviationParams, error) {
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
		return deviationParams{}, errors.New("from должен быть раньше to")
	}
	return deviationParams{
		from: from, to: to, loc: loc,
		team:      strings.TrimSpace(q.Get("team")),
		cluster:   strings.TrimSpace(q.Get("cluster")),
		dept:      strings.TrimSpace(q.Get("department")),
		personKey: strings.TrimSpace(personKey(r)),
	}, nil
}

// handleViolations считает отклонения за период (JSON для вкладки).
func (s *Server) handleViolations(w http.ResponseWriter, r *http.Request) {
	p, err := s.deviationParams(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	items, note, err := s.deviations(r, p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	items = s.scopeViolations(r, items)
	writeJSON(w, http.StatusOK, map[string]any{
		"from": p.from, "to": p.to,
		"overtime_enabled": s.overtime != nil,
		"overtime_note":    note,
		"violations":       orEmpty(items),
	})
}

// handleViolationsExport выгружает отклонения кластера/департамента в CSV.
func (s *Server) handleViolationsExport(w http.ResponseWriter, r *http.Request) {
	p, err := s.deviationParams(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	items, _, err := s.deviations(r, p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	items = s.scopeViolations(r, items)

	name := "deviations"
	if p.cluster != "" {
		name += "-" + strings.ReplaceAll(strings.ToLower(p.cluster), " ", "_")
	}
	if p.dept != "" {
		name += "-" + strings.ReplaceAll(strings.ToLower(p.dept), " ", "_")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s-%s.csv"`, name, p.from.Format("2006-01-02"), p.to.Format("2006-01-02")))
	// BOM — чтобы Excel открыл UTF-8 с кириллицей без танцев.
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"Кластер", "Департамент", "Команда", "Сотрудник", "Правило", "Серьёзность", "Даты", "Описание", "Ссылка"})
	for _, v := range items {
		_ = cw.Write([]string{
			v.Cluster, v.Department, v.Team, v.Person, v.Title, v.Severity,
			strings.Join(v.Dates, ", "), v.Detail, v.URL,
		})
	}
	cw.Flush()
}

// collectDeviations применяет все правила и обогащает находки положением в
// оргструктуре (кластер/департамент по цепочке команды).
func (s *Server) collectDeviations(r *http.Request, p deviationParams) ([]violationItem, string, error) {
	ctx := r.Context()
	people, err := s.store.ListPeople(ctx)
	if err != nil {
		return nil, "", err
	}
	exEmails := map[string]bool{}
	structure := map[string]hrdb.StructureUnit{}
	if s.hrdb.Current() != nil {
		if ex, err := s.hrdb.Current().ExEmployeeEmails(ctx); err == nil {
			exEmails = ex
		}
		if st, err := s.hrdb.Current().Structure(ctx); err != nil {
			s.log.Warn("оргструктура HRDB недоступна", "err", err)
		} else {
			structure = st
		}
	}
	chainOf := func(team string) hrdb.TeamChainInfo {
		return hrdb.TeamChain(structure, team)
	}

	// Овертаймы за период — одним запросом, затем матчинг по людям.
	overtimeNote := ""
	var entries []overtime.Entry
	if s.overtime != nil {
		entries, err = s.overtime.Fetch(ctx, p.from, p.to)
		if err != nil {
			overtimeNote = "овертаймы недоступны: " + err.Error()
			s.log.Warn("не удалось получить овертаймы", "err", err)
			err = nil
		}
	} else {
		overtimeNote = "овертаймы не настроены (OVERTIME_JIRA_TOKEN)"
	}

	// Отбор людей под фильтры — до похода в БД.
	var filtered []models.Person
	for _, person := range people {
		if exEmails[strings.ToLower(strings.TrimSpace(person.Email))] {
			continue
		}
		chain := chainOf(person.Team)
		if p.team != "" && !strings.EqualFold(person.Team, p.team) {
			continue
		}
		if p.cluster != "" && !strings.EqualFold(chain.Cluster, p.cluster) {
			continue
		}
		if p.dept != "" && !strings.EqualFold(chain.Department, p.dept) {
			continue
		}
		if p.personKey != "" && person.Key != p.personKey {
			continue
		}
		filtered = append(filtered, person)
	}

	// Все данные по людям — батчами, а не по 4 запроса на человека:
	// на этом держится скорость вкладки.
	keys := make([]string, 0, len(filtered))
	for _, person := range filtered {
		keys = append(keys, person.Key)
	}
	pad := 7 * 24 * time.Hour
	activityAll, err := s.store.ActivityByDayAll(ctx, keys, p.from, p.to, p.loc)
	if err != nil {
		return nil, "", err
	}
	daysAll, err := s.store.ListPersonDaysAll(ctx, keys, p.from.Add(-pad), p.to.Add(pad))
	if err != nil {
		return nil, "", err
	}
	lastVacs, err := s.store.LastVacationDays(ctx, keys, p.to)
	if err != nil {
		return nil, "", err
	}
	spansAll, err := s.store.DaySpansAll(ctx, keys, p.from, p.to, p.loc)
	if err != nil {
		return nil, "", err
	}
	typeCountsAll, err := s.store.TypeCountsAll(ctx, keys, p.from, p.to)
	if err != nil {
		return nil, "", err
	}
	mrDelays, err := s.store.MRReviewDelays(ctx, keys, p.from, p.to)
	if err != nil {
		return nil, "", err
	}
	peerCounts, err := s.store.OneOnOnePeerCounts(ctx, keys, p.from, p.to)
	if err != nil {
		return nil, "", err
	}
	hybridAll, err := s.store.ListHybridHistoryAll(ctx, keys)
	if err != nil {
		return nil, "", err
	}
	formatAll, err := s.store.ListWorkFormatHistoryAll(ctx, keys)
	if err != nil {
		return nil, "", err
	}
	slackOnlyAll, err := s.store.SlackOnlyDaysAll(ctx, keys, p.from, p.to, p.loc)
	if err != nil {
		return nil, "", err
	}
	// Дни низкой активности — той же логикой, что матрица (per-area набор
	// «поверхностных» типов + непосещённые встречи не считаются работой).
	// Набор зависит от Area, поэтому считаем по каждому человеку отдельно.
	shallowCfg := s.loadShallowConfig(ctx)
	lowActivityAll := map[string]map[string]bool{}
	for _, person := range filtered {
		rdates := remoteDates(formatAll[person.Key], p.from, p.to, p.loc)
		days, derr := s.store.ShallowDays(ctx, person.Key, p.from, p.to, p.loc, shallowCfg.typesFor(person.Area), rdates)
		if derr != nil {
			s.log.Warn("дни низкой активности для отклонений не вычислены", "person", person.Key, "err", derr)
			continue
		}
		m := make(map[string]bool, len(days))
		for _, d := range days {
			m[d] = true
		}
		lowActivityAll[person.Key] = m
	}
	// Корпоративные праздники — по странам, один раз на страну.
	type countryHolidays struct {
		covered map[int]bool
		comp    []storage.CompanyHoliday
	}
	byCountry := map[string]countryHolidays{}
	for _, person := range filtered {
		country := models.CountryForOffice(person.Office)
		if country == "" {
			continue
		}
		if _, ok := byCountry[country]; ok {
			continue
		}
		covered, err := s.store.CompanyHolidayYears(ctx, country)
		if err != nil {
			return nil, "", err
		}
		comp, err := s.store.ListCompanyHolidays(ctx, country, p.from.Add(-pad), p.to.Add(pad))
		if err != nil {
			return nil, "", err
		}
		byCountry[country] = countryHolidays{covered: covered, comp: comp}
	}

	type roleKey struct{ team, area string }
	roleMembers := map[roleKey]int{}
	roleVacations := map[roleKey]map[string]int{} // день → в отпуске
	kindsByPerson := map[string]map[string]string{}
	personByKey := map[string]models.Person{}

	var out []violationItem
	for _, person := range filtered {
		chain := chainOf(person.Team)
		special := daysAll[person.Key]
		if ch, ok := byCountry[models.CountryForOffice(person.Office)]; ok {
			special = storage.MergeCompanyHolidays(special, person.Key, ch.covered, ch.comp)
		}
		data := personData{
			special:     special,
			activity:    activityAll[person.Key],
			lastVac:     lastVacs[person.Key],
			spans:       spansAll[person.Key],
			peers:       peerCounts[person.Key],
			hybrid:      hybridAll[person.Key],
			format:      formatAll[person.Key],
			slackOnly:   slackOnlyAll[person.Key],
			lowActivity: lowActivityAll[person.Key],
		}
		items, vacDates, kinds := s.personDeviations(person, p, matchEntries(entries, person), data)
		for i := range items {
			items[i].Cluster = chain.Cluster
			items[i].Department = chain.Department
		}
		out = append(out, items...)
		kindsByPerson[person.Key] = kinds
		personByKey[person.Key] = person

		if person.Team != "" && person.Area != "" {
			rk := roleKey{team: person.Team, area: person.Area}
			roleMembers[rk]++
			if roleVacations[rk] == nil {
				roleVacations[rk] = map[string]int{}
			}
			for _, d := range vacDates {
				roleVacations[rk][d]++
			}
		}
	}

	// Командные и ролевые правила — поверх собранных пер-персональных данных.
	out = append(out, s.teamDeviations(p, filtered, chainOf, activityAll, typeCountsAll, kindsByPerson)...)
	out = append(out, s.reviewDeviations(p, mrDelays, personByKey, chainOf)...)
	out = append(out, s.roleChampionDeviations(filtered, typeCountsAll, chainOf)...)

	// Правило: больше половины сотрудников роли в отпуске одновременно.
	for rk, total := range roleMembers {
		if total < 2 {
			continue
		}
		var days []string
		for day, n := range roleVacations[rk] {
			if n*2 > total {
				days = append(days, day)
			}
		}
		if len(days) == 0 {
			continue
		}
		sort.Strings(days)
		chain := chainOf(rk.team)
		out = append(out, violationItem{
			Person:     fmt.Sprintf("%s · %s", rk.team, rk.area),
			Team:       rk.team,
			Cluster:    chain.Cluster,
			Department: chain.Department,
			Rule:       "role_vacation_overlap",
			Severity:   "warn",
			Dates:      capDates(days, 8),
			Title:      "Больше половины роли в отпуске",
			Detail: fmt.Sprintf("Роль «%s» в команде %s: в %d дн. периода в отпуске одновременно более 50%% из %d чел.",
				rk.area, rk.team, len(days), total),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "warn"
		}
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		if out[i].Department != out[j].Department {
			return out[i].Department < out[j].Department
		}
		return out[i].Person < out[j].Person
	})
	return out, overtimeNote, nil
}

// matchEntries отбирает овертаймы человека: поле Employee сверяется с
// именем, e-mail и его локальной частью.
func matchEntries(entries []overtime.Entry, p models.Person) []overtime.Entry {
	ids := map[string]bool{}
	add := func(s string) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			ids[s] = true
			if i := strings.Index(s, "@"); i > 0 {
				ids[s[:i]] = true
			}
		}
	}
	add(p.Email)
	add(p.GoogleEmail)
	add(p.DisplayName)
	add(p.GitLabUser)
	var out []overtime.Entry
	for _, e := range entries {
		if ids[strings.ToLower(strings.TrimSpace(e.Employee))] {
			out = append(out, e)
		}
	}
	return out
}

// personData — предзагруженные батчами данные одного человека.
type personData struct {
	special  []models.PersonDay
	activity map[string]int
	lastVac  *time.Time
	spans    map[string]storage.DaySpan
	peers    int
	// hybrid — история изменений шаблона гибридных дней (журнал Assets + HCM).
	hybrid []models.HybridChange
	// format — история формата работы (Office | Hybrid | Remote): в ремоут- и
	// офис-периоды гибридный шаблон не действует.
	format []models.HybridChange
	// slackOnly — даты, где вся активность человека состоит из событий Slack.
	slackOnly map[string]bool
	// lowActivity — даты низкой активности (per-area набор «поверхностных»
	// типов; та же логика, что у матрицы активности).
	lowActivity map[string]bool
}

// personDeviations применяет пер-персональные правила к предзагруженным
// данным (в БД не ходит); дополнительно возвращает даты отпуска в периоде и
// карту видов дней — их используют командные правила.
func (s *Server) personDeviations(person models.Person, p deviationParams, overtimes []overtime.Entry,
	data personData) ([]violationItem, []string, map[string]string) {

	special, activity, lastVac := data.special, data.activity, data.lastVac
	from, to, loc := p.from, p.to, p.loc
	rank := map[string]int{models.DayVacation: 3, models.DaySick: 2, models.DayHoliday: 1}
	kinds := map[string]string{}
	for _, d := range special {
		key := d.Day.Format("2006-01-02")
		if rank[d.Kind] > rank[kinds[key]] {
			kinds[key] = d.Kind
		}
	}
	isOff := func(day time.Time) bool {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			return true
		}
		return kinds[day.Format("2006-01-02")] == models.DayHoliday
	}

	totalActivity := 0
	for _, n := range activity {
		totalActivity += n
	}

	base := violationItem{PersonKey: person.Key, Person: person.DisplayName, Team: person.Team}
	var out []violationItem

	// Больничные и отпуска за период.
	var sickDays []time.Time
	var vacDates []string
	for _, d := range special {
		if d.Day.Before(from) || !d.Day.Before(to) {
			continue
		}
		switch d.Kind {
		case models.DaySick:
			sickDays = append(sickDays, d.Day)
		case models.DayVacation:
			vacDates = append(vacDates, d.Day.Format("2006-01-02"))
		}
	}
	sort.Slice(sickDays, func(i, j int) bool { return sickDays[i].Before(sickDays[j]) })

	// Правило 1: короткий больничный, примыкающий к выходным/праздникам.
	for _, run := range sickRuns(sickDays) {
		if len(run) > 2 {
			continue
		}
		before := isOff(run[0].AddDate(0, 0, -1))
		after := isOff(run[len(run)-1].AddDate(0, 0, 1))
		if !before && !after {
			continue
		}
		v := base
		v.Rule = "sick_adjacent"
		v.Severity = "warn"
		for _, d := range run {
			v.Dates = append(v.Dates, d.Format("2006-01-02"))
		}
		v.Title = "Больничный впритык к выходным"
		switch {
		case before && after:
			v.Detail = "Больничный целиком между выходными/праздниками."
		case after:
			v.Detail = "Больничный прямо перед выходными/праздником."
		default:
			v.Detail = "Больничный сразу после выходных/праздника."
		}
		out = append(out, v)
	}

	// Правило 2: больничные из месяца в месяц (2+ последовательных месяца).
	months := map[string]int{}
	for _, d := range sickDays {
		months[d.Format("2006-01")]++
	}
	if streak := longestMonthStreak(months); len(streak) >= 2 {
		v := base
		v.Rule = "sick_monthly"
		v.Severity = "warn"
		v.Dates = streak
		total := 0
		for _, m := range streak {
			total += months[m]
		}
		v.Title = "Больничные из месяца в месяц"
		v.Detail = fmt.Sprintf("Больничные в %d месяцах подряд (%s), всего %d дн.",
			len(streak), strings.Join(streak, ", "), total)
		out = append(out, v)
	}

	// Правило 3: давно не был в отпуске (считается к концу периода).
	ref := person.HireDate
	if lastVac != nil && (ref == nil || lastVac.After(*ref)) {
		ref = lastVac
	}
	if ref != nil {
		monthsNoVac := int(to.Sub(*ref).Hours() / 24 / 30)
		if monthsNoVac >= 3 {
			v := base
			v.Rule = "no_vacation"
			v.Severity = "info"
			v.Title = "Не был в отпуске более 3 месяцев"
			if monthsNoVac >= 6 {
				v.Severity = "warn"
				v.Title = "Не был в отпуске более 6 месяцев"
			}
			since := "с найма"
			if lastVac != nil && (person.HireDate == nil || lastVac.After(*person.HireDate)) {
				since = "последний отпуск " + lastVac.Format("2006-01-02")
			}
			v.Dates = []string{ref.Format("2006-01-02")}
			v.Detail = fmt.Sprintf("Около %d мес. без отпуска (%s).", monthsNoVac, since)
			out = append(out, v)
		}
	}

	// Правило 4: молчание — нет активности более 2 рабочих дней подряд.
	// Только для людей, у которых за период вообще есть события: иначе это
	// просто несобранные данные, а не отклонение.
	if totalActivity > 0 {
		var streaks []string
		runStart, runLen := "", 0
		flush := func() {
			if runLen > 2 {
				streaks = append(streaks, fmt.Sprintf("%s (%d дн.)", runStart, runLen))
			}
			runStart, runLen = "", 0
		}
		fromLoc := from.In(loc)
		for d := time.Date(fromLoc.Year(), fromLoc.Month(), fromLoc.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
				continue // выходные/отпуск/больничный молчание не образуют и не рвут
			}
			if person.HireDate != nil && d.Before(*person.HireDate) {
				continue
			}
			if activity[key] == 0 {
				if runLen == 0 {
					runStart = key
				}
				runLen++
			} else {
				flush()
			}
		}
		flush()
		if len(streaks) > 0 {
			v := base
			v.Rule = "idle_streak"
			v.Severity = "warn"
			v.Dates = capDates(streaks, 6)
			v.Title = "Без активности более 2 рабочих дней подряд"
			v.Detail = fmt.Sprintf("Периоды молчания (рабочие дни, без отпусков и больничных): %s.",
				strings.Join(capDates(streaks, 6), "; "))
			out = append(out, v)
		}
	}

	// Правило: дни, где вся активность — только Slack (ни задач, ни кода,
	// ни документов, ни даже входа в аккаунт Google). Выходные, отпуска,
	// больничные и дни до найма не считаются.
	if totalActivity > 0 && len(data.slackOnly) > 0 {
		var slackDays []string
		startLoc := from.In(loc)
		for d := time.Date(startLoc.Year(), startLoc.Month(), startLoc.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
				continue
			}
			if person.HireDate != nil && d.Before(*person.HireDate) {
				continue
			}
			if data.slackOnly[key] && activity[key] > 0 {
				slackDays = append(slackDays, key)
			}
		}
		if len(slackDays) > 0 {
			v := base
			v.Rule = "slack_only_days"
			v.Severity = "warn"
			v.Dates = capDates(slackDays, 6)
			v.Title = "Активность только в Slack"
			v.Detail = fmt.Sprintf("Рабочих дней, где вся активность — сообщения и реакции Slack: %d (%s).",
				len(slackDays), strings.Join(capDates(slackDays, 6), ", "))
			out = append(out, v)
		}
	}

	// Правило: рабочие дни низкой активности — вся активность из «поверхностного»
	// набора для направления (чат, входы, непосещённые встречи), настраивается
	// на вкладке «Низкая активность». Выходные/отпуск/больничный/до найма не в счёт.
	if totalActivity > 0 && len(data.lowActivity) > 0 {
		var lowDays []string
		startLoc := from.In(loc)
		for d := time.Date(startLoc.Year(), startLoc.Month(), startLoc.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
				continue
			}
			if person.HireDate != nil && d.Before(*person.HireDate) {
				continue
			}
			if data.lowActivity[key] && activity[key] > 0 {
				lowDays = append(lowDays, key)
			}
		}
		if len(lowDays) > 0 {
			v := base
			v.Rule = "low_activity_days"
			v.Severity = "warn"
			v.Dates = capDates(lowDays, 6)
			v.Title = "Дни с низкой активностью"
			v.Detail = fmt.Sprintf("Рабочих дней с низкой активностью (только «поверхностные» действия): %d (%s).",
				len(lowDays), strings.Join(capDates(lowDays, 6), ", "))
			out = append(out, v)
		}
	}

	// Правила по гибридным дням (Hybrid remote days из HRDB): нулевая
	// активность в день из дома и заметное снижение против офисных дней.
	// Шаблон берётся на конкретную дату из истории изменений (журнал Assets):
	// смена гибридных дней посреди периода не даёт ложных отклонений.
	// Считаем только людей с событиями за период — иначе это несобранные
	// данные, а не отклонение.
	if totalActivity > 0 && (person.HybridDays != "" || len(data.hybrid) > 0) {
		var remoteZero []string
		remoteDays, remoteEvents := 0, 0
		officeDays, officeEvents := 0, 0
		startLoc := from.In(loc)
		for d := time.Date(startLoc.Year(), startLoc.Month(), startLoc.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
				continue
			}
			if person.HireDate != nil && d.Before(*person.HireDate) {
				continue
			}
			hybridSet := models.ParseWeekdays(models.HybridDaysAt(data.hybrid, person.HybridDays, d))
			// В ремоут- и офис-периоды гибридный шаблон не действует: у
			// ремоута нет «дней из дома» (он весь из дома), у офиса — тем более.
			if f := models.NormalizeWorkFormat(models.HybridDaysAt(data.format, "", d)); f == "remote" || f == "office" {
				hybridSet = nil
			}
			if hybridSet[d.Weekday()] {
				remoteDays++
				remoteEvents += activity[key]
				if activity[key] == 0 {
					remoteZero = append(remoteZero, key)
				}
			} else {
				officeDays++
				officeEvents += activity[key]
			}
		}
		if len(remoteZero) > 0 {
			v := base
			v.Rule = "remote_zero"
			v.Severity = "warn"
			v.Dates = capDates(remoteZero, 8)
			v.Title = "Нулевая активность в день из дома"
			v.Detail = fmt.Sprintf("В %d из %d гибридных дней (%s) не найдено ни одного события.",
				len(remoteZero), remoteDays, person.HybridDays)
			out = append(out, v)
		}
		if remoteDays >= 3 && officeDays >= 5 && officeEvents > 0 {
			remoteAvg := float64(remoteEvents) / float64(remoteDays)
			officeAvg := float64(officeEvents) / float64(officeDays)
			if officeAvg >= 3 && remoteAvg < officeAvg*0.5 {
				v := base
				v.Rule = "remote_drop"
				v.Severity = "warn"
				v.Title = "Снижение активности в дни из дома"
				v.Detail = fmt.Sprintf("В гибридные дни (%s) в среднем %.1f событий против %.1f в офисные — падение на %d%%.",
					person.HybridDays, remoteAvg, officeAvg, int((1-remoteAvg/officeAvg)*100))
				out = append(out, v)
			}
		}
	}

	// Правило по заявкам WFH из VAC (надёжнее HRDB-шаблона): рабочий день из
	// дома по одобренной заявке, а активности в системах ноль. Считаем только
	// людей с событиями за период — иначе это несобранные данные.
	if totalActivity > 0 {
		var wfhZero []string
		wfhTotal := 0
		for _, d := range special {
			if d.Kind != models.DayRemote || d.Day.Before(from) || !d.Day.Before(to) {
				continue
			}
			key := d.Day.Format("2006-01-02")
			// Отпуск/больничный/праздник в тот же день важнее — не WFH-провал.
			if kinds[key] == models.DayVacation || kinds[key] == models.DaySick ||
				kinds[key] == models.DayHoliday {
				continue
			}
			if person.HireDate != nil && d.Day.Before(*person.HireDate) {
				continue
			}
			wfhTotal++
			if activity[key] == 0 {
				wfhZero = append(wfhZero, key)
			}
		}
		if len(wfhZero) > 0 {
			v := base
			v.Rule = "wfh_zero"
			v.Severity = "warn"
			v.Dates = capDates(wfhZero, 8)
			v.Title = "Нулевая активность в WFH-день по заявке"
			v.Detail = fmt.Sprintf("В %d из %d дней работы из дома по заявке VAC не найдено ни одного события.",
				len(wfhZero), wfhTotal)
			out = append(out, v)
		}
	}

	// Правило 5а: овертайм заведён, а активности в его дни нет.
	overtimeDays := map[string]overtime.Entry{}
	for _, e := range overtimes {
		days := e.Days(from, to)
		var idle, low []string
		for _, day := range days {
			overtimeDays[day] = e
			if activity[day] == 0 {
				idle = append(idle, day)
			} else if activity[day] <= 2 {
				low = append(low, day)
			}
		}
		if len(low) > 0 {
			v := base
			v.Rule = "overtime_low"
			v.Severity = "warn"
			v.Dates = low
			v.Title = "Овертайм с минимальной активностью"
			v.Detail = fmt.Sprintf("Заявка %s (%s): в %d дн. овертайма всего 1–2 события.",
				e.Key, e.Summary, len(low))
			v.URL = e.URL
			out = append(out, v)
		}
		if len(idle) > 0 {
			v := base
			v.Rule = "overtime_idle"
			v.Severity = "warn"
			v.Dates = idle
			v.Title = "Овертайм без активности"
			v.Detail = fmt.Sprintf("Заявка %s (%s): в %d из %d дн. овертайма не найдено ни одного события.",
				e.Key, e.Summary, len(idle), len(days))
			v.URL = e.URL
			out = append(out, v)
		}
	}

	// Правило 5б: активность в выходной/праздник без заявки на овертайм.
	var offActive []string
	offEvents := 0
	fromLoc := from.In(loc)
	for d := time.Date(fromLoc.Year(), fromLoc.Month(), fromLoc.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		if activity[key] > 0 && isOff(d) && kinds[key] != models.DayVacation && kinds[key] != models.DaySick {
			if _, has := overtimeDays[key]; !has {
				offActive = append(offActive, key)
				offEvents += activity[key]
			}
		}
	}
	if len(offActive) > 0 {
		v := base
		v.Rule = "offday_activity"
		v.Severity = "info"
		v.Dates = capDates(offActive, 8)
		v.Title = "Активность в выходные без овертайма"
		v.Detail = fmt.Sprintf("%d дн. с активностью (%d событий) в выходные/праздники без заявки на овертайм. "+
			"Для сведения лида: действия требуются не всегда.", len(offActive), offEvents)
		out = append(out, v)
	}

	// Правило 6: работа в отпуске или на больничном.
	var absWork []string
	absEvents := 0
	for day, n := range activity {
		if n > 0 && (kinds[day] == models.DayVacation || kinds[day] == models.DaySick) {
			absWork = append(absWork, day)
			absEvents += n
		}
	}
	if len(absWork) > 0 {
		sort.Strings(absWork)
		v := base
		v.Rule = "work_on_vacation"
		v.Severity = "warn"
		v.Dates = capDates(absWork, 8)
		v.Title = "Работа в отпуске или на больничном"
		v.Detail = fmt.Sprintf("%d дн. отпуска/больничного с активностью (%d событий).", len(absWork), absEvents)
		out = append(out, v)
	}

	// Правило 7: рабочие дни длиннее 9 часов (первое и последнее событие дня).
	var longDays []string
	maxSpan := 0.0
	for day, span := range data.spans {
		if span.Events < 4 {
			continue // пара случайных событий утром и вечером — не рабочий день
		}
		hours := span.Last.Sub(span.First).Hours()
		if hours > 9 {
			longDays = append(longDays, day)
			if hours > maxSpan {
				maxSpan = hours
			}
		}
	}
	if len(longDays) >= 3 {
		sort.Strings(longDays)
		v := base
		v.Rule = "long_workday"
		v.Severity = "warn"
		v.Dates = capDates(longDays, 8)
		v.Title = "Рабочие дни длиннее 9 часов"
		v.Detail = fmt.Sprintf("%d дн. со сплошной активностью более 9 часов (максимум %.1f ч).",
			len(longDays), maxSpan)
		out = append(out, v)
	}

	// Правило 8: резкое падение активности — вторая половина периода вдвое
	// ниже первой (при значимом объёме в первой).
	if to.Sub(from).Hours()/24 >= 14 {
		mid := from.Add(to.Sub(from) / 2).In(loc).Format("2006-01-02")
		first, second := 0, 0
		for day, n := range activity {
			if day < mid {
				first += n
			} else {
				second += n
			}
		}
		if first >= 20 && second*2 < first {
			v := base
			v.Rule = "activity_drop"
			v.Severity = "warn"
			v.Dates = []string{mid}
			v.Title = "Резкое падение активности"
			v.Detail = fmt.Sprintf("Вторая половина периода: %d событий против %d в первой (−%d%%).",
				second, first, 100-second*100/first)
			out = append(out, v)
		}
	}

	// Правило 9 (позитивное): стабильный ритм — активность почти каждый
	// рабочий день, без выходных и без затяжных дней.
	workDays, activeWorkDays := 0, 0
	fromLoc2 := from.In(loc)
	for d := time.Date(fromLoc2.Year(), fromLoc2.Month(), fromLoc2.Day(), 0, 0, 0, 0, loc); d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
			continue
		}
		if person.HireDate != nil && d.Before(*person.HireDate) {
			continue
		}
		workDays++
		if activity[key] > 0 {
			activeWorkDays++
		}
	}
	if workDays >= 15 && activeWorkDays*10 >= workDays*9 && len(offActive) == 0 &&
		len(absWork) == 0 && len(longDays) == 0 {
		v := base
		v.Rule = "steady_rhythm"
		v.Severity = "info"
		v.Title = "Стабильный здоровый ритм"
		v.Detail = fmt.Sprintf("Активность в %d из %d рабочих дней, без работы в выходные, отпуске и без затяжных дней.",
			activeWorkDays, workDays)
		out = append(out, v)
	}

	// Правило 10: онбординг — рост (или его отсутствие) у нанятых недавно.
	if person.HireDate != nil && person.HireDate.After(to.AddDate(0, 0, -90)) && person.HireDate.Before(to) {
		sinceHire := int(to.Sub(*person.HireDate).Hours() / 24)
		if sinceHire >= 30 {
			hireLoc := person.HireDate.In(loc)
			mid := hireLoc.Add(to.Sub(hireLoc) / 2).Format("2006-01-02")
			first, second := 0, 0
			for day, n := range activity {
				if day < mid {
					first += n
				} else {
					second += n
				}
			}
			switch {
			case first >= 10 && second*10 >= first*13:
				v := base
				v.Rule = "onboarding_rise"
				v.Severity = "info"
				v.Title = "Рост после найма"
				v.Detail = fmt.Sprintf("Нанят %s: активность растёт (%d → %d событий по половинам срока).",
					person.HireDate.Format("2006-01-02"), first, second)
				out = append(out, v)
			case totalActivity < 10:
				v := base
				v.Rule = "onboarding_flat"
				v.Severity = "warn"
				v.Title = "Нет роста после найма"
				v.Detail = fmt.Sprintf("Нанят %s (%d дн. назад), но за период лишь %d событий.",
					person.HireDate.Format("2006-01-02"), sinceHire, totalActivity)
				out = append(out, v)
			}
		}
	}

	// Правило 11 (позитивное): наставничество — 1:1 с несколькими людьми.
	if data.peers >= 3 {
		v := base
		v.Rule = "mentor_one_on_ones"
		v.Severity = "info"
		v.Title = "Регулярные 1:1 с разными людьми"
		v.Detail = fmt.Sprintf("Встречи 1:1 с %d разными собеседниками за период.", data.peers)
		out = append(out, v)
	}

	// Правило 12 (позитивное): вернулся в ритм после отпуска (≥3 дней) —
	// активность в первые три рабочих дня после его окончания.
	if run := lastVacationRun(special, from, to); len(run) >= 3 {
		end := run[len(run)-1]
		checked, active := 0, false
		for d := end.AddDate(0, 0, 1); checked < 3 && d.Before(to.In(loc)); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if isOff(d) || kinds[key] == models.DayVacation || kinds[key] == models.DaySick {
				continue
			}
			checked++
			if activity[key] > 0 {
				active = true
				break
			}
		}
		if checked > 0 && active {
			v := base
			v.Rule = "vacation_recovery"
			v.Severity = "info"
			v.Title = "Вернулся в ритм после отпуска"
			v.Detail = fmt.Sprintf("Отпуск %d дн. до %s, активность возобновилась в первые рабочие дни.",
				len(run), end.Format("2006-01-02"))
			out = append(out, v)
		}
	}

	return out, vacDates, kinds
}

// lastVacationRun — последний непрерывный отпускной отрезок, закончившийся
// внутри периода (мостом через выходные считается одним отрезком).
func lastVacationRun(special []models.PersonDay, from, to time.Time) []time.Time {
	var days []time.Time
	for _, d := range special {
		if d.Kind == models.DayVacation {
			days = append(days, d.Day)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	var runs [][]time.Time
	for _, d := range days {
		if n := len(runs); n > 0 {
			last := runs[n-1][len(runs[n-1])-1]
			if int(d.Sub(last).Hours()/24) <= 3 {
				runs[n-1] = append(runs[n-1], d)
				continue
			}
		}
		runs = append(runs, []time.Time{d})
	}
	for i := len(runs) - 1; i >= 0; i-- {
		end := runs[i][len(runs[i])-1]
		if !end.Before(from) && end.Before(to.AddDate(0, 0, -1)) {
			return runs[i]
		}
	}
	return nil
}

// teamDeviations — командные правила: bus-фактор, отсутствие ревьюеров и
// молчащая роль целиком.
func (s *Server) teamDeviations(p deviationParams, people []models.Person,
	chainOf func(string) hrdb.TeamChainInfo,
	activityAll map[string]map[string]int, typeCounts map[string]map[string]int,
	kindsByPerson map[string]map[string]string) []violationItem {

	byTeam := map[string][]models.Person{}
	for _, person := range people {
		if person.Team != "" {
			byTeam[person.Team] = append(byTeam[person.Team], person)
		}
	}

	var out []violationItem
	for team, members := range byTeam {
		if len(members) < 3 {
			continue
		}
		chain := chainOf(team)
		base := violationItem{Person: team, Team: team, Cluster: chain.Cluster, Department: chain.Department}

		// Bus-фактор: >70% событий команды делает один человек.
		total, maxN, maxName := 0, 0, ""
		reviewers, gitlabActive := 0, 0
		for _, m := range members {
			n := 0
			for _, c := range activityAll[m.Key] {
				n += c
			}
			total += n
			if n > maxN {
				maxN, maxName = n, m.DisplayName
			}
			tc := typeCounts[m.Key]
			if tc["gitlab.review_comment"] > 0 {
				reviewers++
			}
			for typ, c := range tc {
				if strings.HasPrefix(typ, "gitlab.") && c > 0 {
					gitlabActive++
					break
				}
			}
		}
		if total >= 100 && maxN*10 > total*7 {
			v := base
			v.Rule = "bus_factor"
			v.Severity = "warn"
			v.Title = "Bus-фактор: активность держится на одном"
			v.Detail = fmt.Sprintf("%s делает %d%% событий команды %s (%d из %d).",
				maxName, maxN*100/total, team, maxN, total)
			out = append(out, v)
		}

		// Некому ревьюить: GitLab-активность есть, а ревью пишут ≤1 человек.
		if gitlabActive >= 3 && reviewers <= 1 {
			v := base
			v.Rule = "no_reviewers"
			v.Severity = "warn"
			v.Title = "Некому ревьюить"
			v.Detail = fmt.Sprintf("В команде %s ревью-комментарии пишут %d чел. при %d активных в GitLab.",
				team, reviewers, gitlabActive)
			out = append(out, v)
		}

		// Молчащая роль: все люди роли одновременно без активности
		// ≥3 рабочих дней подряд, хотя присутствуют (не отпуск/больничный).
		byArea := map[string][]models.Person{}
		for _, m := range members {
			if m.Area != "" {
				byArea[m.Area] = append(byArea[m.Area], m)
			}
		}
		for area, roleMembers := range byArea {
			if len(roleMembers) < 2 {
				continue
			}
			var streaks []string
			runStart, runLen := "", 0
			flush := func() {
				if runLen >= 3 {
					streaks = append(streaks, fmt.Sprintf("%s (%d дн.)", runStart, runLen))
				}
				runStart, runLen = "", 0
			}
			fromLoc := p.from.In(p.loc)
			for d := time.Date(fromLoc.Year(), fromLoc.Month(), fromLoc.Day(), 0, 0, 0, 0, p.loc); d.Before(p.to.In(p.loc)); d = d.AddDate(0, 0, 1) {
				if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
					continue
				}
				key := d.Format("2006-01-02")
				present, active := 0, 0
				for _, m := range roleMembers {
					kind := kindsByPerson[m.Key][key]
					if kind == models.DayVacation || kind == models.DaySick || kind == models.DayHoliday {
						continue
					}
					present++
					active += activityAll[m.Key][key]
				}
				if present == 0 {
					flush()
					continue
				}
				if active == 0 {
					if runLen == 0 {
						runStart = key
					}
					runLen++
				} else {
					flush()
				}
			}
			flush()
			if len(streaks) > 0 {
				v := base
				v.Person = fmt.Sprintf("%s · %s", team, area)
				v.Rule = "role_inactive"
				v.Severity = "warn"
				v.Dates = capDates(streaks, 5)
				v.Title = "Роль целиком без активности"
				v.Detail = fmt.Sprintf("Роль «%s» (%d чел.) молчала: %s.",
					area, len(roleMembers), strings.Join(capDates(streaks, 5), "; "))
				out = append(out, v)
			}
		}
	}
	return out
}

// reviewDeviations — MR без первого отклика ревью дольше трёх суток.
func (s *Server) reviewDeviations(p deviationParams, mrs []storage.MROpenReview,
	personByKey map[string]models.Person, chainOf func(string) hrdb.TeamChainInfo) []violationItem {

	const maxDelay = 72 * time.Hour
	slow := map[string][]string{}
	for _, m := range mrs {
		delay := time.Duration(0)
		if m.FirstReview != nil {
			delay = m.FirstReview.Sub(m.Opened)
		} else {
			delay = p.to.Sub(m.Opened)
		}
		if delay > maxDelay {
			slow[m.PersonKey] = append(slow[m.PersonKey],
				fmt.Sprintf("%s (%.0f дн.)", truncateStr(m.Title, 40), delay.Hours()/24))
		}
	}
	var out []violationItem
	for key, list := range slow {
		person := personByKey[key]
		chain := chainOf(person.Team)
		out = append(out, violationItem{
			PersonKey: key, Person: person.DisplayName, Team: person.Team,
			Cluster: chain.Cluster, Department: chain.Department,
			Rule: "slow_review", Severity: "warn",
			Title:  "MR долго без ревью",
			Detail: fmt.Sprintf("%d MR без первого отклика более 3 дней: %s.", len(list), strings.Join(capDates(list, 4), "; ")),
		})
	}
	return out
}

// roleChampionDeviations — позитив: заметно больше ревью, чем в среднем по роли.
func (s *Server) roleChampionDeviations(people []models.Person,
	typeCounts map[string]map[string]int, chainOf func(string) hrdb.TeamChainInfo) []violationItem {

	byArea := map[string][]models.Person{}
	for _, person := range people {
		if person.Area != "" {
			byArea[person.Area] = append(byArea[person.Area], person)
		}
	}
	var out []violationItem
	for area, members := range byArea {
		if len(members) < 3 {
			continue
		}
		total := 0
		for _, m := range members {
			total += typeCounts[m.Key]["gitlab.review_comment"]
		}
		avg := total / len(members)
		for _, m := range members {
			n := typeCounts[m.Key]["gitlab.review_comment"]
			if n >= 10 && n >= 2*avg && avg > 0 {
				chain := chainOf(m.Team)
				out = append(out, violationItem{
					PersonKey: m.Key, Person: m.DisplayName, Team: m.Team,
					Cluster: chain.Cluster, Department: chain.Department,
					Rule: "review_champion", Severity: "info",
					Title:  "Ревью-чемпион",
					Detail: fmt.Sprintf("%d ревью-комментариев при среднем %d по роли «%s».", n, avg, area),
				})
			}
		}
	}
	return out
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func capDates(dates []string, n int) []string {
	if len(dates) <= n {
		return dates
	}
	return append(append([]string{}, dates[:n]...), "…")
}

// sickRuns группирует больничные в непрерывные отрезки; выходные и праздники
// между больничными отрезок не рвут (мостом через выходные — тот же ран).
func sickRuns(days []time.Time) [][]time.Time {
	var runs [][]time.Time
	for _, d := range days {
		if n := len(runs); n > 0 {
			last := runs[n-1][len(runs[n-1])-1]
			if gap := int(d.Sub(last).Hours() / 24); gap <= 3 {
				runs[n-1] = append(runs[n-1], d)
				continue
			}
		}
		runs = append(runs, []time.Time{d})
	}
	return runs
}

// longestMonthStreak — самая длинная последовательность подряд идущих
// месяцев с больничными (YYYY-MM).
func longestMonthStreak(months map[string]int) []string {
	if len(months) == 0 {
		return nil
	}
	keys := make([]string, 0, len(months))
	for m := range months {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	best, cur := []string{}, []string{keys[0]}
	for i := 1; i < len(keys); i++ {
		prev, _ := time.Parse("2006-01", keys[i-1])
		if prev.AddDate(0, 1, 0).Format("2006-01") == keys[i] {
			cur = append(cur, keys[i])
		} else {
			cur = []string{keys[i]}
		}
		if len(cur) > len(best) {
			best = append([]string{}, cur...)
		}
	}
	if len(cur) > len(best) {
		best = cur
	}
	return best
}
