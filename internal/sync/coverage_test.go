package sync

import (
	"testing"
	"time"
)

func TestIncrementalFrom(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	from := base
	to := base.AddDate(0, 0, 30) // окно [1 сен, 1 окт)
	day := 24 * time.Hour
	reprobe := 2 * day

	cases := []struct {
		name    string
		covered time.Time
		want    time.Time
	}{
		{"нет покрытия", time.Time{}, from},
		{"покрытие до начала окна", base.AddDate(0, 0, -5), from},
		{"покрытие в начале окна (кандидат <= from)", base.AddDate(0, 0, 1), from},
		{"покрытие в середине — сужаем до covered-reprobe", base.AddDate(0, 0, 20), base.AddDate(0, 0, 18)},
		{"покрытие у самого конца", base.AddDate(0, 0, 29), base.AddDate(0, 0, 27)},
		{"покрытие за окном — полное окно", base.AddDate(0, 0, 40), from},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := incrementalFrom(from, to, c.covered, reprobe)
			if !got.Equal(c.want) {
				t.Errorf("incrementalFrom = %s, want %s", got.Format(time.DateOnly), c.want.Format(time.DateOnly))
			}
		})
	}
}

func TestIncrementalFromZeroReprobe(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := base.AddDate(0, 0, 30)
	covered := base.AddDate(0, 0, 20)
	got := incrementalFrom(base, to, covered, 0)
	if !got.Equal(covered) {
		t.Errorf("при reprobe=0 начало = covered (%s), получили %s", covered.Format(time.DateOnly), got.Format(time.DateOnly))
	}
}
