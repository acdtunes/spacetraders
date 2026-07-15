package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgTypes "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"golang.org/x/sync/errgroup"
)

// Type aliases matching the factory coordinator's pattern (the container command factory
// builds the command; the handler consumes it).
type RunConstructionCoordinatorCommand = mfgTypes.RunConstructionCoordinatorCommand
type RunConstructionCoordinatorResponse = mfgTypes.RunConstructionCoordinatorResponse

const (
	// constructionDrainTickInterval is the delay between drain ticks for a standing
	// (MaxIterations<=0) run. Sits in the same 30-60s band as the factory coordinator's
	// discovery cadence; overridable per-launch via TickSeconds.
	constructionDrainTickInterval = 30 * time.Second

	noWorkNoReadyConstruction = "no_ready_construction_tasks"
	noWorkNoIdleHauler        = "no_idle_hauler_in_system"

	// constructionOperationContext labels the sourcing/delivery transactions for attribution.
	constructionOperationContext = "construction_supply"

	// defaultConstructionWorkerCap bounds concurrent supplyTask workers when a tick's pipeline
	// exposes no positive max_workers. This is defensive only — readyConstructionTasks yields
	// tasks solely from EXECUTING pipelines, which always carry a max_workers — and mirrors the
	// domain's default construction max_workers so an unset pipeline drains at the width the
	// planner would have chosen (RULINGS #5: a named fallback, not an inline magic number).
	defaultConstructionWorkerCap = 5

	// constructionSupplyTaskDefaultTimeout bounds a single supplyTask so one wedged task can never
	// silently freeze the drain goroutine (sp-6zkg). The drain dispatches workers under errgroup and
	// joins them with group.Wait(); before this, a worker blocked in an unbounded downstream wait (a
	// hull already at the gate whose supply 4219s, a navigation/dock that never returns, a bad task
	// state) held Wait() forever — the coordinator stayed RUNNING but went fully SILENT, cleared only
	// by a daemon bounce. Generous enough never to cut a legitimate in-system source+deliver round trip
	// (construction legs are single-system, RULINGS #14); firm enough to convert an "indefinite hang"
	// into a logged, retried tick. A named default (RULINGS #5), tunable via the handler's taskTimeout.
	//
	// sp-ubwi: RAISED 10m→30m. The 10m wrapped the ENTIRE supplyTask (claim→source→route-to-gate-with-
	// refuel-hops→dock→supply→record); a legit multi-hop light-hauler round trip exceeds 10m, so healthy
	// long hauls were abandoned AT the finish line and the retry grabbed a FRESH empty hull and re-bought,
	// stranding the laden hull out of the pool. 30m clears an in-system multi-hop haul while still
	// converting a genuine indefinite hang into a logged, retried tick. Overridable per-launch via
	// SupplyTaskTimeoutSeconds ([manufacturing].construction_supply_task_timeout_seconds).
	constructionSupplyTaskDefaultTimeout = 30 * time.Minute

	// defaultConstructionLotUnits is the fallback per-lot hull-load used to size the fan-out (sp-ubwi)
	// when an idle hull exposes no cargo capacity — a light hauler's hold. The fan-out prefers each
	// idle hull's ACTUAL capacity (representativeLotUnits); this named default (RULINGS #5) only backstops
	// a capacity-less hull so ceil(remaining/lotUnits) never divides by zero.
	defaultConstructionLotUnits = 40
)

// ConstructionProducer is the narrow slice of the shared ProductionExecutor the drain
// delegates ALL sourcing and delivery to — so the drain adds NO duplicate sourcing/nav
// logic. *services.ProductionExecutor satisfies it (ProduceGood sources the material into
// the hauler; DeliverToConstructionSite flies it to the site and supplies it).
type ConstructionProducer interface {
	ProduceGood(ctx context.Context, ship *navigation.Ship, node *goods.SupplyChainNode, systemSymbol string, playerID int, opContext *shared.OperationContext, inputsOnly bool) (*mfgServices.ProductionResult, error)
	DeliverToConstructionSite(ctx context.Context, shipSymbol, good, site string, playerID shared.PlayerID) (int, error)
}

// ConstructionActivator is the surviving activator wired each tick: it promotes PENDING
// DELIVER_TO_CONSTRUCTION tasks (deps complete, re-sourced) to READY. *services.SupplyMonitor
// satisfies it via ActivateConstructionTasks — the drain adds NO new activation logic.
type ConstructionActivator interface {
	ActivateConstructionTasks(ctx context.Context) int
}

// ConstructionTreeResolver builds the scarcity-gated supply-chain dependency tree for a FABRICATE
// material (sp-yfzi), so the drain PRODUCES a scarce intermediate that has a factory (recursing its
// sub-chain) instead of the old flat one-level "fabricate root, buy every immediate input" node.
// *services.SupplyChainResolver satisfies it via BuildDependencyTree. It is an OPTIONAL collaborator
// wired by SetTreeResolver: left unset (nil), the drain falls back to the one-level node — the
// pre-sp-yfzi construction behaviour, so a coordinator built without it is byte-identical (and the
// buy-final path never consults it at all).
type ConstructionTreeResolver interface {
	BuildDependencyTree(ctx context.Context, targetGood, systemSymbol string, playerID int) (*goods.SupplyChainNode, error)
}

// RunConstructionCoordinatorHandler is the thin construction-supply drain (sp-382j). Each
// tick it: runs the activator, polls READY DELIVER_TO_CONSTRUCTION tasks from EXECUTING
// pipelines, claims idle in-system haulers under the shared "manufacturing" identity, then
// delegates source+deliver to the ProductionExecutor and records pipeline progress. An
// unsourceable material is PARKED for resupply (never failed). It is queue-driven (not
// tree-driven) and holds no cross-tick state — a restart re-polls persistence and resumes.
type RunConstructionCoordinatorHandler struct {
	taskRepo     manufacturing.TaskRepository
	pipelineRepo manufacturing.PipelineRepository
	shipRepo     navigation.ShipRepository
	producer     ConstructionProducer
	// newActivator builds the surviving activator for a specific player each tick. It is a
	// per-player factory (not a fixed instance) because SupplyMonitor bakes in the playerID at
	// construction, whereas this handler is registered once and serves the command's PlayerID —
	// the same player-agnostic contract ProduceGood/ClaimShip follow. nil disables activation.
	newActivator func(playerID int) ConstructionActivator
	clock        shared.Clock
	// resolver builds the scarcity-gated dependency tree for a FABRICATE material (sp-yfzi). Optional
	// (wired by SetTreeResolver); nil falls back to the pre-sp-yfzi one-level fabricate node.
	resolver ConstructionTreeResolver
	// recordMu serializes the pipeline delivery read-modify-write (recordDelivery) across the
	// concurrent supplyTask workers (sp-01eh): two workers supplying the SAME pipeline must not
	// both load-add-store its material counters and lose an update. It guards an in-tick section
	// only, not any cross-tick/persisted state (RULINGS #2 unaffected).
	recordMu sync.Mutex
	// taskTimeout bounds a single supplyTask (claim→source→deliver→record) so one wedged task can
	// never silently freeze the whole drain goroutine (sp-6zkg). Defaulted in the constructor to
	// constructionSupplyTaskDefaultTimeout; overridable (RULINGS #5) — the daemon can tune it and the
	// in-package tests set a tiny bound to keep the timeout test fast.
	taskTimeout time.Duration
}

// NewRunConstructionCoordinatorHandler builds the drain. clock defaults to a RealClock when nil.
func NewRunConstructionCoordinatorHandler(
	taskRepo manufacturing.TaskRepository,
	pipelineRepo manufacturing.PipelineRepository,
	shipRepo navigation.ShipRepository,
	producer ConstructionProducer,
	newActivator func(playerID int) ConstructionActivator,
	clock shared.Clock,
) *RunConstructionCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunConstructionCoordinatorHandler{
		taskRepo:     taskRepo,
		pipelineRepo: pipelineRepo,
		shipRepo:     shipRepo,
		producer:     producer,
		newActivator: newActivator,
		clock:        clock,
		taskTimeout:  constructionSupplyTaskDefaultTimeout,
	}
}

// SetTreeResolver wires the scarcity-gated supply-chain resolver (sp-yfzi) so the drain PRODUCES a
// FABRICATE material's scarce intermediates recursively instead of the flat one-level node. Optional
// — the daemon injects the shared goodsResolver singleton; left unset the drain uses the one-level
// fallback (pre-sp-yfzi behaviour). A setter (not a constructor arg) keeps the existing coordinator
// tests, which never build the resolver tree, unchanged (nil → fallback → byte-identical).
func (h *RunConstructionCoordinatorHandler) SetTreeResolver(resolver ConstructionTreeResolver) {
	h.resolver = resolver
}

// Handle runs the standing drain loop: drain each tick until the container is cancelled
// (or MaxIterations is reached for a bounded run). The per-tick delay is raced against
// cancellation so a stop is prompt. reconcile lives in drainOnce (the unit tests drive).
func (h *RunConstructionCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunConstructionCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type for construction coordinator")
	}
	logger := common.LoggerFromContext(ctx)

	tick := constructionDrainTickInterval
	if cmd.TickSeconds > 0 {
		tick = time.Duration(cmd.TickSeconds) * time.Second
	}

	iterations := 0
	var last *RunConstructionCoordinatorResponse
	for {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		default:
		}

		resp, err := h.drainOnce(ctx, cmd)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Construction drain tick failed: %v", err), nil)
		} else {
			last = resp
		}

		iterations++
		if cmd.MaxIterations > 0 && iterations >= cmd.MaxIterations {
			return last, nil
		}

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return last, ctx.Err()
		}
	}
}

// drainOnce is one reconcile tick: activate, poll ready construction tasks from EXECUTING
// pipelines, and source+deliver each with a claimed idle hauler.
func (h *RunConstructionCoordinatorHandler) drainOnce(ctx context.Context, cmd *RunConstructionCoordinatorCommand) (*RunConstructionCoordinatorResponse, error) {
	logger := common.LoggerFromContext(ctx)

	// Surviving activator (sp-jav2 kept the subpackage): PENDING -> READY for construction
	// tasks whose deps are complete (and re-source deferred ones). NO new activation logic.
	// Per-step enter/exit + count logging (sp-6zkg): a stuck activation used to be an
	// undiagnosable silent block; the enter/exit bracket makes it visible in the log stream.
	if h.newActivator != nil {
		if activator := h.newActivator(cmd.PlayerID); activator != nil {
			logger.Log("INFO", "Construction drain: activating construction tasks", nil)
			promoted := activator.ActivateConstructionTasks(ctx)
			logger.Log("INFO", fmt.Sprintf("Construction drain: activation done (%d task(s) promoted to READY)", promoted), map[string]interface{}{"promoted": promoted})
		}
	}

	tasks, err := h.readyConstructionTasks(ctx, cmd.PlayerID)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return &RunConstructionCoordinatorResponse{NoWorkReason: noWorkNoReadyConstruction}, nil
	}

	// Operating system: the launch system if given, else derived from the first ready task's
	// construction site (gate-construction tasks share the home gate's system). This lets the
	// bootstrap gate launch the drain with no system while still restricting hauler discovery
	// to the site's system — construction legs are in-system, so an out-of-system hull is
	// UNSELECTABLE here rather than claimed-then-failed.
	systemSymbol := cmd.SystemSymbol
	if systemSymbol == "" {
		systemSymbol = shared.ExtractSystemSymbol(tasks[0].ConstructionSite())
	}

	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	// sp-e55b: discover the drain's OWN dedicated fleet FIRST, then supplement with opportunistic idle
	// hulls. The old single call to FindIdleLightHaulers structurally EXCLUDED every dedicated hull, so
	// the drain's own gate haulers were invisible and it poached opportunistic hulls instead.
	idleShips, err := h.selectHaulers(ctx, cmd, playerID, systemSymbol)
	if err != nil {
		return nil, err
	}
	if len(idleShips) == 0 {
		return &RunConstructionCoordinatorResponse{NoWorkReason: noWorkNoIdleHauler}, nil
	}

	// Return this container's claims to the idle pool at tick end so a drained hull is
	// reusable next tick (ship claims also auto-release on restart via ReleaseAllActive).
	defer h.releaseClaims(ctx, cmd.ContainerID, playerID)

	// Fan the ready materials into concurrent lot-tasks and pair each with an idle hauler (sp-ubwi).
	// The pipeline stages exactly ONE task per material, so pairing 1:1 capped throughput at
	// #materials-remaining — making --max-workers dead (2 materials => only 2 haulers ever worked,
	// regardless of the cap). planDispatchLots fans each material into ceil(remaining/hull-load)
	// concurrent lot-tasks — BOUNDED by max_workers and by the material's own remaining requirement (so
	// concurrent lots never buy past what the gate needs) — so len(lots) scales to the hauler pool and
	// the already-wired errgroup dispatches all of them.
	workerCap := h.resolveWorkerCap(ctx, tasks)
	lots := h.planDispatchLots(ctx, tasks, idleShips)
	if len(lots) == 0 {
		// Every ready material's bill is already met (a met/racing-replenishment leftover) — nothing to
		// buy without over-supplying. Report a clean no-drain tick rather than dispatch an empty haul.
		return &RunConstructionCoordinatorResponse{TasksDrained: 0}, nil
	}

	// Dispatch the lot-tasks CONCURRENTLY (sp-01eh regression-restore): one goroutine per hull, each
	// claiming + sourcing + delivering its OWN lot in parallel. The pipeline's max_workers is WIRED as
	// the concurrency bound via errgroup.SetLimit, so throughput scales with the idle pool (capped)
	// instead of one-hull-at-a-time. No worker-container machinery is revived (Admiral veto): this stays
	// the thin drain, now fanned out past #materials.
	// Per-tick summary (sp-6zkg observability): a drain tick can no longer be silent — it always
	// announces how much work it is about to dispatch, so a stall is visible against this line.
	logger.Log("INFO", fmt.Sprintf("Construction drain: dispatching %d lot-task(s) across %d idle hauler(s) for %d ready material-task(s) (worker cap %d)", len(lots), len(idleShips), len(tasks), workerCap), map[string]interface{}{
		"lot_tasks": len(lots), "ready_tasks": len(tasks), "idle_haulers": len(idleShips), "worker_cap": workerCap,
	})
	var drained atomic.Int64
	var group errgroup.Group
	group.SetLimit(workerCap)
	for i := range lots {
		lot := lots[i]
		group.Go(func() error {
			// Atomic claim under the drain's dedicated-fleet identity (RULINGS #7): a hull pinned to
			// ANOTHER fleet, or grabbed since discovery, is rejected at the DB, not clobbered. The claim
			// tx is the concurrency guard — each worker claims its OWN distinct hull, so there is no
			// double-claim and no poaching of another operation's pinned hull. The operation string equals
			// the preferred fleet tag (h.dedicatedFleet, default "manufacturing" == operationManufacturing)
			// so the drain can claim its OWN dedicated hulls (tag == operation) while a foreign-pinned hull
			// is still rejected — the same coupling the contract coordinator uses (sp-e55b).
			if err := h.shipRepo.ClaimShip(ctx, lot.ship.ShipSymbol(), cmd.ContainerID, playerID, h.dedicatedFleet(cmd)); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Skipping hauler %s for construction: claim rejected: %v", lot.ship.ShipSymbol(), err), nil)
				return nil // lot stays undispatched; the material's task is retried next tick
			}
			// supplyTaskBounded (sp-6zkg): a per-task deadline so a single wedged task can never
			// hold group.Wait() — and thus this whole tick / the coordinator goroutine — forever.
			if h.supplyTaskBounded(ctx, cmd, systemSymbol, lot, playerID) {
				drained.Add(1)
			}
			// Task-level failures are recorded per worker (fail/defer); never propagated, so one
			// worker's failure does not abort its peers mid-flight.
			return nil
		})
	}
	_ = group.Wait() // workers always return nil; Wait joins them before the tick reports

	return &RunConstructionCoordinatorResponse{TasksDrained: int(drained.Load())}, nil
}

// dedicatedFleet is the Ship.DedicatedFleet() tag this drain PREFERS, defaulting to the shared
// "manufacturing" identity (sp-e55b). The default is deliberately EQUAL to operationManufacturing (the
// ClaimShip operation): FindIdleShipsByFleet looks hulls up BY this tag AND ClaimShip authorizes a new
// claim only when the hull's tag equals the operation, so one value must drive both — a mismatch would
// leave the drain unable to claim its own dedicated hull. Parametrized per-launch via cmd.DedicatedFleet
// (RULINGS #5); read fresh each tick so a live re-pin (or a restart) re-derives preference with no
// carried state (RULINGS #2).
func (h *RunConstructionCoordinatorHandler) dedicatedFleet(cmd *RunConstructionCoordinatorCommand) string {
	if cmd.DedicatedFleet != "" {
		return cmd.DedicatedFleet
	}
	return operationManufacturing
}

// selectHaulers builds the tick's ordered claim pool, PREFERRING the drain's own dedicated fleet
// (sp-e55b). The bug it fixes: the drain used to consult ONLY FindIdleLightHaulers, which by design
// EXCLUDES every dedicated hull (ship_pool_manager.go: `if ship.DedicatedFleet() != "" { continue }`) —
// so its own gate haulers (TORWIND-C/-D, pinned "manufacturing") were structurally INVISIBLE while an
// idle UNPINNED former-trade hull was grabbed opportunistically.
//
// The fix mirrors the contract coordinator's split: FindIdleShipsByFleet surfaces the OWN dedicated
// fleet (system-scoped here — construction legs never jump), FindIdleLightHaulers the opportunistic
// pool. The two pools are DISJOINT (FindIdleLightHaulers excludes every tagged hull), and dedicated
// hulls are placed FIRST so the fan-out pairs them ahead of any opportunistic hull. Opportunistic hulls
// only SUPPLEMENT, when dedicated capacity is insufficient (the default), and are dropped entirely in
// ExclusiveDedicatedFleet mode. A hull pinned to ANOTHER operation is in NEITHER pool, and even if it
// were, ClaimShip rejects it atomically (RULINGS #7).
func (h *RunConstructionCoordinatorHandler) selectHaulers(ctx context.Context, cmd *RunConstructionCoordinatorCommand, playerID shared.PlayerID, systemSymbol string) ([]*navigation.Ship, error) {
	fleet := h.dedicatedFleet(cmd)

	// The drain's OWN dedicated fleet: idle, cargo-capable members. FindIdleShipsByFleet is fleet-wide
	// (no system filter), so restrict to the operating system here — an out-of-system dedicated hull is
	// UNSELECTABLE, not claimed-then-failed (sp-qr3v fail-closed, matching FindIdleLightHaulers' own
	// single-system pre-filter).
	dedicatedIdle, _, err := contract.FindIdleShipsByFleet(ctx, playerID, h.shipRepo, fleet, contract.RequireCargoCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to discover dedicated construction haulers: %w", err)
	}
	dedicatedIdle = haulersInSystem(dedicatedIdle, systemSymbol)

	// EXCLUSIVE MODE (opt-in, contract sp-wq7r parity): once ANY hull carries the fleet tag, the drain is
	// sealed to its dedicated members and never supplements from the opportunistic pool — even when no
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
// system-scope the fleet-wide FindIdleShipsByFleet result (sp-e55b).
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

// constructionLot is one hull's unit of work this tick (sp-ubwi): a DELIVER_TO_CONSTRUCTION task paired
// with an idle hull, plus the fan-out bookkeeping. A material's SINGLE ready task becomes one non-ephemeral
// lot; the fan-out adds EPHEMERAL clone lots so several hulls work the same material concurrently.
type constructionLot struct {
	task *manufacturing.ManufacturingTask
	ship *navigation.Ship
	// fillCap bounds this lot's PHASE-2 buy so concurrent lots of the same material do not collectively
	// buy past its remaining requirement (the over-supply guard). 0 = NO cap: the sole lot for a material
	// fills toward the full outstanding bill (sp-2me2 preserved); >0 = a per-lot slice of the remaining
	// requirement, sized to a hull-load, so the slices across a material's lots sum to its remaining.
	fillCap int
	// ephemeral marks a fan-out CLONE (not one of the pipeline's persisted ready tasks): it does the real
	// source+deliver+record work but skips task-status persistence AND replenishment — the material's
	// original ready task (always dispatched alongside a clone) owns those, so the ready queue stays at
	// the planner's one-task-per-material and the fan-out re-derives parallelism from live hulls each tick.
	ephemeral bool
}

// planDispatchLots fans the ready material-tasks into per-hull lot-tasks (sp-ubwi), the fix for the
// #materials concurrency ceiling. It (1) dispatches each existing ready task once (preserving today's
// per-task behavior), skipping a material whose bill is already met; then (2) fans spare idle hulls onto
// materials that still want more concurrent lots — bounded per material by ceil(remaining/hull-load) so a
// material is never over-dispatched, and globally by the WHOLE idle pool up to the materials' total
// remaining requirement (sp-vr9q: tap the pool, not just #materials or max_workers). Finally it assigns
// each lot a buy cap so concurrent same-material lots never buy past the material's remaining requirement.
// The returned lots are index-paired to distinct idle hulls (lots[i].ship == idleShips[i]); the caller's
// errgroup SetLimit(max_workers) caps how many run at once, so surplus lots form the top-up queue that
// keeps a slow lane from collapsing effective concurrency to 1 (sp-vr9q #2).
func (h *RunConstructionCoordinatorHandler) planDispatchLots(ctx context.Context, tasks []*manufacturing.ManufacturingTask, idleShips []*navigation.Ship) []constructionLot {
	if len(idleShips) == 0 {
		return nil
	}
	lotUnits := representativeLotUnits(idleShips)

	// Per-material outstanding budget (units we may still BUY this tick, read once) + a representative
	// task to clone for fan-out, in first-seen order for deterministic distribution.
	order := make([]string, 0, len(tasks))
	remaining := make(map[string]int)
	repTask := make(map[string]*manufacturing.ManufacturingTask)
	for _, task := range tasks {
		key := materialKey(task)
		if _, seen := remaining[key]; !seen {
			remaining[key] = h.remainingBill(ctx, task)
			repTask[key] = task
			order = append(order, key)
		}
	}

	// Global lot ceiling (sp-vr9q): tap the WHOLE idle pool, bounded only by the materials' total remaining
	// requirement (sum of ceil(remaining/hull-load) across distinct materials) — never mint a lot no
	// material needs (the over-supply guard's global counterpart), but DO mint past #materials and past
	// max_workers so the errgroup has a top-up queue. Concurrency stays capped at max_workers via SetLimit
	// in drainOnce: when the pool exceeds max_workers the surplus lots queue and each freed worker slot
	// pulls the next, so one slow lane can no longer collapse effective concurrency to 1 (the incident).
	// The old min(pool, max(#materials, max_workers)) ceiling capped lots at max_workers whenever #materials
	// was small (the common case), leaving no queue to top up from.
	lotCeiling := len(idleShips)
	if demand := totalLotDemand(order, remaining, lotUnits); demand < lotCeiling {
		lotCeiling = demand
	}

	lots := make([]constructionLot, 0, lotCeiling)
	assigned := make(map[string]int)

	// Pass 1: one lot per existing ready task, in order, skipping a material whose bill is already met
	// (remaining<=0: a met/racing-replenishment leftover — dispatching it would buy against no demand) or
	// whose per-material lot budget is already full (ceil(remaining/hull-load) — defends the over-supply
	// guard even if the queue somehow over-staged a material).
	for _, task := range tasks {
		if len(lots) >= lotCeiling {
			break
		}
		key := materialKey(task)
		if remaining[key] <= 0 || assigned[key] >= ceilDiv(remaining[key], lotUnits) {
			continue
		}
		lots = append(lots, constructionLot{task: task, ship: idleShips[len(lots)]})
		assigned[key]++
	}

	// Pass 2: fan spare hulls onto the materials that still want more concurrent lots (ephemeral clones),
	// picking the neediest each time so multiple materials share the pool fairly.
	for len(lots) < lotCeiling {
		key := neediestMaterial(order, remaining, assigned, lotUnits)
		if key == "" {
			break // no material wants another lot (every remaining requirement is covered)
		}
		clone := nextConstructionDeliveryTask(repTask[key])
		if err := clone.MarkReady(); err != nil {
			break // cannot stage a clone lot-task; stop fanning (all originals are already dispatched)
		}
		lots = append(lots, constructionLot{task: clone, ship: idleShips[len(lots)], ephemeral: true})
		assigned[key]++
	}

	assignFillCaps(lots, remaining, lotUnits)
	return lots
}

// neediestMaterial returns the material with the greatest unmet lot need (desired − assigned), where
// desired = ceil(remaining/hull-load); "" when every material's lot budget is already filled. Ties break
// by first-seen order for determinism.
func neediestMaterial(order []string, remaining, assigned map[string]int, lotUnits int) string {
	best := ""
	bestNeed := 0
	for _, key := range order {
		need := ceilDiv(remaining[key], lotUnits) - assigned[key]
		if need > bestNeed {
			bestNeed = need
			best = key
		}
	}
	return best
}

// assignFillCaps sets each lot's buy cap so concurrent same-material lots never buy past the material's
// remaining requirement (the sp-ubwi over-supply guard). A material with a SINGLE lot gets cap 0 (no cap:
// fill toward the full outstanding bill, sp-2me2 preserved). A material with MULTIPLE lots has its
// remaining requirement sliced into hull-load caps that sum to the remaining, so the concurrent lots
// together buy at most what the gate still needs.
func assignFillCaps(lots []constructionLot, remaining map[string]int, lotUnits int) {
	counts := make(map[string]int)
	for i := range lots {
		counts[materialKey(lots[i].task)]++
	}
	budget := make(map[string]int, len(remaining))
	for key, rem := range remaining {
		budget[key] = rem
	}
	for i := range lots {
		key := materialKey(lots[i].task)
		if counts[key] <= 1 {
			lots[i].fillCap = 0 // sole lot: fill toward the full outstanding bill (no per-lot cap)
			continue
		}
		slice := lotUnits
		if budget[key] < slice {
			slice = budget[key]
		}
		if slice < 0 {
			slice = 0
		}
		lots[i].fillCap = slice
		budget[key] -= slice
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
// requirement this tick — sum of ceil(remaining/hull-load) (sp-vr9q). It bounds the fan-out so the drain
// never stages a lot no material needs (the over-supply guard's global counterpart), while deliberately
// allowing lots to exceed max_workers so the errgroup gains a top-up queue that keeps a slow lane from
// starving the pool.
func totalLotDemand(order []string, remaining map[string]int, lotUnits int) int {
	total := 0
	for _, key := range order {
		total += ceilDiv(remaining[key], lotUnits)
	}
	return total
}

// resolveWorkerCap is the concurrency bound for this tick's dispatch: the largest max_workers
// among the distinct EXECUTING pipelines backing the ready tasks. sp-01eh WIRES pipeline
// max_workers (previously stored but read by no dispatcher — vestigial) into an actual cap on
// concurrent supplyTask workers. Falls back to defaultConstructionWorkerCap if no pipeline
// resolves, and never returns < 1 (SetLimit(0) would deadlock the group).
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

// supplyTask advances one construction material for the claimed hauler. It runs in two phases:
//
//	PHASE 1 (deliver-on-hand, sp-9ptm): if the hull ALREADY HOLDS units of the material the site
//	still needs, unload them to the site FIRST — UNCONDITIONALLY, before and independent of the
//	source-buy gate below. Delivering cargo already aboard has zero market impact and always
//	advances the gate (RULINGS #1 never-skip); it is NOT a buy, so the sp-a5j7 fail-closed buy
//	guard does not govern it (RULINGS #4 untouched). This is the fix for stranded gate material:
//	a laden hull released mid-delivery (pipeline/coordinator restart) used to reach ProduceGood
//	first and park on a dry source WITHOUT ever unloading.
//
//	PHASE 2 (source-then-deliver): source the still-outstanding remainder via the shared engine
//	(the fail-closed buy path, UNCHANGED) and deliver it. Reached for the common empty-hull drain
//	(nothing on-hand) or when the hull's on-hand load did not cover the full bill.
//
// Returns true when the tick delivered anything for this task. A genuinely-unsourceable remainder
// is deferred (parked PENDING, source cleared) rather than failed (RULINGS #1) — but on-hand cargo
// that was already delivered is never stranded: such a task advances (completed; the outstanding
// remainder re-stages via replenishment).
func (h *RunConstructionCoordinatorHandler) supplyTask(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, lot constructionLot, playerID shared.PlayerID) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task
	ship := lot.ship

	if err := task.AssignShip(ship.ShipSymbol()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not assign hauler to construction task %s: %v", task.ID(), err), nil)
		return false
	}
	if err := task.StartExecution(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not start construction task %s: %v", task.ID(), err), nil)
		return false
	}

	// ── PHASE 1: deliver ON-HAND cargo first, bypassing the source-buy gate (sp-9ptm). Gated ONLY
	// by "does the site still need this good" so a met bill never over-delivers. The common empty
	// hull (on-hand == 0) skips this entirely and behaves exactly as before (RULINGS #2).
	deliveredOnHand := 0
	var pipeline *manufacturing.ManufacturingPipeline
	if onHandUnits(ship, task.Good()) > 0 && h.remainingBill(ctx, task) > 0 {
		delivered, err := h.producer.DeliverToConstructionSite(ctx, ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
		if err != nil {
			// A 4219 'ship has 0 units' means the on-hand cargo was PHANTOM (the cache was never
			// decremented after an earlier supply, sp-v5d1): resync the hull + defer, never fail/loop.
			if isPhantomCargoSupplyError(err) {
				return h.handlePhantomCargo(ctx, task, nil, ship, playerID, 0, lot.ephemeral)
			}
			if !lot.ephemeral {
				h.failTask(ctx, task, fmt.Sprintf("delivering on-hand %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
			}
			return false
		}
		if delivered > 0 {
			deliveredOnHand = delivered
			pipeline = h.recordDelivery(ctx, task, delivered)
			// The on-hand load met the site's remaining bill: complete now — never buy past demand.
			if h.remainingForGoodLocked(pipeline, task.Good()) <= 0 {
				return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand, lot.ephemeral)
			}
			// Otherwise fall through to source the still-outstanding remainder.
		}
	}

	// ── PHASE 2: source + deliver the REMAINDER via the shared engine.
	// Fill the hauler TOWARD hull capacity before delivering (sp-2me2): stamp the material's
	// outstanding bill (now net of any on-hand delivery above) as the executor's hull-fill target so
	// a full round-trip carries ~a hull, not one ~trade-volume tranche (~1/4 hull, which quadrupled
	// the round-trips). The executor loops market buys until the hold is full, the bill is met, or a
	// money/price guard trips (fail-closed). fraction 0 => the full-hull default resolved in the
	// executor; the fraction is the RULINGS #5 seam a per-run config can later tighten. A 0 bill
	// (pipeline/material unreadable) leaves the executor to fill to full capacity — a supply is never
	// harmful.
	//
	// sp-ubwi: when this lot is one of SEVERAL fanned onto the same material this tick, its buy is
	// capped to a hull-load SLICE (lot.fillCap) so the concurrent lots together never buy past the
	// material's remaining requirement (the over-supply guard). The SOLE lot for a material carries
	// fillCap 0 and fills toward the full outstanding bill exactly as before (sp-2me2).
	fillTarget := h.remainingBill(ctx, task)
	if lot.fillCap > 0 && lot.fillCap < fillTarget {
		fillTarget = lot.fillCap
	}
	ctx = mfgServices.WithHullFillTarget(ctx, fillTarget, 0)

	// Source the material INTO the hauler on the shared engine, honoring the planner's
	// already-made buy-vs-produce decision recorded on the task: a direct BUY of the final good
	// (source market resolved, no factory), or a FABRICATION (a factory resolved) driven as an
	// AcquisitionFabricate node so the engine buys the inputs, feeds the factory, and harvests
	// the output into the hauler. sp-qmp8 restores this fabricate sourcing — a buy-only drain
	// explodes the market bid and cannot fill the gate at scale (regression from sp-jav2). No
	// duplicate sourcing logic either way; the shared engine owns sourcing. sp-yfzi: a FABRICATE
	// material now resolves the FULL scarcity-gated tree (produce scarce intermediates, buy abundant)
	// via the shared resolver, bounded by the pipeline's depth; a buy-final material is unchanged.
	node := h.constructionSourcingNode(ctx, cmd, task, systemSymbol, cmd.PlayerID)

	// Mark the run as construction supply so the engine's RESALE-margin guards (chain-margin
	// sp-iv65, crushed-sink bp6f #3) are scoped out — the harvested output is delivered to the
	// gate, never resold. INPUT buys still pass the full money-guard stack (RULINGS #4). The
	// hull-fill target stamped above rides on ctx, so produceCtx carries both (sp-2me2 + sp-qmp8).
	produceCtx := shared.WithConstructionSupply(ctx)
	// sp-vh1s — under unified gate-fill, mark this run a UNIFIED GATE NODE carrying the gate waypoint.
	// IsUnifiedGateNode is then true through the whole tree (ctx threads by value), so the source
	// factory's output-buy is THROUGHPUT-PACED (k×tv/hr — the dropped price ceiling's replacement) and
	// lane B's per-node gates go MARGIN-BLIND (Admiral sign-off). OFF stamps nothing (byte-identical).
	if cmd.UnifiedGateFill {
		produceCtx = mfgServices.WithUnifiedGateFill(produceCtx, true)
		produceCtx = mfgServices.WithDeliveryTarget(produceCtx, mfgServices.ConstructionSiteTarget(task.ConstructionSite()))
	}
	// sp-to2v — the fabrication-efficiency feeding policy for the drain's per-material production
	// (balanced-to-limiting input feeding, saturation-capped tranches, taproot-first, feed-responsive-
	// only). Same executor delivery policy a goods factory runs; OFF (the default) stamps nothing →
	// greedy byte-identical feeding.
	if cmd.FabricationEfficiency {
		produceCtx = mfgServices.WithFeedingPolicy(produceCtx, cmd.FeedSaturationMaxUnits, cmd.FeedSaturationMinUnits, cmd.FeedNonResponsiveGoods, false)
	}
	result, err := h.producer.ProduceGood(produceCtx, ship, node, systemSymbol, cmd.PlayerID, h.operationContext(cmd), false)
	if err != nil {
		// A hard sourcing error on the remainder must not nuke on-hand progress already recorded:
		// if the hull delivered its on-hand load this tick, complete the task (it advanced the gate)
		// and let replenishment re-stage the remainder. Only a task that delivered NOTHING fails.
		if deliveredOnHand > 0 {
			return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand, lot.ephemeral)
		}
		if !lot.ephemeral {
			h.failTask(ctx, task, fmt.Sprintf("sourcing %s failed: %v", task.Good(), err))
		}
		return false
	}
	if result == nil || result.QuantityAcquired == 0 {
		// Dry / no eligible source (the sp-a5j7 fail-closed park, UNCHANGED). If on-hand cargo was
		// already delivered this tick, NEVER strand it: the task advanced, so complete it and let
		// replenishment re-stage the unsourceable remainder for the SupplyMonitor to re-source. Only
		// an empty-of-the-material hull defers here (RULINGS #1 never-skip) — the incident's fix. A
		// fan-out CLONE never defers: its material's original ready task (dispatched alongside) owns the
		// defer, so the clone just abandons its empty trip.
		if deliveredOnHand > 0 {
			return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand, lot.ephemeral)
		}
		if !lot.ephemeral {
			h.deferTask(ctx, task)
		}
		return false
	}

	delivered, err := h.producer.DeliverToConstructionSite(ctx, ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
	if err != nil {
		// A 4219 on the sourced load is the same phantom-cargo signal: resync + recover without failing.
		// On-hand progress already recorded this tick is preserved by handlePhantomCargo (never stranded).
		if isPhantomCargoSupplyError(err) {
			return h.handlePhantomCargo(ctx, task, pipeline, ship, playerID, deliveredOnHand, lot.ephemeral)
		}
		if deliveredOnHand > 0 {
			return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand, lot.ephemeral)
		}
		if !lot.ephemeral {
			h.failTask(ctx, task, fmt.Sprintf("delivering %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
		}
		return false
	}

	pipeline = h.recordDelivery(ctx, task, delivered)
	return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand+delivered, lot.ephemeral)
}

// supplyTaskTimeout is the per-task deadline (sp-6zkg), defaulting to constructionSupplyTaskDefaultTimeout.
func (h *RunConstructionCoordinatorHandler) supplyTaskTimeout() time.Duration {
	if h.taskTimeout > 0 {
		return h.taskTimeout
	}
	return constructionSupplyTaskDefaultTimeout
}

// effectiveSupplyTaskTimeout resolves the per-supplyTask deadline for this run (sp-ubwi): a per-launch
// SupplyTaskTimeoutSeconds ([manufacturing].construction_supply_task_timeout_seconds) wins, else the
// handler default (supplyTaskTimeout — the raised 30m, or a test override). This is the seam that made
// the timeout CONFIGURABLE instead of the hardcoded 10m that abandoned healthy long hauls at the gate.
func (h *RunConstructionCoordinatorHandler) effectiveSupplyTaskTimeout(cmd *RunConstructionCoordinatorCommand) time.Duration {
	if cmd != nil && cmd.SupplyTaskTimeoutSeconds > 0 {
		return time.Duration(cmd.SupplyTaskTimeoutSeconds) * time.Second
	}
	return h.supplyTaskTimeout()
}

// supplyTaskBounded runs supplyTask under a per-task deadline so a single wedged task can NEVER hold
// group.Wait() — and thus the whole drain goroutine — indefinitely (sp-6zkg). The task body runs on a
// child goroutine over a timeout ctx; the worker is reclaimed the instant the task finishes OR the
// deadline elapses, whichever comes first, so the tick always makes progress and always reports. This
// is the hard safety net: even a downstream op that ignored ctx entirely (the "silent for hours until
// a daemon bounce" incident) can no longer freeze the coordinator — at worst its goroutine unwinds
// later while the drain keeps ticking, and because taskCtx is cancelled the money paths abort rather
// than spend (RULINGS #4). done is buffered so a late finish never blocks a possibly-orphaned child.
// Per-step enter/exit logging makes a slow/wedged task diagnosable rather than an undiagnosable hang.
func (h *RunConstructionCoordinatorHandler) supplyTaskBounded(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, lot constructionLot, playerID shared.PlayerID) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task
	ship := lot.ship
	timeout := h.effectiveSupplyTaskTimeout(cmd)
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.Log("INFO", fmt.Sprintf("Construction drain: START supply of %s via %s -> %s (timeout %s)", task.Good(), ship.ShipSymbol(), task.ConstructionSite(), timeout), map[string]interface{}{
		"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
	})

	done := make(chan bool, 1)
	go func() { done <- h.supplyTask(taskCtx, cmd, systemSymbol, lot, playerID) }()

	select {
	case drained := <-done:
		logger.Log("INFO", fmt.Sprintf("Construction drain: END supply of %s via %s (drained=%t)", task.Good(), ship.ShipSymbol(), drained), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "task": task.ID(), "drained": drained,
		})
		return drained
	case <-taskCtx.Done():
		logger.Log("ERROR", fmt.Sprintf("Construction drain: supply of %s via %s exceeded %s — ABANDONING this tick (hull released, task retried next tick; the coordinator keeps ticking, never hangs): %v", task.Good(), ship.ShipSymbol(), timeout, taskCtx.Err()), map[string]interface{}{
			"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(), "timeout": timeout.String(),
		})
		return false
	}
}

// completeSupply finishes a task that delivered a load this tick: complete + persist it, enqueue the
// next single-load replenishment when the site's bill is not yet met (PHASE-5 continuous refill,
// sp-utjr), and log the supply. Returns true (the task drained). Shared by the deliver-on-hand path
// and the source-then-deliver path (sp-9ptm) so the completion/refill logic cannot drift.
func (h *RunConstructionCoordinatorHandler) completeSupply(ctx context.Context, task *manufacturing.ManufacturingTask, pipeline *manufacturing.ManufacturingPipeline, ship *navigation.Ship, delivered int, ephemeral bool) bool {
	logger := common.LoggerFromContext(ctx)
	// A fan-out CLONE (sp-ubwi) has ALREADY done the real work — sourced, delivered, and recorded the
	// pipeline progress (recordDelivery, above) — so it must NOT persist a task status or enqueue a
	// replenishment: the material's ORIGINAL ready task (dispatched alongside every clone) owns the
	// task lifecycle and the single-per-material re-staging. Skipping both keeps the ready queue at the
	// planner's one-task-per-material and lets the fan-out re-derive parallelism from live hulls each
	// tick, with no clone-created task rows to orphan.
	if !ephemeral {
		if err := task.Complete(); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not complete construction task %s: %v", task.ID(), err), nil)
		}
		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not persist completed construction task %s: %v", task.ID(), err), nil)
		}
		// One supplyTask delivers a single hauler load. If the site's bill for this material is not yet
		// met, enqueue the next delivery so the gate keeps filling without a manual re-plan — the drain
		// self-re-stages until each material's full bill is met, instead of stalling on the planner's
		// single per-material task.
		h.enqueueReplenishmentIfNeeded(ctx, task, pipeline)
	}
	logger.Log("INFO", fmt.Sprintf("Supplied %d %s to construction site %s", delivered, task.Good(), task.ConstructionSite()), map[string]interface{}{
		"good": task.Good(), "units": delivered, "construction_site": task.ConstructionSite(), "ship": ship.ShipSymbol(), "ephemeral": ephemeral,
	})
	return true
}

// onHandUnits reports how many units of good the ship currently holds (0 if the hold is empty or
// unset) — the nil-safe read the deliver-on-hand path uses to decide whether the claimed hull is
// already laden with the construction material (sp-9ptm).
func onHandUnits(ship *navigation.Ship, good string) int {
	cargo := ship.Cargo()
	if cargo == nil {
		return 0
	}
	return cargo.GetItemUnits(good)
}

// constructionSourcingNode builds the SupplyChainNode the drain hands to ProduceGood for one
// construction material, honoring the buy-vs-produce decision the planner recorded on the task:
//
//   - FactorySymbol == "": the planner found a market selling the final good, so BUY it directly
//     (the non-regression path — one hop, no chain). UNCHANGED, byte-identical: the resolver is
//     never consulted here.
//   - FactorySymbol != "": the planner chose FABRICATION. sp-yfzi builds the FULL scarcity-gated
//     dependency tree via the shared SupplyChainResolver, so the drain PRODUCES a scarce
//     intermediate that has a factory (recursing its sub-chain to relieve the scarcity) and BUYS an
//     abundant one — instead of the old flat one-level "fabricate root, buy every immediate input"
//     node. The tree resolves under the run strategy (smart by default) and is bounded by the
//     pipeline's SupplyChainDepth (the depth backstop) + the resolver's cycle guard. When the
//     resolver is unwired (existing coordinator tests) OR cannot resolve the good (stale/absent
//     market data), it FALLS BACK to the original one-level fabricate node — never dying (RULINGS
//     #1) and never worse than pre-sp-yfzi.
//
// A fabricate task whose good has no known recipe (should not happen — the planner never
// fabricates a raw good) falls back to a BUY so the engine attempts a market source rather than
// polling forever on a childless fabricate.
func (h *RunConstructionCoordinatorHandler) constructionSourcingNode(ctx context.Context, cmd *RunConstructionCoordinatorCommand, task *manufacturing.ManufacturingTask, systemSymbol string, playerID int) *goods.SupplyChainNode {
	// sp-vh1s — unified gate-fill short-circuits the bespoke buy-vs-fabricate planner path. Feeding is
	// INHERENT in the tree, so ALWAYS resolve the full scarcity-gated tree, ignoring the planner's
	// frozen decision (the pure-BUY it froze at plan time fed NOTHING — the bug: FAB/ADV bought the
	// output cold, depleted the source, tripped the ceiling). A gate material whose good HAS a source
	// factory now fabricates+feeds instead of buying it cold; the resolver itself decides buy-vs-fabricate
	// per node by live supply. Falls through to the frozen decision below if the resolver cannot build
	// (unwired/stale market data) — never dies, never worse than today (RULING #1).
	if cmd.UnifiedGateFill {
		if tree := h.resolveFabricationTree(ctx, cmd, task, systemSymbol, playerID); tree != nil {
			return tree
		}
	}
	if task.FactorySymbol() == "" {
		return &goods.SupplyChainNode{Good: task.Good(), AcquisitionMethod: goods.AcquisitionBuy}
	}
	if tree := h.resolveFabricationTree(ctx, cmd, task, systemSymbol, playerID); tree != nil {
		return tree
	}
	// Fallback: the original one-level fabricate node (byte-identical to pre-sp-yfzi construction).
	node := goods.NewSupplyChainNode(task.Good(), goods.AcquisitionFabricate)
	for _, input := range goods.GetRequiredInputs(task.Good()) {
		node.AddChild(goods.NewSupplyChainNode(input, goods.AcquisitionBuy))
	}
	if node.IsLeaf() {
		return &goods.SupplyChainNode{Good: task.Good(), AcquisitionMethod: goods.AcquisitionBuy}
	}
	return node
}

// resolveFabricationTree builds the scarcity-gated dependency tree for a FABRICATE material via the
// shared resolver (sp-yfzi). It stamps, on the tree-build ctx only (the buy-vs-fabricate decision is
// baked into the tree at build time — ProduceGood just walks the shape, so produceCtx is untouched):
//   - the per-run PRODUCTION strategy (WithProductionStrategy — smart by default), so a scarce
//     intermediate with a factory fabricates while an abundant one is bought;
//   - the pipeline's SupplyChainDepth as the fabricate depth cap (WithFabricateDepthCap — the safety
//     backstop; a 0/unset pipeline resolves to the depth-3 default);
//   - the pipeline's per-good overrides (WithGoodGatingOverrides), so a single bottleneck material
//     can be pinned buy/fabricate without disturbing the rest (sp-sdyo).
//
// Returns nil — so constructionSourcingNode falls back to the one-level node — when the resolver is
// unwired or errors (stale/absent market data). The pipeline is read the way remainingBill does
// (FindByID); config fields (depth, overrides) are immutable post-creation, so this read needs no
// lock (matching pipelineExecuting's unlocked read).
func (h *RunConstructionCoordinatorHandler) resolveFabricationTree(ctx context.Context, cmd *RunConstructionCoordinatorCommand, task *manufacturing.ManufacturingTask, systemSymbol string, playerID int) *goods.SupplyChainNode {
	if h.resolver == nil {
		return nil
	}
	depth := 0
	var overrides manufacturing.GoodGatingOverrides
	if task.PipelineID() != "" {
		if pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID()); err == nil && pipeline != nil {
			depth = pipeline.SupplyChainDepth()
			overrides = pipeline.GoodOverrides()
		}
	}
	buildCtx := mfgServices.WithProductionStrategy(ctx, cmd.ProductionStrategy)
	buildCtx = mfgServices.WithFabricateDepthCap(buildCtx, depth, false)
	buildCtx = mfgServices.WithGoodGatingOverrides(buildCtx, overrides)
	tree, err := h.resolver.BuildDependencyTree(buildCtx, task.Good(), systemSymbol, playerID)
	if err != nil || tree == nil {
		return nil
	}
	return tree
}

// readyConstructionTasks returns the READY DELIVER_TO_CONSTRUCTION tasks whose pipeline is
// EXECUTING — the drain's queue. Non-construction READY tasks and tasks from non-EXECUTING
// (PLANNING/terminal) pipelines are filtered out.
func (h *RunConstructionCoordinatorHandler) readyConstructionTasks(ctx context.Context, playerID int) ([]*manufacturing.ManufacturingTask, error) {
	ready, err := h.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusReady)
	if err != nil {
		return nil, fmt.Errorf("failed to find ready construction tasks: %w", err)
	}
	executingCache := make(map[string]bool)
	var out []*manufacturing.ManufacturingTask
	for _, task := range ready {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
			continue
		}
		if !h.pipelineExecuting(ctx, executingCache, task.PipelineID()) {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func (h *RunConstructionCoordinatorHandler) pipelineExecuting(ctx context.Context, cache map[string]bool, pipelineID string) bool {
	if v, ok := cache[pipelineID]; ok {
		return v
	}
	pipeline, err := h.pipelineRepo.FindByID(ctx, pipelineID)
	executing := err == nil && pipeline != nil && pipeline.Status() == manufacturing.PipelineStatusExecuting
	cache[pipelineID] = executing
	return executing
}

// remainingBill returns how many more units of the task's material the construction site still
// needs — the pipeline material target minus what has been delivered (sp-2me2). It bounds the
// executor's hull-fill so a trip never over-buys past demand. Returns 0 (no bill cap → the
// executor fills to full hull capacity) whenever the pipeline or material is unavailable: a
// supply is never harmful (the site accepts only what it needs and the next tick re-polls), so an
// unreadable bill safely falls back to a full-hull fill rather than blocking the trip.
func (h *RunConstructionCoordinatorHandler) remainingBill(ctx context.Context, task *manufacturing.ManufacturingTask) int {
	if task.PipelineID() == "" {
		return 0
	}
	// Read the shared material counter under recordMu (the sp-01eh serializer): a concurrent worker's
	// recordDelivery mutates deliveredQuantity, so this read must be serialized with that write to
	// stay race-free when several workers drain the SAME pipeline object. Value-identical to an
	// unlocked read — the lock only removes the data race, not any behavior.
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		return 0
	}
	material := pipeline.GetMaterial(task.Good())
	if material == nil {
		return 0
	}
	if remaining := material.RemainingQuantity(); remaining > 0 {
		return remaining
	}
	return 0
}

// recordDelivery advances the pipeline's construction progress by the delivered units and
// persists it, so a supply moves the pipeline past 0%. A missing pipeline/material is a
// warning, never a task failure — the supply already succeeded. Returns the updated pipeline
// (with the just-recorded delivery applied to its persisted bill) so the caller can decide
// whether the material still needs refilling; nil on any path where progress was not recorded.
func (h *RunConstructionCoordinatorHandler) recordDelivery(ctx context.Context, task *manufacturing.ManufacturingTask, delivered int) *manufacturing.ManufacturingPipeline {
	logger := common.LoggerFromContext(ctx)
	if task.PipelineID() == "" || delivered <= 0 {
		return nil
	}
	// Serialize the load-add-store of pipeline progress across the concurrent workers (sp-01eh):
	// two workers delivering to the SAME pipeline must not both read the old material total and
	// store a sum that drops the other's units. Cheap relative to the parallel hauling it guards.
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Could not load pipeline %s to record construction delivery", task.PipelineID()), nil)
		return nil
	}
	if err := pipeline.RecordMaterialDelivery(task.Good(), delivered); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not record construction delivery of %s: %v", task.Good(), err), nil)
		return nil
	}
	if err := h.pipelineRepo.Update(ctx, pipeline); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist construction pipeline progress %s: %v", task.PipelineID(), err), nil)
	}
	return pipeline
}

// enqueueReplenishmentIfNeeded restores PHASE-5 continuous refill (sp-utjr; regression from
// sp-jav2 ef2281b8). One supplyTask delivers a single hauler cargo-load; the planner stages only
// one DELIVER_TO_CONSTRUCTION task per material, so without this the pipeline stalls EXECUTING
// below 100% after that first load. When the delivered material's bill is not yet met, it enqueues
// the next single-load delivery task — left READY for the drain to pick up next tick — so the
// pipeline self-re-stages one load at a time until every material's full bill is met. The remaining
// is read from the pipeline's persisted material bill (RULINGS #2: no new cross-restart state — the
// pipeline is already persisted and reloaded on boot), and the follow-on reuses this task's resolved
// delivery spec via the same domain factory the planner uses, so the two paths cannot drift. When
// remaining <= 0 the material is complete and nothing is queued, so the chain settles cleanly.
func (h *RunConstructionCoordinatorHandler) enqueueReplenishmentIfNeeded(ctx context.Context, task *manufacturing.ManufacturingTask, pipeline *manufacturing.ManufacturingPipeline) {
	logger := common.LoggerFromContext(ctx)
	if pipeline == nil {
		return
	}
	remaining := h.remainingForGoodLocked(pipeline, task.Good())
	if remaining <= 0 {
		return // material bill met — stop cleanly, no further task
	}

	next := nextConstructionDeliveryTask(task)
	if err := next.MarkReady(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction refill: could not ready replenishment task for %s: %v", task.Good(), err), nil)
		return
	}
	// Fresh context, not the passed ctx: ctx is the supply task's (cancelled when the delivery ends);
	// the replenishment must outlive it or a timed-out create silently kills the material's task chain.
	var createErr error
	for attempt := 1; attempt <= 3; attempt++ {
		createCtx, createCancel := context.WithTimeout(context.Background(), 15*time.Second)
		createErr = h.taskRepo.Create(createCtx, next)
		createCancel()
		if createErr == nil {
			break
		}
		logger.Log("WARNING", fmt.Sprintf("Construction refill: enqueue replenishment for %s attempt %d/3 failed: %v", task.Good(), attempt, createErr), nil)
	}
	if createErr != nil {
		logger.Log("ERROR", fmt.Sprintf("Construction refill: could not enqueue replenishment task for %s after 3 attempts — chain stalled until re-plan: %v", task.Good(), createErr), nil)
		return
	}
	logger.Log("INFO", fmt.Sprintf("Construction refill: queued next %s delivery (%d remaining)", task.Good(), remaining), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(), "remaining": remaining, "next_task": next.ID(), "pipeline_id": task.PipelineID(),
	})
}

// remainingForGood returns how many units of good the pipeline's construction bill still needs,
// from the just-updated persisted material target (RULINGS #2). A nil pipeline (recordDelivery could
// not load/persist it) or a material absent from the pipeline reports 0 — nothing to refill. The
// nil guard lets the deliver-on-hand path (sp-9ptm) treat an unrecordable delivery as "bill met"
// exactly as the pre-existing success tail does (complete, no replenishment).
func remainingForGood(pipeline *manufacturing.ManufacturingPipeline, good string) int {
	if pipeline == nil {
		return 0
	}
	material := pipeline.GetMaterial(good)
	if material == nil {
		return 0
	}
	return material.RemainingQuantity()
}

// remainingForGoodLocked reads pipeline's remaining bill for good under recordMu (the sp-01eh
// serializer), so the read is race-free against a concurrent worker's recordDelivery write when
// several workers drain the SAME pipeline object. Value-identical to remainingForGood — the lock
// only removes the data race. Callers must NOT already hold recordMu (recordDelivery releases it
// before its result is read here), so there is no reentrancy.
func (h *RunConstructionCoordinatorHandler) remainingForGoodLocked(pipeline *manufacturing.ManufacturingPipeline, good string) int {
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	return remainingForGood(pipeline, good)
}

// nextConstructionDeliveryTask builds the follow-on single-load delivery task for a just-completed
// DELIVER_TO_CONSTRUCTION task, reusing its resolved delivery spec (pipeline, player, good, source
// market or factory, construction site) with no dependencies. It funnels through the same domain
// factory the planner uses, so planner and refill paths cannot drift.
func nextConstructionDeliveryTask(completed *manufacturing.ManufacturingTask) *manufacturing.ManufacturingTask {
	return manufacturing.NewDeliverToConstructionTask(
		completed.PipelineID(),
		completed.PlayerID(),
		completed.Good(),
		completed.SourceMarket(),
		completed.FactorySymbol(),
		completed.ConstructionSite(),
		nil,
	)
}

// deferTask parks an unsourceable material's task back to a deferred PENDING for resupply
// (RULINGS #1): the dry source is cleared so it reads as IsDeferredConstruction and the
// SupplyMonitor re-sources it when the market refills, instead of failing it toward death.
func (h *RunConstructionCoordinatorHandler) deferTask(ctx context.Context, task *manufacturing.ManufacturingTask) {
	logger := common.LoggerFromContext(ctx)
	// Clear the dry source so the task reverts to the deferred signature (construction-only;
	// harmless if it was already sourceless).
	_ = task.ClearSourceForResupply()
	if err := task.ParkForResupply(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not park construction task %s for resupply: %v", task.ID(), err), nil)
		return
	}
	if err := h.taskRepo.Update(ctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist deferred construction task %s: %v", task.ID(), err), nil)
	}
	logger.Log("INFO", fmt.Sprintf("Deferred unsourceable construction material %s for resupply", task.Good()), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(),
	})
}

// isPhantomCargoSupplyError reports whether err is the API's 4219 'ship cargo does not contain N
// units / ship has 0 units' — the signal that the hull does NOT actually hold the cargo the drain
// routed it to deliver (a PHANTOM left by an un-written-back cache after an earlier supply, sp-v5d1).
// It is NOT a site/bill failure and must NOT be treated as a generic delivery error: retrying re-routes
// the empty hull to re-deliver forever (sp-j09q) and, when the hull is already at the gate, wedges the
// drain (sp-6zkg). Matched on the API error-code substring, consistent with isTransientDockStateError's
// 4214/4244 matching — the raw response body ({"error":{"code":4219,...}}) is wrapped through verbatim.
func isPhantomCargoSupplyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "4219")
}

// handlePhantomCargo recovers from a 4219 phantom-cargo supply rejection (sp-6zkg/sp-j09q) instead of
// failing or re-looping. The hull did not actually hold the cargo the drain routed it to deliver, so:
// (1) RESYNC the hull from the server, clearing the desynced cache (belt-and-suspenders with the
// sp-v5d1 write-back — so even a phantom that arose some other way is reconciled); then (2) advance
// WITHOUT failing (RULINGS #1): if on-hand progress was already recorded this tick, complete it (never
// strand delivered units); otherwise DEFER the task for re-sourcing, so the NEXT tick sources into a
// hull with REAL cargo rather than re-routing this empty one to re-deliver. Returns whether the task
// counts as drained this tick.
func (h *RunConstructionCoordinatorHandler) handlePhantomCargo(ctx context.Context, task *manufacturing.ManufacturingTask, pipeline *manufacturing.ManufacturingPipeline, ship *navigation.Ship, playerID shared.PlayerID, deliveredOnHand int, ephemeral bool) bool {
	logger := common.LoggerFromContext(ctx)
	logger.Log("WARNING", fmt.Sprintf("Construction drain: hauler %s supply of %s rejected as PHANTOM cargo (API 4219, ship has 0 units) — resyncing hull and recovering the task (never re-routing/hanging) [sp-6zkg/sp-j09q]", ship.ShipSymbol(), task.Good()), map[string]interface{}{
		"ship": ship.ShipSymbol(), "good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
	})
	h.resyncShipCargo(ctx, ship.ShipSymbol(), playerID)
	if deliveredOnHand > 0 {
		// On-hand units already delivered + recorded this tick — complete (never strand them).
		return h.completeSupply(ctx, task, pipeline, ship, deliveredOnHand, ephemeral)
	}
	// A fan-out CLONE never defers (its material's original ready task owns the defer); only a real task
	// parks for re-sourcing.
	if !ephemeral {
		h.deferTask(ctx, task)
	}
	return false
}

// resyncShipCargo forces the hull's cached state back to server truth after a phantom-cargo 4219
// (sp-6zkg). SyncShipFromAPI is the daemon's own GET-and-write-through reconcile — the same path a
// boot sync and `ship refresh` run — so this stays within the single-writer model (RULINGS #3; the
// daemon is reconciling its OWN cache, not a CLI writer). Best-effort: a resync failure is logged, not
// fatal — the next tick re-polls and the sp-v5d1 write-back keeps the cache coherent on the happy path.
func (h *RunConstructionCoordinatorHandler) resyncShipCargo(ctx context.Context, shipSymbol string, playerID shared.PlayerID) {
	logger := common.LoggerFromContext(ctx)
	if _, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction drain: could not resync hull %s after phantom-cargo 4219 (cache reconciles on next sync): %v", shipSymbol, err), nil)
	}
}

func (h *RunConstructionCoordinatorHandler) failTask(ctx context.Context, task *manufacturing.ManufacturingTask, reason string) {
	logger := common.LoggerFromContext(ctx)
	if err := task.Fail(reason); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not fail construction task %s: %v", task.ID(), err), nil)
	}
	if err := h.taskRepo.Update(ctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist failed construction task %s: %v", task.ID(), err), nil)
	}
}

// releaseClaims returns every hull this container claimed this tick to the idle pool.
func (h *RunConstructionCoordinatorHandler) releaseClaims(ctx context.Context, containerID string, playerID shared.PlayerID) {
	logger := common.LoggerFromContext(ctx)
	ships, err := h.shipRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not list claimed haulers for release: %v", err), nil)
		return
	}
	for _, ship := range ships {
		symbol := ship.ShipSymbol()
		// Release under CAS-retry (sp-wa7c): re-apply ForceRelease on the FRESH row
		// so a concurrent lot-task's cargo/nav update on the same hull survives
		// instead of being last-write-wins clobbered by this tick's cached
		// FindByContainer snapshot — the gate-FAB stall under sp-ubwi fan-out. The
		// guard lives INSIDE the mutation so it is re-checked on every re-find: a
		// hull already released, or freshly re-claimed by another container, yields
		// changed=false (no write, no spurious version bump), so a live claim is
		// never ripped out from under its new owner by a raced retry (RULING #7).
		if _, _, err := h.shipRepo.SaveWithRetry(ctx, symbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease("construction_tick_complete", h.clock)
				return true, nil
			}); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not release hauler %s after construction tick: %v", symbol, err), nil)
		}
	}
}

func (h *RunConstructionCoordinatorHandler) operationContext(cmd *RunConstructionCoordinatorCommand) *shared.OperationContext {
	if cmd.ContainerID == "" {
		return nil
	}
	return shared.NewOperationContext(cmd.ContainerID, constructionOperationContext)
}
