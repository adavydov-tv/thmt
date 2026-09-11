/** Страница AI-оценки сообщений и вводный гид (онбординг-тур).
 * Плюральные формы — через «|»: ru «день|дня|дней», en «day|days». */
export const aiRu = {
  'ai.title': 'AI-оценка сообщений',
  'ai.subtitle':
    'Локальная модель оценивает содержательность сообщений Slack от 0 до 10; оценка используется фильтром «Мин. AI-оценка» в учёте активности.',
  'ai.statusLabel': 'Состояние модели',
  'ai.disabledBefore': 'AI-скорер выключен: поднимите контейнер (',
  'ai.disabledAfter': ') и проверьте AI_SCORER_URL.',
  'ai.unavailable': 'Скорер недоступен: {error}',
  'ai.model': 'Модель',
  'ai.mode': 'Режим',
  'ai.modeCalibrated': 'откалибрована',
  'ai.modeHeuristic': 'эвристика (мало примеров)',
  'ai.examples': 'Примеров',
  'ai.needAtLeast': '(нужно ≥ {n})',
  'ai.training': 'Обучаю…',
  'ai.retrain': 'Переобучить',
  'ai.rescoreHint': 'Прогнать сообщения Slack выбранного человека за период через модель',
  'ai.scoring': 'Оцениваю…',
  'ai.rescoreFor': 'Переоценить за {from} — {to}',
  'ai.scored': 'оценено',
  'ai.pluralMessages': 'сообщение|сообщения|сообщений',
  'ai.calibrationLabel': 'Калибровка: сообщения из Slack за период',
  'ai.noMessagesTitle': 'Сообщений Slack за период нет',
  'ai.noMessagesDesc': 'Соберите Slack или добавьте примеры вручную ниже.',
  'ai.addManualLabel': 'Добавить пример вручную',
  'ai.manualPlaceholder': 'Текст сообщения…',
  'ai.saveExample': 'Сохранить пример',
  'ai.labelsTitle': 'Примеры калибровки ({n})',
  'ai.noLabels': 'Разметки ещё нет — оцените несколько сообщений выше.',
  'ai.delete': 'Удалить',
  'ai.rulesTitle': 'Автоправила (оценка 0 без прогона через модель)',
  'ai.rulesHint':
    'Паттерн — точное совпадение всего сообщения, без учёта регистра и крайней пунктуации.',
  'ai.minLength': 'Мин. длина обрабатываемого сообщения',
  'ai.emoji': 'Эмодзи',
  'ai.emojiOnly': 'Сообщения из одних эмодзи — сразу 0',
  'ai.patternsLabel': 'Паттерны сообщений (по одному на строку)',
  'ai.saving': 'Сохраняю…',
  'ai.saveRules': 'Сохранить правила',
  'ai.savedNote': 'Сохранено — действует со следующей оценки.',
  'ai.score': 'Оценка',
  'ai.currentScoreHint': 'Текущая оценка модели',
  'ai.labelBtn': 'Разметить',

  'tour.aria': 'Знакомство с дашбордом',
  'tour.introTitle': 'Что это за дашборд',
  'tour.introBody':
    'Здесь собирается активность людей из Jira, GitLab, Slack, Google Docs, календаря, Allure и Confluence, ' +
    'приводится к единым событиям и превращается в метрики: обзор по человеку, сравнение людей и команд, отклонения. ' +
    'Все фильтры живут в URL — любой вид можно отправить коллеге ссылкой.',
  'tour.s1Title': '1 · Заведите людей и соберите данные',
  'tour.s1Body':
    'В «Настройках» добавьте человека через поиск по HRDB (аккаунты во всех системах найдутся по e-mail автоматически) ' +
    'и нажмите «Собрать». Целую команду проще завести на вкладке «Сравнение» кнопкой «Собрать по выбранным командам», ' +
    'а кластер или департамент целиком — на «Отклонениях».',
  'tour.s1Link': 'Открыть настройки',
  'tour.s2Title': '2 · Обзор: активность одного человека',
  'tour.s2Body':
    'KPI, таймлайн, матрица активности с выходными, праздниками (по офису из HRDB), отпусками и больничными. ' +
    'Панель «Учитывается в активности» решает, какие типы событий попадают во все цифры: по умолчанию выбрано всё, ' +
    'кликом вы убираете ненужное. Выбор общий для всех вкладок и переживает перезагрузку.',
  'tour.s3Title': '3 · Сравнение и команды',
  'tour.s3Body':
    '«Сравнение» — карточки людей с волнами активности и трендами, честной нормировкой на рабочие дни (отпуска, больничные ' +
    'и дата найма учтены). «Команды» — сравнение команд, включая средневзвешенный показатель «события на присутствующего». ' +
    'Сравнивайте людей одного направления (Area of Responsibility) — для этого есть фильтр.',
  'tour.s3Link': 'Открыть сравнение',
  'tour.s4Title': '4 · Отклонения',
  'tour.s4Body':
    'Автоматические правила: подозрительные больничные, переработки, молчание, овертаймы без активности, bus-фактор и другие. ' +
    'Красные — требуют внимания, зелёные — информационные и позитивные. Разверните «Что означают отклонения» на самой вкладке — ' +
    'там по каждому правилу написано, что оно значит и что делать.',
  'tour.s4Link': 'Открыть отклонения',
  'tour.s5Title': '5 · Тонкая настройка',
  'tour.s5Body':
    'В «Настройках» правится корпоративный календарь праздников по стране и году (он приоритетнее данных Google). ' +
    'Вкладка «AI» — локальная модель, оценивающая содержательность сообщений Slack: её можно калибровать и использовать ' +
    'фильтром «Мин. AI-оценка». Повторно открыть этот гид — кнопка «?» в шапке.',
  'tour.s5Link': 'К настройкам',
  'tour.skip': 'Пропустить',
  'tour.stepAria': 'Шаг {n}',
  'tour.back': 'Назад',
  'tour.start': 'Начать работу',
  'tour.next': 'Дальше',
} as const

export const aiEn: Record<keyof typeof aiRu, string> = {
  'ai.title': 'AI message scoring',
  'ai.subtitle':
    'A local model rates how substantive Slack messages are, from 0 to 10; the score is used by the “Min. AI score” filter in activity accounting.',
  'ai.statusLabel': 'Model status',
  'ai.disabledBefore': 'AI scorer is off: start the container (',
  'ai.disabledAfter': ') and check AI_SCORER_URL.',
  'ai.unavailable': 'Scorer unavailable: {error}',
  'ai.model': 'Model',
  'ai.mode': 'Mode',
  'ai.modeCalibrated': 'calibrated',
  'ai.modeHeuristic': 'heuristic (too few examples)',
  'ai.examples': 'Examples',
  'ai.needAtLeast': '(need ≥ {n})',
  'ai.training': 'Training…',
  'ai.retrain': 'Retrain',
  'ai.rescoreHint': 'Run the selected person’s Slack messages for the period through the model',
  'ai.scoring': 'Scoring…',
  'ai.rescoreFor': 'Rescore for {from} — {to}',
  'ai.scored': 'scored',
  'ai.pluralMessages': 'message|messages',
  'ai.calibrationLabel': 'Calibration: Slack messages for the period',
  'ai.noMessagesTitle': 'No Slack messages for the period',
  'ai.noMessagesDesc': 'Collect Slack data or add examples manually below.',
  'ai.addManualLabel': 'Add an example manually',
  'ai.manualPlaceholder': 'Message text…',
  'ai.saveExample': 'Save example',
  'ai.labelsTitle': 'Calibration examples ({n})',
  'ai.noLabels': 'No labels yet — rate a few messages above.',
  'ai.delete': 'Delete',
  'ai.rulesTitle': 'Auto rules (score 0 without running the model)',
  'ai.rulesHint':
    'A pattern is an exact match of the whole message, ignoring case and surrounding punctuation.',
  'ai.minLength': 'Min. length of a message to process',
  'ai.emoji': 'Emoji',
  'ai.emojiOnly': 'Emoji-only messages get 0 right away',
  'ai.patternsLabel': 'Message patterns (one per line)',
  'ai.saving': 'Saving…',
  'ai.saveRules': 'Save rules',
  'ai.savedNote': 'Saved — takes effect from the next scoring.',
  'ai.score': 'Score',
  'ai.currentScoreHint': 'Current model score',
  'ai.labelBtn': 'Label',

  'tour.aria': 'Dashboard introduction',
  'tour.introTitle': 'What this dashboard is',
  'tour.introBody':
    'It gathers people’s activity from Jira, GitLab, Slack, Google Docs, the calendar, Allure and Confluence, ' +
    'normalizes it into unified events and turns it into metrics: a per-person overview, comparison of people and teams, deviations. ' +
    'All filters live in the URL — any view can be sent to a colleague as a link.',
  'tour.s1Title': '1 · Add people and collect data',
  'tour.s1Body':
    'In Settings, add a person via HRDB search (their accounts in all systems are found by e-mail automatically) ' +
    'and press “Collect”. A whole team is easier to add on the Compare tab with the “Collect for selected teams” button, ' +
    'and an entire cluster or department — on Deviations.',
  'tour.s1Link': 'Open settings',
  'tour.s2Title': '2 · Overview: one person’s activity',
  'tour.s2Body':
    'KPIs, a timeline, an activity matrix with weekends, holidays (based on the office from HRDB), vacations and sick leaves. ' +
    'The “Counted in activity” panel decides which event types go into all the numbers: everything is selected by default, ' +
    'and a click removes what you don’t need. The selection is shared across all tabs and survives a reload.',
  'tour.s3Title': '3 · Comparison and teams',
  'tour.s3Body':
    '“Compare” shows people cards with activity waves and trends, fairly normalized by working days (vacations, sick leaves ' +
    'and hire date are taken into account). “Teams” compares teams, including the weighted “events per present person” metric. ' +
    'Compare people from the same area (Area of Responsibility) — there is a filter for that.',
  'tour.s3Link': 'Open comparison',
  'tour.s4Title': '4 · Deviations',
  'tour.s4Body':
    'Automatic rules: suspicious sick leaves, overwork, silence, overtime without activity, bus factor and more. ' +
    'Red ones need attention, green ones are informational and positive. Expand “What deviations mean” right on the tab — ' +
    'it explains what each rule means and what to do.',
  'tour.s4Link': 'Open deviations',
  'tour.s5Title': '5 · Fine-tuning',
  'tour.s5Body':
    'Settings lets you edit the corporate holiday calendar by country and year (it takes priority over Google data). ' +
    'The AI tab is a local model that rates how substantive Slack messages are: you can calibrate it and use it ' +
    'via the “Min. AI score” filter. To reopen this guide, press the “?” button in the header.',
  'tour.s5Link': 'To settings',
  'tour.skip': 'Skip',
  'tour.stepAria': 'Step {n}',
  'tour.back': 'Back',
  'tour.start': 'Get started',
  'tour.next': 'Next',
}
