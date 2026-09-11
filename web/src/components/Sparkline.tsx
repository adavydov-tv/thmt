import { useMemo } from 'react'
import { useT } from '../i18n'
import type { SparkPoint } from '../lib/types'

/**
 * Разворачивает точки таймлайна в непрерывный ряд значений с нулями в пустых
 * корзинах — иначе спарклайн «склеивает» простои и волны активности врут.
 */
export function zeroFillSeries(
  points: SparkPoint[] | null | undefined,
  from: string,
  to: string,
  granularity: 'day' | 'week',
): number[] {
  const byKey = new Map<string, number>()
  for (const p of points ?? []) {
    byKey.set(p.bucket.slice(0, 10), (byKey.get(p.bucket.slice(0, 10)) ?? 0) + p.count)
  }
  const start = new Date(from)
  const end = new Date(to)
  start.setHours(0, 0, 0, 0)
  if (granularity === 'week') {
    // date_trunc('week') — понедельник; двигаем старт к понедельнику.
    start.setDate(start.getDate() - ((start.getDay() + 6) % 7))
  }
  const stepDays = granularity === 'week' ? 7 : 1
  const out: number[] = []
  const pad = (n: number) => String(n).padStart(2, '0')
  for (let d = new Date(start); d.getTime() < end.getTime(); d.setDate(d.getDate() + stepDays)) {
    const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
    out.push(byKey.get(key) ?? 0)
    if (out.length > 800) break
  }
  return out
}

/**
 * Тренд активности: вторая половина ряда против первой, в процентах.
 * null — тренд не определён (в первой половине пусто).
 */
export function seriesTrend(values: number[]): number | null {
  if (values.length < 4) return null
  const mid = Math.floor(values.length / 2)
  const first = values.slice(0, mid).reduce((a, b) => a + b, 0)
  const second = values.slice(mid).reduce((a, b) => a + b, 0)
  if (first === 0) return null
  return Math.round(((second - first) / first) * 100)
}

interface SparklineProps {
  values: number[]
  color: string
  height?: number
  ariaLabel?: string
}

/** Мини-график «волн активности»: линия с заливкой, без осей. */
export function Sparkline({ values, color, height = 48, ariaLabel }: SparklineProps) {
  const { t } = useT()
  const { line, area } = useMemo(() => {
    const n = values.length
    if (n === 0) return { line: '', area: '' }
    const max = Math.max(...values, 1)
    const W = 100
    const H = 100
    const x = (i: number) => (n === 1 ? W / 2 : (i / (n - 1)) * W)
    // 4% сверху, чтобы пик не срезался обводкой.
    const y = (v: number) => H - (v / max) * (H - 4)
    const pts = values.map((v, i) => `${x(i).toFixed(2)},${y(v).toFixed(2)}`)
    return {
      line: pts.join(' '),
      area: `M0,${H} L${pts.join(' L')} L${W},${H} Z`,
    }
  }, [values])

  if (!line) return null

  return (
    <svg
      className="spark"
      viewBox="0 0 100 100"
      preserveAspectRatio="none"
      style={{ width: '100%', height, display: 'block' }}
      role="img"
      aria-label={ariaLabel ?? t('chart.sparkAria')}
    >
      <path d={area} fill={color} fillOpacity={0.14} stroke="none" />
      <polyline
        points={line}
        fill="none"
        stroke={color}
        strokeWidth={2}
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}
