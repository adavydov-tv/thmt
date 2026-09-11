// Package storage — слой доступа к PostgreSQL: миграции, запись событий и
// аналитические выборки для дашборда.
package storage

import (
	"context"
	"crypto/cipher"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

//go:embed schema.sql
var schemaSQL string

// Store — репозиторий поверх пула соединений pgx.
type Store struct {
	pool *pgxpool.Pool
	// textAEAD — шифр текстов Slack-архива (nil — тексты не шифруются).
	textAEAD cipher.AEAD
}

// New открывает пул соединений и, если включено, применяет схему.
func New(ctx context.Context, cfg config.PostgresConfig) (*Store, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("разбор POSTGRES_DSN: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	pc.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("подключение к postgres: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres недоступен: %w", err)
	}

	s := &Store{pool: pool}
	if cfg.MigrateOnStart {
		if err := s.Migrate(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return s, nil
}

// Migrate применяет схему (идемпотентно).
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("применение схемы: %w", err)
	}
	return nil
}

// Close закрывает пул.
func (s *Store) Close() { s.pool.Close() }

// Ping проверяет доступность БД.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---------- people ----------

// UpsertPerson создаёт или обновляет человека. Пустые office/team/area не
// затирают сохранённые: они заполняются из HRDB, и клиенты их обычно не
// присылают.
func (s *Store) UpsertPerson(ctx context.Context, p models.Person) (models.Person, error) {
	const q = `
INSERT INTO people (key, display_name, email, jira_account_id, gitlab_username, slack_user_id, google_email, office, team, area, hire_date, hybrid_days)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (key) DO UPDATE SET
    display_name    = EXCLUDED.display_name,
    email           = EXCLUDED.email,
    jira_account_id = EXCLUDED.jira_account_id,
    gitlab_username = EXCLUDED.gitlab_username,
    slack_user_id   = EXCLUDED.slack_user_id,
    google_email    = EXCLUDED.google_email,
    office          = CASE WHEN EXCLUDED.office <> '' THEN EXCLUDED.office ELSE people.office END,
    team            = CASE WHEN EXCLUDED.team <> '' THEN EXCLUDED.team ELSE people.team END,
    area            = CASE WHEN EXCLUDED.area <> '' THEN EXCLUDED.area ELSE people.area END,
    hire_date       = COALESCE(EXCLUDED.hire_date, people.hire_date),
    hybrid_days     = CASE WHEN EXCLUDED.hybrid_days <> '' THEN EXCLUDED.hybrid_days ELSE people.hybrid_days END,
    updated_at      = now()
RETURNING id, key, display_name, email, jira_account_id, gitlab_username, slack_user_id, google_email, office, team, area, hire_date, hybrid_days, created_at, updated_at`
	var out models.Person
	err := s.pool.QueryRow(ctx, q, p.Key, p.DisplayName, p.Email, p.JiraAccount, p.GitLabUser, p.SlackUser, p.GoogleEmail, p.Office, p.Team, p.Area, p.HireDate, p.HybridDays).
		Scan(&out.ID, &out.Key, &out.DisplayName, &out.Email, &out.JiraAccount, &out.GitLabUser, &out.SlackUser, &out.GoogleEmail, &out.Office, &out.Team, &out.Area, &out.HireDate, &out.HybridDays, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return models.Person{}, fmt.Errorf("upsert person %q: %w", p.Key, err)
	}
	return out, nil
}

// SetPersonHRDB проставляет поля из HRDB, не трогая остальные. Пустые
// значения не затирают сохранённые.
func (s *Store) SetPersonHRDB(ctx context.Context, key, office, team, area, hybridDays string, hireDate *time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE people SET
    office    = CASE WHEN $2 <> '' THEN $2 ELSE office END,
    team      = CASE WHEN $3 <> '' THEN $3 ELSE team END,
    area      = CASE WHEN $4 <> '' THEN $4 ELSE area END,
    hire_date = COALESCE($5, hire_date),
    hybrid_days = CASE WHEN $6 <> '' THEN $6 ELSE hybrid_days END,
    updated_at = now()
WHERE key = $1`, key, office, team, area, hireDate, hybridDays)
	return err
}

// ListPeople возвращает всех известных сервису людей.
func (s *Store) ListPeople(ctx context.Context) ([]models.Person, error) {
	const q = `SELECT id, key, display_name, email, jira_account_id, gitlab_username, slack_user_id, google_email, office, team, area, hire_date, hybrid_days, created_at, updated_at
	           FROM people ORDER BY display_name, key`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Person
	for rows.Next() {
		var p models.Person
		if err := rows.Scan(&p.ID, &p.Key, &p.DisplayName, &p.Email, &p.JiraAccount, &p.GitLabUser, &p.SlackUser, &p.GoogleEmail, &p.Office, &p.Team, &p.Area, &p.HireDate, &p.HybridDays, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPerson возвращает человека по ключу.
func (s *Store) GetPerson(ctx context.Context, key string) (models.Person, error) {
	const q = `SELECT id, key, display_name, email, jira_account_id, gitlab_username, slack_user_id, google_email, office, team, area, hire_date, hybrid_days, created_at, updated_at
	           FROM people WHERE key = $1`
	var p models.Person
	err := s.pool.QueryRow(ctx, q, key).Scan(&p.ID, &p.Key, &p.DisplayName, &p.Email, &p.JiraAccount, &p.GitLabUser, &p.SlackUser, &p.GoogleEmail, &p.Office, &p.Team, &p.Area, &p.HireDate, &p.HybridDays, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return models.Person{}, ErrNotFound
		}
		return models.Person{}, err
	}
	return p, nil
}

// ErrNotFound — сущность отсутствует.
var ErrNotFound = fmt.Errorf("не найдено")

// SetPersonSourceID сохраняет найденный при сборе аккаунт человека в
// источнике (например, Slack user id) — только если поле ещё пустое:
// заданное вручную значение не перезаписываем.
func (s *Store) SetPersonSourceID(ctx context.Context, key, source, id string) error {
	col := map[string]string{
		"slack":  "slack_user_id",
		"gitlab": "gitlab_username",
		"jira":   "jira_account_id",
	}[source]
	if col == "" || id == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE people SET `+col+` = $2, updated_at = now() WHERE key = $1 AND `+col+` = ''`,
		key, id)
	return err
}

// ---------- users (роли доступа) ----------

// AppUser — пользователь дашборда с ролью.
type AppUser struct {
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	AddedBy   string    `json:"added_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListUsers возвращает всех пользователей с ролями.
func (s *Store) ListUsers(ctx context.Context) ([]AppUser, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT email, role, added_by, created_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppUser
	for rows.Next() {
		var u AppUser
		if err := rows.Scan(&u.Email, &u.Role, &u.AddedBy, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUserRole возвращает роль по email ("" — пользователя нет).
func (s *Store) GetUserRole(ctx context.Context, email string) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM users WHERE email = $1`, strings.ToLower(strings.TrimSpace(email))).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return role, err
}

// UpsertUser создаёт или меняет роль пользователя.
func (s *Store) UpsertUser(ctx context.Context, email, role, addedBy string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO users (email, role, added_by) VALUES ($1, $2, $3)
ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role, updated_at = now()`,
		strings.ToLower(strings.TrimSpace(email)), role, addedBy)
	return err
}

// DeleteUser убирает доступ пользователя.
func (s *Store) DeleteUser(ctx context.Context, email string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE email = $1`,
		strings.ToLower(strings.TrimSpace(email)))
	return err
}

// AvgRunDurationMS — типичная длительность прогона за последние сутки:
// медиана только успешных (упавшие по таймауту растянули бы среднее и
// завысили оценку очереди в разы).
func (s *Store) AvgRunDurationMS(ctx context.Context) (int64, error) {
	var med *float64
	err := s.pool.QueryRow(ctx, `
SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - started_at)) * 1000
FROM sync_runs
WHERE status = 'done' AND finished_at IS NOT NULL
  AND started_at > now() - interval '24 hours'`).Scan(&med)
	if err != nil || med == nil {
		return 0, err
	}
	return int64(*med), nil
}

// GetSetting возвращает значение настройки приложения ("" — не задана).
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting сохраняет настройку приложения.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO app_settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// GetHRDBCache читает снапшот кэша HRDB (nil — снапшота нет).
func (s *Store) GetHRDBCache(ctx context.Context, mode, kind string) ([]byte, time.Time, error) {
	var payload []byte
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT payload, updated_at FROM hrdb_cache WHERE mode = $1 AND kind = $2`,
		mode, kind).Scan(&payload, &at)
	if err == pgx.ErrNoRows {
		return nil, time.Time{}, nil
	}
	return payload, at, err
}

// SaveHRDBCache сохраняет снапшот кэша HRDB.
func (s *Store) SaveHRDBCache(ctx context.Context, mode, kind string, payload []byte) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO hrdb_cache (mode, kind, payload) VALUES ($1, $2, $3)
ON CONFLICT (mode, kind) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now()`,
		mode, kind, payload)
	return err
}

// ExcludeEmails добавляет e-mail в чёрный список: синк команды не будет
// пересоздавать человека, «Удалить уволенных» удалит его независимо от HRDB.
func (s *Store) ExcludeEmails(ctx context.Context, personKey string, emails []string) error {
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx, `
INSERT INTO excluded_people (email, person_key) VALUES ($1, $2)
ON CONFLICT (email) DO NOTHING`, e, personKey); err != nil {
			return fmt.Errorf("чёрный список %q: %w", e, err)
		}
	}
	return nil
}

// ExcludedEmails возвращает чёрный список e-mail (в нижнем регистре).
func (s *Store) ExcludedEmails(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT email FROM excluded_people`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[e] = true
	}
	return out, rows.Err()
}

// DeletePerson удаляет человека и все его данные: события, особые дни,
// связки с документами и историю прогонов.
func (s *Store) DeletePerson(ctx context.Context, key string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, q := range []string{
		`DELETE FROM events WHERE person_key = $1`,
		`DELETE FROM person_days WHERE person_key = $1`,
		`DELETE FROM doc_links WHERE person_key = $1`,
		`DELETE FROM sync_runs WHERE person_key = $1`,
	} {
		if _, err := tx.Exec(ctx, q, key); err != nil {
			return fmt.Errorf("удаление данных человека %q: %w", key, err)
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM people WHERE key = $1`, key)
	if err != nil {
		return fmt.Errorf("удаление человека %q: %w", key, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ---------- events ----------

// SaveEvents пишет пачку событий, перезаписывая уже существующие по id.
func (s *Store) SaveEvents(ctx context.Context, events []models.Event) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
INSERT INTO events (id, person_key, source, type, external_id, occurred_at, title, body, url,
                    project, project_name, ref_id, parent_ref_id, effort, effort_unit, meta)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title, body = EXCLUDED.body, url = EXCLUDED.url,
    project = EXCLUDED.project, project_name = EXCLUDED.project_name,
    parent_ref_id = CASE WHEN EXCLUDED.parent_ref_id <> '' THEN EXCLUDED.parent_ref_id ELSE events.parent_ref_id END,
    effort = EXCLUDED.effort, effort_unit = EXCLUDED.effort_unit,
    meta = EXCLUDED.meta, ingested_at = now()`

	batch := &pgx.Batch{}
	for i := range events {
		e := &events[i]
		e.Normalize()
		meta, err := json.Marshal(orEmptyMap(e.Meta))
		if err != nil {
			return 0, fmt.Errorf("сериализация meta события %s: %w", e.ID, err)
		}
		batch.Queue(q, e.ID, e.PersonKey, string(e.Source), string(e.Type), e.ExternalID, e.OccurredAt,
			e.Title, e.Body, e.URL, e.Project, e.ProjectName, e.RefID, e.ParentRefID, e.Effort, e.EffortUnit, meta)
	}

	br := tx.SendBatch(ctx, batch)
	for range events {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return 0, fmt.Errorf("запись событий: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(events), nil
}

// DeleteRange удаляет ранее собранные события источника за период — чтобы
// повторный сбор не оставлял «хвостов» от удалённых в источнике сущностей.
func (s *Store) DeleteRange(ctx context.Context, personKey, source string, from, to time.Time) error {
	const q = `DELETE FROM events WHERE person_key = $1 AND source = $2 AND occurred_at >= $3 AND occurred_at < $4`
	_, err := s.pool.Exec(ctx, q, personKey, source, from, to)
	return err
}

// GetCoverage возвращает момент, до которого источник уже собран по человеку
// (нулевое время — покрытия ещё нет).
func (s *Store) GetCoverage(ctx context.Context, personKey, source string) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT covered_to FROM source_coverage WHERE person_key = $1 AND source = $2`,
		personKey, source).Scan(&t)
	if err == pgx.ErrNoRows {
		return time.Time{}, nil
	}
	return t, err
}

// AdvanceCoverage сдвигает границу покрытия вперёд (никогда не назад).
func (s *Store) AdvanceCoverage(ctx context.Context, personKey, source string, to time.Time) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO source_coverage (person_key, source, covered_to) VALUES ($1, $2, $3)
ON CONFLICT (person_key, source) DO UPDATE
  SET covered_to = GREATEST(source_coverage.covered_to, EXCLUDED.covered_to), updated_at = now()`,
		personKey, source, to)
	return err
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// SlackText — сообщение Slack для AI-оценки.
type SlackText struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Score *float64 `json:"score,omitempty"`
}

// ListSlackTexts возвращает сообщения Slack за период: id, текст и текущая
// AI-оценка. onlyUnscored — только ещё не оценённые. personKey пустой — все.
func (s *Store) ListSlackTexts(ctx context.Context, personKey string, from, to time.Time, onlyUnscored bool, limit int) ([]SlackText, error) {
	if limit <= 0 || limit > 20000 {
		limit = 5000
	}
	q := `SELECT id, COALESCE(NULLIF(body, ''), title), (meta->>'ai_score')::float8
	      FROM events WHERE source = 'slack' AND occurred_at >= $1 AND occurred_at < $2`
	args := []any{from, to}
	if personKey != "" {
		args = append(args, personKey)
		q += fmt.Sprintf(" AND person_key = $%d", len(args))
	}
	if onlyUnscored {
		q += " AND meta->>'ai_score' IS NULL"
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY occurred_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlackText
	for rows.Next() {
		var t SlackText
		if err := rows.Scan(&t.ID, &t.Text, &t.Score); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateEventScores пишет AI-оценки в meta событий.
func (s *Store) UpdateEventScores(ctx context.Context, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}
	const q = `UPDATE events SET meta = jsonb_set(COALESCE(meta, '{}'::jsonb), '{ai_score}', to_jsonb($2::numeric)) WHERE id = $1`
	batch := &pgx.Batch{}
	for id, score := range scores {
		batch.Queue(q, id, score)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range scores {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("запись ai_score: %w", err)
		}
	}
	return nil
}

// LastVacationDay — последний день отпуска человека не позже before;
// nil — отпусков не зафиксировано.
func (s *Store) LastVacationDay(ctx context.Context, personKey string, before time.Time) (*time.Time, error) {
	var d *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(day) FROM person_days WHERE person_key = $1 AND kind = 'vacation' AND day <= $2::date`,
		personKey, before).Scan(&d)
	return d, err
}

// ---------- company_holidays ----------

// CompanyHoliday — праздник корпоративного календаря.
type CompanyHoliday struct {
	Country string    `json:"country"`
	Day     time.Time `json:"day"`
	Label   string    `json:"label"`
}

// ListCompanyHolidays возвращает праздники страны за период [from, to).
func (s *Store) ListCompanyHolidays(ctx context.Context, country string, from, to time.Time) ([]CompanyHoliday, error) {
	const q = `SELECT country, day, label FROM company_holidays
	           WHERE country = $1 AND day >= $2::date AND day < $3::date ORDER BY day`
	rows, err := s.pool.Query(ctx, q, country, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanyHoliday
	for rows.Next() {
		var h CompanyHoliday
		if err := rows.Scan(&h.Country, &h.Day, &h.Label); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CompanyHolidayYears возвращает годы, покрытые корпоративным календарём
// страны: для них праздники Google игнорируются целиком.
func (s *Store) CompanyHolidayYears(ctx context.Context, country string) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT EXTRACT(YEAR FROM day)::int FROM company_holidays WHERE country = $1`, country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err != nil {
			return nil, err
		}
		out[y] = true
	}
	return out, rows.Err()
}

// UpsertCompanyHoliday добавляет или обновляет праздник.
func (s *Store) UpsertCompanyHoliday(ctx context.Context, h CompanyHoliday) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO company_holidays (country, day, label) VALUES ($1, $2, $3)
ON CONFLICT (country, day) DO UPDATE SET label = EXCLUDED.label`, h.Country, h.Day, h.Label)
	return err
}

// DeleteCompanyHoliday удаляет праздник.
func (s *Store) DeleteCompanyHoliday(ctx context.Context, country string, day time.Time) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM company_holidays WHERE country = $1 AND day = $2::date`, country, day)
	return err
}

// FailStaleRuns помечает прогоны, оставшиеся running/pending от прежнего
// процесса сервера: прогоны живут только в памяти, поэтому на старте любые
// такие записи заведомо мертвы и без этого висели бы «идущими» вечно.
func (s *Store) FailStaleRuns(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE sync_runs
SET status = 'failed', error = 'прерван перезапуском сервера', finished_at = now()
WHERE status IN ('running', 'pending')`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---------- person_days ----------

// ReplacePersonDays перезаписывает особые дни человека в окне [from, to)
// (совместимость: источник gcal). См. ReplacePersonDaysForSource.
func (s *Store) ReplacePersonDays(ctx context.Context, personKey string, from, to time.Time, days []models.PersonDay) error {
	return s.ReplacePersonDaysForSource(ctx, personKey, "gcal", from, to, days)
}

// ReplacePersonDaysForSource перезаписывает особые дни ОДНОГО источника в окне
// [from, to): удаляются прежние записи периода этого источника, затем пишутся
// свежие. Разные источники (gcal, vacjira) не затирают друг друга — отменённый
// отпуск исчезает, а праздники из календаря остаются.
func (s *Store) ReplacePersonDaysForSource(ctx context.Context, personKey, source string, from, to time.Time, days []models.PersonDay) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM person_days WHERE person_key = $1 AND source = $2 AND day >= $3::date AND day < $4::date`,
		personKey, source, from, to); err != nil {
		return fmt.Errorf("очистка person_days: %w", err)
	}

	const q = `
INSERT INTO person_days (person_key, day, kind, label, source)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (person_key, day, kind) DO UPDATE SET label = EXCLUDED.label, source = EXCLUDED.source`
	batch := &pgx.Batch{}
	for _, d := range days {
		batch.Queue(q, personKey, d.Day, d.Kind, d.Label, source)
	}
	br := tx.SendBatch(ctx, batch)
	for range days {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("запись person_days: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- hybrid_history / work_format_history ----------
// Две таблицы одной формы: хронологии изменений гибридных дней и формата
// работы. Имя таблицы подставляется из белого списка — не из ввода.

// ReplaceHybridHistory перезаписывает историю изменений гибридных дней.
func (s *Store) ReplaceHybridHistory(ctx context.Context, personKey string, changes []models.HybridChange) error {
	return s.replaceTimeline(ctx, "hybrid_history", personKey, changes)
}

// ReplaceWorkFormatHistory перезаписывает историю изменений формата работы.
func (s *Store) ReplaceWorkFormatHistory(ctx context.Context, personKey string, changes []models.HybridChange) error {
	return s.replaceTimeline(ctx, "work_format_history", personKey, changes)
}

// ListHybridHistory — история гибридных дней человека по возрастанию времени.
func (s *Store) ListHybridHistory(ctx context.Context, personKey string) ([]models.HybridChange, error) {
	return s.listTimeline(ctx, "hybrid_history", personKey)
}

// ListWorkFormatHistory — история формата работы человека.
func (s *Store) ListWorkFormatHistory(ctx context.Context, personKey string) ([]models.HybridChange, error) {
	return s.listTimeline(ctx, "work_format_history", personKey)
}

// ListHybridHistoryAll — истории гибридных дней набора людей одним запросом.
func (s *Store) ListHybridHistoryAll(ctx context.Context, keys []string) (map[string][]models.HybridChange, error) {
	return s.listTimelineAll(ctx, "hybrid_history", keys)
}

// ListWorkFormatHistoryAll — истории формата работы набора людей.
func (s *Store) ListWorkFormatHistoryAll(ctx context.Context, keys []string) (map[string][]models.HybridChange, error) {
	return s.listTimelineAll(ctx, "work_format_history", keys)
}

func (s *Store) replaceTimeline(ctx context.Context, table, personKey string, changes []models.HybridChange) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE person_key = $1`, personKey); err != nil {
		return fmt.Errorf("очистка %s: %w", table, err)
	}
	batch := &pgx.Batch{}
	for _, h := range changes {
		batch.Queue(`INSERT INTO `+table+` (person_key, changed_at, old_days, new_days)
		             VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			personKey, h.At, h.Old, h.New)
	}
	br := tx.SendBatch(ctx, batch)
	for range changes {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("запись %s: %w", table, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) listTimeline(ctx context.Context, table, personKey string) ([]models.HybridChange, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT changed_at, old_days, new_days FROM `+table+` WHERE person_key = $1 ORDER BY changed_at`,
		personKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.HybridChange
	for rows.Next() {
		var h models.HybridChange
		if err := rows.Scan(&h.At, &h.Old, &h.New); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) listTimelineAll(ctx context.Context, table string, keys []string) (map[string][]models.HybridChange, error) {
	out := map[string][]models.HybridChange{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT person_key, changed_at, old_days, new_days FROM `+table+`
		 WHERE person_key = ANY($1) ORDER BY person_key, changed_at`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var h models.HybridChange
		if err := rows.Scan(&key, &h.At, &h.Old, &h.New); err != nil {
			return nil, err
		}
		out[key] = append(out[key], h)
	}
	return out, rows.Err()
}

// ListPersonDays возвращает особые дни человека за период [from, to).
func (s *Store) ListPersonDays(ctx context.Context, personKey string, from, to time.Time) ([]models.PersonDay, error) {
	const q = `SELECT person_key, day, kind, label FROM person_days
	           WHERE person_key = $1 AND day >= $2::date AND day < $3::date
	           ORDER BY day, kind`
	rows, err := s.pool.Query(ctx, q, personKey, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.PersonDay
	for rows.Next() {
		var d models.PersonDay
		if err := rows.Scan(&d.PersonKey, &d.Day, &d.Kind, &d.Label); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
