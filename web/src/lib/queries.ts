import { useMemo } from 'react'
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from '@tanstack/react-query'
import { api } from './api'
import { SOURCE_KEYS } from './chartTheme'
import type {
  ActivityEvent,
  Breakdown,
  BreakdownDimension,
  CompareQuery,
  CompareResponse,
  BySourceQuery,
  BySourceResponse,
  CountItem,
  DaysResponse,
  DiscoverRequest,
  DiscoverResponse,
  DocLink,
  HrdbEmployeesResponse,
  HrdbTeamsResponse,
  EventDetailResponse,
  EventPage,
  EventsQuery,
  Granularity,
  Health,
  HeatmapCell,
  Meta,
  Person,
  PurgePreviewQuery,
  PurgeRequest,
  PurgeResult,
  StatsQuery,
  SourceKey,
  Summary,
  SyncRequest,
  SyncRun,
  TeamsCompareQuery,
  TeamsCompareResponse,
  ViolationsResponse,
  TeamSyncRequest,
  TeamSyncResponse,
  Timeline,
} from './types'

type Opt<T> = Omit<UseQueryOptions<T, Error, T, readonly unknown[]>, 'queryKey' | 'queryFn'>

export const keys = {
  health: ['health'] as const,
  meta: ['meta'] as const,
  people: ['people'] as const,
  personProjects: (key: string) => ['people', key, 'projects'] as const,
  summary: (q: StatsQuery) => ['stats', 'summary', q] as const,
  timeline: (q: StatsQuery, g?: Granularity) => ['stats', 'timeline', q, g ?? 'auto'] as const,
  breakdown: (q: StatsQuery, d: BreakdownDimension) => ['stats', 'breakdown', q, d] as const,
  heatmap: (q: StatsQuery) => ['stats', 'heatmap', q] as const,
  events: (q: EventsQuery) => ['events', q] as const,
  event: (id: string) => ['events', id] as const,
  refEvents: (refId: string, person: string) => ['refs', refId, person] as const,
  docs: (person: string) => ['docs', person] as const,
  syncRuns: (person: string) => ['sync', 'runs', person] as const,
  syncRun: (id: string) => ['sync', 'run', id] as const,
}

export function useHealth() {
  return useQuery<Health>({
    queryKey: keys.health,
    queryFn: api.health,
    refetchInterval: 60_000,
    retry: false,
  })
}

export function useMeta() {
  return useQuery<Meta>({ queryKey: keys.meta, queryFn: api.meta, staleTime: 5 * 60_000 })
}

/**
 * Источники, включённые на бэкенде. Пока мета не загрузилась (или список пуст),
 * считаем включёнными все: иначе навигация на первом рендере схлопнулась бы,
 * а потом дёрнулась. Скрываем только навигацию и фильтры — исторические
 * события отключённого источника продолжают попадать в графики.
 */
export function useEnabledSources(): SourceKey[] {
  const { data: meta } = useMeta()
  return useMemo(() => {
    const list = meta?.sources ?? []
    if (list.length === 0) return SOURCE_KEYS
    const enabled = new Set(list.filter((s) => s.enabled).map((s) => s.key))
    return SOURCE_KEYS.filter((key) => enabled.has(key))
  }, [meta])
}

export function usePeople() {
  return useQuery<Person[]>({ queryKey: keys.people, queryFn: api.people, staleTime: 60_000 })
}

/** Сотрудники из HRDB с поиском по имени/e-mail. Кэш — минута. */
export function useHrdbEmployees(q: string, enabled = true) {
  return useQuery<HrdbEmployeesResponse>({
    queryKey: ['hrdb', 'employees', q] as const,
    queryFn: () => api.hrdbEmployees(q),
    enabled,
    staleTime: 60_000,
    placeholderData: (prev) => prev,
  })
}

/** Команды и направления из HRDB. Кэш — 5 минут. */
export function useHrdbTeams() {
  return useQuery<HrdbTeamsResponse>({
    queryKey: ['hrdb', 'teams'] as const,
    queryFn: api.hrdbTeams,
    staleTime: 5 * 60_000,
  })
}

/** Метрики сравнения продуктивности с фильтрами по команде и направлению. */
export function useCompare(q: CompareQuery, options?: Opt<CompareResponse>) {
  return useQuery<CompareResponse>({
    queryKey: ['stats', 'compare', q] as const,
    queryFn: () => api.compare(q),
    ...options,
  })
}

/** Активность людей в одной системе по месяцам — вкладка «По системе». */
export function useBySource(q: BySourceQuery, options?: Opt<BySourceResponse>) {
  return useQuery<BySourceResponse>({
    queryKey: ['stats', 'by-source', q] as const,
    queryFn: () => api.bySource(q),
    ...options,
  })
}

/** Метрики команд для вкладки сравнения команд. */
export function useCompareTeams(q: TeamsCompareQuery, options?: Opt<TeamsCompareResponse>) {
  return useQuery<TeamsCompareResponse>({
    queryKey: ['stats', 'teams', q] as const,
    queryFn: () => api.compareTeams(q),
    ...options,
  })
}

/** Массовый сбор по команде из HRDB. */
export function useStartTeamSync() {
  const qc = useQueryClient()
  return useMutation<TeamSyncResponse, Error, TeamSyncRequest>({
    mutationFn: api.startTeamSync,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['sync'] })
      void qc.invalidateQueries({ queryKey: keys.people })
    },
  })
}

/** Автопоиск аккаунтов человека по e-mail во всех системах. */
export function useDiscoverPerson() {
  return useMutation<DiscoverResponse, Error, DiscoverRequest>({
    mutationFn: api.discoverPerson,
  })
}

export function useSavePerson() {
  const qc = useQueryClient()
  return useMutation<Person, Error, Person>({
    mutationFn: api.savePerson,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.people })
    },
  })
}

export function usePersonProjects(personKey: string) {
  return useQuery<CountItem[]>({
    queryKey: keys.personProjects(personKey),
    queryFn: () => api.personProjects(personKey),
    enabled: Boolean(personKey),
  })
}

export function useSummary(q: StatsQuery, options?: Opt<Summary>) {
  return useQuery<Summary>({
    queryKey: keys.summary(q),
    queryFn: () => api.summary(q),
    enabled: Boolean(q.person_key),
    ...options,
  })
}

export function useTimeline(q: StatsQuery, granularity?: Granularity, options?: Opt<Timeline>) {
  return useQuery<Timeline>({
    queryKey: keys.timeline(q, granularity),
    queryFn: () => api.timeline(q, granularity),
    enabled: Boolean(q.person_key),
    ...options,
  })
}

export function useBreakdown(q: StatsQuery, dimension: BreakdownDimension, options?: Opt<Breakdown>) {
  return useQuery<Breakdown>({
    queryKey: keys.breakdown(q, dimension),
    queryFn: () => api.breakdown(q, dimension),
    enabled: Boolean(q.person_key),
    ...options,
  })
}

export function useHeatmap(q: StatsQuery, options?: Opt<HeatmapCell[]>) {
  return useQuery<HeatmapCell[]>({
    queryKey: keys.heatmap(q),
    queryFn: () => api.heatmap(q),
    enabled: Boolean(q.person_key),
    ...options,
  })
}

/** Отклонения за период (расчёт командный, кэш 10 мин; фильтруем по человеку
 *  на клиенте). Для карточки отклонений в обзоре. */
export function useViolations(q: StatsQuery, options?: Opt<ViolationsResponse>) {
  return useQuery<ViolationsResponse>({
    queryKey: ['violations', 'overview', q.from, q.to, q.tz] as const,
    queryFn: () => api.violations({ from: q.from, to: q.to, tz: q.tz }),
    ...options,
  })
}

/** Особые дни (праздники по офису, отпуска) — для разметки матрицы активности. */
export function useDays(q: StatsQuery, options?: Opt<DaysResponse>) {
  return useQuery<DaysResponse>({
    queryKey: ['stats', 'days', q.person_key, q.from, q.to] as const,
    queryFn: () => api.days(q),
    enabled: Boolean(q.person_key),
    ...options,
  })
}

export function useEvents(q: EventsQuery, options?: Opt<EventPage>) {
  return useQuery<EventPage>({
    queryKey: keys.events(q),
    queryFn: () => api.events(q),
    enabled: Boolean(q.person_key),
    placeholderData: (prev) => prev,
    ...options,
  })
}

export function useEvent(id: string) {
  return useQuery<EventDetailResponse>({
    queryKey: keys.event(id),
    queryFn: () => api.event(id),
    enabled: Boolean(id),
  })
}

export function useRefEvents(refId: string, personKey: string, enabled = true) {
  return useQuery<ActivityEvent[]>({
    queryKey: keys.refEvents(refId, personKey),
    queryFn: () => api.refEvents(refId, personKey),
    enabled: enabled && Boolean(refId) && Boolean(personKey),
  })
}

export function useDocs(personKey: string) {
  return useQuery<DocLink[]>({
    queryKey: keys.docs(personKey),
    queryFn: () => api.docs(personKey),
    enabled: Boolean(personKey),
  })
}

export function useSyncRuns(personKey: string, limit = 20) {
  return useQuery<SyncRun[]>({
    queryKey: keys.syncRuns(personKey),
    queryFn: () => api.syncRuns(personKey, limit),
    enabled: Boolean(personKey),
  })
}

/** Поллинг активного прогона раз в 2 секунды, пока он не завершится. */
export function useSyncRun(id: string | null) {
  return useQuery<SyncRun>({
    queryKey: keys.syncRun(id ?? ''),
    queryFn: () => api.syncRun(id as string),
    enabled: Boolean(id),
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'pending' || status === 'running' ? 2000 : false
    },
  })
}

export function useStartSync() {
  const qc = useQueryClient()
  return useMutation<SyncRun, Error, SyncRequest>({
    mutationFn: api.startSync,
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: keys.syncRuns(run.person_key) })
    },
  })
}

/** После завершения сбора освежаем всю статистику. */
export function useInvalidateStats() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['stats'] })
    void qc.invalidateQueries({ queryKey: ['events'] })
    void qc.invalidateQueries({ queryKey: ['docs'] })
  }
}

/**
 * Preview очистки — мутация, а не запрос: считать надо ровно по кнопке, а не
 * при каждом изменении формы, иначе «что удалится» разъезжается с тем, что
 * пользователь уже видит на экране.
 */
export function usePurgePreview() {
  return useMutation<PurgeResult, Error, PurgePreviewQuery>({ mutationFn: api.purgePreview })
}

/** Удаление данных за период. После успеха сбрасываем кэш статистики целиком. */
export function usePurge() {
  const qc = useQueryClient()
  const invalidateStats = useInvalidateStats()
  return useMutation<PurgeResult, Error, PurgeRequest>({
    mutationFn: api.purge,
    onSuccess: (res) => {
      invalidateStats()
      void qc.invalidateQueries({ queryKey: keys.syncRuns(res.person_key) })
    },
  })
}
