// Package models содержит унифицированную модель активности, общую для всех
// источников (Jira, GitLab, Slack, Google Docs, Google Calendar).
package models

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source — источник данных.
type Source string

const (
	SourceJira       Source = "jira"
	SourceGitLab     Source = "gitlab"
	SourceSlack      Source = "slack"
	SourceGDocs      Source = "gdocs"
	SourceGCal       Source = "gcal"
	SourceAllure     Source = "allure"
	SourceConfluence Source = "confluence"
	// SourceGWork — аудит Google Workspace (Admin SDK Reports API): реальное
	// присутствие на встречах Meet, просмотры Drive, логины, счётчики Gmail,
	// устройства. Отдельно от gdocs/gcal: другой скоуп и админ-доступ.
	SourceGWork Source = "gwork"
	// SourceArgoCD — деплои через Argo CD (кто синкал приложение в прод).
	SourceArgoCD Source = "argocd"
	// SourceZabbix — работа с мониторингом Zabbix: квитирование проблем,
	// комментарии к инцидентам, изменения триггеров/хостов (auditlog).
	SourceZabbix Source = "zabbix"
	// SourceJenkins — запуски сборок/деплоев в Jenkins (кто запустил job).
	SourceJenkins Source = "jenkins"
	// SourceGrafana — работа с Grafana: правки дашбордов и аннотации.
	SourceGrafana Source = "grafana"
	// SourceFigma — работа в Figma: правки файлов, комментарии, публикации
	// библиотек (org-wide Activity Logs).
	SourceFigma Source = "figma"
	// SourceNetSuite — аудит действий в NetSuite ERP: изменения записей
	// (SystemNote) и входы (LoginAudit) через SuiteQL.
	SourceNetSuite Source = "netsuite"
	// SourceClaude — использование Claude Code (число запросов и токенов по
	// дням), выгружаемое в S3-бакет. Атрибуция по e-mail из записи.
	SourceClaude Source = "claude"
)

// AllSources — порядок источников, используемый и в UI (цветовые слоты).
var AllSources = []Source{
	SourceJira, SourceGitLab, SourceSlack, SourceGDocs, SourceGCal, SourceAllure, SourceConfluence, SourceGWork, SourceArgoCD, SourceZabbix, SourceJenkins, SourceGrafana, SourceFigma, SourceNetSuite, SourceClaude,
}

// EventType — тип активности. Значения стабильны: на них завязаны фильтры UI.
type EventType string

const (
	// Jira
	TypeIssueCreated  EventType = "jira.issue_created"
	TypeIssueAssigned EventType = "jira.issue_assigned"
	TypeIssueResolved EventType = "jira.issue_resolved"
	TypeStatusChanged EventType = "jira.status_changed"
	TypeFieldChanged  EventType = "jira.field_changed"
	TypeJiraComment   EventType = "jira.comment"
	TypeWorklog       EventType = "jira.worklog"

	// GitLab
	TypeCommit        EventType = "gitlab.commit"
	TypeMROpened      EventType = "gitlab.mr_opened"
	TypeMRMerged      EventType = "gitlab.mr_merged"
	TypeMRClosed      EventType = "gitlab.mr_closed"
	TypeMRApproved    EventType = "gitlab.mr_approved"
	TypeReviewComment EventType = "gitlab.review_comment"
	TypeGitLabIssue   EventType = "gitlab.issue"
	TypeGitLabNote    EventType = "gitlab.note"
	TypePush          EventType = "gitlab.push"

	// Slack
	TypeSlackMessage  EventType = "slack.message"
	TypeSlackReply    EventType = "slack.thread_reply"
	TypeSlackReaction EventType = "slack.reaction"

	// Google Docs
	TypeDocEdit    EventType = "gdocs.edit"
	TypeDocComment EventType = "gdocs.comment"
	TypeDocCreate  EventType = "gdocs.create"
	TypeDocSuggest EventType = "gdocs.suggestion"

	// Google Calendar
	TypeMeeting   EventType = "gcal.meeting"
	TypeRecurring EventType = "gcal.recurring"
	TypeOneOnOne  EventType = "gcal.one_on_one"
	TypeInterview EventType = "gcal.interview"

	// Allure TestOps
	TypeAllureLaunch      EventType = "allure.launch"
	TypeAllureCaseCreated EventType = "allure.testcase_created"
	TypeAllureCaseUpdated EventType = "allure.testcase_updated"
	TypeAllureDefect      EventType = "allure.defect"

	// Confluence
	TypeConfluencePageCreated EventType = "confluence.page_created"
	TypeConfluencePageEdited  EventType = "confluence.page_edited"
	TypeConfluenceComment     EventType = "confluence.comment"
	TypeConfluenceBlogpost    EventType = "confluence.blogpost"

	// Google Workspace audit (source gwork).
	TypeMeetAttended  EventType = "gwork.meet_attended"  // факт присутствия на звонке Meet
	TypeDriveView     EventType = "gwork.drive_view"     // просмотр документа
	TypeDriveDownload EventType = "gwork.drive_download" // скачивание
	TypeDriveEdit     EventType = "gwork.drive_edit"     // правка/создание документа (org-wide аудит)
	TypeCalendarEdit  EventType = "gwork.calendar_edit"  // действие с календарём: создание/правка/удаление встречи
	TypeLogin         EventType = "gwork.login"          // вход в аккаунт
	TypeGmailSent     EventType = "gwork.gmail_sent"     // отправлено писем за день (счётчик)
	TypeDeviceSync    EventType = "gwork.device_sync"    // активность устройства

	// Argo CD (source argocd).
	TypeDeploySync EventType = "argocd.sync" // ручной деплой приложения

	// Zabbix (source zabbix).
	TypeZabbixAck    EventType = "zabbix.ack"    // квитирование/комментарий к проблеме
	TypeZabbixChange EventType = "zabbix.change" // изменение триггера/хоста/шаблона (auditlog)

	// Jenkins (source jenkins).
	TypeJenkinsBuild EventType = "jenkins.build" // запуск сборки/джобы человеком

	// Grafana (source grafana).
	TypeGrafanaDashboard  EventType = "grafana.dashboard_edit" // правка дашборда (новая версия)
	TypeGrafanaAnnotation EventType = "grafana.annotation"     // аннотация на графике

	// Figma (source figma).
	TypeFigmaEdit    EventType = "figma.file_edit" // правка/сохранение версии файла
	TypeFigmaComment EventType = "figma.comment"   // комментарий в файле
	TypeFigmaPublish EventType = "figma.publish"   // публикация библиотеки/компонентов

	// NetSuite (source netsuite).
	TypeNetSuiteChange EventType = "netsuite.change" // изменение записи (SystemNote)
	TypeNetSuiteLogin  EventType = "netsuite.login"  // вход в NetSuite (LoginAudit)

	// Claude (source claude).
	TypeClaudeUsage EventType = "claude.usage" // использование Claude Code за день (запросы/токены)
)

// SourceOf возвращает источник по типу события.
func (t EventType) SourceOf() Source {
	if i := strings.Index(string(t), "."); i > 0 {
		return Source(string(t)[:i])
	}
	return ""
}

// Label — человекочитаемое название типа события (используется в UI как fallback).
func (t EventType) Label() string {
	if l, ok := typeLabels[t]; ok {
		return l
	}
	return string(t)
}

var typeLabels = map[EventType]string{
	TypeIssueCreated:  "Создана задача",
	TypeIssueAssigned: "Назначена задача",
	TypeIssueResolved: "Закрыта задача",
	TypeStatusChanged: "Переход статуса",
	TypeFieldChanged:  "Изменено поле",
	TypeJiraComment:   "Комментарий в Jira",
	TypeWorklog:       "Ворклог",
	TypeCommit:        "Коммит",
	TypeMROpened:      "Открыт MR",
	TypeMRMerged:      "Смёржен MR",
	TypeMRClosed:      "Закрыт MR",
	TypeMRApproved:    "Апрув MR",
	TypeReviewComment: "Комментарий ревью",
	TypeGitLabIssue:   "Issue в GitLab",
	TypeGitLabNote:    "Заметка в GitLab",
	TypePush:          "Push",
	TypeSlackMessage:  "Сообщение",
	TypeSlackReply:    "Ответ в треде",
	TypeSlackReaction: "Реакция",
	TypeDocEdit:       "Правка документа",
	TypeDocComment:    "Комментарий к документу",
	TypeDocCreate:     "Создан документ",
	TypeDocSuggest:    "Предложение правки",
	TypeMeeting:       "Разовая встреча",
	TypeRecurring:     "Регулярная встреча",
	TypeOneOnOne:      "Встреча 1:1",
	TypeInterview:     "Собеседование",

	TypeAllureLaunch:      "Запуск тестов",
	TypeAllureCaseCreated: "Создан тест-кейс",
	TypeAllureCaseUpdated: "Изменён тест-кейс",
	TypeAllureDefect:      "Заведён дефект",

	TypeConfluencePageCreated: "Создана страница",
	TypeConfluencePageEdited:  "Правка страницы",
	TypeConfluenceComment:     "Комментарий в Confluence",
	TypeConfluenceBlogpost:    "Пост в блоге",

	TypeMeetAttended:  "Присутствие на встрече",
	TypeDriveView:     "Просмотр документа",
	TypeDriveDownload: "Скачивание документа",
	TypeDriveEdit:     "Правка документа (Workspace)",
	TypeCalendarEdit:  "Действие с календарём (Workspace)",
	TypeLogin:         "Вход в аккаунт",
	TypeGmailSent:     "Отправка писем",
	TypeDeviceSync:    "Активность устройства",

	TypeDeploySync: "Деплой (Argo CD)",

	TypeZabbixAck:    "Квитирование проблемы",
	TypeZabbixChange: "Изменение в Zabbix",
	TypeJenkinsBuild: "Запуск сборки (Jenkins)",

	TypeGrafanaDashboard:  "Правка дашборда (Grafana)",
	TypeGrafanaAnnotation: "Аннотация (Grafana)",

	TypeFigmaEdit:    "Правка файла (Figma)",
	TypeFigmaComment: "Комментарий (Figma)",
	TypeFigmaPublish: "Публикация библиотеки (Figma)",

	TypeNetSuiteChange: "Изменение записи (NetSuite)",
	TypeNetSuiteLogin:  "Вход в NetSuite",

	TypeClaudeUsage: "Использование Claude Code",
}

// TypeLabels отдаёт словарь подписей во фронтенд.
func TypeLabels() map[string]string {
	out := make(map[string]string, len(typeLabels))
	for k, v := range typeLabels {
		out[string(k)] = v
	}
	return out
}

// Person — человек, чью активность собираем, со всеми его идентификаторами.
type Person struct {
	ID          int64  `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	JiraAccount string `json:"jira_account_id"`
	GitLabUser  string `json:"gitlab_username"`
	SlackUser   string `json:"slack_user_id"`
	GoogleEmail string `json:"google_email"`
	// Office — офис из HRDB (Limassol, London, Remote work, …). Определяет,
	// какой праздничный календарь применяется к матрице активности.
	Office string `json:"office"`
	// Team и Area — команда и направление (Area of Responsibility) из HRDB:
	// по ним фильтруется страница сравнения продуктивности.
	Team string `json:"team"`
	Area string `json:"area"`
	// Title — должность из HRDB (не хранится в БД; обогащается из HRDB-кэша
	// при отдаче списка людей).
	Title string `json:"title,omitempty"`
	// Grade — грейд из HRDB (не хранится в БД; обогащается вместе с Title).
	Grade string `json:"grade,omitempty"`
	// HireDate — дата найма из HRDB: дни до неё не считаются рабочими,
	// у сотрудника не может быть активности до найма.
	HireDate *time.Time `json:"hire_date,omitempty"`
	// HybridDays — дни недели работы из дома из HRDB («Wednesday, Friday»).
	HybridDays string    `json:"hybrid_days"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PersonMetrics — метрики одного человека для сравнения продуктивности.
// Разбивки по типам достаточно, чтобы фронтенд собрал любые производные
// метрики (коммиты, MR, ревью и т.п.) без изменения API.
type PersonMetrics struct {
	Person            Person         `json:"person"`
	TotalEvents       int            `json:"total_events"`
	ActiveDays        int            `json:"active_days"`
	WorkingDays       int            `json:"working_days"`
	ActiveWorkingDays int            `json:"active_working_days"`
	IdleWorkingDays   int            `json:"idle_working_days"`
	VacationDays      int            `json:"vacation_days"`
	SickDays          int            `json:"sick_days"`
	WorklogHours      float64        `json:"worklog_hours"`
	MeetingHours      float64        `json:"meeting_hours"`
	LinesChanged      int64          `json:"lines_changed"`
	BySource          map[string]int `json:"by_source"`
	ByType            map[string]int `json:"by_type"`
	// Timeline — события по корзинам времени для спарклайна «волн активности».
	Timeline []SparkPoint `json:"timeline"`
}

// SparkPoint — точка спарклайна: корзина времени и число событий.
type SparkPoint struct {
	Bucket time.Time `json:"bucket"`
	Count  int       `json:"count"`
}

// WeightedPoint — точка средневзвешенного ряда команды: событий на одного
// присутствующего сотрудника (Presence — человеко-дни присутствия в корзине).
type WeightedPoint struct {
	Bucket   time.Time `json:"bucket"`
	Events   int       `json:"events"`
	Presence int       `json:"presence"`
	Value    float64   `json:"value"`
}

// TeamMetrics — агрегированные метрики команды для сравнения команд.
type TeamMetrics struct {
	Team        string `json:"team"`
	Members     int    `json:"members"`
	TotalEvents int    `json:"total_events"`
	// PersonDays — человеко-дни присутствия: рабочие дни без отпуска,
	// больничного и праздников, начиная с даты найма.
	PersonDays int `json:"person_days"`
	// EventsPerPersonDay — средневзвешенный показатель: события на один
	// человеко-день присутствия.
	EventsPerPersonDay float64         `json:"events_per_person_day"`
	BySource           map[string]int  `json:"by_source"`
	Timeline           []SparkPoint    `json:"timeline"`
	Weighted           []WeightedPoint `json:"weighted"`
}

// PersonDay — особый день человека: государственный праздник или отпуск.
// Выходные (суббота/воскресенье) вычисляются на лету и в БД не хранятся.
type PersonDay struct {
	PersonKey string    `json:"person_key"`
	Day       time.Time `json:"day"`
	Kind      string    `json:"kind"` // DayHoliday | DayVacation
	Label     string    `json:"label,omitempty"`
}

const (
	DayHoliday  = "holiday"
	DayVacation = "vacation"
	DaySick     = "sick"
	// DayRemote — день работы из дома по заявке VAC (Paid: Work From Home).
	// Рабочий день, но отдельная пометка на календаре.
	DayRemote = "remote"
	// DayOvertime — день заведённого овертайма (VAC Paid: Overtime). Хранится
	// в person_days, но на матрице это отдельный маркер (точка), а не «вид дня».
	DayOvertime = "overtime"
)

// CountryForOffice сопоставляет офис из HRDB со страной праздничного
// календаря: Remote work — российские праздники, Limassol/Paphos — кипрские,
// London — британские, Malaga — испанские. Пусто — офис не распознан.
func CountryForOffice(office string) string {
	o := strings.ToLower(office)
	switch {
	case strings.Contains(o, "remote"):
		return "RU"
	case strings.Contains(o, "limassol"), strings.Contains(o, "paphos"):
		return "CY"
	case strings.Contains(o, "london"):
		return "GB"
	case strings.Contains(o, "malaga"), strings.Contains(o, "málaga"):
		return "ES"
	case strings.Contains(o, "tbilisi"):
		return "GE"
	}
	return ""
}

// ParseWeekdays разбирает список дней недели («Wednesday, Friday» или
// русские названия) в множество time.Weekday.
func ParseWeekdays(s string) map[time.Weekday]bool {
	names := map[string]time.Weekday{
		"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
		"понедельник": time.Monday, "вторник": time.Tuesday, "среда": time.Wednesday,
		"четверг": time.Thursday, "пятница": time.Friday, "суббота": time.Saturday, "воскресенье": time.Sunday,
	}
	out := map[time.Weekday]bool{}
	for _, part := range strings.Split(s, ",") {
		if wd, ok := names[strings.ToLower(strings.TrimSpace(part))]; ok {
			out[wd] = true
		}
	}
	return out
}

// HybridChange — одно изменение атрибута «Hybrid remote days» в HRDB:
// в момент At шаблон гибридных дней сменился с Old на New. Источник —
// журнал изменений объекта Assets.
type HybridChange struct {
	At  time.Time `json:"at"`
	Old string    `json:"old"`
	New string    `json:"new"`
}

// HybridDaysAt возвращает шаблон гибридных дней, действовавший на дату at.
// История должна быть отсортирована по времени; current — текущее значение
// из HRDB, используется когда истории нет.
func HybridDaysAt(history []HybridChange, current string, at time.Time) string {
	if len(history) == 0 {
		return current
	}
	if at.Before(history[0].At) {
		return history[0].Old
	}
	out := history[0].New
	for _, h := range history[1:] {
		if at.Before(h.At) {
			break
		}
		out = h.New
	}
	return out
}

// MergeHybridTimeline объединяет две хронологии гибридных дней в одну:
// assets — журнал изменений объекта HRDB (есть старые значения, момент —
// время применения), tickets — одобренные заявки HCM (момент — дата
// вступления в силу, точнее журнала). Точки с одинаковым набором дней подряд
// схлопываются в самую раннюю — заявка и её применение в HRDB не дублируются.
func MergeHybridTimeline(assets, tickets []HybridChange) []HybridChange {
	return mergeTimeline(assets, tickets, weekdaySetKey)
}

// MergeWorkFormatTimeline — та же склейка для формата работы
// (Office | Hybrid | Remote): значения сравниваются нормализованно.
func MergeWorkFormatTimeline(assets, tickets []HybridChange) []HybridChange {
	return mergeTimeline(assets, tickets, NormalizeWorkFormat)
}

// NormalizeWorkFormat приводит формат работы к канону: remote | hybrid |
// office | "" (неизвестно). Строки HRDB/HCM разнятся («Remote work», «Remote»).
func NormalizeWorkFormat(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(s, "remote"):
		return "remote"
	case strings.Contains(s, "hybrid") || strings.Contains(s, "гибрид"):
		return "hybrid"
	case strings.Contains(s, "office") || strings.Contains(s, "офис"):
		return "office"
	}
	return ""
}

// weekdaySetKey — канонический ключ набора дней недели для сравнения
// («Wednesday, Friday» == «Friday, Wednesday»).
func weekdaySetKey(s string) string {
	set := ParseWeekdays(s)
	nums := make([]int, 0, len(set))
	for wd := range set {
		nums = append(nums, int(wd))
	}
	sort.Ints(nums)
	return fmt.Sprint(nums)
}

func mergeTimeline(assets, tickets []HybridChange, key func(string) string) []HybridChange {
	all := make([]HybridChange, 0, len(assets)+len(tickets))
	all = append(all, assets...)
	all = append(all, tickets...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })

	var out []HybridChange
	for _, h := range all {
		if len(out) > 0 && key(out[len(out)-1].New) == key(h.New) {
			continue
		}
		out = append(out, h)
	}
	// Old восстанавливаем цепочкой: до первой точки действовало старое
	// значение из журнала Assets (если он есть).
	firstOld := ""
	if len(assets) > 0 {
		firstOld = assets[0].Old
	}
	for i := range out {
		if i == 0 {
			out[i].Old = firstOld
			continue
		}
		out[i].Old = out[i-1].New
	}
	return out
}

// Event — одно атомарное действие пользователя в одной из систем.
type Event struct {
	// ID — детерминированный хеш (source + external_id), чтобы повторный сбор
	// того же периода не плодил дубли.
	ID         string    `json:"id"`
	PersonKey  string    `json:"person_key"`
	Source     Source    `json:"source"`
	Type       EventType `json:"type"`
	ExternalID string    `json:"external_id"`
	OccurredAt time.Time `json:"occurred_at"`

	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`

	// Project — проект/репозиторий/канал/папка, в котором произошло событие.
	Project     string `json:"project,omitempty"`
	ProjectName string `json:"project_name,omitempty"`

	// RefID — ключ сущности внутри источника: JIRA-123, !42, sha, ts, doc id.
	RefID string `json:"ref_id,omitempty"`
	// ParentRefID — связь с родительской сущностью. Для Google-дока, найденного
	// по ссылке в задаче, здесь лежит ключ этой задачи (TSD-связка).
	ParentRefID string `json:"parent_ref_id,omitempty"`

	// Effort — числовая мера объёма работы в единицах, зависящих от типа:
	// секунды для ворклогов, число изменённых строк для коммитов, символы для
	// правок документа. 0 — если мера неприменима.
	Effort float64 `json:"effort"`
	// EffortUnit — "seconds" | "lines" | "chars" | "".
	EffortUnit string `json:"effort_unit,omitempty"`

	Meta map[string]any `json:"meta,omitempty"`
}

// ComputeID проставляет детерминированный идентификатор события.
func (e *Event) ComputeID() {
	h := sha1.New()
	h.Write([]byte(string(e.Source)))
	h.Write([]byte{0})
	h.Write([]byte(string(e.Type)))
	h.Write([]byte{0})
	h.Write([]byte(e.PersonKey))
	h.Write([]byte{0})
	h.Write([]byte(e.ExternalID))
	e.ID = hex.EncodeToString(h.Sum(nil))
}

// Normalize приводит событие к консистентному виду перед записью в БД.
func (e *Event) Normalize() {
	if e.Source == "" {
		e.Source = e.Type.SourceOf()
	}
	e.OccurredAt = e.OccurredAt.UTC()
	e.Title = truncate(collapseSpace(e.Title), 500)
	e.Body = truncate(e.Body, 4000)
	if e.ExternalID == "" {
		e.ExternalID = e.RefID + "@" + e.OccurredAt.Format(time.RFC3339Nano)
	}
	if e.ID == "" {
		e.ComputeID()
	}
}

// DocLink — ссылка на Google-документ, найденная в задаче Jira (TSD).
type DocLink struct {
	DocID        string    `json:"doc_id"`
	DocURL       string    `json:"doc_url"`
	Title        string    `json:"title"`
	IssueKey     string    `json:"issue_key"`
	IssueTitle   string    `json:"issue_title,omitempty"`
	PersonKey    string    `json:"person_key"`
	FoundIn      string    `json:"found_in"` // description | comment | remotelink | summary
	DiscoveredAt time.Time `json:"discovered_at"`

	// Заполняется коллектором Google Docs после обогащения.
	Enriched     bool       `json:"enriched"`
	LastModified *time.Time `json:"last_modified,omitempty"`
	EditCount    int        `json:"edit_count"`
	CommentCount int        `json:"comment_count"`
}

// SyncStatus — состояние прогона сбора.
type SyncStatus string

const (
	SyncPending SyncStatus = "pending"
	SyncRunning SyncStatus = "running"
	SyncDone    SyncStatus = "done"
	SyncFailed  SyncStatus = "failed"
	SyncPartial SyncStatus = "partial"
)

// SyncRun — один запуск сбора данных.
type SyncRun struct {
	ID         string               `json:"id"`
	PersonKey  string               `json:"person_key"`
	From       time.Time            `json:"from"`
	To         time.Time            `json:"to"`
	Status     SyncStatus           `json:"status"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`
	Sources    map[string]SourceRun `json:"sources"`
	Error      string               `json:"error,omitempty"`
}

// SourceRun — результат сбора по одному источнику.
type SourceRun struct {
	Source     string     `json:"source"`
	Status     SyncStatus `json:"status"`
	Events     int        `json:"events"`
	DurationMS int64      `json:"duration_ms"`
	Error      string     `json:"error,omitempty"`
	Note       string     `json:"note,omitempty"`
	// Grouped — источник закрывается групповой фазой массового сбора,
	// идущей параллельно per-person прогону; execute() его не трогает.
	Grouped bool `json:"grouped,omitempty"`
}

// ---- Аналитические структуры, отдаваемые в API ----

// Summary — сводка за период.
type Summary struct {
	PersonKey    string      `json:"person_key"`
	From         time.Time   `json:"from"`
	To           time.Time   `json:"to"`
	TotalEvents  int         `json:"total_events"`
	ActiveDays   int         `json:"active_days"`
	SpanDays     int         `json:"span_days"`
	BySource     []CountItem `json:"by_source"`
	TopProjects  []CountItem `json:"top_projects"`
	Highlights   []Highlight `json:"highlights"`
	WorklogHours float64     `json:"worklog_hours"`
	MeetingHours float64     `json:"meeting_hours"`
	LinesChanged int64       `json:"lines_changed"`
	FirstEventAt *time.Time  `json:"first_event_at,omitempty"`
	LastEventAt  *time.Time  `json:"last_event_at,omitempty"`
	PrevTotal    int         `json:"prev_total"`
	PrevBySource []CountItem `json:"prev_by_source"`

	// Разбивка дней периода: приоритет отпуск > праздник > выходной > рабочий.
	WorkingDays       int `json:"working_days"`
	ActiveWorkingDays int `json:"active_working_days"`
	// ActiveDayBuckets — рабочие дни с активностью по её объёму за день.
	ActiveDayBuckets []CountItem `json:"active_day_buckets"`
	WeekendDays      int         `json:"weekend_days"`
	HolidayDays      int         `json:"holiday_days"`
	VacationDays     int         `json:"vacation_days"`
	SickDays         int         `json:"sick_days"`
	IdleWorkingDays  int         `json:"idle_working_days"`
	Office           string      `json:"office,omitempty"`
	HolidayCountry   string      `json:"holiday_country,omitempty"`
}

// Highlight — ключевая метрика для карточки-плитки.
type Highlight struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Source   string  `json:"source,omitempty"`
	Delta    float64 `json:"delta"` // изменение относительно предыдущего периода, %
	HasDelta bool    `json:"has_delta"`
}

// CountItem — пара «ключ → количество» для разбивок.
type CountItem struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Count int     `json:"count"`
	Value float64 `json:"value,omitempty"`
}

// TimelinePoint — точка временного ряда: количество событий по источникам.
type TimelinePoint struct {
	Bucket time.Time      `json:"bucket"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// HeatCell — ячейка тепловой карты «день недели × час».
type HeatCell struct {
	Weekday int `json:"weekday"` // 0 = понедельник
	Hour    int `json:"hour"`
	Count   int `json:"count"`
}

// EventPage — страница ленты событий.
type EventPage struct {
	Items   []Event `json:"items"`
	Total   int     `json:"total"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
}

// EventFilter — фильтр выборки событий.
type EventFilter struct {
	PersonKey string
	From      time.Time
	To        time.Time
	Sources   []string
	Types     []string
	Projects  []string
	Query     string
	// MinAIScore отсекает сообщения Slack с AI-оценкой ниже порога (0 —
	// фильтр выключен). Неоценённые сообщения и другие источники проходят.
	MinAIScore float64
	Page       int
	PerPage    int
	SortDesc   bool
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
