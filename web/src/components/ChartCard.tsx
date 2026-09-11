import { useId, useState, type ReactNode } from 'react'
import { useT } from '../i18n'

interface ChartCardProps {
  title: string
  subtitle?: ReactNode
  /** Слот действий в углу карточки (селекты, переключатели). */
  actions?: ReactNode
  /** Табличное представление тех же данных — требование доступности. */
  table?: ReactNode
  children: ReactNode
  className?: string
}

export function ChartCard({ title, subtitle, actions, table, children, className }: ChartCardProps) {
  const { t } = useT()
  const [showTable, setShowTable] = useState(false)
  const bodyId = useId()

  return (
    <section className={`card chart-card${className ? ` ${className}` : ''}`}>
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{title}</h2>
          {subtitle && <div className="chart-card-subtitle">{subtitle}</div>}
        </div>
        <div className="chart-card-actions">
          {actions}
          {table && (
            <button
              type="button"
              className="btn btn-icon"
              aria-pressed={showTable}
              aria-controls={bodyId}
              title={showTable ? t('chart.showChart') : t('chart.showTable')}
              onClick={() => setShowTable((v) => !v)}
            >
              <span aria-hidden="true">{showTable ? '▦' : '▤'}</span>
              <span className="visually-hidden">
                {showTable ? t('chart.showChart') : t('chart.showTable')}
              </span>
            </button>
          )}
        </div>
      </header>
      <div className="chart-card-body" id={bodyId}>
        {showTable && table ? <div className="table-wrap">{table}</div> : children}
      </div>
    </section>
  )
}
