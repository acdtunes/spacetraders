package commands

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// The heavy-yard PRICING ERRAND suite.
//
// TEST BUDGET (behaviour-first): 7 distinct behaviours × 2 = 14 unit tests maximum.
//
//	B1 a known-but-unpriced heavy yard with no hull near it dispatches ONE errand
//	B2 only ONE errand is in flight at a time
//	B3 the carrier is a SPARE PARKED PROBE, and only ever a spare one
//	B4 no eligible carrier ⇒ nothing moves (never steal one)
//	B5 candidate ordering is deterministic and stable
//	B6 the errand stands down when no heavy is wanted / everything is priced
//	B7 every decline STATES ITS REASON — the errand is never silent
//	B8 exactly ONE coordinator drives it, and it drives it on the PROBE wave
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

// spareParkedProbe is THE eligible carrier: a zero-hold satellite in the parked-sensing pool,
// standing still, and named by no scout post. Zero cargo is the production shape of every probe —
// a fixture with a hold would let a re-introduced cargo predicate pass unnoticed.
//
// IT TAGS THE HULL FROM THE WRITER'S SYMBOL, NOT THE READER'S. Tagging from the allowlist const
// would compare the predicate against itself: the fixture would co-vary with any re-spelling and
// the entire eligibility suite would stay green while the eligible set emptied in production.
// Sourcing the tag from the side that STAMPS it makes the two independent, so a drift fails here.
func spareParkedProbe(symbol, at string) PricingErrandHull {
	return PricingErrandHull{Symbol: symbol, Fleet: parkedsensing.SensingParkedFleetTag, Location: at, Idle: true, CargoCapacity: 0}
}

// mannedScoutProbe is the probe the standing owner rule forbids taking: same pool, same idle look —
// but a live scout post names it, so it belongs to the scout coordinator between tours as much as
// during one.
func mannedScoutProbe(symbol, at string) PricingErrandHull {
	return PricingErrandHull{Symbol: symbol, Fleet: parkedsensing.SensingParkedFleetTag, Location: at, Idle: true, MannedScoutPost: true}
}

// idleTradeHull is a WORKING hull that happens to be free this instant — cargo-capable, idle,
// parked, and still not the errand's to take. It is the pool the errand used to draw from, and the
// pool that was busy every time it looked.
func idleTradeHull(symbol, at string) PricingErrandHull {
	return PricingErrandHull{Symbol: symbol, Fleet: "trade", Location: at, Idle: true, CargoCapacity: 80}
}

// runErrandTick drives one reconcile with a capturing logger and hands back what it said, so a test
// can assert on the DECLINE REASON as well as on the dispatch.
func runErrandTick(t *testing.T, h *RunFleetGrowthCoordinatorHandler, cmd *RunFleetGrowthCoordinatorCommand) *capturingLogger {
	t.Helper()
	logger := &capturingLogger{}
	if _, err := h.reconcileOnce(logging.WithLogger(context.Background(), logger), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	return logger
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

// errandReserveSink captures the reservation the tick derived. It overrides ONE method of the
// no-op sink so an un-recorded call cannot pass as a zero reserve.
type errandReserveSink struct {
	noopGrowthSink
	lastReserve int64
}

func (s *errandReserveSink) RecordHeavyReserve(_ string, reserve, _ int64, _, _ int) {
	s.lastReserve = reserve
}

// errandHandler wires a coordinator whose every buy-path reader is healthy, plus the errand ports.
//
// It builds the FLEET-GROWTH coordinator because that is the fleet's heavy buyer and therefore the
// errand's one driver. Nothing about the errand's policy is expressed here — only which tick loop
// calls it — which is what makes the assertions below transfer unread.
func errandHandler(catalog *fakeHeavyYardCatalog, errand *recordingErrandPort) (*RunFleetGrowthCoordinatorHandler, *errandReserveSink) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	h.SetTreasuryReader(&fakeTreasury{credits: 5000000, ok: true})
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 40, ok: true})
	h.SetYardPriceReader(&fakeYardPrice{price: 437000, cheapest: 400000, yard: "KA42-A2", ok: true})
	// The owned-heavy census must be READABLE or the errand stands down on a blind census before
	// any of the yard rules is reached. The harness owns no heavy hull, so the cap has room and
	// each test keeps pinning the rule it is about.
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	metrics := &errandReserveSink{}
	h.SetMetricsSink(metrics)
	h.SetHeavyYardCatalogReader(catalog)
	h.SetHeavyPricingErrandPort(errand)
	return h, metrics
}

func errandCmd() *RunFleetGrowthCoordinatorCommand {
	return &RunFleetGrowthCoordinatorCommand{PlayerID: 5, ContainerID: "growth-1", HeavyCap: intPtr(5)}
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
	errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
	h, metrics := errandHandler(catalog, errand)
	// No priced heavy yard exists yet — exactly what CheapestHeavyPrice reports in production.
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if len(errand.sent) != 1 {
		t.Fatalf("a known-but-unpriced heavy yard must dispatch exactly ONE pricing errand, got %d", len(errand.sent))
	}
	if errand.sent[0].Yard != "X1-QR78-AE4F" || errand.sent[0].Ship != "PROBE-A" {
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
	errand.hulls = []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-QR78-AE4F")}

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
// The fixture supplies THREE unpriced yards (strictly more than the bound of one) and THREE spare
// probes, so a policy that walked the candidate list would dispatch three. Exactly one may go.
func TestReconcile_ManyUnpricedYards_DispatchesOnlyOneErrandPerTick(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-QR78-AE4F", 3),
		unpricedYard("X1-QR78-FE8C", 3),
		unpricedYard("X1-XX12-DD8D", 5),
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		spareParkedProbe("PROBE-A", "X1-HOME-A1"),
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
		spareParkedProbe("PROBE-C", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("three unpriced yards and three free hulls must still dispatch ONE errand, got %d", len(errand.sent))
	}
	if errand.sent[0].Ship != "PROBE-A" {
		t.Fatalf("the tie must break on ship symbol so two ticks cannot disagree about which hull is THE errand: sent %s, want PROBE-A", errand.sent[0].Ship)
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
		{Symbol: "PROBE-A", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-QR78-FE8C", Idle: true, InTransit: true},
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
		spareParkedProbe("PROBE-C", "X1-HOME-A1"),
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
		spareParkedProbe("PROBE-A", "X1-QR78-AE4F"), // arrived, parked, scan pending
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
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

// B3 — THE PRODUCTION REGRESSION, AND THE FIX (sp-gmfvw).
//
// This is the live staging fleet, reproduced: FIVE trade hulls, none of them free (four in transit,
// one docked mid-tour), and a pool of idle parked probes standing by. Under the old trade-only,
// cargo-capable allowlist the eligible set was EMPTY — and it was empty on every tick of an entire
// era, while thirteen known heavy yards sat at purchase_price 0.
//
// A probe must be selected. Pricing a yard needs presence, not a hold.
//
// MUTATION: restore `h.Fleet != "trade"` or `h.CargoCapacity <= 0` in pricingErrandCarrier. Either
// one empties the eligible set against this fixture and no errand goes — which is the bug.
func TestReconcile_EveryTradeHullBusy_SendsASpareParkedProbe(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-RX81-B7", 5)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		// The trade pool, exactly as staging held it: nothing spare in it, ever.
		{Symbol: "TORWIND-1", Fleet: "trade", Location: "X1-KP46-A2", Idle: true, InTransit: true, CargoCapacity: 80},
		{Symbol: "TORWIND-2", Fleet: "trade", Location: "X1-BA69-D10D", Idle: true, InTransit: true, CargoCapacity: 80},
		{Symbol: "TORWIND-3", Fleet: "trade", Location: "X1-MY3-FE7D", Idle: true, InTransit: true, CargoCapacity: 80},
		{Symbol: "TORWIND-4", Fleet: "trade", Location: "X1-KP23-A2", Idle: true, InTransit: true, CargoCapacity: 80},
		{Symbol: "TORWIND-5", Fleet: "trade", Location: "X1-KP46-A2", Idle: false, CargoCapacity: 80}, // docked mid-tour
		// The spare pool nobody was looking at.
		spareParkedProbe("PROBE-M", "X1-KP23-C38"),
		// THE WINNER CARRIES THE TAG AS A LITERAL — the exact string the parked-sensing engine
		// stamps into dedicated_fleet the instant it buys a probe. Every other carrier fixture in
		// this suite names a const, so a re-spelling of the shared tag would move both sides
		// together and be invisible; this hull is the one that is independent of both. Rename the
		// tag and it stops being eligible, the errand falls to PROBE-M, and the assertion below
		// fires — which is what a value written into a live column deserves.
		{Symbol: "PROBE-B", Fleet: "sensing_parked", Location: "X1-KP46-A2", Idle: true, CargoCapacity: 0},
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if len(errand.sent) != 1 {
		t.Fatalf("with every trade hull busy and idle probes standing by, ONE probe must fly the errand — got %d dispatches. "+
			"This is the sp-gmfvw stall: a carrier pool that is never spare means the yard is never priced", len(errand.sent))
	}
	if errand.sent[0].Ship != "PROBE-B" {
		t.Fatalf("the carrier must be the lowest-symbol SPARE PROBE (deterministic tie-break), got %q. "+
			"PROBE-B is tagged with the literal wire value, so losing it means the allowlist no longer "+
			"matches the tag production writes and the eligible set is empty on the live fleet", errand.sent[0].Ship)
	}
	if errand.sent[0].Yard != "X1-RX81-B7" {
		t.Fatalf("the errand must fly to the nearest unpriced heavy yard, got %q", errand.sent[0].Yard)
	}
}

// B3 (second half) — THE STANDING OWNER RULE, which the probe pool now needs explicitly.
//
// The fleet tag used to separate spare hulls from working ones on its own. It cannot any more: the
// scout coordinator mans its posts from THIS pool, so a probe carrying the sensing tag may be
// someone's working scout. A hull a live post NAMES is that post's — including in the idle gap
// between two tours, which is exactly when it looks most available.
//
// Every hull here is a sensing-tagged probe, so the fleet allowlist alone would have taken one.
func TestReconcile_NeverTakesAProbeThatMansAScoutPostOrIsAlreadyFlying(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		mannedScoutProbe("PROBE-1", "X1-BA69-D10D"), // a post names it — idle between tours
		mannedScoutProbe("PROBE-2", "X1-MY3-FE7D"),  // and so does another
		{Symbol: "PROBE-3", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-MC90-B4", Idle: true, InTransit: true}, // already flying somewhere else
		{Symbol: "PROBE-4", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-MC90-B4", Idle: false},                 // working
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a probe manning a scout post, in transit, or working must NEVER be taken for a pricing errand, got %+v", errand.sent)
	}
}

// B3 (third half) — a manned probe standing beside a free one must not shadow it, and must not be
// preferred by an ordering that ignores the rule. The manned probe sorts FIRST on symbol, so a
// carrier walk that checked the owner rule after choosing would return the wrong hull.
func TestReconcile_AMannedProbeIsSkipped_NotTreatedAsTheWinner(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		mannedScoutProbe("PROBE-AAA", "X1-BA69-D10D"), // sorts first, and is not available
		spareParkedProbe("PROBE-ZZZ", "X1-KP23-C38"),  // sorts last, and is the only one free
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 || errand.sent[0].Ship != "PROBE-ZZZ" {
		t.Fatalf("the free probe must win even though the manned one sorts ahead of it, got %+v", errand.sent)
	}
}

// B4 — with no eligible carrier the tick does NOTHING rather than reaching for a hull it should
// not have. Every hull here is ineligible for a different reason, so no single relaxation of the
// predicate would let the errand through — and the trade hull is idle and cargo-capable on purpose,
// because "free right now" is not the same as "spare".
//
// The near-miss hull is here because the allowlist is an EXACT match on the tag, never a prefix or
// a contains: a pool named for the sensing one is a DIFFERENT pool, and admitting it would take a
// hull this engine does not own. Written as a literal so the property survives a rename of the tag.
func TestReconcile_NoSpareProbe_SendsNothing(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		idleTradeHull("TORWIND-A", "X1-HOME-A1"),                                                       // earning, even when momentarily idle
		{Symbol: "HAULER-C", Fleet: "contract", Location: "X1-HOME-A1", Idle: true, CargoCapacity: 80}, // another fleet's hull
		{Symbol: "SPARE-D", Fleet: "", Location: "X1-HOME-A1", Idle: true, CargoCapacity: 80},          // undedicated: not ours to take
		{Symbol: "PROBE-E", Fleet: "sensing_parked_v2", Location: "X1-HOME-A1", Idle: true},            // a near-miss tag is another fleet
		{Symbol: "", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-HOME-A1", Idle: true},   // a torn row names no ship
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("with no spare parked probe the tick must do nothing, got %+v", errand.sent)
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
			errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
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
		{"every known heavy yard is priced", &fakeHeavyYardCatalog{yards: pricedOnly}, &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}},
		{"no heavy yard is known at all", &fakeHeavyYardCatalog{}, &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}},
		{"the catalogue is unreadable", &fakeHeavyYardCatalog{err: errors.New("db down")}, &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}},
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
// with no catalogue and no errand port buys normally and never panics.
func TestReconcile_PricingErrandUnwired_TickIsUnaffected(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
	})
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("an unwired errand must not disturb the buying tick: purchased=%d orders=%d", res.Purchased, buyer.calls)
	}
}

// A dispatch failure is a WAIT, never a fatal: the tick continues and the errand retries later.
func TestReconcile_ErrandDispatchFailure_IsARetryNotAFailure(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{
		hulls:   []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")},
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

// B7 — THE DEFECT BEHIND THE DEFECT: the errand used to decline with NO LOG LINE AT ALL.
//
// That silence is why sp-gmfvw took production-log archaeology across an entire era to find: a
// mechanism that is running and waiting looked exactly like one that was never wired, and nothing
// in the log could tell an operator which. Every decline must now NAME its reason, and the reasons
// must be distinguishable — "nothing free to send" and "already under way" are both waits, but only
// one of them means the carrier pool is wrong.
//
// MUTATION: delete any logPricingErrandDecline call, or collapse two reasons into one string.
func TestReconcile_EveryPricingErrandDecline_NamesItsReason(t *testing.T) {
	unpriced := []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}
	cases := []struct {
		name    string
		reason  pricingErrandDecline
		catalog *fakeHeavyYardCatalog
		errand  *recordingErrandPort
		census  *fakeHeavyCensus
		cap     *int
	}{
		{
			name:    "no spare probe is free — THE LIVE FAILURE MODE",
			reason:  pricingErrandNoCarrier,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand:  &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}},
		},
		{
			name:    "an errand is already under way",
			reason:  pricingErrandAlreadyInFlight,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand: &recordingErrandPort{hulls: []PricingErrandHull{
				spareParkedProbe("PROBE-A", "X1-QR78-AE4F"),
				spareParkedProbe("PROBE-B", "X1-HOME-A1"),
			}},
		},
		{
			name:    "no heavy yard is in the catalogue yet — the sweep keeps looking",
			reason:  pricingErrandNoYardKnown,
			catalog: &fakeHeavyYardCatalog{},
			errand:  &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}},
		},
		{
			name:   "every known heavy yard is unpriced but out of reach",
			reason: pricingErrandNothingInReach,
			catalog: &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
				{SystemSymbol: "X1-FA00", WaypointSymbol: "X1-FA00-AA1A", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 9, Reachable: false},
			}},
			errand: &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}},
		},
		{
			name:    "the fleet already holds its heavy cap",
			reason:  pricingErrandAtHeavyCap,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand:  &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}},
			census:  &fakeHeavyCensus{owned: 5},
			cap:     intPtr(5),
		},
		{
			name:    "the owned-heavy census cannot be read",
			reason:  pricingErrandCensusBlind,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand:  &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}},
			census:  &fakeHeavyCensus{err: errors.New("db down")},
		},
	}

	seen := map[pricingErrandDecline]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := errandHandler(tc.catalog, tc.errand)
			h.SetHeavyYardReader(&fakeHeavyYard{found: false})
			if tc.census != nil {
				h.SetHeavyCensusReader(tc.census)
			}
			cmd := errandCmd()
			if tc.cap != nil {
				cmd.HeavyCap = tc.cap
			}

			logger := runErrandTick(t, h, cmd)

			if len(tc.errand.sent) != 0 {
				t.Fatalf("this case must decline, not dispatch: %+v", tc.errand.sent)
			}
			if !logger.sawAction("autosizer_heavy_pricing_declined") {
				t.Fatalf("the errand declined SILENTLY — that silence is the sp-gmfvw defect. Lines seen:\n%s", logger.joined())
			}
			if !strings.Contains(logger.joined(), string(tc.reason)) {
				t.Fatalf("the decline must NAME reason %q so an operator can act on it. Lines seen:\n%s", tc.reason, logger.joined())
			}
			seen[tc.reason] = true
		})
	}

	// The reasons must be DISTINCT: one string reused for two states restores the ambiguity.
	if len(seen) != len(cases) {
		t.Fatalf("the six decline cases produced only %d distinct reasons — collapsing two states into one reason "+
			"re-creates the ambiguity this test exists to prevent", len(seen))
	}
}

// B7 (second half) — a decline line carries the EVIDENCE, not just a verdict. The counts are what
// separate "the map has not grown far enough yet" from "the map is fine and the carrier pool is
// empty", and staging sat in the first while the log said nothing at all.
func TestReconcile_ADeclineLineCarriesTheYardCounts(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-RX81-B7", 5), // in reach
		{SystemSymbol: "X1-FA00", WaypointSymbol: "X1-FA00-AA1A", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 9, Reachable: false},
		{SystemSymbol: "X1-FB01", WaypointSymbol: "X1-FB01-AA1A", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 11, Reachable: false},
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{idleTradeHull("TORWIND-A", "X1-HOME-A1")}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	line := logger.joined()
	for _, want := range []string{"3 heavy yard(s) known", "1 unpriced and in reach", "X1-RX81-B7", "2 unpriced but out of reach"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the decline line must carry %q — without the counts the same sentence describes a healthy fleet and a stalled one. Lines seen:\n%s", want, line)
		}
	}
}

// B7 (third half) — a DISPATCH says so too, and says it loudly enough to correlate with the price
// landing a tick or two later. A dispatch that logged nothing would leave the successful path as
// invisible as the broken one was.
func TestReconcile_ADispatchAnnouncesTheHullAndTheYard(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-RX81-B7", 5)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	if !logger.sawAction("autosizer_heavy_pricing_dispatched") {
		t.Fatalf("a dispatched errand must announce itself. Lines seen:\n%s", logger.joined())
	}
	if logger.sawAction("autosizer_heavy_pricing_declined") {
		t.Fatalf("a tick that dispatched must not ALSO report a decline. Lines seen:\n%s", logger.joined())
	}
	line := logger.joined()
	if !strings.Contains(line, "PROBE-A") || !strings.Contains(line, "X1-RX81-B7") {
		t.Fatalf("the dispatch line must name the hull and the yard. Lines seen:\n%s", line)
	}
}

// B8 — EXACTLY ONE COORDINATOR DRIVES THE ERRAND.
//
// The errand's whole policy — one hull in flight fleet-wide, one spare probe, never a manned one —
// is stated once and derived from ship rows every tick. Two coordinators holding the ports would
// each read the same durable facts, each conclude nothing was in flight, and each dispatch: the
// bound of one becomes a bound of one PER DRIVER. So ownership is asserted structurally rather than
// left to the reader of two tick loops.
func TestPricingErrand_HasExactlyOneDriver(t *testing.T) {
	for _, port := range []string{"heavyYardCatalog", "heavyErrand"} {
		if reflect.ValueOf(&RunFleetAutosizerCoordinatorHandler{}).Elem().FieldByName(port).IsValid() {
			t.Fatalf("the autosizer still holds the %q port — two heavy-pricing drivers is two opinions "+
				"about the one-hull-at-a-time bound", port)
		}
		if !reflect.ValueOf(&RunFleetGrowthCoordinatorHandler{}).Elem().FieldByName(port).IsValid() {
			t.Fatalf("the fleet's heavy buyer must hold the %q port — unheld, no yard is ever priced "+
				"and no heavy can ever be bought", port)
		}
	}
}

// THE ERRAND RUNS ON THE PROBE WAVE, and that is not incidental — it is what lets the wave ever
// flip. Before any heavy is priced there is no target, so the predicate is PROBE; if the errand
// only ran on HEAVY, no yard would ever be priced and no HEAVY wave could ever occur.
func TestPricingErrand_RunsOnTheProbeWave(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-AA-YARD", 1)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})
	// Every profitable lane is served, so the wave is PROBE for the plainest possible reason.
	h.SetUnservedLaneReader(&fakeLanes{count: 0, readable: true})

	wave := &recordingWaveSink{}
	h.SetMetricsSink(wave)

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wave.waves) != 1 || wave.waves[0] != common.WaveProbe {
		t.Fatalf("the fixture must produce a PROBE wave or this test proves nothing, got %v", wave.waves)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("the errand must run on a PROBE wave, got %d dispatches", len(errand.sent))
	}
}

// It spends nothing and buys nothing — it makes a LATER tick's price readable, which is why it
// weakens no money guard by running before one can pass.
func TestPricingErrand_SpendsNothing(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-AA-YARD", 1)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})
	h.SetUnservedLaneReader(&fakeLanes{count: 0, readable: true})

	buyer := &growthPurchaseRecorder{}
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("the fixture must dispatch or this test proves nothing, got %d", len(errand.sent))
	}
	if buyer.calls != 0 {
		t.Fatalf("the errand must spend nothing, got %d buys", buyer.calls)
	}
}

// THE MASTER SWITCH REACHES THE ERRAND. The errand spends no credits, so a gate placed on the
// PURCHASE would leave it running: it would keep reading the catalogue and the fleet and keep
// flying probes for a coordinator the operator has switched off. Off must stop the reads.
func TestReconcile_GrowthSwitchedOff_RunsNoErrand(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	// SATURATION FIRST: the same fixture with the switch ON must dispatch, or the assertion below
	// would hold with the switch deleted entirely.
	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("FIXTURE IS NOT SATURATED: the switch ON must dispatch, got %d", len(errand.sent))
	}

	h.SetGrowthConfigReader(stubGrowthConfig{growthEnabledKey: growthDisabled})
	errand.sent = nil
	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("a paused tick must not error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a switched-off coordinator must fly no pricing errand, got %+v", errand.sent)
	}
}
