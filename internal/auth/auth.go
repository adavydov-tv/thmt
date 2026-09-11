// Package auth — аутентификация через Google OIDC (Workspace) и авторизация
// по ролям из таблицы users. Секретов Google хватает стандартных: code flow →
// обмен кода на токен → userinfo; JWT самостоятельно не разбираем — данные
// приходят напрямую от Google по TLS.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Роли доступа.
const (
	RoleViewer        = "viewer"
	RoleLead          = "lead"
	RoleSupervisor    = "supervisor"
	RoleAdministrator = "administrator"
)

// ValidRole — известная ли роль.
func ValidRole(r string) bool {
	switch r {
	case RoleViewer, RoleLead, RoleSupervisor, RoleAdministrator:
		return true
	}
	return false
}

// Config — параметры входа через Google.
type Config struct {
	Enabled      bool
	ClientID     string
	ClientSecret string
	// PublicURL — внешний адрес приложения (для redirect_uri).
	PublicURL string
	// Domain — допустимый Workspace-домен (hd claim), напр. tradingview.com.
	Domain string
	// AdminEmails — bootstrap-администраторы (создаются при старте).
	AdminEmails []string
	// CookieSecret — ключ подписи сессионной куки.
	CookieSecret string
	SessionTTL   time.Duration
}

// Store — то, что auth нужно от хранилища.
type Store interface {
	GetUserRole(ctx context.Context, email string) (string, error)
	UpsertUser(ctx context.Context, email, role, addedBy string) error
}

// Service — обработчики /auth/* и middleware.
type Service struct {
	cfg   Config
	store Store
	log   *slog.Logger
	hc    *http.Client

	// Кэш ролей: смена роли применяется без перелогина, но и БД не
	// дёргается на каждый запрос.
	mu    sync.Mutex
	roles map[string]roleEntry
}

type roleEntry struct {
	role string
	at   time.Time
}

const roleCacheTTL = 30 * time.Second

// User — аутентифицированный пользователь запроса.
type User struct {
	Email string
	Role  string
}

type ctxKey struct{}

// FromContext возвращает пользователя запроса (ok=false — аноним).
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// New создаёт сервис и заводит bootstrap-администраторов.
func New(cfg Config, store Store, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Enabled {
		if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CookieSecret == "" {
			return nil, fmt.Errorf("auth: включён, но не заданы AUTH_GOOGLE_CLIENT_ID / AUTH_GOOGLE_CLIENT_SECRET / AUTH_COOKIE_SECRET")
		}
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	s := &Service{cfg: cfg, store: store, log: log,
		hc: &http.Client{Timeout: 15 * time.Second}, roles: map[string]roleEntry{}}
	for _, email := range cfg.AdminEmails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := store.UpsertUser(ctx, email, RoleAdministrator, "env:ADMIN_EMAILS"); err != nil {
			log.Warn("не удалось завести администратора из ENV", "email", email, "err", err)
		}
		cancel()
	}
	return s, nil
}

// Enabled — включена ли аутентификация.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// ---------- сессионная кука ----------

const sessionCookie = "uad_session"
const stateCookie = "uad_oauth_state"

func (s *Service) sign(payload string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.CookieSecret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Service) encodeSession(email string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(email)) + "." + strconv.FormatInt(exp.Unix(), 10)
	return payload + "." + s.sign(payload)
}

func (s *Service) decodeSession(v string) (string, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(s.sign(payload)), []byte(parts[2])) {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	email, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	return string(email), true
}

func (s *Service) setSession(w http.ResponseWriter, r *http.Request, email string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.encodeSession(email, time.Now().Add(s.cfg.SessionTTL)),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.HasPrefix(s.cfg.PublicURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

// ---------- роль ----------

func (s *Service) roleOf(ctx context.Context, email string) string {
	s.mu.Lock()
	if e, ok := s.roles[email]; ok && time.Since(e.at) < roleCacheTTL {
		s.mu.Unlock()
		return e.role
	}
	s.mu.Unlock()
	role, err := s.store.GetUserRole(ctx, email)
	if err != nil {
		s.log.Warn("не удалось получить роль", "email", email, "err", err)
		return ""
	}
	s.mu.Lock()
	s.roles[email] = roleEntry{role: role, at: time.Now()}
	s.mu.Unlock()
	return role
}

// InvalidateRoles сбрасывает кэш ролей (после правок в админке).
func (s *Service) InvalidateRoles() {
	s.mu.Lock()
	s.roles = map[string]roleEntry{}
	s.mu.Unlock()
}

// ---------- OIDC flow ----------

func (s *Service) redirectURI() string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/auth/callback"
}

// HandleLogin отправляет на страницу согласия Google.
func (s *Service) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	buf := make([]byte, 18)
	_, _ = rand.Read(buf)
	state := base64.RawURLEncoding.EncodeToString(buf)
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" || !strings.HasPrefix(redirect, "/") {
		redirect = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state + "|" + url.QueryEscape(redirect),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	q := url.Values{
		"client_id":     {s.cfg.ClientID},
		"redirect_uri":  {s.redirectURI()},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	if s.cfg.Domain != "" {
		q.Set("hd", s.cfg.Domain)
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusFound)
}

type tokenResp struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

type userInfo struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HD            string `json:"hd"`
	Name          string `json:"name"`
}

// HandleCallback — обмен кода на токен, проверка домена и выдача сессии.
func (s *Service) HandleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sc, err := r.Cookie(stateCookie)
	if err != nil || sc.Value == "" {
		http.Redirect(w, r, "/?auth_error=state", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/", MaxAge: -1})
	parts := strings.SplitN(sc.Value, "|", 2)
	if len(parts) != 2 || parts[0] != q.Get("state") {
		http.Redirect(w, r, "/?auth_error=state", http.StatusFound)
		return
	}
	redirect, _ := url.QueryUnescape(parts[1])
	if redirect == "" || !strings.HasPrefix(redirect, "/") {
		redirect = "/"
	}

	form := url.Values{
		"code":          {q.Get("code")},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"redirect_uri":  {s.redirectURI()},
		"grant_type":    {"authorization_code"},
	}
	resp, err := s.hc.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		s.log.Error("auth: обмен кода не удался", "err", err)
		http.Redirect(w, r, "/?auth_error=exchange", http.StatusFound)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var tok tokenResp
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		s.log.Error("auth: пустой access_token", "google_error", tok.Error, "desc", tok.ErrorDesc)
		http.Redirect(w, r, "/?auth_error=exchange", http.StatusFound)
		return
	}

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		"https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := s.hc.Do(req)
	if err != nil {
		http.Redirect(w, r, "/?auth_error=userinfo", http.StatusFound)
		return
	}
	ubody, _ := io.ReadAll(uresp.Body)
	_ = uresp.Body.Close()
	var ui userInfo
	if json.Unmarshal(ubody, &ui) != nil || ui.Email == "" || !ui.EmailVerified {
		http.Redirect(w, r, "/?auth_error=userinfo", http.StatusFound)
		return
	}
	email := strings.ToLower(strings.TrimSpace(ui.Email))
	// Домен проверяем дважды: hd-claim и суффикс почты (hd нет у обычных
	// gmail-аккаунтов — они отсекаются именно здесь).
	if s.cfg.Domain != "" &&
		(ui.HD != s.cfg.Domain || !strings.HasSuffix(email, "@"+s.cfg.Domain)) {
		s.log.Warn("auth: чужой домен", "email", email, "hd", ui.HD)
		http.Redirect(w, r, "/?auth_error=domain", http.StatusFound)
		return
	}
	// Авторизация: пользователь должен быть заведён администратором.
	if s.roleOf(r.Context(), email) == "" {
		s.log.Warn("auth: пользователь не в списке доступа", "email", email)
		http.Redirect(w, r, "/?auth_error=not_allowed", http.StatusFound)
		return
	}
	s.setSession(w, r, email)
	s.log.Info("auth: вход", "email", email)
	http.Redirect(w, r, redirect, http.StatusFound)
}

// HandleLogout снимает сессию.
func (s *Service) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

// Authenticate достаёт пользователя из куки; при выключенном auth все
// запросы идут от имени встроенного администратора (локальная разработка).
func (s *Service) Authenticate(r *http.Request) (User, bool) {
	if !s.cfg.Enabled {
		return User{Email: "dev@local", Role: RoleAdministrator}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return User{}, false
	}
	email, ok := s.decodeSession(c.Value)
	if !ok {
		return User{}, false
	}
	role := s.roleOf(r.Context(), email)
	if role == "" {
		return User{}, false // доступ отозвали — сессия больше не действует
	}
	return User{Email: email, Role: role}, true
}

// WithUser кладёт пользователя в контекст запроса.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// Allowed — карта функционала по ролям: можно ли роли выполнить запрос.
//
//	administrator — всё;
//	supervisor    — всё, кроме вкладки AI, удаления данных и общих настроек;
//	lead          — как supervisor (данные дополнительно режутся HRDB-скоупом);
//	viewer        — только чтение, без AI.
func Allowed(role, method, path string) bool {
	if strings.HasPrefix(path, "/api/auth/") || path == "/api/health" || path == "/api/meta" {
		return true
	}
	switch role {
	case RoleAdministrator:
		return true
	case RoleViewer:
		if strings.HasPrefix(path, "/api/ai/") || strings.HasPrefix(path, "/api/users") ||
			path == "/api/slack/text" {
			return false
		}
		return method == http.MethodGet
	case RoleSupervisor, RoleLead:
		if strings.HasPrefix(path, "/api/ai/") ||
			strings.HasPrefix(path, "/api/purge") ||
			strings.HasPrefix(path, "/api/users") ||
			path == "/api/slack/text" {
			return false
		}
		if role == RoleLead && path == "/api/slack/relink" {
			return false
		}
		if strings.HasPrefix(path, "/api/settings/") && method != http.MethodGet {
			return false
		}
		if method == http.MethodDelete || path == "/api/people/cleanup-ex" {
			return false
		}
		// lead не заводит людей: его зона — существующие карточки поддерева.
		if role == RoleLead && method == http.MethodPost &&
			(path == "/api/people" || path == "/api/people/discover") {
			return false
		}
		return true
	}
	return false
}
