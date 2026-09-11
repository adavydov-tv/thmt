import { useT } from '../i18n'
import type { MessageKey } from '../i18n'
import { fromDateInput, toDateInput } from '../lib/format'
import { currentMonthRange, defaultRange } from '../lib/useFilters'

interface DateRangePickerProps {
  from: string
  to: string
  onChange: (range: { from: string; to: string }) => void
}

const PRESETS: { key: string; labelKey: MessageKey; days?: number; month?: boolean }[] = [
  { key: '7', labelKey: 'picker.days7', days: 7 },
  { key: '30', labelKey: 'picker.days30', days: 30 },
  { key: '90', labelKey: 'picker.days90', days: 90 },
  { key: 'month', labelKey: 'picker.currentMonth', month: true },
]

function matchPreset(from: string, to: string): string | null {
  for (const p of PRESETS) {
    const range = p.month ? currentMonthRange() : defaultRange(p.days)
    if (
      toDateInput(range.from) === toDateInput(from) &&
      toDateInput(range.to) === toDateInput(to)
    ) {
      return p.key
    }
  }
  return null
}

export function DateRangePicker({ from, to, onChange }: DateRangePickerProps) {
  const { t } = useT()
  const active = matchPreset(from, to)

  return (
    <div className="inline-group">
      <div className="segmented" role="group" aria-label={t('picker.presetsAria')}>
        {PRESETS.map((p) => (
          <button
            key={p.key}
            type="button"
            aria-pressed={active === p.key}
            onClick={() => onChange(p.month ? currentMonthRange() : defaultRange(p.days))}
          >
            {t(p.labelKey)}
          </button>
        ))}
      </div>
      <div className="inline-group">
        <label className="visually-hidden" htmlFor="range-from">
          {t('picker.rangeFrom')}
        </label>
        <input
          id="range-from"
          className="input"
          type="date"
          value={toDateInput(from)}
          max={toDateInput(to)}
          onChange={(e) => {
            const value = e.target.value
            if (value) onChange({ from: fromDateInput(value, 'start'), to })
          }}
        />
        <span className="muted" aria-hidden="true">
          —
        </span>
        <label className="visually-hidden" htmlFor="range-to">
          {t('picker.rangeTo')}
        </label>
        <input
          id="range-to"
          className="input"
          type="date"
          value={toDateInput(to)}
          min={toDateInput(from)}
          onChange={(e) => {
            const value = e.target.value
            if (value) onChange({ from, to: fromDateInput(value, 'end') })
          }}
        />
      </div>
    </div>
  )
}
