import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { SkeletonLines } from '../components/Skeleton'
import { EventRow } from '../components/EventRow'
import { useDocs, useMeta, useRefEvents } from '../lib/queries'
import { useFilters } from '../lib/useFilters'
import { useTypeLabeler } from '../lib/useStatsQuery'
import { fmtDate, fmtDateTime, fmtNumber } from '../lib/format'
import { useT } from '../i18n'

export function DocsPage() {
  const { filters, linkSearch } = useFilters()
  const { t, tp } = useT()
  const [searchParams, setSearchParams] = useSearchParams()
  const selected = searchParams.get('doc') ?? ''
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)

  const docs = useDocs(filters.person)
  const docEvents = useRefEvents(selected, filters.person, Boolean(selected))

  const selectedDoc = useMemo(
    () => docs.data?.find((d) => d.doc_id === selected),
    [docs.data, selected],
  )

  const select = (docId: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (docId && docId !== selected) next.set('doc', docId)
      else next.delete('doc')
      return next
    })
  }

  if (!filters.person) {
    return <EmptyState title={t('events.noPerson')} description={t('events.noPersonHint')} showSyncLink={false} />
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('docs.title')}</h1>
          <p>{t('docs.subtitle')}</p>
        </div>
      </div>

      <div className="detail-grid">
        <section className="card chart-card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{t('docs.linkedTitle')}</h2>
              {docs.data && (
                <div className="chart-card-subtitle num">
                  {fmtNumber(docs.data.length)}{' '}
                  {tp('docs.pluralDocs', docs.data.length)}
                </div>
              )}
            </div>
          </header>

          {docs.isError ? (
            <div className="panel-body">
              <ErrorState error={docs.error} onRetry={() => void docs.refetch()} />
            </div>
          ) : docs.isLoading ? (
            <div className="panel-body">
              <SkeletonLines count={6} height={22} />
            </div>
          ) : !docs.data || docs.data.length === 0 ? (
            <EmptyState
              title={t('docs.empty')}
              description={t('docs.emptyHint')}
            />
          ) : (
            <div className="table-wrap" style={{ maxHeight: 'none' }}>
              <table className="data">
                <caption className="visually-hidden">{t('docs.tableCaption')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('docs.colDoc')}</th>
                    <th scope="col">{t('docs.colIssue')}</th>
                    <th scope="col" className="num">
                      {t('docs.colEdits')}
                    </th>
                    <th scope="col" className="num">
                      {t('docs.colComments')}
                    </th>
                    <th scope="col">{t('docs.colModified')}</th>
                    <th scope="col">{t('docs.colFoundIn')}</th>
                  </tr>
                </thead>
                <tbody>
                  {docs.data.map((doc) => (
                    <tr
                      key={doc.doc_id}
                      className={`clickable${doc.doc_id === selected ? ' selected' : ''}`}
                      onClick={() => select(doc.doc_id)}
                      aria-selected={doc.doc_id === selected}
                    >
                      <th scope="row" style={{ fontWeight: 400 }}>
                        <span style={{ fontWeight: 500 }}>{doc.title || doc.doc_id}</span>
                        {!doc.enriched && (
                          <>
                            {' '}
                            <span className="badge">{t('docs.notEnriched')}</span>
                          </>
                        )}
                        <div className="muted mono">{doc.doc_id}</div>
                      </th>
                      <td>
                        <div>{doc.issue_key || '—'}</div>
                        {doc.issue_title && <div className="muted">{doc.issue_title}</div>}
                      </td>
                      <td className="num">{fmtNumber(doc.edit_count)}</td>
                      <td className="num">{fmtNumber(doc.comment_count)}</td>
                      <td className="num">{doc.last_modified ? fmtDateTime(doc.last_modified) : '—'}</td>
                      <td>
                        <span className="badge">{doc.found_in || '—'}</span>
                        <div className="muted num">
                          {t('docs.discovered', { date: fmtDate(doc.discovered_at) })}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <section className="card chart-card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{selectedDoc ? selectedDoc.title || t('docs.docFallback') : t('docs.eventsTitle')}</h2>
              <div className="chart-card-subtitle">
                {selectedDoc ? (
                  <>
                    {selectedDoc.issue_key && (
                      <>{t('docs.issuePrefix', { key: selectedDoc.issue_key })} · </>
                    )}
                    <a href={selectedDoc.doc_url} target="_blank" rel="noreferrer">
                      {t('docs.openDoc')}
                    </a>
                  </>
                ) : (
                  t('docs.pickHint')
                )}
              </div>
            </div>
            {selectedDoc && (
              <div className="chart-card-actions">
                <button type="button" className="btn btn-sm" onClick={() => select('')}>
                  {t('docs.close')}
                </button>
              </div>
            )}
          </header>

          {!selected ? (
            <div className="panel-body">
              <p className="muted">{t('docs.eventsPlaceholder')}</p>
            </div>
          ) : docEvents.isError ? (
            <div className="panel-body">
              <ErrorState error={docEvents.error} onRetry={() => void docEvents.refetch()} />
            </div>
          ) : docEvents.isLoading ? (
            <div className="panel-body">
              <SkeletonLines count={4} height={24} />
            </div>
          ) : !docEvents.data || docEvents.data.length === 0 ? (
            <EmptyState title={t('docs.noEvents')} showSyncLink={false} />
          ) : (
            <div className="event-list">
              {docEvents.data.map((e) => (
                <EventRow key={String(e.id)} event={e} typeLabel={typeLabel} search={linkSearch} />
              ))}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
