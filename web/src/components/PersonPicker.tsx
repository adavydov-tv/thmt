import { useT } from '../i18n'
import type { Person } from '../lib/types'

interface PersonPickerProps {
  people: Person[] | undefined
  value: string
  onChange: (personKey: string) => void
  isLoading?: boolean
}

export function PersonPicker({ people, value, onChange, isLoading }: PersonPickerProps) {
  const { t } = useT()
  return (
    <div className="inline-group">
      <label className="field-label" htmlFor="person-picker">
        {t('picker.person')}
      </label>
      <select
        id="person-picker"
        className="select"
        value={value}
        disabled={isLoading || !people || people.length === 0}
        onChange={(e) => onChange(e.target.value)}
      >
        {(!people || people.length === 0) && (
          <option value="">{isLoading ? t('picker.loading') : t('picker.noPeople')}</option>
        )}
        {people?.map((p) => (
          <option key={p.key} value={p.key}>
            {p.display_name || p.key}
          </option>
        ))}
      </select>
    </div>
  )
}
