package hrdb

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Switcher выбирает активную HRDB: «cloud» — новая (Atlassian Cloud Assets),
// «dc» — старая (Insight на jira.xtools.tv). У каждого клиента свои кэши,
// поэтому переключение мгновенное; прогрев запускается при первой активации.
type Switcher struct {
	mu      sync.RWMutex
	mode    string
	clients map[string]*Client
	warmed  map[string]bool

	ctx context.Context
	log *slog.Logger
}

// NewSwitcher собирает переключатель из доступных клиентов. Если начальный
// режим недоступен, берётся любой доступный. Возвращает nil, когда клиентов
// нет вовсе — код выше трактует это как «HRDB не настроен».
func NewSwitcher(ctx context.Context, log *slog.Logger, mode string, clients map[string]*Client) *Switcher {
	alive := map[string]*Client{}
	for name, c := range clients {
		if c != nil {
			alive[name] = c
		}
	}
	if len(alive) == 0 {
		return nil
	}
	if _, ok := alive[mode]; !ok {
		names := make([]string, 0, len(alive))
		for n := range alive {
			names = append(names, n)
		}
		sort.Strings(names)
		mode = names[0]
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Switcher{mode: mode, clients: alive, warmed: map[string]bool{}, ctx: ctx, log: log}
	s.warm(mode)
	return s
}

// Current — активный клиент; nil-безопасен (nil-Switcher → nil-клиент).
func (s *Switcher) Current() *Client {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[s.mode]
}

// Client возвращает клиент конкретной HRDB по имени (cloud | dc) независимо
// от активного режима; nil, если такая не настроена. Нужен фичам, живущим
// только в одной из HRDB (журнал изменений объектов — только в Cloud Assets).
func (s *Switcher) Client(name string) *Client {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[name]
}

// Mode — имя активной HRDB.
func (s *Switcher) Mode() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// Available — отсортированный список настроенных HRDB.
func (s *Switcher) Available() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.clients))
	for n := range s.clients {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SetMode переключает активную HRDB и греет её кэши в фоне.
func (s *Switcher) SetMode(mode string) error {
	if s == nil {
		return fmt.Errorf("HRDB не настроен")
	}
	s.mu.Lock()
	if _, ok := s.clients[mode]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("HRDB %q не настроена (доступны: %v)", mode, s.Available())
	}
	s.mode = mode
	s.mu.Unlock()
	s.warm(mode)
	return nil
}

// warm запускает вечный прогрев кэшей клиента один раз за время жизни.
func (s *Switcher) warm(mode string) {
	s.mu.Lock()
	c, done := s.clients[mode], s.warmed[mode]
	if !done {
		s.warmed[mode] = true
	}
	s.mu.Unlock()
	if done || c == nil {
		return
	}
	go c.WarmUp(s.ctx, s.log.With("hrdb", mode))
}
