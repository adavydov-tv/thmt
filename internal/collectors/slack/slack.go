// Package slack реализует collectors.Collector поверх Slack Web API.
//
// Поддерживаются две стратегии сбора:
//
//	A (search) — search.messages от имени пользовательского токена (xoxp) со
//	  скоупом search:read. Дёшево по числу запросов и ловит сообщения во всех
//	  каналах, где состоит человек, но не отдаёт реакции.
//	B (channels) — обход conversations.list → conversations.history →
//	  conversations.replies ботовым (или пользовательским) токеном. Дороже, зато
//	  доступны реакции; ограничение — бот должен состоять в канале.
//
// Стратегия B включается автоматически, если пользовательского токена нет либо
// search вернул not_allowed_token_type / missing_scope.
//
// Пакет не тянет внешних зависимостей: только stdlib и internal/*.
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// baseURL — единая точка входа Slack Web API.
const baseURL = "https://slack.com/api"

const (
	// searchRateDelay — Tier 2 (search.messages): ~20 запросов в минуту.
	searchRateDelay = 1100 * time.Millisecond
	// apiRateDelay — щадящая пауза для остальных методов (Tier 3/4).
	apiRateDelay = 250 * time.Millisecond

	// maxSearchPages — предохранитель от бесконечной пагинации search.messages.
	maxSearchPages = 100
	// maxHistoryPages — предохранитель на один канал в стратегии B.
	maxHistoryPages = 200
	// maxRepliesPages — предохранитель на один тред.
	maxRepliesPages = 20
	// maxUserLookups — сколько users.info готовы потратить на раскрытие
	// упоминаний вида <@U123> за один прогон.
	maxUserLookups = 300
	// titleLen — длина заголовка события в рунах.
	titleLen = 120
)

// Collector собирает активность одного человека из Slack.
type Collector struct {
	cfg config.SlackConfig
	log *slog.Logger

	// api — клиент для conversations.*/users.*/auth.test. Использует ботовый
	// токен, если он задан, иначе пользовательский.
	api *httpx.Client
	// search — отдельный клиент под search.messages с повышенной паузой;
	// nil, если пользовательского токена нет.
	search *httpx.Client

	progress collectors.Progress

	mu        sync.Mutex
	teamURL   string            // https://<team>.slack.com/
	channels  map[string]string // id канала → имя без «#»
	users     map[string]string // id пользователя → отображаемое имя
	userCalls int               // израсходованные users.info

	// rawSink/rawBuf — канал-центричный архив всех сообщений (см. RawMessage).
	rawSink func(ctx context.Context, batch []RawMessage) error
	rawBuf  []RawMessage

	// coverage/readArchive — инкрементальный режим стратегии B (см. SetArchive):
	// Slack API вызывается только для непокрытых интервалов, события строятся
	// из архива.
	coverage    CoverageStore
	readArchive func(ctx context.Context, uid string, from, to time.Time) ([]ArchivedMessage, error)
}

// New создаёт коллектор Slack. Требуется хотя бы один непустой токен.
func New(cfg config.SlackConfig, timeout time.Duration, maxRetries int, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(cfg.UserToken) == "" && strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("slack: не задан ни SLACK_BOT_TOKEN, ни SLACK_USER_TOKEN")
	}
	if cfg.PageSize <= 0 || cfg.PageSize > 1000 {
		cfg.PageSize = 200
	}

	// Для conversations.* предпочитаем ботовый токен: у него обычно шире набор
	// каналов, а xoxp годится как запасной вариант.
	apiToken := strings.TrimSpace(cfg.BotToken)
	if apiToken == "" {
		apiToken = strings.TrimSpace(cfg.UserToken)
	}

	api, err := newClient(apiToken, timeout, maxRetries, apiRateDelay)
	if err != nil {
		return nil, err
	}

	var search *httpx.Client
	if ut := strings.TrimSpace(cfg.UserToken); ut != "" {
		search, err = newClient(ut, timeout, maxRetries, searchRateDelay)
		if err != nil {
			return nil, err
		}
	}

	return &Collector{
		cfg:      cfg,
		log:      log.With("collector", "slack"),
		api:      api,
		search:   search,
		channels: map[string]string{},
		users:    map[string]string{},
	}, nil
}

func newClient(token string, timeout time.Duration, maxRetries int, delay time.Duration) (*httpx.Client, error) {
	return httpx.New(httpx.Options{
		BaseURL:    baseURL,
		Timeout:    timeout,
		MaxRetries: maxRetries,
		RateDelay:  delay,
		// Slack принимает POST form-urlencoded с токеном в заголовке.
		Headers: map[string]string{"Authorization": "Bearer " + token},
	})
}

// RawMessage — сырое сообщение/реакция для канал-центричного архива:
// сохраняются ВСЕ авторы (не только заведённые люди), чтобы добавление
// нового человека было линковкой по slack_uid без повторного обхода Slack.
type RawMessage struct {
	ID          string // msg:<ch>:<ts> | reaction:<ch>:<ts>:<name>:<uid>
	ChannelID   string
	ChannelName string
	UID         string
	Kind        string // message | reply | reaction
	TS          time.Time
	SlackTS     string
	ThreadTS    string
	Text        string
	URL         string
	ReplyCount  int
	Reactions   int
	Reaction    string
}

// SetRawSink подключает приёмник сырых сообщений (архив). Батчи сбрасываются
// по мере накопления и в конце обхода.
func (c *Collector) SetRawSink(fn func(ctx context.Context, batch []RawMessage) error) {
	c.rawSink = fn
}

// ArchivedMessage — строка канал-центричного архива, отдаваемая читателем
// архива (SetArchive): из неё строятся события без похода в Slack.
type ArchivedMessage struct {
	ID          string
	ChannelID   string
	ChannelName string
	UID         string
	Kind        string // message | reply | reaction
	TS          time.Time
	SlackTS     string
	ThreadTS    string
	URL         string
	ReplyCount  int
	Reactions   int
	Reaction    string
	AIScore     *float64
}

// CoverageStore — учёт покрытия архива: какие интервалы истории каждого
// канала уже выкачаны. Covered отдаёт интервалы по возрастанию, Mark
// добавляет интервал (пересекающиеся сливает хранилище).
type CoverageStore interface {
	Covered(ctx context.Context, channelID string) ([][2]time.Time, error)
	Mark(ctx context.Context, channelID string, from, to time.Time) error
}

// SetArchive включает инкрементальный режим стратегии B: Slack API вызывается
// только для непокрытых архивом интервалов периода (плюс свежий «хвост»
// RescanDays), обходятся ВСЕ треды (архив должен быть полным для будущей
// линковки), а события всех людей строятся из архива по slack_uid.
func (c *Collector) SetArchive(cov CoverageStore,
	read func(ctx context.Context, uid string, from, to time.Time) ([]ArchivedMessage, error)) {
	c.coverage = cov
	c.readArchive = read
}

// archiveMode — включён ли инкрементальный канал-центричный режим.
func (c *Collector) archiveMode() bool {
	return c.coverage != nil && c.readArchive != nil && c.rawSink != nil
}

func (c *Collector) rescanTail() time.Duration {
	if c.cfg.RescanDays <= 0 {
		return 0
	}
	return time.Duration(c.cfg.RescanDays) * 24 * time.Hour
}

// EventFromArchive — обезличенное событие из строки архива: текст не
// сохраняется (он живёт в зашифрованном архиве), AI-оценка берётся из
// архива, если уже посчитана. Используется и линковкой новых людей.
func EventFromArchive(personKey string, m ArchivedMessage) models.Event {
	display := "#" + m.ChannelName
	if m.ChannelName == "" {
		display = m.ChannelID
	}
	refID := m.ThreadTS
	if refID == "" {
		refID = m.SlackTS
	}
	var typ models.EventType
	var titleText, externalID string
	meta := map[string]any{
		"channel_id":          m.ChannelID,
		"channel_name":        m.ChannelName,
		"thread_ts":           m.ThreadTS,
		"ts":                  m.SlackTS,
		"message_id":          "msg:" + m.ChannelID + ":" + m.SlackTS,
		"reply_count":         m.ReplyCount,
		"reactions_count":     m.Reactions,
		"is_thread_reply":     m.Kind == "reply",
		"permalink":           m.URL,
		"linked_from_archive": true,
		// UID актора (автор сообщения / поставивший реакцию) — чтобы атрибуцию
		// можно было проверить по событию в БД, не поднимая строку архива.
		"actor_uid": m.UID,
	}
	switch m.Kind {
	case "reaction":
		typ = models.TypeSlackReaction
		titleText = fmt.Sprintf(":%s: на сообщение в %s · %s", m.Reaction, display, m.SlackTS)
		externalID = "reaction:" + m.ChannelID + ":" + m.SlackTS + ":" + m.Reaction
		meta["reaction"] = m.Reaction
		meta["time_is_message_ts"] = true
	case "reply":
		typ = models.TypeSlackReply
		titleText = fmt.Sprintf("Ответ в треде %s · %s", display, m.SlackTS)
		externalID = m.ID
	default:
		typ = models.TypeSlackMessage
		titleText = fmt.Sprintf("Сообщение в %s · %s", display, m.SlackTS)
		externalID = m.ID
	}
	if m.AIScore != nil {
		meta["ai_score"] = *m.AIScore
	}
	ev := models.Event{
		PersonKey:   personKey,
		Source:      models.SourceSlack,
		Type:        typ,
		ExternalID:  externalID,
		OccurredAt:  m.TS,
		Title:       titleText,
		URL:         m.URL,
		Project:     m.ChannelID,
		ProjectName: display,
		RefID:       refID,
		Meta:        meta,
	}
	ev.Normalize()
	return ev
}

// uncoveredGaps вычитает покрытые интервалы из [from, to] и добавляет
// «хвост» rescan перед to: свежие треды пополняются ответами и реакциями
// уже после первого обхода. covered — по возрастанию.
func uncoveredGaps(from, to time.Time, covered [][2]time.Time, rescan time.Duration) [][2]time.Time {
	var gaps [][2]time.Time
	cur := from
	for _, iv := range covered {
		s, e := iv[0], iv[1]
		if !e.After(cur) || !s.Before(to) {
			continue
		}
		if s.After(cur) {
			end := s
			if end.After(to) {
				end = to
			}
			gaps = append(gaps, [2]time.Time{cur, end})
		}
		if e.After(cur) {
			cur = e
		}
		if !cur.Before(to) {
			break
		}
	}
	if cur.Before(to) {
		gaps = append(gaps, [2]time.Time{cur, to})
	}
	if rescan > 0 {
		tailFrom := to.Add(-rescan)
		if tailFrom.Before(from) {
			tailFrom = from
		}
		gaps = append(gaps, [2]time.Time{tailFrom, to})
	}
	return mergeGaps(gaps)
}

// mergeGaps сливает пересекающиеся и смежные интервалы.
func mergeGaps(in [][2]time.Time) [][2]time.Time {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i][0].Before(in[j][0]) })
	out := [][2]time.Time{in[0]}
	for _, iv := range in[1:] {
		last := &out[len(out)-1]
		if !iv[0].After(last[1]) {
			if iv[1].After(last[1]) {
				last[1] = iv[1]
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

const rawFlushSize = 500

func (c *Collector) emitRaw(ctx context.Context, m RawMessage) {
	if c.rawSink == nil {
		return
	}
	c.rawBuf = append(c.rawBuf, m)
	if len(c.rawBuf) >= rawFlushSize {
		c.flushRaw(ctx)
	}
}

func (c *Collector) flushRaw(ctx context.Context) {
	if c.rawSink == nil || len(c.rawBuf) == 0 {
		return
	}
	if err := c.rawSink(ctx, c.rawBuf); err != nil {
		c.log.Warn("архив slack: не удалось сохранить батч", "size", len(c.rawBuf), "err", err)
	}
	c.rawBuf = c.rawBuf[:0]
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceSlack }

// SetProgress подключает колбэк прогресса (collectors.ProgressAware).
func (c *Collector) SetProgress(p collectors.Progress) { c.progress = p }

func (c *Collector) report(stage string, done, total int) {
	if c.progress != nil {
		c.progress(stage, done, total)
	}
}

// ---------------------------------------------------------------------------
// Ответы Slack и разбор ошибок
// ---------------------------------------------------------------------------

// baseResp — общая часть любого ответа Slack Web API.
type baseResp struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error"`
	Warning          string `json:"warning"`
	ResponseMetadata struct {
		NextCursor string   `json:"next_cursor"`
		Messages   []string `json:"messages"`
	} `json:"response_metadata"`
}

func (b *baseResp) base() *baseResp { return b }

// slackResponse — любой ответ, у которого есть поля ok/error.
type slackResponse interface{ base() *baseResp }

// APIError — логическая ошибка Slack: HTTP 200, но "ok": false.
type APIError struct {
	Method  string // вызванный метод, например conversations.history
	Code    string // содержимое поля error
	Warning string
	// HaveScopes и NeedScopes Slack отдаёт заголовками x-oauth-scopes и
	// x-accepted-oauth-scopes. Без них диагностика missing_scope сводится к
	// гаданию, какой именно скоуп забыли выдать.
	HaveScopes string
	NeedScopes string
}

// errorHints — человекочитаемые пояснения к самым частым кодам ошибок.
var errorHints = map[string]string{
	"not_allowed_token_type": "метод недоступен для этого типа токена: search.messages работает только с пользовательским токеном xoxp",
	"missing_scope":          "у токена не хватает OAuth-скоупа (для search нужен search:read, для обхода каналов — channels:history, groups:history, channels:read, groups:read)",
	"ratelimited":            "превышен лимит запросов Slack; повторы по Retry-After исчерпаны",
	"rate_limited":           "превышен лимит запросов Slack; повторы по Retry-After исчерпаны",
	"invalid_auth":           "токен недействителен или отозван",
	"account_inactive":       "аккаунт токена деактивирован",
	"token_revoked":          "токен отозван",
	"not_in_channel":         "приложение не состоит в канале",
	"channel_not_found":      "канал не найден или недоступен токену",
	"users_not_found":        "пользователь с таким email не найден в рабочем пространстве",
	"user_not_found":         "пользователь не найден",
	"invalid_cursor":         "некорректный курсор пагинации",
	"fatal_error":            "внутренняя ошибка Slack",
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("slack %s: %s", e.Method, e.Code)
	if hint, ok := errorHints[e.Code]; ok {
		msg += " (" + hint + ")"
	}
	if e.NeedScopes != "" {
		msg += "; методу нужен один из: " + e.NeedScopes
	}
	if e.HaveScopes != "" {
		msg += "; у токена есть: " + e.HaveScopes
	}
	if e.Warning != "" {
		msg += "; warning: " + e.Warning
	}
	return msg
}

// codeIs сообщает, что ошибка — логическая ошибка Slack с одним из кодов.
func codeIs(err error, codes ...string) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if ae.Code == c {
			return true
		}
	}
	return false
}

// call выполняет POST form-urlencoded и проверяет флаг ok в ответе.
func call(ctx context.Context, cl *httpx.Client, method string, form url.Values, out slackResponse) error {
	if cl == nil {
		return fmt.Errorf("slack %s: клиент не сконфигурирован (нет подходящего токена)", method)
	}
	hdr, err := cl.PostForm(ctx, method, form, out)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	b := out.base()
	if !b.OK {
		code := b.Error
		if code == "" {
			code = "unknown_error"
		}
		e := &APIError{Method: method, Code: code, Warning: b.Warning}
		if hdr != nil {
			e.HaveScopes = hdr.Get("x-oauth-scopes")
			e.NeedScopes = hdr.Get("x-accepted-oauth-scopes")
		}
		return e
	}
	return nil
}

// ---------------------------------------------------------------------------
// Структуры ответов конкретных методов
// ---------------------------------------------------------------------------

type authTestResp struct {
	baseResp
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
}

type slackUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Profile  struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

// label возвращает наиболее «человеческое» имя пользователя.
func (u slackUser) label() string {
	for _, v := range []string{u.Profile.DisplayName, u.Profile.RealName, u.RealName, u.Name} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return u.ID
}

type userResp struct {
	baseResp
	User slackUser `json:"user"`
}

type reaction struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Users []string `json:"users"`
}

type message struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Text       string `json:"text"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
	// ReplyUsers — id ответивших в треде (Slack отдаёт первых ~5) и их общее
	// число: по ним треды без нашего человека пропускаются без похода в
	// conversations.replies — главная экономия rate-limit при обходе.
	ReplyUsers      []string   `json:"reply_users"`
	ReplyUsersCount int        `json:"reply_users_count"`
	Permalink       string     `json:"permalink"`
	Reactions       []reaction `json:"reactions"`
}

// threadMayContainAny — стоит ли ходить в тред: точно да, если среди
// reply_users есть кто-то из наших; точно нет, если список полный и наших
// в нём нет. Усечённый список (ответивших больше, чем Slack показал) — идём.
func threadMayContainAny(uids map[string]string, m message) bool {
	for _, u := range m.ReplyUsers {
		if _, ok := uids[u]; ok {
			return true
		}
	}
	if len(m.ReplyUsers) > 0 && m.ReplyUsersCount <= len(m.ReplyUsers) {
		return false
	}
	return true
}

type historyResp struct {
	baseResp
	Messages []message `json:"messages"`
	HasMore  bool      `json:"has_more"`
}

type channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsArchived bool   `json:"is_archived"`
	IsMember   bool   `json:"is_member"`
	IsPrivate  bool   `json:"is_private"`
}

type conversationsListResp struct {
	baseResp
	Channels []channel `json:"channels"`
}

type conversationsInfoResp struct {
	baseResp
	Channel channel `json:"channel"`
}

// searchMatch — элемент messages.matches[] из search.messages.
type searchMatch struct {
	Type     string `json:"type"`
	Team     string `json:"team"`
	User     string `json:"user"`
	Username string `json:"username"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	Channel  struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	Permalink string `json:"permalink"`
	ThreadTS  string `json:"thread_ts"`
}

type searchResp struct {
	baseResp
	Messages struct {
		Total   int           `json:"total"`
		Matches []searchMatch `json:"matches"`
		Paging  struct {
			Count int `json:"count"`
			Total int `json:"total"`
			Page  int `json:"page"`
			Pages int `json:"pages"`
		} `json:"paging"`
	} `json:"messages"`
}

// ---------------------------------------------------------------------------
// Collect
// ---------------------------------------------------------------------------

// Collect выгружает активность человека в Slack за период [req.From, req.To].
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result

	uid, err := c.resolveUserID(ctx, req.Person)
	if err != nil {
		return res, err
	}
	// Найденный по email id отдаём оркестратору — он сохранит его в карточку.
	if strings.TrimSpace(req.Person.SlackUser) == "" {
		res.ResolvedAccount = uid
	}
	// Домен команды нужен, чтобы строить ссылки, когда permalink не пришёл.
	c.loadTeamURL(ctx)

	from, to := req.From.UTC(), req.To.UTC()
	if !to.After(from) {
		return res, fmt.Errorf("slack: некорректный период %s..%s", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}

	// --- Гибрид: search.messages по открытым каналам + бот по приватным ---
	if c.search != nil {
		byPerson, note, err := c.collectHybrid(ctx, map[string]string{uid: req.Person.Key}, from, to)
		switch {
		case err == nil:
			res.Events = byPerson[req.Person.Key]
			res.Note = note
			return res, nil
		case codeIs(err, "not_allowed_token_type", "missing_scope"):
			c.log.Warn("search.messages недоступен, переходим на обход каналов", "err", err.Error())
		default:
			return res, err
		}
	}

	// --- Стратегия B: обход каналов (через общий мультиперсональный код) ---
	byPerson, note, err := c.collectViaChannels(ctx, map[string]string{uid: req.Person.Key}, from, to)
	if err != nil {
		return res, err
	}
	res.Events = byPerson[req.Person.Key]
	if c.search == nil {
		note += "; search.messages недоступен без пользовательского токена xoxp"
	}
	res.Note = note
	return res, nil
}

// CollectGroup — канал-центричный сбор: один обход каналов на всю группу
// людей, события раскладываются по авторам. Возвращает события по ключам
// людей, найденные по email slack-id (для сохранения в карточки) и note.
// Люди, чей slack-id определить не удалось, пропускаются с записью в лог —
// сбор группы из-за одного человека не падает.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (
	map[string][]models.Event, map[string]string, string, error) {

	c.loadTeamURL(ctx)
	uids := make(map[string]string, len(people))     // slack uid → person key
	resolved := make(map[string]string, len(people)) // person key → uid (для карточек)
	for _, p := range people {
		uid, err := c.resolveUserID(ctx, p)
		if err != nil {
			c.log.Warn("групповой сбор: slack-id не определён, человек пропущен",
				"person", p.Key, "err", err.Error())
			continue
		}
		uids[uid] = p.Key
		if strings.TrimSpace(p.SlackUser) == "" {
			resolved[p.Key] = uid
		}
	}
	if len(uids) == 0 {
		return map[string][]models.Event{}, resolved, "групповой сбор: ни у кого не определён slack-id", nil
	}
	var (
		byPerson map[string][]models.Event
		note     string
		err      error
	)
	// Гибрид: при рабочем user-токене открытые каналы закрывает search,
	// бот обходит только приватные. Токен-ошибки — откат на полный обход.
	if c.search != nil {
		byPerson, note, err = c.collectHybrid(ctx, uids, from.UTC(), to.UTC())
		if err != nil && codeIs(err, "not_allowed_token_type", "missing_scope") {
			c.log.Warn("search.messages недоступен, групповой сбор обходом каналов", "err", err.Error())
			byPerson, err = nil, nil
		} else if err != nil {
			return nil, resolved, "", err
		}
	}
	if byPerson == nil {
		byPerson, note, err = c.collectViaChannels(ctx, uids, from.UTC(), to.UTC())
		if err != nil {
			return nil, resolved, "", err
		}
	}
	if len(resolved) > 0 {
		note += fmt.Sprintf("; slack-id найден по email у %d чел.", len(resolved))
	}
	if skipped := len(people) - len(uids); skipped > 0 {
		note += fmt.Sprintf("; пропущено людей без slack-id: %d", skipped)
	}
	return byPerson, resolved, note, nil
}

// resolveUserID определяет Slack-id человека: из карточки либо по email.
func (c *Collector) resolveUserID(ctx context.Context, p models.Person) (string, error) {
	if id := strings.TrimSpace(p.SlackUser); id != "" {
		return id, nil
	}
	email := strings.TrimSpace(p.Email)
	if email == "" {
		return "", fmt.Errorf("slack: у %q не задан ни slack_user_id, ни email — некого искать", p.Key)
	}
	var resp userResp
	if err := call(ctx, c.api, "users.lookupByEmail", url.Values{"email": {email}}, &resp); err != nil {
		return "", fmt.Errorf("slack: не удалось определить пользователя по email %s: %w "+
			"(укажите slack_user_id вида Uxxxxxxxx в карточке человека)", email, err)
	}
	if resp.User.ID == "" {
		return "", fmt.Errorf("slack: users.lookupByEmail для %s вернул пустой id", email)
	}
	c.cacheUser(resp.User)
	c.log.Debug("slack-пользователь найден по email", "email", email, "user_id", resp.User.ID)
	return resp.User.ID, nil
}

// loadTeamURL подтягивает домен рабочего пространства из auth.test.
// Ошибка не фатальна: без домена просто не сможем достроить ссылки вручную.
func (c *Collector) loadTeamURL(ctx context.Context) {
	c.mu.Lock()
	known := c.teamURL != ""
	c.mu.Unlock()
	if known {
		return
	}
	var resp authTestResp
	if err := call(ctx, c.api, "auth.test", url.Values{}, &resp); err != nil {
		c.log.Debug("auth.test не удался, ссылки будут строиться только из permalink", "err", err.Error())
		return
	}
	c.mu.Lock()
	c.teamURL = strings.TrimRight(resp.URL, "/") + "/"
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Стратегия A — search.messages
// ---------------------------------------------------------------------------

// collectViaSearch собирает сообщения через search.messages.
//
// Slack трактует after:/before: как «строго после/до указанного дня», поэтому
// границы расширяются на сутки в обе стороны, а точная отсечка делается по ts.
func (c *Collector) collectViaSearch(ctx context.Context, req collectors.Request, uid string, from, to time.Time) ([]models.Event, error) {
	query := fmt.Sprintf("from:<@%s> after:%s before:%s",
		uid,
		from.AddDate(0, 0, -1).Format("2006-01-02"),
		to.AddDate(0, 0, 1).Format("2006-01-02"),
	)

	var (
		events []models.Event
		seen   = map[string]bool{}
		pages  = 1
	)

	for page := 1; page <= maxSearchPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		form := url.Values{
			"query":    {query},
			"count":    {"100"},
			"page":     {strconv.Itoa(page)},
			"sort":     {"timestamp"},
			"sort_dir": {"asc"},
		}
		var resp searchResp
		if err := call(ctx, c.search, "search.messages", form, &resp); err != nil {
			return nil, err
		}

		if resp.Messages.Paging.Pages > 0 {
			pages = resp.Messages.Paging.Pages
		}
		c.report("search", page, pages)

		for _, m := range resp.Messages.Matches {
			// Чужие сообщения в выдачу попасть не должны, но проверим.
			if m.User != "" && m.User != uid {
				continue
			}
			ts, err := parseTS(m.TS)
			if err != nil {
				continue
			}
			// Границы search — по дням, поэтому дофильтровываем точно.
			if ts.Before(from) || ts.After(to) {
				continue
			}
			chID := m.Channel.ID
			if m.Channel.Name != "" {
				c.putChannel(chID, m.Channel.Name)
			}

			threadTS := m.ThreadTS
			if threadTS == "" {
				// В матчах search thread_ts часто отсутствует, но есть в permalink.
				threadTS = threadTSFromPermalink(m.Permalink)
			}
			typ := models.TypeSlackMessage
			isReply := threadTS != "" && threadTS != m.TS
			if isReply {
				typ = models.TypeSlackReply
			}

			ev := c.buildMessageEvent(ctx, req.Person.Key, chID, m.Text, m.TS, threadTS, m.Permalink, typ, isReply, 0, 0, ts)
			if seen[ev.ExternalID] {
				continue
			}
			seen[ev.ExternalID] = true
			events = append(events, ev)
		}

		if len(resp.Messages.Matches) == 0 || page >= pages {
			break
		}
	}

	c.report("search", pages, pages)
	c.log.Debug("search.messages завершён", "events", len(events), "pages", pages)
	return events, nil
}

// ---------------------------------------------------------------------------
// Стратегия B — обход каналов
// ---------------------------------------------------------------------------

// collectViaChannels собирает сообщения, ответы и реакции обходом каналов.
// collectHybrid — гибридная стратегия при рабочем user-токене (xoxp):
// сообщения людей во ВСЕХ открытых каналах воркспейса находит search.messages
// (обход не нужен), ботовый обход остаётся только для приватных каналов, где
// бот состоит — search их не видит, а реакции search не отдаёт вовсе.
// События дополняются из архива (приватные сообщения, реакции прежних
// обходов); дубли с search отсекаются по ExternalID.
func (c *Collector) collectHybrid(ctx context.Context, uids map[string]string, from, to time.Time) (map[string][]models.Event, string, error) {
	byPerson := map[string][]models.Event{}
	seen := map[string]bool{}
	add := func(ev models.Event) {
		key := ev.PersonKey + "|" + ev.ExternalID
		if seen[key] {
			return
		}
		seen[key] = true
		byPerson[ev.PersonKey] = append(byPerson[ev.PersonKey], ev)
	}

	// search по каждому человеку; ошибка типа токена всплывает наверх —
	// вызывающий откатится на полный обход каналов.
	done := 0
	for uid, pk := range uids {
		evs, err := c.collectViaSearch(ctx, collectors.Request{
			Person: models.Person{Key: pk}, From: from, To: to,
		}, uid, from, to)
		if err != nil {
			return nil, "", err
		}
		for _, ev := range evs {
			add(ev)
		}
		done++
		c.report("search", done, len(uids))
	}

	// Приватные каналы — ботовый обход (инкрементальный при archiveMode).
	chans, err := c.listChannels(ctx)
	if err != nil {
		return nil, "", err
	}
	var private []channel
	for _, ch := range chans {
		if ch.IsPrivate {
			private = append(private, ch)
		}
	}
	walkAdd := add
	if c.archiveMode() {
		walkAdd = func(models.Event) {}
	}
	oldest, latest := formatTS(from), formatTS(to)
	skipped := 0
	c.report("history", 0, len(private))
	for i, ch := range private {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		var werr error
		if c.archiveMode() {
			_, werr = c.walkChannelGaps(ctx, uids, ch, from, to, walkAdd)
		} else {
			_, _, werr = c.walkChannel(ctx, uids, ch, oldest, latest, from, to, walkAdd)
		}
		if werr != nil {
			if codeIs(werr, "not_in_channel", "channel_not_found", "is_archived", "restricted_action") {
				skipped++
				c.log.Debug("приватный канал пропущен", "channel", ch.ID, "err", werr.Error())
			} else {
				return nil, "", werr
			}
		}
		c.report("history", i+1, len(private))
	}
	c.flushRaw(ctx)

	// Архив: приватные сообщения этого обхода и реакции/сообщения прежних
	// ботовых обходов (в т.ч. публичных каналов).
	if c.readArchive != nil {
		for uid, pk := range uids {
			rows, err := c.readArchive(ctx, uid, from, to)
			if err != nil {
				return nil, "", fmt.Errorf("slack: чтение архива для %s: %w", pk, err)
			}
			for _, m := range rows {
				add(EventFromArchive(pk, m))
			}
		}
	}

	note := fmt.Sprintf("гибридный сбор: search.messages по открытым каналам (людей %d) + бот по %d приватным",
		len(uids), len(private))
	if skipped > 0 {
		note += fmt.Sprintf(", пропущено недоступных %d", skipped)
	}
	note += "; реакции доступны только в каналах, которые обходит бот"
	return byPerson, note, nil
}

func (c *Collector) collectViaChannels(ctx context.Context, uids map[string]string, from, to time.Time) (map[string][]models.Event, string, error) {
	byPerson := map[string][]models.Event{}
	chans, err := c.listChannels(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(chans) == 0 {
		return byPerson, "обход каналов (стратегия B): не найдено ни одного доступного канала — " +
			"добавьте приложение в нужные каналы или задайте SLACK_CHANNELS", nil
	}

	var skipped int
	seen := map[string]bool{}
	add := func(ev models.Event) {
		key := ev.PersonKey + "|" + ev.ExternalID
		if seen[key] {
			return
		}
		seen[key] = true
		byPerson[ev.PersonKey] = append(byPerson[ev.PersonKey], ev)
	}

	// Инкрементальный режим: обход по API — только для непокрытых архивом
	// интервалов, события в конце строятся из архива (walkAdd — заглушка,
	// чтобы не плодить дубли с другими id реакций).
	incremental := c.archiveMode()
	walkAdd := add
	if incremental {
		walkAdd = func(models.Event) {}
	}

	oldest := formatTS(from)
	latest := formatTS(to)
	var apiWalked int

	c.report("history", 0, len(chans))
	for i, ch := range chans {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		var err error
		if incremental {
			var walked bool
			walked, err = c.walkChannelGaps(ctx, uids, ch, from, to, walkAdd)
			if walked {
				apiWalked++
			}
		} else {
			_, _, err = c.walkChannel(ctx, uids, ch, oldest, latest, from, to, walkAdd)
		}
		if err != nil {
			// Каналы, куда бот не добавлен, просто пропускаем.
			if codeIs(err, "not_in_channel", "channel_not_found", "is_archived", "restricted_action") {
				skipped++
				c.log.Debug("канал пропущен", "channel", ch.ID, "name", ch.Name, "err", err.Error())
			} else {
				return nil, "", err
			}
		}
		c.report("history", i+1, len(chans))
	}

	// Архив дописан — события всех людей строятся из него линковкой по uid.
	c.flushRaw(ctx)
	if incremental {
		for uid, pk := range uids {
			rows, err := c.readArchive(ctx, uid, from, to)
			if err != nil {
				return nil, "", fmt.Errorf("slack: чтение архива для %s: %w", pk, err)
			}
			for _, m := range rows {
				add(EventFromArchive(pk, m))
			}
		}
	}

	note := fmt.Sprintf("собрано обходом каналов (стратегия B): каналов %d, людей %d", len(chans), len(uids))
	if incremental {
		note = fmt.Sprintf("канал-центричный сбор: каналов %d (обход API только для %d — остальные из архива), людей %d",
			len(chans), apiWalked, len(uids))
	}
	if skipped > 0 {
		note += fmt.Sprintf(", пропущено недоступных %d (приложение не состоит в канале)", skipped)
	}
	if c.cfg.IncludeReactions {
		note += "; реакции включены (время реакции Slack не отдаёт — используется ts сообщения)"
	}
	return byPerson, note, nil
}

// walkChannelGaps обходит только непокрытые архивом интервалы канала (плюс
// свежий «хвост» RescanDays) и помечает период покрытым. Обрезанный
// предохранителем страниц обход покрытием не помечается — следующий запуск
// дособерёт. Возвращает, ходил ли обход в Slack API.
func (c *Collector) walkChannelGaps(ctx context.Context, uids map[string]string, ch channel,
	from, to time.Time, add func(models.Event)) (bool, error) {

	covered, err := c.coverage.Covered(ctx, ch.ID)
	if err != nil {
		return false, fmt.Errorf("slack: покрытие канала %s: %w", ch.ID, err)
	}
	gaps := uncoveredGaps(from, to, covered, c.rescanTail())
	if len(gaps) == 0 {
		return false, nil
	}
	truncated := false
	for _, g := range gaps {
		_, tr, err := c.walkChannel(ctx, uids, ch, formatTS(g[0]), formatTS(g[1]), g[0], g[1], add)
		if err != nil {
			return true, err
		}
		truncated = truncated || tr
	}
	if truncated {
		c.log.Warn("канал обрезан предохранителем страниц — покрытие не помечено",
			"channel", ch.Name, "id", ch.ID)
		return true, nil
	}
	if err := c.coverage.Mark(ctx, ch.ID, from, to); err != nil {
		c.log.Warn("не удалось пометить покрытие канала", "channel", ch.ID, "err", err)
	}
	return true, nil
}

// walkChannel обходит историю одного канала и его треды. Второй результат —
// обход обрезан предохранителем страниц (история длиннее лимита).
func (c *Collector) walkChannel(ctx context.Context, uids map[string]string, ch channel,
	oldest, latest string, from, to time.Time, add func(models.Event)) (int, bool, error) {

	var (
		cursor    string
		count     int
		truncated bool
	)
	for page := 0; ; page++ {
		if page >= maxHistoryPages {
			truncated = true
			break
		}
		if err := ctx.Err(); err != nil {
			return count, truncated, err
		}
		form := url.Values{
			"channel":   {ch.ID},
			"oldest":    {oldest},
			"latest":    {latest},
			"inclusive": {"true"},
			"limit":     {strconv.Itoa(c.cfg.PageSize)},
		}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var resp historyResp
		if err := call(ctx, c.api, "conversations.history", form, &resp); err != nil {
			return count, truncated, err
		}

		for _, m := range resp.Messages {
			count += c.handleMessage(ctx, uids, ch, m, from, to, add)

			// Ответы в треде отдельным запросом: history их не возвращает.
			// В инкрементальном режиме обходим ВСЕ треды — архив должен быть
			// полным, чтобы будущие люди линковались без повторного обхода.
			// В легаси-режиме треды без наших людей (полный reply_users без
			// них) пропускаем — главная экономия rate-limit.
			if m.ReplyCount > 0 && m.TS != "" && (c.archiveMode() || threadMayContainAny(uids, m)) {
				n, tr, err := c.walkThread(ctx, uids, ch, m.TS, from, to, add)
				if err != nil {
					if codeIs(err, "thread_not_found", "message_not_found", "not_in_channel", "channel_not_found") {
						c.log.Debug("тред пропущен", "channel", ch.ID, "thread_ts", m.TS, "err", err.Error())
					} else {
						return count, truncated, err
					}
				}
				truncated = truncated || tr
				count += n
			}
		}

		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}
	return count, truncated, nil
}

// walkThread добирает ответы в треде. Второй результат — тред обрезан
// предохранителем страниц.
func (c *Collector) walkThread(ctx context.Context, uids map[string]string, ch channel,
	threadTS string, from, to time.Time, add func(models.Event)) (int, bool, error) {

	var (
		cursor string
		count  int
	)
	for page := 0; page < maxRepliesPages; page++ {
		if err := ctx.Err(); err != nil {
			return count, false, err
		}
		form := url.Values{
			"channel": {ch.ID},
			"ts":      {threadTS},
			"limit":   {strconv.Itoa(c.cfg.PageSize)},
		}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var resp historyResp
		if err := call(ctx, c.api, "conversations.replies", form, &resp); err != nil {
			return count, false, err
		}
		for _, m := range resp.Messages {
			if m.TS == threadTS {
				continue // родительское сообщение уже обработано в history
			}
			count += c.handleMessage(ctx, uids, ch, m, from, to, add)
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			return count, false, nil
		}
	}
	return count, true, nil
}

// handleMessage превращает одно сообщение в события (само сообщение и реакции).
func (c *Collector) handleMessage(ctx context.Context, uids map[string]string, ch channel,
	m message, from, to time.Time, add func(models.Event)) int {

	ts, err := parseTS(m.TS)
	if err != nil {
		return 0
	}
	var n int

	inWindow := !ts.Before(from) && !ts.After(to)

	// Архив: каждое пользовательское сообщение окна, автор любой — по нему
	// позже линкуются новые люди без повторного обхода Slack.
	if c.rawSink != nil && m.User != "" && !skipSubtype(m.Subtype) && inWindow {
		kind := "message"
		if m.ThreadTS != "" && m.ThreadTS != m.TS {
			kind = "reply"
		}
		chName := c.channelName(ctx, ch.ID)
		link := m.Permalink
		if link == "" {
			link = c.buildPermalink(ch.ID, m.TS)
		}
		c.emitRaw(ctx, RawMessage{
			ID: "msg:" + ch.ID + ":" + m.TS, ChannelID: ch.ID, ChannelName: chName,
			UID: m.User, Kind: kind, TS: ts, SlackTS: m.TS, ThreadTS: m.ThreadTS,
			Text: c.clean(ctx, m.Text), URL: link,
			ReplyCount: m.ReplyCount, Reactions: reactionsCount(m.Reactions),
		})
		if c.cfg.IncludeReactions && inWindow {
			for _, r := range m.Reactions {
				for _, ru := range r.Users {
					c.emitRaw(ctx, RawMessage{
						ID:        "reaction:" + ch.ID + ":" + m.TS + ":" + r.Name + ":" + ru,
						ChannelID: ch.ID, ChannelName: chName, UID: ru, Kind: "reaction",
						TS: ts, SlackTS: m.TS, ThreadTS: m.ThreadTS, URL: link, Reaction: r.Name,
						ReplyCount: m.ReplyCount, Reactions: reactionsCount(m.Reactions),
					})
				}
			}
		}
	}

	// Само сообщение — если автор один из наших людей.
	if pk, ok := uids[m.User]; ok && !skipSubtype(m.Subtype) && inWindow {
		isReply := m.ThreadTS != "" && m.ThreadTS != m.TS
		typ := models.TypeSlackMessage
		if isReply {
			typ = models.TypeSlackReply
		}
		add(c.buildMessageEvent(ctx, pk, ch.ID, m.Text, m.TS, m.ThreadTS, m.Permalink, typ, isReply,
			m.ReplyCount, reactionsCount(m.Reactions), ts))
		n++
	}

	// Реакции — каждому нашему человеку, поставившему реакцию.
	if c.cfg.IncludeReactions && inWindow {
		for _, r := range m.Reactions {
			for _, ru := range r.Users {
				if pk, ok := uids[ru]; ok {
					add(c.buildReactionEvent(ctx, pk, ch.ID, m, r, ts))
					n++
				}
			}
		}
	}
	return n
}

// listChannels возвращает каналы для обхода с учётом cfg.Channels.
func (c *Collector) listChannels(ctx context.Context) ([]channel, error) {
	wanted := map[string]bool{}
	allIDs := true
	for _, raw := range c.cfg.Channels {
		v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		if v == "" {
			continue
		}
		wanted[strings.ToLower(v)] = true
		if !looksLikeChannelID(v) {
			allIDs = false
		}
	}

	// Если заданы только id — не нужен полный обход воркспейса.
	if len(wanted) > 0 && allIDs {
		var out []channel
		for _, raw := range c.cfg.Channels {
			id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
			if id == "" {
				continue
			}
			var resp conversationsInfoResp
			if err := call(ctx, c.api, "conversations.info", url.Values{"channel": {id}}, &resp); err != nil {
				if codeIs(err, "channel_not_found", "not_in_channel") {
					c.log.Debug("канал из SLACK_CHANNELS недоступен", "channel", id, "err", err.Error())
					continue
				}
				return nil, err
			}
			c.putChannel(resp.Channel.ID, resp.Channel.Name)
			out = append(out, resp.Channel)
		}
		c.report("channels", len(out), len(out))
		return out, nil
	}

	// Без явного SLACK_CHANNELS обходим только каналы, где токен состоит.
	// users.conversations отдаёт ровно их (одна-две страницы), тогда как
	// conversations.list пагинирует ВЕСЬ воркспейс (тысячи каналов, Tier 2
	// ~20 запросов/мин — минуты на один только список).
	method := "conversations.list"
	if len(wanted) == 0 {
		method = "users.conversations"
	}

	var (
		out    []channel
		cursor string
	)
	for page := 0; page < 100; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		form := url.Values{
			"types":            {"public_channel,private_channel"},
			"exclude_archived": {"true"},
			"limit":            {"200"},
		}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var resp conversationsListResp
		if err := call(ctx, c.api, method, form, &resp); err != nil {
			return nil, err
		}
		for _, ch := range resp.Channels {
			c.putChannel(ch.ID, ch.Name)
			if len(wanted) > 0 && !wanted[strings.ToLower(ch.ID)] && !wanted[strings.ToLower(ch.Name)] {
				continue
			}
			out = append(out, ch)
		}
		c.report("channels", len(out), len(out))
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Сборка событий
// ---------------------------------------------------------------------------

// buildMessageEvent собирает событие «сообщение»/«ответ в треде».
func (c *Collector) buildMessageEvent(ctx context.Context, personKey, chID, text, ts, threadTS, permalink string,
	typ models.EventType, isReply bool, replyCount, reactionsCnt int, occurred time.Time) models.Event {

	chName := c.channelName(ctx, chID)
	_ = text // текст события не сохраняется: он живёт в зашифрованном архиве
	link := permalink
	if link == "" {
		link = c.buildPermalink(chID, ts)
	}
	refID := threadTS
	if refID == "" {
		refID = ts
	}

	msgID := "msg:" + chID + ":" + ts
	titleText := fmt.Sprintf("Сообщение в %s · %s", displayChannel(chName), ts)
	if typ == models.TypeSlackReply {
		titleText = fmt.Sprintf("Ответ в треде %s · %s", displayChannel(chName), ts)
	}
	ev := models.Event{
		PersonKey:   personKey,
		Source:      models.SourceSlack,
		Type:        typ,
		ExternalID:  msgID,
		OccurredAt:  occurred,
		Title:       titleText,
		Body:        "",
		URL:         link,
		Project:     chID,
		ProjectName: displayChannel(chName),
		RefID:       refID,
		Effort:      0,
		Meta: map[string]any{
			"channel_id":      chID,
			"channel_name":    chName,
			"thread_ts":       threadTS,
			"ts":              ts,
			"message_id":      msgID,
			"reply_count":     replyCount,
			"reactions_count": reactionsCnt,
			"is_thread_reply": isReply,
			"permalink":       link,
		},
	}
	ev.Normalize()
	return ev
}

// buildReactionEvent собирает событие «реакция».
//
// Slack не хранит время постановки реакции, поэтому берём ts сообщения и
// помечаем это в meta через time_is_message_ts.
func (c *Collector) buildReactionEvent(ctx context.Context, personKey, chID string,
	m message, r reaction, occurred time.Time) models.Event {

	chName := c.channelName(ctx, chID)
	link := m.Permalink
	if link == "" {
		link = c.buildPermalink(chID, m.TS)
	}
	refID := m.ThreadTS
	if refID == "" {
		refID = m.TS
	}

	titleText := fmt.Sprintf(":%s: на сообщение в %s · %s", r.Name, displayChannel(chName), m.TS)

	ev := models.Event{
		PersonKey:   personKey,
		Source:      models.SourceSlack,
		Type:        models.TypeSlackReaction,
		ExternalID:  "reaction:" + chID + ":" + m.TS + ":" + r.Name,
		OccurredAt:  occurred,
		Title:       titleText,
		Body:        "",
		URL:         link,
		Project:     chID,
		ProjectName: displayChannel(chName),
		RefID:       refID,
		Effort:      0,
		Meta: map[string]any{
			"channel_id":         chID,
			"channel_name":       chName,
			"thread_ts":          m.ThreadTS,
			"ts":                 m.TS,
			"message_id":         "msg:" + chID + ":" + m.TS,
			"reply_count":        m.ReplyCount,
			"reactions_count":    reactionsCount(m.Reactions),
			"is_thread_reply":    m.ThreadTS != "" && m.ThreadTS != m.TS,
			"permalink":          link,
			"reaction":           r.Name,
			"time_is_message_ts": true,
		},
	}
	ev.Normalize()
	return ev
}

// buildPermalink достраивает ссылку вида
// https://<team>.slack.com/archives/<channel>/p<ts без точки>.
func (c *Collector) buildPermalink(chID, ts string) string {
	c.mu.Lock()
	team := c.teamURL
	c.mu.Unlock()
	if team == "" || chID == "" || ts == "" {
		return ""
	}
	return team + "archives/" + chID + "/p" + strings.ReplaceAll(ts, ".", "")
}

// ---------------------------------------------------------------------------
// Кэши имён
// ---------------------------------------------------------------------------

func (c *Collector) putChannel(id, name string) {
	if id == "" || name == "" {
		return
	}
	c.mu.Lock()
	c.channels[id] = name
	c.mu.Unlock()
}

func (c *Collector) cacheUser(u slackUser) {
	if u.ID == "" {
		return
	}
	c.mu.Lock()
	c.users[u.ID] = u.label()
	c.mu.Unlock()
}

// channelName возвращает имя канала (без «#»), при промахе кэша дёргает
// conversations.info. Ошибка не фатальна — тогда остаётся id.
func (c *Collector) channelName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	c.mu.Lock()
	name, ok := c.channels[id]
	c.mu.Unlock()
	if ok {
		return name
	}
	var resp conversationsInfoResp
	if err := call(ctx, c.api, "conversations.info", url.Values{"channel": {id}}, &resp); err != nil {
		c.log.Debug("conversations.info не удался", "channel", id, "err", err.Error())
		c.putChannel(id, id) // отрицательный кэш, чтобы не долбить API
		return id
	}
	if resp.Channel.Name == "" {
		c.putChannel(id, id)
		return id
	}
	c.putChannel(id, resp.Channel.Name)
	return resp.Channel.Name
}

// userName возвращает отображаемое имя по id пользователя. Бюджет запросов
// ограничен maxUserLookups: сверх него упоминания остаются в виде id.
func (c *Collector) userName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	c.mu.Lock()
	name, ok := c.users[id]
	budgetLeft := c.userCalls < maxUserLookups
	c.mu.Unlock()
	if ok {
		return name
	}
	if !budgetLeft {
		return id
	}

	var resp userResp
	c.mu.Lock()
	c.userCalls++
	c.mu.Unlock()
	if err := call(ctx, c.api, "users.info", url.Values{"user": {id}}, &resp); err != nil {
		c.log.Debug("users.info не удался", "user", id, "err", err.Error())
		c.mu.Lock()
		c.users[id] = id // отрицательный кэш
		c.mu.Unlock()
		return id
	}
	c.cacheUser(resp.User)
	return resp.User.label()
}

// clean — обёртка над cleanSlackText с резолвом имён через кэши коллектора.
func (c *Collector) clean(ctx context.Context, text string) string {
	return cleanSlackText(text,
		func(id string) string { return c.userName(ctx, id) },
		func(id string) string {
			c.mu.Lock()
			name := c.channels[id]
			c.mu.Unlock()
			return name
		},
	)
}

// ---------------------------------------------------------------------------
// Разметка Slack
// ---------------------------------------------------------------------------

var (
	reUserMention = regexp.MustCompile(`<@([UWB][A-Z0-9]+)(?:\|([^>]*))?>`)
	reChannelRef  = regexp.MustCompile(`<#([CGD][A-Z0-9]+)(?:\|([^>]*))?>`)
	reSubteam     = regexp.MustCompile(`<!subteam\^([A-Z0-9]+)(?:\|([^>]*))?>`)
	reSpecial     = regexp.MustCompile(`<!(here|channel|everyone)(?:\|[^>]*)?>`)
	reLink        = regexp.MustCompile(`<((?:https?://|mailto:|tel:)[^|>]*)(?:\|([^>]*))?>`)
	reWhitespace  = regexp.MustCompile(`[ \t]+`)
)

var htmlUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// cleanSlackText раскрывает разметку Slack в читаемый текст:
// упоминания пользователей и каналов, групповые и специальные упоминания,
// ссылки вида <url|label> и HTML-сущности.
//
// resolveUser и resolveChannel могут вернуть пустую строку — тогда в тексте
// остаётся исходный идентификатор.
func cleanSlackText(s string, resolveUser, resolveChannel func(string) string) string {
	if s == "" {
		return ""
	}

	s = reUserMention.ReplaceAllStringFunc(s, func(m string) string {
		g := reUserMention.FindStringSubmatch(m)
		if len(g) > 2 && strings.TrimSpace(g[2]) != "" {
			return "@" + g[2]
		}
		if resolveUser != nil {
			if name := strings.TrimSpace(resolveUser(g[1])); name != "" {
				return "@" + name
			}
		}
		return "@" + g[1]
	})

	s = reChannelRef.ReplaceAllStringFunc(s, func(m string) string {
		g := reChannelRef.FindStringSubmatch(m)
		if len(g) > 2 && strings.TrimSpace(g[2]) != "" {
			return "#" + g[2]
		}
		if resolveChannel != nil {
			if name := strings.TrimSpace(resolveChannel(g[1])); name != "" {
				return "#" + name
			}
		}
		return "#" + g[1]
	})

	s = reSubteam.ReplaceAllStringFunc(s, func(m string) string {
		g := reSubteam.FindStringSubmatch(m)
		if label := strings.TrimSpace(g[2]); len(g) > 2 && label != "" {
			// Slack обычно уже присылает метку с «@».
			return "@" + strings.TrimPrefix(label, "@")
		}
		return "@" + g[1]
	})

	s = reSpecial.ReplaceAllString(s, "@$1")

	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		if len(g) > 2 && strings.TrimSpace(g[2]) != "" {
			return g[2]
		}
		return g[1]
	})

	// Экранирование Slack снимаем последним, чтобы не сломать разбор разметки.
	s = htmlUnescaper.Replace(s)
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// Мелкие утилиты
// ---------------------------------------------------------------------------

// parseTS разбирает slack-таймстемп "1712345678.123456".
func parseTS(ts string) (time.Time, error) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, errors.New("пустой ts")
	}
	secStr, fracStr, _ := strings.Cut(ts, ".")
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("некорректный ts %q: %w", ts, err)
	}
	var nsec int64
	if fracStr != "" {
		if len(fracStr) > 9 {
			fracStr = fracStr[:9]
		}
		fracStr += strings.Repeat("0", 9-len(fracStr))
		if n, err := strconv.ParseInt(fracStr, 10, 64); err == nil {
			nsec = n
		}
	}
	return time.Unix(sec, nsec).UTC(), nil
}

// formatTS переводит время в slack-таймстемп с дробной частью.
func formatTS(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 6, 64)
}

// threadTSFromPermalink вытаскивает thread_ts из permalink-а матча search.
func threadTSFromPermalink(link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("thread_ts")
}

// looksLikeChannelID отличает id канала (C/G/D + буквы-цифры) от имени.
func looksLikeChannelID(s string) bool {
	if len(s) < 8 {
		return false
	}
	switch s[0] {
	case 'C', 'G', 'D':
	default:
		return false
	}
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

// skipSubtype отсекает служебные сообщения, которые не являются активностью.
func skipSubtype(sub string) bool {
	switch sub {
	case "channel_join", "channel_leave", "group_join", "group_leave",
		"channel_topic", "channel_purpose", "channel_name", "channel_archive",
		"channel_unarchive", "bot_message", "tombstone":
		return true
	}
	return false
}

func reactionsCount(rs []reaction) int {
	n := 0
	for _, r := range rs {
		n += r.Count
	}
	return n
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// displayChannel приводит имя канала к виду «#name».
func displayChannel(name string) string {
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "#") {
		return name
	}
	return "#" + name
}

// title формирует однострочный заголовок события из текста сообщения.
func title(text, chName string) string {
	if t := oneLine(text, titleLen); t != "" {
		return t
	}
	if chName != "" {
		return "Сообщение в " + displayChannel(chName)
	}
	return "Сообщение в Slack"
}

// oneLine схлопывает переводы строк и обрезает текст до n рун.
func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = reWhitespace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// Проверки контракта на этапе компиляции.
var (
	_ collectors.Collector     = (*Collector)(nil)
	_ collectors.ProgressAware = (*Collector)(nil)
)
