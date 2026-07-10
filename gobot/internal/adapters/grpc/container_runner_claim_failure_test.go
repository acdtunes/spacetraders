package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

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

	// SHIP-BUSY is already flying for a different container that never releases it,
	// so the claim can never succeed. sp-ku8e retries the transient handoff race
	// briefly first, but once the bounded budget is exhausted the row must still
	// terminalize (FAILED) exactly as sp-cr86 requires — not linger at RUNNING.
	busyShip := newIdleTradeShip(t, "SHIP-BUSY", playerID)
	require.NoError(t, busyShip.AssignToContainer("orbit-SHIP-BUSY", shared.NewRealClock()))
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-BUSY": busyShip}}

	const containerID = "navigate-SHIP-BUSY"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-BUSY"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	// MockClock makes the sp-ku8e retry backoff instant.
	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, claimTestClock())

	err := runner.Start()

	require.Error(t, err, "a ship held by another container for the whole retry budget must still fail Start()")
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
	// sp-ku8e: a dedication rejection is permanent — it must fail fast on the first
	// attempt, never enter the transient handoff-race retry loop.
	require.Equal(t, 1, repo.claimCallCount(), "a dedication rejection must not be retried")
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

// sp-sg35: the legacy (non-operation) claim path must enforce the SAME
// fleet-dedication guard as the atomic ClaimShip, closing the operation-key side
// door — a container that never stamped an "operation" key (here a captain
// navigate) must NOT be able to claim a hull pinned to a foreign fleet through
// the legacy FindBySymbol+AssignToContainer+Save fallback. The rejection is the
// standing ShipDedicatedToOtherFleetError, so it rides the same sp-cr86 terminal
// path as any other permanent claim failure (row FAILED, hull untouched, no
// zombie runner) and fails fast without entering the transient handoff retry.
func TestStartFailsFastWhenLegacyClaimHitsForeignDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	// SHIP-PINNED is dedicated to the "contract" fleet; the navigate container
	// below carries no "operation" key, so it takes the legacy claim path.
	pinned := newIdleTradeShip(t, "SHIP-PINNED", playerID)
	pinned.SetDedicatedFleet("contract")
	repo := &handoffRaceShipRepo{held: pinned, released: pinned, releaseAfterFinds: claimRetryMaxAttempts + 5}
	s.shipRepo = repo

	const containerID = "navigate-SHIP-PINNED"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-PINNED"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, claimTestClock())

	err := runner.Start()

	require.Error(t, err, "the legacy path must reject a foreign-fleet-dedicated hull")
	var dedErr *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedErr, "must be the standing dedication rejection")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.Equal(t, 1, repo.findCount(), "a dedication rejection is permanent — it must not be retried")
	require.False(t, pinned.IsAssigned(), "the pinned hull must not be claimed through the legacy side door")
	require.Nil(t, s.registeredRunner(containerID))
}

// sp-sg35 regression: the new legacy-path dedication guard must leave an
// UNDEDICATED hull entirely claimable — the common case (most hulls, and every
// captain manual op on a general-pool hull) is unaffected. A navigate container
// (no "operation" key) claiming a DedicatedFleet=="" hull still lands RUNNING
// with the hull assigned.
func TestStartLegacyClaimStillClaimsUndedicatedHull(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	idle := newIdleTradeShip(t, "SHIP-FREE", playerID) // DedicatedFleet == "" by construction
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"SHIP-FREE": idle}}

	const containerID = "navigate-SHIP-FREE"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-FREE"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "an undedicated hull must remain claimable on the legacy path")
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.True(t, idle.IsAssigned())
	require.Equal(t, containerID, idle.ContainerID())
}

// claimTestClock returns a MockClock whose Sleep advances virtual time instantly,
// so the sp-ku8e claim-retry backoff adds no real delay to these tests while the
// production RealClock still sleeps for real.
func claimTestClock() *shared.MockClock {
	return &shared.MockClock{CurrentTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// handoffRaceShipRepo models the sp-ku8e claim-handoff race for the legacy
// (non-operation) assign path: FindBySymbol serves a hull still held by a
// just-finished container for the first releaseAfterFinds reads, then serves it
// released — exactly the sub-second window where navigate's claim outruns orbit's
// synchronous release. Set releaseAfterFinds beyond the retry budget to model a
// hull that never frees up. Save records the eventual assignment.
type handoffRaceShipRepo struct {
	navigation.ShipRepository
	mu                sync.Mutex
	held              *navigation.Ship // still assigned to the dying container
	released          *navigation.Ship // same hull, now idle
	releaseAfterFinds int
	finds             int
	saved             *navigation.Ship
}

func (r *handoffRaceShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finds++
	if r.finds <= r.releaseAfterFinds {
		return r.held, nil
	}
	return r.released, nil
}

func (r *handoffRaceShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = ship
	return nil
}

// FindByContainer backs the release path; these tests never leave a ship assigned
// to the runner's own container, so it has nothing to release.
func (r *handoffRaceShipRepo) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, nil
}

func (r *handoffRaceShipRepo) findCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finds
}

// sp-ku8e core acceptance: a captain CLI chain (orbit then navigate ~1s apart)
// races on the claim handoff — navigate's claim lands while orbit's synchronous
// release is still in flight, surfacing as a transient ShipAlreadyAssignedError.
// The claim must retry briefly and succeed once the release lands, WITHOUT a
// manual retry from the captain.
func TestStartRetriesTransientClaimHandoffRaceThenSucceeds(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	held := newIdleTradeShip(t, "SHIP-RACE", playerID)
	require.NoError(t, held.AssignToContainer("orbit-SHIP-RACE", shared.NewRealClock()))
	released := newIdleTradeShip(t, "SHIP-RACE", playerID)
	repo := &handoffRaceShipRepo{held: held, released: released, releaseAfterFinds: 1}
	s.shipRepo = repo

	const containerID = "navigate-SHIP-RACE"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-RACE"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, claimTestClock())
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "navigate must succeed once orbit's release lands, without a manual retry")
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.GreaterOrEqual(t, repo.findCount(), 2, "the claim must have been retried at least once")
	require.NotNil(t, repo.saved)
	require.True(t, repo.saved.IsAssigned())
	require.Equal(t, containerID, repo.saved.ContainerID())
}

// sp-ku8e / sp-l7h2: a captain reservation is a standing rejection, not a transient
// handoff race — assigning a captain-reserved hull to a container must fail fast on
// the FIRST attempt (no retry), and still terminalize the row like any claim failure.
func TestStartFailsFastWhenHullIsCaptainReserved(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	reserved := newIdleTradeShip(t, "SHIP-RESERVED", playerID)
	require.NoError(t, reserved.ReserveByCaptain("captain manual survey", shared.NewRealClock()))
	repo := &handoffRaceShipRepo{held: reserved, released: reserved, releaseAfterFinds: claimRetryMaxAttempts + 5}
	s.shipRepo = repo

	const containerID = "navigate-SHIP-RESERVED"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-RESERVED"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, claimTestClock())

	err := runner.Start()

	require.Error(t, err, "a captain-reserved hull must fail the claim")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.Equal(t, 1, repo.findCount(), "a captain reservation is permanent — it must not be retried")
	require.True(t, reserved.IsReservedByCaptain(), "the captain reservation must be untouched")
	require.Nil(t, s.registeredRunner(containerID))
}

// sp-ku8e: the transient-race retry must be BOUNDED — a hull held by a live
// container for the whole budget must make the claim give up after exactly
// claimRetryMaxAttempts attempts (no retry storm) and terminalize the row.
func TestStartStopsRetryingClaimAfterBoundedAttempts(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	held := newIdleTradeShip(t, "SHIP-STUCK", playerID)
	require.NoError(t, held.AssignToContainer("worker-SHIP-STUCK", shared.NewRealClock()))
	// releaseAfterFinds beyond the budget => the hull never frees up.
	repo := &handoffRaceShipRepo{held: held, released: held, releaseAfterFinds: claimRetryMaxAttempts + 5}
	s.shipRepo = repo

	const containerID = "navigate-SHIP-STUCK"
	entity := container.NewContainer(containerID, container.ContainerTypeNavigate, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-STUCK"}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "navigate_ship"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, claimTestClock())

	err := runner.Start()

	require.Error(t, err, "a hull held for the whole retry budget must fail Start()")
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.Equal(t, claimRetryMaxAttempts, repo.findCount(), "must try exactly the bounded number of attempts, then stop")
	require.Equal(t, "worker-SHIP-STUCK", held.ContainerID(), "the holder's claim must be untouched")
	require.Nil(t, s.registeredRunner(containerID))
}
