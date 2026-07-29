package commands

import (
	"context"
	"errors"
	"testing"
)

// The heavy-yard PRICING ERRAND suite.
//
// TEST BUDGET (behaviour-first): 6 distinct behaviours × 2 = 12 unit tests maximum.
//
//	B1 a known-but-unpriced heavy yard with no hull near it dispatches ONE errand
//	B2 only ONE errand is in flight at a time
//	B3 a parked sensing probe is never selected
//	B4 no eligible carrier ⇒ nothing moves (never steal one)
//	B5 candidate ordering is deterministic and stable
//	B6 the errand stands down when no heavy is wanted / everything is priced
//
// Every test enters through reconcileOnce (the coordinator's driving port) or through the pure
// policy, and asserts at the dispatch port — never on internal call counts.

// --- fakes at the driven-port boundary ---

// fakeHeavyYardCatalog is the known-heavy-yard read, INCLUDING availability-only rows.
type fakeHeavyYardCatalog struct {
	yards []KnownHeavyYard
	err   error
}

func (f *fakeHeavyYardCatalog) KnownHeavyYards(_ context.Context, _ int) ([]KnownHeavyYard, error) {
	return f.yards, f.err
}

// recordingErrandPort records every dispatch so the tests assert on the OUTCOME (which hull went
// where) rather than on how the decision was reached.
type recordingErrandPort struct {
	hulls    []PricingErrandHull
	hullsErr error
	sendErr  error
	sent     []heavyPricingErrand
}

func (f *recordingErrandPort) ErrandHulls(_ context.Context, _ int) ([]PricingErrandHull, error) {
	return f.hulls, f.hullsErr
}

func (f *recordingErrandPort) SendToYard(_ context.Context, _ int, shipSymbol, waypointSymbol string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, heavyPricingErrand{Ship: shipSymbol, Yard: waypointSymbol})
	return nil
}

// idleTradeHull is an eligible carrier: trade-dedicated, cargo-capable, idle and parked.
func idleTradeHull(symbol, at string) PricingErrandHull {
	return PricingErrandHull{Symbol: symbol, Fleet: heavyPricingErrandFleet, Location: at, Idle: true, CargoCapacity: 80}
}

// parkedSensingProbe is the hull the standing owner rule forbids re-tasking: a zero-hold satellite
// standing on a sensing post, tagged to the sensing fleet.
func parkedSensingProbe(symbol, at string) PricingErrandHull {
	return PricingErrandHull{Symbol: symbol, Fleet: "sensing_parked", Location: at, Idle: true, CargoCapacity: 0}
}

// unpricedYard is the production shape: a heavy yard whose availability is known and whose ask has
// never been read, because nothing has ever stood there.
func unpricedYard(waypoint string, hops int) KnownHeavyYard {
	return KnownHeavyYard{
		SystemSymbol:   waypoint[:len(waypoint)-5],
		WaypointSymbol: waypoint,
		ShipType:       "SHIP_HEAVY_FREIGHTER",
		PurchasePrice:  0,
		Hops:           hops,
		Reachable:      true,
	}
}

// errandHandler wires a coordinator whose every buy-path reader is healthy, plus the errand ports.
func errandHandler(catalog *fakeHeavyYardCatalog, errand *recordingErrandPort) (*RunFleetAutosizerCoordinatorHandler, *recordingMetrics) {
	h, _, metrics, _ := armedHandler()
	h.SetHeavyYardCatalogReader(catalog)
	h.SetHeavyPricingErrandPort(errand)
	return h, metrics
}

func errandCmd() *RunFleetAutosizerCoordinatorCommand {
	return &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "autosizer-1", HeavyCap: intPtr(5)}
}

// B1 — THE PRODUCTION REGRESSION.
//
// Reproduces the live state exactly: a heavy-selling yard is KNOWN (an availability-only
// shipyard_inventory row) with NO price, and no hull stands anywhere near it. Nothing in the fleet
// sends one, so the ask is never read, no price ever exists, the reservation is permanently zero,
// probe buying never stands down, treasury never accumulates and no heavy is ever bought.
//
// The tick must dispatch a pricing errand — and once that errand's scan lands a price, the
// reservation must become non-zero, which is the whole point of paying for the trip.
func TestReconcile_UnpricedHeavyYard_DispatchesErrandAndReservesOncePriced(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}
	h, metrics := errandHandler(catalog, errand)
	// No priced heavy yard exists yet — exactly what CheapestHeavyPrice reports in production.
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if len(errand.sent) != 1 {
		t.Fatalf("a known-but-unpriced heavy yard must dispatch exactly ONE pricing errand, got %d", len(errand.sent))
	}
	if errand.sent[0].Yard != "X1-QR78-AE4F" || errand.sent[0].Ship != "TORWIND-A" {
		t.Fatalf("errand went to the wrong place: %+v", errand.sent[0])
	}
	if metrics.lastReserve != 0 {
		t.Fatalf("with no price known the reservation must still be 0, got %d", metrics.lastReserve)
	}

	// The errand's scan lands: the yard now carries a real ask.
	catalog.yards = []KnownHeavyYard{{
		SystemSymbol: "X1-QR78", WaypointSymbol: "X1-QR78-AE4F", ShipType: "SHIP_HEAVY_FREIGHTER",
		PurchasePrice: 1_565_500, Hops: 3, Reachable: true,
	}}
	h.SetHeavyYardReader(&fakeHeavyYard{price: 1_565_500, found: true})
	errand.hulls = []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-QR78-AE4F")}

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if metrics.lastReserve != 1_565_500 {
		t.Fatalf("once the price landed the reservation must engage: reserve = %d, want 1565500", metrics.lastReserve)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("a priced yard needs no further errand, got %d dispatches in total", len(errand.sent))
	}
}

// B2 — the in-flight bound is CONSULTED, not incidental.
//
// The fixture supplies THREE unpriced yards (strictly more than the bound of one) and THREE idle
// trade hulls, so a policy that walked the candidate list would dispatch three. Exactly one may go.
func TestReconcile_ManyUnpricedYards_DispatchesOnlyOneErrandPerTick(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-QR78-AE4F", 3),
		unpricedYard("X1-QR78-FE8C", 3),
		unpricedYard("X1-XX12-DD8D", 5),
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		idleTradeHull("TORWIND-A", "X1-HOME-A1"),
		idleTradeHull("TORWIND-B", "X1-HOME-A1"),
		idleTradeHull("TORWIND-C", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("three unpriced yards and three free hulls must still dispatch ONE errand, got %d", len(errand.sent))
	}
}

// B2 (second half) — a hull already committed holds the whole errand, even though other unpriced
// yards are uncovered and other hulls are free. Without this, every tick of the flight window
// dispatches another hull and the bound of one becomes a convoy.
func TestReconcile_ErrandAlreadyInFlight_SendsNothingElse(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-QR78-AE4F", 3),
		unpricedYard("X1-QR78-FE8C", 3),
		unpricedYard("X1-XX12-DD8D", 5),
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		// Flying to the SECOND candidate: a hull's location is its destination while in transit.
		{Symbol: "TORWIND-A", Fleet: heavyPricingErrandFleet, Location: "X1-QR78-FE8C", Idle: true, InTransit: true, CargoCapacity: 80},
		idleTradeHull("TORWIND-B", "X1-HOME-A1"),
		idleTradeHull("TORWIND-C", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("an errand already in flight must hold every other dispatch, got %+v", errand.sent)
	}
}

// B2 (third half) — a hull that has ARRIVED but whose scan has not yet written the price is the
// same errand one step further along. Counting only hulls in transit would dispatch a second hull
// on every tick of the arrival window.
func TestReconcile_HullStandingAtUnpricedYard_CountsAsInFlight(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-QR78-AE4F", 3),
		unpricedYard("X1-QR78-FE8C", 3),
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		idleTradeHull("TORWIND-A", "X1-QR78-AE4F"), // arrived, parked, scan pending
		idleTradeHull("TORWIND-B", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a hull standing at an unpriced heavy yard IS the errand — nothing else may go, got %+v", errand.sent)
	}
}

// B3 — THE STANDING OWNER RULE. Parked sensing probes are load-bearing and must never be
// re-tasked. Here they are the ONLY hulls that look free, and the errand must go hungry.
//
// The fixture includes a MIS-TAGGED probe (a zero-hold satellite carrying the trade tag, the shape
// the sensing engine documents as a recoverable tag-write miss) to prove the second, independent
// lock: the fleet allowlist alone would have taken it.
func TestReconcile_NeverRetasksAParkedSensingProbe(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		parkedSensingProbe("PROBE-1", "X1-BA69-D10D"),
		parkedSensingProbe("PROBE-2", "X1-MY3-FE7D"),
		{Symbol: "PROBE-3", Fleet: heavyPricingErrandFleet, Location: "X1-MC90-B4", Idle: true, CargoCapacity: 0},
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a parked sensing probe must NEVER be taken for a pricing errand, got %+v", errand.sent)
	}
}

// B4 — with no eligible carrier the tick does NOTHING rather than reaching for a hull it should
// not have. Every hull here is ineligible for a different reason, so no single relaxation of the
// predicate would let the errand through.
func TestReconcile_NoIdleTradeHull_SendsNothing(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		{Symbol: "TORWIND-A", Fleet: heavyPricingErrandFleet, Location: "X1-HOME-A1", Idle: false, CargoCapacity: 80},                // working a tour
		{Symbol: "TORWIND-B", Fleet: heavyPricingErrandFleet, Location: "X1-FAR-B2", Idle: true, InTransit: true, CargoCapacity: 80}, // already flying somewhere else
		{Symbol: "HAULER-C", Fleet: "contract", Location: "X1-HOME-A1", Idle: true, CargoCapacity: 80},                               // another fleet's hull
		{Symbol: "SPARE-D", Fleet: "", Location: "X1-HOME-A1", Idle: true, CargoCapacity: 80},                                        // undedicated: not ours to take
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("with no idle trade hull the tick must do nothing, got %+v", errand.sent)
	}
}

// B5 — candidate ordering is TOTAL, deterministic and stable: nearest first, waypoint symbol as
// the tiebreak, unreachable yards excluded entirely. An unstable order would let consecutive ticks
// each start an errand to a different yard and call the other one "already in flight".
func TestUnpricedHeavyYards_OrderIsNearestThenSymbolAndStable(t *testing.T) {
	yards := []KnownHeavyYard{
		unpricedYard("X1-ZZ99-ZZ9Z", 1),
		{SystemSymbol: "X1-UN00", WaypointSymbol: "X1-UN00-AA1A", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 0, Reachable: false},
		unpricedYard("X1-AA11-AA1A", 1),
		unpricedYard("X1-BB22-BB2B", 0),
		{SystemSymbol: "X1-PR33", WaypointSymbol: "X1-PR33-CC3C", ShipType: "SHIP_BULK_FREIGHTER", PurchasePrice: 2_931_905, Hops: 0, Reachable: true},
	}
	want := []string{"X1-BB22-BB2B", "X1-AA11-AA1A", "X1-ZZ99-ZZ9Z"}

	for attempt := 0; attempt < 5; attempt++ {
		got := unpricedHeavyYards(yards)
		if len(got) != len(want) {
			t.Fatalf("attempt %d: got %d candidates, want %d (priced and unreachable yards must be excluded)", attempt, len(got), len(want))
		}
		for i := range want {
			if got[i].WaypointSymbol != want[i] {
				t.Fatalf("attempt %d: candidate %d = %s, want %s", attempt, i, got[i].WaypointSymbol, want[i])
			}
		}
	}
}

// B6 — the errand stands down whenever no heavy is wanted, so it never spends a working hull's
// time buying information about a purchase that cannot happen. All three shapes are the SAME rule
// the reservation applies, which is why both stand down together.
func TestReconcile_NoHeavyWanted_SendsNoPricingErrand(t *testing.T) {
	cases := []struct {
		name   string
		census *fakeHeavyCensus
		cap    *int
	}{
		{"already at the heavy cap", &fakeHeavyCensus{owned: 5}, intPtr(5)},
		{"operator holds at zero heavies", &fakeHeavyCensus{owned: 0}, intPtr(0)},
		{"owned-heavy census unreadable", &fakeHeavyCensus{err: errors.New("db down")}, intPtr(5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
			errand := &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}
			h, _ := errandHandler(catalog, errand)
			h.SetHeavyCensusReader(tc.census)
			h.SetHeavyYardReader(&fakeHeavyYard{found: false})

			cmd := errandCmd()
			cmd.HeavyCap = tc.cap
			if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
				t.Fatalf("reconcileOnce error: %v", err)
			}
			if len(errand.sent) != 0 {
				t.Fatalf("no heavy wanted must send no errand, got %+v", errand.sent)
			}
		})
	}
}

// B6 (second half) — every known heavy yard already priced means the errand has nothing to do,
// which is success, not a wait. Also pins the nil-safe and unreadable paths: an unwired or blind
// tick moves no hull and never panics.
func TestReconcile_PricingErrandStandsDownWhenThereIsNothingToPrice(t *testing.T) {
	pricedOnly := []KnownHeavyYard{{
		SystemSymbol: "X1-QR78", WaypointSymbol: "X1-QR78-AE4F", ShipType: "SHIP_HEAVY_FREIGHTER",
		PurchasePrice: 1_565_500, Hops: 3, Reachable: true,
	}}
	cases := []struct {
		name    string
		catalog *fakeHeavyYardCatalog
		errand  *recordingErrandPort
	}{
		{"every known heavy yard is priced", &fakeHeavyYardCatalog{yards: pricedOnly}, &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}},
		{"no heavy yard is known at all", &fakeHeavyYardCatalog{}, &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}},
		{"the catalogue is unreadable", &fakeHeavyYardCatalog{err: errors.New("db down")}, &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}},
		{"the fleet is unreadable", &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}, &recordingErrandPort{hullsErr: errors.New("db down")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := errandHandler(tc.catalog, tc.errand)
			h.SetHeavyYardReader(&fakeHeavyYard{found: false})
			if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
				t.Fatalf("reconcileOnce error: %v", err)
			}
			if len(tc.errand.sent) != 0 {
				t.Fatalf("nothing to price must send nothing, got %+v", tc.errand.sent)
			}
		})
	}
}

// Unwired ports must leave the coordinator exactly as it was before the errand existed: a tick
// with no catalogue and no errand port sizes classes normally and never panics.
func TestReconcile_PricingErrandUnwired_TickIsUnaffected(t *testing.T) {
	h, purchaser, _, _ := armedHandler(lightShortfall())
	res, err := h.reconcileOnce(context.Background(), errandCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || len(purchaser.orders) != 1 {
		t.Fatalf("an unwired errand must not disturb the sizing tick: purchased=%d orders=%d", res.Purchased, len(purchaser.orders))
	}
}

// A dispatch failure is a WAIT, never a fatal: the tick continues and the errand retries later.
func TestReconcile_ErrandDispatchFailure_IsARetryNotAFailure(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{
		hulls:   []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")},
		sendErr: errors.New("no route"),
	}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("a failed pricing errand must not fail the tick: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a failed dispatch must record no errand, got %+v", errand.sent)
	}
}
