import { Link } from 'react-router-dom'
import { sourceLabel, useChartTokens } from '../lib/chartTheme'
import { fmtDateTime, fmtDecimal } from '../lib/format'
import { useT } from '../i18n'
import type { ActivityEvent } from '../lib/types'

interface EventRowProps {
  event: ActivityEvent
  typeLabel?: (type: string) => string
  /** search-строка фильтров, чтобы ссылка сохраняла контекст */
  search?: string
}

export function EventRow({ event, typeLabel, search = '' }: EventRowProps) {
  const t = useChartTokens()
  const { t: tr } = useT()
  const color = t.sourceColor(event.source)
  const label = typeLabel ? typeLabel(event.type) : event.type

  return (
    <Link className="event-row" to={`/events/${event.id}${search}`}>
      <span className="event-marker" style={{ background: color }} aria-hidden="true" />
      <span>
        <span className="event-title">{event.title || tr('ui.untitled')}</span>
        <span className="event-meta">
          <span>{sourceLabel(event.source)}</span>
          <span className="badge">{label}</span>
          {(event.project_name || event.project) && <span>{event.project_name || event.project}</span>}
          {event.effort > 0 &&
            (event.effort_unit === 'seconds' ? (
              <span className="num">
                {Math.round(event.effort / 60)} {tr('ui.minutes')}
              </span>
            ) : (
              <span className="num">
                {fmtDecimal(event.effort)} {event.effort_unit ?? ''}
              </span>
            ))}
          {typeof event.meta?.ai_score === 'number' && (
            <span className="badge num" title={tr('slackArch.aiScoreHint')}>
              AI {fmtDecimal(event.meta.ai_score as number)}
            </span>
          )}
          {typeof event.meta?.meet_minutes === 'number' && (
            <span className="badge badge-attended num" title={tr('meet.minutesHint')}>
              {tr('meet.minutes', { n: event.meta.meet_minutes as number })}
            </span>
          )}
          {event.meta?.maybe_offline === true && (
            <span className="badge badge-unconfirmed" title={tr('meet.maybeOfflineHint')}>
              {tr('meet.maybeOffline')}
            </span>
          )}
          {event.meta?.presence_unclear === true && (
            <span className="badge badge-unclear" title={tr('meet.presenceUnclearHint')}>
              {tr('meet.presenceUnclear')}
            </span>
          )}
        </span>
      </span>
      <span className="event-time">{fmtDateTime(event.occurred_at)}</span>
    </Link>
  )
}
