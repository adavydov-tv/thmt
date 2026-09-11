// Command rescoreall пере-скоривает весь архив Slack обученным скорером:
// расшифровывает text_enc, прогоняет батчами через AI-скорер (/score) и
// пишет ai_score обратно в slack_messages. Нужно после переобучения модели,
// чтобы старые оценки обновились под новую калибровку.
//
// Запуск: go run ./cmd/rescoreall [-batch 500]
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	batch := flag.Int("batch", 500, "размер батча для /score")
	envPath := flag.String("env", ".env", "путь к .env")
	flag.Parse()

	env := readEnv(*envPath)
	dsn := env["POSTGRES_DSN"]
	scorer := strings.TrimRight(env["AI_SCORER_URL"], "/")
	if scorer == "" {
		scorer = "http://localhost:8091"
	}
	if dsn == "" {
		fatal("нет POSTGRES_DSN")
	}
	aead := mustAEAD(env["SLACK_TEXT_KEY"])

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatal("подключение к БД: " + err.Error())
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `SELECT id, text_enc FROM slack_messages
		WHERE kind <> 'reaction' AND text_enc IS NOT NULL ORDER BY id`)
	if err != nil {
		fatal("выборка: " + err.Error())
	}
	type msg struct {
		id, text string
	}
	var all []msg
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			fatal("scan: " + err.Error())
		}
		t, err := decrypt(aead, blob)
		if err != nil || strings.TrimSpace(t) == "" {
			continue
		}
		all = append(all, msg{id: id, text: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fatal("итерация: " + err.Error())
	}
	fmt.Printf("сообщений к скорингу: %d\n", len(all))

	client := &http.Client{Timeout: 120 * time.Second}
	scored := 0
	for i := 0; i < len(all); i += *batch {
		j := i + *batch
		if j > len(all) {
			j = len(all)
		}
		texts := make([]string, j-i)
		for k := range all[i:j] {
			texts[k] = all[i+k].text
		}
		scores, err := score(client, scorer, texts)
		if err != nil {
			fatal(fmt.Sprintf("score батча %d-%d: %v", i, j, err))
		}
		// bulk UPDATE через временный маппинг id→score одним запросом на батч
		b := &pgx.Batch{}
		for k, s := range scores {
			b.Queue(`UPDATE slack_messages SET ai_score=$1 WHERE id=$2`, s, all[i+k].id)
		}
		br := pool.SendBatch(ctx, b)
		for range scores {
			if _, err := br.Exec(); err != nil {
				br.Close()
				fatal("update: " + err.Error())
			}
		}
		br.Close()
		scored += len(scores)
		fmt.Printf("  %d/%d\n", scored, len(all))
	}
	fmt.Printf("готово: обновлено ai_score у %d сообщений\n", scored)
}

func score(client *http.Client, base string, texts []string) ([]float64, error) {
	body, _ := json.Marshal(map[string]any{"texts": texts})
	resp, err := client.Post(base+"/score", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("скорер вернул %d", resp.StatusCode)
	}
	var out struct {
		Scores []float64 `json:"scores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Scores) != len(texts) {
		return nil, fmt.Errorf("вернул %d оценок на %d текстов", len(out.Scores), len(texts))
	}
	return out.Scores, nil
}

func mustAEAD(b64key string) cipher.AEAD {
	if b64key == "" {
		fatal("нет SLACK_TEXT_KEY")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64key))
	if err != nil {
		fatal("SLACK_TEXT_KEY не base64: " + err.Error())
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

func readEnv(path string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("открыть " + path + ": " + err.Error())
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "rescoreall: "+msg)
	os.Exit(1)
}
