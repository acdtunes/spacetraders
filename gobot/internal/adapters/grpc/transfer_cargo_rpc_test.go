package grpc

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	appMediator "github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

const transferTestPlayerID = 1

// recordingMediator captures the request the RPC dispatched, so a test can assert
// the operator's arguments reached the EXISTING gas command unchanged.
type recordingMediator struct {
	sent common.Request
}

func (m *recordingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sent = request
	cmd, ok := request.(*gasCmd.TransferCargoCommand)
	if !ok {
		return nil, fmt.Errorf("unexpected request type %T", request)
	}
	return &gasCmd.TransferCargoResponse{UnitsTransferred: cmd.Units}, nil
}

func (m *recordingMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *recordingMediator) RegisterMiddleware(common.Middleware)               {}

// transferRPCFakeAPI stands in for the SpaceTraders API at the driven-port boundary.
// Only the calls the transfer seam makes are implemented; transferErr injects a
// server-side rejection (e.g. a full receiving hold).
type transferRPCFakeAPI struct {
	domainPorts.APIClient
	nav         map[string]string
	transfers   []transferRPCCall
	transferErr error
}

type transferRPCCall struct {
	from, to, good string
	units          int
}

func (f *transferRPCFakeAPI) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	return &navigation.ShipData{Symbol: symbol, NavStatus: f.nav[symbol]}, nil
}

func (f *transferRPCFakeAPI) OrbitShip(_ context.Context, symbol, _ string) error {
	f.nav[symbol] = string(navigation.NavStatusInOrbit)
	return nil
}

func (f *transferRPCFakeAPI) DockShip(_ context.Context, symbol, _ string) error {
	f.nav[symbol] = string(navigation.NavStatusDocked)
	return nil
}

func (f *transferRPCFakeAPI) TransferCargo(_ context.Context, from, to, good string, units int, _ string) (*domainPorts.TransferResult, error) {
	f.transfers = append(f.transfers, transferRPCCall{from: from, to: to, good: good, units: units})
	if f.transferErr != nil {
		return nil, f.transferErr
	}
	return &domainPorts.TransferResult{
		FromShip: from, ToShip: to, GoodSymbol: good, UnitsTransferred: units,
		RemainingCargo: &navigation.CargoData{Capacity: 40, Units: 0},
	}, nil
}

// transferRPCShipRepo serves the two hulls by symbol and applies persistence
// mutations in memory, mirroring the real repository's non-conflict path.
type transferRPCShipRepo struct {
	navigation.ShipRepository
	ships map[string]*navigation.Ship
}

func (r *transferRPCShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	ship, ok := r.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship %s not found", symbol)
	}
	return ship, nil
}

func (r *transferRPCShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

func (r *transferRPCShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	ship, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(ship)
	return ship, changed, err
}

// transferRPCPlayerRepo backs the production token middleware, so the tests exercise
// the same PlayerID -> token resolution the daemon performs.
type transferRPCPlayerRepo struct {
	player.PlayerRepository
}

func (transferRPCPlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	return player.NewPlayer(id, "TORWIND", "tok"), nil
}

func buildTransferRPCShip(t *testing.T, symbol, waypoint string, nav navigation.NavStatus, good string, units, capacity int) *navigation.Ship {
	t.Helper()
	wp, err := shared.NewWaypoint(waypoint, 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem(good, good, "desc", units)
		require.NoError(t, err)
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(capacity, units, inventory)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(transferTestPlayerID), wp, fuel, 100, capacity, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, nav)
	require.NoError(t, err)
	return ship
}

// newTransferRPCService wires the RPC to the REAL registered gas handler over fake
// infrastructure — the production path end to end, so a refusal in the handler or at
// the API surfaces exactly as an operator would see it.
func newTransferRPCService(t *testing.T, api *transferRPCFakeAPI, ships map[string]*navigation.Ship) *daemonServiceImpl {
	t.Helper()
	med := appMediator.NewMediator()
	med.RegisterMiddleware(auth.PlayerTokenMiddleware(transferRPCPlayerRepo{}))
	require.NoError(t, appMediator.RegisterHandler[*gasCmd.TransferCargoCommand](
		med, gasCmd.NewTransferCargoHandler(&transferRPCShipRepo{ships: ships}, api)))

	return newDaemonServiceImpl(&DaemonServer{mediator: med})
}

// The verb is a thin operator shell: the operator's four arguments must land on the
// EXISTING gas TransferCargoCommand — the same command type the siphon, stocker and
// tour deposits already dispatch — with no duplicated transfer logic in between.
func TestTransferCargoRPCDelegatesTheOperatorsArgumentsToTheExistingGasCommand(t *testing.T) {
	med := &recordingMediator{}
	service := newDaemonServiceImpl(&DaemonServer{mediator: med})

	resp, err := service.TransferCargo(context.Background(), &pb.TransferCargoRequest{
		FromShipSymbol: "TORWIND-F6",
		ToShipSymbol:   "TORWIND-2",
		GoodSymbol:     "MODULE_WARP_DRIVE_I",
		Units:          1,
		PlayerId:       transferTestPlayerID,
	})

	require.NoError(t, err)
	cmd, ok := med.sent.(*gasCmd.TransferCargoCommand)
	require.True(t, ok, "the RPC dispatches the existing gas TransferCargoCommand, not a new one")
	require.Equal(t, "TORWIND-F6", cmd.FromShip)
	require.Equal(t, "TORWIND-2", cmd.ToShip)
	require.Equal(t, "MODULE_WARP_DRIVE_I", cmd.GoodSymbol)
	require.Equal(t, 1, cmd.Units)
	require.Equal(t, transferTestPlayerID, cmd.PlayerID.Value(),
		"the resolved player rides on the command so the token middleware can authenticate it")
	require.True(t, resp.Success)
	require.Equal(t, int32(1), resp.UnitsTransferred)
	require.Empty(t, resp.Error)
}

// The move an operator actually wants: a module lifted out of one hull's cargo lands
// in another's. Asserted at the driven port — the API received exactly the requested
// (from, to, good, units).
func TestTransferCargoRPCMovesTheGoodBetweenCoLocatedHulls(t *testing.T) {
	api := &transferRPCFakeAPI{nav: map[string]string{
		"TORWIND-F6": string(navigation.NavStatusInOrbit),
		"TORWIND-2":  string(navigation.NavStatusInOrbit),
	}}
	service := newTransferRPCService(t, api, map[string]*navigation.Ship{
		"TORWIND-F6": buildTransferRPCShip(t, "TORWIND-F6", "X1-YY85-ZD2D", navigation.NavStatusInOrbit, "MODULE_WARP_DRIVE_I", 1, 40),
		"TORWIND-2":  buildTransferRPCShip(t, "TORWIND-2", "X1-YY85-ZD2D", navigation.NavStatusInOrbit, "", 0, 40),
	})

	resp, err := service.TransferCargo(context.Background(), &pb.TransferCargoRequest{
		FromShipSymbol: "TORWIND-F6",
		ToShipSymbol:   "TORWIND-2",
		GoodSymbol:     "MODULE_WARP_DRIVE_I",
		Units:          1,
		PlayerId:       transferTestPlayerID,
	})

	require.NoError(t, err)
	require.Empty(t, resp.Error)
	require.True(t, resp.Success)
	require.Equal(t, []transferRPCCall{{from: "TORWIND-F6", to: "TORWIND-2", good: "MODULE_WARP_DRIVE_I", units: 1}}, api.transfers)
	require.Equal(t, int32(1), resp.UnitsTransferred)
	require.Equal(t, "TORWIND-F6", resp.FromShipSymbol)
	require.Equal(t, "TORWIND-2", resp.ToShipSymbol)
	require.Equal(t, "MODULE_WARP_DRIVE_I", resp.GoodSymbol)
}

// First failure mode an operator hits: the hulls are not parked together. The refusal
// must name the condition AND both waypoints, otherwise the operator cannot tell which
// hull to move — and nothing may be attempted at the API.
func TestTransferCargoRPCRefusesHullsAtDifferentWaypointsNamingBothLocations(t *testing.T) {
	api := &transferRPCFakeAPI{nav: map[string]string{
		"TORWIND-F6": string(navigation.NavStatusInOrbit),
		"TORWIND-2":  string(navigation.NavStatusInOrbit),
	}}
	service := newTransferRPCService(t, api, map[string]*navigation.Ship{
		"TORWIND-F6": buildTransferRPCShip(t, "TORWIND-F6", "X1-YY85-ZD2D", navigation.NavStatusInOrbit, "MODULE_WARP_DRIVE_I", 1, 40),
		"TORWIND-2":  buildTransferRPCShip(t, "TORWIND-2", "X1-YY85-A1", navigation.NavStatusInOrbit, "", 0, 40),
	})

	resp, err := service.TransferCargo(context.Background(), &pb.TransferCargoRequest{
		FromShipSymbol: "TORWIND-F6",
		ToShipSymbol:   "TORWIND-2",
		GoodSymbol:     "MODULE_WARP_DRIVE_I",
		Units:          1,
		PlayerId:       transferTestPlayerID,
	})

	require.NoError(t, err, "a refusal is reported in the response, not as a transport failure")
	require.False(t, resp.Success)
	require.Empty(t, api.transfers, "the refusal comes before the API is touched")
	require.Contains(t, resp.Error, "same location", "the refusal names the condition that tripped")
	require.Contains(t, resp.Error, "X1-YY85-ZD2D", "and where the source hull is")
	require.Contains(t, resp.Error, "X1-YY85-A1", "and where the receiver is")
}

// Second failure mode: the receiving hull has no room. The server's own words carry
// the units and capacity, so they must reach the operator VERBATIM — a generic
// "transfer failed" would hide which of the two conditions tripped.
func TestTransferCargoRPCSurfacesAFullReceivingHoldVerbatim(t *testing.T) {
	const apiRefusal = "API error 4217: Failed to transfer 40 unit(s) of ALUMINUM. Ship TORWIND-2 has capacity 40 and 40 units in cargo."
	api := &transferRPCFakeAPI{
		nav: map[string]string{
			"TORWIND-F6": string(navigation.NavStatusInOrbit),
			"TORWIND-2":  string(navigation.NavStatusInOrbit),
		},
		transferErr: fmt.Errorf("%s", apiRefusal),
	}
	service := newTransferRPCService(t, api, map[string]*navigation.Ship{
		"TORWIND-F6": buildTransferRPCShip(t, "TORWIND-F6", "X1-YY85-ZD2D", navigation.NavStatusInOrbit, "ALUMINUM", 40, 40),
		"TORWIND-2":  buildTransferRPCShip(t, "TORWIND-2", "X1-YY85-ZD2D", navigation.NavStatusInOrbit, "ALUMINUM", 40, 40),
	})

	resp, err := service.TransferCargo(context.Background(), &pb.TransferCargoRequest{
		FromShipSymbol: "TORWIND-F6",
		ToShipSymbol:   "TORWIND-2",
		GoodSymbol:     "ALUMINUM",
		Units:          40,
		PlayerId:       transferTestPlayerID,
	})

	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.Error, apiRefusal,
		"the server's own capacity refusal reaches the operator whole, not flattened")
	require.Zero(t, resp.UnitsTransferred)
}

// A transfer is instantaneous — nothing to track, nothing to re-adopt after a restart.
// It therefore stays a synchronous mediator op like `ship jump` and the outfit verbs it
// sits between, and must NOT have acquired a container command type.
func TestTransferCargoStaysASynchronousMediatorOpNotAContainer(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	_, err := s.buildCommandForType("transfer_cargo", map[string]interface{}{}, playerID, "transfer-TORWIND-F6")

	// Asserted on the "unknown command type" wording specifically: a bare require.Error
	// would also be satisfied by a REGISTERED spec that merely rejected the empty config,
	// which is the opposite of what this pins.
	require.ErrorContains(t, err, "unknown command type",
		"transfer registers no container command type")
}
