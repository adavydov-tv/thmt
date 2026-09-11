import { Fragment, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { TypeBreakdown } from '../components/TypeBreakdown'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { Sparkline, seriesTrend, zeroFillSeries } from '../components/Sparkline'
import { TeamPicker } from '../components/TeamPicker'
import { useCompare, useHrdbTeams, useStartTeamSync } from '../lib/queries'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtNumber } from '../lib/format'
import { useT, type MessageKey } from '../i18n'
import type { PersonMetrics } from '../lib/types'

/** Метрика сравнения: как достать значение из PersonMetrics. */
interface MetricDef {
  key: string
  label: MessageKey
  /** Короткая подпись для колонки таблицы. */
  short: MessageKey
  value: (m: PersonMetrics) => number
  /** Значение дробное (часы) — в таблице показываем с десятыми. */
  fractional?: boolean
}

const typeSum = (m: PersonMetrics, ...types: string[]) =>
  types.reduce((acc, t) => acc + (m.by_type[t] ?? 0), 0)

const METRICS: MetricDef[] = [
  { key: 'total', label: 'compare.metricTotal', short: 'compare.metricTotalShort', value: (m) => m.total_events },
  {
    key: 'active_working',
    label: 'compare.metricActiveWorking',
    short: 'compare.metricActiveWorkingShort',
    value: (m) => m.active_working_days,
  },
  {
    key: 'idle',
    label: 'compare.metricIdle',
    short: 'compare.metricIdleShort',
    value: (m) => m.idle_working_days,
  },
  { key: 'commits', label: 'compare.metricCommits', short: 'compare.metricCommitsShort', value: (m) => typeSum(m, 'gitlab.commit', 'gitlab.push') },
  {
    key: 'mrs',
    label: 'compare.metricMrs',
    short: 'compare.metricMrsShort',
    value: (m) => typeSum(m, 'gitlab.mr_opened', 'gitlab.mr_merged'),
  },
  { key: 'reviews', label: 'compare.metricReviews', short: 'compare.metricReviewsShort', value: (m) => typeSum(m, 'gitlab.review_comment') },
  { key: 'lines', label: 'compare.metricLines', short: 'compare.metricLinesShort', value: (m) => m.lines_changed },
  {
    key: 'issues',
    label: 'compare.metricIssues',
    short: 'compare.metricIssuesShort',
    value: (m) => typeSum(m, 'jira.issue_resolved'),
  },
  { key: 'worklog', label: 'compare.metricWorklog', short: 'compare.metricWorklogShort', value: (m) => m.worklog_hours, fractional: true },
  {
    key: 'meetings',
    label: 'compare.metricMeetings',
    short: 'compare.metricMeetingsShort',
    value: (m) => m.meeting_hours,
    fractional: true,
  },
  {
    key: 'docs',
    label: 'compare.metricDocs',
    short: 'compare.metricDocsShort',
    value: (m) => typeSum(m, 'gdocs.edit', 'gdocs.comment', 'gdocs.suggestion'),
  },
  {
    key: 'tests',
    label: 'compare.metricTests',
    short: 'compare.metricTestsShort',
    value: (m) =>
      typeSum(m, 'allure.launch', 'allure.testcase_created', 'allure.testcase_updated', 'allure.defect'),
  },
]

/** Колонки таблицы: активные рабочие дни уже показаны отдельной колонкой. */
const TABLE_METRICS = METRICS.filter((m) => m.key !== 'active_working')

/** Карточка человека: волна активности, тренд и дни без активности. */
function PersonCard({
  m,
  from,
  to,
  granularity,
}: {
  m: PersonMetrics
  from: string
  to: string
  granularity: 'day' | 'week'
}) {
  const ct = useChartTokens()
  const { t, tp } = useT()
  const values = useMemo(
    () => zeroFillSeries(m.timeline, from, to, granularity),
    [m.timeline, from, to, granularity],
  )
  const trend = seriesTrend(values)
  const idleShare = m.working_days > 0 ? m.idle_working_days / m.working_days : 0
  const name = m.person.display_name || m.person.key

  return (
    <article className="card person-card">
      <div className="person-card-head">
        <div>
          <strong>{name}</strong>
          <p className="muted person-card-sub">
            {[m.person.team, m.person.office].filter(Boolean).join(' · ') || '—'}
          </p>
        </div>
        {trend !== null && (
          <span
            className="num person-trend"
            style={{ color: trend >= 0 ? ct.positive : ct.negative }}
            title={t('compare.trendTitle')}
          >
            {trend >= 0 ? '↑' : '↓'} {Math.abs(trend)}%
          </span>
        )}
      </div>

      <Sparkline
        values={values}
        color={ct.slot(0)}
        height={52}
        ariaLabel={t('compare.sparklineAria', { name })}
      />

      <div className="person-card-stats num">
        <span>
          <span className="muted">{t('compare.cardEvents')}</span> {fmtNumber(m.total_events)}
        </span>
        <span>
          <span className="muted">{t('compare.cardActiveDays')}</span>{' '}
          {fmtNumber(m.active_working_days)} {t('compare.of')} {fmtNumber(m.working_days)}
        </span>
        <span style={{ color: idleShare > 0.25 ? ct.negative : undefined }}>
          <span className="muted">{t('compare.cardIdle')}</span> {fmtNumber(m.idle_working_days)}{' '}
          {tp('compare.pluralDays', m.idle_working_days)}
        </span>
        <span>
          <span className="muted">{t('compare.cardVacationSick')}</span> {fmtNumber(m.vacation_days)}
          {' / '}
          {fmtNumber(m.sick_days)}
        </span>
      </div>
    </article>
  )
}

export function ComparePage() {
  const { filters, setFilters } = useFilters()
  const [searchParams, setSearchParams] = useSearchParams()
  const ct = useChartTokens()
  const { t, tp } = useT()
  const teamsQ = useHrdbTeams()
  const teamSync = useStartTeamSync()
  const [metricKey, setMetricKey] = useState('total')

  // Мультивыбор команд: источник истины — локальное состояние (чекбоксы
  // реагируют мгновенно и не зависят от тонкостей роутера), URL (?teams=…)
  // синхронизируется вторично, чтобы ссылкой можно было делиться.
  const [selectedTeams, setSelectedTeamsState] = useState<string[]>(() =>
    (searchParams.get('teams') ?? '').split(',').map((s) => s.trim()).filter(Boolean),
  )
  const setSelectedTeams = (next: string[]) => {
    setSelectedTeamsState(next)
    setSearchParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        if (next.length) p.set('teams', next.join(','))
        else p.delete('teams')
        return p
      },
      { replace: true },
    )
  }

  const compare = useCompare({
    from: filters.from,
    to: filters.to,
    tz: TIMEZONE,
    team: selectedTeams.length ? selectedTeams : undefined,
    area: filters.area || undefined,
  })

  const rows = useMemo(() => compare.data?.people ?? [], [compare.data])

  // Направления для фильтра: из HRDB плюс встреченные у заведённых людей.
  const areaOptions = useMemo(() => {
    const set = new Set<string>(teamsQ.data?.areas ?? [])
    for (const m of rows) {
      if (m.person.area) set.add(m.person.area)
    }
    return [...set].sort()
  }, [teamsQ.data, rows])

  const metric = METRICS.find((m) => m.key === metricKey) ?? METRICS[0]

  const chartItems = useMemo(
    () =>
      rows
        .map((m) => ({
          key: m.person.key,
          label: m.person.display_name || m.person.key,
          count: Math.round(metric.value(m)),
        }))
        .sort((a, b) => b.count - a.count),
    [rows, metric],
  )

  // Группировка по направлению: сравнивать имеет смысл людей одного профиля.
  const byArea = useMemo(() => {
    const groups = new Map<string, PersonMetrics[]>()
    for (const m of rows) {
      const area = m.person.area || t('compare.noArea')
      const list = groups.get(area) ?? []
      list.push(m)
      groups.set(area, list)
    }
    return [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0], 'ru'))
  }, [rows, t])

  const fmtVal = (def: MetricDef, m: PersonMetrics) => {
    const v = def.value(m)
    return def.fractional ? v.toFixed(1) : fmtNumber(v)
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('compare.title')}</h1>
          <p className="num">
            {fmtDate(filters.from)} — {fmtDate(filters.to)}
            {rows.length > 0 && (
              <>
                {' · '}
                {fmtNumber(rows.length)} {tp('compare.pluralPeople', rows.length)}
              </>
            )}
          </p>
        </div>
      </div>

      <section className="card" style={{ padding: 14, display: 'grid', gap: 12 }}>
        <div className="form-grid">
          <TeamPicker
            teams={teamsQ.data?.teams ?? []}
            selected={selectedTeams}
            onChange={setSelectedTeams}
            label={t('compare.teamsLabel')}
          />
          <div className="field">
            <label htmlFor="compare-area">{t('compare.areaLabel')}</label>
            <select
              id="compare-area"
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
            <span className="field-label">{t('compare.syncLabel')}</span>
            <div className="inline-group">
              <button
                type="button"
                className="btn btn-primary"
                disabled={selectedTeams.length === 0 || teamSync.isPending}
                title={selectedTeams.length ? undefined : t('compare.syncHint')}
                onClick={() =>
                  selectedTeams.forEach((team) =>
                    teamSync.mutate({ team, from: filters.from, to: filters.to }),
                  )
                }
              >
                {teamSync.isPending ? t('compare.syncStarting') : t('compare.syncButton')}
              </button>
            </div>
          </div>
        </div>
        {teamSync.isError && (
          <p className="muted" role="alert">
            {t('compare.syncError', { error: teamSync.error.message })}
          </p>
        )}
        {teamSync.data && (
          <p className="muted">
            {t('compare.syncTeam', { team: teamSync.data.team, n: teamSync.data.members })}{' '}
            {tp('compare.pluralEmployees', teamSync.data.members)},{' '}
            {teamSync.data.created > 0 && (
              <>{t('compare.syncCreated', { n: teamSync.data.created })}, </>
            )}
            {t('compare.syncStarted', { n: teamSync.data.started })}{' '}
            {tp('compare.pluralSyncs', teamSync.data.started)} — {t('compare.syncNote')}
          </p>
        )}
      </section>

      {compare.isError ? (
        <ErrorState error={compare.error} onRetry={() => void compare.refetch()} />
      ) : compare.isLoading ? (
        <Skeleton height={340} radius={10} />
      ) : rows.length === 0 ? (
        <div className="card">
          <EmptyState
            title={t('compare.emptyTitle')}
            description={t('compare.emptyDescription')}
            showSyncLink={false}
          />
        </div>
      ) : (
        <>
          {byArea.map(([area, people]) => (
            <section className="stack" key={`cards-${area}`} style={{ gap: 10 }}>
              <h2 className="compare-group-title">
                {area}{' '}
                <span className="muted num" style={{ fontWeight: 400 }}>
                  · {people.length} {tp('compare.pluralPeople', people.length)}
                </span>
              </h2>
              <div className="grid grid-3">
                {[...people]
                  .sort((a, b) => b.total_events - a.total_events)
                  .map((m) => (
                    <PersonCard
                      key={m.person.key}
                      m={m}
                      from={compare.data?.from ?? filters.from}
                      to={compare.data?.to ?? filters.to}
                      granularity={compare.data?.granularity ?? 'day'}
                    />
                  ))}
              </div>
            </section>
          ))}

          <TypeBreakdown
            title={t(metric.label)}
            subtitle={t('compare.chartSubtitle')}
            items={chartItems}
            color={ct.slot(0)}
            categoryLabel={t('compare.employee')}
            valueLabel={t(metric.short)}
            limit={30}
            emptyTitle={t('compare.noData')}
            actions={
              <select
                className="select"
                aria-label={t('compare.metricAria')}
                value={metricKey}
                onChange={(e) => setMetricKey(e.target.value)}
              >
                {METRICS.map((m) => (
                  <option key={m.key} value={m.key}>
                    {t(m.label)}
                  </option>
                ))}
              </select>
            }
          />

          <section className="card" style={{ padding: 14, overflowX: 'auto' }}>
            <table className="data">
              <caption className="visually-hidden">
                {t('compare.tableCaption')}
              </caption>
              <thead>
                <tr>
                  <th scope="col">{t('compare.employee')}</th>
                  <th scope="col">{t('compare.team')}</th>
                  <th scope="col" className="num">{t('compare.workingDays')}</th>
                  {TABLE_METRICS.map((m) => (
                    <th scope="col" className="num" key={m.key}>
                      {t(m.short)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {byArea.map(([area, people]) => (
                  <Fragment key={area}>
                    <tr>
                      <th
                        colSpan={3 + TABLE_METRICS.length}
                        scope="colgroup"
                        style={{ paddingTop: 14, color: 'var(--text-secondary)' }}
                      >
                        {area} · {people.length}{' '}
                        {tp('compare.pluralPeople', people.length)}
                      </th>
                    </tr>
                    {[...people]
                      .sort((a, b) => metric.value(b) - metric.value(a))
                      .map((m) => (
                        <tr key={m.person.key}>
                          <th scope="row" style={{ fontWeight: 400 }}>
                            {m.person.display_name || m.person.key}
                          </th>
                          <td>{m.person.team || '—'}</td>
                          <td className="num">
                            {fmtNumber(m.active_working_days)} {t('compare.of')}{' '}
                            {fmtNumber(m.working_days)}
                            {m.vacation_days > 0 &&
                              ` (${t('compare.vacationShort')} ${fmtNumber(m.vacation_days)})`}
                          </td>
                          {TABLE_METRICS.map((def) => (
                            <td className="num" key={def.key}>
                              {fmtVal(def, m)}
                            </td>
                          ))}
                        </tr>
                      ))}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </section>
        </>
      )}
    </div>
  )
}
