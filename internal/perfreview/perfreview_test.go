package perfreview

import (
	"testing"
	"time"
)

func d(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func TestParse(t *testing.T) {
	raws := []rawIssue{
		// Q2: line manager поставил Exceeds, final — Meets: побеждает Final.
		{Key: "PR-1", Type: "Line manager review", Status: "Closed", Created: d("2026-07-08"),
			Summary:  "2026Q2 - OKR Quarterly Check | Denis Shamsutdinov | Line manager review",
			Employee: "Denis Shamsutdinov (HRDB-222448)", Rate: "Exceeds expectations", BaseURL: "https://jira"},
		{Key: "PR-2", Type: "Final review", Status: "Closed", Created: d("2026-07-10"),
			Summary:  "2026Q2 - OKR Quarterly Check | Denis Shamsutdinov | Final review",
			Employee: "Denis Shamsutdinov (HRDB-222448)", Rate: "Meets expectations", BaseURL: "https://jira"},
		// Годовое ревью — раньше по дате, значит первый цикл в хронологии.
		{Key: "PR-3", Type: "Final review", Status: "Closed", Created: d("2025-12-29"),
			Summary:  "Annual Performance Review 2025/2026 | Denis Shamsutdinov | Final review",
			Employee: "Denis Shamsutdinov (HRDB-222448)", Rate: "Partially meets expectations", BaseURL: "https://jira"},
		// Без HRDB-ключа — привязка по имени из summary.
		{Key: "PR-4", Type: "Final review", Status: "Closed", Created: d("2026-07-11"),
			Summary: "2026Q2 - OKR Quarterly Check | Pavel Nemtsev | Final review",
			Rate:    "Does not meet expectations", BaseURL: "https://jira"},
		// Тикеты без оценки и «чужие» типы игнорируются.
		{Key: "PR-5", Type: "Task", Summary: "2026Q2 - OKR Quarterly Check | Pavel Nemtsev | Task", Created: d("2026-07-01")},
		{Key: "PR-6", Type: "Peer review", Summary: "2026Q2 - OKR Quarterly Check | Pavel Nemtsev | Peer review", Rate: "Exceeds expectations", Created: d("2026-07-01")},
		// PIP активный и PDP закрытый.
		{Key: "PR-7", Type: "Personal Development Plan", Status: "In Progress", Created: d("2026-07-29"),
			Summary: "PIP: Performance Improvement Plan - Artyom Zhirov", Employee: "Artyom Zhirov (HRDB-435945)",
			PlanType: "PIP: Performance Improvement Plan", BaseURL: "https://jira"},
		{Key: "PR-8", Type: "Personal Development Plan", Status: "Closed", Created: d("2026-05-01"),
			Summary: "PDP: Personal Development Plan - Artyom Zhirov", Employee: "Artyom Zhirov (HRDB-435945)", BaseURL: "https://jira"},
	}
	snap := parse(raws)

	if len(snap.Cycles) != 2 || snap.Cycles[0].Key != "Annual Performance Review 2025/2026" || snap.Cycles[1].Key != "2026Q2 - OKR Quarterly Check" {
		t.Fatalf("циклы должны идти по хронологии (годовое, затем Q2): %+v", snap.Cycles)
	}

	ds := snap.ByHRDB["HRDB-222448"]
	if ds == nil {
		t.Fatal("сотрудник HRDB-222448 не найден")
	}
	q2 := ds.Ratings["2026Q2 - OKR Quarterly Check"]
	if q2.Grade != GradeMeets || q2.IssueKey != "PR-2" || q2.Source != "Final review" {
		t.Errorf("за Q2 должна победить оценка Final review (M, PR-2): %+v", q2)
	}
	if got := ds.Ratings["Annual Performance Review 2025/2026"].Grade; got != GradePartially {
		t.Errorf("годовое ревью: ожидалось P, получено %q", got)
	}
	if q2.URL != "https://jira/browse/PR-2" {
		t.Errorf("ссылка на тикет: %q", q2.URL)
	}

	pn := snap.ByName[NormalizeName("Pavel  Nemtsev")]
	if pn == nil || pn.Ratings["2026Q2 - OKR Quarterly Check"].Grade != GradeNotMeet {
		t.Errorf("оценка без HRDB-ключа должна найтись по имени: %+v", pn)
	}
	if pn != nil && len(pn.Ratings) != 1 {
		t.Errorf("Task и Peer review не должны давать оценок: %+v", pn.Ratings)
	}

	az := snap.ByHRDB["HRDB-435945"]
	if az == nil || len(az.Plans) != 2 {
		t.Fatalf("у Artyom Zhirov ожидались 2 плана: %+v", az)
	}
	if az.Plans[0].Type != "PIP" || !az.Plans[0].Active || az.Plans[0].IssueKey != "PR-7" {
		t.Errorf("первым (свежим) должен идти активный PIP PR-7: %+v", az.Plans[0])
	}
	if az.Plans[1].Type != "PDP" || az.Plans[1].Active {
		t.Errorf("закрытый PDP не должен быть активным: %+v", az.Plans[1])
	}
}

func TestHelpers(t *testing.T) {
	if n, k := employeeOf("Denis Shamsutdinov (HRDB-222448)"); n != "Denis Shamsutdinov" || k != "HRDB-222448" {
		t.Errorf("employeeOf: %q %q", n, k)
	}
	if n, k := employeeOf("Just Name"); n != "Just Name" || k != "" {
		t.Errorf("employeeOf без ключа: %q %q", n, k)
	}
	if c := cycleOf("2026Q1 - OKR Quarterly Check | X | Final review"); c != "2026Q1 - OKR Quarterly Check" {
		t.Errorf("cycleOf: %q", c)
	}
	if g := gradeCode("MEETS EXPECTATIONS"); g != GradeMeets {
		t.Errorf("gradeCode регистронезависим: %q", g)
	}
	if g := gradeCode("n/a"); g != "" {
		t.Errorf("неизвестный текст — не оценка: %q", g)
	}
	if p := planTypeOf("", "PIP: Performance Improvement Plan - Someone"); p != "PIP" {
		t.Errorf("planTypeOf по summary: %q", p)
	}
}
