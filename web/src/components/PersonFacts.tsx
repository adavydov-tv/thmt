import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { useFilters, TIMEZONE } from '../lib/useFilters'
import { sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { HeatmapCell, Summary } from '../lib/types'

interface PersonFactsProps {
  summary?: Summary
  heatmap?: HeatmapCell[]
}

interface Fact {
  key: string
  label: string
  value: string
  hint?: string
  to?: string
  color?: string
}

/** Окно из span подряд идущих часов с максимумом событий: «самые активные часы». */
function peakWindow(byHour: number[], span: number): { from: number; to: number; share: number } | null {
  const total = byHour.reduce((a, b) => a + b, 0)
  if (total === 0) return null
  let best = 0
  let bestSum = -1
  for (let h = 0; h + span <= 24; h++) {
    const sum = byHour.slice(h, h + span).reduce((a, b) => a + b, 0)
    if (sum > bestSum) {
      bestSum = sum
      best = h
    }
  }
  return { from: best, to: best + span, share: bestSum / total }
}

/** Диапазон часов, в котором лежит основная активность (обрезаем хвосты по 5%). */
function observedHours(byHour: number[]): { from: number; to: number } | null {
  const total = byHour.reduce((a, b) => a + b, 0)
  if (total === 0) return null
  const cut = total * 0.05
  let lo = 0
  let acc = 0
  while (lo < 23 && acc + byHour[lo] <= cut) {
    acc += byHour[lo]
    lo++
  }
  let hi = 23
  acc = 0
  while (hi > lo && acc + byHour[hi] <= cut) {
    acc += byHour[hi]
    hi--
  }
  return { from: lo, to: hi + 1 }
}

const hh = (h: number) => `${String(h).padStart(2, '0')}:00`

/**
 * Панель фактов о человеке над фильтрами: отклонения за период, активные и
 * наблюдаемые рабочие часы и другие важные факты из собранных данных.
 */
export function PersonFacts({ summary, heatmap }: PersonFactsProps) {
  const { filters, linkSearch } = useFilters()
  const { t, tp } = useT()
  const tokens = useChartTokens()

  const deviations = useQuery({
    queryKey: ['violations', 'person', filters.person, filters.from, filters.to] as const,
    queryFn: () =>
      api.violations({
        from: filters.from,
        to: filters.to,
        tz: TIMEZONE,
        person_key: filters.person,
      }),
    enabled: Boolean(filters.person),
    staleTime: 5 * 60_000,
  })

  const facts = useMemo<Fact[]>(() => {
    const out: Fact[] = []

    const items = deviations.data?.violations ?? []
    const warns = items.filter((v) => v.severity === 'warn').length
    const infos = items.length - warns
    out.push({
      key: 'deviations',
      label: t('facts.deviations'),
      value: items.length === 0 ? t('facts.none') : `${warns} ⚠ · ${infos} ✓`,
      hint: t('facts.deviationsHint'),
      to: `/violations${linkSearch}${linkSearch ? '&' : '?'}vperson=${encodeURIComponent(filters.person)}`,
      color: warns > 0 ? tokens.negative : tokens.positive,
    })

    // Почасовое распределение — только по будням: выходные искажают картину.
    const byHour = Array.from({ length: 24 }, () => 0)
    for (const c of heatmap ?? []) {
      if (c.weekday <= 4) byHour[c.hour] += c.count
    }
    const peak = peakWindow(byHour, 3)
    if (peak) {
      out.push({
        key: 'peak',
        label: t('facts.peakHours'),
        value: `${hh(peak.from)}–${hh(peak.to)}`,
        hint: t('facts.peakHoursHint', { share: Math.round(peak.share * 100) }),
      })
    }
    const observed = observedHours(byHour)
    if (observed) {
      out.push({
        key: 'observed',
        label: t('facts.observedHours'),
        value: `${hh(observed.from)}–${hh(observed.to)}`,
        hint: t('facts.observedHoursHint'),
      })
    }

    if (summary) {
      if (summary.active_working_days > 0) {
        const perDay = summary.total_events / summary.active_working_days
        out.push({
          key: 'perday',
          label: t('facts.perDay'),
          value: perDay.toFixed(1),
          hint: t('facts.perDayHint'),
        })
      }
      const topSource = [...(summary.by_source ?? [])].sort((a, b) => b.count - a.count)[0]
      if (topSource && topSource.count > 0) {
        out.push({
          key: 'topsource',
          label: t('facts.topSource'),
          value: sourceLabel(topSource.key),
          hint: `${fmtNumber(topSource.count)} ${tp('plural.events', topSource.count)} · ${Math.round(
            (topSource.count / Math.max(1, summary.total_events)) * 100,
          )}%`,
          color: tokens.sourceColor(topSource.key),
        })
      }
      const topProject = summary.top_projects?.[0]
      if (topProject && topProject.count > 0) {
        out.push({
          key: 'topproject',
          label: t('facts.topProject'),
          value: topProject.label || topProject.key,
          hint: `${fmtNumber(topProject.count)} ${tp('plural.events', topProject.count)}`,
        })
      }
      if (summary.meeting_hours > 0) {
        out.push({
          key: 'meetings',
          label: t('facts.meetingHours'),
          value: `≈${Math.round(summary.meeting_hours)} ${t('facts.hoursUnit')}`,
          hint: t('facts.meetingHoursHint'),
        })
      }
      if (summary.idle_working_days > 0) {
        out.push({
          key: 'idle',
          label: t('facts.idleDays'),
          value: String(summary.idle_working_days),
          hint: t('facts.idleDaysHint'),
          color: tokens.negative,
        })
      }
    }
    return out
  }, [deviations.data, heatmap, summary, filters.person, linkSearch, t, tp, tokens])

  if (!filters.person || facts.length === 0) return null

  return (
    <section className="card facts" aria-label={t('facts.aria')}>
      {facts.map((f) => {
        const body = (
          <>
            <span className="facts-label">{f.label}</span>
            <span className="facts-value" style={f.color ? { color: f.color } : undefined}>
              {f.value}
            </span>
            {f.hint && <span className="facts-hint">{f.hint}</span>}
          </>
        )
        return f.to ? (
          <Link key={f.key} className="facts-item facts-link" to={f.to} title={f.hint}>
            {body}
          </Link>
        ) : (
          <div key={f.key} className="facts-item" title={f.hint}>
            {body}
          </div>
        )
      })}
    </section>
  )
}
