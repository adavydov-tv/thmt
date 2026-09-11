import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  CartesianGrid,
  Legend as RLegend,
  Line,
  LineChart,
  ResponsiveContainer,
  Scatter,
  ScatterChart,
  Tooltip,
  XAxis,
  YAxis,
  ZAxis,
} from 'recharts'
import { ChartCard } from '../components/ChartCard'
import { EmptyState } from '../components/EmptyState'
import { ErrorState } from '../components/ErrorState'
import { Skeleton } from '../components/Skeleton'
import { api } from '../lib/api'
import { TIMEZONE, useFilters } from '../lib/useFilters'
import { fmtNumber, fmtBucket } from '../lib/format'
import { useChartTokens } from '../lib/chartTheme'
import { ruleGroups, ruleHelpEntries } from '../lib/ruleHelp'
import { useT } from '../i18n'
import type { FormatCompareResponse, FormatScatterPoint, Granularity } from '../lib/types'

const FORMAT_LABEL: Record<string, [string, string]> = {
  office: ['Офис', 'Office'],
  hybrid: ['Гибрид', 'Hybrid'],
  remote: ['Удалёнка', 'Remote'],
}
const FORMAT_SLOT: Record<string, number> = { office: 3, hybrid: 5, remote: 0 }

// Позитивные (info) правила — не считаем «отклонениями» по умолчанию.
const POSITIVE_RULES = new Set([
  'no_vacation',
  'offday_activity',
  'steady_rhythm',
  'onboarding_rise',
  'mentor_one_on_ones',
  'vacation_recovery',
  'review_champion',
])

type View = 'table' | 'heatmap' | 'scatter' | 'trend'
type Metric = 'events' | 'useful' | 'active'
type Translate = ReturnType<typeof useT>['t']

const HINT_KEY = {
  table: 'formatCompare.tableHint',
  heatmap: 'formatCompare.heatmapHint',
  scatter: 'formatCompare.scatterHint',
  trend: 'formatCompare.trendHint',
} as const

export function FormatComparePage() {
  const { filters } = useFilters()
  const { t, lang } = useT()
  const tokens = useChartTokens()

  const [dim, setDim] = useState<'area' | 'cluster' | 'grade'>('area')
  const [areas, setAreas] = useState<string[]>([])
  const [clusters, setClusters] = useState<string[]>([])
  const [grades, setGrades] = useState<string[]>([])
  const [view, setView] = useState<View>('table')
  const [metric, setMetric] = useState<Metric>('events')

  // Каталог правил по группам и негативный набор по умолчанию.
  const { rulesByGroup, negativeRules } = useMemo(() => {
    const entries = ruleHelpEntries()
    const byGroup: Record<string, { key: string; label: string }[]> = {}
    const neg: string[] = []
    for (const [key, help] of entries) {
      ;(byGroup[help.group] ??= []).push({ key, label: help.label })
      if (!POSITIVE_RULES.has(key)) neg.push(key)
    }
    return { rulesByGroup: byGroup, negativeRules: neg }
  }, [lang])
  const [rules, setRules] = useState<string[]>(negativeRules)

  const q = useQuery({
    queryKey: ['format-compare', filters.from, filters.to, dim, areas, clusters, grades, rules],
    queryFn: () =>
      api.formatCompare({
        from: filters.from,
        to: filters.to,
        tz: TIMEZONE,
        dim,
        areas,
        clusters,
        grades,
        rules,
      }),
  })

  const fmtLabel = (f: string) => FORMAT_LABEL[f]?.[lang === 'en' ? 1 : 0] ?? f
  const formatColor = (f: string) => tokens.slot(FORMAT_SLOT[f] ?? 0)
  const groups = ruleGroups()
  const groupLabel = (key: string) => groups.find((g) => g.key === key)?.label ?? key

  const dimTh =
    dim === 'area'
      ? t('formatCompare.dimArea')
      : dim === 'cluster'
        ? t('formatCompare.dimCluster')
        : t('formatCompare.dimGrade')

  const views: { key: View; label: string }[] = [
    { key: 'table', label: t('formatCompare.viewTable') },
    { key: 'heatmap', label: t('formatCompare.viewHeatmap') },
    { key: 'scatter', label: t('formatCompare.viewScatter') },
    { key: 'trend', label: t('formatCompare.viewTrend') },
  ]

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{t('formatCompare.title')}</h1>
          <p>{t('formatCompare.subtitle')}</p>
        </div>
      </div>

      {/* Панель фильтров */}
      <section className="card" style={{ padding: 14 }}>
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', alignItems: 'flex-start' }}>
          <label className="muted" style={{ fontSize: 13 }}>
            {t('formatCompare.rowsBy')}{' '}
            <select value={dim} onChange={(e) => setDim(e.target.value as typeof dim)}>
              <option value="area">{t('formatCompare.dimArea')}</option>
              <option value="cluster">{t('formatCompare.dimCluster')}</option>
              <option value="grade">{t('formatCompare.dimGrade')}</option>
            </select>
          </label>
          <label className="muted" style={{ fontSize: 13 }}>
            {t('formatCompare.metric')}{' '}
            <select value={metric} onChange={(e) => setMetric(e.target.value as Metric)}>
              <option value="events">{t('formatCompare.metricEvents')}</option>
              <option value="useful">{t('formatCompare.metricUseful')}</option>
              <option value="active">{t('formatCompare.metricActive')}</option>
            </select>
          </label>
        </div>
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', marginTop: 10 }}>
          <MultiChips label={t('formatCompare.filterArea')} options={q.data?.areas ?? []} selected={areas} onChange={setAreas} />
          <MultiChips label={t('formatCompare.filterCluster')} options={q.data?.clusters ?? []} selected={clusters} onChange={setClusters} />
          <MultiChips label={t('formatCompare.filterGrade')} options={q.data?.grades ?? []} selected={grades} onChange={setGrades} />
        </div>
        <DeviationPicker
          rulesByGroup={rulesByGroup}
          groupLabel={groupLabel}
          groupOrder={groups.map((g) => g.key)}
          selected={rules}
          onChange={setRules}
          allNegatives={negativeRules}
          t={t}
        />
      </section>

      {/* Переключатель представлений */}
      <div className="inline-group" role="tablist">
        {views.map((v) => (
          <button
            key={v.key}
            type="button"
            className={`btn btn-sm${view === v.key ? ' btn-primary' : ''}`}
            onClick={() => setView(v.key)}
          >
            {v.label}
          </button>
        ))}
      </div>

      <ChartCard title={views.find((v) => v.key === view)!.label} subtitle={t(HINT_KEY[view])}>
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.isLoading ? (
          <Skeleton height={280} radius={10} />
        ) : !q.data || q.data.rows.length === 0 ? (
          <EmptyState title={t('formatCompare.empty')} showSyncLink={false} />
        ) : view === 'table' ? (
          <MatrixTable data={q.data} metric={metric} dimTh={dimTh} fmtLabel={fmtLabel} t={t} />
        ) : view === 'heatmap' ? (
          <FormatHeatmap data={q.data} groupLabel={groupLabel} fmtLabel={fmtLabel} tokens={tokens} t={t} />
        ) : view === 'scatter' ? (
          <DeviationScatter data={q.data} metric={metric} fmtLabel={fmtLabel} formatColor={formatColor} tokens={tokens} t={t} />
        ) : (
          <FormatTrendChart data={q.data} fmtLabel={fmtLabel} formatColor={formatColor} tokens={tokens} />
        )}
      </ChartCard>
    </div>
  )
}

/** Аддитивный мультивыбор чипами: пустой список = «все». */
function MultiChips({
  label,
  options,
  selected,
  onChange,
}: {
  label: string
  options: string[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  if (options.length === 0) return null
  const toggle = (o: string) => {
    const next = selected.includes(o) ? selected.filter((x) => x !== o) : [...selected, o]
    onChange(next)
  }
  return (
    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
      <span className="muted" style={{ fontSize: 13 }}>
        {label}:
      </span>
      {options.map((o) => {
        const on = selected.length === 0 || selected.includes(o)
        return (
          <button
            key={o}
            type="button"
            className={`chip${on ? ' chip-on' : ''}`}
            onClick={() => toggle(o)}
            style={{ fontSize: 12 }}
          >
            {o}
          </button>
        )
      })}
    </div>
  )
}

/** Пикер отклонений: 5 групп с раскрытием в правила. */
function DeviationPicker({
  rulesByGroup,
  groupLabel,
  groupOrder,
  selected,
  onChange,
  allNegatives,
  t,
}: {
  rulesByGroup: Record<string, { key: string; label: string }[]>
  groupLabel: (k: string) => string
  groupOrder: string[]
  selected: string[]
  onChange: (next: string[]) => void
  allNegatives: string[]
  t: Translate
}) {
  const [open, setOpen] = useState<string | null>(null)
  const sel = new Set(selected)
  const toggleRule = (key: string) => {
    const next = new Set(sel)
    next.has(key) ? next.delete(key) : next.add(key)
    onChange([...next])
  }
  const toggleGroup = (g: string) => {
    const keys = (rulesByGroup[g] ?? []).map((r) => r.key)
    const allOn = keys.every((k) => sel.has(k))
    const next = new Set(sel)
    keys.forEach((k) => (allOn ? next.delete(k) : next.add(k)))
    onChange([...next])
  }

  return (
    <div style={{ marginTop: 12 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 6 }}>
        <span className="muted" style={{ fontSize: 13 }}>
          {t('formatCompare.deviations')}:
        </span>
        <button type="button" className="btn btn-sm" onClick={() => onChange(allNegatives)}>
          {t('formatCompare.devAllNegative')}
        </button>
        <button type="button" className="btn btn-sm" onClick={() => onChange([])}>
          {t('formatCompare.devDefault')}
        </button>
      </div>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        {groupOrder.map((g) => {
          const keys = (rulesByGroup[g] ?? []).map((r) => r.key)
          if (keys.length === 0) return null
          const on = keys.filter((k) => sel.has(k)).length
          const state = on === 0 ? 'off' : on === keys.length ? 'on' : 'some'
          return (
            <div key={g} style={{ border: '1px solid var(--border)', borderRadius: 8, padding: '6px 8px' }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <label style={{ display: 'inline-flex', gap: 6, alignItems: 'center', cursor: 'pointer', fontSize: 13 }}>
                  <input
                    type="checkbox"
                    checked={state === 'on'}
                    ref={(el) => {
                      if (el) el.indeterminate = state === 'some'
                    }}
                    onChange={() => toggleGroup(g)}
                  />
                  <strong>{groupLabel(g)}</strong>
                  <span className="muted" style={{ fontSize: 11 }}>
                    {on}/{keys.length}
                  </span>
                </label>
                <button
                  type="button"
                  className="btn btn-sm"
                  onClick={() => setOpen(open === g ? null : g)}
                  aria-expanded={open === g}
                >
                  {open === g ? '▾' : '▸'}
                </button>
              </div>
              {open === g && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 6, paddingLeft: 4 }}>
                  {(rulesByGroup[g] ?? []).map((r) => (
                    <label key={r.key} style={{ display: 'inline-flex', gap: 6, alignItems: 'center', cursor: 'pointer', fontSize: 12 }}>
                      <input type="checkbox" checked={sel.has(r.key)} onChange={() => toggleRule(r.key)} />
                      {r.label}
                    </label>
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

function cellMetric(c: { avg_events_day: number; useful_events_day: number; active_ratio: number } | undefined, metric: Metric): string {
  if (!c) return '—'
  if (metric === 'active') return `${Math.round(c.active_ratio * 100)}%`
  const v = metric === 'useful' ? c.useful_events_day : c.avg_events_day
  return fmtNumber(Math.round(v * 10) / 10)
}

function MatrixTable({
  data,
  metric,
  dimTh,
  fmtLabel,
  t,
}: {
  data: FormatCompareResponse
  metric: Metric
  dimTh: string
  fmtLabel: (f: string) => string
  t: Translate
}) {
  return (
    <div className="tablewrap" style={{ overflowX: 'auto' }}>
      <table className="data format-compare">
        <thead>
          <tr>
            <th rowSpan={2}>{dimTh}</th>
            {data.formats.map((f) => (
              <th key={f} colSpan={3} style={{ textAlign: 'center' }}>
                {fmtLabel(f)}
              </th>
            ))}
          </tr>
          <tr>
            {data.formats.map((f) => [
              <th key={`${f}-p`} className="num" title={t('formatCompare.people')}>
                {t('formatCompare.peopleShort')}
              </th>,
              <th key={`${f}-m`} className="num">
                {metric === 'active'
                  ? t('formatCompare.metricActive')
                  : metric === 'useful'
                    ? t('formatCompare.metricUseful')
                    : t('formatCompare.metricEvents')}
              </th>,
              <th key={`${f}-d`} className="num" title={t('formatCompare.devPerPersonFull')}>
                {t('formatCompare.devPerPerson')}
              </th>,
            ])}
          </tr>
        </thead>
        <tbody>
          {data.rows.map((row) => (
            <tr key={row.key}>
              <td>{row.key}</td>
              {data.formats.map((f) => {
                const c = row.cells[f]
                const empty = !c || c.people === 0
                const mut = empty ? { color: 'var(--text-muted)' } : undefined
                return [
                  <td key={`${f}-p`} className="num" style={mut}>
                    {empty ? '—' : c.people}
                  </td>,
                  <td key={`${f}-m`} className="num" style={mut}>
                    {empty ? '—' : cellMetric(c, metric)}
                  </td>,
                  <td
                    key={`${f}-d`}
                    className="num"
                    style={empty ? mut : c.dev_per_person > 0 ? { color: 'var(--negative)', fontWeight: 600 } : undefined}
                  >
                    {empty ? '—' : c.dev_per_person.toFixed(2)}
                  </td>,
                ]
              })}
            </tr>
          ))}
          <tr style={{ fontWeight: 600, borderTop: '2px solid var(--border-strong)' }}>
            <td>{t('formatCompare.total')}</td>
            {data.formats.map((f) => {
              const c = data.totals[f]
              return [
                <td key={`${f}-p`} className="num">
                  {c?.people ?? 0}
                </td>,
                <td key={`${f}-m`} className="num">
                  {c && c.people ? cellMetric(c, metric) : '—'}
                </td>,
                <td key={`${f}-d`} className="num">
                  {c ? c.dev_per_person.toFixed(2) : '—'}
                </td>,
              ]
            })}
          </tr>
        </tbody>
      </table>
    </div>
  )
}

/** Тепловая карта: строки — форматы, столбцы — группы отклонений, заливка — на человека. */
function FormatHeatmap({
  data,
  groupLabel,
  fmtLabel,
  tokens,
  t,
}: {
  data: FormatCompareResponse
  groupLabel: (k: string) => string
  fmtLabel: (f: string) => string
  tokens: ReturnType<typeof useChartTokens>
  t: Translate
}) {
  const groups = data.groups
  let max = 0
  for (const f of data.formats) for (const g of groups) max = Math.max(max, data.totals[f]?.dev_by_group?.[g] ?? 0)
  return (
    <div style={{ overflowX: 'auto' }}>
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: `minmax(90px,auto) repeat(${groups.length}, minmax(90px,1fr))`,
          gap: 4,
          minWidth: 480,
        }}
      >
        <div />
        {groups.map((g) => (
          <div key={g} className="muted" style={{ fontSize: 12, textAlign: 'center', fontWeight: 600 }}>
            {groupLabel(g)}
          </div>
        ))}
        {data.formats.map((f) => (
          <FmtRow key={f} label={fmtLabel(f)} groups={groups} cell={data.totals[f]} max={max} tokens={tokens} />
        ))}
      </div>
      <p className="muted" style={{ fontSize: 12, marginTop: 8 }}>
        {t('formatCompare.heatmapLegend')}
      </p>
    </div>
  )
}

function FmtRow({
  label,
  groups,
  cell,
  max,
  tokens,
}: {
  label: string
  groups: string[]
  cell?: { dev_by_group: Record<string, number> }
  max: number
  tokens: ReturnType<typeof useChartTokens>
}) {
  return (
    <>
      <div style={{ fontSize: 13, fontWeight: 600, alignSelf: 'center' }}>{label}</div>
      {groups.map((g) => {
        const v = cell?.dev_by_group?.[g] ?? 0
        const ratio = max > 0 ? v / max : 0
        return (
          <div
            key={g}
            title={`${v.toFixed(2)}`}
            style={{
              background: tokens.sequential(ratio),
              color: ratio > 0.55 ? '#fff' : 'var(--text)',
              borderRadius: 6,
              textAlign: 'center',
              padding: '10px 4px',
              fontVariantNumeric: 'tabular-nums',
              fontSize: 13,
            }}
          >
            {v > 0 ? v.toFixed(2) : '·'}
          </div>
        )
      })}
    </>
  )
}

/** Скаттер: точка на человека, X — продуктивность, Y — отклонения, цвет — формат. */
function DeviationScatter({
  data,
  metric,
  fmtLabel,
  formatColor,
  tokens,
  t,
}: {
  data: FormatCompareResponse
  metric: Metric
  fmtLabel: (f: string) => string
  formatColor: (f: string) => string
  tokens: ReturnType<typeof useChartTokens>
  t: Translate
}) {
  const xKey: keyof FormatScatterPoint = metric === 'useful' ? 'useful_day' : 'events_day'
  const xLabel = metric === 'useful' ? t('formatCompare.metricUseful') : t('formatCompare.metricEvents')
  const byFormat = data.formats.map((f) => ({ f, points: data.scatter.filter((p) => p.format === f) }))
  return (
    <div style={{ width: '100%', height: 360 }}>
      <ResponsiveContainer>
        <ScatterChart margin={{ top: 10, right: 20, bottom: 30, left: 10 }}>
          <CartesianGrid stroke={tokens.grid} />
          <XAxis
            type="number"
            dataKey={xKey}
            name={xLabel}
            stroke={tokens.axis}
            tick={{ fontSize: 12 }}
            label={{ value: xLabel, position: 'insideBottom', offset: -15, fill: tokens.textMuted, fontSize: 12 }}
          />
          <YAxis
            type="number"
            dataKey="dev"
            name={t('formatCompare.devAxis')}
            stroke={tokens.axis}
            tick={{ fontSize: 12 }}
            label={{ value: t('formatCompare.devAxis'), angle: -90, position: 'insideLeft', fill: tokens.textMuted, fontSize: 12 }}
          />
          <ZAxis range={[60, 60]} />
          <Tooltip
            cursor={{ strokeDasharray: '3 3' }}
            formatter={(v: number, n: string) => [v, n]}
            labelFormatter={() => ''}
            content={({ payload }) => {
              const p = payload?.[0]?.payload as FormatScatterPoint | undefined
              if (!p) return null
              return (
                <div style={{ background: tokens.surface, border: '1px solid var(--border)', borderRadius: 6, padding: 8, fontSize: 12 }}>
                  <strong>{p.name}</strong>
                  <div className="muted">
                    {fmtLabel(p.format)} · {p.area || '—'} · {p.grade || '—'}
                  </div>
                  <div>
                    {xLabel}: {p[xKey]} · {t('formatCompare.devAxis')}: {p.dev}
                  </div>
                </div>
              )
            }}
          />
          <RLegend />
          {byFormat.map(({ f, points }) => (
            <Scatter key={f} name={fmtLabel(f)} data={points} fill={formatColor(f)} />
          ))}
        </ScatterChart>
      </ResponsiveContainer>
    </div>
  )
}

/** Тренд: линия событий на человека по бакетам, на формат. */
function FormatTrendChart({
  data,
  fmtLabel,
  formatColor,
  tokens,
}: {
  data: FormatCompareResponse
  fmtLabel: (f: string) => string
  formatColor: (f: string) => string
  tokens: ReturnType<typeof useChartTokens>
}) {
  const gran = (data.trend.granularity || 'week') as Granularity
  const rows = data.trend.buckets.map((b, i) => {
    const row: Record<string, string | number> = { label: fmtBucket(b, gran) }
    for (const f of data.formats) row[f] = data.trend.series[f]?.[i] ?? 0
    return row
  })
  return (
    <div style={{ width: '100%', height: 340 }}>
      <ResponsiveContainer>
        <LineChart data={rows} margin={{ top: 10, right: 20, bottom: 10, left: 0 }}>
          <CartesianGrid stroke={tokens.grid} vertical={false} />
          <XAxis dataKey="label" stroke={tokens.axis} tick={{ fontSize: 12 }} />
          <YAxis stroke={tokens.axis} tick={{ fontSize: 12 }} />
          <Tooltip contentStyle={{ background: tokens.surface, border: '1px solid var(--border)', fontSize: 12 }} />
          <RLegend />
          {data.formats.map((f) => (
            <Line key={f} type="monotone" dataKey={f} name={fmtLabel(f)} stroke={formatColor(f)} strokeWidth={2} dot={false} />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}
