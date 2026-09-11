package gdocs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/driveactivity/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

const (
	// maxActivityPages — предохранитель от бесконечной пагинации Drive Activity.
	maxActivityPages = 100
	// docConcurrency — максимум одновременных обходов документов.
	docConcurrency = 4
	// defaultPageSize используется, если размер страницы не задан в конфиге.
	defaultPageSize = 100
	// maxPageSize — потолок, который принимают оба API.
	maxPageSize = 1000
	// defaultMaxDocs — сколько документов максимум обходить при расширенном
	// поиске: по каждому идёт отдельный запрос активности.
	defaultMaxDocs = 500

	// docMimeType — mime-тип Google-документа.
	docMimeType = "application/vnd.google-apps.document"
	// docURLTemplate — запасная ссылка, если Drive не отдал webViewLink.
	docURLTemplate = "https://docs.google.com/document/d/%s/edit"

	// fileFields — поля, которых хватает и на событие, и на обогащение doc_links.
	fileFields = "id,name,mimeType,webViewLink,modifiedTime,createdTime,owners,lastModifyingUser,parents"
	// listFields — поля постраничного листинга Drive.
	listFields = "nextPageToken, files(id,name,webViewLink,modifiedTime,lastModifyingUser)"
)

// DocEnrichment — агрегат по одному документу, которым оркестратор дополняет
// запись в doc_links (см. models.DocLink).
type DocEnrichment struct {
	DocID        string
	Title        string
	LastModified *time.Time
	Edits        int
	Comments     int
}

// Collector собирает активность в Google Docs/Drive.
//
// Ограничение Drive Activity API, важное для понимания результата:
// в ленте активности актор приходит как actor.User.KnownUser.PersonName в
// формате "people/<id>" — email там нет и получить его напрямую нельзя
// (People API в разрешённых scope недоступен). Поэтому автор события
// определяется так:
//
//  1. по карте people-id → email. Тот же id лежит в permissionId пользователя
//     в Drive, поэтому карта наполняется из прав доступа документа
//     (permissions.list), его ревизий (revisions.list), авторов комментариев и
//     метаданных файла (owners, lastModifyingUser);
//  2. по флагу KnownUser.IsCurrentUser — но только если учётка, которой мы
//     ходим в API, и есть интересующий нас человек. Флаг означает «это владелец
//     токена», а не «это тот, чью активность мы собираем»: без такой проверки на
//     дашборд одного человека уезжали бы действия другого;
//  3. если опознать автора не удалось — активность отбрасывается. Число
//     отброшенных попадает в Result.Note, чтобы неполнота была видна.
//
// Комментарии собираются не из ленты активности, а через Drive Comments API:
// там у автора есть email и permissionId, поэтому чужие комментарии отсеиваются
// точно. Из ленты берутся правки, создание документа и предложения правок.
//
// Для режима service_account обязателен domain-wide delegation
// (config.GoogleConfig.ImpersonateSubject), иначе активности сотрудника не
// будет видно вовсе — см. комментарий к пакету.
type Collector struct {
	cfg config.GoogleConfig
	log *slog.Logger

	activity *driveactivity.Service
	drive    *drive.Service

	// sharedMode=true — ходим в API от учётки, у которой нет своего Drive с
	// документами команды: сервис-аккаунт без domain-wide delegation. В этом
	// режиме «мой Drive» пуст, обход items/root бессмыслен, и единственный
	// доступный корпус — документы, расшаренные на эту учётку, плюс те, чьи
	// ссылки пришли из задач Jira.
	sharedMode bool
	// identity — email учётки, от имени которой идут запросы. Нужен в первую
	// очередь для подсказки «расшарьте документы на этот адрес».
	identity string

	mu         sync.Mutex
	docIDs     []string
	issueByDoc map[string]string
	progress   collectors.Progress

	// people — кэш "people/<id>" → что мы знаем об этом человеке.
	peopleMu sync.RWMutex
	people   map[string]personRef

	// person — кто именно нас интересует в текущем прогоне Collect.
	person personIdentity

	// enrich — агрегаты по документам за последний прогон Collect.
	enrichMu sync.Mutex
	enrich   map[string]*DocEnrichment
}

// New создаёт коллектор: поднимает HTTP-клиент с авторизацией Google и на его
// основе клиентов Drive Activity и Drive.
func New(ctx context.Context, cfg config.GoogleConfig, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}

	httpClient, err := NewHTTPClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	activitySvc, err := driveactivity.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("gdocs: не удалось создать клиент Drive Activity API: %w", err)
	}
	driveSvc, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("gdocs: не удалось создать клиент Drive API: %w", err)
	}

	c := &Collector{
		cfg:      cfg,
		log:      log.With("source", string(models.SourceGDocs)),
		activity: activitySvc,
		drive:    driveSvc,
		// Без impersonation сервис-аккаунт видит только расшаренное на него.
		sharedMode: cfg.Mode == config.GoogleAuthServiceAccount && strings.TrimSpace(cfg.ImpersonateSubject) == "",
		issueByDoc: map[string]string{},
		people:     map[string]personRef{},
		enrich:     map[string]*DocEnrichment{},
	}
	c.identity = c.resolveIdentity(ctx)
	if c.sharedMode {
		c.log.Info("gdocs: работаем в режиме расшаренных документов",
			"identity", c.identity,
			"hint", "domain-wide delegation не настроен: видны только документы, доступные этой учётке")
	}
	return c, nil
}

// resolveIdentity узнаёт, от чьего имени мы ходим в Drive. Ошибку не считаем
// фатальной — это подсказка для пользователя, а не условие работы.
func (c *Collector) resolveIdentity(ctx context.Context) string {
	about, err := c.drive.About.Get().Fields("user(emailAddress,displayName)").Context(ctx).Do()
	if err != nil || about == nil || about.User == nil {
		if err != nil {
			c.log.Debug("gdocs: не удалось определить учётку Drive", "err", err)
		}
		return ""
	}
	return about.User.EmailAddress
}

// Identity возвращает email учётки, от имени которой идут запросы к Google
// (для сервис-аккаунта — его собственный адрес). Пусто, если определить не вышло.
func (c *Collector) Identity() string { return c.identity }

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceGDocs }

// SetDocIDs задаёт список id документов, найденных по ссылкам в задачах Jira
// (TSD). Оркестратор вызывает его до Collect.
func (c *Collector) SetDocIDs(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.docIDs = append([]string(nil), ids...)
}

// SetIssueByDoc задаёт соответствие «id документа → ключ задачи Jira».
// Ключ задачи попадает в Event.ParentRefID и в meta["issue_key"].
func (c *Collector) SetIssueByDoc(m map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.issueByDoc = make(map[string]string, len(m))
	for docID, issueKey := range m {
		c.issueByDoc[docID] = issueKey
	}
}

// SetProgress реализует collectors.ProgressAware.
func (c *Collector) SetProgress(p collectors.Progress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.progress = p
}

// Enrichments возвращает агрегаты по документам, посчитанные во время Collect:
// оркестратор переносит их в doc_links.
func (c *Collector) Enrichments() []DocEnrichment {
	c.enrichMu.Lock()
	defer c.enrichMu.Unlock()

	out := make([]DocEnrichment, 0, len(c.enrich))
	for _, e := range c.enrich {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocID < out[j].DocID })
	return out
}

// Collect выгружает активность за период [req.From, req.To).
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	c.mu.Lock()
	docIDs := append([]string(nil), c.docIDs...)
	issueByDoc := make(map[string]string, len(c.issueByDoc))
	for k, v := range c.issueByDoc {
		issueByDoc[k] = v
	}
	progress := c.progress
	c.mu.Unlock()

	c.enrichMu.Lock()
	c.enrich = map[string]*DocEnrichment{}
	c.enrichMu.Unlock()

	// Признаки, по которым узнаём нужного человека: адреса и отображаемое имя.
	// Имя обязательно — Drive Comments API часто отдаёт автора без адреса.
	c.person = newPersonIdentity(req.Person)
	if c.person.empty() {
		return collectors.Result{}, fmt.Errorf(
			"gdocs: у пользователя %q не заданы ни google_email, ни email, ни display_name — "+
				"сопоставить автора правок не с чем; заполните их в настройках или в ACTIVITY_PEOPLE",
			req.Person.Key)
	}

	filter := timeFilter(req.From, req.To)
	stats := newRunStats()

	var (
		events []models.Event
		errs   []error
	)

	seen := make(map[string]bool, len(docIDs))
	for _, id := range docIDs {
		seen[id] = true
	}

	// --- Режим A: конкретные документы, найденные по ссылкам в Jira. ---
	if len(docIDs) > 0 {
		docEvents, docErrs := c.collectDocs(ctx, req, docIDs, issueByDoc, filter, stats, progress, "gdocs: документы")
		events = append(events, docEvents...)
		errs = append(errs, docErrs...)
	}

	// --- Режим B: расширенный поиск. ---
	//
	// Обход по дереву (AncestorName) применяется только к явно заданным папкам.
	// Для «всего, что доступно учётке» он не годится: items/root — это лишь
	// «Мой диск», а документы из «Доступных мне» и с общих дисков лежат вне
	// этого поддерева и в такой обход не попадают. Поэтому по умолчанию мы
	// сначала находим документы листингом Drive (он видит и своё, и
	// расшаренное, и общие диски), а потом обходим найденное поштучно.
	if c.cfg.ScanDrive {
		useAncestors := len(c.cfg.DriveFolderIDs) > 0 && !c.sharedMode
		if useAncestors {
			scanEvents, err := c.scanDrive(ctx, req, issueByDoc, filter, stats, progress)
			if err != nil {
				errs = append(errs, err)
			}
			events = append(events, scanEvents...)
		} else {
			extra, err := c.discoverDocs(ctx, req, seen, stats, progress)
			if err != nil {
				errs = append(errs, err)
			}
			if len(extra) > 0 {
				discovered, discErrs := c.collectDocs(ctx, req, extra, issueByDoc, filter, stats, progress, "gdocs: доступные документы")
				events = append(events, discovered...)
				errs = append(errs, discErrs...)
			}
		}
	}

	stats.setMode(c.modeName(), c.identity)
	stats.setPerson(c.person.primaryEmail())

	sort.Slice(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].ExternalID < events[j].ExternalID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})

	res := collectors.Result{Events: events, Note: stats.note()}

	// Полный отказ отдаём ошибкой, частичный — только в лог и Note.
	if len(errs) > 0 && len(events) == 0 {
		return res, errors.Join(errs...)
	}
	if len(errs) > 0 {
		c.log.Warn("gdocs: часть документов не собрана", "errors", len(errs), "first", errs[0])
	}
	return res, nil
}

// modeName — человекочитаемое имя режима работы, уезжает в Result.Note.
func (c *Collector) modeName() string {
	if c.cfg.Mode == config.GoogleAuthOAuth {
		return "oauth"
	}
	if c.sharedMode {
		return "service_account (только расшаренные документы)"
	}
	return "service_account + impersonation"
}

// collectDocs обходит список документов с ограничением параллелизма.
// Возвращает события и накопленные ошибки: сбой по одному документу не должен
// отменять остальные.
func (c *Collector) collectDocs(
	ctx context.Context,
	req collectors.Request,
	docIDs []string,
	issueByDoc map[string]string,
	filter string,
	stats *runStats,
	progress collectors.Progress,
	stage string,
) ([]models.Event, []error) {
	var (
		mu     sync.Mutex
		events []models.Event
		errs   []error
		done   int
	)

	sem := make(chan struct{}, docConcurrency)
	var wg sync.WaitGroup

	for _, id := range docIDs {
		docID := id
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			errs = append(errs, ctx.Err())
			mu.Unlock()
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			docEvents, err := c.collectDoc(ctx, req, docID, issueByDoc[docID], filter, stats)

			mu.Lock()
			if err != nil {
				errs = append(errs, err)
			}
			events = append(events, docEvents...)
			done++
			cur := done
			mu.Unlock()

			if progress != nil {
				progress(stage, cur, len(docIDs))
			}
		}()
	}
	wg.Wait()
	return events, errs
}

// discoverDocs ищет Google-документы, доступные текущей учётке, среди
// изменённых в интересующем периоде.
//
// Листинг Drive — единственный способ увидеть весь доступный корпус разом:
// в отличие от обхода по дереву он покрывает и «Мой диск», и «Доступные мне»,
// и общие диски. Если заданы GOOGLE_DRIVE_FOLDER_IDS, поиск сужается до этих
// папок.
//
// Результат ограничен MaxDocs: у активного пользователя за квартал доступных
// документов может оказаться очень много, а дальше по каждому идёт отдельный
// запрос активности. Факт усечения не замалчивается — он попадает в Result.Note.
func (c *Collector) discoverDocs(
	ctx context.Context,
	req collectors.Request,
	skip map[string]bool,
	stats *runStats,
	progress collectors.Progress,
) ([]string, error) {
	base := fmt.Sprintf("mimeType = '%s' and trashed = false and modifiedTime > '%s'",
		docMimeType, req.From.UTC().Format(time.RFC3339))

	queries := make([]string, 0, len(c.cfg.DriveFolderIDs)+1)
	if len(c.cfg.DriveFolderIDs) > 0 {
		for _, folderID := range c.cfg.DriveFolderIDs {
			queries = append(queries, fmt.Sprintf("%s and '%s' in parents", base, folderID))
		}
	} else {
		queries = append(queries, base)
	}

	limit := c.maxDocs()
	var (
		out       []string
		errs      []error
		truncated bool
	)
	found := make(map[string]bool, 64)

	for qi, q := range queries {
		var token string
	pages:
		for page := 0; page < maxActivityPages; page++ {
			call := c.drive.Files.List().
				Q(q).
				Fields(listFields).
				PageSize(int64(c.pageSize())).
				OrderBy("modifiedTime desc").
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true)
			if token != "" {
				call = call.PageToken(token)
			}

			resp, err := call.Context(ctx).Do()
			if err != nil {
				if code := httpStatus(err); code == 403 || code == 404 {
					c.log.Info("gdocs: листинг доступных документов запрещён, пропускаю", "code", code)
					break
				}
				errs = append(errs, fmt.Errorf("gdocs: поиск доступных документов: %w", err))
				break
			}

			for _, file := range resp.Files {
				c.rememberUser(file.LastModifyingUser)
				if skip[file.Id] || found[file.Id] {
					continue
				}
				if len(out) >= limit {
					truncated = true
					break pages
				}
				found[file.Id] = true
				out = append(out, file.Id)
			}

			if resp.NextPageToken == "" {
				break
			}
			token = resp.NextPageToken
		}
		if progress != nil {
			progress("gdocs: поиск доступных документов", qi+1, len(queries))
		}
	}

	stats.setDiscovered(len(out), truncated, limit)
	if truncated {
		c.log.Warn("gdocs: список доступных документов усечён",
			"limit", limit, "hint", "поднимите GOOGLE_MAX_DOCS или задайте GOOGLE_DRIVE_FOLDER_IDS")
	}

	if len(errs) > 0 && len(out) == 0 {
		return nil, errors.Join(errs...)
	}
	for _, err := range errs {
		c.log.Warn("gdocs: ошибка при поиске доступных документов", "err", err)
	}
	return out, nil
}

// collectDoc собирает активность по одному документу (режим A).
func (c *Collector) collectDoc(
	ctx context.Context,
	req collectors.Request,
	docID, issueKey, filter string,
	stats *runStats,
) ([]models.Event, error) {
	file, err := c.drive.Files.Get(docID).
		Fields(fileFields).
		SupportsAllDrives(true).
		Context(ctx).
		Do()
	if err != nil {
		// 404/403 — документ удалён или недоступен нашей учётке: не повод
		// валить весь прогон.
		if code := httpStatus(err); code == 404 || code == 403 {
			c.log.Info("gdocs: документ недоступен, пропускаю", "doc_id", docID, "code", code)
			stats.incSkippedDoc()
			return nil, nil
		}
		return nil, fmt.Errorf("gdocs: не удалось получить документ %s: %w", docID, err)
	}

	// Метаданные файла — единственный источник соответствия people-id → email.
	c.rememberUser(file.LastModifyingUser)
	for _, owner := range file.Owners {
		c.rememberUser(owner)
	}

	docName := file.Name
	docURL := file.WebViewLink
	if docURL == "" {
		docURL = fmt.Sprintf(docURLTemplate, docID)
	}
	parent := ""
	if len(file.Parents) > 0 {
		parent = file.Parents[0]
	}
	if modified := parseTime(file.ModifiedTime); !modified.IsZero() {
		c.touchEnrichment(docID, docName, modified)
	} else {
		c.touchEnrichment(docID, docName, time.Time{})
	}

	info := docInfo{
		ID:       docID,
		Name:     docName,
		MimeType: file.MimeType,
		URL:      docURL,
		Parent:   parent,
		IssueKey: issueKey,
	}

	// Сначала выясняем, кто есть кто в этом документе: без карты
	// people-id → email автора активности сопоставить не с чем.
	c.resolveDocPeople(ctx, docID)

	// Комментарии берём отдельным запросом: там у автора есть email, и чужие
	// комментарии отсеиваются точно.
	commentEvents, commentsOK := c.collectComments(ctx, req, info, stats)

	acts, err := c.queryActivities(ctx, driveactivity.QueryDriveActivityRequest{
		ItemName: "items/" + docID,
		Filter:   filter,
	})
	if err != nil {
		if code := httpStatus(err); code == 404 || code == 403 {
			// Читателю документа Drive Activity может быть закрыт целиком.
			// Тогда лента правок недоступна, но метаданные файла у нас уже есть —
			// из них получается пусть грубое, но не пустое событие.
			c.log.Info("gdocs: лента активности документа недоступна, беру метаданные файла",
				"doc_id", docID, "code", code)
			stats.incActivityDenied()
			return append(commentEvents, c.fallbackEvents(req, file, info, stats)...), nil
		}
		return commentEvents, fmt.Errorf("gdocs: активность документа %s: %w", docID, err)
	}

	events := make([]models.Event, 0, len(acts)+len(commentEvents))
	events = append(events, commentEvents...)

	for _, act := range acts {
		ev, ok := c.buildEvent(req, act, info, stats)
		if !ok {
			continue
		}
		// Комментарии уже взяты точным путём — не задваиваем.
		if commentsOK && ev.Type == models.TypeDocComment {
			continue
		}
		events = append(events, ev)
	}

	// Пустая выборка ещё не значит «правок не было»: могло не хватить доступа к
	// ленте либо не выйти опознать автора. Метаданные файла — последний шанс не
	// потерять правку, и только если последним документ менял наш человек.
	if len(events) == 0 {
		events = c.fallbackEvents(req, file, info, stats)
	}
	return events, nil
}

// fallbackEvents порождает событие правки из метаданных файла, когда лента
// активности недоступна или пуста. Событие создаётся, только если последним
// документ менял наш человек и время правки попадает в период — иначе мы бы
// приписали ему чужую работу.
func (c *Collector) fallbackEvents(
	req collectors.Request,
	file *drive.File,
	info docInfo,
	stats *runStats,
) []models.Event {
	if file.LastModifyingUser == nil {
		return nil
	}
	kind := c.matchUser(file.LastModifyingUser)
	if kind == matchNone {
		return nil
	}
	modified := parseTime(file.ModifiedTime)
	if modified.IsZero() || modified.Before(req.From) || !modified.Before(req.To) {
		return nil
	}

	ev := models.Event{
		PersonKey:   req.Person.Key,
		Source:      models.SourceGDocs,
		Type:        models.TypeDocEdit,
		ExternalID:  externalID(info.ID, modified, models.TypeDocEdit),
		OccurredAt:  modified,
		Title:       title(info.Name, models.TypeDocEdit),
		URL:         info.URL,
		RefID:       info.ID,
		ParentRefID: info.IssueKey,
		Meta: map[string]any{
			"doc_id":        info.ID,
			"doc_name":      info.Name,
			"mime_type":     info.MimeType,
			"issue_key":     info.IssueKey,
			"action_detail": "edit",
			"from":          "drive.files.get",
			// Это агрегат «последняя правка», а не отдельное действие из ленты:
			// в UI видно, что точность здесь ниже.
			"approximate": true,
		},
	}
	ev.Normalize()

	c.touchEnrichment(info.ID, info.Name, modified)
	c.bumpEnrichment(info.ID, models.TypeDocEdit)
	stats.incEvent()
	return []models.Event{ev}
}

// scanDrive обходит Drive целиком или заданные папки (режим B).
func (c *Collector) scanDrive(
	ctx context.Context,
	req collectors.Request,
	issueByDoc map[string]string,
	filter string,
	stats *runStats,
	progress collectors.Progress,
) ([]models.Event, error) {
	ancestors := c.cfg.DriveFolderIDs
	if len(ancestors) == 0 {
		ancestors = []string{"root"}
	}

	var (
		events []models.Event
		errs   []error
	)
	for i, folderID := range ancestors {
		acts, err := c.queryActivities(ctx, driveactivity.QueryDriveActivityRequest{
			AncestorName: "items/" + folderID,
			Filter:       filter,
		})
		if err != nil {
			if code := httpStatus(err); code == 404 || code == 403 {
				c.log.Info("gdocs: папка недоступна, пропускаю", "folder_id", folderID, "code", code)
				stats.incSkippedDoc()
				continue
			}
			errs = append(errs, fmt.Errorf("gdocs: активность папки %s: %w", folderID, err))
			continue
		}

		for _, act := range acts {
			info, ok := targetDoc(act)
			if !ok {
				stats.incSkippedAction("not_a_document")
				continue
			}
			info.IssueKey = issueByDoc[info.ID]
			ev, ok := c.buildEvent(req, act, info, stats)
			if !ok {
				continue
			}
			events = append(events, ev)
		}

		if progress != nil {
			progress("gdocs: папки Drive", i+1, len(ancestors))
		}
	}

	// Добор через Drive: файлы, изменённые в периоде именно нашим человеком.
	listEvents, err := c.listRecentFiles(ctx, req, issueByDoc, stats)
	if err != nil {
		errs = append(errs, err)
	}
	events = append(events, listEvents...)

	if len(errs) > 0 && len(events) == 0 {
		return nil, errors.Join(errs...)
	}
	for _, err := range errs {
		c.log.Warn("gdocs: ошибка при скане Drive", "err", err)
	}
	return events, nil
}

// listRecentFiles добирает правки через drive.Files.List: событие порождается
// только если последним документ менял наш человек.
func (c *Collector) listRecentFiles(
	ctx context.Context,
	req collectors.Request,
	issueByDoc map[string]string,
	stats *runStats,
) ([]models.Event, error) {
	if c.person.empty() {
		// Человека не по чему опознать — сопоставлять lastModifyingUser не с чем.
		return nil, nil
	}

	query := fmt.Sprintf("modifiedTime > '%s' and mimeType='%s'",
		req.From.UTC().Format(time.RFC3339), docMimeType)

	var (
		events []models.Event
		token  string
	)
	for page := 0; page < maxActivityPages; page++ {
		call := c.drive.Files.List().
			Q(query).
			Fields(listFields).
			PageSize(int64(c.pageSize())).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true)
		if token != "" {
			call = call.PageToken(token)
		}

		resp, err := call.Context(ctx).Do()
		if err != nil {
			if code := httpStatus(err); code == 404 || code == 403 {
				c.log.Info("gdocs: листинг Drive недоступен, пропускаю", "code", code)
				return events, nil
			}
			return events, fmt.Errorf("gdocs: листинг файлов Drive: %w", err)
		}

		for _, file := range resp.Files {
			c.rememberUser(file.LastModifyingUser)
			if c.matchUser(file.LastModifyingUser) == matchNone {
				continue
			}
			modified := parseTime(file.ModifiedTime)
			if modified.IsZero() || modified.Before(req.From) || !modified.Before(req.To) {
				continue
			}

			docURL := file.WebViewLink
			if docURL == "" {
				docURL = fmt.Sprintf(docURLTemplate, file.Id)
			}
			ev := models.Event{
				PersonKey:   req.Person.Key,
				Source:      models.SourceGDocs,
				Type:        models.TypeDocEdit,
				ExternalID:  externalID(file.Id, modified, models.TypeDocEdit),
				OccurredAt:  modified,
				Title:       title(file.Name, models.TypeDocEdit),
				URL:         docURL,
				RefID:       file.Id,
				ParentRefID: issueByDoc[file.Id],
				Effort:      0,
				Meta: map[string]any{
					"doc_id":                file.Id,
					"doc_name":              file.Name,
					"mime_type":             docMimeType,
					"issue_key":             issueByDoc[file.Id],
					"action_detail":         "edit",
					"actor_person_name":     "",
					"activity_target_count": 0,
					"from":                  "drive.files.list",
				},
			}
			ev.Normalize()
			events = append(events, ev)

			c.touchEnrichment(file.Id, file.Name, modified)
			c.bumpEnrichment(file.Id, models.TypeDocEdit)
			stats.incEvent()
		}

		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return events, nil
}

// docInfo — сведения о документе, нужные для сборки события.
type docInfo struct {
	ID       string
	Name     string
	MimeType string
	URL      string
	Parent   string
	IssueKey string
}

// buildEvent превращает одну активность Drive в событие. Второй результат —
// false, если активность не наша либо её тип нам не интересен.
func (c *Collector) buildEvent(
	req collectors.Request,
	act *driveactivity.DriveActivity,
	info docInfo,
	stats *runStats,
) (models.Event, bool) {
	if act == nil {
		return models.Event{}, false
	}

	typ, detail, ok := mapActionDetail(act.PrimaryActionDetail)
	if !ok {
		// Move/Rename/PermissionChange и прочее служебное только считаем.
		stats.incSkippedAction(detail)
		return models.Event{}, false
	}

	occurred := activityTime(act)
	if occurred.IsZero() {
		stats.incSkippedAction("no_timestamp")
		return models.Event{}, false
	}

	personName, kind, matched := c.matchActor(act, stats)
	if !matched {
		return models.Event{}, false
	}
	stats.countMatch(kind)

	docURL := info.URL
	if docURL == "" {
		docURL = fmt.Sprintf(docURLTemplate, info.ID)
	}

	meta := map[string]any{
		"doc_id":                info.ID,
		"doc_name":              info.Name,
		"mime_type":             info.MimeType,
		"issue_key":             info.IssueKey,
		"action_detail":         detail,
		"actor_person_name":     personName,
		"actor_matched_by":      string(kind),
		"activity_target_count": len(act.Targets),
	}

	ev := models.Event{
		PersonKey:   req.Person.Key,
		Source:      models.SourceGDocs,
		Type:        typ,
		ExternalID:  externalID(info.ID, occurred, typ),
		OccurredAt:  occurred,
		Title:       title(info.Name, typ),
		URL:         docURL,
		Project:     info.Parent,
		ProjectName: "",
		RefID:       info.ID,
		ParentRefID: info.IssueKey,
		Effort:      0,
		Meta:        meta,
	}
	ev.Normalize()

	c.touchEnrichment(info.ID, info.Name, occurred)
	c.bumpEnrichment(info.ID, typ)
	stats.incEvent()
	return ev, true
}

// queryActivities выполняет постраничный запрос к Drive Activity API.
func (c *Collector) queryActivities(
	ctx context.Context,
	base driveactivity.QueryDriveActivityRequest,
) ([]*driveactivity.DriveActivity, error) {
	base.PageSize = int64(c.pageSize())

	var (
		out   []*driveactivity.DriveActivity
		token string
	)
	for page := 0; page < maxActivityPages; page++ {
		body := base
		body.PageToken = token

		resp, err := c.activity.Activity.Query(&body).Context(ctx).Do()
		if err != nil {
			return out, err
		}
		out = append(out, resp.Activities...)
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

// matchActor решает, наш ли человек автор активности.
//
// Возвращает people-id актора, признак, по которому он опознан, и итог
// сопоставления. Подробности ограничения — в комментарии к типу Collector.
func (c *Collector) matchActor(act *driveactivity.DriveActivity, stats *runStats) (string, matchKind, bool) {
	var known []*driveactivity.KnownUser
	for _, actor := range act.Actors {
		if actor == nil || actor.User == nil || actor.User.KnownUser == nil {
			continue
		}
		known = append(known, actor.User.KnownUser)
	}
	if len(known) == 0 {
		stats.incUnattributed()
		stats.seeAuthor("(актор не указан)")
		return "", matchNone, false
	}

	for _, ku := range known {
		// IsCurrentUser означает «это тот, чьим токеном мы ходим», а вовсе не
		// «это тот, чью активность мы собираем». Доверять флагу можно, только
		// когда учётка API и есть интересующий нас человек — иначе на его
		// дашборд уехали бы чужие действия.
		if ku.IsCurrentUser && c.authenticatedAsPerson() {
			return ku.PersonName, matchAccount, true
		}
		if ref, ok := c.lookupPerson(ku.PersonName); ok {
			if kind := c.matchRef(ref); kind != matchNone {
				return ku.PersonName, kind, true
			}
		}
	}

	// Автора сопоставить не удалось — событие не наше, пока не доказано обратное.
	//
	// Раньше здесь стояло допущение: «если актор ровно один и его email
	// неизвестен, считаем действие нашим». Оно было терпимо, пока корпус
	// ограничивался документами из задач самого человека, но с поиском по всему
	// доступному Drive превращается в приписывание пользователю чужих правок и
	// комментариев. Молчаливая догадка хуже пропуска: пропуск виден в счётчике,
	// а выдуманное событие — нет.
	stats.incUnattributed()
	stats.seeAuthor(c.describePerson(known[0].PersonName))
	return "", matchNone, false
}

// resolveDocPeople наполняет кэш «people-id → email» до разбора активности.
//
// Без этого сопоставить автора почти невозможно: Drive Activity отдаёт актора
// как "people/<id>" и не даёт ни email, ни имени. Тот же id лежит в
// permissionId пользователя в Drive, поэтому пары id↔email добываются из трёх
// мест, каждое из которых может быть закрыто правами — берём что дадут:
//
//   - permissions.list — все, у кого есть доступ к документу (полнее всего,
//     но читателю часто отдаёт 403);
//   - revisions.list — те, кто реально правил документ, то есть ровно те, кого
//     нам и нужно опознать;
//   - метаданные файла (owners, lastModifyingUser) — уже собраны вызывающим.
func (c *Collector) resolveDocPeople(ctx context.Context, docID string) {
	perms, err := c.drive.Permissions.List(docID).
		Fields("permissions(id,emailAddress,displayName,type,deleted)").
		PageSize(100).
		SupportsAllDrives(true).
		Context(ctx).Do()
	if err == nil {
		for _, p := range perms.Permissions {
			if p == nil || p.Id == "" {
				continue
			}
			c.rememberPerson("people/"+p.Id, p.EmailAddress, p.DisplayName)
		}
	} else if code := httpStatus(err); code != 403 && code != 404 {
		c.log.Debug("gdocs: не удалось получить права документа", "doc_id", docID, "err", err)
	}

	revs, err := c.drive.Revisions.List(docID).
		Fields("revisions(id,modifiedTime,lastModifyingUser(permissionId,emailAddress,displayName))").
		PageSize(1000).
		Context(ctx).Do()
	if err == nil {
		for _, r := range revs.Revisions {
			if r != nil {
				c.rememberUser(r.LastModifyingUser)
			}
		}
	} else if code := httpStatus(err); code != 403 && code != 404 {
		c.log.Debug("gdocs: не удалось получить ревизии документа", "doc_id", docID, "err", err)
	}
}

// collectComments собирает комментарии и ответы в тредах через Drive Comments
// API. Это надёжнее ленты активности: у комментария есть автор с email и
// permissionId, поэтому чужие комментарии отсеиваются точно, а не по догадке.
//
// Второй результат — удалось ли прочитать комментарии. Если да, комментарии из
// ленты активности по этому документу отбрасываются, чтобы не задваивать.
func (c *Collector) collectComments(
	ctx context.Context,
	req collectors.Request,
	info docInfo,
	stats *runStats,
) ([]models.Event, bool) {
	var (
		events []models.Event
		token  string
	)

	for page := 0; page < maxActivityPages; page++ {
		call := c.drive.Comments.List(info.ID).
			Fields("nextPageToken, comments(id,content,createdTime,modifiedTime,resolved,deleted," +
				"author(displayName,emailAddress,permissionId,me)," +
				"replies(id,content,createdTime,action,deleted,author(displayName,emailAddress,permissionId,me)))").
			PageSize(100).
			StartModifiedTime(req.From.UTC().Format(time.RFC3339))
		if token != "" {
			call = call.PageToken(token)
		}

		resp, err := call.Context(ctx).Do()
		if err != nil {
			if code := httpStatus(err); code == 403 || code == 404 {
				c.log.Debug("gdocs: комментарии документа недоступны", "doc_id", info.ID, "code", code)
				return nil, false
			}
			c.log.Warn("gdocs: не удалось прочитать комментарии", "doc_id", info.ID, "err", err)
			return nil, false
		}

		for _, cm := range resp.Comments {
			if cm == nil || cm.Deleted {
				continue
			}
			// Автор комментария заодно пополняет кэш: он же может встретиться
			// актором в ленте активности.
			c.rememberUser(cm.Author)
			if ev, ok := c.commentEvent(req, info, cm.Id, "", cm.Author, cm.CreatedTime, cm.Content, stats); ok {
				events = append(events, ev)
			}
			for _, rp := range cm.Replies {
				if rp == nil || rp.Deleted {
					continue
				}
				c.rememberUser(rp.Author)
				if ev, ok := c.commentEvent(req, info, cm.Id, rp.Id, rp.Author, rp.CreatedTime, rp.Content, stats); ok {
					events = append(events, ev)
				}
			}
		}

		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return events, true
}

// commentEvent превращает комментарий или ответ в событие, если его автор —
// наш человек, а время попадает в период.
func (c *Collector) commentEvent(
	req collectors.Request,
	info docInfo,
	commentID, replyID string,
	author *drive.User,
	createdTime, content string,
	stats *runStats,
) (models.Event, bool) {
	kind := c.matchUser(author)
	if kind == matchNone {
		if author != nil {
			stats.incForeignComment()
			stats.seeAuthor(describeUser(author))
		}
		return models.Event{}, false
	}

	created := parseTime(createdTime)
	if created.IsZero() || created.Before(req.From) || !created.Before(req.To) {
		return models.Event{}, false
	}

	kindLabel := "comment"
	if replyID != "" {
		kindLabel = "reply"
	}

	ev := models.Event{
		PersonKey:   req.Person.Key,
		Source:      models.SourceGDocs,
		Type:        models.TypeDocComment,
		ExternalID:  fmt.Sprintf("comment:%s:%s:%s", info.ID, commentID, replyID),
		OccurredAt:  created,
		Title:       title(info.Name, models.TypeDocComment),
		Body:        content,
		URL:         commentURL(info, commentID),
		Project:     info.Parent,
		RefID:       info.ID,
		ParentRefID: info.IssueKey,
		Meta: map[string]any{
			"doc_id":        info.ID,
			"doc_name":      info.Name,
			"mime_type":     info.MimeType,
			"issue_key":     info.IssueKey,
			"action_detail": kindLabel,
			"comment_id":    commentID,
			"reply_id":      replyID,
			"author_email":  normEmail(author.EmailAddress),
			"author_name":   author.DisplayName,
			"matched_by":    string(kind),
			"from":          "drive.comments.list",
		},
	}
	ev.Normalize()

	c.touchEnrichment(info.ID, info.Name, created)
	c.bumpEnrichment(info.ID, models.TypeDocComment)
	stats.incEvent()
	return ev, true
}

// commentURL — ссылка на конкретный комментарий внутри документа.
func commentURL(info docInfo, commentID string) string {
	base := info.URL
	if base == "" {
		base = fmt.Sprintf(docURLTemplate, info.ID)
	}
	if commentID == "" {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "disco=" + commentID
}

// touchEnrichment создаёт/обновляет агрегат по документу.
func (c *Collector) touchEnrichment(docID, docTitle string, seen time.Time) {
	if docID == "" {
		return
	}
	c.enrichMu.Lock()
	defer c.enrichMu.Unlock()

	e, ok := c.enrich[docID]
	if !ok {
		e = &DocEnrichment{DocID: docID}
		c.enrich[docID] = e
	}
	if docTitle != "" {
		e.Title = docTitle
	}
	if !seen.IsZero() && (e.LastModified == nil || seen.After(*e.LastModified)) {
		t := seen.UTC()
		e.LastModified = &t
	}
}

// bumpEnrichment увеличивает счётчики правок и комментариев документа.
func (c *Collector) bumpEnrichment(docID string, typ models.EventType) {
	if docID == "" {
		return
	}
	c.enrichMu.Lock()
	defer c.enrichMu.Unlock()

	e, ok := c.enrich[docID]
	if !ok {
		e = &DocEnrichment{DocID: docID}
		c.enrich[docID] = e
	}
	switch typ {
	case models.TypeDocEdit:
		e.Edits++
	case models.TypeDocComment, models.TypeDocSuggest:
		e.Comments++
	}
}

// pageSize возвращает валидный размер страницы.
func (c *Collector) pageSize() int {
	n := c.cfg.PageSize
	if n <= 0 {
		return defaultPageSize
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

// maxDocs — потолок числа документов, которые обходятся при расширенном поиске.
func (c *Collector) maxDocs() int {
	if n := c.cfg.MaxDocs; n > 0 {
		return n
	}
	return defaultMaxDocs
}

// mapActionDetail переводит PrimaryActionDetail в тип события.
// Для неинтересных нам действий второй результат — их имя (для статистики),
// третий — false.
func mapActionDetail(d *driveactivity.ActionDetail) (models.EventType, string, bool) {
	if d == nil {
		return "", "unknown", false
	}
	switch {
	case d.Edit != nil:
		return models.TypeDocEdit, "edit", true
	case d.Comment != nil:
		// Предложение правки приходит тем же Comment, но с блоком Suggestion.
		if d.Comment.Suggestion != nil {
			return models.TypeDocSuggest, "suggestion", true
		}
		return models.TypeDocComment, "comment", true
	case d.Create != nil:
		return models.TypeDocCreate, "create", true
	case d.Move != nil:
		return "", "move", false
	case d.Rename != nil:
		return "", "rename", false
	case d.PermissionChange != nil:
		return "", "permissionChange", false
	case d.Delete != nil:
		return "", "delete", false
	case d.Restore != nil:
		return "", "restore", false
	case d.SettingsChange != nil:
		return "", "settingsChange", false
	case d.Reference != nil:
		return "", "reference", false
	case d.DlpChange != nil:
		return "", "dlpChange", false
	case d.AppliedLabelChange != nil:
		return "", "appliedLabelChange", false
	default:
		return "", "unknown", false
	}
}

// activityTime берёт момент активности: Timestamp, иначе конец TimeRange.
func activityTime(act *driveactivity.DriveActivity) time.Time {
	if t := parseTime(act.Timestamp); !t.IsZero() {
		return t
	}
	if act.TimeRange != nil {
		if t := parseTime(act.TimeRange.EndTime); !t.IsZero() {
			return t
		}
		if t := parseTime(act.TimeRange.StartTime); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// targetDoc достаёт сведения о документе из целей активности (режим B).
func targetDoc(act *driveactivity.DriveActivity) (docInfo, bool) {
	for _, target := range act.Targets {
		if target == nil {
			continue
		}
		item := target.DriveItem
		if item == nil && target.FileComment != nil {
			item = target.FileComment.Parent
		}
		if item == nil || item.MimeType != docMimeType {
			continue
		}
		id := strings.TrimPrefix(item.Name, "items/")
		if id == "" {
			continue
		}
		name := item.Title
		if name == "" {
			name = id
		}
		return docInfo{
			ID:       id,
			Name:     name,
			MimeType: item.MimeType,
			URL:      fmt.Sprintf(docURLTemplate, id),
		}, true
	}
	return docInfo{}, false
}

// timeFilter собирает фильтр Drive Activity по периоду.
func timeFilter(from, to time.Time) string {
	return fmt.Sprintf("time >= %q AND time < %q",
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
}

// externalID собирает стабильный идентификатор события.
func externalID(docID string, at time.Time, typ models.EventType) string {
	return fmt.Sprintf("activity:%s:%s:%s", docID, at.UTC().Format(time.RFC3339Nano), typ)
}

// title собирает заголовок события: имя документа плюс краткое действие.
func title(docName string, typ models.EventType) string {
	if docName == "" {
		docName = "Документ"
	}
	return docName + " — " + actionWord(typ)
}

// actionWord — краткое описание действия для заголовка.
func actionWord(typ models.EventType) string {
	switch typ {
	case models.TypeDocEdit:
		return "правка"
	case models.TypeDocComment:
		return "комментарий"
	case models.TypeDocSuggest:
		return "предложение правки"
	case models.TypeDocCreate:
		return "создан документ"
	default:
		return string(typ)
	}
}

// parseTime разбирает время из ответов Google (RFC3339).
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// httpStatus достаёт HTTP-код из ошибки Google API (0, если это не она).
func httpStatus(err error) int {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code
	}
	return 0
}

// runStats копит статистику прогона для Result.Note.
type runStats struct {
	mu           sync.Mutex
	events       int
	skippedDocs  int
	skippedTypes map[string]int
	// activityDenied — документы, у которых лента активности закрыта и пришлось
	// довольствоваться метаданными файла.
	activityDenied int
	// unattributed — активности, у которых не удалось опознать автора: они
	// отброшены, а не приписаны нашему человеку.
	unattributed int
	// foreignComments — комментарии, чей автор — не наш человек.
	foreignComments int
	// authorsSeen — чьи действия встретились и были отброшены. Ключевая
	// диагностика: сразу видно, если в выборку лезет не тот человек.
	authorsSeen map[string]int
	// matchKinds — по какому признаку опознавались засчитанные события.
	matchKinds map[string]int
	// discovered — сколько документов нашлось поиском по доступным.
	discovered int
	// truncated — список доступных документов упёрся в потолок MaxDocs.
	truncated bool
	docLimit  int
	mode      string
	identity  string
	person    string
}

func newRunStats() *runStats {
	return &runStats{
		skippedTypes: map[string]int{},
		authorsSeen:  map[string]int{},
		matchKinds:   map[string]int{},
	}
}

// seeAuthor запоминает автора, чьё действие отброшено как чужое.
func (s *runStats) seeAuthor(who string) {
	if who == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorsSeen[who]++
}

// countMatch запоминает, по какому признаку опознали автора засчитанного события.
func (s *runStats) countMatch(kind matchKind) {
	if kind == matchNone {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matchKinds[string(kind)]++
}

func (s *runStats) incEvent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events++
}

func (s *runStats) incSkippedDoc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skippedDocs++
}

func (s *runStats) incSkippedAction(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skippedTypes[name]++
}

func (s *runStats) incActivityDenied() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activityDenied++
}

func (s *runStats) incUnattributed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unattributed++
}

func (s *runStats) incForeignComment() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.foreignComments++
}

func (s *runStats) setDiscovered(n int, truncated bool, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discovered = n
	s.truncated = truncated
	s.docLimit = limit
}

func (s *runStats) setMode(mode, identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	s.identity = identity
}

// setPerson запоминает, чью активность собирали, — чтобы в примечании было
// видно, с кем именно сравнивались авторы.
func (s *runStats) setPerson(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.person = email
}

// note собирает пояснение для UI: в том числе про ограничение Drive Activity
// API по идентификации автора события.
func (s *runStats) note() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var parts []string
	if s.mode != "" {
		mode := "режим: " + s.mode
		if s.identity != "" {
			mode += " от имени " + s.identity
		}
		if s.person != "" {
			mode += "; ищем активность " + s.person
		}
		parts = append(parts, mode)
	}
	if s.discovered > 0 {
		found := fmt.Sprintf("найдено доступных документов: %d", s.discovered)
		if s.truncated {
			found += fmt.Sprintf(" (упёрлись в потолок GOOGLE_MAX_DOCS=%d, часть документов не просмотрена)", s.docLimit)
		}
		parts = append(parts, found)
	}
	parts = append(parts,
		"Drive Activity API не отдаёт email автора: он сопоставляется по карте "+
			"people-id → email из прав доступа, ревизий и комментариев документа. "+
			"Активность с неопознанным автором отбрасывается, а не приписывается человеку.")
	if s.unattributed > 0 {
		parts = append(parts, fmt.Sprintf(
			"отброшено действий с неопознанным автором: %d (нужны права на просмотр "+
				"участников документа, иначе часть правок опознать нельзя)", s.unattributed))
	}
	if s.foreignComments > 0 {
		parts = append(parts, fmt.Sprintf("чужих комментариев отсеяно: %d", s.foreignComments))
	}
	if len(s.matchKinds) > 0 {
		chunks := make([]string, 0, len(s.matchKinds))
		for _, k := range sortedByCount(s.matchKinds, 0) {
			chunks = append(chunks, fmt.Sprintf("%s=%d", k, s.matchKinds[k]))
		}
		parts = append(parts, "автор опознан по: "+strings.Join(chunks, ", "))
	}
	if len(s.authorsSeen) > 0 {
		chunks := make([]string, 0, 5)
		for _, who := range sortedByCount(s.authorsSeen, 5) {
			chunks = append(chunks, fmt.Sprintf("%s — %d", who, s.authorsSeen[who]))
		}
		tail := ""
		if len(s.authorsSeen) > 5 {
			tail = fmt.Sprintf(" и ещё %d", len(s.authorsSeen)-5)
		}
		parts = append(parts, "чьи действия отброшены как чужие: "+strings.Join(chunks, "; ")+tail)
	}
	if s.activityDenied > 0 {
		parts = append(parts, fmt.Sprintf(
			"документов без доступа к ленте активности: %d — по ним взята последняя правка "+
				"из метаданных файла (meta.approximate=true)", s.activityDenied))
	}
	if s.skippedDocs > 0 {
		parts = append(parts, fmt.Sprintf("недоступных документов/папок пропущено: %d", s.skippedDocs))
	}
	if len(s.skippedTypes) > 0 {
		names := make([]string, 0, len(s.skippedTypes))
		for name := range s.skippedTypes {
			names = append(names, name)
		}
		sort.Strings(names)
		chunks := make([]string, 0, len(names))
		for _, name := range names {
			chunks = append(chunks, fmt.Sprintf("%s=%d", name, s.skippedTypes[name]))
		}
		parts = append(parts, "пропущено служебных действий: "+strings.Join(chunks, ", "))
	}
	return strings.Join(parts, "; ")
}

// Проверки контракта на этапе компиляции.
var (
	_ collectors.Collector     = (*Collector)(nil)
	_ collectors.ProgressAware = (*Collector)(nil)
)
