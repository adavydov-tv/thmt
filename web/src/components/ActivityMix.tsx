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
import { fmtDecimal, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { CountItem } from '../lib/types'

const OTHER_KEY = '__other__'

interface ActivityMixProps {
  title?: string
  /** Типы событий по всем источникам, `useBreakdown(q, 'type')`. */
  items: CountItem[]
  /** Сколько типов показываем отдельными столбцами. */
  limit?: number
  actions?: ReactNode
}

interface Row {
  key: string
  label: string
  count: number
  /** Источник типа: часть ключа до первой точки; null у строки «Прочее». */
  source: string | null
  color: string
}

/** Источник кодируется префиксом типа: `jira.comment` → `jira`. */
function sourceOf(typeKey: string): string {
  const dot = typeKey.indexOf('.')
  return dot > 0 ? typeKey.slice(0, dot) : typeKey
}

/**
 * Из чего складывается активность: топ типов событий по всем источникам сразу.
 * Цвет столбца — цвет источника, так на одном графике видно, кто даёт объём.
 */
export function ActivityMix({ title, items, limit = 10, actions }: ActivityMixProps) {
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const cardTitle = title ?? tr('chart.mixTitle')

  const { rows, hiddenTypes, total } = useMemo(() => {
    const positive = items.filter((i) => i.count > 0).sort((a, b) => b.count - a.count)
    const sum = positive.reduce((acc, i) => acc + i.count, 0)
    // Схлопывать один-два типа в безымянное «Прочее» бессмысленно: строка та же,
    // а название категории теряется. Сворачиваем, только когда это правда экономит место.
    const cut = positive.length - limit >= 3 ? limit : positive.length
    const top = positive.slice(0, cut)
    const rest = positive.slice(cut)

    const list: Row[] = top.map((i) => {
      const source = sourceOf(i.key)
      return {
        key: i.key,
        label: i.label || i.key,
        count: i.count,
        source,
        color: t.sourceColor(source),
      }
    })

    // Хвост схлопываем, но не молча: сколько типов свёрнуто — в подзаголовке.
    if (rest.length > 0) {
      list.push({
        key: OTHER_KEY,
        label: tr('chart.other'),
        count: rest.reduce((acc, i) => acc + i.count, 0),
        source: null,
        color: t.textMuted,
      })
    }

    return { rows: list, hiddenTypes: rest.length, total: sum }
  }, [items, limit, t, tr])

  const legend = useMemo(() => {
    const seen = new Set<string>()
    for (const row of rows) {
      if (row.source) seen.add(row.source)
    }
    const ordered = [
      ...SOURCE_KEYS.filter((k) => seen.has(k)),
      ...[...seen].filter((k) => !(SOURCE_KEYS as string[]).includes(k)).sort(),
    ]
    const entries = ordered.map((key) => ({
      key,
      label: sourceLabel(key),
      color: t.sourceColor(key),
    }))
    if (rows.some((row) => row.key === OTHER_KEY)) {
      entries.push({ key: OTHER_KEY, label: tr('chart.otherSources'), color: t.textMuted })
    }
    return entries
  }, [rows, t, tr])

  const subtitle =
    hiddenTypes > 0
      ? tr('chart.mixCollapsed', {
          limit,
          n: fmtNumber(hiddenTypes),
          rest: tp('chart.pluralCollapsed', hiddenTypes),
          other: tr('chart.other'),
        })
      : tr('chart.mixAllTypes')

  const tooltip = (props: TooltipProps<number, string>) => {
    if (!props.active || !props.payload?.length) return null
    const row = props.payload[0]?.payload as Row | undefined
    if (!row) return null
    const share = total > 0 ? (row.count / total) * 100 : 0
    return (
      <ChartTooltip
        title={row.source ? `${row.label} · ${sourceLabel(row.source)}` : row.label}
        rows={[{ name: tr('chart.events'), value: row.count, color: row.color }]}
        totalLabel={tr('chart.shareOfAll')}
        total={Number(share.toFixed(1))}
      />
    )
  }

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('chart.tableCaption', { title: cardTitle })}</caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.type')}</th>
          <th scope="col">{tr('chart.source')}</th>
          <th scope="col" className="num">
            {tr('chart.count')}
          </th>
          <th scope="col" className="num">
            {tr('chart.shareOfAll')}
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.key}>
            <th scope="row" style={{ fontWeight: 400 }}>
              {row.label}
            </th>
            <td>{row.source ? sourceLabel(row.source) : tr('chart.mixed')}</td>
            <td className="num">{fmtNumber(row.count)}</td>
            <td className="num">{fmtDecimal(total > 0 ? (row.count / total) * 100 : 0)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  const axisTick = { fill: t.textMuted, fontSize: 11, fontVariantNumeric: 'tabular-nums' } as const
  const height = Math.max(200, rows.length * 30 + 24)

  return (
    <ChartCard title={cardTitle} subtitle={subtitle} actions={actions} table={rows.length ? table : undefined}>
      {rows.length === 0 ? (
        <EmptyState title={tr('chart.noTypes')} description={tr('chart.noRecords')} />
      ) : (
        <>
          <ResponsiveContainer width="100%" height={height}>
            <BarChart
              data={rows}
              layout="vertical"
              margin={{ top: 4, right: 56, bottom: 4, left: 4 }}
              barCategoryGap="28%"
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
                width={170}
              />
              <Tooltip cursor={{ fill: t.grid, fillOpacity: 0.4 }} content={tooltip} />
              <Bar
                dataKey="count"
                name={tr('chart.events')}
                maxBarSize={24}
                radius={[0, 4, 4, 0]}
                isAnimationActive={false}
              >
                {rows.map((row) => (
                  <Cell key={row.key} fill={row.color} />
                ))}
                <LabelList content={<CountLabel rows={rows} textFill={t.textSecondary} />} />
              </Bar>
            </BarChart>
          </ResponsiveContainer>
          <ul className="legend" style={{ listStyle: 'none', margin: 0, padding: '10px 0 0' }}>
            {legend.map((entry) => (
              <li className="legend-item" key={entry.key}>
                <span className="dot" style={{ background: entry.color }} aria-hidden="true" />
                <span>{entry.label}</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </ChartCard>
  )
}

interface CountLabelProps {
  /* Приходят от recharts при клонировании content-элемента. */
  x?: number | string
  y?: number | string
  width?: number | string
  height?: number | string
  index?: number
  viewBox?: { x?: number; y?: number; width?: number; height?: number }
  rows?: Row[]
  /** Не `fill`: recharts подставляет в клон цвет столбца и затёр бы токен текста. */
  textFill?: string
}

/** Прямая подпись с количеством справа от столбца. */
function CountLabel({ x, y, width, height, index, viewBox, rows, textFill }: CountLabelProps) {
  const row = rows && index !== undefined ? rows[index] : undefined
  if (!row) return null

  const left = Number(viewBox?.x ?? x)
  const top = Number(viewBox?.y ?? y)
  const w = Number(viewBox?.width ?? width)
  const h = Number(viewBox?.height ?? height)
  if (!Number.isFinite(left) || !Number.isFinite(top) || !Number.isFinite(w) || !Number.isFinite(h)) {
    return null
  }

  return (
    <text
      x={left + w + 8}
      y={top + h / 2}
      fill={textFill}
      fontSize={11}
      dominantBaseline="central"
      style={{ fontVariantNumeric: 'tabular-nums' }}
    >
      {fmtNumber(row.count)}
    </text>
  )
}
