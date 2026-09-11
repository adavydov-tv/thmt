// Package gdocs собирает активность пользователя в Google Docs/Drive через
// Drive Activity API v2 и Drive API v3.
//
// Поддерживаются два режима авторизации (см. config.GoogleConfig.Mode):
//
//   - service_account — JSON-ключ сервис-аккаунта. Чтобы видеть активность
//     конкретного человека, а не самого сервис-аккаунта, обязателен
//     domain-wide delegation: в Google Workspace Admin ключу выдаются нужные
//     scopes, а в конфиге проставляется GOOGLE_IMPERSONATE_SUBJECT — email
//     сотрудника, от имени которого ходим в API. Без impersonation сервис-аккаунт
//     видит только те файлы, которые ему явно расшарены, и в Drive Activity не
//     будет активности сотрудника.
//   - oauth — client id/secret приложения плюс refresh token пользователя.
//     Ходим прямо от имени человека, поэтому Drive Activity помечает его
//     действия флагом KnownUser.IsCurrentUser.
package gdocs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/adavydov/user-activity-dashboard/internal/config"
)

// Scopes — минимальный набор прав, необходимый коллектору: чтение ленты
// активности Drive и чтение метаданных файлов.
var Scopes = []string{
	"https://www.googleapis.com/auth/drive.activity.readonly",
	"https://www.googleapis.com/auth/drive.readonly",
	"https://www.googleapis.com/auth/drive.metadata.readonly",
}

// NewHTTPClient собирает HTTP-клиент с авторизацией Google по настройкам cfg.
// Клиент пригоден для передачи в option.WithHTTPClient.
func NewHTTPClient(ctx context.Context, cfg config.GoogleConfig) (*http.Client, error) {
	return NewHTTPClientWithScopes(ctx, cfg, Scopes)
}

// NewHTTPClientWithScopes — то же, но с явным списком scopes. Нужен другим
// Google-коллекторам (например, Calendar): у сервис-аккаунта scopes
// запрашиваются на каждый токен, и общий расширенный список сломал бы
// уже настроенный domain-wide delegation для Drive.
func NewHTTPClientWithScopes(ctx context.Context, cfg config.GoogleConfig, scopes []string) (*http.Client, error) {
	switch cfg.Mode {
	case config.GoogleAuthServiceAccount:
		return serviceAccountClient(ctx, cfg, scopes)
	case config.GoogleAuthOAuth:
		return oauthClient(ctx, cfg, scopes)
	case "":
		return nil, errors.New("gdocs: не задан режим авторизации Google (GOOGLE_AUTH_MODE)")
	default:
		return nil, fmt.Errorf("gdocs: неизвестный режим авторизации Google %q: допустимы %q или %q",
			cfg.Mode, config.GoogleAuthServiceAccount, config.GoogleAuthOAuth)
	}
}

// serviceAccountClient строит клиент по JSON-ключу сервис-аккаунта.
func serviceAccountClient(ctx context.Context, cfg config.GoogleConfig, scopes []string) (*http.Client, error) {
	raw, err := serviceAccountJSON(cfg)
	if err != nil {
		return nil, err
	}

	jwtCfg, err := google.JWTConfigFromJSON(raw, scopes...)
	if err != nil {
		return nil, fmt.Errorf("gdocs: не удалось разобрать JSON-ключ сервис-аккаунта: %w", err)
	}
	// Subject включает domain-wide delegation: запросы уходят от имени
	// указанного сотрудника. Без него мы увидим только активность самого
	// сервис-аккаунта, что почти всегда пусто.
	if s := strings.TrimSpace(cfg.ImpersonateSubject); s != "" {
		jwtCfg.Subject = s
	}

	ts := jwtCfg.TokenSource(ctx)
	if err := probe(ts); err != nil {
		return nil, explainTokenError(err, cfg, jwtCfg.Email, scopes)
	}
	return oauth2.NewClient(ctx, ts), nil
}

// serviceAccountJSON достаёт ключ из файла или из inline-значения конфига.
func serviceAccountJSON(cfg config.GoogleConfig) ([]byte, error) {
	if path := strings.TrimSpace(cfg.CredentialsFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("gdocs: не удалось прочитать файл ключа %q: %w", path, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("gdocs: файл ключа %q пуст", path)
		}
		return raw, nil
	}
	if inline := strings.TrimSpace(cfg.CredentialsJSON); inline != "" {
		return []byte(inline), nil
	}
	return nil, errors.New("gdocs: для режима service_account нужен GOOGLE_CREDENTIALS_FILE или GOOGLE_CREDENTIALS_JSON")
}

// oauthClient строит клиент по refresh token пользователя.
func oauthClient(ctx context.Context, cfg config.GoogleConfig, scopes []string) (*http.Client, error) {
	var missing []string
	if strings.TrimSpace(cfg.ClientID) == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_ID")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_SECRET")
	}
	if strings.TrimSpace(cfg.RefreshToken) == "" {
		missing = append(missing, "GOOGLE_OAUTH_REFRESH_TOKEN")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("gdocs: для режима oauth не хватает параметров: %s", strings.Join(missing, ", "))
	}

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       scopes,
	}
	// Access token добудется по refresh token; проверяем это сразу, чтобы
	// проблема с доступом всплыла на старте, а не посреди сбора.
	ts := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: cfg.RefreshToken})
	if err := probe(ts); err != nil {
		return nil, explainTokenError(err, cfg, cfg.ClientID, scopes)
	}
	return oauth2.NewClient(ctx, ts), nil
}

// probe запрашивает access token один раз — дешёвая проверка «настройки живые».
func probe(ts oauth2.TokenSource) error {
	_, err := ts.Token()
	return err
}

// explainTokenError превращает лаконичные ответы Google OAuth в инструкцию:
// сами по себе `unauthorized_client` и `invalid_grant` не подсказывают,
// что именно надо донастроить.
func explainTokenError(err error, cfg config.GoogleConfig, actor string, scopes []string) error {
	text := err.Error()
	var hint string

	switch {
	case strings.Contains(text, "unauthorized_client"):
		if cfg.Mode == config.GoogleAuthServiceAccount && strings.TrimSpace(cfg.ImpersonateSubject) != "" {
			hint = "для сервис-аккаунта не настроен domain-wide delegation. " +
				"В GCP включите его в свойствах сервис-аккаунта и скопируйте числовой Client ID, " +
				"затем в Google Admin → Security → Access and data control → API controls → " +
				"Domain-wide delegation добавьте этот Client ID со scopes: " + strings.Join(scopes, ",") + ". " +
				"Изменения применяются в течение нескольких минут. " +
				"Если прав администратора Workspace нет — переключитесь на GOOGLE_AUTH_MODE=oauth " +
				"(получить refresh token: go run ./cmd/googleauth)"
		} else {
			hint = "клиенту не разрешены запрошенные scopes; проверьте consent screen и список разрешённых scopes у OAuth-клиента"
		}
	case strings.Contains(text, "invalid_grant"):
		hint = "refresh token недействителен: он отозван, истёк, выдан для другого client_id " +
			"или приложение осталось в статусе Testing (там токен живёт 7 дней). " +
			"Выпустите новый: go run ./cmd/googleauth"
	case strings.Contains(text, "invalid_client"):
		hint = "не совпадают GOOGLE_OAUTH_CLIENT_ID / GOOGLE_OAUTH_CLIENT_SECRET"
	case strings.Contains(text, "access_denied"):
		hint = "доступ запрещён политикой организации: администратор Workspace ограничил доступ сторонних приложений к Drive"
	}

	if hint == "" {
		return fmt.Errorf("gdocs: не удалось получить токен Google (%s): %w", actor, err)
	}
	return fmt.Errorf("gdocs: не удалось получить токен Google (%s): %w\n  → %s", actor, err, hint)
}
