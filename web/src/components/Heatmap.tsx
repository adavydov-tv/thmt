import { useMemo, useState, type ReactNode } from 'react'
import { ChartCard } from './ChartCard'
import { ChartTooltip } from './ChartTooltip'
import { EmptyState } from './EmptyState'
import { SEQUENTIAL, useChartTokens } from '../lib/chartTheme'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import { pick } from '../i18n/lang'
import type { HeatmapCell } from '../lib/types'

// Вызывается в рендере: приложение перемонтируется при смене языка.
function weekdayNames(): string[] {
  return pick(
    ['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'],
    ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'],
  )
}
const HOURS = Array.from({ length: 24 }, (_, h) => h)

interface HeatmapProps {
  title?: string
  subtitle?: ReactNode
  cells: HeatmapCell[]
  actions?: ReactNode
}

interface HoverState {
  weekday: number
  hour: number
  count: number
  x: number
  y: number
}

/** Тепловая карта «день недели × час». Секвенциальная шкала: один синий hue, светлое → тёмное. */
export function Heatmap({ title, subtitle, cells, actions }: HeatmapProps) {
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const [hover, setHover] = useState<HoverState | null>(null)
  const cardTitle = title ?? tr('chart.heatmapTitle')
  const weekdays = weekdayNames()

  const { matrix, max, total } = useMemo(() => {
    const m: number[][] = Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => 0))
    let mx = 0
    let sum = 0
    for (const c of cells) {
      if (c.weekday < 0 || c.weekday > 6 || c.hour < 0 || c.hour > 23) continue
      m[c.weekday][c.hour] += c.count
      sum += c.count
      if (m[c.weekday][c.hour] > mx) mx = m[c.weekday][c.hour]
    }
    return { matrix: m, max: mx, total: sum }
  }, [cells])

  const colorFor = (count: number) => {
    if (count <= 0) return t.surface
    if (max <= 0) return t.surface
    // нулевые ячейки — цвет поверхности, ненулевые начинаются со второго шага шкалы
    const ratio = count / max
    const idx = Math.min(SEQUENTIAL.length - 1, Math.max(0, Math.round(ratio * (SEQUENTIAL.length - 1))))
    return SEQUENTIAL[idx]
  }

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('chart.tableCaption', { title: cardTitle })}</caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.day')}</th>
          {HOURS.map((h) => (
            <th scope="col" className="num" key={h}>
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {weekdays.map((day, wd) => (
          <tr key={day}>
            <th scope="row" style={{ fontWeight: 400 }}>
              {day}
            </th>
            {HOURS.map((h) => (
              <td className="num" key={h}>
                {matrix[wd][h] || ''}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )

  return (
    <ChartCard title={cardTitle} subtitle={subtitle} actions={actions} table={total ? table : undefined}>
      {total === 0 ? (
        <EmptyState title={tr('chart.noActivity')} />
      ) : (
        <div style={{ position: 'relative' }} onMouseLeave={() => setHover(null)}>
          <div className="heatmap">
            <div aria-hidden="true" />
            {HOURS.map((h) => (
              <div className="heatmap-col-label" key={`h-${h}`} aria-hidden="true">
                {h % 3 === 0 ? h : ''}
              </div>
            ))}
            {weekdays.map((day, wd) => (
              <Row
                key={day}
                day={day}
                weekday={wd}
                counts={matrix[wd]}
                colorFor={colorFor}
                onHover={setHover}
              />
            ))}
          </div>

          <div className="heatmap-scale">
            <span>{tr('chart.less')}</span>
            <div className="heatmap-scale-steps" aria-hidden="true">
              <span className="heatmap-scale-step" style={{ background: t.surface }} />
              {SEQUENTIAL.filter((_, i) => i % 2 === 0).map((c) => (
                <span className="heatmap-scale-step" key={c} style={{ background: c }} />
              ))}
            </div>
            <span>{tr('chart.more')}</span>
            <span className="num" style={{ marginLeft: 'auto' }}>
              {tr('chart.maxPerHour', { n: fmtNumber(max), ev: tp('plural.events', max) })}
            </span>
          </div>

          {hover && (
            <div
              style={{
                position: 'absolute',
                left: Math.max(0, hover.x - 80),
                top: Math.max(0, hover.y - 76),
                pointerEvents: 'none',
                zIndex: 5,
              }}
            >
              <ChartTooltip
                title={`${weekdays[hover.weekday]}, ${String(hover.hour).padStart(2, '0')}:00–${String(
                  hover.hour,
                ).padStart(2, '0')}:59`}
                rows={[{ name: tr('chart.events'), value: hover.count, color: colorFor(hover.count) }]}
              />
            </div>
          )}
        </div>
      )}
    </ChartCard>
  )
}

interface RowProps {
  day: string
  weekday: number
  counts: number[]
  colorFor: (count: number) => string
  onHover: (state: HoverState | null) => void
}

function Row({ day, weekday, counts, colorFor, onHover }: RowProps) {
  return (
    <>
      <div className="heatmap-row-label">{day}</div>
      {HOURS.map((h) => (
        <div
          key={h}
          className="heatmap-cell"
          style={{ background: colorFor(counts[h]) }}
          title={`${day}, ${String(h).padStart(2, '0')}:00 — ${counts[h]}`}
          onMouseMove={(e) => {
            const parent = e.currentTarget.offsetParent as HTMLElement | null
            const parentRect = parent?.getBoundingClientRect()
            const rect = e.currentTarget.getBoundingClientRect()
            onHover({
              weekday,
              hour: h,
              count: counts[h],
              x: rect.left - (parentRect?.left ?? 0) + rect.width / 2,
              y: rect.top - (parentRect?.top ?? 0),
            })
          }}
        />
      ))}
    </>
  )
}
