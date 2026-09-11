import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ErrorState } from '../components/ErrorState'
import { SkeletonLines } from '../components/Skeleton'
import { EmptyState } from '../components/EmptyState'
import { useEvent, useMeta } from '../lib/queries'
import { useTypeLabeler } from '../lib/useStatsQuery'
import { useFilters } from '../lib/useFilters'
import { sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDateTime, fmtDecimal } from '../lib/format'
import { useT } from '../i18n'
import { useMe } from '../lib/authClient'
import { api } from '../lib/api'
import type { ActivityEvent } from '../lib/types'

export function EventDetail() {
  const { id = '' } = useParams()
  const { linkSearch } = useFilters()
  const { t } = useT()
  const tokens = useChartTokens()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)
  const query = useEvent(id)
  const me = useMe()
  const [slackText, setSlackText] = useState<string | null>(null)
  const [slackTextErr, setSlackTextErr] = useState('')

  if (query.isLoading) {
    return (
      <div className="card panel-body">
        <SkeletonLines count={6} height={22} />
      </div>
    )
  }

  if (query.isError) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />
  }

  if (!query.data) {
    return <EmptyState title={t('event.notFound')} showSyncLink={false} />
  }

  const { event, related } = query.data
  const color = tokens.sourceColor(event.source)
  const metaEntries = Object.entries(event.meta ?? {})

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <div className="chart-card-subtitle">
            <Link to={`/events${linkSearch}`}>{t('event.backToFeed')}</Link>
          </div>
          <h1>
            <span className="dot" style={{ background: color, marginRight: 8 }} aria-hidden="true" />
            {event.title || t('event.noTitle')}
          </h1>
          <p>
            {sourceLabel(event.source)} · <span className="badge">{typeLabel(event.type)}</span> ·{' '}
            <span className="num">{fmtDateTime(event.occurred_at)}</span>
          </p>
        </div>
        <span className="spacer" />
        {event.url && (
          <a className="btn btn-primary" href={event.url} target="_blank" rel="noreferrer">
            {t('event.openInSource')}
          </a>
        )}
      </div>

      <div className="detail-grid">
        <div className="stack">
          <section className="card chart-card">
            <header className="chart-card-head">
              <div className="chart-card-titles">
                <h2>{t('event.fields')}</h2>
              </div>
            </header>
            <div className="panel-body">
              <dl className="kv">
                <dt>ID</dt>
                <dd className="mono">{String(event.id)}</dd>
                <dt>{t('event.person')}</dt>
                <dd>{event.person_key}</dd>
                <dt>{t('event.source')}</dt>
                <dd>{sourceLabel(event.source)}</dd>
                <dt>{t('event.type')}</dt>
                <dd>
                  {typeLabel(event.type)} <span className="muted mono">{event.type}</span>
                </dd>
                <dt>{t('event.externalId')}</dt>
                <dd className="mono">{event.external_id || '—'}</dd>
                <dt>{t('event.occurredAt')}</dt>
                <dd className="num">{fmtDateTime(event.occurred_at)}</dd>
                <dt>{t('event.project')}</dt>
                <dd>
                  {event.project_name || event.project || '—'}
                  {event.project_name && event.project && (
                    <span className="muted mono"> {event.project}</span>
                  )}
                </dd>
                <dt>Ref ID</dt>
                <dd className="mono">{event.ref_id || '—'}</dd>
                <dt>{t('event.parentRef')}</dt>
                <dd className="mono">{event.parent_ref_id || '—'}</dd>
                <dt>{t('event.effort')}</dt>
                <dd className="num">
                  {fmtDecimal(event.effort)} {event.effort_unit ?? ''}
                </dd>
                <dt>{t('event.link')}</dt>
                <dd>
                  {event.url ? (
                    <a href={event.url} target="_blank" rel="noreferrer">
                      {event.url}
                    </a>
                  ) : (
                    '—'
                  )}
                </dd>
              </dl>

              {event.body && <pre className="body-preview">{event.body}</pre>}
              {/* Slack: текст события не хранится — админ может запросить его
                  из зашифрованного архива по явной кнопке. */}
              {event.source === 'slack' &&
                !event.body &&
                typeof event.meta?.message_id === 'string' &&
                me.data?.role === 'administrator' && (
                  <div className="stack" style={{ gap: 8, marginTop: 8 }}>
                    {slackText === null ? (
                      <button
                        type="button"
                        className="btn btn-sm"
                        onClick={() => {
                          setSlackTextErr('')
                          api
                            .slackText(String(event.meta!.message_id))
                            .then((r) => setSlackText(r.text || t('slackArch.emptyText')))
                            .catch((e) => setSlackTextErr(e.message))
                        }}
                      >
                        {t('slackArch.showText')}
                      </button>
                    ) : (
                      <pre className="body-preview">{slackText}</pre>
                    )}
                    {slackTextErr && <p className="error-text">{slackTextErr}</p>}
                    <p className="muted" style={{ fontSize: 12, margin: 0 }}>
                      {t('slackArch.adminOnlyHint')}
                    </p>
                  </div>
                )}
            </div>
          </section>

          <section className="card chart-card">
            <header className="chart-card-head">
              <div className="chart-card-titles">
                <h2>Meta</h2>
                <div className="chart-card-subtitle">{t('event.metaHint')}</div>
              </div>
            </header>
            <div className="panel-body">
              {metaEntries.length === 0 ? (
                <p className="muted">{t('event.metaEmpty')}</p>
              ) : (
                <div className="table-wrap" style={{ maxHeight: 'none' }}>
                  <table className="data">
                    <thead>
                      <tr>
                        <th scope="col">{t('event.metaKey')}</th>
                        <th scope="col">{t('event.metaValue')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {metaEntries.map(([key, value]) => (
                        <tr key={key}>
                          <th scope="row" className="mono" style={{ fontWeight: 400 }}>
                            {key}
                          </th>
                          <td className="mono">{renderMetaValue(value)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </section>
        </div>

        <section className="card chart-card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{t('event.historyTitle')}</h2>
              <div className="chart-card-subtitle">{t('event.historyHint')}</div>
            </div>
          </header>
          <div className="panel-body">
            {related.length === 0 ? (
              <p className="muted">{t('event.noRelated')}</p>
            ) : (
              <div className="timeline-rail">
                {related.map((rel) => (
                  <RelatedItem
                    key={String(rel.id)}
                    event={rel}
                    current={String(rel.id) === String(event.id)}
                    color={tokens.sourceColor(rel.source)}
                    typeLabel={typeLabel}
                    search={linkSearch}
                  />
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}

function RelatedItem({
  event,
  current,
  color,
  typeLabel,
  search,
}: {
  event: ActivityEvent
  current: boolean
  color: string
  typeLabel: (type: string) => string
  search: string
}) {
  const { t } = useT()
  return (
    <div className="rail-item">
      <span className="rail-dot" style={{ background: color }} aria-hidden="true" />
      <div>
        <div className={current ? 'rail-current' : undefined}>
          {current ? (
            <span>{event.title || typeLabel(event.type)}</span>
          ) : (
            <Link to={`/events/${event.id}${search}`}>{event.title || typeLabel(event.type)}</Link>
          )}
        </div>
        <div className="event-meta">
          <span className="badge">{typeLabel(event.type)}</span>
          <span className="num">{fmtDateTime(event.occurred_at)}</span>
          {current && <span className="badge">{t('event.current')}</span>}
        </div>
      </div>
    </div>
  )
}

function renderMetaValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
