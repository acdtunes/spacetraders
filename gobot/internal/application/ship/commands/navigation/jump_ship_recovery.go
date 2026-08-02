package navigation

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// claimShipForJump takes exclusive hold of the hull for the jump and returns the
// release the caller MUST defer. Jump does not run through ContainerRunner — it
// returns a rich typed response synchronously — so it creates a lightweight
// container row purely to satisfy the ship_assignments(container_id, player_id)
// foreign key, then claims the ship directly.
//
// The row id carries a per-attempt nonce. A leaked deterministic row would
// collide on containers_pkey for every LATER jump of that hull, wedging it
// permanently; and since ships.container_id is ON DELETE SET NULL, a shared id
// would let a losing attempt's cleanup silently unclaim the winner's hull.
func (h *JumpShipHandler) claimShipForJump(ctx context.Context, cmd *JumpShipCommand, playerID shared.PlayerID, logger common.ContainerLogger) (func(), error) {
	jumpContainerID := fmt.Sprintf("ship-jump-%s-%d", cmd.ShipSymbol, h.clock.Now().UnixNano())
	jumpContainer := domainContainer.NewContainer(
		jumpContainerID,
		domainContainer.ContainerTypeJump,
		playerID.Value(),
		1,
		nil,
		map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"destination": cmd.DestinationSystem,
		},
		h.clock,
	)
	if err := h.containerRepo.Add(ctx, jumpContainer, "jump_ship"); err != nil {
		return nil, fmt.Errorf("failed to create jump container record: %w", err)
	}
	removeContainer := func() {
		_ = h.containerRepo.Remove(ctx, jumpContainerID, playerID.Value())
	}

	// Claim under CAS-retry: the closure re-applies AssignToContainer
	// on the FRESH row so a concurrent writer's cargo/nav/fuel update on the
	// same hull survives instead of being last-write-wins clobbered by this
	// handler's pre-jump snapshot. This op owns ONLY the assignment; a fresh row
	// already assigned to someone else still surfaces the AssignToContainer
	// error, so exclusivity (RULINGS #7) is unchanged.
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, playerID,
		func(sh *domainNavigation.Ship) (bool, error) {
			if aerr := sh.AssignToContainer(jumpContainerID, h.clock); aerr != nil {
				return false, aerr
			}
			return true, nil
		}); err != nil {
		removeContainer()
		return nil, fmt.Errorf("failed to save ship claim: %w", err)
	}

	// The claim has landed, so this is the one moment where the leftovers of
	// this hull's earlier jumps are PROVABLY dead. See reapStrandedJumpContainers.
	h.reapStrandedJumpContainers(ctx, cmd.ShipSymbol, jumpContainerID, playerID, logger)

	return func() {
		// Release under CAS-retry too: re-apply ForceRelease on the fresh row,
		// touching only the assignment, and skip if the hull is no longer this
		// jump's claim (already released / re-claimed) so nothing else is clobbered.
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, playerID,
			func(sh *domainNavigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != jumpContainerID {
					return false, nil
				}
				sh.ForceRelease("jump_complete", h.clock)
				return true, nil
			})
		removeContainer()
	}, nil
}

// maxJumpOrbitRetries bounds how many times jumpWithOrbitRetry re-orbits and
// retries a jump the live API rejected as not-in-orbit (4236). One retry clears
// the realistic case (a single stale nav_status), and the bound guarantees a
// jump that keeps 4236-ing for any OTHER reason surfaces the error instead of
// looping forever.
// reapStrandedJumpContainers deletes the leftover JUMP container rows this hull
// accumulated from earlier jumps, so a crash-leaked claim record cannot outlive the
// fleet. activeContainerID is this attempt's own row, which is never touched.
//
// WHY THIS CANNOT CLEAR A JUMP THAT IS ACTUALLY IN FLIGHT. It runs only AFTER this
// handler's own AssignToContainer has committed, so this hull's single assignment row
// points at activeContainerID right now. A jump holds its hull claimed for its entire
// duration, so no other jump for this hull can be in flight while we hold that claim —
// any concurrent attempt is already guaranteed to lose at AssignToContainer, and its own
// deferred Remove is a harmless no-op against a row that is already gone.
//
// That held claim is POSITIVE evidence — a fact about the present, read from the same
// assignment row the rest of the fleet reads ownership from — and not an age heuristic.
// An age rule would have been the wrong instrument here: a jump row is written BEFORE its
// claim, so "old and unclaimed" cannot tell a stranded row from one whose jump is a
// millisecond away from claiming, and picking the wrong side of that rips a hull mid-spawn.
// Holding the claim removes the ambiguity entirely instead of trading it off.
//
// BEST-EFFORT BY CONSTRUCTION. With per-attempt IDs a leftover row is inert — it can no
// longer wedge anything — so this is hygiene, not the fix, and a failure here must never
// fail the jump. Both outcomes are counted, because a reap that never runs and a reap
// that runs and fails must not emit the same signal.
func (h *JumpShipHandler) reapStrandedJumpContainers(
	ctx context.Context,
	shipSymbol string,
	activeContainerID string,
	playerID shared.PlayerID,
	logger common.ContainerLogger,
) {
	if h.containerRepo == nil {
		return
	}

	ids, err := h.containerRepo.ListJumpContainersForShip(ctx, shipSymbol, playerID.Value())
	if err != nil {
		logger.Log("WARN", "could not look for stranded jump claim records, continuing with the jump", map[string]interface{}{
			"ship":  shipSymbol,
			"error": err.Error(),
		})
		return
	}

	for _, id := range ids {
		if id == activeContainerID {
			continue // this jump's own claim record
		}
		if err := h.containerRepo.Remove(ctx, id, playerID.Value()); err != nil {
			metrics.RecordStrandedJumpContainer(playerID.Value(), "clear_failed")
			logger.Log("WARN", "found a stranded jump claim record but could not clear it", map[string]interface{}{
				"ship":         shipSymbol,
				"container_id": id,
				"error":        err.Error(),
			})
			continue
		}
		metrics.RecordStrandedJumpContainer(playerID.Value(), "cleared")
		logger.Log("INFO", "cleared a stranded jump claim record left by an earlier jump", map[string]interface{}{
			"ship":         shipSymbol,
			"container_id": id,
		})
	}
}

const maxJumpOrbitRetries = 2

// jumpWithOrbitRetry executes the live jump, riding out a not-in-orbit rejection
// (400 code 4236) instead of hard-failing on it. Handle's proactive
// guard already orbits a hull it READ as docked; this covers the residual race
// where the persisted nav_status lagged a server-side dock, so the hull is
// docked on the server while the daemon believed it orbited. It mirrors how the
// trade-route coordinator rides a cooldown-409 (wc5h jumpHop): classify the one
// recoverable error, take the corrective action (orbit live), retry — bounded,
// with every other error propagated on the first attempt so a genuine jump
// failure (4262, a missing gate connection, an auth error) is never masked as a
// stale orbit.
func (h *JumpShipHandler) jumpWithOrbitRetry(
	ctx context.Context,
	ship *domainNavigation.Ship,
	cmd *JumpShipCommand,
	destinationGateWaypointSymbol, token string,
	playerID shared.PlayerID,
) (*ports.JumpResult, error) {
	logger := common.LoggerFromContext(ctx)
	for attempt := 0; ; attempt++ {
		jumpResult, err := h.apiClient.JumpShip(ctx, cmd.ShipSymbol, destinationGateWaypointSymbol, token)
		if err == nil {
			return jumpResult, nil
		}
		if !isNotInOrbitError(err) || attempt >= maxJumpOrbitRetries {
			return nil, err
		}
		logger.Log("WARNING", "Jump rejected as not-in-orbit (4236) — orbiting live and retrying (raced nav_status; resume-safe, sp-28n2)", map[string]interface{}{
			"ship_symbol":        cmd.ShipSymbol,
			"destination_system": cmd.DestinationSystem,
			"attempt":            attempt + 1,
		})
		if oerr := h.shipRepo.Orbit(ctx, ship, playerID); oerr != nil {
			return nil, fmt.Errorf("failed to orbit %s after a not-in-orbit jump rejection: %w", cmd.ShipSymbol, oerr)
		}
	}
}

// isNotInOrbitError reports whether the API rejected an action because the ship
// is not in orbit (error 4236). Mirrors isDestinationGateUnderConstructionError's
// string-matching approach — the wire form is
// `API error (status 400): {"error":{"code":4236,"message":"Ship ... is not currently in orbit ..."}}`.
func isNotInOrbitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4236") || strings.Contains(msg, "not currently in orbit")
}
