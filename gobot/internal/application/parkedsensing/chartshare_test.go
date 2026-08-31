package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// chartshare_test.go pins the crew partition the fleet-partitioning VRP produces:
// that it is a partition at all, that it is solved once and then read from the
// ledger, and that an unreachable solver leaves charting running on sectors.

// --- the fake solver ---------------------------------------------------------

// dealRoundRobin is the fake solver's answer: stops dealt to the crew in catalog
// order. NO ANGULAR SECTOR CAN PRODUCE IT, which is what lets a test tell the
// solver's assignment apart from the fallback's on the same fixture.
func dealRoundRobin(req *routing.VRPRequest) *routing.VRPResponse {
	crew := append([]string(nil), req.ShipSymbols...)
	sort.Strings(crew)
	assignments := map[string]*routing.ShipTourData{}
	for i, waypoint := range req.MarketWaypoints {
		ship := crew[i%len(crew)]
		tour, held := assignments[ship]
		if !held {
			tour = &routing.ShipTourData{}
			assignments[ship] = tour
		}
		tour.Waypoints = append(tour.Waypoints, waypoint)
	}
	return &routing.VRPResponse{Assignments: assignments}
}

type fakePartitioner struct {
	answer   func(*routing.VRPRequest) (*routing.VRPResponse, error)
	requests []*routing.VRPRequest
}

func (f *fakePartitioner) PartitionFleet(_ context.Context, req *routing.VRPRequest) (*routing.VRPResponse, error) {
	f.requests = append(f.requests, req)
	if f.answer != nil {
		return f.answer(req)
	}
	return dealRoundRobin(req), nil
}

func (f *fakePartitioner) calls() int { return len(f.requests) }

// ownerOf indexes a written partition by waypoint, failing on the two ways a
// partition stops being one.
func ownerOf(t *testing.T, shares []ChartShare) map[string]string {
	t.Helper()
	owner := map[string]string{}
	for _, share := range shares {
		for _, waypoint := range share.Waypoints {
			if held, taken := owner[waypoint]; taken {
				t.Fatalf("%s is owned by both %s and %s — two hulls charting one waypoint", waypoint, held, share.Ship)
			}
			owner[waypoint] = share.Ship
		}
	}
	return owner
}

// writtenShares is the last partition the tick persisted for a system.
func writtenShares(t *testing.T, led *fakeExpandLedger, system string) []ChartShare {
	t.Helper()
	for i := len(led.setShares) - 1; i >= 0; i-- {
		if led.setShares[i].system == system {
			return led.setShares[i].shares
		}
	}
	t.Fatalf("no charting partition was persisted for %q; writes = %v", system, led.setShares)
	return nil
}

// --- the VRP primary ---------------------------------------------------------

// THE PROPERTY THE WHOLE FEATURE RESTS ON, now asserted over the SOLVER's answer:
// the crew's shares are disjoint and together cover every outstanding stop. Two
// probes charting one waypoint is a hull-hour spent on nothing; a waypoint owned
// by nobody leaves the system's count stuck above zero and the system permanently
// re-seeded.
func TestChartShares_TheSolverPartitionIsDisjointAndCoversEveryStop(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 31)
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 1 {
		t.Fatalf("the solver was asked %d times, want exactly one solve for one crew", h.partitioner.calls())
	}

	shares := writtenShares(t, h.ledger, "X1-DARK")
	owner := ownerOf(t, shares)
	if len(owner) != len(stops) {
		t.Fatalf("the crew owns %d of %d stops, want every one — an unowned waypoint never gets charted", len(owner), len(stops))
	}
	for _, share := range shares {
		if len(share.Waypoints) == 0 {
			t.Fatalf("%s owns nothing of an evenly spread system, so its hull charts nothing", share.Ship)
		}
	}
}

// The request carries the crew and the outstanding stops, with each hull's own
// position, so the solver prices the walk each of them actually faces.
func TestChartShares_TheRequestCarriesTheCrewAndItsStops(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 12)
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() == 0 {
		t.Fatal("no partition was ever asked of the solver")
	}
	req := h.partitioner.requests[0]
	if len(req.ShipSymbols) != 3 {
		t.Fatalf("the solver was given %v, want the whole crew", req.ShipSymbols)
	}
	if len(req.MarketWaypoints) != len(stops) {
		t.Fatalf("the solver was given %d stops, want the system's %d outstanding", len(req.MarketWaypoints), len(stops))
	}
	if len(req.AllWaypoints) != len(stops) {
		t.Fatalf("the solver was given %d coordinates, want one per stop — without them every arc is unreachable", len(req.AllWaypoints))
	}
	for _, ship := range req.ShipSymbols {
		cfg, wired := req.ShipConfigs[ship]
		if !wired || cfg.CurrentLocation == "" {
			t.Fatalf("%s reached the solver with no position (%+v)", ship, cfg)
		}
	}
}

// A WHOLE DARK CATALOG PARTITIONS ON THE VRP PATH, at the widest crew the sizing
// can hand one system and a stop list covering every waypoint in it.
//
// TWO PROPERTIES, AND THE SECOND IS WHAT BOUNDS THE SOLVE. The first is that the
// answer is still a partition at this size — disjoint, covering, every hull
// working. The second is that the solver is asked about ONE SYSTEM: it is handed
// exactly that system's stops and no more, so the size of the request is bounded
// by a system's waypoint list rather than by the fleet's outstanding total,
// however large that total grows. The tick's timeout only ever has to cover the
// former.
func TestChartShares_AWholeCatalogPartitionsAcrossTheWidestCrew(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 120) // more waypoints than any one system holds
	crew := make([]string, 0, maxChartCrew)
	for i := 0; i < maxChartCrew; i++ {
		crew = append(crew, fmt.Sprintf("PROBE-%02d", i))
	}
	uncharted := darkSystemCrewOf(h, crew, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 1 {
		t.Fatalf("the solver was asked %d times, want exactly one solve for one crew", h.partitioner.calls())
	}

	req := h.partitioner.requests[0]
	if len(req.MarketWaypoints) != len(stops) || len(req.AllWaypoints) != len(stops) {
		t.Fatalf("the solver was given %d stops and %d coordinates, want this system's %d and nothing else",
			len(req.MarketWaypoints), len(req.AllWaypoints), len(stops))
	}

	shares := writtenShares(t, h.ledger, "X1-DARK")
	owner := ownerOf(t, shares)
	if len(owner) != len(stops) {
		t.Fatalf("the crew owns %d of %d stops, want every one — an unowned waypoint never gets charted and pins the count above zero",
			len(owner), len(stops))
	}
	if len(shares) != len(crew) {
		t.Fatalf("%d hulls hold a share, want the whole crew of %d", len(shares), len(crew))
	}
	for _, share := range shares {
		if len(share.Waypoints) == 0 {
			t.Fatalf("%s owns nothing of an evenly spread system, so its hull charts nothing", share.Ship)
		}
	}
}

// THE HULLS FLY THE SOLVER'S ASSIGNMENT, not a sector's. Round-robin over the
// catalog gives each hull a share no angular cut could hand it, so this fails if
// the walk is still driven by geometry.
func TestChartShares_TheCrewFliesTheSolverAssignment(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	rep, err := h.run(t, uncharted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Navigated != 3 {
		t.Fatalf("Navigated = %d, want 3 — every hull of the crew gets its step", rep.Navigated)
	}

	owner := ownerOf(t, writtenShares(t, h.ledger, "X1-DARK"))
	flown := 0
	for _, call := range h.seed.calls {
		if call.verb != "navigate" {
			continue
		}
		flown++
		if owner[call.arg] != call.ship {
			t.Fatalf("%s flew at %s, which the solver gave to %q", call.ship, call.arg, owner[call.arg])
		}
	}
	if flown != 3 {
		t.Fatalf("the crew flew %d legs, want one per hull", flown)
	}
}

// The catalog's VALUE TIER survives the solver's ordering: a hull works the
// shipyard- and market-bearing stops it owns before the dead rock, whatever order
// the tour came back in. That tier is what makes a system buyable, and a purely
// geometric walk leaves it to chance.
func TestChartShares_TheShipyardTierLeadsEachHullsWalk(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 6)
	// The catalog hands its stops tier-ordered; here the LAST two carry the value.
	stops[4].Priority, stops[5].Priority = 0, 0
	for i := 0; i < 4; i++ {
		stops[i].Priority = 3
	}
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, share := range writtenShares(t, h.ledger, "X1-DARK") {
		best := -1
		for i, waypoint := range share.Waypoints {
			priority := 3
			if waypoint == stops[4].Waypoint || waypoint == stops[5].Waypoint {
				priority = 0
			}
			if i > 0 && priority < best {
				t.Fatalf("%s works %v, which puts dead rock ahead of a value-tier stop", share.Ship, share.Waypoints)
			}
			best = priority
		}
	}
}

// --- the declared fallback ---------------------------------------------------

// A SOLVER WE CANNOT REACH MUST NOT STALL CHARTING. The sector partitioner takes
// over, the properties still hold, and the cause is named once.
func TestChartShares_AnUnreachableSolverFallsBackToSectors(t *testing.T) {
	h := newExpandHarness()
	h.partitioner.answer = func(*routing.VRPRequest) (*routing.VRPResponse, error) {
		return nil, errors.New("routing service unreachable")
	}
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	logger := &recordingPlacementLogger{}
	rep, err := h.runWithContext(t, logging.WithLogger(context.Background(), logger), uncharted, defaultExpandKnobs(h))
	if err != nil {
		t.Fatalf("an unreachable solver must not fail the tick, got: %v", err)
	}
	if rep.Navigated != 3 {
		t.Fatalf("Navigated = %d, want 3 — charting must not stall on the routing service", rep.Navigated)
	}

	shares := writtenShares(t, h.ledger, "X1-DARK")
	owner := ownerOf(t, shares)
	if len(owner) != len(stops) {
		t.Fatalf("the fallback owns %d of %d stops, want every one", len(owner), len(stops))
	}
	for _, share := range shares {
		sector := partitionOf(stops, sortedCrew(shares), share.Ship)
		if strings.Join(share.Waypoints, ",") != strings.Join(sector, ",") {
			t.Fatalf("%s owns %v, want its angular sector %v", share.Ship, share.Waypoints, sector)
		}
	}

	named := logger.withAction("parked_sensing_chart_partition_fallback")
	if len(named) != 1 {
		t.Fatalf("the fallback was silent (%d matching lines): a routing service down for good would look "+
			"exactly like a working solve", len(named))
	}
	if !strings.Contains(named[0].message, "routing service unreachable") {
		t.Fatalf("the underlying cause did not survive into the message: %q", named[0].message)
	}
}

// A solver that DROPS a stop has not produced a partition, and the unowned stop
// would never be charted. It is refused whole, and the sectors answer instead.
func TestChartShares_APartitionMissingAStopIsRefused(t *testing.T) {
	h := newExpandHarness()
	h.partitioner.answer = func(req *routing.VRPRequest) (*routing.VRPResponse, error) {
		resp := dealRoundRobin(req)
		for _, tour := range resp.Assignments {
			tour.Waypoints = tour.Waypoints[1:]
			break
		}
		return resp, nil
	}
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	owner := ownerOf(t, writtenShares(t, h.ledger, "X1-DARK"))
	if len(owner) != len(stops) {
		t.Fatalf("a partition short of %d stops was accepted; a dropped stop is never charted", len(stops)-len(owner))
	}
}

// A solver that hands one waypoint to two hulls is likewise not a partition.
func TestChartShares_APartitionWithAnOverlapIsRefused(t *testing.T) {
	h := newExpandHarness()
	h.partitioner.answer = func(req *routing.VRPRequest) (*routing.VRPResponse, error) {
		resp := dealRoundRobin(req)
		crew := append([]string(nil), req.ShipSymbols...)
		sort.Strings(crew)
		resp.Assignments[crew[0]].Waypoints = append(resp.Assignments[crew[0]].Waypoints,
			resp.Assignments[crew[1]].Waypoints[0])
		return resp, nil
	}
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ownerOf(t, writtenShares(t, h.ledger, "X1-DARK")) // fails the test on any overlap
}

// A hull the crew does not hold cannot be given work by the solver.
func TestChartShares_APartitionNamingAnOffCrewHullIsRefused(t *testing.T) {
	h := newExpandHarness()
	h.partitioner.answer = func(req *routing.VRPRequest) (*routing.VRPResponse, error) {
		resp := dealRoundRobin(req)
		resp.Assignments["PROBE-GHOST"] = &routing.ShipTourData{Waypoints: []string{"X1-DARK-W00"}}
		return resp, nil
	}
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, share := range writtenShares(t, h.ledger, "X1-DARK") {
		if share.Ship == "PROBE-GHOST" {
			t.Fatal("a hull off the crew was handed a share; the roster is what grants one")
		}
	}
}

// --- persistence -------------------------------------------------------------

// A RESTART RE-READS THE PARTITION RATHER THAN RE-SOLVING IT. Stability across
// ticks is the stored assignment, so a fresh process with the same crew asks the
// solver nothing and the hulls keep the shares they were flying.
func TestChartShares_AStoredPartitionSurvivesARestart(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	h.ledger.chartShares = dealtShares("X1-DARK", crew, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 0 {
		t.Fatalf("the solver was asked %d times for a partition already stored — a re-solve every tick "+
			"is both a stalled tick and a partition that can move under the hulls flying it", h.partitioner.calls())
	}
	if len(h.ledger.setShares) != 0 {
		t.Fatalf("a stored partition was rewritten: %v", h.ledger.setShares)
	}

	owner := map[string]string{}
	for _, share := range h.ledger.chartShares {
		for _, waypoint := range share.Waypoints {
			owner[waypoint] = share.Ship
		}
	}
	for _, call := range h.seed.calls {
		if call.verb == "navigate" && owner[call.arg] != call.ship {
			t.Fatalf("%s flew at %s, which the stored partition gave to %q", call.ship, call.arg, owner[call.arg])
		}
	}
}

// A CREW THAT LOSES A HULL RE-SOLVES FOR THE SURVIVORS. The dead hull's stops are
// unowned until they are re-dealt, so the trigger is membership and not a clock.
func TestChartShares_AHullLeavingReSolvesForTheSurvivors(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)
	// The ledger's crew is two hulls; the stored partition was solved for three.
	h.ledger.systems[0].ExtraSeeds = []SeedErrand{{Ship: "PROBE-B", State: SeedStateCharting}}
	h.ledger.chartShares = dealtShares("X1-DARK", []string{"PROBE-A", "PROBE-B", "PROBE-C"}, stops)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 1 {
		t.Fatalf("the solver was asked %d times, want one re-solve for the surviving crew", h.partitioner.calls())
	}

	shares := writtenShares(t, h.ledger, "X1-DARK")
	if len(shares) != 2 {
		t.Fatalf("the re-solve wrote %d shares, want one per surviving hull", len(shares))
	}
	owner := ownerOf(t, shares)
	if len(owner) != len(stops) {
		t.Fatalf("the survivors own %d of %d stops — the dead hull's share must be re-dealt, not orphaned",
			len(owner), len(stops))
	}
	for _, share := range shares {
		if share.Ship == "PROBE-C" {
			t.Fatal("a hull off the crew kept a share")
		}
	}
}

// A stop the catalog reveals AFTER the partition was solved is owned by nobody,
// which is the same hole a dead hull leaves: re-deal rather than leave it dark.
func TestChartShares_AStopNoHullOwnsForcesAReSolve(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	stored := dealtShares("X1-DARK", crew, stops[:len(stops)-1])
	h.ledger.chartShares = stored

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 1 {
		t.Fatalf("the solver was asked %d times; a stop no hull owns is never charted and the system is re-seeded forever",
			h.partitioner.calls())
	}
	if len(ownerOf(t, writtenShares(t, h.ledger, "X1-DARK"))) != len(stops) {
		t.Fatal("the re-solve did not cover every outstanding stop")
	}
}

// Stops charted meanwhile simply leave a stored share — the benign already-charted
// path — WITHOUT re-solving. A partition that moved every time a crewmate charted
// something would walk across the system and hand two hulls one waypoint in turn.
func TestChartShares_ChartedStopsLeaveAShareWithoutAReSolve(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	h.ledger.chartShares = dealtShares("X1-DARK", crew, stops)

	// Half the system is charted between ticks, the head of every share included.
	kept := make([]ChartStop, 0, len(stops))
	for i, stop := range stops {
		if i%2 == 1 {
			kept = append(kept, stop)
		}
	}
	uncharted := darkSystemCrew(h, kept)
	h.ledger.systems[0].UnchartedCount = len(kept)

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 0 || len(h.ledger.setShares) != 0 {
		t.Fatalf("charting away half a share re-solved the partition (solves=%d writes=%v)",
			h.partitioner.calls(), h.ledger.setShares)
	}
	live := map[string]bool{}
	for _, stop := range kept {
		live[stop.Waypoint] = true
	}
	for _, call := range h.seed.calls {
		if call.verb == "navigate" && !live[call.arg] {
			t.Fatalf("%s flew at %s, which is no longer uncharted", call.ship, call.arg)
		}
	}
}

// A hull ending its errand drops its share, so nothing keeps naming stops for a
// probe that is no longer charting.
func TestChartShares_AFinishedHullDropsItsShare(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	shares := dealtShares("X1-DARK", crew, stops)
	// PROBE-A's own share is charted through; the rest of the system is not.
	mine := map[string]bool{}
	for _, share := range shares {
		if share.Ship == "PROBE-A" {
			for _, waypoint := range share.Waypoints {
				mine[waypoint] = true
			}
		}
	}
	remaining := make([]ChartStop, 0, len(stops))
	for _, stop := range stops {
		if !mine[stop.Waypoint] {
			remaining = append(remaining, stop)
		}
	}
	uncharted := darkSystemCrew(h, remaining)
	h.ledger.systems[0].UnchartedCount = len(remaining)
	h.ledger.chartShares = shares
	h.screen.verdicts["X1-DARK"] = VerdictNoWhitelist

	if _, err := h.run(t, uncharted); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(h.ledger.clearedShares, "PROBE-A") {
		t.Fatalf("PROBE-A stood down still holding a share; cleared = %v", h.ledger.clearedShares)
	}
}

// --- the kill switch ---------------------------------------------------------

// A CAP OF ONE IS THE SINGLE-HULL TOUR AGAIN: no solve, no partition row, and the
// lone hull owns the whole catalog in the catalog's own order.
func TestChartShares_ACapOfOneNeitherSolvesNorPartitions(t *testing.T) {
	h := newExpandHarness()
	stops := ringStops("X1-DARK", 30)
	uncharted := darkSystemCrew(h, stops)
	h.ledger.systems[0].ExtraSeeds = nil

	knobs := defaultExpandKnobs(h)
	knobs.ChartHullCap = 1
	rep, err := h.runWithKnobs(t, uncharted, knobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.partitioner.calls() != 0 {
		t.Fatalf("a single-hull tour asked the solver %d times, want none", h.partitioner.calls())
	}
	if len(h.ledger.setShares) != 0 {
		t.Fatalf("a single-hull tour wrote a partition: %v", h.ledger.setShares)
	}
	if rep.Navigated != 1 {
		t.Fatalf("Navigated = %d, want the lone hull's single step", rep.Navigated)
	}
	for _, call := range h.seed.calls {
		if call.verb == "navigate" && call.arg != stops[0].Waypoint {
			t.Fatalf("the lone hull flew at %s, want the catalog head %s", call.arg, stops[0].Waypoint)
		}
	}
}

// --- fixtures ----------------------------------------------------------------

// dealtShares is a stored partition in the fake solver's own shape, so a test can
// stand a crew up mid-tour without running a solve first.
func dealtShares(system string, crew []string, stops []ChartStop) []ChartShare {
	sorted := append([]string(nil), crew...)
	sort.Strings(sorted)
	key := crewKey(sorted)
	byShip := map[string][]string{}
	for i, stop := range stops {
		ship := sorted[i%len(sorted)]
		byShip[ship] = append(byShip[ship], stop.Waypoint)
	}
	shares := make([]ChartShare, 0, len(sorted))
	for _, ship := range sorted {
		shares = append(shares, ChartShare{Ship: ship, System: system, Waypoints: byShip[ship], CrewKey: key})
	}
	return shares
}

func sortedCrew(shares []ChartShare) []string {
	crew := make([]string, 0, len(shares))
	for _, share := range shares {
		crew = append(crew, share.Ship)
	}
	sort.Strings(crew)
	return crew
}

func defaultExpandKnobs(h *expandHarness) ExpandKnobs {
	return ExpandKnobs{SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist}
}
