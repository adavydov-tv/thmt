import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChartCard } from '../components/ChartCard'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { api } from '../lib/api'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'

const FORMAT_LABEL: Record<string, [string, string]> = {
  office: ['Офис', 'Office'],
  hybrid: ['Гибрид', 'Hybrid'],
  remote: ['Удалёнка', 'Remote'],
}

export function FormatComparePage() {
  const { filters } = useFilters()
  const { t, lang } = useT()
  const [dim, setDim] = useState<'area' | 'cluster' | 'grade'>('area')
  const [cluster, setCluster] = useState('')
  const [grade, setGrade] = useState('')

  const q = useQuery({
    queryKey: ['format-compare', filters.from, filters.to, dim, cluster, grade],
    queryFn: () =>
      api.formatCompare({
        from: filters.from,
        to: filters.to,
        tz: TIMEZONE,
        dim,
        cluster: cluster || undefined,
        grade: grade || undefined,
      }),
  })

  const fmtLabel = (f: string) => FORMAT_LABEL[f]?.[lang === 'en' ? 1 : 0] ?? f
  const dimTh =
    dim === 'area'
      ? t('formatCompare.dimArea')
      : dim === 'cluster'
        ? t('formatCompare.dimCluster')
        : t('formatCompare.dimGrade')

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('formatCompare.title')}</h1>
          <p>{t('formatCompare.subtitle')}</p>
        </div>
      </div>

      <ChartCard
        title={t('formatCompare.card')}
        subtitle={t('formatCompare.metricHint')}
        actions={
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <label className="muted" style={{ fontSize: 13 }}>
              {t('formatCompare.rowsBy')}{' '}
              <select value={dim} onChange={(e) => setDim(e.target.value as typeof dim)}>
                <option value="area">{t('formatCompare.dimArea')}</option>
                <option value="cluster">{t('formatCompare.dimCluster')}</option>
                <option value="grade">{t('formatCompare.dimGrade')}</option>
              </select>
            </label>
            <select value={cluster} onChange={(e) => setCluster(e.target.value)}>
              <option value="">{t('formatCompare.allClusters')}</option>
              {(q.data?.clusters ?? []).map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
            <select value={grade} onChange={(e) => setGrade(e.target.value)}>
              <option value="">{t('formatCompare.allGrades')}</option>
              {(q.data?.grades ?? []).map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </select>
          </div>
        }
      >
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.isLoading ? (
          <Skeleton height={260} radius={10} />
        ) : !q.data || q.data.rows.length === 0 ? (
          <EmptyState title={t('formatCompare.empty')} showSyncLink={false} />
        ) : (
          <div className="tablewrap" style={{ overflowX: 'auto' }}>
            <table className="data format-compare">
              <thead>
                <tr>
                  <th rowSpan={2}>{dimTh}</th>
                  {q.data.formats.map((f) => (
                    <th key={f} colSpan={3} style={{ textAlign: 'center' }}>
                      {fmtLabel(f)}
                    </th>
                  ))}
                </tr>
                <tr>
                  {q.data.formats.map((f) => [
                    <th key={`${f}-p`} className="num" title={t('formatCompare.people')}>
                      {t('formatCompare.peopleShort')}
                    </th>,
                    <th key={`${f}-e`} className="num" title={t('formatCompare.perDayFull')}>
                      {t('formatCompare.perDay')}
                    </th>,
                    <th key={`${f}-d`} className="num" title={t('formatCompare.devFull')}>
                      {t('formatCompare.dev')}
                    </th>,
                  ])}
                </tr>
              </thead>
              <tbody>
                {q.data.rows.map((row) => (
                  <tr key={row.key}>
                    <td>{row.key}</td>
                    {q.data!.formats.map((f) => {
                      const c = row.cells[f]
                      const empty = !c || c.people === 0
                      const mut = empty ? { color: 'var(--text-muted)' } : undefined
                      return [
                        <td key={`${f}-p`} className="num" style={mut}>
                          {empty ? '—' : c.people}
                        </td>,
                        <td key={`${f}-e`} className="num" style={mut}>
                          {empty ? '—' : fmtNumber(Math.round(c.avg_events_day * 10) / 10)}
                        </td>,
                        <td
                          key={`${f}-d`}
                          className="num"
                          style={empty ? mut : c.deviations > 0 ? { color: 'var(--negative)', fontWeight: 600 } : undefined}
                        >
                          {empty ? '—' : c.deviations || '0'}
                        </td>,
                      ]
                    })}
                  </tr>
                ))}
                <tr style={{ fontWeight: 600, borderTop: '2px solid var(--border-strong)' }}>
                  <td>{t('formatCompare.total')}</td>
                  {q.data.formats.map((f) => {
                    const c = q.data!.totals[f]
                    return [
                      <td key={`${f}-p`} className="num">
                        {c?.people ?? 0}
                      </td>,
                      <td key={`${f}-e`} className="num">
                        {c && c.people ? fmtNumber(Math.round(c.avg_events_day * 10) / 10) : '—'}
                      </td>,
                      <td key={`${f}-d`} className="num">
                        {c?.deviations ?? 0}
                      </td>,
                    ]
                  })}
                </tr>
              </tbody>
            </table>
          </div>
        )}
      </ChartCard>
    </div>
  )
}
