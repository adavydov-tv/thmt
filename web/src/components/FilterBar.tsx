import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { SourceChips } from './SourceChips'
import { useFilters } from '../lib/useFilters'
import { useEnabledSources, useMeta } from '../lib/queries'
import { sourceLabel } from '../lib/chartTheme'
import { useT } from '../i18n'
import type { CountItem, MetaSource } from '../lib/types'

interface FilterBarProps {
  sourceOptions?: MetaSource[]
  typeOptions?: CountItem[]
  projectOptions?: CountItem[]
  typeLabel?: (type: string) => string
  /** Скрыть чипы источников (например, на странице конкретного источника). */
  hideSources?: boolean
  /** Показывать проекты чипами с мультивыбором вместо выпадающего списка. */
  projectsAsChips?: boolean
  /** Подпись блока проектов (например, «Группы встреч» на странице календаря). */
  projectLabel?: string
  /** Отдельный блок чипов групп встреч (календарь) — тот же фильтр по проектам. */
  groupOptions?: CountItem[]
  groupLabel?: string
  /** Компактный сворачиваемый вид: строка-сводка + разворачивание по клику. */
  collapsible?: boolean
  /** Дополнительный блок внутри развёрнутой панели (учёт активности). */
  extra?: ReactNode
}

/** Панель фильтров: всё состояние — в URL search-params. */
export function FilterBar({
  sourceOptions,
  typeOptions,
  projectOptions,
  typeLabel,
  hideSources = false,
  projectsAsChips = false,
  projectLabel,
  groupOptions,
  groupLabel,
  collapsible = false,
  extra,
}: FilterBarProps) {
  const { filters, setFilters } = useFilters()
  const { t } = useT()
  const [q, setQ] = useState(filters.q)
  const [open, setOpen] = useState(!collapsible)
  const enabledSources = useEnabledSources()
  const { data: meta } = useMeta()

  const projectLabelText = projectLabel ?? t('filters.project')
  const groupLabelText = groupLabel ?? t('filters.meetingGroups')

  // Фильтровать можно только по включённым источникам: отключённый чип вводил бы
  // в заблуждение. На сами графики это не влияет — там остаются все данные.
  const sourceItems = useMemo<MetaSource[]>(() => {
    const enabled = new Set<string>(enabledSources)
    const base: MetaSource[] = sourceOptions?.length
      ? sourceOptions
      : enabledSources.map((key) => ({ key, label: sourceLabel(key), enabled: true }))
    return base.filter((s) => enabled.has(s.key))
  }, [sourceOptions, enabledSources])

  useEffect(() => {
    setQ(filters.q)
  }, [filters.q])

  // Типы событий — вычитающая логика, как у источников: пустой фильтр
  // означает «выбраны все» (все чипы подсвечены), клик убирает один тип.
  const typeKeys = (typeOptions ?? []).map((tt) => tt.key)
  const effectiveTypesSel = filters.types.length > 0 ? filters.types : typeKeys
  const toggleType = (key: string) => {
    const next = effectiveTypesSel.includes(key)
      ? effectiveTypesSel.filter((x) => x !== key)
      : [...effectiveTypesSel, key]
    // Сняли последний или выбрали все — возвращаемся к каноническому «все».
    if (next.length === 0 || typeKeys.every((k) => next.includes(k))) {
      setFilters({ types: [] })
      return
    }
    setFilters({ types: next })
  }

  const toggleProject = (key: string) => {
    const next = filters.projects.includes(key)
      ? filters.projects.filter((p) => p !== key)
      : [...filters.projects, key]
    setFilters({ projects: next })
  }

  // Сводка активных фильтров для свёрнутого вида.
  const activeParts = useMemo(() => {
    const parts: string[] = []
    if (filters.sources.length > 0)
      parts.push(`${t('filters.sources')}: ${filters.sources.map(sourceLabel).join(', ')}`)
    if (filters.types.length > 0)
      parts.push(`${t('filters.types')}: ${filters.types.length}`)
    if (filters.projects.length > 0)
      parts.push(`${projectLabelText}: ${filters.projects.length}`)
    if (filters.q) parts.push(`${t('filters.search')}: «${filters.q}»`)
    if (filters.minAi > 0) parts.push(`AI ≥ ${filters.minAi}`)
    return parts
  }, [filters, projectLabelText, t])

  const resetAll = () =>
    setFilters({ sources: [], types: [], projects: [], q: '', minAi: 0 })

  // Блок чипов, завязанный на общий фильтр по проектам. «Сбросить» снимает
  // только ключи этого блока: проекты и группы встреч не мешают друг другу.
  const projectChips = (id: string, label: string, options: CountItem[]) => {
    const selectedHere = options.filter((o) => filters.projects.includes(o.key)).map((o) => o.key)
    return (
      <div className="field">
        <span className="field-label" id={id}>
          {label}
        </span>
        <div className="chips" role="group" aria-labelledby={id}>
          {options.slice(0, 24).map((p) => (
            <button
              key={p.key}
              type="button"
              className="chip"
              aria-pressed={filters.projects.includes(p.key)}
              onClick={() => toggleProject(p.key)}
            >
              {p.label || p.key}
              <span className="num muted">{p.count}</span>
            </button>
          ))}
          {selectedHere.length > 0 && (
            <button
              type="button"
              className="chip"
              onClick={() =>
                setFilters({ projects: filters.projects.filter((p) => !selectedHere.includes(p)) })
              }
            >
              {t('filters.reset')}
            </button>
          )}
        </div>
      </div>
    )
  }

  return (
    <section className="card" style={{ padding: 14, display: 'grid', gap: 12 }}>
      {collapsible && (
        <div className="filterbar-head">
          <div className="filterbar-summary">
            <strong>{t('filters.title')}</strong>
            {activeParts.length === 0 ? (
              <span className="muted">{t('filters.noneActive')}</span>
            ) : (
              <span className="muted">{activeParts.join(' · ')}</span>
            )}
          </div>
          <div className="inline-group">
            {activeParts.length > 0 && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={resetAll}>
                {t('filters.resetAll')}
              </button>
            )}
            <button
              type="button"
              className="btn btn-sm"
              aria-expanded={open}
              onClick={() => setOpen((v) => !v)}
            >
              {open ? t('filters.collapse') : t('filters.expand')}
            </button>
          </div>
        </div>
      )}

      {open && (
        <>
          {extra}

          {!hideSources && sourceItems.length > 0 && (
            <SourceChips
              options={sourceItems}
              selected={filters.sources}
              onChange={(next) => setFilters({ sources: next })}
            />
          )}

          {typeOptions && typeOptions.length > 0 && (
            <div className="field">
              <span className="field-label" id="type-chips-label">
                {t('filters.eventTypes')}
              </span>
              <div className="chips" role="group" aria-labelledby="type-chips-label">
                {typeOptions.slice(0, 24).map((tt) => (
                  <button
                    key={tt.key}
                    type="button"
                    className="chip"
                    aria-pressed={effectiveTypesSel.includes(tt.key)}
                    title={t('filters.typeChipHint')}
                    onClick={() => toggleType(tt.key)}
                  >
                    {typeLabel ? typeLabel(tt.key) : tt.label || tt.key}
                    <span className="num muted">{tt.count}</span>
                  </button>
                ))}
                {filters.types.length > 0 && (
                  <button type="button" className="chip" onClick={() => setFilters({ types: [] })}>
                    {t('filters.reset')}
                  </button>
                )}
              </div>
            </div>
          )}

          {projectsAsChips &&
            projectOptions &&
            projectOptions.length > 0 &&
            projectChips('project-chips-label', projectLabelText, projectOptions)}

          {groupOptions &&
            groupOptions.length > 0 &&
            projectChips('group-chips-label', groupLabelText, groupOptions)}

          <div className="form-grid">
            {!projectsAsChips && (
              <div className="field">
                <label htmlFor="filter-project">{projectLabelText}</label>
                <select
                  id="filter-project"
                  className="select"
                  value={filters.projects[0] ?? ''}
                  onChange={(e) => setFilters({ projects: e.target.value ? [e.target.value] : [] })}
                >
                  <option value="">{t('filters.allProjects')}</option>
                  {projectOptions?.map((p) => (
                    <option key={p.key} value={p.key}>
                      {p.label || p.key} ({p.count})
                    </option>
                  ))}
                </select>
              </div>
            )}

            {meta?.ai_enabled && (
              <div className="field">
                <label htmlFor="filter-min-ai">{t('filters.minAi')}</label>
                <select
                  id="filter-min-ai"
                  className="select"
                  value={filters.minAi || ''}
                  onChange={(e) => setFilters({ minAi: Number(e.target.value) || 0 })}
                  title={t('filters.minAiHint')}
                >
                  <option value="">{t('filters.noFilter')}</option>
                  {[1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => (
                    <option key={n} value={n}>
                      ≥ {n}
                    </option>
                  ))}
                </select>
              </div>
            )}

            <form
              className="field"
              onSubmit={(e) => {
                e.preventDefault()
                setFilters({ q })
              }}
            >
              <label htmlFor="filter-q">{t('filters.textSearch')}</label>
              <div className="inline-group">
                <input
                  id="filter-q"
                  className="input"
                  type="search"
                  placeholder={t('filters.searchPlaceholder')}
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  style={{ flex: '1 1 160px' }}
                />
                <button type="submit" className="btn">
                  {t('filters.find')}
                </button>
              </div>
            </form>
          </div>
        </>
      )}
    </section>
  )
}
