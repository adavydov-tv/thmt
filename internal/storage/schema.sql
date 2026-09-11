-- Схема хранилища активности. Применяется идемпотентно при старте сервиса
-- (POSTGRES_MIGRATE_ON_START=true) либо руками: psql -f internal/storage/schema.sql

CREATE TABLE IF NOT EXISTS people (
    id              BIGSERIAL PRIMARY KEY,
    key             TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    jira_account_id TEXT NOT NULL DEFAULT '',
    gitlab_username TEXT NOT NULL DEFAULT '',
    slack_user_id   TEXT NOT NULL DEFAULT '',
    google_email    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
    id           TEXT PRIMARY KEY,
    person_key   TEXT NOT NULL,
    source       TEXT NOT NULL,
    type         TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    project      TEXT NOT NULL DEFAULT '',
    project_name TEXT NOT NULL DEFAULT '',
    ref_id       TEXT NOT NULL DEFAULT '',
    parent_ref_id TEXT NOT NULL DEFAULT '',
    effort       DOUBLE PRECISION NOT NULL DEFAULT 0,
    effort_unit  TEXT NOT NULL DEFAULT '',
    meta         JSONB NOT NULL DEFAULT '{}'::jsonb,
    ingested_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS events_person_time_idx ON events (person_key, occurred_at DESC);
CREATE INDEX IF NOT EXISTS events_person_source_time_idx ON events (person_key, source, occurred_at DESC);
CREATE INDEX IF NOT EXISTS events_type_idx ON events (person_key, type);
CREATE INDEX IF NOT EXISTS events_project_idx ON events (person_key, project);
CREATE INDEX IF NOT EXISTS events_ref_idx ON events (person_key, ref_id);
CREATE INDEX IF NOT EXISTS events_parent_ref_idx ON events (parent_ref_id) WHERE parent_ref_id <> '';

-- Полнотекстовый поиск по заголовку и телу события (для строки поиска в ленте).
CREATE INDEX IF NOT EXISTS events_search_idx
    ON events USING gin (to_tsvector('simple', title || ' ' || body));

-- Google-документы (TSD), найденные по ссылкам в задачах Jira.
CREATE TABLE IF NOT EXISTS doc_links (
    doc_id        TEXT NOT NULL,
    person_key    TEXT NOT NULL,
    issue_key     TEXT NOT NULL DEFAULT '',
    doc_url       TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    issue_title   TEXT NOT NULL DEFAULT '',
    found_in      TEXT NOT NULL DEFAULT '',
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    enriched      BOOLEAN NOT NULL DEFAULT false,
    last_modified TIMESTAMPTZ,
    edit_count    INTEGER NOT NULL DEFAULT 0,
    comment_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (doc_id, person_key, issue_key)
);

CREATE INDEX IF NOT EXISTS doc_links_person_idx ON doc_links (person_key);
CREATE INDEX IF NOT EXISTS doc_links_issue_idx ON doc_links (person_key, issue_key);

CREATE TABLE IF NOT EXISTS sync_runs (
    id          TEXT PRIMARY KEY,
    person_key  TEXT NOT NULL,
    range_from  TIMESTAMPTZ NOT NULL,
    range_to    TIMESTAMPTZ NOT NULL,
    status      TEXT NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    sources     JSONB NOT NULL DEFAULT '{}'::jsonb,
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS sync_runs_person_idx ON sync_runs (person_key, started_at DESC);

-- Поля из HRDB: офис определяет праздничный календарь, команда и направление
-- (Area of Responsibility) — фильтры страницы сравнения продуктивности.
ALTER TABLE people ADD COLUMN IF NOT EXISTS office TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN IF NOT EXISTS team TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN IF NOT EXISTS area TEXT NOT NULL DEFAULT '';
-- Дата найма: дни до неё не считаются рабочими.
ALTER TABLE people ADD COLUMN IF NOT EXISTS hire_date DATE;
-- Дни недели работы из дома (Hybrid remote days из HRDB).
ALTER TABLE people ADD COLUMN IF NOT EXISTS hybrid_days TEXT NOT NULL DEFAULT '';

-- Пользователи дашборда и их роли (viewer | lead | supervisor | administrator).
-- Аутентификация — Google OIDC, авторизация — по этой таблице.
CREATE TABLE IF NOT EXISTS users (
    email      TEXT PRIMARY KEY,
    role       TEXT NOT NULL DEFAULT 'viewer',
    added_by   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Архив сообщений Slack по КАНАЛАМ (все авторы, не только заведённые люди):
-- один обход канала записывается сюда навсегда, добавление нового человека —
-- это только «линковка» его slack_uid к уже собранным строкам, без Slack API.
-- Текст хранится зашифрованным (AES-GCM, ключ SLACK_TEXT_KEY) и читается
-- только администратором по явному запросу; всем остальным — id + AI-оценка.
CREATE TABLE IF NOT EXISTS slack_messages (
    id           TEXT PRIMARY KEY,   -- msg:<ch>:<ts> | reaction:<ch>:<ts>:<name>:<uid>
    channel_id   TEXT NOT NULL,
    channel_name TEXT NOT NULL DEFAULT '',
    slack_uid    TEXT NOT NULL,      -- автор сообщения либо поставивший реакцию
    kind         TEXT NOT NULL,      -- message | reply | reaction
    ts           TIMESTAMPTZ NOT NULL,
    slack_ts     TEXT NOT NULL DEFAULT '',
    thread_ts    TEXT NOT NULL DEFAULT '',
    text_enc     BYTEA,
    url          TEXT NOT NULL DEFAULT '',
    reply_count  INTEGER NOT NULL DEFAULT 0,
    reactions    INTEGER NOT NULL DEFAULT 0,
    reaction     TEXT NOT NULL DEFAULT '',
    ai_score     DOUBLE PRECISION,
    ingested_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS slack_messages_uid_ts_idx ON slack_messages (slack_uid, ts);
CREATE INDEX IF NOT EXISTS slack_messages_chan_ts_idx ON slack_messages (channel_id, ts);
CREATE INDEX IF NOT EXISTS slack_messages_unscored_idx ON slack_messages (ingested_at)
    WHERE ai_score IS NULL AND kind <> 'reaction';

-- Покрытие канал-центричного архива Slack: какие интервалы истории каждого
-- канала уже выкачаны. Пересекающиеся/смежные интервалы сливаются при записи;
-- сбор обходит по API только дыры между ними (+ свежий хвост SLACK_RESCAN_DAYS).
CREATE TABLE IF NOT EXISTS slack_coverage (
    channel_id TEXT        NOT NULL,
    from_ts    TIMESTAMPTZ NOT NULL,
    to_ts      TIMESTAMPTZ NOT NULL,
    walked_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, from_ts)
);

-- Настройки приложения, меняемые из UI (например, активная HRDB).
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Снапшоты кэшей HRDB (сотрудники/структура/бывшие) по источникам: после
-- рестарта сервис отвечает из снапшота мгновенно, свежесть догоняет фоном.
CREATE TABLE IF NOT EXISTS hrdb_cache (
    mode       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    payload    JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (mode, kind)
);

-- Чёрный список: удалённые вручную люди. Синк команды их не пересоздаёт,
-- «Удалить уволенных» удаляет независимо от статуса в HRDB (там запись
-- может отставать от реальности — человек уволен, а тип всё ещё Employee).
CREATE TABLE IF NOT EXISTS excluded_people (
    email      TEXT PRIMARY KEY,
    person_key TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Особые дни человека: государственные праздники и отпуска (kind =
-- holiday | vacation). Выходные вычисляются на лету и здесь не хранятся.
CREATE TABLE IF NOT EXISTS person_days (
    person_key TEXT NOT NULL,
    day        DATE NOT NULL,
    kind       TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (person_key, day, kind)
);

CREATE INDEX IF NOT EXISTS person_days_person_idx ON person_days (person_key, day);
-- Источник особого дня: gcal (календарь) | vacjira (заявки VAC старой Jira).
-- Каждый источник перезаписывает только свои дни (ReplacePersonDaysForSource).
ALTER TABLE person_days ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';

-- История изменений шаблона гибридных дней («Hybrid remote days» из журнала
-- Assets): в момент changed_at значение сменилось с old_days на new_days.
-- Правила отклонений применяют шаблон, действовавший на конкретную дату.
CREATE TABLE IF NOT EXISTS hybrid_history (
    person_key TEXT        NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL,
    old_days   TEXT        NOT NULL DEFAULT '',
    new_days   TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (person_key, changed_at)
);

-- История изменений формата работы (Office | Hybrid | Remote): в ремоут- и
-- офис-периоды гибридный шаблон дней из дома не действует. Схема как у
-- hybrid_history; для постоянных ремоутов пишется синтетическая точка
-- с changed_at в эпохе.
CREATE TABLE IF NOT EXISTS work_format_history (
    person_key TEXT        NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL,
    old_days   TEXT        NOT NULL DEFAULT '',
    new_days   TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (person_key, changed_at)
);

-- Корпоративный календарь праздников: правится руками в UI. Если для страны
-- и года здесь есть хотя бы одна запись, весь этот год берётся отсюда, а
-- праздники из Google для него игнорируются.
CREATE TABLE IF NOT EXISTS company_holidays (
    country TEXT NOT NULL,
    day     DATE NOT NULL,
    label   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (country, day)
);

-- Инкрементальное покрытие сбора по паре (человек, источник): докуда уже
-- собрано. Прогон тянет только [max(from, covered_to − перекрытие), to] и
-- двигает covered_to к to; «перекрытие» (в днях, на систему) перепроверяет
-- хвост на случай поздних событий. Флаг перезаписи покрытие игнорирует.
CREATE TABLE IF NOT EXISTS source_coverage (
    person_key TEXT        NOT NULL,
    source     TEXT        NOT NULL,
    covered_to TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (person_key, source)
);
