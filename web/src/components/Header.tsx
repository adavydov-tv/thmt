import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { PersonPicker } from './PersonPicker'
import { PeriodPicker } from './PeriodPicker'
import { SyncEta } from './SyncEta'
import { QueueBadge } from './SyncQueue'
import { useFilters } from '../lib/useFilters'
import { useEnabledSources, useHealth, useMeta, usePeople } from '../lib/queries'
import { useSync } from '../lib/syncContext'
import { useTheme } from '../lib/theme'
import { sourceLabel } from '../lib/chartTheme'
import { useLang, useT } from '../i18n'
import { useMe, can } from '../lib/authClient'
import { api } from '../lib/api'

interface NavChild {
  to: string
  label: string
  end?: boolean
}

interface NavGroup {
  key: string
  label: string
  to: string
  end?: boolean
  children?: NavChild[]
}

/** Страницы, привязанные к конкретному человеку: только на них в шапке
 * показывается выбор человека и кнопка обновления его данных. */
function isPersonScoped(pathname: string): boolean {
  return (
    pathname === '/' ||
    pathname.startsWith('/source/') ||
    pathname.startsWith('/docs') ||
    pathname.startsWith('/events')
  )
}

/** Пункт меню с выпадашкой: подпункты разворачиваются при наведении (клик по
 * заголовку ведёт на первый экран группы; шеврон-клик — для тач-устройств).
 * Закрытие с небольшой задержкой, чтобы курсор успел дойти до списка. */
function NavDropdown({ group, search }: { group: NavGroup; search: string }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const closeTimer = useRef<number | undefined>(undefined)
  const location = useLocation()

  const hoverOpen = () => {
    window.clearTimeout(closeTimer.current)
    setOpen(true)
  }
  const hoverClose = () => {
    window.clearTimeout(closeTimer.current)
    closeTimer.current = window.setTimeout(() => setOpen(false), 180)
  }

  useEffect(() => setOpen(false), [location.pathname, location.search])
  useEffect(() => () => window.clearTimeout(closeTimer.current), [])
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  // Параметры, которыми управляют подпункты этой группы (например tone=),
  // вычищаются из текущей строки поиска — иначе клик по «Позитивным» с
  // открытых «Негативных» давал бы ?tone=warn&tone=info, где читается первое.
  const ownParams = (group.children ?? [])
    .filter((c) => c.to.includes('?'))
    .flatMap((c) => [...new URLSearchParams(c.to.split('?')[1]).keys()])
  const cleanedSearch = (() => {
    if (ownParams.length === 0) return search
    const p = new URLSearchParams(search)
    for (const key of ownParams) p.delete(key)
    p.delete('vperson')
    const s = p.toString()
    return s ? `?${s}` : ''
  })()

  if (!group.children || group.children.length === 0) {
    return (
      <NavLink
        to={`${group.to}${search}`}
        end={group.end}
        className={({ isActive }) => `nav-link${isActive ? ' active' : ''}`}
      >
        {group.label}
      </NavLink>
    )
  }

  const groupActive =
    group.end || group.to === '/'
      ? location.pathname === group.to ||
        group.children.some((c) => location.pathname === c.to.split('?')[0])
      : location.pathname.startsWith(group.to) ||
        group.children.some((c) => location.pathname.startsWith(c.to.split('?')[0]))

  return (
    <div
      className={`nav-group${groupActive ? ' active' : ''}`}
      ref={ref}
      onMouseEnter={hoverOpen}
      onMouseLeave={hoverClose}
    >
      <NavLink
        to={`${group.to}${cleanedSearch}`}
        end={group.end}
        className={() => `nav-link${groupActive ? ' active' : ''}`}
      >
        {group.label}
      </NavLink>
      <button
        type="button"
        className="nav-caret"
        aria-expanded={open}
        aria-label={group.label}
        onClick={() => setOpen((v) => !v)}
      >
        ▾
      </button>
      {open && (
        <div className="nav-pop card" role="menu">
          {group.children.map((c) => {
            // Ссылки с собственным query (например, ?tone=) дописываются к
            // текущим фильтрам; активность считаем по совпадению фрагмента.
            if (c.to.includes('?')) {
              const [path, extra] = c.to.split('?')
              const href = `${path}${cleanedSearch}${cleanedSearch ? '&' : '?'}${extra}`
              const active =
                location.pathname === path && location.search.includes(extra)
              return (
                <NavLink
                  key={c.to}
                  to={href}
                  className={() => `nav-pop-link${active ? ' active' : ''}`}
                  role="menuitem"
                >
                  {c.label}
                </NavLink>
              )
            }
            const active =
              c.end || c.to === '/'
                ? location.pathname === c.to && !location.search.includes('tone=')
                : location.pathname.startsWith(c.to)
            return (
              <NavLink
                key={c.to}
                to={`${c.to}${cleanedSearch}`}
                end={c.end}
                className={() => `nav-pop-link${active ? ' active' : ''}`}
                role="menuitem"
              >
                {c.label}
              </NavLink>
            )
          })}
        </div>
      )}
    </div>
  )
}

export function Header({ onShowHelp }: { onShowHelp?: () => void }) {
  const { filters, setFilters, linkSearch } = useFilters()
  const { data: people, isLoading: peopleLoading } = usePeople()
  const { data: health } = useHealth()
  const { data: meta } = useMeta()
  const enabledSources = useEnabledSources()
  const { mode, toggle } = useTheme()
  const { lang, setLang } = useLang()
  const { t } = useT()
  const sync = useSync()
  const location = useLocation()
  const me = useMe()
  const role = me.data?.role ?? 'viewer'
  const authOn = me.data?.enabled ?? false

  // Первый человек становится выбранным по умолчанию, если в URL никого нет.
  useEffect(() => {
    if (!filters.person && people && people.length > 0) {
      setFilters({ person: people[0].key }, { replace: true })
    }
  }, [filters.person, people, setFilters])

  // Основное меню: вложенное, на первом уровне — крупные разделы.
  const nav = useMemo<NavGroup[]>(
    () => [
      {
        key: 'people',
        label: t('nav.people'),
        to: '/',
        end: true,
        children: [
          { to: '/', label: t('nav.overview'), end: true },
          ...enabledSources.map((key) => ({ to: `/source/${key}`, label: sourceLabel(key) })),
          { to: '/docs', label: t('nav.docs') },
          { to: '/events', label: t('nav.events') },
        ],
      },
      {
        key: 'compare',
        label: t('nav.compare'),
        to: '/compare',
        children: [
          { to: '/compare', label: t('nav.comparePeople') },
          { to: '/teams', label: t('nav.compareTeams') },
          { to: '/compare/format', label: t('nav.compareFormat') },
          { to: '/compare/system', label: t('nav.compareSystem') },
        ],
      },
      {
        key: 'deviations',
        label: t('nav.deviations'),
        to: '/violations',
        children: [
          { to: '/violations', label: t('nav.deviationsAll'), end: true },
          { to: `/violations?tone=warn`, label: t('nav.deviationsNegative') },
          { to: `/violations?tone=info`, label: t('nav.deviationsPositive') },
          { to: '/abusers', label: t('nav.abusers') },
        ],
      },
      ...(can.ai(role) ? [{ key: 'ai', label: 'AI', to: '/ai' } as NavGroup] : []),
      ...(can.settings(role)
        ? [
            {
              key: 'settings',
              label: t('nav.settings'),
              to: '/settings',
              children: [
                { to: '/settings', label: t('nav.settingsSync'), end: true },
                ...(can.generalSettings(role)
                  ? [
                      { to: '/settings/general', label: t('nav.settingsGeneral') },
                      { to: '/settings/activity', label: t('nav.settingsActivity') },
                    ]
                  : []),
                { to: '/settings/holidays', label: t('nav.settingsHolidays') },
                ...(can.purge(role) ? [{ to: '/settings/purge', label: t('nav.settingsPurge') }] : []),
                ...(role === 'administrator'
                  ? [{ to: '/settings/users', label: t('nav.settingsUsers') }]
                  : []),
              ],
            } as NavGroup,
          ]
        : []),
    ],
    [enabledSources, t, role],
  )

  // Индикатор состояния — только по включённым источникам.
  const sources = meta?.sources?.length
    ? meta.sources.filter((s) => s.enabled)
    : Object.keys(health?.sources ?? {}).map((key) => ({ key, label: sourceLabel(key), enabled: true }))

  const running = sync.activeRun?.status === 'running' || sync.activeRun?.status === 'pending'
  const personScoped = isPersonScoped(location.pathname)

  return (
    <header className="app-header">
      <div className="header-row">
        <div className="brand">
          <Link className="brand-title" to={`/${linkSearch}`}>
            <img className="brand-logo" src="/logo.svg" alt="" width={26} height={26} />
            <span>Team Health Management Tools</span>
          </Link>
        </div>
        <nav className="nav" aria-label={t('nav.aria')}>
          {nav.map((group) => (
            <NavDropdown key={group.key} group={group} search={linkSearch} />
          ))}
        </nav>
        <div className="header-side">
          {sources.length > 0 && (
            <div className="health" title={health ? `${t('header.db')}: ${health.db}` : t('header.stateUnknown')}>
              {sources.map((s) => {
                const on = health?.sources?.[s.key] ?? false
                return (
                  <span className="health-item" key={s.key}>
                    <span className={`health-dot ${on ? 'health-on' : 'health-off'}`} aria-hidden="true" />
                    {sourceLabel(s.key)}
                    <span className="visually-hidden">
                      {on ? t('header.available') : t('header.unavailable')}
                    </span>
                  </span>
                )
              })}
            </div>
          )}
          {onShowHelp && (
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              onClick={onShowHelp}
              title={t('header.helpHint')}
            >
              ?
            </button>
          )}
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={() => setLang(lang === 'ru' ? 'en' : 'ru')}
            title={t('header.langToggle')}
          >
            {lang === 'ru' ? 'EN' : 'RU'}
          </button>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={toggle}
            aria-pressed={mode === 'dark'}
            title={t('header.themeToggle')}
          >
            <span aria-hidden="true">{mode === 'dark' ? '☾' : '☀'}</span>
          </button>
          {authOn && me.data && (
            <span className="user-chip" title={`${me.data.email} · ${t(`role.${role}` as never)}`}>
              <span className="user-chip-name">{me.data.email.split('@')[0]}</span>
              <span className="badge">{t(`role.${role}` as never)}</span>
              <button
                type="button"
                className="btn btn-ghost btn-sm"
                title={t('header.logout')}
                onClick={() => {
                  void api.logout().then(() => window.location.assign('/'))
                }}
              >
                ⎋
              </button>
            </span>
          )}
        </div>
      </div>

      {/* Период закреплён всегда; человек и обновление данных — только на
          страницах, привязанных к человеку. */}
      <div className="header-row">
        {personScoped && (
          <PersonPicker
            people={people}
            value={filters.person}
            onChange={(person) => setFilters({ person })}
            isLoading={peopleLoading}
          />
        )}
        <PeriodPicker
          from={filters.from}
          to={filters.to}
          onChange={({ from, to }) => setFilters({ from, to })}
        />
        <span className="spacer" />
        <QueueBadge />
        {running && (
          <>
            <span className="badge badge-status badge-running">
              {t('header.sync')}:{' '}
              {sync.activeRun?.status === 'pending' ? t('header.syncQueued') : t('header.syncRunning')}
            </span>
            {sync.activeRun && <SyncEta run={sync.activeRun} compact />}
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              onClick={sync.cancel}
              disabled={sync.isCancelling}
              title={t('header.stopHint')}
            >
              {sync.isCancelling ? t('header.stopping') : t('header.stop')}
            </button>
          </>
        )}
        {personScoped && can.mutate(role) && (
          <button
            type="button"
            className="btn btn-primary btn-sm"
            disabled={!filters.person || sync.isStarting || running}
            onClick={() =>
              sync.start({ person_key: filters.person, from: filters.from, to: filters.to })
            }
          >
            {t('header.updateData')}
          </button>
        )}
      </div>
    </header>
  )
}
