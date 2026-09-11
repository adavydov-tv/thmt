package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// writeJSON отдаёт значение как JSON с указанным статусом.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Заголовки уже отправлены — остаётся только не молчать в логе.
		fmt.Fprintf(io.Discard, "%v", err)
	}
}

// errorResponse — единый формат ошибки API.
type errorResponse struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

// readJSON разбирает тело запроса с ограничением размера.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("некорректное тело запроса: %w", err)
	}
	return nil
}
