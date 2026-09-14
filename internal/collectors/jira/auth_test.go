package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// newTestCollector поднимает фейковый Jira: /myself отвечает кодом myselfStatus,
// /search/jql — пустой страницей (как настоящая Jira Cloud анониму).
func newTestCollector(t *testing.T, myselfStatus int) (*Collector, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var myselfCalls, searchCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/myself"):
			myselfCalls.Add(1)
			w.WriteHeader(myselfStatus)
			if myselfStatus == http.StatusOK {
				_ = json.NewEncoder(w).Encode(map[string]any{"accountId": "acc-bot", "active": true})
				return
			}
			_, _ = w.Write([]byte("Client must be authenticated to access this resource."))
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/search/jql"):
			searchCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "isLast": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := New(config.JiraConfig{BaseURL: srv.URL, Email: "bot@example.com", APIToken: "dead"}, 5*time.Second, 0, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, &myselfCalls, &searchCalls
}

func testRequest() collectors.Request {
	now := time.Now().UTC()
	return collectors.Request{
		Person: models.Person{Key: "ivanov", JiraAccount: "acc-ivanov"},
		From:   now.Add(-24 * time.Hour),
		To:     now,
	}
}

// Протухший токен: Jira отдаёт 401 на /myself — сбор должен упасть с явной
// ошибкой, а не вернуть «done, 0 событий».
func TestCollect_RejectsWhenUnauthorized(t *testing.T) {
	c, myself, search := newTestCollector(t, http.StatusUnauthorized)

	_, err := c.Collect(context.Background(), testRequest())
	if err == nil {
		t.Fatal("ожидалась ошибка авторизации, получен nil")
	}
	if !strings.Contains(err.Error(), "авторизация отклонена") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("неожиданный текст ошибки: %v", err)
	}
	if myself.Load() != 1 {
		t.Fatalf("/myself должен быть вызван 1 раз, вызван %d", myself.Load())
	}
	if search.Load() != 0 {
		t.Fatalf("при отказе авторизации поиск не должен запускаться, вызван %d раз", search.Load())
	}
}

// Валидный токен: /myself проверяется один раз на коллектор, а не на каждого
// человека — второй Collect идёт сразу в поиск.
func TestCollect_AuthCheckIsCached(t *testing.T) {
	c, myself, search := newTestCollector(t, http.StatusOK)

	for i := 0; i < 3; i++ {
		if _, err := c.Collect(context.Background(), testRequest()); err != nil {
			t.Fatalf("Collect #%d: %v", i+1, err)
		}
	}
	if myself.Load() != 1 {
		t.Fatalf("/myself должен быть вызван 1 раз, вызван %d", myself.Load())
	}
	if search.Load() != 3 {
		t.Fatalf("ожидалось 3 поиска, выполнено %d", search.Load())
	}
}
