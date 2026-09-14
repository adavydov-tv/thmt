package api

import (
	"testing"
	"time"
)

func TestTenureYears(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	hire := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := tenureYears(&hire, now); got == nil || *got != 5.3 {
		t.Fatalf("ожидалось 5.3 года, получено %v", got)
	}
	future := now.AddDate(0, 1, 0)
	if tenureYears(&future, now) != nil || tenureYears(nil, now) != nil {
		t.Fatal("дата в будущем или отсутствие даты — стажа нет")
	}
}
