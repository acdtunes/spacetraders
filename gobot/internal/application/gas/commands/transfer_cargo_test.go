package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// transferFakeAPI overrides only the four methods the deposit seam uses; any other
// call panics, keeping the fake honest about what the handler actually touches.
type transferFakeAPI struct {
	domainPorts.APIClient
	nav        map[string]string
	orbitCalls []string
	dockCalls  []string
	transfers  int
}

func (f *transferFakeAPI) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	return &navigation.ShipData{Symbol: symbol, NavStatus: f.nav[symbol]}, nil
}
func (f *transferFakeAPI) OrbitShip(_ context.Context, symbol, _ string) error {
	f.orbitCalls = append(f.orbitCalls, symbol)
	f.nav[symbol] = string(navigation.NavStatusInOrbit)
	return nil
}
func (f *transferFakeAPI) DockShip(_ context.Context, symbol, _ string) error {
	f.dockCalls = append(f.dockCalls, symbol)
	f.nav[symbol] = string(navigation.NavStatusDocked)
	return nil
}
func (f *transferFakeAPI) TransferCargo(_ context.Context, from, to, good string, units int, _ string) (*domainPorts.TransferResult, error) {
	f.transfers++
	return &domainPorts.TransferResult{FromShip: from, ToShip: to, GoodSymbol: good, UnitsTransferred: units}, nil
}

// transferFakeRepo returns ships by symbol and records the last Save of each.
type transferFakeRepo struct {
	navigation.ShipRepository
	ships map[string]*navigation.Ship
	saved map[string]*navigation.Ship
}

func (r *transferFakeRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ships[symbol], nil
}
func (r *transferFakeRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if r.saved == nil {
		r.saved = map[string]*navigation.Ship{}
	}
	r.saved[ship.ShipSymbol()] = ship
	return nil
}

func buildTransferShip(t *testing.T, symbol, waypoint string, nav navigation.NavStatus, good string, units, capacity int) *navigation.Ship {
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
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 100, capacity, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, nav)
	require.NoError(t, err)
	return ship
}

// The sp-5qs1 incident at the handler seam: the stocker (visitor) arrives DOCKED,
// the warehouse hull parks IN_ORBIT. The deposit must orbit the visitor to match the
// warehouse, transfer, and persist the visitor's reconciled (in-orbit) nav state.
func TestTransferCargoHandler_Deposit_VisitorDockedWarehouseOrbit_OrbitsAndTransfers(t *testing.T) {
	visitor := buildTransferShip(t, "VISITOR", "X1-HOME-E42", navigation.NavStatusDocked, "FOOD", 80, 100)
	warehouse := buildTransferShip(t, "WAREHOUSE", "X1-HOME-E42", navigation.NavStatusInOrbit, "", 0, 1000)

	api := &transferFakeAPI{nav: map[string]string{
		"VISITOR":   string(navigation.NavStatusDocked),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	}}
	repo := &transferFakeRepo{ships: map[string]*navigation.Ship{"VISITOR": visitor, "WAREHOUSE": warehouse}}
	handler := NewTransferCargoHandler(repo, api)

	ctx := common.WithPlayerToken(context.Background(), "tok")
	resp, err := handler.Handle(ctx, &TransferCargoCommand{
		FromShip:   "VISITOR",
		ToShip:     "WAREHOUSE",
		GoodSymbol: "FOOD",
		Units:      80,
		PlayerID:   shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Equal(t, []string{"VISITOR"}, api.orbitCalls, "visitor orbited to match the in-orbit warehouse")
	require.Empty(t, api.dockCalls, "the warehouse hull is never moved")
	require.Equal(t, 1, api.transfers)
	transferResp, ok := resp.(*TransferCargoResponse)
	require.True(t, ok)
	require.Equal(t, 80, transferResp.UnitsTransferred)
	require.Equal(t, navigation.NavStatusInOrbit, repo.saved["VISITOR"].NavStatus(), "the aligned nav state is persisted, not the pre-deposit DOCKED")
}

// A docked warehouse docks the visitor (proves align-to-warehouse, not hardcoded orbit).
func TestTransferCargoHandler_Deposit_VisitorOrbitWarehouseDocked_DocksVisitor(t *testing.T) {
	visitor := buildTransferShip(t, "VISITOR", "X1-HOME-E42", navigation.NavStatusInOrbit, "FOOD", 80, 100)
	warehouse := buildTransferShip(t, "WAREHOUSE", "X1-HOME-E42", navigation.NavStatusDocked, "", 0, 1000)

	api := &transferFakeAPI{nav: map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusDocked),
	}}
	repo := &transferFakeRepo{ships: map[string]*navigation.Ship{"VISITOR": visitor, "WAREHOUSE": warehouse}}
	handler := NewTransferCargoHandler(repo, api)

	ctx := common.WithPlayerToken(context.Background(), "tok")
	_, err := handler.Handle(ctx, &TransferCargoCommand{
		FromShip: "VISITOR", ToShip: "WAREHOUSE", GoodSymbol: "FOOD", Units: 80, PlayerID: shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Equal(t, []string{"VISITOR"}, api.dockCalls, "visitor docked to match the docked warehouse")
	require.Empty(t, api.orbitCalls)
	require.Equal(t, navigation.NavStatusDocked, repo.saved["VISITOR"].NavStatus())
}
