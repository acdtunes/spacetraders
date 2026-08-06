package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// unreadableShipJSON is a hull the API serves in a shape this client cannot turn
// into a ship: "nav" arrives as a string where an object is required. This is the
// TORWIND-5 shape — the hull itself is identifiable, but its ship object is
// corrupt — and it is what makes a whole-page unmarshal fail for all 253 hulls.
func unreadableShipJSON(symbol string) string {
	return fmt.Sprintf(`{
		"symbol": %q,
		"registration": {"role": "HAULER"},
		"nav": "The server did not return a valid response.",
		"fuel": {"current": 100, "capacity": 100},
		"cargo": {"capacity": 40, "units": 0, "inventory": []},
		"engine": {"speed": 30},
		"frame": {"symbol": "FRAME_LIGHT_FREIGHTER"}
	}`, symbol)
}

// symbollessShipJSON decodes perfectly into a zero-valued shipDTO. toShipData()
// is total, so nothing downstream would reject it — it becomes a ship row with an
// empty ship_symbol.
func symbollessShipJSON() string {
	return `{
		"registration": {"role": "HAULER"},
		"nav": {"systemSymbol": "X1-SYS", "waypointSymbol": "X1-SYS-WP", "status": "DOCKED", "flightMode": "CRUISE"},
		"fuel": {"current": 100, "capacity": 100},
		"cargo": {"capacity": 40, "units": 0, "inventory": []},
		"engine": {"speed": 30},
		"frame": {"symbol": "FRAME_PROBE"}
	}`
}

// scriptedFleetPager serves GET /my/ships from a literal script of per-page
// element lists. fleetPager (client_list_ships_pagination_test.go) generates only
// well-formed hulls; poisoning an INDIVIDUAL element is the thing it cannot
// express, and every test here turns on exactly that. Pages past the script come
// back empty, as the real API does.
type scriptedFleetPager struct {
	pages [][]string
	total int

	mu    sync.Mutex
	calls int
}

func (p *scriptedFleetPager) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))

		p.mu.Lock()
		p.calls++
		p.mu.Unlock()

		var items []string
		if page >= 1 && page <= len(p.pages) {
			items = p.pages[page-1]
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data": [%s], "meta": {"total": %d, "page": %d, "limit": 20}}`,
			strings.Join(items, ","), p.total, page)
	}
}

func (p *scriptedFleetPager) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// runListShipsWithReport drives the reporting read against pager and returns the
// hull symbols plus the report.
func runListShipsWithReport(t *testing.T, pager *scriptedFleetPager) ([]string, FleetReadReport) {
	t.Helper()
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, report, err := client.ListShipsWithReport(context.Background(), "token")
	if err != nil {
		t.Fatalf("a fleet read must survive an unreadable member, got error: %v", err)
	}
	symbols := make([]string, 0, len(ships))
	for _, s := range ships {
		symbols = append(symbols, s.Symbol)
	}
	return symbols, report
}

func goodHulls(symbols ...string) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, minimalShipJSON(s))
	}
	return out
}

func assertSymbols(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hulls returned: want %d %v, got %d %v", len(want), want, len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hull %d: want %q, got %q (full result %v)", i, want[i], got[i], got)
		}
	}
}

// THE regression for the 24h TORWIND outage: one hull that will not deserialise
// took the entire fleet read down with it, and SyncAllFromAPI aborted with zero
// ships synced. The readable hulls must come back.
func TestListShipsReturnsReadableHullsWhenOneMemberIsUnreadable(t *testing.T) {
	pager := &scriptedFleetPager{
		pages: [][]string{{
			minimalShipJSON("TORWIND-4"),
			unreadableShipJSON("TORWIND-5"),
			minimalShipJSON("TORWIND-6"),
		}},
		total: 3,
	}

	got, report := runListShipsWithReport(t, pager)

	assertSymbols(t, got, "TORWIND-4", "TORWIND-6")

	if !report.Partial() {
		t.Fatal("report.Partial(): want true — a dropped hull that is not reported is the silent fleet corruption this guard exists to prevent")
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want 1, got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
	// Naming the hull is the difference between diagnosing TORWIND-5 in minutes
	// and re-running the outage. encoding/json records the first type error and
	// keeps decoding, so the symbol survives a corrupt nav block.
	if got := report.Unreadable[0].Symbol; got != "TORWIND-5" {
		t.Errorf("unreadable hull symbol: want TORWIND-5, got %q", got)
	}
	if got := report.Unreadable[0].Page; got != 1 {
		t.Errorf("unreadable hull page: want 1, got %d", got)
	}
	if got := report.Unreadable[0].Index; got != 1 {
		t.Errorf("unreadable hull index: want 1, got %d", got)
	}
	if report.Unreadable[0].Reason == "" {
		t.Error("unreadable hull reason: want a non-empty decode error, got empty")
	}
}

// The port method every existing caller uses must inherit the same survival.
// Testing only the reporting form would leave the actual production entry point
// (ShipRepository -> ports.APIClient.ListShips) unproven.
func TestListShipsPortFormSurvivesAnUnreadableHull(t *testing.T) {
	pager := &scriptedFleetPager{
		pages: [][]string{{
			minimalShipJSON("TORWIND-4"),
			unreadableShipJSON("TORWIND-5"),
		}},
		total: 2,
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, err := client.ListShips(context.Background(), "token")
	if err != nil {
		t.Fatalf("ListShips must survive an unreadable member, got error: %v", err)
	}
	if len(ships) != 1 || ships[0].Symbol != "TORWIND-4" {
		t.Fatalf("want the single readable hull TORWIND-4, got %+v", ships)
	}
}

// A zero-valued ship is worse than an absent one: toShipData() is total, so an
// element that decodes to nothing becomes a persisted row under an empty
// ship_symbol. Unmodified code returns three hulls here, one of them blank.
func TestListShipsTreatsSymbollessElementAsUnreadable(t *testing.T) {
	pager := &scriptedFleetPager{
		pages: [][]string{{
			minimalShipJSON("TORWIND-4"),
			symbollessShipJSON(),
			minimalShipJSON("TORWIND-6"),
		}},
		total: 3,
	}

	got, report := runListShipsWithReport(t, pager)

	for _, symbol := range got {
		if symbol == "" {
			t.Fatalf("a zero-valued hull reached the caller: %v", got)
		}
	}
	assertSymbols(t, got, "TORWIND-4", "TORWIND-6")
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want 1, got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
}

// JSON null unmarshals into a struct as a silent no-op — no error, a zero
// shipDTO — so it is caught only by the symbol check, not the decode error. A
// bare string is the other end: a type error with no symbol to recover.
func TestListShipsReportsAlienElementsWithoutARecoverableSymbol(t *testing.T) {
	pager := &scriptedFleetPager{
		pages: [][]string{{
			minimalShipJSON("TORWIND-4"),
			`null`,
			`"The server did not return a valid response."`,
			minimalShipJSON("TORWIND-7"),
		}},
		total: 4,
	}

	got, report := runListShipsWithReport(t, pager)

	assertSymbols(t, got, "TORWIND-4", "TORWIND-7")
	if len(report.Unreadable) != 2 {
		t.Fatalf("unreadable entries: want 2, got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
	for i, u := range report.Unreadable {
		if u.Symbol != "" {
			t.Errorf("unreadable[%d]: want an empty symbol (nothing recoverable), got %q", i, u.Symbol)
		}
		if u.Reason == "" {
			t.Errorf("unreadable[%d]: want a non-empty reason", i)
		}
	}
}

// Skipped elements must not shorten a page. Both stop signals count hulls the
// SERVER holds, so scoring them against successfully-parsed hulls desynchronises
// the loop from meta.total and buys a needless extra request on every sync — on
// an API budget where List Ships is already the third-largest consumer.
func TestListShipsPaginationIsNotTruncatedBySkippedElements(t *testing.T) {
	firstPage := make([]string, 0, 20)
	for i := 1; i <= 20; i++ {
		if i == 8 {
			firstPage = append(firstPage, unreadableShipJSON("TORWIND-5"))
			continue
		}
		firstPage = append(firstPage, minimalShipJSON(fmt.Sprintf("SHIP-%03d", i)))
	}
	secondPage := goodHulls("SHIP-021", "SHIP-022", "SHIP-023", "SHIP-024", "SHIP-025")

	pager := &scriptedFleetPager{pages: [][]string{firstPage, secondPage}, total: 25}

	got, report := runListShipsWithReport(t, pager)

	if len(got) != 24 {
		t.Fatalf("hulls returned: want 24 (25 minus the unreadable one), got %d", len(got))
	}
	// The page-2 hulls are the ones a truncating loop would lose.
	if got[len(got)-1] != "SHIP-025" {
		t.Fatalf("last hull: want SHIP-025 from page 2, got %q", got[len(got)-1])
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want 1, got %d", len(report.Unreadable))
	}
	// 25 raw hulls read against meta.total=25 ends the loop on page 2. Counting
	// the 24 PARSED hulls instead leaves the total unsatisfied and spends a third
	// request discovering an empty page.
	if calls := pager.callCount(); calls != 2 {
		t.Fatalf("HTTP calls: want 2 (meta.total is satisfied by RAW hulls read), got %d", calls)
	}
}

// The severe form of the same grain error: filter the page first and an
// all-unreadable page reads as the empty page that terminates pagination,
// silently dropping every hull behind it. Here that would return 0 of 25.
func TestListShipsFullyUnreadablePageDoesNotEndPagination(t *testing.T) {
	poisoned := make([]string, 0, 20)
	for i := 1; i <= 20; i++ {
		poisoned = append(poisoned, unreadableShipJSON(fmt.Sprintf("BAD-%03d", i)))
	}
	secondPage := make([]string, 0, 20)
	for i := 1; i <= 20; i++ {
		secondPage = append(secondPage, minimalShipJSON(fmt.Sprintf("SHIP-%03d", i)))
	}
	thirdPage := goodHulls("SHIP-041", "SHIP-042", "SHIP-043", "SHIP-044", "SHIP-045")

	pager := &scriptedFleetPager{pages: [][]string{poisoned, secondPage, thirdPage}, total: 45}

	got, report := runListShipsWithReport(t, pager)

	if len(got) != 25 {
		t.Fatalf("hulls returned: want 25 (pages 2 and 3 survive an entirely poisoned page 1), got %d", len(got))
	}
	if got[0] != "SHIP-001" || got[len(got)-1] != "SHIP-045" {
		t.Fatalf("want SHIP-001..SHIP-045 from the pages behind the poisoned one, got first=%q last=%q", got[0], got[len(got)-1])
	}
	if len(report.Unreadable) != 20 {
		t.Fatalf("unreadable entries: want all 20 poisoned hulls reported, got %d", len(report.Unreadable))
	}
}

// Calibration, and the most important test in this file after the hazard itself:
// suppression of the stale-row prune is keyed on Partial(). A read that reports
// partial when it is not would disable pruning permanently and silently, and
// every other test here would still pass.
func TestListShipsCompleteReadReportsNothingUnreadable(t *testing.T) {
	pager := &scriptedFleetPager{
		pages: [][]string{goodHulls("TORWIND-4", "TORWIND-5", "TORWIND-6")},
		total: 3,
	}

	got, report := runListShipsWithReport(t, pager)

	assertSymbols(t, got, "TORWIND-4", "TORWIND-5", "TORWIND-6")
	if report.Partial() {
		t.Fatalf("report.Partial(): want false for a clean fleet, got true (%+v) — this would suppress stale-row pruning forever", report.Unreadable)
	}
}
