package claude

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

const sampleRecord = `{
  "source": "claude-usage-collector",
  "event_type": "ai_cli_usage",
  "hostname": "host-1",
  "os_version": "26.6.2",
  "email": "adavydov@tradingview.com",
  "billing_type": "stripe_subscription",
  "report": {
    "daily": [
      {"period": "2026-09-10", "inputTokens": 592, "outputTokens": 381581,
       "cacheReadTokens": 61997918, "totalTokens": 63269767, "requests": 296,
       "modelsUsed": ["claude-opus-5"]},
      {"period": "2026-09-09", "requests": 0, "totalTokens": 0}
    ]
  }
}`

func TestParseObject(t *testing.T) {
	c := &Collector{}
	people := []models.Person{{Key: "u1", Email: "adavydov@tradingview.com"}}
	ids := buildIdentities(people)
	out := map[string][]models.Event{}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	added := c.parseObject([]byte(sampleRecord), "k.jsonl", ids, from, to, out)
	if added != 1 {
		t.Fatalf("ожидали 1 событие (день с активностью), получили %d", added)
	}
	evs := out["u1"]
	if len(evs) != 1 {
		t.Fatalf("ожидали 1 событие у u1, получили %d", len(evs))
	}
	ev := evs[0]
	if ev.Type != models.TypeClaudeUsage {
		t.Errorf("тип: %s", ev.Type)
	}
	if ev.Source != models.SourceClaude {
		t.Errorf("источник: %s", ev.Source)
	}
	if ev.Effort != 296 || ev.EffortUnit != "requests" {
		t.Errorf("effort: %v %s", ev.Effort, ev.EffortUnit)
	}
	if got := ev.OccurredAt.Format("2006-01-02"); got != "2026-09-10" {
		t.Errorf("дата события: %s", got)
	}
	if ev.ExternalID != "claude:adavydov@tradingview.com:2026-09-10:host-1" {
		t.Errorf("external id: %s", ev.ExternalID)
	}
}

func TestParseObjectFiltersWindow(t *testing.T) {
	c := &Collector{}
	ids := buildIdentities([]models.Person{{Key: "u1", Email: "adavydov@tradingview.com"}})
	out := map[string][]models.Event{}
	// Окно НЕ включает 2026-09-10.
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if added := c.parseObject([]byte(sampleRecord), "k.jsonl", ids, from, to, out); added != 0 {
		t.Fatalf("вне окна не должно быть событий, получили %d", added)
	}
}

func TestS3EncodePath(t *testing.T) {
	got := s3EncodePath("/claude-usage/dt=2026-09-10/C3WN.jsonl")
	want := "/claude-usage/dt%3D2026-09-10/C3WN.jsonl"
	if got != want {
		t.Errorf("s3EncodePath: %s (ожидали %s)", got, want)
	}
}

func TestCanonicalQueryString(t *testing.T) {
	q := url.Values{}
	q.Set("prefix", "claude-usage/dt=2026-09-10/")
	q.Set("list-type", "2")
	got := canonicalQueryString(q)
	want := "list-type=2&prefix=claude-usage%2Fdt%3D2026-09-10%2F"
	if got != want {
		t.Errorf("canonicalQueryString: %s (ожидали %s)", got, want)
	}
}

// TestSigV4Primitives сверяет цепочку подписи с официальным примером AWS
// (General Reference, «Signature Version 4» — ListUsers для IAM), проверяя
// вывод signing key + итоговую подпись из документированного StringToSign.
func TestSigV4Primitives(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	dateStamp := "20150830"
	region := "us-east-1"
	service := "iam"
	stringToSign := "AWS4-HMAC-SHA256\n" +
		"20150830T123600Z\n" +
		"20150830/us-east-1/iam/aws4_request\n" +
		"f536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59"

	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	const want = "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if signature != want {
		t.Errorf("подпись SigV4: %s\nожидали:      %s", signature, want)
	}
}

// TestWirePathMatchesSigned проверяет, что путь, который net/http реально
// отправит (EscapedPath), совпадает с путём, который мы подписываем — иначе
// «=» в dt= уехал бы на провод без %3D и S3 вернул бы SignatureDoesNotMatch.
func TestWirePathMatchesSigned(t *testing.T) {
	path := "/tradingview-it-claude-usage/claude-usage/dt=2026-09-10/C3WN.jsonl"
	encPath := s3EncodePath(path)
	rawURL := "https://s3.amazonaws.com" + encPath
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.URL.RawPath = encPath
	if got := req.URL.EscapedPath(); got != encPath {
		t.Errorf("на провод уйдёт %q, а подписываем %q", got, encPath)
	}
}

func TestDateRange(t *testing.T) {
	got := dateRange(
		time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC),
	)
	want := []string{"2026-09-08", "2026-09-09", "2026-09-10"}
	if len(got) != len(want) {
		t.Fatalf("dateRange len %d: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dateRange[%d]=%s ожидали %s", i, got[i], want[i])
		}
	}
}
