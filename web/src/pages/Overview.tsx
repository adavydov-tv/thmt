import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { KpiTile } from '../components/KpiTile'
import { TimelineChart } from '../components/TimelineChart'
import { SourceBreakdown } from '../components/SourceBreakdown'
import { TypeBreakdown } from '../components/TypeBreakdown'
import { PeriodComparison } from '../components/PeriodComparison'
import { ActivityMix } from '../components/ActivityMix'
import { ActivityCalendar, type SpecialDay } from '../components/ActivityCalendar'
import { DeviationsCard } from '../components/DeviationsCard'
import { ActivityTypesPicker } from '../components/ActivityTypesPicker'
import { DayDistribution } from '../components/DayDistribution'
import { FilterBar } from '../components/FilterBar'
import { Heatmap } from '../components/Heatmap'
import { EventList } from '../components/EventList'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { PersonFacts } from '../components/PersonFacts'
import { SectionNav, type PageSection } from '../components/SectionNav'
import { Skeleton } from '../components/Skeleton'
import {
  useBreakdown,
  useDays,
  useEnabledSources,
  useEvents,
  useHeatmap,
  useMeta,
  usePeople,
  useSummary,
  useTimeline,
  useViolations,
} from '../lib/queries'
import { useStatsQuery, useTypeLabeler } from '../lib/useStatsQuery'
import { TIMELINE_STEPS, timelineStepLabel, useFilters } from '../lib/useFilters'
import { SOURCE_KEYS, sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { Granularity } from '../lib/types'

export function Overview() {
  const { filters, setFilters, linkSearch } = useFilters()
  const q = useStatsQuery()
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)

  const step = filters.granularity
  const summary = useSummary(q)
  const timeline = useTimeline(q, step === 'auto' ? undefined : step)
  // Матрице активности всегда нужен шаг «день», независимо от шага таймлайна.
  const dailyTimeline = useTimeline(q, 'day')
  const types = useBreakdown(q, 'type')
  const heatmap = useHeatmap(q)
  const recent = useEvents({ ...q, page: 1, per_page: 15, sort: 'desc' })
  const violations = useViolations(q)
  const people = usePeople()
  const person = people.data?.find((p) => p.key === filters.person)

  /** Как называем фактический шаг, который выбрал бэкенд. */
  const granularityText: Record<Granularity, string> = {
    hour: tr('granularity.hour'),
    day: tr('granularity.day'),
    week: tr('granularity.week'),
    month: tr('granularity.month'),
  }

  // Праздники, отпуска и больничные для матрицы активности; при пересечении
  // приоритет: отпуск > больничный > праздник.
  const daysInfo = useDays(q)
  const specialDays = useMemo(() => {
    const rank = { vacation: 3, sick: 2, holiday: 1, remote: 0 } as const
    const map: Record<string, SpecialDay> = {}
    for (const d of daysInfo.data?.days ?? []) {
      const key = d.day.slice(0, 10)
      const cur = map[key]
      if (cur && rank[cur.kind] >= rank[d.kind]) continue
      map[key] = { kind: d.kind, label: d.label }
    }
    // Гибридные дни из дома (HRDB) — добавляем к дням из VAC-заявок;
    // отпуск/больничный/праздник/явный remote в тот же день важнее.
    // hybrid_dates — явные даты с учётом истории изменений шаблона;
    // недельный шаблон hybrid_days остаётся фолбэком для старого API.
    const hybridDates = daysInfo.data?.hybrid_dates
    if (hybridDates && hybridDates.length > 0) {
      for (const key of hybridDates) {
        if (!map[key]) map[key] = { kind: 'remote' }
      }
    } else {
      const hybrid = new Set(daysInfo.data?.hybrid_days ?? [])
      if (hybrid.size > 0) {
        const end = new Date(filters.to)
        for (const d = new Date(filters.from); d.getTime() <= end.getTime(); d.setDate(d.getDate() + 1)) {
          const iso = ((d.getDay() + 6) % 7) + 1
          if (!hybrid.has(iso)) continue
          const pad = (n: number) => String(n).padStart(2, '0')
          const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
          if (!map[key]) map[key] = { kind: 'remote' }
        }
      }
    }
    return map
  }, [daysInfo.data, filters.from, filters.to])

  // Опции фильтров считаются без учёта фильтров по типам и проектам — иначе
  // выбор группы встреч или проекта сокращал бы списки чипов: чипы должны
  // оставаться на месте, меняется только их подсветка.
  const enabledSources = useEnabledSources()
  const activeSources = filters.sources.length ? filters.sources : enabledSources
  const gcalActive = activeSources.includes('gcal')
  const projectSources = activeSources.filter((s) => s !== 'gcal')
  const projectsQ = useMemo(
    () => ({ ...q, source: projectSources, project: undefined, type: undefined }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [q, projectSources.join(',')],
  )
  const projectOptions = useBreakdown(projectsQ, 'project', {
    enabled: projectSources.length > 0 && Boolean(filters.person),
  })
  const groupsQ = useMemo(
    () => ({ ...q, source: ['gcal'], project: undefined, type: undefined }),
    [q],
  )
  const groupOptions = useBreakdown(groupsQ, 'project', {
    enabled: gcalActive && Boolean(filters.person),
  })
  // Опции чипов типов — без фильтров по типам И по проектам: выбор группы
  // встреч не должен прятать типы событий из списка.
  const typeOptsQ = useMemo(() => ({ ...q, type: undefined, project: undefined }), [q])
  const typeOptions = useBreakdown(typeOptsQ, 'type')

  const seriesKeys = useMemo(() => {
    const present = new Set<string>()
    for (const p of timeline.data?.points ?? []) {
      for (const [key, value] of Object.entries(p.counts ?? {})) {
        if (value > 0) present.add(key)
      }
    }
    for (const item of summary.data?.by_source ?? []) {
      if (item.count > 0) present.add(item.key)
    }
    const ordered = SOURCE_KEYS.filter((k) => present.has(k))
    const extra = [...present].filter((k) => !(SOURCE_KEYS as string[]).includes(k)).sort()
    return [...ordered, ...extra]
  }, [timeline.data, summary.data])

  const highlights = summary.data?.highlights ?? []

  const usedGranularity = timeline.data?.granularity
  const timelineSubtitle =
    step === 'auto'
      ? `${tr('overview.timelineSubtitle')} · ${tr('overview.stepAuto')}${
          usedGranularity ? `: ${granularityText[usedGranularity]}` : ''
        }`
      : `${tr('overview.timelineSubtitle')} · ${tr('overview.step')}: ${granularityText[step]}`

  const stepSwitch = (
    <div className="segmented" role="group" aria-label={tr('overview.stepAria')}>
      {TIMELINE_STEPS.map((option) => (
        <button
          key={option}
          type="button"
          aria-pressed={step === option}
          onClick={() => setFilters({ granularity: option })}
        >
          {timelineStepLabel(option)}
        </button>
      ))}
    </div>
  )

  const sections: PageSection[] = [
    { id: 'sec-calendar', label: tr('overview.secCalendar') },
    { id: 'sec-deviations', label: tr('overview.secDeviations') },
    { id: 'sec-timeline', label: tr('overview.secTimeline') },
    { id: 'sec-sources', label: tr('overview.secSources') },
    { id: 'sec-mix', label: tr('overview.secMix') },
    { id: 'sec-days', label: tr('overview.secDays') },
    { id: 'sec-heatmap', label: tr('overview.secHeatmap') },
    { id: 'sec-events', label: tr('overview.secEvents') },
  ]

  if (!filters.person) {
    return (
      <EmptyState
        title={tr('overview.noPerson')}
        description={tr('overview.noPersonHint')}
        showSyncLink={false}
        action={
          <Link className="btn btn-primary" to="/settings">
            {tr('overview.goSettings')}
          </Link>
        }
      />
    )
  }

  if (summary.isError) {
    return <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />
  }

  const noData = summary.data && summary.data.total_events === 0

  return (
    <div className="page-with-rail">
      <div className="stack">
        <div className="page-head">
          <div>
            <h1>{person?.display_name || tr('overview.title')}</h1>
            {person && (person.title || person.area) && (
              <p className="person-meta">
                {person.title && <span>{person.title}</span>}
                {person.title && person.area && <span className="person-meta-sep">·</span>}
                {person.area && <span className="muted">{person.area}</span>}
              </p>
            )}
            <p className="num">
              {fmtDate(filters.from)} — {fmtDate(filters.to)}
              {summary.data && (
                <>
                  {' · '}
                  {fmtNumber(summary.data.total_events)} {tp('plural.events', summary.data.total_events)}
                  {' · '}
                  {tr('overview.activeDays', {
                    active: fmtNumber(summary.data.active_days),
                    span: fmtNumber(summary.data.span_days),
                  })}
                </>
              )}
            </p>
            {summary.data && summary.data.working_days > 0 && (
              <p className="num">
                {tr('overview.workingDays', {
                  active: fmtNumber(summary.data.active_working_days),
                  total: fmtNumber(summary.data.working_days),
                })}
                {' · '}
                {fmtNumber(summary.data.weekend_days + summary.data.holiday_days)}{' '}
                {tr('overview.weekendHoliday')}
                {summary.data.vacation_days > 0 && (
                  <>
                    {' · '}
                    {fmtNumber(summary.data.vacation_days)}{' '}
                    {tp('plural.vacationDays', summary.data.vacation_days)}
                  </>
                )}
                {summary.data.sick_days > 0 && (
                  <>
                    {' · '}
                    {fmtNumber(summary.data.sick_days)} {tp('plural.sickDays', summary.data.sick_days)}
                  </>
                )}
              </p>
            )}
          </div>
        </div>

        <PersonFacts summary={summary.data} heatmap={heatmap.data} />

        <FilterBar
          collapsible
          typeOptions={typeOptions.data?.items}
          typeLabel={typeLabel}
          projectOptions={projectSources.length > 0 ? projectOptions.data?.items : []}
          projectsAsChips
          projectLabel={tr('filters.projects')}
          groupOptions={gcalActive ? groupOptions.data?.items : []}
          groupLabel={tr('filters.meetingGroups')}
          extra={<ActivityTypesPicker embedded />}
        />

        {summary.isLoading ? (
          <div className="grid grid-kpi">
            {Array.from({ length: 4 }, (_, i) => (
              <Skeleton key={i} height={92} radius={10} />
            ))}
          </div>
        ) : noData ? (
          <div className="card">
            <EmptyState title={tr('overview.noData')} description={tr('overview.noDataHint')} />
          </div>
        ) : (
          highlights.length > 0 && (
            <div className="grid grid-kpi">
              {highlights.map((h) => (
                <KpiTile
                  key={h.key}
                  label={h.label}
                  value={h.value}
                  unit={h.unit}
                  delta={h.delta}
                  hasDelta={h.has_delta}
                  markerColor={h.source ? t.sourceColor(h.source) : undefined}
                />
              ))}
            </div>
          )
        )}

        {!noData && (
          <>
            <div id="sec-calendar">
              {dailyTimeline.isError ? (
                <ErrorState
                  error={dailyTimeline.error}
                  onRetry={() => void dailyTimeline.refetch()}
                />
              ) : dailyTimeline.isLoading ? (
                <Skeleton height={200} radius={10} />
              ) : (
                <ActivityCalendar
                  points={dailyTimeline.data?.points ?? []}
                  from={filters.from}
                  to={filters.to}
                  specialDays={specialDays}
                  overtimeDays={daysInfo.data?.overtime_days}
                  shallowDays={daysInfo.data?.shallow_days}
                  hireDate={daysInfo.data?.hire_date || undefined}
                />
              )}
            </div>

            <div id="sec-deviations">
              {violations.isError ? (
                <ErrorState error={violations.error} onRetry={() => void violations.refetch()} />
              ) : violations.isLoading ? (
                <Skeleton height={140} radius={10} />
              ) : (
                <DeviationsCard
                  violations={violations.data?.violations ?? []}
                  personKey={filters.person}
                  search={linkSearch}
                />
              )}
            </div>

            <div id="sec-timeline">
              {timeline.isError ? (
                <ErrorState error={timeline.error} onRetry={() => void timeline.refetch()} />
              ) : timeline.isLoading ? (
                <Skeleton height={340} radius={10} />
              ) : (
                <TimelineChart
                  title={tr('overview.secTimeline')}
                  subtitle={timelineSubtitle}
                  points={timeline.data?.points ?? []}
                  seriesKeys={seriesKeys.length ? seriesKeys : ['jira']}
                  granularity={timeline.data?.granularity ?? 'day'}
                  colorOf={(key) => t.sourceColor(key)}
                  labelOf={sourceLabel}
                  actions={stepSwitch}
                />
              )}
            </div>

            <div className="grid grid-2" id="sec-sources">
              <SourceBreakdown
                items={summary.data?.by_source ?? []}
                subtitle={tr('overview.sourceShare')}
              />
              {summary.isLoading || !summary.data ? (
                <Skeleton height={340} radius={10} />
              ) : (
                <PeriodComparison
                  items={summary.data.by_source ?? []}
                  prevItems={summary.data.prev_by_source ?? []}
                  total={summary.data.total_events}
                  prevTotal={summary.data.prev_total}
                  from={filters.from}
                  to={filters.to}
                />
              )}
            </div>

            <div className="grid grid-2" id="sec-mix">
              {types.isError ? (
                <ErrorState error={types.error} onRetry={() => void types.refetch()} />
              ) : types.isLoading ? (
                <Skeleton height={340} radius={10} />
              ) : (
                <ActivityMix
                  items={(types.data?.items ?? []).map((i) => ({
                    ...i,
                    label: i.label || typeLabel(i.key),
                  }))}
                />
              )}
              <TypeBreakdown
                title={tr('overview.topProjects')}
                subtitle={tr('overview.topProjectsHint')}
                items={summary.data?.top_projects ?? []}
                categoryLabel={tr('overview.project')}
                color={t.slot(0)}
                emptyTitle={tr('overview.noProjects')}
              />
            </div>

            <div id="sec-days">{summary.data && <DayDistribution summary={summary.data} />}</div>

            <div id="sec-heatmap">
              {heatmap.isError ? (
                <ErrorState error={heatmap.error} onRetry={() => void heatmap.refetch()} />
              ) : heatmap.isLoading ? (
                <Skeleton height={260} radius={10} />
              ) : (
                <Heatmap cells={heatmap.data ?? []} subtitle={tr('overview.heatmapHint')} />
              )}
            </div>

            <div id="sec-events">
              <EventList
                title={tr('overview.recentEvents')}
                subtitle={tr('overview.recentEventsHint')}
                events={recent.data?.items}
                isLoading={recent.isLoading}
                error={recent.isError ? recent.error : undefined}
                onRetry={() => void recent.refetch()}
                typeLabel={typeLabel}
                search={linkSearch}
                actions={
                  <Link className="btn btn-sm" to={`/events${linkSearch}`}>
                    {tr('overview.allEvents')}
                  </Link>
                }
              />
            </div>
          </>
        )}
      </div>
      {!noData && <SectionNav sections={sections} />}
    </div>
  )
}
