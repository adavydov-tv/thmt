import { useT } from '../i18n'

interface SkeletonProps {
  height?: number | string
  width?: number | string
  radius?: number
}

export function Skeleton({ height = 16, width = '100%', radius = 6 }: SkeletonProps) {
  const { t } = useT()
  return (
    <div
      className="skeleton"
      style={{ height, width, borderRadius: radius }}
      role="status"
      aria-label={t('ui.loading')}
    />
  )
}

export function SkeletonLines({ count = 3, height = 16 }: { count?: number; height?: number }) {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} height={height} width={i === count - 1 ? '60%' : '100%'} />
      ))}
    </div>
  )
}
