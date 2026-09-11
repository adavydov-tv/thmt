// Package ai — клиент локального AI-скорера сообщений (отдельный docker-
// контейнер, см. ai-scorer/): оценка содержательности текста от 0 до 10.
package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/httpx"
)

// Client — клиент AI-скорера.
type Client struct {
	cl *httpx.Client
}

// New создаёт клиент. Скорер локальный, поэтому таймаут щадящий, но батчи
// эмбеддингов на CPU не мгновенные — меньше минуты ставить не стоит.
func New(cfg config.AIConfig, maxRetries int) (*Client, error) {
	cl, err := httpx.New(httpx.Options{
		BaseURL:    cfg.BaseURL,
		Timeout:    2 * time.Minute,
		MaxRetries: maxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("ai: %w", err)
	}
	return &Client{cl: cl}, nil
}

// Status — состояние скорера.
type Status struct {
	Status    string `json:"status"`
	Model     string `json:"model"`
	Labels    int    `json:"labels"`
	MinLabels int    `json:"min_labels"`
	Trained   bool   `json:"trained"`
	Mode      string `json:"mode"`
}

// Health возвращает состояние скорера.
func (c *Client) Health(ctx context.Context) (Status, error) {
	var s Status
	_, err := c.cl.GetJSON(ctx, "/health", nil, &s)
	return s, err
}

// Score оценивает тексты от 0 до 10. Порядок оценок соответствует текстам.
func (c *Client) Score(ctx context.Context, texts []string) ([]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var out struct {
		Scores []float64 `json:"scores"`
	}
	// Батчами: у скорера лимит на запрос, а эмбеддинги на CPU не бесплатные.
	const batch = 200
	scores := make([]float64, 0, len(texts))
	for i := 0; i < len(texts); i += batch {
		j := min(i+batch, len(texts))
		if _, err := c.cl.PostJSON(ctx, "/score", map[string]any{"texts": texts[i:j]}, &out); err != nil {
			return nil, fmt.Errorf("ai: оценка текстов: %w", err)
		}
		if len(out.Scores) != j-i {
			return nil, fmt.Errorf("ai: скорер вернул %d оценок на %d текстов", len(out.Scores), j-i)
		}
		scores = append(scores, out.Scores...)
	}
	return scores, nil
}

// Label — размеченный пример калибровки.
type Label struct {
	ID        int64   `json:"id"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
	CreatedAt string  `json:"created_at"`
}

// Labels возвращает примеры калибровки.
func (c *Client) Labels(ctx context.Context) ([]Label, error) {
	var out struct {
		Labels []Label `json:"labels"`
	}
	_, err := c.cl.GetJSON(ctx, "/labels", nil, &out)
	return out.Labels, err
}

// AddLabel сохраняет пример и переобучает модель.
func (c *Client) AddLabel(ctx context.Context, text string, score float64) error {
	_, err := c.cl.PostJSON(ctx, "/labels", map[string]any{"text": text, "score": score}, nil)
	return err
}

// DeleteLabel удаляет пример и переобучает модель.
func (c *Client) DeleteLabel(ctx context.Context, id int64) error {
	_, err := c.cl.DoJSON(ctx, "DELETE", fmt.Sprintf("/labels/%d", id), nil, nil, nil)
	return err
}

// Rules — автоправила скорера: такие сообщения получают 0 без прогона
// через модель.
type Rules struct {
	MinLength     int      `json:"min_length"`
	Patterns      []string `json:"patterns"`
	DropEmojiOnly bool     `json:"drop_emoji_only"`
}

// GetRules возвращает автоправила.
func (c *Client) GetRules(ctx context.Context) (Rules, error) {
	var r Rules
	_, err := c.cl.GetJSON(ctx, "/rules", nil, &r)
	return r, err
}

// SetRules сохраняет автоправила.
func (c *Client) SetRules(ctx context.Context, r Rules) (Rules, error) {
	var out Rules
	_, err := c.cl.DoJSON(ctx, "PUT", "/rules", nil, r, &out)
	return out, err
}

// Train переобучает модель на текущей разметке.
func (c *Client) Train(ctx context.Context) (Status, error) {
	if _, err := c.cl.PostJSON(ctx, "/train", map[string]any{}, nil); err != nil {
		return Status{}, err
	}
	return c.Health(ctx)
}
