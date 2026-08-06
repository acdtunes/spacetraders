package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// errAllDeliveriesFail models the delivery that never lands — the whole reason a paid-for load
// sits in a hold long enough to be bought again.
var errAllDeliveriesFail = errors.New("supply rejected: hull never reached the site")

// sp-v2a2h — GATE MATERIAL BOUGHT TWICE BECAUSE THE FIRST LOAD WAS STILL IN A HOLD.
//
// THE LEDGER, and it is unambiguous. ADVANCED_CIRCUITRY against a 400-unit requirement:
//
//	20:47:26Z  +28u  running=392
//	20:47:27Z  + 8u  running=400   <- correct, stopped exactly on the requirement
//	23:17:15Z  +28u  running=428
//	23:17:15Z  + 8u  running=436   <- the SAME 36 units, bought again
//
// TORWINDSTG-B carried the first 36 for two and a half hours without delivering, so the site
// still read 364/400 and the coordinator sized 400-364 = 36 a second time. 144,464 credits, and
// 36 surplus units the site will refuse outright (API 4801).
//
// THE IDENTICAL 28+8 SPLIT IN BOTH BUYS IS THE PROOF, and it names the code path: that is
// gate.PlanFill's own tranche arithmetic (take=36 against trade_volume 28 -> ceil = 2
// transactions), so the second buy came through deliverGateLeg, sized off the RAW bill.
//
// THE DRAIN ALREADY HAD IN-FLIGHT ACCOUNTING. materialBuyBudgets nets supplyWorkers.reserved out
// of the bill. It failed three ways and each is covered below:
//
//  1. RESTART-WIPE — supplyWorkers is in-process only. The daemon restarted at 23:05:47Z; the
//     duplicate landed 11m28s later with an empty registry.
//  2. TIMEOUT-EXPIRY — the reservation carries its own clock (task deadline + reap grace, 90m+10m
//     for a depth-3 material), so it had already lapsed ~an hour BEFORE the second buy while the
//     hull was still stranded and still laden.
//  3. THE GATE LEG NEVER CONSULTED IT AT ALL, and a delivery trip is MIXED — it buys every
//     pipeline material, not just its lot's — so even a perfectly durable reservation would have
//     been bypassed.
//
// These tests drive the drain's own seams. Nothing here asserts on spend alone: a fill the money
// guards happen to refuse and a fill that was never planned are the same zero in `bought` and
// completely different bugs, so the double-buy assertions read the buyer's CALL COUNT.

// ---------------------------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------------------------

// inTransitLadenHull is the hull at the centre of the incident: a gate-delivery hauler that BOUGHT
// its load and is still carrying it, mid-flight and therefore invisible to hauler discovery
// (FindIdleShipsByFleet drops NavStatusInTransit). Its cargo is the only durable record that those
// units are already paid for.
func inTransitLadenHull(t *testing.T, symbol, good string, units int) *navigation.Ship {
	t.Helper()
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	cargo, err := shared.NewCargo(gateTestHoldCapacity, units, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(gateTestWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, gateTestHoldCapacity, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInTransit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	ship.SetDedicatedFleet(gate.DeliveryFleetTag)
	return ship
}

// fleet seeds the ship repository the commitment fold reads. The fold derives from the PERSISTED
// fleet, not from the tick's idle pool, which is the whole point: a stranded hull is invisible to
// discovery and must still be counted.
func (f *gateLegFixture) fleet(ships ...*navigation.Ship) {
	f.shipRepo.mu.Lock()
	defer f.shipRepo.mu.Unlock()
	f.shipRepo.ships = append(f.shipRepo.ships, ships...)
}

// loadAboard puts units of good into a hull's hold — what a successful buy leaves behind when the
// delivery that was supposed to follow it never lands.
func loadAboard(t *testing.T, ship *navigation.Ship, good string, units int) {
	t.Helper()
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	cargo, err := shared.NewCargo(gateTestHoldCapacity, units, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship.SetCargo(cargo)
}

// runWith drives ONE delivery leg on an explicit hull, which the fixture's own run() cannot do —
// these tests need the acting hull to also be a member of the persisted fleet, so the leg's
// exclusion of its own commitment is actually exercised rather than trivially true.
func (f *gateLegFixture) runWith(t *testing.T, ship *navigation.Ship) bool {
	t.Helper()
	return f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateTestLot(t, ship), shared.MustNewPlayerID(1))
}

// incidentFixture stages the exact mid-build state of the ledger above: ADVANCED_CIRCUITRY at
// 364/400 (36 outstanding), FAB_MATS still wide open so the trip is MIXED — which is the detail
// that makes the bug reachable at all, since the lot that re-bought the circuitry was not
// dispatched for it.
func incidentFixture(t *testing.T) *gateLegFixture {
	t.Helper()
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.pipeline.setBill(gateMaterialSecondary, 400)
	f.pipeline.setDelivered(gateMaterialSecondary, 364)
	f.pipeline.setBill(gateMaterialPrimary, 1600)
	f.pipeline.setDelivered(gateMaterialPrimary, 1141)
	return f
}

// ---------------------------------------------------------------------------------------------
// ACCEPTANCE 1 + 3 — remaining need is net of in-flight, and re-evaluating buys nothing more
// ---------------------------------------------------------------------------------------------

// THE HEADLINE REGRESSION. The site says 364/400, one hull is already carrying the outstanding 36,
// and the leg must buy ZERO of them — while still buying the material that genuinely needs it.
//
// The second clause is not decoration. A guard that simply refused the whole trip would pass a
// "bought no circuitry" assertion and stall the gate, so the FAB_MATS leg is asserted alongside:
// the fix must net one material without starving the other.
func TestGateLeg_DoesNotRebuyUnitsAlreadyAboardAStrandedHull(t *testing.T) {
	f := incidentFixture(t)
	// FAB_MATS is left OPEN but small (20 of 1600 outstanding), so the trip is genuinely MIXED —
	// which is the mechanism — while still leaving hold for the circuitry.
	//
	// THE SIZE IS A CALIBRATION, NOT A DETAIL. With FAB_MATS wide open it takes the whole 80-unit
	// hold, the circuitry is reached at capacityLeft 0 and skipped as hold_full, and this test
	// goes green against a drain with NO in-flight accounting at all — a mutation probe caught
	// exactly that. A guard is only under test when the hull could otherwise have bought.
	f.pipeline.setDelivered(gateMaterialPrimary, 1580)
	f.fleet(inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 36))

	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	if strings.Contains(f.logLines(), gateMaterialSecondary+": "+gate.SkipHoldFull) {
		t.Fatalf("the trip ran out of hold before it reached %s, so this test could not have detected a re-purchase; the trip log is:\n%s", gateMaterialSecondary, f.logLines())
	}
	if got := f.buyer.attemptsFor(gateMaterialSecondary); got != 0 {
		t.Fatalf("the leg attempted %d purchase(s) of %s against a 36-unit bill that a stranded hull is ALREADY carrying, want 0 — this is the sp-v2a2h double-buy: 144,464 credits for cargo the site rejects with API 4801", got, gateMaterialSecondary)
	}
	if got := f.buyer.goods()[gateMaterialSecondary]; got != 0 {
		t.Fatalf("bought %d %s that were already paid for and in a hold, want 0", got, gateMaterialSecondary)
	}
	if got := f.buyer.attemptsFor(gateMaterialPrimary); got == 0 {
		t.Fatalf("the leg bought no %s either — netting out one material's in-flight load must not stall the whole trip, or the fix trades a double-buy for a stalled gate", gateMaterialPrimary)
	}
}

// The netting is ARITHMETIC, not a boolean. A partially covered bill must still buy the
// UNCOVERED part: covering 20 of 36 outstanding units means buying exactly 16, not 36 (the bug)
// and not 0 (a fix that over-corrects into a stall).
func TestGateLeg_BuysOnlyTheUncoveredRemainder(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141) // close FAB_MATS so only the circuitry is in play
	f.fleet(inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 20))

	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	if got := f.buyer.goods()[gateMaterialSecondary]; got != 16 {
		t.Fatalf("bought %d %s against 36 outstanding with 20 already in a hold, want exactly 16 — a bill netted by the wrong amount either re-buys (36) or stalls the gate (0)", got, gateMaterialSecondary)
	}
}

// ACCEPTANCE 3, literally: two sizing passes with NO delivery in between. The first buys the
// shortfall; the second must buy nothing, because the first load is now in a hold. This is the
// exact shape of the ledger — two identical 28+8 buys, hours apart, nothing delivered between.
func TestGateLeg_ASecondPassWithNoDeliveryInBetweenBuysNothing(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141)

	// PASS 1: nothing in flight, so the shortfall is bought — and, because the delivery does not
	// land, the units end up riding in the hull rather than moving the site's counter.
	first := gateTestHull(t, "TORWINDSTG-B", gate.DeliveryFleetTag)
	f.fleet(first)
	f.producer.deliverErr = errAllDeliveriesFail
	f.runWith(t, first)
	if got := f.buyer.goods()[gateMaterialSecondary]; got != 36 {
		t.Fatalf("the first pass bought %d %s, want 36 — the fixture is not creating the condition under test", got, gateMaterialSecondary)
	}
	loadAboard(t, first, gateMaterialSecondary, 36)

	// PASS 2: same site state (the delivery never landed), same outstanding 36 — but they are now
	// paid for and aboard.
	f.buyer.reset()
	f.producer.deliverErr = nil
	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	if got := f.buyer.attemptsFor(gateMaterialSecondary); got != 0 {
		t.Fatalf("re-evaluating with nothing delivered in between attempted %d more purchase(s) of %s, want 0 — the sizing pass is not idempotent and every re-evaluation re-buys the same shortfall", got, gateMaterialSecondary)
	}
}

// ---------------------------------------------------------------------------------------------
// ACCEPTANCE 2 — a met requirement is never purchased, asserted on the CALL COUNT
// ---------------------------------------------------------------------------------------------

func TestGateLeg_NeverAttemptsAPurchaseForAMetRequirement(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setDelivered(gateMaterialSecondary, 400) // 400/400 — met
	f.pipeline.setBill(gateMaterialPrimary, 1141)       // and nothing else to buy

	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	if got := f.buyer.calls(); got != 0 {
		t.Fatalf("the leg made %d buyer call(s) with every requirement met, want 0 — asserting on spend alone would have missed this, since a refused fill and an unplanned one are the same zero", got)
	}
	if !strings.Contains(f.logLines(), gate.SkipBillSatisfied) {
		t.Fatalf("a met requirement was not reported as %q; the trip log is:\n%s", gate.SkipBillSatisfied, f.logLines())
	}
}

// ---------------------------------------------------------------------------------------------
// ACCEPTANCE 4 — a daemon RESTART must not resurrect the double-buy
// ---------------------------------------------------------------------------------------------

// THIS IS THE CRITERION THAT NAMES THE ACTUAL DEFECT. supplyWorkers is documented "in-process
// only: a restart loses the workers and this registry together", and the staging daemon restarted
// 11m28s before the duplicate buy. Any accounting that lives only in that registry is wiped while
// the cargo it stood for is still physically in a hold.
//
// The restart is modelled the way production performs one: a BRAND NEW handler over the SAME
// persisted state, with an empty worker registry and no reservation to inherit. The commitment
// must survive anyway, because it is re-derived from the hull's cargo.
func TestGateLeg_ADaemonRestartDoesNotResurrectTheDoubleBuy(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141)
	stranded := inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 36)
	f.fleet(stranded)

	// A worker of the PRE-restart process holds the stranded hull and its buy reservation.
	if _, admitted := f.handler.supplies.admit(stranded.ShipSymbol(), "C-1", pipelineMaterialKey(gateTestPipelineID, gateMaterialSecondary), 36, f.handler.clock.Now().Add(time.Hour)); !admitted {
		t.Fatal("could not register the pre-restart worker this test depends on")
	}

	// RESTART. A new handler over the same repositories: the registry is gone, ReleaseAllActive
	// has returned the claims, and nothing in memory remembers the 36 units.
	restarted := NewRunConstructionCoordinatorHandler(
		f.taskRepo, f.pipeline, f.shipRepo, f.producer,
		staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{},
	)
	restarted.SetGateDelivery(f.topo, f.buyer)
	if got := restarted.supplies.reservedUnits(pipelineMaterialKey(gateTestPipelineID, gateMaterialSecondary)); got != 0 {
		t.Fatalf("the restarted handler inherited a %d-unit reservation, want 0 — the fixture is not modelling a restart, so this test would pass on in-memory state the real restart destroys", got)
	}

	restarted.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateTestLot(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag)), shared.MustNewPlayerID(1))

	if got := f.buyer.attemptsFor(gateMaterialSecondary); got != 0 {
		t.Fatalf("after a restart the drain attempted %d purchase(s) of %s that a stranded hull is still carrying, want 0 — in-flight accounting is depending on in-memory-only state (sp-v2a2h acceptance 4)", got, gateMaterialSecondary)
	}
}

// The reservation's OWN CLOCK is the second wipe, and it needs no restart at all: the registration
// expires at the worker's task deadline plus a cleanup grace, and a hull stranded past that is
// released while still holding every unit it bought. A longer timeout only widens the window.
//
// Modelled by the state that expiry LEAVES BEHIND — a laden hull with no registration — which is
// indistinguishable from the post-restart state and must be netted from the cargo either way.
func TestGateLeg_ALapsedReservationStillCountsTheCargoItStoodFor(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141)
	stranded := inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 36)
	f.fleet(stranded)

	if f.handler.supplies.holds(stranded.ShipSymbol()) {
		t.Fatal("the stranded hull still has a live registration — this test must run with the reservation already reaped")
	}

	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	if got := f.buyer.attemptsFor(gateMaterialSecondary); got != 0 {
		t.Fatalf("a hull whose reservation had lapsed but whose hold is still full drew %d re-purchase(s), want 0 — the accounting is keyed to the registration's clock rather than to the cargo", got)
	}
}

// ---------------------------------------------------------------------------------------------
// The leg's OWN hull is excluded — the direction that stalls rather than overspends
// ---------------------------------------------------------------------------------------------

// A leg flushes its own cargo into the bill BEFORE it sizes anything, so counting that same cargo
// as a commitment would net it out twice and refuse a purchase the gate genuinely needs. The
// exclusion is by SYMBOL and not by subtracting the hull's cargo, because the cached *Ship the leg
// holds is deliberately not updated by DeliverToConstructionSite — subtracting a PRE-flush figure
// from a POST-flush fold under-counts, which is the OVER-buying direction.
func TestGateLeg_ExcludesItsOwnHullFromTheCommitment(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setDelivered(gateMaterialSecondary, 400) // close the circuitry; only FAB_MATS is in play
	f.pipeline.setBill(gateMaterialPrimary, 1600)
	f.pipeline.setDelivered(gateMaterialPrimary, 1564) // 36 outstanding, as in the ledger
	f.producer.delivered = 20                          // the flush accepts exactly what is aboard

	acting := gateTestLadenHull(t, "TORWINDSTG-A", gateMaterialPrimary, 20)
	f.fleet(acting)

	f.runWith(t, acting)

	// Flushed its own 20 of the 36 outstanding, so 16 remain and nothing else is committed. Count
	// the hull's own load as a foreign commitment and the leg buys 0 instead — a stalled gate,
	// which is the direction an over-eager netting fails in.
	if got := f.buyer.goods()[gateMaterialPrimary]; got != 16 {
		t.Fatalf("a hull that flushed its own 20 units then bought %d %s, want 16 — its own load was counted as a foreign commitment and netted out twice, stalling the gate", got, gateMaterialPrimary)
	}
}

// The MIXED-trip half of the same hazard, and it fails the other way. Only the LOT'S OWN material
// has its flush recorded into the pipeline counter, so for a second material the bill this leg
// reads is stale by exactly what the flush just unloaded — and a leg that sizes against it buys
// those units again. That is sp-v2a2h's own mechanism at the width of a single leg.
func TestGateLeg_DoesNotRebuyASecondMaterialItJustFlushed(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141) // close FAB_MATS: the lot's own material is done
	f.producer.delivered = 36                     // the flush lands the whole outstanding circuitry bill

	// The hull carries the OTHER material — 36 ADVANCED_CIRCUITRY against a 36-unit bill — while
	// its lot is a FAB_MATS lot, so nothing records the flush into the circuitry counter.
	acting := gateTestLadenHull(t, "TORWINDSTG-A", gateMaterialSecondary, 36)
	f.fleet(acting)

	f.runWith(t, acting)

	if got := f.buyer.attemptsFor(gateMaterialSecondary); got != 0 {
		t.Fatalf("the leg flushed 36 %s and then attempted %d purchase(s) of the same bill, want 0 — a second material's delivery is not recorded into the counter this leg reads, so it sized against a bill it had just met", gateMaterialSecondary, got)
	}
}

// ---------------------------------------------------------------------------------------------
// The DISPATCH PLANNER side, and the deadlock a naive netting creates there
// ---------------------------------------------------------------------------------------------

// commitmentBudget stages a one-material budget over an explicit fleet and idle pool.
func commitmentBudget(t *testing.T, target, delivered int, fleet []*navigation.Ship, idle []*navigation.Ship) (*materialBudget, string) {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.ReconstructConstructionMaterialTarget(gateMaterialSecondary, target, delivered)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)
	handler := NewRunConstructionCoordinatorHandler(
		&drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}},
		&drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}},
		newDrainShipRepo(fleet...), &fakeConstructionProducer{}, nil, &factoryFakeClock{},
	)
	// gateTestSystem, matching where the gate fixtures place their hulls: the commitment fold is
	// system-scoped, so a mismatch here would count NOTHING and every assertion below would be
	// measuring the scoping rather than the netting.
	budget := handler.materialBuyBudgets(t.Context(), gateTestCmd(), gateTestSystem, []*manufacturing.ManufacturingTask{task}, idle, gateTestHoldCapacity)
	return budget, materialKey(task)
}

// The system scope must reject on POSITIVE evidence. A gate hull parked in another system is
// genuinely not going to deliver here, and counting it would stall this gate; a hull whose
// location cannot be read has not been shown to be anywhere, and dropping it licenses a second
// purchase for cargo that may well be aboard. The two must not be conflated.
func TestCommitment_CountsAHullWhoseSystemCannotBeRead(t *testing.T) {
	unlocatable := inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 36)
	if outOfSystem(unlocatable, "") {
		t.Fatal("a hull is being called out-of-system against an UNSET system filter")
	}
	elsewhere := inTransitLadenHull(t, "TORWINDSTG-C", gateMaterialSecondary, 36)
	if !outOfSystem(elsewhere, "X1-OTHER") {
		t.Fatal("a hull positively located in another system is not being excluded, which would stall this gate on cargo that will never arrive")
	}
}

// The planner must not mint a BUYING lot for a material whose outstanding bill is entirely aboard
// a hull it cannot dispatch — that is the same double-buy one layer up.
func TestDispatchBudget_AFullyCommittedMaterialHasNoBuyBudget(t *testing.T) {
	stranded := inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 36)
	budget, key := commitmentBudget(t, 400, 364, []*navigation.Ship{stranded}, nil)

	if got := budget.remaining[key]; got > 0 {
		t.Fatalf("the buy budget is %d with all 36 outstanding units aboard a stranded hull, want <= 0 — the planner would size a second purchase of units already paid for", got)
	}
	if got := budget.rawRemaining[key]; got != 36 {
		t.Fatalf("the site's DEMAND reads %d, want 36 — netting the buy budget must not erase the demand, or a hull carrying wanted cargo is declared to be carrying nothing", got)
	}
}

// THE DEADLOCK THIS FIX MUST NOT CREATE, and it is the reason lot minting reads a different number
// from buy sizing.
//
// Net every commitment out of the LOT count and a hull sitting idle with the final tranche aboard
// makes its own material want zero lots: no lot is minted, so the hull is never dispatched, so the
// cargo is never unloaded, so the bill never closes — the gate stalls one tranche from done, with
// the units it needs already bought and parked in the fleet. Unloading spends nothing, so a zero
// BUY budget must never withhold the hull that carries the load in.
func TestDispatchBudget_ALadenIdleHullStillDrawsALotWhenItsBillIsFullyCovered(t *testing.T) {
	laden := gateTestLadenHull(t, "TORWINDSTG-A", gateMaterialSecondary, 36)
	budget, key := commitmentBudget(t, 400, 364, []*navigation.Ship{laden}, []*navigation.Ship{laden})

	if got := budget.remaining[key]; got > 0 {
		t.Fatalf("the buy budget is %d though the idle hull already carries the whole outstanding bill, want <= 0", got)
	}
	if !budget.wantsAnotherLot(key) {
		t.Fatal("a material whose last tranche is sitting in an IDLE hull wants no lot — nothing will ever dispatch that hull, so the cargo is never unloaded and the gate stalls one tranche from done with the units already bought")
	}
}

// The same laden hull must ALSO be reachable through the real planner, not only through the
// budget's own predicate: wantsAnotherLot is one of three gates (the lot ceiling and the total lot
// demand are the others) and a fix that satisfies only this one still mints zero lots.
func TestPlanDispatchLots_MintsAnUnloadLotForAFullyCoveredMaterial(t *testing.T) {
	laden := gateTestLadenHull(t, "TORWINDSTG-A", gateMaterialSecondary, 36)
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.ReconstructConstructionMaterialTarget(gateMaterialSecondary, 400, 364)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)
	handler := NewRunConstructionCoordinatorHandler(
		&drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}},
		&drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}},
		newDrainShipRepo(laden), &fakeConstructionProducer{}, nil, &factoryFakeClock{},
	)

	lots := handler.planDispatchLots(t.Context(), gateTestCmd(), gateTestSystem, []*manufacturing.ManufacturingTask{task}, []*navigation.Ship{laden}, 5)

	if len(lots) != 1 {
		t.Fatalf("the planner minted %d lot(s) for a material whose last tranche is aboard an idle hull, want 1 — the hull is never dispatched, the cargo never unloads, and the gate stalls with the units already paid for", len(lots))
	}
	if lots[0].mayBuy() {
		t.Fatalf("the unload lot may still BUY (fillCap=%d) — it would re-purchase the very units it is carrying", lots[0].fillCap)
	}
}

// A lot minted purely to unload must be marked so. A zero fillCap already means "no cap", so the
// planner's "buy nothing" has to be distinguishable from it or the shared path hands an UNCAPPED
// fill to precisely the lot that must not spend — the same double-buy, re-entered.
func TestDispatchBudget_AnUnloadOnlyLotIsMarkedAsBuyingNothing(t *testing.T) {
	laden := gateTestLadenHull(t, "TORWINDSTG-A", gateMaterialSecondary, 36)
	budget, key := commitmentBudget(t, 400, 364, []*navigation.Ship{laden}, []*navigation.Ship{laden})

	lots := []constructionLot{{task: budget.repTask[key], ship: laden}}
	budget.assignFillCaps(lots)

	if lots[0].mayBuy() {
		t.Fatalf("a lot with a 0-unit buy budget reports it MAY buy (fillCap=%d, buyCapped=%t) — read as the zero value that means 'no cap', it fills a whole hold against a bill that is already paid for", lots[0].fillCap, lots[0].buyCapped)
	}
	if got := lots[0].buyReservation(); got != 0 {
		t.Fatalf("an unload-only lot reserved %d units of buy budget, want 0 — it would suppress a purchase some other hull genuinely needs to make", got)
	}
}

// ---------------------------------------------------------------------------------------------
// ACCEPTANCE 6 — the skip and the overshoot are both visible
// ---------------------------------------------------------------------------------------------

func TestGateLeg_ReportsTheInFlightSkipAndTheOvershoot(t *testing.T) {
	f := incidentFixture(t)
	f.pipeline.setBill(gateMaterialPrimary, 1141)
	// 80 units aboard against 36 outstanding: covered AND over-bought — the 436-vs-400 state.
	f.fleet(inTransitLadenHull(t, "TORWINDSTG-B", gateMaterialSecondary, 80))

	f.runWith(t, gateTestHull(t, "TORWINDSTG-A", gate.DeliveryFleetTag))

	logs := f.logLines()
	if !strings.Contains(logs, "already paid for") {
		t.Fatalf("a purchase declined for in-flight coverage said nothing an operator could see; the log is:\n%s", logs)
	}
	if !strings.Contains(logs, "OVER-BOUGHT by 44") {
		t.Fatalf("80 units held against a 36-unit requirement raised no overshoot warning — the 436-vs-400 overshoot was recoverable only by summing the ledger by hand, which is what acceptance 6 exists to end; the log is:\n%s", logs)
	}
	if !strings.Contains(logs, gate.SkipInFlightCovered) {
		t.Fatalf("the trip did not report %q — a covered material and a finished one must not render the same; the log is:\n%s", gate.SkipInFlightCovered, f.logLines())
	}
}
