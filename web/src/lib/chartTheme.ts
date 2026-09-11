import { useMemo } from 'react'
import { pick } from '../i18n/lang'
import { useTheme, type ThemeMode } from './theme'
import type { SourceKey } from './types'

/**
 * Валидированная палитра. Значения продублированы в styles.css как CSS-переменные,
 * но Recharts получает конкретные значения отсюда.
 */
const CATEGORICAL: Record<ThemeMode, string[]> = {
  light: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#4a3aa7', '#e34948'],
  dark: ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'],
}

/** Секвенциальная шкала (один синий hue, светлое → тёмное). */
export const SEQUENTIAL = [
  '#cde2fb',
  '#b7d3f6',
  '#9ec5f4',
  '#86b6ef',
  '#6da7ec',
  '#5598e7',
  '#3987e5',
  '#2a78d6',
  '#256abf',
  '#1c5cab',
  '#184f95',
  '#104281',
  '#0d366b',
]

/** Шкала «поверхностных» дней матрицы активности (только Slack и входы):
 *  розово-красная, той же длины, что SEQUENTIAL — насыщенность сравнима
 *  с синей шкалой при том же числе событий. Жёлто-оранжевую не берём:
 *  сливалась бы с янтарным пунктиром WFH и точкой овертайма. */
export const SEQUENTIAL_SHALLOW = [
  '#fbd4dc',
  '#f8c0cb',
  '#f5abba',
  '#f096a8',
  '#ea8096',
  '#e36a84',
  '#da5472',
  '#cf3f60',
  '#c02f50',
  '#ad2745',
  '#98203c',
  '#831b33',
  '#6e162b',
]

/** Цвет закреплён за источником, а не за порядком в выборке. */
const SOURCE_SLOT: Record<SourceKey, number> = {
  jira: 0,
  gitlab: 1,
  slack: 2,
  gdocs: 3,
  gcal: 4,
  allure: 5,
  confluence: 6,
  gwork: 4,
  argocd: 3,
  zabbix: 1,
  jenkins: 5,
  grafana: 2,
  figma: 6,
  netsuite: 7,
  claude: 0,
}

// Подписи источников зависят от языка (gcal), поэтому наружу отдаётся только
// функция sourceLabel() — константу нельзя замораживать при импорте.
const SOURCE_LABELS_RU: Record<SourceKey, string> = {
  jira: 'Jira',
  gitlab: 'GitLab',
  slack: 'Slack',
  gdocs: 'Google Docs',
  gcal: 'Календарь',
  allure: 'Allure',
  confluence: 'Confluence',
  gwork: 'Google Workspace',
  argocd: 'Argo CD',
  zabbix: 'Zabbix',
  jenkins: 'Jenkins',
  grafana: 'Grafana',
  figma: 'Figma',
  netsuite: 'NetSuite',
  claude: 'Claude Code',
}

const SOURCE_LABELS_EN: Record<SourceKey, string> = {
  ...SOURCE_LABELS_RU,
  gcal: 'Calendar',
}

export const SOURCE_KEYS: SourceKey[] = [
  'jira',
  'gitlab',
  'slack',
  'gdocs',
  'gcal',
  'allure',
  'confluence',
  'gwork',
  'argocd',
  'zabbix',
  'jenkins',
  'grafana',
  'figma',
  'netsuite',
  'claude',
]

export function isSourceKey(value: string): value is SourceKey {
  return (SOURCE_KEYS as string[]).includes(value)
}

export function sourceLabel(key: string): string {
  return isSourceKey(key) ? pick(SOURCE_LABELS_RU, SOURCE_LABELS_EN)[key] : key
}

const CHROME = {
  light: {
    surface: '#fcfcfb',
    page: '#f9f9f7',
    text: '#0b0b0b',
    textSecondary: '#52514e',
    textMuted: '#898781',
    grid: '#e1e0d9',
    axis: '#c3c2b7',
    border: 'rgba(11,11,11,0.10)',
    positive: '#006300',
    negative: '#d03b3b',
  },
  dark: {
    surface: '#1a1a19',
    page: '#0d0d0d',
    text: '#ffffff',
    textSecondary: '#c3c2b7',
    textMuted: '#898781',
    grid: '#2c2c2a',
    axis: '#383835',
    border: 'rgba(255,255,255,0.10)',
    positive: '#0ca30c',
    negative: '#d03b3b',
  },
} as const

export interface ChartTokens {
  mode: ThemeMode
  surface: string
  page: string
  text: string
  textSecondary: string
  textMuted: string
  grid: string
  axis: string
  border: string
  positive: string
  negative: string
  categorical: string[]
  /** Цвет источника — фиксированный слот. */
  sourceColor: (source: string) => string
  /** Цвет для произвольной категории по её позиции (типы событий, проекты). */
  slot: (index: number) => string
  /** Шаг секвенциальной шкалы; 0 → цвет поверхности. */
  sequential: (ratio: number) => string
}

export function useChartTokens(): ChartTokens {
  const { mode } = useTheme()
  return useMemo<ChartTokens>(() => {
    const chrome = CHROME[mode]
    const palette = CATEGORICAL[mode]
    return {
      mode,
      ...chrome,
      categorical: palette,
      sourceColor: (source: string) =>
        isSourceKey(source) ? palette[SOURCE_SLOT[source]] : palette[(hashIndex(source) % palette.length)],
      slot: (index: number) => palette[((index % palette.length) + palette.length) % palette.length],
      sequential: (ratio: number) => {
        if (ratio <= 0) return chrome.surface
        const idx = Math.min(SEQUENTIAL.length - 1, Math.max(0, Math.round(ratio * (SEQUENTIAL.length - 1))))
        return SEQUENTIAL[idx]
      },
    }
  }, [mode])
}

function hashIndex(value: string): number {
  let h = 0
  for (let i = 0; i < value.length; i += 1) {
    h = (h * 31 + value.charCodeAt(i)) >>> 0
  }
  return h
}
