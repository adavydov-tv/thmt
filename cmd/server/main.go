// Command server — точка входа сервиса сбора и визуализации активности.
//
// Все секреты и параметры берутся из переменных окружения (см. .env.example).
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/ai"
	"github.com/adavydov/user-activity-dashboard/internal/api"
	authsvc "github.com/adavydov/user-activity-dashboard/internal/auth"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/discovery"
	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/overtime"
	"github.com/adavydov/user-activity-dashboard/internal/perfreview"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
	syncsvc "github.com/adavydov/user-activity-dashboard/internal/sync"
)

// hrdbPersist хранит снапшоты кэшей HRDB в Postgres (по источнику).
type hrdbPersist struct {
	store *storage.Store
	mode  string
}

func (h hrdbPersist) Load(ctx context.Context, kind string) ([]byte, time.Time, error) {
	return h.store.GetHRDBCache(ctx, h.mode, kind)
}

func (h hrdbPersist) Save(ctx context.Context, kind string, payload []byte) error {
	return h.store.SaveHRDBCache(ctx, h.mode, kind, payload)
}

func main() {
	var (
		migrateOnly = flag.Bool("migrate", false, "применить схему БД и выйти")
		collectOnce = flag.Bool("collect", false, "выполнить один сбор и выйти (CLI-режим)")
		personKey   = flag.String("person", "", "ключ пользователя для -collect")
		fromFlag    = flag.String("from", "", "начало периода для -collect (YYYY-MM-DD)")
		toFlag      = flag.String("to", "", "конец периода для -collect (YYYY-MM-DD)")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка конфигурации:", err)
		os.Exit(1)
	}

	log := newLogger(cfg.Server.LogLevel)
	slog.SetDefault(log)
	log.Info("конфигурация загружена", "cfg", cfg.Redacted())

	if len(cfg.EnabledSources()) == 0 {
		log.Warn("ни один источник не сконфигурирован — сбор работать не будет",
			"hint", "заполните токены в .env, см. .env.example")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := storage.New(ctx, cfg.Postgres)
	if err != nil {
		log.Error("не удалось подключиться к PostgreSQL", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// Ключ шифрования текстов Slack-архива (AES-256-GCM).
	if k := cfg.Slack.TextKey; k != "" {
		key, kerr := base64.StdEncoding.DecodeString(k)
		if kerr != nil || len(key) != 32 {
			log.Error("SLACK_TEXT_KEY должен быть base64 от 32 байт (openssl rand -base64 32)")
			os.Exit(1)
		}
		if err := store.SetTextKey(key); err != nil {
			log.Error("не удалось включить шифрование текстов", "err", err)
			os.Exit(1)
		}
		log.Info("тексты Slack-архива шифруются (AES-256-GCM)")
	} else {
		log.Warn("SLACK_TEXT_KEY не задан — тексты архива Slack хранятся открытыми")
	}

	// Разовая миграция: тексты старых slack-событий → зашифрованный архив,
	// события обезличиваются (в ленте остаются id + AI-оценка).
	if done, _ := store.GetSetting(ctx, "slack_archive_migrated"); done != "1" {
		ppl, _ := store.ListPeople(ctx)
		uidBy := map[string]string{}
		for _, p := range ppl {
			if p.SlackUser != "" {
				uidBy[p.Key] = p.SlackUser
			}
		}
		if n, merr := store.MigrateSlackTextsToArchive(ctx, uidBy); merr != nil {
			log.Error("миграция slack-текстов в архив не удалась", "err", merr)
		} else {
			if n > 0 {
				log.Info("slack-тексты перенесены в архив, события обезличены", "events", n)
			}
			_ = store.SetSetting(ctx, "slack_archive_migrated", "1")
		}
	}

	// Прогоны сбора живут только в памяти процесса: всё, что осталось в БД
	// со статусом running/pending, — от прежнего процесса и заведомо мертво.
	if n, err := store.FailStaleRuns(ctx); err != nil {
		log.Warn("не удалось пометить зависшие прогоны", "err", err)
	} else if n > 0 {
		log.Info("зависшие прогоны прежнего процесса помечены прерванными", "count", n)
	}

	if *migrateOnly {
		if err := store.Migrate(ctx); err != nil {
			log.Error("миграция не удалась", "err", err)
			os.Exit(1)
		}
		log.Info("схема применена")
		return
	}

	// Люди из ENV — источник истины при старте; правки через API их дополняют.
	for _, p := range cfg.People {
		if _, err := store.UpsertPerson(ctx, models.Person{
			Key: p.Key, DisplayName: p.DisplayName, Email: p.Email,
			JiraAccount: p.JiraAccount, GitLabUser: p.GitLabUser,
			SlackUser: p.SlackUser, GoogleEmail: p.GoogleEmail,
		}); err != nil {
			log.Error("не удалось сохранить пользователя из ENV", "key", p.Key, "err", err)
		}
	}

	// HRDB опционален: без workspace id блок в UI просто скрывается.
	// Настроены обе HRDB — новая (Atlassian Cloud) и старая (Insight на
	// jira.xtools.tv); активную выбирает переключатель в настройках, выбор
	// хранится в БД. Клиент нужен и оркестратору — офис сотрудника определяет
	// праздничный календарь в матрице активности.
	var hrdbClient *hrdb.Switcher
	if cfg.HRDB.Enabled {
		clients := map[string]*hrdb.Client{}
		if c, cErr := hrdb.New(cfg.HRDB, cfg.Jira.Email, cfg.Jira.APIToken,
			cfg.Sync.HTTPTimeout, cfg.Sync.MaxRetries); cErr != nil {
			log.Warn("новая HRDB (cloud) недоступна", "err", cErr)
		} else {
			clients["cloud"] = c
		}
		if c, cErr := hrdb.NewDC(cfg.HRDB, cfg.Sync.HTTPTimeout, cfg.Sync.MaxRetries); cErr != nil {
			log.Warn("старая HRDB (jira.xtools.tv) недоступна", "err", cErr)
		} else {
			clients["dc"] = c
		}
		// Снапшоты кэшей в БД: после рестарта сервис отвечает мгновенно из
		// снапшота, свежесть догоняет фоновое обновление.
		for name, c := range clients {
			c.SetPersist(ctx, hrdbPersist{store: store, mode: name})
		}
		mode := cfg.HRDB.Mode
		if saved, sErr := store.GetSetting(ctx, "hrdb_mode"); sErr == nil && saved != "" {
			mode = saved
		}
		// Прогрев активного клиента стартует внутри переключателя: полная
		// выкачка HRDB занимает ~30 секунд, и без прогрева их ждал бы первый
		// открывший страницу настроек.
		hrdbClient = hrdb.NewSwitcher(ctx, log, mode, clients)
		if hrdbClient == nil {
			log.Warn("HRDB недоступен, блок сотрудников выключен")
			cfg.HRDB.Enabled = false
		} else {
			log.Info("HRDB активна", "mode", hrdbClient.Mode(), "available", hrdbClient.Available())
			// Неактивный источник без снапшота прогреваем один раз в фоне,
			// чтобы переключение на него в UI не упиралось в холодную выкачку.
			for name, c := range clients {
				if name == hrdbClient.Mode() || c.HasData() {
					continue
				}
				go func(name string, c *hrdb.Client) {
					if _, err := c.ListEmployees(ctx); err != nil {
						log.Warn("прогрев неактивной HRDB не удался", "hrdb", name, "err", err)
						return
					}
					_, _ = c.Structure(ctx)
					_, _ = c.ExEmployeeEmails(ctx)
					log.Info("неактивная HRDB прогрета", "hrdb", name)
				}(name, c)
			}
		}
	}

	// Локальный AI-скорер сообщений (docker-контейнер ai-scorer).
	var aiClient *ai.Client
	if cfg.AI.Enabled {
		if aiClient, err = ai.New(cfg.AI, cfg.Sync.MaxRetries); err != nil {
			log.Warn("AI-скорер недоступен, оценка сообщений выключена", "err", err)
			aiClient = nil
		}
	}

	orch := syncsvc.New(cfg, store, hrdbClient, aiClient, log)

	if *collectOnce {
		if err := runCollectOnce(ctx, cfg, store, orch, log, *personKey, *fromFlag, *toFlag); err != nil {
			log.Error("сбор не удался", "err", err)
			os.Exit(1)
		}
		return
	}

	// Ежедневное обновление данных по расписанию из общих настроек.
	go orch.DailyScheduler(ctx)

	disc := discovery.New(cfg, log)

	// Аутентификация Google OIDC + роли. При AUTH_ENABLED=false все запросы
	// идут от встроенного администратора (локальный режим).
	authService, err := authsvc.New(authsvc.Config{
		Enabled:      cfg.Auth.Enabled,
		ClientID:     cfg.Auth.ClientID,
		ClientSecret: cfg.Auth.ClientSecret,
		PublicURL:    cfg.Auth.PublicURL,
		Domain:       cfg.Auth.Domain,
		AdminEmails:  cfg.Auth.AdminEmails,
		CookieSecret: cfg.Auth.CookieSecret,
		SessionTTL:   cfg.Auth.SessionTTL,
	}, store, log)
	if err != nil {
		log.Error("конфигурация аутентификации некорректна", "err", err)
		os.Exit(1)
	}
	if cfg.Auth.Enabled {
		log.Info("аутентификация включена", "domain", cfg.Auth.Domain, "admins", len(cfg.Auth.AdminEmails))
	} else {
		log.Warn("аутентификация ВЫКЛЮЧЕНА (AUTH_ENABLED=false) — доступ без входа")
	}

	// Заявки на овертаймы из Jira DC — для вкладки «Нарушения».
	var overtimeClient *overtime.Client
	if cfg.Overtime.Enabled {
		if overtimeClient, err = overtime.New(cfg.Overtime, cfg.Sync.HTTPTimeout, cfg.Sync.MaxRetries, log); err != nil {
			log.Warn("овертаймы недоступны", "err", err)
			overtimeClient = nil
		} else {
			// Снапшот в БД + фоновый прогрев широкого окна заявок: вкладка
			// отклонений не ждёт Jira DC даже сразу после рестарта.
			overtimeClient.SetPersist(ctx, hrdbPersist{store: store, mode: "overtime"})
			orch.SetVAC(overtimeClient) // отпуска/больничные/WFH из VAC → person_days
			go overtimeClient.WarmUp(ctx, log)
		}
	}

	// Оценки Performance Review и PIP из Jira DC (проект PR) — HR-колонки
	// вкладки «По системе». Прогрев фоном: Jira DC за прокси отвечает секундами.
	var perfClient *perfreview.Client
	if cfg.PerfReview.Enabled {
		if perfClient, err = perfreview.New(cfg.PerfReview, cfg.Sync.HTTPTimeout, cfg.Sync.MaxRetries, log); err != nil {
			log.Warn("performance review недоступен", "err", err)
			perfClient = nil
		} else {
			go perfClient.WarmUp(ctx, log)
		}
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           api.NewServer(cfg, store, orch, hrdbClient, disc, aiClient, overtimeClient, perfClient, authService, log).Router(),
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Порт занимаем ДО того, как отрапортовать о запуске. Иначе при уже
	// работающем экземпляре в лог уходит «сервер запущен», следом ошибка bind,
	// а процесс молча завершается с нулевым кодом — и потом непонятно, почему
	// браузер разговаривает со старой сборкой.
	listener, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		log.Error("не удалось занять адрес", "addr", cfg.Server.Addr, "err", err,
			"hint", "порт уже занят другим экземпляром; найдите его: "+
				"lsof -nP -iTCP"+portOf(cfg.Server.Addr)+" -sTCP:LISTEN, затем kill -9 <PID>")
		os.Exit(1)
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("сервер запущен", "addr", listener.Addr().String(), "sources", cfg.EnabledSources())
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			cancel()
			return
		}
		serveErr <- nil
	}()

	<-ctx.Done()
	log.Info("останавливаемся...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("некорректная остановка", "err", err)
	}

	// Падение сервера — это ненулевой код возврата: иначе make и systemd
	// считают, что всё прошло успешно.
	select {
	case err := <-serveErr:
		if err != nil {
			log.Error("сервер остановлен с ошибкой", "err", err)
			os.Exit(1)
		}
	default:
	}
}

// portOf выделяет ":порт" из адреса вида ":8080" или "127.0.0.1:8080" —
// нужен только для подсказки в сообщении об ошибке.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return ":" + port
	}
	return addr
}

// runCollectOnce — сбор из командной строки, удобно для крона.
func runCollectOnce(ctx context.Context, cfg *config.Config, store *storage.Store,
	orch *syncsvc.Orchestrator, log *slog.Logger, key, fromS, toS string) error {

	if key == "" {
		return errors.New("укажите -person")
	}
	person, err := store.GetPerson(ctx, key)
	if err != nil {
		return fmt.Errorf("пользователь %q: %w", key, err)
	}

	to := time.Now().UTC()
	if toS != "" {
		if t, err := time.Parse("2006-01-02", toS); err == nil {
			to = t.UTC()
		}
	}
	from := to.Add(-cfg.Sync.DefaultLookback)
	if fromS != "" {
		if t, err := time.Parse("2006-01-02", fromS); err == nil {
			from = t.UTC()
		}
	}

	// CLI-сбор явного окна — с перезаписью (force=true): собираем ровно
	// запрошенный интервал, не сужая его инкрементальным покрытием.
	run, err := orch.Start(person, from, to, nil, true)
	if err != nil {
		return err
	}
	log.Info("сбор запущен", "run_id", run.ID, "from", from.Format(time.DateOnly), "to", to.Format(time.DateOnly))

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cur, err := orch.Get(ctx, run.ID)
			if err != nil {
				return err
			}
			switch cur.Status {
			case models.SyncDone, models.SyncFailed, models.SyncPartial:
				for name, sr := range cur.Sources {
					log.Info("источник", "name", name, "status", sr.Status,
						"events", sr.Events, "ms", sr.DurationMS, "err", sr.Error, "note", sr.Note)
				}
				if cur.Status == models.SyncFailed {
					return errors.New(cur.Error)
				}
				return nil
			}
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
