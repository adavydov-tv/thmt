import { fmtDecimal, fmtNumber, fmtPercent } from '../lib/format'
import { useT } from '../i18n'

interface KpiTileProps {
  label: string
  value: number
  unit?: string
  delta?: number
  hasDelta?: boolean
  /** Цвет источника — маркер идентичности рядом с подписью. */
  markerColor?: string
  markerLabel?: string
  /** Для метрик, где рост — это плохо (например, количество падений). */
  invertDelta?: boolean
}

export function KpiTile({
  label,
  value,
  unit,
  delta,
  hasDelta,
  markerColor,
  markerLabel,
  invertDelta = false,
}: KpiTileProps) {
  const { t } = useT()
  const showDelta = Boolean(hasDelta) && delta !== undefined && Number.isFinite(delta)
  const positiveDirection = showDelta && (invertDelta ? (delta as number) < 0 : (delta as number) > 0)
  const negativeDirection = showDelta && (invertDelta ? (delta as number) > 0 : (delta as number) < 0)
  const arrow = showDelta ? ((delta as number) > 0 ? '▲' : (delta as number) < 0 ? '▼' : '■') : ''
  const deltaClass = positiveDirection ? 'delta-up' : negativeDirection ? 'delta-down' : ''

  const displayValue = Number.isInteger(value) ? fmtNumber(value) : fmtDecimal(value)

  return (
    <div className="card kpi">
      <div className="kpi-label">
        {markerColor && (
          <span className="dot" style={{ background: markerColor }} aria-hidden="true" />
        )}
        <span>{label}</span>
      </div>
      <div className="kpi-value">
        {displayValue}
        {unit && <span className="kpi-unit">{unit}</span>}
      </div>
      <div className="kpi-delta">
        {showDelta ? (
          <>
            <span className={deltaClass}>
              <span aria-hidden="true">{arrow}</span> {fmtPercent(delta)}
            </span>
            <span className="muted">{t('ui.vsPrevPeriod')}</span>
          </>
        ) : (
          // Без сравнимого прошлого периода строку оставляем пустой: повторённое
          // на всех плитках «нет данных» — это шум, а не информация.
          <span className="muted">{markerLabel ?? ' '}</span>
        )}
      </div>
    </div>
  )
}
