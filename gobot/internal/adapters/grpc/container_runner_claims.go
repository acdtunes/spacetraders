package grpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// A captain CLI chain like `ship orbit` then `ship navigate` issued
// ~1s apart spawns back-to-back containers on the same hull. The second
// container's claim can land in the sub-second window before the first's
// synchronous release has been persisted, surfacing as a *transient*
// ShipAlreadyAssignedError. createShipAssignments retries exactly that failure a
// bounded number of times with growing backoff to absorb the handoff window,
// instead of failing to the captain and forcing a manual retry. A *permanent*
// rejection (captain reservation or foreign-fleet dedication) is never
// retried — no amount of waiting clears it. The bound keeps a genuinely-held
// hull from causing a retry storm; the growing backoff (200ms → 3s cap, ~9s
// worst case over the whole budget) resolves the common sub-second race on the
// first short retry while still tolerating a slightly slower release.
const (
	claimRetryMaxAttempts = 7
	claimRetryBaseBackoff = 200 * time.Millisecond
	claimRetryMaxBackoff  = 3 * time.Second
)

// createShipAssignments claims the hull named in the container metadata
// ("ship_symbol") for this container, so concurrent containers can't operate on
// the same ship. It is a no-op for containers that carry no "ship_symbol" (e.g.
// scout-fleet-assignment).
//
// The claim is retried briefly on the transient claim-handoff race: a
// captain CLI chain (orbit then navigate ~1s apart) can have navigate's claim
// land before orbit's synchronous release has been persisted, surfacing as a
// ShipAlreadyAssignedError. Retrying absorbs that window instead of failing to
// the captain. A permanent rejection — captain reservation or foreign-fleet
// dedication — is returned on the first attempt, never retried.
func (r *ContainerRunner) createShipAssignments() error {
	if r.shipRepo == nil {
		return nil
	}

	metadata := r.containerEntity.Metadata()

	shipSymbol, ok := metadata["ship_symbol"].(string)
	if !ok {
		// No ship_symbol in config = no ships to assign (e.g. scout-fleet-assignment).
		return nil
	}

	playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
	operation, _ := metadata["operation"].(string)
	// captainManualAuthority (BRIDGE) is set ONLY by the CLI manual-op path
	// (container_ops_ship.go); it lets a deliberate captain op override the
	// dedication guard on the legacy claim path. No automated coordinator sets it.
	captainManualAuthority, _ := metadata[captainManualAuthorityKey].(bool)

	backoff := claimRetryBaseBackoff
	for attempt := 1; ; attempt++ {
		err := r.attemptClaimShip(shipSymbol, operation, captainManualAuthority, playerID)
		if err == nil {
			if attempt > 1 {
				r.log("INFO", fmt.Sprintf("Claimed ship %s on attempt %d — transient claim-handoff race cleared", shipSymbol, attempt), nil)
			}
			return nil
		}

		// Only the transient handoff race is worth waiting on; a permanent
		// rejection (dedication / captain reservation / DB error) fails fast, and
		// the bounded attempt count keeps a genuinely-held hull from a retry storm.
		if !isTransientClaimError(err) || attempt >= claimRetryMaxAttempts {
			return err
		}

		r.log("INFO", fmt.Sprintf("Ship %s lost the claim-handoff race (attempt %d/%d), retrying in %s: %v",
			shipSymbol, attempt, claimRetryMaxAttempts, backoff, err), nil)

		if waitErr := r.sleepOrCancel(backoff); waitErr != nil {
			return fmt.Errorf("failed to claim ship %s: retry canceled: %w", shipSymbol, waitErr)
		}

		backoff *= 2
		if backoff > claimRetryMaxBackoff {
			backoff = claimRetryMaxBackoff
		}
	}
}

// captainManualAuthorityKey (BRIDGE) is the container-metadata flag that
// marks a deliberate captain-initiated manual CLI op (navigate/dock/orbit/refuel/
// jettison) permitted to operate a fleet-dedicated hull as an explicit, audited
// override of the legacy-path dedication guard. It is set ONLY by the CLI
// manual-op path (container_ops_ship.go); NO automated coordinator sets it, so the
// dedication guard stays fully in force for every automated claim. Single-purpose
// and deprecable: delete this const, the guard's override branch, its audit log,
// and the container_ops_ship.go stamps together when hands-free manual
// repositioning retires the need for it (sp-lxwn/sp-zhii).
const captainManualAuthorityKey = "captain_manual_authority"

// attemptClaimShip performs a single claim of the hull for this container — the
// retryable unit of createShipAssignments. Containers carrying an "operation"
// metadata key (the launcher's fleet identity, e.g. StartTradeRoute's "trade")
// claim through the atomic operation-checked ShipRepository.ClaimShip (sp-l7h2
// Phase 2): assignment and fleet dedication are re-checked inside one row-locked
// transaction, so a hull pinned to a foreign fleet — or grabbed between discovery
// and this write — is rejected, never clobbered. Containers without the key
// (pre-change persisted rows, and every kind whose coordinator claims the hull
// BEFORE starting the runner) keep the legacy read-modify-write path, where the
// already-assigned-to-this-container check makes a recovered container's re-claim
// a no-op. That legacy path ALSO enforces the same fleet-dedication guard
// (sp-sg35): a hull pinned to a foreign fleet is rejected there too, so the
// absence of an "operation" key is not a side door around ownership. The
// exceptions are both captainAuthority claims (the captainManualAuthorityKey flag,
// set only by the CLI manual-op path): a deliberate captain override may operate a
// foreign-fleet-DEDICATED hull, and may operate its OWN captain-RESERVED hull
// (sp-sfoe) — the latter skips the claim entirely so the reservation survives the
// op — both audited on every use, and automated paths never set the flag. Both
// paths surface a transient *shared.ShipAlreadyAssignedError when the hull is
// momentarily still held by a just-finished container; a permanent rejection
// (foreign-fleet dedication, or a captain reservation on a non-captain claim) is
// returned unchanged for createShipAssignments to classify.
func (r *ContainerRunner) attemptClaimShip(shipSymbol, operation string, captainAuthority bool, playerID shared.PlayerID) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	if operation != "" {
		if err := r.shipRepo.ClaimShip(ctx, shipSymbol, r.containerEntity.ID(), playerID, operation); err != nil {
			return fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
		}
		r.log("INFO", fmt.Sprintf("Claimed ship %s for container (operation %s)", shipSymbol, operation), nil)
		return nil
	}

	ship, err := r.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}

	// Idempotent for a recovered container. Ordered FIRST like ClaimShip's own sequence:
	// dedication governs the NEXT acquisition, never eviction of the current holder.
	if ship.IsAssigned() && ship.ContainerID() == r.containerEntity.ID() {
		r.log("INFO", fmt.Sprintf("Ship %s already assigned to this container (recovered)", shipSymbol), nil)
		return nil
	}

	if err := r.enforceFleetDedication(ship, shipSymbol, operation, captainAuthority); err != nil {
		return err
	}

	if captainAuthority && ship.IsReservedByCaptain() {
		// Skipping the claim (rather than converting the reservation into an assignment) keeps
		// the hull assignment_owner=captain, so every coordinator path stays locked out throughout.
		r.log("WARNING", fmt.Sprintf(
			"Captain-context override: manual op %s operates captain-reserved hull %s without dropping the reservation (sp-sfoe)",
			r.containerEntity.Type(), shipSymbol),
			map[string]interface{}{
				"action":       "captain_context_reservation_passthrough",
				"ship_symbol":  shipSymbol,
				"op":           string(r.containerEntity.Type()),
				"container_id": r.containerEntity.ID(),
			})
		return nil
	}

	// Guard pass on the loaded snapshot: a hull already held by another container, or
	// captain-reserved, is rejected here with the error type createShipAssignments
	// classifies (transient handoff race vs standing rejection).
	if err := ship.AssignToContainer(r.containerEntity.ID(), r.clock); err != nil {
		return fmt.Errorf("failed to assign ship %s: %w", shipSymbol, err)
	}

	return r.persistClaimOnFreshRow(ctx, shipSymbol, playerID)
}

// enforceFleetDedication applies the SAME guard atomic ClaimShip enforces, so a foreign-fleet
// hull can never be claimed through this side door — operation is empty, so any pin rejects.
func (r *ContainerRunner) enforceFleetDedication(ship *navigation.Ship, shipSymbol, operation string, captainAuthority bool) error {
	if ship.DedicatedFleet() == "" || ship.DedicatedFleet() == operation {
		return nil
	}

	// Unassign-first is non-atomic and would strand a heavy unpinned. The flag is set ONLY by
	// container_ops_ship.go, so no automated coordinator can reach this branch.
	if !captainAuthority {
		return shared.NewShipDedicatedToOtherFleetError(shipSymbol, ship.DedicatedFleet(), operation)
	}
	r.log("WARNING", fmt.Sprintf(
		"Captain-authority override: manual op %s claims %s-dedicated hull %s without unassigning (bridge authority — deprecate with sp-lxwn/sp-zhii)",
		r.containerEntity.Type(), ship.DedicatedFleet(), shipSymbol),
		map[string]interface{}{
			"action":          "captain_manual_authority_override",
			"ship_symbol":     shipSymbol,
			"op":              string(r.containerEntity.Type()),
			"dedicated_fleet": ship.DedicatedFleet(),
			"container_id":    r.containerEntity.ID(),
		})
	return nil
}

// persistClaimOnFreshRow writes the claim on the FRESH row: a Save that loses the version
// race takes its ownership columns from the row, so the claim would silently not stick.
func (r *ContainerRunner) persistClaimOnFreshRow(ctx context.Context, shipSymbol string, playerID shared.PlayerID) error {
	if _, _, err := r.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(fresh *navigation.Ship) (bool, error) {
			if fresh.IsAssigned() && fresh.ContainerID() == r.containerEntity.ID() {
				return false, nil
			}
			if err := fresh.AssignToContainer(r.containerEntity.ID(), r.clock); err != nil {
				return false, err
			}
			return true, nil
		}); err != nil {
		return fmt.Errorf("failed to persist ship %s assignment: %w", shipSymbol, err)
	}

	r.log("INFO", fmt.Sprintf("Assigned ship %s to container", shipSymbol), nil)
	return nil
}

// isTransientClaimError reports whether a claim failure is the transient
// claim-handoff race — the hull is momentarily still assigned to
// another, just-finished container — and is therefore worth a brief retry. A
// captain reservation (ShipReservedByCaptainError) and a foreign-fleet
// dedication (ShipDedicatedToOtherFleetError) are standing rejections
// that no wait will clear, so those — and every other error, e.g. a DB failure —
// are permanent and returned to the caller immediately.
func isTransientClaimError(err error) bool {
	var alreadyAssigned *shared.ShipAlreadyAssignedError
	return errors.As(err, &alreadyAssigned)
}

// releaseShipAssignments releases all ship assignments for this container
// flowRemovalWanted reports whether a container exit for the given reason is
// terminal and should drop the container's flow from the read-only feed. Only the
// resumable "canceled" reason (ctx-cancel before re-adoption) preserves the entry,
// so the re-adopted container keeps its flow until it re-publishes (sp-7yej inv-4).
func flowRemovalWanted(reason string) bool {
	return reason != "canceled"
}

func (r *ContainerRunner) releaseShipAssignments(reason string) {
	if flowRemovalWanted(reason) {
		flowfeed.Remove(r.containerEntity.ID())
	}
	if r.shipRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	playerID := shared.MustNewPlayerID(r.containerEntity.PlayerID())
	assignedShips, err := r.shipRepo.FindByContainer(ctx, r.containerEntity.ID(), playerID)
	if err != nil {
		r.log("ERROR", fmt.Sprintf("Failed to find ships for container: %v", err), nil)
		return
	}

	for _, ship := range assignedShips {
		symbol := ship.ShipSymbol()
		// Release under CAS-retry: re-apply ForceRelease on the FRESH row
		// so a concurrent writer's cargo/nav update on the same hull survives instead
		// of being last-write-wins clobbered by the FindByContainer snapshot. Skip
		// unless the hull is still assigned to THIS container (a concurrent release or
		// a fresh re-claim by another container -> changed=false), so a hull that
		// moved on is never released out from under its new owner (RULINGS #7).
		if _, _, err := r.shipRepo.SaveWithRetry(ctx, symbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != r.containerEntity.ID() {
					return false, nil
				}
				sh.ForceRelease(reason, r.clock)
				return true, nil
			}); err != nil {
			r.log("ERROR", fmt.Sprintf("Failed to release ship %s: %v", symbol, err), nil)
		}
	}

	if len(assignedShips) > 0 {
		r.log("INFO", fmt.Sprintf("Released %d ship assignments (reason: %s)", len(assignedShips), reason), nil)
	}
}
