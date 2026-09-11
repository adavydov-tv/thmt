import { useMemo } from 'react'
import { useFilters } from './useFilters'
import { useActivityTypes } from './activityTypes'
import type { StatsQuery } from './types'

/**
 * Собирает параметры запроса статистики из фильтров в URL и выбора
 * «что учитывать в активности» (см. activityTypes).
 */
export function useStatsQuery(overrides?: Partial<StatsQuery>): StatsQuery {
  const { filters, tz } = useFilters()
  const { effectiveTypes } = useActivityTypes()
  const { person, from, to, sources, types, projects, q, minAi } = filters
  const type = effectiveTypes(types)
  return useMemo<StatsQuery>(
    () => ({
      person_key: person,
      from,
      to,
      tz,
      source: sources.length ? sources : undefined,
      type,
      project: projects.length ? projects : undefined,
      q: q || undefined,
      min_ai: minAi > 0 ? minAi : undefined,
      ...overrides,
    }),
    // overrides передаются литералом — сериализуем для стабильности ключа
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [person, from, to, tz, sources.join(','), (type ?? []).join(','), projects.join(','), q, minAi, JSON.stringify(overrides ?? null)],
  )
}

/** Человекочитаемая подпись периода. */
export function useTypeLabeler(typeLabels: Record<string, string> | undefined) {
  return useMemo(() => (type: string) => typeLabels?.[type] ?? type, [typeLabels])
}
