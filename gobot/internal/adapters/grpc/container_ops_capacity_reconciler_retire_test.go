package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-2jrz — stop-is-complete-retire (fix a). Decommissioning the capacity
// reconciler is a DURABLE operator STOP. A stop must not merely halt the loop: it
// must RELEASE the role-fleet dedications the reconciler wrote (warehouse / stocker
// / depot-delivery) back to the general pool so trade re-adopts the hulls — the
// missing half that forced the captain to manually re-dedicate the 9 stranded
// lights. Recovery already skips a STOPPED reconciler, so a stopped domain then
// stays retired across a restart (the Admiral's "a restart must not change ship
// assignments").

// assertDedicatedFleet asserts a hull's persisted dedicated_fleet tag.
func assertDedicatedFleet(t *testing.T, db *gorm.DB, symbol, want string, playerID int) {
	t.Helper()
	var model persistence.ShipModel
	require.NoError(t, db.First(&model, "ship_symbol = ? AND player_id = ?", symbol, playerID).Error)
	require.Equal(t, want, model.DedicatedFleet,
		"ship %s dedicated_fleet", symbol)
}

// stopping the reconciler releases its OWN role-fleet dedications on idle hulls,
// and leaves an operator's trade pin (and any other fleet) untouched.
func TestStopCapacityReconciler_ReleasesRoleFleetHullsToPool(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	ctx := context.Background()

	// The reconciler's stranded artifacts: idle hulls it re-dedicated to its own
	// buffer role-fleets (the 9 lights). No live claim (idle) — the common stranded
	// shape once the buffer containers have stopped ticking.
	insertDepotDeliveryHull(t, db, "LIGHT-13", playerID, "stocker", "X1-J58-A", false)
	insertDepotDeliveryHull(t, db, "LIGHT-11", playerID, "warehouse", "X1-J58-B", false)
	insertDepotDeliveryHull(t, db, "LIGHT-8", playerID, "depot-delivery", "X1-J58-C", false)
	// Controls that must NEVER be touched: the operator's trade pin (the captain's
	// remedy) and a contract-reserve hull.
	insertDepotDeliveryHull(t, db, "LIGHT-TRADE", playerID, "trade", "X1-J58-D", false)
	insertDepotDeliveryHull(t, db, "LIGHT-CONTRACT", playerID, "contract", "X1-J58-E", false)

	reconcilerID, err := s.CapacityReconcilerCoordinator(ctx, playerID, false)
	require.NoError(t, err)

	// Operator STOP -> complete retire.
	require.NoError(t, s.StopContainer(reconcilerID))

	assertDedicatedFleet(t, db, "LIGHT-13", "", playerID)
	assertDedicatedFleet(t, db, "LIGHT-11", "", playerID)
	assertDedicatedFleet(t, db, "LIGHT-8", "", playerID)
	assertDedicatedFleet(t, db, "LIGHT-TRADE", "trade", playerID)
	assertDedicatedFleet(t, db, "LIGHT-CONTRACT", "contract", playerID)

	// And the reconciler itself is durably STOPPED (recovery skips it).
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", reconcilerID).Error)
	require.Equal(t, string(container.ContainerStatusStopped), model.Status)
}

// LANE SPLIT (idle-only): a role-fleet hull still held by a LIVE buffer container is
// NOT released here, and the buffer container is NOT reaped — that is lane (ii)/sp-udgc
// (the depot-launch owner). Lane (i) leaves the claimed hull dedicated so it is never
// yanked mid-work; lane (ii) reaps the container, after which the hull idles and a later
// retire returns it to the pool.
func TestStopCapacityReconciler_LeavesClaimedRoleFleetHullForReapLane(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	ctx := context.Background()

	insertRunningContainer(t, db, "stocker-LIGHT-14", "stocker", string(container.ContainerTypeTrading),
		`{"ship_symbol":"LIGHT-14"}`, playerID, nil)
	insertClaimedRoleFleetHull(t, db, "LIGHT-14", playerID, "stocker", "X1-J58-A", "stocker-LIGHT-14")

	reconcilerID, err := s.CapacityReconcilerCoordinator(ctx, playerID, false)
	require.NoError(t, err)
	require.NoError(t, s.StopContainer(reconcilerID))

	// idle-only: the claimed hull keeps its dedication (not force-yanked mid-work)...
	assertDedicatedFleet(t, db, "LIGHT-14", "stocker", playerID)
	// ...and the buffer container is left running for lane (ii)/sp-udgc to reap.
	var bufferModel persistence.ContainerModel
	require.NoError(t, db.First(&bufferModel, "id = ?", "stocker-LIGHT-14").Error)
	require.Equal(t, string(container.ContainerStatusRunning), bufferModel.Status,
		"lane (i) must not reap the buffer container — that is lane (ii)/sp-udgc")
}

// The Admiral's invariant, made explicit: the INTERRUPTED / deploy-recovery path must
// NEVER retire. interruptAllContainers (graceful deploy) marks the reconciler
// INTERRUPTED and leaves every role-fleet dedication intact — a recovered reconciler
// re-establishes its topology on the next tick. Only a durable operator STOP releases
// (proven above); a deploy must not churn ship assignments.
func TestInterruptDoesNotRetireCapacityReconciler(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	ctx := context.Background()
	insertDepotDeliveryHull(t, db, "LIGHT-13", playerID, "stocker", "X1-J58-A", false)

	reconcilerID, err := s.CapacityReconcilerCoordinator(ctx, playerID, false)
	require.NoError(t, err)
	requireContainerStatusEventually(t, db, reconcilerID, string(container.ContainerStatusRunning))

	s.interruptAllContainers() // the graceful-deploy path

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", reconcilerID).Error)
	require.Equal(t, string(container.ContainerStatusInterrupted), model.Status,
		"a deploy interrupts the reconciler for recovery — it does not stop/retire it")
	assertDedicatedFleet(t, db, "LIGHT-13", "stocker", playerID)
}

// The restart-half of the Admiral's invariant: after a durable STOP, a daemon restart's
// recovery pass must NOT resurrect the reconciler or re-strand the freed hulls. Recovery
// lists only RUNNING/INTERRUPTED containers, so a STOPPED (retired) reconciler stays down
// and its released hulls stay in the general pool.
func TestRecoveryDoesNotResurrectStoppedCapacityReconciler(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	ctx := context.Background()
	insertDepotDeliveryHull(t, db, "LIGHT-13", playerID, "stocker", "X1-J58-A", false)

	reconcilerID, err := s.CapacityReconcilerCoordinator(ctx, playerID, false)
	require.NoError(t, err)
	require.NoError(t, s.StopContainer(reconcilerID))
	assertDedicatedFleet(t, db, "LIGHT-13", "", playerID) // retired -> released to the pool

	// Simulate the daemon-restart recovery pass.
	require.NoError(t, s.RecoverRunningContainers(ctx))

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", reconcilerID).Error)
	require.Equal(t, string(container.ContainerStatusStopped), model.Status,
		"recovery must not resurrect a STOPPED (retired) reconciler")
	assertDedicatedFleet(t, db, "LIGHT-13", "", playerID)
}

// insertClaimedRoleFleetHull seeds a role-fleet-dedicated hull with a live claim held
// by containerID (the "buffer container holds the hull" shape).
func insertClaimedRoleFleetHull(t *testing.T, db *gorm.DB, symbol string, playerID int, fleet, location, containerID string) {
	t.Helper()
	model := &persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		Role:             "HAULER",
		CargoCapacity:    80,
		FuelCapacity:     100,
		FuelCurrent:      100,
		EngineSpeed:      30,
		FrameSymbol:      "FRAME_LIGHT_FREIGHTER",
		NavStatus:        "DOCKED",
		LocationSymbol:   location,
		SystemSymbol:     shared.ExtractSystemSymbol(location),
		DedicatedFleet:   fleet,
		ContainerID:      &containerID,
		AssignmentStatus: "active",
		AssignmentOwner:  string(navigation.AssignmentOwnerContainer),
	}
	require.NoError(t, db.Create(model).Error)
}

// requireContainerStatusEventually polls the persisted container status until it
// matches want or the poll times out — bridges the launch goroutine's PENDING->RUNNING
// transition without a fixed sleep.
func requireContainerStatusEventually(t *testing.T, db *gorm.DB, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var model persistence.ContainerModel
		if err := db.First(&model, "id = ?", id).Error; err == nil && model.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("container %s did not reach status %q within timeout", id, want)
}
