import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SkeletonLines } from './Skeleton'
import { api } from '../lib/api'
import { fmtNumber } from '../lib/format'
import { useT, type MessageKey } from '../i18n'

/** Страны, для которых определён маппинг офисов. */
const COUNTRIES: { code: string; label: MessageKey }[] = [
  { code: 'RU', label: 'holidays.country.RU' },
  { code: 'CY', label: 'holidays.country.CY' },
  { code: 'GB', label: 'holidays.country.GB' },
  { code: 'ES', label: 'holidays.country.ES' },
  { code: 'GE', label: 'holidays.country.GE' },
]

/**
 * Редактор корпоративного календаря праздников: страна × год. Если для года
 * есть хотя бы одна запись, весь год берётся из этого календаря, а праздники
 * Google для него игнорируются.
 */
export function HolidaysEditor() {
  const { t, tp, lang } = useT()
  const qc = useQueryClient()
  const now = new Date().getFullYear()
  const dayFmt = new Intl.DateTimeFormat(lang === 'en' ? 'en-GB' : 'ru-RU', {
    day: 'numeric',
    month: 'long',
    weekday: 'short',
  })
  const [country, setCountry] = useState('RU')
  const [year, setYear] = useState(now)
  const [newDay, setNewDay] = useState('')
  const [newLabel, setNewLabel] = useState('')

  const key = ['holidays', country, year] as const
  const holidays = useQuery({ queryKey: key, queryFn: () => api.holidays(country, year) })
  const invalidate = () => void qc.invalidateQueries({ queryKey: ['holidays'] })

  const add = useMutation({
    mutationFn: () => api.addHoliday({ country, day: newDay, label: newLabel.trim() }),
    onSuccess: () => {
      setNewDay('')
      setNewLabel('')
      invalidate()
    },
  })
  const del = useMutation({
    mutationFn: (day: string) => api.deleteHoliday(country, day),
    onSuccess: invalidate,
  })
  const importGoogle = useMutation({
    mutationFn: () => api.importHolidays(country, year),
    onSuccess: invalidate,
  })

  const days = holidays.data?.days ?? []

  return (
    <section className="card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{t('holidays.title')}</h2>
          <div className="chart-card-subtitle">{t('holidays.subtitle')}</div>
        </div>
      </header>
      <div className="panel-body stack">
        <div className="form-grid">
          <div className="field">
            <label htmlFor="hol-country">{t('holidays.countryLabel')}</label>
            <select
              id="hol-country"
              className="select"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
            >
              {COUNTRIES.map((c) => (
                <option key={c.code} value={c.code}>
                  {c.code} — {t(c.label)}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="hol-year">{t('holidays.year')}</label>
            <select
              id="hol-year"
              className="select num"
              value={year}
              onChange={(e) => setYear(Number(e.target.value))}
            >
              {[now - 2, now - 1, now, now + 1].map((y) => (
                <option key={y} value={y}>
                  {y}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <span className="field-label">{t('holidays.fill')}</span>
            <div className="inline-group">
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => importGoogle.mutate()}
                disabled={importGoogle.isPending}
                title={t('holidays.importHint')}
              >
                {importGoogle.isPending ? t('holidays.importing') : t('holidays.importGoogle')}
              </button>
              {importGoogle.data && (
                <span className="muted num">
                  {t('holidays.imported', { n: importGoogle.data.imported })}
                </span>
              )}
              {importGoogle.isError && (
                <span className="error-text">{importGoogle.error.message}</span>
              )}
            </div>
          </div>
        </div>

        {holidays.isLoading ? (
          <SkeletonLines count={4} height={22} />
        ) : (
          <>
            <p className="muted" style={{ margin: 0 }}>
              {holidays.data?.covered
                ? `${t('holidays.covered', { year })} ${fmtNumber(days.length)} ${tp('holidays.pluralDays', days.length)}.`
                : t('holidays.notCovered', { year })}
            </p>
            {days.length > 0 && (
              <div className="stack" style={{ gap: 4 }}>
                {days.map((d) => (
                  <div className="inline-group" key={d.day} style={{ alignItems: 'baseline' }}>
                    <span className="num" style={{ minWidth: 150 }}>
                      {dayFmt.format(new Date(d.day))}
                    </span>
                    <span style={{ flex: 1 }}>{d.label || '—'}</span>
                    <button
                      type="button"
                      className="btn btn-ghost btn-sm"
                      onClick={() => del.mutate(d.day.slice(0, 10))}
                      disabled={del.isPending}
                    >
                      {t('settings.delete')}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </>
        )}

        <div className="form-grid">
          <div className="field">
            <label htmlFor="hol-day">{t('holidays.date')}</label>
            <input
              id="hol-day"
              className="input"
              type="date"
              min={`${year}-01-01`}
              max={`${year}-12-31`}
              value={newDay}
              onChange={(e) => setNewDay(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="hol-label">{t('holidays.name')}</label>
            <div className="inline-group">
              <input
                id="hol-label"
                className="input"
                type="text"
                placeholder={t('holidays.namePlaceholder')}
                value={newLabel}
                onChange={(e) => setNewLabel(e.target.value)}
                style={{ flex: '1 1 160px' }}
              />
              <button
                type="button"
                className="btn btn-primary"
                disabled={!newDay || add.isPending}
                onClick={() => add.mutate()}
              >
                {t('holidays.add')}
              </button>
            </div>
          </div>
        </div>
        {add.isError && <div className="error-text">{add.error.message}</div>}
      </div>
    </section>
  )
}
