// Календарные периоды для фильтра «Период»: день, неделя, месяц, квартал, год.
// Всё считается в локальной зоне пользователя; границы — начало/конец дня.

import { pick } from '../i18n/lang'

export type PeriodKind = 'custom' | 'day' | 'week' | 'month' | 'quarter' | 'year'

export const PERIOD_KINDS: PeriodKind[] = ['custom', 'day', 'week', 'month', 'quarter', 'year']

/** Подпись вида периода — по текущему языку (нельзя замораживать в константе). */
export function periodKindLabel(kind: PeriodKind): string {
  const labels: Record<PeriodKind, string> = pick(
    { custom: 'Произвольный', day: 'День', week: 'Неделя', month: 'Месяц', quarter: 'Квартал', year: 'Год' },
    { custom: 'Custom', day: 'Day', week: 'Week', month: 'Month', quarter: 'Quarter', year: 'Year' },
  )
  return labels[kind]
}

export interface PeriodOption {
  key: string
  label: string
  from: string // RFC3339, начало периода
  to: string // RFC3339, конец периода (не позже текущего момента)
}

// Форматтеры зависят от языка, поэтому создаются лениво и кэшируются по локали.
function locale(): string {
  return pick('ru-RU', 'en-US')
}

function memoFmt(options: Intl.DateTimeFormatOptions): () => Intl.DateTimeFormat {
  let cached: Intl.DateTimeFormat | null = null
  let cachedLocale = ''
  return () => {
    const loc = locale()
    if (!cached || cachedLocale !== loc) {
      cached = new Intl.DateTimeFormat(loc, options)
      cachedLocale = loc
    }
    return cached
  }
}

const dayFmt = memoFmt({ day: 'numeric', month: 'short', year: 'numeric' })
const dayShortFmt = memoFmt({ day: 'numeric', month: 'short' })
const monthFmt = memoFmt({ month: 'long', year: 'numeric' })

function startOfDay(d: Date): Date {
  const x = new Date(d)
  x.setHours(0, 0, 0, 0)
  return x
}

function endOfDay(d: Date): Date {
  const x = new Date(d)
  x.setHours(23, 59, 59, 999)
  return x
}

/** Понедельник недели, в которую попадает дата. */
function startOfWeek(d: Date): Date {
  const x = startOfDay(d)
  const shift = (x.getDay() + 6) % 7 // 0 = понедельник
  x.setDate(x.getDate() - shift)
  return x
}

function clampToNow(d: Date): Date {
  const now = new Date()
  return d.getTime() > now.getTime() ? now : d
}

function option(key: string, label: string, from: Date, to: Date): PeriodOption {
  return { key, label, from: from.toISOString(), to: clampToNow(to).toISOString() }
}

/**
 * Последние count периодов заданного вида, от текущего к прошлым.
 * Текущий (незавершённый) период включается и обрезается сегодняшним днём.
 */
export function periodOptions(kind: PeriodKind, count = 12): PeriodOption[] {
  const now = new Date()
  const out: PeriodOption[] = []

  for (let i = 0; i < count; i++) {
    switch (kind) {
      case 'day': {
        const d = new Date(now)
        d.setDate(d.getDate() - i)
        const from = startOfDay(d)
        out.push(option(`d${i}`, dayFmt().format(from), from, endOfDay(d)))
        break
      }
      case 'week': {
        const from = startOfWeek(now)
        from.setDate(from.getDate() - i * 7)
        const to = new Date(from)
        to.setDate(to.getDate() + 6)
        out.push(
          option(`w${i}`, `${dayShortFmt().format(from)} — ${dayFmt().format(to)}`, from, endOfDay(to)),
        )
        break
      }
      case 'month': {
        const from = new Date(now.getFullYear(), now.getMonth() - i, 1)
        const to = new Date(from.getFullYear(), from.getMonth() + 1, 0)
        out.push(option(`m${i}`, monthFmt().format(from), from, endOfDay(to)))
        break
      }
      case 'quarter': {
        const q = Math.floor(now.getMonth() / 3) - i
        const from = new Date(now.getFullYear(), q * 3, 1)
        const to = new Date(from.getFullYear(), from.getMonth() + 3, 0)
        const qn = Math.floor(from.getMonth() / 3) + 1
        out.push(option(`q${i}`, `Q${qn} ${from.getFullYear()}`, from, endOfDay(to)))
        break
      }
      case 'year': {
        const from = new Date(now.getFullYear() - i, 0, 1)
        const to = new Date(from.getFullYear(), 11, 31)
        out.push(option(`y${i}`, String(from.getFullYear()), from, endOfDay(to)))
        break
      }
      default:
        return out
    }
  }
  return out
}

/** Ключ дня `YYYY-MM-DD` в локальной зоне — для сравнения границ периодов. */
function dayKey(iso: string): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/**
 * Пытается опознать выбранный диапазон как один из календарных периодов —
 * чтобы селекторы показывали актуальное состояние после перехода по ссылке.
 */
export function detectPeriod(
  from: string,
  to: string,
  count = 12,
): { kind: PeriodKind; key: string } | null {
  for (const kind of PERIOD_KINDS) {
    if (kind === 'custom') continue
    for (const opt of periodOptions(kind, count)) {
      if (dayKey(opt.from) === dayKey(from) && dayKey(opt.to) === dayKey(to)) {
        return { kind, key: opt.key }
      }
    }
  }
  return null
}
