import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { SyncEta } from './SyncEta'
import { api } from '../lib/api'
import { fmtDateTime } from '../lib/format'
import { useT } from '../i18n'
import type { SyncQueueResponse } from '../lib/types'

export function useSyncQueue() {
  return useQuery({
    queryKey: ['sync', 'queue'] as const,
    queryFn: api.syncQueue,
    // Очередь живая: пока в ней что-то есть — опрашиваем каждые 10 секунд.
    refetchInterval: (q) => {
      const d = q.state.data
      return d && d.running.length + d.pending.length > 0 ? 10_000 : 30_000
    },
  })
}

function fmtDurationShort(ms: number): string {
  const min = Math.round(ms / 60000)
  if (min < 1) return '<1 мин'
  if (min < 60) return `${min} мин`
  const h = Math.floor(min / 60)
  return `${h} ч ${min % 60} мин`
}

/** Оценка времени до осушения очереди: pending покрывается пулом воркеров по
 * средней длительности прогона, плюс остаток самого свежего активного. */
export function queueEta(d: SyncQueueResponse): number | null {
  if (!d.avg_run_ms) return null
  const workers = Math.max(1, d.workers)
  let runningRemainder = 0
  for (const run of d.running) {
    const elapsed = Date.now() - new Date(run.started_at).getTime()
    runningRemainder = Math.max(runningRemainder, d.avg_run_ms - elapsed)
  }
  return Math.max(0, runningRemainder) + Math.ceil(d.pending.length / workers) * d.avg_run_ms
}

/** Компактный индикатор очереди для шапки: N прогонов · ~время. */
export function QueueBadge() {
  const { t } = useT()
  const q = useSyncQueue()
  const d = q.data
  if (!d) return null
  const total = d.running.length + d.pending.length
  if (total === 0) return null
  const eta = queueEta(d)
  return (
    <Link
      className="badge badge-status badge-running"
      to="/settings"
      title={t('queue.badgeHint')}
    >
      {t('queue.badge', { n: total })}
      {eta !== null && ` · ~${fmtDurationShort(eta)}`}
    </Link>
  )
}

/** Карточка очереди сбора: активные прогоны, ожидающие и итоговая оценка. */
export function SyncQueueCard() {
  const { t, tp } = useT()
  const q = useSyncQueue()
  const d = q.data
  if (!d) return null
  const total = d.running.length + d.pending.length
  if (total === 0) return null
  const eta = queueEta(d)

  return (
    <section className="card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('queue.title')}</h2>
          <div className="chart-card-subtitle">
            {t('queue.summary', { total })}{' '}
            {eta !== null && (
              <>
                · {t('eta.remaining')} ~{fmtDurationShort(eta)}
              </>
            )}
            {d.avg_run_ms > 0 && (
              <> · {t('queue.avgRun', { m: Math.round(d.avg_run_ms / 60000) })}</>
            )}
          </div>
        </div>
      </header>
      <div className="panel-body stack" style={{ gap: 10 }}>
        {d.running.map((run) => (
          <div key={run.id} className="inline-group">
            <span className="badge badge-status badge-running">{t('queue.nowRunning')}</span>
            <strong>{run.person_key}</strong>
            <SyncEta run={run} compact />
            {Object.values(run.sources ?? {})
              .filter((s) => s.status === 'running' && s.note)
              .map((s) => (
                <span key={s.source} className="muted num">
                  {s.source}: {s.note}
                </span>
              ))}
          </div>
        ))}
        {d.pending.length > 0 && (
          <div className="stack" style={{ gap: 4 }}>
            <span className="field-label">
              {t('queue.waiting')} · {d.pending.length} {tp('plural.syncs', d.pending.length)}
            </span>
            <div className="chips">
              {d.pending.map((run, i) => (
                <span key={run.id} className="chip" title={fmtDateTime(run.started_at)}>
                  <span className="num muted">{i + 1}.</span> {run.person_key}
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
    </section>
  )
}
