import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { useMeta } from './queries'
import { SOURCE_KEYS } from './chartTheme'

/**
 * Выбор «что учитывать в активности»: по каждому источнику можно исключить
 * часть типов событий. Это настройка просмотра, а не разовый фильтр, поэтому
 * живёт в localStorage и применяется ко всем запросам статистики сразу —
 * обзор, страницы источников и лента событий считают одни и те же цифры.
 */

const STORAGE_KEY = 'activity-excluded-types'

/**
 * Сентинел «не выбран ни один тип»: бэкенд трактует пустой список типов как
 * отсутствие фильтра, поэтому шлём заведомо несуществующий тип.
 */
export const NO_TYPES = '__none__'

/** Канонический порядок типов — зеркало internal/models. Мета может дополнить. */
const TYPE_ORDER: string[] = [
  'jira.issue_created',
  'jira.issue_assigned',
  'jira.issue_resolved',
  'jira.status_changed',
  'jira.field_changed',
  'jira.comment',
  'jira.worklog',
  'gitlab.commit',
  'gitlab.mr_opened',
  'gitlab.mr_merged',
  'gitlab.mr_closed',
  'gitlab.mr_approved',
  'gitlab.review_comment',
  'gitlab.issue',
  'gitlab.note',
  'gitlab.push',
  'slack.message',
  'slack.thread_reply',
  'slack.reaction',
  'gdocs.edit',
  'gdocs.comment',
  'gdocs.create',
  'gdocs.suggestion',
  'gcal.meeting',
  'gcal.recurring',
  'gcal.one_on_one',
  'gcal.interview',
  'allure.launch',
  'allure.testcase_created',
  'allure.testcase_updated',
  'allure.defect',
  'confluence.page_created',
  'confluence.page_edited',
  'confluence.comment',
  'confluence.blogpost',
]

export function sourceOfType(type: string): string {
  const i = type.indexOf('.')
  return i > 0 ? type.slice(0, i) : ''
}

function loadExcluded(): string[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

interface ActivityTypesContextValue {
  /** Типы, исключённые из активности. */
  excluded: Set<string>
  /** Все известные типы, сгруппированные по источникам. */
  typesBySource: Record<string, string[]>
  toggleType: (type: string) => void
  /** Включить (included=true) или выключить все типы источника разом. */
  setSource: (source: string, included: boolean) => void
  reset: () => void
  /**
   * Итоговый параметр `type` для запросов: пересечение выбора «что учитывать»
   * с разовым URL-фильтром по типам. undefined = фильтр не нужен.
   */
  effectiveTypes: (urlTypes: string[]) => string[] | undefined
}

const Ctx = createContext<ActivityTypesContextValue | null>(null)

export function ActivityTypesProvider({ children }: { children: ReactNode }) {
  const { data: meta } = useMeta()
  const [excludedList, setExcludedList] = useState<string[]>(loadExcluded)

  useEffect(() => {
    try {
      if (excludedList.length === 0) localStorage.removeItem(STORAGE_KEY)
      else localStorage.setItem(STORAGE_KEY, JSON.stringify(excludedList))
    } catch {
      // localStorage недоступен — выбор просто не переживёт перезагрузку
    }
  }, [excludedList])

  const allTypes = useMemo(() => {
    const known = new Set(TYPE_ORDER)
    const extra = Object.keys(meta?.type_labels ?? {})
      .filter((t) => !known.has(t))
      .sort()
    return [...TYPE_ORDER, ...extra]
  }, [meta])

  const typesBySource = useMemo(() => {
    const out: Record<string, string[]> = {}
    for (const key of SOURCE_KEYS) out[key] = []
    for (const t of allTypes) {
      const src = sourceOfType(t)
      if (!src) continue
      ;(out[src] ??= []).push(t)
    }
    return out
  }, [allTypes])

  const excluded = useMemo(() => new Set(excludedList), [excludedList])

  const toggleType = useCallback((type: string) => {
    setExcludedList((prev) =>
      prev.includes(type) ? prev.filter((t) => t !== type) : [...prev, type],
    )
  }, [])

  const setSource = useCallback(
    (source: string, included: boolean) => {
      const own = typesBySource[source] ?? []
      setExcludedList((prev) => {
        const rest = prev.filter((t) => sourceOfType(t) !== source)
        return included ? rest : [...rest, ...own]
      })
    },
    [typesBySource],
  )

  const reset = useCallback(() => setExcludedList([]), [])

  const effectiveTypes = useCallback(
    (urlTypes: string[]): string[] | undefined => {
      if (excluded.size === 0) return urlTypes.length ? urlTypes : undefined
      const base = urlTypes.length ? urlTypes : allTypes
      const out = base.filter((t) => !excluded.has(t))
      return out.length ? out : [NO_TYPES]
    },
    [excluded, allTypes],
  )

  const value = useMemo(
    () => ({ excluded, typesBySource, toggleType, setSource, reset, effectiveTypes }),
    [excluded, typesBySource, toggleType, setSource, reset, effectiveTypes],
  )

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useActivityTypes(): ActivityTypesContextValue {
  const v = useContext(Ctx)
  if (!v) throw new Error('useActivityTypes требует ActivityTypesProvider')
  return v
}
