import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { pick } from '../i18n/lang'

/** Шаг таймлайна: `auto` = не передавать granularity, бэкенд подберёт сам. */
export type TimelineStep = 'auto' | 'day' | 'week' | 'month'

export const TIMELINE_STEPS: TimelineStep[] = ['auto', 'day', 'week', 'month']

/** Подпись шага таймлайна — по текущему языку (нельзя замораживать в константе). */
export function timelineStepLabel(step: TimelineStep): string {
  const labels: Record<TimelineStep, string> = pick(
    { auto: 'Авто', day: 'День', week: 'Неделя', month: 'Месяц' },
    { auto: 'Auto', day: 'Day', week: 'Week', month: 'Month' },
  )
  return labels[step]
}

export interface Filters {
  person: string
  from: string
  to: string
  sources: string[]
  types: string[]
  projects: string[]
  q: string
  /** Фильтры вкладки «Сравнение»: команда и направление из HRDB. */
  team: string
  area: string
  /** Минимальная AI-оценка сообщений Slack (0 — без фильтра). */
  minAi: number
  page: number
  perPage: number
  /** Шаг таймлайна из URL-параметра `g`. */
  granularity: TimelineStep
}

export interface FilterPatch {
  person?: string
  from?: string
  to?: string
  sources?: string[]
  types?: string[]
  projects?: string[]
  q?: string
  team?: string
  area?: string
  minAi?: number
  page?: number
  perPage?: number
  granularity?: TimelineStep
}

export const DEFAULT_LOOKBACK_DAYS = 30
export const TIMEZONE = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

function startOfDay(d: Date): Date {
  const x = new Date(d)
  x.setHours(0, 0, 0, 0)
  return x
}

function endOfDay(d: Date): Date {
  const x = new Date(d)
  x.setHours(23, 59, 59, 999)
  return x
}

export function defaultRange(days = DEFAULT_LOOKBACK_DAYS): { from: string; to: string } {
  const now = new Date()
  const from = startOfDay(new Date(now.getTime() - (days - 1) * 86400000))
  return { from: from.toISOString(), to: endOfDay(now).toISOString() }
}

export function currentMonthRange(): { from: string; to: string } {
  const now = new Date()
  const from = new Date(now.getFullYear(), now.getMonth(), 1, 0, 0, 0, 0)
  return { from: from.toISOString(), to: endOfDay(now).toISOString() }
}

function parseList(value: string | null): string[] {
  if (!value) return []
  return value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

function parseStep(value: string | null): TimelineStep {
  return (TIMELINE_STEPS as string[]).includes(value ?? '') ? (value as TimelineStep) : 'auto'
}

/**
 * Всё состояние фильтров живёт в URL search-params — ссылкой можно поделиться.
 */
export function useFilters() {
  const [searchParams, setSearchParams] = useSearchParams()

  const fallback = useMemo(() => defaultRange(), [])

  const filters = useMemo<Filters>(() => {
    const pageRaw = Number(searchParams.get('page'))
    const perPageRaw = Number(searchParams.get('per_page'))
    return {
      person: searchParams.get('person') ?? '',
      from: searchParams.get('from') || fallback.from,
      to: searchParams.get('to') || fallback.to,
      sources: parseList(searchParams.get('source')),
      types: parseList(searchParams.get('type')),
      projects: parseList(searchParams.get('project')),
      q: searchParams.get('q') ?? '',
      team: searchParams.get('team') ?? '',
      area: searchParams.get('area') ?? '',
      minAi: Number(searchParams.get('min_ai')) || 0,
      page: Number.isFinite(pageRaw) && pageRaw > 0 ? pageRaw : 1,
      perPage: Number.isFinite(perPageRaw) && perPageRaw > 0 ? perPageRaw : 25,
      granularity: parseStep(searchParams.get('g')),
    }
  }, [searchParams, fallback])

  const setFilters = useCallback(
    (patch: FilterPatch, options?: { replace?: boolean }) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          const put = (key: string, value: string) => {
            if (value) next.set(key, value)
            else next.delete(key)
          }
          if (patch.person !== undefined) put('person', patch.person)
          if (patch.from !== undefined) put('from', patch.from)
          if (patch.to !== undefined) put('to', patch.to)
          if (patch.sources !== undefined) put('source', patch.sources.join(','))
          if (patch.types !== undefined) put('type', patch.types.join(','))
          if (patch.projects !== undefined) put('project', patch.projects.join(','))
          if (patch.q !== undefined) put('q', patch.q)
          if (patch.team !== undefined) put('team', patch.team)
          if (patch.area !== undefined) put('area', patch.area)
          if (patch.minAi !== undefined) put('min_ai', patch.minAi > 0 ? String(patch.minAi) : '')
          if (patch.perPage !== undefined) put('per_page', String(patch.perPage))
          // «Авто» — состояние по умолчанию, в URL его не пишем.
          if (patch.granularity !== undefined) {
            put('g', patch.granularity === 'auto' ? '' : patch.granularity)
          }
          if (patch.page !== undefined) {
            if (patch.page > 1) next.set('page', String(patch.page))
            else next.delete('page')
          } else if (
            patch.sources !== undefined ||
            patch.types !== undefined ||
            patch.projects !== undefined ||
            patch.q !== undefined ||
            patch.from !== undefined ||
            patch.to !== undefined ||
            patch.person !== undefined
          ) {
            // смена фильтра сбрасывает пагинацию
            next.delete('page')
          }
          return next
        },
        { replace: options?.replace ?? false },
      )
    },
    [setSearchParams],
  )

  /** Строка search-params для ссылок между страницами (без page). */
  const linkSearch = useMemo(() => {
    const next = new URLSearchParams(searchParams)
    next.delete('page')
    const s = next.toString()
    return s ? `?${s}` : ''
  }, [searchParams])

  return { filters, setFilters, linkSearch, tz: TIMEZONE }
}
