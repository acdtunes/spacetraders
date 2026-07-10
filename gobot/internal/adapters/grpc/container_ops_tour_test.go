package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// dedicationGuardShipRepo enforces ClaimShip's real fleet-dedication rule (permit
// iff operation == dedicated_fleet, or the hull is undedicated) so a test can
// prove an operation-keyed claim under the matching fleet identity is PERMITTED on
// a same-fleet-dedicated hull and REJECTED on a foreign one. FindBySymbol + Save
// back the legacy branch; ClaimShip backs the operation-keyed branch the tour now
// takes (sp-sg35).
type dedicationGuardShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *dedicationGuardShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *dedicationGuardShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	if r.ship.DedicatedFleet() != "" && r.ship.DedicatedFleet() != operation {
		return shared.NewShipDedicatedToOtherFleetError(symbol, r.ship.DedicatedFleet(), operation)
	}
	return r.ship.AssignToContainer(containerID, shared.NewRealClock())
}

func (r *dedicationGuardShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

func (r *dedicationGuardShipRepo) FindByContainer(_ context.Context, _ string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, nil
}

// sp-sg35 THE PREREQUISITE — this is the test that would have caught the
// fleet-killer. The tour heavies (TORWIND-19/2B/2C/2D/2E/37/39/42) are dedicated
// to the "trade" fleet, and tour_run claims through the ContainerRunner. If the
// launch config does not stamp the "trade" operation, the dedication guard (both
// the atomic ClaimShip and the sp-sg35 legacy-path guard) rejects a tour from
// claiming its OWN hull. StartTourRun must persist "operation":"trade" so both a
// fresh start and a recovery rebuild claim under operation == dedication.
func TestStartTourRun_StampsTradeOperationInLaunchConfig(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	// TORWIND-19 is a real tour heavy, dedicated to the "trade" fleet.
	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-19": hull}}

	result, err := s.StartTourRun(context.Background(), "TORWIND-19", 5, int64(100000), 10, 3, int64(0), "AGENT", 1, playerID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "TORWIND-19", result.ShipSymbol)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner, "a live runner must own the tour (release-on-death)")
	defer runner.cancelFunc()

	// The persisted row must be rebuildable by recovery under the trade identity.
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Equal(t, "tour_run", model.CommandType)
	// The fleet identity MUST ride the launch config as 'trade' (== the DB
	// dedication value) so a 'trade'-dedicated tour heavy is permitted, not rejected.
	require.Contains(t, model.Config, `"operation":"trade"`)
}

// sp-sg35 — the permit half: a tour_run-shaped container (operation "trade", the
// value StartTourRun now stamps) claiming a hull dedicated to the SAME "trade"
// fleet is permitted (operation == dedication), so the trade-fleet tours keep
// flying their own hulls.
func TestTourClaimPermittedOnMatchingTradeDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "TORWIND-19", playerID)
	hull.SetDedicatedFleet("trade")
	s.shipRepo = &dedicationGuardShipRepo{ship: hull}

	const containerID = "tour-run-TORWIND-19"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-19", "operation": operationTrade}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "tour_run"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	defer runner.cancelFunc()

	err := runner.Start()

	require.NoError(t, err, "a tour claiming its OWN 'trade'-dedicated hull under operation=trade must be permitted")
	requireContainerState(t, db, containerID, "RUNNING", "")
	require.True(t, hull.IsAssigned())
	require.Equal(t, containerID, hull.ContainerID())
}

// sp-sg35 — the reject half: even with the trade identity, a tour must NOT claim a
// hull dedicated to a FOREIGN fleet (here "contract"); the guard still rejects it,
// so the stamp is a scoped permission for the tour's own fleet, not a skeleton key.
func TestTourClaimRejectedOnForeignFleetDedication(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	hull := newIdleTradeShip(t, "SHIP-CONTRACT", playerID)
	hull.SetDedicatedFleet("contract")
	s.shipRepo = &dedicationGuardShipRepo{ship: hull}

	const containerID = "tour-run-SHIP-CONTRACT"
	entity := container.NewContainer(containerID, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "SHIP-CONTRACT", "operation": operationTrade}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "tour_run"))

	runner := NewContainerRunner(entity, s.mediator, nil, s.logRepo, s.containerRepo, s.shipRepo, s.clock)

	err := runner.Start()

	require.Error(t, err, "a trade tour must not claim a foreign 'contract'-dedicated hull")
	var dedErr *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedErr)
	requireContainerState(t, db, containerID, "FAILED", "claim_failed")
	require.False(t, hull.IsAssigned())
}
