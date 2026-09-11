import { useMemo, type ReactNode } from 'react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  LabelList,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts'
import { ChartCard } from './ChartCard'
import { ChartTooltip } from './ChartTooltip'
import { EmptyState } from './EmptyState'
import { SOURCE_KEYS, sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtNumber, fmtPercent } from '../lib/format'
import { useT } from '../i18n'
import type { CountItem } from '../lib/types'

const TOTAL_KEY = '__total__'

/** Приглушение прошлого периода: та же краска, меньшая непрозрачность. */
const PREV_OPACITY = 0.35

interface PeriodComparisonProps {
  title?: string
  /** Текущий период, `summary.by_source`. */
  items: CountItem[]
  /** Предыдущий период равной длительности, `summary.prev_by_source`. */
  prevItems: CountItem[]
  total: number
  prevTotal: number
  /** Границы текущего периода — по ним считаем предыдущий. */
  from: string
  to: string
  actions?: ReactNode
}

interface Row {
  key: string
  label: string
  current: number
  previous: number
  color: string
  /** Изменение в процентах; null — сравнивать не с чем (в прошлом нули). */
  delta: number | null
}

/** Предыдущий период равной длительности: [from − (to − from), from). */
function prevRange(from: string, to: string): { from: string; to: string } | null {
  const start = new Date(from).getTime()
  const end = new Date(to).getTime()
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null
  const span = end - start
  return { from: new Date(start - span).toISOString(), to: new Date(start - 1).toISOString() }
}

function toMap(items: CountItem[]): Map<string, CountItem> {
  const map = new Map<string, CountItem>()
  for (const item of items) map.set(item.key, item)
  return map
}

function deltaOf(current: number, previous: number): number | null {
  if (previous <= 0) return null
  return ((current - previous) / previous) * 100
}

/**
 * Сравнение текущего периода с предыдущим по каждому источнику.
 * Горизонтальные сгруппированные столбцы, одна общая шкала событий:
 * вторая ось Y превратила бы сравнение в оптическую иллюзию.
 */
export function PeriodComparison({
  title,
  items,
  prevItems,
  total,
  prevTotal,
  from,
  to,
  actions,
}: PeriodComparisonProps) {
  const t = useChartTokens()
  const { t: tr } = useT()
  const cardTitle = title ?? tr('chart.periodCompareTitle')
  const currentLabel = tr('chart.currentPeriod')
  const prevLabel = tr('chart.prevPeriod')

  const rows = useMemo<Row[]>(() => {
    const currentMap = toMap(items)
    const prevMap = toMap(prevItems)
    const present = new Set<string>([...currentMap.keys(), ...prevMap.keys()])
    const ordered = [
      ...SOURCE_KEYS.filter((k) => present.has(k)),
      ...[...present].filter((k) => !(SOURCE_KEYS as string[]).includes(k)).sort(),
    ]

    const sourceRows = ordered
      .map<Row>((key) => {
        const current = currentMap.get(key)?.count ?? 0
        const previous = prevMap.get(key)?.count ?? 0
        return {
          key,
          label: currentMap.get(key)?.label || prevMap.get(key)?.label || sourceLabel(key),
          current,
          previous,
          color: t.sourceColor(key),
          delta: deltaOf(current, previous),
        }
      })
      .filter((row) => row.current > 0 || row.previous > 0)

    // «Всего» намеренно не столбец: сумма кратно больше любого источника и,
    // попав на общую шкалу, сплющила бы все остальные группы. Итог показан
    // строкой над графиком.
    return sourceRows
  }, [items, prevItems, t])

  const totalRow = useMemo<Row>(
    () => ({
      key: TOTAL_KEY,
      label: tr('chart.total'),
      current: total,
      previous: prevTotal,
      color: t.textMuted,
      delta: deltaOf(total, prevTotal),
    }),
    [total, prevTotal, t, tr],
  )

  const range = prevRange(from, to)
  const subtitle = range
    ? tr('chart.periodCompareRange', {
        from: fmtDate(from),
        to: fmtDate(to),
        prevFrom: fmtDate(range.from),
        prevTo: fmtDate(range.to),
      })
    : tr('chart.periodCompareHint')

  const hasPrev = prevTotal > 0 || prevItems.some((i) => i.count > 0)

  const tooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const row = props.payload[0]?.payload as Row | undefined
    if (!row) return null
    return (
      <ChartTooltip
        title={row.label}
        rows={[
          { name: currentLabel, value: row.current, color: row.color },
          { name: prevLabel, value: row.previous, color: row.color },
        ]}
      />
    )
  }

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('chart.tableCaption', { title: cardTitle })}</caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.source')}</th>
          <th scope="col" className="num">
            {tr('chart.current')}
          </th>
          <th scope="col" className="num">
            {tr('chart.previous')}
          </th>
          <th scope="col" className="num">
            {tr('chart.change')}
          </th>
        </tr>
      </thead>
      <tbody>
        {[totalRow, ...rows].map((row) => (
          <tr key={row.key}>
            <th scope="row" style={{ fontWeight: row.key === TOTAL_KEY ? 600 : 400 }}>
              {row.label}
            </th>
            <td className="num">{fmtNumber(row.current)}</td>
            <td className="num">{fmtNumber(row.previous)}</td>
            <td className="num">{row.delta === null ? '—' : fmtPercent(row.delta)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  const axisTick = { fill: t.textMuted, fontSize: 11, fontVariantNumeric: 'tabular-nums' } as const
  const height = Math.max(200, rows.length * 54 + 24)

  return (
    <ChartCard title={cardTitle} subtitle={subtitle} actions={actions} table={hasPrev ? table : undefined}>
      {!hasPrev ? (
        <EmptyState
          title={tr('chart.nothingToCompare')}
          description={tr('chart.nothingToCompareHint')}
          showSyncLink={false}
        />
      ) : (
        <>
          <p className="compare-total">
            <span className="muted">{tr('chart.totalForPeriod')}</span>{' '}
            <strong className="num">{fmtNumber(totalRow.current)}</strong>{' '}
            <span className="muted">{tr('chart.versus')}</span>{' '}
            <span className="num">{fmtNumber(totalRow.previous)}</span>
            {totalRow.delta !== null && (
              <>
                {' '}
                <span
                  className="num"
                  style={{ color: totalRow.delta >= 0 ? t.positive : t.negative }}
                >
                  {fmtPercent(totalRow.delta)}
                </span>
              </>
            )}
          </p>
          <ResponsiveContainer width="100%" height={height}>
            <BarChart
              data={rows}
              layout="vertical"
              margin={{ top: 4, right: 72, bottom: 4, left: 4 }}
              barCategoryGap="26%"
              barGap={2}
            >
              <CartesianGrid stroke={t.grid} strokeWidth={1} horizontal={false} />
              <XAxis
                type="number"
                tick={axisTick}
                tickLine={false}
                axisLine={{ stroke: t.axis }}
                allowDecimals={false}
              />
              <YAxis
                type="category"
                dataKey="label"
                tick={{ fill: t.textSecondary, fontSize: 12 }}
                tickLine={false}
                axisLine={false}
                width={110}
              />
              <Tooltip cursor={{ fill: t.grid, fillOpacity: 0.4 }} content={tooltip} />
              <Bar
                dataKey="current"
                name={currentLabel}
                maxBarSize={16}
                radius={[0, 4, 4, 0]}
                isAnimationActive={false}
              >
                {rows.map((row) => (
                  <Cell key={row.key} fill={row.color} />
                ))}
                <LabelList
                  content={
                    <DeltaLabel
                      rows={rows}
                      positive={t.positive}
                      negative={t.negative}
                      muted={t.textMuted}
                    />
                  }
                />
              </Bar>
              <Bar
                dataKey="previous"
                name={prevLabel}
                maxBarSize={16}
                radius={[0, 4, 4, 0]}
                fillOpacity={PREV_OPACITY}
                isAnimationActive={false}
              >
                {rows.map((row) => (
                  <Cell key={row.key} fill={row.color} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
          <ul className="legend" style={{ listStyle: 'none', margin: 0, padding: '10px 0 0' }}>
            <li className="legend-item">
              <span className="dot" style={{ background: t.textMuted }} aria-hidden="true" />
              <span>{currentLabel}</span>
            </li>
            <li className="legend-item">
              <span
                className="dot"
                style={{ background: t.textMuted, opacity: PREV_OPACITY }}
                aria-hidden="true"
              />
              <span>{prevLabel}</span>
            </li>
            <li className="legend-item muted">{tr('chart.periodCompareLegend')}</li>
          </ul>
        </>
      )}
    </ChartCard>
  )
}

interface DeltaLabelProps {
  /* Приходят от recharts при клонировании content-элемента. */
  x?: number | string
  y?: number | string
  width?: number | string
  height?: number | string
  index?: number
  viewBox?: { x?: number; y?: number; width?: number; height?: number }
  rows?: Row[]
  positive?: string
  negative?: string
  muted?: string
}

/** Прямая подпись с дельтой справа от столбца текущего периода. */
function DeltaLabel({
  x,
  y,
  width,
  height,
  index,
  viewBox,
  rows,
  positive,
  negative,
  muted,
}: DeltaLabelProps) {
  const row = rows && index !== undefined ? rows[index] : undefined
  if (!row) return null

  const left = Number(viewBox?.x ?? x)
  const top = Number(viewBox?.y ?? y)
  const w = Number(viewBox?.width ?? width)
  const h = Number(viewBox?.height ?? height)
  if (!Number.isFinite(left) || !Number.isFinite(top) || !Number.isFinite(w) || !Number.isFinite(h)) {
    return null
  }

  const text = row.delta === null ? '—' : fmtPercent(row.delta)
  const fill = row.delta === null || row.delta === 0 ? muted : row.delta > 0 ? positive : negative

  return (
    <text
      x={left + w + 8}
      y={top + h / 2}
      fill={fill}
      fontSize={11}
      fontWeight={600}
      dominantBaseline="central"
      style={{ fontVariantNumeric: 'tabular-nums' }}
    >
      {text}
    </text>
  )
}
