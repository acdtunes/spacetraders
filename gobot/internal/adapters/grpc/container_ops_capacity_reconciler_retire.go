package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-2jrz: stop-is-complete-retire for the capacity reconciler — lane (i).
//
// The armed reconciler dedicates idle pool hulls to its own contract-delivery
// role-fleets (warehouse / stocker / depot-delivery — see the capacity actuator's
// fleetForRole) to fill a desired topology driven by contract HISTORY. When the
// contract-delivery domain is operationally decommissioned that history lingers, so
// the running reconciler keeps re-dedicating hulls to dead buffer roles — and a daemon
// restart faithfully recovers it and re-strands them.
//
// On a DURABLE operator STOP this releases the reconciler's own role-fleet dedications
// back to the general pool so trade re-adopts the hulls. Recovery already skips a
// STOPPED container, so the domain then stays retired across a restart (the Admiral's
// invariant that a restart must not change ship assignments).
//
// LANE SPLIT: this lane (i) only RELEASES the reconciler's dedications. It does NOT
// reap the buffer containers — those are launched by the depot-launch path, which lane
// (ii) (sp-udgc) owns. So the release is IDLE-ONLY: a role-fleet hull still held by a
// LIVE buffer container is left dedicated here and freed when lane (ii) reaps that
// container; an already-idle stranded hull is released straight to the pool.

// capacityReconcilerRoleFleets are the fleet tags the reconciler's tier-1 reassign
// dedicates hulls to (capacity actuator fleetForRole). Tied to the existing grpc
// operation/fleet constants so there is no literal-drift with the role coordinators the
// tags must match. These fleets are reconciler-owned BY CONSTRUCTION: an operator pins a
// light to "trade", never to a buffer role, so releasing them on decommission cannot
// clobber an operator's own dedication.
var capacityReconcilerRoleFleets = []string{operationWarehouse, operationStocker, depot.DeliveryHullFleet}

// roleFleetHull is one hull dedicated to a reconciler role-fleet, with the container (if
// any) currently claiming it — non-empty means a live buffer container still holds it.
type roleFleetHull struct {
	shipSymbol  string
	fleet       string
	containerID string
}

// retireCapacityReconcilerDedications completes the capacity reconciler's decommission
// on a DURABLE operator STOP by releasing the reconciler's own role-fleet dedications
// back to the general pool.
//
// STOP-PATH ONLY. Invoked from StopContainer (which persists STOPPED), NEVER from
// interruptAllContainers / the deploy-recovery path (which persists INTERRUPTED via a
// direct UpdateStatus and re-adopts). A graceful deploy of a live reconciler must
// re-establish its dedications on the next tick, not have them torn down — releasing on
// the recovery path would churn assignments on every deploy, the exact bug
// (sp-2jrz / sp-ve3q).
//
// IDLE-ONLY + loudly logged: a role-fleet hull still claimed by a live buffer container
// is left dedicated (lane (ii)/sp-udgc reaps that container), never force-yanked
// mid-work. Best-effort: a per-hull failure logs and does not abort the rest.
func (s *DaemonServer) retireCapacityReconcilerDedications(ctx context.Context, playerID int) {
	hulls, err := s.roleFleetHulls(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: capacity reconciler retire (player %d): failed to list role-fleet hulls: %v\n", playerID, err)
		return
	}
	if len(hulls) == 0 {
		return
	}

	pid := shared.MustNewPlayerID(playerID)
	released, heldForReap := 0, 0
	for _, h := range hulls {
		if h.containerID != "" {
			// Still held by a live buffer container — idle-only: leave the dedication so
			// the hull is not yanked mid-work; lane (ii) reaps the container and frees it.
			fmt.Printf("Capacity reconciler retire: %s still claimed by %s — left dedicated to %q (idle-only; lane ii reaps the buffer container, sp-2jrz)\n",
				h.shipSymbol, h.containerID, h.fleet)
			heldForReap++
			continue
		}
		if err := s.shipRepo.AssignFleet(ctx, h.shipSymbol, "", pid); err != nil {
			fmt.Printf("Warning: capacity reconciler retire: failed to release %s from %q: %v\n", h.shipSymbol, h.fleet, err)
			continue
		}
		released++
		fmt.Printf("Capacity reconciler retire: released %s from %q back to the general pool (sp-2jrz)\n", h.shipSymbol, h.fleet)
	}

	fmt.Printf("Capacity reconciler retired (player %d): released %d idle role-fleet hull(s) to the pool, left %d claimed for lane ii reap\n",
		playerID, released, heldForReap)
}

// roleFleetHulls lists the player's hulls dedicated to a reconciler role-fleet, with the
// container (if any) currently claiming each. A read-only projection over the ships
// table (mirrors the capacity Sensor's utilization read) — nil db fails safe (nothing to
// retire).
func (s *DaemonServer) roleFleetHulls(ctx context.Context, playerID int) ([]roleFleetHull, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []struct {
		ShipSymbol     string
		DedicatedFleet string
		ContainerID    *string
	}
	if err := s.db.WithContext(ctx).
		Table("ships").
		Select("ship_symbol, dedicated_fleet, container_id").
		Where("player_id = ? AND dedicated_fleet IN ?", playerID, capacityReconcilerRoleFleets).
		Order("ship_symbol").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	hulls := make([]roleFleetHull, 0, len(rows))
	for _, row := range rows {
		containerID := ""
		if row.ContainerID != nil {
			containerID = *row.ContainerID
		}
		hulls = append(hulls, roleFleetHull{shipSymbol: row.ShipSymbol, fleet: row.DedicatedFleet, containerID: containerID})
	}
	return hulls, nil
}
