import { useEffect, useState } from 'react'

export interface PageSection {
  id: string
  label: string
}

/**
 * Навигация по блокам внутри вкладки: закреплённая колонка справа со ссылками
 * на секции страницы. Активная секция подсвечивается по мере прокрутки.
 * На узких экранах колонка скрывается (см. styles.css).
 */
export function SectionNav({ sections }: { sections: PageSection[] }) {
  const [active, setActive] = useState(sections[0]?.id ?? '')

  useEffect(() => {
    const observer = new IntersectionObserver(
      (entries) => {
        // Первая видимая сверху секция считается активной.
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)
        if (visible[0]) setActive(visible[0].target.id)
      },
      { rootMargin: '-120px 0px -60% 0px' },
    )
    for (const s of sections) {
      const el = document.getElementById(s.id)
      if (el) observer.observe(el)
    }
    return () => observer.disconnect()
  }, [sections])

  const jump = (id: string) => {
    const el = document.getElementById(id)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'start' })
    setActive(id)
  }

  if (sections.length === 0) return null
  return (
    <nav className="section-nav" aria-label="Sections">
      {sections.map((s) => (
        <button
          key={s.id}
          type="button"
          className={`section-nav-link${active === s.id ? ' active' : ''}`}
          onClick={() => jump(s.id)}
        >
          {s.label}
        </button>
      ))}
    </nav>
  )
}
