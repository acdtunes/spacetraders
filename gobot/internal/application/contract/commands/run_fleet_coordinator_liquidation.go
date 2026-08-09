package commands

import (
	"context"
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

// reconcileParkedHulls settles every hull FilterUnrelatedCargo parked for unrelated
// cargo against ONE hold read taken HERE, at dispatch, never the older fleet-snapshot
// read the parking decision was made on. A hull whose hold is CLEAR is READMITTED
// (first return) to the candidate pool for THIS pass — its exclusion premise is void,
// and deferring that correction to the next pass is what once flew a 766-unit ferry
// past a hull docked on the source market. A hull whose strand is confirmed stays
// parked (second return) behind the NO-CARGO-DUMP guard and gets a one-shot
// cargo_liquidation worker, so it re-enters candidacy later rather than never.
//
// The liquidation half is STANDING, gated by the AutoLiquidationDisabled escape hatch
// and a per-hull cooldown so an unsellable hold never storms. RE-ADMISSION IGNORES
// THAT ESCAPE HATCH: opting out of liquidation is a choice about spawning workers,
// never a licence to select over a pool already known to be wrong. It does honor the
// cooldown, which is what stops an unverifiable hull being re-read every pass — an
// unexamined hull keeps its parking (fail closed, RULINGS #1). A spawn failure is
// logged and left on cooldown so a persistent failure cannot spin.
func (h *RunFleetCoordinatorHandler) reconcileParkedHulls(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	parkedShips []string,
	requiredCargo string,
	cooldown map[string]time.Time,
) (readmitted, stillParked []string) {
	if len(parkedShips) == 0 {
		return nil, nil
	}
	logger := common.LoggerFromContext(ctx)
	now := h.clock.Now()
	for _, shipSymbol := range parkedShips {
		if until, ok := cooldown[shipSymbol]; ok && now.Before(until) {
			// Unexamined this pass, so the parking premise stands.
			stillParked = append(stillParked, shipSymbol)
			continue
		}
		cooldown[shipSymbol] = now.Add(liquidationDispatchCooldown)

		ship, err := h.verifyParkedHold(ctx, cmd, shipSymbol)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Hold of parked hull %s is unverifiable (%v) - it stays parked and is retried after cooldown", shipSymbol, err), map[string]interface{}{
				"action":      "parked_hold_unverifiable",
				"ship_symbol": shipSymbol,
			})
			stillParked = append(stillParked, shipSymbol)
			continue
		}

		if cargo := ship.Cargo(); cargo == nil || !cargo.HasItemsOtherThan(requiredCargo) {
			logger.Log("INFO", fmt.Sprintf("Parked hull %s cleared its hold between the parking decision and dispatch - returning it to the candidate pool for THIS pass; no liquidation was spawned or claimed", shipSymbol), map[string]interface{}{
				"action":      "parked_hull_readmitted",
				"ship_symbol": shipSymbol,
			})
			readmitted = append(readmitted, shipSymbol)
			continue
		}

		stillParked = append(stillParked, shipSymbol)
		if cmd.AutoLiquidationDisabled {
			continue
		}
		workerID, err := h.spawnLiquidationWorker(ctx, cmd, ship)
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
	return readmitted, stillParked
}

// verifyParkedHold resolves what a parked hull is ACTUALLY holding, because the
// parking decision can be right when made and wrong when acted on: a contract
// turnover empties the hold and flips requiredCargo within about a second, so a hull
// parked for the OUTGOING good is routinely clear by the time it would be acted on.
// This is the ONE cargo read of the dispatch and BOTH verdicts are taken from it, so
// the coordinator never decides on one read and acts on another. It is the read the
// liquidation worker itself opens with, so no extra call is spent on a hull that gets
// one, and the sync persists the true hold for the next pass's snapshot.
//
// A FAILED live read falls back to the PERSISTED hold: the call that strands a hull is
// the same one that would clear it, and an unreadable ship must not make its own remedy
// unreachable. No freshness bound — FilterUnrelatedCargo parks on that very read, and the
// whole pool is admitted on that trust already. Fails CLOSED with BOTH reads unavailable
// (RULINGS #1): the caller leaves the hull parked.
func (h *RunFleetCoordinatorHandler) verifyParkedHold(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	shipSymbol string,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, cmd.PlayerID)
	if err == nil {
		return ship, nil
	}
	cached, cachedErr := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
	if cachedErr != nil || cached == nil {
		return nil, fmt.Errorf("failed to verify hold of %s: %w", shipSymbol, err)
	}
	logger.Log("INFO", fmt.Sprintf("Hold check for parked hull %s fell back to the persisted hold - the live read failed (%v), and the unreadable ship is the one that needs clearing", shipSymbol, err), map[string]interface{}{
		"action":      "liquidation_hold_fallback",
		"ship_symbol": shipSymbol,
	})
	return cached, nil
}

// spawnLiquidationWorker persists, claims, and starts a one-shot cargo_liquidation worker on a
// parked hull whose strand verifyParkedHold has already confirmed, mirroring spawnContractWorker's
// atomic-claim + rollback lifecycle. The claim goes through ClaimShip under the contract fleet
// identity, so an unpinned or contract-pinned hull claims cleanly while a hull pinned to another
// fleet is rejected at the DB rather than poached. On a start failure the assignment is released so
// the hull returns to the pool.
func (h *RunFleetCoordinatorHandler) spawnLiquidationWorker(
	ctx context.Context,
	cmd *RunFleetCoordinatorCommand,
	ship *navigation.Ship,
) (string, error) {
	logger := common.LoggerFromContext(ctx)
	shipSymbol := ship.ShipSymbol()

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
