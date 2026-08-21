package gate

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultRoleDwell is the minimum time a hull stays in a role before it may be moved again.
	// It is the reallocation's own hysteresis: the buy/resume floors stop supply chatter, this
	// stops the WORKFORCE chatter one level up.
	//
	// The spec directs reuse of the worker_rebalancer's ferry cooldown and max-concurrent-ferry
	// knobs. Those knobs are DEAD, verified in this tree: the coordinator was deleted in 712b6f66
	// (sp-hoj8u), `resolveWorkerRebalancerConfig` survives only as two COMMENTS and is not a
	// function, DaemonServer.workerRebalancerConfig is WRITTEN (daemon_server.go, from a constructor
	// parameter) and READ BY NOTHING — the read side is the load-bearing half and the only one that
	// makes the field dead, WorkerRebalancerConfig.EnabledOrDefault has zero callers, and
	// worker_rebalancer_coordinator sits in retiredCommandTypes with no builder and no launch site.
	// ferry_cooldown_seconds and max_concurrent_ferries have exactly TWO declarations in the Go
	// tree, both mapstructure tags in internal/infrastructure/config/worker_rebalancer.go.
	//
	// (An earlier revision of this paragraph said "four lines in the Go tree, all declarations". The
	// search does return four — but two of them are these very sentences. A grep count that includes
	// the prose doing the counting is not a measurement, and it was inflating the evidence for the
	// conclusion by 2x. The conclusion is unchanged and independently re-verified.)
	//
	// They are also the wrong SHAPE, which is the part that would still bite if someone revived
	// them: they govern a cross-system ferry of UNDEDICATED light haulers, and
	// ship_pool_manager.go's `if ship.DedicatedFleet() != "" { continue }` excludes every gate
	// hull from that pool by construction. So this policy declares its own constraints.
	//
	// 10m against a 30s drain tick: a supply level that dips and recovers inside ten minutes
	// costs zero role changes. Tunables' defaults, not feature flags — an unset value resolves
	// here rather than disabling the guard.
	//
	// It gates the BORROW ONLY, never the return to baseline — see the dwell guard in
	// PlanReallocation for why that is both safe and necessary.
	DefaultRoleDwell = 10 * time.Minute

	// DefaultMaxRoleMovesPerTick bounds how far one tick may swing the workforce. A move spends
	// nothing, but a burst would re-role the whole fleet on a single noisy observation; at 1 a
	// 4-hull fleet still converges in under two minutes.
	DefaultMaxRoleMovesPerTick = 1

	// maxLoggedSkips bounds the held-list in LogLine. One entry per wanted-but-blocked hull, on
	// every drain tick: trivial at the designed 4-hull fleet, unbounded in fleet size. The COUNT
	// stays exact when the list is trimmed, so a spent move cap still reads as "8 of 19" rather
	// than turning one log line into a page.
	maxLoggedSkips = 8
)

// Move reasons. A role change with no stated cause is exactly the opacity this design exists to
// remove — an operator seeing a hull flip roles must be able to tell a pause from an adoption.
const (
	MoveReasonLegacyAdoption        = "legacy hull adopted into a role fleet"
	MoveReasonPauseToFactory        = "delivery paused — every gate material is below its buy floor"
	MoveReasonResumeToBaseline      = "delivery resumed — returning to the D/F/F/D baseline mix"
	MoveReasonFactoryIdleToDelivery = "factory has nothing to feed — redirecting to delivery"
)

// Skip reasons: why a hull the target mix WANTED moved was held back. Only actionable declines
// are recorded — a hull already where the target wants it is not a decision.
const (
	MoveSkipBusy    = "busy"
	MoveSkipDwell   = "dwell"
	MoveSkipMoveCap = "move_cap"
)

// Worker is one gate hull as the reallocator sees it.
//
// Idle means idle AND not in transit AND not held by a live supply worker — the caller collapses
// all three, because all three mean the same thing here: something is mid-haul with this hull.
//
// LastMovedByUs is when THIS PROCESS last moved the hull, and the CALLER owns it. It is the only
// one of the four fields that is NOT derivable from a single read of the ship, so a caller that
// builds Workers straight from the DB each tick leaves it zero forever, every hull reads "never
// moved by us", and the dwell guard is inert — while looking fine, because an inert dwell produces
// MORE role changes, which reads as activity rather than as a broken guard. The caller needs an
// in-memory ship -> lastMovedAt ledger, written when it executes a Move. ReallocationPlan
// .DwellRecords counts how many hulls carry one and LogLine renders it, so an unmaintained ledger
// reads "dwell records 0/4" from the very first tick instead of rendering identically to a healthy
// one.
//
// The zero value means "never moved by us" and is eligible IMMEDIATELY: the dwell clock is the
// drain's, not the ship's, and refusing on an absent record would deadlock every reallocation
// after a restart. Losing the ledger on restart costs at most one move and never a spend — the
// same trade phase 2 made for pause state.
type Worker struct {
	Ship          string
	FleetTag      string
	Idle          bool
	LastMovedByUs time.Time
}

// Move is one role change to execute through AssignFleet — the single write path for the
// dedicated_fleet column (RULINGS #3).
type Move struct {
	Ship string
	// From is the hull's live fleet tag, for the log. It is NOT an idempotence check: moveTarget
	// returns wanted=false whenever a roled hull already carries the target role, and an unroled
	// hull cannot carry it by definition, so From != To.FleetTag() is an invariant of every
	// emitted Move and comparing the two at the write site can only ever pass. Catching a tag
	// that went stale between plan and write takes a FRESH read there, not this field.
	From   string
	To     Role
	Reason string
}

// MoveSkip is one hull the target mix wanted moved and a guard held back.
type MoveSkip struct {
	Ship   string
	Reason string
}

// ReallocationInput is one tick's view. Now is passed in rather than read, so the policy stays
// pure and the dwell is testable without a sleep.
type ReallocationInput struct {
	Now time.Time
	// DeliveryPaused is EVERY gate material paused, never any one of them — the caller supplies
	// it from BuyPolicy.FleetPaused, which already rules that way and is pinned there.
	//
	// A hull fills greedily from whatever is eligible, so delivery still has useful work while
	// one material is buyable; moving workers then would starve delivery of capacity it can
	// still use. Getting it backwards idles the fleet whenever a single material dips.
	DeliveryPaused bool
	// FactoryHasNoWork mirrors DeliveryPaused for the opposite end of the pipeline: does the
	// factory role have any feedable step right now, for any outstanding gate material. The caller
	// derives it from the SAME live check feedGateLeg itself uses (planGateFeed), never a cached or
	// task-level heuristic.
	//
	// Not structurally exclusive with DeliveryPaused — a material can be too scarce to buy directly
	// while its own precursor chain is also unfeedable. roleTarget checks DeliveryPaused first, so
	// a caller asserting both lands on the established, dwell-protected recovery state.
	FactoryHasNoWork bool
	Workers          []Worker
	Dwell            time.Duration
	MaxMoves         int
}

// ReallocationPlan is the tick's ruling, materialized.
type ReallocationPlan struct {
	DeliveryPaused bool
	// FactoryHasNoWork is carried straight from ReallocationInput so LogLine can name why an
	// unpaused target is all-delivery rather than the baseline.
	FactoryHasNoWork bool
	// WantDelivery and WantFactory describe the ROLED population only, and they are reported AFTER
	// this tick's adoptions: an adoption grows the roled population, so it grows the target with
	// it. HaveDelivery/HaveFactory/Unroled are the census as it stood at the START of the tick, so
	// a log line reads "the census we ruled on, the target this tick's adoptions leave us with,
	// and the moves that get there".
	WantDelivery int
	WantFactory  int
	HaveDelivery int
	HaveFactory  int
	Unroled      int
	// Foreign is how many of the input workers are not gate hulls at all — a foreign fleet tag or
	// the undedicated empty one. They are excluded from the census and from the candidate list,
	// and counted here rather than silently dropped: dedicated_fleet is the single ownership
	// column (RULINGS #3), so a foreign hull arriving in Workers is a wiring bug, and a wiring bug
	// that renders as nothing is a wiring bug nobody finds.
	Foreign int
	// DwellRecords is how many gate hulls carry a non-zero Worker.LastMovedByUs, against a crew of
	// HaveDelivery+HaveFactory+Unroled. It exists to make an INERT dwell guard visible: the
	// guard's zero path emits no move and no skip, so without this count a caller that never
	// maintains the ledger produces a log line byte-identical to a healthy one. A steady 0/N says
	// the ledger is not being kept and the dwell is doing nothing at all.
	DwellRecords int
	Moves        []Move
	Skips        []MoveSkip
}

// LogLine renders the whole ruling for the container log — everything in the MESSAGE, because
// the container log renderer drops metadata maps.
func (p ReallocationPlan) LogLine() string {
	// paused before factoryHasNoWork, matching roleTarget's own precedence.
	state := "running"
	switch {
	case p.DeliveryPaused:
		state = "PAUSED"
	case p.FactoryHasNoWork:
		state = "running, factory has no work"
	}
	moves := make([]string, 0, len(p.Moves))
	for _, m := range p.Moves {
		moves = append(moves, fmt.Sprintf("%s %s->%s (%s)", m.Ship, m.From, m.To, m.Reason))
	}

	moveText := "no role changes"
	if len(moves) > 0 {
		moveText = strings.Join(moves, "; ")
	}
	crew := p.HaveDelivery + p.HaveFactory + p.Unroled
	line := fmt.Sprintf(
		"Gate roles (delivery %s): have %dD/%dF + %d unroled + %d foreign, want %dD/%dF, dwell records %d/%d — %s",
		state, p.HaveDelivery, p.HaveFactory, p.Unroled, p.Foreign,
		p.WantDelivery, p.WantFactory, p.DwellRecords, crew, moveText)
	if len(p.Skips) == 0 {
		return line
	}

	shown, prefix := p.Skips, "; held "
	if len(shown) > maxLoggedSkips {
		shown = shown[:maxLoggedSkips]
		prefix = fmt.Sprintf("; held %d of %d: ", maxLoggedSkips, len(p.Skips))
	}
	held := make([]string, 0, len(shown))
	for _, s := range shown {
		held = append(held, fmt.Sprintf("%s: %s", s.Ship, s.Reason))
	}
	return line + prefix + strings.Join(held, ", ")
}

// BaselineMix is the role split the D/F/F/D purchase order would have produced for n hulls.
//
// Derived by RUNNING NextRole rather than by a formula, deliberately: the purchase order is
// already pinned at every partial N by the role tests, and a second expression of the same rule
// is a second thing to drift. A negative census answers 0/0 rather than extrapolating.
func BaselineMix(n int) (delivery, factory int) {
	for i := 0; i < n; i++ {
		if NextRole(delivery, factory) == RoleDelivery {
			delivery++
			continue
		}
		factory++
	}
	return delivery, factory
}

// PlanReallocation rules on this tick's workforce split.
//
// THE TARGET IS A BASELINE, NOT A DIRECTION. A literal reading of "when delivery unpauses its
// workers move back" returns EVERY factory hull to delivery — including the two the purchase
// order bought as factory hulls — which empties the factory fleet, starves the terminal
// factories, and re-trips the pause. That is a thrash no dwell timer can fix, so the unpaused
// target is the D/F/F/D baseline and convergence is capped per tick.
//
// Paused, the target is ALL FACTORY. That is what makes the pause self-shortening: delivery
// pauses because a terminal factory is low, those hulls go feed it, it produces faster, supply
// recovers sooner, delivery resumes. It is also what makes an aggressive buy floor safe — over-
// buying costs a reallocation, not a stall.
//
// UNPAUSED WITH THE FACTORY ROLE IDLE, the target is ALL DELIVERY — paused's mirror image: instead
// of sending the roled population to feed a starved factory, it sends them to deliver because there
// is nothing left to feed. See ReallocationInput.FactoryHasNoWork for the signal and the precedence
// when a caller asserts both.
//
// THE TARGET DESCRIBES THE ROLED POPULATION, AND ADOPTION IS NOT A DEFICIT DECISION. These are
// one rule, and getting them apart is what makes the whole thing stable.
//
// An earlier shape targeted BaselineMix over EVERY gate hull, unroled ones included. Unroled hulls
// are in no role census, so they inflated the target into a deficit that no re-role could satisfy
// — and because a guard skip does not consume the need the skipped hull was selected for, a
// BLOCKED unroled hull pushed its phantom deficit onto a roled hull, which chased it, INVERTING
// the deficit to the other role, which the same hull chased back next tick. Measured: one hull
// ping-ponging delivery<->factory forever, with the pause signal held constant and the crew
// unchanged. The dwell masked it at one role-flip per dwell period rather than fixing it, and a
// hull abandoning its role on a timer is exactly the unpredictability this operation exists to
// remove.
//
// So the target is BaselineMix over the ROLED census, which sums to that census by construction —
// the target is always REACHABLE, and needDelivery/needFactory are exact negatives of each other.
// Every shape roleTarget can return sums to the same roled census for the same reason, which is
// what makes a third shape safe to add. Unroled hulls are ADOPTED instead: purely additive, never
// re-roling an already-correct hull, and baseline-PRESERVING, because adoptionTarget is by
// construction the role the target itself gains when the roled population grows by one. Adoption
// therefore moves the census and the target in lockstep and opens no deficit at all.
//
// UNROLED hulls are still considered FIRST, and for ONE reason: adoption must outrank a re-role for
// the tick's one move, or the arming starves — on a fleet already holding four legacy hulls the
// purchase ramp buys nothing, so no role tag is ever written and the whole two-fleet feature stays
// inert. It is NOT needed to keep the target final for a deficit read; the lockstep property above
// already guarantees that in either order. See unroledFirst.
func PlanReallocation(in ReallocationInput) ReallocationPlan {
	plan := ReallocationPlan{DeliveryPaused: in.DeliveryPaused, FactoryHasNoWork: in.FactoryHasNoWork}

	// MEMBERSHIP FIRST. ParseFleetTag's ok=false covers THREE distinct classes — the legacy gate
	// tag, a FOREIGN fleet tag, and the undedicated empty tag — and only the first is ours to
	// adopt. IsGateFleetTag is the package's designated re-tag membership test (role.go); this is
	// the fourth re-tag decision in the tree and the other three already gate on it.
	//
	// The filter is load-bearing even when the intruder is never selected, because the target is
	// BaselineMix over the census: one foreign hull beside four gate hulls asks for
	// BaselineMix(5) = 3D/2F, and the planner then re-tags a FACTORY hull to chase a delivery
	// deficit that does not exist.
	crew := make([]Worker, 0, len(in.Workers))
	for _, worker := range in.Workers {
		if !IsGateFleetTag(worker.FleetTag) {
			plan.Foreign++
			continue
		}
		crew = append(crew, worker)
	}
	if len(crew) == 0 {
		return plan
	}

	dwell := in.Dwell
	if dwell <= 0 {
		dwell = DefaultRoleDwell
	}
	maxMoves := in.MaxMoves
	if maxMoves <= 0 {
		maxMoves = DefaultMaxRoleMovesPerTick
	}

	for _, worker := range crew {
		if !worker.LastMovedByUs.IsZero() {
			plan.DwellRecords++
		}
		role, roled := ParseFleetTag(worker.FleetTag)
		switch {
		case !roled:
			plan.Unroled++
		case role == RoleDelivery:
			plan.HaveDelivery++
		default:
			plan.HaveFactory++
		}
	}

	roled := plan.HaveDelivery + plan.HaveFactory
	plan.WantDelivery, plan.WantFactory = roleTarget(in.DeliveryPaused, in.FactoryHasNoWork, roled)

	haveDelivery, haveFactory := plan.HaveDelivery, plan.HaveFactory
	for _, worker := range unroledFirst(crew) {
		role, isRoled := ParseFleetTag(worker.FleetTag)

		target := adoptionTarget(in.DeliveryPaused, in.FactoryHasNoWork, haveDelivery, haveFactory)
		if isRoled {
			var wanted bool
			target, wanted = moveTarget(role, plan.WantDelivery-haveDelivery, plan.WantFactory-haveFactory)
			if !wanted {
				continue // already where the target wants it: not a decision, so not recorded
			}
		}
		// The hull's OWN state rules before the tick's BUDGET. Checking the cap first would label
		// every hull past the cap `move_cap` whatever its real state, so a permanently busy hull
		// would report `move_cap` forever and never surface as stuck — and two hulls in different
		// states would get the same reason purely from their slice position. Making a stall
		// legible is what the skip vocabulary is for, so busy and dwell rule first, cap last.
		if !worker.Idle {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipBusy})
			continue
		}
		// THE DWELL GATES ENTRY INTO EITHER SPECIAL STATE — factory under a pause, or delivery
		// under FactoryHasNoWork — and NEVER the return to baseline, which stays immediate so a
		// short trigger cannot strand the fleet short of a whole role for a full dwell window.
		//
		// Safe for a structural reason: the target sums to the roled census, so moveTarget only ever
		// WANTS hulls in the surplus role and a re-role walks the deficit to zero without
		// overshooting, whichever direction is gated.
		//
		// FactoryHasNoWork is gated the SAME way rather than left immediate, because unlike
		// DeliveryPaused — which BuyPolicy already runs through its own buy/resume hysteresis — it is
		// a fresh read every tick against a live ask and live treasury headroom, either of which can
		// cross the reserve line and back with no real market event.
		borrow := isRoled && (in.DeliveryPaused || in.FactoryHasNoWork)
		if borrow && !worker.LastMovedByUs.IsZero() && in.Now.Sub(worker.LastMovedByUs) < dwell {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipDwell})
			continue
		}
		if len(plan.Moves) >= maxMoves {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipMoveCap})
			continue
		}

		plan.Moves = append(plan.Moves, Move{
			Ship:   worker.Ship,
			From:   worker.FleetTag,
			To:     target,
			Reason: moveReason(in.DeliveryPaused, in.FactoryHasNoWork, isRoled),
		})
		if isRoled {
			// A re-role is a TRANSFER inside the roled population: its size is unchanged, so the
			// target is unchanged.
			if role == RoleDelivery {
				haveDelivery--
			} else {
				haveFactory--
			}
		} else {
			// An adoption GROWS the roled population, so the target grows with it — and grows by
			// exactly the role adoptionTarget just assigned, which is what makes adoption open no
			// deficit. Every adoption is decided before any re-role (unroledFirst), so the target
			// is final by the time a deficit is read.
			roled++
			plan.WantDelivery, plan.WantFactory = roleTarget(in.DeliveryPaused, in.FactoryHasNoWork, roled)
		}
		if target == RoleDelivery {
			haveDelivery++
		} else {
			haveFactory++
		}
	}
	return plan
}

// roleTarget is the split this planner drives the ROLED population toward. It sums to roled in
// EVERY state (BaselineMix(n) sums to n; the paused target is 0+n; the all-delivery target is n+0),
// which is what makes the target reachable and the needs exact negatives.
//
// Paused, the target is ALL FACTORY; under FactoryHasNoWork, the mirror, ALL DELIVERY; otherwise
// the D/F/F/D baseline. Paused is checked first, so a caller asserting both gets all-factory — see
// ReallocationInput.FactoryHasNoWork for why.
func roleTarget(paused, factoryHasNoWork bool, roled int) (delivery, factory int) {
	switch {
	case paused:
		return 0, roled
	case factoryHasNoWork:
		return roled, 0
	default:
		return BaselineMix(roled)
	}
}

// adoptionTarget is the role the CURRENT target assigns to one more hull — literally
// roleTarget(paused, factoryHasNoWork, roled+1) minus roleTarget(paused, factoryHasNoWork, roled),
// expressed directly, so there is no second mix rule for either trigger to drift from. Paused it is
// factory; under FactoryHasNoWork, delivery; otherwise NextRole, matching roleTarget's precedence.
func adoptionTarget(paused, factoryHasNoWork bool, haveDelivery, haveFactory int) Role {
	switch {
	case paused:
		return RoleFactory
	case factoryHasNoWork:
		return RoleDelivery
	default:
		return NextRole(haveDelivery, haveFactory)
	}
}

// unroledFirst orders the GATE crew: legacy/unroled hulls, then the rest in input order. Stable
// and deterministic, so a tick's ruling is reproducible from its inputs alone. Callers pass the
// IsGateFleetTag-filtered crew, so "unroled" here means the legacy tag and nothing else.
//
// LOAD-BEARING FOR ONE REASON, NOT TWO: adoption must outrank a re-role for the tick's one move. A
// fleet that keeps re-roling never adopts, and an unadopted legacy hull does no gate work at all,
// so the arming starves behind cosmetic churn.
//
// This comment used to give a second reason — that a roled hull decided before an adoption "reads a
// target that a later adoption then grows". The reading is accurate and the hazard is not, and
// stating it as a hazard hides the property that actually makes the ordering safe to reason about:
// adoption is baseline-PRESERVING, so it moves the roled census and the target in LOCKSTEP and opens
// no deficit at all. A re-role decided first therefore reads a target that is exactly right for the
// census it saw, and the later adoption does not invalidate it — both orders converge on the same
// mix, differing only in WHICH hull spends the tick's move. That is the reason above, and it is the
// whole of it.
func unroledFirst(workers []Worker) []Worker {
	ordered := make([]Worker, 0, len(workers))
	for _, worker := range workers {
		if _, roled := ParseFleetTag(worker.FleetTag); !roled {
			ordered = append(ordered, worker)
		}
	}
	for _, worker := range workers {
		if _, roled := ParseFleetTag(worker.FleetTag); roled {
			ordered = append(ordered, worker)
		}
	}
	return ordered
}

// moveTarget answers which role (if any) an already-ROLED hull should move to.
//
// The target sums to the roled census by construction, so needDelivery and needFactory are exact
// negatives of each other and at most one is ever positive: there is no both-short case to break
// a tie in. That case only ever arose from the phantom deficit, and only ADOPTION grows the roled
// population — which is not a deficit decision at all and goes through adoptionTarget instead.
//
// Consequently a re-role walks the deficit to zero one hull at a time and cannot overshoot: each
// move takes needFactory one step toward 0, and at 0 every remaining hull returns wanted=false.
// It takes needs, not state, so a new roleTarget shape never touches this function.
func moveTarget(role Role, needDelivery, needFactory int) (Role, bool) {
	var target Role
	switch {
	case needFactory > 0:
		target = RoleFactory
	case needDelivery > 0:
		target = RoleDelivery
	default:
		return 0, false
	}
	if role == target {
		return 0, false
	}
	return target, true
}

// moveReason names WHY, so a role change is diagnosable from the log without a code read. roled is
// checked first and alone decides adoption, independent of paused/factoryHasNoWork.
func moveReason(paused, factoryHasNoWork, roled bool) string {
	if !roled {
		return MoveReasonLegacyAdoption
	}
	switch {
	case paused:
		return MoveReasonPauseToFactory
	case factoryHasNoWork:
		return MoveReasonFactoryIdleToDelivery
	default:
		return MoveReasonResumeToBaseline
	}
}
