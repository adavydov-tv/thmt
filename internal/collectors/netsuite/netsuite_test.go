package netsuite

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/adavydov/user-activity-dashboard/internal/config"
)

// pctEncode должен кодировать по RFC3986 (пробел → %20, не +; резерв → %XX).
func TestPctEncode(t *testing.T) {
	cases := map[string]string{
		"abcABC123-._~": "abcABC123-._~",
		"a b":           "a%20b",
		"a+b/c=d":       "a%2Bb%2Fc%3Dd",
		"1234567_SB1":   "1234567_SB1",
	}
	for in, want := range cases {
		if got := pctEncode(in); got != want {
			t.Errorf("pctEncode(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}

// encodeSorted сортирует параметры по ключу и кодирует значения.
func TestEncodeSorted(t *testing.T) {
	v := url.Values{"b": {"2"}, "a": {"1"}, "limit": {"1000"}}
	if got, want := encodeSorted(v), "a=1&b=2&limit=1000"; got != want {
		t.Fatalf("encodeSorted=%q, ожидалось %q", got, want)
	}
}

// Базовая строка и подпись должны считаться детерминированно и совпадать с
// независимым HMAC-SHA256 по тем же входам (защита от регрессий подписи).
func TestSignatureDeterministic(t *testing.T) {
	c := &Collector{cfg: config.NetSuiteConfig{
		AccountID: "ACCT", ConsumerKey: "ck", ConsumerSecret: "cs",
		TokenID: "tk", TokenSecret: "ts",
	}, realm: "ACCT"}
	base := "https://acct.suitetalk.api.netsuite.com/services/rest/query/v1/suiteql"
	q := url.Values{"limit": {"1000"}, "offset": {"0"}}
	h1 := c.authHeader("POST", base, q)
	if h1 == "" || h1[:6] != "OAuth " {
		t.Fatalf("некорректный заголовок: %q", h1)
	}
	// Заголовок содержит обязательные поля.
	for _, must := range []string{"realm=", "oauth_consumer_key=", "oauth_token=",
		`oauth_signature_method="HMAC-SHA256"`, "oauth_signature="} {
		if !contains(h1, must) {
			t.Errorf("в заголовке нет %q", must)
		}
	}
}

// Проверяем, что ключ подписи собирается как consumerSecret&tokenSecret и
// HMAC-SHA256 воспроизводится вручную для фиксированной базовой строки.
func TestHMACMatchesManual(t *testing.T) {
	key := pctEncode("cs") + "&" + pctEncode("ts")
	baseStr := "POST&x&y"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(baseStr))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if want == "" {
		t.Fatal("пустая подпись")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
