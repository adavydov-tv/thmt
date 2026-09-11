import { useMemo, type ReactNode } from 'react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts'
import { ChartCard } from './ChartCard'
import { ChartTooltip } from './ChartTooltip'
import { EmptyState } from './EmptyState'
import { useChartTokens } from '../lib/chartTheme'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { CountItem } from '../lib/types'

interface TypeBreakdownProps {
  title: string
  subtitle?: ReactNode
  items: CountItem[]
  /** Одна серия — один цвет на все столбики; ramp по значению запрещён. */
  color?: string
  /** Цвет конкретного столбика (например, по группе категории). */
  itemColor?: (item: CountItem) => string
  /** Подпись столбца в таблице и тултипе. */
  categoryLabel?: string
  valueLabel?: string
  limit?: number
  actions?: ReactNode
  onSelect?: (item: CountItem) => void
  emptyTitle?: string
}

/** Горизонтальные столбики для номинальных категорий: типы событий, проекты, каналы. */
export function TypeBreakdown({
  title,
  subtitle,
  items,
  color,
  itemColor,
  categoryLabel,
  valueLabel,
  limit = 10,
  actions,
  onSelect,
  emptyTitle,
}: TypeBreakdownProps) {
  const t = useChartTokens()
  const { t: tr } = useT()
  const barColor = color ?? t.slot(0)
  const catLabel = categoryLabel ?? tr('chart.category')
  const valLabel = valueLabel ?? tr('chart.events')

  const data = useMemo(
    () =>
      items
        .filter((i) => i.count > 0)
        .slice(0, limit)
        .map((i) => ({ key: i.key, label: i.label || i.key, count: i.count, raw: i })),
    [items, limit],
  )

  const tooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const point = props.payload[0]?.payload as (typeof data)[number] | undefined
    if (!point) return null
    return (
      <ChartTooltip
        title={point.label}
        rows={[{ name: valLabel, value: point.count, color: itemColor ? itemColor(point.raw) : barColor }]}
      />
    )
  }

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('chart.tableCaption', { title })}</caption>
      <thead>
        <tr>
          <th scope="col">{catLabel}</th>
          <th scope="col" className="num">
            {valLabel}
          </th>
        </tr>
      </thead>
      <tbody>
        {data.map((d) => (
          <tr key={d.key}>
            <th scope="row" style={{ fontWeight: 400 }}>
              {d.label}
            </th>
            <td className="num">{fmtNumber(d.count)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  const axisTick = { fill: t.textMuted, fontSize: 11, fontVariantNumeric: 'tabular-nums' } as const
  const height = Math.max(180, data.length * 30 + 24)

  return (
    <ChartCard title={title} subtitle={subtitle} actions={actions} table={data.length ? table : undefined}>
      {data.length === 0 ? (
        <EmptyState title={emptyTitle ?? tr('common.noData')} description={tr('chart.noRecords')} />
      ) : (
        <ResponsiveContainer width="100%" height={height}>
          <BarChart
            data={data}
            layout="vertical"
            margin={{ top: 4, right: 28, bottom: 4, left: 4 }}
            barCategoryGap="28%"
          >
            <CartesianGrid stroke={t.grid} strokeWidth={1} horizontal={false} />
            <XAxis type="number" tick={axisTick} tickLine={false} axisLine={{ stroke: t.axis }} allowDecimals={false} />
            <YAxis
              type="category"
              dataKey="label"
              tick={{ fill: t.textSecondary, fontSize: 12 }}
              tickLine={false}
              axisLine={false}
              width={140}
            />
            <Tooltip cursor={{ fill: t.grid, fillOpacity: 0.4 }} content={tooltip} />
            <Bar
              dataKey="count"
              name={valLabel}
              fill={barColor}
              maxBarSize={24}
              /* скругление 4px на конце данных, у baseline — прямой угол */
              radius={[0, 4, 4, 0]}
              isAnimationActive={false}
              onClick={onSelect ? (payload: { raw?: CountItem }) => payload?.raw && onSelect(payload.raw) : undefined}
              cursor={onSelect ? 'pointer' : undefined}
            >
              {data.map((d) => (
                <Cell key={d.key} fill={itemColor ? itemColor(d.raw) : barColor} />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      )}
    </ChartCard>
  )
}
