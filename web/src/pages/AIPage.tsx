import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { EmptyState } from '../components/EmptyState'
import { Skeleton } from '../components/Skeleton'
import { api } from '../lib/api'
import { useFilters } from '../lib/useFilters'
import { useT } from '../i18n'
import { fmtDate, fmtNumber } from '../lib/format'
import type { AIRules, AISampleItem, AIStatus } from '../lib/types'

const aiKeys = {
  status: ['ai', 'status'] as const,
  labels: ['ai', 'labels'] as const,
  sample: (person: string, from: string, to: string) => ['ai', 'sample', person, from, to] as const,
}

/** Страница локальной AI-модели: статус, калибровка и переоценка сообщений. */
export function AIPage() {
  const { t, tp } = useT()
  const { filters } = useFilters()
  const qc = useQueryClient()

  const status = useQuery<AIStatus>({ queryKey: aiKeys.status, queryFn: api.aiStatus })
  const labels = useQuery({
    queryKey: aiKeys.labels,
    queryFn: api.aiLabels,
    enabled: status.data?.enabled === true && !status.data.error,
  })
  const sample = useQuery({
    queryKey: aiKeys.sample(filters.person, filters.from, filters.to),
    queryFn: () => api.aiSample(filters.person, filters.from, filters.to),
    enabled: Boolean(filters.person),
  })

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['ai'] })
  }
  const addLabel = useMutation({
    mutationFn: ({ text, score }: { text: string; score: number }) => api.aiAddLabel(text, score),
    onSuccess: invalidate,
  })
  const deleteLabel = useMutation({ mutationFn: api.aiDeleteLabel, onSuccess: invalidate })
  const train = useMutation({ mutationFn: api.aiTrain, onSuccess: invalidate })
  const rescore = useMutation({
    mutationFn: () =>
      api.aiRescore({ person_key: filters.person || undefined, from: filters.from, to: filters.to }),
    onSuccess: () => {
      invalidate()
      void qc.invalidateQueries({ queryKey: ['stats'] })
      void qc.invalidateQueries({ queryKey: ['events'] })
    },
  })

  const [manualText, setManualText] = useState('')
  const [manualScore, setManualScore] = useState(5)

  const st = status.data
  const ready = st?.enabled && !st.error

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('ai.title')}</h1>
          <p>{t('ai.subtitle')}</p>
        </div>
      </div>

      <section className="card" style={{ padding: 14, display: 'grid', gap: 8 }}>
        <span className="field-label">{t('ai.statusLabel')}</span>
        {status.isLoading ? (
          <Skeleton height={40} radius={8} />
        ) : !st?.enabled ? (
          <p className="muted">
            {t('ai.disabledBefore')}
            <code>docker compose up -d ai-scorer</code>
            {t('ai.disabledAfter')}
          </p>
        ) : st.error ? (
          <p className="error-text">{t('ai.unavailable', { error: st.error })}</p>
        ) : (
          <div className="inline-group num" style={{ gap: 18 }}>
            <span>
              <span className="muted">{t('ai.model')}</span> {st.model}
            </span>
            <span>
              <span className="muted">{t('ai.mode')}</span>{' '}
              {st.mode === 'calibrated' ? t('ai.modeCalibrated') : t('ai.modeHeuristic')}
            </span>
            <span>
              <span className="muted">{t('ai.examples')}</span> {fmtNumber(st.labels ?? 0)}
              {!st.trained && ` ${t('ai.needAtLeast', { n: st.min_labels ?? 0 })}`}
            </span>
            <button
              type="button"
              className="btn btn-sm"
              onClick={() => train.mutate()}
              disabled={train.isPending}
            >
              {train.isPending ? t('ai.training') : t('ai.retrain')}
            </button>
            <button
              type="button"
              className="btn btn-sm btn-primary"
              onClick={() => rescore.mutate()}
              disabled={rescore.isPending}
              title={t('ai.rescoreHint')}
            >
              {rescore.isPending
                ? t('ai.scoring')
                : t('ai.rescoreFor', { from: fmtDate(filters.from), to: fmtDate(filters.to) })}
            </button>
            {rescore.data && (
              <span className="muted">
                {t('ai.scored')} {fmtNumber(rescore.data.scored)}{' '}
                {tp('ai.pluralMessages', rescore.data.scored)}
              </span>
            )}
          </div>
        )}
      </section>

      {ready && (
        <>
          <RulesCard />

          <section className="card" style={{ padding: 14, display: 'grid', gap: 10 }}>
            <span className="field-label">{t('ai.calibrationLabel')}</span>
            {sample.isLoading ? (
              <Skeleton height={80} radius={8} />
            ) : !sample.data || sample.data.items.length === 0 ? (
              <EmptyState
                title={t('ai.noMessagesTitle')}
                description={t('ai.noMessagesDesc')}
                showSyncLink={false}
              />
            ) : (
              <div className="stack" style={{ gap: 10 }}>
                {sample.data.items.map((item) => (
                  <SampleRow
                    key={item.id}
                    item={item}
                    onSave={(score) => addLabel.mutate({ text: item.text, score })}
                    saving={addLabel.isPending}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="card" style={{ padding: 14, display: 'grid', gap: 10 }}>
            <span className="field-label">{t('ai.addManualLabel')}</span>
            <textarea
              className="input"
              rows={3}
              placeholder={t('ai.manualPlaceholder')}
              value={manualText}
              onChange={(e) => setManualText(e.target.value)}
            />
            <div className="inline-group">
              <ScoreInput value={manualScore} onChange={setManualScore} id="manual-score" />
              <button
                type="button"
                className="btn btn-primary btn-sm"
                disabled={!manualText.trim() || addLabel.isPending}
                onClick={() => {
                  addLabel.mutate({ text: manualText.trim(), score: manualScore })
                  setManualText('')
                }}
              >
                {t('ai.saveExample')}
              </button>
            </div>
          </section>

          <section className="card" style={{ padding: 14, display: 'grid', gap: 10 }}>
            <span className="field-label">
              {t('ai.labelsTitle', { n: fmtNumber(labels.data?.labels.length ?? 0) })}
            </span>
            {!labels.data || labels.data.labels.length === 0 ? (
              <p className="muted">{t('ai.noLabels')}</p>
            ) : (
              <div className="stack" style={{ gap: 6 }}>
                {labels.data.labels.map((l) => (
                  <div className="inline-group" key={l.id} style={{ alignItems: 'baseline' }}>
                    <span className="badge num">{l.score}</span>
                    <span style={{ flex: 1, minWidth: 200 }}>{truncate(l.text, 160)}</span>
                    <button
                      type="button"
                      className="btn btn-ghost btn-sm"
                      onClick={() => deleteLabel.mutate(l.id)}
                      disabled={deleteLabel.isPending}
                    >
                      {t('ai.delete')}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s
}

/**
 * Автоправила: сообщения-паттерны, слишком короткие и состоящие из одних
 * эмодзи получают 0 сразу, без прогона через модель.
 */
function RulesCard() {
  const { t } = useT()
  const qc = useQueryClient()
  const rules = useQuery<AIRules>({ queryKey: ['ai', 'rules'], queryFn: api.aiRules })

  const [minLength, setMinLength] = useState(15)
  const [patterns, setPatterns] = useState('')
  const [dropEmoji, setDropEmoji] = useState(true)
  useEffect(() => {
    if (rules.data) {
      setMinLength(rules.data.min_length)
      setPatterns(rules.data.patterns.join('\n'))
      setDropEmoji(rules.data.drop_emoji_only)
    }
  }, [rules.data])

  const save = useMutation({
    mutationFn: () =>
      api.aiSaveRules({
        min_length: minLength,
        patterns: patterns.split('\n').map((p) => p.trim()).filter(Boolean),
        drop_emoji_only: dropEmoji,
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['ai', 'rules'] }),
  })

  return (
    <section className="card" style={{ padding: 14, display: 'grid', gap: 10 }}>
      <div>
        <span className="field-label">{t('ai.rulesTitle')}</span>
        <p className="muted" style={{ margin: '2px 0 0', fontSize: 13 }}>
          {t('ai.rulesHint')}
        </p>
      </div>
      {rules.isLoading ? (
        <Skeleton height={90} radius={8} />
      ) : (
        <>
          <div className="form-grid">
            <div className="field">
              <label htmlFor="rules-min-length">{t('ai.minLength')}</label>
              <input
                id="rules-min-length"
                className="input num"
                type="number"
                min={0}
                max={1000}
                value={minLength}
                onChange={(e) => setMinLength(Math.max(0, Number(e.target.value) || 0))}
              />
            </div>
            <div className="field">
              <span className="field-label">{t('ai.emoji')}</span>
              <label className="checkbox" htmlFor="rules-emoji">
                <input
                  id="rules-emoji"
                  type="checkbox"
                  checked={dropEmoji}
                  onChange={(e) => setDropEmoji(e.target.checked)}
                />
                {t('ai.emojiOnly')}
              </label>
            </div>
          </div>
          <div className="field">
            <label htmlFor="rules-patterns">{t('ai.patternsLabel')}</label>
            <textarea
              id="rules-patterns"
              className="input mono"
              rows={6}
              value={patterns}
              onChange={(e) => setPatterns(e.target.value)}
            />
          </div>
          <div className="inline-group">
            <button
              type="button"
              className="btn btn-primary btn-sm"
              onClick={() => save.mutate()}
              disabled={save.isPending}
            >
              {save.isPending ? t('ai.saving') : t('ai.saveRules')}
            </button>
            {save.isSuccess && <span className="muted">{t('ai.savedNote')}</span>}
            {save.isError && <span className="error-text">{save.error.message}</span>}
          </div>
        </>
      )}
    </section>
  )
}

function ScoreInput({
  value,
  onChange,
  id,
}: {
  value: number
  onChange: (v: number) => void
  id: string
}) {
  const { t } = useT()
  return (
    <label className="inline-group num" htmlFor={id} style={{ gap: 8 }}>
      <span className="muted">{t('ai.score')}</span>
      <input
        id={id}
        type="range"
        min={0}
        max={10}
        step={1}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
      <strong style={{ width: 20, textAlign: 'right' }}>{value}</strong>
    </label>
  )
}

function SampleRow({
  item,
  onSave,
  saving,
}: {
  item: AISampleItem
  onSave: (score: number) => void
  saving: boolean
}) {
  const { t } = useT()
  const [score, setScore] = useState(Math.round(item.score ?? 5))
  return (
    <div className="inline-group" style={{ alignItems: 'baseline' }}>
      <span className="badge num" title={t('ai.currentScoreHint')}>
        {item.score !== undefined && item.score !== null ? item.score : '—'}
      </span>
      <span style={{ flex: 1, minWidth: 220 }}>{truncate(item.text, 200)}</span>
      <ScoreInput value={score} onChange={setScore} id={`sample-${item.id}`} />
      <button type="button" className="btn btn-sm" onClick={() => onSave(score)} disabled={saving}>
        {t('ai.labelBtn')}
      </button>
    </div>
  )
}
