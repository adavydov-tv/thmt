// Package netsuite собирает аудит действий в NetSuite ERP через SuiteQL (REST):
//   - SystemNote — изменения записей (кто, когда, какое поле, старое→новое);
//   - LoginAudit — входы в NetSuite (с IP и статусом).
//
// Аутентификация — Token-Based Authentication (OAuth 1.0a, HMAC-SHA256).
// Это ГРУППОВОЙ коллектор: один набор запросов на всю группу, строки
// раскладываются по актору (по e-mail, иначе по отображаемому имени).
//
// ВНИМАНИЕ: имена колонок SuiteQL зависят от версии аккаунта; запросы в
// systemNoteQuery/loginAuditQuery, возможно, придётся подстроить под схему
// конкретного NetSuite (проверяется на живом аккаунте с рабочим токеном).
package netsuite

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Collector — клиент NetSuite SuiteQL под TBA.
type Collector struct {
	cfg    config.NetSuiteConfig
	log    *slog.Logger
	client *http.Client
	host   string // <account>.suitetalk.api.netsuite.com
	realm  string // account id для realm в заголовке OAuth
}

// New создаёт коллектор. Хост SuiteTalk строится из AccountID: подчёркивания
// → дефисы, нижний регистр (напр. 1234567_SB1 → 1234567-sb1).
func New(cfg config.NetSuiteConfig, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.AccountID == "" || cfg.ConsumerKey == "" || cfg.ConsumerSecret == "" ||
		cfg.TokenID == "" || cfg.TokenSecret == "" {
		return nil, fmt.Errorf("netsuite: нужны NETSUITE_ACCOUNT_ID и все четыре секрета TBA")
	}
	hostAcct := strings.ToLower(strings.ReplaceAll(cfg.AccountID, "_", "-"))
	return &Collector{
		cfg:    cfg,
		log:    log.With("collector", "netsuite"),
		client: &http.Client{Timeout: timeout},
		host:   hostAcct + ".suitetalk.api.netsuite.com",
		realm:  strings.ToUpper(cfg.AccountID),
	}, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceNetSuite }

// CollectGroup собирает аудит всей группы за период одним набором запросов.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	ids := buildIdentities(people)
	out := map[string][]models.Event{}

	changes, err := c.collectSystemNotes(ctx, from, to, ids, out)
	if err != nil {
		return nil, "", err
	}
	logins, err := c.collectLoginAudit(ctx, from, to, ids, out)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("NetSuite audit: изменений записей %d, входов %d", changes, logins)
	return out, note, nil
}

// ---- SuiteQL-запросы ----

// systemNoteQuery — изменения записей за период. BUILTIN.DF разворачивает
// ссылку на пользователя в отображаемое имя; e-mail берём джойном на Employee.
func systemNoteQuery(from, to time.Time) string {
	return fmt.Sprintf(`
SELECT sn.date AS ts, BUILTIN.DF(sn.name) AS actor_name, e.email AS actor_email,
       sn.recordtypeid AS record_type, sn.recordid AS record_id,
       sn.field AS field, sn.oldvalue AS oldvalue, sn.newvalue AS newvalue, sn.context AS context
FROM SystemNote sn
LEFT JOIN Employee e ON e.id = sn.name
WHERE sn.date >= TO_DATE('%s','YYYY-MM-DD') AND sn.date < TO_DATE('%s','YYYY-MM-DD')
ORDER BY sn.date`, from.Format("2006-01-02"), to.Format("2006-01-02"))
}

// loginAuditQuery — входы за период. emailaddress — e-mail, под которым вошли.
func loginAuditQuery(from, to time.Time) string {
	return fmt.Sprintf(`
SELECT la.date AS ts, BUILTIN.DF(la."user") AS actor_name, la.emailaddress AS actor_email,
       la.role AS role, la.ipaddress AS ip, la.status AS status, la.oauthappname AS app
FROM LoginAudit la
WHERE la.date >= TO_DATE('%s','YYYY-MM-DD') AND la.date < TO_DATE('%s','YYYY-MM-DD')
ORDER BY la.date`, from.Format("2006-01-02"), to.Format("2006-01-02"))
}

func (c *Collector) collectSystemNotes(ctx context.Context, from, to time.Time, ids map[string]string, out map[string][]models.Event) (int, error) {
	rows, err := c.querySuiteQL(ctx, systemNoteQuery(from, to))
	if err != nil {
		return 0, fmt.Errorf("netsuite systemnote: %w", err)
	}
	n := 0
	for _, r := range rows {
		pk := matchActor(ids, r["actor_email"], r["actor_name"])
		if pk == "" {
			continue
		}
		ts := parseNSTime(r["ts"])
		if ts.IsZero() || ts.Before(from) || !ts.Before(to) {
			continue
		}
		field := r["field"]
		title := fmt.Sprintf("NetSuite: %s #%s · %s", r["record_type"], r["record_id"], field)
		ev := models.Event{
			PersonKey:  matchActorKey(ids, r["actor_email"], r["actor_name"]),
			Source:     models.SourceNetSuite,
			Type:       models.TypeNetSuiteChange,
			ExternalID: fmt.Sprintf("nschange:%s:%s:%s:%s", r["record_type"], r["record_id"], field, r["ts"]),
			OccurredAt: ts,
			Title:      title,
			Meta: map[string]any{
				"record_type": r["record_type"],
				"record_id":   r["record_id"],
				"field":       field,
				"old":         r["oldvalue"],
				"new":         r["newvalue"],
				"context":     r["context"],
				"actor":       firstNonEmpty(r["actor_email"], r["actor_name"]),
			},
		}
		ev.Normalize()
		out[pk] = append(out[pk], ev)
		n++
	}
	return n, nil
}

func (c *Collector) collectLoginAudit(ctx context.Context, from, to time.Time, ids map[string]string, out map[string][]models.Event) (int, error) {
	rows, err := c.querySuiteQL(ctx, loginAuditQuery(from, to))
	if err != nil {
		return 0, fmt.Errorf("netsuite loginaudit: %w", err)
	}
	n := 0
	for _, r := range rows {
		// Только успешные входы — действие человека (неуспешные не считаем).
		if st := strings.ToLower(r["status"]); st != "" && !strings.Contains(st, "success") {
			continue
		}
		pk := matchActor(ids, r["actor_email"], r["actor_name"])
		if pk == "" {
			continue
		}
		ts := parseNSTime(r["ts"])
		if ts.IsZero() || ts.Before(from) || !ts.Before(to) {
			continue
		}
		ev := models.Event{
			PersonKey:  pk,
			Source:     models.SourceNetSuite,
			Type:       models.TypeNetSuiteLogin,
			ExternalID: fmt.Sprintf("nslogin:%s:%s", firstNonEmpty(r["actor_email"], r["actor_name"]), r["ts"]),
			OccurredAt: ts,
			Title:      "Вход в NetSuite",
			Meta: map[string]any{
				"ip":    r["ip"],
				"role":  r["role"],
				"app":   r["app"],
				"actor": firstNonEmpty(r["actor_email"], r["actor_name"]),
			},
		}
		ev.Normalize()
		out[pk] = append(out[pk], ev)
		n++
	}
	return n, nil
}

// ---- транспорт SuiteQL + пагинация ----

type suiteQLResp struct {
	Items      []map[string]any `json:"items"`
	HasMore    bool             `json:"hasMore"`
	Offset     int              `json:"offset"`
	TotalCount int              `json:"totalResults"`
}

// querySuiteQL выполняет SuiteQL с постраничной выборкой, возвращая все строки
// как строковые карты (значения приводятся к строке).
func (c *Collector) querySuiteQL(ctx context.Context, q string) ([]map[string]string, error) {
	limit := c.cfg.PageSize
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var all []map[string]string
	for offset := 0; offset < 500000; offset += limit {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		page, more, err := c.suiteQLPage(ctx, q, limit, offset)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if !more || len(page) == 0 {
			break
		}
	}
	return all, nil
}

func (c *Collector) suiteQLPage(ctx context.Context, q string, limit, offset int) ([]map[string]string, bool, error) {
	base := "https://" + c.host + "/services/rest/query/v1/suiteql"
	query := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	full := base + "?" + query.Encode()

	body, _ := json.Marshal(map[string]string{"q": q})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, strings.NewReader(string(body)))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "transient")
	req.Header.Set("Authorization", c.authHeader(http.MethodPost, base, query))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var buf [512]byte
		nb, _ := resp.Body.Read(buf[:])
		return nil, false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf[:nb])))
	}
	var out suiteQLResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("разбор ответа: %w", err)
	}
	rows := make([]map[string]string, 0, len(out.Items))
	for _, it := range out.Items {
		row := make(map[string]string, len(it))
		for k, v := range it {
			row[strings.ToLower(k)] = anyToString(v)
		}
		rows = append(rows, row)
	}
	return rows, out.HasMore, nil
}

// authHeader строит заголовок OAuth 1.0a TBA (HMAC-SHA256). Query-параметры
// URL (limit/offset) входят в базовую строку подписи; realm — только в заголовок.
func (c *Collector) authHeader(method, base string, query url.Values) string {
	oauth := map[string]string{
		"oauth_consumer_key":     c.cfg.ConsumerKey,
		"oauth_token":            c.cfg.TokenID,
		"oauth_signature_method": "HMAC-SHA256",
		"oauth_timestamp":        strconv.FormatInt(time.Now().Unix(), 10),
		"oauth_nonce":            nonce(),
		"oauth_version":          "1.0",
	}
	// Базовая строка: все oauth_* + query-параметры, отсортированные по ключу.
	params := url.Values{}
	for k, v := range oauth {
		params.Set(k, v)
	}
	for k, vs := range query {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	baseStr := method + "&" + pctEncode(base) + "&" + pctEncode(encodeSorted(params))
	key := pctEncode(c.cfg.ConsumerSecret) + "&" + pctEncode(c.cfg.TokenSecret)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(baseStr))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	parts := []string{fmt.Sprintf(`realm="%s"`, pctEncode(c.realm))}
	for _, k := range []string{"oauth_consumer_key", "oauth_token", "oauth_signature_method",
		"oauth_timestamp", "oauth_nonce", "oauth_version"} {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, pctEncode(oauth[k])))
	}
	parts = append(parts, fmt.Sprintf(`oauth_signature="%s"`, pctEncode(sig)))
	return "OAuth " + strings.Join(parts, ", ")
}

// encodeSorted кодирует параметры в отсортированную строку k=v&… с RFC3986.
func encodeSorted(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := append([]string{}, v[k]...)
		sort.Strings(vals)
		for _, val := range vals {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(pctEncode(k))
			b.WriteByte('=')
			b.WriteString(pctEncode(val))
		}
	}
	return b.String()
}

// pctEncode — RFC3986 percent-encoding (unreserved: A-Za-z0-9-._~).
func pctEncode(s string) string {
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

func nonce() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// ---- атрибуция ----

// buildIdentities строит карту «email/локальная часть/имя → person key».
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
		add(strings.ToLower(p.DisplayName), p.Key)
	}
	return ids
}

// matchActor возвращает person key по e-mail (сильнее), иначе по имени.
func matchActor(ids map[string]string, email, name string) string {
	if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
		if pk, ok := ids[e]; ok {
			return pk
		}
		if i := strings.Index(e, "@"); i > 0 {
			if pk, ok := ids[e[:i]]; ok {
				return pk
			}
		}
	}
	if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
		if pk, ok := ids[n]; ok {
			return pk
		}
	}
	return ""
}

// matchActorKey — то же, но всегда через matchActor (для читаемости вызова).
func matchActorKey(ids map[string]string, email, name string) string {
	return matchActor(ids, email, name)
}

// ---- утилиты значений ----

func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// parseNSTime разбирает дату/время NetSuite. SuiteQL отдаёт разные форматы в
// зависимости от настроек аккаунта — пробуем распространённые.
func parseNSTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"01/02/2006 3:04 pm",
		"01/02/2006 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
