import { useEffect, useMemo, useState } from 'react'
import { useT } from '../i18n'
import { DateRangePicker } from './DateRangePicker'
import {
  PERIOD_KINDS,
  detectPeriod,
  periodKindLabel,
  periodOptions,
  type PeriodKind,
} from '../lib/periods'

interface PeriodPickerProps {
  from: string
  to: string
  onChange: (range: { from: string; to: string }) => void
  /** Сколько последних периодов предлагать в выпадающем списке. */
  count?: number
  /** Компактный режим — без пресетов произвольного диапазона (для форм). */
  compact?: boolean
}

/**
 * Фильтр «Период»: сначала вид (день/неделя/месяц/квартал/год), затем
 * конкретный период этого вида — например, «Квартал → Q2 2026». Выбор сразу
 * устанавливает границы from/to. Вид «Произвольный» — обычные две даты.
 */
export function PeriodPicker({ from, to, onChange, count = 12, compact = false }: PeriodPickerProps) {
  const { t } = useT()
  const detected = useMemo(() => detectPeriod(from, to, count), [from, to, count])
  const [kind, setKind] = useState<PeriodKind>(detected?.kind ?? 'custom')

  // Диапазон могли поменять снаружи (ссылка, пресет) — селекторы догоняют.
  useEffect(() => {
    if (detected) setKind(detected.kind)
  }, [detected])

  const options = useMemo(
    () => (kind === 'custom' ? [] : periodOptions(kind, count)),
    [kind, count],
  )
  const selectedKey = detected && detected.kind === kind ? detected.key : ''

  const pickKind = (next: PeriodKind) => {
    setKind(next)
    if (next === 'custom') return
    // Смена вида сразу выбирает текущий период этого вида.
    const [first] = periodOptions(next, 1)
    if (first) onChange({ from: first.from, to: first.to })
  }

  return (
    <div className="inline-group">
      <label className="visually-hidden" htmlFor="period-kind">
        {t('picker.periodKind')}
      </label>
      <select
        id="period-kind"
        className="select"
        value={kind}
        onChange={(e) => pickKind(e.target.value as PeriodKind)}
      >
        {PERIOD_KINDS.map((k) => (
          <option key={k} value={k}>
            {periodKindLabel(k)}
          </option>
        ))}
      </select>

      {kind !== 'custom' && (
        <>
          <label className="visually-hidden" htmlFor="period-instance">
            {t('picker.periodInstance')}
          </label>
          <select
            id="period-instance"
            className="select"
            value={selectedKey}
            onChange={(e) => {
              const opt = options.find((o) => o.key === e.target.value)
              if (opt) onChange({ from: opt.from, to: opt.to })
            }}
          >
            {!selectedKey && <option value="">{t('picker.choose')}</option>}
            {options.map((o) => (
              <option key={o.key} value={o.key}>
                {o.label}
              </option>
            ))}
          </select>
        </>
      )}

      {kind === 'custom' && !compact && <DateRangePicker from={from} to={to} onChange={onChange} />}
    </div>
  )
}
