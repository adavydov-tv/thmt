package overtime

// Заявки HCM «Change request» со сменой гибридных дней. Проект HCM живёт в
// той же Jira DC, что и VAC, поэтому клиент общий: тот же токен, тот же
// транспорт. Поле «Hybrid remote days» содержит НОВЫЙ шаблон целиком,
// «Effective date» — дату вступления изменения в силу.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// HybridTicket — одобренная заявка на смену гибридных дней и/или формата
// работы (Office | Hybrid | Remote).
type HybridTicket struct {
	Key        string    `json:"key"`
	Employee   string    `json:"employee"`
	Days       string    `json:"days"` // новый шаблон: «Wednesday, Friday»
	WorkFormat string    `json:"work_format,omitempty"`
	Effective  time.Time `json:"effective"`
}

// hcmJQL — одобренные заявки со сменой гибридных дней или формата работы.
// «Effective date is not EMPTY» отсекает вакансии (Vacancy): у них тоже есть
// поле «Work format», но это формат ПОЗИЦИИ, а не изменение сотрудника.
const hcmJQL = `project = HCM AND ("Hybrid remote days" is not EMPTY OR "Work format" is not EMPTY) AND "Effective date" is not EMPTY AND resolution = Done ORDER BY created ASC`

const hcmCacheTTL = 10 * time.Minute

// HybridTickets возвращает все одобренные заявки на смену гибридных дней
// (кэш на hcmCacheTTL; протухший кэш отдаётся при ошибке сети).
func (c *Client) HybridTickets(ctx context.Context) ([]HybridTicket, error) {
	c.hcmMu.Lock()
	if !c.hcmAt.IsZero() && time.Since(c.hcmAt) < hcmCacheTTL {
		out := c.hcmCached
		c.hcmMu.Unlock()
		return out, nil
	}
	c.hcmMu.Unlock()

	tickets, err := c.fetchHybridTickets(ctx)
	if err != nil {
		c.hcmMu.Lock()
		stale := c.hcmCached
		had := !c.hcmAt.IsZero()
		c.hcmMu.Unlock()
		if had {
			return stale, nil
		}
		return nil, err
	}
	c.hcmMu.Lock()
	c.hcmAt, c.hcmCached = time.Now(), tickets
	c.hcmMu.Unlock()
	return tickets, nil
}

func (c *Client) fetchHybridTickets(ctx context.Context) ([]HybridTicket, error) {
	var out []HybridTicket
	// id кастомных полей резолвятся один раз через /rest/api/2/field — в инстансе
	// они глобальны (у старых тикетов различается лишь то, какие поля заполнены,
	// а не их id), поэтому *all + expand=names на каждой странице не нужны.
	m, err := c.fieldIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("hcm: список полей Jira: %w", err)
	}
	employeeID := m["employee"]
	daysID := m["hybrid remote days"]
	effectiveID := m["effective date"]
	formatID := m["work format"]
	fields := fieldsParam([]string{"reporter"}, employeeID, daysID, effectiveID, formatID)

	for start := 0; start < 10000; start += c.pageSize() {
		q := url.Values{
			"jql":        {hcmJQL},
			"startAt":    {fmt.Sprint(start)},
			"maxResults": {fmt.Sprint(c.pageSize())},
			"fields":     {fields},
		}
		var res searchResp
		if _, err := c.cl.GetJSON(ctx, "/rest/api/2/search", q, &res); err != nil {
			return nil, fmt.Errorf("hcm: поиск заявок: %w", err)
		}
		for _, issue := range res.Issues {
			t := HybridTicket{Key: issue.Key}
			t.Employee = cleanEmployee(userString(issue.Fields[employeeID]))
			if t.Employee == "" {
				t.Employee = userString(issue.Fields["reporter"])
			}
			t.Days = optionValues(issue.Fields[daysID])
			t.WorkFormat = optionValues(issue.Fields[formatID])
			// Только явная дата вступления в силу: resolutiondate — момент
			// закрытия бумаг, он ставил бы изменение не в тот период.
			t.Effective = parseDate(str(issue.Fields[effectiveID]))
			if t.Effective.IsZero() || (t.Days == "" && t.WorkFormat == "") {
				continue
			}
			out = append(out, t)
		}
		if start+len(res.Issues) >= res.Total || len(res.Issues) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Effective.Before(out[j].Effective) })
	return out, nil
}

// optionValues собирает значения кастомного поля-списка Jira («Wednesday,
// Friday»): каждый элемент — объект опции с полем value.
func optionValues(v any) string {
	list, ok := v.([]any)
	if !ok {
		if m, ok := v.(map[string]any); ok {
			return str(m["value"])
		}
		return ""
	}
	var parts []string
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if s := str(m["value"]); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, ", ")
}
