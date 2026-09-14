import { Fragment, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { TeamPicker } from '../components/TeamPicker'
import { TypeBreakdown } from '../components/TypeBreakdown'
import { useBySource, useEnabledSources, useHrdbTeams } from '../lib/queries'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtDecimal, fmtMonth, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { BySourceBucket, BySourceCell, BySourcePerson, ReviewCycle, ReviewRating } from '../lib/types'

/**
 * Вкладка «По системе»: активность людей в одной системе (источнике) по
 * календарным месяцам периода. Ячейка — события или «усилие» источника
 * (запросы Claude, строки GitLab, часы ворклогов Jira / встреч), плюс дельта
 * последнего месяца к предыдущему и итог. Строки сгруппированы по направлению,
 * как на вкладке «По людям»: сравнивать имеет смысл людей одного профиля.
 */

type Metric = 'events' | 'effort'
type Translate = ReturnType<typeof useT>['t']

/** Как показывать «усилие» источника: подпись единицы и масштаб. */
interface UnitView {
  label: string
  scale: number
  fractional: boolean
}

function unitView(unit: string, t: Translate): UnitView | null {
  switch (unit) {
    case '':
      return null
    case 'requests':
      return { label: t('system.unit.requests'), scale: 1, fractional: false }
    case 'lines':
      return { label: t('system.unit.lines'), scale: 1, fractional: false }
    case 'seconds':
      return { label: t('system.unit.hours'), scale: 1 / 3600, fractional: true }
    case 'мин':
      return { label: t('system.unit.hours'), scale: 1 / 60, fractional: true }
    case 'писем':
      return { label: t('system.unit.letters'), scale: 1, fractional: false }
    default:
      return { label: unit, scale: 1, fractional: false }
  }
}

const DEFAULT_SOURCE = 'claude'

/** Бейдж оценки PR за один цикл; пустой — оценки за цикл нет. */
function GradeBadge({ cycle, rating }: { cycle: string; rating: ReviewRating | null }) {
  const { t } = useT()
  if (!rating) {
    return (
      <span className="pr-badge pr-badge-none" title={`${cycle}: ${t('system.noRating')}`} aria-label={`${cycle}: ${t('system.noRating')}`}>
        —
      </span>
    )
  }
  const text = t(`system.grade${rating.grade}` as 'system.gradeE' | 'system.gradeM' | 'system.gradeP' | 'system.gradeD')
  return (
    <a
      className={`pr-badge pr-badge-${rating.grade}`}
      href={rating.url}
      target="_blank"
      rel="noreferrer"
      title={`${cycle}: ${text} (${rating.issue_key}, ${rating.source})`}
    >
      {rating.grade}
    </a>
  )
}

export function SystemComparePage() {
  const { filters, setFilters } = useFilters()
  const [searchParams, setSearchParams] = useSearchParams()
  const ct = useChartTokens()
  const { t, tp } = useT()
  const teamsQ = useHrdbTeams()
  const sources = useEnabledSources()

  const setParam = (key: string, value: string) =>
    setSearchParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        if (value) p.set(key, value)
        else p.delete(key)
        return p
      },
      { replace: true },
    )

  // Система — из URL, чтобы ссылкой можно было делиться; по умолчанию Claude.
  const sourceParam = searchParams.get('source') ?? ''
  const source = sources.includes(sourceParam as (typeof sources)[number])
    ? sourceParam
    : sources.includes(DEFAULT_SOURCE)
      ? DEFAULT_SOURCE
      : (sources[0] ?? DEFAULT_SOURCE)

  const [selectedTeams, setSelectedTeamsState] = useState<string[]>(() =>
    (searchParams.get('teams') ?? '').split(',').map((s) => s.trim()).filter(Boolean),
  )
  const setSelectedTeams = (next: string[]) => {
    setSelectedTeamsState(next)
    setParam('teams', next.join(','))
  }

  // Метрика — в URL. По умолчанию для Claude — усилие (запросы): события у него
  // это лишь дни с активностью; для остальных систем — события.
  const metricParam = searchParams.get('metric')
  const metric: Metric =
    metricParam === 'effort' || metricParam === 'events'
      ? metricParam
      : source === DEFAULT_SOURCE
        ? 'effort'
        : 'events'
  const setMetric = (m: Metric) => setParam('metric', m)
  const [showIdle, setShowIdle] = useState(false)
  const [sort, setSort] = useState<{ key: string; desc: boolean }>({ key: 'total', desc: true })

  const q = useBySource({
    source,
    from: filters.from,
    to: filters.to,
    tz: TIMEZONE,
    team: selectedTeams.length ? selectedTeams : undefined,
    area: filters.area || undefined,
  })

  const buckets: BySourceBucket[] = q.data?.buckets ?? []
  const cycles: ReviewCycle[] = q.data?.review_cycles ?? []
  const unit = useMemo(() => unitView(q.data?.effort_unit ?? '', t), [q.data?.effort_unit, t])
  // Усилия у источника нет — показываем события, даже если выбрано «усилие».
  const effMetric: Metric = unit ? metric : 'events'

  const allRows = useMemo(() => q.data?.people ?? [], [q.data])
  const hasAny = allRows.some((r) => r.total_events > 0)
  const idleCount = allRows.filter((r) => r.total_events === 0).length
  const rows = useMemo(
    () => (showIdle ? allRows : allRows.filter((r) => r.total_events > 0)),
    [allRows, showIdle],
  )

  const areaOptions = useMemo(() => {
    const set = new Set<string>(teamsQ.data?.areas ?? [])
    for (const r of allRows) {
      if (r.person.area) set.add(r.person.area)
    }
    return [...set].sort()
  }, [teamsQ.data, allRows])

  const cellValue = (c: BySourceCell) => (effMetric === 'effort' && unit ? c.effort * unit.scale : c.events)
  const totalValue = (r: BySourcePerson) =>
    effMetric === 'effort' && unit ? r.total_effort * unit.scale : r.total_events
  const deltaValue = (r: BySourcePerson) => {
    const n = r.cells.length
    if (n < 2) return null
    return cellValue(r.cells[n - 1]) - cellValue(r.cells[n - 2])
  }
  const sortValue = (r: BySourcePerson) => {
    if (sort.key === 'total') return totalValue(r)
    if (sort.key === 'tenure') return r.hr?.tenure_years ?? -1
    if (sort.key === 'delta') return deltaValue(r) ?? Number.NEGATIVE_INFINITY
    const i = buckets.findIndex((b) => b.key === sort.key)
    return i >= 0 ? cellValue(r.cells[i]) : 0
  }
  const fractional = effMetric === 'effort' && !!unit?.fractional
  const fmtVal = (v: number) => (fractional ? fmtDecimal(v) : fmtNumber(Math.round(v)))
  const fmtDelta = (d: number) => {
    const s = fmtVal(Math.abs(d))
    return d > 0 ? `+${s}` : d < 0 ? `−${s}` : '0'
  }

  const byArea = useMemo(() => {
    const groups = new Map<string, BySourcePerson[]>()
    for (const r of rows) {
      const area = r.person.area || t('compare.noArea')
      const list = groups.get(area) ?? []
      list.push(r)
      groups.set(area, list)
    }
    const dir = sort.desc ? -1 : 1
    for (const list of groups.values()) {
      list.sort((a, b) => dir * (sortValue(a) - sortValue(b)))
    }
    return [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0], 'ru'))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, sort, effMetric, unit, buckets, t])

  const metricLabel =
    effMetric === 'effort' && unit ? `${t('system.metricEffort')}, ${unit.label}` : t('system.metricEvents')

  const chartItems = useMemo(
    () =>
      rows
        .map((r) => ({
          key: r.person.key,
          label: r.person.display_name || r.person.key,
          count: Math.round(totalValue(r)),
        }))
        .filter((i) => i.count > 0)
        .sort((a, b) => b.count - a.count),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rows, effMetric, unit],
  )

  const toggleSort = (key: string) =>
    setSort((prev) => (prev.key === key ? { key, desc: !prev.desc } : { key, desc: true }))

  const SortTh = ({ k, children, title }: { k: string; children: React.ReactNode; title?: string }) => {
    const active = sort.key === k
    return (
      <th scope="col" className="num" aria-sort={active ? (sort.desc ? 'descending' : 'ascending') : 'none'}>
        <button
          type="button"
          className="th-sort"
          onClick={() => toggleSort(k)}
          title={title}
          aria-label={`${t('system.sortAria')}: ${typeof children === 'string' ? children : k}`}
        >
          {children}
          {active && <span aria-hidden="true"> {sort.desc ? '↓' : '↑'}</span>}
        </button>
      </th>
    )
  }

  const hrCols = 2 + (cycles.length > 0 ? 2 : 0) // грейд, стаж, [PR, PIP]
  const colCount = 2 + hrCols + buckets.length + (buckets.length >= 2 ? 1 : 0) + 1

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('system.title')}</h1>
          <p className="num">
            {fmtDate(filters.from)} — {fmtDate(filters.to)}
            {allRows.length > 0 && (
              <>
                {' · '}
                {fmtNumber(allRows.length)} {tp('compare.pluralPeople', allRows.length)}
              </>
            )}
          </p>
        </div>
      </div>

      <section className="card" style={{ padding: 14, display: 'grid', gap: 12 }}>
        <div className="field">
          <span className="field-label">{t('system.sourceLabel')}</span>
          <div className="segmented" role="group" aria-label={t('system.sourceLabel')} style={{ flexWrap: 'wrap' }}>
            {sources.map((key) => (
              <button
                key={key}
                type="button"
                aria-pressed={key === source}
                onClick={() => setParam('source', key)}
              >
                {sourceLabel(key)}
              </button>
            ))}
          </div>
        </div>
        <div className="form-grid">
          <TeamPicker
            teams={teamsQ.data?.teams ?? []}
            selected={selectedTeams}
            onChange={setSelectedTeams}
            label={t('compare.teamsLabel')}
          />
          <div className="field">
            <label htmlFor="system-area">{t('compare.areaLabel')}</label>
            <select
              id="system-area"
              className="select"
              value={filters.area}
              onChange={(e) => setFilters({ area: e.target.value })}
            >
              <option value="">{t('compare.allAreas')}</option>
              {areaOptions.map((area) => (
                <option key={area} value={area}>
                  {area}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <span className="field-label">{t('system.metricLabel')}</span>
            <div className="segmented" role="group" aria-label={t('system.metricLabel')}>
              <button type="button" aria-pressed={effMetric === 'events'} onClick={() => setMetric('events')}>
                {t('system.metricEvents')}
              </button>
              <button
                type="button"
                aria-pressed={effMetric === 'effort'}
                disabled={!unit}
                onClick={() => setMetric('effort')}
                title={unit ? unit.label : undefined}
              >
                {t('system.metricEffort')}
                {unit ? `, ${unit.label}` : ''}
              </button>
            </div>
          </div>
        </div>
        <label className="inline-group" style={{ gap: 8, cursor: 'pointer' }}>
          <input type="checkbox" checked={showIdle} onChange={(e) => setShowIdle(e.target.checked)} />
          <span>
            {t('system.showIdle')}
            {idleCount > 0 && <span className="muted num"> · {fmtNumber(idleCount)}</span>}
          </span>
        </label>
      </section>

      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isLoading ? (
        <Skeleton height={340} radius={10} />
      ) : rows.length === 0 || !hasAny ? (
        <div className="card">
          <EmptyState
            title={t('system.emptyTitle')}
            description={t('system.emptyDescription')}
            showSyncLink={false}
          />
        </div>
      ) : (
        <>
          <TypeBreakdown
            title={`${sourceLabel(source)} · ${metricLabel}`}
            subtitle={t('system.chartSubtitle')}
            items={chartItems}
            color={ct.slot(0)}
            categoryLabel={t('system.employee')}
            valueLabel={metricLabel}
            limit={30}
            emptyTitle={t('system.noData')}
          />

          <section className="card" style={{ padding: 14, overflowX: 'auto' }}>
            <table className="data">
              <caption className="visually-hidden">{t('system.tableCaption')}</caption>
              <thead>
                <tr>
                  <th scope="col">{t('system.employee')}</th>
                  <th scope="col">{t('system.team')}</th>
                  <th scope="col">{t('system.grade')}</th>
                  <SortTh k="tenure" title={t('system.tenureTitle')}>
                    {t('system.tenure')}
                  </SortTh>
                  {cycles.length > 0 && (
                    <>
                      <th scope="col" title={`${t('system.prTitle')}: ${cycles.map((c) => c.key).join(' → ')}`}>
                        {t('system.pr')}
                      </th>
                      <th scope="col" title={t('system.pipTitle')}>
                        {t('system.pip')}
                      </th>
                    </>
                  )}
                  {buckets.map((b) => (
                    <SortTh key={b.key} k={b.key} title={b.partial ? t('system.partialMonth') : undefined}>
                      {fmtMonth(b.from)}
                      {b.partial ? '*' : ''}
                    </SortTh>
                  ))}
                  {buckets.length >= 2 && (
                    <SortTh k="delta" title={t('system.deltaTitle')}>
                      {t('system.delta')}
                    </SortTh>
                  )}
                  <SortTh k="total">{t('system.total')}</SortTh>
                </tr>
              </thead>
              <tbody>
                {byArea.map(([area, people]) => (
                  <Fragment key={area}>
                    <tr>
                      <th colSpan={colCount} scope="colgroup" style={{ paddingTop: 14, color: 'var(--text-secondary)' }}>
                        {area} · {people.length} {tp('compare.pluralPeople', people.length)}
                      </th>
                    </tr>
                    {people.map((r) => {
                      const d = deltaValue(r)
                      return (
                        <tr key={r.person.key}>
                          <th scope="row" style={{ fontWeight: 400 }}>
                            {r.person.display_name || r.person.key}
                            {r.person.title && (
                              <div className="muted" style={{ fontSize: 12 }}>
                                {r.person.title}
                              </div>
                            )}
                          </th>
                          <td>{r.person.team || '—'}</td>
                          <td>{r.person.grade || '—'}</td>
                          <td className="num">
                            {r.hr?.tenure_years !== undefined ? fmtDecimal(r.hr.tenure_years) : '—'}
                          </td>
                          {cycles.length > 0 && (
                            <>
                              <td>
                                <span className="pr-badges">
                                  {cycles.map((c, i) => (
                                    <GradeBadge key={c.key} cycle={c.key} rating={r.hr?.reviews?.[i] ?? null} />
                                  ))}
                                </span>
                              </td>
                              <td>
                                {r.hr?.pip ? (
                                  <a
                                    href={r.hr.pip.url}
                                    target="_blank"
                                    rel="noreferrer"
                                    className={r.hr.pip.active ? 'pip-flag pip-flag-active' : 'pip-flag'}
                                    title={`${r.hr.pip.issue_key} · ${r.hr.pip.status}`}
                                  >
                                    {r.hr.pip.active ? t('system.pipActive') : t('system.pipClosed')}
                                  </a>
                                ) : (
                                  <span style={{ color: 'var(--text-secondary)' }}>—</span>
                                )}
                              </td>
                            </>
                          )}
                          {r.cells.map((c, i) => {
                            const v = cellValue(c)
                            return (
                              <td className="num" key={buckets[i]?.key ?? i} style={v === 0 ? { color: 'var(--text-secondary)' } : undefined}>
                                {v === 0 ? '—' : fmtVal(v)}
                              </td>
                            )
                          })}
                          {buckets.length >= 2 && (
                            <td
                              className="num"
                              style={{
                                color: d === null || d === 0 ? undefined : d > 0 ? ct.positive : ct.negative,
                                fontWeight: d ? 600 : undefined,
                              }}
                            >
                              {d === null ? '—' : fmtDelta(d)}
                            </td>
                          )}
                          <td className="num" style={{ fontWeight: 600 }}>
                            {fmtVal(totalValue(r))}
                          </td>
                        </tr>
                      )
                    })}
                  </Fragment>
                ))}
              </tbody>
            </table>
            {buckets.some((b) => b.partial) && (
              <p className="muted" style={{ marginTop: 8, fontSize: 12 }}>
                * {t('system.partialMonth')}
              </p>
            )}
            {cycles.length > 0 && (
              <p className="muted" style={{ marginTop: 4, fontSize: 12 }}>
                {t('system.prLegend')} {cycles.map((c) => c.key).join(' → ')}
              </p>
            )}
          </section>
        </>
      )}
    </div>
  )
}
