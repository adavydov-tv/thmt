// Command googleauth однократно проводит OAuth-флоу Google и печатает
// refresh token для GOOGLE_OAUTH_REFRESH_TOKEN.
//
// Нужен, когда нет прав администратора Google Workspace и настроить
// domain-wide delegation для сервис-аккаунта невозможно: в режиме oauth
// сервис ходит в Drive от вашего имени и видит ровно то, что видите вы.
//
// Использование:
//
//	go run ./cmd/googleauth
//
// Client ID и secret берутся из GOOGLE_OAUTH_CLIENT_ID / GOOGLE_OAUTH_CLIENT_SECRET
// (в том числе из .env) либо из флагов -id и -secret.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/adavydov/user-activity-dashboard/internal/collectors/gcal"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gdocs"
	"github.com/adavydov/user-activity-dashboard/internal/config"
)

// scopes — всё, что нужно обоим Google-коллекторам: Drive/Docs и Calendar.
// Токен запрашивается сразу со всеми правами, чтобы не перевыпускать его
// при включении календаря.
var scopes = append(append([]string{}, gdocs.Scopes...), gcal.Scope)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nОшибка:", err)
		os.Exit(1)
	}
}

func run() error {
	id, secret, port, err := params()
	if err != nil {
		return err
	}

	// Слушаем loopback: Google разрешает произвольный порт для клиентов типа
	// «Desktop app», поэтому регистрировать redirect URI заранее не нужно.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%s", port))
	if err != nil {
		return fmt.Errorf("не удалось занять порт %s: %w", port, err)
	}
	defer func() { _ = ln.Close() }()

	redirect := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	oauthCfg := &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirect,
		Scopes:       scopes,
	}

	state := fmt.Sprintf("uad-%d", time.Now().UnixNano())
	authURL := oauthCfg.AuthCodeURL(state,
		// offline + consent обязательны: без них Google повторно не выдаёт
		// refresh token, если вы уже разрешали доступ этому приложению.
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	fmt.Println("Откройте в браузере (если он не открылся сам):")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	fmt.Println("Ожидаю подтверждения на " + redirect + " …")
	openBrowser(authURL)

	code, err := waitForCode(ln, state)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("обмен кода на токен не удался: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("Google не вернул refresh token. Обычно это значит, что доступ уже был выдан ранее: " +
			"отзовите приложение на https://myaccount.google.com/permissions и повторите")
	}

	fmt.Println()
	fmt.Println("Готово. Добавьте в .env:")
	fmt.Println()
	fmt.Println("GOOGLE_AUTH_MODE=oauth")
	fmt.Println("GOOGLE_OAUTH_CLIENT_ID=" + id)
	fmt.Println("GOOGLE_OAUTH_CLIENT_SECRET=" + secret)
	fmt.Println("GOOGLE_OAUTH_REFRESH_TOKEN=" + tok.RefreshToken)
	fmt.Println()
	fmt.Println("Выданные scopes: " + strings.Join(scopes, ", "))
	return nil
}

// params собирает client id/secret из флагов или окружения (включая .env).
func params() (id, secret, port string, err error) {
	port = "0" // 0 — занять любой свободный порт
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-id", "--id":
			if i+1 < len(args) {
				i++
				id = args[i]
			}
		case "-secret", "--secret":
			if i+1 < len(args) {
				i++
				secret = args[i]
			}
		case "-port", "--port":
			if i+1 < len(args) {
				i++
				port = args[i]
			}
		case "-h", "--help":
			fmt.Println("Использование: go run ./cmd/googleauth [-id CLIENT_ID] [-secret CLIENT_SECRET] [-port N]")
			os.Exit(0)
		}
	}

	if id == "" || secret == "" {
		// config.Load подтягивает .env, так что значения можно не дублировать.
		if cfg, cerr := config.Load(); cerr == nil {
			if id == "" {
				id = cfg.Google.ClientID
			}
			if secret == "" {
				secret = cfg.Google.ClientSecret
			}
		}
	}

	var missing []string
	if id == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_ID (или -id)")
	}
	if secret == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_SECRET (или -secret)")
	}
	if len(missing) > 0 {
		return "", "", "", fmt.Errorf("не заданы: %s\n\n"+
			"Создайте OAuth-клиент типа «Desktop app» в GCP:\n"+
			"  APIs & Services → Credentials → Create credentials → OAuth client ID → Desktop app\n"+
			"и убедитесь, что в проекте включены Google Drive API и Drive Activity API.",
			strings.Join(missing, ", "))
	}
	return id, secret, port, nil
}

// waitForCode поднимает одноразовый HTTP-обработчик и ждёт редирект от Google.
func waitForCode(ln net.Listener, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				reply(w, "Доступ не выдан: "+e)
				done <- result{err: fmt.Errorf("пользователь отклонил доступ: %s", e)}
				return
			}
			if q.Get("state") != state {
				reply(w, "Неверный state — возможно, открыта старая вкладка.")
				return // не завершаем: ждём правильный редирект
			}
			code := q.Get("code")
			if code == "" {
				reply(w, "В ответе нет кода авторизации.")
				return
			}
			reply(w, "Готово. Вернитесь в терминал — refresh token напечатан там.")
			done <- result{code: code}
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	select {
	case res := <-done:
		return res.code, res.err
	case <-time.After(5 * time.Minute):
		return "", errors.New("истекло время ожидания подтверждения (5 минут)")
	}
}

func reply(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8">
<title>user-activity-dashboard</title>
<body style="font:16px/1.5 system-ui,-apple-system,sans-serif;padding:48px;max-width:38em">
<h1 style="font-size:20px">%s</h1></body>`, msg)
}

// openBrowser пытается открыть ссылку; молча сдаётся, если не вышло —
// URL всё равно напечатан в терминал.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}
