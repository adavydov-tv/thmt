// Package config загружает всю конфигурацию приложения из переменных окружения.
//
// Единственное место в проекте, где читается ENV. Все токены доступа к внешним
// системам задаются только здесь — см. .env.example.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — корневая конфигурация сервиса.
type Config struct {
	Server     ServerConfig
	Postgres   PostgresConfig
	Jira       JiraConfig
	GitLab     GitLabConfig
	Slack      SlackConfig
	Google     GoogleConfig
	Allure     AllureConfig
	Confluence ConfluenceConfig
	GWork      GWorkConfig
	ArgoCD     ArgoCDConfig
	Zabbix     ZabbixConfig
	Jenkins    JenkinsConfig
	Grafana    GrafanaConfig
	Figma      FigmaConfig
	NetSuite   NetSuiteConfig
	Claude     ClaudeConfig
	Overtime   OvertimeConfig
	PerfReview PerfReviewConfig
	AI         AIConfig
	Auth       AuthConfig
	HRDB       HRDBConfig
	Sync       SyncConfig
	People     []PersonSeed
}

// AuthConfig — вход через Google OIDC и роли доступа.
type AuthConfig struct {
	// Enabled — включает аутентификацию; без неё все запросы идут от
	// встроенного администратора (локальная разработка).
	Enabled      bool
	ClientID     string
	ClientSecret string
	// PublicURL — внешний адрес приложения для redirect_uri Google.
	PublicURL string
	// Domain — допустимый Workspace-домен (пусто — любой Google-аккаунт).
	Domain string
	// AdminEmails — bootstrap-администраторы.
	AdminEmails []string
	// CookieSecret — ключ подписи сессионной куки (случайная строка 32+).
	CookieSecret string
	SessionTTL   time.Duration
}

// ServerConfig — параметры HTTP-сервера.
type ServerConfig struct {
	Addr        string
	CORSOrigins []string
	StaticDir   string
	LogLevel    string
}

// PostgresConfig — подключение к БД.
type PostgresConfig struct {
	DSN            string
	MaxConns       int32
	MigrateOnStart bool
	ConnectTimeout time.Duration
	StatementDebug bool
}

// JiraConfig — Jira Cloud, Basic auth (email + API token).
//
// Токен создаётся тут: https://id.atlassian.com/manage-profile/security/api-tokens
type JiraConfig struct {
	Enabled  bool
	BaseURL  string // https://<site>.atlassian.net
	Email    string
	APIToken string
	// Projects ограничивает JQL списком проектов. Пусто — все доступные.
	Projects []string
	PageSize int
	// FetchChangelog включает выгрузку истории переходов статусов (дороже по API).
	FetchChangelog bool
	// FetchWorklogs включает выгрузку ворклогов.
	FetchWorklogs bool
}

// GitLabConfig — self-hosted GitLab, Personal Access Token.
type GitLabConfig struct {
	Enabled  bool
	BaseURL  string // https://gitlab.example.com
	Token    string
	PageSize int
	// SkipTLSVerify — для инсталляций с самоподписанным сертификатом.
	SkipTLSVerify bool
	// MaxProjects ограничивает число проектов, по которым идёт добор коммитов.
	MaxProjects int
}

// SlackConfig — Slack API.
//
// UserToken (xoxp-...) нужен для search.messages. BotToken (xoxb-...) умеет
// conversations.history/replies, но не умеет search. Достаточно любого из двух,
// набор собираемых данных зависит от того, что задано.
type SlackConfig struct {
	Enabled   bool
	UserToken string
	BotToken  string
	// Channels ограничивает обход конкретными каналами (id или имена).
	Channels []string
	// TextKey — base64-ключ AES-256 для шифрования текстов архива Slack.
	TextKey string
	// IncludeReactions включает сбор реакций, поставленных пользователем.
	IncludeReactions bool
	PageSize         int
	// RescanDays — «хвост» периода (в днях), который переобходится даже при
	// полном покрытии архивом: свежие треды пополняются ответами и реакциями
	// уже после первого обхода.
	RescanDays int
}

// GoogleAuthMode определяет способ авторизации в Google API.
type GoogleAuthMode string

const (
	// GoogleAuthServiceAccount — JSON-ключ сервис-аккаунта (+ опциональный
	// domain-wide delegation через GOOGLE_IMPERSONATE_SUBJECT).
	GoogleAuthServiceAccount GoogleAuthMode = "service_account"
	// GoogleAuthOAuth — client id/secret + refresh token пользователя.
	GoogleAuthOAuth GoogleAuthMode = "oauth"
)

// GoogleConfig — Google Drive / Docs / Drive Activity.
type GoogleConfig struct {
	Enabled bool
	Mode    GoogleAuthMode

	// service_account
	CredentialsFile    string
	CredentialsJSON    string
	ImpersonateSubject string

	// oauth
	ClientID     string
	ClientSecret string
	RefreshToken string

	// ScanDrive=true — обходить не только доки, найденные по ссылкам в Jira,
	// но и всё, к чему есть доступ, через Drive Activity API.
	ScanDrive bool
	// DriveFolderIDs ограничивает скан конкретными папками.
	DriveFolderIDs []string
	// MaxDocs — потолок числа документов при расширенном поиске: по каждому
	// найденному документу идёт отдельный запрос активности.
	MaxDocs  int
	PageSize int

	// CalendarEnabled включает источник «gcal» — участие во встречах Google
	// Calendar. Использует те же учётные данные Google, но отдельный scope
	// calendar.readonly, поэтому выключается независимо от gdocs.
	CalendarEnabled bool
	// CalendarID — чей календарь читать. Пусто — календарь самого сотрудника
	// (по его google_email); требует, чтобы календарь был виден учётке,
	// которой ходим в API (или domain-wide delegation).
	CalendarID string
	// InterviewMarkers — подстроки в названии встречи, по которым событие
	// классифицируется как собеседование, а не обычная встреча.
	InterviewMarkers []string
	// VacationMarkers — подстроки в названии целодневного события, по которым
	// оно распознаётся как отпуск («Konstantin Homutov - Vacation»).
	VacationMarkers []string
	// SickMarkers — подстроки для больничных («… - Sick leave»).
	SickMarkers []string
}

// AllureConfig — Allure TestOps: запуски тестов, тест-кейсы и дефекты.
//
// Token — API-токен пользователя (профиль → API tokens). Новые версии TestOps
// принимают его напрямую в заголовке Api-Token; для старых коллектор сам
// обменивает его на JWT через /api/uaa/oauth/token.
type AllureConfig struct {
	Enabled bool
	BaseURL string // https://allure.example.com
	Token   string
	// Projects ограничивает обход проектами (id или имя). Пусто — все доступные.
	Projects []string
	PageSize int
	// MaxPages — потолок страниц пагинации на сущность в одном проекте.
	MaxPages int
}

// ConfluenceConfig — Confluence Cloud: страницы, комментарии, посты блога.
// Живёт в том же Atlassian-тенанте, что и Jira, поэтому URL и учётные данные
// по умолчанию выводятся из JiraConfig.
type ConfluenceConfig struct {
	Enabled  bool
	BaseURL  string // https://<site>.atlassian.net/wiki
	Email    string
	APIToken string
	// Spaces ограничивает сбор пространствами (ключи). Пусто — все доступные.
	Spaces   []string
	PageSize int
}

// GWorkConfig — аудит Google Workspace (Admin SDK Reports API). Требует
// админ-доступа: сервис-аккаунт с domain-wide delegation на reports-скоупы
// ЛИБО OAuth-токен админа. Использует те же учётные данные Google, что gdocs.
// Выключен по умолчанию — включается после выдачи нового скоупа и токена.
type GWorkConfig struct {
	Enabled bool
	// AuthMode переопределяет способ авторизации Google только для gwork
	// (пусто — общий GOOGLE_AUTH_MODE). Нужен, когда gdocs/gcal ходят по
	// oauth, а reports-скоупы делегированы только сервис-аккаунту.
	AuthMode GoogleAuthMode
	// Модули аудита (каждый — отдельный applicationName Reports API).
	Meet     bool // присутствие на встречах Meet (meet audit)
	Drive    bool // просмотры/скачивания/правки Drive (drive audit)
	Login    bool // логины (login audit)
	Gmail    bool // счётчики отправленных писем (usage report)
	Device   bool // активность устройств (mobile/user usage)
	Calendar bool // действия с календарём: создание/правка/удаление встреч (calendar audit)
	// OfficeCIDRs — офисные подсети (CIDR): логин из них считается «из офиса».
	// Правится в общих настройках, ENV задаёт значение по умолчанию.
	OfficeCIDRs []string
	PageSize    int
}

// ArgoCDConfig — деплои Argo CD. Токен: argocd account generate-token
// (или Settings → Accounts). Прод за SSO-прокси (нужен VPN/внутренний DNS),
// staging обычно доступен напрямую. Автосинки (initiatedBy.automated) не
// считаются активностью человека — берём только ручные синки.
// ArgoInstance — один инстанс Argo CD (среда prod/staging/…).
type ArgoInstance struct {
	Env      string // метка среды (prod, staging)
	BaseURL  string
	Token    string
	Insecure bool
}

type ArgoCDConfig struct {
	Enabled   bool
	Instances []ArgoInstance
}

// ZabbixConfig — работа с мониторингом Zabbix (JSON-RPC api_jsonrpc.php).
// Аутентификация — API-токен (Zabbix 5.4+) в заголовке Authorization: Bearer.
// Прод за SSO-прокси (нужен VPN). Собирается через event.get (квитирование
// проблем с автором) и auditlog.get (изменения по пользователю).
// ZabbixInstance — один инстанс Zabbix (среда).
type ZabbixInstance struct {
	Env      string
	BaseURL  string // без /api_jsonrpc.php
	Token    string
	Insecure bool
}

type ZabbixConfig struct {
	Enabled   bool
	Instances []ZabbixInstance
}

// JenkinsConfig — запуски сборок Jenkins. Аутентификация — user + API token
// (Basic). Кто запустил — из actions[].causes[].userId сборки.
// JenkinsInstance — один инстанс Jenkins (среда).
type JenkinsInstance struct {
	Env      string
	BaseURL  string
	User     string
	Token    string
	Insecure bool
}

type JenkinsConfig struct {
	Enabled   bool
	Instances []JenkinsInstance
	// MaxJobs / BuildsPerJob ограничивают обход (Jenkins бывает огромным).
	MaxJobs      int
	BuildsPerJob int
}

// GrafanaInstance — один инстанс Grafana (среда). Токен — service account
// token (Bearer). Публичные хосты за OAuth-прокси: нужен VPN.
type GrafanaInstance struct {
	Env      string
	BaseURL  string
	Token    string
	Insecure bool
}

// GrafanaConfig — правки дашбордов и аннотации. Атрибуция по createdBy/login.
type GrafanaConfig struct {
	Enabled       bool
	Instances     []GrafanaInstance
	MaxDashboards int // потолок обхода дашбордов
	VersionsPer   int // сколько версий на дашборд смотреть
}

// FigmaConfig — org-wide активность в Figma через Activity Logs API. Один
// SaaS-инстанс (без сред). Токен — personal access token от org-админа со
// скоупом org:activity_log_read. Атрибуция по actor.email.
type FigmaConfig struct {
	Enabled  bool
	Token    string
	BaseURL  string
	MaxPages int // потолок страниц пагинации Activity Logs за прогон
}

// NetSuiteConfig — аудит действий в NetSuite ERP через SuiteQL (REST).
// Аутентификация — Token-Based Authentication (OAuth 1.0a, HMAC-SHA256):
// четыре секрета от одного read-only integration+token. AccountID — realm
// (напр. 1234567 или 1234567_SB1), из него строится хост suitetalk.
type NetSuiteConfig struct {
	Enabled        bool
	AccountID      string
	ConsumerKey    string
	ConsumerSecret string
	TokenID        string
	TokenSecret    string
	PageSize       int // размер страницы SuiteQL (макс. 1000)
}

// ClaudeConfig — использование Claude Code, выгружаемое в S3-бакет.
// Каждый объект пути claude-usage/dt=YYYY-MM-DD/<host>.jsonl — JSON-запись по
// одному человеку/машине за день (report.daily[]: запросы и токены по дням).
// Доступ к S3 — AWS Signature V4 (read-only ключ). Для S3-совместимого
// хранилища (MinIO) задаётся Endpoint (path-style); для AWS оставить пустым.
// Атрибуция по e-mail из записи.
type ClaudeConfig struct {
	Enabled      bool
	Bucket       string
	Prefix       string // префикс ключей, по умолчанию "claude-usage/"
	Region       string // регион AWS (для подписи), по умолчанию us-east-1
	Endpoint     string // необязательный endpoint S3-совместимого хранилища
	AccessKey    string
	SecretKey    string
	SessionToken string // необязательный токен временных креденшелов STS
	MaxDays      int    // потолок числа дней (dt-партиций) за один прогон
}

// OvertimeConfig — заявки на оплачиваемые овертаймы из Jira Server/DC
// (jira.xtools.tv, тикеты VAC-* с Request type «Paid: Overtime»).
type OvertimeConfig struct {
	Enabled bool
	BaseURL string
	// Token — Personal Access Token Jira DC (Bearer).
	Token string
	// JQL — базовый запрос заявок; период дописывается автоматически.
	JQL string
	// EmployeeField — имя поля с сотрудником. StartDateField/EndDateField —
	// имена полей с датами овертайма (диапазон может быть многодневным);
	// если дат в заявке нет, берётся дата создания.
	EmployeeField  string
	StartDateField string
	EndDateField   string
	PageSize       int
}

// PerfReviewConfig — оценки Performance Review и планы PIP/PDP из Jira DC
// (проект PR): тикеты «Final review» / «Line manager review» с полем «Rate
// overall impact» и «Personal Development Plan» с полем Type. По умолчанию —
// тот же инстанс и токен, что у овертаймов (OVERTIME_JIRA_*).
type PerfReviewConfig struct {
	Enabled bool
	BaseURL string
	Token   string
	// JQL — базовый запрос тикетов оценок и планов развития.
	JQL      string
	PageSize int
}

// AIConfig — локальный AI-скорер сообщений (docker-контейнер ai-scorer):
// оценивает содержательность сообщений Slack от 0 до 10.
type AIConfig struct {
	Enabled bool
	BaseURL string
}

// HRDBConfig — HRDB на базе Atlassian Assets (JSM Insight): объект «Employee»
// хранит сотрудников с рабочими e-mail. Авторизация — те же JIRA_EMAIL и
// JIRA_API_TOKEN, что и у Jira-коллектора: Assets живёт в том же тенанте.
type HRDBConfig struct {
	Enabled bool
	// WorkspaceID — id workspace Assets. Берётся из
	// GET https://<site>.atlassian.net/rest/servicedeskapi/assets/workspace
	WorkspaceID string
	// EmployeeTypeID — objectTypeId типа «Employee» в схеме HRDB (по нему
	// резолвятся атрибуты; типы делят их id).
	EmployeeTypeID string
	// EmployeeTypes — имена типов, считающихся действующими сотрудниками.
	EmployeeTypes []string
	// NameAttrID / EmailAttrID / OfficeAttrID — переопределение id атрибутов;
	// пусто — атрибуты находятся по имени («Display Name» / «Work Email» /
	// «Office»).
	NameAttrID   string
	EmailAttrID  string
	OfficeAttrID string
	TeamAttrID   string
	AreaAttrID   string
	HireAttrID   string
	// ExEmployeeTypes — имена object type с бывшими: в иерархии Person это
	// Ex-employee и Ex-contractor. HRDB переносит
	// уволенных туда, и по этому списку они скрываются из сравнения.
	ExEmployeeTypes []string
	// CacheTTL — сколько держать список сотрудников в памяти.
	CacheTTL time.Duration
	PageSize int

	// Mode — какая HRDB активна по умолчанию: cloud (новая, Atlassian Cloud
	// Assets) или dc (старая, Insight на jira.xtools.tv). Выбор из UI
	// сохраняется в БД и имеет приоритет над этим значением.
	Mode string
	// DCURL / DCToken — адрес и Bearer-токен (PAT) старой HRDB; токен по
	// умолчанию берётся из OVERTIME_JIRA_TOKEN — это тот же Jira DC.
	DCURL   string
	DCToken string
	// DCEmployeeTypeID — objectTypeId типа «Employee» в схеме HRDB старой Jira.
	DCEmployeeTypeID string
}

// SyncConfig — параметры оркестратора сбора.
type SyncConfig struct {
	// Concurrency — сколько коллекторов бежит параллельно внутри одного прогона.
	Concurrency int
	// PeopleConcurrency — сколько людей собирается одновременно при сборе
	// команды/кластера (StartMany). 1 — строго последовательно, как раньше.
	PeopleConcurrency int
	// HTTPTimeout — таймаут одного запроса к внешнему API.
	HTTPTimeout time.Duration
	// RunTimeout — общий таймаут одного прогона сбора.
	RunTimeout time.Duration
	// MaxRetries — число повторов при 429/5xx.
	MaxRetries int
	// DefaultLookback — период по умолчанию, если не задан в запросе.
	DefaultLookback time.Duration
	// ReprobeDays — сколько дней с хвоста уже собранного перепроверять при
	// инкрементальном сборе (страховка от поздних событий). Дефолт для всех
	// систем; на каждую систему переопределяется в настройках UI (reprobe_days).
	ReprobeDays int
}

// PersonSeed — сопоставление одного человека с его идентификаторами в системах.
// Задаётся через ENV ACTIVITY_PEOPLE (JSON-массив) и/или правится через API.
type PersonSeed struct {
	Key         string `json:"key"`          // короткий уникальный ключ, напр. "adavydov"
	DisplayName string `json:"display_name"` // "Aleksei Davydov"
	Email       string `json:"email"`
	JiraAccount string `json:"jira_account_id"` // accountId в Jira Cloud
	GitLabUser  string `json:"gitlab_username"`
	SlackUser   string `json:"slack_user_id"` // Uxxxxxxxx
	GoogleEmail string `json:"google_email"`
}

// Load читает конфигурацию из окружения. Файл .env, если он есть рядом с
// бинарником или по пути ENV_FILE, подгружается до чтения переменных;
// уже выставленные в окружении значения имеют приоритет.
func Load() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}

	cfg := &Config{
		Server: ServerConfig{
			Addr:        env("SERVER_ADDR", ":8080"),
			CORSOrigins: envList("CORS_ALLOWED_ORIGINS", []string{"http://localhost:5173"}),
			StaticDir:   env("STATIC_DIR", "web/dist"),
			LogLevel:    env("LOG_LEVEL", "info"),
		},
		Postgres: PostgresConfig{
			DSN:            env("POSTGRES_DSN", "postgres://activity:activity@localhost:5432/activity?sslmode=disable"),
			MaxConns:       int32(envInt("POSTGRES_MAX_CONNS", 8)),
			MigrateOnStart: envBool("POSTGRES_MIGRATE_ON_START", true),
			ConnectTimeout: envDuration("POSTGRES_CONNECT_TIMEOUT", 10*time.Second),
			StatementDebug: envBool("POSTGRES_DEBUG", false),
		},
		Jira: JiraConfig{
			Enabled:        envBool("JIRA_ENABLED", true),
			BaseURL:        strings.TrimRight(env("JIRA_BASE_URL", ""), "/"),
			Email:          env("JIRA_EMAIL", ""),
			APIToken:       env("JIRA_API_TOKEN", ""),
			Projects:       envList("JIRA_PROJECTS", nil),
			PageSize:       envInt("JIRA_PAGE_SIZE", 100),
			FetchChangelog: envBool("JIRA_FETCH_CHANGELOG", true),
			FetchWorklogs:  envBool("JIRA_FETCH_WORKLOGS", true),
		},
		GitLab: GitLabConfig{
			Enabled:       envBool("GITLAB_ENABLED", true),
			BaseURL:       strings.TrimRight(env("GITLAB_BASE_URL", ""), "/"),
			Token:         env("GITLAB_TOKEN", ""),
			PageSize:      envInt("GITLAB_PAGE_SIZE", 100),
			SkipTLSVerify: envBool("GITLAB_SKIP_TLS_VERIFY", false),
			MaxProjects:   envInt("GITLAB_MAX_PROJECTS", 200),
		},
		Slack: SlackConfig{
			Enabled:          envBool("SLACK_ENABLED", true),
			UserToken:        env("SLACK_USER_TOKEN", ""),
			BotToken:         env("SLACK_BOT_TOKEN", ""),
			Channels:         envList("SLACK_CHANNELS", nil),
			TextKey:          env("SLACK_TEXT_KEY", ""),
			IncludeReactions: envBool("SLACK_INCLUDE_REACTIONS", true),
			PageSize:         envInt("SLACK_PAGE_SIZE", 200),
			RescanDays:       envInt("SLACK_RESCAN_DAYS", 7),
		},
		Google: GoogleConfig{
			Enabled:            envBool("GOOGLE_ENABLED", true),
			Mode:               GoogleAuthMode(env("GOOGLE_AUTH_MODE", string(GoogleAuthServiceAccount))),
			CredentialsFile:    env("GOOGLE_CREDENTIALS_FILE", ""),
			CredentialsJSON:    env("GOOGLE_CREDENTIALS_JSON", ""),
			ImpersonateSubject: env("GOOGLE_IMPERSONATE_SUBJECT", ""),
			ClientID:           env("GOOGLE_OAUTH_CLIENT_ID", ""),
			ClientSecret:       env("GOOGLE_OAUTH_CLIENT_SECRET", ""),
			RefreshToken:       env("GOOGLE_OAUTH_REFRESH_TOKEN", ""),
			ScanDrive:          envBool("GOOGLE_SCAN_DRIVE", false),
			DriveFolderIDs:     envList("GOOGLE_DRIVE_FOLDER_IDS", nil),
			MaxDocs:            envInt("GOOGLE_MAX_DOCS", 500),
			PageSize:           envInt("GOOGLE_PAGE_SIZE", 100),
			CalendarEnabled:    envBool("GCAL_ENABLED", true),
			CalendarID:         env("GCAL_CALENDAR_ID", ""),
			InterviewMarkers:   envList("GCAL_INTERVIEW_MARKERS", []string{"interview", "собеседование"}),
			VacationMarkers:    envList("GCAL_VACATION_MARKERS", []string{"vacation", "отпуск"}),
			SickMarkers:        envList("GCAL_SICK_MARKERS", []string{"sick leave", "sick day", "больничный"}),
		},
		Allure: AllureConfig{
			Enabled:  envBool("ALLURE_ENABLED", true),
			BaseURL:  strings.TrimRight(env("ALLURE_BASE_URL", ""), "/"),
			Token:    env("ALLURE_TOKEN", ""),
			Projects: envList("ALLURE_PROJECTS", nil),
			PageSize: envInt("ALLURE_PAGE_SIZE", 100),
			MaxPages: envInt("ALLURE_MAX_PAGES", 50),
		},
		Confluence: ConfluenceConfig{
			Enabled:  envBool("CONFLUENCE_ENABLED", true),
			BaseURL:  strings.TrimRight(env("CONFLUENCE_BASE_URL", ""), "/"),
			Email:    env("CONFLUENCE_EMAIL", ""),
			APIToken: env("CONFLUENCE_API_TOKEN", ""),
			Spaces:   envList("CONFLUENCE_SPACES", nil),
			PageSize: envInt("CONFLUENCE_PAGE_SIZE", 100),
		},
		GWork: GWorkConfig{
			Enabled:  envBool("GWORK_ENABLED", false),
			AuthMode: GoogleAuthMode(env("GWORK_AUTH_MODE", "")),
			Meet:     envBool("GWORK_MEET", true),
			Drive:    envBool("GWORK_DRIVE", true),
			Login:    envBool("GWORK_LOGIN", true),
			Gmail:    envBool("GWORK_GMAIL", true),
			// device_sync — пассивный сигнал (фоновая синхронизация телефона),
			// не действие человека: в общий счёт активности не по умолчанию.
			Device:      envBool("GWORK_DEVICE", false),
			Calendar:    envBool("GWORK_CALENDAR", true),
			OfficeCIDRs: envList("GWORK_OFFICE_CIDRS", nil),
			PageSize:    envInt("GWORK_PAGE_SIZE", 1000),
		},
		ArgoCD: ArgoCDConfig{
			Enabled:   envBool("ARGOCD_ENABLED", false),
			Instances: loadArgoInstances(),
		},
		Zabbix: ZabbixConfig{
			Enabled:   envBool("ZABBIX_ENABLED", false),
			Instances: loadZabbixInstances(),
		},
		Jenkins: JenkinsConfig{
			Enabled:      envBool("JENKINS_ENABLED", false),
			Instances:    loadJenkinsInstances(),
			MaxJobs:      envInt("JENKINS_MAX_JOBS", 300),
			BuildsPerJob: envInt("JENKINS_BUILDS_PER_JOB", 50),
		},
		Grafana: GrafanaConfig{
			Enabled:       envBool("GRAFANA_ENABLED", false),
			Instances:     loadGrafanaInstances(),
			MaxDashboards: envInt("GRAFANA_MAX_DASHBOARDS", 500),
			VersionsPer:   envInt("GRAFANA_VERSIONS_PER_DASH", 20),
		},
		Figma: FigmaConfig{
			Enabled:  envBool("FIGMA_ENABLED", false),
			Token:    env("FIGMA_TOKEN", ""),
			BaseURL:  strings.TrimRight(env("FIGMA_BASE_URL", "https://api.figma.com"), "/"),
			MaxPages: envInt("FIGMA_MAX_PAGES", 50),
		},
		NetSuite: NetSuiteConfig{
			Enabled:        envBool("NETSUITE_ENABLED", false),
			AccountID:      env("NETSUITE_ACCOUNT_ID", ""),
			ConsumerKey:    env("NETSUITE_CONSUMER_KEY", ""),
			ConsumerSecret: env("NETSUITE_CONSUMER_SECRET", ""),
			TokenID:        env("NETSUITE_TOKEN_ID", ""),
			TokenSecret:    env("NETSUITE_TOKEN_SECRET", ""),
			PageSize:       envInt("NETSUITE_PAGE_SIZE", 1000),
		},
		Claude: ClaudeConfig{
			Enabled:      envBool("CLAUDE_ENABLED", false),
			Bucket:       env("CLAUDE_S3_BUCKET", ""),
			Prefix:       env("CLAUDE_S3_PREFIX", "claude-usage/"),
			Region:       env("CLAUDE_S3_REGION", "us-east-1"),
			Endpoint:     strings.TrimRight(env("CLAUDE_S3_ENDPOINT", ""), "/"),
			AccessKey:    env("CLAUDE_S3_ACCESS_KEY_ID", ""),
			SecretKey:    env("CLAUDE_S3_SECRET_ACCESS_KEY", ""),
			SessionToken: env("CLAUDE_S3_SESSION_TOKEN", ""),
			MaxDays:      envInt("CLAUDE_MAX_DAYS", 120),
		},
		PerfReview: PerfReviewConfig{
			Enabled: envBool("PERF_REVIEW_ENABLED", true),
			BaseURL: strings.TrimRight(env("PERF_REVIEW_JIRA_URL", env("OVERTIME_JIRA_URL", "https://jira.xtools.tv")), "/"),
			Token:   env("PERF_REVIEW_JIRA_TOKEN", env("OVERTIME_JIRA_TOKEN", "")),
			JQL: env("PERF_REVIEW_JQL",
				`project = PR AND issuetype in ("Final review", "Line manager review", "Functional manager review", "Personal Development Plan")`),
			PageSize: envInt("PERF_REVIEW_PAGE_SIZE", 100),
		},
		Overtime: OvertimeConfig{
			Enabled: envBool("OVERTIME_ENABLED", true),
			BaseURL: strings.TrimRight(env("OVERTIME_JIRA_URL", "https://jira.xtools.tv"), "/"),
			Token:   env("OVERTIME_JIRA_TOKEN", ""),
			// Поле Request Type видно не всем токенам (права JSM), поэтому
			// по умолчанию фильтруем по summary — заявки называются
			// «Paid: Overtime request for <Name>». Учитываются только
			// одобренные заявки: resolution = Done.
			// Заявки VAC: завершённые (resolution = Done) плюс идущие сейчас
			// (In Progress — одобренный отпуск/WFH ещё не закончился, резолюции
			// нет). Тип (overtime/vacation/sick/remote) распознаётся по summary,
			// т.к. поле Request Type скрыто от токена.
			JQL:            env("OVERTIME_JQL", `project = VAC AND (resolution = Done OR status = "In Progress")`),
			EmployeeField:  env("OVERTIME_EMPLOYEE_FIELD", "Employee"),
			StartDateField: env("OVERTIME_START_FIELD", "Start Date"),
			EndDateField:   env("OVERTIME_END_FIELD", "End Date"),
			PageSize:       envInt("OVERTIME_PAGE_SIZE", 100),
		},
		Auth: AuthConfig{
			Enabled:      envBool("AUTH_ENABLED", false),
			ClientID:     env("AUTH_GOOGLE_CLIENT_ID", ""),
			ClientSecret: env("AUTH_GOOGLE_CLIENT_SECRET", ""),
			PublicURL:    env("AUTH_PUBLIC_URL", "http://localhost:8080"),
			Domain:       env("AUTH_ALLOWED_DOMAIN", "tradingview.com"),
			AdminEmails:  envList("ADMIN_EMAILS", nil),
			CookieSecret: env("AUTH_COOKIE_SECRET", ""),
			SessionTTL:   envDuration("AUTH_SESSION_TTL", 12*time.Hour),
		},
		AI: AIConfig{
			Enabled: envBool("AI_ENABLED", true),
			BaseURL: strings.TrimRight(env("AI_SCORER_URL", "http://localhost:8090"), "/"),
		},
		HRDB: HRDBConfig{
			Enabled:        envBool("HRDB_ENABLED", true),
			WorkspaceID:    env("HRDB_WORKSPACE_ID", ""),
			EmployeeTypeID: env("HRDB_EMPLOYEE_TYPE_ID", ""),
			EmployeeTypes:  envList("HRDB_EMPLOYEE_TYPES", []string{"Employee", "Contractor"}),
			NameAttrID:     env("HRDB_EMPLOYEE_NAME_ATTR", ""),
			EmailAttrID:    env("HRDB_EMPLOYEE_EMAIL_ATTR", ""),
			OfficeAttrID:   env("HRDB_EMPLOYEE_OFFICE_ATTR", ""),
			TeamAttrID:     env("HRDB_EMPLOYEE_TEAM_ATTR", ""),
			AreaAttrID:     env("HRDB_EMPLOYEE_AREA_ATTR", ""),
			HireAttrID:     env("HRDB_EMPLOYEE_HIRE_ATTR", ""),
			ExEmployeeTypes: envList("HRDB_EX_EMPLOYEE_TYPES",
				envList("HRDB_EX_EMPLOYEE_TYPE", []string{"Ex-employee", "Ex-contractor"})),
			Mode:             env("HRDB_MODE", "cloud"),
			DCURL:            env("HRDB_DC_URL", "https://jira.xtools.tv"),
			DCToken:          env("HRDB_DC_TOKEN", env("OVERTIME_JIRA_TOKEN", "")),
			DCEmployeeTypeID: env("HRDB_DC_EMPLOYEE_TYPE_ID", "41"),
			CacheTTL:         envDuration("HRDB_CACHE_TTL", 10*time.Minute),
			PageSize:         envInt("HRDB_PAGE_SIZE", 100),
		},
		Sync: SyncConfig{
			Concurrency:       envInt("SYNC_CONCURRENCY", 4),
			PeopleConcurrency: envInt("SYNC_PEOPLE_CONCURRENCY", 3),
			HTTPTimeout:       envDuration("SYNC_HTTP_TIMEOUT", 45*time.Second),
			RunTimeout:        envDuration("SYNC_RUN_TIMEOUT", 30*time.Minute),
			MaxRetries:        envInt("SYNC_MAX_RETRIES", 4),
			DefaultLookback:   envDuration("SYNC_DEFAULT_LOOKBACK", 30*24*time.Hour),
			ReprobeDays:       envInt("SYNC_REPROBE_DAYS", 2),
		},
	}

	people, err := parsePeople(env("ACTIVITY_PEOPLE", ""))
	if err != nil {
		return nil, fmt.Errorf("ACTIVITY_PEOPLE: %w", err)
	}
	cfg.People = people

	cfg.applyDisables()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDisables выключает источники, для которых не заданы обязательные секреты,
// чтобы сервис поднимался с частичной конфигурацией.
func (c *Config) applyDisables() {
	if c.Jira.BaseURL == "" || c.Jira.APIToken == "" || c.Jira.Email == "" {
		c.Jira.Enabled = false
	}
	if c.GitLab.BaseURL == "" || c.GitLab.Token == "" {
		c.GitLab.Enabled = false
	}
	if c.Slack.UserToken == "" && c.Slack.BotToken == "" {
		c.Slack.Enabled = false
	}
	if c.Allure.BaseURL == "" || c.Allure.Token == "" {
		c.Allure.Enabled = false
	}
	// Confluence по умолчанию наследует тенант и учётные данные Jira.
	if c.Confluence.BaseURL == "" && c.Jira.BaseURL != "" {
		c.Confluence.BaseURL = c.Jira.BaseURL + "/wiki"
	}
	if c.Confluence.Email == "" {
		c.Confluence.Email = c.Jira.Email
	}
	if c.Confluence.APIToken == "" {
		c.Confluence.APIToken = c.Jira.APIToken
	}
	if c.Confluence.BaseURL == "" || c.Confluence.Email == "" || c.Confluence.APIToken == "" {
		c.Confluence.Enabled = false
	}
	if c.AI.BaseURL == "" {
		c.AI.Enabled = false
	}
	if c.Overtime.BaseURL == "" || c.Overtime.Token == "" {
		c.Overtime.Enabled = false
	}
	// HRDB ходит в Assets под теми же учётными данными, что и Jira.
	if c.HRDB.WorkspaceID == "" || c.HRDB.EmployeeTypeID == "" ||
		c.Jira.Email == "" || c.Jira.APIToken == "" {
		c.HRDB.Enabled = false
	}
	switch c.Google.Mode {
	case GoogleAuthServiceAccount:
		if c.Google.CredentialsFile == "" && c.Google.CredentialsJSON == "" {
			c.Google.Enabled = false
		}
	case GoogleAuthOAuth:
		if c.Google.ClientID == "" || c.Google.ClientSecret == "" || c.Google.RefreshToken == "" {
			c.Google.Enabled = false
		}
	}
}

// Validate проверяет то, без чего сервис не запустится вовсе.
func (c *Config) Validate() error {
	var errs []string
	if c.Postgres.DSN == "" {
		errs = append(errs, "POSTGRES_DSN обязателен")
	}
	if c.Google.Mode != GoogleAuthServiceAccount && c.Google.Mode != GoogleAuthOAuth {
		errs = append(errs, fmt.Sprintf("GOOGLE_AUTH_MODE=%q: допустимы %q или %q",
			c.Google.Mode, GoogleAuthServiceAccount, GoogleAuthOAuth))
	}
	if m := c.GWork.AuthMode; m != "" && m != GoogleAuthServiceAccount && m != GoogleAuthOAuth {
		errs = append(errs, fmt.Sprintf("GWORK_AUTH_MODE=%q: допустимы %q, %q или пусто (наследовать GOOGLE_AUTH_MODE)",
			m, GoogleAuthServiceAccount, GoogleAuthOAuth))
	}
	if c.Sync.Concurrency < 1 {
		errs = append(errs, "SYNC_CONCURRENCY должен быть >= 1")
	}
	if c.Sync.PeopleConcurrency < 1 {
		errs = append(errs, "SYNC_PEOPLE_CONCURRENCY должен быть >= 1")
	}
	seen := map[string]bool{}
	for i, p := range c.People {
		if p.Key == "" {
			errs = append(errs, fmt.Sprintf("ACTIVITY_PEOPLE[%d]: пустой key", i))
			continue
		}
		if seen[p.Key] {
			errs = append(errs, fmt.Sprintf("ACTIVITY_PEOPLE: дублирующийся key %q", p.Key))
		}
		seen[p.Key] = true
	}
	if len(errs) > 0 {
		return errors.New("конфигурация невалидна:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}

// EnabledSources возвращает список включённых источников.
func (c *Config) EnabledSources() []string {
	var out []string
	if c.Jira.Enabled {
		out = append(out, "jira")
	}
	if c.GitLab.Enabled {
		out = append(out, "gitlab")
	}
	if c.Slack.Enabled {
		out = append(out, "slack")
	}
	if c.Google.Enabled {
		out = append(out, "gdocs")
	}
	if c.Google.Enabled && c.Google.CalendarEnabled {
		out = append(out, "gcal")
	}
	if c.Allure.Enabled {
		out = append(out, "allure")
	}
	if c.Confluence.Enabled {
		out = append(out, "confluence")
	}
	if c.GWork.Enabled {
		out = append(out, "gwork")
	}
	if c.ArgoCD.Enabled && len(c.ArgoCD.Instances) > 0 {
		out = append(out, "argocd")
	}
	if c.Zabbix.Enabled && len(c.Zabbix.Instances) > 0 {
		out = append(out, "zabbix")
	}
	if c.Jenkins.Enabled && len(c.Jenkins.Instances) > 0 {
		out = append(out, "jenkins")
	}
	if c.Figma.Enabled && c.Figma.Token != "" {
		out = append(out, "figma")
	}
	if c.Grafana.Enabled && len(c.Grafana.Instances) > 0 {
		out = append(out, "grafana")
	}
	if c.NetSuite.Enabled && c.NetSuite.AccountID != "" && c.NetSuite.TokenID != "" {
		out = append(out, "netsuite")
	}
	if c.Claude.Enabled && c.Claude.Bucket != "" && c.Claude.AccessKey != "" {
		out = append(out, "claude")
	}
	return out
}

// envNames возвращает список сред источника: {PREFIX}_ENVS (через запятую),
// либо ["prod"] по умолчанию (совместимость с одиночной конфигурацией).
func envNames(prefix string) []string {
	if list := envList(prefix+"_ENVS", nil); len(list) > 0 {
		return list
	}
	return []string{"prod"}
}

// perEnv читает переменную инстанса: сперва {PREFIX}_{ENV}_{KEY},
// затем — как фолбэк на одиночную конфигурацию — {PREFIX}_{KEY}.
func perEnv(prefix, envName, key string) string {
	up := strings.ToUpper(strings.TrimSpace(envName))
	if v := env(prefix+"_"+up+"_"+key, ""); v != "" {
		return v
	}
	return env(prefix+"_"+key, "")
}

func perEnvBool(prefix, envName, key string) bool {
	return strings.EqualFold(perEnv(prefix, envName, key), "true")
}

func loadArgoInstances() []ArgoInstance {
	var out []ArgoInstance
	for _, name := range envNames("ARGOCD") {
		url := strings.TrimRight(perEnv("ARGOCD", name, "URL"), "/")
		token := perEnv("ARGOCD", name, "TOKEN")
		if url == "" || token == "" {
			continue
		}
		out = append(out, ArgoInstance{Env: name, BaseURL: url, Token: token,
			Insecure: perEnvBool("ARGOCD", name, "INSECURE")})
	}
	return out
}

func loadZabbixInstances() []ZabbixInstance {
	var out []ZabbixInstance
	for _, name := range envNames("ZABBIX") {
		url := strings.TrimRight(perEnv("ZABBIX", name, "URL"), "/")
		token := perEnv("ZABBIX", name, "TOKEN")
		if url == "" || token == "" {
			continue
		}
		out = append(out, ZabbixInstance{Env: name, BaseURL: url, Token: token,
			Insecure: perEnvBool("ZABBIX", name, "INSECURE")})
	}
	return out
}

func loadGrafanaInstances() []GrafanaInstance {
	var out []GrafanaInstance
	for _, name := range envNames("GRAFANA") {
		url := strings.TrimRight(perEnv("GRAFANA", name, "URL"), "/")
		token := perEnv("GRAFANA", name, "TOKEN")
		if url == "" || token == "" {
			continue
		}
		out = append(out, GrafanaInstance{Env: name, BaseURL: url, Token: token,
			Insecure: perEnvBool("GRAFANA", name, "INSECURE")})
	}
	return out
}

func loadJenkinsInstances() []JenkinsInstance {
	var out []JenkinsInstance
	for _, name := range envNames("JENKINS") {
		url := strings.TrimRight(perEnv("JENKINS", name, "URL"), "/")
		token := perEnv("JENKINS", name, "TOKEN")
		user := perEnv("JENKINS", name, "USER")
		if url == "" || token == "" || user == "" {
			continue
		}
		out = append(out, JenkinsInstance{Env: name, BaseURL: url, User: user, Token: token,
			Insecure: perEnvBool("JENKINS", name, "INSECURE")})
	}
	return out
}

func parsePeople(raw string) ([]PersonSeed, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Допускаем как inline-JSON, так и путь к файлу.
	if !strings.HasPrefix(raw, "[") {
		b, err := os.ReadFile(raw)
		if err != nil {
			return nil, fmt.Errorf("не удалось прочитать файл: %w", err)
		}
		raw = string(b)
	}
	var out []PersonSeed
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("невалидный JSON: %w", err)
	}
	for i := range out {
		if out[i].DisplayName == "" {
			out[i].DisplayName = out[i].Key
		}
	}
	return out, nil
}

// loadDotEnv — минимальный парсер .env без внешних зависимостей.
// Уже установленные переменные окружения не перезаписываются.
func loadDotEnv() error {
	path := os.Getenv("ENV_FILE")
	if path == "" {
		path = ".env"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("чтение %s: %w", path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := env(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := env(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envList(key string, def []string) []string {
	v := env(key, "")
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// Redacted возвращает копию конфига, пригодную для логирования.
func (c Config) Redacted() map[string]any {
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		if len(s) <= 6 {
			return "***"
		}
		return s[:3] + "***" + s[len(s)-2:]
	}
	return map[string]any{
		"server_addr":     c.Server.Addr,
		"postgres":        maskDSN(c.Postgres.DSN),
		"jira_enabled":    c.Jira.Enabled,
		"jira_base_url":   c.Jira.BaseURL,
		"jira_token":      mask(c.Jira.APIToken),
		"gitlab_enabled":  c.GitLab.Enabled,
		"gitlab_base_url": c.GitLab.BaseURL,
		"gitlab_token":    mask(c.GitLab.Token),
		"slack_enabled":   c.Slack.Enabled,
		"slack_token":     mask(c.Slack.UserToken + c.Slack.BotToken),
		"google_enabled":  c.Google.Enabled,
		"google_mode":     string(c.Google.Mode),
		"allure_enabled":  c.Allure.Enabled,
		"allure_base_url": c.Allure.BaseURL,
		"allure_token":    mask(c.Allure.Token),
		"hrdb_enabled":    c.HRDB.Enabled,
		"hrdb_workspace":  c.HRDB.WorkspaceID,
		"people":          len(c.People),
	}
}

func maskDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return dsn
	}
	return dsn[:scheme+3] + "***@" + dsn[at+1:]
}
