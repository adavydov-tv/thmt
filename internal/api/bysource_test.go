package api

import (
	"testing"
	"time"
)

func TestMonthBuckets(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Nicosia")
	from := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	got := monthBuckets(from, to, loc)
	if len(got) != 3 {
		t.Fatalf("ожидалось 3 месяца (июль, август, сентябрь), получено %d: %+v", len(got), got)
	}
	if got[0].Key != "2026-07" || got[1].Key != "2026-08" || got[2].Key != "2026-09" {
		t.Fatalf("неверные ключи: %s %s %s", got[0].Key, got[1].Key, got[2].Key)
	}
	// Первый и последний месяцы обрезаны границами периода — неполные.
	if !got[0].Partial || !got[0].From.Equal(from) {
		t.Errorf("июль должен начинаться с from и быть неполным: %+v", got[0])
	}
	if got[1].Partial {
		t.Errorf("август покрыт целиком, не должен быть partial: %+v", got[1])
	}
	if !got[2].Partial || !got[2].To.Equal(to) {
		t.Errorf("сентябрь должен заканчиваться to и быть неполным: %+v", got[2])
	}
	// Граница месяца — по локальному времени loc, не UTC.
	if got[1].From.In(loc).Day() != 1 || got[1].From.In(loc).Hour() != 0 {
		t.Errorf("граница августа должна быть полуночью в loc: %v", got[1].From.In(loc))
	}

	if n := len(monthBuckets(to, from, loc)); n != 0 {
		t.Errorf("пустой период должен давать 0 месяцев, получено %d", n)
	}
}
