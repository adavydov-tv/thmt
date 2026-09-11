import { useMemo, type ReactNode } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts'
import { ChartCard } from './ChartCard'
import { ChartTooltip } from './ChartTooltip'
import { Legend, type LegendEntry } from './Legend'
import { EmptyState } from './EmptyState'
import { useChartTokens } from '../lib/chartTheme'
import { fmtBucket, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { Granularity, TimelinePoint } from '../lib/types'

interface TimelineChartProps {
  title: string
  subtitle?: ReactNode
  points: TimelinePoint[]
  /** Ключи серий (источники или типы). Порядок влияет только на укладку стека. */
  seriesKeys: string[]
  colorOf: (key: string) => string
  labelOf: (key: string) => string
  granularity: Granularity
  actions?: ReactNode
  emptyHint?: string
}

interface Row {
  bucket: string
  label: string
  total: number
  [key: string]: string | number
}

export function TimelineChart({
  title,
  subtitle,
  points,
  seriesKeys,
  colorOf,
  labelOf,
  granularity,
  actions,
  emptyHint,
}: TimelineChartProps) {
  const t = useChartTokens()
  const { t: tr } = useT()

  const rows = useMemo<Row[]>(
    () =>
      points.map((p) => {
        const row: Row = { bucket: p.bucket, label: fmtBucket(p.bucket, granularity), total: p.total }
        for (const key of seriesKeys) {
          row[key] = p.counts?.[key] ?? 0
        }
        return row
      }),
    [points, seriesKeys, granularity],
  )

  const legend = useMemo<LegendEntry[]>(
    () => seriesKeys.map((key) => ({ key, label: labelOf(key), color: colorOf(key) })),
    [seriesKeys, colorOf, labelOf],
  )

  const single = seriesKeys.length === 1

  const tooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const point = props.payload[0]?.payload as Row | undefined
    if (!point) return null
    const tooltipRows = seriesKeys
      .map((key) => ({ name: labelOf(key), value: Number(point[key] ?? 0), color: colorOf(key) }))
      .filter((r) => r.value > 0)
    return (
      <ChartTooltip
        title={point.label}
        rows={tooltipRows.length ? tooltipRows : [{ name: tr('chart.events'), value: 0, color: t.textMuted }]}
        total={single ? undefined : point.total}
        totalLabel={tr('chart.totalEvents')}
      />
    )
  }

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('chart.tableCaption', { title })}</caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.period')}</th>
          {seriesKeys.map((key) => (
            <th scope="col" className="num" key={key}>
              {labelOf(key)}
            </th>
          ))}
          <th scope="col" className="num">
            {tr('chart.total')}
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.bucket}>
            <th scope="row" style={{ fontWeight: 400 }}>
              {row.label}
            </th>
            {seriesKeys.map((key) => (
              <td className="num" key={key}>
                {fmtNumber(Number(row[key] ?? 0))}
              </td>
            ))}
            <td className="num">{fmtNumber(row.total)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  const axisTick = { fill: t.textMuted, fontSize: 11, fontVariantNumeric: 'tabular-nums' } as const

  return (
    <ChartCard title={title} subtitle={subtitle} actions={actions} table={rows.length ? table : undefined}>
      {rows.length === 0 ? (
        <EmptyState
          title={tr('chart.noEvents')}
          description={emptyHint ?? tr('chart.noEventsHint')}
        />
      ) : (
        <>
          <ResponsiveContainer width="100%" height={280}>
            {single ? (
              <LineChart data={rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
                <CartesianGrid stroke={t.grid} strokeWidth={1} vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={axisTick}
                  tickLine={false}
                  axisLine={{ stroke: t.axis }}
                  minTickGap={24}
                />
                <YAxis
                  tick={axisTick}
                  tickLine={false}
                  axisLine={false}
                  width={44}
                  allowDecimals={false}
                />
                <Tooltip cursor={{ stroke: t.grid, strokeWidth: 1 }} content={tooltip} />
                <Line
                  type="monotone"
                  dataKey={seriesKeys[0]}
                  name={labelOf(seriesKeys[0])}
                  stroke={colorOf(seriesKeys[0])}
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  dot={false}
                  activeDot={{ r: 4, strokeWidth: 2, stroke: t.surface }}
                />
              </LineChart>
            ) : (
              <AreaChart data={rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
                <CartesianGrid stroke={t.grid} strokeWidth={1} vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={axisTick}
                  tickLine={false}
                  axisLine={{ stroke: t.axis }}
                  minTickGap={24}
                />
                <YAxis
                  tick={axisTick}
                  tickLine={false}
                  axisLine={false}
                  width={44}
                  allowDecimals={false}
                />
                <Tooltip cursor={{ stroke: t.grid, strokeWidth: 1 }} content={tooltip} />
                {seriesKeys.map((key) => (
                  <Area
                    key={key}
                    type="monotone"
                    dataKey={key}
                    name={labelOf(key)}
                    stackId="events"
                    fill={colorOf(key)}
                    fillOpacity={1}
                    /* зазор 2px цветом поверхности между сегментами стека */
                    stroke={t.surface}
                    strokeWidth={2}
                    activeDot={{ r: 4, strokeWidth: 2, stroke: t.surface, fill: colorOf(key) }}
                  />
                ))}
              </AreaChart>
            )}
          </ResponsiveContainer>
          <Legend entries={legend} />
        </>
      )}
    </ChartCard>
  )
}
