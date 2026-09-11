export interface LegendEntry {
  key: string
  label: string
  color: string
}

/** Легенда присутствует всегда, когда серий ≥ 2. Для одной серии её называет заголовок. */
export function Legend({ entries }: { entries: LegendEntry[] }) {
  if (entries.length < 2) return null
  return (
    <ul className="legend" style={{ listStyle: 'none', margin: 0, padding: '10px 0 0' }}>
      {entries.map((e) => (
        <li className="legend-item" key={e.key}>
          <span className="dot" style={{ background: e.color }} aria-hidden="true" />
          <span>{e.label}</span>
        </li>
      ))}
    </ul>
  )
}
