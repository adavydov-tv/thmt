import { Fragment, useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { KpiTile } from '../components/KpiTile'
import { Skeleton } from '../components/Skeleton'
import { TypeBreakdown } from '../components/TypeBreakdown'
import { api, buildQuery } from '../lib/api'
import { useHrdbTeams } from '../lib/queries'
import { useChartTokens } from '../lib/chartTheme'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { fmtDate } from '../lib/format'
import { ruleGroups, ruleGroupOf, ruleHelpEntries, ruleHint, ruleLabel } from '../lib/ruleHelp'
import { useT } from '../i18n'
import type { ViolationItem } from '../lib/types'

/**
 * Вкладка «Отклонения»: сводка, кликабельный список людей со шкалой
 * положительных/отрицательных находок, графики и таблица, сгруппированная по
 * типам отклонений. Режимы «Негативные»/«Позитивные» — через ?tone= из меню.
 */
export function ViolationsPage() {
  const { filters, setFilters } = useFilters()
  const { t, tp } = useT()
  const teamsDir = useHrdbTeams()
  const tk = useChartTokens()
  const [searchParams, setSearchParams] = useSearchParams()
  const [cluster, setCluster] = useState('')
  const [department, setDepartment] = useState('')
  const [ruleFilter, setRuleFilter] = useState<string[]>([])
  const [groupFilter, setGroupFilter] = useState<string[]>([])

  // Режим из меню: warn — только негативные, info — только позитивные.
  const tone = searchParams.get('tone') ?? ''
  // Фокус на человеке — из кликабельного списка людей или с Обзора.
  const vperson = searchParams.get('vperson') ?? ''
  const setVperson = (key: string) => {
    setSearchParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        if (key) p.set('vperson', key)
        else p.delete('vperson')
        return p
      },
      { replace: true },
    )
  }

  const params = {
    from: filters.from,
    to: filters.to,
    tz: TIMEZONE,
    team: filters.team || undefined,
    cluster: cluster || undefined,
    department: department || undefined,
  }
  const query = useQuery({
    queryKey: ['violations', params] as const,
    queryFn: () => api.violations(params),
    // Смена фильтра не «моргает» пустой страницей: старые данные видны,
    // пока грузятся новые; свежий результат живёт 5 минут без перезапроса.
    placeholderData: (prev) => prev,
    staleTime: 5 * 60_000,
  })

  const all = useMemo(() => query.data?.violations ?? [], [query.data])
  // Фильтры по режиму, группам, типам и человеку — клиентские.
  const items = useMemo(() => {
    let out = all
    if (tone) out = out.filter((v) => v.severity === tone)
    if (vperson) out = out.filter((v) => v.person_key === vperson)
    if (groupFilter.length) out = out.filter((v) => groupFilter.includes(ruleGroupOf(v.rule).key))
    if (ruleFilter.length) out = out.filter((v) => ruleFilter.includes(v.rule))
    return out
  }, [all, tone, vperson, ruleFilter, groupFilter])
  const warns = items.filter((v) => v.severity === 'warn')
  const infos = items.length - warns.length
  const peopleTouched = new Set(items.map((v) => v.person)).size

  // Кликабельный список людей: количество и шкала warn/info (по данным до
  // клиентских фильтров по человеку, чтобы список не схлопывался).
  const peopleStats = useMemo(() => {
    // При фокусе на человеке (переход из обзора) в списке — только он.
    let base = tone ? all.filter((v) => v.severity === tone) : all
    if (vperson) base = base.filter((v) => v.person_key === vperson)
    const m = new Map<string, { key: string; name: string; team: string; warn: number; info: number }>()
    for (const v of base) {
      const cur = m.get(v.person_key) ?? {
        key: v.person_key,
        name: v.person,
        team: v.team ?? '',
        warn: 0,
        info: 0,
      }
      if (v.severity === 'warn') cur.warn++
      else cur.info++
      m.set(v.person_key, cur)
    }
    return [...m.values()].sort((a, b) => b.warn - a.warn || b.info - a.info)
  }, [all, tone, vperson])
  const maxPersonTotal = Math.max(1, ...peopleStats.map((p) => p.warn + p.info))

  // Графики: по типам (в текущем режиме) и warn по командам.
  const toneItems = useMemo(
    () => (tone ? all.filter((v) => v.severity === tone) : all),
    [all, tone],
  )
  const byRule = useMemo(() => {
    const m = new Map<string, number>()
    for (const v of toneItems) m.set(v.rule, (m.get(v.rule) ?? 0) + 1)
    return [...m.entries()]
      .map(([rule, count]) => ({ key: rule, label: ruleLabel(rule), count }))
      .sort((a, b) => b.count - a.count)
  }, [toneItems])
  const byGroup = useMemo(() => {
    const m = new Map<string, number>()
    for (const v of toneItems) {
      const g = ruleGroupOf(v.rule).key
      m.set(g, (m.get(g) ?? 0) + 1)
    }
    return m
  }, [toneItems])
  const warnByTeam = useMemo(() => {
    const m = new Map<string, number>()
    for (const v of items) {
      if (v.severity !== 'warn') continue
      const team = v.team || '—'
      m.set(team, (m.get(team) ?? 0) + 1)
    }
    return [...m.entries()]
      .map(([team, count]) => ({ key: team, label: team, count }))
      .sort((a, b) => b.count - a.count)
  }, [items])

  // Таблица делится на крупные группы (Трудовая дисциплина, Переработки, …),
  // внутри группы — секции по типу отклонения: одинаковые находки лежат
  // рядом, у секций — цвет группы, подпись правила и счётчик.
  const groupSections = useMemo(() => {
    const byRule = new Map<string, ViolationItem[]>()
    for (const v of items) {
      const arr = byRule.get(v.rule) ?? []
      arr.push(v)
      byRule.set(v.rule, arr)
    }
    for (const arr of byRule.values()) {
      arr.sort((a, b) => a.person.localeCompare(b.person, 'ru'))
    }
    return ruleGroups()
      .map((g) => {
        const rules = [...byRule.entries()]
          .filter(([rule]) => ruleGroupOf(rule).key === g.key)
          // Негативные правила раньше позитивных, внутри — по числу находок.
          .sort((a, b) => {
            const aWarn = a[1][0]?.severity === 'warn' ? 0 : 1
            const bWarn = b[1][0]?.severity === 'warn' ? 0 : 1
            return aWarn - bWarn || b[1].length - a[1].length
          })
        const total = rules.reduce((acc, [, list]) => acc + list.length, 0)
        return { group: g, rules, total }
      })
      .filter((g) => g.rules.length > 0)
  }, [items])

  const exportHref = `/api/violations/export${buildQuery({ ...params })}`

  const unitSync = useMutation({
    mutationFn: () =>
      api.startUnitSync({
        cluster: cluster || undefined,
        department: cluster ? undefined : department || undefined,
        from: filters.from,
        to: filters.to,
      }),
  })
  const unitName = cluster || department

  const toggleRule = (rule: string) =>
    setRuleFilter((prev) => (prev.includes(rule) ? prev.filter((r) => r !== rule) : [...prev, rule]))

  const groups = ruleGroups()
  const title =
    tone === 'warn'
      ? t('viol.titleNegative')
      : tone === 'info'
        ? t('viol.titlePositive')
        : t('viol.title')

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{title}</h1>
          <p className="num">
            {fmtDate(filters.from)} — {fmtDate(filters.to)}
            {vperson && (
              <>
                {' · '}
                {peopleStats.find((p) => p.key === vperson)?.name ?? vperson}{' '}
                <button type="button" className="chip" onClick={() => setVperson('')}>
                  ✕ {t('viol.dropPerson')}
                </button>
              </>
            )}
          </p>
        </div>
      </div>

      <section className="card" style={{ padding: 14, display: 'grid', gap: 10 }}>
        <div className="form-grid">
          <div className="field">
            <label htmlFor="viol-dept">{t('viol.department')}</label>
            <select
              id="viol-dept"
              className="select"
              value={department}
              onChange={(e) => setDepartment(e.target.value)}
            >
              <option value="">{t('viol.allDepartments')}</option>
              {teamsDir.data?.departments.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="viol-cluster">{t('viol.cluster')}</label>
            <select
              id="viol-cluster"
              className="select"
              value={cluster}
              onChange={(e) => setCluster(e.target.value)}
            >
              <option value="">{t('viol.allClusters')}</option>
              {teamsDir.data?.clusters.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="viol-team">{t('viol.team')}</label>
            <select
              id="viol-team"
              className="select"
              value={filters.team}
              onChange={(e) => setFilters({ team: e.target.value })}
            >
              <option value="">{t('viol.allTeams')}</option>
              {teamsDir.data?.teams.map((tm) => (
                <option key={tm.name} value={tm.name} title={(tm.chain ?? []).join(' → ')}>
                  {[...(tm.chain ?? [tm.name])].reverse().join(' / ')}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <span className="field-label">{t('viol.dataExport')}</span>
            <div className="inline-group">
              <button
                type="button"
                className="btn btn-primary btn-sm"
                disabled={!unitName || unitSync.isPending}
                title={unitName ? `${t('viol.unitSyncHintOk')}: ${unitName}` : t('viol.unitSyncHint')}
                onClick={() => {
                  if (window.confirm(t('viol.unitSyncConfirm', { unit: unitName }))) {
                    unitSync.mutate()
                  }
                }}
              >
                {unitSync.isPending ? t('viol.starting') : t('viol.unitSync')}
              </button>
              <a className="btn btn-sm" href={exportHref} download>
                {t('viol.exportCsv')}
              </a>
            </div>
          </div>
        </div>

        <div className="field">
          <span className="field-label" id="group-chips-label">
            {t('viol.groups')}
          </span>
          <div className="chips" role="group" aria-labelledby="group-chips-label">
            {groups.map((g) => (
              <button
                key={g.key}
                type="button"
                className="chip"
                aria-pressed={groupFilter.includes(g.key)}
                onClick={() =>
                  setGroupFilter((prev) =>
                    prev.includes(g.key) ? prev.filter((x) => x !== g.key) : [...prev, g.key],
                  )
                }
              >
                <span className="dot" style={{ background: tk.slot(g.slot) }} aria-hidden="true" />
                {g.label}
                <span className="num muted">{byGroup.get(g.key) ?? 0}</span>
              </button>
            ))}
            {groupFilter.length > 0 && (
              <button type="button" className="chip" onClick={() => setGroupFilter([])}>
                {t('filters.reset')}
              </button>
            )}
          </div>
        </div>

        <div className="field">
          <span className="field-label" id="rule-chips-label">
            {t('viol.ruleTypes')}
          </span>
          <div className="chips" role="group" aria-labelledby="rule-chips-label">
            {byRule.map((r) => (
              <button
                key={r.key}
                type="button"
                className="chip"
                aria-pressed={ruleFilter.includes(r.key)}
                title={ruleHint(r.key)}
                onClick={() => toggleRule(r.key)}
              >
                <span
                  className="dot"
                  style={{ background: tk.slot(ruleGroupOf(r.key).slot) }}
                  aria-hidden="true"
                />
                {r.label}
                <span className="num muted">{r.count}</span>
              </button>
            ))}
            {ruleFilter.length > 0 && (
              <button type="button" className="chip" onClick={() => setRuleFilter([])}>
                {t('filters.reset')}
              </button>
            )}
          </div>
        </div>

        {unitSync.data && (
          <p className="muted">
            «{unitSync.data.team}»: {unitSync.data.members} {tp('plural.employees', unitSync.data.members)},{' '}
            {unitSync.data.created > 0 && (
              <>
                {t('viol.createdNew')}: {unitSync.data.created},{' '}
              </>
            )}
            {t('viol.started')} {unitSync.data.started} {tp('plural.syncs', unitSync.data.started)} —{' '}
            {t('viol.progressInSettings')}
          </p>
        )}
        {unitSync.isError && <p className="error-text">{unitSync.error.message}</p>}
        {query.data?.overtime_note && <p className="muted">{query.data.overtime_note}</p>}

        <details className="rule-legend">
          <summary>{t('viol.legendTitle')}</summary>
          <div className="rule-legend-body">
            <p className="muted" style={{ margin: '4px 0 8px' }}>
              {t('viol.legendHint')}
            </p>
            {groups.map((g) => (
              <div key={g.key} style={{ display: 'grid', gap: 6 }}>
                <strong style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <span className="dot" style={{ background: tk.slot(g.slot) }} aria-hidden="true" />
                  {g.label}
                </strong>
                {ruleHelpEntries()
                  .filter(([, h]) => h.group === g.key)
                  .map(([key, h]) => (
                    <div className="rule-legend-row" key={key} style={{ paddingLeft: 16 }}>
                      <strong>{h.label}.</strong> {h.what}{' '}
                      <span className="muted">
                        {t('viol.whatToDo')}: {h.action}
                      </span>
                    </div>
                  ))}
              </div>
            ))}
          </div>
        </details>
      </section>

      {query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : query.isLoading ? (
        <Skeleton height={240} radius={10} />
      ) : all.length === 0 ? (
        <div className="card">
          <EmptyState title={t('viol.empty')} description={t('viol.emptyHint')} showSyncLink={false} />
        </div>
      ) : (
        <>
          <div className="grid grid-kpi">
            <KpiTile label={t('viol.kpiNegative')} value={warns.length} markerColor={tk.negative} />
            <KpiTile label={t('viol.kpiInfo')} value={infos} markerColor={tk.positive} />
            <KpiTile label={t('viol.kpiPeople')} value={peopleTouched} />
            <KpiTile
              label={t('viol.kpiUnits')}
              value={new Set(items.map((v) => v.cluster || v.department || '—')).size}
            />
          </div>

          {/* Кликабельный список людей: количество и шкала warn/info. */}
          {peopleStats.length > 0 && (
            <section className="card chart-card">
              <header className="chart-card-head">
                <div className="chart-card-titles">
                  <h2>{t('viol.peopleTitle')}</h2>
                  <div className="chart-card-subtitle">{t('viol.peopleHint')}</div>
                </div>
              </header>
              <div className="people-dev-list">
                {peopleStats.map((p) => {
                  const total = p.warn + p.info
                  const active = vperson === p.key
                  return (
                    <button
                      key={p.key}
                      type="button"
                      className={`people-dev-row${active ? ' active' : ''}`}
                      aria-pressed={active}
                      onClick={() => setVperson(active ? '' : p.key)}
                      title={`${p.warn} ⚠ / ${p.info} ✓`}
                    >
                      <span className="people-dev-name">{p.name}</span>
                      <span className="people-dev-team muted">{p.team || '—'}</span>
                      <span className="num people-dev-counts">
                        {p.warn > 0 && <span style={{ color: tk.negative }}>{p.warn} ⚠</span>}
                        {p.info > 0 && <span style={{ color: tk.positive }}>{p.info} ✓</span>}
                      </span>
                      <span
                        className="people-dev-bar"
                        style={{ width: `${Math.max(6, (total / maxPersonTotal) * 100)}%` }}
                        aria-hidden="true"
                      >
                        {p.warn > 0 && (
                          <span
                            style={{ flex: p.warn, background: tk.negative }}
                            title={`${p.warn} ⚠`}
                          />
                        )}
                        {p.info > 0 && (
                          <span
                            style={{ flex: p.info, background: tk.positive }}
                            title={`${p.info} ✓`}
                          />
                        )}
                      </span>
                    </button>
                  )
                })}
              </div>
            </section>
          )}

          <div className="grid grid-2">
            <TypeBreakdown
              title={t('viol.byRuleTitle')}
              subtitle={t('viol.byRuleHint')}
              items={byRule}
              color={tk.slot(0)}
              itemColor={(item) => tk.slot(ruleGroupOf(item.key).slot)}
              categoryLabel={t('viol.type')}
              valueLabel={t('viol.findings')}
              onSelect={(item) => toggleRule(item.key)}
              emptyTitle={t('common.noData')}
            />
            <TypeBreakdown
              title={t('viol.byTeamTitle')}
              subtitle={t('viol.byTeamHint')}
              items={warnByTeam}
              color={tk.negative}
              categoryLabel={t('viol.team')}
              valueLabel={t('viol.deviations')}
              onSelect={(item) => setFilters({ team: filters.team === item.key ? '' : item.key })}
              emptyTitle={t('viol.noNegative')}
            />
          </div>

          {items.length === 0 ? (
            <div className="card">
              <EmptyState title={t('viol.nothingMatched')} showSyncLink={false} />
            </div>
          ) : (
            <section className="card" style={{ padding: 14, overflowX: 'auto' }}>
              <table className="data">
                <caption className="visually-hidden">{t('viol.tableCaption')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('viol.person')}</th>
                    <th scope="col">{t('viol.team')}</th>
                    <th scope="col">{t('viol.unit')}</th>
                    <th scope="col">{t('viol.dates')}</th>
                    <th scope="col">{t('viol.detail')}</th>
                  </tr>
                </thead>
                <tbody>
                  {groupSections.map(({ group, rules, total }) => (
                    <Fragment key={group.key}>
                      {/* Заголовок крупной группы: цветная плашка на всю строку. */}
                      <tr className="group-section-row">
                        <th colSpan={5} scope="colgroup">
                          <span
                            className="group-section-head"
                            style={{
                              borderLeft: `6px solid ${tk.slot(group.slot)}`,
                              background: `color-mix(in srgb, ${tk.slot(group.slot)} 8%, transparent)`,
                            }}
                          >
                            <span className="dot" style={{ background: tk.slot(group.slot) }} aria-hidden="true" />
                            {group.label}
                            <span className="num muted">
                              {total} {tp('plural.findings', total)}
                            </span>
                          </span>
                        </th>
                      </tr>
                      {rules.map(([rule, list]) => {
                        const severity = list[0]?.severity ?? 'info'
                        return (
                          <Fragment key={rule}>
                            <tr className="rule-section-row">
                              <th colSpan={5} scope="colgroup">
                                <span
                                  className="rule-section-head"
                                  style={{ borderLeft: `4px solid ${tk.slot(group.slot)}` }}
                                >
                                  <span
                                    className={`badge badge-status ${severity === 'warn' ? 'badge-failed' : 'badge-done'}`}
                                    title={ruleHint(rule)}
                                  >
                                    <span
                                      className="dot"
                                      style={{ background: tk.slot(group.slot), marginRight: 4 }}
                                      aria-hidden="true"
                                    />
                                    {ruleLabel(rule)}
                                  </span>
                                  <span className="muted">
                                    {list.length} {tp('plural.findings', list.length)}
                                  </span>
                                </span>
                              </th>
                            </tr>
                            {list.map((v, i) => (
                          <tr key={`${v.person_key}-${i}`} className={`deviation-${v.severity}`}>
                            <th
                              scope="row"
                              style={{
                                fontWeight: 400,
                                boxShadow: `inset 3px 0 0 ${tk.slot(group.slot)}`,
                              }}
                            >
                              <button
                                type="button"
                                className="linklike"
                                onClick={() => setVperson(vperson === v.person_key ? '' : v.person_key)}
                                title={t('viol.focusPerson')}
                              >
                                {v.person}
                              </button>
                            </th>
                            <td>{v.team || '—'}</td>
                            <td className="muted">
                              {[v.department, v.cluster].filter(Boolean).join(' · ') || '—'}
                            </td>
                            <td className="num">
                              {(v.dates ?? []).slice(0, 5).join(', ')}
                              {(v.dates ?? []).length > 5 ? '…' : ''}
                            </td>
                            <td>
                              {v.detail}{' '}
                              {v.url && (
                                <a href={v.url} target="_blank" rel="noreferrer">
                                  {v.url.split('/').pop()}
                                </a>
                              )}
                            </td>
                          </tr>
                        ))}
                          </Fragment>
                        )
                      })}
                    </Fragment>
                  ))}
                </tbody>
              </table>
            </section>
          )}
        </>
      )}
    </div>
  )
}
