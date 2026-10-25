package service

import (
	"math/rand"
	"testing"
	"time"
)

var planNow = time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)

func kept(plan []VersionDecision) []int {
	var out []int
	for _, d := range plan {
		if d.Keep {
			out = append(out, d.Version)
		}
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The example of section 2: Ana 10:00-11:40, Bruno 11:45-12:10, Ana 15:00-15:20. After the first
// day three versions remain; after 30 days, only the last of the day.
func TestPlanHistoryExample(t *testing.T) {
	p := DefaultHistoryPolicy()
	build := func(day time.Time) []HistoryVersion {
		var vs []HistoryVersion
		add := func(author string, from time.Time, n int, step time.Duration) {
			for i := 0; i < n; i++ {
				vs = append(vs, HistoryVersion{Version: len(vs) + 1, Author: author, Via: "human", Kind: KindUpdate,
					CreatedAt: from.Add(time.Duration(i) * step)})
			}
		}
		at := func(h, m int) time.Time {
			return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, p.Location)
		}
		add("ana", at(10, 0), 101, time.Minute) // 10:00 .. 11:40
		add("bruno", at(11, 45), 26, time.Minute)
		add("ana", at(15, 0), 21, time.Minute) // .. 15:20
		return vs
	}
	vs := build(planNow.AddDate(0, 0, -3))
	current := len(vs) + 1 // a later version, so none of these is the current one
	if got := kept(PlanHistory(vs, current, nil, planNow, p)); !sameInts(got, []int{101, 127, 148}) {
		t.Fatalf("after a day: %v", got)
	}
	vs = build(planNow.AddDate(0, 0, -40))
	if got := kept(PlanHistory(vs, current, nil, planNow, p)); !sameInts(got, []int{148}) {
		t.Fatalf("after 30 days: %v", got)
	}
}

// Agent versions are not protected by their origin; service ones are.
func TestPlanHistoryProtection(t *testing.T) {
	p := DefaultHistoryPolicy()
	old := planNow.AddDate(0, 0, -60)
	vs := []HistoryVersion{
		{Version: 1, Author: "a", Via: "human", Kind: KindCreate, Protected: true, CreatedAt: old},
		{Version: 2, Author: "a", Via: "agent", Kind: KindUpdate, CreatedAt: old.Add(time.Minute)},
		{Version: 3, Author: "a", Via: "agent", Kind: KindUpdate, HasMessage: true, Protected: true, CreatedAt: old.Add(2 * time.Minute)},
		{Version: 4, Author: "a", Via: "agent", Kind: KindUpdate, CreatedAt: old.Add(3 * time.Minute)},
		{Version: 5, Author: "s", Via: "service", Kind: KindUpdate, Protected: true, CreatedAt: old.Add(4 * time.Minute)},
		{Version: 6, Author: "a", Via: "human", Kind: KindUpdate, CreatedAt: old.Add(5 * time.Minute)},
		{Version: 7, Author: "a", Via: "human", Kind: KindUpdate, CreatedAt: old.Add(6 * time.Minute)},
		{Version: 8, Author: "a", Via: "human", Kind: KindUpdate, CreatedAt: old.Add(7 * time.Minute)},
	}
	plan := PlanHistory(vs, 9, map[int]bool{6: true}, planNow, p)
	// 1 create, 3 message, 5 service, 6 pinned, 8 last of the day; 2 and 4 are agent versions
	// (2 inside its block, 4 last of a block but not of the day), 7 is a human one.
	if got := kept(plan); !sameInts(got, []int{1, 3, 5, 6, 8}) {
		t.Fatalf("kept %v (%+v)", got, plan)
	}
	reasons := map[int]string{}
	for _, d := range plan {
		reasons[d.Version] = d.Reason
	}
	want := map[int]string{1: ReasonCreate, 2: ReasonInBlock, 3: ReasonMessage, 4: ReasonInDay, 5: ReasonService,
		6: ReasonPinned, 7: ReasonInBlock, 8: ReasonDay}
	for v, r := range want {
		if reasons[v] != r {
			t.Errorf("version %d: reason %s, want %s", v, reasons[v], r)
		}
	}
}

// Running the plan on what it kept removes nothing more (idempotence), for random histories.
func TestPlanHistoryIdempotent(t *testing.T) {
	p := DefaultHistoryPolicy()
	r := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test data
	authors := []string{"ana", "bruno", "carla"}
	vias := []string{"human", "agent", "service"}
	for round := 0; round < 300; round++ {
		at := planNow.AddDate(0, 0, -70)
		var vs []HistoryVersion
		n := 20 + r.Intn(200)
		for i := 0; i < n; i++ {
			at = at.Add(time.Duration(r.Intn(180)) * time.Minute * time.Duration(1+r.Intn(3)))
			if at.After(planNow) {
				break
			}
			v := HistoryVersion{Version: i + 1, Author: authors[r.Intn(2)], Via: vias[r.Intn(2)], Kind: KindUpdate, CreatedAt: at}
			if r.Intn(30) == 0 {
				v.Via = vias[2]
			}
			if r.Intn(25) == 0 {
				v.HasMessage = true
			}
			if i == 0 {
				v.Kind = KindCreate
			}
			v.Protected = v.Kind == KindCreate || v.HasMessage || v.Via == "service"
			if r.Intn(40) == 0 {
				v.Author = authors[2]
			}
			vs = append(vs, v)
		}
		current := vs[len(vs)-1].Version
		first := PlanHistory(vs, current, nil, planNow, p)
		var rest []HistoryVersion
		for i, d := range first {
			if d.Keep {
				rest = append(rest, vs[i])
			}
		}
		second := PlanHistory(rest, current, nil, planNow, p)
		if got := kept(second); len(got) != len(rest) {
			t.Fatalf("round %d: the second pass removed %d of %d versions", round, len(rest)-len(got), len(rest))
		}
		// At most one unprotected version per calendar day older than 30 days.
		days := map[string]int{}
		for _, v := range rest {
			if planNow.Sub(v.CreatedAt) > p.KeepBlocks && !v.Protected && v.Version != current {
				days[v.CreatedAt.In(p.Location).Format("2006-01-02")]++
			}
		}
		for d, c := range days {
			if c > 1 {
				t.Fatalf("round %d: %d unprotected versions kept on %s", round, c, d)
			}
		}
	}
}
