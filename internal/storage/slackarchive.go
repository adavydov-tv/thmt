package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var jsonUnmarshalFn = json.Unmarshal

// SlackMessage — строка канал-центричного архива Slack. Text заполняется
// только на записи и при явной расшифровке; в обычных выборках он пуст.
type SlackMessage struct {
	ID          string
	ChannelID   string
	ChannelName string
	UID         string
	Kind        string // message | reply | reaction
	TS          time.Time
	SlackTS     string
	ThreadTS    string
	Text        string
	URL         string
	ReplyCount  int
	Reactions   int
	Reaction    string
	AIScore     *float64
}

// SetTextKey подключает ключ шифрования текстов Slack (AES-256-GCM).
// Без ключа тексты хранятся открытыми (лог предупредит при старте).
func (s *Store) SetTextKey(key []byte) error {
	if len(key) == 0 {
		s.textAEAD = nil
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("slack text key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	s.textAEAD = aead
	return nil
}

// TextEncryptionOn — включено ли шифрование текстов.
func (s *Store) TextEncryptionOn() bool { return s.textAEAD != nil }

func (s *Store) encryptText(text string) []byte {
	if text == "" {
		return nil
	}
	if s.textAEAD == nil {
		return []byte("plain:" + text)
	}
	nonce := make([]byte, s.textAEAD.NonceSize())
	_, _ = rand.Read(nonce)
	return append(nonce, s.textAEAD.Seal(nil, nonce, []byte(text), nil)...)
}

func (s *Store) decryptText(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if len(blob) > 6 && string(blob[:6]) == "plain:" {
		return string(blob[6:]), nil
	}
	if s.textAEAD == nil {
		return "", fmt.Errorf("текст зашифрован, а SLACK_TEXT_KEY не задан")
	}
	ns := s.textAEAD.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("повреждённый шифртекст")
	}
	plain, err := s.textAEAD.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("расшифровка не удалась: %w", err)
	}
	return string(plain), nil
}

// UpsertSlackMessages пишет пачку строк архива. Существующие строки
// обновляются (счётчики, текст), но AI-оценка сохраняется.
func (s *Store) UpsertSlackMessages(ctx context.Context, msgs []SlackMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	const q = `
INSERT INTO slack_messages
    (id, channel_id, channel_name, slack_uid, kind, ts, slack_ts, thread_ts,
     text_enc, url, reply_count, reactions, reaction)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (id) DO UPDATE SET
    channel_name = EXCLUDED.channel_name,
    text_enc     = EXCLUDED.text_enc,
    url          = EXCLUDED.url,
    reply_count  = EXCLUDED.reply_count,
    reactions    = EXCLUDED.reactions`
	for _, m := range msgs {
		batch.Queue(q, m.ID, m.ChannelID, m.ChannelName, m.UID, m.Kind, m.TS, m.SlackTS,
			m.ThreadTS, s.encryptText(m.Text), m.URL, m.ReplyCount, m.Reactions, m.Reaction)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range msgs {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// SlackMessagesForUID — строки архива одного slack-пользователя (без текста).
func (s *Store) SlackMessagesForUID(ctx context.Context, uid string, from, to time.Time) ([]SlackMessage, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, channel_id, channel_name, slack_uid, kind, ts, slack_ts, thread_ts,
       url, reply_count, reactions, reaction, ai_score
FROM slack_messages WHERE slack_uid = $1 AND ts >= $2 AND ts < $3
ORDER BY ts`, uid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlackMessage
	for rows.Next() {
		var m SlackMessage
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.ChannelName, &m.UID, &m.Kind, &m.TS,
			&m.SlackTS, &m.ThreadTS, &m.URL, &m.ReplyCount, &m.Reactions, &m.Reaction, &m.AIScore); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SlackCoverage — покрытые архивом интервалы канала по возрастанию.
func (s *Store) SlackCoverage(ctx context.Context, channelID string) ([][2]time.Time, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT from_ts, to_ts FROM slack_coverage WHERE channel_id = $1 ORDER BY from_ts`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]time.Time
	for rows.Next() {
		var from, to time.Time
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		out = append(out, [2]time.Time{from, to})
	}
	return out, rows.Err()
}

// MarkSlackCoverage помечает интервал канала покрытым, сливая пересекающиеся
// и смежные интервалы в один.
func (s *Store) MarkSlackCoverage(ctx context.Context, channelID string, from, to time.Time) error {
	if !to.After(from) {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Пересекающиеся/смежные интервалы поглощаются новым.
	rows, err := tx.Query(ctx,
		`SELECT from_ts, to_ts FROM slack_coverage
		 WHERE channel_id = $1 AND from_ts <= $3 AND to_ts >= $2 FOR UPDATE`,
		channelID, from, to)
	if err != nil {
		return err
	}
	lo, hi := from, to
	for rows.Next() {
		var f, t time.Time
		if err := rows.Scan(&f, &t); err != nil {
			rows.Close()
			return err
		}
		if f.Before(lo) {
			lo = f
		}
		if t.After(hi) {
			hi = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM slack_coverage WHERE channel_id = $1 AND from_ts <= $3 AND to_ts >= $2`,
		channelID, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO slack_coverage (channel_id, from_ts, to_ts) VALUES ($1,$2,$3)
		 ON CONFLICT (channel_id, from_ts) DO UPDATE SET to_ts = GREATEST(slack_coverage.to_ts, EXCLUDED.to_ts)`,
		channelID, lo, hi); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SlackArchiveSpan — покрытый архивом период (zero-времена, если архив пуст).
func (s *Store) SlackArchiveSpan(ctx context.Context) (time.Time, time.Time, error) {
	var from, to *time.Time
	err := s.pool.QueryRow(ctx, `SELECT min(ts), max(ts) FROM slack_messages`).Scan(&from, &to)
	if err != nil || from == nil || to == nil {
		return time.Time{}, time.Time{}, err
	}
	return *from, *to, nil
}

// UnscoredSlackTexts — расшифрованные тексты сообщений без AI-оценки
// (реакции не оцениваются).
func (s *Store) UnscoredSlackTexts(ctx context.Context, limit int) ([]SlackText, error) {
	if limit <= 0 || limit > 20000 {
		limit = 2000
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, text_enc FROM slack_messages
WHERE ai_score IS NULL AND kind <> 'reaction' ORDER BY ingested_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlackText
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		text, err := s.decryptText(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, SlackText{ID: id, Text: text})
	}
	return out, rows.Err()
}

// SlackTextsSample — расшифрованная выборка для калибровки AI (админский
// раздел): случайные сообщения периода, при необходимости только неоценённые.
func (s *Store) SlackTextsSample(ctx context.Context, from, to time.Time, onlyUnscored bool, limit int) ([]SlackText, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, text_enc, ai_score FROM slack_messages
	      WHERE kind <> 'reaction' AND ts >= $1 AND ts < $2 AND text_enc IS NOT NULL`
	if onlyUnscored {
		q += ` AND ai_score IS NULL`
	}
	q += ` ORDER BY random() LIMIT $3`
	rows, err := s.pool.Query(ctx, q, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlackText
	for rows.Next() {
		var st SlackText
		var blob []byte
		if err := rows.Scan(&st.ID, &blob, &st.Score); err != nil {
			return nil, err
		}
		if st.Text, err = s.decryptText(blob); err != nil {
			return nil, err
		}
		if st.Text != "" {
			out = append(out, st)
		}
	}
	return out, rows.Err()
}

// SetSlackScores сохраняет AI-оценки строк архива.
func (s *Store) SetSlackScores(ctx context.Context, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for id, sc := range scores {
		batch.Queue(`UPDATE slack_messages SET ai_score = $2 WHERE id = $1`, id, sc)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range scores {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// UpdateEventScoresByExternalID проставляет meta.ai_score всем slack-событиям
// с данным external_id (у сообщения общий external_id на всех людей).
func (s *Store) UpdateEventScoresByExternalID(ctx context.Context, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for extID, sc := range scores {
		batch.Queue(`UPDATE events SET meta = jsonb_set(COALESCE(meta, '{}'::jsonb), '{ai_score}', to_jsonb($2::numeric))
		             WHERE source = 'slack' AND external_id = $1`, extID, sc)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range scores {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// MigrateSlackTextsToArchive — разовая миграция: тексты старых slack-событий
// переезжают в зашифрованный архив, сами события обезличиваются (id + оценка).
// uidByPerson — person_key → slack uid (события без известного uid пропускаются
// в архиве, но тоже обезличиваются).
func (s *Store) MigrateSlackTextsToArchive(ctx context.Context, uidByPerson map[string]string) (int, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, person_key, external_id, type, occurred_at, title, body, url, meta
FROM events WHERE source = 'slack' AND (body <> '' OR title NOT LIKE 'Сообщение в %' AND title NOT LIKE 'Ответ в треде %' AND title NOT LIKE ':%')`)
	if err != nil {
		return 0, err
	}
	type evRow struct {
		id, personKey, externalID, typ, title, body, url string
		occurred                                         time.Time
		meta                                             map[string]any
	}
	var evs []evRow
	for rows.Next() {
		var e evRow
		var metaRaw []byte
		if err := rows.Scan(&e.id, &e.personKey, &e.externalID, &e.typ, &e.occurred,
			&e.title, &e.body, &e.url, &metaRaw); err != nil {
			rows.Close()
			return 0, err
		}
		e.meta = map[string]any{}
		_ = jsonUnmarshal(metaRaw, &e.meta)
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}

	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	num := func(m map[string]any, k string) int {
		if f, ok := m[k].(float64); ok {
			return int(f)
		}
		return 0
	}

	var archive []SlackMessage
	scores := map[string]float64{}
	titleUpdates := &pgx.Batch{}
	for _, e := range evs {
		chID := str(e.meta, "channel_id")
		chName := str(e.meta, "channel_name")
		slackTS := str(e.meta, "ts")
		display := "#" + chName
		if chName == "" {
			display = chID
		}
		kind, newTitle := "message", "Сообщение в "+display+" · "+slackTS
		archiveID := e.externalID
		reaction := str(e.meta, "reaction")
		switch {
		case e.typ == "slack.reaction":
			kind = "reaction"
			newTitle = ":" + reaction + ": на сообщение в " + display + " · " + slackTS
			if uid := uidByPerson[e.personKey]; uid != "" {
				archiveID = e.externalID + ":" + uid
			}
		case e.typ == "slack.reply":
			kind = "reply"
			newTitle = "Ответ в треде " + display + " · " + slackTS
		}
		if uid := uidByPerson[e.personKey]; uid != "" && slackTS != "" {
			m := SlackMessage{
				ID: archiveID, ChannelID: chID, ChannelName: chName, UID: uid, Kind: kind,
				TS: e.occurred, SlackTS: slackTS, ThreadTS: str(e.meta, "thread_ts"),
				Text: e.body, URL: e.url,
				ReplyCount: num(e.meta, "reply_count"), Reactions: num(e.meta, "reactions_count"),
				Reaction: reaction,
			}
			archive = append(archive, m)
			if f, ok := e.meta["ai_score"].(float64); ok && kind != "reaction" {
				scores[archiveID] = f
			}
		}
		titleUpdates.Queue(`UPDATE events SET body = '', title = $2,
			meta = jsonb_set(COALESCE(meta,'{}'::jsonb), '{message_id}', to_jsonb($3::text))
			WHERE id = $1`, e.id, newTitle, "msg:"+chID+":"+slackTS)
	}
	for i := 0; i < len(archive); i += 500 {
		end := min(i+500, len(archive))
		if err := s.UpsertSlackMessages(ctx, archive[i:end]); err != nil {
			return 0, err
		}
	}
	if err := s.SetSlackScores(ctx, scores); err != nil {
		return 0, err
	}
	br := s.pool.SendBatch(ctx, titleUpdates)
	defer br.Close()
	for range evs {
		if _, err := br.Exec(); err != nil {
			return 0, err
		}
	}
	return len(evs), nil
}

func jsonUnmarshal(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return jsonUnmarshalFn(b, v)
}

// SlackMessageText — расшифрованный текст одной строки архива (админ-доступ
// контролируется на уровне API).
func (s *Store) SlackMessageText(ctx context.Context, id string) (string, error) {
	var blob []byte
	err := s.pool.QueryRow(ctx, `SELECT text_enc FROM slack_messages WHERE id = $1`, id).Scan(&blob)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.decryptText(blob)
}
