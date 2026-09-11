/** Пикеры фильтров: человек, источники, команды, период и диапазон дат. */
export const pickersRu = {
  'picker.person': 'Человек',
  'picker.loading': 'Загрузка…',
  'picker.noPeople': 'Нет людей',

  'picker.sources': 'Источники',
  'picker.sourceDisabled': 'Источник отключён на бэкенде',
  'picker.reset': 'Сбросить',

  'picker.teams': 'Команды',
  'picker.allTeams': 'Все команды',
  'picker.selected': 'Выбрано: {n}',
  'picker.removeTeam': 'Убрать команду из сравнения',
  'picker.teamsDialog': 'Выбор команд',
  'picker.teamSearch': 'Поиск команды, кластера, департамента…',
  'picker.resetN': 'Сбросить ({n})',
  'picker.nothingFound': 'Ничего не найдено.',
  'picker.noDepartment': 'Без департамента',
  'picker.noCluster': 'Без кластера',

  'picker.days7': '7 дней',
  'picker.days30': '30 дней',
  'picker.days90': '90 дней',
  'picker.currentMonth': 'Текущий месяц',
  'picker.presetsAria': 'Пресеты периода',
  'picker.rangeFrom': 'Начало периода',
  'picker.rangeTo': 'Конец периода',

  'picker.periodKind': 'Вид периода',
  'picker.periodInstance': 'Конкретный период',
  'picker.choose': '— выберите —',
} as const

export const pickersEn: Record<keyof typeof pickersRu, string> = {
  'picker.person': 'Person',
  'picker.loading': 'Loading…',
  'picker.noPeople': 'No people',

  'picker.sources': 'Sources',
  'picker.sourceDisabled': 'Source is disabled on the backend',
  'picker.reset': 'Reset',

  'picker.teams': 'Teams',
  'picker.allTeams': 'All teams',
  'picker.selected': 'Selected: {n}',
  'picker.removeTeam': 'Remove the team from comparison',
  'picker.teamsDialog': 'Team selection',
  'picker.teamSearch': 'Search team, cluster, department…',
  'picker.resetN': 'Reset ({n})',
  'picker.nothingFound': 'Nothing found.',
  'picker.noDepartment': 'No department',
  'picker.noCluster': 'No cluster',

  'picker.days7': '7 days',
  'picker.days30': '30 days',
  'picker.days90': '90 days',
  'picker.currentMonth': 'Current month',
  'picker.presetsAria': 'Period presets',
  'picker.rangeFrom': 'Period start',
  'picker.rangeTo': 'Period end',

  'picker.periodKind': 'Period kind',
  'picker.periodInstance': 'Specific period',
  'picker.choose': '— choose —',
}
