import { useState } from 'react'
import { ErrorState } from './ErrorState'
import { PeriodPicker } from './PeriodPicker'
import { SyncEta } from './SyncEta'
import { SkeletonLines } from './Skeleton'
import { SourceChips } from './SourceChips'
import { useSyncRuns } from '../lib/queries'
import { useSync } from '../lib/syncContext'
import { useFilters } from '../lib/useFilters'
import { fmtDateTime, fmtDuration, fmtNumber, toDateInput, fromDateInput } from '../lib/format'
import { sourceLabel } from '../lib/chartTheme'
import { useT } from '../i18n'
import type { MetaSource, SyncRun, SyncStatus } from '../lib/types'

function statusClass(status: string): string {
  if (status === 'done') return 'badge badge-status badge-done'
  if (status === 'failed') return 'badge badge-status badge-failed'
  if (status === 'running' || status === 'pending') return 'badge badge-status badge-running'
  return 'badge badge-status'
}

function useStatusLabels(): Record<SyncStatus, string> {
  const { t } = useT()
  return {
    pending: t('sync.statusPending'),
    running: t('sync.statusRunning'),
    done: t('sync.statusDone'),
    failed: t('sync.statusFailed'),
    partial: t('sync.statusPartial'),
  }
}

/** Форма запуска сбора по выбранному человеку + карточка активного прогона. */
export function SyncStartCard({
  sourceOptions,
  personKey,
}: {
  sourceOptions?: MetaSource[]
  personKey?: string
}) {
  const { filters } = useFilters()
  const { t } = useT()
  const sync = useSync()
  const statusLabels = useStatusLabels()
  const person = personKey ?? filters.person

  const [from, setFrom] = useState(toDateInput(filters.from))
  const [to, setTo] = useState(toDateInput(filters.to))
  const [selectedSources, setSelectedSources] = useState<string[]>([])
  const [overwrite, setOverwrite] = useState(false)

  const active = sync.activeRun
  const running = active?.status === 'running' || active?.status === 'pending'

  const submit = () => {
    if (!person) return
    sync.start({
      person_key: person,
      from: fromDateInput(from, 'start'),
      to: fromDateInput(to, 'end'),
      sources: selectedSources.length ? selectedSources : undefined,
      overwrite: overwrite || undefined,
    })
  }

  return (
    <div className="stack">
      <section className="card">
        <header className="chart-card-head">
          <div className="chart-card-titles">
            <h2>{t('sync.title')}</h2>
            <div className="chart-card-subtitle">{t('sync.subtitle')}</div>
          </div>
        </header>
        <div className="panel-body stack">
          <div className="field">
            <span className="field-label">{t('sync.period')}</span>
            {/* Быстрый выбор календарного периода: «Квартал → Q2 2026» сразу
                заполняет обе даты — удобно загружать целый квартал или год. */}
            <PeriodPicker
              compact
              from={fromDateInput(from, 'start')}
              to={fromDateInput(to, 'end')}
              onChange={({ from: f, to: tt }) => {
                setFrom(toDateInput(f))
                setTo(toDateInput(tt))
              }}
            />
          </div>
          <div className="form-grid">
            <div className="field">
              <label htmlFor="sync-from">{t('sync.from')}</label>
              <input
                id="sync-from"
                className="input"
                type="date"
                value={from}
                max={to}
                onChange={(e) => setFrom(e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="sync-to">{t('sync.to')}</label>
              <input
                id="sync-to"
                className="input"
                type="date"
                value={to}
                min={from}
                onChange={(e) => setTo(e.target.value)}
              />
            </div>
          </div>

          <SourceChips options={sourceOptions} selected={selectedSources} onChange={setSelectedSources} />

          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
            <input type="checkbox" checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
            <span>{t('sync.overwrite')}</span>
            <span className="muted" style={{ fontSize: 12 }}>
              {t('sync.overwriteHint')}
            </span>
          </label>

          <div className="inline-group">
            <button
              type="button"
              className="btn btn-primary"
              onClick={submit}
              disabled={!person || sync.isStarting || running}
            >
              {sync.isStarting ? t('sync.starting') : t('sync.start')}
            </button>
            {!person && <span className="muted">{t('sync.pickPersonFirst')}</span>}
          </div>

          {sync.error && <div className="error-text">{sync.error.message}</div>}
        </div>
      </section>

      {active && (
        <section className="card">
          <header className="chart-card-head">
            <div className="chart-card-titles">
              <h2>{t('sync.currentRun')}</h2>
              <div className="chart-card-subtitle mono">{active.id}</div>
            </div>
            <div className="chart-card-actions">
              <span className={statusClass(active.status)}>
                {statusLabels[active.status] ?? active.status}
              </span>
              {running ? (
                <button
                  type="button"
                  className="btn btn-sm"
                  onClick={sync.cancel}
                  disabled={sync.isCancelling}
                >
                  {sync.isCancelling ? t('header.stopping') : t('header.stop')}
                </button>
              ) : (
                <button type="button" className="btn btn-sm" onClick={sync.dismiss}>
                  {t('sync.hide')}
                </button>
              )}
            </div>
          </header>
          <div className="panel-body stack">
            {running && (
              <div className="progress" role="progressbar" aria-label={t('sync.progressAria')}>
                <div className="progress-bar" style={{ width: `${progressPercent(active)}%` }} />
              </div>
            )}
            <SyncEta run={active} />
            <RunSources run={active} />
            {active.error && <div className="error-text">{active.error}</div>}
          </div>
        </section>
      )}
    </div>
  )
}

/** История прогонов выбранного человека. */
export function SyncHistoryCard() {
  const { filters } = useFilters()
  const { t } = useT()
  const runs = useSyncRuns(filters.person)
  const statusLabels = useStatusLabels()

  return (
    <section className="card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('sync.historyTitle')}</h2>
          <div className="chart-card-subtitle">{t('sync.historyHint')}</div>
        </div>
      </header>
      <div className="panel-body">
        {runs.isError ? (
          <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
        ) : runs.isLoading ? (
          <SkeletonLines count={3} height={24} />
        ) : !runs.data || runs.data.length === 0 ? (
          <p className="muted">{t('sync.noRuns')}</p>
        ) : (
          <div className="stack" style={{ gap: 12 }}>
            {runs.data.map((run) => (
              <div key={run.id} className="stack" style={{ gap: 6 }}>
                <div className="inline-group">
                  <span className={statusClass(run.status)}>
                    {statusLabels[run.status] ?? run.status}
                  </span>
                  <span className="num">{fmtDateTime(run.started_at)}</span>
                  <span className="muted num">
                    {t('sync.rangeLabel')} {fmtDateTime(run.from)} — {fmtDateTime(run.to)}
                  </span>
                  <SyncEta run={run} compact />
                  <span className="muted mono">{run.id}</span>
                </div>
                <RunSources run={run} />
                {run.error && <div className="error-text">{run.error}</div>}
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

/** Совместимость: старый компонент — форма + история. */
export function SyncPanel({ sourceOptions }: { sourceOptions?: MetaSource[] }) {
  return (
    <div className="stack">
      <SyncStartCard sourceOptions={sourceOptions} />
      <SyncHistoryCard />
    </div>
  )
}

function progressPercent(run: SyncRun): number {
  const entries = Object.values(run.sources ?? {})
  if (entries.length === 0) return 10
  const finished = entries.filter((s) => s.status !== 'running' && s.status !== 'pending').length
  return Math.max(8, Math.round((finished / entries.length) * 100))
}

function RunSources({ run }: { run: SyncRun }) {
  const { t, tp } = useT()
  const entries = Object.entries(run.sources ?? {})
  if (entries.length === 0) return <p className="muted">{t('sync.noSourceData')}</p>
  return (
    <div>
      {entries.map(([key, s]) => (
        <div className="sync-source-row" key={key}>
          <span className={statusClass(s.status)}>{s.status}</span>
          <strong>{sourceLabel(s.source || key)}</strong>
          <span className="num muted">
            {fmtNumber(s.events)} {tp('plural.eventsShort', s.events)}
          </span>
          <span className="num muted">{fmtDuration(s.duration_ms)}</span>
          {s.note && <span className="muted">{s.note}</span>}
          {s.error && <span className="error-text">{s.error}</span>}
        </div>
      ))}
    </div>
  )
}
