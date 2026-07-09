package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tradeRouteShipRepo is an in-memory ship repository for the trade-route container
// tests: FindBySymbol serves the hull, Save persists it, and FindByContainer supports
// the ContainerRunner's release path. It lets a test drive DaemonServer.StartTradeRoute
// and recovery without the live ship repo.
type tradeRouteShipRepo struct {
	navigation.ShipRepository
	mu    sync.Mutex
	ships map[string]*navigation.Ship
}

func (r *tradeRouteShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ships[symbol], nil
}

func (r *tradeRouteShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ships[ship.ShipSymbol()] = ship
	return nil
}

func (r *tradeRouteShipRepo) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
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

func newIdleTradeShip(t *testing.T, symbol string, playerID int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint("X1-TR-EXPORT", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	return ship
}

// The idle-gap discipline: trade-route only takes a genuinely idle hull. A hull the
// daemon is actively flying (assigned to another container) must be refused BEFORE any
// container is persisted, so a refused start has no side effects. This moved from the
// old CLI claimShip refusal to the container-start boundary (sp-zewt).
func TestStartTradeRoute_RefusesNonIdleShip(t *testing.T) {
	ship := newIdleTradeShip(t, "TRADER-BUSY", 1)
	require.NoError(t, ship.AssignToContainer("goods_factory-OTHER", shared.NewRealClock()))

	s := &DaemonServer{
		shipRepo:       &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TRADER-BUSY": ship}},
		containers:     make(map[string]*ContainerRunner),
		containerSpecs: make(map[string]ContainerSpec),
	}
	s.registerContainerSpecs()

	result, err := s.StartTradeRoute(context.Background(), "TRADER-BUSY", "X1-TR", 0, 1, "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "not idle")
	// No container may be registered for a refused start.
	require.Empty(t, s.containers)
	// The other coordinator's claim must be untouched.
	require.Equal(t, "goods_factory-OTHER", ship.ContainerID())
}

// An idle hull must produce a recovery-visible trade_route container: persisted with
// the trade_route command_type and a config carrying ship_symbol/system_symbol so
// restart recovery can rebuild it, and driven by a live runner (which claims the hull
// and flips the row to RUNNING). This is the recovery-visibility that retires the vjwb
// PENDING-orphan — the CLI runner's claim lived in a PENDING row no runner ever owned,
// so recovery could not see it. The RUNNING transition and adoption on restart are
// asserted by TestRecoveryAdoptsRunningTradeRouteContainer (the runner's async status
// write lands on a different pooled :memory: connection than this goroutine can read).
func TestStartTradeRoute_IdleShip_PersistsRecoveryVisibleContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "TRADER-1", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TRADER-1": ship}}

	result, err := s.StartTradeRoute(context.Background(), "TRADER-1", "X1-TR", 20, playerID, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.ContainerID)
	require.Equal(t, "TRADER-1", result.ShipSymbol)
	require.Equal(t, "X1-TR", result.SystemSymbol)

	runner := s.registeredRunner(result.ContainerID)
	require.NotNil(t, runner, "a live runner must own the trade-route circuit (release-on-death)")
	defer runner.cancelFunc()

	// The persisted row must be rebuildable by recovery: trade_route command_type,
	// TRADING container_type, and a config with the circuit inputs. (Persisted
	// synchronously by StartTradeRoute on this goroutine, so it is readable here.)
	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
	require.Equal(t, "trade_route", model.CommandType)
	require.Equal(t, "TRADING", model.ContainerType)
	require.Contains(t, model.Config, "TRADER-1")
	require.Contains(t, model.Config, "X1-TR")
	require.Contains(t, model.Config, "max_visits")
}

// Recovery must ADOPT a RUNNING trade_route container as a top-level coordinator (not
// skip it as a worker, not leave it orphaned): rebuild the circuit command from the
// launch config, re-assign the idle hull, and start a live runner. This is the daemon
// restart guarantee the CLI runner never had (sp-zewt / sp-vjwb).
func TestRecoveryAdoptsRunningTradeRouteContainer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	ship := newIdleTradeShip(t, "TRADER-2", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TRADER-2": ship}}

	insertRunningContainer(t, db, "trade-rec-1", "trade_route", "TRADING",
		`{"ship_symbol":"TRADER-2","system_symbol":"X1-TR","container_id":"trade-rec-1"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	runner := s.registeredRunner("trade-rec-1")
	require.NotNil(t, runner, "a RUNNING trade_route container must be adopted by recovery, not skipped")
	defer runner.cancelFunc()
	requireContainerState(t, db, "trade-rec-1", "RUNNING", "")
	// The hull must be re-assigned to the recovered container (not left idle/stranded).
	require.True(t, ship.IsAssigned())
	require.Equal(t, "trade-rec-1", ship.ContainerID())
}
