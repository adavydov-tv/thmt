// Package discovery по e-mail сотрудника находит его аккаунты во всех
// подключённых системах: accountId в Jira Cloud, username в GitLab,
// user id в Slack. Google-идентификатор — сам e-mail.
//
// Результат — заготовка карточки Person: каждая система опрашивается
// независимо, отказ одной не мешает остальным, и по каждой возвращается
// человекочитаемый статус для UI.
package discovery

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Status — итог поиска в одной системе.
type Status string

const (
	StatusFound    Status = "found"
	StatusNotFound Status = "not_found"
	StatusError    Status = "error"
	StatusDisabled Status = "disabled"
)

// SystemResult — что нашлось (или не нашлось) в одной системе.
type SystemResult struct {
	Source string `json:"source"`
	Status Status `json:"status"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Service выполняет поиск аккаунтов.
type Service struct {
	cfg *config.Config
	log *slog.Logger
}

// New создаёт сервис. Логгер может быть nil.
func New(cfg *config.Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{cfg: cfg, log: log.With("component", "discovery")}
}

// Discover опрашивает все включённые системы параллельно и собирает заготовку
// карточки Person. Person.Key и DisplayName заполняет вызывающая сторона.
func (s *Service) Discover(ctx context.Context, email string) (models.Person, []SystemResult) {
	email = strings.ToLower(strings.TrimSpace(email))
	p := models.Person{Email: email}

	results := make([]SystemResult, 4)
	var wg sync.WaitGroup
	run := func(i int, fn func() SystemResult) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = fn()
		}()
	}

	run(0, func() SystemResult { return s.findJira(ctx, email) })
	run(1, func() SystemResult { return s.findGitLab(ctx, email) })
	run(2, func() SystemResult { return s.findSlack(ctx, email) })
	run(3, func() SystemResult {
		if !s.cfg.Google.Enabled {
			return SystemResult{Source: "gdocs", Status: StatusDisabled, Detail: "источник выключен"}
		}
		// Отдельного справочника пользователей у Drive нет — идентификатор
		// человека в Google и есть его корпоративный e-mail.
		return SystemResult{Source: "gdocs", Status: StatusFound, Value: email}
	})
	wg.Wait()

	for _, r := range results {
		if r.Status != StatusFound {
			continue
		}
		switch r.Source {
		case "jira":
			p.JiraAccount = r.Value
		case "gitlab":
			p.GitLabUser = r.Value
		case "slack":
			p.SlackUser = r.Value
		case "gdocs":
			p.GoogleEmail = r.Value
		}
	}
	return p, results
}

// ---- Jira ----

type jiraUser struct {
	AccountID   string `json:"accountId"`
	AccountType string `json:"accountType"`
	Email       string `json:"emailAddress"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

// findJira ищет accountId через /rest/api/3/user/search. Jira Cloud прячет
// e-mail в ответе у большинства пользователей, но сам поиск по query=email
// работает: точное совпадение адреса возвращает ровно одного человека.
func (s *Service) findJira(ctx context.Context, email string) SystemResult {
	out := SystemResult{Source: "jira"}
	if !s.cfg.Jira.Enabled {
		out.Status = StatusDisabled
		out.Detail = "источник выключен"
		return out
	}
	cred := base64.StdEncoding.EncodeToString([]byte(s.cfg.Jira.Email + ":" + s.cfg.Jira.APIToken))
	cl, err := httpx.New(httpx.Options{
		BaseURL:    s.cfg.Jira.BaseURL,
		Timeout:    s.cfg.Sync.HTTPTimeout,
		MaxRetries: 1,
		Headers:    map[string]string{"Authorization": "Basic " + cred},
	})
	if err != nil {
		out.Status = StatusError
		out.Detail = err.Error()
		return out
	}
	var users []jiraUser
	q := url.Values{"query": {email}, "maxResults": {"10"}}
	if _, err := cl.GetJSON(ctx, "/rest/api/3/user/search", q, &users); err != nil {
		out.Status = StatusError
		out.Detail = err.Error()
		return out
	}
	// Сначала точное совпадение адреса (если Jira его отдала), затем
	// единственный активный человек в выдаче.
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.Email), email) {
			out.Status = StatusFound
			out.Value = u.AccountID
			out.Detail = u.DisplayName
			return out
		}
	}
	var active []jiraUser
	for _, u := range users {
		if u.Active && (u.AccountType == "" || u.AccountType == "atlassian") {
			active = append(active, u)
		}
	}
	switch len(active) {
	case 1:
		out.Status = StatusFound
		out.Value = active[0].AccountID
		out.Detail = active[0].DisplayName
	case 0:
		out.Status = StatusNotFound
		out.Detail = "поиск по e-mail не дал результатов"
	default:
		out.Status = StatusNotFound
		out.Detail = fmt.Sprintf("неоднозначно: %d кандидатов, укажите accountId вручную", len(active))
	}
	return out
}

// ---- GitLab ----

type glUser struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	PublicEmail string `json:"public_email"`
	State       string `json:"state"`
}

// findGitLab ищет username через /api/v4/users?search=email. Поле email в
// ответе видно только администраторам, поэтому точное совпадение проверяется
// и по нему, и по public_email, а одиночный результат принимается как есть.
func (s *Service) findGitLab(ctx context.Context, email string) SystemResult {
	out := SystemResult{Source: "gitlab"}
	if !s.cfg.GitLab.Enabled {
		out.Status = StatusDisabled
		out.Detail = "источник выключен"
		return out
	}
	cl, err := httpx.New(httpx.Options{
		BaseURL:       strings.TrimRight(s.cfg.GitLab.BaseURL, "/") + "/api/v4",
		Timeout:       s.cfg.Sync.HTTPTimeout,
		MaxRetries:    1,
		SkipTLSVerify: s.cfg.GitLab.SkipTLSVerify,
		Headers:       map[string]string{"PRIVATE-TOKEN": s.cfg.GitLab.Token},
	})
	if err != nil {
		out.Status = StatusError
		out.Detail = err.Error()
		return out
	}
	var users []glUser
	if _, err := cl.GetJSON(ctx, "/users", url.Values{"search": {email}}, &users); err != nil {
		out.Status = StatusError
		out.Detail = err.Error()
		return out
	}
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.Email), email) ||
			strings.EqualFold(strings.TrimSpace(u.PublicEmail), email) {
			out.Status = StatusFound
			out.Value = u.Username
			out.Detail = u.Name
			return out
		}
	}
	var active []glUser
	for _, u := range users {
		if u.State == "" || u.State == "active" {
			active = append(active, u)
		}
	}
	switch len(active) {
	case 1:
		out.Status = StatusFound
		out.Value = active[0].Username
		out.Detail = active[0].Name
	case 0:
		out.Status = StatusNotFound
		out.Detail = "поиск по e-mail не дал результатов"
	default:
		out.Status = StatusNotFound
		out.Detail = fmt.Sprintf("неоднозначно: %d кандидатов, укажите username вручную", len(active))
	}
	return out
}

// ---- Slack ----

type slackLookupResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	User  struct {
		ID      string `json:"id"`
		Profile struct {
			RealName string `json:"real_name"`
		} `json:"profile"`
	} `json:"user"`
}

// findSlack ищет user id через users.lookupByEmail (нужен scope
// users:read.email). Пробуем ботовый токен, затем пользовательский.
func (s *Service) findSlack(ctx context.Context, email string) SystemResult {
	out := SystemResult{Source: "slack"}
	if !s.cfg.Slack.Enabled {
		out.Status = StatusDisabled
		out.Detail = "источник выключен"
		return out
	}
	tokens := []string{s.cfg.Slack.BotToken, s.cfg.Slack.UserToken}
	var lastErr string
	for _, token := range tokens {
		if token == "" {
			continue
		}
		cl, err := httpx.New(httpx.Options{
			BaseURL:    "https://slack.com/api",
			Timeout:    s.cfg.Sync.HTTPTimeout,
			MaxRetries: 1,
			Headers:    map[string]string{"Authorization": "Bearer " + token},
		})
		if err != nil {
			lastErr = err.Error()
			continue
		}
		var resp slackLookupResp
		if _, err := cl.PostForm(ctx, "/users.lookupByEmail", url.Values{"email": {email}}, &resp); err != nil {
			lastErr = err.Error()
			continue
		}
		if resp.OK && resp.User.ID != "" {
			out.Status = StatusFound
			out.Value = resp.User.ID
			out.Detail = resp.User.Profile.RealName
			return out
		}
		if resp.Error == "users_not_found" {
			out.Status = StatusNotFound
			out.Detail = "пользователь с таким e-mail не найден"
			return out
		}
		lastErr = resp.Error
	}
	if lastErr == "missing_scope" {
		lastErr = "missing_scope: токену нужен scope users:read.email"
	}
	out.Status = StatusError
	out.Detail = lastErr
	return out
}
