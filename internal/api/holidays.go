package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/collectors/gcal"
	"github.com/adavydov/user-activity-dashboard/internal/storage"
)

// ---------- корпоративный календарь праздников ----------

// handleHolidays отдаёт праздники страны за год: корпоративные, а если год
// не покрыт корпоративным календарём — подсказку, что действуют данные Google.
func (s *Server) handleHolidays(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	country := strings.ToUpper(strings.TrimSpace(q.Get("country")))
	year := parseYear(q.Get("year"))
	if country == "" || year == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("нужны параметры country и year"))
		return
	}
	from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(1, 0, 0)
	days, err := s.store.ListCompanyHolidays(r.Context(), country, from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"country": country,
		"year":    year,
		// covered=true — год закреплён корпоративным календарём (даже пустым
		// его не считаем: нет записей — значит год не покрыт).
		"covered": len(days) > 0,
		"days":    orEmpty(days),
	})
}

type holidayRequest struct {
	Country string `json:"country"`
	Day     string `json:"day"` // YYYY-MM-DD
	Label   string `json:"label"`
}

func (s *Server) handleAddHoliday(w http.ResponseWriter, r *http.Request) {
	var req holidayRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	day, err := time.Parse("2006-01-02", strings.TrimSpace(req.Day))
	if err != nil || strings.TrimSpace(req.Country) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("нужны country и day (YYYY-MM-DD)"))
		return
	}
	h := storage.CompanyHoliday{
		Country: strings.ToUpper(strings.TrimSpace(req.Country)),
		Day:     day,
		Label:   strings.TrimSpace(req.Label),
	}
	if err := s.store.UpsertCompanyHoliday(r.Context(), h); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteHoliday(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	country := strings.ToUpper(strings.TrimSpace(q.Get("country")))
	day, err := time.Parse("2006-01-02", strings.TrimSpace(q.Get("day")))
	if country == "" || err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("нужны параметры country и day (YYYY-MM-DD)"))
		return
	}
	if err := s.store.DeleteCompanyHoliday(r.Context(), country, day); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleImportHolidays наполняет корпоративный календарь праздниками Google
// за год — как отправную точку для ручной правки.
func (s *Server) handleImportHolidays(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Country string `json:"country"`
		Year    int    `json:"year"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	if country == "" || req.Year < 2000 || req.Year > 2100 {
		writeErr(w, http.StatusBadRequest, errors.New("нужны country и year"))
		return
	}
	if !s.cfg.Google.Enabled {
		writeErr(w, http.StatusBadRequest, errors.New("Google не сконфигурирован — импорт недоступен, добавьте дни вручную"))
		return
	}
	c, err := gcal.New(r.Context(), s.cfg.Google, s.log)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	from := time.Date(req.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	days, err := c.HolidaysRange(r.Context(), country, from, from.AddDate(1, 0, 0))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	imported := 0
	for _, d := range days {
		// Google на границе диапазона может вернуть день соседнего года
		// (Новый год 1 января следующего) — он пометил бы тот год «покрытым»
		// одной записью, поэтому импорт строго обрезается по году.
		if d.Day.Year() != req.Year {
			continue
		}
		if err := s.store.UpsertCompanyHoliday(r.Context(), storage.CompanyHoliday{
			Country: country, Day: d.Day, Label: d.Label,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported})
}

func parseYear(s string) int {
	var y int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &y); err != nil || y < 2000 || y > 2100 {
		return 0
	}
	return y
}
