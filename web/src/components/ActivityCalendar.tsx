import { Fragment, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { ChartCard } from './ChartCard'
import { DayDetail, DAY_EVENTS_LIMIT, dayRangeISO } from './DayDetail'
import { EmptyState } from './EmptyState'
import { SEQUENTIAL, SEQUENTIAL_SHALLOW, sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtNumber } from '../lib/format'
import { useEvents, useMeta } from '../lib/queries'
import { useStatsQuery, useTypeLabeler } from '../lib/useStatsQuery'
import { useT } from '../i18n'
import { pick } from '../i18n/lang'
import type { TimelinePoint } from '../lib/types'

// Словарики вызываются в рендере: приложение перемонтируется при смене языка.
function weekdayLabels(): string[] {
  return pick(['Пн', '', 'Ср', '', 'Пт', '', 'Вс'], ['Mon', '', 'Wed', '', 'Fri', '', 'Sun'])
}

function monthNames(): string[] {
  return pick(
    ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'],
    ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
  )
}

function makeDayFmt(): Intl.DateTimeFormat {
  return new Intl.DateTimeFormat(pick('ru-RU', 'en-US'), {
    weekday: 'short',
    day: 'numeric',
    month: 'long',
    year: 'numeric',
  })
}

/** Особый день для разметки: праздник, отпуск, больничный или день из дома (выходные считаются сами). */
export interface SpecialDay {
  kind: 'holiday' | 'vacation' | 'sick' | 'remote'
  label?: string
}

type DayKind = '' | 'weekend' | 'holiday' | 'vacation' | 'sick' | 'remote'

function kindLabels(): Record<Exclude<DayKind, ''>, string> {
  return {
    weekend: pick('выходной', 'weekend'),
    holiday: pick('праздник', 'holiday'),
    vacation: pick('отпуск', 'vacation'),
    sick: pick('больничный', 'sick leave'),
    remote: pick('день из дома', 'work from home'),
  }
}

interface ActivityCalendarProps {
  points: TimelinePoint[]
  from: string
  to: string
  /** Праздники и отпуска по датам (YYYY-MM-DD). */
  specialDays?: Record<string, SpecialDay>
  /** Дни заведённых овертаймов (YYYY-MM-DD) — центральная точка на матрице. */
  overtimeDays?: string[]
  /** «Поверхностные» дни (YYYY-MM-DD): активность — только Slack и входы;
   *  красятся розово-красной шкалой вместо синей. */
  shallowDays?: string[]
  /** Дата найма (YYYY-MM-DD) — уголок-флажок на ячейке дня выхода. */
  hireDate?: string
  subtitle?: ReactNode
  actions?: ReactNode
}

interface DayCell {
  key: string // YYYY-MM-DD
  date: Date
  count: number
  kind: DayKind
  kindLabel: string
  overtime: boolean
  /** Активность дня — только Slack и входы (presence без работы). */
  shallow: boolean
  hired: boolean
  /** День до даты найма — сотрудник ещё не работал (штриховка). */
  prehire: boolean
}

interface HoverState {
  key: string
  label: string
  sub?: string
  count: number
  /** Координаты вьюпорта (position: fixed): тултип не клипается карточкой
   *  (.chart-card имеет overflow: hidden) даже при длинной разбивке. */
  left: number
  /** Верхняя граница тултипа — растёт вниз (ячейка в верхней части экрана). */
  top?: number
  /** Отступ от низа вьюпорта — растёт вверх (ячейка в нижней части экрана). */
  bottom?: number
}

/** Строка почасовой разбивки в тултипе. */
interface HourRow {
  /** Подпись «7–8» только у первой строки часа. */
  hourLabel: string
  name: string
  color: string
  count: number
}

const TOOLTIP_MAX_ROWS = 14

function dayKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

function dateFromKey(key: string): Date {
  const [y, m, d] = key.split('-').map(Number)
  return new Date(y, (m ?? 1) - 1, d ?? 1)
}

/**
 * Матрица активности по дням, как календарь контрибуций в GitLab: колонка —
 * неделя, строка — день недели, насыщенность — число событий за день.
 * Наведение на день показывает почасовую разбивку, клик открывает окно
 * с событиями дня и таймлайном.
 */
export function ActivityCalendar({ points, from, to, specialDays, overtimeDays, shallowDays, hireDate, subtitle, actions }: ActivityCalendarProps) {
  const t = useChartTokens()
  const { t: tr, tp } = useT()
  const [hover, setHover] = useState<HoverState | null>(null)
  const [selected, setSelected] = useState<DayCell | null>(null)
  const dayFmt = makeDayFmt()
  const kinds = kindLabels()

  const { data: meta } = useMeta()
  const typeLabel = useTypeLabeler(meta?.type_labels)
  const q = useStatsQuery()

  const { weeks, max, total } = useMemo(() => {
    const kindLabelOf = kindLabels()
    const otSet = new Set(overtimeDays ?? [])
    const shallowSet = new Set(shallowDays ?? [])
    const byDay = new Map<string, number>()
    for (const p of points) {
      const d = new Date(p.bucket)
      if (Number.isNaN(d.getTime())) continue
      byDay.set(dayKey(d), (byDay.get(dayKey(d)) ?? 0) + p.total)
    }

    const start = new Date(from)
    const end = new Date(to)
    start.setHours(0, 0, 0, 0)
    end.setHours(0, 0, 0, 0)
    // Начинаем с понедельника первой недели, чтобы колонки были ровными.
    start.setDate(start.getDate() - ((start.getDay() + 6) % 7))

    const ws: DayCell[][] = []
    let week: DayCell[] = []
    let mx = 0
    let sum = 0
    for (let d = new Date(start); d.getTime() <= end.getTime(); d.setDate(d.getDate() + 1)) {
      const key = dayKey(d)
      const count = byDay.get(key) ?? 0
      if (count > mx) mx = count
      sum += count
      const special = specialDays?.[key]
      const kind: DayKind =
        special?.kind ?? (d.getDay() === 0 || d.getDay() === 6 ? 'weekend' : '')
      const kindLabel = kind === '' ? '' : special?.label || kindLabelOf[kind]
      week.push({
        key,
        date: new Date(d),
        count,
        kind,
        kindLabel,
        overtime: otSet.has(key),
        shallow: count > 0 && shallowSet.has(key),
        hired: key === hireDate,
        prehire: Boolean(hireDate) && key < hireDate!,
      })
      if (week.length === 7) {
        ws.push(week)
        week = []
      }
    }
    if (week.length > 0) ws.push(week)
    return { weeks: ws, max: mx, total: sum }
  }, [points, from, to, specialDays, overtimeDays, shallowDays, hireDate])

  // Почасовая разбивка для тултипа: события наведённого дня подгружаются
  // с задержкой (чтобы пробег мыши по матрице не рассыпался в запросы)
  // и кэшируются React Query — повторное наведение мгновенно.
  const hoverKey = hover && hover.count > 0 ? hover.key : null
  const [detailKey, setDetailKey] = useState<string | null>(null)
  useEffect(() => {
    if (!hoverKey) return
    if (hoverKey === detailKey) return
    const id = setTimeout(() => setDetailKey(hoverKey), 150)
    return () => clearTimeout(id)
  }, [hoverKey, detailKey])

  const detailRange = useMemo(
    () => (detailKey ? dayRangeISO(dateFromKey(detailKey)) : null),
    [detailKey],
  )
  const dayEvents = useEvents(
    {
      ...q,
      from: detailRange?.from,
      to: detailRange?.to,
      page: 1,
      per_page: DAY_EVENTS_LIMIT,
      sort: 'asc',
    },
    { enabled: Boolean(detailRange), staleTime: 5 * 60_000 },
  )

  // Строки тултипа: час → счётчики по паре источник+тип («9–10 · коммит · GitLab ×2»).
  const { hourRows, hourRowsRest } = useMemo(() => {
    const items = dayEvents.data?.items ?? []
    if (items.length === 0) return { hourRows: [] as HourRow[], hourRowsRest: 0 }
    const byHour = new Map<number, Map<string, { source: string; type: string; count: number }>>()
    for (const e of items) {
      const d = new Date(e.occurred_at)
      if (Number.isNaN(d.getTime())) continue
      const h = d.getHours()
      const bucket = byHour.get(h) ?? new Map()
      const k = `${e.source}|${e.type}`
      const cur = bucket.get(k) ?? { source: e.source, type: e.type, count: 0 }
      cur.count += 1
      bucket.set(k, cur)
      byHour.set(h, bucket)
    }
    const rows: HourRow[] = []
    for (const h of [...byHour.keys()].sort((a, b) => a - b)) {
      const entries = [...(byHour.get(h)?.values() ?? [])].sort((a, b) => b.count - a.count)
      entries.forEach((entry, i) => {
        rows.push({
          hourLabel: i === 0 ? `${h}–${(h + 1) % 24}` : '',
          name: `${typeLabel(entry.type)} · ${sourceLabel(entry.source)}`,
          color: t.sourceColor(entry.source),
          count: entry.count,
        })
      })
    }
    if (rows.length <= TOOLTIP_MAX_ROWS) return { hourRows: rows, hourRowsRest: 0 }
    // Обрезаем по границе часа, чтобы не бросать час на полуслове.
    let cut = TOOLTIP_MAX_ROWS
    while (cut < rows.length && rows[cut].hourLabel === '') cut += 1
    return { hourRows: rows.slice(0, cut), hourRowsRest: rows.length - cut }
  }, [dayEvents.data, typeLabel, t])

  const colorFor = (count: number, shallow = false) => {
    if (count <= 0 || max <= 0) return t.surface
    const ramp = shallow ? SEQUENTIAL_SHALLOW : SEQUENTIAL
    const idx = Math.min(ramp.length - 1, Math.max(1, Math.round((count / max) * (ramp.length - 1))))
    return ramp[idx]
  }

  // Разметка особых дней: выходной — приглушённый фон пустой ячейки,
  // праздник — зелёное кольцо, отпуск — фиолетовое, больничный — красное
  // (слоты палитры те же, что в «Распределении дней»).
  // Цвет кольца по виду нерабочего дня: выходной — нейтральный серый,
  // праздник — зелёный, отпуск — фиолетовый, больничный — красный.
  const kindRing: Record<string, string> = {
    weekend: 'var(--text-muted)',
    holiday: t.slot(2),
    vacation: t.slot(6),
    sick: t.slot(7),
  }
  // Дни, в которые активности быть НЕ должно: активность здесь — сигнал,
  // и её нужно чётко видеть на заливке.
  const OFF_KINDS = new Set(['weekend', 'holiday', 'vacation', 'sick'])
  // День из дома — янтарный в обеих темах (ΔE от красного больничного 17–21,
  // тёмный слот палитры сливался бы с ним) и пунктиром, а не сплошным кольцом:
  // форма отличает его от больничного даже без цвета.
  const REMOTE_RING = '#eda100'
  // Дата найма — синий: свободен от остальных маркеров календаря.
  const HIRE_COLOR = '#d6219c'
  const cellStyle = (day: DayCell): CSSProperties => {
    const style: CSSProperties = { background: colorFor(day.count, day.shallow) }
    const off = OFF_KINDS.has(day.kind)
    // День из дома — рабочий: пустая ячейка не приглушается, только кольцо.
    if (day.count <= 0 && day.kind !== '' && day.kind !== 'remote') {
      style.background = 'var(--surface-sunken)'
    }
    if (day.kind === 'remote') {
      style.outline = `2px dashed ${REMOTE_RING}`
      style.outlineOffset = -2
    } else if (off && day.count > 0) {
      // Активность в нерабочий день: жирное кольцо цвета вида + светлый
      // разделитель внутри — так кольцо читается на любой заливке.
      style.boxShadow = `inset 0 0 0 3px ${kindRing[day.kind]}, inset 0 0 0 4px var(--surface)`
    } else if (kindRing[day.kind] && day.kind !== 'weekend') {
      // Пустой особый день — тонкое кольцо (пустой выходной не выделяем).
      style.boxShadow = `inset 0 0 0 2px ${kindRing[day.kind]}`
    }
    // Дни до найма — штриховка: сотрудник ещё не работал.
    if (day.prehire) {
      style.background =
        'repeating-linear-gradient(135deg, var(--surface-sunken) 0 4px, transparent 4px 8px)'
    }
    // День найма — жирное синее кольцо, поверх остальных маркеров.
    if (day.hired) {
      style.outline = `2px solid ${HIRE_COLOR}`
      style.outlineOffset = -1
    }
    return style
  }

  // Подпись месяца — над неделей, где начинается новый месяц. Подпись шире
  // колонки, поэтому две ближе трёх недель наезжают друг на друга — в этом
  // случае остаётся только более поздняя (обрезанный месяц в начале периода
  // уступает место полному).
  const monthLabels = useMemo(() => {
    const months = monthNames()
    const labels = weeks.map(() => '')
    let prevMonth = -1
    let lastIdx = -1
    weeks.forEach((w, i) => {
      const m = w[0]?.date.getMonth() ?? -1
      if (m === prevMonth) return
      prevMonth = m
      if (lastIdx >= 0 && i - lastIdx < 3) labels[lastIdx] = ''
      labels[i] = months[m] ?? ''
      lastIdx = i
    })
    return labels
  }, [weeks])

  const table = (
    <table className="data">
      <caption className="visually-hidden">{tr('cal.tableCaption')}</caption>
      <thead>
        <tr>
          <th scope="col">{tr('chart.day')}</th>
          <th scope="col" className="num">
            {tr('chart.events')}
          </th>
        </tr>
      </thead>
      <tbody>
        {weeks
          .flat()
          .filter((d) => d.count > 0)
          .map((d) => (
            <tr key={d.key}>
              <th scope="row" style={{ fontWeight: 400 }}>
                {dayFmt.format(d.date)}
              </th>
              <td className="num">{fmtNumber(d.count)}</td>
            </tr>
          ))}
      </tbody>
    </table>
  )

  // Разбивка показывается, когда данные загружены именно для наведённого дня:
  // useEvents подставляет placeholderData предыдущего запроса, поэтому без
  // проверки isPlaceholderData тултип мигал бы разбивкой чужого дня.
  const breakdownReady =
    hover != null && hover.key === detailKey && dayEvents.data != null && !dayEvents.isPlaceholderData
  const breakdownLoading = hoverKey != null && !breakdownReady

  return (
    <ChartCard
      title={tr('cal.title')}
      subtitle={subtitle ?? tr('cal.subtitle')}
      actions={actions}
      table={total ? table : undefined}
    >
      {total === 0 ? (
        <EmptyState title={tr('chart.noActivity')} />
      ) : (
        <div className="cal-root" style={{ position: 'relative' }} onMouseLeave={() => setHover(null)}>
          <div className="cal-scroll">
            {/* Одна сетка на всё: колонка подписей дней + по колонке на неделю.
                Ячейки резиновые (1fr, aspect-ratio 1:1) — матрица растягивается
                по ширине карточки; потолок размера ячейки задаёт max-width. */}
            <div
              className="cal"
              role="group"
              aria-label={tr('cal.aria')}
              style={{
                gridTemplateColumns: `34px repeat(${weeks.length}, minmax(10px, 1fr))`,
                // Потолок ячейки ~42px: короткий период (месяц, квартал) даёт
                // крупные квадраты, годовая матрица растягивается на всю карточку.
                maxWidth: 34 + weeks.length * 45,
              }}
            >
              <div aria-hidden="true" />
              {monthLabels.map((label, i) => (
                <div className="cal-month" key={`m-${i}`} aria-hidden="true">
                  {label}
                </div>
              ))}
              {weekdayLabels().map((label, wd) => (
                <Fragment key={`row-${wd}`}>
                  <div className="cal-weekday" aria-hidden="true">
                    {label}
                  </div>
                  {weeks.map((week, wi) => {
                    const day = week[wd]
                    if (!day) return <div key={`e-${wi}`} />
                    const clickable = day.count > 0
                    return (
                      <div
                        key={day.key}
                        className={clickable ? 'cal-cell cal-cell-active' : 'cal-cell'}
                        style={cellStyle(day)}
                        role={clickable ? 'button' : undefined}
                        tabIndex={clickable ? 0 : undefined}
                        aria-label={
                          clickable
                            ? `${dayFmt.format(day.date)} — ${fmtNumber(day.count)} ${tp('plural.events', day.count)}`
                            : undefined
                        }
                        title={`${dayFmt.format(day.date)}${day.kindLabel ? ` (${day.kindLabel})` : ''}${day.overtime ? ` · ${tr('cal.overtime')}` : ''}${day.hired ? ` · ${tr('cal.hired')}` : ''}${day.prehire ? ` · ${tr('cal.prehire')}` : ''} — ${fmtNumber(
                          day.count,
                        )} ${tp('plural.events', day.count)}`}
                        onClick={clickable ? () => setSelected(day) : undefined}
                        onKeyDown={
                          clickable
                            ? (e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                  e.preventDefault()
                                  setSelected(day)
                                }
                              }
                            : undefined
                        }
                        onMouseMove={(e) => {
                          const rect = e.currentTarget.getBoundingClientRect()
                          const left = Math.max(
                            8,
                            Math.min(rect.left + rect.width / 2 - 90, window.innerWidth - 226),
                          )
                          // Ячейка в верхней половине экрана — тултип под ней
                          // (растёт вниз), иначе над ней: длинная почасовая
                          // разбивка всегда помещается во вьюпорт.
                          const below = rect.top < window.innerHeight * 0.55
                          setHover({
                            key: day.key,
                            label: dayFmt.format(day.date),
                            sub:
                              [
                                day.kindLabel,
                                day.shallow ? tr('cal.shallow') : '',
                                day.hired ? tr('cal.hired') : '',
                                day.prehire ? tr('cal.prehire') : '',
                              ]
                                .filter(Boolean)
                                .join(' · ') || undefined,
                            count: day.count,
                            left,
                            ...(below
                              ? { top: rect.bottom + 6 }
                              : { bottom: window.innerHeight - rect.top + 6 }),
                          })
                        }}
                      >
                        {day.overtime && <span className="cal-ot-dot" aria-hidden="true" />}
                        {day.hired && <span className="cal-hire-flag" aria-hidden="true" />}
                      </div>
                    )
                  })}
                </Fragment>
              ))}
            </div>
          </div>

          <div className="heatmap-scale" style={{ gap: 12 }}>
            <span className="cal-kind">
              <span
                className="heatmap-scale-step"
                style={{ background: 'var(--surface-sunken)' }}
                aria-hidden="true"
              />
              {kinds.weekend}
            </span>
            <span className="cal-kind">
              <span
                className="heatmap-scale-step"
                style={{ boxShadow: `inset 0 0 0 2px ${t.slot(2)}` }}
                aria-hidden="true"
              />
              {kinds.holiday}
            </span>
            <span className="cal-kind">
              <span
                className="heatmap-scale-step"
                style={{ boxShadow: `inset 0 0 0 2px ${t.slot(6)}` }}
                aria-hidden="true"
              />
              {kinds.vacation}
            </span>
            <span className="cal-kind">
              <span
                className="heatmap-scale-step"
                style={{ boxShadow: `inset 0 0 0 2px ${t.slot(7)}` }}
                aria-hidden="true"
              />
              {kinds.sick}
            </span>
            <span className="cal-kind">
              <span
                className="heatmap-scale-step"
                style={{ outline: '2px dashed #eda100', outlineOffset: -2 }}
                aria-hidden="true"
              />
              {kinds.remote}
            </span>
            <span className="cal-kind" title={tr('cal.shallowHint')}>
              <span
                className="heatmap-scale-step"
                style={{ background: SEQUENTIAL_SHALLOW[5] }}
                aria-hidden="true"
              />
              {tr('cal.shallow')}
            </span>
            {hireDate && (
              <>
                <span className="cal-kind">
                  <span
                    className="heatmap-scale-step cal-hire-legend"
                    style={{ outline: '2px solid #d6219c', outlineOffset: -1 }}
                    aria-hidden="true"
                  />
                  {tr('cal.hired')}
                </span>
                <span className="cal-kind">
                  <span
                    className="heatmap-scale-step"
                    style={{
                      background:
                        'repeating-linear-gradient(135deg, var(--surface-sunken) 0 4px, transparent 4px 8px)',
                    }}
                    aria-hidden="true"
                  />
                  {tr('cal.prehire')}
                </span>
              </>
            )}
          </div>

          <div className="heatmap-scale">
            <span>{tr('chart.less')}</span>
            <div className="heatmap-scale-steps" aria-hidden="true">
              <span className="heatmap-scale-step" style={{ background: t.surface }} />
              {SEQUENTIAL.filter((_, i) => i % 2 === 0).map((c) => (
                <span className="heatmap-scale-step" key={c} style={{ background: c }} />
              ))}
            </div>
            <span>{tr('chart.more')}</span>
            <span className="num" style={{ marginLeft: 'auto' }}>
              {tr('cal.max', { n: fmtNumber(max), ev: tp('plural.events', max) })}
            </span>
          </div>

          {hover && (
            <div
              style={{
                position: 'fixed',
                left: hover.left,
                ...(hover.top != null ? { top: hover.top } : { bottom: hover.bottom }),
                maxHeight: 'calc(100vh - 16px)',
                overflow: 'hidden',
                pointerEvents: 'none',
                zIndex: 50,
              }}
            >
              <div className="chart-tooltip">
                <div className="chart-tooltip-title">
                  {hover.sub ? `${hover.label} · ${hover.sub}` : hover.label}
                </div>
                <div className="chart-tooltip-row">
                  <span className="dot" style={{ background: colorFor(hover.count) }} aria-hidden="true" />
                  <span className="chart-tooltip-name">{tr('chart.events')}</span>
                  <span className="chart-tooltip-value num">{fmtNumber(hover.count)}</span>
                </div>
                {hover.count > 0 && breakdownReady && hourRows.length > 0 && (
                  <div className="tt-hours">
                    {hourRows.map((row, i) => (
                      <div className="tt-hour-row" key={i}>
                        <span className="tt-hour num">{row.hourLabel}</span>
                        <span className="dot" style={{ background: row.color }} aria-hidden="true" />
                        <span className="chart-tooltip-name">{row.name}</span>
                        <span className="chart-tooltip-value num">{fmtNumber(row.count)}</span>
                      </div>
                    ))}
                    {hourRowsRest > 0 && (
                      <div className="tt-hour-row tt-more">{tr('cal.moreRows', { n: fmtNumber(hourRowsRest) })}</div>
                    )}
                  </div>
                )}
                {hover.count > 0 && breakdownLoading && (
                  <div className="tt-hours">
                    <div className="tt-hour-row tt-more">{tr('ui.loading')}…</div>
                  </div>
                )}
                {hover.count > 0 && <div className="tt-hint">{tr('cal.clickHint')}</div>}
              </div>
            </div>
          )}

          {selected && (
            <DayDetail
              date={selected.date}
              kindLabel={selected.kindLabel || undefined}
              onClose={() => setSelected(null)}
            />
          )}
        </div>
      )}
    </ChartCard>
  )
}
