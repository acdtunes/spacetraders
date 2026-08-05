package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ROLE REALLOCATION. Two decisions this task settles and both are load-bearing:
//
//  1. LEGACY HULLS ARE ADOPTED. On a fleet already holding four "manufacturing" hulls the
//     purchase ramp buys nothing (obs.GateWorkers counts all three tags and already equals
//     gateWorkerTarget), so no role tag is ever written and phases 1-3 are entirely inert.
//     Adopting them costs an AssignFleet write and no purchase.
//
//  2. CLAIMS ARE NEVER MOVED. AssignFleet does not evict a holder by design, and
//     constructionLot.claimIdentity is FROZEN at plan time — so a hull re-tagged in flight
//     presents a stale tag and ClaimShip rejects it. Only idle, unheld hulls are moved.
//
// THE DWELL LEDGER IS THE HAZARD NO TEST IN THE PLANNER'S PACKAGE CAN CATCH. gate.Worker
// .LastMovedByUs is the only field not derivable from one read of the ship: a caller that rebuilds
// Workers straight from the DB each tick leaves it zero forever, every hull reads "never moved by
// us", and the dwell guard is INERT — while looking healthy, because an inert dwell produces MORE
// role changes, which reads as a busy fleet rather than a broken guard. The alarm is
// ReallocationPlan.DwellRecords, rendered as "dwell records N/M";
// TestReallocateGateRoles_KeepsTheDwellLedgerAcrossTicksSoTheGuardIsNotInert pins it.

// gateReallocFixture wires both roles and seeds a live gate fleet.
//
// The crew is an ORDERED slice, never a map: FindAllByPlayer hands the planner its candidates in
// this order, and the planner's move cap means the tick's one move goes to the first eligible hull
// it sees. A map fixture would randomize that and make an order-sensitive assertion flake.
type gateReallocFixture struct {
	*gateFeedFixture
	ships []*navigation.Ship
}

// gateCrewMember is one seeded hull: its symbol and the dedicated_fleet tag it starts on.
type gateCrewMember struct{ ship, fleet string }

func newGateReallocHandler(t *testing.T, crew ...gateCrewMember) *gateReallocFixture {
	t.Helper()
	ships := make([]*navigation.Ship, 0, len(crew))
	for _, member := range crew {
		ships = append(ships, gateTestHull(t, member.ship, member.fleet))
	}
	return newGateReallocHandlerWithHulls(t, ships...)
}

// newGateReallocHandlerWithHulls seeds the fleet from caller-built hulls, for a test that must stage
// a HOLD as well as a tag. gateCrewMember can only express the tag.
func newGateReallocHandlerWithHulls(t *testing.T, ships ...*navigation.Ship) *gateReallocFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	f.shipRepo.mu.Lock()
	f.shipRepo.ships = ships
	f.shipRepo.mu.Unlock()
	return &gateReallocFixture{gateFeedFixture: f, ships: ships}
}

// pauseEveryMaterial drives the SAME policy object the delivery legs write to, so the trigger
// under test is the state production actually produces rather than a fabricated flag.
func (f *gateReallocFixture) pauseEveryMaterial() {
	policy := f.handler.gate.policyFor("", "")
	for _, good := range []string{gateMaterialPrimary, gateMaterialSecondary} {
		policy.Decide(good, good+"-EXPORTER", shared.SupplyLevelScarce)
	}
}

// resumeEveryMaterial clears the pause through the same policy object, at the RESUME floor (the
// hysteresis will not lift on the buy floor alone).
func (f *gateReallocFixture) resumeEveryMaterial() {
	policy := f.handler.gate.policyFor("", "")
	for _, good := range []string{gateMaterialPrimary, gateMaterialSecondary} {
		policy.Decide(good, good+"-EXPORTER", shared.SupplyLevelAbundant)
	}
}

func (f *gateReallocFixture) reallocate(t *testing.T) {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)
	f.handler.reallocateGateRoles(f.ctx(), gateTestSystem, []*manufacturing.ManufacturingTask{task}, shared.MustNewPlayerID(1))
}

// assignments is every AssignFleet write the reallocator made, in order.
func (f *gateReallocFixture) assignments() []fleetAssignment {
	f.shipRepo.mu.Lock()
	defer f.shipRepo.mu.Unlock()
	return append([]fleetAssignment(nil), f.shipRepo.assigned...)
}

// ruled reports whether the reallocator reached its ruling at all. Every negative test needs it:
// "nobody moved" is satisfied by a reallocator that returned on line one, which is the exact
// do-nothing implementation these tests exist to reject.
func (f *gateReallocFixture) ruled() bool {
	return strings.Contains(f.logLines(), "Gate roles (delivery ")
}

// THE TRIGGER. Delivery hulls move to the factory role only when EVERY gate material is paused.
func TestReallocateGateRoles_MovesDeliveryHullsToFactoryWhenEveryMaterialIsPaused(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"D-2", gate.DeliveryFleetTag},
		gateCrewMember{"F-1", gate.FactoryFleetTag}, gateCrewMember{"F-2", gate.FactoryFleetTag},
	)
	f.pauseEveryMaterial()

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("a fully paused delivery fleet moved nobody.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if w.fleet != gate.FactoryFleetTag {
			t.Fatalf("wrote fleet %q for %s; a paused fleet moves TOWARD the factory role", w.fleet, w.ship)
		}
		if !strings.HasPrefix(w.ship, "D-") {
			t.Fatalf("re-tagged %s, which is already a factory hull", w.ship)
		}
	}
	// The PAUSE is what the log must name, not just the move: an operator reading "delivery
	// running" beside a move to the factory role has been told two contradictory things.
	if !strings.Contains(f.logLines(), "delivery PAUSED") {
		t.Fatalf("the ruling does not report the fleet as paused, so the move has no stated cause:\n%s", f.logLines())
	}
}

// EVERY, NOT ANY. One material still buyable leaves delivery real work — a hull fills greedily
// from whatever is eligible — so moving workers then would starve capacity delivery can use.
func TestReallocateGateRoles_DoesNotMoveWhenOnlyOneMaterialIsPaused(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"D-2", gate.DeliveryFleetTag},
		gateCrewMember{"F-1", gate.FactoryFleetTag}, gateCrewMember{"F-2", gate.FactoryFleetTag},
	)
	policy := f.handler.gate.policyFor("", "")
	policy.Decide(gateMaterialPrimary, gateMaterialPrimary+"-EXPORTER", shared.SupplyLevelScarce)
	policy.Decide(gateMaterialSecondary, gateMaterialSecondary+"-EXPORTER", shared.SupplyLevelHigh)

	f.reallocate(t)

	// NON-VACUITY FIRST. len(writes) == 0 is satisfied by a reallocator that never ran, never read
	// the fleet, or read an empty one — none of which says anything about EVERY-vs-ANY. The ruling
	// line proves the census was taken and the trigger was evaluated.
	if !f.ruled() {
		t.Fatalf("the reallocator never reached a ruling, so 'moved nobody' says nothing about the EVERY rule:\n%s", f.logLines())
	}
	if !strings.Contains(f.logLines(), "delivery running") {
		t.Fatalf("with one material still buyable the fleet is NOT paused; the ruling says otherwise:\n%s", f.logLines())
	}
	if writes := f.assignments(); len(writes) != 0 {
		t.Fatalf("moved %+v with ONE material still buyable; that starves delivery of capacity it can still use", writes)
	}
}

// A MET BILL IS NOT A PAUSE. A completed material is never decided on, so its pause state stays
// false forever — an unfiltered FleetPaused would then read false exactly when the OTHER material
// is starved, which is when reallocation matters most.
func TestReallocateGateRoles_IgnoresMaterialsWhoseBillIsAlreadyMet(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"F-1", gate.FactoryFleetTag},
	)
	f.pipeline.setBill(gateMaterialSecondary, 0) // its bill is closed; no leg will ever decide on it
	f.handler.gate.policyFor("", "").Decide(gateMaterialPrimary, gateMaterialPrimary+"-EXPORTER", shared.SupplyLevelScarce)

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("a fleet whose ONLY outstanding material is paused moved nobody — a met bill is being counted as an un-paused material.\nlog:\n%s", f.logLines())
	}
	if writes[0].ship != "D-1" || writes[0].fleet != gate.FactoryFleetTag {
		t.Fatalf("wrote %+v; the paused fleet's one delivery hull is what moves, and it moves to the factory role", writes[0])
	}
}

// THE ARMING FIX. Four legacy hulls, no purchase possible, no role tag in existence. The
// reallocator must adopt them, or every phase of this design is dead code on this fleet.
func TestReallocateGateRoles_AdoptsLegacyManufacturingHulls(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"L-1", gate.LegacyFleetTag}, gateCrewMember{"L-2", gate.LegacyFleetTag},
		gateCrewMember{"L-3", gate.LegacyFleetTag}, gateCrewMember{"L-4", gate.LegacyFleetTag},
	)

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("no legacy hull was adopted; with four of them the ramp buys nothing (GateWorkers == gateWorkerTarget), so no role tag is EVER written and the whole feature is inert.\nlog:\n%s", f.logLines())
	}
	if got, want := writes[0].fleet, gate.DeliveryFleetTag; got != want {
		t.Fatalf("the first adoption wrote %q, want %q — the D/F/F/D order puts delivery first so accumulated stock starts moving immediately", got, want)
	}
	// The whole fleet converges, one hull per tick, and the mix it converges ON is the baseline.
	// Asserting only the first write would pass an implementation that adopts one hull and then
	// stops, which is the arming failure wearing a green test.
	for tick := 0; tick < len(f.ships); tick++ {
		f.reallocate(t)
	}
	tags := map[string]int{}
	for _, ship := range f.ships {
		tags[ship.DedicatedFleet()]++
	}
	if tags[gate.DeliveryFleetTag] != 2 || tags[gate.FactoryFleetTag] != 2 {
		t.Fatalf("after five ticks the fleet is %+v, want 2 delivery + 2 factory — adoption stalled, or it does not converge on the D/F/F/D baseline.\nlog:\n%s", tags, f.logLines())
	}
}

// NARROW BY DESIGN. A drain launched with its own dedicated fleet, and every foreign fleet, are
// outside the census entirely. Re-tagging one would be a poach (RULINGS #7).
func TestReallocateGateRoles_NeverTouchesAForeignOrCustomFleetTag(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"X-1", "contract"}, gateCrewMember{"X-2", "trade"},
		gateCrewMember{"X-3", "gate-alt"}, gateCrewMember{"X-4", ""},
		// A real gate hull, so the tick is CAPABLE of a write: without it "nobody was re-tagged"
		// is also what a reallocator that does nothing at all produces.
		gateCrewMember{"D-1", gate.DeliveryFleetTag},
	)
	f.pauseEveryMaterial()

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("the tick wrote nothing at all, so it cannot show that it declined the foreign hulls SPECIFICALLY.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if strings.HasPrefix(w.ship, "X-") {
			t.Fatalf("re-tagged %s (fleet %q); only the two role tags and the legacy one are gate hulls", w.ship, w.fleet)
		}
	}
	// The planner counts any non-gate hull it is handed as `foreign` and renders the count. This
	// caller filters BEFORE the planner (see gateWorkforce), so a non-zero count here means the
	// census is being fed hulls this operation has no business ruling on — the wiring half of the
	// poach bug, which the planner's own filter would otherwise absorb silently.
	// "+ 0 foreign", not "0 foreign": the latter is a substring of "10 foreign".
	if !strings.Contains(f.logLines(), "+ 0 foreign") {
		t.Fatalf("the census was fed hulls outside the gate workforce; the planner's foreign backstop caught them, but this caller must never hand them over:\n%s", f.logLines())
	}
}

// CLAIMS ARE NEVER MOVED. A hull a supply worker still holds is not idle, and re-tagging it would
// make its lot's FROZEN claim identity stale — ClaimShip would then reject the claim and the hull
// would be dispatched and silently never work.
func TestReallocateGateRoles_NeverMovesAHullASupplyWorkerStillHolds(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"D-2", gate.DeliveryFleetTag},
	)
	f.pauseEveryMaterial()
	// Register D-1 as held, exactly as dispatchSupplyWorkers does before starting a worker.
	if _, admitted := f.handler.supplies.admit("D-1", "C-1", gateMaterialPrimary, 40, f.handler.clock.Now().Add(time.Hour)); !admitted {
		t.Fatal("fixture: could not register the in-flight worker")
	}

	f.reallocate(t)

	// The tick must still have DONE something, or "D-1 was not moved" is the same sentence as
	// "nothing was considered".
	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("the tick moved nobody at all; with D-2 idle and the fleet paused it had a move to make.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if w.ship == "D-1" {
			t.Fatal("re-tagged a hull a live supply worker still holds; its lot's frozen claim identity would then be rejected at the DB")
		}
	}
	// And the hold must be the STATED reason, not an accident of ordering: a held hull that the
	// move cap happened to exclude would satisfy every assertion above.
	if !strings.Contains(f.logLines(), "D-1: "+gate.MoveSkipBusy) {
		t.Fatalf("D-1 was not recorded as held; a hull silently absent from the ruling is indistinguishable from one the move cap skipped:\n%s", f.logLines())
	}
}

// A hull IN TRANSIT is likewise never moved, even with no worker registered — a restart can leave
// a flying hull with no registration behind it.
func TestReallocateGateRoles_NeverMovesAHullInTransit(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"D-2", gate.DeliveryFleetTag},
	)
	f.pauseEveryMaterial()
	for _, ship := range f.ships {
		if ship.ShipSymbol() == "D-1" {
			ship.SetNavStatus(navigation.NavStatusInTransit)
		}
	}
	// NON-VACUITY. IsIdle() reads the ASSIGNMENT, not the nav status, so a hull in transit is idle
	// by that measure alone — which is precisely why the caller must collapse all three facts. If
	// the fixture's hull were already non-idle this test would pass on the wrong term.
	for _, ship := range f.ships {
		if ship.ShipSymbol() == "D-1" && !ship.IsIdle() {
			t.Fatal("fixture is inert: D-1 is already un-idle by assignment, so the in-transit term is never the thing that excludes it")
		}
	}

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("the tick moved nobody at all; with D-2 parked and the fleet paused it had a move to make.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if w.ship == "D-1" {
			t.Fatal("re-tagged a hull in transit — the house rule is never to yank a hull mid-delivery")
		}
	}
	if !strings.Contains(f.logLines(), "D-1: "+gate.MoveSkipBusy) {
		t.Fatalf("the in-transit hull was not recorded as held:\n%s", f.logLines())
	}
}

// THE LEDGER, which is the one thing the planner's own package cannot test. LastMovedByUs is not
// derivable from a ship row: the caller must remember it. A wiring that rebuilds Workers from the
// DB every tick leaves it zero forever and the dwell guard never fires — and the failure LOOKS
// like a busy fleet, not a stalled one, so nothing else surfaces it.
//
// DwellRecords is the alarm. It must read 1/4 on the tick AFTER a move, never 0/4.
func TestReallocateGateRoles_KeepsTheDwellLedgerAcrossTicksSoTheGuardIsNotInert(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"D-1", gate.DeliveryFleetTag}, gateCrewMember{"D-2", gate.DeliveryFleetTag},
		gateCrewMember{"F-1", gate.FactoryFleetTag}, gateCrewMember{"F-2", gate.FactoryFleetTag},
	)
	f.pauseEveryMaterial()

	f.reallocate(t)
	if len(f.assignments()) != 1 {
		t.Fatalf("the first tick made %d move(s), want exactly 1; with no move there is no ledger entry to observe.\nlog:\n%s", len(f.assignments()), f.logLines())
	}
	// The FIRST tick legitimately reads 0/4 — nothing had been moved when its census was taken.
	// Asserting on the whole log would therefore match that line and pass a broken ledger.
	if !strings.Contains(f.logLines(), "dwell records 0/4") {
		t.Fatalf("the first tick's census should hold no ledger entries yet:\n%s", f.logLines())
	}

	before := len(f.logLines())
	f.reallocate(t)

	second := f.logLines()[before:]
	if !strings.Contains(second, "dwell records 1/4") {
		t.Fatalf("the tick after a move reports no ledger entry. gate.Worker.LastMovedByUs is not derivable from a ship row — a wiring that rebuilds Workers from the DB each tick leaves every hull at 'never moved by us' and the dwell guard NEVER FIRES, which shows up as a fleet that churns roles rather than one that stalls.\nsecond tick:\n%s", second)
	}
}

// THE DWELL ITSELF, end to end and in the direction the planner actually gates: the BORROW.
//
// The planner exempts the return to baseline deliberately (a dwell-locked return leaves the fleet
// with zero delivery hulls for the rest of the window), so a test that unpauses and expects the
// just-moved hull to stay put would be asserting against the opposite of the documented rule.
// What the dwell does guarantee is that a hull moved on one tick is not BORROWED to the factory
// role on the next.
func TestReallocateGateRoles_HoldsTheDwellOnABorrowAcrossTicks(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"F-1", gate.FactoryFleetTag}, gateCrewMember{"F-2", gate.FactoryFleetTag},
		gateCrewMember{"F-3", gate.FactoryFleetTag}, gateCrewMember{"D-1", gate.DeliveryFleetTag},
	)
	f.resumeEveryMaterial() // running: the target is the D/F/F/D baseline, so a factory hull returns

	f.reallocate(t)
	moved := f.assignments()
	if len(moved) != 1 || moved[0].fleet != gate.DeliveryFleetTag {
		t.Fatalf("first tick wrote %+v, want exactly one return to the delivery role; without it there is no dwell-stamped hull to observe.\nlog:\n%s", moved, f.logLines())
	}

	// Now PAUSE. The target flips to all-factory, so both delivery hulls are wanted — but the one
	// this process just moved is inside its dwell and must be held back.
	f.pauseEveryMaterial()
	before := len(f.assignments())
	f.reallocate(t)

	borrowed := f.assignments()[before:]
	if len(borrowed) == 0 {
		t.Fatalf("the paused tick borrowed nobody, so the dwell cannot be distinguished from an inert tick.\nlog:\n%s", f.logLines())
	}
	for _, w := range borrowed {
		if w.ship == moved[0].ship {
			t.Fatalf("%s was borrowed to the factory role on the very next tick after this process moved it; the dwell is the workforce-level hysteresis and it is not being applied", w.ship)
		}
	}
	if !strings.Contains(f.logLines(), moved[0].ship+": "+gate.MoveSkipDwell) {
		t.Fatalf("%s was left alone but the dwell is not the recorded reason, so a hull excluded by ordering would read the same:\n%s", moved[0].ship, f.logLines())
	}
}

// OUT OF SYSTEM IS OUT OF SCOPE. Construction legs are in-system; a gate hull parked in another
// system is not this drain's to re-role, and re-tagging it would change which pool it lands in
// for a drain that can actually reach it.
func TestReallocateGateRoles_LeavesGateHullsInAnotherSystemAlone(t *testing.T) {
	f := newGateReallocHandler(t, gateCrewMember{"D-1", gate.DeliveryFleetTag})
	elsewhere := newTestHaulerInFleet(t, "D-FAR", gate.DeliveryFleetTag) // testFactoryWaypoint, not the gate system
	f.shipRepo.mu.Lock()
	f.shipRepo.ships = append(f.shipRepo.ships, elsewhere)
	f.shipRepo.mu.Unlock()
	f.pauseEveryMaterial()

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("the tick moved nobody, so it cannot show that the OUT-OF-SYSTEM hull specifically was left alone.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if w.ship == "D-FAR" {
			t.Fatalf("re-tagged %s, which sits in another system entirely", w.ship)
		}
	}
	// It must be absent from the CENSUS too, not merely unmoved: counted in, it would inflate the
	// target this system's hulls are driven toward.
	if !strings.Contains(f.logLines(), "have 1D/0F") {
		t.Fatalf("the out-of-system hull was counted into the census, which moves the target for the hulls that ARE here:\n%s", f.logLines())
	}
}

// A FAILED WRITE STOPS EARLY and leaves a partial result. A hull left on its old tag is safe —
// it is simply retried a later tick — but continuing past an error would keep writing against a
// repository that just said no.
func TestReallocateGateRoles_StopsOnTheFirstFailedAssignAndSaysSo(t *testing.T) {
	f := newGateReallocHandler(t,
		gateCrewMember{"L-1", gate.LegacyFleetTag}, gateCrewMember{"L-2", gate.LegacyFleetTag},
	)
	f.shipRepo.assignErr = errors.New("row lock timeout")

	f.reallocate(t)

	if !strings.Contains(f.logLines(), "row lock timeout") {
		t.Fatalf("a failed role write is invisible in the log:\n%s", f.logLines())
	}
	// "could not re-tag L-1", not "L-1". The bare symbol is ALREADY in the log by this point: the
	// plan's own LogLine renders every planned move ("L-1 manufacturing->gate-delivery (…)") and is
	// logged BEFORE the write loop runs. Matching on it would pass even if the failure line named no
	// hull at all, which is exactly the claim this assertion exists to make.
	if !strings.Contains(f.logLines(), "could not re-tag L-1") {
		t.Fatalf("the failure does not name the hull it could not re-tag:\n%s", f.logLines())
	}
	// A failed write must NOT stamp the ledger: the role did not change, so the hull is eligible
	// again immediately rather than sitting out a dwell it never earned.
	if !f.handler.roleChangedAt("L-1").IsZero() {
		t.Fatal("a REFUSED role write stamped the dwell ledger; the hull would then sit out a dwell for a move that never happened")
	}
}

// ---------------------------------------------------------------------------------------------
// THE HOLD GATES THE RETURN TO DELIVERY (F22)
// ---------------------------------------------------------------------------------------------
//
// A factory hull that ends a leg EXACTLY full — the state feedGateLeg mints when a successful buy is
// followed by a failed or refused feed, "the cargo stays aboard for the next leg" — is IDLE by every
// measure gateWorkforce has. Returned to delivery it is then declined by wedgedAtFullHold every
// tick (fabrication inputs are never bill materials) and feedGateLegFromHold, the only path that
// empties it, is factory-role-only. The role that can recover it is the role it just left.

// gateTestFullHullTagged is a hull whose hold is EXACTLY full of one good, on the given role tag.
// The tag is a parameter because the scoping is the subject: the same hold must hold a FACTORY hull
// back and must NOT hold a DELIVERY hull back.
func gateTestFullHullTagged(t *testing.T, symbol, fleet, good string) *navigation.Ship {
	t.Helper()
	ship := gateTestHull(t, symbol, fleet)
	item, err := shared.NewCargoItem(good, good, "", gateTestHoldCapacity)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	cargo, err := shared.NewCargo(gateTestHoldCapacity, gateTestHoldCapacity, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("failed to build laden cargo: %v", err)
	}
	ship.SetCargo(cargo)
	return ship
}

// assertUndeliverableFullHold pins BOTH halves of the state under test, which is what every
// assertion below rests on: a full hold, and nothing aboard that the construction bill still wants.
// One unit of slack, or a rename that makes the cargo a gate material, and the hull is an ordinary
// working hull — every test in this block would then pass for a reason unrelated to its subject.
func assertUndeliverableFullHold(t *testing.T, ship *navigation.Ship) {
	t.Helper()
	if cargo := ship.Cargo(); cargo == nil || cargo.Capacity-cargo.Units != 0 {
		t.Fatalf("fixture is inert: %s has free hold, so it is not the wedged hull this block is about", ship.ShipSymbol())
	}
	for _, good := range []string{gateMaterialPrimary, gateMaterialSecondary} {
		if onHandUnits(ship, good) > 0 {
			t.Fatalf("fixture is inert: %s carries %s, which the bill still wants, so the delivery role could unload it and nothing here is wedged", ship.ShipSymbol(), good)
		}
	}
}

// A FACTORY hull carrying cargo only the factory role can unload is NOT returned to delivery, even
// though the baseline wants a delivery hull and nothing else is blocking the move.
func TestReallocateGateRoles_KeepsAFactoryHullCarryingUndeliverableCargoOnTheFactoryRole(t *testing.T) {
	hull := gateTestFullHullTagged(t, "F-1", gate.FactoryFleetTag, "IRON_ORE")
	assertUndeliverableFullHold(t, hull)
	f := newGateReallocHandlerWithHulls(t, hull)

	f.reallocate(t)

	if writes := f.assignments(); len(writes) != 0 {
		t.Fatalf("re-tagged %+v: the hull is full of cargo no bill wants, so on the delivery role wedgedAtFullHold declines it every tick and feedGateLegFromHold — factory-role-only — never runs. Moving it strands it in the one role that cannot empty it", writes)
	}
	// NON-VACUITY, and it is the whole weight of the test: "nobody moved" is satisfied by a
	// reallocator that returned on line one, by a census that never wanted this hull, and by a
	// planner whose target already matched. A recorded SKIP is only ever written for a hull the
	// target actually wanted moved, so this proves the decision was reached and then declined.
	if !strings.Contains(f.logLines(), "held F-1:") {
		t.Fatalf("no skip was recorded for F-1, so the planner never wanted it moved and this tick declined nothing:\n%s", f.logLines())
	}
	if !strings.Contains(f.logLines(), "want 1D/0F") {
		t.Fatalf("the target was not a delivery hull, so nothing here exercised the return-to-baseline direction:\n%s", f.logLines())
	}
	// The HOLD is what an operator needs, and the planner's own vocabulary can only say `busy`.
	// Without this line a hull held for a full hold and a hull held mid-haul are the same observation.
	log := f.logLines()
	if !strings.Contains(log, "F-1") || !strings.Contains(log, "IRON_ORE") {
		t.Fatalf("nothing in the log names the hull and the cargo holding it on the factory role:\n%s", log)
	}
}

// THE CONTROL. Without it, a reallocator that never returns ANY factory hull to delivery passes the
// test above — and that reallocator would strand the fleet all-factory the moment a pause ended.
func TestReallocateGateRoles_StillReturnsAFactoryHullWhoseHoldIsFree(t *testing.T) {
	f := newGateReallocHandler(t, gateCrewMember{"F-1", gate.FactoryFleetTag})

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) != 1 {
		t.Fatalf("wrote %+v; an EMPTY factory hull must still return to the D/F/F/D baseline, or a pause that ends never gives delivery its capacity back.\nlog:\n%s", writes, f.logLines())
	}
	if writes[0].ship != "F-1" || writes[0].fleet != gate.DeliveryFleetTag {
		t.Fatalf("wrote %+v, want F-1 -> %q", writes[0], gate.DeliveryFleetTag)
	}
}

// THE SECOND CONTROL, for the guard's other half. A factory hull full of a material the site STILL
// WANTS moves normally: the delivery role's flush unloads exactly that cargo at the construction
// site, so nothing about it is undeliverable. Drop the outstanding-materials check and such a hull
// is pinned to the factory role for no reason — a weakening no assertion above can see, because
// every hull in them carries cargo no bill wants.
func TestReallocateGateRoles_StillReturnsAFactoryHullFullOfMaterialTheBillWants(t *testing.T) {
	hull := gateTestFullHullTagged(t, "F-1", gate.FactoryFleetTag, gateMaterialPrimary)
	f := newGateReallocHandlerWithHulls(t, hull)

	// Non-vacuity: the hold must be FULL, or the hull moves on the predicate's first branch and this
	// says nothing about the bill check; and the cargo must genuinely be outstanding on the bill.
	if cargo := hull.Cargo(); cargo == nil || cargo.Capacity-cargo.Units != 0 {
		t.Fatal("fixture is inert: a hull with free hold is never held back whatever it carries")
	}
	if onHandUnits(hull, gateMaterialPrimary) == 0 {
		t.Fatalf("fixture is inert: the hull carries no %s, so the bill check is not what lets it move", gateMaterialPrimary)
	}

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) != 1 || writes[0].fleet != gate.DeliveryFleetTag {
		t.Fatalf("wrote %+v; a hull full of %s — which the site still wants and the delivery flush unloads — has no reason to be pinned to the factory role.\nlog:\n%s", writes, gateMaterialPrimary, f.logLines())
	}
}

// THE DIRECTION. The hold guard is scoped to the FACTORY role, and that scoping is load-bearing in
// the opposite direction: a wedged DELIVERY hull must STILL be borrowed to the factory role under a
// pause, because that borrow IS its recovery — feedGateLegFromHold empties it there.
//
// A guard that simply removed "carrying undeliverable cargo" from Idle would block this move too,
// and would convert a self-healing degradation into a permanent one. Nothing is lost by scoping to
// the factory role: moveTarget returns wanted=false for a factory hull whenever needFactory > 0, so
// the only move a factory-role hull can EVER be selected for is the return to delivery.
func TestReallocateGateRoles_StillBorrowsAWedgedDELIVERYHullIntoTheFactoryRole(t *testing.T) {
	hull := gateTestFullHullTagged(t, "D-1", gate.DeliveryFleetTag, "IRON_ORE")
	assertUndeliverableFullHold(t, hull) // the SAME hold that holds a factory hull back
	f := newGateReallocHandlerWithHulls(t, hull)
	f.pauseEveryMaterial()

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) != 1 {
		t.Fatalf("wrote %+v; a wedged DELIVERY hull must still be borrowed to the factory role — that borrow is the only recovery it has, and blocking it makes the wedge permanent.\nlog:\n%s", writes, f.logLines())
	}
	if writes[0].ship != "D-1" || writes[0].fleet != gate.FactoryFleetTag {
		t.Fatalf("wrote %+v, want D-1 -> %q", writes[0], gate.FactoryFleetTag)
	}
}

// UNWIRED: the reallocator is a no-op, never a nil panic. Moving hulls into a role whose leg is
// not wired would strand them on a path that does nothing.
func TestReallocateGateRoles_IsANoOpWhenEitherRoleIsUnwired(t *testing.T) {
	base := newGateDeliveryHandler(t) // SetGateFactory deliberately NOT called
	base.shipRepo.mu.Lock()
	base.shipRepo.ships = []*navigation.Ship{gateTestHull(t, "L-1", gate.LegacyFleetTag)}
	base.shipRepo.mu.Unlock()

	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)
	base.handler.reallocateGateRoles(base.ctx(), gateTestSystem, []*manufacturing.ManufacturingTask{task}, shared.MustNewPlayerID(1))

	base.shipRepo.mu.Lock()
	defer base.shipRepo.mu.Unlock()
	if len(base.shipRepo.assigned) != 0 {
		t.Fatalf("re-tagged %+v while the factory leg is unwired; those hulls would be stranded on a role that does nothing", base.shipRepo.assigned)
	}
	// NON-VACUITY, and it is exact rather than approximate: EVERY other exit in this coordinator
	// logs, deliberately ("a reallocator that declines every decision is otherwise
	// indistinguishable from one that is never invoked"). The unwired guard is the ONLY silent one.
	// So an empty "Gate roles" stream is precisely the claim "we returned at the wiring guard" —
	// where "wrote nobody" alone is equally satisfied by a tick that ran in full and found nothing
	// to move, or by one that exited at a bill that was already met.
	if strings.Contains(base.logLines(), "Gate roles") {
		t.Fatalf("the reallocator produced output with the factory leg unwired, so it ran PAST the wiring guard and stopped somewhere else:\n%s", base.logLines())
	}
}

// EVERY EXIT LOGS. commands/ has no metrics seam at all, so the log IS this coordinator's only
// counter — and a coordinator that declines every decision is indistinguishable from one that is
// never invoked unless the quiet paths say so themselves.
func TestReallocateGateRoles_AnnouncesTheQuietPathsToo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		system  string
		build   func(t *testing.T) (*gateReallocFixture, []*manufacturing.ManufacturingTask)
		want    string
		notWant string
	}{
		{
			// The message must say the census is EMPTY, never that its hulls are BUSY. A busy hull
			// IS in this census (Idle is a field on the worker, not a filter) and is reported as
			// held by the ruling, so "none is idle" would send an operator hunting for hauls that
			// do not exist.
			name:   "no gate hull in the fleet",
			system: gateTestSystem,
			build: func(t *testing.T) (*gateReallocFixture, []*manufacturing.ManufacturingTask) {
				f := newGateReallocHandler(t, gateCrewMember{"X-1", "contract"})
				return f, []*manufacturing.ManufacturingTask{manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)}
			},
			want:    "no gate-tagged hull exists in " + gateTestSystem,
			notWant: "idle",
		},
		{
			// An UNSET system means the in-system filter was skipped and the census was fleet-wide.
			// That is a different statement, so it gets its own phrase rather than interpolating an
			// empty symbol into "in %s" and rendering a dangling "in  " with two spaces.
			name:   "no gate hull and no system scoping",
			system: "",
			build: func(t *testing.T) (*gateReallocFixture, []*manufacturing.ManufacturingTask) {
				f := newGateReallocHandler(t, gateCrewMember{"X-1", "contract"})
				return f, []*manufacturing.ManufacturingTask{manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)}
			},
			want:    "no gate-tagged hull exists anywhere in the fleet",
			notWant: "  ", // a dangling "in  " from an empty symbol
		},
		{
			name:   "every bill already met",
			system: gateTestSystem,
			build: func(t *testing.T) (*gateReallocFixture, []*manufacturing.ManufacturingTask) {
				f := newGateReallocHandler(t, gateCrewMember{"D-1", gate.DeliveryFleetTag})
				f.pipeline.setBill(gateMaterialPrimary, 0)
				f.pipeline.setBill(gateMaterialSecondary, 0)
				return f, []*manufacturing.ManufacturingTask{manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)}
			},
			want: "no gate material is outstanding",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tasks := tc.build(t)
			f.handler.reallocateGateRoles(f.ctx(), tc.system, tasks, shared.MustNewPlayerID(1))

			if !strings.Contains(f.logLines(), tc.want) {
				t.Fatalf("this exit is silent, so a reallocator stuck here reads exactly like one that is never called. want %q in:\n%s", tc.want, f.logLines())
			}
			if tc.notWant != "" && strings.Contains(f.logLines(), tc.notWant) {
				t.Fatalf("the line contains %q, which misstates what this exit means:\n%s", tc.notWant, f.logLines())
			}
			if writes := f.assignments(); len(writes) != 0 {
				t.Fatalf("a quiet path still wrote %+v", writes)
			}
		})
	}
}

// THE TICK HOOK IS WIRED. A reallocation that exists but is never called is the same as one that
// does not exist — drive the real drain and assert the write happened.
func TestConstructionDrain_ReallocatesRolesDuringItsTick(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	// newTestHaulerInFleet, NOT gateTestHull: newDrainCommand runs in testSystem, and a gateTestHull
	// sits in the gate fixture's own system, where this drain would (correctly) decline to re-role it.
	hull := newTestHaulerInFleet(t, "L-1", gate.LegacyFleetTag)
	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	shipRepo.mu.Lock()
	defer shipRepo.mu.Unlock()
	if len(shipRepo.assigned) == 0 {
		t.Fatal("a drain tick adopted no legacy hull — reallocateGateRoles exists but is never called, which is indistinguishable from not existing")
	}
	if got, want := shipRepo.assigned[0].fleet, gate.DeliveryFleetTag; got != want {
		t.Fatalf("the drain adopted L-1 into %q, want %q", got, want)
	}
}

// THE RE-TAG IS VISIBLE TO THE SAME TICK. The hook runs before hauler discovery precisely so an
// adopted hull is discovered under its NEW pool and claimed under its NEW identity immediately;
// placed after discovery it would cost a whole tick per hull and, worse, the hull would be claimed
// under an identity that no longer matches its tag.
func TestConstructionDrain_ClaimsAnAdoptedHullUnderItsNewRoleIdentityTheSameTick(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	hull := newTestHaulerInFleet(t, "L-1", gate.LegacyFleetTag)
	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	shipRepo.mu.Lock()
	defer shipRepo.mu.Unlock()
	if len(shipRepo.claims) == 0 {
		t.Fatalf("the adopted hull was never claimed this tick; the re-tag has to land BEFORE discovery or it costs a tick per hull. assigned=%+v", shipRepo.assigned)
	}
	if got := shipRepo.claims[0].operation; got != gate.DeliveryFleetTag {
		t.Fatalf("claimed under %q after adopting the hull into %q — ClaimShip authorizes only when tag == operation, so this hull is rejected at the DB and then silently never works", got, gate.DeliveryFleetTag)
	}
}

// THE FIXTURE'S OWN FIDELITY, because two assertions above rest entirely on it.
//
// The same-tick claim test is only meaningful if (a) AssignFleet actually changes the tag the next
// read sees, as the real repository does by invalidating the ship-list cache, and (b) the fake
// rejects a claim whose operation does not equal that tag, as ClaimShip does. Miss (a) and the
// re-tag is invisible to discovery; miss (b) and every claim-identity assertion in this file passes
// on a repository that would have accepted anything.
func TestDrainFakeShipRepo_ModelsTheRetagAndTheDedicationGuard(t *testing.T) {
	hull := newTestHaulerInFleet(t, "L-1", gate.LegacyFleetTag)
	repo := newDrainShipRepo(hull)

	if err := repo.AssignFleet(context.Background(), "L-1", gate.DeliveryFleetTag, shared.MustNewPlayerID(1)); err != nil {
		t.Fatalf("AssignFleet: %v", err)
	}
	ships, err := repo.FindAllByPlayer(context.Background(), shared.MustNewPlayerID(1))
	if err != nil || len(ships) != 1 {
		t.Fatalf("FindAllByPlayer = %d ship(s), %v", len(ships), err)
	}
	if got := ships[0].DedicatedFleet(); got != gate.DeliveryFleetTag {
		t.Fatalf("a re-tagged hull reads back as %q; the real repository invalidates its ship-list cache, so a fake that only RECORDS the write makes every same-tick discovery assertion vacuous", got)
	}

	if err := repo.ClaimShip(context.Background(), "L-1", "C-1", shared.MustNewPlayerID(1), operationManufacturing); err == nil {
		t.Fatal("the fake accepted a claim whose operation does not equal the hull's dedicated fleet; production rejects it, so every claim-identity assertion in this file would be vacuous")
	}
	if err := repo.ClaimShip(context.Background(), "L-1", "C-1", shared.MustNewPlayerID(1), gate.DeliveryFleetTag); err != nil {
		t.Fatalf("the fake rejected a MATCHING claim identity: %v", err)
	}
}

// EXHAUSTIVENESS, standing in for the compiler. gate.Role is an int enum and both the routing
// switch and the dispatch decline branch on it; neither is checked for exhaustiveness by the
// compiler, so a third role added to gate.roleTags would be routed somewhere plausible and
// exempted from the decline in silence.
//
// This drives every tag the domain publishes and requires each to reach a leg of its OWN. Add a
// role without a case and this fails by name instead of shipping a silent mis-route.
//
// THE DISCRIMINATOR IS ProduceGood, NOT THE DELIVERED GOOD. A role that loses its case falls
// through to the SHARED source-then-deliver path, which terminates at the same construction-site
// delivery the delivery leg does — so "the site received the material" is true of both and cannot
// tell them apart. ProduceGood is called by the shared path and by neither gate leg, so requiring
// it to be UNTOUCHED is what makes "reached a leg of its own" mean anything. (Verified: without
// this term the probe that strips gate.RoleDelivery's case leaves this test green.)
func TestSupplyTask_EveryPublishedRoleTagReachesALegOfItsOwn(t *testing.T) {
	reached := map[string]string{}
	for _, tag := range gate.RoleFleetTags() {
		f := newGateFactoryHandler(t) // BOTH roles wired, so a mis-route has somewhere to go
		lot := gateReadyLot(t, gateTestHull(t, "GR-1", tag))
		lot.claimIdentity = tag

		f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

		fellBack := len(f.producer.produceGoods) > 0
		switch {
		case fellBack:
			t.Fatalf("a lot tagged %q fell through to the SHARED source-then-deliver path (ProduceGood on %v). A role with no case of its own is routed by the fall-through AND exempted from the wedged-hull decline, with no compiler help either way.\nlog:\n%s",
				tag, f.producer.produceGoods, f.logLines())
		case len(f.feeder.feeds()) > 0 && !f.deliveredGood(gateMaterialPrimary):
			reached[tag] = "feed"
		case f.deliveredGood(gateMaterialPrimary) && len(f.feeder.feeds()) == 0:
			reached[tag] = "deliver"
		default:
			t.Fatalf("a lot tagged %q reached no gate leg at all (feeds=%d, delivered=%v).\nlog:\n%s",
				tag, len(f.feeder.feeds()), f.deliveredGood(gateMaterialPrimary), f.logLines())
		}
	}
	if len(reached) != len(gate.RoleFleetTags()) {
		t.Fatalf("reached = %+v; every published role tag must resolve to exactly one leg", reached)
	}
	// Two tags that both reached the SAME leg would satisfy the loop above while proving the legs
	// are not actually distinct.
	seen := map[string]string{}
	for tag, leg := range reached {
		if other, dup := seen[leg]; dup {
			t.Fatalf("%q and %q both run the %s leg; the roles are not routed apart", tag, other, leg)
		}
		seen[leg] = tag
	}
}
