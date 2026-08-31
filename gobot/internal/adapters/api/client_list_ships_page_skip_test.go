package api

import (
	"context"
	"fmt"
	"testing"
)

// bigPoisonedFleet builds an n-hull fleet with a poisoned record at each of poisoned,
// the shape a fleet-wide serialisation fault takes: scattered bad records, one per page,
// each poisoning its whole 20-hull window.
func bigPoisonedFleet(n int, poisoned ...int) *poisonedFleetServer {
	fleet := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		fleet = append(fleet, fmt.Sprintf("SHIP-%03d", i))
	}
	bad := make(map[int]bool, len(poisoned))
	for _, i := range poisoned {
		bad[i] = true
	}
	return &poisonedFleetServer{fleet: fleet, poisoned: bad}
}

func pageNumbersOf(pages []UnreadablePage) []int {
	out := make([]int, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.Page)
	}
	return out
}

// THE cost regression sp-u5x6n exists to prevent. Narrowing a refused page costs a
// full span of per-hull probes, and nothing bounded the SUM of those spans: a fleet
// with one poisoned record per page narrowed every page, and 91 pages x 20 probes is
// limit=1 fleet-wide — the ~1,811-call enumeration that paging at 20 exists to avoid,
// against a 2.00 req/s account-wide ceiling. Past the budget a refused page must be
// skipped and REPORTED, for one call.
func TestListShipsSkipsRefusedPagesOnceTheIsolationBudgetIsSpent(t *testing.T) {
	// 3 pages of 20, one poisoned record on each, so every page 500s.
	pager := bigPoisonedFleet(60, 0, 20, 40)
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()
	client.SetFleetIsolationProbeBudget(apiPageLimitMax) // one page span

	got, report := symbolsOfShips(t, pager, client)

	// The page the budget covered was narrowed: its 19 healthy hulls still arrive.
	assertContains(t, got, "SHIP-002", "the narrowed page's readable hulls must still be returned")
	if len(got) != 19 {
		t.Fatalf("readable hulls: want 19 (one narrowed page), got %d", len(got))
	}

	// Everything past the budget is skipped WHOLE and named by page — the only
	// partiality a caller can be shown, since no hull symbol survives a 500.
	if pages := pageNumbersOf(report.UnreadablePages); len(pages) != 2 || pages[0] != 2 || pages[1] != 3 {
		t.Fatalf("skipped pages: want [2 3], got %v", pages)
	}
	for _, p := range report.UnreadablePages {
		if p.Hulls != apiPageLimitMax {
			t.Errorf("skipped page %d: want its full %d-hull span accounted for, got %d", p.Page, apiPageLimitMax, p.Hulls)
		}
		if p.Reason == "" {
			t.Errorf("skipped page %d: want a reason an operator can act on, got empty", p.Page)
		}
	}
	if !report.Partial() {
		t.Fatal("report.Partial(): want true — a read that skipped 2 pages must never look complete")
	}
	// 1 named hull + 2 spans of 20: a caller can tell 41-of-60-missing from the
	// one-poisoned-record case, which is the whole point of carrying a magnitude.
	if got := report.MissingHulls(); got != 41 {
		t.Fatalf("MissingHulls: want 41 (1 named + 2 skipped spans), got %d", got)
	}
	// The skip itself is what keeps the price down: 3 page reads + one span.
	if reqs := pager.requestLog(); len(reqs) != 3+apiPageLimitMax {
		t.Fatalf("requests: want %d (3 page reads + one probe span), got %d", 3+apiPageLimitMax, len(reqs))
	}
}

// The bound itself, stated in the only currency that matters: requests. Without a
// budget this fleet costs 100 probes; the whole point is that it cannot.
func TestListShipsIsolationSpendIsBoundedAcrossTheWholeEnumeration(t *testing.T) {
	pager := bigPoisonedFleet(100, 0, 20, 40, 60, 80)
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	if _, _, err := client.ListShipsWithReport(context.Background(), "token"); err != nil {
		t.Fatalf("a fleet with readable pages must not fail the read: %v", err)
	}

	if probes := pager.perHullProbes(); probes != defaultFleetIsolationProbeBudget {
		t.Fatalf("per-hull probes across the enumeration: want exactly the %d-probe budget, got %d",
			defaultFleetIsolationProbeBudget, probes)
	}
	// 5 page reads + the budget. Unbudgeted, this is 105 and scales with the fleet.
	if reqs := pager.requestLog(); len(reqs) != 5+defaultFleetIsolationProbeBudget {
		t.Fatalf("total requests: want %d (5 page reads + the probe budget), got %d",
			5+defaultFleetIsolationProbeBudget, len(reqs))
	}
}

// A page is skippable only once the read knows where the fleet ENDS. meta.total is
// that finish line, and a refused page carries none — so a first page that cannot be
// isolated must stay FATAL. Skipping it would leave the loop walking a dead API with
// no stopping condition, inventing a fleet as it went.
func TestListShipsRefusedFirstPageStaysFatalWithoutAKnownTotal(t *testing.T) {
	pager := bigPoisonedFleet(100, 0)
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()
	// Below one page span, so isolation cannot afford even the first page.
	client.SetFleetIsolationProbeBudget(1)

	ships, _, err := client.ListShipsWithReport(context.Background(), "token")
	if err == nil {
		t.Fatalf("a first page that cannot be isolated must fail CLOSED, got %d ships and no error", len(ships))
	}
	if ships != nil {
		t.Fatalf("a failed read must return no fleet at all, got %d hulls", len(ships))
	}
}

// The finish line has to survive a RUN of poisoned pages: meta.total arrives only on a
// page the server served, so an enumeration whose first pages were all isolated would
// have none. The per-hull probes carry it, and harvesting it there is what keeps a
// later skip from failing closed for want of a total it could have had.
func TestListShipsHarvestsTheFleetTotalFromIsolationProbes(t *testing.T) {
	// Every page is poisoned, so NO page is ever served whole: meta.total can only
	// have come from a per-hull probe.
	pager := bigPoisonedFleet(40, 0, 20)
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()
	client.SetFleetIsolationProbeBudget(apiPageLimitMax)

	got, report := symbolsOfShips(t, pager, client)

	if len(got) != 19 {
		t.Fatalf("readable hulls: want 19 from the narrowed page, got %d", len(got))
	}
	if pages := pageNumbersOf(report.UnreadablePages); len(pages) != 1 || pages[0] != 2 {
		t.Fatalf("skipped pages: want [2] — without a total harvested from the probes this read fails closed instead, got %v", pages)
	}
}

// RULINGS #5: an operational number, tunable at boot without a rebuild. 0/unset keeps
// the built-in default (covered by the budget-bound test above).
func TestFleetIsolationProbeBudgetIsTunable(t *testing.T) {
	pager := bigPoisonedFleet(100, 0, 20, 40, 60, 80)
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()
	client.SetFleetIsolationProbeBudget(apiPageLimitMax) // one page span only

	got, report := symbolsOfShips(t, pager, client)

	if len(got) != 19 {
		t.Fatalf("readable hulls with a one-span budget: want 19 (one narrowed page), got %d", len(got))
	}
	if probes := pager.perHullProbes(); probes != apiPageLimitMax {
		t.Fatalf("per-hull probes: want exactly the tuned %d, got %d", apiPageLimitMax, probes)
	}
	if pages := pageNumbersOf(report.UnreadablePages); len(pages) != 4 {
		t.Fatalf("skipped pages: want 4 (every page past the one the budget covered), got %v", pages)
	}
}

// A healthy fleet must be untouched by any of this: one request per page, no probes,
// no budget spent, and a report that says COMPLETE. This is the anti-vacuity control
// for the whole slice — a skip or a sweep that fired unconditionally would pass every
// test above while multiplying the cost of the fleet read and suppressing the
// stale-row prune forever.
func TestListShipsHealthyMultiPageFleetSpendsNoBudgetAndSkipsNothing(t *testing.T) {
	pager := bigPoisonedFleet(100) // no poisoned records
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	got, report := symbolsOfShips(t, pager, client)

	if len(got) != 100 {
		t.Fatalf("readable hulls: want all 100, got %d", len(got))
	}
	if report.Partial() {
		t.Fatalf("report.Partial(): want false for a clean fleet, got true (%+v)", report.UnreadablePages)
	}
	if got := report.MissingHulls(); got != 0 {
		t.Fatalf("MissingHulls: want 0 on a complete read, got %d", got)
	}
	if probes := pager.perHullProbes(); probes != 0 {
		t.Fatalf("per-hull probes on a healthy fleet: want 0, got %d", probes)
	}
	// 5 full pages + the empty-page probe that confirms the end: exactly today's cost.
	if reqs := pager.requestLog(); len(reqs) != 6 {
		t.Fatalf("requests for a healthy 100-hull fleet: want 6, got %d (%v)", len(reqs), reqs)
	}
}

// MissingHulls is what lets a caller weigh a partial read instead of only detecting
// one, so its arithmetic is pinned directly: skipped pages count their SPAN, because a
// refused page returns no payload to count.
func TestFleetReadReportMissingHullsCountsSkippedPageSpans(t *testing.T) {
	report := FleetReadReport{
		Unreadable:      []UnreadableShip{{Page: 1, Index: 3}, {Page: 1, Index: 7}},
		UnreadablePages: []UnreadablePage{{Page: 4, Hulls: 20}, {Page: 9, Hulls: 20}},
	}
	if got := report.MissingHulls(); got != 42 {
		t.Fatalf("MissingHulls: want 42 (2 named hulls + 2 twenty-hull spans), got %d", got)
	}
	if !report.Partial() {
		t.Fatal("Partial(): want true")
	}

	pagesOnly := FleetReadReport{UnreadablePages: []UnreadablePage{{Page: 2, Hulls: 20}}}
	if !pagesOnly.Partial() {
		t.Fatal("Partial(): want true for a read that skipped a page but named no hull — the whole page IS the gap")
	}
	if got := (FleetReadReport{}).MissingHulls(); got != 0 {
		t.Fatalf("MissingHulls on a complete read: want 0, got %d", got)
	}
}

func assertContains(t *testing.T, got []string, want, why string) {
	t.Helper()
	for _, s := range got {
		if s == want {
			return
		}
	}
	t.Errorf("%s: %s missing from the %d hulls returned", why, want, len(got))
}
