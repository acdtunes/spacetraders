package ship_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// --- Test doubles ---------------------------------------------------------

// countingShipyardAPI is the driven-port stub at the ONLY external boundary the
// scan crosses: the live shipyard read. gets records the call count so the test
// proves the scanner's trait gate actually opened and a real scan proceeded on
// the route path — not that a row arrived by some other route.
type countingShipyardAPI struct {
	data *domainPorts.ShipyardData
	gets int
}

func (s *countingShipyardAPI) GetShipyard(context.Context, string, string, string) (*domainPorts.ShipyardData, error) {
	s.gets++
	return s.data, nil
}

// succeedingMediator is a minimal common.Mediator that satisfies every atomic
// command RouteExecutor.executeSegment issues on a happy single-leg arrival
// (orbit, pre-departure/​post-arrival refuel, set flight mode, navigate). Navigate
// returns an empty ArrivalTime so the executor skips the event-based arrival wait
// (no subscriber traffic), and reports a full tank so no leg is rejected with the
// API's 4203 insufficient-fuel error — the same shape route_executor_test.go's
// recordingMediator relies on.
type succeedingMediator struct {
	fuelCapacity int
}

func (m *succeedingMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch cmd := request.(type) {
	case *types.OrbitShipCommand:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		return &types.RefuelShipResponse{Status: "refueled", CurrentFuel: m.fuelCapacity, FuelCapacity: m.fuelCapacity}, nil
	case *types.SetFlightModeCommand:
		return &types.SetFlightModeResponse{Status: "set", Mode: cmd.Mode}, nil
	case *types.NavigateDirectCommand:
		return &types.NavigateDirectResponse{Status: "navigating", FuelCurrent: m.fuelCapacity, FuelCapacity: m.fuelCapacity}, nil
	default:
		return nil, fmt.Errorf("succeedingMediator: unexpected command type %T", request)
	}
}

func (m *succeedingMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *succeedingMediator) RegisterMiddleware(mediator.Middleware)               {}

// noopSubscriber satisfies domainNavigation.ShipEventSubscriber. The executor
// constructor requires a non-nil subscriber, but a ship that departs IN_ORBIT
// and navigates with an empty ArrivalTime never waits on an arrival event, so
// every method is an unused no-op.
type noopSubscriber struct{}

func (noopSubscriber) SubscribeArrived(string) <-chan domainNavigation.ShipArrivedEvent { return nil }
func (noopSubscriber) UnsubscribeArrived(string, <-chan domainNavigation.ShipArrivedEvent) {
}
func (noopSubscriber) SubscribeWorkerCompleted(string) <-chan domainNavigation.WorkerCompletedEvent {
	return nil
}
func (noopSubscriber) UnsubscribeWorkerCompleted(string, <-chan domainNavigation.WorkerCompletedEvent) {
}
func (noopSubscriber) SubscribeTasksBecameReady(int) <-chan domainNavigation.TasksBecameReadyEvent {
	return nil
}
func (noopSubscriber) UnsubscribeTasksBecameReady(int, <-chan domainNavigation.TasksBecameReadyEvent) {
}
func (noopSubscriber) SubscribeTransportRequested(int) <-chan domainNavigation.TransportRequestedEvent {
	return nil
}
func (noopSubscriber) UnsubscribeTransportRequested(int, <-chan domainNavigation.TransportRequestedEvent) {
}
func (noopSubscriber) SubscribeTransferCompleted(int) <-chan domainNavigation.TransferCompletedEvent {
	return nil
}
func (noopSubscriber) UnsubscribeTransferCompleted(int, <-chan domainNavigation.TransferCompletedEvent) {
}

// newScoutShip builds an in-orbit, full-tank ship at location, mirroring the
// proven NewShip fixture in route_executor_test.go (frame/role are irrelevant to
// the scan hook).
func newScoutShip(t *testing.T, location *shared.Waypoint, playerID shared.PlayerID) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	shipEntity, err := domainNavigation.NewShip(
		"SCOUT-1",
		playerID,
		location,
		fuel,
		400,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		domainNavigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	return shipEntity
}

// --- Test -----------------------------------------------------------------

// TestExecuteRoute_ArrivalAtShipyardMarketplace_PersistsShipyardInventoryRow drives
// the real production driving port (RouteExecutor.ExecuteRoute) to an arrival at a
// waypoint that is BOTH a marketplace (so the executor's market-scan hook fires) AND
// bears the immutable SHIPYARD trait, and asserts a shipyard_inventory row is
// persisted. It wires the REAL GORM waypoint repo (the trait gate) and the REAL
// shipyard inventory store (the observable outcome), with a test double ONLY at the
// external shipyard API boundary.
func TestExecuteRoute_ArrivalAtShipyardMarketplace_PersistsShipyardInventoryRow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// An open live era for the player (open = highest era_id, no closed_at). The
	// inventory store stamps + reads era-scoped, so a row can only land and be
	// read back under an open era — exactly as the fleet autosizer reads it.
	closedAt := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)
	var openEra persistence.EraModel
	require.NoError(t, db.Where("name = ?", "orion").First(&openEra).Error)
	openEraID := openEra.EraID

	// The destination: a real cached SHIPYARD-trait marketplace. The scanner's
	// trait gate reads this row via the REAL GormWaypointRepository.
	const yardSymbol = "X1-RTE-YARD"
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: yardSymbol,
		SystemSymbol:   "X1-RTE",
		Type:           "ORBITAL_STATION",
		X:              10, Y: 0,
		Traits:   `["MARKETPLACE","SHIPYARD"]`,
		SyncedAt: time.Now().Format(time.RFC3339),
		EraID:    &openEraID,
	}).Error)

	waypointRepo := persistence.NewGormWaypointRepository(db)
	inventoryRepo := persistence.NewShipyardInventoryRepository(db)
	api := &countingShipyardAPI{data: &domainPorts.ShipyardData{
		Symbol:    yardSymbol,
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: "SHIP_PROBE"}},
		Ships: []domainPorts.ShipListingData{
			{Type: "SHIP_PROBE", PurchasePrice: 55_000, Supply: "ABUNDANT"},
		},
	}}
	shipyardScanner := ship.NewShipyardScanner(api, inventoryRepo, waypointRepo, nil, shipyard.NewHeavyShipTypeSet(nil))

	// A single-leg route from a plain origin to the SHIPYARD-trait marketplace.
	// The in-memory destination waypoint must carry the MARKETPLACE trait so the
	// executor's IsMarketplace() scan gate opens (matching the DB row above).
	from := mustTestWaypoint(t, "X1-RTE-A", 0, 0)
	to := mustTestWaypoint(t, yardSymbol, 10, 0)
	to.Traits = []string{"MARKETPLACE", "SHIPYARD"}
	segment := domainNavigation.NewRouteSegment(from, to, 10, 10, 0, shared.FlightModeCruise, false)

	route, err := domainNavigation.NewRoute(
		"route-scout-1", "SCOUT-1", 2,
		[]*domainNavigation.RouteSegment{segment}, 400, false,
	)
	require.NoError(t, err)

	playerID := shared.MustNewPlayerID(2)
	shipEntity := newScoutShip(t, from, playerID)

	// marketScanner is intentionally nil: the shipyard scan must fire on its own
	// gate at the route-arrival hook, independent of the market scanner.
	executor := ship.NewRouteExecutor(
		nil,
		&succeedingMediator{fuelCapacity: 400},
		nil,
		nil,
		shipyardScanner,
		nil,
		nil,
		noopSubscriber{},
	)

	ctx := common.WithPlayerToken(context.Background(), "test-token")
	require.NoError(t, executor.ExecuteRoute(ctx, route, shipEntity, playerID))

	// The trait gate opened on the route path: the one live shipyard read fired.
	require.Equal(t, 1, api.gets, "arriving at a SHIPYARD-trait marketplace via the route executor must trigger exactly one live shipyard read")

	// And the scan persisted a shipyard_inventory row, readable via the same
	// era-scoped path the fleet autosizer's heavy branch consumes.
	rows, err := inventoryRepo.ListByTypes(ctx, 2, []string{"SHIP_PROBE"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a route arrival at a SHIPYARD-trait marketplace must persist a shipyard_inventory row (sp-42ow emit path)")
	require.Equal(t, yardSymbol, rows[0].WaypointSymbol)
	require.Equal(t, 55_000, rows[0].PurchasePrice)
}

// mustTestWaypoint builds an in-memory waypoint fixture (external test package
// cannot reach the in-package mustWaypoint helper).
func mustTestWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	w, err := shared.NewWaypoint(symbol, x, y)
	require.NoError(t, err)
	return w
}
