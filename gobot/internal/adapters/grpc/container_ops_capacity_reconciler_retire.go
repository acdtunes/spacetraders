package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-2jrz: stop-is-complete-retire for the capacity reconciler.
//
// The armed reconciler dedicates idle pool hulls to its own contract-delivery
// role-fleets (warehouse / stocker / depot-delivery — see the capacity actuator's
// fleetForRole) to fill a desired topology driven by contract HISTORY. When the
// contract-delivery domain is operationally decommissioned, that history lingers, so
// the running reconciler keeps re-dedicating hulls to dead buffer roles — and a
// daemon restart faithfully recovers it and re-strands them (the sp-2jrz incident).
//
// The captain's manual remedy was: stop the reconciler + stop its zombie buffer
// containers + re-dedicate the stranded lights back to trade. This makes a DURABLE
// operator STOP do that whole remedy atomically: reap the buffer containers holding
// the role-fleet hulls, then release those role-fleet dedications back to the general
// pool so trade re-adopts them. Recovery already skips a STOPPED container, so the
// domain then stays retired across a restart — the Admiral's invariant that a restart
// must not change ship assignments.

// capacityReconcilerRoleFleets are the fleet tags the reconciler's tier-1 reassign
// dedicates hulls to (capacity actuator fleetForRole). Tied to the existing grpc
// operation/fleet constants so there is no literal-drift with the role coordinators
// the tags must match. These fleets are reconciler-owned BY CONSTRUCTION: an operator
// pins a light to "trade", never to a buffer role, so releasing them on decommission
// cannot clobber an operator's own dedication.
var capacityReconcilerRoleFleets = []string{operationWarehouse, operationStocker, depot.DeliveryHullFleet}

// roleFleetHull is one hull dedicated to a reconciler role-fleet, plus the container
// (if any) currently claiming it — the buffer container to reap.
type roleFleetHull struct {
	shipSymbol  string
	fleet       string
	containerID string
}

// retireCapacityReconcilerDomain completes the capacity reconciler's decommission on
// a DURABLE operator STOP: reap the buffer containers claiming its role-fleet hulls,
// then release those role-fleet dedications back to the general pool.
//
// STOP-PATH ONLY. This is invoked from StopContainer (which persists STOPPED), NEVER
// from interruptAllContainers / the deploy-recovery path (which persists INTERRUPTED
// and re-adopts). A graceful deploy of a live reconciler must re-establish its
// dedications on the next tick, not have them torn down — releasing on the recovery
// path would churn assignments on every deploy, the exact bug (sp-2jrz / sp-ve3q).
//
// Every step is best-effort and loudly logged: a decommission that half-completes must
// be visible, and a per-hull failure must not abort the rest. Release is IDLE-ONLY — a
// role-fleet hull still claimed after the reap is left dedicated and logged, never
// force-yanked mid-work.
func (s *DaemonServer) retireCapacityReconcilerDomain(ctx context.Context, playerID int) {
	hulls, err := s.roleFleetHulls(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: capacity reconciler retire (player %d): failed to list role-fleet hulls: %v\n", playerID, err)
		return
	}
	if len(hulls) == 0 {
		return
	}

	// 1. REAP the buffer containers claiming the role-fleet hulls. A role-fleet hull's
	//    only legitimate claimant is its own-operation buffer container (the atomic
	//    operation-checked ClaimShip), so reaping the claimer only ever stops a
	//    reconciler-owned buffer container — never trade/contract/the reconciler.
	reaped := map[string]bool{}
	for _, h := range hulls {
		if h.containerID == "" || reaped[h.containerID] {
			continue
		}
		reaped[h.containerID] = true
		s.reapBufferContainer(ctx, h.containerID, playerID)
	}

	// 2. RELEASE the role-fleet dedications back to the pool so trade re-adopts (idle-only).
	pid := shared.MustNewPlayerID(playerID)
	released := 0
	for _, h := range hulls {
		claimed, err := s.hullClaimingContainer(ctx, h.shipSymbol, playerID)
		if err != nil {
			fmt.Printf("Warning: capacity reconciler retire: failed to re-read %s: %v\n", h.shipSymbol, err)
			continue
		}
		if claimed != "" {
			fmt.Printf("Warning: capacity reconciler retire: %s still claimed by %s after reap — left dedicated to %q (idle-only); operator can `fleet unassign`\n",
				h.shipSymbol, claimed, h.fleet)
			continue
		}
		if err := s.shipRepo.AssignFleet(ctx, h.shipSymbol, "", pid); err != nil {
			fmt.Printf("Warning: capacity reconciler retire: failed to release %s from %q: %v\n", h.shipSymbol, h.fleet, err)
			continue
		}
		released++
		fmt.Printf("Capacity reconciler retire: released %s from %q back to the general pool (sp-2jrz)\n", h.shipSymbol, h.fleet)
	}

	fmt.Printf("Capacity reconciler retired (player %d): reaped %d buffer container(s), released %d role-fleet hull(s) to the pool\n",
		playerID, len(reaped), released)
}

// reapBufferContainer stops one buffer container the reconciler's converge left
// holding a role-fleet hull. The in-memory graceful stop (StopContainer) releases the
// hull's claim on a clean exit; an orphaned RUNNING row with no live runner is
// terminalized directly and its claims released, so the hull idles either way and step
// 2 can return it to the pool.
func (s *DaemonServer) reapBufferContainer(ctx context.Context, containerID string, playerID int) {
	if err := s.StopContainer(containerID); err == nil {
		fmt.Printf("Capacity reconciler retire: reaped buffer container %s (sp-2jrz)\n", containerID)
		return
	}
	// Not in the in-memory runner map (orphaned RUNNING row, or already exited):
	// terminalize the row and release its ship claims so the role-fleet hull idles.
	now := time.Now()
	exitCode := 0
	if err := s.containerRepo.UpdateStatus(ctx, containerID, playerID, container.ContainerStatusStopped, &now, &exitCode, "capacity reconciler retired (sp-2jrz)"); err != nil {
		fmt.Printf("Warning: capacity reconciler retire: failed to mark orphaned buffer container %s STOPPED: %v\n", containerID, err)
	}
	s.releaseBufferContainerClaims(ctx, containerID, playerID)
	fmt.Printf("Capacity reconciler retire: reaped orphaned buffer container %s (marked STOPPED + released claims, sp-2jrz)\n", containerID)
}

// releaseBufferContainerClaims force-releases the live claims held by a reaped orphaned
// buffer container, mirroring markContainerFailed's release: the hull's DedicatedFleet
// tag is left untouched here (retireCapacityReconcilerDomain clears it in step 2), only
// the container claim is broken so the hull reads idle.
func (s *DaemonServer) releaseBufferContainerClaims(ctx context.Context, containerID string, playerID int) {
	pid := shared.MustNewPlayerID(playerID)
	ships, err := s.shipRepo.FindByContainer(ctx, containerID, pid)
	if err != nil {
		fmt.Printf("Warning: capacity reconciler retire: failed to find ships for buffer container %s: %v\n", containerID, err)
		return
	}
	for _, ship := range ships {
		shipSymbol := ship.ShipSymbol()
		if _, _, err := s.shipRepo.SaveWithRetry(ctx, shipSymbol, pid,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease("capacity reconciler retired (sp-2jrz)", s.clock)
				return true, nil
			}); err != nil {
			fmt.Printf("Warning: capacity reconciler retire: failed to release %s from buffer container %s: %v\n", shipSymbol, containerID, err)
		}
	}
}

// roleFleetHulls lists the player's hulls dedicated to a reconciler role-fleet, with
// the container (if any) currently claiming each. A read-only projection over the ships
// table (mirrors the capacity Sensor's utilization read) — nil db fails safe (nothing
// to retire).
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

// hullClaimingContainer returns the container id currently claiming the hull, or ""
// when the hull is idle — the idle-only guard for the dedication release.
func (s *DaemonServer) hullClaimingContainer(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	if s.db == nil {
		return "", nil
	}
	var row struct{ ContainerID *string }
	if err := s.db.WithContext(ctx).
		Table("ships").
		Select("container_id").
		Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID).
		Scan(&row).Error; err != nil {
		return "", err
	}
	if row.ContainerID == nil {
		return "", nil
	}
	return *row.ContainerID, nil
}
