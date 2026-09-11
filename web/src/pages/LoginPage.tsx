import { useT } from '../i18n'

/** Экран входа: кнопка Google + расшифровка ошибок колбэка (?auth_error=)
 *  и пометка об истёкшей сессии (флаг из sessionStorage, ставит 401-обработчик). */
export function LoginPage() {
  const { t } = useT()
  const err = new URLSearchParams(window.location.search).get('auth_error')
  const errText =
    err === 'not_allowed'
      ? t('login.notAllowed')
      : err === 'domain'
        ? t('login.wrongDomain')
        : err
          ? t('login.failed')
          : ''

  // Сессия истекла: 401-обработчик поставил флаг, показываем мягкое пояснение.
  const expired = sessionStorage.getItem('uad_session_expired') === '1'
  if (expired) sessionStorage.removeItem('uad_session_expired')

  const login = () => {
    const redirect = window.location.pathname + window.location.search
    window.location.href = `/auth/login?redirect=${encodeURIComponent(redirect)}`
  }

  return (
    <div className="login-screen">
      <div className="login-aurora" aria-hidden="true" />
      <div className="login-card">
        <div className="login-brand">
          <img className="login-mark" src="/logo.svg" alt="" width={40} height={40} />
          <span className="login-brandname">Team Health Management Tools</span>
        </div>

        <p className="login-subtitle">{t('login.subtitle')}</p>

        {expired && !errText && <p className="login-note">{t('login.expired')}</p>}
        {errText && <p className="login-error">{errText}</p>}

        <button type="button" className="google-btn" onClick={login}>
          <svg className="google-g" width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
            <path fill="#4285F4" d="M17.64 9.2c0-.64-.06-1.25-.16-1.84H9v3.48h4.84a4.14 4.14 0 0 1-1.8 2.72v2.26h2.92c1.7-1.57 2.68-3.88 2.68-6.62Z" />
            <path fill="#34A853" d="M9 18c2.43 0 4.47-.8 5.96-2.18l-2.92-2.26c-.8.54-1.84.86-3.04.86-2.34 0-4.32-1.58-5.03-3.7H.96v2.33A9 9 0 0 0 9 18Z" />
            <path fill="#FBBC05" d="M3.97 10.72a5.4 5.4 0 0 1 0-3.44V4.95H.96a9 9 0 0 0 0 8.1l3-2.33Z" />
            <path fill="#EA4335" d="M9 3.58c1.32 0 2.5.46 3.44 1.35l2.58-2.58C13.47.9 11.43 0 9 0A9 9 0 0 0 .96 4.95l3 2.33C4.68 5.16 6.66 3.58 9 3.58Z" />
          </svg>
          {t('login.google')}
        </button>

        <p className="login-hint">{t('login.hint')}</p>
      </div>
    </div>
  )
}
