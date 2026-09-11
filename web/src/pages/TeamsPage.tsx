import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { TimelineChart } from '../components/TimelineChart'
import { SourceChips } from '../components/SourceChips'
import { ActivityTypesPicker } from '../components/ActivityTypesPicker'
import { TeamPicker } from '../components/TeamPicker'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { useCompareTeams, useEnabledSources, useHrdbTeams, useMeta } from '../lib/queries'
import { useActivityTypes } from '../lib/activityTypes'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { useChartTokens } from '../lib/chartTheme'
import { fmtDate, fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { TeamMetrics, TimelinePoint } from '../lib/types'

type Mode = 'absolute' | 'weighted'

/**
 * Сравнение команд: абсолютная активность и средневзвешенный показатель —
 * события на одного присутствующего сотрудника (присутствие считается
 * по-дневно: без выходных, праздников, отпусков, больничных и до даты найма).
 */
export function TeamsPage() {
  const { filters, setFilters } = useFilters()
  const [searchParams, setSearchParams] = useSearchParams()
  const ct = useChartTokens()
  const { t, tp } = useT()
  const { data: meta } = useMeta()
  const enabledSources = useEnabledSources()
  const teamsDir = useHrdbTeams()
  const { effectiveTypes } = useActivityTypes()
  const [mode, setMode] = useState<Mode>('weighted')

  // Выбор команд: источник истины — локальное состояние, URL — вторичен.
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

  const type = effectiveTypes(filters.types)
  const query = useCompareTeams({
    from: filters.from,
    to: filters.to,
    tz: TIMEZONE,
    team: selectedTeams.length ? selectedTeams : undefined,
    source: filters.sources.length ? filters.sources : undefined,
    type,
    min_ai: filters.minAi > 0 ? filters.minAi : undefined,
  })

  const teams = useMemo(() => query.data?.teams ?? [], [query.data])

  const hrdbTeam = (name: string) =>
    teamsDir.data?.teams.find((ht) => ht.name.toLowerCase() === name.toLowerCase())
  const hrdbSize = (name: string) => hrdbTeam(name)?.members

  // Опции пикера: команды из HRDB плюс команды, встречающиеся в данных.
  const pickerTeams = useMemo(() => {
    const byName = new Map((teamsDir.data?.teams ?? []).map((t) => [t.name.toLowerCase(), t]))
    for (const tm of teams) {
      if (!byName.has(tm.team.toLowerCase())) {
        byName.set(tm.team.toLowerCase(), { name: tm.team, members: tm.members })
      }
    }
    return [...byName.values()]
  }, [teams, teamsDir.data])

  // Общий график: серия — команда; значение — события или события на
  // присутствующего (по выбранному режиму).
  const chartPoints = useMemo<TimelinePoint[]>(() => {
    const byBucket = new Map<string, TimelinePoint>()
    for (const tm of teams) {
      const series = mode === 'absolute' ? tm.timeline ?? [] : tm.weighted ?? []
      for (const p of series) {
        const key = p.bucket
        let point = byBucket.get(key)
        if (!point) {
          point = { bucket: key, total: 0, counts: {} }
          byBucket.set(key, point)
        }
        const value = 'value' in p ? p.value : p.count
        point.counts[tm.team] = value
        point.total += value
      }
    }
    return [...byBucket.values()].sort((a, b) => a.bucket.localeCompare(b.bucket))
  }, [teams, mode])

  const teamColor = useMemo(() => {
    const map = new Map<string, string>()
    teams.forEach((tm, i) => map.set(tm.team, ct.slot(i)))
    return map
  }, [teams, ct])

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('teams.title')}</h1>
          <p className="num">
            {fmtDate(filters.from)} — {fmtDate(filters.to)}
            {teams.length > 0 && (
              <>
                {' · '}
                {fmtNumber(teams.length)} {tp('compare.pluralTeams', teams.length)}
              </>
            )}
          </p>
        </div>
      </div>

      <ActivityTypesPicker />

      <section className="card" style={{ padding: 14, display: 'grid', gap: 12 }}>
        <div className="form-grid">
          <TeamPicker
            teams={pickerTeams}
            selected={selectedTeams}
            onChange={setSelectedTeams}
            label={t('teams.teamsLabel')}
          />
        </div>

        <SourceChips
          selected={filters.sources}
          onChange={(next) => setFilters({ sources: next })}
          options={enabledSources.map((key) => ({ key, label: '', enabled: true }))}
        />

        {meta?.ai_enabled && (
          <div className="field" style={{ maxWidth: 260 }}>
            <label htmlFor="teams-min-ai">{t('teams.minAiLabel')}</label>
            <select
              id="teams-min-ai"
              className="select"
              value={filters.minAi || ''}
              onChange={(e) => setFilters({ minAi: Number(e.target.value) || 0 })}
            >
              <option value="">{t('teams.noFilter')}</option>
              {[1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => (
                <option key={n} value={n}>
                  ≥ {n}
                </option>
              ))}
            </select>
          </div>
        )}
      </section>

      {query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.isLoading ? (
        <Skeleton height={340} radius={10} />
      ) : teams.length === 0 ? (
        <div className="card">
          <EmptyState
            title={t('teams.emptyTitle')}
            description={t('teams.emptyDescription')}
            showSyncLink={false}
          />
        </div>
      ) : (
        <>
          <TimelineChart
            title={mode === 'weighted' ? t('teams.chartTitleWeighted') : t('teams.chartTitleAbsolute')}
            subtitle={
              mode === 'weighted'
                ? t('teams.chartSubtitleWeighted')
                : t('teams.chartSubtitleAbsolute')
            }
            points={chartPoints}
            seriesKeys={teams.map((tm) => tm.team)}
            granularity={query.data?.granularity ?? 'day'}
            colorOf={(key) => teamColor.get(key) ?? ct.slot(0)}
            labelOf={(key) => key}
            actions={
              <div className="segmented" role="group" aria-label={t('teams.modeAria')}>
                <button
                  type="button"
                  aria-pressed={mode === 'weighted'}
                  onClick={() => setMode('weighted')}
                >
                  {t('teams.modePerPerson')}
                </button>
                <button
                  type="button"
                  aria-pressed={mode === 'absolute'}
                  onClick={() => setMode('absolute')}
                >
                  {t('teams.modeTotal')}
                </button>
              </div>
            }
          />

          <section className="card" style={{ padding: 14, overflowX: 'auto' }}>
            <table className="data">
              <caption className="visually-hidden">{t('teams.tableCaption')}</caption>
              <thead>
                <tr>
                  <th scope="col">{t('teams.colTeam')}</th>
                  <th scope="col" className="num">{t('teams.colMembers')}</th>
                  <th scope="col" className="num">{t('teams.colEvents')}</th>
                  <th scope="col" className="num">{t('teams.colPersonDays')}</th>
                  <th scope="col" className="num">{t('teams.colEventsPerDay')}</th>
                </tr>
              </thead>
              <tbody>
                {[...teams]
                  .sort((a, b) => b.events_per_person_day - a.events_per_person_day)
                  .map((tm: TeamMetrics) => (
                    <tr key={tm.team}>
                      <th scope="row" style={{ fontWeight: 400 }}>
                        <span
                          className="dot"
                          style={{ background: teamColor.get(tm.team), marginRight: 6 }}
                          aria-hidden="true"
                        />
                        {tm.team}
                      </th>
                      <td className="num">
                        {fmtNumber(tm.members)}
                        {hrdbSize(tm.team) !== undefined && ` / ${fmtNumber(hrdbSize(tm.team)!)}`}
                      </td>
                      <td className="num">{fmtNumber(tm.total_events)}</td>
                      <td className="num">{fmtNumber(tm.person_days)}</td>
                      <td className="num">{tm.events_per_person_day.toFixed(2)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
            <p className="muted" style={{ marginTop: 10, fontSize: 12 }}>
              {t('teams.footnote')}
            </p>
          </section>
        </>
      )}
    </div>
  )
}
