import { useMemo, useState } from 'react'
import { useActivityTypes } from '../lib/activityTypes'
import { useEnabledSources, useMeta } from '../lib/queries'
import { useTypeLabeler } from '../lib/useStatsQuery'
import { sourceLabel, useChartTokens } from '../lib/chartTheme'
import { useT } from '../i18n'

interface ActivityTypesPickerProps {
  /** Ограничить панель источниками страницы (например, одним на /source/:source). */
  sources?: string[]
  /** Встроенный режим: без собственной карточки (внутри панели фильтров). */
  embedded?: boolean
}

/**
 * Панель «что учитывать в активности»: по каждому источнику выбираются типы
 * событий, которые попадают в графики, метрики и ленту. Выбор общий для всех
 * вкладок и переживает перезагрузку страницы.
 */
export function ActivityTypesPicker({ sources, embedded = false }: ActivityTypesPickerProps) {
  const { excluded, typesBySource, toggleType, setSource } = useActivityTypes()
  const enabledSources = useEnabledSources()
  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const [open, setOpen] = useState(false)

  const shown = useMemo(
    () =>
      (sources?.length ? sources : enabledSources).filter(
        (s) => (typesBySource[s] ?? []).length > 0,
      ),
    [sources, enabledSources, typesBySource],
  )

  const excludedShown = useMemo(
    () =>
      shown.reduce(
        (acc, s) => acc + (typesBySource[s] ?? []).filter((x) => excluded.has(x)).length,
        0,
      ),
    [shown, typesBySource, excluded],
  )

  if (shown.length === 0) return null

  const includeAll = () => shown.forEach((s) => setSource(s, true))

  return (
    <section className={embedded ? 'activity-picker' : 'card activity-picker'}>
      <div className="activity-picker-head">
        <div>
          <span className="field-label">{tr('types.title')}</span>
          <p className="muted" style={{ margin: 0, fontSize: 13 }}>
            {excludedShown === 0
              ? tr('types.allIncluded')
              : `${tr('types.excluded')} ${excludedShown} ${tp('plural.types', excludedShown)}`}
          </p>
        </div>
        <div className="inline-group">
          {excludedShown > 0 && (
            <button type="button" className="btn btn-sm" onClick={includeAll}>
              {tr('types.includeAll')}
            </button>
          )}
          <button
            type="button"
            className="btn btn-sm"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? tr('types.collapse') : tr('types.configure')}
          </button>
        </div>
      </div>

      {open && (
        <div className="activity-picker-body">
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>
            {tr('types.hint')}
          </p>
          {shown.map((src) => {
            const own = typesBySource[src] ?? []
            const offCount = own.filter((x) => excluded.has(x)).length
            return (
              <div className="field" key={src}>
                <div className="inline-group">
                  <span
                    className="field-label"
                    id={`activity-types-${src}`}
                    style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
                  >
                    <span
                      className="dot"
                      style={{ background: t.sourceColor(src) }}
                      aria-hidden="true"
                    />
                    {sourceLabel(src)}
                  </span>
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost"
                    disabled={offCount === 0}
                    onClick={() => setSource(src, true)}
                  >
                    {tr('types.all')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost"
                    disabled={offCount === own.length}
                    onClick={() => setSource(src, false)}
                  >
                    {tr('types.none')}
                  </button>
                </div>
                <div className="chips" role="group" aria-labelledby={`activity-types-${src}`}>
                  {own.map((type) => (
                    <button
                      key={type}
                      type="button"
                      className="chip"
                      aria-pressed={!excluded.has(type)}
                      onClick={() => toggleType(type)}
                    >
                      {typeLabel(type)}
                    </button>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
