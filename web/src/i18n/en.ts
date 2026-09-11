/** Английский словарь — те же ключи, что в ru.ts: TypeScript проверяет полноту
 * перевода (Record<MessageKey, string>). Правки — в файлах parts/. */
import { coreEn } from './parts/core'
import { compareEn } from './parts/compare'
import { pagesEn } from './parts/pages'
import { aiEn } from './parts/ai'
import { adminEn } from './parts/admin'
import { chartsEn } from './parts/charts'
import { pickersEn } from './parts/pickers'
import type { MessageKey } from './ru'

export const en: Record<MessageKey, string> = {
  ...coreEn,
  ...compareEn,
  ...pagesEn,
  ...aiEn,
  ...adminEn,
  ...chartsEn,
  ...pickersEn,
}
