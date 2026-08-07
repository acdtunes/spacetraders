package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// workerWait parameterises one bounded park on worker completion.
type workerWait struct {
	ch           <-chan navigation.WorkerCompletedEvent
	recheck      time.Duration
	completedMsg string // format string taking the completed hull's symbol
	timeoutLevel string // empty leaves an elapsed recheck window silent
	timeoutMsg   string
}

// awaitWorkerSlot parks until a worker completes, the recheck window elapses or ctx
// is cancelled. A true return means the active worker was stopped and the loop must exit.
func (h *RunFleetCoordinatorHandler) awaitWorkerSlot(ctx context.Context, wait workerWait, active *string) bool {
	logger := common.LoggerFromContext(ctx)
	select {
	case event := <-wait.ch:
		recordWorkerCompletion(logger, event, fmt.Sprintf(wait.completedMsg, event.ShipSymbol))
		*active = ""
	case <-time.After(wait.recheck):
		if wait.timeoutLevel != "" {
			logger.Log(wait.timeoutLevel, wait.timeoutMsg, nil)
		}
	case <-ctx.Done():
		h.stopActiveWorker(ctx, *active)
		return true
	}
	return false
}

// recordWorkerCompletion logs the outcome of a worker-completion event honestly
// and reports whether it should count toward the completed-contracts metric.
// A successful worker is logged at INFO with successMsg and counts; a crashed
// worker is logged at ERROR carrying event.Error and does NOT count — so the
// logs and the ContractsCompleted metric never treat a failure as a completion.
// Every worker-completion receive site funnels through here.
func recordWorkerCompletion(logger common.ContainerLogger, event navigation.WorkerCompletedEvent, successMsg string) (succeeded bool) {
	if event.Success {
		logger.Log("INFO", successMsg, nil)
		return true
	}
	logger.Log("ERROR", fmt.Sprintf("Worker for ship %s failed: %s", event.ShipSymbol, event.Error), nil)
	return false
}

// readoptInterruptedDeliveries resumes a contract delivery that a daemon restart orphaned
// mid-flight. A restart marks the in-flight worker container FAILED (markWorkerInterrupted) but
// deliberately leaves its ship holding the contract cargo, and the "ship has cargo -> resume
// delivery" path in StopExistingWorkers only inspects RUNNING workers, so it never fires for a
// FAILED one. Without this pass the coordinator ForceReleases that ship and restarts the workflow
// from negotiate, stalling a fully-loaded hull behind a purchase-market gate it does not need while
// scouts repopulate market data.
//
// Re-adopting spawns a fresh worker directly for the cargo-laden ship, and the worker's idempotent
// workflow resumes at the delivery leg with no re-negotiation and no re-purchase. At most one ship
// is re-adopted per startup: one active contract per player means only one ship is ever
// mid-delivery. Empty interrupted ships are left for ReclaimShipsFromInterruptedWorkers to free
// into normal discovery, and any failure here falls back to that reclaim path, so a transient error
// forgoes the fast resume but can never strand the ship. Returns the re-adopted worker's container
// ID, or "" if nothing was re-adopted.
func (h *RunFleetCoordinatorHandler) readoptInterruptedDeliveries(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
) string {
	logger := common.LoggerFromContext(ctx)

	ships, err := h.workerLifecycleManager.FindInterruptedWorkerShipsWithCargo(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to find interrupted deliveries to re-adopt: %v", err), nil)
		return ""
	}
	if len(ships) == 0 {
		return ""
	}

	ship := ships[0]
	shipSymbol := ship.ShipSymbol()

	// Detach from the dead worker container so spawnContractWorker can re-assign
	// the ship to the fresh one. Mirrors ReclaimShipsFromInterruptedWorkers' own
	// detach, but here we immediately re-adopt instead of returning to discovery.
	// Under CAS-retry: re-apply ForceRelease on the FRESH row so a concurrent
	// writer's cargo/nav update on the same hull survives instead of being
	// last-write-wins clobbered, and skip unless the hull is still on its dead
	// worker (already released / re-claimed elsewhere -> changed=false).
	deadWorkerContainer := ship.ContainerID()
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() || sh.ContainerID() != deadWorkerContainer {
				return false, nil
			}
			sh.ForceRelease("worker_readopt", h.clock)
			return true, nil
		}); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release ship %s for re-adoption (falling back to reclaim/discovery): %v", shipSymbol, err), nil)
		return ""
	}

	// A resume is never a fresh last-resort decision: the frigate (if this hull
	// is the command frigate) was already mid-contract, so re-orphaning it here
	// would be wrong. Always authorize the command draft here.
	// A resume runs the FULL leg (source the remainder, then deliver), never
	// deliver-held: re-adoption exists to finish an interrupted delivery, and the
	// coordinator has not weighed this hull's placement here.
	workerContainerID, err := h.spawnContractWorker(ctx, cmd, shipSymbol, true, false)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to re-adopt in-flight delivery for ship %s (falling back to discovery): %v", shipSymbol, err), nil)
		return ""
	}

	logger.Log("INFO", fmt.Sprintf("Re-adopted in-flight contract delivery: ship %s resuming in worker %s (cargo aboard, no re-negotiation)", shipSymbol, workerContainerID), map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"container_id": workerContainerID,
		"action":       "readopt_delivery",
	})
	return workerContainerID
}

// spawnContractWorker persists, claims, and starts a contract-workflow worker on
// selectedShip. commandDraftAllowed authorizes drafting the command frigate for
// this leg: a FRESH draft from the main loop passes the last-resort verdict
// (true only when no regular hauler is an idle candidate), while a RESUME of an
// interrupted delivery (readoptInterruptedDeliveries) always passes true — a
// mid-delivery frigate must never be re-orphaned. The value governs only an
// UNDEDICATED command frigate; every regular hull and every contract-dedicated
// command hull is unaffected.
func (h *RunFleetCoordinatorHandler) spawnContractWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	selectedShip string,
	commandDraftAllowed bool,
	deliverHeldOnly bool,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("contract-work", selectedShip)

	workerCmd := &RunWorkflowCommand{
		ShipSymbol:    selectedShip,
		PlayerID:      cmd.PlayerID,
		ContainerID:   workerContainerID,
		CoordinatorID: cmd.ContainerID,
		// DELIVER-HELD mode (sp-5jce2) — set only when this hull is a badly-placed
		// partial holder standing on the delivery; see weighHolderPlacement.
		DeliverHeldOnly: deliverHeldOnly,
	}

	logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", workerContainerID, selectedShip), nil)
	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindContractWorkflow, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("Failed to persist worker container: %v", err)
	}

	logger.Log("INFO", fmt.Sprintf("Assigning %s to worker container", selectedShip), nil)
	ship, err := h.shipRepo.FindBySymbol(ctx, selectedShip, cmd.PlayerID)
	if err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("Failed to load ship %s: %v", selectedShip, err)
	}

	// Claim-side last-resort backstop (RULINGS #7): refuse to draft an
	// UNDEDICATED command frigate for a contract haul unless it is a genuine
	// last resort. This is the single-writer backstop at the claim itself, so
	// even a discovery regression cannot silently re-sweep the frigate onto
	// contracts. A contract-DEDICATED command hull (tag "contract") is a
	// legitimate fleet member and passes untouched; a resume (readopt) passes
	// commandDraftAllowed=true so a mid-delivery frigate is never re-orphaned.
	// Rolled back exactly like a rejected claim — no ship write, no worker
	// started.
	if !commandDraftAllowed && ship.DedicatedFleet() == "" && domainContract.IsCommandHull(ship) {
		logger.Log("INFO", fmt.Sprintf(
			"Refusing to draft undedicated command frigate %s for a contract haul while a regular hauler is available — command frigate hauls only as last resort (RULINGS #7)", selectedShip),
			map[string]interface{}{
				"action":      "skipped:command_frigate_not_last_resort",
				"ship_symbol": selectedShip,
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("refusing to draft undedicated command frigate %s for a contract haul: %w", selectedShip, ErrCommandFrigateNotLastResort)
	}

	// Atomic claim: assignment AND fleet dedication are re-checked inside
	// ClaimShip's row-locked transaction, so a `fleet assign` racing discovery
	// can never slip a foreign-pinned hull — including the command frigate under
	// its "command" pin — into a contract worker. A hull pinned to a fleet other
	// than dedicatedFleetContract ("contract") is rejected at the DB, not
	// clobbered; a contract-pinned or unpinned hull claims normally. Both
	// callers hand this an idle ship: the main loop selects from idle
	// candidates, and readoptInterruptedDeliveries force-releases the dead
	// worker's hull to idle before re-adopting it.
	//
	// A DEPOT DELIVERY hull carries the distinct depot.DeliveryHullFleet
	// dedication (so discovery can never re-grab it) and reaches this claim ONLY
	// via routeContractViaDepot or a mid-delivery readopt. contractClaimFleet
	// keys the claim on the hull's own dedication so that depot-routed dispatch
	// passes the dedication guard, while every other hull still claims under
	// "contract".
	if err := h.shipRepo.ClaimShip(ctx, selectedShip, workerContainerID, cmd.PlayerID, contractClaimFleet(ship.DedicatedFleet())); err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		// %w so callers (and the poach-vector test) can distinguish a fleet-
		// dedication rejection from a transient failure; the string is identical.
		return "", fmt.Errorf("Failed to claim ship %s: %w", selectedShip, err)
	}

	// Mirror the committed claim into the in-memory entity so the start-failure
	// rollback below (and any later read of `ship`) sees the assignment. A sync
	// failure here is a WARN, not an unclaim: the DB claim already holds the
	// ship, so returning an error would orphan a committed claim with no holder
	// to release it (matches the factory/gas Phase 2 migration).
	if err := ship.AssignToContainer(workerContainerID, h.clock); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Ship %s claimed in DB but in-memory assign failed (claim stands): %v", selectedShip, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Starting worker container for %s", selectedShip), nil)
	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindContractWorkflow, workerContainerID); err != nil {
		// Release the just-claimed hull under CAS-retry: re-apply ForceRelease on
		// the FRESH row so a concurrent writer's cargo/nav update survives instead
		// of being last-write-wins clobbered, and skip unless the hull is still
		// this worker's claim (RULINGS #7 — never release out from under a new
		// owner).
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, selectedShip, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != workerContainerID {
					return false, nil
				}
				sh.ForceRelease("worker_start_failed", h.clock)
				return true, nil
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("Failed to start worker container: %v", err)
	}

	return workerContainerID, nil
}

func (h *RunFleetCoordinatorHandler) stopActiveWorker(ctx context.Context, activeWorkerContainerID string) {
	if activeWorkerContainerID == "" {
		return
	}
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
	_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
}
