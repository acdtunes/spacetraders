package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-cr86: a container that dies PRE-CLAIM (spawns, then fails to claim its ship at
// startup - e.g. the ship is already assigned to another live container, or reserved
// by the captain) must not leave its row stuck at RUNNING forever. Start() persists
// RUNNING to the DB and only THEN attempts the ship claim; before this fix, a claim
// failure returned an error to the caller but left that just-written RUNNING row
// untouched - and since the heartbeat goroutine only starts AFTER the claim succeeds,
// the row's heartbeat_at timestamp never advances again either. The watchkeeper then
// spammed heartbeat_lost forever for a container with no live process behind it (6
// real instances in one night, requiring manual captain intervention each time).
// Start() must terminalize the row (FAILED) on the claim-failure exit, mirroring how
// a normal completion/failure terminalizes (handleError) and how recovery marks an
// interrupted worker FAILED (markWorkerInterrupted).
func TestStartTerminalizesRowWhenShipClaimFails(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	// SHIP-BUSY is already flying for a different container (e.g. an orbit container
	// that hasn't released it yet) - exactly the "already assigned to container" claim
	// failure the bead reports.
	busyShip := newIdleTradeShip(t, "SHIP-BUSY", playerID)
	require.NoError(t, busyShip.AssignToContainer("orbit-SHIP-BUSY", shared.NewRealClock()))
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-BUSY": busyShip}}

	const containerID = "navigate-SHIP-BUSY"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-BUSY"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "a ship claimed by another container must still fail Start()")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	// The other container's claim must be untouched by our failed attempt - we must
	// not have stolen it, and our cleanup must not have force-released it either.
	require.Equal(t, "orbit-SHIP-BUSY", busyShip.ContainerID())
	// No live runner should remain registered/heartbeating for a claim that never
	// succeeded.
	require.Nil(t, s.registeredRunner(containerID))
}

// sp-l7h2 Phase 2: a container carrying an "operation" metadata key claims its
// hull through the atomic operation-checked ClaimShip. A hull dedicated to a
// foreign fleet is rejected INSIDE that call's locked transaction — here the
// rejection must ride the same sp-cr86 terminal path as any other claim
// failure (row FAILED claim_failed, no stolen hull, no zombie runner).
func TestStartTerminalizesRowWhenOperationClaimIsRejected(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	pinnedShip := newIdleTradeShip(t, "SHIP-PINNED", playerID)
	repo := &tradeRouteShipRepo{
		ships:    map[string]*navigation.Ship{"SHIP-PINNED": pinnedShip},
		claimErr: shared.NewShipDedicatedToOtherFleetError("SHIP-PINNED", "contract", "trade"),
	}
	s.shipRepo = repo

	const containerID = "trade-route-SHIP-PINNED"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-PINNED", "operation": "trade"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "trade_route"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "a hull dedicated to another fleet must fail the operation claim")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	// The pinned hull must be untouched: not assigned by us, not force-released by
	// our cleanup, and no legacy read-modify-write may have slipped past the claim.
	require.False(t, pinnedShip.IsAssigned())
	require.Empty(t, repo.recordedClaims())
	require.Nil(t, s.registeredRunner(containerID))
}

// sp-l7h2 Phase 2 happy path: an operation-carrying container claims through
// ClaimShip under its fleet identity — the claim is recorded with the exact
// operation string, the hull ends up assigned, and the row lands RUNNING.
func TestStartClaimsUnderOperationWhenMetadataPresent(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	idleShip := newIdleTradeShip(t, "SHIP-TRADE", playerID)
	repo := &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-TRADE": idleShip}}
	s.shipRepo = repo

	const containerID = "trade-route-SHIP-TRADE"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-TRADE", "operation": "trade"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "trade_route"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err)
	requireContainerState(t, db, containerID, "RUNNING", "")
	claims := repo.recordedClaims()
	require.Len(t, claims, 1)
	require.Equal(t, tradeShipClaim{symbol: "SHIP-TRADE", containerID: containerID, operation: "trade"}, claims[0])
	require.True(t, idleShip.IsAssigned())
	require.Equal(t, containerID, idleShip.ContainerID())
}

// Regression guard: a normal claim+run must be entirely unaffected by the new
// claim-failure path - the row still lands RUNNING (not accidentally terminalized)
// and the ship ends up assigned to the new container.
func TestStartLeavesRowRunningWhenClaimSucceeds(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	idleShip := newIdleTradeShip(t, "SHIP-IDLE", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-IDLE": idleShip}}

	const containerID = "navigate-SHIP-IDLE"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-IDLE"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err)
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.True(t, idleShip.IsAssigned())
	require.Equal(t, containerID, idleShip.ContainerID())
}
