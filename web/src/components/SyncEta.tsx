import { useEffect, useState } from 'react'
import { useT } from '../i18n'
import type { SyncRun } from '../lib/types'

/** Прогресс прогона 0..1: завершённые источники + доля текущего из note
 * («history 405/1325» пишет прогресс-колбэк коллектора). */
function runProgress(run: SyncRun): number {
  const entries = Object.values(run.sources ?? {})
  if (entries.length === 0) return 0
  let sum = 0
  for (const s of entries) {
    if (s.status !== 'running' && s.status !== 'pending') {
      sum += 1
      continue
    }
    const m = /(\d+)\s*\/\s*(\d+)/.exec(s.note ?? '')
    if (m) {
      const done = Number(m[1])
      const total = Number(m[2])
      if (total > 0) sum += Math.min(1, done / total)
    }
  }
  return sum / entries.length
}

function fmtClock(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000))
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  const mm = String(m).padStart(2, '0')
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`
}

/**
 * Прошедшее время сбора и оценка оставшегося: тикает раз в секунду, остаток
 * экстраполируется из прогресса (elapsed × (1−p)/p) и показывается, когда
 * прогресс достаточно надёжен (>5%).
 */
export function SyncEta({ run, compact = false }: { run: SyncRun; compact?: boolean }) {
  const { t } = useT()
  const [now, setNow] = useState(() => Date.now())

  const active = run.status === 'running' || run.status === 'pending'
  useEffect(() => {
    if (!active) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [active])

  const started = new Date(run.started_at).getTime()
  if (!Number.isFinite(started)) return null
  const end = run.finished_at ? new Date(run.finished_at).getTime() : now
  const elapsed = Math.max(0, end - started)

  if (!active) {
    return (
      <span className="num muted" title={t('eta.elapsedHint')}>
        {t('eta.took')} {fmtClock(elapsed)}
      </span>
    )
  }

  const p = runProgress(run)
  const eta = p > 0.05 ? (elapsed * (1 - p)) / p : null
  const pct = p > 0 ? ` · ${Math.round(p * 100)}%` : ''

  if (compact) {
    return (
      <span className="num muted" title={t('eta.hint')}>
        {fmtClock(elapsed)}
        {eta !== null ? ` · ~${fmtClock(eta)}` : ''}
      </span>
    )
  }
  return (
    <span className="num muted" title={t('eta.hint')}>
      {t('eta.elapsed')} {fmtClock(elapsed)}
      {eta !== null ? ` · ${t('eta.remaining')} ~${fmtClock(eta)}` : ` · ${t('eta.estimating')}`}
      {pct}
    </span>
  )
}
