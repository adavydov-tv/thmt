// Package zabbix собирает активность в мониторинге Zabbix через JSON-RPC
// (api_jsonrpc.php): квитирование проблем и комментарии (event.get +
// acknowledges) и изменения конфигурации (auditlog.get). Это работа
// дежурных/SRE, которой нет в git.
//
// Поддерживает несколько сред (prod/staging) — каждая со своим URL/токеном.
// Групповой сбор: на всю группу людей делается один набор запросов к каждой
// среде, результат раскладывается по авторам (атрибуция по userid Zabbix).
//
// Аутентификация — API-токен (Zabbix 5.4+), заголовок Authorization: Bearer.
// Прод за SSO-прокси: используйте внутренний фронтенд-хост, доступный по VPN.
package zabbix

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// one — клиент одного инстанса Zabbix.
type one struct {
	inst   config.ZabbixInstance
	log    *slog.Logger
	client *http.Client
	rpcID  int
}

// Collector перебирает все среды Zabbix.
type Collector struct {
	insts []*one
	log   *slog.Logger
}

// New создаёт коллектор по списку инстансов.
func New(insts []config.ZabbixInstance, timeout time.Duration, log *slog.Logger) (*Collector, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(insts) == 0 {
		return nil, fmt.Errorf("zabbix: не задан ни один инстанс")
	}
	c := &Collector{log: log.With("collector", "zabbix")}
	for _, inst := range insts {
		if inst.BaseURL == "" || inst.Token == "" {
			return nil, fmt.Errorf("zabbix[%s]: нужны URL и TOKEN", inst.Env)
		}
		tr := &http.Transport{}
		if inst.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		}
		c.insts = append(c.insts, &one{inst: inst,
			log:    log.With("collector", "zabbix", "env", inst.Env),
			client: &http.Client{Timeout: timeout, Transport: tr}})
	}
	return c, nil
}

// Source возвращает идентификатор источника.
func (c *Collector) Source() models.Source { return models.SourceZabbix }

func (o *one) rpc(ctx context.Context, method string, params any, out any) error {
	o.rpcID++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": o.rpcID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.inst.BaseURL+"/api_jsonrpc.php", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	req.Header.Set("Authorization", "Bearer "+o.inst.Token)
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("zabbix %s: %w", method, err)
	}
	defer resp.Body.Close()
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("zabbix %s: разбор ответа (возможно HTML от прокси, нужен VPN): %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("zabbix %s: %s (%s)", method, env.Error.Message, env.Error.Data)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// userMap строит userid → person key по всем людям группы (один user.get).
func (o *one) userMap(ctx context.Context, people []models.Person) (map[string]string, error) {
	var users []struct {
		UserID   string `json:"userid"`
		Username string `json:"username"`
		Alias    string `json:"alias"`
		Name     string `json:"name"`
		Surname  string `json:"surname"`
	}
	if err := o.rpc(ctx, "user.get", map[string]any{"output": []string{"userid", "username", "alias", "name", "surname"}}, &users); err != nil {
		return nil, err
	}
	// логин/имя человека → его key
	byID := map[string]string{}
	want := map[string]string{}
	for _, p := range people {
		add := func(v string) {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				want[v] = p.Key
				if i := strings.Index(v, "@"); i > 0 {
					want[v[:i]] = p.Key
				}
			}
		}
		add(p.Email)
		add(p.GoogleEmail)
		add(p.GitLabUser)
		// DisplayName намеренно НЕ добавляется: матч по ФИО приписывал бы
		// события полного тёзки (единственный коллектор, где так делалось).
	}
	for _, u := range users {
		login := u.Username
		if login == "" {
			login = u.Alias
		}
		if pk, ok := want[strings.ToLower(login)]; ok {
			byID[u.UserID] = pk
		}
	}
	return byID, nil
}

// collectGroup собирает по одному инстансу события всех людей группы.
func (o *one) collectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, error) {
	out := map[string][]models.Event{}
	byID, err := o.userMap(ctx, people)
	if err != nil {
		return nil, err
	}
	if len(byID) == 0 {
		return out, nil // никто из группы не сопоставлен в этом Zabbix
	}
	tf := strconv.FormatInt(from.Unix(), 10)
	tt := strconv.FormatInt(to.Unix(), 10)

	// 1) Квитирование проблем — event.get + acknowledges.
	var evs []struct {
		EventID string `json:"eventid"`
		Name    string `json:"name"`
		Acks    []struct {
			UserID  string `json:"userid"`
			Clock   string `json:"clock"`
			Message string `json:"message"`
		} `json:"acknowledges"`
	}
	err = o.rpc(ctx, "event.get", map[string]any{
		"output": []string{"eventid", "name"}, "selectAcknowledges": "extend",
		"time_from": tf, "time_till": tt, "acknowledged": true,
		"source": 0, "value": 1, "limit": 20000,
	}, &evs)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		for _, a := range e.Acks {
			pk, ok := byID[a.UserID]
			if !ok {
				continue
			}
			ts, _ := strconv.ParseInt(a.Clock, 10, 64)
			at := time.Unix(ts, 0)
			if at.Before(from) || !at.Before(to) {
				continue
			}
			title := "Квитирование: " + e.Name
			if strings.TrimSpace(a.Message) != "" {
				title = "Комментарий к проблеме: " + e.Name
			}
			ev := models.Event{
				PersonKey: pk, Source: models.SourceZabbix, Type: models.TypeZabbixAck,
				ExternalID: "zbx-ack:" + o.inst.Env + ":" + e.EventID + ":" + a.Clock,
				OccurredAt: at, Title: title + " (" + o.inst.Env + ")",
				URL:   o.inst.BaseURL + "/tr_events.php?triggerid=0&eventid=" + e.EventID,
				RefID: e.EventID,
				Meta:  map[string]any{"env": o.inst.Env, "event_id": e.EventID, "problem": e.Name, "message": a.Message},
			}
			ev.Normalize()
			out[pk] = append(out[pk], ev)
		}
	}

	// 2) Изменения конфигурации — auditlog.get по userids группы.
	userids := make([]string, 0, len(byID))
	for id := range byID {
		userids = append(userids, id)
	}
	var audits []struct {
		AuditID      string `json:"auditid"`
		UserID       string `json:"userid"`
		Clock        string `json:"clock"`
		Action       string `json:"action"`
		ResourceType string `json:"resourcetype"`
		ResourceName string `json:"resourcename"`
	}
	if err := o.rpc(ctx, "auditlog.get", map[string]any{
		"output": "extend", "userids": userids, "time_from": tf, "time_till": tt, "limit": 20000,
	}, &audits); err != nil {
		o.log.Warn("zabbix: auditlog недоступен", "err", err)
	}
	actionLabel := map[string]string{"0": "Добавление", "1": "Изменение", "2": "Удаление"}
	for _, a := range audits {
		pk, ok := byID[a.UserID]
		if !ok {
			continue
		}
		ts, _ := strconv.ParseInt(a.Clock, 10, 64)
		at := time.Unix(ts, 0)
		if at.Before(from) || !at.Before(to) {
			continue
		}
		act := actionLabel[a.Action]
		if act == "" {
			// Коды вне {0,1,2} — Login/Logout/Failed login/History clear и т.п.:
			// это не изменения конфигурации, а частью и вовсе не действия человека.
			continue
		}
		ev := models.Event{
			PersonKey: pk, Source: models.SourceZabbix, Type: models.TypeZabbixChange,
			ExternalID: "zbx-audit:" + o.inst.Env + ":" + a.AuditID,
			OccurredAt: at,
			Title:      fmt.Sprintf("%s: %s %s (%s)", act, a.ResourceType, a.ResourceName, o.inst.Env),
			Meta:       map[string]any{"env": o.inst.Env, "action": a.Action, "resource_type": a.ResourceType, "resource": a.ResourceName},
		}
		ev.Normalize()
		out[pk] = append(out[pk], ev)
	}
	return out, nil
}

// CollectGroup — сбор по всей группе людей за один набор запросов на среду.
func (c *Collector) CollectGroup(ctx context.Context, people []models.Person, from, to time.Time) (map[string][]models.Event, string, error) {
	out := map[string][]models.Event{}
	total := 0
	for _, o := range c.insts {
		byPerson, err := o.collectGroup(ctx, people, from.UTC(), to.UTC())
		if err != nil {
			c.log.Warn("инстанс недоступен", "env", o.inst.Env, "err", err)
			continue
		}
		for pk, evs := range byPerson {
			out[pk] = append(out[pk], evs...)
			total += len(evs)
		}
	}
	return out, fmt.Sprintf("Zabbix: сред %d, событий %d", len(c.insts), total), nil
}

// Collect — одиночный человек (через групповой путь на группе из одного).
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
