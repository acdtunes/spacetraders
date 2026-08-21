package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// dispatchableHaulers drops hulls a supply worker is still hauling with. Discovery reads a hull's
// idle flag, so a claimed hull is normally invisible already; this is the drain's OWN authority on
// the question, and it is load-bearing because a re-claim by the SAME container is idempotent at the
// DB — ClaimShip would hand a second worker the same hull rather than reject it.
func (h *RunConstructionCoordinatorHandler) dispatchableHaulers(ships []*navigation.Ship) []*navigation.Ship {
	free := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if h.supplies.holds(ship.ShipSymbol()) {
			continue
		}
		free = append(free, ship)
	}
	return free
}

// dedicatedFleet is the Ship.DedicatedFleet() tag this drain PREFERS. The default is deliberately
// EQUAL to operationManufacturing, the ClaimShip operation: FindIdleShipsByFleet looks hulls up BY
// this tag AND ClaimShip authorizes a new claim only when the hull's tag equals the operation, so
// one value must drive both or the drain cannot claim its own dedicated hull. Read fresh each tick,
// so a live re-pin re-derives preference with no carried state.
func (h *RunConstructionCoordinatorHandler) dedicatedFleet(cmd *RunConstructionCoordinatorCommand) string {
	if cmd.DedicatedFleet != "" {
		return cmd.DedicatedFleet
	}
	return operationManufacturing
}

// claimIdentityFor is the ClaimShip operation string for ONE hull.
//
// This is load-bearing and easy to get silently wrong. ClaimShip authorizes a NEW claim only when
// the hull's dedicated_fleet EQUALS the operation, so a hull tagged gate-delivery claimed under the
// drain's DEFAULT identity is rejected at the DB: the hull is discovered, paired with a lot,
// dispatched, and then silently never works. That failure ships green.
//
// The GATE-tag allowlist must not be relaxed to "whatever tag the hull carries". Claiming under any
// tag would let the drain claim a CONTRACT or TRADE hull under that fleet's own identity and sail
// past the dedication guard, defeating the no-poach rule entirely. An undedicated hull claims under
// the drain's identity, and so does a foreign-pinned one — precisely so ClaimShip REJECTS it.
func (h *RunConstructionCoordinatorHandler) claimIdentityFor(cmd *RunConstructionCoordinatorCommand, ship *navigation.Ship) string {
	if tag := ship.DedicatedFleet(); gate.IsGateFleetTag(tag) {
		return tag
	}
	return h.dedicatedFleet(cmd)
}

// selectHaulers builds the tick's ordered claim pool, PREFERRING the drain's own dedicated fleet.
// FindIdleLightHaulers EXCLUDES every dedicated hull by design, so the drain's own fleet must be
// discovered separately via FindIdleShipsByFleet or its gate haulers stay invisible while an idle
// unpinned hull gets grabbed opportunistically instead.
//
// This mirrors the contract coordinator's split: FindIdleShipsByFleet surfaces the OWN dedicated
// fleet (system-scoped here — construction legs never jump), FindIdleLightHaulers the opportunistic
// pool. The two pools are DISJOINT, and dedicated hulls are placed FIRST so the fan-out pairs them
// ahead of any opportunistic hull. Opportunistic hulls only SUPPLEMENT when dedicated capacity is
// insufficient, and are dropped entirely in ExclusiveDedicatedFleet mode. A hull pinned to ANOTHER
// operation is in NEITHER pool, and even if it were, ClaimShip rejects it atomically.
func (h *RunConstructionCoordinatorHandler) selectHaulers(ctx context.Context, cmd *RunConstructionCoordinatorCommand, playerID shared.PlayerID, systemSymbol string) ([]*navigation.Ship, error) {
	fleet := h.dedicatedFleet(cmd)

	// Discover the drain's own identity AND every gate ROLE pool. FindIdleLightHaulers excludes
	// every dedicated hull by design (ship_pool_manager.go: `if ship.DedicatedFleet() != "" {
	// continue }`), so a role-tagged hull appears in NO pool unless its own tag is queried here —
	// it would simply be invisible to the drain forever. FindIdleShipsByFleet is fleet-wide (no
	// system filter), so the union is restricted to the operating system below: an out-of-system
	// hull is UNSELECTABLE, not claimed-then-failed (fail-closed, matching FindIdleLightHaulers'
	// own single-system pre-filter).
	fleets := gateDiscoveryFleets(fleet)
	seen := make(map[string]bool)
	var dedicatedIdle []*navigation.Ship
	for _, pool := range fleets {
		found, _, err := contract.FindIdleShipsByFleet(ctx, playerID, h.shipRepo, pool, contract.RequireCargoCapacity)
		if err != nil {
			return nil, fmt.Errorf("failed to discover %s construction haulers: %w", pool, err)
		}
		for _, ship := range found {
			if ship == nil || seen[ship.ShipSymbol()] {
				continue // one hull, one lot: a duplicate would be dispatched twice
			}
			seen[ship.ShipSymbol()] = true
			dedicatedIdle = append(dedicatedIdle, ship)
		}
	}
	dedicatedIdle = haulersInSystem(dedicatedIdle, systemSymbol)

	// EXCLUSIVE MODE (opt-in): once ANY hull carries a gate fleet tag, the drain is sealed to its
	// dedicated members and never supplements from the opportunistic pool, even when no dedicated
	// hull is dispatchable this tick. Membership is checked across the SAME set discovery reads: a
	// seal that only saw the drain's own identity would read "no dedicated fleet" while role-tagged
	// hulls exist, and fall through to poaching opportunistic hulls.
	if cmd.ExclusiveDedicatedFleet {
		for _, pool := range fleets {
			active, err := contract.FleetHasMembers(ctx, playerID, h.shipRepo, pool)
			if err != nil {
				return nil, fmt.Errorf("failed to check dedicated fleet membership: %w", err)
			}
			if active {
				return dedicatedIdle, nil
			}
		}
	}

	// Opportunistic pool: undedicated idle haulers in-system. FindIdleLightHaulers already excludes
	// every dedicated hull and system-filters, so it never double-counts the dedicated pool above.
	// Appended AFTER dedicated so the fan-out always pairs dedicated hulls first.
	opportunistic, _, err := contract.FindIdleLightHaulers(ctx, playerID, h.shipRepo, systemSymbol, contract.ReleaseGateHandback)
	if err != nil {
		return nil, fmt.Errorf("failed to discover idle haulers: %w", err)
	}
	return append(dedicatedIdle, opportunistic...), nil
}

// gateDiscoveryFleets is the ordered, de-duplicated set of dedicated pools the drain looks up:
// its own launch identity FIRST (so its own hulls are still paired ahead of any other), then every
// gate ROLE tag. An empty identity is dropped (FindIdleShipsByFleet answers nothing for it), and a
// launch identity that happens to EQUAL a role tag is listed once — scanning a pool twice would
// surface the same hull twice and pair it with two lots.
func gateDiscoveryFleets(fleet string) []string {
	pools := make([]string, 0, 1+len(gate.RoleFleetTags()))
	listed := make(map[string]bool)
	for _, pool := range append([]string{fleet}, gate.RoleFleetTags()...) {
		if pool == "" || listed[pool] {
			continue
		}
		listed[pool] = true
		pools = append(pools, pool)
	}
	return pools
}

// haulersInSystem keeps only ships whose CURRENT system equals systemSymbol; a hull whose location is
// unknown is dropped (fail-closed), mirroring FindIdleLightHaulers' single-system pre-filter. Used to
// system-scope the fleet-wide FindIdleShipsByFleet result.
func haulersInSystem(ships []*navigation.Ship, systemSymbol string) []*navigation.Ship {
	filtered := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		loc := ship.CurrentLocation()
		if loc == nil {
			continue
		}
		if shared.ExtractSystemSymbol(loc.Symbol) != systemSymbol {
			continue
		}
		filtered = append(filtered, ship)
	}
	return filtered
}

// constructionLot is one hull's unit of work this tick: a DELIVER_TO_CONSTRUCTION task paired with
// an idle hull, plus the fan-out bookkeeping. A material's SINGLE ready task becomes one
// non-ephemeral lot; the fan-out adds EPHEMERAL clone lots so several hulls work it concurrently.
type constructionLot struct {
	task *manufacturing.ManufacturingTask
	ship *navigation.Ship
	// fillCap bounds this lot's PHASE-2 buy so the lots working a material do not collectively buy
	// past what it still needs, and is the buy reservation the lot's worker holds against that
	// material for its whole life. A material's caps sum to its outstanding bill net of what is
	// already committed. 0 = NO cap UNLESS buyCapped says otherwise (see below).
	fillCap int
	// buyCapped marks fillCap AUTHORITATIVE, including when it is zero.
	//
	// A zero fillCap has two opposite meanings and the planner mints both. The zero VALUE means
	// "no cap" (a lot built by hand). A zero ASSIGNED by the planner means "buy NOTHING": the lot
	// exists only to unload cargo the hull already carries, because every outstanding unit of its
	// material is already paid for. Reading the second as the first hands an uncapped fill to
	// exactly the lot that must not spend.
	buyCapped bool
	// ephemeral marks a fan-out CLONE (not one of the pipeline's persisted ready tasks): it does the real
	// source+deliver+record work but skips task-status persistence AND replenishment — the material's
	// original ready task (always dispatched alongside a clone) owns those, so the ready queue stays at
	// the planner's one-task-per-material and the fan-out re-derives parallelism from live hulls each tick.
	ephemeral bool
	// claimIdentity is the ClaimShip operation this lot's hull must be claimed under: its OWN
	// gate tag, or the drain's identity for an undedicated/foreign hull. Resolved at plan time
	// from the paired hull, because the worker that claims runs long after the tick that planned
	// it and must not re-derive from a tag that may have changed underneath.
	claimIdentity string
}

// buyReservation is what this lot can actually PAY FOR on its trip: its fill cap, bounded by what the
// hull can carry — one trip never buys more than a hold, however much outstanding bill sits behind an
// uncapped fill target. This is the figure a later tick nets out of the material's bill while the lot
// is still in the air, so it must be the trip's real spend and not the bill it is filling toward.
func (l constructionLot) buyReservation() int {
	capacity := defaultConstructionLotUnits
	if cargo := l.ship.Cargo(); cargo != nil && cargo.Capacity > 0 {
		capacity = cargo.Capacity
	}
	if l.buyCapped && l.fillCap <= 0 {
		return 0 // an unload-only lot reserves no budget: it is not going to spend
	}
	if l.fillCap > 0 && l.fillCap < capacity {
		return l.fillCap
	}
	return capacity
}

// mayBuy reports whether this lot is allowed to SPEND at all. False only for a planner-assigned
// zero cap — the unload-only lot described on buyCapped.
func (l constructionLot) mayBuy() bool {
	return !l.buyCapped || l.fillCap > 0
}

// cappedNeed bounds a sourcing target by this lot's authoritative buy budget. A lot with no
// authoritative cap keeps today's behaviour (a positive fillCap bounds, a zero one does not).
func (l constructionLot) cappedNeed(need int) int {
	if !l.mayBuy() {
		return 0
	}
	if l.fillCap > 0 && l.fillCap < need {
		return l.fillCap
	}
	return need
}

// factoryRoleHasNoFeedingWorkFor reports whether ship is a FACTORY-role hull about to be paired
// with an already BUY-resolved task (task.SourceMarket() != ""). feedGateLeg's plan is
// PIPELINE-WIDE, never scoped to the paired task's own good, so such a pairing buys the factory
// role no feeding work — and when the leg then finds nothing else to feed, it completes THIS task,
// clearing the buy resolution a delivery hull needs. A task that is not buy-resolved is unaffected:
// feeding those is the factory role's whole purpose.
func (h *RunConstructionCoordinatorHandler) factoryRoleHasNoFeedingWorkFor(cmd *RunConstructionCoordinatorCommand, ship *navigation.Ship, task *manufacturing.ManufacturingTask) bool {
	if task.SourceMarket() == "" {
		return false
	}
	role, ok := h.gateLegRole(h.claimIdentityFor(cmd, ship))
	return ok && role == gate.RoleFactory
}

// planDispatchLots fans the ready material-tasks into per-hull lot-tasks so throughput is not capped
// at the number of materials. It dispatches each existing ready task once, skipping a material whose
// bill is already met, then fans spare idle hulls onto materials wanting more concurrent lots —
// bounded per material by ceil(remaining/hull-load) so a material is never over-dispatched, and
// globally by the WHOLE idle pool up to the materials' total remaining requirement. Each lot then
// gets a buy cap so concurrent same-material lots never buy past that remaining requirement.
//
// The returned lots hold distinct idle hulls, each drawn from the WHOLE pool by haulerPool so a hull
// already laden with the lot's good takes that lot — adoption before re-buy — even when maxLots
// means only a few hulls actually start. maxLots is the tick's free budget under max_workers: every
// lot minted here is dispatched, and a slot a haul frees is refilled by the NEXT tick from live
// hulls rather than from a plan made before that haul began.
func (h *RunConstructionCoordinatorHandler) planDispatchLots(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, tasks []*manufacturing.ManufacturingTask, idleShips []*navigation.Ship, maxLots int) []constructionLot {
	if len(idleShips) == 0 || maxLots <= 0 {
		return nil
	}
	budget := h.materialBuyBudgets(ctx, cmd, systemSymbol, tasks, idleShips, representativeLotUnits(idleShips))

	// Drop hulls that can do NO work this tick before any lot is minted against them (see
	// wedgedAtFullHold). Done here, after the budget, because "can this hull do anything" is a
	// question about the outstanding BILL, not about the hull alone.
	idleShips = h.dispatchableByHold(ctx, cmd, idleShips, budget)
	if len(idleShips) == 0 {
		return nil
	}

	lotCeiling := budget.lotCeiling(len(idleShips), maxLots)

	lots := make([]constructionLot, 0, lotCeiling)
	pool := newHaulerPool(idleShips)

	// Pass 1: one lot per existing ready task, in order, skipping a material whose bill is already
	// met (a racing-replenishment leftover, which would buy against no demand) or whose per-material
	// lot budget is full — the over-supply guard, even if the queue over-staged a material.
	for _, task := range tasks {
		if len(lots) >= lotCeiling {
			break
		}
		key := materialKey(task)
		if !budget.wantsAnotherLot(key) {
			continue
		}
		hull := pool.takeFor(task.Good(), func(ship *navigation.Ship) bool {
			return !h.factoryRoleHasNoFeedingWorkFor(cmd, ship, task)
		})
		if hull == nil {
			// A miss here means only THIS task found no eligible hull; try the rest before giving up.
			continue
		}
		lots = append(lots, constructionLot{task: task, ship: hull, claimIdentity: h.claimIdentityFor(cmd, hull)})
		budget.assign(key)
	}

	// Pass 2: fan spare hulls onto the materials that still want more concurrent lots (ephemeral clones),
	// picking the neediest each time so multiple materials share the pool fairly.
	for len(lots) < lotCeiling {
		key := budget.neediestMaterial()
		if key == "" {
			break // no material wants another lot (every remaining requirement is covered)
		}
		clone := nextConstructionDeliveryTask(budget.repTask[key])
		if err := clone.MarkReady(); err != nil {
			break // cannot stage a clone lot-task; stop fanning (all originals are already dispatched)
		}
		// clone inherits its representative task's resolution, so the same eligibility check applies.
		// Unlike pass 1, a miss here BREAKS rather than continues: neediestMaterial() would otherwise
		// hand back this same key forever with nothing changed, which is an infinite loop, not a retry.
		hull := pool.takeFor(clone.Good(), func(ship *navigation.Ship) bool {
			return !h.factoryRoleHasNoFeedingWorkFor(cmd, ship, clone)
		})
		if hull == nil {
			break
		}
		lots = append(lots, constructionLot{task: clone, ship: hull, ephemeral: true, claimIdentity: h.claimIdentityFor(cmd, hull)})
		budget.assign(key)
	}

	budget.assignFillCaps(lots)
	return lots
}

// dispatchableByHold drops hulls that can do NO work this tick, so they stop consuming a lot every
// tick forever. Every drop is logged by name: a hull silently missing from the pool is exactly the
// invisibility this whole phase exists to remove.
func (h *RunConstructionCoordinatorHandler) dispatchableByHold(ctx context.Context, cmd *RunConstructionCoordinatorCommand, ships []*navigation.Ship, budget *materialBudget) []*navigation.Ship {
	logger := common.LoggerFromContext(ctx)
	usable := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		// THE SAME FUNCTION supplyTask routes on, but the decline asks only about the DELIVERY role.
		// wedgedAtFullHold ("full hold, nothing aboard is a material whose bill is still open") is
		// sound only for the delivery leg, whose repertoire is flush-then-buy. A FACTORY hull's hold
		// is full of fabrication INPUTS, which are never bill materials, so the predicate is true
		// for every laden one — declining on it would make the entire factory fleet permanently
		// invisible. Its leg recovers that hull by a route the predicate cannot see: FeedFactory
		// SELLS the inputs into the factory's import listing. With a role's collaborator unwired,
		// its hulls take the shared fabricate path and recover there, which the ok half covers.
		//
		// WHAT IS SHARED IS THE ROLE DERIVATION, NOT THE DISPATCH TABLE. gateLegRole guarantees both
		// sides read the same ROLE off the same identity; a shared BOOLEAN could only widen both
		// together, and widening this one is the fleet-killer above. It does NOT guarantee the two
		// act on that role consistently — both branch non-exhaustively on a Go int enum, so a third
		// role lands in BOTH fall-through arms at once with no compiler help. supplyTask's switch
		// has a loud default for exactly that; a new role still needs a deliberate decision HERE.
		// wedgedAtFullHold's soundness is per-role, so the honest default for an unknown role is the
		// exemption — the decline is the dangerous direction.
		role, hasGateRole := h.gateLegRole(h.claimIdentityFor(cmd, ship))
		if !hasGateRole || role != gate.RoleDelivery || !wedgedAtFullHold(ship, budget) {
			usable = append(usable, ship)
			continue
		}
		// NAME THE RECOVERY THAT EXISTS. The system recovers this state on its own for the commonest
		// cargo, so a flat "sell or transfer it" sends an operator to intervene by hand for nothing.
		// Which of the two cases applies is decided entirely by what is in the hold, so the hold is
		// rendered into the message (the container log renderer drops metadata maps).
		logger.Log("WARNING", fmt.Sprintf("Construction drain: gate-delivery hull %s has a FULL hold (%s) and carries nothing any ready material still needs — its leg can neither deliver nor buy, so it is not dispatched this tick. If that cargo is factory feedstock the next delivery PAUSE borrows the hull into the factory role, whose from-hold feed unloads it and spends nothing; cargo NO factory in this chain imports — typically a gate material whose bill has since closed, which a terminal factory will not buy back — has no automatic route out and must be sold or transferred", ship.ShipSymbol(), describeHold(ship)), map[string]interface{}{
			"ship": ship.ShipSymbol(), "hold": describeHold(ship), "action": "skip_wedged_full_hold",
		})
	}
	return usable
}

// wedgedAtFullHold reports whether a DELIVERY-role hull can make NO progress on any ready material:
// its hold is FULL, and nothing aboard is a material whose bill is still open.
//
// Such a hull cannot deliver (nothing it carries is wanted) and cannot buy (no free hold). Left in
// the pool it takes a lot, plans zero stops, parks — and because haulerPool.take PREFERS a hull
// already holding the lot's good, it is handed the same lot again next tick, consuming a dispatch
// slot forever while the drain reports RUNNING. This is the gate's EXPECTED end state, not an edge
// case: when a material's bill closes, every in-flight hull still holding it lands here, and
// reconcilePipelinesFromSite only ever RAISES delivered counters, so the bill never re-opens.
// Declining converts a fleet-wide stall into one degraded hull.
//
// THE CALLER SCOPES THIS TO THE DELIVERY ROLE, and that scoping is load-bearing. "Cannot deliver
// and cannot buy" is true only of the gate leg, whose whole repertoire is flush-then-buy. The
// LEGACY fabricate role recovers the same hull by a route this predicate cannot see:
// sourceAndDeliverRemainder has no free-capacity precheck, and fabricateGood navigates to the
// factory and frees the hold there before harvesting.
//
// That recovery is STRONGER than an unload of inputs, which is what makes the scoping justified
// rather than merely cautious. makeRoomForOutputHarvest delegates to freeCargoSpace, which SELLS
// every item aboard except protectGood — the good currently being harvested, withheld only because
// dumping a fabricated output at its own factory's bid ladders that bid down against us. A finished
// material whose bill already closed draws no lot, so it is never the harvested nor the protected
// good: the legacy role clears that hull BY SELLING its load outright. An unscoped predicate would
// instead declare it dead and drop it every tick forever, relocating this very failure mode onto a
// role that had a working recovery.
//
// It decides on POSITIVE evidence only. An unreadable or capacity-less hold is never called
// wedged — we drop a hull because we can show it has no work, never because we could not tell.
func wedgedAtFullHold(ship *navigation.Ship, budget *materialBudget) bool {
	cargo := ship.Cargo()
	if cargo == nil || cargo.Capacity <= 0 {
		return false
	}
	if cargo.Capacity-cargo.Units > 0 {
		return false // hold left to buy into
	}
	return !budget.wantsMaterialAboard(ship)
}

// materialBudget is the tick's fan-out arithmetic for one drain: how many units of each distinct
// material may still be BOUGHT, how many lots have been staged against each, and the hull-load that
// count is sized in. remaining is already net of what in-flight workers are authorized to buy.
type materialBudget struct {
	order     []string
	remaining map[string]int
	// rawRemaining is the material's OUTSTANDING BILL, before any commitment is netted out. It
	// answers a different question from remaining and the two must not be confused: remaining is
	// how much may still be BOUGHT, rawRemaining is how much the site still WANTS. Delivering
	// cargo already aboard spends nothing, so it is governed by demand, never by a buy budget — a
	// hull whose material is fully covered by in-flight loads is still carrying units the site
	// needs, and can still unload them for free.
	rawRemaining map[string]int
	// lotDemand is how many units still need a HULL this tick: the outstanding bill less what is
	// committed to hulls this tick CANNOT dispatch. It sits between the other two and is what the
	// lot count is sized from.
	//
	// It cannot be remaining, which also nets out cargo aboard hulls in this tick's own pool: a
	// material covered that way would want ZERO lots, leaving the laden hull with no lot minted
	// to unload it and the bill never closing. It cannot be rawRemaining either, which re-mints a
	// lot for every unit an unreachable hull already carries.
	lotDemand map[string]int
	repTask   map[string]*manufacturing.ManufacturingTask
	assigned  map[string]int
	lotUnits  int
}

// materialBuyBudgets reads, once per distinct material, how many units may still be BOUGHT this
// tick, plus a representative task to clone for fan-out, in first-seen order for deterministic
// distribution.
//
// The budget nets out COMMITTED units — everything already paid for, whether it sits in a live
// worker's reservation or in a hull's hold. Sizing against the raw bill buys the same units twice;
// sizing against the in-memory reservation alone survives neither a restart nor the reservation's
// own expiry, while the cargo stays aboard through both.
func (h *RunConstructionCoordinatorHandler) materialBuyBudgets(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	tasks []*manufacturing.ManufacturingTask,
	idleShips []*navigation.Ship,
	lotUnits int,
) *materialBudget {
	b := &materialBudget{
		order:        make([]string, 0, len(tasks)),
		remaining:    make(map[string]int),
		rawRemaining: make(map[string]int),
		lotDemand:    make(map[string]int),
		repTask:      make(map[string]*manufacturing.ManufacturingTask),
		assigned:     make(map[string]int),
		lotUnits:     lotUnits,
	}
	dispatchable := make(map[string]bool, len(idleShips))
	for _, ship := range idleShips {
		if ship != nil {
			dispatchable[ship.ShipSymbol()] = true
		}
	}
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	// One fold per PIPELINE, so a multi-material gate costs one fleet read, not one per material.
	commitments := make(map[string]gateCommitments)
	for _, pipelineID := range distinctPipelines(tasks) {
		commitments[pipelineID] = h.gateMaterialCommitments(ctx, cmd, playerID, systemSymbol, pipelineID, pipelineGoods(tasks, pipelineID), dispatchable)
	}

	for _, task := range tasks {
		key := materialKey(task)
		if _, seen := b.remaining[key]; seen {
			continue
		}
		// One read, three questions: the site's outstanding DEMAND, how many units still need a
		// HULL, and what may still be BOUGHT once every commitment is netted out.
		bill := h.remainingBill(ctx, task)
		commitment := commitments[task.PipelineID()]
		committed := commitment.unitsFor(task.Good())
		warnOnOvershoot(ctx, task.Good(), bill, committed)

		b.rawRemaining[key] = bill
		b.lotDemand[key] = bill - commitment.undispatchableFor(task.Good())
		b.remaining[key] = bill - committed
		if bill > 0 && b.remaining[key] <= 0 {
			logInFlightSkip(ctx, task.Good(), bill, committed)
		}
		b.repTask[key] = task
		b.order = append(b.order, key)
	}
	return b
}

// distinctPipelines is the tick's pipeline ids, first-seen order, deduplicated.
func distinctPipelines(tasks []*manufacturing.ManufacturingTask) []string {
	seen := make(map[string]bool, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if id := task.PipelineID(); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// pipelineGoods is the distinct goods pipelineID's ready tasks name, first-seen order.
func pipelineGoods(tasks []*manufacturing.ManufacturingTask, pipelineID string) []string {
	seen := make(map[string]bool, len(tasks))
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.PipelineID() != pipelineID || seen[task.Good()] {
			continue
		}
		seen[task.Good()] = true
		out = append(out, task.Good())
	}
	return out
}

// desiredLots is how many hull-load lots this material still wants: ceil(lotDemand/hull-load), 0
// once every outstanding unit is either delivered or aboard a hull this tick cannot dispatch.
func (b *materialBudget) desiredLots(key string) int {
	return ceilDiv(b.lotDemand[key], b.lotUnits)
}

// wantsAnotherLot reports whether the material still has work for a hull AND unfilled lot budget.
// It asks lotDemand, not remaining: a lot that only UNLOADS cargo already aboard spends nothing
// and still advances the gate, so a zero buy budget must not withhold the hull that carries the
// last tranche in.
func (b *materialBudget) wantsAnotherLot(key string) bool {
	return b.lotDemand[key] > 0 && b.assigned[key] < b.desiredLots(key)
}

// wantsMaterialAboard reports whether the hull carries any material the SITE still wants — the
// material a deliver-on-hand phase (or the gate leg's flush) could actually unload.
//
// It asks rawRemaining, the outstanding BILL, deliberately: `remaining` is a BUY budget already net
// of what in-flight workers may purchase, and unloading cargo already aboard is not a purchase.
// Asking the buy budget would declare a hull carrying genuinely-wanted material to be carrying
// nothing — declining it while an in-flight worker pays market for the very units it holds.
func (b *materialBudget) wantsMaterialAboard(ship *navigation.Ship) bool {
	for _, key := range b.order {
		task := b.repTask[key]
		if task == nil || b.rawRemaining[key] <= 0 {
			continue
		}
		if onHandUnits(ship, task.Good()) > 0 {
			return true
		}
	}
	return false
}

func (b *materialBudget) assign(key string) {
	b.assigned[key]++
}

// lotCeiling caps the tick's lots by the idle pool, by the materials' total remaining
// requirement (never mint a lot no material needs — the over-supply guard's global counterpart),
// and by the free worker slots, so the plan is exactly what gets dispatched.
func (b *materialBudget) lotCeiling(idleHulls, maxLots int) int {
	ceiling := idleHulls
	if demand := b.totalLotDemand(); demand < ceiling {
		ceiling = demand
	}
	if maxLots < ceiling {
		ceiling = maxLots
	}
	return ceiling
}

// haulerPool hands out each idle hull at most once, PREFERRING a hull that ALREADY HOLDS the lot's
// good. An interrupted delivery leaves its load aboard, and pool-order, cargo-blind pairing lets
// the laden hull draw whichever task comes up while an empty one re-buys the same material at
// market, so paid-for gate material rides along undelivered. Pairing on the hull's ACTUAL cargo
// re-adopts that load with no cross-tick state to go stale, and the leg unloads it before any buy:
// PHASE-1 deliver-on-hand for every shared-path role, flushOnHandGateMaterials for the
// gate-delivery role, which is routed ahead of that phase. BOTH must exist — a role that re-adopts
// a laden hull with no unload wedges it at a full hold forever, since zero free capacity plans zero
// stops. An all-empty pool hands out in pool order, so dedicated hulls still come first.
type haulerPool struct {
	ships []*navigation.Ship
	taken []bool
}

func newHaulerPool(ships []*navigation.Ship) *haulerPool {
	return &haulerPool{ships: ships, taken: make([]bool, len(ships))}
}

// take claims the next hull for a lot of good: the first untaken hull already carrying good, else
// the first untaken hull. nil once the pool is exhausted.
func (p *haulerPool) take(good string) *navigation.Ship {
	return p.takeFor(good, nil)
}

// takeFor is take, restricted to hulls eligible reports true for (nil = every hull is eligible,
// making take its special case). An ineligible hull is left untaken for a later, better-suited
// task rather than claimed and wasted.
func (p *haulerPool) takeFor(good string, eligible func(*navigation.Ship) bool) *navigation.Ship {
	next := -1
	for i, ship := range p.ships {
		if p.taken[i] || (eligible != nil && !eligible(ship)) {
			continue
		}
		if onHandUnits(ship, good) > 0 {
			return p.claim(i)
		}
		if next < 0 {
			next = i
		}
	}
	if next < 0 {
		return nil
	}
	return p.claim(next)
}

func (p *haulerPool) claim(index int) *navigation.Ship {
	p.taken[index] = true
	return p.ships[index]
}

// neediestMaterial returns the material with the greatest unmet lot need (desired − assigned), where
// desired = ceil(remaining/hull-load); "" when every material's lot budget is already filled. Ties break
// by first-seen order for determinism.
func (b *materialBudget) neediestMaterial() string {
	best := ""
	bestNeed := 0
	for _, key := range b.order {
		need := b.desiredLots(key) - b.assigned[key]
		if need > bestNeed {
			bestNeed = need
			best = key
		}
	}
	return best
}

// assignFillCaps sets each lot's buy cap so the lots working a material never buy past what it still
// needs (the over-supply guard). A material with a SINGLE lot takes the whole budget — an
// effectively uncapped fill, safe because the executor stops at hull capacity anyway. A material
// with MULTIPLE lots has that budget sliced into hull-load caps that sum to it. The budget is the
// planner's, already net of every committed unit, so it is also the buy reservation each dispatched
// lot registers.
//
// EVERY cap it assigns is marked AUTHORITATIVE, zero included. A zero here is not "no cap" — it is
// a lot minted purely to unload cargo the hull already carries, whose material has nothing left to
// buy. Left unmarked it reads as the zero value and licenses an uncapped fill.
func (b *materialBudget) assignFillCaps(lots []constructionLot) {
	counts := make(map[string]int)
	for i := range lots {
		counts[materialKey(lots[i].task)]++
	}
	unspent := make(map[string]int, len(b.remaining))
	for key, rem := range b.remaining {
		unspent[key] = rem
	}
	for i := range lots {
		key := materialKey(lots[i].task)
		slice := b.lotUnits
		if counts[key] <= 1 || unspent[key] < slice {
			slice = unspent[key]
		}
		if slice < 0 {
			slice = 0
		}
		lots[i].fillCap = slice
		lots[i].buyCapped = true
		unspent[key] -= slice
	}
}

// materialKey identifies a construction material by its pipeline + good, so two goods on one gate (and
// the same good on two gates) are budgeted independently for the fan-out.
func materialKey(task *manufacturing.ManufacturingTask) string {
	return task.PipelineID() + "\x00" + task.Good()
}

// representativeLotUnits is the per-lot hull-load the fan-out sizes against — the cargo capacity of the
// idle haulers (uniform light haulers in practice). Falls back to defaultConstructionLotUnits for a hull
// exposing no capacity, so ceil(remaining/lotUnits) never divides by zero.
func representativeLotUnits(ships []*navigation.Ship) int {
	for _, ship := range ships {
		if cargo := ship.Cargo(); cargo != nil && cargo.Capacity > 0 {
			return cargo.Capacity
		}
	}
	return defaultConstructionLotUnits
}

// ceilDiv is ceil(units/per) for positive inputs, 0 when there is nothing to divide (remaining<=0) or no
// divisor — so a met bill yields a desired-lot count of 0 (no lot).
func ceilDiv(units, per int) int {
	if units <= 0 || per <= 0 {
		return 0
	}
	return (units + per - 1) / per
}

// totalLotDemand is the number of hull-load lots needed to meet every distinct material's remaining
// requirement this tick — sum of ceil(remaining/hull-load). It bounds the fan-out so the drain
// never stages a lot no material needs (the over-supply guard's global counterpart).
func (b *materialBudget) totalLotDemand() int {
	total := 0
	for _, key := range b.order {
		total += b.desiredLots(key)
	}
	return total
}

// resolveWorkerCap is the bound on supply workers IN FLIGHT: the largest max_workers among the
// distinct EXECUTING pipelines backing the ready tasks. Read fresh every tick, so a live
// `construction workers --count` write takes effect on the next tick with no restart. Falls back to
// defaultConstructionWorkerCap if no pipeline resolves, and never returns < 1 (a 0 cap would leave
// the drain unable to start anything).
func (h *RunConstructionCoordinatorHandler) resolveWorkerCap(ctx context.Context, tasks []*manufacturing.ManufacturingTask) int {
	workerCap := 0
	seen := make(map[string]bool)
	for _, task := range tasks {
		pipelineID := task.PipelineID()
		if pipelineID == "" || seen[pipelineID] {
			continue
		}
		seen[pipelineID] = true
		pipeline, err := h.pipelineRepo.FindByID(ctx, pipelineID)
		if err != nil || pipeline == nil {
			continue
		}
		if mw := pipeline.MaxWorkers(); mw > workerCap {
			workerCap = mw
		}
	}
	if workerCap < 1 {
		workerCap = defaultConstructionWorkerCap
	}
	return workerCap
}
