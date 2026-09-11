// Package httpx — общий HTTP-клиент для коллекторов: таймауты, ретраи с
// экспоненциальной задержкой, уважение Retry-After и разбор JSON.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client — тонкая обёртка над http.Client с ретраями.
type Client struct {
	base       *url.URL
	http       *http.Client
	headers    http.Header
	maxRetries int
	// RateDelay — минимальная пауза между запросами (щадящий режим для API).
	RateDelay time.Duration
	last      time.Time
}

// Options — параметры конструктора.
type Options struct {
	BaseURL       string
	Timeout       time.Duration
	MaxRetries    int
	SkipTLSVerify bool
	Headers       map[string]string
	RateDelay     time.Duration
}

// New создаёт клиент.
func New(opt Options) (*Client, error) {
	var base *url.URL
	if opt.BaseURL != "" {
		u, err := url.Parse(opt.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("некорректный BaseURL %q: %w", opt.BaseURL, err)
		}
		base = u
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	if opt.SkipTLSVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // осознанно, для self-hosted с самоподписанным сертификатом
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 45 * time.Second
	}
	if opt.MaxRetries <= 0 {
		opt.MaxRetries = 3
	}
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "user-activity-dashboard/1.0")
	for k, v := range opt.Headers {
		h.Set(k, v)
	}
	return &Client{
		base:       base,
		http:       &http.Client{Timeout: opt.Timeout, Transport: tr},
		headers:    h,
		maxRetries: opt.MaxRetries,
		RateDelay:  opt.RateDelay,
	}, nil
}

// APIError — неуспешный ответ внешнего API.
type APIError struct {
	Status int
	URL    string
	Body   string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("HTTP %d от %s: %s", e.Status, e.URL, body)
}

// IsNotFound сообщает, что ресурс отсутствует или недоступен.
func (e *APIError) IsNotFound() bool {
	return e.Status == http.StatusNotFound || e.Status == http.StatusForbidden
}

// GetJSON выполняет GET и разбирает JSON-ответ в out.
// Возвращает заголовки ответа — они нужны для пагинации GitLab (X-Next-Page).
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	return c.DoJSON(ctx, http.MethodGet, path, query, nil, out)
}

// PostJSON выполняет POST с JSON-телом.
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) (http.Header, error) {
	return c.DoJSON(ctx, http.MethodPost, path, nil, body, out)
}

// PostForm выполняет POST с телом application/x-www-form-urlencoded (Slack Web API).
func (c *Client) PostForm(ctx context.Context, path string, form url.Values, out any) (http.Header, error) {
	return c.do(ctx, http.MethodPost, path, nil, strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded", out)
}

// DoJSON — общий метод с JSON-телом.
func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, body, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	return c.do(ctx, method, path, query, reader, "application/json", out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, out any) (http.Header, error) {
	target, err := c.resolve(path, query)
	if err != nil {
		return nil, err
	}

	var payload []byte
	if body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}

		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rdr)
		if err != nil {
			return nil, err
		}
		req.Header = c.headers.Clone()
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &APIError{Status: resp.StatusCode, URL: target, Body: string(respBody)}
			wait := retryAfter(resp.Header)
			if wait == 0 {
				wait = backoff(attempt)
			}
			if attempt == c.maxRetries {
				break
			}
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}

		if resp.StatusCode >= 400 {
			return resp.Header, &APIError{Status: resp.StatusCode, URL: target, Body: string(respBody)}
		}

		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				// HTML вместо JSON — почти всегда SSO-прокси перехватил запрос
				// (инстансы за OAuth-прокси доступны только из VPN).
				if b := strings.TrimSpace(string(respBody)); strings.HasPrefix(b, "<") {
					return resp.Header, fmt.Errorf(
						"ответ %s — HTML вместо JSON: запрос перехвачен SSO-прокси, проверьте VPN", target)
				}
				return resp.Header, fmt.Errorf("разбор ответа %s: %w", target, err)
			}
		}
		return resp.Header, nil
	}
	return nil, fmt.Errorf("запрос %s не удался после %d попыток: %w", target, c.maxRetries+1, lastErr)
}

func (c *Client) resolve(path string, query url.Values) (string, error) {
	var target string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		target = path
	} else if c.base != nil {
		u := *c.base
		u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
		target = u.String()
	} else {
		return "", fmt.Errorf("относительный путь %q без BaseURL", path)
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + query.Encode()
	}
	return target, nil
}

func (c *Client) throttle(ctx context.Context) error {
	if c.RateDelay <= 0 {
		return nil
	}
	wait := c.RateDelay - time.Since(c.last)
	c.last = time.Now()
	if wait > 0 {
		return sleep(ctx, wait)
	}
	return nil
}

func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		v = h.Get("X-RateLimit-Reset")
	}
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n > 300 { // похоже на unix-таймстемп
			d := time.Until(time.Unix(int64(n), 0))
			if d > 0 && d < 5*time.Minute {
				return d
			}
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 && d < 5*time.Minute {
			return d
		}
	}
	return 0
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
