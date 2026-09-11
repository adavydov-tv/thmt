import { useMemo, type ReactNode } from 'react'
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip, type TooltipProps } from 'recharts'
import { ChartCard } from './ChartCard'
import { ChartTooltip } from './ChartTooltip'
import { EmptyState } from './EmptyState'
import { useChartTokens, sourceLabel } from '../lib/chartTheme'
import { fmtDecimal, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { CountItem } from '../lib/types'

interface SourceBreakdownProps {
  title?: string
  subtitle?: ReactNode
  items: CountItem[]
  actions?: ReactNode
}

/** Донат по источникам: part-to-whole «с одного взгляда», ≤ 4 сегмента. */
export function SourceBreakdown({ title, subtitle, items, actions }: SourceBreakdownProps) {
  const t = useChartTokens()
  const { t: tr } = useT()
  const cardTitle = title ?? tr('chart.sourcesTitle')

  const data = useMemo(
    () =>
      items
        .filter((i) => i.count > 0)
        .map((i) => ({
          key: i.key,
          label: i.label || sourceLabel(i.key),
          count: i.count,
          color: t.sourceColor(i.key),
        })),
    [items, t],
  )

  const total = data.reduce((acc, i) => acc + i.count, 0)

  const tooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const point = props.payload[0]?.payload as (typeof data)[number] | undefined
    if (!point) return null
    const share = total > 0 ? (point.count / total) * 100 : 0
    return (
      <ChartTooltip
        title={point.label}
        rows={[{ name: tr('chart.events'), value: point.count, color: point.color }]}
        totalLabel={tr('chart.share')}
        total={Number(share.toFixed(1))}
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
            {tr('chart.events')}
          </th>
          <th scope="col" className="num">
            {tr('chart.sharePct')}
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
            <td className="num">{fmtDecimal(total > 0 ? (d.count / total) * 100 : 0)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  return (
    <ChartCard title={cardTitle} subtitle={subtitle} actions={actions} table={data.length ? table : undefined}>
      {data.length === 0 ? (
        <EmptyState title={tr('chart.noSourceData')} />
      ) : (
        <>
          <ResponsiveContainer width="100%" height={220}>
            <PieChart>
              <Tooltip content={tooltip} />
              <Pie
                data={data}
                dataKey="count"
                nameKey="label"
                innerRadius="58%"
                outerRadius="86%"
                paddingAngle={0}
                /* зазор 2px цветом поверхности между сегментами */
                stroke={t.surface}
                strokeWidth={2}
                isAnimationActive={false}
              >
                {data.map((d) => (
                  <Cell key={d.key} fill={d.color} />
                ))}
              </Pie>
            </PieChart>
          </ResponsiveContainer>
          <ul className="legend" style={{ listStyle: 'none', margin: 0, padding: '10px 0 0' }}>
            {data.map((d) => (
              <li className="legend-item" key={d.key}>
                <span className="dot" style={{ background: d.color }} aria-hidden="true" />
                <span>{d.label}</span>
                <span className="num" style={{ color: 'var(--text)', fontWeight: 600 }}>
                  {fmtNumber(d.count)}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </ChartCard>
  )
}
