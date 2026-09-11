import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useT } from '../i18n'

const STORAGE_KEY = 'onboarding-done'

interface Step {
  title: string
  body: string
  link?: { to: string; label: string }
}

/** Шаги гида собираются функцией (не top-level константой), чтобы строки
 * брались из актуального словаря при каждом рендере. */
function buildSteps(t: ReturnType<typeof useT>['t']): Step[] {
  return [
    {
      title: t('tour.introTitle'),
      body: t('tour.introBody'),
    },
    {
      title: t('tour.s1Title'),
      body: t('tour.s1Body'),
      link: { to: '/settings', label: t('tour.s1Link') },
    },
    {
      title: t('tour.s2Title'),
      body: t('tour.s2Body'),
    },
    {
      title: t('tour.s3Title'),
      body: t('tour.s3Body'),
      link: { to: '/compare', label: t('tour.s3Link') },
    },
    {
      title: t('tour.s4Title'),
      body: t('tour.s4Body'),
      link: { to: '/violations', label: t('tour.s4Link') },
    },
    {
      title: t('tour.s5Title'),
      body: t('tour.s5Body'),
      link: { to: '/settings', label: t('tour.s5Link') },
    },
  ]
}

/** Обёртка запуска: показывает гид при первом заходе, «?» в шапке — повторно. */
export function useOnboarding() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    try {
      if (!localStorage.getItem(STORAGE_KEY)) setOpen(true)
    } catch {
      // без localStorage гид просто не автозапускается
    }
  }, [])
  const close = () => {
    try {
      localStorage.setItem(STORAGE_KEY, new Date().toISOString())
    } catch {
      // ок
    }
    setOpen(false)
  }
  return { open, show: () => setOpen(true), close }
}

/** Пошаговый гид по админке для новых пользователей. */
export function OnboardingTour({ onClose }: { onClose: () => void }) {
  const { t } = useT()
  const [step, setStep] = useState(0)
  const steps = buildSteps(t)
  const s = steps[step]
  const last = step === steps.length - 1

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      if (e.key === 'ArrowRight' && !last) setStep((v) => v + 1)
      if (e.key === 'ArrowLeft' && step > 0) setStep((v) => v - 1)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [step, last, onClose])

  return (
    <div className="tour-backdrop" role="dialog" aria-modal="true" aria-label={t('tour.aria')}>
      <div className="tour card">
        <div className="tour-head">
          <span className="muted num">
            {step + 1} / {steps.length}
          </span>
          <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>
            {t('tour.skip')}
          </button>
        </div>
        <h2 style={{ margin: '4px 0 8px' }}>{s.title}</h2>
        <p style={{ margin: 0, lineHeight: 1.55 }}>{s.body}</p>
        <div className="tour-dots" aria-hidden="true">
          {steps.map((_, i) => (
            <button
              key={i}
              type="button"
              className={`tour-dot${i === step ? ' active' : ''}`}
              onClick={() => setStep(i)}
              aria-label={t('tour.stepAria', { n: i + 1 })}
            />
          ))}
        </div>
        <div className="tour-actions">
          {s.link && (
            <Link className="btn btn-sm" to={s.link.to} onClick={onClose}>
              {s.link.label}
            </Link>
          )}
          <span className="spacer" style={{ flex: 1 }} />
          {step > 0 && (
            <button type="button" className="btn btn-sm" onClick={() => setStep(step - 1)}>
              {t('tour.back')}
            </button>
          )}
          {last ? (
            <button type="button" className="btn btn-primary btn-sm" onClick={onClose}>
              {t('tour.start')}
            </button>
          ) : (
            <button type="button" className="btn btn-primary btn-sm" onClick={() => setStep(step + 1)}>
              {t('tour.next')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
