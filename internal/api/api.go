// Package api — HTTP-слой: маршруты, разбор параметров, JSON-ответы и раздача
// собранного React-приложения.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/adavydov/user-activity-dashboard/internal/ai"
	"github.com/adavydov/user-activity-dashboard/internal/auth"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/discovery"
	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/overtime"
	"github.com/adavydov/user-activity-dashboard/internal/perfreview"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
	syncsvc "github.com/adavydov/user-activity-dashboard/internal/sync"
)

// Server связывает хранилище, оркестратор и HTTP-маршруты.
type Server struct {
	cfg   *config.Config
	store *storage.Store
	orch  *syncsvc.Orchestrator
	log   *slog.Logger
	// hrdb может быть nil — HRDB не сконфигурирован; Current() nil-безопасен.
	hrdb     *hrdb.Switcher
	discover *discovery.Service
	// ai может быть nil — AI-скорер не сконфигурирован.
	ai *ai.Client
	// overtime может быть nil — Jira с заявками на овертаймы не настроена.
	overtime *overtime.Client
	// perf может быть nil — Jira DC с оценками Performance Review не настроена.
	perf *perfreview.Client

	// devCache — кэш рассчитанных отклонений (см. violations.go).
	devMu    sync.Mutex
	devCache map[string]devCacheEntry

	// auth и кэш HRDB-скоупов роли lead (см. auth.go).
	auth       *auth.Service
	scopeMu    sync.Mutex
	leadScopes map[string]*leadScopeData
}

// NewServer создаёт HTTP-сервер приложения. hrdbClient, aiClient и
// overtimeClient могут быть nil.
func NewServer(cfg *config.Config, store *storage.Store, orch *syncsvc.Orchestrator,
	hrdbClient *hrdb.Switcher, disc *discovery.Service, aiClient *ai.Client,
	overtimeClient *overtime.Client, perfClient *perfreview.Client, authSvc *auth.Service, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, store: store, orch: orch, hrdb: hrdbClient,
		discover: disc, ai: aiClient, overtime: overtimeClient, perf: perfClient, log: log,
		devCache: map[string]devCacheEntry{}, auth: authSvc,
		leadScopes: map[string]*leadScopeData{}}
	// Завершение любого сбора делает кэш отклонений неактуальным.
	orch.SetOnFinish(s.InvalidateDeviations)
	return s
}

// Router собирает маршрутизатор.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.cfg.Server.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Вход/выход — вне /api: колбэк Google приходит без сессии.
	r.Get("/auth/login", s.auth.HandleLogin)
	r.Get("/auth/callback", s.auth.HandleCallback)
	r.Post("/auth/logout", s.auth.HandleLogout)

	r.Route("/api", func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Get("/auth/me", s.handleAuthMe)

		// Пользователи и роли (только administrator — см. auth.Allowed).
		r.Get("/users", s.handleListUsers)
		r.Post("/users", s.handleUpsertUser)
		r.Delete("/users", s.handleDeleteUser)

		r.Get("/health", s.handleHealth)
		r.Get("/meta", s.handleMeta)

		r.Get("/people", s.handleListPeople)
		r.Post("/people", s.handleUpsertPerson)
		r.Delete("/people/{key}", s.handleDeletePerson)
		r.Post("/people/cleanup-ex", s.handleCleanupExPeople)
		r.Get("/people/{key}/projects", s.handleProjects)

		// HRDB: список сотрудников, команды и автопоиск аккаунтов по e-mail.
		r.Get("/hrdb/employees", s.handleHRDBEmployees)
		r.Get("/hrdb/teams", s.handleHRDBTeams)
		r.Post("/people/discover", s.handleDiscover)

		r.Post("/sync", s.handleStartSync)
		r.Post("/sync/all", s.handleSyncAll)
		r.Post("/sync/team", s.handleSyncTeam)
		r.Post("/sync/unit", s.handleSyncUnit)
		r.Post("/sync/{id}/cancel", s.handleCancelSync)
		r.Get("/sync/queue", s.handleSyncQueue)
		r.Get("/sync", s.handleListSyncRuns)
		r.Get("/sync/{id}", s.handleGetSyncRun)

		r.Get("/stats/summary", s.handleSummary)
		r.Get("/stats/timeline", s.handleTimeline)
		r.Get("/stats/breakdown", s.handleBreakdown)
		r.Get("/stats/heatmap", s.handleHeatmap)
		r.Get("/stats/days", s.handleDays)
		r.Get("/stats/compare", s.handleCompare)
		r.Get("/stats/by-source", s.handleCompareBySource)
		r.Get("/stats/teams", s.handleCompareTeams)
		r.Get("/stats/format-compare", s.handleFormatCompare)
		r.Get("/stats/abusers", s.handleAbusers)

		r.Get("/events", s.handleEvents)
		r.Get("/events/{id}", s.handleEvent)
		r.Get("/refs/{refID}/events", s.handleRefEvents)

		r.Get("/docs", s.handleDocs)

		// Переключение источника HRDB: новая (Atlassian Cloud) / старая (Jira DC).
		r.Get("/settings/hrdb", s.handleGetHRDBMode)
		r.Put("/settings/hrdb", s.handleSetHRDBMode)

		// Отключаемые правила отклонений (хранится в БД, влияет на всех).
		r.Get("/settings/rules", s.handleGetDisabledRules)
		r.Put("/settings/rules", s.handleSetDisabledRules)
		r.Get("/settings/shallow", s.handleGetShallowConfig)
		r.Put("/settings/shallow", s.handleSetShallowConfig)

		// Расписание ежедневного обновления данных.
		r.Get("/settings/schedule", s.handleGetSchedule)
		r.Put("/settings/schedule", s.handleSetSchedule)

		// Офисные подсети (для gwork: логин из офиса/вне офиса).
		r.Get("/settings/office-cidrs", s.handleGetOfficeCIDRs)
		r.Put("/settings/office-cidrs", s.handleSetOfficeCIDRs)

		// Дни перекрытия инкрементального сбора на каждую систему.
		r.Get("/settings/reprobe", s.handleGetReprobe)
		r.Put("/settings/reprobe", s.handleSetReprobe)

		// Канал-центричный архив Slack: линковка людей к архиву и текст
		// сообщения (текст — только administrator, см. auth.Allowed).
		r.Post("/slack/relink", s.handleSlackRelink)
		r.Get("/slack/text", s.handleSlackText)

		// Корпоративный календарь праздников (страна × год) и нарушения.
		r.Get("/holidays", s.handleHolidays)
		r.Post("/holidays", s.handleAddHoliday)
		r.Delete("/holidays", s.handleDeleteHoliday)
		r.Post("/holidays/import", s.handleImportHolidays)
		r.Get("/violations", s.handleViolations)
		r.Get("/violations/export", s.handleViolationsExport)

		// Локальный AI-скорер: статус, калибровка и переоценка сообщений.
		r.Route("/ai", func(r chi.Router) {
			r.Get("/status", s.handleAIStatus)
			r.Get("/labels", s.handleAILabels)
			r.Post("/labels", s.handleAIAddLabel)
			r.Delete("/labels/{id}", s.handleAIDeleteLabel)
			r.Post("/train", s.handleAITrain)
			r.Get("/rules", s.handleAIRules)
			r.Put("/rules", s.handleAISaveRules)
			r.Get("/sample", s.handleAISample)
			r.Post("/rescore", s.handleAIRescore)
		})

		// Очистка собранных данных за период. Preview отделён от самого
		// удаления намеренно: в UI сначала показывается, что именно исчезнет.
		r.Get("/purge/preview", s.handlePurgePreview)
		r.Post("/purge", s.handlePurge)
	})

	// Раздача собранного фронтенда с SPA-фолбэком на index.html.
	if dir := s.cfg.Server.StaticDir; dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			r.Handle("/*", spaHandler(dir))
		} else {
			s.log.Warn("статика фронтенда не найдена, отдаётся только API",
				"dir", dir, "hint", "соберите фронт: cd web && npm install && npm run build")
			r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{
					"service": "user-activity-dashboard",
					"hint":    "фронтенд не собран; API доступно на /api",
				})
			})
		}
	}
	return r
}

func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean(r.URL.Path)
		if _, err := os.Stat(filepath.Join(dir, clean)); err != nil || clean == "/" {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// ---------- служебные эндпоинты ----------

// startedAt — момент старта процесса. Вместе со списком features позволяет
// за один curl понять, свежий ли сервер отвечает: «код обновил, а порт занят
// старым процессом» — самая частая причина внезапных 404.
var startedAt = time.Now()

// features — возможности этой сборки. Список пополняется вместе с API, так что
// по нему видно, дожил ли до работающего процесса нужный маршрут.
var features = []string{
	"purge",              // удаление собранных данных за период
	"gdocs-name-match",   // сопоставление автора в Google Docs по имени
	"gdocs-comments-api", // комментарии через Drive Comments API
	"hrdb-employees",     // список сотрудников из HRDB (Atlassian Assets)
	"account-discovery",  // автопоиск аккаунтов по e-mail во всех системах
	"gcal-meetings",      // встречи и собеседования из Google Calendar
	"workday-stats",      // праздники по офису, отпуска и разбивка рабочих дней
	"team-compare",       // команды из HRDB, командный сбор и сравнение продуктивности
	"allure-testops",     // запуски тестов, тест-кейсы и дефекты из Allure TestOps
	"ai-scoring",         // локальная AI-оценка сообщений Slack с калибровкой
	"company-holidays",   // редактируемый календарь праздников по стране и году
	"violations",         // вкладка «Нарушения»: больничные и овертаймы
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := "ok"
	if err := s.store.Ping(r.Context()); err != nil {
		dbOK = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"db":         dbOK,
		"started_at": startedAt.UTC(),
		"uptime":     time.Since(startedAt).Round(time.Second).String(),
		"features":   features,
		"sources": map[string]bool{
			"jira":       s.cfg.Jira.Enabled,
			"gitlab":     s.cfg.GitLab.Enabled,
			"slack":      s.cfg.Slack.Enabled,
			"gdocs":      s.cfg.Google.Enabled,
			"gcal":       s.cfg.Google.Enabled && s.cfg.Google.CalendarEnabled,
			"allure":     s.cfg.Allure.Enabled,
			"confluence": s.cfg.Confluence.Enabled,
		},
		"hrdb": s.cfg.HRDB.Enabled,
	})
}

func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	// Единый источник правды о включённости — cfg.EnabledSources(): он учитывает
	// не только флаг Enabled, но и наличие токенов/инстансов/бакета у каждого
	// источника. Иначе новые источники (gwork…netsuite, claude) висят enabled=
	// false и не попадают в навигацию.
	enabledSet := make(map[string]bool)
	for _, src := range s.cfg.EnabledSources() {
		enabledSet[src] = true
	}
	sources := make([]map[string]any, 0, len(models.AllSources))
	for _, src := range models.AllSources {
		sources = append(sources, map[string]any{
			"key":     string(src),
			"label":   sourceLabels[string(src)],
			"enabled": enabledSet[string(src)],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sources":               sources,
		"type_labels":           models.TypeLabels(),
		"default_lookback_days": int(s.cfg.Sync.DefaultLookback.Hours() / 24),
		"hrdb_enabled":          s.cfg.HRDB.Enabled,
		"ai_enabled":            s.cfg.AI.Enabled,
	})
}

var sourceLabels = map[string]string{
	"jira": "Jira", "gitlab": "GitLab", "slack": "Slack", "gdocs": "Google Docs",
	"gcal": "Календарь", "allure": "Allure", "confluence": "Confluence",
	"gwork": "Google Workspace", "argocd": "Argo CD", "zabbix": "Zabbix",
	"jenkins": "Jenkins", "grafana": "Grafana", "figma": "Figma",
	"netsuite": "NetSuite", "claude": "Claude Code",
}

// ---------- люди ----------

func (s *Server) handleListPeople(w http.ResponseWriter, r *http.Request) {
	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Роль lead видит только людей своего HRDB-поддерева.
	if scope := s.requestScope(r); scope != nil {
		kept := people[:0]
		for _, p := range people {
			if scope.persons[p.Key] {
				kept = append(kept, p)
			}
		}
		people = kept
	}
	// Обогащаем должностью и грейдом из HRDB-кэша по e-mail (в БД не хранятся).
	s.enrichTitles(r, people)
	if people == nil {
		people = []models.Person{}
	}
	writeJSON(w, http.StatusOK, people)
}

func (s *Server) handleUpsertPerson(w http.ResponseWriter, r *http.Request) {
	var p models.Person
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(p.Key) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("поле key обязательно"))
		return
	}
	if p.DisplayName == "" {
		p.DisplayName = p.Key
	}
	out, err := s.store.UpsertPerson(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeletePerson удаляет человека со всеми его данными.
func (s *Server) handleDeletePerson(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if s.orch.IsBusy(key) {
		writeErr(w, http.StatusConflict, errors.New("по человеку идёт сбор — остановите его перед удалением"))
		return
	}
	// До удаления запоминаем e-mail: заносим в чёрный список, чтобы синк
	// команды не пересоздал человека, пока HRDB числит его действующим.
	if p, err := s.store.GetPerson(r.Context(), key); err == nil {
		if err := s.store.ExcludeEmails(r.Context(), key, []string{p.Email, p.GoogleEmail}); err != nil {
			s.log.Warn("не удалось занести в чёрный список", "key", key, "err", err)
		}
	}
	if err := s.store.DeletePerson(r.Context(), key); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Warn("человек удалён вместе с данными", "key", key)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": key})
}

// handleCleanupExPeople удаляет всех людей, числящихся в HRDB бывшими
// (object type Ex-employee), вместе с их данными.
func (s *Server) handleCleanupExPeople(w http.ResponseWriter, r *http.Request) {
	if s.hrdb.Current() == nil {
		writeErr(w, http.StatusBadRequest, errors.New("HRDB не настроен"))
		return
	}
	ex, err := s.hrdb.Current().ExEmployeeEmails(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// Чёрный список — удалённые вручную: HRDB может отставать (человек уволен,
	// а тип записи всё ещё Employee), поэтому эти e-mail чистятся всегда.
	if excluded, exclErr := s.store.ExcludedEmails(r.Context()); exclErr == nil {
		for e := range excluded {
			ex[e] = true
		}
	} else {
		s.log.Warn("чёрный список недоступен", "err", exclErr)
	}
	if len(ex) == 0 {
		// Пустой список — либо бывших нет, либо кэш ещё греется; удалять
		// «по пустому списку» безопасно, но и нечего.
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": []string{}, "note": "список бывших пуст (возможно, кэш HRDB ещё греется — повторите через минуту)",
		})
		return
	}
	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	deleted := []string{}
	for _, p := range people {
		if !ex[strings.ToLower(strings.TrimSpace(p.Email))] {
			continue
		}
		if s.orch.IsBusy(p.Key) {
			continue
		}
		if err := s.store.DeletePerson(r.Context(), p.Key); err != nil {
			s.log.Warn("не удалось удалить бывшего сотрудника", "key", p.Key, "err", err)
			continue
		}
		deleted = append(deleted, p.Key)
	}
	s.log.Warn("удалены бывшие сотрудники", "count", len(deleted), "keys", deleted)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// hrdbModeLabels — человекочитаемые подписи режимов HRDB.
var hrdbModeLabels = map[string]string{
	"cloud": "Новая — Atlassian Cloud Assets",
	"dc":    "Старая — Insight на jira.xtools.tv",
}

func (s *Server) handleGetHRDBMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      s.hrdb.Mode(),
		"available": s.hrdb.Available(),
		"labels":    hrdbModeLabels,
	})
}

func (s *Server) handleSetHRDBMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.hrdb.SetMode(strings.TrimSpace(req.Mode)); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetSetting(r.Context(), "hrdb_mode", s.hrdb.Mode()); err != nil {
		s.log.Warn("не удалось сохранить настройку hrdb_mode", "err", err)
	}
	// Отклонения зависят от оргструктуры и списка бывших — сбрасываем кэш.
	s.InvalidateDeviations()
	s.log.Info("источник HRDB переключён", "mode", s.hrdb.Mode())
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      s.hrdb.Mode(),
		"available": s.hrdb.Available(),
		"labels":    hrdbModeLabels,
	})
}

// handleGetOfficeCIDRs возвращает офисные подсети (список CIDR).
func netParseCIDR(v string) (any, any, error) { a, b, e := net.ParseCIDR(v); return a, b, e }
func netParseIP(v string) net.IP              { return net.ParseIP(v) }

func (s *Server) handleGetOfficeCIDRs(w http.ResponseWriter, r *http.Request) {
	v, _ := s.store.GetSetting(r.Context(), "office_cidrs")
	list := []string{}
	for _, c := range strings.Split(v, ",") {
		if c = strings.TrimSpace(c); c != "" {
			list = append(list, c)
		}
	}
	if len(list) == 0 {
		list = append(list, s.cfg.GWork.OfficeCIDRs...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"cidrs": list})
}

func (s *Server) handleSetOfficeCIDRs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDRs []string `json:"cidrs"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	clean := make([]string, 0, len(req.CIDRs))
	for _, c := range req.CIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Валидация: CIDR или одиночный IP.
		if _, _, err := netParseCIDR(c); err != nil {
			if ip := netParseIP(c); ip == nil {
				writeErr(w, http.StatusBadRequest, errStr("неверная подсеть/IP: "+c))
				return
			}
		}
		clean = append(clean, c)
	}
	if err := s.store.SetSetting(r.Context(), "office_cidrs", strings.Join(clean, ",")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.cfg.GWork.OfficeCIDRs = clean // применяется к новым прогонам gwork
	s.log.Info("офисные подсети обновлены", "count", len(clean))
	writeJSON(w, http.StatusOK, map[string]any{"cidrs": clean})
}

// handleGetReprobe возвращает дни перекрытия инкрементального сбора: дефолт и
// переопределения на каждую систему.
func (s *Server) handleGetReprobe(w http.ResponseWriter, r *http.Request) {
	days := map[string]int{}
	if raw, _ := s.store.GetSetting(r.Context(), syncsvc.SettingReprobeDays); raw != "" {
		_ = json.Unmarshal([]byte(raw), &days)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default": s.cfg.Sync.ReprobeDays,
		"days":    days,
		"sources": models.AllSources,
	})
}

// handleSetReprobe сохраняет переопределения дней перекрытия на систему. Пустая
// карта очищает переопределения (все системы возвращаются к дефолту).
func (s *Server) handleSetReprobe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Days map[string]int `json:"days"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	known := map[string]bool{}
	for _, src := range models.AllSources {
		known[string(src)] = true
	}
	clean := map[string]int{}
	for src, d := range req.Days {
		if !known[src] {
			writeErr(w, http.StatusBadRequest, errStr("неизвестный источник: "+src))
			return
		}
		if d < 0 || d > 365 {
			writeErr(w, http.StatusBadRequest, errStr("дни перекрытия — 0..365: "+src))
			return
		}
		clean[src] = d
	}
	payload, _ := json.Marshal(clean)
	if err := s.store.SetSetting(r.Context(), syncsvc.SettingReprobeDays, string(payload)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("дни перекрытия обновлены", "overrides", len(clean))
	writeJSON(w, http.StatusOK, map[string]any{"default": s.cfg.Sync.ReprobeDays, "days": clean, "sources": models.AllSources})
}

// handleSlackRelink строит slack-события людей из архива без похода в Slack:
// после массового добавления людей достаточно линковки.
func (s *Server) handleSlackRelink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PersonKeys []string `json:"person_keys"`
	}
	_ = readJSON(r, &req) // пустое тело — все люди
	people, events, err := s.orch.RelinkSlack(r.Context(), req.PersonKeys)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.InvalidateDeviations()
	writeJSON(w, http.StatusOK, map[string]any{"people": people, "events": events})
}

// handleSlackText — расшифрованный текст сообщения из архива (administrator).
func (s *Server) handleSlackText(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, errStr("нужен параметр id"))
		return
	}
	text, err := s.store.SlackMessageText(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, errStr("сообщение не найдено в архиве"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	actor, _ := auth.FromContext(r.Context())
	s.log.Info("админ запросил текст сообщения", "id", id, "by", actor.Email)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "text": text})
}

// handleGetSchedule — настройки ежедневного обновления и факт последнего запуска.
func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	get := func(k string) string { v, _ := s.store.GetSetting(r.Context(), k); return v }
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":      get(syncsvc.SettingDailyEnabled) == "true",
		"time":         get(syncsvc.SettingDailyTime),
		"catchup":      get(syncsvc.SettingDailyCatchup) == "true",
		"last_date":    get(syncsvc.SettingDailyLastDate),
		"last_at":      get(syncsvc.SettingDailyLastAt),
		"last_started": get(syncsvc.SettingDailyLastStarted),
		"workers":      s.orch.Workers(),
		"server_time":  time.Now().Format("15:04"),
	})
}

func (s *Server) handleSetSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Time    string `json:"time"`
		Catchup bool   `json:"catchup"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Time = strings.TrimSpace(req.Time)
	if req.Enabled {
		if _, err := time.Parse("15:04", req.Time); err != nil {
			writeErr(w, http.StatusBadRequest, errStr("время запуска — в формате HH:MM (например 06:30)"))
			return
		}
	}
	enabled := "false"
	if req.Enabled {
		enabled = "true"
	}
	if err := s.store.SetSetting(r.Context(), syncsvc.SettingDailyEnabled, enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Time != "" {
		if err := s.store.SetSetting(r.Context(), syncsvc.SettingDailyTime, req.Time); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	catchup := "false"
	if req.Catchup {
		catchup = "true"
	}
	if err := s.store.SetSetting(r.Context(), syncsvc.SettingDailyCatchup, catchup); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("расписание ежедневного обновления изменено", "enabled", req.Enabled, "time", req.Time, "catchup", req.Catchup)
	s.handleGetSchedule(w, r)
}

// handleSyncQueue — снапшот очереди сбора: кто собирается сейчас, кто ждёт,
// средняя длительность прогона за сутки — UI считает по ним общий ETA.
func (s *Server) handleSyncQueue(w http.ResponseWriter, r *http.Request) {
	running, pending := s.orch.Queue()
	avgMs, err := s.store.AvgRunDurationMS(r.Context())
	if err != nil {
		s.log.Warn("не удалось посчитать среднюю длительность прогона", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running":    orEmpty(running),
		"pending":    orEmpty(pending),
		"avg_run_ms": avgMs,
		"workers":    s.orch.Workers(),
	})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.DistinctProjects(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(items))
}

// ---------- HRDB и автопоиск аккаунтов ----------

// handleHRDBEmployees отдаёт сотрудников из HRDB. Когда HRDB не сконфигурирован,
// возвращается enabled=false с пустым списком — UI просто прячет блок.
func (s *Server) handleHRDBEmployees(w http.ResponseWriter, r *http.Request) {
	if s.hrdb.Current() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "employees": []hrdb.Employee{}})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	employees, err := s.hrdb.Current().Search(r.Context(), q.Get("q"), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if scope := s.requestScope(r); scope != nil {
		kept := employees[:0]
		for _, e := range employees {
			if scope.teams[strings.ToLower(strings.TrimSpace(e.Team))] {
				kept = append(kept, e)
			}
		}
		employees = kept
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "employees": orEmpty(employees)})
}

// handleHRDBTeams отдаёт команды (с иерархической цепочкой оргструктуры),
// направления, кластеры и департаменты из HRDB.
func (s *Server) handleHRDBTeams(w http.ResponseWriter, r *http.Request) {
	if s.hrdb.Current() == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "teams": []any{}, "areas": []string{},
			"clusters": []string{}, "departments": []string{},
		})
		return
	}
	teams, areas, err := s.hrdb.Current().Teams(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	structure, err := s.hrdb.Current().Structure(r.Context())
	if err != nil {
		s.log.Warn("оргструктура HRDB недоступна", "err", err)
		structure = map[string]hrdb.StructureUnit{}
	}

	type teamOut struct {
		hrdb.Team
		Chain      []string `json:"chain"`
		Unit       string   `json:"unit,omitempty"`
		Cluster    string   `json:"cluster,omitempty"`
		Department string   `json:"department,omitempty"`
	}
	scope := s.requestScope(r)
	out := make([]teamOut, 0, len(teams))
	for _, t := range teams {
		if scope != nil && !scope.teams[strings.ToLower(t.Name)] {
			continue
		}
		info := hrdb.TeamChain(structure, t.Name)
		out = append(out, teamOut{Team: t, Chain: info.Chain,
			Unit: info.Unit, Cluster: info.Cluster, Department: info.Department})
	}
	clusterSet, deptSet := map[string]bool{}, map[string]bool{}
	for _, u := range structure {
		if scope != nil && !scope.teams[strings.ToLower(u.Name)] {
			continue
		}
		switch strings.ToLower(u.EntityType) {
		case "cluster":
			clusterSet[u.Name] = true
		case "department":
			deptSet[u.Name] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     true,
		"teams":       orEmpty(out),
		"areas":       orEmpty(areas),
		"clusters":    orEmpty(sortedKeys(clusterSet)),
		"departments": orEmpty(sortedKeys(deptSet)),
	})
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type teamSyncRequest struct {
	Team    string   `json:"team"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Sources []string `json:"sources"`
	// Overwrite — принудительно перезаписать интервал (игнорировать инкремент).
	Overwrite bool `json:"overwrite"`
}

// handleSyncAll запускает пересбор по всем уже заведённым людям одним вызовом
// StartMany — так включается групповая фаза (Slack/Jenkins/Zabbix/Argo/Grafana
// обходятся один раз на всю группу). lead ограничивается своим HRDB-поддеревом.
func (s *Server) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	var req teamSyncRequest // используем те же поля from/to/sources (team игнорируем)
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	to := parseTimeOr(req.To, time.Now().UTC())
	from := parseTimeOr(req.From, to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}
	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if scope := s.requestScope(r); scope != nil {
		filtered := people[:0]
		for _, p := range people {
			if scope.persons[p.Key] {
				filtered = append(filtered, p)
			}
		}
		people = filtered
	}
	if len(people) == 0 {
		writeErr(w, http.StatusNotFound, errors.New("нет людей для сбора"))
		return
	}
	runs := s.orch.StartMany(people, from, to, req.Sources, req.Overwrite)
	s.log.Info("запущен полный пересбор", "people", len(people), "runs", len(runs),
		"from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"people":  len(people),
		"started": len(runs),
		"runs":    orEmpty(runs),
	})
}

// handleSyncTeam запускает сбор по всем сотрудникам команды из HRDB.
// Незаведённые люди создаются на лету: аккаунты во внешних системах ищутся
// автопоиском по e-mail. Прогоны выполняются последовательно.
func (s *Server) handleSyncTeam(w http.ResponseWriter, r *http.Request) {
	if s.hrdb.Current() == nil {
		writeErr(w, http.StatusBadRequest, errors.New("HRDB не настроен — командный сбор недоступен"))
		return
	}
	var req teamSyncRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Team) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("параметр team обязателен"))
		return
	}
	if scope := s.requestScope(r); scope != nil && !scope.teams[strings.ToLower(strings.TrimSpace(req.Team))] {
		writeErr(w, http.StatusForbidden, errors.New("команда вне вашей зоны ответственности (HRDB)"))
		return
	}
	to := parseTimeOr(req.To, time.Now().UTC())
	from := parseTimeOr(req.From, to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}

	members, err := s.hrdb.Current().TeamMembers(r.Context(), req.Team)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if len(members) == 0 {
		writeErr(w, http.StatusNotFound, errors.New("в HRDB нет сотрудников команды "+req.Team))
		return
	}
	s.syncEmployees(w, r, req.Team, members, from, to, req.Sources, req.Overwrite)
}

// handleSyncUnit запускает сбор по всем сотрудникам кластера или департамента:
// берутся команды, чья иерархическая цепочка проходит через юнит.
func (s *Server) handleSyncUnit(w http.ResponseWriter, r *http.Request) {
	if s.hrdb.Current() == nil {
		writeErr(w, http.StatusBadRequest, errors.New("HRDB не настроен — сбор по юниту недоступен"))
		return
	}
	var req struct {
		Cluster    string   `json:"cluster"`
		Department string   `json:"department"`
		From       string   `json:"from"`
		To         string   `json:"to"`
		Sources    []string `json:"sources"`
		Overwrite  bool     `json:"overwrite"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	unit := strings.TrimSpace(req.Cluster)
	matchCluster := unit != ""
	if unit == "" {
		unit = strings.TrimSpace(req.Department)
	}
	if unit == "" {
		writeErr(w, http.StatusBadRequest, errors.New("нужен cluster или department"))
		return
	}
	if scope := s.requestScope(r); scope != nil && !scope.teams[strings.ToLower(unit)] {
		writeErr(w, http.StatusForbidden, errors.New("подразделение вне вашей зоны ответственности (HRDB)"))
		return
	}
	to := parseTimeOr(req.To, time.Now().UTC())
	from := parseTimeOr(req.From, to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}

	structure, err := s.hrdb.Current().Structure(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	employees, err := s.hrdb.Current().ListEmployees(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	var members []hrdb.Employee
	for _, emp := range employees {
		if emp.Team == "" {
			continue
		}
		chain := hrdb.TeamChain(structure, emp.Team)
		target := chain.Department
		if matchCluster {
			target = chain.Cluster
		}
		if strings.EqualFold(target, unit) {
			members = append(members, emp)
		}
	}
	if len(members) == 0 {
		writeErr(w, http.StatusNotFound, errors.New("в HRDB нет сотрудников юнита "+unit))
		return
	}
	s.syncEmployees(w, r, unit, members, from, to, req.Sources, req.Overwrite)
}

// syncEmployees заводит недостающих людей и запускает последовательный сбор.
func (s *Server) syncEmployees(w http.ResponseWriter, r *http.Request, label string,
	members []hrdb.Employee, from, to time.Time, sources []string, overwrite bool) {

	existing, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	byEmail := make(map[string]models.Person, len(existing))
	for _, p := range existing {
		for _, e := range []string{p.Email, p.GoogleEmail} {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				byEmail[e] = p
			}
		}
	}
	// Удалённые вручную не пересоздаются, даже пока HRDB числит их в команде.
	excluded, err := s.store.ExcludedEmails(r.Context())
	if err != nil {
		s.log.Warn("чёрный список недоступен", "err", err)
		excluded = map[string]bool{}
	}

	var people []models.Person
	created := 0
	for _, emp := range members {
		if excluded[strings.ToLower(strings.TrimSpace(emp.Email))] {
			continue
		}
		if p, ok := byEmail[emp.Email]; ok {
			// Освежаем поля HRDB — команда и направление нужны фильтрам.
			if err := s.store.SetPersonHRDB(r.Context(), p.Key, emp.Office, emp.Team, emp.Area, emp.HybridDays, emp.HireTime()); err == nil {
				p.Office, p.Team, p.Area = emp.Office, emp.Team, emp.Area
				if emp.HybridDays != "" {
					p.HybridDays = emp.HybridDays
				}
			}
			people = append(people, p)
			continue
		}
		// Новый человек: автопоиск аккаунтов по e-mail во всех системах.
		person, _ := s.discover.Discover(r.Context(), emp.Email)
		person.Key = strings.SplitN(emp.Email, "@", 2)[0]
		person.DisplayName = emp.DisplayName
		if person.DisplayName == "" {
			person.DisplayName = person.Key
		}
		person.Office, person.Team, person.Area = emp.Office, emp.Team, emp.Area
		person.HireDate = emp.HireTime()
		person.HybridDays = emp.HybridDays
		saved, err := s.store.UpsertPerson(r.Context(), person)
		if err != nil {
			s.log.Warn("не удалось создать человека из HRDB", "email", emp.Email, "err", err)
			continue
		}
		created++
		people = append(people, saved)
	}

	runs := s.orch.StartMany(people, from, to, sources, overwrite)
	s.log.Info("запущен массовый сбор", "unit", label,
		"members", len(members), "created", created, "runs", len(runs))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"team":    label,
		"members": len(members),
		"created": created,
		"started": len(runs),
		"runs":    orEmpty(runs),
	})
}

type discoverRequest struct {
	Email string `json:"email"`
	// Key и DisplayName опциональны: если пусты, выводятся из e-mail.
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
}

// handleDiscover ищет аккаунты человека во всех системах по e-mail и
// возвращает заготовку карточки. Ничего не сохраняет — сохранение делает
// UI через POST /api/people после просмотра результата.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var req discoverRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, errors.New("нужен корректный e-mail"))
		return
	}

	person, results := s.discover.Discover(r.Context(), email)
	person.Key = strings.TrimSpace(req.Key)
	if person.Key == "" {
		// Локальная часть адреса — естественный ключ: adavydov@… → adavydov.
		person.Key = strings.SplitN(email, "@", 2)[0]
	}
	person.DisplayName = strings.TrimSpace(req.DisplayName)
	if person.DisplayName == "" {
		person.DisplayName = person.Key
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"person":  person,
		"results": results,
	})
}

// ---------- сбор ----------

type startSyncRequest struct {
	PersonKey string   `json:"person_key"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Sources   []string `json:"sources"`
	// Overwrite — принудительно перезаписать интервал (игнорировать инкремент).
	Overwrite bool `json:"overwrite"`
}

func (s *Server) handleStartSync(w http.ResponseWriter, r *http.Request) {
	var req startSyncRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if scope := s.requestScope(r); scope != nil && !scope.persons[req.PersonKey] {
		writeErr(w, http.StatusForbidden, errors.New("человек вне вашей зоны ответственности (HRDB)"))
		return
	}
	person, err := s.store.GetPerson(r.Context(), req.PersonKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, errors.New("пользователь не найден: "+req.PersonKey))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	to := parseTimeOr(req.To, time.Now().UTC())
	from := parseTimeOr(req.From, to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}

	run, err := s.orch.Start(person, from, to, req.Sources, req.Overwrite)
	if err != nil {
		if errors.Is(err, syncsvc.ErrAlreadyRunning) {
			writeJSON(w, http.StatusConflict, run)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// handleCancelSync останавливает прогон сбора вручную.
func (s *Server) handleCancelSync(w http.ResponseWriter, r *http.Request) {
	run, err := s.orch.Cancel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleGetSyncRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.orch.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListSyncRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.ListSyncRuns(r.Context(), personKey(r), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(runs))
}

// ---------- статистика ----------

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	f, loc, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sum, err := s.store.Summary(r.Context(), f, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	f, loc, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	gran := r.URL.Query().Get("granularity")
	if gran == "" {
		gran = autoGranularity(f.From, f.To)
	}
	points, err := s.store.Timeline(r.Context(), f, gran, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"granularity": gran,
		"points":      orEmpty(points),
	})
}

// autoGranularity подбирает шаг так, чтобы на графике было 20–90 точек.
func autoGranularity(from, to time.Time) string {
	days := to.Sub(from).Hours() / 24
	switch {
	case days <= 3:
		return "hour"
	case days <= 120:
		return "day"
	case days <= 730:
		return "week"
	default:
		return "month"
	}
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	f, loc, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	_ = loc
	dim := r.URL.Query().Get("dimension")
	if dim == "" {
		dim = "source"
	}
	items, err := s.store.Breakdown(r.Context(), f, dim)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dimension": dim, "items": orEmpty(items)})
}

// handleCompare отдаёт метрики продуктивности людей за период с фильтрами по
// команде и направлению (Area of Responsibility) — данные вкладки «Сравнение».
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
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
	// Команды — мультивыбор: можно сравнивать произвольный состав.
	wantTeams := map[string]bool{}
	for _, t := range multi(q["team"]) {
		wantTeams[strings.ToLower(t)] = true
	}
	filtered, err := s.comparePeople(r, wantTeams, strings.TrimSpace(q.Get("area")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	metrics, granularity, err := s.store.ComparePeople(r.Context(), filtered, from, to, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":        from,
		"to":          to,
		"granularity": granularity,
		"people":      orEmpty(metrics),
	})
}

// handleCompareTeams отдаёт метрики команд для вкладки сравнения команд:
// абсолютные события и средневзвешенный ряд «событий на присутствующего».
// Фильтры: team (multi), source, type, min_ai; бывшие сотрудники исключаются.
func (s *Server) handleCompareTeams(w http.ResponseWriter, r *http.Request) {
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
	wantTeams := map[string]bool{}
	for _, t := range multi(q["team"]) {
		wantTeams[strings.ToLower(t)] = true
	}

	people, err := s.store.ListPeople(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	exEmails := map[string]bool{}
	if s.hrdb != nil {
		if ex, err := s.hrdb.Current().ExEmployeeEmails(r.Context()); err == nil {
			exEmails = ex
		}
	}

	teams := map[string][]models.Person{}
	for _, p := range people {
		if p.Team == "" || exEmails[strings.ToLower(strings.TrimSpace(p.Email))] {
			continue
		}
		if len(wantTeams) > 0 && !wantTeams[strings.ToLower(p.Team)] {
			continue
		}
		teams[p.Team] = append(teams[p.Team], p)
	}

	minAI := 0.0
	if v := strings.TrimSpace(q.Get("min_ai")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			minAI = f
		}
	}
	f := models.EventFilter{
		From:       from,
		To:         to,
		Sources:    multi(q["source"]),
		Types:      multi(q["type"]),
		MinAIScore: minAI,
	}
	metrics, granularity, err := s.store.CompareTeams(r.Context(), teams, f, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":        from,
		"to":          to,
		"granularity": granularity,
		"teams":       orEmpty(metrics),
	})
}

// handleDays отдаёт особые дни человека за период — праздники (по офису из
// HRDB) и отпуска — для разметки матрицы активности. Выходные фронтенд
// вычисляет сам.
func (s *Server) handleDays(w http.ResponseWriter, r *http.Request) {
	f, loc, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	person, _ := s.store.GetPerson(r.Context(), f.PersonKey)
	person.Key = f.PersonKey
	// Особые дни с учётом корпоративного календаря праздников.
	days, err := s.store.EffectiveSpecialDays(r.Context(), person, f.From, f.To)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	office := person.Office

	// Отпуска/больничные/WFH/овертаймы берём ТОЛЬКО из person_days (их пишет
	// прогон: orchestrator.persistVACDays, источник vacjira). Никаких живых
	// запросов в jira.xtools.tv на рендере — иначе VPN/DNS-флап вешал весь
	// календарь до HTTP-таймаута, пряча и праздники, и отпуска. Овертайм-дни
	// (kind=overtime) — отдельный маркер: вынимаем их из days в overtimeDays.
	overtimeDays := []string{}
	{
		kept := days[:0]
		for _, d := range days {
			if d.Kind == models.DayOvertime {
				overtimeDays = append(overtimeDays, d.Day.Format("2006-01-02"))
				continue
			}
			kept = append(kept, d)
		}
		days = kept
		sort.Strings(overtimeDays)
	}

	// Гибридные дни — ISO-номера дней недели (1=Пн … 7=Вс), фронтенд сам
	// раскрашивает совпадающие даты; приоритет у отпуска/больничного/праздника.
	hybrid := []int{}
	for wd := range models.ParseWeekdays(person.HybridDays) {
		n := int(wd)
		if n == 0 {
			n = 7
		}
		hybrid = append(hybrid, n)
	}
	sort.Ints(hybrid)

	// Явные даты гибридных дней с учётом истории изменений шаблона (журнал
	// Assets + HCM): фронтенд красит их напрямую; недельный шаблон hybrid_days
	// остаётся текущим значением для легенды и старых клиентов. В ремоут- и
	// офис-периоды (история формата работы) шаблон не действует.
	hybridDates := []string{}
	if history, err := s.store.ListHybridHistory(r.Context(), f.PersonKey); err == nil {
		formats, _ := s.store.ListWorkFormatHistory(r.Context(), f.PersonKey)
		for d := f.From; d.Before(f.To); d = d.AddDate(0, 0, 1) {
			if fv := models.NormalizeWorkFormat(models.HybridDaysAt(formats, "", d)); fv == "remote" || fv == "office" {
				continue
			}
			set := models.ParseWeekdays(models.HybridDaysAt(history, person.HybridDays, d))
			if set[d.Weekday()] {
				hybridDates = append(hybridDates, d.Format("2006-01-02"))
			}
		}
	}

	// Дата найма — для отметки на матрице активности.
	hireDate := ""
	if person.HireDate != nil {
		hireDate = person.HireDate.Format("2006-01-02")
	}

	// Дни низкой активности — матрица красит их отдельной шкалой. Что считать
	// низкой активностью, задаётся в настройках для Area of Responsibility
	// человека (иначе — набор по умолчанию).
	shallowTypes := s.loadShallowConfig(r.Context()).typesFor(person.Area)
	fmts, _ := s.store.ListWorkFormatHistory(r.Context(), f.PersonKey)
	rdates := remoteDates(fmts, f.From, f.To, loc)
	shallowDays, err := s.store.ShallowDays(r.Context(), f.PersonKey, f.From, f.To, loc, shallowTypes, rdates)
	if err != nil {
		s.log.Warn("дни низкой активности не вычислены", "person", f.PersonKey, "err", err)
	}
	// Низкую активность в выходные/праздники/отпуск/больничный не показываем:
	// в нерабочий день её и не ждём. Отсекаем такие даты.
	if len(shallowDays) > 0 {
		off := map[string]bool{}
		for _, d := range days {
			if d.Kind == models.DayHoliday || d.Kind == models.DayVacation || d.Kind == models.DaySick {
				off[d.Day.Format("2006-01-02")] = true
			}
		}
		kept := shallowDays[:0]
		for _, ds := range shallowDays {
			t, _ := time.Parse("2006-01-02", ds)
			if off[ds] || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
				continue
			}
			kept = append(kept, ds)
		}
		shallowDays = kept
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"office":        office,
		"country":       models.CountryForOffice(office),
		"days":          orEmpty(days),
		"hybrid_days":   hybrid,
		"hybrid_dates":  hybridDates,
		"overtime_days": overtimeDays,
		"shallow_days":  orEmpty(shallowDays),
		"hire_date":     hireDate,
	})
}

func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	f, loc, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cells, err := s.store.Heatmap(r.Context(), f, loc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(cells))
}

// ---------- события ----------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	f, _, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.PerPage, _ = strconv.Atoi(q.Get("per_page"))
	f.SortDesc = q.Get("sort") != "asc"

	page, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	absorbed := s.enrichMeetPresence(r.Context(), page.Items)
	// Схлопывание «встреча + присутствие» в одну строку: поглощённые
	// meet-события скрываем, кроме явного просмотра сырого gwork
	// (фильтр по источникам без gcal — например, страница источника gwork).
	if len(absorbed) > 0 && showsGCal(f.Sources) {
		kept := page.Items[:0]
		for i := range page.Items {
			if !absorbed[i] {
				kept = append(kept, page.Items[i])
			}
		}
		page.Items = kept
	}
	writeJSON(w, http.StatusOK, page)
}

// showsGCal — лента без фильтра источников или с gcal в фильтре: дубль
// присутствия скрываем, потому что сама встреча в выдаче есть/возможна.
func showsGCal(sources []string) bool {
	if len(sources) == 0 {
		return true
	}
	for _, s := range sources {
		if s == "gcal" {
			return true
		}
	}
	return false
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	ev, err := s.store.GetEvent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	related, err := s.store.RelatedEvents(r.Context(), ev.PersonKey, ev.RefID, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	evs := []models.Event{ev}
	_ = s.enrichMeetPresence(r.Context(), evs) // карточку не скрываем, только метки
	writeJSON(w, http.StatusOK, map[string]any{"event": evs[0], "related": orEmpty(related)})
}

func (s *Server) handleRefEvents(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.RelatedEvents(r.Context(), personKey(r), chi.URLParam(r, "refID"), 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(items))
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.ListDocLinks(r.Context(), personKey(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(links))
}

// ---------- AI-оценка сообщений ----------

// requireAI возвращает клиент скорера или пишет 400, если AI выключен.
func (s *Server) requireAI(w http.ResponseWriter) *ai.Client {
	if s.ai == nil {
		writeErr(w, http.StatusBadRequest,
			errors.New("AI-скорер не настроен: поднимите контейнер ai-scorer и задайте AI_SCORER_URL"))
		return nil
	}
	return s.ai
}

func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	if s.ai == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	st, err := s.ai.Health(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true, "status": "unavailable", "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "status": st.Status, "model": st.Model,
		"labels": st.Labels, "min_labels": st.MinLabels,
		"trained": st.Trained, "mode": st.Mode,
	})
}

func (s *Server) handleAILabels(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	labels, err := cl.Labels(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": orEmpty(labels)})
}

func (s *Server) handleAIAddLabel(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	var req struct {
		Text  string  `json:"text"`
		Score float64 `json:"score"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" || req.Score < 0 || req.Score > 10 {
		writeErr(w, http.StatusBadRequest, errors.New("нужны text и score от 0 до 10"))
		return
	}
	if err := cl.AddLabel(r.Context(), req.Text, req.Score); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.handleAIStatus(w, r)
}

func (s *Server) handleAIDeleteLabel(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("некорректный id"))
		return
	}
	if err := cl.DeleteLabel(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.handleAIStatus(w, r)
}

func (s *Server) handleAITrain(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	st, err := cl.Train(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleAIRules(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	rules, err := cl.GetRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleAISaveRules(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	var req ai.Rules
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.MinLength < 0 || req.MinLength > 1000 {
		writeErr(w, http.StatusBadRequest, errors.New("min_length должен быть от 0 до 1000"))
		return
	}
	rules, err := cl.SetRules(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// handleAISample отдаёт сообщения Slack за период с текущими AI-оценками —
// материал для калибровки.
func (s *Server) handleAISample(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	to := parseTimeOr(q.Get("to"), time.Now().UTC())
	from := parseTimeOr(q.Get("from"), to.Add(-s.cfg.Sync.DefaultLookback))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	items, err := s.store.SlackTextsSample(r.Context(), from, to, false, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmpty(items)})
}

// handleAIRescore прогоняет сообщения Slack за период через скорер и пишет
// оценки в meta событий.
func (s *Server) handleAIRescore(w http.ResponseWriter, r *http.Request) {
	cl := s.requireAI(w)
	if cl == nil {
		return
	}
	var req struct {
		PersonKey    string `json:"person_key"`
		From         string `json:"from"`
		To           string `json:"to"`
		OnlyUnscored bool   `json:"only_unscored"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	to := parseTimeOr(req.To, time.Now().UTC())
	from := parseTimeOr(req.From, to.Add(-s.cfg.Sync.DefaultLookback))

	items, err := s.store.SlackTextsSample(r.Context(), from, to, req.OnlyUnscored, 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"scored": 0})
		return
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	scores, err := cl.Score(r.Context(), texts)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	byID := make(map[string]float64, len(items))
	for i, it := range items {
		byID[it.ID] = scores[i]
	}
	if err := s.store.SetSlackScores(r.Context(), byID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.UpdateEventScoresByExternalID(r.Context(), byID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scored": len(byID)})
}

// ---------- очистка данных ----------

func (s *Server) handlePurgePreview(w http.ResponseWriter, r *http.Request) {
	f, _, err := s.filterFrom(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.store.PurgePreview(r.Context(), f, boolParam(r.URL.Query().Get("sync_runs")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type purgeRequest struct {
	PersonKey string   `json:"person_key"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Sources   []string `json:"sources"`
	// SyncRuns — удалить заодно историю прогонов за период.
	SyncRuns bool `json:"sync_runs"`
	// ResetDocLinks — обнулить счётчики правок и комментариев по документам.
	ResetDocLinks bool `json:"reset_doc_links"`
	// Confirm — защита от случайного вызова: без него удаление не выполняется.
	Confirm bool `json:"confirm"`
}

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	var req purgeRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, errors.New("удаление требует confirm=true"))
		return
	}
	if strings.TrimSpace(req.PersonKey) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("параметр person_key обязателен"))
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		writeErr(w, http.StatusBadRequest,
			errors.New("период обязателен: удаление без границ не выполняется"))
		return
	}

	from := parseTimeOr(req.From, time.Time{})
	to := parseTimeOr(req.To, time.Time{})
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		writeErr(w, http.StatusBadRequest, errors.New("from должен быть раньше to"))
		return
	}

	f := models.EventFilter{
		PersonKey: req.PersonKey,
		From:      from,
		To:        to,
		Sources:   multi(req.Sources),
	}
	res, err := s.store.Purge(r.Context(), f, req.SyncRuns, req.ResetDocLinks)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Warn("удалены собранные данные",
		"person", res.PersonKey, "from", res.From, "to", res.To,
		"sources", f.Sources, "events", res.Events, "sync_runs", res.SyncRuns)
	writeJSON(w, http.StatusOK, res)
}

func boolParam(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// ---------- разбор параметров ----------

// filterFrom собирает EventFilter из query-параметров запроса.
func (s *Server) filterFrom(r *http.Request) (models.EventFilter, *time.Location, error) {
	q := r.URL.Query()
	key := personKey(r)
	if key == "" {
		return models.EventFilter{}, nil, errors.New("параметр person_key обязателен")
	}

	loc := time.UTC
	if tz := q.Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}

	to := parseTimeOr(q.Get("to"), time.Now().UTC())
	from := parseTimeOr(q.Get("from"), to.Add(-s.cfg.Sync.DefaultLookback))
	if !from.Before(to) {
		return models.EventFilter{}, nil, errors.New("from должен быть раньше to")
	}

	minAI := 0.0
	if v := strings.TrimSpace(q.Get("min_ai")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			minAI = f
		}
	}

	return models.EventFilter{
		PersonKey:  key,
		From:       from,
		To:         to,
		Sources:    multi(q["source"]),
		Types:      multi(q["type"]),
		Projects:   multi(q["project"]),
		Query:      q.Get("q"),
		MinAIScore: minAI,
	}, loc, nil
}

func personKey(r *http.Request) string {
	q := r.URL.Query()
	if v := q.Get("person_key"); v != "" {
		return v
	}
	return q.Get("person")
}

// multi поддерживает и повторяющиеся параметры (?source=a&source=b),
// и списки через запятую (?source=a,b).
func multi(values []string) []string {
	var out []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// parseTimeOr принимает RFC3339, "YYYY-MM-DD" или относительное "-30d"/"-12h".
func parseTimeOr(s string, def time.Time) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC()
	}
	if strings.HasPrefix(s, "-") && len(s) > 2 {
		if strings.HasSuffix(s, "d") {
			if n, err := strconv.Atoi(strings.TrimSuffix(s[1:], "d")); err == nil {
				return time.Now().UTC().AddDate(0, 0, -n)
			}
		}
		if d, err := time.ParseDuration(s); err == nil {
			return time.Now().UTC().Add(d)
		}
	}
	return def
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
