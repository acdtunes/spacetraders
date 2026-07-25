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

// minimalShipJSON is the smallest ship object toShipData accepts. Pagination
// tests care about how many hulls come back and how many HTTP calls it took, not
// about field mapping (client_ship_mapper_test.go owns that), so the fixture
// stays small enough to build 40+ hull fleets inline.
func minimalShipJSON(symbol string) string {
	return fmt.Sprintf(`{
		"symbol": %q,
		"registration": {"role": "HAULER"},
		"nav": {"systemSymbol": "X1-SYS", "waypointSymbol": "X1-SYS-WP", "status": "DOCKED", "flightMode": "CRUISE"},
		"fuel": {"current": 100, "capacity": 100},
		"cargo": {"capacity": 40, "units": 0, "inventory": []},
		"engine": {"speed": 30},
		"frame": {"symbol": "FRAME_LIGHT_FREIGHTER"}
	}`, symbol)
}

// fleetPager fakes GET /my/ships the way the real API serves it: pageSize-item
// slices of whatever fleetAt reports for that request, with an honest per-request
// meta block. Every request is recorded so a test can assert the exact HTTP call
// count, which is the whole point of the trailing-empty-page work.
type fleetPager struct {
	pageSize int
	// fleetAt returns the fleet the server sees for request n (1-based), which
	// lets a test grow or shrink the fleet between pages.
	fleetAt func(call int) []string
	// omitMeta drops the meta block entirely, standing in for a server that does
	// not report a total.
	omitMeta bool
	// forceTotal, when non-nil, replaces the honest len(fleet) in meta.total.
	forceTotal *int

	mu       sync.Mutex
	pages    []int
	limits   []string
	rawPages []string
}

func staticFleet(symbols []string) func(int) []string {
	return func(int) []string { return symbols }
}

func fleetOfSize(n int) []string {
	symbols := make([]string, n)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("SHIP-%03d", i+1)
	}
	return symbols
}

func (p *fleetPager) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawPage := r.URL.Query().Get("page")
		page, _ := strconv.Atoi(rawPage)

		p.mu.Lock()
		p.pages = append(p.pages, page)
		p.rawPages = append(p.rawPages, rawPage)
		p.limits = append(p.limits, r.URL.Query().Get("limit"))
		call := len(p.pages)
		p.mu.Unlock()

		fleet := p.fleetAt(call)
		start := (page - 1) * p.pageSize
		if start > len(fleet) {
			start = len(fleet)
		}
		end := start + p.pageSize
		if end > len(fleet) {
			end = len(fleet)
		}

		items := make([]string, 0, end-start)
		for _, symbol := range fleet[start:end] {
			items = append(items, minimalShipJSON(symbol))
		}

		w.Header().Set("Content-Type", "application/json")
		if p.omitMeta {
			_, _ = fmt.Fprintf(w, `{"data": [%s]}`, strings.Join(items, ","))
			return
		}
		total := len(fleet)
		if p.forceTotal != nil {
			total = *p.forceTotal
		}
		_, _ = fmt.Fprintf(w, `{"data": [%s], "meta": {"total": %d, "page": %d, "limit": %d}}`,
			strings.Join(items, ","), total, page, p.pageSize)
	}
}

func (p *fleetPager) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pages)
}

func (p *fleetPager) requestedLimits() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.limits...)
}

// runListShips drives ListShips against pager and returns the hull symbols it
// produced, in order.
func runListShips(t *testing.T, pager *fleetPager) []string {
	t.Helper()
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, err := client.ListShips(context.Background(), "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	symbols := make([]string, 0, len(ships))
	for _, s := range ships {
		symbols = append(symbols, s.Symbol)
	}
	return symbols
}

func assertFleetComplete(t *testing.T, got []string, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("fleet size: want %d hulls, got %d", want, len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, s := range got {
		if seen[s] {
			t.Fatalf("duplicate hull %q in result", s)
		}
		seen[s] = true
	}
	for i := 1; i <= want; i++ {
		symbol := fmt.Sprintf("SHIP-%03d", i)
		if !seen[symbol] {
			t.Fatalf("hull %q missing from result", symbol)
		}
	}
}

// A fleet whose size is not an exact multiple of the page size ends on a short
// page, and meta.total confirms it: the sync must stop there instead of spending
// a request to discover an empty page. This is the production shape (253 hulls).
func TestListShipsStopsOnceMetaTotalIsSatisfied(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(25))}

	assertFleetComplete(t, runListShips(t, pager), 25)

	if got := pager.callCount(); got != 2 {
		t.Fatalf("HTTP calls: want 2 (pages 1-2, no trailing empty probe), got %d", got)
	}
}

// The 253-hull production fleet fits in 13 pages, so 13 requests is the floor:
// List Ships is the third-largest consumer of a permanently saturated budget, and
// a 14th request that can only ever return an empty page buys nothing.
func TestListShipsProductionSizedFleetCostsThirteenCalls(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(253))}

	assertFleetComplete(t, runListShips(t, pager), 253)

	if got := pager.callCount(); got != 13 {
		t.Fatalf("HTTP calls: want 13 for a 253-hull fleet, got %d", got)
	}
}

// A fleet that ends exactly on a page boundary gives no short page to confirm
// meta.total against, so the loop deliberately keeps the trailing probe. The
// alternative — trusting a full page's total — silently drops a hull bought
// between that page and the next request, and reconcileFleetToLive then treats
// the missing hull as sold and deletes its row.
func TestListShipsExactPageMultipleKeepsTrailingProbe(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(40))}

	assertFleetComplete(t, runListShips(t, pager), 40)

	if got := pager.callCount(); got != 3 {
		t.Fatalf("HTTP calls: want 3 (pages 1-2 plus the boundary probe), got %d", got)
	}
}

func TestListShipsEmptyFleetCostsOneCall(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(nil)}

	if got := runListShips(t, pager); len(got) != 0 {
		t.Fatalf("fleet size: want 0 hulls, got %d", len(got))
	}
	if got := pager.callCount(); got != 1 {
		t.Fatalf("HTTP calls: want 1 for an empty fleet, got %d", got)
	}
}

// A server that reports no meta at all must fall back to the empty-page probe.
// Truncating at the first page here would hand every caller a 20-hull fleet and
// let reconcileFleetToLive delete the rest.
func TestListShipsWithoutMetaFallsBackToEmptyPageProbe(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(25)), omitMeta: true}

	assertFleetComplete(t, runListShips(t, pager), 25)

	if got := pager.callCount(); got != 3 {
		t.Fatalf("HTTP calls: want 3 (empty-page fallback), got %d", got)
	}
}

// Same fallback for a meta block that is present but reports total=0 — a total
// that cannot be reconciled with the hulls already read is not a stop signal.
func TestListShipsWithZeroMetaTotalFallsBackToEmptyPageProbe(t *testing.T) {
	zero := 0
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(25)), forceTotal: &zero}

	assertFleetComplete(t, runListShips(t, pager), 25)

	if got := pager.callCount(); got != 3 {
		t.Fatalf("HTTP calls: want 3 (empty-page fallback), got %d", got)
	}
}

// A total that understates what has already been read is a lying server, not a
// finished collection: keep paging rather than truncate.
func TestListShipsUndercountingMetaTotalDoesNotTruncateFleet(t *testing.T) {
	one := 1
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(25)), forceTotal: &one}

	assertFleetComplete(t, runListShips(t, pager), 25)
}

// meta.total is re-read on every page, so a hull bought mid-pagination extends
// the loop instead of being cut off by the total page 1 happened to report.
func TestListShipsFollowsFleetGrowthMidPagination(t *testing.T) {
	grown := fleetOfSize(26)
	pager := &fleetPager{
		pageSize: 20,
		fleetAt: func(call int) []string {
			if call == 1 {
				return grown[:25]
			}
			return grown
		},
	}

	assertFleetComplete(t, runListShips(t, pager), 26)
}

// A hull bought after a full page was served is invisible to that page's total,
// which is why a full page is never a stopping point however satisfied the total
// looks. Here the 41st hull appears only once the boundary page has been read.
func TestListShipsCatchesHullBoughtAfterAFullBoundaryPage(t *testing.T) {
	grown := fleetOfSize(41)
	pager := &fleetPager{
		pageSize: 20,
		fleetAt: func(call int) []string {
			if call <= 2 {
				return grown[:40]
			}
			return grown
		},
	}

	assertFleetComplete(t, runListShips(t, pager), 41)
}

// A hull sold mid-pagination shrinks the collection under the loop; the sync must
// still terminate on the fresh total rather than fall through to an extra probe.
func TestListShipsSurvivesFleetShrinkingMidPagination(t *testing.T) {
	full := fleetOfSize(25)
	pager := &fleetPager{
		pageSize: 20,
		fleetAt: func(call int) []string {
			if call == 1 {
				return full
			}
			return full[:21]
		},
	}

	if got := len(runListShips(t, pager)); got != 21 {
		t.Fatalf("fleet size: want 21 hulls after the shrink, got %d", got)
	}
	if got := pager.callCount(); got != 2 {
		t.Fatalf("HTTP calls: want 2 (page 2's total already accounts for the shrunk fleet), got %d", got)
	}
}

// A server handing back fewer hulls per page than the client asked for must not
// read as the end of the collection while meta.total says otherwise.
func TestListShipsUnderfilledPagesDoNotTruncateFleet(t *testing.T) {
	pager := &fleetPager{pageSize: 10, fleetAt: staticFleet(fleetOfSize(25))}

	assertFleetComplete(t, runListShips(t, pager), 25)
}

// 20 is the API maximum for /my/ships (openapi.json: limit maximum 20, default
// 10), so page size is not a lever — asking for less only adds requests.
func TestListShipsRequestsTheApiMaximumPageSize(t *testing.T) {
	pager := &fleetPager{pageSize: 20, fleetAt: staticFleet(fleetOfSize(25))}

	runListShips(t, pager)

	for i, limit := range pager.requestedLimits() {
		if limit != "20" {
			t.Fatalf("request %d asked for limit=%q, want the API maximum 20", i+1, limit)
		}
	}
}
