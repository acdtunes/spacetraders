package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// poisonedFleetServer models the failure agent TORWIND actually hit: a hull whose
// record the SERVER cannot serialise, so every window of GET /my/ships that
// CONTAINS it answers 500 while every window that excludes it answers 200. That
// window rule is the whole point — it is what makes a page-level read fatal and a
// per-hull read recoverable, and no existing fixture can express it
// (scriptedFleetPager always answers 200 and poisons the JSON instead).
//
// Verified against the live API in sp-2br34:
//
//	GET /my/ships?limit=20&page=1  500   (the page contains TORWIND-5)
//	GET /my/ships?limit=1&page=1     200   TORWIND-1
//	GET /my/ships?limit=1&page=5     500   (the 5th ship IS TORWIND-5)
type poisonedFleetServer struct {
	// fleet is the server-side hull order; index i is served by (page=i+1, limit=1).
	fleet []string
	// poisoned holds the 0-based indices whose record the server cannot serialise.
	poisoned map[int]bool
	// poisonEverything stands in for a real outage: every request 500s regardless
	// of the window, which is what a dead API looks like from here.
	poisonEverything bool

	mu       sync.Mutex
	requests []string
}

func (s *poisonedFleetServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if page < 1 {
			page = 1
		}
		if limit < 1 {
			limit = apiPageLimitMax
		}

		s.mu.Lock()
		s.requests = append(s.requests, fmt.Sprintf("page=%d&limit=%d", page, limit))
		s.mu.Unlock()

		start := (page - 1) * limit
		end := start + limit
		if end > len(s.fleet) {
			end = len(s.fleet)
		}

		if s.poisonEverything || s.windowIsPoisoned(start, end) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": {"message": "The server did not return a valid response.", "code": 3000}}`)
			return
		}

		items := make([]string, 0, limit)
		for i := start; i < end; i++ {
			items = append(items, minimalShipJSON(s.fleet[i]))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data": [%s], "meta": {"total": %d, "page": %d, "limit": %d}}`,
			strings.Join(items, ","), len(s.fleet), page, limit)
	}
}

func (s *poisonedFleetServer) windowIsPoisoned(start, end int) bool {
	for i := start; i < end; i++ {
		if s.poisoned[i] {
			return true
		}
	}
	return false
}

func (s *poisonedFleetServer) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// perHullProbes counts the requests that read exactly one hull — the isolation
// sweep's footprint, and the number a request-budget argument is made of.
func (s *poisonedFleetServer) perHullProbes() int {
	n := 0
	for _, req := range s.requestLog() {
		if strings.HasSuffix(req, "&limit=1") {
			n++
		}
	}
	return n
}

func fleetOf(symbols ...string) []string { return symbols }

// newPoisonedFleetTestServer serves pager and returns the base URL, for the tests
// that need a client whose retry ladder is NOT newTestClient's zero.
func newPoisonedFleetTestServer(pager *poisonedFleetServer) (string, func()) {
	server := httptest.NewServer(pager.handler())
	return server.URL, server.Close
}

func symbolsOfShips(t *testing.T, pager *poisonedFleetServer, client *SpaceTradersClient) ([]string, FleetReadReport) {
	t.Helper()
	ships, report, err := client.ListShipsWithReport(context.Background(), "token")
	if err != nil {
		t.Fatalf("one unreadable hull must not fail the whole fleet read, got error: %v (requests: %v)", err, pager.requestLog())
	}
	symbols := make([]string, 0, len(ships))
	for _, s := range ships {
		symbols = append(symbols, s.Symbol)
	}
	return symbols, report
}

// THE regression for the 2-day production freeze (sp-2br34). TORWIND-5's cargo is
// corrupt on SpaceTraders' own server, so the paged list-ships call 500s; the read
// failed the WHOLE refresh closed, bootstrap skipped every tick for two days, and
// the fleet earned nothing. The four hulls that read perfectly well must come back.
func TestListShipsIsolatesAPoisonedPageAndReturnsTheReadableHulls(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisoned: map[int]bool{4: true}, // TORWIND-5, the 5th hull
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	got, report := symbolsOfShips(t, pager, client)

	assertSymbols(t, got, "TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4")

	// The hull must be PRESENT-BUT-UNKNOWN, never absent: Partial() is what
	// suppresses the stale-row prune downstream, so a read that dropped a hull
	// without reporting it would delete the row of the hull we still own.
	if !report.Partial() {
		t.Fatal("report.Partial(): want true — an unreported drop lets the prune delete the hull we still own")
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want exactly 1 (the poisoned hull), got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
	if got := report.Unreadable[0].Index; got != 4 {
		t.Errorf("unreadable hull index: want 4 (the 5th hull), got %d", got)
	}
	if report.Unreadable[0].Reason == "" {
		t.Error("unreadable hull reason: want the server error that isolated it, got empty")
	}
}

// The port form is the production entry point (ShipRepository -> ports.APIClient),
// so proving only the reporting form would leave the path that actually runs untested.
func TestListShipsPortFormSurvivesAPoisonedPage(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisoned: map[int]bool{4: true},
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, err := client.ListShips(context.Background(), "token")
	if err != nil {
		t.Fatalf("ListShips must survive a poisoned page, got error: %v", err)
	}
	if len(ships) != 4 {
		t.Fatalf("hulls returned: want 4 readable hulls, got %d (%+v)", len(ships), ships)
	}
}

// ANTI-VACUITY CONTROL, and the one that keeps the request budget honest: a healthy
// fleet must cost EXACTLY what it costs today — one page request, no per-hull
// probes — and must not report itself partial. A sweep that ran unconditionally
// would pass every other test in this file while multiplying the request cost of
// the third-largest API consumer by 20 and suppressing the stale-row prune forever.
func TestListShipsHealthyFleetPaysNothingForIsolation(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet: fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	got, report := symbolsOfShips(t, pager, client)

	assertSymbols(t, got, "TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5")
	if report.Partial() {
		t.Fatalf("report.Partial(): want false for a clean fleet, got true (%+v) — this suppresses stale-row pruning forever", report.Unreadable)
	}
	if probes := pager.perHullProbes(); probes != 0 {
		t.Fatalf("per-hull probes on a healthy fleet: want 0, got %d (requests: %v)", probes, pager.requestLog())
	}
	if reqs := pager.requestLog(); len(reqs) != 1 {
		t.Fatalf("requests for a healthy 5-hull fleet: want 1, got %d (%v)", len(reqs), reqs)
	}
}

// A poisoned page must not truncate the fleet behind it. The sweep covers the
// failed page's span and pagination then continues, so the hulls on later pages —
// the ones a fail-fast read loses entirely — still arrive.
func TestListShipsIsolationKeepsReadingThePagesBehindAPoisonedOne(t *testing.T) {
	fleet := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		fleet = append(fleet, fmt.Sprintf("SHIP-%03d", i))
	}
	pager := &poisonedFleetServer{fleet: fleet, poisoned: map[int]bool{3: true}} // SHIP-004, on page 1
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	got, report := symbolsOfShips(t, pager, client)

	if len(got) != 24 {
		t.Fatalf("hulls returned: want 24 (25 minus the poisoned one), got %d", len(got))
	}
	if got[len(got)-1] != "SHIP-025" {
		t.Fatalf("last hull: want SHIP-025 from page 2 — a truncating read loses it — got %q", got[len(got)-1])
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want 1, got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
	// 20 probes for the failed page's span + the page-2 read + the failed page itself.
	if probes := pager.perHullProbes(); probes != 20 {
		t.Fatalf("per-hull probes: want 20 (exactly the failed page's span), got %d", probes)
	}
}

// A poisoned hull on page 2 pins the coordinate convention, which page 1 cannot:
// there its within-page index and its fleet-wide index are the same number, so
// either convention passes and an operator reading "index 3" on page 2 could be
// sent to the wrong hull. It also carries the reproduce request an operator runs
// to see the fault for themselves — the thing a 500 gives up no symbol for.
func TestListShipsIsolationReportsAPoisonedHullByItsPositionWithinThePage(t *testing.T) {
	fleet := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		fleet = append(fleet, fmt.Sprintf("SHIP-%03d", i))
	}
	pager := &poisonedFleetServer{fleet: fleet, poisoned: map[int]bool{22: true}} // SHIP-023: page 2, offset 2
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	got, report := symbolsOfShips(t, pager, client)

	if len(got) != 24 {
		t.Fatalf("hulls returned: want 24, got %d", len(got))
	}
	if len(report.Unreadable) != 1 {
		t.Fatalf("unreadable entries: want 1, got %d (%+v)", len(report.Unreadable), report.Unreadable)
	}
	u := report.Unreadable[0]
	if u.Page != 2 {
		t.Errorf("unreadable hull page: want 2, got %d", u.Page)
	}
	if u.Index != 2 {
		t.Errorf("unreadable hull index: want 2 (position WITHIN page 2), got %d — a fleet-wide index here reads as 22 and points at the wrong hull", u.Index)
	}
	if !strings.Contains(u.Reason, "page=23&limit=1") {
		t.Errorf("unreadable hull reason: want the reproduce request page=23&limit=1, got %q", u.Reason)
	}
}

// A REAL outage must still fail closed, and must not amplify the request storm
// while doing it. Every window 500s, so no hull is attributable to a poisoned
// record: the sweep aborts on its failure streak instead of spending the whole
// span, and the error propagates so bootstrap skips the tick rather than acting on
// a fleet it could not read.
func TestListShipsFailsClosedAndBoundsRequestsWhenEverythingIs500(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:            fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisonEverything: true,
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, _, err := client.ListShipsWithReport(context.Background(), "token")
	if err == nil {
		t.Fatalf("a total API outage must fail the read CLOSED, got %d ships and no error", len(ships))
	}
	if ships != nil {
		t.Fatalf("a failed read must return no fleet at all, got %+v", ships)
	}
	// 1 page read + defaultFleetIsolationAbortStreak probes, then the sweep gives
	// up. Unbounded probing would spend the whole span against a dead API at 2 req/s.
	wantRequests := 1 + defaultFleetIsolationAbortStreak
	if reqs := pager.requestLog(); len(reqs) != wantRequests {
		t.Fatalf("requests during a total outage: want %d (one page + %d probes before the sweep aborts), got %d (%v)",
			wantRequests, defaultFleetIsolationAbortStreak, len(reqs), reqs)
	}
}

// The other shape of "the refresh genuinely IS untrustworthy": the sweep completes
// — it reaches the end of the fleet without ever hitting its failure streak — but
// not one hull was readable. Returning (no ships, no error) there would look like
// SUCCESS to SyncAllFromAPI, which would sync nothing, prune nothing, and let every
// coordinator act on a fleet snapshot that is entirely stale.
func TestListShipsFailsClosedWhenNotOneHullIsReadable(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2"),
		poisoned: map[int]bool{0: true, 1: true},
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, _, err := client.ListShipsWithReport(context.Background(), "token")
	if err == nil {
		t.Fatalf("a read that recovered no hull at all must fail closed, got %d ships and no error", len(ships))
	}
}

// A genuinely empty fleet is NOT an outage. Nothing was unreadable, so the read is
// complete and authoritative — and it must stay that way, or a freshly-registered
// agent can never sync, and the dead-era ghost prune loses its only trigger.
func TestListShipsEmptyFleetIsACompleteRead(t *testing.T) {
	pager := &poisonedFleetServer{fleet: nil}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	ships, report, err := client.ListShipsWithReport(context.Background(), "token")
	if err != nil {
		t.Fatalf("an empty fleet is a complete read, not a failure: %v", err)
	}
	if len(ships) != 0 {
		t.Fatalf("hulls returned: want 0, got %d", len(ships))
	}
	if report.Partial() {
		t.Fatal("report.Partial(): want false for an empty fleet")
	}
}

// The isolation probe must NOT climb the full retry ladder. The page-level read has
// already spent it (10 retries, ~3.5 minutes of exponential backoff in production)
// before isolation ever begins, so a probe that repeats it doubles the freeze it is
// meant to end — every tick, forever, because the poisoned record never heals.
func TestPoisonedHullProbeUsesAShortRetryLadder(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2"),
		poisoned: map[int]bool{1: true},
	}
	server, closeFn := newPoisonedFleetTestServer(pager)
	defer closeFn()
	// maxRetries=5 so the two ladders are distinguishable: the page read may spend
	// all 6 attempts, the probe may not.
	client := NewSpaceTradersClientWithConfig(server, 5, time.Millisecond, nil)

	if _, _, err := client.ListShipsWithReport(context.Background(), "token"); err != nil {
		t.Fatalf("one poisoned hull must not fail the read: %v", err)
	}

	poisonedProbes := 0
	for _, req := range pager.requestLog() {
		if req == "page=2&limit=1" {
			poisonedProbes++
		}
	}
	wantMax := defaultFleetIsolationProbeRetries + 1
	if poisonedProbes > wantMax {
		t.Fatalf("attempts against the poisoned hull's own probe: want at most %d, got %d — the probe is climbing the page read's ladder", wantMax, poisonedProbes)
	}
	if poisonedProbes == 0 {
		t.Fatal("the poisoned hull was never probed individually — the fixture never reached the isolation path")
	}
}

// RULINGS #5: the failure tolerance is an operational number, tunable at boot
// without a rebuild. 0/unset must keep the built-in default.
func TestFleetIsolationAbortStreakIsTunable(t *testing.T) {
	pager := &poisonedFleetServer{
		fleet:            fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisonEverything: true,
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()
	client.SetFleetIsolationAbortStreak(1)

	if _, _, err := client.ListShipsWithReport(context.Background(), "token"); err == nil {
		t.Fatal("a total outage must fail closed")
	}
	if reqs := pager.requestLog(); len(reqs) != 2 {
		t.Fatalf("requests with the streak tuned to 1: want 2 (one page + one probe), got %d (%v)", len(reqs), reqs)
	}
}

// The per-call retry cap sits on the path EVERY API call takes, so its scope has to
// be pinned exactly. A 429 must keep the client-wide ladder even on a capped call:
// rate-limit backpressure is the limiter telling us to wait and says nothing about
// the record being poisoned, so shortening it there would convert ordinary
// contention into hulls reported unreadable — during exactly the saturation a
// 20-probe sweep creates.
func TestRetryCapAppliesToServerErrorsButNotRateLimiting(t *testing.T) {
	cap2, cap99 := 2, 99
	capped := apiRequest{serverErrorRetryCap: &cap2}
	uncapped := apiRequest{}

	serverError := classifyResponse(http.StatusInternalServerError, nil)
	rateLimited := classifyResponse(http.StatusTooManyRequests, nil)

	cases := []struct {
		name      string
		call      apiRequest
		decision  retryDecision
		clientMax int
		want      int
	}{
		{"capped call, server error: the cap binds", capped, serverError, 10, 2},
		{"capped call, 429: the client-wide ladder survives", capped, rateLimited, 10, 10},
		{"capped call, cap above the client ladder: never RAISES it", apiRequest{serverErrorRetryCap: &cap99}, serverError, 10, 10},
		{"uncapped call, server error: untouched", uncapped, serverError, 10, 10},
		{"uncapped call, 429: untouched", uncapped, rateLimited, 10, 10},
	}
	for _, tc := range cases {
		if got := tc.call.retryCapFor(tc.decision, tc.clientMax); got != tc.want {
			t.Errorf("%s: retryCapFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}
