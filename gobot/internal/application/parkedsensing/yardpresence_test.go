package parkedsensing

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardpresence_test.go covers the pass that sends a hull to a shipyard whose
// prices the API will not disclose without one.
//
// THE FIXTURE IS SHAPED SO EVERY GUARD IS THE BINDING ONE SOMEWHERE. The defect
// this area produces is a fixture where a hull happens to be ineligible for two
// reasons at once, so removing either guard changes nothing and the test survives
// its own mutation. Each system below therefore offers exactly one plausible
// candidate, and what makes that candidate ineligible differs system by system:
// in X1-MAN it is a scout post, in X1-SOLE it is being the last observer of a
// good, in X1-OK it is nothing at all and the hull moves.

// --- fakes -------------------------------------------------------------------

// fakePresenceDemand is the shipyard-read budget as this pass sees it: a ranked
// request list and an allowance.
//
// tokens is a COUNTER rather than a rate limiter on purpose. What this file has
// to prove about metering is that the pass ASKS before it moves and OBEYS a
// refusal; that the allowance itself refills at the configured rate is the
// budget's own property and is pinned in its own package, where the clock can be
// controlled. Duplicating a limiter here would test golang.org/x/time/rate.
type fakePresenceDemand struct {
	requests []yardscan.PresenceRequest
	tokens   int

	// asked counts AdmitPresence calls, which is what shows the meter is consulted
	// once per dispatch rather than once per request considered.
	asked int
	// limit records the cap the pass asked for, so the request bound is observable.
	limit int
}

func (f *fakePresenceDemand) PresenceRequests(_ context.Context, _ int, limit int) []yardscan.PresenceRequest {
	f.limit = limit
	if len(f.requests) > limit {
		return f.requests[:limit]
	}
	return f.requests
}

func (f *fakePresenceDemand) AdmitPresence() bool {
	f.asked++
	if f.tokens <= 0 {
		return false
	}
	f.tokens--
	return true
}

// --- fixture builders ---------------------------------------------------------

func presenceMarket(system, waypoint, hull string, depth int64, goods ...string) QueuedSlot {
	return QueuedSlot{
		Waypoint:       waypoint,
		System:         system,
		Kind:           SlotKindMarket,
		State:          SlotStateParked,
		AssignedShip:   hull,
		DepthCredits:   depth,
		WhitelistGoods: goods,
	}
}

func wantedYard(system, waypoint string, goods ...string) QueuedSlot {
	return QueuedSlot{
		Waypoint:       waypoint,
		System:         system,
		Kind:           SlotKindMarket,
		State:          SlotStateWanted,
		WhitelistGoods: goods,
	}
}

func presenceRequest(waypoint, system string, heavy bool) yardscan.PresenceRequest {
	return yardscan.PresenceRequest{Waypoint: waypoint, System: system, Heavy: heavy, Weight: 8}
}

// presenceWorld wires a ledger, a ship reader and a post reader into the port
// bundle, with every hull in the ledger parked and locatable unless a test says
// otherwise.
func presenceWorld(t *testing.T, slots []QueuedSlot, mannedHulls []string, demand *fakePresenceDemand) (YardPresencePorts, *fakeBuyLedger, *flyingWorld) {
	t.Helper()
	ledger := &fakeBuyLedger{slots: slots}
	world := newFlyingWorld()
	for _, s := range slots {
		if s.AssignedShip != "" {
			world.park(s.AssignedShip, s.Waypoint)
		}
	}
	manned := map[string]bool{}
	for _, h := range mannedHulls {
		manned[h] = true
	}
	return YardPresencePorts{
		Demand:      demand,
		Ledger:      ledger,
		Ships:       world,
		MannedHulls: &fakeManned{hulls: manned},
	}, ledger, world
}

// --- the happy path -----------------------------------------------------------

// TestPresence_SendsRedundantHullToUnpricedYard is the acceptance case: a yard we
// know sells a wanted hull, whose price no read can reveal, gets one.
func TestPresence_SendsRedundantHullToUnpricedYard(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("expected one hull sent to the unpriced yard, got %d (nohull=%d metered=%d)", rep.Dispatched, rep.NoHull, rep.Metered)
	}

	// The cheapest sacrifice goes: HULL-A at depth 100, not HULL-B at 200.
	claims := ledger.transitionsTo(SlotStateInTransit)
	if len(claims) != 1 || claims[0].waypoint != "X1-OK-Y1" {
		t.Fatalf("expected the yard claimed IN_TRANSIT, got %+v", claims)
	}
	if claims[0].assignedShip == nil || *claims[0].assignedShip != "HULL-A" {
		t.Fatalf("expected the least valuable market's hull to be sent, got %+v", claims[0].assignedShip)
	}

	// The market it left reverts to WANTED with no hull, so the fleet still
	// intends to cover it and the probe cap stops counting it twice.
	released := ledger.transitionsTo(SlotStateWanted)
	if len(released) != 1 || released[0].waypoint != "X1-OK-A1" {
		t.Fatalf("expected the source market released to WANTED, got %+v", released)
	}
	if released[0].assignedShip == nil || *released[0].assignedShip != "" {
		t.Fatalf("expected the source market's hull reference CLEARED, got %+v", released[0].assignedShip)
	}
}

// TestPresence_ClaimsTargetBeforeReleasingSource pins the write order that is a
// money guard: a crash between the writes must double-count a hull (cap reads
// high, fewer probes bought), never lose one (cap reads low, a replacement bought
// for a probe we own — the direction RULINGS #4 forbids).
func TestPresence_ClaimsTargetBeforeReleasingSource(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", false)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	if _, err := DispatchYardPresence(context.Background(), ports, 1); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if len(ledger.transitions) != 2 {
		t.Fatalf("expected exactly two ledger writes, got %+v", ledger.transitions)
	}
	if ledger.transitions[0].to != SlotStateInTransit {
		t.Fatalf("the TARGET must be claimed first; got %s first", ledger.transitions[0].to)
	}
	if ledger.transitions[1].to != SlotStateWanted {
		t.Fatalf("the SOURCE must be released second; got %s second", ledger.transitions[1].to)
	}
}

// --- guard: a hull manning a scout post is not ours to take --------------------

// TestPresence_WillNotTakeAHullManningAScoutPost is the target of the
// spare-hull-safety mutation. The only hull whose goods are redundant is standing
// a scout post, so a pass that stopped consulting the post reader would take a
// hull that is doing paid sensing work.
func TestPresence_WillNotTakeAHullManningAScoutPost(t *testing.T) {
	slots := []QueuedSlot{
		// HULL-M is redundant with HULL-N on every good it watches, so redundancy
		// alone would release it. It is manning a post.
		presenceMarket("X1-MAN", "X1-MAN-A1", "HULL-M", 100, "FUEL", "IRON"),
		// HULL-N is the sole observer of PLATINUM, so redundancy pins it in place
		// and it can never be the one taken. That keeps the POST guard the only
		// thing standing between the pass and HULL-M.
		presenceMarket("X1-MAN", "X1-MAN-A2", "HULL-N", 200, "FUEL", "IRON", "PLATINUM"),
		wantedYard("X1-MAN", "X1-MAN-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-MAN-Y1", "X1-MAN", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, []string{"HULL-M"}, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 {
		t.Fatalf("a hull manning a scout post was taken for a yard read: %+v", ledger.transitions)
	}
	if rep.NoHull != 1 {
		t.Fatalf("expected the request recorded as having no releasable hull, got %+v", rep)
	}
	if len(ledger.transitions) != 0 {
		t.Fatalf("expected no ledger writes at all, got %+v", ledger.transitions)
	}
}

// --- guard: the last observer of a good stays put ------------------------------

// TestPresence_WillNotTakeTheLastObserverOfAGood is the second target of the
// spare-hull-safety mutation. No post is manned here, so the ONLY thing making
// the candidate ineligible is that its departure would blind the system to a
// good — which is exactly "a probe doing paid sensing work".
func TestPresence_WillNotTakeTheLastObserverOfAGood(t *testing.T) {
	slots := []QueuedSlot{
		// HULL-S is the only hull watching SILVER anywhere in the system, and the
		// yard it would be sent to does not watch SILVER either.
		presenceMarket("X1-SOLE", "X1-SOLE-A1", "HULL-S", 100, "SILVER"),
		presenceMarket("X1-SOLE", "X1-SOLE-A2", "HULL-T", 200, "COPPER", "TIN"),
		wantedYard("X1-SOLE", "X1-SOLE-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-SOLE-Y1", "X1-SOLE", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 {
		t.Fatalf("the last observer of a good was taken for a yard read: %+v", ledger.transitions)
	}
	if len(ledger.transitions) != 0 {
		t.Fatalf("expected no ledger writes at all, got %+v", ledger.transitions)
	}
}

// TestPresence_DestinationGoodsPreserveCoverage is the other half of that rule
// and the reason the move-aware test exists: the same sole observer IS releasable
// when the yard it moves to watches the good itself, because the hull relocates
// the coverage rather than removing it.
func TestPresence_DestinationGoodsPreserveCoverage(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-REL", "X1-REL-A1", "HULL-S", 100, "SILVER"),
		presenceMarket("X1-REL", "X1-REL-A2", "HULL-T", 200, "COPPER"),
		// The yard watches SILVER, so SILVER survives HULL-S's departure.
		wantedYard("X1-REL", "X1-REL-Y1", "SILVER"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-REL-Y1", "X1-REL", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("expected the hull to move when the destination preserves its good, got %+v", rep)
	}
	claims := ledger.transitionsTo(SlotStateInTransit)
	if len(claims) != 1 || claims[0].assignedShip == nil || *claims[0].assignedShip != "HULL-S" {
		t.Fatalf("expected HULL-S sent to the yard, got %+v", claims)
	}
}

// --- guard: the metered allowance ---------------------------------------------

// TestPresence_ObeysTheMeteredAllowance is the target of the metering mutation. A
// perfectly releasable hull is standing beside a yard that needs it and the
// allowance is empty, so a pass that stopped consulting the meter would issue an
// unmetered reposition.
func TestPresence_ObeysTheMeteredAllowance(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 0}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 {
		t.Fatalf("a reposition was issued with no allowance left: %+v", ledger.transitions)
	}
	if rep.Metered != 1 {
		t.Fatalf("expected the refusal attributed to the meter, got %+v", rep)
	}
	if len(ledger.transitions) != 0 {
		t.Fatalf("expected no ledger writes at all, got %+v", ledger.transitions)
	}
	if demand.asked != 1 {
		t.Fatalf("expected the allowance to be consulted exactly once, got %d", demand.asked)
	}
}

// TestPresence_SpendsNoAllowanceWhenNoHullCanBeSpared pins the check ORDER. Most
// requests have nobody to send, and asking the meter for those would drain the
// allowance on yards that move nothing — throttling the pass without ever pacing
// a real reposition.
func TestPresence_SpendsNoAllowanceWhenNoHullCanBeSpared(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-SOLE", "X1-SOLE-A1", "HULL-S", 100, "SILVER"),
		presenceMarket("X1-SOLE", "X1-SOLE-A2", "HULL-T", 200, "COPPER"),
		wantedYard("X1-SOLE", "X1-SOLE-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-SOLE-Y1", "X1-SOLE", true)}, tokens: 4}
	ports, _, _ := presenceWorld(t, slots, nil, demand)

	if _, err := DispatchYardPresence(context.Background(), ports, 1); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if demand.asked != 0 {
		t.Fatalf("the allowance was spent on a request that could move nothing (%d asks)", demand.asked)
	}
	if demand.tokens != 4 {
		t.Fatalf("expected the allowance untouched, got %d tokens left", demand.tokens)
	}
}

// --- bounds --------------------------------------------------------------------

// TestPresence_StopsAtThePerTickBound proves the backstop binds, and the fixture
// deliberately offers MORE fillable systems than the bound: a fixture with only
// two would pass whether the bound existed or not.
func TestPresence_StopsAtThePerTickBound(t *testing.T) {
	var slots []QueuedSlot
	var requests []yardscan.PresenceRequest
	systems := []string{"X1-P1", "X1-P2", "X1-P3", "X1-P4", "X1-P5"}
	for _, s := range systems {
		slots = append(slots,
			presenceMarket(s, s+"-A1", "HULL-"+s+"-A", 100, "FUEL", "IRON"),
			presenceMarket(s, s+"-A2", "HULL-"+s+"-B", 200, "FUEL", "IRON"),
			wantedYard(s, s+"-Y1", "GOLD"),
		)
		requests = append(requests, presenceRequest(s+"-Y1", s, true))
	}
	// Allowance deliberately far past the bound, so the ONLY thing that can stop
	// the pass at two is MaxYardPresenceDispatches.
	demand := &fakePresenceDemand{requests: requests, tokens: 99}
	ports, _, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != MaxYardPresenceDispatches {
		t.Fatalf("expected the per-tick bound of %d to hold, got %d", MaxYardPresenceDispatches, rep.Dispatched)
	}
}

// TestPresence_AsksForMoreRequestsThanItCanSend pins the deliberate gap between
// the request limit and the dispatch bound. Most requests cannot be filled, so
// asking for only two would usually yield nothing.
func TestPresence_AsksForMoreRequestsThanItCanSend(t *testing.T) {
	demand := &fakePresenceDemand{}
	ports, _, _ := presenceWorld(t, nil, nil, demand)

	if _, err := DispatchYardPresence(context.Background(), ports, 1); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if demand.limit <= MaxYardPresenceDispatches {
		t.Fatalf("the request limit (%d) must exceed the dispatch bound (%d) or unfillable yards starve fillable ones",
			demand.limit, MaxYardPresenceDispatches)
	}
}

// --- what the pass must never do ------------------------------------------------

// TestPresence_NeverCreatesAPlacement is the RULINGS-shaped guard on scope:
// placements are the screen's to write. A yard with no WANTED slot is skipped,
// not conjured, so this pass can only ever ACCELERATE a placement the fleet had
// already decided it wanted.
func TestPresence_NeverCreatesAPlacement(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-NOSLOT", "X1-NOSLOT-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-NOSLOT", "X1-NOSLOT-A2", "HULL-B", 200, "FUEL", "IRON"),
		// No row at all for X1-NOSLOT-Y1.
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-NOSLOT-Y1", "X1-NOSLOT", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
		t.Fatalf("a yard with no placement was filled anyway: %+v", ledger.transitions)
	}
}

// TestPresence_SkipsAPlacementAlreadyBeingFilled keeps this pass out of the
// drain's way: a QUEUED placement may have money moving against it, and anything
// from BOUGHT on already names a hull.
func TestPresence_SkipsAPlacementAlreadyBeingFilled(t *testing.T) {
	for _, state := range []string{SlotStateQueued, SlotStateBought, SlotStateInTransit, SlotStateParked} {
		t.Run(state, func(t *testing.T) {
			target := wantedYard("X1-BUSY", "X1-BUSY-Y1", "GOLD")
			target.State = state
			if state != SlotStateWanted {
				target.AssignedShip = "HULL-EXISTING"
			}
			slots := []QueuedSlot{
				presenceMarket("X1-BUSY", "X1-BUSY-A1", "HULL-A", 100, "FUEL", "IRON"),
				presenceMarket("X1-BUSY", "X1-BUSY-A2", "HULL-B", 200, "FUEL", "IRON"),
				target,
			}
			demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-BUSY-Y1", "X1-BUSY", true)}, tokens: 4}
			ports, ledger, _ := presenceWorld(t, slots, nil, demand)

			rep, err := DispatchYardPresence(context.Background(), ports, 1)
			if err != nil {
				t.Fatalf("dispatch failed: %v", err)
			}
			if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
				t.Fatalf("a %s placement was re-tasked out from under the machine filling it: %+v", state, ledger.transitions)
			}
		})
	}
}

// TestPresence_WillNotTakeAHullThatIsFlying stops two machines steering one hull.
// The ledger says PARKED; the ships table disagrees, and the ships table wins.
func TestPresence_WillNotTakeAHullThatIsFlying(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ports, ledger, world := presenceWorld(t, slots, nil, demand)
	// HULL-A is the one the pool would pick (cheapest). It is in flight.
	world.positions["HULL-A"] = ShipPos{Waypoint: "X1-OK-A1", NavStatus: navigation.NavStatusInTransit, Found: true}

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
		t.Fatalf("a hull already in flight was re-tasked: %+v", ledger.transitions)
	}
}

// TestPresence_WillNotTakeAHullItCannotLocate is the same rule for a hull the
// ships table does not know. Unreadable is NOT takeable.
func TestPresence_WillNotTakeAHullItCannotLocate(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ports, ledger, world := presenceWorld(t, slots, nil, demand)
	delete(world.positions, "HULL-A")

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
		t.Fatalf("a hull the ships table cannot locate was re-tasked: %+v", ledger.transitions)
	}
}

// TestPresence_DrawsOnlyFromTheYardsOwnSystem. A price read never justifies a
// gate crossing; only a foothold does.
func TestPresence_DrawsOnlyFromTheYardsOwnSystem(t *testing.T) {
	slots := []QueuedSlot{
		// Plenty of releasable hulls — in the WRONG system.
		presenceMarket("X1-FAR", "X1-FAR-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-FAR", "X1-FAR-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-NEAR", "X1-NEAR-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-NEAR-Y1", "X1-NEAR", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
		t.Fatalf("a hull was flown out of its own system for a price read: %+v", ledger.transitions)
	}
}

// TestPresence_RedundancyIsRedecidedAfterEveryTake. Redundancy is a property of a
// SET: if A and B both watch a good and nothing else does, a snapshot marks BOTH
// releasable and taking both loses the good entirely.
func TestPresence_RedundancyIsRedecidedAfterEveryTake(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-PAIR", "X1-PAIR-A1", "HULL-A", 100, "FUEL"),
		presenceMarket("X1-PAIR", "X1-PAIR-A2", "HULL-B", 200, "FUEL"),
		// Two yards, so the pass would happily take a second hull if it could.
		wantedYard("X1-PAIR", "X1-PAIR-Y1", "GOLD"),
		wantedYard("X1-PAIR", "X1-PAIR-Y2", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{
		presenceRequest("X1-PAIR-Y1", "X1-PAIR", true),
		presenceRequest("X1-PAIR-Y2", "X1-PAIR", true),
	}, tokens: 99}
	ports, _, _ := presenceWorld(t, slots, nil, demand)

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if rep.Dispatched != 1 {
		t.Fatalf("FUEL lost its last observer: expected exactly one take from a mutually-redundant pair, got %d", rep.Dispatched)
	}
}

// --- fail-closed ----------------------------------------------------------------

// TestPresence_FailsClosedWhenPostsAreUnreadable. An unreadable post list read
// permissively is an EMPTY one — "no hull is manned" — which would hand the
// scouting fleet's hulls to this pass.
func TestPresence_FailsClosedWhenPostsAreUnreadable(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ledger := &fakeBuyLedger{slots: slots}
	world := newFlyingWorld()
	for _, s := range slots {
		if s.AssignedShip != "" {
			world.park(s.AssignedShip, s.Waypoint)
		}
	}
	ports := YardPresencePorts{
		Demand: demand,
		Ledger: ledger,
		Ships:  world,
		// Adversarial: a populated manned set ALONGSIDE the error, so a reader that
		// leaked the error would still look like it had answered.
		MannedHulls: &fakeManned{hulls: map[string]bool{"HULL-Z": true}, err: errors.New("posts unreadable")},
	}

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err == nil {
		t.Fatal("expected an unreadable post list to stop the pass, got nil")
	}
	if rep.Dispatched != 0 || len(ledger.transitions) != 0 {
		t.Fatalf("hulls were moved without knowing which are manned: %+v", ledger.transitions)
	}
}

// TestPresence_FailsClosedWhenParkedSlotsAreUnreadable. Without the parked list
// the pool is empty, which looks identical to "nothing can be spared" — so the
// read failure has to be surfaced rather than silently yielding no dispatch.
func TestPresence_FailsClosedWhenParkedSlotsAreUnreadable(t *testing.T) {
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ledger := &fakeBuyLedger{slotsErr: errors.New("ledger down")}
	ports := YardPresencePorts{
		Demand:      demand,
		Ledger:      ledger,
		Ships:       newFlyingWorld(),
		MannedHulls: &fakeManned{},
	}
	if _, err := DispatchYardPresence(context.Background(), ports, 1); err == nil {
		t.Fatal("expected an unreadable placement ledger to stop the pass, got nil")
	}
}

// TestPresence_IsInertWithoutItsPorts. The pass is an extension to sensing, not a
// precondition for it: a daemon wired without the budget must tick unchanged.
func TestPresence_IsInertWithoutItsPorts(t *testing.T) {
	rep, err := DispatchYardPresence(context.Background(), YardPresencePorts{}, 1)
	if err != nil {
		t.Fatalf("an unwired pass must be inert, not fatal: %v", err)
	}
	if rep.Requested != 0 || rep.Dispatched != 0 {
		t.Fatalf("an unwired pass reported work: %+v", rep)
	}
}

// TestPresence_SurvivesALostRaceForTheTarget. Another writer claimed the
// placement between the read and the write. The source is untouched — which is
// why the target is claimed first — so nothing needs undoing.
func TestPresence_SurvivesALostRaceForTheTarget(t *testing.T) {
	slots := []QueuedSlot{
		presenceMarket("X1-OK", "X1-OK-A1", "HULL-A", 100, "FUEL", "IRON"),
		presenceMarket("X1-OK", "X1-OK-A2", "HULL-B", 200, "FUEL", "IRON"),
		wantedYard("X1-OK", "X1-OK-Y1", "GOLD"),
	}
	demand := &fakePresenceDemand{requests: []yardscan.PresenceRequest{presenceRequest("X1-OK-Y1", "X1-OK", true)}, tokens: 4}
	ports, ledger, _ := presenceWorld(t, slots, nil, demand)
	ledger.transitionErr = map[string]error{"X1-OK-Y1→" + SlotStateInTransit: ErrSlotClaimed}

	rep, err := DispatchYardPresence(context.Background(), ports, 1)
	if err != nil {
		t.Fatalf("a lost race is not an error: %v", err)
	}
	if rep.Dispatched != 0 {
		t.Fatalf("expected no dispatch recorded for a lost race, got %+v", rep)
	}
	if released := ledger.transitionsTo(SlotStateWanted); len(released) != 0 {
		t.Fatalf("the source market was released after losing the target race: %+v", released)
	}
}
