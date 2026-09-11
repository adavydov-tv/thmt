/** Русский словарь интерфейса — собирается из тематических файлов в parts/.
 * Правки переводов — в соответствующем файле части. */
import { coreRu } from './parts/core'
import { compareRu } from './parts/compare'
import { pagesRu } from './parts/pages'
import { aiRu } from './parts/ai'
import { adminRu } from './parts/admin'
import { chartsRu } from './parts/charts'
import { pickersRu } from './parts/pickers'

export const ru = {
  ...coreRu,
  ...compareRu,
  ...pagesRu,
  ...aiRu,
  ...adminRu,
  ...chartsRu,
  ...pickersRu,
} as const

export type MessageKey = keyof typeof ru
