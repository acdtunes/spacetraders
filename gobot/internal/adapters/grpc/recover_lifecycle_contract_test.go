package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipNavCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-7yej invariant 4 (universal restart re-adoption) and invariant 3 (unified
// iteration semantics) pin tests. Tonight's evidence: TORWIND-18/12's RUNNING
// navigate_ship containers hit "unknown command type" at restart recovery and
// were dropped (FAILED, flight abandoned), and finite scout tours diverged
// from infinite ones (0 = instant no-op; N double-looped as container-N ×
// command-N tours).

// recoveryLiveShipRepo is a working in-memory ship repo for POSITIVE
// re-adoption tests: recoverContainer must be able to load, reassign and save
// the hull (the stub recoveryStubShipRepo deliberately errors to keep the
// older negative tests deterministic).
type recoveryLiveShipRepo struct {
	navigation.ShipRepository
	mu    sync.Mutex
	ships map[string]*navigation.Ship
}

func (r *recoveryLiveShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ships[symbol], nil
}

func (r *recoveryLiveShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ships[ship.ShipSymbol()] = ship
	return nil
}

func (r *recoveryLiveShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*navigation.Ship
	for _, s := range r.ships {
		if s.IsAssigned() && s.ContainerID() == containerID {
			out = append(out, s)
		}
	}
	return out, nil
}

// withLiveShip swaps the recovery harness's erroring ship stub for a working
// repo holding one idle hull, so positive re-adoption paths can run end to end.
func withLiveShip(t *testing.T, s *DaemonServer, symbol string, playerID int) *recoveryLiveShipRepo {
	t.Helper()
	repo := &recoveryLiveShipRepo{ships: map[string]*navigation.Ship{}}
	if symbol != "" {
		repo.ships[symbol] = newIdleTradeShip(t, symbol, playerID)
	}
	s.shipRepo = repo
	return repo
}

// (4a) A RUNNING navigate_ship container must RE-ADOPT at restart — rebuilt
// from its persisted {ship_symbol, destination} config, its hull reassigned,
// its runner registered — instead of being marked FAILED "unknown command
// type" with the flight abandoned (the TORWIND-18/12 orphaning).
func TestRecoveryReadoptsNavigateShip(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	repo := withLiveShip(t, s, "TORWIND-18", playerID)
	insertRunningContainer(t, db, "navigate-TORWIND-18-abc", "navigate_ship", "NAVIGATE",
		`{"ship_symbol":"TORWIND-18","destination":"X1-TR-B10D"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("navigate-TORWIND-18-abc")
	require.NotNil(t, runner, "a mid-flight navigate must re-adopt, not orphan (sp-7yej invariant 4)")
	requireContainerState(t, db, "navigate-TORWIND-18-abc", "RUNNING", "")

	// The rebuilt command is the navigate itself, aimed at the persisted leg.
	nav, ok := runner.command.(*shipNavCmd.NavigateRouteCommand)
	require.True(t, ok, "expected a NavigateRouteCommand, got %T", runner.command)
	require.Equal(t, "TORWIND-18", nav.ShipSymbol)
	require.Equal(t, "X1-TR-B10D", nav.Destination)

	// The hull is claimed back by the recovered container, not left released.
	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-18", shared.MustNewPlayerID(playerID))
	require.NoError(t, err)
	require.True(t, ship.IsAssigned(), "recovered navigate must re-claim its hull")
	require.Equal(t, "navigate-TORWIND-18-abc", ship.ContainerID())

	runner.cancelFunc()
}

// (4b) Every one-shot ship op re-adopts at restart. Each rebuilds trivially
// from its config and is safe to re-run (dock/orbit/refuel no-op when already
// done; a re-run jettison of gone cargo fails honestly).
func TestRecoveryReadoptsOneShotShipOps(t *testing.T) {
	cases := []struct {
		commandType string
		config      string
	}{
		{"dock_ship", `{"ship_symbol":"SHIP-OP"}`},
		{"orbit_ship", `{"ship_symbol":"SHIP-OP"}`},
		{"refuel_ship", `{"ship_symbol":"SHIP-OP","units":40}`},
		{"jettison_cargo", `{"ship_symbol":"SHIP-OP","good_symbol":"ICE_WATER","units":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.commandType, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			withLiveShip(t, s, "SHIP-OP", playerID)
			id := "op-" + tc.commandType
			insertRunningContainer(t, db, id, tc.commandType, "SHIP_OP", tc.config, playerID, nil)

			require.NoError(t, s.RecoverRunningContainers(context.Background()))

			runner := s.registeredRunner(id)
			require.NotNil(t, runner, "%s must re-adopt at restart (sp-7yej invariant 4)", tc.commandType)
			requireContainerState(t, db, id, "RUNNING", "")
			runner.cancelFunc()
		})
	}
}

// (4c) scout_fleet_assignment (no hull claim) re-adopts and re-runs its VRP
// pass instead of vanishing.
func TestRecoveryReadoptsScoutFleetAssignment(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "scout-fleet-assignment-X1", "scout_fleet_assignment", "SCOUT_FLEET_ASSIGNMENT",
		`{"system_symbol":"X1-TEST"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("scout-fleet-assignment-X1")
	require.NotNil(t, runner)
	requireContainerState(t, db, "scout-fleet-assignment-X1", "RUNNING", "")
	cmd, ok := runner.command.(*scoutingCmd.AssignScoutingFleetCommand)
	require.True(t, ok, "expected AssignScoutingFleetCommand, got %T", runner.command)
	require.Equal(t, "X1-TEST", cmd.SystemSymbol)
	runner.cancelFunc()
}

// (3a) COORDINATOR-OWNED budgets are pinned to ONE runner iteration at
// recovery: a scout tour whose config says iterations=3 hands 3 to the
// COMMAND (the loop that owns it) and 1 to the container. Feeding 3 to both
// was the double-loop defect — a recovered (or freshly created) N-tour scout
// flew N runner iterations × N tours each.
func TestRecoveryPinsCoordinatorOwnedIterationsToOne(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	withLiveShip(t, s, "SCOUT-3", playerID)
	insertRunningContainer(t, db, "scout-tour-3", "scout_tour", "SCOUT",
		`{"ship_symbol":"SCOUT-3","markets":["X1-A","X1-B"],"iterations":3}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("scout-tour-3")
	require.NotNil(t, runner)
	require.Equal(t, 1, runner.Container().MaxIterations(),
		"a coordinator-owned budget must never reach the runner loop (sp-7yej invariant 3)")
	cmd, ok := runner.command.(*scoutingCmd.ScoutTourCommand)
	require.True(t, ok, "expected ScoutTourCommand, got %T", runner.command)
	require.Equal(t, 3, cmd.Iterations, "the tour budget belongs to the command")
	runner.cancelFunc()
}

// (3b) iterations=0 means the type's DEFAULT (one tour), never "zero work".
// Before this, a 0-iteration scout container completed instantly without
// scouting anything — the "0 tours vanished" half of tonight's divergence.
func TestScoutTourBuilder_ZeroIterationsMeansDefaultSingleTour(t *testing.T) {
	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()

	cmd, err := s.buildCommandForType("scout_tour", map[string]interface{}{
		"ship_symbol": "SCOUT-0",
		"markets":     []interface{}{"X1-A"},
		"iterations":  float64(0), // JSON round-trip shape
	}, 1, "scout-tour-0")
	require.NoError(t, err)

	tour, ok := cmd.(*scoutingCmd.ScoutTourCommand)
	require.True(t, ok)
	require.Equal(t, 1, tour.Iterations,
		"0 must normalize to the documented default of one tour (sp-7yej invariant 3)")

	// -1 (infinite) passes through untouched.
	cmd, err = s.buildCommandForType("scout_tour", map[string]interface{}{
		"ship_symbol": "SCOUT-INF",
		"markets":     []interface{}{"X1-A"},
		"iterations":  float64(-1),
	}, 1, "scout-tour-inf")
	require.NoError(t, err)
	require.Equal(t, -1, cmd.(*scoutingCmd.ScoutTourCommand).Iterations)
}

// (4d) The registry is the orphan firewall: every container command type the
// daemon persists (containerRepo.Add call sites) must be either registered
// with a builder or a declared worker — an unknown type at recovery means an
// abandoned operation. This list is maintained by hand; when adding a new
// container_ops_* creation site, add its type to the registry AND here.
func TestEveryPersistedCommandTypeIsRegistered(t *testing.T) {
	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()

	created := []string{
		// coordinators / one-shots
		"scout_tour", "contract_workflow", "contract_fleet_coordinator",
		"purchase_ship", "batch_purchase_ships", "goods_factory_coordinator",
		"manufacturing_coordinator", "gas_coordinator", "trade_route", "arb_run", "tour_run",
		"navigate_ship", "dock_ship", "orbit_ship", "refuel_ship",
		"jettison_cargo", "scout_fleet_assignment",
		// workers (recovered via their coordinator, never standalone)
		"manufacturing_task_worker", "gas_siphon_worker", "storage_ship",
	}
	for _, commandType := range created {
		spec, ok := s.containerSpecs[commandType]
		require.True(t, ok,
			"command type %q is created by a container_ops site but not registered — it will orphan at restart (sp-7yej invariant 4)", commandType)
		if !spec.IsWorker {
			require.NotNil(t, spec.build, "non-worker type %q must have a recovery builder", commandType)
		}
	}
}
