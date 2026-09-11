import type { ViolationItem } from './types'
import { currentLang } from '../i18n/lang'

/** Группы отклонений: смысловая разметка и цветовой слот палитры. */
export interface RuleGroup {
  key: string
  label: string
  /** Индекс категориальной палитры проекта (chartTheme.slot). */
  slot: number
}

// Слоты подобраны перебором как лучшая пятёрка палитры по различимости
// всех пар в обеих темах; группа нигде не кодируется только цветом.
const GROUP_DEFS: { key: string; slot: number; ru: string; en: string }[] = [
  { key: 'sick', slot: 7, ru: 'Трудовая дисциплина', en: 'Work discipline' },
  { key: 'rest', slot: 2, ru: 'Отдых и восстановление', en: 'Rest & recovery' },
  { key: 'overtime', slot: 3, ru: 'Переработки', en: 'Overtime & workload' },
  { key: 'activity', slot: 0, ru: 'Динамика активности', en: 'Activity dynamics' },
  { key: 'team', slot: 5, ru: 'Процессы команды', en: 'Team processes' },
]

/** Локализованный список групп (язык выбирается в момент вызова). */
export function ruleGroups(): RuleGroup[] {
  const en = currentLang() === 'en'
  return GROUP_DEFS.map((g) => ({ key: g.key, slot: g.slot, label: en ? g.en : g.ru }))
}

export function groupOf(key: string): RuleGroup {
  const groups = ruleGroups()
  return groups.find((g) => g.key === key) ?? groups[0]
}

/** Подпись, объяснение, рекомендация и группа каждого правила. */
export interface RuleHelp {
  label: string
  group: string
  what: string
  action: string
}

interface RuleHelpDef {
  group: string
  ru: { label: string; what: string; action: string }
  en: { label: string; what: string; action: string }
}

const RULE_DEFS: Record<ViolationItem['rule'], RuleHelpDef> = {
  sick_adjacent: {
    group: 'sick',
    ru: {
      label: 'Больничный у выходных',
      what: 'Короткий больничный (1–2 дня) прямо перед выходными/праздником, сразу после них или между ними.',
      action: 'Возможное продление выходных. Посмотрите историю: разово — норма, регулярно — поговорите с сотрудником.',
    },
    en: {
      label: 'Sick day adjacent to weekend',
      what: 'A short sick leave (1–2 days) right before, right after, or between weekends/holidays.',
      action: 'Possible weekend extension. Check the history: once is fine, a pattern deserves a conversation.',
    },
  },
  sick_monthly: {
    group: 'sick',
    ru: {
      label: 'Больничные из месяца в месяц',
      what: 'Больничные повторяются два и более месяца подряд.',
      action: 'Либо реальные проблемы со здоровьем, либо скрытое выгорание. Мягкий разговор 1:1 и, при необходимости, план разгрузки.',
    },
    en: {
      label: 'Sick leaves month after month',
      what: 'Sick leaves repeat for two or more consecutive months.',
      action: 'Either genuine health issues or hidden burnout. A gentle 1:1 and, if needed, a workload relief plan.',
    },
  },
  overtime_idle: {
    group: 'overtime',
    ru: {
      label: 'Овертайм без активности',
      what: 'Заявка на оплачиваемый овертайм есть, а событий в системах в эти дни ноль.',
      action: 'Сверьте с сотрудником, чем занимался овертайм: возможно, работа не оставляет следов в системах — или заявка ошибочна.',
    },
    en: {
      label: 'Overtime with zero activity',
      what: 'A paid overtime request exists, but there are no events in any system on those days.',
      action: 'Check what the overtime covered: the work may leave no traces in tracked systems — or the request is wrong.',
    },
  },
  overtime_low: {
    group: 'overtime',
    ru: {
      label: 'Овертайм с мин. активностью',
      what: 'В дни заявленного овертайма всего 1–2 события.',
      action: 'Аналогично «без активности», но мягче: уточните характер работ прежде чем делать выводы.',
    },
    en: {
      label: 'Overtime with minimal activity',
      what: 'Only 1–2 events on the days of a claimed overtime.',
      action: 'Same as “zero activity”, but softer: clarify the nature of the work before drawing conclusions.',
    },
  },
  offday_activity: {
    group: 'overtime',
    ru: {
      label: 'Работа в выходные без овертайма',
      what: 'Активность в выходные или праздники без заявки на овертайм.',
      action: 'Информация для лида: действия нужны не всегда. Если это регулярно — обсудите нагрузку или напомните оформить овертайм.',
    },
    en: {
      label: 'Weekend work without overtime',
      what: 'Activity on weekends or holidays without an overtime request.',
      action: 'For the lead’s awareness: action is not always needed. If regular — discuss workload or remind to file overtime.',
    },
  },
  no_vacation: {
    group: 'rest',
    ru: {
      label: 'Давно не был в отпуске',
      what: 'С последнего отпуска (или с найма) прошло более 3 месяцев; более 6 — красный уровень.',
      action: 'Напомните про отпуск и помогите спланировать: длительная работа без отдыха — прямой путь к выгоранию.',
    },
    en: {
      label: 'No vacation for a long time',
      what: 'More than 3 months since the last vacation (or since hiring); more than 6 is the red level.',
      action: 'Remind about vacation and help plan it: long stretches without rest are a straight road to burnout.',
    },
  },
  idle_streak: {
    group: 'activity',
    ru: {
      label: 'Молчание в рабочие дни',
      what: 'Более 2 рабочих дней подряд без единого события — отпуск, больничный и выходные не считаются.',
      action: 'Проверьте, всё ли в порядке: возможно, работа вне систем (встречи, исследование) или несобранные данные.',
    },
    en: {
      label: 'Silent working days',
      what: 'More than 2 consecutive working days without a single event — vacation, sick leave and weekends excluded.',
      action: 'Check that everything is fine: the work may be outside tracked systems (meetings, research) or data is missing.',
    },
  },
  slack_only_days: {
    group: 'activity',
    ru: {
      label: 'Активность только в Slack',
      what: 'Рабочие дни, где вся активность человека — сообщения и реакции Slack: ни задач, ни кода, ни документов.',
      action: 'Уточните, чем был занят день: обсуждения без результата в системах — повод разобраться в загрузке.',
    },
    en: {
      label: 'Slack-only activity',
      what: 'Working days where all activity is Slack messages and reactions — no issues, code, or documents.',
      action: 'Check what the day was spent on: discussion without tracked output is worth a look.',
    },
  },
  low_activity_days: {
    group: 'activity',
    ru: {
      label: 'Дни с низкой активностью',
      what: 'Рабочие дни, где вся активность — «поверхностные» действия (чат, входы, непосещённые встречи). Набор настраивается на вкладке «Низкая активность».',
      action: 'Разберитесь в причинах: работа вне систем, нехватка задач или несобранные данные.',
    },
    en: {
      label: 'Low-activity days',
      what: 'Working days where all activity is “shallow” (chat, logins, unattended meetings). The set is configured on the “Low activity” tab.',
      action: 'Find out the cause: work outside tracked systems, lack of tasks, or missing data.',
    },
  },
  role_vacation_overlap: {
    group: 'rest',
    ru: {
      label: 'Роль в отпуске одновременно',
      what: 'Больше половины сотрудников одной роли в команде в отпуске в одни и те же дни.',
      action: 'Риск для процессов команды. Согласуйте графики отпусков заранее.',
    },
    en: {
      label: 'Role on vacation simultaneously',
      what: 'More than half of the team members of one role are on vacation on the same days.',
      action: 'A risk to team processes. Coordinate vacation schedules in advance.',
    },
  },
  work_on_vacation: {
    group: 'rest',
    ru: {
      label: 'Работа в отпуске/на больничном',
      what: 'Активность в дни, помеченные как отпуск или больничный.',
      action: 'Отдых не случился. Разберитесь, что заставило работать: срочность, привычка или нехватка замены.',
    },
    en: {
      label: 'Working on vacation / sick leave',
      what: 'Activity on days marked as vacation or sick leave.',
      action: 'The rest did not happen. Find out what forced the work: urgency, habit, or lack of backup.',
    },
  },
  long_workday: {
    group: 'overtime',
    ru: {
      label: 'Рабочий день длиннее 9 часов',
      what: 'От первого до последнего события дня прошло больше 9 часов — и таких дней три и более.',
      action: 'Признак переработок. Обсудите нагрузку; длинный день не обязательно значит непрерывную работу, но регулярность настораживает.',
    },
    en: {
      label: 'Workday longer than 9 hours',
      what: 'More than 9 hours between the first and last event of the day — on three or more days.',
      action: 'A sign of overwork. Discuss workload; a long day is not necessarily continuous work, but regularity is a flag.',
    },
  },
  activity_drop: {
    group: 'activity',
    ru: {
      label: 'Резкое падение активности',
      what: 'Вторая половина периода вдвое (и более) ниже первой.',
      action: 'Выясните причину: смена задач, блокеры, демотивация или уход активности в системы, которые не собираются.',
    },
    en: {
      label: 'Sharp activity drop',
      what: 'The second half of the period is at least twice lower than the first.',
      action: 'Find the cause: task change, blockers, demotivation, or activity moving to untracked systems.',
    },
  },
  remote_zero: {
    group: 'activity',
    ru: {
      label: 'Нулевая активность в день из дома',
      what: 'В гибридный день из дома (Hybrid remote days в HRDB) не найдено ни одного события, хотя в другие дни активность есть.',
      action: 'Уточните, чем занят день из дома: возможно, работа в системах, которые не собираются, либо день фактически выходной.',
    },
    en: {
      label: 'Zero activity on a remote day',
      what: 'No events on a hybrid remote day (Hybrid remote days in HRDB), although other days show activity.',
      action: 'Clarify how the remote day is spent: work may happen in untracked systems, or the day is effectively a day off.',
    },
  },
  remote_drop: {
    group: 'activity',
    ru: {
      label: 'Снижение активности из дома',
      what: 'В гибридные дни из дома в среднем меньше половины событий по сравнению с офисными рабочими днями.',
      action: 'Сравните характер задач в офисе и дома. Если разрыв стабильный — обсудите организацию удалённых дней.',
    },
    en: {
      label: 'Lower activity on remote days',
      what: 'Hybrid remote days average less than half the events of office working days.',
      action: 'Compare the nature of tasks at the office and at home. If the gap is stable — discuss how remote days are organized.',
    },
  },
  wfh_zero: {
    group: 'activity',
    ru: {
      label: 'Нулевая активность в WFH-день',
      what: 'В день работы из дома по одобренной заявке VAC (Paid: Work From Home) не найдено ни одного события, хотя в другие дни активность есть.',
      action: 'Надёжный сигнал (данные из заявки, не из шаблона): уточните, чем занят день WFH — работа в несобираемых системах или день фактически нерабочий.',
    },
    en: {
      label: 'Zero activity on approved WFH day',
      what: 'No events on a day marked work-from-home by an approved VAC request (Paid: Work From Home), although other days show activity.',
      action: 'A reliable signal (from the request, not a pattern): clarify how the WFH day was spent — untracked systems or effectively a day off.',
    },
  },
  slow_review: {
    group: 'team',
    ru: {
      label: 'MR долго без ревью',
      what: 'Merge request без первого отклика (комментарий, апрув, мёрж) более 3 суток.',
      action: 'Проблема процесса, а не автора: напомните команде про ревью или назначьте ответственных.',
    },
    en: {
      label: 'MR waiting for review too long',
      what: 'A merge request without a first response (comment, approval, merge) for more than 3 days.',
      action: 'A process problem, not the author’s: remind the team about reviews or assign owners.',
    },
  },
  bus_factor: {
    group: 'team',
    ru: {
      label: 'Bus-фактор',
      what: 'Более 70% событий команды делает один человек.',
      action: 'Риск концентрации знаний. Распределяйте задачи и подключайте остальных к ключевым областям.',
    },
    en: {
      label: 'Bus factor',
      what: 'More than 70% of the team’s events come from one person.',
      action: 'A knowledge concentration risk. Spread the tasks and involve others in the key areas.',
    },
  },
  no_reviewers: {
    group: 'team',
    ru: {
      label: 'Некому ревьюить',
      what: 'В команде с активной разработкой ревью-комментарии пишет максимум один человек.',
      action: 'Введите правило перекрёстного ревью — одна пара глаз — это отсутствие ревью.',
    },
    en: {
      label: 'Nobody to review',
      what: 'In a team with active development, at most one person writes review comments.',
      action: 'Introduce cross-review: a single pair of eyes means no review at all.',
    },
  },
  role_inactive: {
    group: 'team',
    ru: {
      label: 'Роль целиком молчит',
      what: 'Все сотрудники роли команды одновременно без активности 3+ рабочих дня, хотя присутствуют.',
      action: 'Проверьте загрузку роли: возможно, ждут смежников или их работа не видна системам.',
    },
    en: {
      label: 'Entire role is silent',
      what: 'All members of a team role show no activity for 3+ working days while being present.',
      action: 'Check the role’s workload: they may be waiting on others, or their work is invisible to tracked systems.',
    },
  },
  steady_rhythm: {
    group: 'activity',
    ru: {
      label: 'Стабильный ритм',
      what: 'Активность почти каждый рабочий день, без работы в выходные, отпуске и без затяжных дней.',
      action: 'Здоровый паттерн — отметьте и не ломайте: так выглядит устойчивая продуктивность.',
    },
    en: {
      label: 'Steady rhythm',
      what: 'Activity on almost every working day, no weekend or vacation work, no marathon days.',
      action: 'A healthy pattern — acknowledge it and do not break it: this is what sustainable productivity looks like.',
    },
  },
  review_champion: {
    group: 'team',
    ru: {
      label: 'Ревью-чемпион',
      what: 'Ревью-комментариев минимум вдвое больше среднего по роли.',
      action: 'Вклад, который часто не виден в задачах, — повод для признания.',
    },
    en: {
      label: 'Review champion',
      what: 'At least twice as many review comments as the role average.',
      action: 'A contribution that rarely shows up in tickets — worth recognizing.',
    },
  },
  onboarding_rise: {
    group: 'activity',
    ru: {
      label: 'Рост после найма',
      what: 'Новичок наращивает активность от месяца к месяцу.',
      action: 'Онбординг работает — можно ставить более амбициозные задачи.',
    },
    en: {
      label: 'Ramping up after hire',
      what: 'A newcomer’s activity grows month over month.',
      action: 'Onboarding works — time for more ambitious tasks.',
    },
  },
  onboarding_flat: {
    group: 'activity',
    ru: {
      label: 'Нет роста после найма',
      what: 'Новичок в компании больше месяца, а событий почти нет.',
      action: 'Проверьте онбординг: доступы, наставника, понятность задач. Либо его работа не попадает в собираемые системы.',
    },
    en: {
      label: 'No ramp-up after hire',
      what: 'A newcomer has been with the company for over a month with almost no events.',
      action: 'Check the onboarding: access, mentor, task clarity. Or their work does not reach the tracked systems.',
    },
  },
  mentor_one_on_ones: {
    group: 'team',
    ru: {
      label: '1:1 с разными людьми',
      what: 'Разовые встречи 1:1 с тремя и более разными собеседниками.',
      action: 'Похоже на наставничество или активную коммуникацию — ценный вклад в команду.',
    },
    en: {
      label: '1:1s with many people',
      what: 'One-off 1:1 meetings with three or more different people.',
      action: 'Looks like mentoring or active communication — a valuable contribution to the team.',
    },
  },
  vacation_recovery: {
    group: 'rest',
    ru: {
      label: 'Вернулся в ритм после отпуска',
      what: 'После отпуска от трёх дней активность возобновилась в первые же рабочие дни.',
      action: 'Хороший знак: отпуск отработал как отдых, а не как побег.',
    },
    en: {
      label: 'Back in rhythm after vacation',
      what: 'After a vacation of three days or more, activity resumed within the first working days.',
      action: 'A good sign: the vacation worked as rest, not as an escape.',
    },
  },
}

/** Локализованный справочник правил (язык выбирается в момент вызова). */
export function ruleHelpEntries(): [string, RuleHelp][] {
  const en = currentLang() === 'en'
  return Object.entries(RULE_DEFS).map(([key, def]) => {
    const loc = en ? def.en : def.ru
    return [key, { group: def.group, label: loc.label, what: loc.what, action: loc.action }]
  })
}

function helpOf(rule: string): RuleHelp | undefined {
  const def = RULE_DEFS[rule as ViolationItem['rule']]
  if (!def) return undefined
  const loc = currentLang() === 'en' ? def.en : def.ru
  return { group: def.group, label: loc.label, what: loc.what, action: loc.action }
}

export function ruleLabel(rule: string): string {
  return helpOf(rule)?.label ?? rule
}

export function ruleHint(rule: string): string {
  const h = helpOf(rule)
  if (!h) return ''
  const prefix = currentLang() === 'en' ? 'What to do' : 'Что делать'
  return `${h.what}\n\n${prefix}: ${h.action}`
}

/** Группа, к которой относится правило. */
export function ruleGroupOf(rule: string): RuleGroup {
  return groupOf(RULE_DEFS[rule as ViolationItem['rule']]?.group ?? '')
}
