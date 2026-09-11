package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/auth"
	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
)

// authMiddleware: аутентификация по сессии, проверка прав роли на маршрут и
// (для роли lead) generic-проверка скоупа по query-параметру person/person_key.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.auth.Authenticate(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "требуется вход"})
			return
		}
		if !auth.Allowed(user.Role, r.Method, r.URL.Path) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "недостаточно прав"})
			return
		}
		if user.Role == auth.RoleLead {
			if pk := personKey(r); pk != "" {
				scope := s.leadScope(r, user.Email)
				if scope != nil && !scope.persons[pk] {
					writeJSON(w, http.StatusForbidden,
						map[string]string{"error": "человек вне вашей зоны ответственности (HRDB)"})
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.Authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "enabled": s.auth.Enabled()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":   user.Email,
		"role":    user.Role,
		"enabled": s.auth.Enabled(),
	})
}

// ---------- управление пользователями (только administrator) ----------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": orEmpty(users)})
}

func (s *Server) handleUpsertUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") || !auth.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest,
			errStr("нужны корректный email и роль: viewer | lead | supervisor | administrator"))
		return
	}
	actor, _ := auth.FromContext(r.Context())
	if err := s.store.UpsertUser(r.Context(), req.Email, req.Role, actor.Email); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.auth.InvalidateRoles()
	s.invalidateLeadScopes()
	s.log.Info("роль пользователя обновлена", "email", req.Email, "role", req.Role, "by", actor.Email)
	s.handleListUsers(w, r)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
	actor, _ := auth.FromContext(r.Context())
	if email == "" {
		writeErr(w, http.StatusBadRequest, errStr("нужен параметр email"))
		return
	}
	if strings.EqualFold(email, actor.Email) {
		writeErr(w, http.StatusBadRequest, errStr("нельзя удалить самого себя"))
		return
	}
	if err := s.store.DeleteUser(r.Context(), email); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.auth.InvalidateRoles()
	s.log.Warn("доступ пользователя отозван", "email", email, "by", actor.Email)
	s.handleListUsers(w, r)
}

type errString string

func (e errString) Error() string { return string(e) }

func errStr(s string) error { return errString(s) }

// ---------- HRDB-скоуп роли lead ----------

// leadScopeData — что разрешено конкретному lead: его поддерево оргструктуры.
type leadScopeData struct {
	persons map[string]bool // person key дашборда
	teams   map[string]bool // имена юнитов поддерева (в нижнем регистре)
	at      time.Time
}

const leadScopeTTL = 5 * time.Minute

func (s *Server) invalidateLeadScopes() {
	s.scopeMu.Lock()
	s.leadScopes = map[string]*leadScopeData{}
	s.scopeMu.Unlock()
}

// leadScope строит (с кэшем) поддерево HRDB для lead: юниты, где он указан
// руководителем (Team lead), плюс все вложенные, плюс люди этих команд.
// nil — скоуп не ограничен (auth выключен или роль не lead).
func (s *Server) leadScope(r *http.Request, email string) *leadScopeData {
	s.scopeMu.Lock()
	if sc, ok := s.leadScopes[email]; ok && time.Since(sc.at) < leadScopeTTL {
		s.scopeMu.Unlock()
		return sc
	}
	s.scopeMu.Unlock()

	ctx := r.Context()
	scope := &leadScopeData{persons: map[string]bool{}, teams: map[string]bool{}, at: time.Now()}

	// Отображаемое имя lead — им заполнен атрибут Team lead в структуре.
	leadName := ""
	people, err := s.store.ListPeople(ctx)
	if err != nil {
		s.log.Warn("lead-скоуп: список людей недоступен", "err", err)
		return scope
	}
	for _, p := range people {
		if strings.EqualFold(p.Email, email) || strings.EqualFold(p.GoogleEmail, email) {
			leadName = p.DisplayName
			scope.persons[p.Key] = true // сам себя lead всегда видит
		}
	}
	var structure map[string]hrdb.StructureUnit
	if c := s.hrdb.Current(); c != nil {
		if leadName == "" {
			if emp, found, err := c.FindByEmail(ctx, email); err == nil && found {
				leadName = emp.DisplayName
			}
		}
		if st, err := c.Structure(ctx); err == nil {
			structure = st
		}
	}
	if leadName == "" || structure == nil {
		s.scopeMu.Lock()
		s.leadScopes[email] = scope
		s.scopeMu.Unlock()
		return scope
	}

	// Юнит разрешён, если он сам или любой его предок возглавляется lead.
	allowedUnit := func(name string) bool {
		cur := strings.ToLower(strings.TrimSpace(name))
		for i := 0; i < 10 && cur != ""; i++ { // защита от циклов Parent team
			u, ok := structure[cur]
			if !ok {
				return false
			}
			if strings.EqualFold(u.Lead, leadName) {
				return true
			}
			cur = strings.ToLower(strings.TrimSpace(u.Parent))
		}
		return false
	}
	for name := range structure {
		if allowedUnit(name) {
			scope.teams[name] = true
		}
	}
	for _, p := range people {
		if scope.teams[strings.ToLower(strings.TrimSpace(p.Team))] {
			scope.persons[p.Key] = true
		}
	}

	s.scopeMu.Lock()
	s.leadScopes[email] = scope
	s.scopeMu.Unlock()
	return scope
}

// requestScope — скоуп текущего запроса: nil, если ограничений нет.
func (s *Server) requestScope(r *http.Request) *leadScopeData {
	user, ok := auth.FromContext(r.Context())
	if !ok || user.Role != auth.RoleLead {
		return nil
	}
	return s.leadScope(r, user.Email)
}
