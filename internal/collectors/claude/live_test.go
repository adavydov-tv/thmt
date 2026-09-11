package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/config"
)

// TestLiveS3 — живая проверка коннекта к S3 (запускается только когда заданы
// CLAUDE_S3_* в окружении). Листит последние дни и качает один объект.
func TestLiveS3(t *testing.T) {
	if os.Getenv("CLAUDE_S3_ACCESS_KEY_ID") == "" {
		t.Skip("нет CLAUDE_S3_* в окружении — пропуск живой проверки")
	}
	cfg := config.ClaudeConfig{
		Bucket:       os.Getenv("CLAUDE_S3_BUCKET"),
		Prefix:       firstNonEmptyStr(os.Getenv("CLAUDE_S3_PREFIX"), "claude-usage/"),
		Region:       firstNonEmptyStr(os.Getenv("CLAUDE_S3_REGION"), "us-east-1"),
		Endpoint:     os.Getenv("CLAUDE_S3_ENDPOINT"),
		AccessKey:    os.Getenv("CLAUDE_S3_ACCESS_KEY_ID"),
		SecretKey:    os.Getenv("CLAUDE_S3_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("CLAUDE_S3_SESSION_TOKEN"),
	}
	c, err := New(cfg, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("host=%s pathStyle=%v region=%s prefix=%s", c.host, c.pathStyle, c.region, cfg.Prefix)

	ctx := context.Background()

	// 1) Листинг корневого префикса (проверка кредов/подписи/доступа).
	keys, err := c.listKeys(ctx, cfg.Prefix)
	if err != nil {
		t.Fatalf("listKeys(%q): %v", cfg.Prefix, err)
	}
	t.Logf("под префиксом %q найдено ключей: %d", cfg.Prefix, len(keys))
	for i, k := range keys {
		if i >= 5 {
			t.Logf("  ... и ещё %d", len(keys)-5)
			break
		}
		t.Logf("  key: %s", k)
	}
	if len(keys) == 0 {
		t.Log("ключей нет — проверь префикс; коннект/креды при этом рабочие")
		return
	}

	// 2) Скачать первый .jsonl и убедиться, что он парсится.
	var sample string
	for _, k := range keys {
		if len(k) > 6 && (k[len(k)-6:] == ".jsonl" || k[len(k)-5:] == ".json") {
			sample = k
			break
		}
	}
	if sample == "" {
		sample = keys[0]
	}
	body, err := c.getObject(ctx, sample)
	if err != nil {
		t.Fatalf("getObject(%q): %v", sample, err)
	}
	t.Logf("скачан %s: %d байт", sample, len(body))

	// Проверить, что объект парсится в нашу схему.
	var rec usageRecord
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&rec); err != nil {
		t.Fatalf("разбор %s: %v", sample, err)
	}
	t.Logf("схема ок: email=%q, дней в report.daily=%d", rec.Email, len(rec.Report.Daily))
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
