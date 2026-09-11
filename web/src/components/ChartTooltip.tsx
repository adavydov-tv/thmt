import { fmtDecimal, fmtNumber } from '../lib/format'
import { useT } from '../i18n'

export interface TooltipRow {
  name: string
  value: number
  color: string
}

interface ChartTooltipProps {
  title: string
  rows: TooltipRow[]
  totalLabel?: string
  total?: number
  unit?: string
}

/**
 * Собственный тултип, стилизованный под карточку: hairline-граница + тень.
 * Идентичность серии — цветной маркер + текстовая подпись, никогда только цвет.
 */
export function ChartTooltip({ title, rows, totalLabel, total, unit }: ChartTooltipProps) {
  const { t } = useT()
  const totalName = totalLabel ?? t('chart.total')
  const fmt = (v: number) => (Number.isInteger(v) ? fmtNumber(v) : fmtDecimal(v))
  return (
    <div className="chart-tooltip">
      <div className="chart-tooltip-title">{title}</div>
      {rows.map((row) => (
        <div className="chart-tooltip-row" key={row.name}>
          <span className="dot" style={{ background: row.color }} aria-hidden="true" />
          <span className="chart-tooltip-name">{row.name}</span>
          <span className="chart-tooltip-value num">
            {fmt(row.value)}
            {unit ? ` ${unit}` : ''}
          </span>
        </div>
      ))}
      {total !== undefined && (
        <div className="chart-tooltip-total">
          <span className="chart-tooltip-name">{totalName}</span>
          <span className="chart-tooltip-value num">
            {fmt(total)}
            {unit ? ` ${unit}` : ''}
          </span>
        </div>
      )}
    </div>
  )
}
