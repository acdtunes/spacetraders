package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// liquidationDispatchCooldown is how long a hull stays off the auto-liquidation
// re-dispatch list after one attempt. It bounds a spawn-storm on a genuinely
// stuck hull — no in-system market bids its cargo and jettison is off, so each
// pass would otherwise re-park then re-dispatch it. A sellable hull clears on
// the first attempt and never comes back to the parked list, so the cooldown
// only governs the unsellable-hold tail; the hull is retried after it, since a
// market may appear (scouts scan) — a deferral, never a permanent skip
// (RULINGS #1).
const liquidationDispatchCooldown = 5 * time.Minute

// dispatchLiquidationForParked self-clears the hulls FilterUnrelatedCargo
// parked for holding cargo unrelated to the active contract: each gets a
// one-shot cargo_liquidation worker that sells the strand at the best
// in-system bid (jettison only as a last resort below the configured floor),
// so the hull re-enters candidacy on a later pass instead of sitting filtered
// out of the pool forever. It is a STANDING mechanism (runs every discovery
// pass), gated by the AutoLiquidationDisabled escape hatch and a per-hull
// cooldown so an unsellable hold never storms. Best-effort: a spawn failure is
// logged and the hull is put on cooldown so a persistent failure cannot spin;
// contract work on the spawnable hulls is never blocked by it.
func (h *RunFleetCoordinatorHandler) dispatchLiquidationForParked(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	parkedShips []string,
	requiredCargo string,
	cooldown map[string]time.Time,
) {
	if cmd.AutoLiquidationDisabled || len(parkedShips) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	now := h.clock.Now()
	for _, shipSymbol := range parkedShips {
		if until, ok := cooldown[shipSymbol]; ok && now.Before(until) {
			continue // recently dispatched — don't re-storm a stuck hull
		}
		cooldown[shipSymbol] = now.Add(liquidationDispatchCooldown)
		workerID, err := h.spawnLiquidationWorker(ctx, cmd, shipSymbol, requiredCargo)
		if errors.Is(err, errHoldAlreadyClear) {
			logger.Log("INFO", fmt.Sprintf("Auto-liquidation for parked hull %s skipped - its hold cleared between the parking decision and dispatch, so nothing was spawned or claimed", shipSymbol), map[string]interface{}{
				"action":      "liquidation_dispatch_skipped_clear",
				"ship_symbol": shipSymbol,
			})
			continue
		}
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Auto-liquidation dispatch for parked hull %s failed: %v - will retry after cooldown", shipSymbol, err), map[string]interface{}{
				"action":      "liquidation_dispatch_failed",
				"ship_symbol": shipSymbol,
			})
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Auto-liquidation dispatched for parked hull %s (worker %s) - self-clearing stranded cargo so it re-enters candidacy", shipSymbol, workerID), map[string]interface{}{
			"action":       "liquidation_dispatched",
			"ship_symbol":  shipSymbol,
			"worker_id":    workerID,
			"min_jettison": cmd.LiquidationMinJettisonValue,
		})
	}
}

// errHoldAlreadyClear reports that the hull stopped holding unrelated cargo between
// the parking decision and this dispatch, so there is nothing to liquidate.
var errHoldAlreadyClear = errors.New("hold no longer holds cargo unrelated to the contract")

// spawnLiquidationWorker persists, claims, and starts a one-shot
// cargo_liquidation worker on a parked hull, mirroring spawnContractWorker's
// atomic-claim + rollback lifecycle. The claim goes through ClaimShip under
// the contract fleet identity, so an unpinned or contract-pinned hull claims
// cleanly while a hull pinned to another fleet is rejected at the DB rather
// than poached. On a start failure the assignment is released so the hull
// returns to the pool.
//
// The hold is re-verified against SERVER TRUTH first, because the parking decision
// can be right when made and wrong when acted on: a contract turnover empties the
// hold and flips requiredCargo within about a second, so a hull parked for the
// OUTGOING good is routinely clear by the time the worker would run. Re-evaluating
// FilterUnrelatedCargo's own predicate against the API — the same read the worker
// itself opens with, so no extra call is spent — is what makes decision and action
// agree; the sync also persists the true hold, so the hull re-enters candidacy on
// the next pass. Fails CLOSED: an unverifiable hold is never claimed, and the
// caller's cooldown defers the retry rather than skipping it (RULINGS #1).
func (h *RunFleetCoordinatorHandler) spawnLiquidationWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	shipSymbol string,
	requiredCargo string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, cmd.PlayerID)
	if err != nil {
		return "", fmt.Errorf("failed to verify hold of %s: %w", shipSymbol, err)
	}
	if cargo := ship.Cargo(); cargo == nil || !cargo.HasItemsOtherThan(requiredCargo) {
		return "", errHoldAlreadyClear
	}

	workerContainerID := utils.GenerateContainerID("cargo-liquidation", shipSymbol)
	workerCmd := &liquidation.LiquidateCargoCommand{
		PlayerID:         cmd.PlayerID,
		ShipSymbol:       shipSymbol,
		MinJettisonValue: cmd.LiquidationMinJettisonValue,
		CoordinatorID:    cmd.ContainerID,
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindCargoLiquidation, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist liquidation worker: %w", err)
	}

	// Atomic operation-checked claim, same identity as spawnContractWorker: a
	// foreign-pinned hull is rejected at the DB, not clobbered.
	if err := h.shipRepo.ClaimShip(ctx, shipSymbol, workerContainerID, cmd.PlayerID, dedicatedFleetContract); err != nil {
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
	}

	// Mirror the committed claim into the in-memory entity so the start-failure rollback
	// below sees the assignment; a sync failure is a WARN, not an unclaim (the DB claim
	// stands).
	if err := ship.AssignToContainer(workerContainerID, h.clock); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Ship %s claimed in DB but in-memory assign failed (claim stands): %v", shipSymbol, err), nil)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindCargoLiquidation, workerContainerID); err != nil {
		// Release the just-claimed hull under CAS-retry: re-apply ForceRelease on
		// the FRESH row so a concurrent writer's cargo/nav update survives instead
		// of being last-write-wins clobbered, and skip unless the hull is still
		// this worker's claim (RULINGS #7).
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != workerContainerID {
					return false, nil
				}
				sh.ForceRelease("liquidation_start_failed", h.clock)
				return true, nil
			})
		_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to start liquidation worker: %w", err)
	}

	return workerContainerID, nil
}
