import { pick } from '../i18n/lang'

// Форматтеры зависят от языка интерфейса, поэтому создаются лениво (при первом
// вызове) и кэшируются по локали — замораживать их в константах нельзя.
function locale(): string {
  return pick('ru-RU', 'en-US')
}

function memoFmt<T>(create: (loc: string) => T): () => T {
  let cached: T | null = null
  let cachedLocale = ''
  return () => {
    const loc = locale()
    if (cached === null || cachedLocale !== loc) {
      cached = create(loc)
      cachedLocale = loc
    }
    return cached
  }
}

const numberFmt = memoFmt((loc) => new Intl.NumberFormat(loc))
const decimalFmt = memoFmt((loc) => new Intl.NumberFormat(loc, { maximumFractionDigits: 1 }))
const percentFmt = memoFmt(
  (loc) => new Intl.NumberFormat(loc, { maximumFractionDigits: 1, signDisplay: 'always' }),
)

const dateFmt = memoFmt(
  (loc) => new Intl.DateTimeFormat(loc, { day: '2-digit', month: '2-digit', year: 'numeric' }),
)
const dateShortFmt = memoFmt((loc) => new Intl.DateTimeFormat(loc, { day: '2-digit', month: 'short' }))
const dateTimeFmt = memoFmt(
  (loc) =>
    new Intl.DateTimeFormat(loc, {
      day: '2-digit',
      month: '2-digit',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }),
)
const timeFmt = memoFmt((loc) => new Intl.DateTimeFormat(loc, { hour: '2-digit', minute: '2-digit' }))
const monthFmt = memoFmt((loc) => new Intl.DateTimeFormat(loc, { month: 'long', year: 'numeric' }))

export function fmtNumber(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  return numberFmt().format(value)
}

export function fmtDecimal(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  return decimalFmt().format(value)
}

/**
 * Процент изменения к прошлому периоду. Когда база близка к нулю, обычный
 * процент вырождается в нечитаемое «+241 800 %», поэтому от 10-кратного роста
 * переключаемся на кратность — она честнее отражает порядок величины.
 */
export function fmtPercent(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) return '—'
  if (!Number.isFinite(value)) return '—'
  if (value >= 900) return `×${decimalFmt().format(value / 100 + 1)}`
  if (value <= -99.5) return '−100%'
  return `${percentFmt().format(value)}%`
}

function toDate(value: string | undefined | null): Date | null {
  if (!value) return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

export function fmtDate(value: string | undefined | null): string {
  const d = toDate(value)
  return d ? dateFmt().format(d) : '—'
}

export function fmtDateShort(value: string | undefined | null): string {
  const d = toDate(value)
  return d ? dateShortFmt().format(d) : '—'
}

export function fmtDateTime(value: string | undefined | null): string {
  const d = toDate(value)
  return d ? dateTimeFmt().format(d) : '—'
}

export function fmtTime(value: string | undefined | null): string {
  const d = toDate(value)
  return d ? timeFmt().format(d) : '—'
}

export function fmtMonth(value: string | undefined | null): string {
  const d = toDate(value)
  return d ? monthFmt().format(d) : '—'
}

export function fmtDuration(ms: number | undefined | null): string {
  if (ms === undefined || ms === null) return '—'
  if (ms < 1000) return `${fmtNumber(ms)} ${pick('мс', 'ms')}`
  const seconds = ms / 1000
  if (seconds < 60) return `${fmtDecimal(seconds)} ${pick('с', 's')}`
  const minutes = Math.floor(seconds / 60)
  const rest = Math.round(seconds % 60)
  return `${minutes} ${pick('мин', 'min')} ${rest} ${pick('с', 's')}`
}

/** Подпись бакета таймлайна с учётом гранулярности. */
export function fmtBucket(bucket: string, granularity: string): string {
  const d = toDate(bucket)
  if (!d) return bucket
  switch (granularity) {
    case 'hour':
      return `${dateShortFmt().format(d)}, ${timeFmt().format(d)}`
    case 'month':
      return monthFmt().format(d)
    case 'week':
      return `${pick('нед.', 'wk')} ${dateShortFmt().format(d)}`
    default:
      return dateShortFmt().format(d)
  }
}

/** Дата → `YYYY-MM-DD` для <input type="date"> (в локальном времени). */
export function toDateInput(value: string | Date | undefined | null): string {
  const d = value instanceof Date ? value : toDate(value ?? null)
  if (!d) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** `YYYY-MM-DD` → RFC3339 начало/конец дня в локальной зоне. */
export function fromDateInput(value: string, edge: 'start' | 'end'): string {
  const [y, m, d] = value.split('-').map(Number)
  if (!y || !m || !d) return ''
  const date =
    edge === 'start' ? new Date(y, m - 1, d, 0, 0, 0, 0) : new Date(y, m - 1, d, 23, 59, 59, 999)
  return date.toISOString()
}

export function plural(n: number, one: string, few: string, many: string): string {
  const mod10 = n % 10
  const mod100 = n % 100
  if (mod10 === 1 && mod100 !== 11) return one
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) return few
  return many
}
