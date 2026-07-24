package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/stretchr/testify/require"
)

// sp-mxflh: bootstrapGateSurplusReleaser un-dedicates the requested surplus IDLE manufacturing hulls to the
// UNDEDICATED pool (AssignFleet→"", the single write path) — the OPPOSITE direction of the repurposer — so the
// contract scaler's reuse tier (isReclaimable) then adopts them into the contract fleet. It RE-GUARDS each hull
// at release time: a hull that started a construction task (in transit) or was already re-tagged since the
// observation is NEVER yanked mid-task, and a hull outside the requested set is never touched.
func TestBootstrapGateSurplusReleaser_UndedicatesIdleManufacturingAndSkipsMidTask(t *testing.T) {
	// Symbols avoid the "-1" suffix the command-frigate heuristic reserves (IsCommandHull), so these read as
	// ordinary gate workers, not the flagship.
	idle := reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)      // idle → un-dedicated
	midTask := reclaimHull(t, "MFG-8", 40, "manufacturing", navigation.NavStatusInTransit) // mid-delivery → skipped
	retagged := reclaimHull(t, "MFG-9", 40, "contract", navigation.NavStatusInOrbit)       // re-tagged since obs → skipped
	untouched := reclaimHull(t, "CONTRACT-4", 40, "contract", navigation.NavStatusInOrbit) // not requested → untouched
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{idle, midTask, retagged, untouched}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"MFG-7", "MFG-8", "MFG-9"})
	require.NoError(t, err)
	require.Equal(t, 1, released) // only the idle, still-manufacturing hull
	require.Equal(t, []assignFleetCall{{symbol: "MFG-7", fleet: ""}}, repo.assigned)

	// the released hull, in its post-release (un-dedicated) state, is exactly what the contract scaler's
	// reclaim-before-buy tier adopts — the zero-buy re-balance. (Non-tautological: had the adapter chosen any
	// non-"" fleet, isReclaimable would be false.)
	idle.SetDedicatedFleet(repo.assigned[0].fleet)
	require.True(t, isReclaimable(idle), "a released (un-dedicated) idle cargo hull must be reclaimable by the scaler")
}

// A fleet-read error fails closed: surface it and release nothing (never a blind write on an unknown fleet).
func TestBootstrapGateSurplusReleaser_FleetReadErrorFailsClosed(t *testing.T) {
	r := &bootstrapGateSurplusReleaser{shipRepo: &fakeReclaimShipRepo{findErr: errors.New("db down")}}
	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"MFG-1"})
	require.Error(t, err)
	require.Equal(t, 0, released)
	require.Nil(t, r.shipRepo.(*fakeReclaimShipRepo).assigned, "a read failure must un-dedicate nothing")
}
