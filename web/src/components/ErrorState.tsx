import { useT } from '../i18n'

interface ErrorStateProps {
  error: unknown
  onRetry?: () => void
  title?: string
}

export function ErrorState({ error, onRetry, title }: ErrorStateProps) {
  const { t } = useT()
  const message = error instanceof Error ? error.message : String(error ?? t('ui.unknownError'))
  return (
    <div className="state" role="alert">
      <div className="state-title state-error">{title ?? t('ui.loadError')}</div>
      <div className="secondary">{message}</div>
      {onRetry && (
        <button type="button" className="btn" onClick={onRetry}>
          {t('ui.retry')}
        </button>
      )}
    </div>
  )
}
