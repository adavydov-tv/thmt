import type { ReactNode } from 'react'
import { EventRow } from './EventRow'
import { EmptyState } from './EmptyState'
import { ErrorState } from './ErrorState'
import { SkeletonLines } from './Skeleton'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { ActivityEvent } from '../lib/types'

interface EventListProps {
  title: string
  subtitle?: ReactNode
  events: ActivityEvent[] | undefined
  isLoading?: boolean
  error?: unknown
  onRetry?: () => void
  typeLabel?: (type: string) => string
  search?: string
  actions?: ReactNode
  /** Пагинация; если не передана — список без футера. */
  page?: number
  perPage?: number
  total?: number
  onPageChange?: (page: number) => void
  emptyTitle?: string
}

export function EventList({
  title,
  subtitle,
  events,
  isLoading,
  error,
  onRetry,
  typeLabel,
  search,
  actions,
  page,
  perPage,
  total,
  onPageChange,
  emptyTitle,
}: EventListProps) {
  const { t, tp } = useT()
  const pages = page && perPage && total !== undefined ? Math.max(1, Math.ceil(total / perPage)) : undefined

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{title}</h2>
          {subtitle && <div className="chart-card-subtitle">{subtitle}</div>}
          {total !== undefined && (
            <div className="chart-card-subtitle num">
              {fmtNumber(total)} {tp('plural.events', total)}
            </div>
          )}
        </div>
        {actions && <div className="chart-card-actions">{actions}</div>}
      </header>

      {error ? (
        <div className="panel-body">
          <ErrorState error={error} onRetry={onRetry} />
        </div>
      ) : isLoading ? (
        <div className="panel-body">
          <SkeletonLines count={5} height={28} />
        </div>
      ) : !events || events.length === 0 ? (
        <EmptyState title={emptyTitle ?? t('ui.noEvents')} />
      ) : (
        <div className="event-list">
          {events.map((e) => (
            <EventRow key={String(e.id)} event={e} typeLabel={typeLabel} search={search} />
          ))}
        </div>
      )}

      {pages !== undefined && page !== undefined && onPageChange && !error && (
        <div className="pagination">
          <button
            type="button"
            className="btn btn-sm"
            disabled={page <= 1}
            onClick={() => onPageChange(page - 1)}
          >
            {t('ui.back')}
          </button>
          <span className="num">
            {t('ui.pageOf', { page: fmtNumber(page), pages: fmtNumber(pages) })}
          </span>
          <button
            type="button"
            className="btn btn-sm"
            disabled={page >= pages}
            onClick={() => onPageChange(page + 1)}
          >
            {t('ui.forward')}
          </button>
        </div>
      )}
    </section>
  )
}
