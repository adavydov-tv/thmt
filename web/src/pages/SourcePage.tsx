import { useMemo } from 'react'
import { Navigate, useParams } from 'react-router-dom'
import { KpiTile } from '../components/KpiTile'
import { TimelineChart } from '../components/TimelineChart'
import { TypeBreakdown } from '../components/TypeBreakdown'
import { EventList } from '../components/EventList'
import { FilterBar } from '../components/FilterBar'
import { ActivityTypesPicker } from '../components/ActivityTypesPicker'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { useBreakdown, useEvents, useMeta, useSummary, useTimeline } from '../lib/queries'
import { useStatsQuery, useTypeLabeler } from '../lib/useStatsQuery'
import { useFilters } from '../lib/useFilters'
import { isSourceKey, sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtNumber } from '../lib/format'
import { useT, type MessageKey } from '../i18n'
import type { CountItem, Highlight, SourceKey, Summary } from '../lib/types'

interface SourceConfig {
  title: string
  subtitle: string
  projectsTitle: string
  projectsLabel: string
  typesTitle: string
  /** Резервные KPI, если бэкенд не вернул highlights. */
  fallbackKpis: (summary: Summary, types: CountItem[]) => Highlight[]
}

function pick(types: CountItem[], ...needles: string[]): number {
  return types
    .filter((t) => needles.some((n) => t.key.toLowerCase().includes(n)))
    .reduce((acc, t) => acc + t.count, 0)
}

type Translate = (key: MessageKey, args?: Record<string, string | number>) => string

// Конфиг собирается внутри компонента: подписи зависят от текущего языка,
// поэтому top-level константой быть не может.
function buildConfig(t: Translate): Record<SourceKey, SourceConfig> {
  return {
    jira: {
      title: 'Jira',
      subtitle: t('source.jira.subtitle'),
      projectsTitle: t('source.jira.projects'),
      projectsLabel: t('source.label.project'),
      typesTitle: t('source.jira.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('worklog', t('source.kpi.worklog'), s.worklog_hours, t('source.unit.hours')),
        kpi('transitions', t('source.kpi.transitions'), pick(types, 'transition', 'status')),
        kpi('comments', t('source.kpi.comments'), pick(types, 'comment')),
      ],
    },
    gitlab: {
      title: 'GitLab',
      subtitle: t('source.gitlab.subtitle'),
      projectsTitle: t('source.gitlab.projects'),
      projectsLabel: t('source.label.repo'),
      typesTitle: t('source.gitlab.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('commits', t('source.kpi.commits'), pick(types, 'commit', 'push')),
        kpi('mr', t('source.kpi.mrs'), pick(types, 'merge_request', 'mr_', 'merge')),
        kpi('lines', t('source.kpi.lines'), s.lines_changed),
      ],
    },
    slack: {
      title: 'Slack',
      subtitle: t('source.slack.subtitle'),
      projectsTitle: t('source.slack.projects'),
      projectsLabel: t('source.label.channel'),
      typesTitle: t('source.slack.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('messages', t('source.kpi.messages'), pick(types, 'message', 'post')),
        kpi('threads', t('source.kpi.threadReplies'), pick(types, 'thread', 'reply')),
        kpi('reactions', t('source.kpi.reactions'), pick(types, 'reaction')),
      ],
    },
    gdocs: {
      title: 'Google Docs',
      subtitle: t('source.gdocs.subtitle'),
      projectsTitle: t('source.gdocs.projects'),
      projectsLabel: t('source.label.document'),
      typesTitle: t('source.gdocs.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('edits', t('source.kpi.edits'), pick(types, 'edit', 'revision', 'change')),
        kpi('comments', t('source.kpi.comments'), pick(types, 'comment')),
      ],
    },
    allure: {
      title: 'Allure',
      subtitle: t('source.allure.subtitle'),
      projectsTitle: t('source.allure.projects'),
      projectsLabel: t('source.label.project'),
      typesTitle: t('source.allure.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('test_launches', t('source.kpi.launches'), pick(types, 'launch')),
        kpi('testcases', t('source.kpi.testcases'), pick(types, 'testcase')),
        kpi('defects', t('source.kpi.defects'), pick(types, 'defect')),
      ],
    },
    confluence: {
      title: 'Confluence',
      subtitle: t('source.confluence.subtitle'),
      projectsTitle: t('source.confluence.projects'),
      projectsLabel: t('source.label.space'),
      typesTitle: t('source.confluence.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('pages', t('source.kpi.pagesCreated'), pick(types, 'page_created', 'blogpost')),
        kpi('edits', t('source.kpi.edits'), pick(types, 'page_edited')),
        kpi('comments', t('source.kpi.comments'), pick(types, 'comment')),
      ],
    },
    gcal: {
      title: t('source.gcal.title'),
      subtitle: t('source.gcal.subtitle'),
      projectsTitle: t('source.meetingGroups'),
      projectsLabel: t('source.label.group'),
      typesTitle: t('source.gcal.types'),
      fallbackKpis: (s, types) => [
        kpi('total', t('source.kpi.events'), s.total_events),
        kpi('meetings', t('source.kpi.meetings'), pick(types, 'meeting', 'recurring', 'one_on_one', 'interview')),
        kpi('interviews', t('source.kpi.interviews'), pick(types, 'interview')),
        kpi('meeting_hours', t('source.kpi.meetingHours'), s.meeting_hours, t('source.unit.hours')),
      ],
    },
    gwork: {
      title: t('source.gwork.title'),
      subtitle: t('source.gwork.subtitle'),
      projectsTitle: t('overview.topProjects'),
      projectsLabel: t('source.label.document'),
      typesTitle: t('source.gwork.types'),
      fallbackKpis: (s) => [kpi('total', t('source.kpi.events'), s.total_events)],
    },
    argocd: {
      title: t('source.argocd.title'),
      subtitle: t('source.argocd.subtitle'),
      projectsTitle: t('source.argocd.apps'),
      projectsLabel: t('source.argocd.app'),
      typesTitle: t('source.argocd.types'),
      fallbackKpis: (s) => [kpi('total', t('source.argocd.deploys'), s.total_events)],
    },
    zabbix: {
      title: t('source.zabbix.title'),
      subtitle: t('source.zabbix.subtitle'),
      projectsTitle: t('overview.topProjects'),
      projectsLabel: t('source.label.document'),
      typesTitle: t('source.zabbix.types'),
      fallbackKpis: (s) => [kpi('total', t('source.kpi.events'), s.total_events)],
    },
    jenkins: {
      title: t('source.jenkins.title'),
      subtitle: t('source.jenkins.subtitle'),
      projectsTitle: t('source.jenkins.jobs'),
      projectsLabel: t('source.jenkins.job'),
      typesTitle: t('source.jenkins.types'),
      fallbackKpis: (s) => [kpi('total', t('source.jenkins.builds'), s.total_events)],
    },
    grafana: {
      title: t('source.grafana.title'),
      subtitle: t('source.grafana.subtitle'),
      projectsTitle: t('source.grafana.dashboards'),
      projectsLabel: t('source.grafana.dashboard'),
      typesTitle: t('source.grafana.types'),
      fallbackKpis: (s) => [kpi('total', t('source.grafana.edits'), s.total_events)],
    },
    figma: {
      title: t('source.figma.title'),
      subtitle: t('source.figma.subtitle'),
      projectsTitle: t('source.figma.files'),
      projectsLabel: t('source.figma.file'),
      typesTitle: t('source.figma.types'),
      fallbackKpis: (s) => [kpi('total', t('source.figma.actions'), s.total_events)],
    },
    netsuite: {
      title: t('source.netsuite.title'),
      subtitle: t('source.netsuite.subtitle'),
      projectsTitle: t('source.netsuite.records'),
      projectsLabel: t('source.netsuite.record'),
      typesTitle: t('source.netsuite.types'),
      fallbackKpis: (s) => [kpi('total', t('source.kpi.events'), s.total_events)],
    },
    claude: {
      title: t('source.claude.title'),
      subtitle: t('source.claude.subtitle'),
      projectsTitle: t('source.claude.records'),
      projectsLabel: t('source.claude.record'),
      typesTitle: t('source.claude.types'),
      fallbackKpis: (s) => [kpi('total', t('source.claude.records'), s.total_events)],
    },
  }
}

function kpi(key: string, label: string, value: number, unit?: string): Highlight {
  return { key, label, value: value ?? 0, unit, delta: 0, has_delta: false }
}

export function SourcePage() {
  const { source = '' } = useParams()
  const { filters, setFilters, linkSearch } = useFilters()
  const { t, tp } = useT()
  const tokens = useChartTokens()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)

  const valid = isSourceKey(source)
  const q = useStatsQuery({ source: valid ? [source] : undefined })

  const summary = useSummary(q, { enabled: valid && Boolean(filters.person) })
  const timeline = useTimeline(q, undefined, { enabled: valid && Boolean(filters.person) })
  const types = useBreakdown(q, 'type', { enabled: valid && Boolean(filters.person) })
  // Опции фильтров считаются без учёта самих фильтров — иначе после выбора
  // одного чипа остальные исчезали бы из списка.
  const optionsQ = useMemo(() => ({ ...q, project: undefined }), [q])
  const projects = useBreakdown(optionsQ, 'project', { enabled: valid && Boolean(filters.person) })
  const typeOptsQ = useMemo(() => ({ ...q, type: undefined }), [q])
  const typeOpts = useBreakdown(typeOptsQ, 'type', { enabled: valid && Boolean(filters.person) })
  // Собеседники по встречам 1:1 (meta.peer заполняет коллектор gcal).
  // События без собеседника попадают в группу «—» и здесь не показываются.
  const peers = useBreakdown(q, 'peer', {
    enabled: valid && source === 'gcal' && Boolean(filters.person),
  })
  const peerItems = useMemo(
    () => (peers.data?.items ?? []).filter((i) => i.key !== '—'),
    [peers.data],
  )
  const events = useEvents(
    { ...q, page: filters.page, per_page: filters.perPage, sort: 'desc' },
    { enabled: valid && Boolean(filters.person) },
  )

  const typeItems = useMemo(() => types.data?.items ?? [], [types.data])

  const config = useMemo(() => buildConfig(t), [t])

  const kpis = useMemo<Highlight[]>(() => {
    if (!summary.data || !valid) return []
    // Бэкенд отдаёт плитки по всем источникам сразу; на странице одного
    // источника показываем только его метрики плюс две сквозные, иначе экран
    // забивается нулями из чужих систем.
    const generic = new Set(['total', 'active_days'])
    const relevant = (summary.data.highlights ?? []).filter(
      (h) => generic.has(h.key) || h.source === source,
    )
    if (relevant.length > 0) return relevant
    return config[source].fallbackKpis(summary.data, typeItems)
  }, [summary.data, typeItems, source, valid, config])

  if (!valid) return <Navigate to="/" replace />

  const cfg = config[source]
  const color = tokens.sourceColor(source)

  if (!filters.person) {
    return (
      <EmptyState
        title={t('events.noPerson')}
        description={t('events.noPersonHint')}
        showSyncLink={false}
      />
    )
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            <span className="dot" style={{ background: color, marginRight: 8 }} aria-hidden="true" />
            {cfg.title}
          </h1>
          <p>
            {cfg.subtitle} · <span className="num">{fmtDate(filters.from)} — {fmtDate(filters.to)}</span>
            {summary.data && (
              <>
                {' · '}
                <span className="num">
                  {fmtNumber(summary.data.total_events)}{' '}
                  {tp('plural.events', summary.data.total_events)}
                </span>
              </>
            )}
          </p>
        </div>
      </div>

      {summary.isError ? (
        <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />
      ) : summary.isLoading ? (
        <div className="grid grid-kpi">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} height={92} radius={10} />
          ))}
        </div>
      ) : summary.data && summary.data.total_events === 0 ? (
        <div className="card">
          <EmptyState
            title={t('source.emptyTitle', { source: sourceLabel(source) })}
            description={t('source.emptyHint')}
          />
        </div>
      ) : (
        <div className="grid grid-kpi">
          {kpis.map((h) => (
            <KpiTile
              key={h.key}
              label={h.label}
              value={h.value}
              unit={h.unit}
              delta={h.delta}
              hasDelta={h.has_delta}
              markerColor={color}
            />
          ))}
        </div>
      )}

      <ActivityTypesPicker sources={[source]} />

      <FilterBar
        hideSources
        typeOptions={typeOpts.data?.items}
        projectOptions={projects.data?.items}
        typeLabel={typeLabel}
        projectsAsChips={source === 'gcal'}
        projectLabel={source === 'gcal' ? t('source.meetingGroups') : t('source.label.project')}
      />

      {timeline.isLoading ? (
        <Skeleton height={340} radius={10} />
      ) : timeline.isError ? (
        <ErrorState error={timeline.error} onRetry={() => void timeline.refetch()} />
      ) : (
        <TimelineChart
          title={t('source.activityTitle', { name: cfg.title })}
          subtitle={t('source.activitySubtitle')}
          points={timeline.data?.points ?? []}
          seriesKeys={[source]}
          granularity={timeline.data?.granularity ?? 'day'}
          colorOf={() => color}
          labelOf={() => cfg.title}
        />
      )}

      <div className="grid grid-2">
        <TypeBreakdown
          title={cfg.typesTitle}
          subtitle={t('source.typesHint')}
          items={typeItems.map((i) => ({ ...i, label: typeLabel(i.key) }))}
          color={color}
          categoryLabel={t('source.typeCategory')}
          onSelect={(item) =>
            setFilters({
              types: filters.types.includes(item.key)
                ? filters.types.filter((x) => x !== item.key)
                : [...filters.types, item.key],
            })
          }
          emptyTitle={t('source.noTypes')}
        />
        <TypeBreakdown
          title={cfg.projectsTitle}
          subtitle={t('source.projectsHint')}
          items={projects.data?.items ?? []}
          color={color}
          categoryLabel={cfg.projectsLabel}
          onSelect={(item) =>
            setFilters({
              projects: filters.projects.includes(item.key)
                ? filters.projects.filter((x) => x !== item.key)
                : [...filters.projects, item.key],
            })
          }
          emptyTitle={t('source.nothingFound')}
        />
        {source === 'gcal' && (
          <TypeBreakdown
            title={t('source.peersTitle')}
            subtitle={t('source.peersHint')}
            items={peerItems}
            color={color}
            categoryLabel={t('source.peerLabel')}
            valueLabel={t('source.kpi.meetings')}
            emptyTitle={t('source.noPeers')}
          />
        )}
      </div>

      <EventList
        title={t('events.feedTitle')}
        subtitle={t('source.feedSubtitle', { name: cfg.title })}
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
      />
    </div>
  )
}
