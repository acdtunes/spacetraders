package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgTypes "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	}
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
	if h.newActivator != nil {
		if activator := h.newActivator(cmd.PlayerID); activator != nil {
			activator.ActivateConstructionTasks(ctx)
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
	idleShips, _, err := contract.FindIdleLightHaulers(ctx, playerID, h.shipRepo, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to discover idle haulers: %w", err)
	}
	if len(idleShips) == 0 {
		return &RunConstructionCoordinatorResponse{NoWorkReason: noWorkNoIdleHauler}, nil
	}

	// Return this container's claims to the idle pool at tick end so a drained hull is
	// reusable next tick (ship claims also auto-release on restart via ReleaseAllActive).
	defer h.releaseClaims(ctx, cmd.ContainerID, playerID)

	drained := 0
	for i, task := range tasks {
		if i >= len(idleShips) {
			break // out of in-system haulers this tick; remaining tasks are retried next tick
		}
		ship := idleShips[i]

		// Atomic claim under the shared "manufacturing" identity (RULINGS #7): a hull pinned
		// to another fleet, or grabbed since discovery, is rejected at the DB, not clobbered.
		if err := h.shipRepo.ClaimShip(ctx, ship.ShipSymbol(), cmd.ContainerID, playerID, operationManufacturing); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Skipping hauler %s for construction: claim rejected: %v", ship.ShipSymbol(), err), nil)
			continue // task stays READY; retried next tick
		}

		if h.supplyTask(ctx, cmd, systemSymbol, task, ship, playerID) {
			drained++
		}
	}

	return &RunConstructionCoordinatorResponse{TasksDrained: drained}, nil
}

// supplyTask sources the task's material into the claimed hauler via ProduceGood, delivers
// it to the construction site via DeliverToConstructionSite, records the delivery on the
// pipeline, and completes the task. Returns true only on a delivered supply. An unsourceable
// material is deferred (parked PENDING, source cleared) rather than failed (RULINGS #1).
func (h *RunConstructionCoordinatorHandler) supplyTask(ctx context.Context, cmd *RunConstructionCoordinatorCommand, systemSymbol string, task *manufacturing.ManufacturingTask, ship *navigation.Ship, playerID shared.PlayerID) bool {
	logger := common.LoggerFromContext(ctx)

	if err := task.AssignShip(ship.ShipSymbol()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not assign hauler to construction task %s: %v", task.ID(), err), nil)
		return false
	}
	if err := task.StartExecution(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not start construction task %s: %v", task.ID(), err), nil)
		return false
	}

	// Source the material INTO the hauler on the shared engine (a market BUY node — the
	// planner already resolved the source market on the task). No duplicate sourcing logic.
	node := &goods.SupplyChainNode{Good: task.Good(), AcquisitionMethod: goods.AcquisitionBuy}
	result, err := h.producer.ProduceGood(ctx, ship, node, systemSymbol, cmd.PlayerID, h.operationContext(cmd), false)
	if err != nil {
		h.failTask(ctx, task, fmt.Sprintf("sourcing %s failed: %v", task.Good(), err))
		return false
	}
	if result == nil || result.QuantityAcquired == 0 {
		// Dry / no eligible source: DEFER, do not fail (RULINGS #1 never-skip). Parking to a
		// deferred PENDING re-arms the SupplyMonitor re-sourcing path when the market refills.
		h.deferTask(ctx, task)
		return false
	}

	delivered, err := h.producer.DeliverToConstructionSite(ctx, ship.ShipSymbol(), task.Good(), task.ConstructionSite(), playerID)
	if err != nil {
		h.failTask(ctx, task, fmt.Sprintf("delivering %s to %s failed: %v", task.Good(), task.ConstructionSite(), err))
		return false
	}

	pipeline := h.recordDelivery(ctx, task, delivered)

	if err := task.Complete(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not complete construction task %s: %v", task.ID(), err), nil)
	}
	if err := h.taskRepo.Update(ctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist completed construction task %s: %v", task.ID(), err), nil)
	}

	// Continuous refill (PHASE-5, restored sp-utjr): one supplyTask delivers a single hauler
	// load. If the site's bill for this material is not yet met, enqueue the next delivery so
	// the gate keeps filling without a manual re-plan — the drain self-re-stages until each
	// material's full bill is met, instead of stalling on the planner's single per-material task.
	h.enqueueReplenishmentIfNeeded(ctx, task, pipeline)

	logger.Log("INFO", fmt.Sprintf("Supplied %d %s to construction site %s", delivered, task.Good(), task.ConstructionSite()), map[string]interface{}{
		"good": task.Good(), "units": delivered, "construction_site": task.ConstructionSite(), "ship": ship.ShipSymbol(),
	})
	return true
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
	remaining := remainingForGood(pipeline, task.Good())
	if remaining <= 0 {
		return // material bill met — stop cleanly, no further task
	}

	next := nextConstructionDeliveryTask(task)
	if err := next.MarkReady(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction refill: could not ready replenishment task for %s: %v", task.Good(), err), nil)
		return
	}
	if err := h.taskRepo.Create(ctx, next); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction refill: could not enqueue replenishment task for %s: %v", task.Good(), err), nil)
		return
	}
	logger.Log("INFO", fmt.Sprintf("Construction refill: queued next %s delivery (%d remaining)", task.Good(), remaining), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(), "remaining": remaining, "next_task": next.ID(), "pipeline_id": task.PipelineID(),
	})
}

// remainingForGood returns how many units of good the pipeline's construction bill still needs,
// from the just-updated persisted material target (RULINGS #2). A material absent from the pipeline
// reports 0 — nothing to refill.
func remainingForGood(pipeline *manufacturing.ManufacturingPipeline, good string) int {
	material := pipeline.GetMaterial(good)
	if material == nil {
		return 0
	}
	return material.RemainingQuantity()
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
		ship.ForceRelease("construction_tick_complete", h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not release hauler %s after construction tick: %v", ship.ShipSymbol(), err), nil)
		}
	}
}

func (h *RunConstructionCoordinatorHandler) operationContext(cmd *RunConstructionCoordinatorCommand) *shared.OperationContext {
	if cmd.ContainerID == "" {
		return nil
	}
	return shared.NewOperationContext(cmd.ContainerID, constructionOperationContext)
}
