package sync

import (
	"context"
	"strconv"
	"time"
)

// Ключи настроек ежедневного обновления (правятся в общих настройках UI).
const (
	SettingDailyEnabled     = "daily_sync_enabled"
	SettingDailyTime        = "daily_sync_time" // HH:MM, локальное время сервера
	SettingDailyLastDate    = "daily_sync_last" // защита от повторного запуска в тот же день
	SettingDailyLastAt      = "daily_sync_last_at"
	SettingDailyLastStarted = "daily_sync_last_started"
	// SettingDailyCatchup — режим «догона»: собирать не фиксированные прошедшие
	// сутки, а весь интервал с момента последнего успешного прогона (последней
	// дырки). Если сервер простаивал несколько дней, один запуск закрывает всё.
	SettingDailyCatchup = "daily_sync_catchup"
)

// DailyScheduler раз в сутки в настроенное время обновляет данные всех людей
// за прошедший день (StartMany — пул SYNC_PEOPLE_CONCURRENCY воркеров).
// Настройки читаются из БД на каждом тике: правки в UI подхватываются без
// рестарта. Если сервер был выключен в назначенное время, запуск произойдёт
// сразу после старта (условие «время прошло, а сегодня ещё не бегали»).
func (o *Orchestrator) DailyScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if v, err := o.store.GetSetting(ctx, SettingDailyEnabled); err != nil || v != "true" {
			continue
		}
		hhmm, _ := o.store.GetSetting(ctx, SettingDailyTime)
		sched, err := time.ParseInLocation("15:04", hhmm, time.Local)
		if err != nil {
			continue // время не настроено или мусор — молча ждём корректного
		}
		now := time.Now()
		today := now.Format("2006-01-02")
		runAt := time.Date(now.Year(), now.Month(), now.Day(), sched.Hour(), sched.Minute(), 0, 0, time.Local)
		if now.Before(runAt) {
			continue
		}
		if last, _ := o.store.GetSetting(ctx, SettingDailyLastDate); last == today {
			continue
		}
		// Сначала помечаем день выполненным — двойной запуск хуже пропуска.
		if err := o.store.SetSetting(ctx, SettingDailyLastDate, today); err != nil {
			o.log.Warn("ежедневное обновление: не удалось зафиксировать дату", "err", err)
			continue
		}

		people, err := o.store.ListPeople(ctx)
		if err != nil {
			o.log.Error("ежедневное обновление: список людей недоступен", "err", err)
			continue
		}
		// Окно «прошедший день»: с начала вчерашних суток до текущего момента —
		// перекрытие с прошлым запуском безвредно, persist перезаписывает период.
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -1)
		// Режим «догон»: если включён и известен момент прошлого успешного
		// прогона — начинаем с него (закрываем дырку целиком), но не глубже
		// потолка SYNC_DEFAULT_LOOKBACK, чтобы одиночный первый запуск не улетел
		// на месяцы. Берём самую раннюю из двух точек — «вчера» остаётся полом.
		if catchup, _ := o.store.GetSetting(ctx, SettingDailyCatchup); catchup == "true" {
			if lastAt, _ := o.store.GetSetting(ctx, SettingDailyLastAt); lastAt != "" {
				if t, err := time.Parse(time.RFC3339, lastAt); err == nil && t.Before(from) {
					from = t
				}
			}
			if floor := now.Add(-o.cfg.Sync.DefaultLookback); from.Before(floor) {
				from = floor
			}
		}
		// Ежедневный прогон — инкрементальный (force=false): собирает только
		// новое поверх уже собранного, перепроверяя хвост по настройке reprobe.
		runs := o.StartMany(people, from, now, nil, false)
		_ = o.store.SetSetting(ctx, SettingDailyLastAt, now.Format(time.RFC3339))
		_ = o.store.SetSetting(ctx, SettingDailyLastStarted, strconv.Itoa(len(runs)))
		o.log.Info("ежедневное обновление запущено",
			"people", len(people), "started", len(runs),
			"from", from.Format("2006-01-02 15:04"), "workers", o.Workers())
	}
}
