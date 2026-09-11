/** Текущий язык для чистых функций вне React (словарики в lib/). Дерево
 * приложения перемонтируется при смене языка (key={lang} в App), поэтому
 * чтение localStorage здесь безопасно: значение стабильно в рамках рендера. */
export type Lang = 'ru' | 'en'

export const LANG_STORAGE_KEY = 'lang'

export function currentLang(): Lang {
  try {
    const saved = localStorage.getItem(LANG_STORAGE_KEY)
    if (saved === 'en') return 'en'
  } catch {
    /* приватный режим */
  }
  return 'ru'
}

/** Выбор из пары значений по текущему языку — для словариков в lib/. */
export function pick<T>(ru: T, en: T): T {
  return currentLang() === 'en' ? en : ru
}
