import { useEffect, useMemo, useRef, useState } from 'react'
import { useT } from '../i18n'
import type { HrdbTeam } from '../lib/types'

interface TeamPickerProps {
  teams: HrdbTeam[]
  selected: string[]
  onChange: (next: string[]) => void
  label?: string
  placeholder?: string
}

interface UnitGroup {
  name: string
  teams: HrdbTeam[]
}

interface ClusterGroup {
  name: string
  units: UnitGroup[]
}

interface DeptGroup {
  name: string
  clusters: ClusterGroup[]
}

/**
 * Выбор команд: инпут с выпадашкой — поиск, иерархия «Департамент → Кластер →
 * команды» и чекбоксы на всех уровнях (уровень выбирает всё внутри себя).
 */
export function TeamPicker({
  teams,
  selected,
  onChange,
  label,
  placeholder,
}: TeamPickerProps) {
  // Локальная переменная t занята командами (HrdbTeam) — переводчик зовём tr.
  const { t: tr } = useT()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)

  // Каждое открытие попапа начинается с чистого поиска — «залипший» старый
  // запрос выглядит как пустой список без видимой причины.
  useEffect(() => {
    if (open) setQuery('')
  }, [open])

  // Закрытие по клику мимо и по Escape.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
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

  // Полная иерархия: департамент → кластер → юнит → команды; поиск
  // фильтрует ветки по любому уровню. Пословно: каждое слово запроса должно
  // встретиться где-то в ветке — порядок слов и лишние пробелы не важны
  // («udf storage», «storage udf», «udf  storage» находят одно и то же).
  const groups = useMemo<DeptGroup[]>(() => {
    const norm = (s: string) => s.toLowerCase().replace(/\u00a0/g, ' ')
    const tokens = norm(query).split(/\s+/).filter(Boolean)
    const matches = (t: HrdbTeam) => {
      if (tokens.length === 0) return true
      const hay = norm(
        [t.name, t.unit ?? '', t.cluster ?? '', t.department ?? ''].join(' '),
      )
      return tokens.every((tok) => hay.includes(tok))
    }

    const byDept = new Map<string, Map<string, Map<string, HrdbTeam[]>>>()
    for (const t of teams) {
      if (!matches(t)) continue
      const dept = t.department || tr('picker.noDepartment')
      const cluster = t.cluster || tr('picker.noCluster')
      // Команда без юнита лежит прямо под кластером — без пустого уровня.
      const unit = t.unit || ''
      const clusters = byDept.get(dept) ?? new Map<string, Map<string, HrdbTeam[]>>()
      const units = clusters.get(cluster) ?? new Map<string, HrdbTeam[]>()
      const list = units.get(unit) ?? []
      list.push(t)
      units.set(unit, list)
      clusters.set(cluster, units)
      byDept.set(dept, clusters)
    }
    const sortRu = (a: string, b: string) => a.localeCompare(b, 'ru')
    return [...byDept.entries()].sort((a, b) => sortRu(a[0], b[0])).map(([dept, clusters]) => ({
      name: dept,
      clusters: [...clusters.entries()].sort((a, b) => sortRu(a[0], b[0])).map(([name, units]) => ({
        name,
        units: [...units.entries()]
          .sort((a, b) => sortRu(a[0], b[0]))
          .map(([unit, list]) => ({
            name: unit,
            teams: [...list].sort((a, b) => sortRu(a.name, b.name)),
          })),
      })),
    }))
  }, [teams, query, tr])

  const selectedSet = useMemo(() => new Set(selected), [selected])

  const toggleTeam = (name: string) => {
    onChange(
      selectedSet.has(name) ? selected.filter((s) => s !== name) : [...selected, name],
    )
  }
  // Чекбокс уровня: выбрано всё внутри → снять всё; иначе — добрать всё.
  const toggleGroup = (names: string[]) => {
    const allSelected = names.every((n) => selectedSet.has(n))
    if (allSelected) {
      const drop = new Set(names)
      onChange(selected.filter((s) => !drop.has(s)))
    } else {
      const next = new Set(selected)
      names.forEach((n) => next.add(n))
      onChange([...next])
    }
  }

  const groupState = (names: string[]) => {
    const picked = names.filter((n) => selectedSet.has(n)).length
    return { checked: picked > 0 && picked === names.length, indeterminate: picked > 0 && picked < names.length }
  }

  const summary =
    selected.length === 0
      ? placeholder ?? tr('picker.allTeams')
      : tr('picker.selected', { n: selected.length })

  return (
    <div className="field teampicker" ref={rootRef}>
      <span className="field-label">{label ?? tr('picker.teams')}</span>
      <button
        type="button"
        className="input teampicker-toggle"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={selected.length === 0 ? 'muted' : undefined}>{summary}</span>
        <span aria-hidden="true" className="muted">
          {open ? '▴' : '▾'}
        </span>
      </button>

      {selected.length > 0 && (
        <div className="chips" style={{ marginTop: 6 }}>
          {selected.map((name) => (
            <button
              key={name}
              type="button"
              className="chip"
              aria-pressed="true"
              title={tr('picker.removeTeam')}
              onClick={() => toggleTeam(name)}
            >
              {name} <span aria-hidden="true">✕</span>
            </button>
          ))}
        </div>
      )}

      {open && (
        <div className="teampicker-pop card" role="dialog" aria-label={tr('picker.teamsDialog')}>
          <div className="teampicker-head">
            <input
              className="input"
              type="search"
              placeholder={tr('picker.teamSearch')}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
            />
            {selected.length > 0 && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => onChange([])}>
                {tr('picker.resetN', { n: selected.length })}
              </button>
            )}
          </div>
          <div className="teampicker-list">
            {groups.length === 0 && <p className="muted" style={{ margin: 8 }}>{tr('picker.nothingFound')}</p>}
            {groups.map((dept) => {
              const deptTeams = dept.clusters.flatMap((c) =>
                c.units.flatMap((u) => u.teams.map((t) => t.name)),
              )
              return (
                <div key={dept.name} className="teampicker-dept">
                  <GroupRow
                    name={dept.name}
                    level={0}
                    state={groupState(deptTeams)}
                    onToggle={() => toggleGroup(deptTeams)}
                  />
                  {dept.clusters.map((cluster) => {
                    const clusterTeams = cluster.units.flatMap((u) => u.teams.map((t) => t.name))
                    return (
                      <div key={cluster.name}>
                        <GroupRow
                          name={cluster.name}
                          level={1}
                          state={groupState(clusterTeams)}
                          onToggle={() => toggleGroup(clusterTeams)}
                        />
                        {cluster.units.map((unit) => {
                          const unitTeams = unit.teams.map((t) => t.name)
                          const teamPad = unit.name ? 62 : 44
                          return (
                            <div key={unit.name || '(без юнита)'}>
                              {unit.name && (
                                <GroupRow
                                  name={unit.name}
                                  level={2}
                                  state={groupState(unitTeams)}
                                  onToggle={() => toggleGroup(unitTeams)}
                                />
                              )}
                              {unit.teams.map((t) => (
                                <label
                                  key={t.name}
                                  className="teampicker-row"
                                  style={{ paddingLeft: teamPad }}
                                >
                                  <input
                                    type="checkbox"
                                    checked={selectedSet.has(t.name)}
                                    onChange={() => toggleTeam(t.name)}
                                  />
                                  <span style={{ flex: 1 }}>{t.name}</span>
                                  <span className="num muted">{t.members}</span>
                                </label>
                              ))}
                            </div>
                          )
                        })}
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

function GroupRow({
  name,
  level,
  state,
  onToggle,
}: {
  name: string
  level: number
  state: { checked: boolean; indeterminate: boolean }
  onToggle: () => void
}) {
  return (
    <label className="teampicker-row teampicker-group" style={{ paddingLeft: 8 + level * 18 }}>
      <input
        type="checkbox"
        checked={state.checked}
        ref={(el) => {
          if (el) el.indeterminate = state.indeterminate
        }}
        onChange={onToggle}
      />
      <span style={{ fontWeight: level === 0 ? 600 : 500 }}>{name}</span>
    </label>
  )
}
