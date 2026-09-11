import { Link } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useT } from '../i18n'

interface EmptyStateProps {
  title?: string
  description?: ReactNode
  /** Показать ссылку «Запустить сбор» на /settings. */
  showSyncLink?: boolean
  action?: ReactNode
}

export function EmptyState({ title, description, showSyncLink = true, action }: EmptyStateProps) {
  const { t } = useT()
  return (
    <div className="state">
      <div className="state-title">{title ?? t('ui.emptyTitle')}</div>
      <div>{description ?? t('ui.emptyHint')}</div>
      {action}
      {showSyncLink && (
        <Link className="btn btn-primary" to="/settings">
          {t('ui.startSync')}
        </Link>
      )}
    </div>
  )
}
