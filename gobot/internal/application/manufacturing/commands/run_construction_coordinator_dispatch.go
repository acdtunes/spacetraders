package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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

// dedicatedFleet is the Ship.DedicatedFleet() tag this drain PREFERS, defaulting to the shared
// "manufacturing" identity. The default is deliberately EQUAL to operationManufacturing (the
// ClaimShip operation): FindIdleShipsByFleet looks hulls up BY this tag AND ClaimShip authorizes a new
// claim only when the hull's tag equals the operation, so one value must drive both — a mismatch would
// leave the drain unable to claim its own dedicated hull. Parametrized per-launch via cmd.DedicatedFleet;
// read fresh each tick so a live re-pin (or a restart) re-derives preference with no carried state.
func (h *RunConstructionCoordinatorHandler) dedicatedFleet(cmd *RunConstructionCoordinatorCommand) string {
	if cmd.DedicatedFleet != "" {
		return cmd.DedicatedFleet
	}
	return operationManufacturing
}

// selectHaulers builds the tick's ordered claim pool, PREFERRING the drain's own dedicated fleet.
// FindIdleLightHaulers EXCLUDES every dedicated hull by design (ship_pool_manager.go:
// `if ship.DedicatedFleet() != "" { continue }`), so the drain's own dedicated fleet must be
// discovered separately via FindIdleShipsByFleet or its own gate haulers stay invisible while an
// idle unpinned hull gets grabbed opportunistically instead.
//
// This mirrors the contract coordinator's split: FindIdleShipsByFleet surfaces the OWN dedicated
// fleet (system-scoped here — construction legs never jump), FindIdleLightHaulers the opportunistic
// pool. The two pools are DISJOINT (FindIdleLightHaulers excludes every tagged hull), and dedicated
// hulls are placed FIRST so the fan-out pairs them ahead of any opportunistic hull. Opportunistic hulls
// only SUPPLEMENT, when dedicated capacity is insufficient (the default), and are dropped entirely in
// ExclusiveDedicatedFleet mode. A hull pinned to ANOTHER operation is in NEITHER pool, and even if it
// were, ClaimShip rejects it atomically.
func (h *RunConstructionCoordinatorHandler) selectHaulers(ctx context.Context, cmd *RunConstructionCoordinatorCommand, playerID shared.PlayerID, systemSymbol string) ([]*navigation.Ship, error) {
	fleet := h.dedicatedFleet(cmd)

	// The drain's OWN dedicated fleet: idle, cargo-capable members. FindIdleShipsByFleet is fleet-wide
	// (no system filter), so restrict to the operating system here — an out-of-system dedicated hull is
	// UNSELECTABLE, not claimed-then-failed (fail-closed, matching FindIdleLightHaulers' own
	// single-system pre-filter).
	dedicatedIdle, _, err := contract.FindIdleShipsByFleet(ctx, playerID, h.shipRepo, fleet, contract.RequireCargoCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to discover dedicated construction haulers: %w", err)
	}
	dedicatedIdle = haulersInSystem(dedicatedIdle, systemSymbol)

	// EXCLUSIVE MODE (opt-in): once ANY hull carries the fleet tag, the drain is sealed to its
	// dedicated members and never supplements from the opportunistic pool — even when no
	// dedicated hull is dispatchable this tick.
	if cmd.ExclusiveDedicatedFleet {
		active, err := contract.FleetHasMembers(ctx, playerID, h.shipRepo, fleet)
		if err != nil {
			return nil, fmt.Errorf("failed to check dedicated fleet membership: %w", err)
		}
		if active {
			return dedicatedIdle, nil
		}
	}

	// Opportunistic pool: undedicated idle haulers in-system. FindIdleLightHaulers already excludes every
	// dedicated hull and system-filters, so it never double-counts the dedicated pool above. Appended
	// AFTER dedicated so the fan-out always pairs dedicated hulls first (index-paired in planDispatchLots).
	opportunistic, _, err := contract.FindIdleLightHaulers(ctx, playerID, h.shipRepo, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to discover idle haulers: %w", err)
	}
	return append(dedicatedIdle, opportunistic...), nil
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

// constructionLot is one hull's unit of work this tick: a DELIVER_TO_CONSTRUCTION task paired
// with an idle hull, plus the fan-out bookkeeping. A material's SINGLE ready task becomes one non-ephemeral
// lot; the fan-out adds EPHEMERAL clone lots so several hulls work the same material concurrently.
type constructionLot struct {
	task *manufacturing.ManufacturingTask
	ship *navigation.Ship
	// fillCap bounds this lot's PHASE-2 buy so the lots working a material do not collectively buy past
	// what it still needs (the over-supply guard), and is the buy reservation the lot's worker holds
	// against that material for its whole life. A material's caps sum to its outstanding bill net of
	// the workers already in flight for it. 0 = NO cap (the zero value; planned lots always carry one).
	fillCap int
	// ephemeral marks a fan-out CLONE (not one of the pipeline's persisted ready tasks): it does the real
	// source+deliver+record work but skips task-status persistence AND replenishment — the material's
	// original ready task (always dispatched alongside a clone) owns those, so the ready queue stays at
	// the planner's one-task-per-material and the fan-out re-derives parallelism from live hulls each tick.
	ephemeral bool
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
	if l.fillCap > 0 && l.fillCap < capacity {
		return l.fillCap
	}
	return capacity
}

// planDispatchLots fans the ready material-tasks into per-hull lot-tasks so throughput is not capped
// at #materials. It (1) dispatches each existing ready task once (preserving today's per-task
// behavior), skipping a material whose bill is already met; then (2) fans spare idle hulls onto
// materials that still want more concurrent lots — bounded per material by ceil(remaining/hull-load) so a
// material is never over-dispatched, and globally by the WHOLE idle pool up to the materials' total
// remaining requirement (not just #materials or max_workers). Finally it assigns
// each lot a buy cap so concurrent same-material lots never buy past the material's remaining requirement.
// The returned lots hold distinct idle hulls, each drawn from the WHOLE pool by haulerPool so a hull
// already laden with the lot's good takes that lot (adoption before re-buy) even when maxLots means
// only a few hulls will actually be started. maxLots is the tick's free budget under max_workers:
// every lot minted here is dispatched, and a slot a haul frees is refilled by the next tick from live
// hulls rather than from a plan made before that haul began.
func (h *RunConstructionCoordinatorHandler) planDispatchLots(ctx context.Context, tasks []*manufacturing.ManufacturingTask, idleShips []*navigation.Ship, maxLots int) []constructionLot {
	if len(idleShips) == 0 || maxLots <= 0 {
		return nil
	}
	budget := h.materialBuyBudgets(ctx, tasks, representativeLotUnits(idleShips))
	lotCeiling := budget.lotCeiling(len(idleShips), maxLots)

	lots := make([]constructionLot, 0, lotCeiling)
	pool := newHaulerPool(idleShips)

	// Pass 1: one lot per existing ready task, in order, skipping a material whose bill is already met
	// (remaining<=0: a met/racing-replenishment leftover — dispatching it would buy against no demand) or
	// whose per-material lot budget is already full (ceil(remaining/hull-load) — defends the over-supply
	// guard even if the queue somehow over-staged a material).
	for _, task := range tasks {
		if len(lots) >= lotCeiling {
			break
		}
		key := materialKey(task)
		if !budget.wantsAnotherLot(key) {
			continue
		}
		hull := pool.take(task.Good())
		if hull == nil {
			break
		}
		lots = append(lots, constructionLot{task: task, ship: hull})
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
		hull := pool.take(clone.Good())
		if hull == nil {
			break
		}
		lots = append(lots, constructionLot{task: clone, ship: hull, ephemeral: true})
		budget.assign(key)
	}

	budget.assignFillCaps(lots)
	return lots
}

// materialBudget is the tick's fan-out arithmetic for one drain: how many units of each distinct
// material may still be BOUGHT, how many lots have been staged against each, and the hull-load that
// count is sized in. remaining is already net of what in-flight workers are authorized to buy.
type materialBudget struct {
	order     []string
	remaining map[string]int
	repTask   map[string]*manufacturing.ManufacturingTask
	assigned  map[string]int
	lotUnits  int
}

// materialBuyBudgets reads, once per distinct material, how many units may still be BOUGHT this
// tick, plus a representative task to clone for fan-out, in first-seen order for deterministic
// distribution. The budget nets out what in-flight workers are already authorized to buy: their
// loads are paid for but not yet delivered, so the site's bill has not moved for them and sizing
// against the raw bill would buy the same units twice.
func (h *RunConstructionCoordinatorHandler) materialBuyBudgets(ctx context.Context, tasks []*manufacturing.ManufacturingTask, lotUnits int) *materialBudget {
	b := &materialBudget{
		order:     make([]string, 0, len(tasks)),
		remaining: make(map[string]int),
		repTask:   make(map[string]*manufacturing.ManufacturingTask),
		assigned:  make(map[string]int),
		lotUnits:  lotUnits,
	}
	for _, task := range tasks {
		key := materialKey(task)
		if _, seen := b.remaining[key]; !seen {
			b.remaining[key] = h.remainingBill(ctx, task) - h.supplies.reservedUnits(key)
			b.repTask[key] = task
			b.order = append(b.order, key)
		}
	}
	return b
}

// desiredLots is how many hull-load lots this material still wants: ceil(remaining/hull-load), 0
// once its bill is met.
func (b *materialBudget) desiredLots(key string) int {
	return ceilDiv(b.remaining[key], b.lotUnits)
}

// wantsAnotherLot reports whether the material still has unmet bill AND unfilled lot budget.
func (b *materialBudget) wantsAnotherLot(key string) bool {
	return b.remaining[key] > 0 && b.assigned[key] < b.desiredLots(key)
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
// good. An interrupted delivery leaves its load aboard, and pairing was pool-order and cargo-blind:
// the laden hull drew whichever task came up while an empty one re-bought the same material at
// market, so paid-for gate material rode along undelivered. Pairing on the hull's ACTUAL cargo
// re-adopts that load with no cross-tick state to go stale — PHASE-1 deliver-on-hand then unloads it
// before any buy. An all-empty pool hands out in pool order, so dedicated hulls still come first.
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
	next := -1
	for i, ship := range p.ships {
		if p.taken[i] {
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
// planner's, already net of what in-flight workers are authorized to buy, so it is also the buy
// reservation each dispatched lot registers.
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
