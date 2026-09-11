/* eslint-disable react-refresh/only-export-components */
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { ru, type MessageKey } from './ru'
import { en } from './en'
import { LANG_STORAGE_KEY, currentLang, type Lang } from './lang'

export type { Lang }

const DICTS: Record<Lang, Record<MessageKey, string>> = { ru, en }

interface I18nContextValue {
  lang: Lang
  setLang: (lang: Lang) => void
}

const I18nContext = createContext<I18nContextValue>({ lang: 'ru', setLang: () => {} })

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(currentLang)
  const setLang = useCallback((next: Lang) => {
    setLangState(next)
    try {
      localStorage.setItem(LANG_STORAGE_KEY, next)
    } catch {
      /* ок */
    }
    document.documentElement.lang = next
  }, [])
  const value = useMemo(() => ({ lang, setLang }), [lang, setLang])
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useLang() {
  return useContext(I18nContext)
}

/** Подстановка аргументов: "{n} из {total}" + {n: 1, total: 5}. */
function interpolate(template: string, args?: Record<string, string | number>): string {
  if (!args) return template
  return template.replace(/\{(\w+)\}/g, (m, name) => (name in args ? String(args[name]) : m))
}

/**
 * Переводчик. t('key') — строка; t('key', {n: 5}) — с подстановкой.
 * tp('key', n) — плюрализация: в словаре формы через «|»
 * (ru: «день|дня|дней», en: «day|days»), возвращает нужную форму.
 * Отсутствующий ключ отдаёт сам ключ — видно, что забыли перевести.
 */
export function useT() {
  const { lang } = useContext(I18nContext)
  const dict = DICTS[lang]
  const t = useCallback(
    (key: MessageKey, args?: Record<string, string | number>) =>
      interpolate(dict[key] ?? key, args),
    [dict],
  )
  const tp = useCallback(
    (key: MessageKey, n: number) => {
      const forms = (dict[key] ?? key).split('|')
      if (forms.length === 1) return forms[0]
      if (lang === 'en') return n === 1 ? forms[0] : forms[forms.length - 1]
      // Русский: 1 день / 2 дня / 5 дней.
      const abs = Math.abs(n) % 100
      const d = abs % 10
      if (abs > 10 && abs < 20) return forms[2] ?? forms[forms.length - 1]
      if (d === 1) return forms[0]
      if (d >= 2 && d <= 4) return forms[1] ?? forms[forms.length - 1]
      return forms[2] ?? forms[forms.length - 1]
    },
    [dict, lang],
  )
  return { t, tp, lang }
}

export type { MessageKey }
