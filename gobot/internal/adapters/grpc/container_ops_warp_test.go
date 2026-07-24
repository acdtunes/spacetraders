package grpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipApp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The warp verb is the operator entry point to the off-gate warp executor. Like its
// jump/navigate/route siblings it is a daemon op (RULING #3 — the daemon is the only
// writer of ship state), so the CLI issues an RPC and the daemon spawns a tracked
// container that claims the hull and returns its id.
func TestWarpShipSpawnsATrackedContainerClaimingTheHull(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	hull := newIdleTradeShip(t, "TORWIND-F6", playerID)
	s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-F6": hull}}

	containerID, err := s.WarpShip(context.Background(), "TORWIND-F6", "X1-TY66-A1", playerID)
	require.NoError(t, err)
	if r := s.registeredRunner(containerID); r != nil {
		defer r.cancelFunc()
	}

	require.NotEmpty(t, containerID, "the operator gets a container id to follow with `container logs`")

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", containerID).Error)
	require.Equal(t, string(container.ContainerTypeWarp), model.ContainerType)
	require.Equal(t, "warp_ship", model.CommandType,
		"the persisted command type is what restart recovery rebuilds from (RULING #2)")
	require.Contains(t, model.Config, `"ship_symbol":"TORWIND-F6"`,
		"the hull is claimed through the container's ship_symbol metadata")
	require.Contains(t, model.Config, `"destination":"X1-TY66-A1"`)
	require.Contains(t, model.Config, `"captain_manual_authority":true`,
		"a deliberate captain CLI op may operate a fleet-dedicated hull (audited override)")
}

// strandingMediator answers a warp command with the executor's typed strand refusal,
// standing in for a real warp leg the fuel-safety guard turns away.
type strandingMediator struct{}

func (strandingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*shipNav.WarpShipCommand)
	if !ok {
		return nil, nil
	}
	return nil, &shipApp.ErrWarpWouldStrand{
		ShipSymbol: cmd.ShipSymbol,
		From:       "X1-YY85-ZD2D",
		To:         cmd.Destination,
		Required:   900,
		Available:  800,
		Capacity:   800,
		Reason:     "leg costs more than a full tank",
	}
}

func (strandingMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (strandingMediator) RegisterMiddleware(common.Middleware)               {}

// releasableShipRepo adds the CAS release the runner performs when a container exits,
// which the trade double leaves to its embedded interface.
type releasableShipRepo struct {
	*tradeRouteShipRepo
}

func (r *releasableShipRepo) SaveWithRetry(_ context.Context, symbol string, _ shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	r.mu.Lock()
	hull := r.ships[symbol]
	r.mu.Unlock()
	if hull == nil {
		return nil, false, nil
	}
	changed, err := mutate(hull)
	return hull, changed, err
}

// The diagnostic value of the fail-closed refusals is only real if an operator can
// READ them. A refused warp surfaces the strand's numbers — required, available,
// capacity and reason — verbatim in the container log the verb points at, never
// collapsed into a generic failure.
func TestRefusedWarpReportsTheStrandNumbersInTheContainerLog(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	hull := newIdleTradeShip(t, "TORWIND-F6", playerID)
	shipRepo := &releasableShipRepo{&tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-F6": hull}}}
	logs := &capturingLogRepo{}

	const containerID = "warp-TORWIND-F6"
	entity := container.NewContainer(containerID, container.ContainerTypeWarp, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-F6", "destination": "X1-TY66-A1", captainManualAuthorityKey: true}, nil)
	require.NoError(t, s.containerRepo.Add(context.Background(), entity, "warp_ship"))

	cmd := &shipNav.WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(playerID),
	}
	runner := NewContainerRunner(entity, strandingMediator{}, cmd, logs, s.containerRepo, shipRepo, s.clock)
	defer runner.cancelFunc()
	require.NoError(t, runner.Start())

	// The runner adds up to 5s of startup jitter before the first iteration.
	line := logs.waitForLog(t, "ERROR", "would strand", 20*time.Second)
	require.Contains(t, line.message, "required 900 fuel, available 800, capacity 800",
		"the operator reads exactly how short the tank was")
	require.Contains(t, line.message, "leg costs more than a full tank",
		"and why the leg was refused")
}

// RULING #2 — a daemon restart mid-warp must re-adopt the op, not orphan the hull.
// The persisted launch config alone must rebuild the exact command.
func TestWarpShipCommandRebuildsFromPersistedConfigOnRestart(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	rebuilt, err := s.buildCommandForType("warp_ship", jsonRoundTrip(t, map[string]interface{}{
		"ship_symbol": "TORWIND-F6",
		"destination": "X1-TY66-A1",
	}), playerID, "warp-TORWIND-F6")
	require.NoError(t, err)

	cmd, ok := rebuilt.(*shipNav.WarpShipCommand)
	require.True(t, ok, "recovery rebuilds a WarpShipCommand")
	require.Equal(t, "TORWIND-F6", cmd.ShipSymbol)
	require.Equal(t, "X1-TY66-A1", cmd.Destination)
	require.Equal(t, playerID, cmd.PlayerID.Value())
}

// Adding warp must not disturb its siblings: jump, navigate and route keep their own
// container types and command types, each still rebuildable from its own config.
func TestWarpAdditionLeavesSiblingShipVerbsUntouched(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	siblings := []struct {
		name          string
		call          func() (string, error)
		containerType container.ContainerType
		commandType   string
	}{
		{"navigate", func() (string, error) {
			return s.NavigateShip(context.Background(), "TORWIND-F6", "X1-YY85-A1", playerID)
		}, container.ContainerTypeNavigate, "navigate_ship"},
		{"route", func() (string, error) {
			return s.RouteShip(context.Background(), "TORWIND-F6", "X1-JP61-B1", playerID)
		}, container.ContainerTypeRoute, "route_ship"},
	}

	for _, sibling := range siblings {
		t.Run(sibling.name, func(t *testing.T) {
			hull := newIdleTradeShip(t, "TORWIND-F6", playerID)
			s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{"TORWIND-F6": hull}}

			containerID, err := sibling.call()
			require.NoError(t, err)
			if r := s.registeredRunner(containerID); r != nil {
				defer r.cancelFunc()
			}

			var model persistence.ContainerModel
			require.NoError(t, db.First(&model, "id = ?", containerID).Error)
			require.Equal(t, string(sibling.containerType), model.ContainerType)
			require.Equal(t, sibling.commandType, model.CommandType)
		})
	}

	// `ship jump` stays a synchronous mediator op — it registers no container command
	// type, and warp must not have converted it into one.
	_, err := s.buildCommandForType("jump_ship", map[string]interface{}{}, playerID, "jump-TORWIND-F6")
	require.Error(t, err, "jump remains a synchronous mediator op, not a container command")
}
