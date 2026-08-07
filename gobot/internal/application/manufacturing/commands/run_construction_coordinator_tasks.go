package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// persistCleanupCtx returns ctx unchanged while it is still live, or a short DETACHED context once ctx
// is already cancelled — so a cleanup WRITE triggered by an abandon survives instead of failing
// 'context canceled'. A timed-out supplyTask runs under a cancelled taskCtx; persisting a Fail/Defer
// or a hull release through that ctx would fail and strand the task in a limbo that forces a blind
// restart. On the happy path (live ctx) this is byte-identical — it returns ctx and a no-op cancel.
// Single-writer preserved (still the daemon writing); mirrors the detached-ctx write
// enqueueReplenishmentIfNeeded already uses for the same reason.
func persistCleanupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), constructionDetachedPersistTimeout)
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

// deferTask parks an unsourceable material's task back to a deferred PENDING for resupply: the dry
// source is cleared so it reads as IsDeferredConstruction and the SupplyMonitor re-sources it when
// the market refills, instead of failing it toward death.
func (h *RunConstructionCoordinatorHandler) deferTask(ctx context.Context, task *manufacturing.ManufacturingTask) {
	// A stop is not a dry source: re-queue with the resolved source intact rather than clearing it
	// and waiting on the SupplyMonitor to re-source a market that was never the problem.
	if h.requeueInterrupted(ctx, task) {
		return
	}
	logger := common.LoggerFromContext(ctx)
	// Clear the dry source so the task reverts to the deferred signature (construction-only;
	// harmless if it was already sourceless).
	_ = task.ClearSourceForResupply()
	if err := task.ParkForResupply(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not park construction task %s for resupply: %v", task.ID(), err), nil)
		return
	}
	// Persist through a detached ctx when the caller's ctx is already cancelled (the timeout-abandon
	// path), so the parked PENDING survives instead of failing 'context canceled' — the SupplyMonitor
	// then re-activates it rather than the drain blindly restarting a still-READY task.
	wctx, cancel := persistCleanupCtx(ctx)
	defer cancel()
	if err := h.taskRepo.Update(wctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist deferred construction task %s: %v", task.ID(), err), nil)
	}
	logger.Log("INFO", fmt.Sprintf("Deferred unsourceable construction material %s for resupply", task.Good()), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(),
	})
}

// requeueInterrupted returns a task whose run was CANCELLED — a daemon stop or a coordinator
// restart, never a fault of the haul — to PENDING, so the next tick's activation promotes it back
// to READY. It reports whether it took ownership of the task, so the fail/defer paths it fronts
// stand down.
//
// A cancellation must not be charged to the retry budget, or a handful of deploys spends a leg's
// whole retry allowance and strands it terminal FAILED, holding material the interrupted hull
// already paid for. ParkForResupply leaves the retry count untouched and releases the hull, and the
// resolved source is deliberately NOT cleared — unlike a dry-source defer — because nothing about
// the source failed. The write rides the detached cleanup ctx so it survives the cancellation that
// triggered it; if the process dies first the row is still the READY it was polled as, status never
// being persisted mid-flight, so the next tick picks it up either way.
func (h *RunConstructionCoordinatorHandler) requeueInterrupted(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	logger := common.LoggerFromContext(ctx)
	if err := task.ParkForResupply(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not re-queue interrupted construction task %s: %v", task.ID(), err), nil)
		return false
	}
	wctx, cancel := persistCleanupCtx(ctx)
	defer cancel()
	if err := h.taskRepo.Update(wctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist re-queued construction task %s: %v", task.ID(), err), nil)
	}
	logger.Log("INFO", fmt.Sprintf("Re-queued interrupted construction delivery of %s — the next activation promotes it back to READY, no retry spent", task.Good()), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(), "task": task.ID(),
	})
	return true
}

func (h *RunConstructionCoordinatorHandler) failTask(ctx context.Context, task *manufacturing.ManufacturingTask, reason string) {
	// A stop is not a task fault: re-queue rather than spend a retry on it.
	if h.requeueInterrupted(ctx, task) {
		return
	}
	logger := common.LoggerFromContext(ctx)
	if err := task.Fail(reason); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not fail construction task %s: %v", task.ID(), err), nil)
	}
	// Persist through a detached ctx when the caller's ctx is already cancelled (the timeout-abandon
	// path), so the FAIL survives instead of failing 'context canceled' and stranding the task READY.
	wctx, cancel := persistCleanupCtx(ctx)
	defer cancel()
	if err := h.taskRepo.Update(wctx, task); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist failed construction task %s: %v", task.ID(), err), nil)
	}
}

// releaseClaims returns every hull this container holds with NO live worker behind it to the idle
// pool — a claim orphaned by a release write that failed. A hull a worker is still supplying with is
// skipped: it is mid-haul, and its own worker releases it when it finishes.
func (h *RunConstructionCoordinatorHandler) releaseClaims(ctx context.Context, containerID string, playerID shared.PlayerID) {
	logger := common.LoggerFromContext(ctx)
	// Detach the release read+write from a cancelled ctx (coordinator stop) so a claimed hull is
	// returned to the pool instead of failing 'context canceled' and stranding out of the idle set.
	ctx, cancel := persistCleanupCtx(ctx)
	defer cancel()
	ships, err := h.shipRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not list claimed haulers for release: %v", err), nil)
		return
	}
	for _, ship := range ships {
		if h.supplies.holds(ship.ShipSymbol()) {
			continue
		}
		h.releaseClaim(ctx, containerID, ship.ShipSymbol(), playerID)
	}
}

// releaseClaim returns ONE hull this container claimed to the idle pool.
//
// Release under CAS-retry: re-apply ForceRelease on the FRESH row so a concurrent worker's
// cargo/nav update on the same hull survives instead of being last-write-wins clobbered by a cached
// snapshot. The guard lives INSIDE the mutation so it is re-checked on every re-find: a hull already
// released, or freshly re-claimed by another container, yields changed=false (no write, no spurious
// version bump), so a live claim is never ripped out from under its new owner by a raced retry.
func (h *RunConstructionCoordinatorHandler) releaseClaim(ctx context.Context, containerID, shipSymbol string, playerID shared.PlayerID) {
	logger := common.LoggerFromContext(ctx)
	// A worker outlives its tick, so its release can land after a stop: detach from a cancelled ctx
	// or the hull strands out of the idle set. Byte-identical on a live ctx.
	ctx, cancel := persistCleanupCtx(ctx)
	defer cancel()
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() || sh.ContainerID() != containerID {
				return false, nil
			}
			sh.ForceRelease("construction_supply_complete", h.clock)
			return true, nil
		}); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not release hauler %s after its construction supply: %v", shipSymbol, err), nil)
	}
}
