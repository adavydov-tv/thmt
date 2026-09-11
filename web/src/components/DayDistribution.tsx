import { ChartCard } from './ChartCard'
import { SEQUENTIAL, useChartTokens } from '../lib/chartTheme'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { Summary } from '../lib/types'

interface DayDistributionProps {
  summary: Summary
}

interface Segment {
  key: string
  label: string
  count: number
  color: string
}

/**
 * Распределение дней периода одной стековой полосой: рабочие с активностью
 * (с градацией по объёму активности за день), рабочие без активности,
 * выходные и праздники, отпуск. Категории — слоты категориальной палитры
 * проекта (3/2/6), пары проверены валидатором в обеих темах; градация объёма —
 * величина, а не идентичность, поэтому кодируется секвенциальной синей рампой
 * (монотонно светлый → тёмный). Подписи с числами в легенде и таблица
 * дублируют цвет.
 */
export function DayDistribution({ summary }: DayDistributionProps) {
  const t = useChartTokens()
  const { t: tr, tp } = useT()

  // Ступени рампы разнесены на равные шаги: соседние различимы (ΔE ≈ 19).
  const rampColors = [SEQUENTIAL[3], SEQUENTIAL[7], SEQUENTIAL[11]]
  const buckets = summary.active_day_buckets ?? []
  const activeSegments: Segment[] =
    buckets.length > 0
      ? buckets.map((b, i) => ({
          key: `active-${b.key}`,
          label: tr('chart.daysWorkingPrefix', { label: b.label || b.key }),
          count: b.count,
          color: rampColors[Math.min(i, rampColors.length - 1)],
        }))
      : [
          {
            key: 'active',
            label: tr('chart.daysActive'),
            count: summary.active_working_days,
            color: t.slot(0),
          },
        ]

  const offDays = summary.weekend_days + summary.holiday_days
  const segments: Segment[] = [
    ...activeSegments,
    { key: 'idle', label: tr('chart.daysIdle'), count: summary.idle_working_days, color: t.slot(3) },
    { key: 'off', label: tr('chart.daysOff'), count: offDays, color: t.slot(2) },
    { key: 'vacation', label: tr('chart.daysVacation'), count: summary.vacation_days, color: t.slot(6) },
    { key: 'sick', label: tr('chart.daysSick'), count: summary.sick_days, color: t.slot(7) },
  ]
  const total = segments.reduce((acc, s) => acc + s.count, 0)
  if (total === 0) return null

  const pct = (n: number) => Math.round((n / total) * 1000) / 10

  const subtitleParts = [
    summary.holiday_country
      ? tr('chart.holidaysCountry', { country: summary.holiday_country })
      : tr('chart.holidaysUnknown'),
    summary.office ? tr('chart.office', { office: summary.office }) : null,
  ].filter(Boolean)

  const table = (
    <table className="data">
      <caption className="visually-hidden">
        {tr('chart.tableCaption', { title: tr('chart.daysTitle') })}
      </caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.category')}</th>
          <th scope="col" className="num">{tr('chart.daysCol')}</th>
          <th scope="col" className="num">{tr('chart.share')}</th>
        </tr>
      </thead>
      <tbody>
        {segments.map((s) => (
          <tr key={s.key}>
            <th scope="row" style={{ fontWeight: 400 }}>{s.label}</th>
            <td className="num">{fmtNumber(s.count)}</td>
            <td className="num">{pct(s.count)}%</td>
          </tr>
        ))}
      </tbody>
    </table>
  )

  return (
    <ChartCard
      title={tr('chart.daysTitle')}
      subtitle={`${tr('chart.daysOfPeriod', { n: fmtNumber(total), days: tp('chart.pluralDays', total) })} · ${subtitleParts.join(' · ')}`}
      table={table}
    >
      <div className="daydist">
        <div className="daydist-bar" role="img" aria-label={tr('chart.daysAria')}>
          {segments
            .filter((s) => s.count > 0)
            .map((s) => (
              <div
                key={s.key}
                className="daydist-seg"
                style={{ flexGrow: s.count, background: s.color }}
                title={`${s.label}: ${fmtNumber(s.count)} ${tp('chart.pluralDays', s.count)} (${pct(s.count)}%)`}
              />
            ))}
        </div>
        <div className="daydist-legend">
          {segments.map((s) => (
            <span className="daydist-item" key={s.key}>
              <span className="dot" style={{ background: s.color }} aria-hidden="true" />
              {s.label}
              <span className="num">
                {fmtNumber(s.count)} ({pct(s.count)}%)
              </span>
            </span>
          ))}
        </div>
      </div>
    </ChartCard>
  )
}
