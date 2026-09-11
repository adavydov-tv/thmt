// Package claude собирает использование Claude Code, выгружаемое во внешний
// S3-бакет. Раскладка ключей: <prefix>dt=YYYY-MM-DD/<host>.jsonl, где каждый
// объект — JSON-запись по одному человеку/машине за день:
//
//	{ "email": "...", "report": { "daily": [ {"period":"2026-09-10",
//	  "requests": 296, "totalTokens": 63269767, "modelsUsed": [...] }, ... ] } }
//
// Это ГРУППОВОЙ коллектор: один обход S3 на всю группу, записи раскладываются
// по актору (по e-mail). За каждый день с активностью создаётся одно событие
// claude.usage (effort = число запросов).
//
// Доступ к S3 — по AWS Signature V4 (без внешнего SDK, как OAuth1 в netsuite).
// Для AWS Endpoint пустой (virtual-hosted style), для S3-совместимого хранилища
// (MinIO) задаётся Endpoint (path-style).
package claude

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Collector — клиент S3 (SigV4) поверх выгрузок использования Claude Code.
type Collector struct {
	cfg       config.ClaudeConfig
	log       *slog.Logger
	client    *http.Client
	scheme    string // http|https
	host      string // хост запроса (bucket.s3.region… либо endpoint)
	pathStyle bool   // true → путь /bucket/key (endpoint/MinIO)
	region    string
}

// New создаёт коллектор. Для AWS хост строится из бакета и региона
// (virtual-hosted). Если задан Endpoint — используется path-style против него.
func New(cfg config.ClaudeConfig, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("claude: нужны CLAUDE_S3_BUCKET, CLAUDE_S3_ACCESS_KEY_ID и CLAUDE_S3_SECRET_ACCESS_KEY")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	c := &Collector{
		cfg:    cfg,
		log:    log.With("collector", "claude"),
		client: &http.Client{Timeout: timeout},
		region: region,
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("claude: некорректный CLAUDE_S3_ENDPOINT %q", cfg.Endpoint)
		}
		c.scheme = u.Scheme
		if c.scheme == "" {
			c.scheme = "https"
		}
		c.host = u.Host
		c.pathStyle = true
	} else {
		c.scheme = "https"
		c.host = fmt.Sprintf("%s.s3.%s.amazonaws.com", cfg.Bucket, region)
		c.pathStyle = false
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceClaude }

// Collect — одиночный человек (через групповой путь).
func (c *Collector) Collect(ctx context.Context, req collectors.Request) (collectors.Result, error) {
	var res collectors.Result
	byPerson, note, err := c.CollectGroup(ctx, []models.Person{req.Person}, req.From, req.To)
	if err != nil {
		return res, err
	}
	res.Events = byPerson[req.Person.Key]
	res.Note = note
	return res, nil
}

// CollectGroup обходит dt-партиции за период, качает все объекты и раскладывает
// дневное использование по людям (по e-mail).
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	ids := buildIdentities(people)
	out := map[string][]models.Event{}

	days := dateRange(from.UTC(), to.UTC())
	if c.cfg.MaxDays > 0 && len(days) > c.cfg.MaxDays {
		days = days[len(days)-c.cfg.MaxDays:] // берём хвост — самые свежие дни
	}
	allowed := make(map[string]bool, len(days))
	for _, d := range days {
		allowed[d] = true
	}

	// ListBucket у read-only робота разрешён только на базовом префиксе, поэтому
	// листим его один раз и фильтруем ключи по дню (dt=YYYY-MM-DD) на клиенте.
	keys, err := c.listKeys(ctx, c.cfg.Prefix)
	if err != nil {
		return nil, "", fmt.Errorf("claude list %s: %w", c.cfg.Prefix, err)
	}
	files, matched := 0, 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return out, "", err
		}
		if !strings.HasSuffix(key, ".jsonl") && !strings.HasSuffix(key, ".json") {
			continue
		}
		if d := keyDate(key); d != "" && !allowed[d] {
			continue // партиция вне окна — не качаем
		}
		body, err := c.getObject(ctx, key)
		if err != nil {
			return nil, "", fmt.Errorf("claude get %s: %w", key, err)
		}
		files++
		matched += c.parseObject(body, key, ids, from, to, out)
	}
	note := fmt.Sprintf("Claude Code: файлов %d, дней активности %d", files, matched)
	return out, note, nil
}

// parseObject разбирает объект (одна или несколько JSON-записей подряд) и
// добавляет события в out. Возвращает число добавленных событий.
func (c *Collector) parseObject(body []byte, key string, ids map[string]string, from, to time.Time, out map[string][]models.Event) int {
	dec := json.NewDecoder(bytes.NewReader(body))
	added := 0
	for {
		var rec usageRecord
		if err := dec.Decode(&rec); err != nil {
			if err != io.EOF {
				c.log.Warn("не разобрать запись Claude usage", "key", key, "err", err)
			}
			break
		}
		pk := matchActor(ids, rec.Email)
		if pk == "" {
			continue
		}
		for _, d := range rec.Report.Daily {
			if d.Requests <= 0 {
				continue // день без активности — пропускаем
			}
			ts := parseDay(d.Period)
			if ts.IsZero() || ts.Before(from) || !ts.Before(to) {
				continue
			}
			ev := models.Event{
				PersonKey:  pk,
				Source:     models.SourceClaude,
				Type:       models.TypeClaudeUsage,
				ExternalID: fmt.Sprintf("claude:%s:%s:%s", strings.ToLower(rec.Email), d.Period, rec.Hostname),
				OccurredAt: ts,
				Title:      fmt.Sprintf("Claude Code: %d запросов", d.Requests),
				Effort:     float64(d.Requests),
				EffortUnit: "requests",
				Meta: map[string]any{
					"requests":              d.Requests,
					"total_tokens":          d.TotalTokens,
					"input_tokens":          d.InputTokens,
					"output_tokens":         d.OutputTokens,
					"cache_read_tokens":     d.CacheReadTokens,
					"cache_creation_tokens": d.CacheCreationTokens,
					"models":                d.ModelsUsed,
					"hostname":              rec.Hostname,
					"os_version":            rec.OSVersion,
					"billing_type":          rec.BillingType,
					"actor":                 rec.Email,
				},
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
			added++
		}
	}
	return added
}

// usageRecord — интересующая нас часть JSON-записи выгрузки.
type usageRecord struct {
	Email       string `json:"email"`
	EventType   string `json:"event_type"`
	Hostname    string `json:"hostname"`
	OSVersion   string `json:"os_version"`
	BillingType string `json:"billing_type"`
	Report      struct {
		Daily []dailyUsage `json:"daily"`
	} `json:"report"`
}

type dailyUsage struct {
	Period              string   `json:"period"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	CacheCreationTokens int64    `json:"cacheCreationTokens"`
	CacheReadTokens     int64    `json:"cacheReadTokens"`
	TotalTokens         int64    `json:"totalTokens"`
	Requests            int64    `json:"requests"`
	ModelsUsed          []string `json:"modelsUsed"`
}

// ---- S3 REST (ListObjectsV2 + GetObject) ----

type listBucketResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// listKeys перечисляет ключи с данным префиксом (с пагинацией по
// continuation-token).
func (c *Collector) listKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		if err := ctx.Err(); err != nil {
			return keys, err
		}
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		q.Set("max-keys", "1000")
		if token != "" {
			q.Set("continuation-token", token)
		}
		path := "/"
		if c.pathStyle {
			path = "/" + c.cfg.Bucket
		}
		body, err := c.doGET(ctx, path, q)
		if err != nil {
			return keys, err
		}
		var res listBucketResult
		if err := xml.Unmarshal(body, &res); err != nil {
			return keys, fmt.Errorf("разбор ListBucketResult: %w", err)
		}
		for _, obj := range res.Contents {
			keys = append(keys, obj.Key)
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}
	return keys, nil
}

// getObject качает содержимое ключа.
func (c *Collector) getObject(ctx context.Context, key string) ([]byte, error) {
	path := "/" + key
	if c.pathStyle {
		path = "/" + c.cfg.Bucket + "/" + key
	}
	return c.doGET(ctx, path, nil)
}

// doGET подписывает и выполняет GET, возвращая тело при 2xx.
func (c *Collector) doGET(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if query == nil {
		query = url.Values{}
	}
	encPath := s3EncodePath(path)
	canonicalQuery := canonicalQueryString(query)

	rawURL := c.scheme + "://" + c.host + encPath
	if canonicalQuery != "" {
		rawURL += "?" + canonicalQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawPath = encPath
	c.sign(req, encPath, canonicalQuery)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 400)])))
	}
	return body, nil
}

// sign проставляет заголовки AWS Signature V4 (сервис s3) на запрос.
func (c *Collector) sign(req *http.Request, encPath, canonicalQuery string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	req.Header.Set("Host", c.host)
	req.Host = c.host
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", emptyHash)
	if c.cfg.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.cfg.SessionToken)
	}

	// Канонические заголовки (отсортированы по имени, значения обрезаны).
	type hdr struct{ k, v string }
	hs := []hdr{
		{"host", c.host},
		{"x-amz-content-sha256", emptyHash},
		{"x-amz-date", amzDate},
	}
	if c.cfg.SessionToken != "" {
		hs = append(hs, hdr{"x-amz-security-token", c.cfg.SessionToken})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].k < hs[j].k })
	var canonHeaders strings.Builder
	signedNames := make([]string, 0, len(hs))
	for _, h := range hs {
		canonHeaders.WriteString(h.k)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(strings.TrimSpace(h.v))
		canonHeaders.WriteByte('\n')
		signedNames = append(signedNames, h.k)
	}
	signedHeaders := strings.Join(signedNames, ";")

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		encPath,
		canonicalQuery,
		canonHeaders.String(),
		signedHeaders,
		emptyHash,
	}, "\n")

	scope := dateStamp + "/" + c.region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(c.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
}

// ---- утилиты подписи/кодирования ----

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// s3EncodePath кодирует путь по RFC3986, сохраняя разделители «/».
func s3EncodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

// canonicalQueryString строит каноническую строку запроса SigV4: ключи
// отсортированы, ключ и значение закодированы по RFC3986.
func canonicalQueryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := append([]string{}, q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(uriEncode(k))
			b.WriteByte('=')
			b.WriteString(uriEncode(v))
		}
	}
	return b.String()
}

// uriEncode — RFC3986 percent-encoding (unreserved: A-Za-z0-9-_.~), всё
// остальное — %XX в верхнем регистре.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '.' || ch == '_' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// ---- атрибуция и разбор дат ----

// buildIdentities строит карту «email/локальная часть → person key».
func buildIdentities(people []models.Person) map[string]string {
	ids := map[string]string{}
	add := func(v, key string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		ids[v] = key
		if i := strings.Index(v, "@"); i > 0 {
			ids[v[:i]] = key
		}
	}
	for _, p := range people {
		add(p.Email, p.Key)
		add(p.GoogleEmail, p.Key)
	}
	return ids
}

// matchActor возвращает person key по e-mail (или его локальной части).
func matchActor(ids map[string]string, email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return ""
	}
	if pk, ok := ids[e]; ok {
		return pk
	}
	if i := strings.Index(e, "@"); i > 0 {
		if pk, ok := ids[e[:i]]; ok {
			return pk
		}
	}
	return ""
}

// keyDate вытаскивает день партиции из ключа вида «…/dt=YYYY-MM-DD/…».
func keyDate(key string) string {
	i := strings.Index(key, "dt=")
	if i < 0 {
		return ""
	}
	s := key[i+3:]
	if len(s) < 10 {
		return ""
	}
	return s[:10]
}

// parseDay разбирает дату дня «YYYY-MM-DD» и ставит полдень UTC, чтобы событие
// стабильно попадало в нужные календарные сутки при пересчёте в таймзону офиса.
func parseDay(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.UTC)
}

// dateRange перечисляет даты «YYYY-MM-DD» от дня from до дня to включительно.
func dateRange(from, to time.Time) []string {
	d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	var out []string
	for !d.After(end) && len(out) < 3660 { // потолок ~10 лет от зацикливания
		out = append(out, d.Format("2006-01-02"))
		d = d.AddDate(0, 0, 1)
	}
	return out
}
