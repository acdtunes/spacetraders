package gate

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var reallocNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// idleWorker is a hull eligible on every guard: idle, and never moved by this process.
func idleWorker(ship, tag string) Worker {
	return Worker{Ship: ship, FleetTag: tag, Idle: true}
}

func movedShips(plan ReallocationPlan) []string {
	out := make([]string, 0, len(plan.Moves))
	for _, m := range plan.Moves {
		out = append(out, m.Ship+"->"+m.To.String())
	}
	return out
}

func skipReason(plan ReallocationPlan, ship string) string {
	for _, s := range plan.Skips {
		if s.Ship == ship {
			return s.Reason
		}
	}
	return ""
}

// THE BASELINE IS THE PURCHASE ORDER. BaselineMix must equal what D/F/F/D produces at every N,
// because it is derived from the SAME NextRole and there must be no second mix rule to drift.
func TestBaselineMix_MatchesTheDFFDPurchaseOrderAtEveryN(t *testing.T) {
	cases := []struct{ n, wantDelivery, wantFactory int }{
		{0, 0, 0}, {1, 1, 0}, {2, 1, 1}, {3, 1, 2}, {4, 2, 2}, {8, 4, 4},
	}
	for _, tc := range cases {
		delivery, factory := BaselineMix(tc.n)
		if delivery != tc.wantDelivery || factory != tc.wantFactory {
			t.Fatalf("BaselineMix(%d) = %dD/%dF, want %dD/%dF", tc.n, delivery, factory, tc.wantDelivery, tc.wantFactory)
		}
	}
	if delivery, factory := BaselineMix(-3); delivery != 0 || factory != 0 {
		t.Fatalf("BaselineMix(-3) = %dD/%dF; a negative census is answered, not extrapolated", delivery, factory)
	}
}

// PAUSED: delivery hulls move to the factory role. This is what makes the pause a
// self-shortening feedback loop — the hulls go feed the factory that is low, so supply recovers
// sooner and delivery resumes.
func TestPlanReallocation_APausedFleetMovesDeliveryHullsToTheFactoryRole(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("F-1", FactoryFleetTag),
			idleWorker("F-2", FactoryFleetTag),
		},
		MaxMoves: 4,
	})

	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %v, want both delivery hulls moved to factory", movedShips(plan))
	}
	for _, m := range plan.Moves {
		if m.To != RoleFactory {
			t.Fatalf("move %+v targets %v; a paused fleet moves TOWARD the factory role", m, m.To)
		}
		if m.From != DeliveryFleetTag {
			t.Fatalf("move %+v came from %q; a factory hull is already where the paused target wants it and must not be re-tagged", m, m.From)
		}
		if m.Reason != MoveReasonPauseToFactory {
			t.Fatalf("move %+v reason = %q, want %q — an operator must be able to tell WHY a hull changed role", m, m.Reason, MoveReasonPauseToFactory)
		}
	}
}

// UNPAUSED: the fleet returns to the D/F/F/D BASELINE, not to all-delivery.
//
// A literal reading of "they move back" would return every factory hull to delivery, including
// the two the purchase order BOUGHT as factory hulls — emptying the factory fleet, starving the
// terminal factories, and re-tripping the pause. That is a thrash no dwell timer can fix.
func TestPlanReallocation_AnUnpausedFleetReturnsToTheBaselineMixNotToAllDelivery(t *testing.T) {
	workers := []Worker{
		idleWorker("H-1", FactoryFleetTag),
		idleWorker("H-2", FactoryFleetTag),
		idleWorker("H-3", FactoryFleetTag),
		idleWorker("H-4", FactoryFleetTag),
	}

	// Converge with an unbounded move cap so the END STATE is asserted, not the pacing.
	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: workers, MaxMoves: 10})

	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %v, want exactly 2 hulls back to delivery (baseline 2D/2F), not all 4", movedShips(plan))
	}
	for _, m := range plan.Moves {
		if m.To != RoleDelivery {
			t.Fatalf("move %+v targets %v; an unpaused fleet is short of DELIVERY", m, m.To)
		}
		if m.Reason != MoveReasonResumeToBaseline {
			t.Fatalf("move %+v reason = %q, want %q", m, m.Reason, MoveReasonResumeToBaseline)
		}
	}
	if plan.WantDelivery != 2 || plan.WantFactory != 2 {
		t.Fatalf("target mix = %dD/%dF, want the 2D/2F baseline for 4 hulls", plan.WantDelivery, plan.WantFactory)
	}
}

// A fleet already AT its target moves nothing. Reallocation is a correction, not a heartbeat.
func TestPlanReallocation_AFleetAtItsTargetMovesNothing(t *testing.T) {
	workers := []Worker{
		idleWorker("D-1", DeliveryFleetTag),
		idleWorker("D-2", DeliveryFleetTag),
		idleWorker("F-1", FactoryFleetTag),
		idleWorker("F-2", FactoryFleetTag),
	}

	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: workers, MaxMoves: 10})

	if len(plan.Moves) != 0 {
		t.Fatalf("moves = %v; a fleet already at the baseline must not churn", movedShips(plan))
	}
	// "No moves" alone is satisfiable by a reallocator that computes nothing, so pin the two
	// POSITIVE facts that make this a settled fleet rather than a broken one: the target really
	// was the 2D/2F baseline, and the census really did meet it.
	if plan.WantDelivery != 2 || plan.WantFactory != 2 || plan.HaveDelivery != 2 || plan.HaveFactory != 2 {
		t.Fatalf("plan = %+v; want the 2D/2F baseline already met by a 2D/2F census", plan)
	}
	// A hull already where the target wants it is not a DECISION, so it is not a skip either.
	// Recording it would bury the actionable declines under one entry per hull per tick.
	if len(plan.Skips) != 0 {
		t.Fatalf("skips = %+v; a hull already in its target role was never held back, so it must not be reported as one", plan.Skips)
	}
}

// LEGACY ADOPTION — THE ARMING FIX. A hull carrying the legacy "manufacturing" tag holds no
// role, so phase 2's leg never fires for it. On a fleet already holding four of them the ramp
// buys nothing (GateWorkers == gateWorkerTarget), so no role tag is EVER written and the entire
// two-fleet feature is inert. Adopting them is what arms it, and it costs no purchase.
//
// The adoption order is the D/F/F/D order itself, via the same NextRole the purchase path uses.
func TestPlanReallocation_LegacyHullsAreAdoptedIntoRolesInTheDFFDOrder(t *testing.T) {
	workers := []Worker{
		idleWorker("L-1", LegacyFleetTag),
		idleWorker("L-2", LegacyFleetTag),
		idleWorker("L-3", LegacyFleetTag),
		idleWorker("L-4", LegacyFleetTag),
	}

	// One move per tick is the default; drive four ticks and re-feed the plan's own decisions
	// back in, exactly as the drain does.
	tagOf := map[string]string{"L-1": LegacyFleetTag, "L-2": LegacyFleetTag, "L-3": LegacyFleetTag, "L-4": LegacyFleetTag}
	order := make([]Role, 0, 4)
	for tick := 0; tick < 4; tick++ {
		live := make([]Worker, 0, len(workers))
		for _, w := range workers {
			live = append(live, idleWorker(w.Ship, tagOf[w.Ship]))
		}
		plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: live, MaxMoves: 1})
		if len(plan.Moves) != 1 {
			t.Fatalf("tick %d: moves = %v, want exactly one (the default per-tick cap)", tick, movedShips(plan))
		}
		move := plan.Moves[0]
		if move.Reason != MoveReasonLegacyAdoption {
			t.Fatalf("tick %d: move %+v reason = %q, want %q", tick, move, move.Reason, MoveReasonLegacyAdoption)
		}
		tagOf[move.Ship] = move.To.FleetTag()
		order = append(order, move.To)
	}

	want := []Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("adoption order = %v, want the D/F/F/D purchase order %v — the mix rule must be NextRole, not a second one", order, want)
		}
	}
}

// A legacy hull is adopted even while the fleet is PAUSED: it holds no role, so moving it can
// only fill a deficit. The paused target is all-factory, so that is where it goes.
func TestPlanReallocation_ALegacyHullIsAdoptedEvenWhilePaused(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{idleWorker("L-1", LegacyFleetTag)},
		MaxMoves:       1,
	})

	if len(plan.Moves) != 1 || plan.Moves[0].To != RoleFactory {
		t.Fatalf("moves = %v, want the legacy hull adopted into the factory role while paused", movedShips(plan))
	}
}

// THRASH GUARD 1 — BUSY. A hull mid-haul is never re-tagged. constructionLot.claimIdentity is
// FROZEN at plan time, so a re-tag in flight makes the worker present a stale tag and ClaimShip
// rejects it (it authorizes only when tag == operation) — the hull is dispatched and then
// silently never works.
func TestPlanReallocation_AHullMidHaulIsNeverMoved(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: false},
			idleWorker("D-2", DeliveryFleetTag),
		},
		MaxMoves: 10,
	})

	for _, m := range plan.Moves {
		if m.Ship == "D-1" {
			t.Fatalf("moved D-1 while it was mid-haul; its lot's frozen claim identity would then be rejected at the DB")
		}
	}
	if got := skipReason(plan, "D-1"); got != MoveSkipBusy {
		t.Fatalf("skip reason for the busy hull = %q, want %q — a hull held back must say so, or a stalled reallocation looks like a satisfied one", got, MoveSkipBusy)
	}
	// "D-1 is absent from the moves" is also true of a reallocator that moves NOTHING, so pin the
	// positive: the busy guard holds one hull, it does not stall the policy.
	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "D-2" {
		t.Fatalf("moves = %v; the idle hull must still move — one hull mid-haul must not freeze the whole reallocation", movedShips(plan))
	}
}

// THRASH GUARD 2 — DWELL. Supply oscillating at the buy floor must not oscillate the workforce.
func TestPlanReallocation_AHullInsideItsDwellWindowIsHeld(t *testing.T) {
	justMoved := Worker{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Minute)}
	settled := Worker{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Hour)}

	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{justMoved, settled},
		Dwell:          10 * time.Minute,
		MaxMoves:       10,
	})

	for _, m := range plan.Moves {
		if m.Ship == "D-1" {
			t.Fatalf("moved D-1 one minute into a 10-minute dwell — that is the oscillation the dwell exists to stop")
		}
	}
	if got := skipReason(plan, "D-1"); got != MoveSkipDwell {
		t.Fatalf("skip reason inside the dwell = %q, want %q", got, MoveSkipDwell)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "D-2" {
		t.Fatalf("moves = %v; the settled hull must still move — the dwell holds one hull, not the whole policy", movedShips(plan))
	}
}

// A hull this process has NEVER moved has a zero LastMovedByUs and is eligible immediately. The
// dwell clock is the drain's, not the ship's; refusing on an absent record would deadlock the
// arming after every restart.
func TestPlanReallocation_AnUnseenHullIsEligibleImmediately(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true}}, // zero LastMovedByUs
		Dwell:          time.Hour,
		MaxMoves:       1,
	})

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %v; a hull with no dwell record must be movable, or a restart deadlocks every reallocation", movedShips(plan))
	}
}

// THRASH GUARD 3 — PER-TICK MOVE CAP. Each move is free, but a burst would swing the whole
// fleet on one noisy observation.
func TestPlanReallocation_TheMoveCapBoundsOneTickAndRecordsWhoWasHeld(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("D-3", DeliveryFleetTag),
		},
		MaxMoves: 1,
	})

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %v, want exactly 1 under a cap of 1", movedShips(plan))
	}
	held := 0
	for _, s := range plan.Skips {
		if s.Reason == MoveSkipMoveCap {
			held++
		}
	}
	if held != 2 {
		t.Fatalf("skips = %+v, want the 2 hulls the cap held recorded as %q", plan.Skips, MoveSkipMoveCap)
	}
}

// Unset knobs resolve to the ARMED defaults. There is no off state: a 0 is an unset knob, never
// a disabled policy, and a 0 move cap must not mean "move nothing".
func TestPlanReallocation_UnsetKnobsResolveToTheArmedDefaults(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
		},
	})

	if len(plan.Moves) != DefaultMaxRoleMovesPerTick {
		t.Fatalf("moves = %v with an unset cap; an unset knob must resolve to the armed default %d, never to zero", movedShips(plan), DefaultMaxRoleMovesPerTick)
	}
	if DefaultRoleDwell <= 0 {
		t.Fatalf("DefaultRoleDwell = %s; a non-positive dwell is no thrash guard at all", DefaultRoleDwell)
	}
}

// ...and the unset DWELL resolves to its armed default too, which is a BEHAVIOURAL claim.
// Asserting only that DefaultRoleDwell is positive would stay green if the resolver defaulted an
// unset knob to zero and never consulted the constant: the guard would be structurally present
// and operationally off, which is precisely the failure mode "there is no off state" denies.
func TestPlanReallocation_AnUnsetDwellResolvesToTheArmedDefaultNotToZero(t *testing.T) {
	justMoved := Worker{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Minute)}

	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{justMoved},
		// Dwell deliberately unset.
	})

	if len(plan.Moves) != 0 {
		t.Fatalf("moves = %v; a hull one minute into the %s default dwell must be held even with the knob unset", movedShips(plan), DefaultRoleDwell)
	}
	if got := skipReason(plan, "D-1"); got != MoveSkipDwell {
		t.Fatalf("skip reason with an unset dwell = %q, want %q — the default must be the ARMED %s, not zero", got, MoveSkipDwell, DefaultRoleDwell)
	}
}

// An empty fleet is answered, not panicked on. The drain calls this every tick, including
// before any gate hull exists.
func TestPlanReallocation_AnEmptyFleetIsAnswered(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: true})
	if len(plan.Moves) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want an empty plan for an empty fleet", plan)
	}
	// "Nothing came back" is satisfied by `return ReallocationPlan{}` — and that bare zero value
	// would then report a PAUSED fleet as "delivery running" in the log, which is the one fact an
	// operator reads this line for. The early return carries the pause through on purpose; pin it
	// on the field AND on the rendering, because the rendering is what is actually consumed.
	if !plan.DeliveryPaused {
		t.Fatalf("plan = %+v; the empty-fleet answer must still carry the pause it was asked about", plan)
	}
	if line := plan.LogLine(); !strings.Contains(line, "PAUSED") {
		t.Fatalf("empty-fleet log line %q does not say PAUSED; a paused fleet with no hulls yet must not read as a running one", line)
	}
}

// MEMBERSHIP — A FOREIGN HULL IS NOT A LEGACY HULL. ParseFleetTag returns ok=false for three
// different things: the legacy gate tag, a foreign fleet tag, and the undedicated empty one. Only
// the first is ours. IsGateFleetTag is the package's designated re-tag membership test and this is
// the fourth re-tag decision in the tree; the other three already gate on it.
//
// The DENOMINATOR is the sharper half of this. The target is BaselineMix over the census, so one
// intruder beside four gate hulls asks for BaselineMix(5) = 3D/2F instead of 2D/2F — and the
// planner re-tags a FACTORY hull to chase a delivery deficit that does not exist. That corruption
// needs the intruder to do nothing at all but be counted.
func TestPlanReallocation_AForeignHullIsNeitherAdoptedNorCountedInTheTargetMix(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: false,
		Workers: []Worker{
			idleWorker("X-1", "contract-delivery"), // owned by another operation
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("F-1", FactoryFleetTag),
			idleWorker("F-2", FactoryFleetTag),
		},
		MaxMoves: 10,
	})

	// The four gate hulls are already at their 2D/2F baseline, so a correct planner moves NOTHING.
	// Counting the intruder makes the target 3D/2F and manufactures a move; that is the assertion.
	if len(plan.Moves) != 0 {
		t.Fatalf("moves = %v; the gate crew is already at its 2D/2F baseline — a foreign hull must not be counted into the target mix", movedShips(plan))
	}
	if plan.WantDelivery != 2 || plan.WantFactory != 2 {
		t.Fatalf("target mix = %dD/%dF, want 2D/2F — BaselineMix's denominator is the GATE crew, not the raw input", plan.WantDelivery, plan.WantFactory)
	}
	if plan.HaveDelivery != 2 || plan.HaveFactory != 2 || plan.Unroled != 0 {
		t.Fatalf("census = %dD/%dF + %d unroled; a foreign hull is not a legacy hull and must not be counted as unroled", plan.HaveDelivery, plan.HaveFactory, plan.Unroled)
	}
	// Excluded, but not silently: dedicated_fleet is the single ownership column, so a foreign hull
	// arriving here is a wiring bug that must be visible rather than swallowed.
	if plan.Foreign != 1 {
		t.Fatalf("Foreign = %d, want 1 — an excluded hull that renders as nothing is a wiring bug nobody finds", plan.Foreign)
	}
	if !strings.Contains(plan.LogLine(), "1 foreign") {
		t.Fatalf("log line %q does not name the foreign hull it excluded", plan.LogLine())
	}
}

// ...and the UNDEDICATED empty tag is the same class. It is the more dangerous of the two at the
// call site, because it is what a hull with no dedicated_fleet at all reads as — and unroled hulls
// are considered FIRST, so a mis-fed one would be at the head of the adoption queue.
func TestPlanReallocation_AnUndedicatedHullIsNeitherAdoptedNorCountedInTheTargetMix(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("X-1", ""), // no dedicated_fleet: first in line if it were treated as legacy
			idleWorker("D-1", DeliveryFleetTag),
		},
		MaxMoves: 10,
	})

	for _, m := range plan.Moves {
		if m.Ship == "X-1" {
			t.Fatalf("move %+v re-tagged an UNDEDICATED hull into a gate role; dedicated_fleet is the single ownership column and this hull is not ours", m)
		}
	}
	// The positive half: the gate hull must still be borrowed. Excluding the intruder must not
	// stall the policy — "X-1 was not moved" alone is true of a planner that moved nothing.
	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "D-1" || plan.Moves[0].To != RoleFactory {
		t.Fatalf("moves = %v; the one real gate hull must still move to factory under the pause", movedShips(plan))
	}
	if plan.WantFactory != 1 {
		t.Fatalf("paused target = %dF over a 1-hull gate crew, want 1F — the paused target counts the GATE crew, not the raw input", plan.WantFactory)
	}
	if plan.Foreign != 1 || plan.Unroled != 0 {
		t.Fatalf("Foreign = %d, Unroled = %d; an undedicated hull is foreign, never a legacy hull awaiting adoption", plan.Foreign, plan.Unroled)
	}
}

// DWELL COVERAGE. The dwell guard's zero path emits no move and no skip, so a caller that never
// maintains the ship -> lastMovedAt ledger renders BYTE-IDENTICALLY to a healthy one and the guard
// is inert forever — while looking busier, not quieter, because an inert dwell means more role
// changes. This count is what separates the two, and it is the only in-package assertion that can:
// the ledger itself lives at the task-6 call site.
func TestReallocationPlan_DwellRecordCoverageIsCountedAndRendered(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Hour)},
			{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-2 * time.Hour)},
			idleWorker("D-3", DeliveryFleetTag), // no ledger entry
			idleWorker("X-1", "trade"),          // foreign: not in the crew, so not in the denominator
		},
		MaxMoves: 10,
	})

	if plan.DwellRecords != 2 {
		t.Fatalf("DwellRecords = %d, want 2 of the 3 gate hulls", plan.DwellRecords)
	}
	if line := plan.LogLine(); !strings.Contains(line, "dwell records 2/3") {
		t.Fatalf("log line %q does not render the dwell coverage; without it an inert ledger is indistinguishable from a kept one", line)
	}

	// THE FAILURE THIS EXISTS FOR: a caller rebuilding Workers from the DB every tick leaves every
	// LastMovedByUs zero. The plan is otherwise identical to a healthy one — same moves, same
	// skips — so 0/N is the ONLY signal that the guard is doing nothing.
	inert := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("D-3", DeliveryFleetTag),
			idleWorker("D-4", DeliveryFleetTag),
		},
		MaxMoves: 10,
	})
	if inert.DwellRecords != 0 {
		t.Fatalf("DwellRecords = %d for a crew with no ledger at all, want 0", inert.DwellRecords)
	}
	if line := inert.LogLine(); !strings.Contains(line, "dwell records 0/4") {
		t.Fatalf("log line %q does not read 0/4; an unmaintained ledger must be legible from the very first tick", line)
	}
}

// GUARD ORDER. A held hull reports its OWN reason, not the tick's spent budget. Checking the cap
// first labels everything past it `move_cap` whatever its real state — so a permanently busy hull
// reports `move_cap` forever and never surfaces as stuck, and two hulls in different states get the
// same reason purely from their slice position. Making a stall legible is what these strings exist
// for, so the misattribution is a defect here, not cosmetics.
func TestPlanReallocation_AHeldHullReportsItsOwnReasonNotTheSpentMoveCap(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),                                                                // spends the cap
			{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: false},                                             // busy
			{Ship: "D-3", FleetTag: DeliveryFleetTag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Second)}, // inside the dwell
			idleWorker("D-4", DeliveryFleetTag),                                                                // genuinely held by the cap
		},
		Dwell:    10 * time.Minute,
		MaxMoves: 1,
	})

	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "D-1" {
		t.Fatalf("moves = %v, want exactly D-1 under a cap of 1", movedShips(plan))
	}
	for _, tc := range []struct{ ship, want string }{
		{"D-2", MoveSkipBusy},
		{"D-3", MoveSkipDwell},
		{"D-4", MoveSkipMoveCap},
	} {
		if got := skipReason(plan, tc.ship); got != tc.want {
			t.Fatalf("skip reason for %s = %q, want %q — the cap is the tick's budget and must rule LAST, after each hull's own state", tc.ship, got, tc.want)
		}
	}
}

// The held-list is one entry per wanted-but-blocked hull on every drain tick, and it is unbounded
// in fleet size. Truncate the list; keep the COUNT exact, because the count is the diagnosis.
func TestReallocationPlan_LogLineTruncatesAnOversizedHeldList(t *testing.T) {
	workers := make([]Worker, 0, 20)
	for i := 1; i <= 20; i++ {
		workers = append(workers, idleWorker(fmt.Sprintf("D-%02d", i), DeliveryFleetTag))
	}

	line := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        workers,
		MaxMoves:       1,
	}).LogLine()

	if !strings.Contains(line, "held 8 of 19:") {
		t.Fatalf("log line %q does not report the held count exactly; a truncated list that hides how many were held destroys the diagnosis it exists for", line)
	}
	// D-01 spends the cap, so the 19 skips are D-02..D-20 and the 8 shown are D-02..D-09.
	for _, absent := range []string{"D-10", "D-20"} {
		if strings.Contains(line, absent) {
			t.Fatalf("log line %q still names %s; the held list must be capped at %d entries", line, absent, maxLoggedSkips)
		}
	}
}

// THE PHANTOM DEFICIT — the defect the roled-census target removes at the root.
//
// An earlier shape targeted `BaselineMix` over EVERY gate hull. Unroled hulls are in no role
// census, so they inflated the target into a deficit no re-role could satisfy — and because a guard
// skip does not consume the need the skipped hull was selected for, a BLOCKED unroled hull pushed
// that deficit onto a roled hull, which chased it, INVERTING it, which the same hull chased back on
// the next tick. Held unpaused with the crew unchanged, that ping-ponged one hull delivery<->factory
// forever. The dwell only ever masked it, at one role-flip per dwell period, and a hull abandoning
// its role on a timer is precisely the unpredictability this operation exists to remove.
//
// The target now describes only the hulls whose role this planner governs, so it is REACHABLE and
// a blocked hull is simply reported as blocked.
func TestPlanReallocation_ABlockedUnroledHullDoesNotPushAPhantomDeficitOntoARoledHull(t *testing.T) {
	ledger := map[string]time.Time{}
	tag := DeliveryFleetTag
	now := reallocNow
	moves := 0

	for tick := 0; tick < 8; tick++ {
		plan := PlanReallocation(ReallocationInput{
			Now:            now,
			DeliveryPaused: false, // held unpaused throughout: no flapping signal to blame
			Workers: []Worker{
				{Ship: "L-1", FleetTag: LegacyFleetTag, Idle: false, LastMovedByUs: ledger["L-1"]},
				{Ship: "D-1", FleetTag: tag, Idle: true, LastMovedByUs: ledger["D-1"]},
			},
			Dwell:    time.Hour,
			MaxMoves: 1,
		})

		// Asserted EVERY tick, because the defect showed up as an inversion between consecutive
		// ticks rather than as a wrong value on any one of them.
		if plan.WantDelivery != 1 || plan.WantFactory != 0 {
			t.Fatalf("tick %d: target = %dD/%dF against a roled census of 1D/0F; an unroled hull must not inflate a target it cannot satisfy", tick, plan.WantDelivery, plan.WantFactory)
		}
		if got := skipReason(plan, "L-1"); got != MoveSkipBusy {
			t.Fatalf("tick %d: the blocked legacy hull reported %q, want %q — removing the phantom deficit must not cost the stall its legibility", tick, got, MoveSkipBusy)
		}
		if got := skipReason(plan, "D-1"); got != "" {
			t.Fatalf("tick %d: D-1 was recorded as held (%q); it is already where the target wants it, so it was never a decision", tick, got)
		}

		for _, m := range plan.Moves {
			moves++
			ledger[m.Ship] = now
			if m.Ship == "D-1" {
				tag = m.To.FleetTag()
			}
		}
		now = now.Add(30 * time.Second)
	}

	if moves != 0 {
		t.Fatalf("D-1 moved %d times across 8 unpaused ticks with an unchanged crew and a satisfied target; a blocked unroled hull must not make a roled hull chase a deficit that does not exist", moves)
	}
}

// THE DWELL GATES THE BORROW, NEVER THE RETURN. Both halves are pinned together, because it is the
// ASYMMETRY that is load-bearing and a regression would most likely restore the symmetry.
//
// A dwell-locked return leaves the fleet running with ZERO delivery hulls for the rest of the
// window after even a short pause — a revenue stall in exactly the path this phase exists to
// unblock. The borrow keeps its guard because that is the direction a flapping pause signal can
// re-fire.
func TestPlanReallocation_TheDwellGatesTheBorrowButNotTheReturn(t *testing.T) {
	justMoved := func(ship, tag string) Worker {
		return Worker{Ship: ship, FleetTag: tag, Idle: true, LastMovedByUs: reallocNow.Add(-time.Second)}
	}

	// RETURN: a short pause borrowed all four hulls to factory one second ago. They are deep inside
	// the dwell and must come back regardless.
	resume := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: false,
		Workers: []Worker{
			justMoved("H-1", FactoryFleetTag), justMoved("H-2", FactoryFleetTag),
			justMoved("H-3", FactoryFleetTag), justMoved("H-4", FactoryFleetTag),
		},
		Dwell:    10 * time.Minute,
		MaxMoves: 10,
	})
	if len(resume.Moves) != 2 {
		t.Fatalf("moves = %v one second into a 10-minute dwell, want the 2 hulls the baseline is short of; a dwell-locked return resumes delivery with ZERO delivery hulls", movedShips(resume))
	}
	for _, m := range resume.Moves {
		if m.To != RoleDelivery || m.Reason != MoveReasonResumeToBaseline {
			t.Fatalf("move %+v; the ungated direction is the return to baseline specifically, not every move", m)
		}
	}
	for _, s := range resume.Skips {
		if s.Reason == MoveSkipDwell {
			t.Fatalf("skips = %+v; nothing may be held by the dwell on the return leg", resume.Skips)
		}
	}

	// BORROW: the same freshly-moved hulls, taken TO factory under a pause. Still gated.
	borrow := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			justMoved("D-1", DeliveryFleetTag), justMoved("D-2", DeliveryFleetTag),
			justMoved("F-1", FactoryFleetTag), justMoved("F-2", FactoryFleetTag),
		},
		Dwell:    10 * time.Minute,
		MaxMoves: 10,
	})
	if len(borrow.Moves) != 0 {
		t.Fatalf("moves = %v one second into a 10-minute dwell; the BORROW must stay gated, or a flapping pause signal oscillates the whole workforce", movedShips(borrow))
	}
	for _, ship := range []string{"D-1", "D-2"} {
		if got := skipReason(borrow, ship); got != MoveSkipDwell {
			t.Fatalf("skip reason for %s = %q, want %q", ship, got, MoveSkipDwell)
		}
	}
}

// ADOPTION IS THE TARGET'S OWN INCREMENT — which is the whole answer to "what role does a hull
// adopted under a PAUSE get?". There is no second mix rule and no pause special case: adoption
// follows the same target everything else follows, and that target simply evaluates differently in
// the two states. Unpaused that is NextRole, because the baseline is GENERATED by NextRole; paused
// it is factory, because the paused target is all-factory.
//
// Pinned as an identity over both states and every partial N, so the two can never drift.
func TestPlanReallocation_AdoptionTargetIsExactlyWhatTheTargetGainsFromOneMoreHull(t *testing.T) {
	for _, paused := range []bool{false, true} {
		for roled := 0; roled < 9; roled++ {
			beforeDelivery, beforeFactory := roleTarget(paused, roled)
			afterDelivery, afterFactory := roleTarget(paused, roled+1)

			var gained Role
			switch {
			case afterDelivery == beforeDelivery+1 && afterFactory == beforeFactory:
				gained = RoleDelivery
			case afterFactory == beforeFactory+1 && afterDelivery == beforeDelivery:
				gained = RoleFactory
			default:
				t.Fatalf("paused=%v roled=%d: target went %dD/%dF -> %dD/%dF; one more hull must add exactly one role",
					paused, roled, beforeDelivery, beforeFactory, afterDelivery, afterFactory)
			}
			if got := adoptionTarget(paused, beforeDelivery, beforeFactory); got != gained {
				t.Fatalf("paused=%v roled=%d: adoptionTarget = %v, want %v — adoption that is not the target's own increment is a second mix rule, and it opens a deficit the same tick it fills one",
					paused, roled, got, gained)
			}
		}
	}
}

// ...and a fleet adopted entirely UNDER a pause lands on the same baseline as one that never
// paused. This is the half worth proving: BaselineMix is re-derived from the LIVE roled count every
// tick rather than accumulated from a history, so how the roles were first handed out cannot change
// where the fleet ends up. Adopting via NextRole under a pause instead would hand out delivery roles
// the paused target immediately wants moved to factory — a wasted move per adoption, and a fresh
// churn source from the fix meant to remove one.
func TestPlanReallocation_AFleetAdoptedUnderAPauseConvergesToTheSameBaselineAsOneThatNeverPaused(t *testing.T) {
	ships := []string{"H-1", "H-2", "H-3", "H-4"}
	allLegacy := func() []string {
		return []string{LegacyFleetTag, LegacyFleetTag, LegacyFleetTag, LegacyFleetTag}
	}

	neverPaused := driveToQuiescence(t, false, ships, allLegacy())

	underPause := driveToQuiescence(t, true, ships, allLegacy())
	for i, tag := range underPause {
		if tag != FactoryFleetTag {
			t.Fatalf("%s = %q after adoption under a pause; the paused target is all-factory, so that is what adoption assigns", ships[i], tag)
		}
	}
	resumed := driveToQuiescence(t, false, ships, underPause)

	if mixOf(resumed) != mixOf(neverPaused) {
		t.Fatalf("a paused cold start settled at %s and an unpaused one at %s; the baseline is re-derived from the live roled count, so how the roles were first assigned must not change where the fleet lands",
			mixOf(resumed), mixOf(neverPaused))
	}
	if got := mixOf(neverPaused); got != "2D/2F" {
		t.Fatalf("cold-start baseline = %s, want the 2D/2F D/F/F/D baseline for four hulls", got)
	}
}

// ADOPTION OUTRANKS A RE-ROLE FOR THE TICK'S ONE MOVE. An unroled hull does no gate work at all
// until it is adopted, so a fleet that spends every tick's move re-roling never arms — the ramp
// buys nothing when four legacy hulls already exist, so no role tag would ever be written.
//
// The legacy hull is placed LAST in the input deliberately: input order must not decide this.
func TestPlanReallocation_AdoptionOutranksAReRoleForTheTicksOneMove(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: false,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag), // the roled census is off its baseline, so both
			idleWorker("D-2", DeliveryFleetTag), // of these want a re-role this tick
			idleWorker("L-1", LegacyFleetTag),
		},
		MaxMoves: 1,
	})

	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "L-1" {
		t.Fatalf("moves = %v; the tick's one move must go to the unadopted hull, or the arming starves behind re-role churn", movedShips(plan))
	}
	if plan.Moves[0].Reason != MoveReasonLegacyAdoption {
		t.Fatalf("move %+v reason = %q, want %q", plan.Moves[0], plan.Moves[0].Reason, MoveReasonLegacyAdoption)
	}
}

// An adoption GROWS the roled population, so it must grow the TARGET with it in the same tick.
// Otherwise a re-role decided after it reads a target one hull too small, sees no deficit, and the
// fleet settles off its baseline — the mirror of the phantom deficit, an inch in the other
// direction. Lifting the cap puts the whole correction in one tick, so the END STATE is assertable
// rather than the pacing.
func TestPlanReallocation_AnAdoptionGrowsTheTargetInTheSameTick(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: false,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("L-1", LegacyFleetTag),
		},
		MaxMoves: 10,
	})

	// Three roled hulls once L-1 is adopted, so the target is BaselineMix(3) = 1D/2F.
	if plan.WantDelivery != 1 || plan.WantFactory != 2 {
		t.Fatalf("target = %dD/%dF after adopting one hull into a 2-hull roled census, want BaselineMix(3) = 1D/2F", plan.WantDelivery, plan.WantFactory)
	}
	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %v, want the adoption AND the re-role the grown target implies; a target that lags its own adoption leaves the fleet off baseline", movedShips(plan))
	}
}

// driveToQuiescence runs the planner until it emits no moves, re-feeding its own decisions exactly
// as the drain does, and returns the settled tag per ship in the input order.
func driveToQuiescence(t *testing.T, paused bool, ships, tags []string) []string {
	t.Helper()
	settled := append([]string(nil), tags...)
	now := reallocNow
	for tick := 0; tick < 24; tick++ {
		workers := make([]Worker, 0, len(ships))
		for i, ship := range ships {
			workers = append(workers, idleWorker(ship, settled[i]))
		}
		plan := PlanReallocation(ReallocationInput{Now: now, DeliveryPaused: paused, Workers: workers, MaxMoves: 1})
		if len(plan.Moves) == 0 {
			return settled
		}
		for _, m := range plan.Moves {
			for i, ship := range ships {
				if ship == m.Ship {
					settled[i] = m.To.FleetTag()
				}
			}
		}
		now = now.Add(30 * time.Second)
	}
	t.Fatalf("reallocation never settled (paused=%v): tags = %v", paused, settled)
	return nil
}

func mixOf(tags []string) string {
	delivery, factory := 0, 0
	for _, tag := range tags {
		switch tag {
		case DeliveryFleetTag:
			delivery++
		case FactoryFleetTag:
			factory++
		}
	}
	return fmt.Sprintf("%dD/%dF", delivery, factory)
}

// OBSERVABILITY. The reallocation must be diagnosable from the log alone: paused state, the
// target mix, the census, every move, and every hull held back with its reason — in the MESSAGE,
// because the container log renderer drops metadata maps.
func TestReallocationPlan_LogLineNamesThePauseTheTargetTheMovesAndTheHolds(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: false},
		},
		MaxMoves: 10,
	})

	line := plan.LogLine()
	for _, want := range []string{"PAUSED", "D-1", "factory", "D-2", MoveSkipBusy} {
		if !strings.Contains(line, want) {
			t.Fatalf("reallocation log line %q does not name %q", line, want)
		}
	}

	// A SETTLED fleet and a BROKEN reallocator must not look identical, and "the line is
	// non-empty" does not separate them — any string passes that. What separates them is the
	// census against the target: a settled fleet says have == want and no role changes, so an
	// operator reading a quiet line can tell it was quiet ON PURPOSE.
	quiet := PlanReallocation(ReallocationInput{
		Now:     reallocNow,
		Workers: []Worker{idleWorker("D-1", DeliveryFleetTag), idleWorker("F-1", FactoryFleetTag)},
	}).LogLine()
	for _, want := range []string{"running", "have 1D/1F", "want 1D/1F", "no role changes"} {
		if !strings.Contains(quiet, want) {
			t.Fatalf("settled-fleet log line %q does not name %q; a settled fleet and a broken reallocator must not look identical", quiet, want)
		}
	}
}
