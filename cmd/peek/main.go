// Command peek — отладочный дамп расшифрованных сообщений Slack одного автора,
// отсортированных по ai_score, для проверки калибровки модели. Локально.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	uid := flag.String("uid", "", "slack_uid автора")
	limit := flag.Int("limit", 30, "сколько сообщений")
	order := flag.String("order", "desc", "asc|desc по ai_score")
	trunc := flag.Int("trunc", 300, "обрезка текста")
	flag.Parse()
	env := readEnv(".env")
	aead := mustAEAD(env["SLACK_TEXT_KEY"])
	pool, err := pgxpool.New(context.Background(), env["POSTGRES_DSN"])
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	q := fmt.Sprintf(`SELECT ai_score, channel_name, text_enc FROM slack_messages
		WHERE slack_uid=$1 AND kind<>'reaction' AND ai_score IS NOT NULL
		ORDER BY ai_score %s LIMIT $2`, map[string]string{"asc": "ASC", "desc": "DESC"}[*order])
	rows, err := pool.Query(context.Background(), q, *uid, *limit)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sc float64
		var ch string
		var blob []byte
		rows.Scan(&sc, &ch, &blob)
		t := decrypt(aead, blob)
		t = strings.Join(strings.Fields(t), " ")
		if len([]rune(t)) > *trunc {
			t = string([]rune(t)[:*trunc])
		}
		fmt.Printf("%.1f\t[%s]\t%s\n", sc, ch, t)
	}
}

func decrypt(a cipher.AEAD, b []byte) string {
	if len(b) > 6 && string(b[:6]) == "plain:" {
		return string(b[6:])
	}
	ns := a.NonceSize()
	if len(b) < ns {
		return ""
	}
	p, err := a.Open(nil, b[:ns], b[ns:], nil)
	if err != nil {
		return ""
	}
	return string(p)
}
func mustAEAD(k string) cipher.AEAD {
	key, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
	bl, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	a, _ := cipher.NewGCM(bl)
	return a
}
func readEnv(p string) map[string]string {
	m := map[string]string{}
	d, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(d), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
