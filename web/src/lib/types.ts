// Типы API бэкенда (Go). Base URL — /api.

export type SourceKey = 'jira' | 'gitlab' | 'slack' | 'gdocs' | 'gcal' | 'allure' | 'confluence' | 'gwork' | 'argocd' | 'zabbix' | 'jenkins' | 'grafana' | 'figma' | 'netsuite' | 'claude'

export type Role = 'viewer' | 'lead' | 'supervisor' | 'administrator' | 'anonymous'

/** Текущий пользователь (роль anonymous — не залогинен). */
export interface Me {
  email: string
  role: Role
  enabled: boolean
}

/** Пользователь дашборда в админке ролей. */
export interface AppUser {
  email: string
  role: Role
  added_by?: string
  created_at: string
}

export interface Health {
  status: string
  db: string
  sources: Record<string, boolean>
}

export interface MetaSource {
  key: string
  label: string
  enabled: boolean
}

export interface Meta {
  sources: MetaSource[]
  type_labels: Record<string, string>
  default_lookback_days: number
  hrdb_enabled?: boolean
  ai_enabled?: boolean
}

/** Состояние локального AI-скорера сообщений. */
export interface AIStatus {
  enabled: boolean
  status?: string
  error?: string
  model?: string
  labels?: number
  min_labels?: number
  trained?: boolean
  mode?: 'calibrated' | 'heuristic'
}

/** Размеченный пример калибровки AI. */
export interface AILabel {
  id: number
  text: string
  score: number
  created_at: string
}

/** Автоправила скорера: такие сообщения получают 0 без прогона через модель. */
export interface AIRules {
  min_length: number
  patterns: string[]
  drop_emoji_only: boolean
}

/** Сообщение Slack с текущей AI-оценкой — материал для калибровки. */
export interface AISampleItem {
  id: string
  text: string
  score?: number
}

/** Сотрудник из HRDB (Atlassian Assets). */
export interface HrdbEmployee {
  key: string
  display_name: string
  email: string
}

export interface HrdbEmployeesResponse {
  enabled: boolean
  employees: HrdbEmployee[]
}

/** Итог автопоиска аккаунта в одной системе. */
export interface DiscoverSystemResult {
  source: string
  status: 'found' | 'not_found' | 'error' | 'disabled'
  value?: string
  detail?: string
}

export interface DiscoverRequest {
  email: string
  key?: string
  display_name?: string
}

export interface DiscoverResponse {
  person: Person
  results: DiscoverSystemResult[]
}

export interface Person {
  id?: number
  key: string
  display_name: string
  email?: string
  jira_account_id?: string
  gitlab_username?: string
  slack_user_id?: string
  google_email?: string
  /** Офис из HRDB — определяет праздничный календарь. */
  office?: string
  /** Команда и направление (Area of Responsibility) из HRDB. */
  team?: string
  area?: string
  /** Должность из HRDB (напр. «Staff Backend Development»). */
  title?: string
  /** Дата найма из HRDB: дни до неё не считаются рабочими. */
  hire_date?: string
  created_at?: string
  updated_at?: string
}

export interface HrdbTeam {
  name: string
  members: number
  /** Иерархическая цепочка снизу вверх: команда → юнит → кластер → департамент. */
  chain?: string[]
  unit?: string
  cluster?: string
  department?: string
}

export interface HrdbTeamsResponse {
  enabled: boolean
  teams: HrdbTeam[]
  areas: string[]
  clusters: string[]
  departments: string[]
}

/** Активный источник HRDB: cloud — новая (Atlassian), dc — старая (jira.xtools.tv). */
export interface HrdbModeResponse {
  mode: string
  available: string[]
  labels: Record<string, string>
}

/** Точка спарклайна активности. */
export interface SparkPoint {
  bucket: string
  count: number
}

/** Метрики одного человека для сравнения продуктивности. */
export interface PersonMetrics {
  person: Person
  total_events: number
  active_days: number
  working_days: number
  active_working_days: number
  idle_working_days: number
  vacation_days: number
  sick_days: number
  worklog_hours: number
  meeting_hours: number
  lines_changed: number
  by_source: Record<string, number>
  by_type: Record<string, number>
  timeline: SparkPoint[] | null
}

export interface CompareResponse {
  from: string
  to: string
  granularity: 'day' | 'week'
  people: PersonMetrics[]
}

export interface CompareQuery {
  from?: string
  to?: string
  tz?: string
  /** Мультивыбор команд: произвольный состав для сравнения. */
  team?: string[]
  area?: string
}

export interface TeamSyncRequest {
  team: string
  from: string
  to: string
  sources?: string[]
  overwrite?: boolean
}

export interface ReprobeSettings {
  default: number
  days: Record<string, number>
  sources: string[]
}

/** Точка средневзвешенного ряда команды: события на присутствующего. */
export interface WeightedPoint {
  bucket: string
  events: number
  presence: number
  value: number
}

/** Метрики команды для вкладки сравнения команд. */
export interface TeamMetrics {
  team: string
  members: number
  total_events: number
  person_days: number
  events_per_person_day: number
  by_source: Record<string, number>
  timeline: SparkPoint[] | null
  weighted: WeightedPoint[] | null
}

export interface TeamsCompareResponse {
  from: string
  to: string
  granularity: 'day' | 'week'
  teams: TeamMetrics[]
}

export interface TeamsCompareQuery {
  from?: string
  to?: string
  tz?: string
  team?: string[]
  source?: string[]
  type?: string[]
  min_ai?: number
}

/** Праздник корпоративного календаря. */
export interface CompanyHoliday {
  country: string
  day: string
  label: string
}

export interface HolidaysResponse {
  country: string
  year: number
  /** Год покрыт корпоративным календарём (иначе действуют данные Google). */
  covered: boolean
  days: CompanyHoliday[]
}

/** Находка вкладки «Отклонения». */
export interface ViolationItem {
  person_key: string
  person: string
  team?: string
  cluster?: string
  department?: string
  rule:
    | 'sick_adjacent'
    | 'sick_monthly'
    | 'overtime_idle'
    | 'overtime_low'
    | 'offday_activity'
    | 'no_vacation'
    | 'idle_streak'
    | 'role_vacation_overlap'
    | 'work_on_vacation'
    | 'long_workday'
    | 'activity_drop'
    | 'remote_zero'
    | 'remote_drop'
    | 'wfh_zero'
    | 'slow_review'
    | 'bus_factor'
    | 'no_reviewers'
    | 'role_inactive'
    | 'steady_rhythm'
    | 'review_champion'
    | 'onboarding_rise'
    | 'onboarding_flat'
    | 'mentor_one_on_ones'
    | 'vacation_recovery'
    | 'slack_only_days'
    | 'low_activity_days'
  severity: 'warn' | 'info'
  /** null у правил без привязки к датам (ритм, bus-фактор и т.п.). */
  dates: string[] | null
  title: string
  detail: string
  url?: string
}

export interface FormatCell {
  people: number
  avg_events_day: number
  useful_events_day: number
  active_ratio: number
  dev_per_person: number
  dev_by_rule: Record<string, number>
}
export interface FormatScatterPoint {
  name: string
  format: string
  area: string
  grade: string
  events_day: number
  useful_day: number
  dev: number
}
export interface FormatTrend {
  granularity: string
  buckets: string[]
  series: Record<string, number[]>
}
export interface FormatCompareResponse {
  dim: 'area' | 'cluster' | 'grade'
  formats: string[]
  rows: { key: string; cells: Record<string, FormatCell> }[]
  totals: Record<string, FormatCell>
  scatter: FormatScatterPoint[]
  trend: FormatTrend
  groups: string[]
  rules: string[]
  areas: string[]
  clusters: string[]
  grades: string[]
  from: string
  to: string
}

export interface AbuserRow {
  person: Person
  cluster?: string
  total_events: number
  working_days: number
  active_working_days: number
  idle_working_days: number
  low_activity_days: number
  active_ratio: number
  reason: 'no_activity' | 'idle' | 'low_activity'
}
export interface AbusersResponse {
  from: string
  to: string
  abusers: AbuserRow[]
}

export interface ViolationsResponse {
  from: string
  to: string
  overtime_enabled: boolean
  overtime_note?: string
  violations: ViolationItem[]
}

export interface TeamSyncResponse {
  team: string
  members: number
  created: number
  started: number
  runs: SyncRun[]
}

/** Особый день: государственный праздник, отпуск или больничный. */
export interface PersonDay {
  person_key: string
  day: string
  kind: 'holiday' | 'vacation' | 'sick' | 'remote'
  label?: string
}

export interface DaysResponse {
  office: string
  country: string
  days: PersonDay[]
  /** Гибридные дни из дома — ISO-номера дней недели (1=Пн … 7=Вс). */
  hybrid_days?: number[]
  /** Явные даты гибридных дней (YYYY-MM-DD) с учётом истории изменений шаблона. */
  hybrid_dates?: string[]
  /** Дни заведённых овертаймов (YYYY-MM-DD). */
  overtime_days?: string[]
  /** «Поверхностные» дни (YYYY-MM-DD): активность — только Slack и входы. */
  shallow_days?: string[]
  /** Дата найма (YYYY-MM-DD) — отметка на матрице активности; пустая строка, если неизвестна. */
  hire_date?: string
}

export interface CountItem {
  key: string
  label: string
  count: number
  value?: number
}

export type SyncStatus = 'pending' | 'running' | 'done' | 'failed' | 'partial'

/** Расписание ежедневного обновления данных. */
export interface ScheduleSettings {
  enabled: boolean
  time: string
  catchup?: boolean
  last_date?: string
  last_at?: string
  last_started?: string
  workers: number
  server_time: string
}

/** Снапшот очереди сбора: активные, ожидающие и средняя длительность прогона. */
export interface SyncQueueResponse {
  running: SyncRun[]
  pending: SyncRun[]
  avg_run_ms: number
  workers: number
}

export interface SyncSourceResult {
  source: string
  status: string
  events: number
  duration_ms: number
  error?: string
  note?: string
}

export interface SyncRun {
  id: string
  person_key: string
  from: string
  to: string
  status: SyncStatus
  started_at: string
  finished_at?: string
  sources: Record<string, SyncSourceResult>
  error?: string
}

export interface SyncRequest {
  person_key: string
  from: string
  to: string
  sources?: string[]
  overwrite?: boolean
}

export interface Highlight {
  key: string
  label: string
  value: number
  unit?: string
  source?: string
  delta: number
  has_delta: boolean
}

export interface Summary {
  person_key: string
  from: string
  to: string
  total_events: number
  active_days: number
  span_days: number
  by_source: CountItem[]
  top_projects: CountItem[]
  highlights: Highlight[]
  worklog_hours: number
  meeting_hours: number
  lines_changed: number
  first_event_at?: string
  last_event_at?: string
  prev_total: number
  prev_by_source: CountItem[]

  // Разбивка дней периода: отпуск > праздник > выходной > рабочий.
  working_days: number
  active_working_days: number
  /** Рабочие дни с активностью по её объёму: 1 / 2–5 / >5 событий за день. */
  active_day_buckets: CountItem[]
  weekend_days: number
  holiday_days: number
  vacation_days: number
  sick_days: number
  idle_working_days: number
  office?: string
  holiday_country?: string
}

export type Granularity = 'hour' | 'day' | 'week' | 'month'

export interface TimelinePoint {
  bucket: string
  total: number
  counts: Record<string, number>
}

export interface Timeline {
  granularity: Granularity
  points: TimelinePoint[]
}

export type BreakdownDimension = 'source' | 'type' | 'project' | 'ref' | 'peer'

export interface Breakdown {
  dimension: BreakdownDimension
  items: CountItem[]
}

export interface HeatmapCell {
  weekday: number // 0 = понедельник
  hour: number
  count: number
}

export interface ActivityEvent {
  id: number | string
  person_key: string
  source: SourceKey
  type: string
  external_id: string
  occurred_at: string
  title: string
  body?: string
  url?: string
  project?: string
  project_name?: string
  ref_id?: string
  parent_ref_id?: string
  effort: number
  effort_unit?: string
  meta?: Record<string, unknown>
}

export interface EventPage {
  items: ActivityEvent[]
  total: number
  page: number
  per_page: number
}

export interface EventDetailResponse {
  event: ActivityEvent
  related: ActivityEvent[]
}

export interface DocLink {
  doc_id: string
  doc_url: string
  title: string
  issue_key: string
  issue_title?: string
  person_key: string
  found_in: string
  discovered_at: string
  enriched: boolean
  last_modified?: string
  edit_count: number
  comment_count: number
}

/** Результат очистки данных: одинаков для preview и реального удаления. */
export interface PurgeResult {
  events: number
  by_source: CountItem[]
  sync_runs: number
  doc_links_reset: number
  preview: boolean
  person_key: string
  from: string
  to: string
}

export interface PurgePreviewQuery {
  person_key: string
  from: string
  to: string
  sources?: string[]
  sync_runs?: boolean
}

export interface PurgeRequest {
  person_key: string
  from: string
  to: string
  sources?: string[]
  sync_runs: boolean
  reset_doc_links: boolean
  /** Без `confirm: true` бэкенд отвечает 400 — защита от случайного вызова. */
  confirm: true
}

export interface StatsQuery {
  person_key: string
  from?: string
  to?: string
  tz?: string
  source?: string[]
  type?: string[]
  project?: string[]
  q?: string
  /** Минимальная AI-оценка сообщений Slack (0 — фильтр выключен). */
  min_ai?: number
}

export interface EventsQuery extends StatsQuery {
  page?: number
  per_page?: number
  sort?: 'asc' | 'desc'
}
