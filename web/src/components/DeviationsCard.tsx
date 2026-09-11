import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { ChartCard } from './ChartCard'
import { groupOf } from '../lib/ruleHelp'
import { fmtNumber } from '../lib/format'
import { useT } from '../i18n'
import type { ViolationItem } from '../lib/types'

interface DeviationsCardProps {
  violations: ViolationItem[]
  personKey: string
  /** search-строка фильтров, чтобы ссылка на «Нарушения» сохраняла контекст. */
  search?: string
}

/** Отклонения выбранного человека за период + сводная статистика.
 *  Данные считаются командно (кэш 10 мин) — здесь фильтруем по человеку. */
export function DeviationsCard({ violations, personKey, search = '' }: DeviationsCardProps) {
  const { t: tr } = useT()

  const mine = useMemo(
    () => violations.filter((v) => v.person_key === personKey),
    [violations, personKey],
  )
  const warns = mine.filter((v) => v.severity === 'warn').length
  const infos = mine.length - warns

  // Сводка по смысловым группам правил.
  const byGroup = useMemo(() => {
    const m = new Map<string, number>()
    for (const v of mine) {
      const g = groupOf(v.rule).label
      m.set(g, (m.get(g) ?? 0) + 1)
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [mine])

  return (
    <ChartCard
      title={tr('overview.secDeviations')}
      subtitle={tr('deviations.subtitle')}
      actions={
        <Link className="btn btn-sm" to={`/violations${search}`}>
          {tr('deviations.openAll')}
        </Link>
      }
    >
      {mine.length === 0 ? (
        <p className="muted" style={{ padding: '4px 2px' }}>
          {tr('deviations.none')}
        </p>
      ) : (
        <div className="stack" style={{ gap: 14 }}>
          {/* Сводная статистика */}
          <div className="stat-row" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <span className="deviation-stat">
              <b className="num">{fmtNumber(mine.length)}</b> {tr('deviations.total')}
            </span>
            <span className="deviation-stat warn">
              <b className="num">{fmtNumber(warns)}</b> {tr('deviations.warns')}
            </span>
            <span className="deviation-stat info">
              <b className="num">{fmtNumber(infos)}</b> {tr('deviations.infos')}
            </span>
            {byGroup.map(([g, n]) => (
              <span key={g} className="deviation-stat">
                {g}: <b className="num">{fmtNumber(n)}</b>
              </span>
            ))}
          </div>

          {/* Список отклонений */}
          <div className="tablewrap-plain">
            {mine.map((v, i) => (
              <div key={`${v.rule}-${i}`} className={`deviation-row deviation-${v.severity}`}>
                <span
                  className={`badge badge-status ${v.severity === 'warn' ? 'badge-failed' : 'badge-done'}`}
                >
                  {v.severity === 'warn' ? tr('deviations.warn') : tr('deviations.info')}
                </span>
                <span>
                  <span className="deviation-title">{v.title || v.rule}</span>
                  <span className="deviation-detail muted"> {v.detail}</span>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </ChartCard>
  )
}
