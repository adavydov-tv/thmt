// Command labelsample выгружает стратифицированную выборку сообщений Slack из
// архива, расшифровывает их (AES-256-GCM, ключ SLACK_TEXT_KEY) и пишет JSONL
// {id, ch, len, text} для ручной разметки. Тексты остаются локально.
//
// Стратификация: по каналам и по бакетам длины — чтобы выборка покрывала и
// короткие реплики, и длинные разборы, и разные каналы, а не только самый
// многословный standup.
//
// Запуск: go run ./cmd/labelsample -n 900 -out /path/sample.jsonl
package main

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	n := flag.Int("n", 900, "сколько сообщений выгрузить")
	out := flag.String("out", "sample.jsonl", "путь к JSONL")
	minLen := flag.Int("minlen", 15, "минимальная длина текста (короче — авто-0 по правилам)")
	envPath := flag.String("env", ".env", "путь к .env")
	flag.Parse()

	env := readEnv(*envPath)
	dsn := env["POSTGRES_DSN"]
	if dsn == "" {
		fatal("POSTGRES_DSN не найден в " + *envPath)
	}
	aead := mustAEAD(env["SLACK_TEXT_KEY"])

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatal("подключение к БД: " + err.Error())
	}
	defer pool.Close()

	// Стратифицированная выборка: внутри каждого (канал, бакет длины) берём
	// случайные строки; отбрасываем реакции и слишком короткое. Бакет длины
	// считаем по octet_length(text_enc) как прокси (текст зашифрован).
	// ROW_NUMBER даёт равномерный отбор по стратам.
	const q = `
WITH base AS (
  SELECT id, channel_name, text_enc,
         width_bucket(octet_length(text_enc), 30, 4000, 6) AS lb
  FROM slack_messages
  WHERE kind <> 'reaction' AND text_enc IS NOT NULL
        AND octet_length(text_enc) >= $2
), ranked AS (
  SELECT *, row_number() OVER (PARTITION BY channel_name, lb ORDER BY random()) AS rn
  FROM base
)
SELECT id, channel_name, text_enc FROM ranked
ORDER BY rn, random()
LIMIT $1`

	rows, err := pool.Query(ctx, q, *n, *minLen+16 /*+ ~nonce+tag overhead*/)
	if err != nil {
		fatal("запрос выборки: " + err.Error())
	}
	defer rows.Close()

	f, err := os.Create(*out)
	if err != nil {
		fatal("создание файла: " + err.Error())
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	type rec struct {
		ID   string `json:"id"`
		Ch   string `json:"ch"`
		Len  int    `json:"len"`
		Text string `json:"text"`
	}
	written, skipped := 0, 0
	for rows.Next() {
		var id string
		var ch string
		var blob []byte
		if err := rows.Scan(&id, &ch, &blob); err != nil {
			fatal("scan: " + err.Error())
		}
		text, err := decrypt(aead, blob)
		if err != nil || strings.TrimSpace(text) == "" {
			skipped++
			continue
		}
		text = strings.ReplaceAll(text, "\x00", "")
		line, _ := json.Marshal(rec{ID: id, Ch: ch, Len: len([]rune(text)), Text: text})
		w.Write(line)
		w.WriteByte('\n')
		written++
	}
	if err := rows.Err(); err != nil {
		fatal("итерация: " + err.Error())
	}
	fmt.Printf("выгружено %d, пропущено %d → %s\n", written, skipped, *out)
}

func mustAEAD(b64key string) cipher.AEAD {
	if b64key == "" {
		fatal("SLACK_TEXT_KEY не найден")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64key))
	if err != nil {
		fatal("SLACK_TEXT_KEY: не base64: " + err.Error())
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		fatal("aes: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		fatal("gcm: " + err.Error())
	}
	return aead
}

func decrypt(aead cipher.AEAD, blob []byte) (string, error) {
	if len(blob) > 6 && string(blob[:6]) == "plain:" {
		return string(blob[6:]), nil
	}
	ns := aead.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("короткий шифртекст")
	}
	plain, err := aead.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// readEnv — простой парсер .env (KEY=VALUE, без экранирования).
func readEnv(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		fatal("открыть " + path + ": " + err.Error())
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "labelsample: "+msg)
	os.Exit(1)
}
