import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChartCard } from '../components/ChartCard'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { api } from '../lib/api'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'

const REASON_CLASS: Record<string, string> = {
  no_activity: 'badge-failed',
  idle: 'badge-failed',
  low_activity: 'badge-running',
}

export function AbusersPage() {
  const { filters, linkSearch } = useFilters()
  const { t } = useT()

  const q = useQuery({
    queryKey: ['abusers', filters.from, filters.to],
    queryFn: () => api.abusers({ from: filters.from, to: filters.to, tz: TIMEZONE }),
  })

  const reasonLabel = (r: string) =>
    r === 'no_activity' ? t('abusers.noActivity') : r === 'idle' ? t('abusers.idle') : t('abusers.low')

  const list = q.data?.abusers ?? []

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('abusers.title')}</h1>
          <p>{t('abusers.subtitle')}</p>
        </div>
      </div>

      <ChartCard
        title={t('abusers.card')}
        subtitle={list.length ? t('abusers.count', { n: String(list.length) }) : undefined}
      >
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.isLoading ? (
          <Skeleton height={260} radius={10} />
        ) : list.length === 0 ? (
          <EmptyState title={t('abusers.empty')} showSyncLink={false} />
        ) : (
          <div className="tablewrap" style={{ overflowX: 'auto' }}>
            <table className="data">
              <thead>
                <tr>
                  <th>{t('abusers.person')}</th>
                  <th>{t('abusers.reasonCol')}</th>
                  <th className="num">{t('abusers.idleDays')}</th>
                  <th className="num">{t('abusers.lowDays')}</th>
                  <th className="num">{t('abusers.activeRatio')}</th>
                  <th className="num">{t('abusers.events')}</th>
                </tr>
              </thead>
              <tbody>
                {list.map((a) => (
                  <tr key={a.person.key}>
                    <td>
                      <Link to={`/${linkSearch}${linkSearch ? '&' : '?'}person=${a.person.key}`}>
                        {a.person.display_name}
                      </Link>
                      {a.person.title && (
                        <span className="muted" style={{ fontSize: 12, display: 'block' }}>
                          {a.person.title}
                        </span>
                      )}
                    </td>
                    <td>
                      <span className={`badge badge-status ${REASON_CLASS[a.reason]}`}>
                        {reasonLabel(a.reason)}
                      </span>
                    </td>
                    <td className="num">{a.idle_working_days || '—'}</td>
                    <td className="num">{a.low_activity_days || '—'}</td>
                    <td className="num">{fmtNumber(Math.round(a.active_ratio * 100))}%</td>
                    <td className="num">{fmtNumber(a.total_events)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </ChartCard>
    </div>
  )
}
