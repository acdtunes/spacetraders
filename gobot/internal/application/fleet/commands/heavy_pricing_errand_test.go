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
//	B9 the yard it picks is one the CARRIER can fly to, and presence it already has is USED
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

	// reach is THE NAVIGATOR'S OWN ANSWER, as this fixture models it: origin system → target
	// system → hops, and a target absent from an origin's row is one that origin cannot route to
	// within the executor's bound. It is the fact the yard catalogue does not carry, because the
	// catalogue measures from the nearest system the WHOLE FLEET stands in.
	//
	// A NIL MAP MEANS "THE GRAPH AGREES WITH THE CATALOGUE" — every candidate one jump from every
	// origin. That is the assumption the errand used to make silently, so leaving it nil is how the
	// suite's older fixtures keep asserting the rules they were written for (carrier eligibility,
	// the in-flight bound, the decline taxonomy) without also having to state a gate topology.
	reach    map[string]map[string]int
	reachErr error

	// pricedInPlace records every yard read WHERE A HULL ALREADY STOOD — the zero-movement errand.
	// It is a separate list from sent on purpose: "a price was obtained" and "a hull was flown"
	// are the two outcomes this bead exists to stop conflating.
	pricedInPlace []string
	priceErr      error
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

func (f *recordingErrandPort) PriceYardInPlace(_ context.Context, _ int, waypointSymbol string) error {
	if f.priceErr != nil {
		return f.priceErr
	}
	f.pricedInPlace = append(f.pricedInPlace, waypointSymbol)
	return nil
}

func (f *recordingErrandPort) HopsFrom(_ context.Context, fromSystem string, toSystems []string) (map[string]int, error) {
	if f.reachErr != nil {
		return nil, f.reachErr
	}
	if f.reach == nil {
		out := make(map[string]int, len(toSystems))
		for _, s := range toSystems {
			out[s] = 1
		}
		return out, nil
	}
	out := make(map[string]int, len(toSystems))
	for _, s := range toSystems {
		if d, ok := f.reach[fromSystem][s]; ok {
			out[s] = d
		}
	}
	return out, nil
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

// B2 (third half) — A HULL THAT HAS ARRIVED IS NOT A WAIT, IT IS THE ANSWER (sp-37alr).
//
// This test used to assert the opposite, and the assertion it made is the wedge: a hull standing on
// an unpriced yard "counted as an errand in flight", so the tick declined — and NOTHING ever
// completed that errand, because the only thing that reads a yard is an ARRIVAL and the hull had
// already arrived. Live, a probe stood docked on an unpriced heavy yard for 25 hours while every
// heavy yard the fleet knew carried purchase_price 0.
//
// Presence is the entire thing a dispatch spends a trip to buy. When it already exists the yard
// must be READ where the hull stands: no flight, and no second hull sent to a waypoint one of ours
// is already standing on.
func TestReconcile_HullStandingAtUnpricedYard_PricesItInPlaceAndFliesNothing(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-QR78-AE4F", 3),
		unpricedYard("X1-QR78-FE8C", 3),
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		spareParkedProbe("PROBE-A", "X1-QR78-AE4F"), // arrived, parked, and standing on the answer
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a hull already standing at an unpriced heavy yard must fly NOTHING, got %+v", errand.sent)
	}
	if len(errand.pricedInPlace) != 1 || errand.pricedInPlace[0] != "X1-QR78-AE4F" {
		t.Fatalf("the yard the hull STANDS on must be read where it sits — that presence is what an errand exists to create. "+
			"Priced in place: %v. A tick that neither flies nor reads is the 25-hour stall this bead was filed for", errand.pricedInPlace)
	}
}

// The standing hull's yard is read even when it is NOT the catalogue's nearest — a hull already
// there beats any amount of flying, because the trip is what an errand costs.
//
// The near yard carries no hull and the far one does, so a policy that walked the catalogue order
// and only then asked "is someone there?" would fly to the near yard and leave the free price
// unread.
func TestReconcile_AStandingHullBeatsAFlightToANearerYard(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{
		unpricedYard("X1-NEAR-AA1A", 0), // nearest by the catalogue, nobody there
		unpricedYard("X1-FARR-BB2B", 4), // furthest, and a hull is standing on it
	}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		spareParkedProbe("PROBE-A", "X1-FARR-BB2B"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a free price at the far yard must be taken before any flight to the near one, got %+v", errand.sent)
	}
	if len(errand.pricedInPlace) != 1 || errand.pricedInPlace[0] != "X1-FARR-BB2B" {
		t.Fatalf("the zero-movement errand must win: priced in place %v, want [X1-FARR-BB2B]", errand.pricedInPlace)
	}
}

// A hull FLYING to an unpriced yard has not arrived: its location is its destination while in
// transit, so reading the yard "where it stands" would read a yard nothing is standing at and
// persist another price-0 row. It is still the errand, and it still holds every other dispatch.
func TestReconcile_HullInTransitToAnUnpricedYard_IsNotStanding(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		{Symbol: "PROBE-A", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-QR78-AE4F", Idle: true, InTransit: true},
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	if len(errand.pricedInPlace) != 0 {
		t.Fatalf("a hull still in flight is not presence — reading the yard now persists another zero, got %v", errand.pricedInPlace)
	}
	if len(errand.sent) != 0 {
		t.Fatalf("an errand already in flight must hold every other dispatch, got %+v", errand.sent)
	}
	if !strings.Contains(logger.joined(), string(pricingErrandAlreadyInFlight)) {
		t.Fatalf("the tick must name the in-flight wait. Lines seen:\n%s", logger.joined())
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

// B2 (fourth half) — a TORN ROW parked on an unpriced yard is not an errand in flight.
//
// It is the one case where "standing" and "in flight" can still disagree: a row naming no ship
// cannot be the hull that gets its yard read (standingPricingErrand needs a symbol to name), so if
// the in-flight count also admitted standing hulls it would block the errand on a hull nobody can
// act on — a wait with no possible end, which is the shape this whole bead is about.
//
// MUTATION: drop the !InTransit guard in errandsInFlight and this fixture declines instead of
// dispatching.
func TestReconcile_ATornRowStandingAtAnUnpricedYard_DoesNotHoldTheErrand(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		{Symbol: "", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-QR78-AE4F", Idle: true},
		spareParkedProbe("PROBE-B", "X1-HOME-A1"),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.pricedInPlace) != 0 {
		t.Fatalf("a row naming no ship cannot be the presence a yard is read on, got %v", errand.pricedInPlace)
	}
	if len(errand.sent) != 1 || errand.sent[0].Ship != "PROBE-B" {
		t.Fatalf("the torn row must neither price the yard nor block the hull that can — got %+v", errand.sent)
	}
}

// B9 — THE CATALOGUE MUST TELL THE NAVIGATOR'S TRUTH (sp-37alr).
//
// The live stall, reproduced exactly. Two unpriced heavy yards, BOTH flagged Reachable by the
// catalogue and correctly so — the catalogue measures gate distance from the nearest system the
// WHOLE FLEET stands in, and some other hull of ours is near X1-FH57. The carrier is not. The
// navigator plans from the CARRIER's system and refused that yard 61 times over 25 hours:
//
//	no jump-gate route from X1-AM71 to X1-FH57 within 5 jumps
//
// Nothing in the fixture separates the two yards except the answer to that question, so the errand
// can only get this right by asking it.
//
// MUTATION: drop the per-carrier reach lookup (take unpriced[0] as before) and the errand flies at
// X1-FH57-B10A again — which is the bug, unchanged, for another 25 hours.
func TestReconcile_YardUnroutableFromTheCarrier_IsNotChosenEvenWhenTheCatalogueSaysReachable(t *testing.T) {
	catalogYards := []KnownHeavyYard{
		// Nearest by the FLEET-WIDE measure, and the one the errand kept choosing.
		unpricedYard("X1-FH57-B10A", 2),
		// Further by that measure, and the only one this carrier can actually fly to.
		unpricedYard("X1-KP46-D33F", 4),
	}
	carrier := []PricingErrandHull{spareParkedProbe("TORWINDSTG-10", "X1-AM71-A1")}

	// SATURATION FIRST. With the graph agreeing with the catalogue, the same fixture picks the
	// nearer yard — so the assertion below is moved by the reach answer and by nothing else.
	control := &recordingErrandPort{hulls: carrier}
	hc, _ := errandHandler(&fakeHeavyYardCatalog{yards: catalogYards}, control)
	hc.SetHeavyYardReader(&fakeHeavyYard{found: false})
	if _, err := hc.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(control.sent) != 1 || control.sent[0].Yard != "X1-FH57-B10A" {
		t.Fatalf("FIXTURE IS NOT SATURATED: with an agreeing gate graph the nearer yard must win, got %+v", control.sent)
	}

	// The real topology: from X1-AM71 the graph names a route to X1-KP46 and none to X1-FH57.
	errand := &recordingErrandPort{
		hulls: carrier,
		reach: map[string]map[string]int{"X1-AM71": {"X1-KP46": 3}},
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: catalogYards}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("one yard IS routable from this carrier, so exactly one errand must go, got %+v", errand.sent)
	}
	if errand.sent[0].Yard == "X1-FH57-B10A" {
		t.Fatalf("the errand chose the yard the navigator refuses. This is the 61-failure loop: the catalogue's " +
			"Reachable is measured from the nearest system the FLEET holds, and the flight starts from the CARRIER's")
	}
	if errand.sent[0].Yard != "X1-KP46-D33F" || errand.sent[0].Ship != "TORWINDSTG-10" {
		t.Fatalf("the errand must fly the carrier to the yard IT can reach, got %+v", errand.sent[0])
	}
}

// B9 (second half) — the carrier is chosen WITH the yard, not before it.
//
// Two spare probes in different systems, and one unpriced yard only the SECOND-sorting probe can
// route to. Picking the carrier first (lowest symbol, as the errand used to) and the yard after
// produces a pair that cannot fly, every tick, forever.
func TestReconcile_TheCarrierIsPairedWithAYardItCanReach_NotChosenAheadOfIt(t *testing.T) {
	errand := &recordingErrandPort{
		hulls: []PricingErrandHull{
			spareParkedProbe("PROBE-AAA", "X1-ISOL-A1"), // sorts first, routes nowhere
			spareParkedProbe("PROBE-ZZZ", "X1-NEAR-A1"), // sorts last, and can actually get there
		},
		reach: map[string]map[string]int{
			"X1-ISOL": {},
			"X1-NEAR": {"X1-QR78": 2},
		},
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 1)}}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.sent) != 1 || errand.sent[0].Ship != "PROBE-ZZZ" {
		t.Fatalf("the carrier that can reach the yard must win over the one that merely sorts first, got %+v", errand.sent)
	}
}

// B9 (third half) — no carrier can name a route to any unpriced yard ⇒ NOTHING is dispatched, and
// the tick says which of the two states it is in.
//
// Dispatching anyway is not a harmless optimism: it is exactly what produced 61 identical failures
// while a real, priceable yard went untouched. An absent distance has two meanings — genuinely
// beyond the bound, or an adjacency we have not cached — and BOTH mean the same thing to a hull
// about to be committed: no route can be named, so none is flown.
func TestReconcile_NoYardRoutableFromAnyCarrier_DispatchesNothingAndNamesIt(t *testing.T) {
	errand := &recordingErrandPort{
		hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-AM71-A1")},
		reach: map[string]map[string]int{"X1-AM71": {}},
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-FH57-B10A", 2)}}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	if len(errand.sent) != 0 {
		t.Fatalf("a hull must never be sent where no route can be named, got %+v", errand.sent)
	}
	if !strings.Contains(logger.joined(), string(pricingErrandNoRoutableYard)) {
		t.Fatalf("the tick must NAME this wait — %q is what tells an operator the map has not grown far enough, "+
			"as against a carrier pool that is empty. Lines seen:\n%s", pricingErrandNoRoutableYard, logger.joined())
	}
}

// B9 (fourth half) — an unreadable gate graph moves no hull and is named separately from "no route
// exists". One is a wait for the map; the other is a degraded read an operator must act on.
func TestReconcile_CarrierReachUnreadable_MovesNothingAndSaysSo(t *testing.T) {
	errand := &recordingErrandPort{
		hulls:    []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-AM71-A1")},
		reachErr: errors.New("gate adjacency store down"),
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-FH57-B10A", 2)}}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	if len(errand.sent) != 0 {
		t.Fatalf("an unreadable reach must move nothing, got %+v", errand.sent)
	}
	if !strings.Contains(logger.joined(), string(pricingErrandReachUnreadable)) {
		t.Fatalf("an unreadable gate graph must be named apart from a genuine absence of routes. Lines seen:\n%s", logger.joined())
	}
}

// B9 (fifth half) — the in-place read costs no reach question at all. A hull standing on the yard
// needs no route to it, so an unreadable gate graph must not suppress the one errand that is free.
func TestReconcile_AStandingHullIsPricedEvenWhenTheGateGraphIsUnreadable(t *testing.T) {
	errand := &recordingErrandPort{
		hulls:    []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-QR78-AE4F")},
		reachErr: errors.New("gate adjacency store down"),
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 0)}}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(errand.pricedInPlace) != 1 || errand.pricedInPlace[0] != "X1-QR78-AE4F" {
		t.Fatalf("a hull standing on the yard needs no route to it — the free read must survive a blind gate graph, got %v", errand.pricedInPlace)
	}
}

// B9 (sixth half) — a failed in-place read is a WAIT, never a fatal, and never a fallback into
// flying a second hull to a waypoint one of ours is already standing on.
func TestReconcile_InPlaceReadFailure_IsARetryAndFliesNothing(t *testing.T) {
	errand := &recordingErrandPort{
		hulls:    []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-QR78-AE4F"), spareParkedProbe("PROBE-B", "X1-HOME-A1")},
		priceErr: errors.New("shipyard listings were not read live"),
	}
	h, _ := errandHandler(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 0)}}, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	logger := runErrandTick(t, h, errandCmd())

	// The tick must have REACHED the read and been refused by it. Without this the assertion below
	// would also hold for a tick that declined before ever looking — which is what the old
	// standing-counts-as-in-flight rule did, and it is the state this bead exists to end.
	if !logger.sawAction("autosizer_heavy_pricing_in_place_read_failed") {
		t.Fatalf("the tick must have attempted the free read and reported the refusal. Lines seen:\n%s", logger.joined())
	}
	if len(errand.sent) != 0 {
		t.Fatalf("a declined read is retried next tick, never escalated into a flight to a yard we already occupy, got %+v", errand.sent)
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
			// IN TRANSIT, not standing: a hull that has ARRIVED is no longer a wait, it is the
			// free errand, and it leaves through the dispatch path rather than this one.
			name:    "an errand is already under way",
			reason:  pricingErrandAlreadyInFlight,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand: &recordingErrandPort{hulls: []PricingErrandHull{
				{Symbol: "PROBE-A", Fleet: parkedsensing.SensingParkedFleetTag, Location: "X1-QR78-AE4F", Idle: true, InTransit: true},
				spareParkedProbe("PROBE-B", "X1-HOME-A1"),
			}},
		},
		{
			name:    "no carrier can name a route to any unpriced yard",
			reason:  pricingErrandNoRoutableYard,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand: &recordingErrandPort{
				hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")},
				reach: map[string]map[string]int{"X1-HOME": {}},
			},
		},
		{
			name:    "the stored gate adjacency cannot be read",
			reason:  pricingErrandReachUnreadable,
			catalog: &fakeHeavyYardCatalog{yards: unpriced},
			errand: &recordingErrandPort{
				hulls:    []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-HOME-A1")},
				reachErr: errors.New("gate adjacency store down"),
			},
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
		t.Fatalf("the %d decline cases produced only %d distinct reasons — collapsing two states into one reason "+
			"re-creates the ambiguity this test exists to prevent", len(cases), len(seen))
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

// B8 — EXACTLY ONE COORDINATOR DRIVES THE ERRAND, and it is the fleet's heavy BUYER.
//
// The errand's whole policy — one hull in flight fleet-wide, one spare probe, never a manned one —
// is stated once and derived from ship rows every tick. Two coordinators holding the ports would
// each read the same durable facts, each conclude nothing was in flight, and each dispatch: the
// bound of one becomes a bound of one PER DRIVER. So ownership is asserted structurally rather than
// left to the reader of two tick loops.
//
// The second driver this once guarded against was the fleet autosizer, deleted in sp-5pclx — which
// is why this now asserts only the positive half. The negative half is enforced by the compiler: a
// type that does not exist cannot hold a port.
func TestPricingErrand_HasExactlyOneDriver(t *testing.T) {
	for _, port := range []string{"heavyYardCatalog", "heavyErrand"} {
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
