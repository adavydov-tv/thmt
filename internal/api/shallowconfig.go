package api

// Настройка «что считать низкой активностью» по Area of Responsibility.
// Хранится в app_settings под ключом shallow_config как JSON:
//
//	{"default": ["gwork.login", ...], "areas": {"QA": ["slack.message", ...]}}
//
// День человека помечается как «низкая активность», если ВСЯ его активность
// за день — из набора для его Area (или из default, если для Area набор не
// задан). Матрица красит такие дни отдельной шкалой.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
)

const shallowConfigKey = "shallow_config"

type shallowConfig struct {
	Default []string            `json:"default"`
	Areas   map[string][]string `json:"areas"`
}

// loadShallowConfig читает конфиг из настроек; при отсутствии/ошибке —
// дефолтный набор из storage.DefaultShallowTypes, пустой список областей.
func (s *Server) loadShallowConfig(ctx context.Context) shallowConfig {
	cfg := shallowConfig{Default: storage.DefaultShallowTypes, Areas: map[string][]string{}}
	raw, err := s.store.GetSetting(ctx, shallowConfigKey)
	if err != nil || raw == "" {
		return cfg
	}
	var stored shallowConfig
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return cfg
	}
	if stored.Default != nil {
		cfg.Default = stored.Default
	}
	if stored.Areas != nil {
		cfg.Areas = stored.Areas
	}
	return cfg
}

// typesFor возвращает набор «низкой активности» для конкретной Area:
// переопределение области, иначе default.
func (c shallowConfig) typesFor(area string) []string {
	if area != "" {
		if t, ok := c.Areas[area]; ok {
			return t
		}
	}
	return c.Default
}

// remoteDates — даты периода (YYYY-MM-DD в loc), когда формат работы человека
// Remote. Нужны ShallowDays: только в такие дни непосещённая встреча не
// считается работой (в офис/гибрид оффлайн-встреча возможна).
func remoteDates(formats []models.HybridChange, from, to time.Time, loc *time.Location) []string {
	var out []string
	start := from.In(loc)
	for d := time.Date(start.Year(), start.Month(), start.Day(), 12, 0, 0, 0, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		if models.NormalizeWorkFormat(models.HybridDaysAt(formats, "", d)) == "remote" {
			out = append(out, d.Format("2006-01-02"))
		}
	}
	return out
}

func (s *Server) handleGetShallowConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadShallowConfig(r.Context())

	// Опции для UI: все известные типы событий и список Area из карточек людей.
	types := []string{}
	for t := range models.TypeLabels() {
		types = append(types, t)
	}
	sort.Strings(types)

	areas := map[string]bool{}
	if people, err := s.store.ListPeople(r.Context()); err == nil {
		for _, p := range people {
			if p.Area != "" {
				areas[p.Area] = true
			}
		}
	}
	areaList := make([]string, 0, len(areas))
	for a := range areas {
		areaList = append(areaList, a)
	}
	sort.Strings(areaList)

	writeJSON(w, http.StatusOK, map[string]any{
		"default":     cfg.Default,
		"areas":       cfg.Areas,
		"all_types":   types,
		"known_areas": areaList,
	})
}

func (s *Server) handleSetShallowConfig(w http.ResponseWriter, r *http.Request) {
	var req shallowConfig
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Default == nil {
		req.Default = storage.DefaultShallowTypes
	}
	if req.Areas == nil {
		req.Areas = map[string][]string{}
	}
	// Пустые списки области убираем — они эквивалентны «использовать default».
	for area, types := range req.Areas {
		if len(types) == 0 {
			delete(req.Areas, area)
		}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.SetSetting(r.Context(), shallowConfigKey, string(payload)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
