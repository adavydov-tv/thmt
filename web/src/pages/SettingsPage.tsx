import { useMemo, useState, type ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SyncStartCard, SyncHistoryCard } from '../components/SyncPanel'
import { SyncQueueCard } from '../components/SyncQueue'
import { HolidaysEditor } from '../components/HolidaysEditor'
import { PurgePanel } from '../components/PurgePanel'
import { ErrorState } from '../components/ErrorState'
import { AddPersonCard } from '../components/AddPersonCard'
import { SkeletonLines } from '../components/Skeleton'
import { api } from '../lib/api'
import { keys, useHrdbTeams, useMeta, usePeople } from '../lib/queries'
import { useSync } from '../lib/syncContext'
import { useChartTokens, sourceLabel } from '../lib/chartTheme'
import { ruleGroups, ruleHelpEntries } from '../lib/ruleHelp'
import { useFilters } from '../lib/useFilters'
import { fmtDateTime } from '../lib/format'
import { useT } from '../i18n'
import { useMe, can } from '../lib/authClient'
import type { AppUser, Person } from '../lib/types'

/** Общий каркас настроек: заголовок + подразделы (по правам роли). */
function SettingsLayout({ subtitle, children }: { subtitle: string; children: ReactNode }) {
  const { t } = useT()
  const me = useMe()
  const role = me.data?.role ?? 'viewer'
  const tabs = [
    { to: '/settings', label: t('nav.settingsSync'), end: true },
    ...(can.generalSettings(role)
      ? [{ to: '/settings/general', label: t('nav.settingsGeneral') }]
      : []),
    ...(can.generalSettings(role)
      ? [{ to: '/settings/activity', label: t('nav.settingsActivity') }]
      : []),
    { to: '/settings/holidays', label: t('nav.settingsHolidays') },
    ...(can.purge(role) ? [{ to: '/settings/purge', label: t('nav.settingsPurge') }] : []),
    ...(role === 'administrator' ? [{ to: '/settings/users', label: t('nav.settingsUsers') }] : []),
  ]
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('nav.settings')}</h1>
          <p>{subtitle}</p>
        </div>
      </div>
      <nav className="subnav" aria-label={t('settings.sectionsAria')}>
        {tabs.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            end={tab.end}
            className={({ isActive }) => `subnav-link${isActive ? ' active' : ''}`}
          >
            {tab.label}
          </NavLink>
        ))}
      </nav>
      {children}
    </div>
  )
}

type SyncTab = 'people' | 'units' | 'history'

/** «Сбор данных»: вкладки По людям / По подразделениям / История сбора. */
export function SettingsSyncPage() {
  const { t } = useT()
  const [tab, setTab] = useState<SyncTab>('people')
  const tabs: { key: SyncTab; label: string }[] = [
    { key: 'people', label: t('settings.tabPeople') },
    { key: 'units', label: t('settings.tabUnits') },
    { key: 'history', label: t('settings.tabHistory') },
  ]
  return (
    <SettingsLayout subtitle={t('settings.syncSubtitle')}>
      <div className="segmented" role="tablist" aria-label={t('settings.syncSubtitle')}>
        {tabs.map((x) => (
          <button
            key={x.key}
            type="button"
            role="tab"
            aria-selected={tab === x.key}
            aria-pressed={tab === x.key}
            onClick={() => setTab(x.key)}
          >
            {x.label}
          </button>
        ))}
      </div>
      {tab === 'people' && <PeopleTab />}
      {tab === 'units' && (
        <div className="stack">
          <SyncQueueCard />
          <UnitsTab />
        </div>
      )}
      {tab === 'history' && (
        <div className="stack">
          <SyncQueueCard />
          <SyncHistoryCard />
        </div>
      )}
    </SettingsLayout>
  )
}

/** По людям: поиск по заведённым, обновление данных, управление карточками. */
function PeopleTab() {
  const { filters, setFilters } = useFilters()
  const { t } = useT()
  const people = usePeople()
  const { data: meta } = useMeta()
  const qc = useQueryClient()
  const sync = useSync()

  const [editing, setEditing] = useState<Person | null>(null)
  const [search, setSearch] = useState('')

  const invalidateAll = () => {
    void qc.invalidateQueries({ queryKey: keys.people })
    void qc.invalidateQueries({ queryKey: ['stats'] })
    void qc.invalidateQueries({ queryKey: ['events'] })
  }
  const deletePerson = useMutation({
    mutationFn: api.deletePerson,
    onSuccess: (res) => {
      if (filters.person === res.deleted) setFilters({ person: '' })
      invalidateAll()
    },
  })
  const cleanupEx = useMutation({ mutationFn: api.cleanupExPeople, onSuccess: invalidateAll })
  const relink = useMutation({ mutationFn: api.slackRelink, onSuccess: invalidateAll })

  const confirmDelete = (p: Person) => {
    // Удаление необратимо стирает все собранные данные человека.
    if (window.confirm(t('settings.deleteConfirm', { name: p.display_name || p.key }))) {
      deletePerson.mutate(p.key)
    }
  }

  const running = sync.activeRun?.status === 'running' || sync.activeRun?.status === 'pending'
  const refresh = (p: Person) => {
    setFilters({ person: p.key })
    sync.start({ person_key: p.key, from: filters.from, to: filters.to })
  }

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    const list = people.data ?? []
    if (!q) return list
    return list.filter((p) =>
      [p.display_name, p.key, p.email, p.team, p.gitlab_username]
        .filter(Boolean)
        .some((s) => String(s).toLowerCase().includes(q)),
    )
  }, [people.data, search])

  return (
    <div className="stack">
      <div className="detail-grid">
        <section className="card chart-card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{t('settings.peopleTitle')}</h2>
              <div className="chart-card-subtitle">{t('settings.peopleHint')}</div>
            </div>
            <div className="chart-card-actions">
              <input
                className="input"
                type="search"
                placeholder={t('settings.peopleSearch')}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                style={{ width: 220 }}
              />
              <button
                type="button"
                className="btn btn-sm"
                disabled={cleanupEx.isPending}
                title={t('settings.cleanupHint')}
                onClick={() => {
                  if (window.confirm(t('settings.cleanupConfirm'))) {
                    cleanupEx.mutate()
                  }
                }}
              >
                {cleanupEx.isPending ? t('settings.deleting') : t('settings.cleanupEx')}
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={relink.isPending}
                title={t('slackArch.relinkHint')}
                onClick={() => relink.mutate()}
              >
                {relink.isPending ? t('slackArch.relinking') : t('slackArch.relink')}
              </button>
            </div>
          </header>
          {relink.data && (
            <div className="panel-body" style={{ paddingBottom: 0 }}>
              <p className="muted" style={{ margin: 0 }}>
                {t('slackArch.relinkDone', { people: relink.data.people, events: relink.data.events })}
              </p>
            </div>
          )}
          {relink.isError && (
            <div className="panel-body" style={{ paddingBottom: 0 }}>
              <p className="error-text">{relink.error.message}</p>
            </div>
          )}
          {cleanupEx.data && (
            <div className="panel-body" style={{ paddingBottom: 0 }}>
              <p className="muted" style={{ margin: 0 }}>
                {cleanupEx.data.deleted.length > 0
                  ? `${t('settings.deleted')}: ${cleanupEx.data.deleted.join(', ')}`
                  : cleanupEx.data.note ?? t('settings.noExFound')}
              </p>
            </div>
          )}
          {people.isError ? (
            <div className="panel-body">
              <ErrorState error={people.error} onRetry={() => void people.refetch()} />
            </div>
          ) : people.isLoading ? (
            <div className="panel-body">
              <SkeletonLines count={4} height={22} />
            </div>
          ) : !people.data || people.data.length === 0 ? (
            <div className="panel-body">
              <p className="muted">{t('settings.noPeople')}</p>
            </div>
          ) : (
            <div className="table-wrap" style={{ maxHeight: 'none' }}>
              <table className="data">
                <caption className="visually-hidden">{t('settings.peopleTitle')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('settings.name')}</th>
                    <th scope="col">{t('settings.ids')}</th>
                    <th scope="col">{t('settings.updated')}</th>
                    <th scope="col" />
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((p) => (
                    <tr key={p.key} className={p.key === filters.person ? 'selected' : undefined}>
                      <th scope="row">
                        {p.display_name || p.key}
                        <div className="muted mono" style={{ fontWeight: 400, fontSize: 12 }}>
                          {p.key}
                          {p.team ? ` · ${p.team}` : ''}
                        </div>
                      </th>
                      <td>
                        <div className="event-meta">
                          {p.email && <span className="badge">{p.email}</span>}
                          {p.jira_account_id && <span className="badge">Jira</span>}
                          {p.gitlab_username && <span className="badge">GitLab: {p.gitlab_username}</span>}
                          {p.slack_user_id && <span className="badge">Slack</span>}
                          {p.google_email && <span className="badge">Google</span>}
                        </div>
                      </td>
                      <td className="num">{p.updated_at ? fmtDateTime(p.updated_at) : '—'}</td>
                      <td>
                        <div className="inline-group">
                          <button
                            type="button"
                            className="btn btn-primary btn-sm"
                            disabled={running || sync.isStarting}
                            title={t('settings.refreshHint')}
                            onClick={() => refresh(p)}
                          >
                            {t('header.updateData')}
                          </button>
                          <button type="button" className="btn btn-sm" onClick={() => setEditing(p)}>
                            {t('settings.edit')}
                          </button>
                          <button
                            type="button"
                            className="btn btn-sm"
                            disabled={p.key === filters.person}
                            onClick={() => setFilters({ person: p.key })}
                          >
                            {t('settings.select')}
                          </button>
                          <button
                            type="button"
                            className="btn btn-ghost btn-sm"
                            disabled={deletePerson.isPending}
                            onClick={() => confirmDelete(p)}
                            title={t('settings.deleteHint')}
                          >
                            {t('settings.delete')}
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {filtered.length === 0 && (
                <p className="muted" style={{ padding: 12 }}>
                  {t('settings.searchEmpty')}
                </p>
              )}
            </div>
          )}
        </section>

        <AddPersonCard
          hrdbEnabled={Boolean(meta?.hrdb_enabled)}
          editing={editing}
          onCancelEdit={() => setEditing(null)}
          onSaved={(saved) => {
            setEditing(null)
            if (!filters.person) setFilters({ person: saved.key })
          }}
        />
      </div>

      <SyncStartCard sourceOptions={meta?.sources} />
    </div>
  )
}

type UnitScope = 'team' | 'cluster' | 'department'

/** По подразделениям: сбор данных сразу по команде, кластеру или департаменту. */
function UnitsTab() {
  const { filters } = useFilters()
  const { t, tp } = useT()
  const teamsDir = useHrdbTeams()
  const [scope, setScope] = useState<UnitScope>('team')
  const [team, setTeam] = useState('')
  const [cluster, setCluster] = useState('')
  const [department, setDepartment] = useState('')

  const unitSync = useMutation({
    mutationFn: () => {
      if (scope === 'team') {
        return api.startTeamSync({ team, from: filters.from, to: filters.to })
      }
      return api.startUnitSync({
        cluster: scope === 'cluster' ? cluster : undefined,
        department: scope === 'department' ? department : undefined,
        from: filters.from,
        to: filters.to,
      })
    },
  })

  const unitName = scope === 'team' ? team : scope === 'cluster' ? cluster : department
  const scopes: { key: UnitScope; label: string }[] = [
    { key: 'team', label: t('viol.team') },
    { key: 'cluster', label: t('viol.cluster') },
    { key: 'department', label: t('viol.department') },
  ]

  return (
    <section className="card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('settings.unitsTitle')}</h2>
          <div className="chart-card-subtitle">{t('settings.unitsHint')}</div>
        </div>
      </header>
      <div className="panel-body stack">
        <div className="segmented" role="group" aria-label={t('settings.unitScopeAria')}>
          {scopes.map((s) => (
            <button
              key={s.key}
              type="button"
              aria-pressed={scope === s.key}
              onClick={() => setScope(s.key)}
            >
              {s.label}
            </button>
          ))}
        </div>

        <div className="form-grid">
          {scope === 'team' && (
            <div className="field">
              <label htmlFor="unit-team">{t('viol.team')}</label>
              <select
                id="unit-team"
                className="select"
                value={team}
                onChange={(e) => setTeam(e.target.value)}
              >
                <option value="">—</option>
                {teamsDir.data?.teams.map((tm) => (
                  <option key={tm.name} value={tm.name} title={(tm.chain ?? []).join(' → ')}>
                    {[...(tm.chain ?? [tm.name])].reverse().join(' / ')}
                  </option>
                ))}
              </select>
            </div>
          )}
          {scope === 'cluster' && (
            <div className="field">
              <label htmlFor="unit-cluster">{t('viol.cluster')}</label>
              <select
                id="unit-cluster"
                className="select"
                value={cluster}
                onChange={(e) => setCluster(e.target.value)}
              >
                <option value="">—</option>
                {teamsDir.data?.clusters.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </div>
          )}
          {scope === 'department' && (
            <div className="field">
              <label htmlFor="unit-dept">{t('viol.department')}</label>
              <select
                id="unit-dept"
                className="select"
                value={department}
                onChange={(e) => setDepartment(e.target.value)}
              >
                <option value="">—</option>
                {teamsDir.data?.departments.map((d) => (
                  <option key={d} value={d}>
                    {d}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>

        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {t('settings.unitsPeriodNote')}
        </p>

        <div className="inline-group">
          <button
            type="button"
            className="btn btn-primary"
            disabled={!unitName || unitSync.isPending}
            onClick={() => {
              if (window.confirm(t('viol.unitSyncConfirm', { unit: unitName }))) {
                unitSync.mutate()
              }
            }}
          >
            {unitSync.isPending ? t('viol.starting') : t('header.updateData')}
          </button>
        </div>

        {unitSync.data && (
          <p className="muted">
            «{unitSync.data.team}»: {unitSync.data.members} {tp('plural.employees', unitSync.data.members)},{' '}
            {unitSync.data.created > 0 && (
              <>
                {t('viol.createdNew')}: {unitSync.data.created},{' '}
              </>
            )}
            {t('viol.started')} {unitSync.data.started} {tp('plural.syncs', unitSync.data.started)}.
          </p>
        )}
        {unitSync.isError && <p className="error-text">{unitSync.error.message}</p>}
      </div>
    </section>
  )
}

/** Google Workspace audit: офисные подсети для определения «логин из офиса». */
function GWorkCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['settings', 'office-cidrs'], queryFn: api.officeCidrs })
  const [text, setText] = useState<string | null>(null)
  const save = useMutation({
    mutationFn: (cidrs: string[]) => api.setOfficeCidrs(cidrs),
    onSuccess: (res) => {
      qc.setQueryData(['settings', 'office-cidrs'], res)
      setText(null)
    },
  })
  const value = text ?? (q.data?.cidrs ?? []).join('\n')

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('gwork.title')}</h2>
          <div className="chart-card-subtitle">{t('gwork.hint')}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 10, padding: '4px 2px' }}>
        <label className="field">
          <span className="field-label">{t('gwork.officeCidrs')}</span>
          <textarea
            className="input"
            rows={5}
            placeholder={'10.15.0.0/16\n192.168.1.0/24\n203.0.113.7'}
            value={value}
            onChange={(e) => setText(e.target.value)}
            style={{ fontFamily: 'monospace', resize: 'vertical' }}
          />
        </label>
        <div className="inline-group">
          <button
            type="button"
            className="btn btn-primary btn-sm"
            disabled={save.isPending || text === null}
            onClick={() =>
              save.mutate(
                value
                  .split(/[\n,]+/)
                  .map((x) => x.trim())
                  .filter(Boolean),
              )
            }
          >
            {save.isPending ? t('gwork.saving') : t('gwork.save')}
          </button>
        </div>
        {save.isError && <p className="error-text">{save.error.message}</p>}
        <p className="muted" style={{ fontSize: 12, margin: 0 }}>
          {t('gwork.note')}
        </p>
      </div>
    </section>
  )
}

/** Ежедневное обновление: включатель и время запуска (локальное время сервера). */
function SchedulerCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['settings', 'schedule'], queryFn: api.schedule })
  const [time, setTime] = useState('')
  const save = useMutation({
    mutationFn: (p: { enabled: boolean; time: string; catchup: boolean }) =>
      api.setSchedule(p.enabled, p.time, p.catchup),
    onSuccess: (res) => qc.setQueryData(['settings', 'schedule'], res),
  })
  const d = q.data
  const timeValue = time || d?.time || '06:00'
  const catchup = d?.catchup ?? false

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('sched.title')}</h2>
          <div className="chart-card-subtitle">{t('sched.hint', { n: d?.workers ?? 3 })}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 10, padding: '4px 2px' }}>
        <div className="inline-group">
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={d?.enabled ?? false}
              disabled={save.isPending || !d}
              onChange={(e) => save.mutate({ enabled: e.target.checked, time: timeValue, catchup })}
            />
            <span>{t('sched.enabled')}</span>
          </label>
          <input
            className="input"
            type="time"
            value={timeValue}
            onChange={(e) => setTime(e.target.value)}
            onBlur={() => {
              if (d && time && time !== d.time) save.mutate({ enabled: d.enabled, time, catchup })
            }}
            style={{ width: 110 }}
          />
          {d && (
            <span className="muted num">
              {t('sched.serverTime')}: {d.server_time}
            </span>
          )}
        </div>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={catchup}
            disabled={save.isPending || !d}
            onChange={(e) => save.mutate({ enabled: d?.enabled ?? false, time: timeValue, catchup: e.target.checked })}
          />
          <span>{t('sched.catchup')}</span>
        </label>
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {t('sched.catchupHint')}
        </p>
        {d?.last_at && (
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>
            {t('sched.lastRun')}: {fmtDateTime(d.last_at)} · {t('sched.started')}{' '}
            {d.last_started ?? '?'}
          </p>
        )}
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {t('sched.note')}
        </p>
        {save.isError && <p className="error-text">{save.error.message}</p>}
      </div>
    </section>
  )
}

/** Дни перекрытия инкрементального сбора на каждую систему. */
function ReprobeCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['settings', 'reprobe'], queryFn: api.reprobe })
  const [draft, setDraft] = useState<Record<string, string>>({})
  const save = useMutation({
    mutationFn: (days: Record<string, number>) => api.setReprobe(days),
    onSuccess: (res) => {
      qc.setQueryData(['settings', 'reprobe'], res)
      setDraft({})
    },
  })
  const d = q.data
  const shown = (src: string) => draft[src] ?? (d?.days[src] != null ? String(d.days[src]) : '')
  const onSave = () => {
    const out: Record<string, number> = {}
    for (const src of d?.sources ?? []) {
      const raw = shown(src).trim()
      if (raw === '') continue
      const n = Math.round(Number(raw))
      if (Number.isFinite(n) && n >= 0 && n <= 365) out[src] = n
    }
    save.mutate(out)
  }

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('reprobe.title')}</h2>
          <div className="chart-card-subtitle">{t('reprobe.hint', { n: d?.default ?? 2 })}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 10, padding: '4px 2px' }}>
        <div className="form-grid">
          {(d?.sources ?? []).map((src) => (
            <div className="field" key={src}>
              <label htmlFor={`reprobe-${src}`}>{sourceLabel(src)}</label>
              <input
                id={`reprobe-${src}`}
                className="input"
                type="number"
                min={0}
                max={365}
                placeholder={String(d?.default ?? 2)}
                value={shown(src)}
                disabled={save.isPending || !d}
                onChange={(e) => setDraft((s) => ({ ...s, [src]: e.target.value }))}
                style={{ width: 90 }}
              />
            </div>
          ))}
        </div>
        <div className="inline-group">
          <button type="button" className="btn btn-primary" onClick={onSave} disabled={save.isPending || !d}>
            {t('reprobe.save')}
          </button>
          <span className="muted" style={{ fontSize: 12 }}>
            {t('reprobe.note')}
          </span>
        </div>
        {save.isError && <p className="error-text">{save.error.message}</p>}
      </div>
    </section>
  )
}

const ROLES = ['viewer', 'lead', 'supervisor', 'administrator'] as const

/** Пользователи и роли: кто и с какими правами входит в дашборд. */
function UsersCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const usersQ = useQuery({ queryKey: ['users'], queryFn: api.users })
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<string>('viewer')

  const sync = (res: { users: AppUser[] }) => qc.setQueryData(['users'], res)
  const upsert = useMutation({
    mutationFn: () => api.upsertUser(email.trim(), role),
    onSuccess: (res) => {
      sync(res)
      setEmail('')
    },
  })
  const setUserRole = useMutation({
    mutationFn: (p: { email: string; role: string }) => api.upsertUser(p.email, p.role),
    onSuccess: sync,
  })
  const remove = useMutation({ mutationFn: api.deleteUser, onSuccess: sync })

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('users.title')}</h2>
          <div className="chart-card-subtitle">{t('users.hint')}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 10, padding: '4px 2px' }}>
        <div className="inline-group">
          <input
            className="input"
            type="email"
            placeholder="email@tradingview.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={{ width: 260 }}
          />
          <select className="select" value={role} onChange={(e) => setRole(e.target.value)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {t(`role.${r}` as never)}
              </option>
            ))}
          </select>
          <button
            type="button"
            className="btn btn-primary btn-sm"
            disabled={!email.includes('@') || upsert.isPending}
            onClick={() => upsert.mutate()}
          >
            {t('users.add')}
          </button>
        </div>
        {(upsert.isError || setUserRole.isError || remove.isError) && (
          <p className="error-text">
            {(upsert.error ?? setUserRole.error ?? remove.error)?.message}
          </p>
        )}
        {usersQ.data && usersQ.data.users.length > 0 && (
          <table className="data">
            <thead>
              <tr>
                <th scope="col">Email</th>
                <th scope="col">{t('users.role')}</th>
                <th scope="col">{t('users.addedBy')}</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {usersQ.data.users.map((u) => (
                <tr key={u.email}>
                  <th scope="row" style={{ fontWeight: 400 }}>
                    {u.email}
                  </th>
                  <td>
                    <select
                      className="select"
                      value={u.role}
                      disabled={setUserRole.isPending}
                      onChange={(e) => setUserRole.mutate({ email: u.email, role: e.target.value })}
                    >
                      {ROLES.map((r) => (
                        <option key={r} value={r}>
                          {t(`role.${r}` as never)}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td className="muted">{u.added_by || '—'}</td>
                  <td>
                    <button
                      type="button"
                      className="btn btn-ghost btn-sm"
                      disabled={remove.isPending}
                      title={t('users.removeHint')}
                      onClick={() => {
                        if (window.confirm(t('users.removeConfirm', { email: u.email }))) {
                          remove.mutate(u.email)
                        }
                      }}
                    >
                      {t('settings.delete')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="muted" style={{ fontSize: 12, margin: 0 }}>
          {t('users.rolesLegend')}
        </p>
      </div>
    </section>
  )
}

/** Настройка правил отклонений: какие правила считать, какие выключить. */
function RulesCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const tk = useChartTokens()
  const disabledQ = useQuery({ queryKey: ['settings', 'rules'], queryFn: api.disabledRules })
  const save = useMutation({
    mutationFn: api.setDisabledRules,
    onSuccess: (res) => {
      qc.setQueryData(['settings', 'rules'], res)
      void qc.invalidateQueries({ queryKey: ['violations'] })
    },
  })
  const disabled = new Set(disabledQ.data?.disabled ?? [])
  const entries = ruleHelpEntries()

  const toggle = (rule: string) => {
    const next = new Set(disabled)
    if (next.has(rule)) next.delete(rule)
    else next.add(rule)
    save.mutate([...next])
  }
  const setGroup = (groupKey: string, enabled: boolean) => {
    const next = new Set(disabled)
    for (const [rule, h] of entries) {
      if (h.group !== groupKey) continue
      if (enabled) next.delete(rule)
      else next.add(rule)
    }
    save.mutate([...next])
  }

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('rules.title')}</h2>
          <div className="chart-card-subtitle">{t('rules.hint')}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 14, padding: '4px 2px' }}>
        {ruleGroups().map((g) => {
          const groupRules = entries.filter(([, h]) => h.group === g.key)
          const offCount = groupRules.filter(([rule]) => disabled.has(rule)).length
          return (
            <div key={g.key} className="stack" style={{ gap: 6 }}>
              <div className="inline-group">
                <strong style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <span className="dot" style={{ background: tk.slot(g.slot) }} aria-hidden="true" />
                  {g.label}
                </strong>
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  disabled={offCount === 0 || save.isPending}
                  onClick={() => setGroup(g.key, true)}
                >
                  {t('types.all')}
                </button>
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  disabled={offCount === groupRules.length || save.isPending}
                  onClick={() => setGroup(g.key, false)}
                >
                  {t('types.none')}
                </button>
              </div>
              <div className="stack" style={{ gap: 4, paddingLeft: 14 }}>
                {groupRules.map(([rule, h]) => (
                  <label
                    key={rule}
                    style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}
                    title={h.what}
                  >
                    <input
                      type="checkbox"
                      checked={!disabled.has(rule)}
                      disabled={save.isPending}
                      onChange={() => toggle(rule)}
                    />
                    <span style={disabled.has(rule) ? { color: 'var(--text-muted)' } : undefined}>
                      {h.label}
                    </span>
                  </label>
                ))}
              </div>
            </div>
          )
        })}
        {save.isError && <ErrorState error={save.error} />}
      </div>
    </section>
  )
}

/** Настройка «что считать низкой активностью» по Area of Responsibility. */
// Порядок систем на экране (совпадает с models.AllSources / цветовыми слотами).
const SYSTEM_ORDER = [
  'jira', 'gitlab', 'slack', 'gdocs', 'gcal', 'allure', 'confluence',
  'gwork', 'argocd', 'zabbix', 'jenkins', 'grafana', 'figma', 'netsuite', 'claude',
]

// groupTypesBySystem группирует типы событий по системе (префикс до точки),
// сохраняя порядок SYSTEM_ORDER; неизвестные системы идут в конце по алфавиту.
function groupTypesBySystem(types: string[]): [string, string[]][] {
  const bySystem = new Map<string, string[]>()
  for (const ty of types) {
    const sys = ty.includes('.') ? ty.slice(0, ty.indexOf('.')) : ty
    const list = bySystem.get(sys) ?? []
    list.push(ty)
    bySystem.set(sys, list)
  }
  const known = SYSTEM_ORDER.filter((s) => bySystem.has(s))
  const rest = [...bySystem.keys()].filter((s) => !SYSTEM_ORDER.includes(s)).sort()
  return [...known, ...rest].map((s) => [s, bySystem.get(s)!.sort()])
}

function ShallowActivityCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const { data: meta } = useMeta()
  const cfgQ = useQuery({ queryKey: ['settings', 'shallow'], queryFn: api.shallowConfig })
  const save = useMutation({
    mutationFn: api.setShallowConfig,
    onSuccess: (res) => {
      qc.setQueryData(['settings', 'shallow'], { ...cfgQ.data, ...res })
      void qc.invalidateQueries({ queryKey: ['stats', 'days'] })
    },
  })
  // Выбранная для редактирования Area: '' — набор по умолчанию.
  const [area, setArea] = useState('')

  const typeLabels = meta?.type_labels ?? {}
  const labelOf = (ty: string) => typeLabels[ty] ?? ty

  if (!cfgQ.data) {
    return (
      <section className="card chart-card">
        <SkeletonLines count={4} />
      </section>
    )
  }
  const cfg = cfgQ.data
  // Действующий набор для выбранной области: своё переопределение или default.
  const effective = area && cfg.areas[area] ? cfg.areas[area] : cfg.default
  const selected = new Set(effective)
  const overridden = Boolean(area && cfg.areas[area])

  const persist = (nextDefault: string[], nextAreas: Record<string, string[]>) =>
    save.mutate({ default: nextDefault, areas: nextAreas })

  const persistSet = (next: Set<string>) => {
    const list = [...next]
    if (area === '') {
      persist(list, cfg.areas)
    } else {
      persist(cfg.default, { ...cfg.areas, [area]: list })
    }
  }
  const toggle = (ty: string) => {
    const next = new Set(selected)
    if (next.has(ty)) next.delete(ty)
    else next.add(ty)
    persistSet(next)
  }
  // Включить/выключить все типы одной системы разом.
  const toggleSystem = (types: string[], on: boolean) => {
    const next = new Set(selected)
    for (const ty of types) {
      if (on) next.add(ty)
      else next.delete(ty)
    }
    persistSet(next)
  }
  const resetArea = () => {
    if (!area) return
    const nextAreas = { ...cfg.areas }
    delete nextAreas[area]
    persist(cfg.default, nextAreas)
  }

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('shallowCfg.title')}</h2>
          <div className="chart-card-subtitle">{t('shallowCfg.hint')}</div>
        </div>
      </header>
      <div className="stack" style={{ gap: 12, padding: '4px 2px' }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span className="muted">{t('shallowCfg.area')}</span>
          <select value={area} onChange={(e) => setArea(e.target.value)}>
            <option value="">{t('shallowCfg.defaultOption')}</option>
            {cfg.known_areas.map((a) => (
              <option key={a} value={a}>
                {a}
                {cfg.areas[a] ? ' •' : ''}
              </option>
            ))}
          </select>
          {overridden && (
            <button type="button" className="btn-ghost" onClick={resetArea} disabled={save.isPending}>
              {t('shallowCfg.reset')}
            </button>
          )}
        </label>
        <p className="muted" style={{ fontSize: 12, margin: 0 }}>
          {area === ''
            ? t('shallowCfg.editingDefault')
            : overridden
              ? t('shallowCfg.editingOverride', { area })
              : t('shallowCfg.editingInherited', { area })}
        </p>
        <div className="stack" style={{ gap: 14 }}>
          {groupTypesBySystem(cfg.all_types).map(([system, types]) => {
            const onCount = types.filter((ty) => selected.has(ty)).length
            const allOn = onCount === types.length
            return (
              <div key={system}>
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'baseline',
                    gap: 8,
                    borderBottom: '1px solid var(--border)',
                    paddingBottom: 4,
                    marginBottom: 6,
                  }}
                >
                  <strong style={{ fontSize: 13 }}>{sourceLabel(system)}</strong>
                  <span className="muted" style={{ fontSize: 11 }}>
                    {onCount}/{types.length}
                  </span>
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ marginLeft: 'auto', fontSize: 12 }}
                    disabled={save.isPending}
                    onClick={() => toggleSystem(types, !allOn)}
                  >
                    {allOn ? t('shallowCfg.systemNone') : t('shallowCfg.systemAll')}
                  </button>
                </div>
                <div
                  className="grid"
                  style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 4 }}
                >
                  {types.map((ty) => (
                    <label key={ty} style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                      <input
                        type="checkbox"
                        checked={selected.has(ty)}
                        disabled={save.isPending}
                        onChange={() => toggle(ty)}
                      />
                      <span style={selected.has(ty) ? { color: 'var(--warning)' } : undefined}>
                        {labelOf(ty)}
                      </span>
                    </label>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
        {save.isError && <ErrorState error={save.error} />}
      </div>
    </section>
  )
}

/** Отдельная вкладка: настройка низкой активности по направлениям. */
export function SettingsActivityPage() {
  const { t } = useT()
  return (
    <SettingsLayout subtitle={t('settings.activitySubtitle')}>
      <ShallowActivityCard />
    </SettingsLayout>
  )
}

/** Общие настройки: источник HRDB и прочее. */
export function SettingsGeneralPage() {
  const { t } = useT()
  const qc = useQueryClient()

  // Источник HRDB: новая (Atlassian Cloud) или старая (jira.xtools.tv).
  const hrdbMode = useQuery({ queryKey: ['settings', 'hrdb'], queryFn: api.hrdbMode })
  const setHrdbMode = useMutation({
    mutationFn: api.setHrdbMode,
    onSuccess: (res) => {
      qc.setQueryData(['settings', 'hrdb'], res)
      void qc.invalidateQueries({ queryKey: ['hrdb'] })
      void qc.invalidateQueries({ queryKey: ['violations'] })
    },
  })

  return (
    <SettingsLayout subtitle={t('settings.generalSubtitle')}>
      {hrdbMode.data && hrdbMode.data.available.length > 0 ? (
        <section className="card chart-card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{t('settings.hrdbTitle')}</h2>
              <div className="chart-card-subtitle">{t('settings.hrdbHint')}</div>
            </div>
          </header>
          <div className="stack" style={{ gap: 8, padding: '4px 2px' }}>
            {hrdbMode.data.available.map((m) => (
              <label
                key={m}
                style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}
              >
                <input
                  type="radio"
                  name="hrdb-mode"
                  checked={hrdbMode.data!.mode === m}
                  disabled={setHrdbMode.isPending}
                  onChange={() => setHrdbMode.mutate(m)}
                />
                <span>{hrdbMode.data!.labels[m] ?? m}</span>
                {hrdbMode.data!.mode === m && <span className="muted">— {t('settings.hrdbActive')}</span>}
              </label>
            ))}
            <p className="muted" style={{ fontSize: 12, margin: 0 }}>
              {t('settings.hrdbNote')}
            </p>
            {setHrdbMode.isError && <ErrorState error={setHrdbMode.error} />}
          </div>
        </section>
      ) : (
        <section className="card">
          <p className="muted" style={{ padding: 12 }}>
            {t('settings.hrdbUnavailable')}
          </p>
        </section>
      )}

      <GWorkCard />

      <SchedulerCard />

      <ReprobeCard />

      <RulesCard />
    </SettingsLayout>
  )
}

/** Календари праздников по странам и годам. */
export function SettingsHolidaysPage() {
  const { t } = useT()
  return (
    <SettingsLayout subtitle={t('settings.holidaysSubtitle')}>
      <HolidaysEditor />
    </SettingsLayout>
  )
}

/** Удаление собранных данных. */
export function SettingsPurgePage() {
  const { t } = useT()
  const { data: meta } = useMeta()
  return (
    <SettingsLayout subtitle={t('settings.purgeSubtitle')}>
      <PurgePanel sourceOptions={meta?.sources} />
    </SettingsLayout>
  )
}

/** Пользователи и роли — отдельная вкладка, только для администратора. */
export function SettingsUsersPage() {
  const { t } = useT()
  return (
    <SettingsLayout subtitle={t('settings.usersSubtitle')}>
      <UsersCard />
    </SettingsLayout>
  )
}
