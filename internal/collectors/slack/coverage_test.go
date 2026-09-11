package slack

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func ivs(pairs ...string) [][2]time.Time {
	var out [][2]time.Time
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, [2]time.Time{d(pairs[i]), d(pairs[i+1])})
	}
	return out
}

func eq(a, b [][2]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i][0].Equal(b[i][0]) || !a[i][1].Equal(b[i][1]) {
			return false
		}
	}
	return true
}

func TestUncoveredGaps(t *testing.T) {
	// Пустое покрытие — весь период одна дыра.
	got := uncoveredGaps(d("2026-01-01"), d("2026-02-01"), nil, 0)
	if !eq(got, ivs("2026-01-01", "2026-02-01")) {
		t.Fatalf("пустое покрытие: %v", got)
	}

	// Полное покрытие без хвоста — дыр нет.
	got = uncoveredGaps(d("2026-01-01"), d("2026-02-01"), ivs("2025-12-01", "2026-03-01"), 0)
	if len(got) != 0 {
		t.Fatalf("полное покрытие: %v", got)
	}

	// Полное покрытие с хвостом — переобходится только хвост.
	got = uncoveredGaps(d("2026-01-01"), d("2026-02-01"), ivs("2025-12-01", "2026-03-01"), 7*24*time.Hour)
	if !eq(got, ivs("2026-01-25", "2026-02-01")) {
		t.Fatalf("хвост: %v", got)
	}

	// Дыра в середине + хвост, слившийся с последней дырой.
	got = uncoveredGaps(d("2026-01-01"), d("2026-03-01"),
		ivs("2026-01-01", "2026-01-10", "2026-02-01", "2026-02-25"), 7*24*time.Hour)
	if !eq(got, ivs("2026-01-10", "2026-02-01", "2026-02-22", "2026-03-01")) {
		t.Fatalf("дыры+хвост: %v", got)
	}

	// Покрытие целиком раньше периода — не влияет.
	got = uncoveredGaps(d("2026-06-01"), d("2026-07-01"), ivs("2026-01-01", "2026-02-01"), 0)
	if !eq(got, ivs("2026-06-01", "2026-07-01")) {
		t.Fatalf("покрытие вне периода: %v", got)
	}
}

func TestMergeGaps(t *testing.T) {
	got := mergeGaps(ivs("2026-01-05", "2026-01-10", "2026-01-01", "2026-01-06", "2026-01-10", "2026-01-12", "2026-02-01", "2026-02-02"))
	if !eq(got, ivs("2026-01-01", "2026-01-12", "2026-02-01", "2026-02-02")) {
		t.Fatalf("merge: %v", got)
	}
}
