// Package sync запускает коллекторы, складывает результат в хранилище и
// отслеживает состояние прогонов.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/ai"
	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/allure"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/argocd"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/claude"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/confluence"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/figma"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gcal"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gdocs"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gitlab"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/grafana"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/gwork"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/jenkins"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/jira"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/netsuite"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/slack"
	"github.com/adavydov/user-activity-dashboard/internal/collectors/zabbix"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/hrdb"
	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/overtime"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
)

// Orchestrator управляет сбором активности.
type Orchestrator struct {
	cfg   *config.Config
	store *storage.Store
	log   *slog.Logger
	// hrdb может быть nil: офис тогда не определяется автоматически.
	hrdb *hrdb.Switcher
	// ai может быть nil: сообщения Slack тогда не получают AI-оценку.
	ai *ai.Client

	// runs хранит состояние активных прогонов в памяти; в БД пишется финальное
	// и промежуточное состояние, но опрос статуса из UI идёт отсюда — дешевле.
	mu   sync.RWMutex
	runs map[string]*models.SyncRun
	// inflight не даёт запустить два одновременных сбора по одному человеку.
	inflight map[string]string
	// cancels — отмена контекста активного прогона (runID → cancel).
	cancels map[string]context.CancelFunc
	// cancelled помечает прогоны, остановленные вручную: очередь StartMany их
	// пропускает, а finish проставляет им человекочитаемую причину.
	cancelled map[string]bool
	// overwrite помечает прогоны с принудительной перезаписью интервала: сбор
	// игнорирует инкрементальное покрытие и тянет всё окно [from,to] заново.
	overwrite map[string]bool
	// onFinish вызывается после завершения каждого прогона (сброс кэшей).
	onFinish func()
	// vac — клиент заявок VAC (отпуска/больничные/WFH/овертаймы) старой Jira;
	// nil, если не сконфигурирован. Абсансы пишутся в person_days.
	vac *overtime.Client
}

// SetVAC подключает клиент заявок VAC для записи отсутствий в person_days.
func (o *Orchestrator) SetVAC(c *overtime.Client) { o.vac = c }

// SetOnFinish подключает колбэк завершения прогона.
func (o *Orchestrator) SetOnFinish(fn func()) {
	o.mu.Lock()
	o.onFinish = fn
	o.mu.Unlock()
}

// New создаёт оркестратор. hrdbClient и aiClient могут быть nil.
func New(cfg *config.Config, store *storage.Store, hrdbClient *hrdb.Switcher, aiClient *ai.Client, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{
		cfg:       cfg,
		store:     store,
		log:       log,
		hrdb:      hrdbClient,
		ai:        aiClient,
		runs:      make(map[string]*models.SyncRun),
		inflight:  make(map[string]string),
		cancels:   make(map[string]context.CancelFunc),
		cancelled: make(map[string]bool),
		overwrite: make(map[string]bool),
	}
}

// ErrAlreadyRunning — по этому человеку уже идёт сбор.
var ErrAlreadyRunning = errors.New("сбор по этому пользователю уже выполняется")

// Start запускает сбор в фоне и сразу возвращает созданный прогон. force —
// принудительная перезапись интервала (игнорировать инкрементальное покрытие).
func (o *Orchestrator) Start(person models.Person, from, to time.Time, sources []string, force bool) (models.SyncRun, error) {
	run, err := o.createRun(person, from, to, sources)
	if err != nil {
		return run, err
	}
	go o.execute(run.ID, person, from, to, force)
	return run, nil
}

// StartMany запускает сбор по нескольким людям пулом фоновых воркеров
// (SYNC_PEOPLE_CONCURRENCY). Полная параллельность устроила бы шквал запросов
// во внешние API, поэтому одновременно идёт лишь несколько человек; внутри
// каждого прогона источники и так собираются параллельно (SYNC_CONCURRENCY).
// Люди, по которым сбор уже идёт, пропускаются.
func (o *Orchestrator) StartMany(people []models.Person, from, to time.Time, sources []string, force bool) []models.SyncRun {
	type job struct {
		runID  string
		person models.Person
	}
	var jobs []job
	runs := make([]models.SyncRun, 0, len(people))
	for _, person := range people {
		run, err := o.createRun(person, from, to, sources)
		if err != nil {
			continue
		}
		runs = append(runs, run)
		jobs = append(jobs, job{runID: run.ID, person: person})
	}
	go func() {
		// Slack, Jenkins, Zabbix и пр. собираются «централизованно»: один обход
		// внешней системы на всю группу вместо обхода на каждого человека —
		// иначе N людей × M каналов упираются в rate-limit (наблюдали 429).
		// Slack — самый большой и долгий, поэтому идёт СВОЕЙ горутиной,
		// остальные групповые — второй, per-person пул — параллельно обеим.
		// Прогон финализирует tryFinish — тот, кто закончил последним.
		var phases sync.WaitGroup
		if len(jobs) > 1 {
			enabled := o.resolveSources(sources)
			people := make([]models.Person, 0, len(jobs))
			ids := make([]string, 0, len(jobs))
			for _, j := range jobs {
				people = append(people, j.person)
				ids = append(ids, j.runID)
			}
			type phase struct {
				name string
				run  func()
			}
			var rest []phase
			addRest := func(name string, build func(ctx context.Context) (groupCollector, error)) {
				if contains(enabled, name) {
					rest = append(rest, phase{name, func() { o.groupSource(ids, people, from, to, name, build) }})
				}
			}
			addRest("jenkins", func(ctx context.Context) (groupCollector, error) {
				return jenkins.New(o.cfg.Jenkins, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("zabbix", func(ctx context.Context) (groupCollector, error) {
				return zabbix.New(o.cfg.Zabbix.Instances, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("argocd", func(ctx context.Context) (groupCollector, error) {
				return argocd.New(o.cfg.ArgoCD.Instances, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("grafana", func(ctx context.Context) (groupCollector, error) {
				return grafana.New(o.cfg.Grafana, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("figma", func(ctx context.Context) (groupCollector, error) {
				return figma.New(o.cfg.Figma, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("netsuite", func(ctx context.Context) (groupCollector, error) {
				return netsuite.New(o.cfg.NetSuite, o.cfg.Sync.HTTPTimeout, o.log)
			})
			addRest("claude", func(ctx context.Context) (groupCollector, error) {
				return claude.New(o.cfg.Claude, o.cfg.Sync.HTTPTimeout, o.log)
			})

			// Пометить групповые источники ДО старта per-person пула: execute()
			// не должен собирать их сам, даже если доберётся до человека раньше
			// групповой фазы.
			groupNames := make([]string, 0, len(rest)+1)
			if contains(enabled, "slack") {
				groupNames = append(groupNames, "slack")
			}
			for _, p := range rest {
				groupNames = append(groupNames, p.name)
			}
			for _, j := range jobs {
				for _, src := range groupNames {
					src := src
					o.update(j.runID, func(r *models.SyncRun) {
						sr := r.Sources[src]
						sr.Grouped = true
						sr.Note = "ждёт групповой фазы"
						r.Sources[src] = sr
					})
				}
			}

			if contains(enabled, "slack") {
				phases.Add(1)
				go func() {
					defer phases.Done()
					o.groupSlack(ids, people, from, to)
				}()
			}
			if len(rest) > 0 {
				phases.Add(1)
				go func() {
					defer phases.Done()
					for _, p := range rest {
						p.run()
					}
				}()
			}
		}

		workers := max(1, o.cfg.Sync.PeopleConcurrency)
		if workers > len(jobs) {
			workers = len(jobs)
		}
		ch := make(chan job)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					o.execute(j.runID, j.person, from, to, force)
				}
			}()
		}
		for _, j := range jobs {
			ch <- j
		}
		close(ch)
		wg.Wait()
		phases.Wait()
	}()
	return runs
}

// groupCollector — коллектор, умеющий собирать сразу всю группу людей за один
// набор запросов к внешней системе (Jenkins, Zabbix).
type groupCollector interface {
	CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error)
}

// groupSource — общая групповая фаза массового сбора для источников с
// централизованным обходом: один запрос на группу, результат раскладывается
// по прогонам, источник закрывается здесь (execute его не перезапускает).
func (o *Orchestrator) groupSource(runIDs []string, people []models.Person, from, to time.Time,
	source string, build func(ctx context.Context) (groupCollector, error)) {

	ctx, cancel := context.WithTimeout(context.Background(), o.cfg.Sync.RunTimeout)
	defer cancel()
	started := time.Now()

	setAll := func(mut func(sr *models.SourceRun)) {
		for _, id := range runIDs {
			o.update(id, func(r *models.SyncRun) {
				sr := r.Sources[source]
				mut(&sr)
				r.Sources[source] = sr
			})
		}
	}
	setAll(func(sr *models.SourceRun) { sr.Status = models.SyncRunning; sr.Note = "групповой сбор" })

	col, err := build(ctx)
	var byPerson map[string][]models.Event
	var note string
	if err == nil {
		byPerson, note, err = col.CollectGroup(ctx, people, from, to)
	}
	durMS := time.Since(started).Milliseconds()
	if err != nil {
		o.log.Error("групповой сбор не удался", "source", source, "err", err, "people", len(people))
		msg := err.Error()
		setAll(func(sr *models.SourceRun) { sr.Status = models.SyncFailed; sr.Error = msg; sr.DurationMS = durMS })
		for _, id := range runIDs {
			o.tryFinish(ctx, id)
		}
		return
	}
	total := 0
	for i, id := range runIDs {
		o.mu.RLock()
		cancelledRun := o.cancelled[id]
		o.mu.RUnlock()
		if cancelledRun {
			o.tryFinish(ctx, id)
			continue
		}
		person := people[i]
		events := byPerson[person.Key]
		req := collectors.Request{Person: person, From: from.UTC(), To: to.UTC()}
		sr := models.SourceRun{Source: source, Status: models.SyncDone, Events: len(events), DurationMS: durMS, Note: note, Grouped: true}
		if err := o.persist(ctx, req, source, events); err != nil {
			sr.Status = models.SyncFailed
			sr.Error = err.Error()
		} else {
			total += len(events)
		}
		o.update(id, func(r *models.SyncRun) { r.Sources[source] = sr })
		o.tryFinish(ctx, id)
	}
	o.log.Info("групповой сбор завершён", "source", source, "people", len(people), "events", total,
		"duration", time.Since(started).Round(time.Second).String())
}

// groupSlack — канал-центричная фаза массового сбора: один обход каналов на
// всех людей группы. Результат раскладывается по прогонам: у каждого run
// slack-источник закрывается здесь, execute() его уже не перезапускает.
func (o *Orchestrator) groupSlack(runIDs []string, people []models.Person, from, to time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), o.cfg.Sync.RunTimeout)
	defer cancel()
	started := time.Now()

	setAll := func(mut func(sr *models.SourceRun)) {
		for _, id := range runIDs {
			o.update(id, func(r *models.SyncRun) {
				sr := r.Sources["slack"]
				mut(&sr)
				r.Sources["slack"] = sr
			})
		}
	}
	setAll(func(sr *models.SourceRun) {
		sr.Status = models.SyncRunning
		sr.Note = "групповой обход каналов"
	})

	col, err := slack.New(o.cfg.Slack, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
	if err == nil {
		col.SetRawSink(o.slackSink)
		col.SetArchive(slackCoverage{o.store}, o.readSlackArchive)
		// Прогресс общего обхода транслируется в каждый прогон группы.
		col.SetProgress(func(stage string, done, total int) {
			note := fmt.Sprintf("групповой обход: %s %d/%d", stage, done, total)
			setAll(func(sr *models.SourceRun) { sr.Note = note })
		})
	}
	var byPerson map[string][]models.Event
	var resolved map[string]string
	var note string
	if err == nil {
		byPerson, resolved, note, err = col.CollectGroup(ctx, people, from, to)
	}
	durMS := time.Since(started).Milliseconds()
	if err != nil {
		o.log.Error("групповой сбор slack не удался", "err", err, "people", len(people))
		msg := err.Error()
		setAll(func(sr *models.SourceRun) {
			sr.Status = models.SyncFailed
			sr.Error = msg
			sr.DurationMS = durMS
		})
		for _, id := range runIDs {
			o.tryFinish(ctx, id)
		}
		return
	}

	// Найденные по email slack-id — в карточки, чтобы не искать заново.
	for personKey, uid := range resolved {
		if err := o.store.SetPersonSourceID(ctx, personKey, "slack", uid); err != nil {
			o.log.Warn("не удалось сохранить slack-id", "person", personKey, "err", err)
		}
	}

	total := 0
	for i, id := range runIDs {
		person := people[i]
		o.mu.RLock()
		cancelledRun := o.cancelled[id]
		o.mu.RUnlock()
		if cancelledRun {
			o.tryFinish(ctx, id)
			continue
		}
		events := byPerson[person.Key]
		req := collectors.Request{Person: person, From: from.UTC(), To: to.UTC()}
		sr := models.SourceRun{Source: "slack", Status: models.SyncDone,
			Events: len(events), DurationMS: durMS, Note: note, Grouped: true}
		if err := o.persist(ctx, req, "slack", events); err != nil {
			sr.Status = models.SyncFailed
			sr.Error = err.Error()
		} else {
			o.scoreSlack(ctx, "slack", events)
			total += len(events)
		}
		o.update(id, func(r *models.SyncRun) { r.Sources["slack"] = sr })
		o.tryFinish(ctx, id)
	}
	o.log.Info("групповой сбор slack завершён",
		"people", len(people), "events", total, "duration", time.Since(started).Round(time.Second).String())
}

// RelinkSlack строит slack-события людей из канал-центричного архива БЕЗ
// похода в Slack: кейс «собрали по 39, добавили ещё 650» — новые люди
// получают события линковкой по их slack_uid. personKeys пуст — все люди.
func (o *Orchestrator) RelinkSlack(ctx context.Context, personKeys []string) (people, events int, err error) {
	from, to, err := o.store.SlackArchiveSpan(ctx)
	if err != nil || from.IsZero() {
		return 0, 0, fmt.Errorf("архив slack пуст — сначала выполните сбор: %w", err)
	}
	to = to.Add(time.Second)
	all, err := o.store.ListPeople(ctx)
	if err != nil {
		return 0, 0, err
	}
	want := map[string]bool{}
	for _, k := range personKeys {
		want[k] = true
	}
	for _, p := range all {
		if len(want) > 0 && !want[p.Key] {
			continue
		}
		uid := strings.TrimSpace(p.SlackUser)
		if uid == "" {
			continue // без slack-id линковать нечего; id появится при сборе
		}
		rows, err := o.store.SlackMessagesForUID(ctx, uid, from, to)
		if err != nil {
			return people, events, err
		}
		evs := make([]models.Event, 0, len(rows))
		for _, m := range rows {
			evs = append(evs, slack.EventFromArchive(p.Key, archivedFromStorage(m)))
		}
		req := collectors.Request{Person: p, From: from.UTC(), To: to.UTC()}
		if err := o.persist(ctx, req, "slack", evs); err != nil {
			return people, events, err
		}
		people++
		events += len(evs)
	}
	o.log.Info("линковка slack-архива завершена", "people", people, "events", events,
		"span", from.Format("2006-01-02")+".."+to.Format("2006-01-02"))
	return people, events, nil
}

// archivedFromStorage переводит строку архива из хранилища в тип коллектора:
// событие из неё строит slack.EventFromArchive.
func archivedFromStorage(m storage.SlackMessage) slack.ArchivedMessage {
	return slack.ArchivedMessage{
		ID: m.ID, ChannelID: m.ChannelID, ChannelName: m.ChannelName, UID: m.UID,
		Kind: m.Kind, TS: m.TS, SlackTS: m.SlackTS, ThreadTS: m.ThreadTS, URL: m.URL,
		ReplyCount: m.ReplyCount, Reactions: m.Reactions, Reaction: m.Reaction, AIScore: m.AIScore,
	}
}

// slackCoverage — адаптер учёта покрытия архива поверх Store.
type slackCoverage struct{ store *storage.Store }

func (s slackCoverage) Covered(ctx context.Context, channelID string) ([][2]time.Time, error) {
	return s.store.SlackCoverage(ctx, channelID)
}

func (s slackCoverage) Mark(ctx context.Context, channelID string, from, to time.Time) error {
	return s.store.MarkSlackCoverage(ctx, channelID, from, to)
}

// readSlackArchive — читатель архива для коллектора (линковка по slack_uid).
func (o *Orchestrator) readSlackArchive(ctx context.Context, uid string, from, to time.Time) ([]slack.ArchivedMessage, error) {
	rows, err := o.store.SlackMessagesForUID(ctx, uid, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]slack.ArchivedMessage, 0, len(rows))
	for _, m := range rows {
		out = append(out, archivedFromStorage(m))
	}
	return out, nil
}

// Queue — снапшот очереди: активные и ожидающие прогоны из памяти,
// отсортированные по времени постановки. Для карточки очереди в UI.
func (o *Orchestrator) Queue() (running, pending []models.SyncRun) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, run := range o.runs {
		switch run.Status {
		case models.SyncRunning:
			running = append(running, *run)
		case models.SyncPending:
			pending = append(pending, *run)
		}
	}
	sort.Slice(running, func(i, j int) bool { return running[i].StartedAt.Before(running[j].StartedAt) })
	sort.Slice(pending, func(i, j int) bool { return pending[i].StartedAt.Before(pending[j].StartedAt) })
	return running, pending
}

// Workers — размер пула массового сбора (для оценки времени очереди).
func (o *Orchestrator) Workers() int {
	return max(1, o.cfg.Sync.PeopleConcurrency)
}

// createRun регистрирует новый прогон, не запуская сбор.
func (o *Orchestrator) createRun(person models.Person, from, to time.Time, sources []string) (models.SyncRun, error) {
	o.mu.Lock()
	if id, busy := o.inflight[person.Key]; busy {
		if run := o.runs[id]; run != nil &&
			(run.Status == models.SyncRunning || run.Status == models.SyncPending) {
			snapshot := *run
			o.mu.Unlock()
			return snapshot, ErrAlreadyRunning
		}
		delete(o.inflight, person.Key)
	}

	runID := fmt.Sprintf("%s-%d", person.Key, time.Now().UnixNano())
	run := &models.SyncRun{
		ID:        runID,
		PersonKey: person.Key,
		From:      from.UTC(),
		To:        to.UTC(),
		Status:    models.SyncPending,
		StartedAt: time.Now().UTC(),
		Sources:   map[string]models.SourceRun{},
	}
	for _, s := range o.resolveSources(sources) {
		run.Sources[s] = models.SourceRun{Source: s, Status: models.SyncPending}
	}
	o.runs[runID] = run
	o.inflight[person.Key] = runID
	o.mu.Unlock()

	snapshot := *run
	_ = o.store.SaveSyncRun(context.Background(), snapshot)
	return snapshot, nil
}

// resolveSources пересекает запрошенные источники с включёнными в конфиге.
func (o *Orchestrator) resolveSources(requested []string) []string {
	enabled := o.cfg.EnabledSources()
	if len(requested) == 0 {
		return enabled
	}
	set := make(map[string]bool, len(enabled))
	for _, s := range enabled {
		set[s] = true
	}
	var out []string
	for _, s := range requested {
		s = strings.ToLower(strings.TrimSpace(s))
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// Cancel останавливает прогон. Активному отменяется контекст — коллекторы
// прерываются, уже собранные частичные данные сохраняются; ожидающему в
// очереди сразу проставляется финальный статус. Прогон, оставшийся в БД от
// прежнего процесса, помечается прерванным.
func (o *Orchestrator) Cancel(ctx context.Context, runID string) (models.SyncRun, error) {
	o.mu.Lock()
	if run, ok := o.runs[runID]; ok {
		if run.Status != models.SyncPending && run.Status != models.SyncRunning {
			snapshot := *run
			o.mu.Unlock()
			return snapshot, nil
		}
		if cancel := o.cancels[runID]; cancel != nil {
			// Активный: рвём контекст, execute сам доведёт прогон до finish.
			o.cancelled[runID] = true
			o.mu.Unlock()
			cancel()
			o.log.Info("прогон остановлен вручную", "run_id", runID)
			return o.Get(ctx, runID)
		}
		// В очереди: исполнителя ещё нет — завершаем сами.
		o.cancelled[runID] = true
		now := time.Now().UTC()
		run.Status = models.SyncFailed
		run.Error = "остановлен вручную"
		run.FinishedAt = &now
		for k, sr := range run.Sources {
			if sr.Status == models.SyncPending || sr.Status == models.SyncRunning {
				sr.Status = models.SyncFailed
				sr.Error = "отменён"
				run.Sources[k] = sr
			}
		}
		if o.inflight[run.PersonKey] == runID {
			delete(o.inflight, run.PersonKey)
		}
		snapshot := *run
		o.mu.Unlock()
		_ = o.store.SaveSyncRun(context.Background(), snapshot)
		o.log.Info("прогон снят из очереди", "run_id", runID)
		return snapshot, nil
	}
	o.mu.Unlock()

	// Не в памяти — запись от прежнего процесса сервера.
	run, err := o.store.GetSyncRun(ctx, runID)
	if err != nil {
		return models.SyncRun{}, err
	}
	if run.Status == models.SyncPending || run.Status == models.SyncRunning {
		now := time.Now().UTC()
		run.Status = models.SyncFailed
		run.Error = "прерван перезапуском сервера"
		run.FinishedAt = &now
		if err := o.store.SaveSyncRun(ctx, run); err != nil {
			return run, err
		}
	}
	return run, nil
}

// IsBusy сообщает, идёт ли сейчас сбор по человеку.
func (o *Orchestrator) IsBusy(personKey string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	id, busy := o.inflight[personKey]
	if !busy {
		return false
	}
	run := o.runs[id]
	return run != nil && (run.Status == models.SyncRunning || run.Status == models.SyncPending)
}

// Get возвращает состояние прогона: сперва из памяти, затем из БД.
func (o *Orchestrator) Get(ctx context.Context, id string) (models.SyncRun, error) {
	o.mu.RLock()
	run, ok := o.runs[id]
	o.mu.RUnlock()
	if ok {
		return *run, nil
	}
	return o.store.GetSyncRun(ctx, id)
}

// execute — тело фонового прогона. force — принудительная перезапись интервала
// (сбор игнорирует инкрементальное покрытие).
func (o *Orchestrator) execute(runID string, person models.Person, from, to time.Time, force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), o.cfg.Sync.RunTimeout)
	defer cancel()

	o.mu.Lock()
	if o.cancelled[runID] {
		// Остановлен, пока стоял в очереди StartMany: статус уже проставлен.
		delete(o.cancelled, runID)
		o.mu.Unlock()
		return
	}
	o.cancels[runID] = cancel
	if force {
		o.overwrite[runID] = true
	}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.cancels, runID)
		delete(o.overwrite, runID)
		o.mu.Unlock()
	}()

	o.update(runID, func(r *models.SyncRun) { r.Status = models.SyncRunning })

	o.ensureOffice(ctx, &person)
	req := collectors.Request{Person: person, From: from.UTC(), To: to.UTC()}

	o.mu.RLock()
	sources := make([]string, 0, len(o.runs[runID].Sources))
	for s, sr := range o.runs[runID].Sources {
		// Групповые источники (slack и пр.) собираются параллельной фазой —
		// execute() их не трогает независимо от её текущего статуса.
		if sr.Grouped {
			continue
		}
		if sr.Status == models.SyncPending || sr.Status == models.SyncRunning {
			sources = append(sources, s)
		}
	}
	o.mu.RUnlock()

	// Jira выполняется первой и синхронно: она поставляет ссылки на Google-доки
	// (TSD), без которых коллектору Google нечего обходить в режиме A.
	var jiraDocs []models.DocLink
	if contains(sources, "jira") {
		res := o.runOne(ctx, runID, "jira", req, func() (collectors.Collector, error) {
			return jira.New(o.cfg.Jira, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
		})
		jiraDocs = res.DocLinks
		if len(jiraDocs) > 0 {
			if err := o.store.SaveDocLinks(ctx, jiraDocs); err != nil {
				o.log.Error("не удалось сохранить ссылки на документы", "err", err)
			}
		}
	}

	// Остальные источники — параллельно, с ограничением по конкурентности.
	rest := make([]string, 0, 3)
	for _, s := range sources {
		if s != "jira" {
			rest = append(rest, s)
		}
	}

	sem := make(chan struct{}, max(1, o.cfg.Sync.Concurrency))
	var wg sync.WaitGroup
	for _, src := range rest {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			switch src {
			case "gitlab":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return gitlab.New(o.cfg.GitLab, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
				})
			case "slack":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					col, err := slack.New(o.cfg.Slack, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
					if err == nil {
						col.SetRawSink(o.slackSink)
						col.SetArchive(slackCoverage{o.store}, o.readSlackArchive)
					}
					return col, err
				})
			case "gdocs":
				o.runGDocs(ctx, runID, req, jiraDocs)
			case "gcal":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return gcal.New(ctx, o.cfg.Google, o.log)
				})
			case "allure":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return allure.New(o.cfg.Allure, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
				})
			case "confluence":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return confluence.New(o.cfg.Confluence, o.cfg.Sync.HTTPTimeout, o.cfg.Sync.MaxRetries, o.log)
				})
			case "gwork":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return gwork.New(ctx, o.cfg.Google, o.cfg.GWork, o.log)
				})
			case "argocd":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return argocd.New(o.cfg.ArgoCD.Instances, o.cfg.Sync.HTTPTimeout, o.log)
				})
			case "zabbix":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return zabbix.New(o.cfg.Zabbix.Instances, o.cfg.Sync.HTTPTimeout, o.log)
				})
			case "jenkins":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return jenkins.New(o.cfg.Jenkins, o.cfg.Sync.HTTPTimeout, o.log)
				})
			case "grafana":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return grafana.New(o.cfg.Grafana, o.cfg.Sync.HTTPTimeout, o.log)
				})
			case "figma":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return figma.New(o.cfg.Figma, o.cfg.Sync.HTTPTimeout, o.log)
				})
			case "claude":
				o.runOne(ctx, runID, src, req, func() (collectors.Collector, error) {
					return claude.New(o.cfg.Claude, o.cfg.Sync.HTTPTimeout, o.log)
				})
			}
		}()
	}
	wg.Wait()

	// Отсутствия из заявок VAC (надёжнее календаря): отпуска/больничные/WFH
	// пишем в person_days отдельным источником, не затирая дни календаря.
	o.persistVACDays(ctx, person, from, to)

	// История изменений гибридных дней из журнала Assets: правила отклонений
	// применяют шаблон, действовавший на дату, а не текущий.
	o.persistHybridHistory(ctx, person)

	o.tryFinish(ctx, runID)
}

// tryFinish финализирует прогон, когда закончили ВСЕ его части: и execute,
// и групповые фазы, идущие параллельно (slack — отдельной горутиной).
// Вызывается из каждой части; финализирует та, что закончила последней,
// до этого прогон честно остаётся running.
func (o *Orchestrator) tryFinish(ctx context.Context, runID string) {
	o.mu.RLock()
	run, ok := o.runs[runID]
	// Status == pending значит execute ещё не стартовал — финализировать рано,
	// даже если групповые источники уже готовы.
	ready := ok && run.FinishedAt == nil && run.Status != models.SyncPending
	if ready {
		for _, sr := range run.Sources {
			if sr.Status == models.SyncPending || sr.Status == models.SyncRunning {
				ready = false
				break
			}
		}
	}
	o.mu.RUnlock()
	if ready {
		o.finish(ctx, runID)
	}
}

// personIDs — идентификаторы человека для сопоставления с полем Employee
// заявок Jira: e-mail, его локальная часть, имя и логин GitLab.
func personIDs(person models.Person) map[string]bool {
	ids := map[string]bool{}
	add := func(v string) {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			ids[v] = true
			if i := strings.Index(v, "@"); i > 0 {
				ids[v[:i]] = true
			}
		}
	}
	add(person.Email)
	add(person.GoogleEmail)
	add(person.DisplayName)
	add(person.GitLabUser)
	return ids
}

// persistHybridHistory сохраняет хронологии гибридных дней И формата работы
// (Office | Hybrid | Remote) из двух источников: одобренные заявки HCM
// «Change request» (дата вступления в силу) и журналы объекта Assets ОБОИХ
// инстансов HRDB. Смены гибридных дней и форматов исторически живут в старой
// Jira (DC Insight) — cloud-журнал начинается с миграции и прошлого не знает,
// поэтому читаются оба и склеиваются (дубли схлопывает merge). В ремоут-/
// офис-периоды гибридный шаблон не действует — правила и календарь гейтятся
// форматом. При ошибке любого источника сохранённые истории не трогаются.
func (o *Orchestrator) persistHybridHistory(ctx context.Context, person models.Person) {
	var hybridAssets, formatAssets []models.HybridChange
	var curFormat string
	for _, mode := range []string{"dc", "cloud"} {
		cl := o.hrdb.Client(mode)
		if cl == nil || person.Email == "" {
			continue
		}
		emp, found, err := cl.FindByEmail(ctx, person.Email)
		if err != nil {
			o.log.Warn("поиск сотрудника в HRDB для истории гибридных дней не удался",
				"person", person.Key, "hrdb", mode, "err", err)
			return
		}
		if !found || emp.Key == "" {
			continue
		}
		if curFormat == "" {
			curFormat = emp.WorkFormat
		}
		h, err := cl.HybridHistory(ctx, emp.Key)
		if err != nil {
			o.log.Warn("журнал гибридных дней Assets недоступен", "person", person.Key, "hrdb", mode, "err", err)
			return
		}
		f, err := cl.WorkFormatHistory(ctx, emp.Key)
		if err != nil {
			o.log.Warn("журнал формата работы Assets недоступен", "person", person.Key, "hrdb", mode, "err", err)
			return
		}
		hybridAssets = append(hybridAssets, h...)
		formatAssets = append(formatAssets, f...)
	}

	var hybridTickets, formatTickets []models.HybridChange
	if o.vac != nil {
		all, err := o.vac.HybridTickets(ctx)
		if err != nil {
			o.log.Warn("заявки HCM на смену гибридных дней недоступны", "person", person.Key, "err", err)
			return
		}
		ids := personIDs(person)
		for _, t := range all {
			if !ids[strings.ToLower(strings.TrimSpace(t.Employee))] {
				continue
			}
			if t.Days != "" {
				hybridTickets = append(hybridTickets, models.HybridChange{At: t.Effective, New: t.Days})
			}
			if t.WorkFormat != "" {
				formatTickets = append(formatTickets, models.HybridChange{At: t.Effective, New: t.WorkFormat})
			}
		}
	}

	hybrid := models.MergeHybridTimeline(hybridAssets, hybridTickets)
	format := models.MergeWorkFormatTimeline(formatAssets, formatTickets)
	// Постоянный ремоут/офис без единого изменения: текущее значение из HRDB
	// синтетической точкой в эпохе — действует на любую дату.
	if len(format) == 0 && models.NormalizeWorkFormat(curFormat) != "" {
		format = []models.HybridChange{{At: time.Unix(0, 0).UTC(), Old: curFormat, New: curFormat}}
	}
	if err := o.store.ReplaceHybridHistory(ctx, person.Key, hybrid); err != nil {
		o.log.Error("не удалось сохранить историю гибридных дней", "person", person.Key, "err", err)
		return
	}
	if err := o.store.ReplaceWorkFormatHistory(ctx, person.Key, format); err != nil {
		o.log.Error("не удалось сохранить историю формата работы", "person", person.Key, "err", err)
		return
	}
	if len(hybrid) > 0 || len(format) > 0 {
		o.log.Info("истории гибридных дней и формата работы сохранены",
			"person", person.Key, "hybrid", len(hybrid), "format", len(format))
	}
}

// persistVACDays берёт заявки VAC этого человека и пишет особые дни
// (vacation/sick/remote) источником vacjira. Овертаймы сюда не идут — они
// используются правилами отклонений отдельно.
func (o *Orchestrator) persistVACDays(ctx context.Context, person models.Person, from, to time.Time) {
	if o.vac == nil {
		return
	}
	entries, err := o.vac.Absences(ctx, from, to)
	if err != nil {
		o.log.Warn("не удалось получить заявки VAC", "person", person.Key, "err", err)
		return
	}
	// Absences исключает овертаймы (это не «вид дня»), но их тоже храним в
	// person_days отдельным маркером — добираем через Fetch.
	if ot, err := o.vac.Fetch(ctx, from, to); err == nil {
		entries = append(entries, ot...)
	} else {
		o.log.Warn("не удалось получить овертаймы VAC", "person", person.Key, "err", err)
	}
	// Совпадение заявки с человеком по e-mail/логину/имени (как в отклонениях).
	ids := personIDs(person)

	seen := map[string]bool{}
	var days []models.PersonDay
	for _, e := range entries {
		if !ids[strings.ToLower(strings.TrimSpace(e.Employee))] {
			continue
		}
		// Овертайм — не «вид дня», а отдельный маркер; но храним его в тех же
		// person_days (kind=overtime), чтобы матрица не ходила в Jira на рендере.
		kind := overtime.KindToDayKind(e.Kind)
		if kind == "" {
			if e.Kind == overtime.KindOvertime {
				kind = models.DayOvertime
			} else {
				continue
			}
		}
		label := strings.SplitN(e.Summary, " request", 2)[0]
		for _, ds := range e.Days(from, to) {
			key := ds + "|" + kind
			if seen[key] {
				continue
			}
			seen[key] = true
			day, _ := time.Parse("2006-01-02", ds)
			days = append(days, models.PersonDay{
				PersonKey: person.Key, Day: day, Kind: kind, Label: label,
			})
		}
	}
	if err := o.store.ReplacePersonDaysForSource(ctx, person.Key, "vacjira", from, to, days); err != nil {
		o.log.Error("не удалось сохранить дни VAC", "person", person.Key, "err", err)
		return
	}
	if len(days) > 0 {
		o.log.Info("дни отсутствия из VAC сохранены", "person", person.Key, "days", len(days))
	}
}

// runGDocs — отдельная ветка: коллектору нужно передать список документов из
// Jira и забрать обогащение обратно в doc_links.
func (o *Orchestrator) runGDocs(ctx context.Context, runID string, req collectors.Request, links []models.DocLink) {
	started := time.Now()

	docIDs, issueByDoc := docMaps(links)
	// Документы, найденные в предыдущих прогонах, тоже обходим — задача могла
	// быть обновлена раньше выбранного периода, а правки в доке идти сейчас.
	if stored, err := o.store.PendingDocLinks(ctx, req.Person.Key); err == nil {
		seen := make(map[string]bool, len(docIDs))
		for _, id := range docIDs {
			seen[id] = true
		}
		for _, id := range stored {
			if !seen[id] {
				docIDs = append(docIDs, id)
			}
		}
	}

	c, err := gdocs.New(ctx, o.cfg.Google, o.log)
	if err != nil {
		o.setSource(runID, models.SourceRun{Source: "gdocs", Status: models.SyncFailed,
			Error: err.Error(), DurationMS: time.Since(started).Milliseconds()})
		return
	}
	c.SetDocIDs(docIDs)
	c.SetIssueByDoc(issueByDoc)
	c.SetProgress(o.progressFn(runID, "gdocs"))

	origTo := req.To
	req = o.coverageWindow(ctx, runID, "gdocs", req)

	res, err := c.Collect(ctx, req)
	sr := models.SourceRun{Source: "gdocs", DurationMS: time.Since(started).Milliseconds(), Note: res.Note}
	if err != nil {
		sr.Status = models.SyncFailed
		sr.Error = err.Error()
		o.setSource(runID, sr)
		return
	}

	if err := o.persist(ctx, req, "gdocs", res.Events); err != nil {
		sr.Status = models.SyncFailed
		sr.Error = err.Error()
		o.setSource(runID, sr)
		return
	}
	for _, e := range c.Enrichments() {
		if err := o.store.EnrichDocLink(ctx, req.Person.Key, e.DocID, e.Title, e.LastModified, e.Edits, e.Comments); err != nil {
			o.log.Warn("не удалось обогатить doc_link", "doc_id", e.DocID, "err", err)
		}
	}
	o.advanceCoverage(ctx, "gdocs", req.Person.Key, origTo)
	sr.Status = models.SyncDone
	sr.Events = len(res.Events)
	o.setSource(runID, sr)
}

// runOne выполняет один коллектор целиком: создание, сбор, запись.
// SettingReprobeDays — настройка «дней перекрытия» на каждую систему в виде
// JSON {"<source>": <дни>}; системы без записи берут дефолт cfg.Sync.ReprobeDays.
const SettingReprobeDays = "reprobe_days"

// isOverwrite — идёт ли прогон в режиме принудительной перезаписи интервала.
func (o *Orchestrator) isOverwrite(runID string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.overwrite[runID]
}

// reprobeFor — сколько времени с хвоста уже собранного перепроверять для
// источника (страховка от поздних событий).
func (o *Orchestrator) reprobeFor(ctx context.Context, source string) time.Duration {
	days := o.cfg.Sync.ReprobeDays
	if raw, _ := o.store.GetSetting(ctx, SettingReprobeDays); raw != "" {
		var m map[string]int
		if json.Unmarshal([]byte(raw), &m) == nil {
			if d, ok := m[source]; ok {
				days = d
			}
		}
	}
	if days < 0 {
		days = 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// coverageWindow сужает окно сбора по инкрементальному покрытию источника: уже
// собранную «голову» интервала не перезапрашиваем, перепроверяем лишь хвост в
// reprobe. Режим перезаписи и отсутствие покрытия дают полное окно [from,to].
func (o *Orchestrator) coverageWindow(ctx context.Context, runID, source string, req collectors.Request) collectors.Request {
	if o.isOverwrite(runID) {
		return req
	}
	cov, err := o.store.GetCoverage(ctx, req.Person.Key, source)
	if err != nil {
		return req
	}
	req.From = incrementalFrom(req.From, req.To, cov, o.reprobeFor(ctx, source))
	return req
}

// incrementalFrom вычисляет начало окна с учётом покрытия: уже собранную
// «голову» до covered пропускаем, перепроверяя лишь хвост reprobe. Сужаем,
// только если кандидат-начало попадает внутрь окна; иначе (покрытия нет либо
// оно ушло за окно — например, запрошен исторический интервал) — полное окно.
func incrementalFrom(from, to, covered time.Time, reprobe time.Duration) time.Time {
	if covered.IsZero() || !covered.After(from) {
		return from
	}
	if cand := covered.Add(-reprobe); cand.After(from) && cand.Before(to) {
		return cand
	}
	return from
}

// advanceCoverage двигает границу покрытия к концу окна после успешного сбора.
func (o *Orchestrator) advanceCoverage(ctx context.Context, source, personKey string, to time.Time) {
	if err := o.store.AdvanceCoverage(ctx, personKey, source, to); err != nil {
		o.log.Warn("не удалось обновить покрытие источника", "source", source, "person", personKey, "err", err)
	}
}

func (o *Orchestrator) runOne(ctx context.Context, runID, source string, req collectors.Request,
	build func() (collectors.Collector, error)) collectors.Result {

	started := time.Now()
	sr := models.SourceRun{Source: source, Status: models.SyncRunning}
	o.setSource(runID, sr)

	c, err := build()
	if err != nil {
		sr.Status = models.SyncFailed
		sr.Error = err.Error()
		sr.DurationMS = time.Since(started).Milliseconds()
		o.setSource(runID, sr)
		return collectors.Result{}
	}
	if pa, ok := c.(collectors.ProgressAware); ok {
		pa.SetProgress(o.progressFn(runID, source))
	}

	// Инкрементальное окно: уже собранную «голову» интервала пропускаем (кроме
	// режима перезаписи). persist работает по тому же суженному окну, чтобы не
	// удалить старые события за пределами перезабора.
	origTo := req.To
	req = o.coverageWindow(ctx, runID, source, req)

	res, err := c.Collect(ctx, req)
	sr.DurationMS = time.Since(started).Milliseconds()
	sr.Note = res.Note
	// Аккаунт, найденный по email во время сбора, сохраняем в карточку —
	// следующие прогоны обойдутся без повторного поиска.
	if res.ResolvedAccount != "" {
		if err := o.store.SetPersonSourceID(ctx, req.Person.Key, source, res.ResolvedAccount); err != nil {
			o.log.Warn("не удалось сохранить найденный аккаунт", "source", source, "err", err)
		} else {
			o.log.Info("аккаунт найден по email и сохранён в карточку",
				"person", req.Person.Key, "source", source, "id", res.ResolvedAccount)
		}
	}
	if err != nil {
		sr.Status = models.SyncFailed
		sr.Error = err.Error()
		o.setSource(runID, sr)
		o.log.Error("сбор источника завершился ошибкой", "source", source, "err", err)
		// Частичный результат всё равно сохраняем — лучше неполные данные, чем никакие.
		if len(res.Events) > 0 {
			if perr := o.persist(ctx, req, source, res.Events); perr == nil {
				sr.Status = models.SyncPartial
				sr.Events = len(res.Events)
				o.setSource(runID, sr)
			}
		}
		return res
	}

	if err := o.persist(ctx, req, source, res.Events); err != nil {
		sr.Status = models.SyncFailed
		sr.Error = err.Error()
		o.setSource(runID, sr)
		return res
	}
	o.persistDays(ctx, req, source, res.Days)
	o.scoreSlack(ctx, source, res.Events)
	// Успех — двигаем покрытие к концу запрошенного окна.
	o.advanceCoverage(ctx, source, req.Person.Key, origTo)
	sr.Status = models.SyncDone
	sr.Events = len(res.Events)
	o.setSource(runID, sr)
	return res
}

// scoreSlack проставляет свежесобранным сообщениям Slack AI-оценку
// содержательности. Недоступность скорера сбор не роняет — оценить можно
// позже кнопкой «Переоценить» на странице AI.
// scoreSlack оценивает неоценённые сообщения АРХИВА (тексты событий больше
// не хранятся) и раскатывает оценки на все события с тем же external_id.
func (o *Orchestrator) scoreSlack(ctx context.Context, source string, _ []models.Event) {
	if source != "slack" || o.ai == nil {
		return
	}
	total := 0
	for i := 0; i < 50; i++ { // предохранитель: max 100k за вызов
		batch, err := o.store.UnscoredSlackTexts(ctx, 2000)
		if err != nil {
			o.log.Warn("AI-оценка: выборка архива не удалась", "err", err)
			return
		}
		if len(batch) == 0 {
			break
		}
		texts := make([]string, len(batch))
		for j, m := range batch {
			texts[j] = m.Text
		}
		scores, err := o.ai.Score(ctx, texts)
		if err != nil {
			o.log.Warn("AI-оценка сообщений не удалась", "err", err)
			return
		}
		byID := make(map[string]float64, len(batch))
		for j, m := range batch {
			byID[m.ID] = scores[j]
		}
		if err := o.store.SetSlackScores(ctx, byID); err != nil {
			o.log.Warn("не удалось сохранить AI-оценки архива", "err", err)
			return
		}
		if err := o.store.UpdateEventScoresByExternalID(ctx, byID); err != nil {
			o.log.Warn("не удалось раскатать AI-оценки на события", "err", err)
		}
		total += len(batch)
	}
	if total > 0 {
		o.log.Info("AI-оценка архива slack", "scored", total)
	}
}

// slackSink пишет сырые сообщения канал-центричного архива (текст шифруется
// в хранилище).
func (o *Orchestrator) slackSink(ctx context.Context, batch []slack.RawMessage) error {
	msgs := make([]storage.SlackMessage, len(batch))
	for i, m := range batch {
		msgs[i] = storage.SlackMessage{
			ID: m.ID, ChannelID: m.ChannelID, ChannelName: m.ChannelName, UID: m.UID,
			Kind: m.Kind, TS: m.TS, SlackTS: m.SlackTS, ThreadTS: m.ThreadTS,
			Text: m.Text, URL: m.URL, ReplyCount: m.ReplyCount,
			Reactions: m.Reactions, Reaction: m.Reaction,
		}
	}
	return o.store.UpsertSlackMessages(ctx, msgs)
}

// persistDays перезаписывает особые дни (праздники, отпуска) за период сбора.
// Пишем и пустой список: отменённый отпуск должен исчезнуть из матрицы.
func (o *Orchestrator) persistDays(ctx context.Context, req collectors.Request, source string, days []models.PersonDay) {
	if source != "gcal" {
		return
	}
	if err := o.store.ReplacePersonDays(ctx, req.Person.Key, req.From, req.To, days); err != nil {
		o.log.Error("не удалось сохранить особые дни", "person", req.Person.Key, "err", err)
	}
}

// ensureOffice подтягивает офис, команду и направление из HRDB, если они ещё
// не известны: офис определяет праздничный календарь, команда и направление —
// фильтры страницы сравнения.
func (o *Orchestrator) ensureOffice(ctx context.Context, person *models.Person) {
	if (person.Office != "" && person.Team != "" && person.Area != "" && person.HireDate != nil && person.HybridDays != "") ||
		o.hrdb.Current() == nil || person.Email == "" {
		return
	}
	emp, found, err := o.hrdb.Current().FindByEmail(ctx, person.Email)
	if err != nil {
		o.log.Warn("поиск сотрудника в HRDB не удался", "person", person.Key, "err", err)
		return
	}
	if !found {
		return
	}
	if person.Office == "" {
		person.Office = emp.Office
	}
	if person.Team == "" {
		person.Team = emp.Team
	}
	if person.Area == "" {
		person.Area = emp.Area
	}
	if person.HireDate == nil {
		person.HireDate = emp.HireTime()
	}
	if person.HybridDays == "" {
		person.HybridDays = emp.HybridDays
	}
	if err := o.store.SetPersonHRDB(ctx, person.Key, emp.Office, emp.Team, emp.Area, emp.HybridDays, emp.HireTime()); err != nil {
		o.log.Warn("не удалось сохранить поля HRDB", "person", person.Key, "err", err)
	}
	o.log.Info("данные из HRDB подтянуты", "person", person.Key,
		"office", person.Office, "team", person.Team, "area", person.Area, "hire", person.HireDate)
}

// persist перезаписывает окно данных по источнику: сперва удаляем прежние
// события периода, затем пишем свежие — так исчезнувшее в источнике не залипает.
// События раньше даты найма отбрасываются: это фантомы, а не активность
// (Google приписывает добавленного участника к прошлым инстансам регулярной
// встречи, из-за чего у новичка появлялась «активность» до выхода).
//
// Запись идёт с независимым контекстом: persist вызывается только после
// ПОЛНОСТЬЮ успешного сбора, и истёкший к этому моменту дедлайн прогона не
// должен выбрасывать собранное (годовой прогон упирался ровно в это).
func (o *Orchestrator) persist(ctx context.Context, req collectors.Request, source string, events []models.Event) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	// Инварианты записи, общие для всех коллекторов:
	//  1) событие принадлежит собираемому человеку — чужой PersonKey это баг
	//     коллектора: такое событие ушло бы другому и никогда не вычистилось
	//     бы его пересбором (DeleteRange чистит только req.Person.Key);
	//  2) событие лежит в окне сбора — иначе оно вне зоны DeleteRange и оседает
	//     в БД навсегда, не перезаписываясь ресинками;
	//  3) событие не раньше даты найма (фантомные приглашения задним числом).
	kept := events[:0]
	var wrongPerson, outOfWindow, preHire int
	for _, e := range events {
		switch {
		case e.PersonKey != req.Person.Key:
			wrongPerson++
		case e.OccurredAt.Before(req.From) || !e.OccurredAt.Before(req.To):
			outOfWindow++
		case req.Person.HireDate != nil && e.OccurredAt.Before(*req.Person.HireDate):
			preHire++
		default:
			kept = append(kept, e)
		}
	}
	if wrongPerson > 0 {
		o.log.Warn("коллектор отдал события с чужим person_key — отброшены",
			"person", req.Person.Key, "source", source, "dropped", wrongPerson)
	}
	if outOfWindow > 0 {
		o.log.Warn("события вне окна сбора отброшены",
			"person", req.Person.Key, "source", source, "dropped", outOfWindow,
			"from", req.From.Format(time.RFC3339), "to", req.To.Format(time.RFC3339))
	}
	if preHire > 0 {
		o.log.Info("события до даты найма отброшены",
			"person", req.Person.Key, "source", source, "dropped", preHire,
			"hire", req.Person.HireDate.Format("2006-01-02"))
	}
	events = kept
	if err := o.store.DeleteRange(ctx, req.Person.Key, source, req.From, req.To); err != nil {
		return fmt.Errorf("очистка периода %s: %w", source, err)
	}
	if _, err := o.store.SaveEvents(ctx, events); err != nil {
		return fmt.Errorf("запись событий %s: %w", source, err)
	}
	return nil
}

func (o *Orchestrator) progressFn(runID, source string) collectors.Progress {
	return func(stage string, done, total int) {
		note := stage
		if total > 0 {
			note = fmt.Sprintf("%s %d/%d", stage, done, total)
		} else if done > 0 {
			note = fmt.Sprintf("%s: %d", stage, done)
		}
		o.update(runID, func(r *models.SyncRun) {
			sr := r.Sources[source]
			sr.Source = source
			sr.Status = models.SyncRunning
			sr.Note = note
			r.Sources[source] = sr
		})
	}
}

func (o *Orchestrator) setSource(runID string, sr models.SourceRun) {
	o.update(runID, func(r *models.SyncRun) { r.Sources[sr.Source] = sr })
}

func (o *Orchestrator) update(runID string, fn func(*models.SyncRun)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if r, ok := o.runs[runID]; ok {
		fn(r)
	}
}

func (o *Orchestrator) finish(ctx context.Context, runID string) {
	o.mu.Lock()
	run, ok := o.runs[runID]
	if !ok || run.FinishedAt != nil {
		// Уже финализирован (гонка двух tryFinish из параллельных фаз).
		o.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	run.FinishedAt = &now

	var failed, done int
	var errs []string
	for _, sr := range run.Sources {
		switch sr.Status {
		case models.SyncFailed:
			failed++
			errs = append(errs, sr.Source+": "+sr.Error)
		case models.SyncDone, models.SyncPartial:
			done++
		}
	}
	switch {
	case failed == 0:
		run.Status = models.SyncDone
	case done == 0:
		run.Status = models.SyncFailed
	default:
		run.Status = models.SyncPartial
	}
	run.Error = strings.Join(errs, "; ")
	// Остановленный вручную прогон — не «ошибка сбора», а осознанное действие:
	// причина важнее ворохов context canceled из коллекторов.
	if o.cancelled[runID] {
		delete(o.cancelled, runID)
		if run.Status != models.SyncDone {
			run.Status = models.SyncFailed
			run.Error = "остановлен вручную"
		}
	}
	delete(o.inflight, run.PersonKey)
	snapshot := *run
	o.mu.Unlock()

	// Финальный статус пишем со своим контекстом: ctx прогона при остановке
	// вручную уже отменён, и сохранение с ним молча не проходило — запись
	// зависала в БД как pending навсегда.
	saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.store.SaveSyncRun(saveCtx, snapshot); err != nil {
		o.log.Error("не удалось сохранить прогон", "run_id", runID, "err", err)
	}
	o.log.Info("сбор завершён", "run_id", runID, "status", snapshot.Status,
		"events", totalEvents(snapshot), "duration", now.Sub(snapshot.StartedAt).String())

	o.mu.RLock()
	onFinish := o.onFinish
	o.mu.RUnlock()
	if onFinish != nil {
		onFinish()
	}
}

func totalEvents(r models.SyncRun) int {
	n := 0
	for _, sr := range r.Sources {
		n += sr.Events
	}
	return n
}

func docMaps(links []models.DocLink) ([]string, map[string]string) {
	ids := make([]string, 0, len(links))
	byDoc := make(map[string]string, len(links))
	seen := make(map[string]bool, len(links))
	for _, l := range links {
		if !seen[l.DocID] {
			seen[l.DocID] = true
			ids = append(ids, l.DocID)
		}
		if _, ok := byDoc[l.DocID]; !ok && l.IssueKey != "" {
			byDoc[l.DocID] = l.IssueKey
		}
	}
	return ids, byDoc
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
