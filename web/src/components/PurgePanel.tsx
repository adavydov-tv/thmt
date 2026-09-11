import { useState } from 'react'
import { ErrorState } from './ErrorState'
import { SourceChips } from './SourceChips'
import { usePeople, usePurge, usePurgePreview } from '../lib/queries'
import { useFilters } from '../lib/useFilters'
import { fmtDate, fmtNumber, fromDateInput, toDateInput } from '../lib/format'
import { sourceLabel } from '../lib/chartTheme'
import { useT } from '../i18n'
import type { MetaSource, PurgePreviewQuery, PurgeResult } from '../lib/types'

/**
 * Удаление собранных данных за период. Всегда в два шага: сначала preview
 * (сколько и чего исчезнет), и только потом — само удаление с inline-подтверждением.
 * Любое изменение формы обесценивает посчитанный preview, чтобы нельзя было
 * удалить не то, что показано на экране.
 */
export function PurgePanel({ sourceOptions }: { sourceOptions?: MetaSource[] }) {
  const { t, tp } = useT()
  const { filters } = useFilters()
  const people = usePeople()
  const preview = usePurgePreview()
  const purge = usePurge()

  const [personKey, setPersonKey] = useState(filters.person)
  const [from, setFrom] = useState(toDateInput(filters.from))
  const [to, setTo] = useState(toDateInput(filters.to))
  const [sources, setSources] = useState<string[]>([])
  const [syncRuns, setSyncRuns] = useState(false)
  const [resetDocLinks, setResetDocLinks] = useState(true)

  const [plan, setPlan] = useState<PurgeResult | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [done, setDone] = useState<PurgeResult | null>(null)

  const busy = preview.isPending || purge.isPending

  /** Сброс посчитанного плана — вызывается при любом изменении параметров. */
  const invalidatePlan = () => {
    setPlan(null)
    setConfirming(false)
    setDone(null)
    preview.reset()
    purge.reset()
  }

  const params = (): PurgePreviewQuery => ({
    person_key: personKey,
    from: fromDateInput(from, 'start'),
    to: fromDateInput(to, 'end'),
    sources: sources.length ? sources : undefined,
    sync_runs: syncRuns,
  })

  const canCount = Boolean(personKey) && Boolean(from) && Boolean(to) && !busy

  const runPreview = () => {
    if (!canCount) return
    setPlan(null)
    setConfirming(false)
    setDone(null)
    purge.reset()
    preview.mutate(params(), { onSuccess: (res) => setPlan(res) })
  }

  const runPurge = () => {
    if (!plan || busy) return
    purge.mutate(
      { ...params(), sync_runs: syncRuns, reset_doc_links: resetDocLinks, confirm: true },
      {
        onSuccess: (res) => {
          // Preview сбрасывается: повторное удаление по устаревшим числам
          // должно быть невозможно.
          setDone(res)
          setPlan(null)
          setConfirming(false)
          preview.reset()
        },
      },
    )
  }

  const nothingToDelete = plan !== null && plan.events === 0 && plan.sync_runs === 0
  const period = plan ? `${fmtDate(plan.from)} — ${fmtDate(plan.to)}` : ''
  // Обычно удаляются события; если их нет, но выбраны прогоны — считаем прогоны.
  const planLabel = !plan
    ? ''
    : plan.events > 0
      ? `${fmtNumber(plan.events)} ${tp('purge.pluralEvents', plan.events)}`
      : `${fmtNumber(plan.sync_runs)} ${tp('purge.pluralRuns', plan.sync_runs)}`

  return (
    <section className="card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('purge.title')}</h2>
          <div className="chart-card-subtitle">{t('purge.subtitle')}</div>
        </div>
      </header>

      <div className="panel-body stack">
        <div className="form-grid">
          <div className="field">
            <label htmlFor="purge-person">{t('purge.person')}</label>
            <select
              id="purge-person"
              className="select"
              value={personKey}
              disabled={people.isLoading || !people.data || people.data.length === 0}
              onChange={(e) => {
                setPersonKey(e.target.value)
                invalidatePlan()
              }}
            >
              <option value="">{people.isLoading ? t('purge.loading') : t('purge.notSelected')}</option>
              {/* Пока список не загрузился, выбранный в фильтрах человек всё равно
                  должен отображаться, иначе select выглядит пустым. */}
              {personKey && !people.data?.some((p) => p.key === personKey) && (
                <option value={personKey}>{personKey}</option>
              )}
              {people.data?.map((p) => (
                <option key={p.key} value={p.key}>
                  {p.display_name || p.key}
                </option>
              ))}
            </select>
          </div>

          <div className="field">
            <label htmlFor="purge-from">{t('purge.from')}</label>
            <input
              id="purge-from"
              className="input"
              type="date"
              value={from}
              max={to}
              onChange={(e) => {
                setFrom(e.target.value)
                invalidatePlan()
              }}
            />
          </div>

          <div className="field">
            <label htmlFor="purge-to">{t('purge.to')}</label>
            <input
              id="purge-to"
              className="input"
              type="date"
              value={to}
              min={from}
              onChange={(e) => {
                setTo(e.target.value)
                invalidatePlan()
              }}
            />
          </div>
        </div>

        <SourceChips
          options={sourceOptions}
          selected={sources}
          onChange={(next) => {
            setSources(next)
            invalidatePlan()
          }}
        />

        <div className="inline-group" style={{ gap: 16 }}>
          <label className="checkbox" htmlFor="purge-sync-runs">
            <input
              id="purge-sync-runs"
              type="checkbox"
              checked={syncRuns}
              onChange={(e) => {
                setSyncRuns(e.target.checked)
                invalidatePlan()
              }}
            />
            {t('purge.deleteRuns')}
          </label>

          <label className="checkbox" htmlFor="purge-reset-docs">
            <input
              id="purge-reset-docs"
              type="checkbox"
              checked={resetDocLinks}
              onChange={(e) => {
                setResetDocLinks(e.target.checked)
                invalidatePlan()
              }}
            />
            {t('purge.resetDocs')}
          </label>
        </div>

        <div className="inline-group">
          <button type="button" className="btn" onClick={runPreview} disabled={!canCount}>
            {preview.isPending ? t('purge.counting') : t('purge.preview')}
          </button>
          {!personKey && <span className="muted">{t('purge.pickPerson')}</span>}
        </div>

        {preview.isError && (
          <ErrorState
            error={preview.error}
            title={t('purge.previewError')}
            onRetry={runPreview}
          />
        )}

        {plan && (
          <div className="stack" style={{ gap: 10 }}>
            {nothingToDelete ? (
              <p className="muted">{t('purge.nothing')}</p>
            ) : (
              <>
                <div>
                  {t('purge.planPrefix')} <strong>{fmtNumber(plan.events)}</strong>{' '}
                  {tp('purge.pluralEvents', plan.events)} {t('purge.planPeriod', { period })}
                  {syncRuns && (
                    <>
                      , {t('purge.also')} {fmtNumber(plan.sync_runs)}{' '}
                      {tp('purge.pluralRuns', plan.sync_runs)}
                    </>
                  )}
                  .
                </div>

                {/* Go отдаёт пустой срез как null — отсюда защита от отсутствия массива. */}
                {plan.by_source && plan.by_source.length > 0 && (
                  <div className="table-wrap" style={{ maxHeight: 'none' }}>
                    <table className="data">
                      <caption className="visually-hidden">{t('purge.bySourceCaption')}</caption>
                      <thead>
                        <tr>
                          <th scope="col">{t('purge.source')}</th>
                          <th scope="col" className="num">
                            {t('purge.eventsCol')}
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {plan.by_source.map((item) => (
                          <tr key={item.key}>
                            <th scope="row">{item.label || sourceLabel(item.key)}</th>
                            <td className="num">{fmtNumber(item.count)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}

                {!confirming && (
                  <div className="inline-group">
                    <button
                      type="button"
                      className="btn btn-danger"
                      onClick={() => setConfirming(true)}
                      disabled={busy}
                    >
                      {t('purge.deleteBtn', { what: planLabel })}
                    </button>
                  </div>
                )}

                {confirming && (
                  <div className="danger-box" role="alertdialog" aria-label={t('purge.confirmAria')}>
                    <div>
                      {t('purge.confirmPrefix')} <strong>{planLabel}</strong>{' '}
                      {t('purge.confirmSuffix', { period })}
                    </div>
                    <div className="inline-group">
                      <button
                        type="button"
                        className="btn btn-danger"
                        onClick={runPurge}
                        disabled={busy}
                      >
                        {purge.isPending ? t('purge.deleting') : t('purge.confirmYes')}
                      </button>
                      <button
                        type="button"
                        className="btn"
                        onClick={() => setConfirming(false)}
                        disabled={busy}
                      >
                        {t('purge.cancel')}
                      </button>
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        )}

        {purge.isError && <ErrorState error={purge.error} title={t('purge.deleteError')} />}

        {done && (
          <div className="success-box" role="status">
            {t('purge.done')} {fmtNumber(done.events)} {tp('purge.pluralEvents', done.events)}
            {done.sync_runs > 0 &&
              `, ${fmtNumber(done.sync_runs)} ${tp('purge.pluralRuns', done.sync_runs)}`}
            {done.doc_links_reset > 0 &&
              `, ${t('purge.docsReset', { n: fmtNumber(done.doc_links_reset) })}`}
            . {t('purge.dashboardUpdated')}
          </div>
        )}
      </div>
    </section>
  )
}
