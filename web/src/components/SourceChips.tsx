import { useT } from '../i18n'
import { SOURCE_KEYS, sourceLabel, useChartTokens } from '../lib/chartTheme'

interface SourceChipsProps {
  /** Доступные источники; по умолчанию — все четыре. */
  options?: { key: string; label: string; enabled?: boolean }[]
  selected: string[]
  onChange: (next: string[]) => void
  label?: string
}

/**
 * Мультиселект источников чипами, вычитающая логика: по умолчанию выбраны
 * все, клик по чипу убирает один источник из выбора (а не выбирает его
 * одного). Пустой выбор в URL по-прежнему означает «все источники».
 */
export function SourceChips({ options, selected, onChange, label }: SourceChipsProps) {
  const { t: tr } = useT()
  const t = useChartTokens()
  const list = options?.length
    ? options
    : SOURCE_KEYS.map((key) => ({ key, label: sourceLabel(key), enabled: true }))
  const keys = list.map((o) => o.key)

  // Фактический выбор: пустой список = все источники.
  const effective = selected.length > 0 ? selected : keys

  const toggle = (key: string) => {
    const next = effective.includes(key)
      ? effective.filter((s) => s !== key)
      : [...effective, key]
    // Сняли последний источник или выбрали все — возвращаемся к «все»
    // (канонически это пустой список в URL).
    if (next.length === 0 || keys.every((k) => next.includes(k))) {
      onChange([])
      return
    }
    onChange(next)
  }

  return (
    <div className="field">
      <span className="field-label" id="source-chips-label">
        {label ?? tr('picker.sources')}
      </span>
      <div className="chips" role="group" aria-labelledby="source-chips-label">
        {list.map((opt) => {
          const active = effective.includes(opt.key)
          return (
            <button
              key={opt.key}
              type="button"
              className="chip"
              aria-pressed={active}
              onClick={() => toggle(opt.key)}
              title={opt.enabled === false ? tr('picker.sourceDisabled') : undefined}
            >
              <span
                className="dot"
                style={{ background: t.sourceColor(opt.key), opacity: active ? 1 : 0.35 }}
                aria-hidden="true"
              />
              {opt.label || sourceLabel(opt.key)}
            </button>
          )
        })}
        {selected.length > 0 && (
          <button type="button" className="chip" onClick={() => onChange([])}>
            {tr('picker.reset')}
          </button>
        )}
      </div>
    </div>
  )
}
