package jira

import (
	"testing"
	"time"
)

// Назначение задачи — действие человека только когда он назначил себя сам.
func TestLastSelfAssignment(t *testing.T) {
	me := actor{accountID: "acc-me", email: "me@example.com"}
	at := func(s string) jiraTime {
		ts, _ := time.Parse(time.RFC3339, s)
		return jiraTime{Time: ts}
	}
	assignToMe := []historyItem{{Field: "assignee", To: "acc-me", ToString: "Me"}}

	t.Run("самоназначение засчитывается", func(t *testing.T) {
		hs := []jiraHistory{{
			Author:  &jiraUser{AccountID: "acc-me"},
			Created: at("2026-09-01T10:00:00Z"),
			Items:   assignToMe,
		}}
		got, ok := lastSelfAssignment(hs, me)
		if !ok || !got.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("ожидалось самоназначение 2026-09-01, получено ok=%v %v", ok, got)
		}
	})

	t.Run("назначение чужой рукой не засчитывается", func(t *testing.T) {
		hs := []jiraHistory{{
			Author:  &jiraUser{AccountID: "acc-teamlead"},
			Created: at("2026-09-01T10:00:00Z"),
			Items:   assignToMe,
		}}
		if _, ok := lastSelfAssignment(hs, me); ok {
			t.Fatal("назначение тимлидом засчитано как действие человека")
		}
	})

	t.Run("назначение НЕ на нас не засчитывается", func(t *testing.T) {
		hs := []jiraHistory{{
			Author:  &jiraUser{AccountID: "acc-me"},
			Created: at("2026-09-01T10:00:00Z"),
			Items:   []historyItem{{Field: "assignee", To: "acc-other", ToString: "Other"}},
		}}
		if _, ok := lastSelfAssignment(hs, me); ok {
			t.Fatal("назначение человеком ЧУЖОЙ задачи на другого засчитано как самоназначение")
		}
	})

	t.Run("берётся самое позднее самоназначение", func(t *testing.T) {
		hs := []jiraHistory{
			{Author: &jiraUser{AccountID: "acc-me"}, Created: at("2026-08-01T10:00:00Z"), Items: assignToMe},
			{Author: &jiraUser{AccountID: "acc-teamlead"}, Created: at("2026-09-05T10:00:00Z"), Items: assignToMe},
			{Author: &jiraUser{AccountID: "acc-me"}, Created: at("2026-09-03T10:00:00Z"), Items: assignToMe},
		}
		got, ok := lastSelfAssignment(hs, me)
		if !ok || got.Day() != 3 {
			t.Fatalf("ожидалось самоназначение от 03.09, получено ok=%v %v", ok, got)
		}
	})
}
