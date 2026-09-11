import { useEffect, useMemo } from 'react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts'
import { ChartTooltip } from './ChartTooltip'
import { EventRow } from './EventRow'
import { EmptyState } from './EmptyState'
import { ErrorState } from './ErrorState'
import { Legend, type LegendEntry } from './Legend'
import { SkeletonLines } from './Skeleton'
import { SOURCE_KEYS, sourceLabel, useChartTokens } from '../lib/chartTheme'
import { useEvents, useMeta } from '../lib/queries'
import { useStatsQuery, useTypeLabeler } from '../lib/useStatsQuery'
import { useFilters } from '../lib/useFilters'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import { pick } from '../i18n/lang'

/** Потолок бэкенда на per_page — больше событий за день одним запросом не отдать. */
export const DAY_EVENTS_LIMIT = 500

/** Границы локального дня для запроса событий (RFC3339). */
export function dayRangeISO(date: Date): { from: string; to: string } {
  const from = new Date(date)
  from.setHours(0, 0, 0, 0)
  const to = new Date(date)
  to.setHours(23, 59, 59, 999)
  return { from: from.toISOString(), to: to.toISOString() }
}

interface DayDetailProps {
  date: Date
  /** Подпись особого дня («выходной», «отпуск», …) — уже локализованная. */
  kindLabel?: string
  onClose: () => void
}

interface HourRow {
  hour: number
  label: string
  total: number
  [source: string]: string | number
}

/**
 * Модальное окно дня из матрицы активности: список событий в хронологическом
 * порядке и почасовой таймлайн внизу, с теми же фильтрами, что и вся страница.
 */
export function DayDetail({ date, kindLabel, onClose }: DayDetailProps) {
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const { linkSearch } = useFilters()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)
  const q = useStatsQuery()

  const range = useMemo(() => dayRangeISO(date), [date])
  const events = useEvents({
    ...q,
    from: range.from,
    to: range.to,
    page: 1,
    per_page: DAY_EVENTS_LIMIT,
    sort: 'asc',
  })

  // Esc закрывает окно; на время показа блокируем прокрутку страницы.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', onKey)
      document.body.style.overflow = prev
    }
  }, [onClose])

  const title = useMemo(
    () =>
      new Intl.DateTimeFormat(pick('ru-RU', 'en-US'), {
        weekday: 'long',
        day: 'numeric',
        month: 'long',
        year: 'numeric',
      }).format(date),
    [date],
  )

  const items = events.data?.items ?? []
  const total = events.data?.total ?? 0

  // Почасовые корзины с разбивкой по источникам — для таймлайна внизу.
  const { rows, seriesKeys } = useMemo(() => {
    const present = new Set<string>()
    const byHour = new Map<number, Record<string, number>>()
    for (const e of items) {
      const d = new Date(e.occurred_at)
      if (Number.isNaN(d.getTime())) continue
      const h = d.getHours()
      const bucket = byHour.get(h) ?? {}
      bucket[e.source] = (bucket[e.source] ?? 0) + 1
      byHour.set(h, bucket)
      present.add(e.source)
    }
    const ordered = SOURCE_KEYS.filter((k) => present.has(k))
    const extra = [...present].filter((k) => !(SOURCE_KEYS as string[]).includes(k)).sort()
    const keys = [...ordered, ...extra]
    const out: HourRow[] = []
    for (let h = 0; h < 24; h += 1) {
      const bucket = byHour.get(h) ?? {}
      const row: HourRow = { hour: h, label: String(h), total: 0 }
      for (const key of keys) {
        const n = bucket[key] ?? 0
        row[key] = n
        row.total += n
      }
      out.push(row)
    }
    return { rows: out, seriesKeys: keys }
  }, [items])

  const legend = useMemo<LegendEntry[]>(
    () => seriesKeys.map((key) => ({ key, label: sourceLabel(key), color: t.sourceColor(key) })),
    [seriesKeys, t],
  )

  const chartTooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const row = props.payload[0]?.payload as HourRow | undefined
    if (!row) return null
    const pad = (n: number) => String(n).padStart(2, '0')
    const tooltipRows = seriesKeys
      .map((key) => ({ name: sourceLabel(key), value: Number(row[key] ?? 0), color: t.sourceColor(key) }))
      .filter((r) => r.value > 0)
    return (
      <ChartTooltip
        title={`${pad(row.hour)}:00–${pad((row.hour + 1) % 24)}:00`}
        rows={tooltipRows.length ? tooltipRows : [{ name: tr('chart.events'), value: 0, color: t.textMuted }]}
        total={tooltipRows.length > 1 ? row.total : undefined}
        totalLabel={tr('chart.totalEvents')}
      />
    )
  }

  const axisTick = { fill: t.textMuted, fontSize: 11, fontVariantNumeric: 'tabular-nums' } as const

  return (
    <div className="daymodal-backdrop" onClick={onClose}>
      <div
        className="card daymodal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="daymodal-head">
          <div className="daymodal-titles">
            <h2>{title}</h2>
            <div className="daymodal-sub num">
              {kindLabel && <span className="badge">{kindLabel}</span>}
              {events.data && (
                <span>
                  {fmtNumber(total)} {tp('plural.events', total)}
                </span>
              )}
            </div>
          </div>
          <button type="button" className="btn btn-sm" onClick={onClose}>
            {tr('cal.close')}
          </button>
        </header>

        <div className="daymodal-body">
          {events.isError ? (
            <div className="panel-body">
              <ErrorState error={events.error} onRetry={() => void events.refetch()} />
            </div>
          ) : events.isLoading ? (
            <div className="panel-body">
              <SkeletonLines count={6} height={28} />
            </div>
          ) : items.length === 0 ? (
            <EmptyState title={tr('chart.noEvents')} />
          ) : (
            <>
              {total > items.length && (
                <div className="daymodal-note">
                  {tr('cal.truncated', { n: fmtNumber(items.length), ev: tp('plural.events', items.length) })}
                </div>
              )}
              <div className="event-list">
                {items.map((e) => (
                  <EventRow key={String(e.id)} event={e} typeLabel={typeLabel} search={linkSearch} />
                ))}
              </div>
            </>
          )}
        </div>

        {items.length > 0 && (
          <footer className="daymodal-foot">
            <div className="daymodal-foot-title">{tr('cal.dayTimeline')}</div>
            <ResponsiveContainer width="100%" height={150}>
              <BarChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid stroke={t.grid} strokeWidth={1} vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={axisTick}
                  tickLine={false}
                  axisLine={{ stroke: t.axis }}
                  interval={2}
                />
                <YAxis tick={axisTick} tickLine={false} axisLine={false} width={32} allowDecimals={false} />
                <Tooltip cursor={{ fill: t.grid, fillOpacity: 0.4 }} content={chartTooltip} />
                {seriesKeys.map((key) => (
                  <Bar
                    key={key}
                    dataKey={key}
                    name={sourceLabel(key)}
                    stackId="events"
                    fill={t.sourceColor(key)}
                    maxBarSize={22}
                  />
                ))}
              </BarChart>
            </ResponsiveContainer>
            <Legend entries={legend} />
          </footer>
        )}
      </div>
    </div>
  )
}
