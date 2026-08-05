package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reallocateGateRoles moves gate hulls between the delivery and factory roles, once per tick.
//
// THE PAUSE IS A SELF-SHORTENING FEEDBACK LOOP. Delivery pauses because a terminal factory is
// low; those hulls go feed that factory; it produces faster; supply recovers sooner; delivery
// resumes. The pause actively works to end itself instead of idling capacity — which is also what
// makes an aggressive buy floor safe: over-buying costs a reallocation, not a stall.
//
// IT NEVER MOVES A CLAIM. AssignFleet does not evict a holder by design ("the tag takes effect
// when the present claim is released"), and constructionLot.claimIdentity is FROZEN at plan time
// — a hull re-tagged in flight would present a stale tag, and ClaimShip authorizes a new claim
// only when tag == operation, so it would be rejected at the DB and the hull would be dispatched
// and silently never work. Considering only IDLE, UNHELD hulls makes that unreachable rather than
// merely rare, and it satisfies the spec's "a hull mid-haul must finish its leg" structurally.
//
// AssignFleet is the SINGLE WRITE PATH for the dedicated_fleet column (RULINGS #3). A general
// ship save would be reverted by preserveDedicatedFleetTag, silently.
//
// EVERY EXIT LOGS, including the ones that do nothing. This package has no metrics seam at all,
// so the log is this coordinator's only counter, and a reallocator that declines every decision is
// otherwise indistinguishable from one that is never invoked.
//
// It spends nothing. RULINGS #4 is not in scope here and no floor is read.
func (h *RunConstructionCoordinatorHandler) reallocateGateRoles(
	ctx context.Context,
	systemSymbol string,
	tasks []*manufacturing.ManufacturingTask,
	playerID shared.PlayerID,
) {
	// BOTH roles must have a wired leg. Moving a hull into a role whose leg does not run would
	// strand it on a path that does nothing — worse than leaving it where it was. Silent on
	// purpose: this is the byte-identical shape every pre-existing coordinator test runs in, and a
	// per-tick line about a collaborator that was never wired is noise, not a counter.
	if !h.gate.enabled() || !h.factory.enabled() || len(tasks) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)

	outstanding := h.outstandingGateMaterials(ctx, tasks)
	if len(outstanding) == 0 {
		// Nothing left to build: the workforce split no longer matters.
		logger.Log("INFO", "Gate roles: no gate material is outstanding, so the delivery/factory split no longer decides anything — leaving every role as it is", nil)
		return
	}

	workers, err := h.gateWorkforce(ctx, systemSymbol, outstanding, playerID)
	if err != nil {
		// Fail closed: never write against an unknown fleet.
		logger.Log("WARNING", fmt.Sprintf("Gate roles: cannot read the fleet, so no role is changed this tick: %v", err), nil)
		return
	}
	if len(workers) == 0 {
		// NOT "none is idle", which is what this line used to say and is the opposite diagnosis.
		// gateWorkforce counts every in-system gate hull WHATEVER its state — Idle is a field on the
		// worker, not a filter — so a hull mid-haul is in this census and is reported as `busy` by
		// the ruling below. An empty census therefore means no gate-tagged hull exists here at all,
		// and telling an operator their hulls are busy would send them to look at hauls that do not
		// exist.
		logger.Log("INFO", fmt.Sprintf("Gate roles: no gate-tagged hull exists%s, so there is no workforce to split this tick. A BUSY hull would still be counted here, so this census is empty rather than occupied", gateCensusScope(systemSymbol)), map[string]interface{}{"system": systemSymbol})
		return
	}

	// The pause state the DELIVERY LEGS already wrote. Reading it here rather than re-deciding
	// costs no market read and cannot disagree with what the legs actually did. FleetPaused is
	// EVERY material, never any one of them — that rule lives in the policy and is consumed, not
	// restated, because getting it backwards idles the fleet the moment a single material dips and
	// nothing about the fleet then looks broken.
	pipeline := h.gatePipeline(ctx, tasks)
	buyFloor, resumeFloor := "", ""
	if pipeline != nil {
		buyFloor, resumeFloor = pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor()
	}
	paused := h.gate.policyFor(buyFloor, resumeFloor).FleetPaused(outstanding)

	plan := gate.PlanReallocation(gate.ReallocationInput{
		Now:            h.clock.Now(),
		DeliveryPaused: paused,
		Workers:        workers,
	})
	logger.Log("INFO", plan.LogLine(), map[string]interface{}{
		"paused": paused, "moves": len(plan.Moves), "held": len(plan.Skips),
		"have_delivery": plan.HaveDelivery, "have_factory": plan.HaveFactory, "unroled": plan.Unroled,
		"dwell_records": plan.DwellRecords,
	})

	for _, move := range plan.Moves {
		if err := h.shipRepo.AssignFleet(ctx, move.Ship, move.To.FleetTag(), playerID); err != nil {
			// Stop early and keep the partial result: a hull left on its old tag is safe and is
			// retried a later tick, but writing on past a repository that just refused is not. The
			// ledger is NOT stamped — the role did not change, so the hull must stay immediately
			// eligible rather than sit out a dwell it never earned.
			logger.Log("ERROR", fmt.Sprintf("Gate roles: could not re-tag %s from %q to %q — stopping this tick's reallocation: %v", move.Ship, move.From, move.To.FleetTag(), err), map[string]interface{}{
				"ship": move.Ship, "from": move.From, "to": move.To.FleetTag(),
			})
			return
		}
		h.stampRoleChange(move.Ship)
		logger.Log("INFO", fmt.Sprintf("Gate roles: %s is now a %s hull (was %q) — %s", move.Ship, move.To, move.From, move.Reason), map[string]interface{}{
			"ship": move.Ship, "from": move.From, "to": move.To.FleetTag(), "reason": move.Reason,
		})
	}
}

// gateCensusScope names the scope the census actually covered, as a phrase ready to append.
//
// An unset system symbol means the in-system filter was SKIPPED and the census was fleet-wide —
// a different statement, not a missing word — so it gets its own phrase rather than interpolating
// an empty string into "in %s" and rendering a dangling "in  " with two spaces.
func gateCensusScope(systemSymbol string) string {
	if systemSymbol == "" {
		return " anywhere in the fleet"
	}
	return " in " + systemSymbol
}

// outstandingGateMaterials is the gate materials whose bill is still open.
//
// FILTERING TO OUTSTANDING IS LOAD-BEARING. A material whose bill is MET is never decided on by a
// delivery leg, so its pause state stays false forever; an unfiltered FleetPaused would then read
// FALSE the moment one material completes — exactly when the other is most likely starved and
// reallocation matters most.
func (h *RunConstructionCoordinatorHandler) outstandingGateMaterials(ctx context.Context, tasks []*manufacturing.ManufacturingTask) []string {
	pipeline := h.gatePipeline(ctx, tasks)
	if pipeline == nil {
		return nil
	}
	goods := make([]string, 0, len(pipeline.Materials()))
	for _, material := range pipeline.Materials() {
		if material.RemainingQuantity() <= 0 {
			continue
		}
		goods = append(goods, material.TradeSymbol())
	}
	return goods
}

// gatePipeline reads the pipeline behind this tick's ready tasks. Every construction task in a
// tick belongs to the same gate, so the first task's pipeline is the fleet's bill.
func (h *RunConstructionCoordinatorHandler) gatePipeline(ctx context.Context, tasks []*manufacturing.ManufacturingTask) *manufacturing.ManufacturingPipeline {
	for _, task := range tasks {
		if task == nil || task.PipelineID() == "" {
			continue
		}
		pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
		if err == nil && pipeline != nil {
			return pipeline
		}
	}
	return nil
}

// gateWorkforce is the live gate fleet as the reallocator sees it: every hull carrying a gate tag
// (the two roles PLUS the legacy one), in the operating system.
//
// Idle collapses three facts that all mean the same thing here — something is mid-haul with this
// hull: the drain's own worker registry holds it, the ship is not idle, or it is in transit. The
// registry is the authoritative one; the other two catch a hull flying with no registration
// behind it, which a restart can leave. IsIdle() ALONE is not enough: it reads the assignment, so
// a hull whose claim identity was frozen at plan time and is now flying reads idle by that measure.
//
// The LEGACY tag is included deliberately: those hulls carry no role, and adopting them is the
// only way a fleet that already holds four of them ever gets a role tag at all. A foreign fleet or
// a custom launch identity is NOT a gate tag and is never in this census — re-tagging one would be
// a poach (RULINGS #7).
//
// THE PLANNER FILTERS FOREIGN TAGS TOO, and that redundancy is deliberate rather than accidental.
// The planner's ReallocationPlan.Foreign counter exists for a caller that INTENDS to pass only
// gate hulls, so a foreign hull arriving there is a wiring bug worth rendering. FindAllByPlayer
// hands us the entire fleet — contract haulers, traders, probes — so passing them through would
// render "+ 62 foreign" on every tick and turn that signal into noise. Filtering here is what
// keeps a non-zero Foreign meaningful: from this caller it must always be 0.
func (h *RunConstructionCoordinatorHandler) gateWorkforce(ctx context.Context, systemSymbol string, outstanding []string, playerID shared.PlayerID) ([]gate.Worker, error) {
	logger := common.LoggerFromContext(ctx)
	ships, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}
	workers := make([]gate.Worker, 0, len(ships))
	for _, ship := range ships {
		if ship == nil || !gate.IsGateFleetTag(ship.DedicatedFleet()) {
			continue
		}
		if systemSymbol != "" {
			location := ship.CurrentLocation()
			if location == nil || shared.ExtractSystemSymbol(location.Symbol) != systemSymbol {
				continue // out of system: not this drain's to re-role
			}
		}
		idle := ship.IsIdle() && !ship.IsInTransit() && !h.supplies.holds(ship.ShipSymbol())
		role, roled := gate.ParseFleetTag(ship.DedicatedFleet())
		if idle && roled && role == gate.RoleFactory && onlyTheFactoryLegCanEmpty(ship, outstanding) {
			idle = false
			logger.Log("WARNING", fmt.Sprintf("Gate roles: %s stays on the FACTORY role — its hold is full (%s) and nothing aboard is a gate material the bill still wants, so on the delivery role it could neither unload nor buy and dispatchableByHold would decline it every tick. The factory leg's from-hold feed is the only thing that empties it, and it spends nothing to do so. The ruling below reports this hull as `busy`, which is as close as the planner's own skip vocabulary gets", ship.ShipSymbol(), describeHold(ship)), map[string]interface{}{
				"ship": ship.ShipSymbol(), "hold": describeHold(ship), "action": "held_on_factory_role",
			})
		}
		workers = append(workers, gate.Worker{
			Ship:          ship.ShipSymbol(),
			FleetTag:      ship.DedicatedFleet(),
			Idle:          idle,
			LastMovedByUs: h.roleChangedAt(ship.ShipSymbol()),
		})
	}
	return workers, nil
}

// onlyTheFactoryLegCanEmpty reports whether a hull's hold has exactly one route out of it, and that
// route belongs to the FACTORY role: the hold is FULL, and nothing aboard is a gate material whose
// bill is still open.
//
// IT IS wedgedAtFullHold's OWN SHAPE, and that is not a coincidence — it is wedgedAtFullHold that
// decides this hull's fate on the far side of a move to delivery. A hull matching it there is
// dropped from the dispatch pool every tick, so it never reaches deliverGateLeg and never reaches
// flushOnHandGateMaterials, that role's only unload path — which could not have helped anyway, since
// the flush only moves gate materials and by this predicate the hull carries none. The FACTORY leg
// does have a route: feedGateLegFromHold sells what is aboard into a factory that imports it, from
// an already-owned hold, with no purchase (RULINGS #4 untouched — this function reads no floor and
// opens no spend path; it only declines to move a hull).
//
// THE CALLER SCOPES THIS TO THE FACTORY ROLE, and the scoping is load-bearing in the OTHER
// direction. A wedged DELIVERY hull must still be borrowed to the factory role under a pause,
// because that borrow IS its recovery; blocking it would turn a self-healing degradation into a
// permanent one. Nothing is lost by the narrow scope: moveTarget returns wanted=false for a factory
// hull whenever needFactory > 0, so the ONLY move a factory-role hull can ever be selected for is
// the return to delivery — for that role, "not idle" and "do not return to delivery" are the same
// statement.
//
// It decides on POSITIVE evidence only, exactly as wedgedAtFullHold does: an unreadable or
// capacity-less hold is never called full. Holding a hull back on a hold we could not read would
// freeze the workforce split on a missing observation.
func onlyTheFactoryLegCanEmpty(ship *navigation.Ship, outstanding []string) bool {
	cargo := ship.Cargo()
	if cargo == nil || cargo.Capacity <= 0 {
		return false
	}
	if cargo.Capacity-cargo.Units > 0 {
		return false // hold left to buy into: the delivery role can still work this hull
	}
	for _, good := range outstanding {
		if onHandUnits(ship, good) > 0 {
			return false // it carries material the site still wants, which the delivery flush unloads
		}
	}
	return true
}

// roleChangedAt reports when this process last re-roled the hull; the zero value means never,
// which is eligible immediately. It is the READ half of the dwell ledger — see the roleSince field
// for why an unmaintained ledger silently disables the dwell.
func (h *RunConstructionCoordinatorHandler) roleChangedAt(ship string) time.Time {
	h.roleMu.Lock()
	defer h.roleMu.Unlock()
	return h.roleSince[ship]
}

// stampRoleChange is the WRITE half of the dwell ledger, called only after AssignFleet has
// actually landed the new tag.
func (h *RunConstructionCoordinatorHandler) stampRoleChange(ship string) {
	h.roleMu.Lock()
	defer h.roleMu.Unlock()
	if h.roleSince == nil {
		h.roleSince = make(map[string]time.Time)
	}
	h.roleSince[ship] = h.clock.Now()
}
