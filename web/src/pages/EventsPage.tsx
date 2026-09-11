import { EventList } from '../components/EventList'
import { FilterBar } from '../components/FilterBar'
import { EmptyState } from '../components/EmptyState'
import { useBreakdown, useEvents, useMeta } from '../lib/queries'
import { useStatsQuery, useTypeLabeler } from '../lib/useStatsQuery'
import { useFilters } from '../lib/useFilters'
import { fmtDate } from '../lib/format'
import { useT } from '../i18n'

export function EventsPage() {
  const { filters, setFilters, linkSearch } = useFilters()
  const { t } = useT()
  const q = useStatsQuery()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)

  const types = useBreakdown(q, 'type')
  const projects = useBreakdown(q, 'project')
  const events = useEvents({ ...q, page: filters.page, per_page: filters.perPage, sort: 'desc' })

  if (!filters.person) {
    return <EmptyState title={t('events.noPerson')} description={t('events.noPersonHint')} showSyncLink={false} />
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('events.feedTitle')}</h1>
          <p className="num">
            {fmtDate(filters.from)} — {fmtDate(filters.to)}
          </p>
        </div>
      </div>

      <FilterBar
        sourceOptions={meta?.sources}
        typeOptions={types.data?.items}
        projectOptions={projects.data?.items}
        typeLabel={typeLabel}
      />

      <EventList
        title={t('events.title')}
        subtitle={t('events.sortHint')}
        events={events.data?.items}
        isLoading={events.isLoading}
        error={events.isError ? events.error : undefined}
        onRetry={() => void events.refetch()}
        typeLabel={typeLabel}
        search={linkSearch}
        page={filters.page}
        perPage={filters.perPage}
        total={events.data?.total}
        onPageChange={(page) => setFilters({ page })}
        actions={
          <div className="inline-group">
            <label className="field-label" htmlFor="per-page">
              {t('events.perPage')}
            </label>
            <select
              id="per-page"
              className="select"
              value={filters.perPage}
              onChange={(e) => setFilters({ perPage: Number(e.target.value), page: 1 })}
            >
              {[25, 50, 100].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </div>
        }
      />
    </div>
  )
}
