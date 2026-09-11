package models

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestMergeHybridTimeline(t *testing.T) {
	// Заявка HCM вступает в силу 22.06, HRDB применили 23.06 — точки
	// схлопываются в раннюю (дату заявки), старое значение из журнала.
	assets := []HybridChange{{At: day("2026-06-23"), Old: "Friday", New: "Wednesday"}}
	tickets := []HybridChange{{At: day("2026-06-22"), New: "Wednesday"}}
	got := MergeHybridTimeline(assets, tickets)
	if len(got) != 1 {
		t.Fatalf("ожидалась 1 точка, получено %d: %+v", len(got), got)
	}
	if !got[0].At.Equal(day("2026-06-22")) || got[0].Old != "Friday" || got[0].New != "Wednesday" {
		t.Fatalf("неожиданная точка: %+v", got[0])
	}

	// До первой точки действует старое значение, после — новое.
	if v := HybridDaysAt(got, "Wednesday", day("2026-06-01")); v != "Friday" {
		t.Fatalf("до изменения ожидался Friday, получен %q", v)
	}
	if v := HybridDaysAt(got, "Wednesday", day("2026-07-01")); v != "Wednesday" {
		t.Fatalf("после изменения ожидался Wednesday, получен %q", v)
	}

	// Три разных значения подряд не схлопываются; одинаковые наборы в разном
	// порядке («Wednesday, Friday» и «Friday, Wednesday») — схлопываются.
	got = MergeHybridTimeline(
		[]HybridChange{
			{At: day("2026-01-10"), Old: "", New: "Friday"},
			{At: day("2026-03-10"), Old: "Friday", New: "Wednesday, Friday"},
		},
		[]HybridChange{{At: day("2026-03-09"), New: "Friday, Wednesday"}},
	)
	if len(got) != 2 {
		t.Fatalf("ожидались 2 точки, получено %d: %+v", len(got), got)
	}
	if !got[1].At.Equal(day("2026-03-09")) || got[1].Old != "Friday" {
		t.Fatalf("неожиданная вторая точка: %+v", got[1])
	}

	// Пустые источники — пустая история.
	if got := MergeHybridTimeline(nil, nil); len(got) != 0 {
		t.Fatalf("ожидалась пустая история, получено %+v", got)
	}
}
