import type {
  AILabel,
  AppUser,
  Me,
  AIRules,
  AISampleItem,
  AIStatus,
  Breakdown,
  BreakdownDimension,
  CompanyHoliday,
  CompareQuery,
  BySourceQuery,
  BySourceResponse,
  CompareResponse,
  CountItem,
  DaysResponse,
  DiscoverRequest,
  DiscoverResponse,
  DocLink,
  ActivityEvent,
  EventDetailResponse,
  EventPage,
  EventsQuery,
  Granularity,
  Health,
  HeatmapCell,
  HolidaysResponse,
  HrdbEmployeesResponse,
  HrdbModeResponse,
  HrdbTeamsResponse,
  Meta,
  Person,
  ScheduleSettings,
  PurgePreviewQuery,
  PurgeRequest,
  PurgeResult,
  StatsQuery,
  Summary,
  SyncQueueResponse,
  SyncRequest,
  SyncRun,
  TeamsCompareQuery,
  TeamsCompareResponse,
  FormatCompareResponse,
  AbusersResponse,
  TeamSyncRequest,
  ReprobeSettings,
  TeamSyncResponse,
  Timeline,
  ViolationsResponse,
} from './types'
import { pick } from '../i18n/lang'

const BASE = '/api'

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

// onUnauthorized вызывается при первом 401 от защищённого эндпоинта: сессия
// истекла. Регистрируется в main.tsx и сбрасывает состояние авторизации, чтобы
// приложение показало экран входа вместо «не удалось загрузить данные».
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

type QueryValue = string | number | boolean | string[] | undefined | null

export function buildQuery(params: Record<string, QueryValue>): string {
  const sp = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    if (Array.isArray(value)) {
      if (value.length === 0) continue
      sp.set(key, value.join(','))
    } else {
      sp.set(key, String(value))
    }
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(`${BASE}${path}`, {
      headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}) },
      ...init,
    })
  } catch (e) {
    const reason = e instanceof Error ? e.message : pick('неизвестная ошибка', 'unknown error')
    throw new ApiError(
      `${pick('Не удалось соединиться с сервером', 'Failed to connect to the server')}: ${reason}`,
      0,
    )
  }

  const text = await res.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!res.ok) {
    // Сессия истекла: любой защищённый запрос отвечает 401. Сбрасываем авторизацию
    // (эндпоинты /auth/* обрабатывают свой 401 сами — их не трогаем, чтобы не зациклиться).
    if (res.status === 401 && !path.startsWith('/auth/')) {
      onUnauthorized?.()
    }
    const message =
      payload && typeof payload === 'object' && 'error' in payload && typeof (payload as { error: unknown }).error === 'string'
        ? (payload as { error: string }).error
        : `${pick('Ошибка запроса', 'Request failed')} (${res.status})`
    throw new ApiError(message, res.status)
  }

  return payload as T
}

function statsParams(q: StatsQuery): Record<string, QueryValue> {
  return {
    person_key: q.person_key,
    from: q.from,
    to: q.to,
    tz: q.tz,
    source: q.source,
    type: q.type,
    project: q.project,
    q: q.q,
    min_ai: q.min_ai,
  }
}

export const api = {
  authMe: () => request<Me>('/auth/me'),
  // /auth/* живёт вне /api — сырой fetch.
  logout: () => fetch('/auth/logout', { method: 'POST' }).catch(() => undefined),
  users: () => request<{ users: AppUser[] }>('/users'),
  upsertUser: (email: string, role: string) =>
    request<{ users: AppUser[] }>('/users', { method: 'POST', body: JSON.stringify({ email, role }) }),
  deleteUser: (email: string) =>
    request<{ users: AppUser[] }>(`/users${buildQuery({ email })}`, { method: 'DELETE' }),

  health: () => request<Health>('/health'),
  meta: () => request<Meta>('/meta'),

  people: () => request<Person[]>('/people'),
  savePerson: (person: Person) =>
    request<Person>('/people', { method: 'POST', body: JSON.stringify(person) }),
  deletePerson: (key: string) =>
    request<{ deleted: string }>(`/people/${encodeURIComponent(key)}`, { method: 'DELETE' }),
  cleanupExPeople: () =>
    request<{ deleted: string[]; note?: string }>('/people/cleanup-ex', { method: 'POST', body: '{}' }),
  personProjects: (key: string) => request<CountItem[]>(`/people/${encodeURIComponent(key)}/projects`),

  hrdbEmployees: (q: string, limit = 50) =>
    request<HrdbEmployeesResponse>(`/hrdb/employees${buildQuery({ q, limit })}`),
  hrdbTeams: () => request<HrdbTeamsResponse>('/hrdb/teams'),
  hrdbMode: () => request<HrdbModeResponse>('/settings/hrdb'),
  setHrdbMode: (mode: string) =>
    request<HrdbModeResponse>('/settings/hrdb', { method: 'PUT', body: JSON.stringify({ mode }) }),
  officeCidrs: () => request<{ cidrs: string[] }>('/settings/office-cidrs'),
  setOfficeCidrs: (cidrs: string[]) =>
    request<{ cidrs: string[] }>('/settings/office-cidrs', { method: 'PUT', body: JSON.stringify({ cidrs }) }),
  slackText: (id: string) => request<{ id: string; text: string }>(`/slack/text${buildQuery({ id })}`),
  slackRelink: () =>
    request<{ people: number; events: number }>('/slack/relink', { method: 'POST', body: '{}' }),
  schedule: () => request<ScheduleSettings>('/settings/schedule'),
  setSchedule: (enabled: boolean, time: string, catchup: boolean) =>
    request<ScheduleSettings>('/settings/schedule', {
      method: 'PUT',
      body: JSON.stringify({ enabled, time, catchup }),
    }),
  disabledRules: () => request<{ disabled: string[] }>('/settings/rules'),
  setDisabledRules: (disabled: string[]) =>
    request<{ disabled: string[] }>('/settings/rules', { method: 'PUT', body: JSON.stringify({ disabled }) }),
  reprobe: () => request<ReprobeSettings>('/settings/reprobe'),
  setReprobe: (days: Record<string, number>) =>
    request<ReprobeSettings>('/settings/reprobe', { method: 'PUT', body: JSON.stringify({ days }) }),
  shallowConfig: () =>
    request<{
      default: string[]
      areas: Record<string, string[]>
      all_types: string[]
      known_areas: string[]
    }>('/settings/shallow'),
  setShallowConfig: (body: { default: string[]; areas: Record<string, string[]> }) =>
    request<{ default: string[]; areas: Record<string, string[]> }>('/settings/shallow', {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  discoverPerson: (body: DiscoverRequest) =>
    request<DiscoverResponse>('/people/discover', { method: 'POST', body: JSON.stringify(body) }),

  startSync: (body: SyncRequest) => request<SyncRun>('/sync', { method: 'POST', body: JSON.stringify(body) }),
  startTeamSync: (body: TeamSyncRequest) =>
    request<TeamSyncResponse>('/sync/team', { method: 'POST', body: JSON.stringify(body) }),
  startUnitSync: (body: {
    cluster?: string
    department?: string
    from: string
    to: string
    sources?: string[]
    overwrite?: boolean
  }) => request<TeamSyncResponse>('/sync/unit', { method: 'POST', body: JSON.stringify(body) }),
  cancelSync: (id: string) =>
    request<SyncRun>(`/sync/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  compare: (q: CompareQuery) => request<CompareResponse>(`/stats/compare${buildQuery({ ...q })}`),
  bySource: (q: BySourceQuery) => request<BySourceResponse>(`/stats/by-source${buildQuery({ ...q })}`),
  compareTeams: (q: TeamsCompareQuery) =>
    request<TeamsCompareResponse>(`/stats/teams${buildQuery({ ...q })}`),
  formatCompare: (q: {
    from: string
    to: string
    tz?: string
    dim: string
    areas?: string[]
    clusters?: string[]
    grades?: string[]
    rules?: string[]
  }) =>
    request<FormatCompareResponse>(
      `/stats/format-compare${buildQuery({
        from: q.from,
        to: q.to,
        tz: q.tz,
        dim: q.dim,
        areas: q.areas?.length ? q.areas.join(',') : undefined,
        clusters: q.clusters?.length ? q.clusters.join(',') : undefined,
        grades: q.grades?.length ? q.grades.join(',') : undefined,
        rules: q.rules?.length ? q.rules.join(',') : undefined,
      })}`,
    ),
  abusers: (q: { from: string; to: string; tz?: string }) =>
    request<AbusersResponse>(`/stats/abusers${buildQuery({ ...q })}`),
  syncRun: (id: string) => request<SyncRun>(`/sync/${encodeURIComponent(id)}`),
  syncQueue: () => request<SyncQueueResponse>('/sync/queue'),
  syncRuns: (personKey: string, limit = 20) =>
    request<SyncRun[]>(`/sync${buildQuery({ person_key: personKey, limit })}`),

  summary: (q: StatsQuery) => request<Summary>(`/stats/summary${buildQuery(statsParams(q))}`),
  timeline: (q: StatsQuery, granularity?: Granularity) =>
    request<Timeline>(`/stats/timeline${buildQuery({ ...statsParams(q), granularity })}`),
  breakdown: (q: StatsQuery, dimension: BreakdownDimension) =>
    request<Breakdown>(`/stats/breakdown${buildQuery({ ...statsParams(q), dimension })}`),
  heatmap: (q: StatsQuery) => request<HeatmapCell[]>(`/stats/heatmap${buildQuery(statsParams(q))}`),
  // Особые дни не зависят от фильтров по источникам/типам — только период.
  days: (q: StatsQuery) =>
    request<DaysResponse>(`/stats/days${buildQuery({ person_key: q.person_key, from: q.from, to: q.to })}`),

  events: (q: EventsQuery) =>
    request<EventPage>(
      `/events${buildQuery({ ...statsParams(q), page: q.page, per_page: q.per_page, sort: q.sort })}`,
    ),
  event: (id: string) => request<EventDetailResponse>(`/events/${encodeURIComponent(id)}`),
  refEvents: (refId: string, personKey: string) =>
    request<ActivityEvent[]>(`/refs/${encodeURIComponent(refId)}/events${buildQuery({ person_key: personKey })}`),

  docs: (personKey: string) => request<DocLink[]>(`/docs${buildQuery({ person_key: personKey })}`),

  purgePreview: (q: PurgePreviewQuery) =>
    request<PurgeResult>(
      `/purge/preview${buildQuery({
        person_key: q.person_key,
        from: q.from,
        to: q.to,
        source: q.sources,
        sync_runs: q.sync_runs ? true : undefined,
      })}`,
    ),
  purge: (body: PurgeRequest) =>
    request<PurgeResult>('/purge', { method: 'POST', body: JSON.stringify(body) }),

  // Корпоративный календарь праздников и вкладка «Нарушения».
  holidays: (country: string, year: number) =>
    request<HolidaysResponse>(`/holidays${buildQuery({ country, year })}`),
  addHoliday: (h: CompanyHoliday) =>
    request<CompanyHoliday>('/holidays', { method: 'POST', body: JSON.stringify(h) }),
  deleteHoliday: (country: string, day: string) =>
    request<{ deleted: boolean }>(`/holidays${buildQuery({ country, day })}`, { method: 'DELETE' }),
  importHolidays: (country: string, year: number) =>
    request<{ imported: number }>('/holidays/import', {
      method: 'POST',
      body: JSON.stringify({ country, year }),
    }),
  violations: (q: {
    from?: string
    to?: string
    tz?: string
    team?: string
    cluster?: string
    department?: string
    person_key?: string
  }) => request<ViolationsResponse>(`/violations${buildQuery({ ...q })}`),

  // Локальный AI-скорер сообщений Slack.
  aiStatus: () => request<AIStatus>('/ai/status'),
  aiLabels: () => request<{ labels: AILabel[] }>('/ai/labels'),
  aiAddLabel: (text: string, score: number) =>
    request<AIStatus>('/ai/labels', { method: 'POST', body: JSON.stringify({ text, score }) }),
  aiDeleteLabel: (id: number) =>
    request<AIStatus>(`/ai/labels/${id}`, { method: 'DELETE' }),
  aiTrain: () => request<AIStatus>('/ai/train', { method: 'POST', body: '{}' }),
  aiRules: () => request<AIRules>('/ai/rules'),
  aiSaveRules: (rules: AIRules) =>
    request<AIRules>('/ai/rules', { method: 'PUT', body: JSON.stringify(rules) }),
  aiSample: (personKey: string, from: string, to: string, limit = 30) =>
    request<{ items: AISampleItem[] }>(
      `/ai/sample${buildQuery({ person_key: personKey, from, to, limit })}`,
    ),
  aiRescore: (body: { person_key?: string; from: string; to: string; only_unscored?: boolean }) =>
    request<{ scored: number }>('/ai/rescore', { method: 'POST', body: JSON.stringify(body) }),
}
