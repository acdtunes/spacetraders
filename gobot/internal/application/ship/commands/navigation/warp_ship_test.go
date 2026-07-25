package navigation

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	shipApp "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests drive the warp verb through its REAL executor: the handler is wired
// to a genuine *ship.RouteExecutor whose only test doubles are the WarpNavigator —
// the one port a warp leg crosses to the live API — and the onward-viability reader.
// So every refusal asserted below is produced by the production guards
// (ensureWarpCapable / ensureOnwardViability / the server's own fuel verdict), not
// by a stub standing in for them, and "no warp API call was made" is observable as
// an empty call log at that port.

// fakeWarpAPI is the double at the single API boundary of a warp leg. It records
// every destination it is asked to warp to, so a refused leg is provably a leg that
// reached no API, and returns a canned post-warp fuel state injected by the test.
// refusals lets a test hand the executor the SERVER's own pre-flight rejection.
type fakeWarpAPI struct {
	fuelAfter map[string]int
	refusals  []error
	calls     []string
}

func (f *fakeWarpAPI) Warp(_ context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	f.calls = append(f.calls, destination.Symbol)
	if len(f.refusals) > 0 {
		refusal := f.refusals[0]
		f.refusals = f.refusals[1:]
		if refusal != nil {
			return nil, refusal
		}
	}
	return &domainNavigation.Result{
		Destination:    destination.Symbol,
		ArrivalTimeStr: "", // empty => the executor settles the arrival immediately
		FuelCurrent:    f.fuelAfter[destination.Symbol],
		FuelCapacity:   ship.Fuel().Capacity,
	}, nil
}

// stubEscapeReader answers the executor's onward-viability question. leavable=true
// is the "the hull can get out again" baseline the non-viability scenarios need.
type stubEscapeReader struct {
	leavable bool
}

func (s stubEscapeReader) EscapeOptions(_ context.Context, _ string, _ shared.PlayerID) (shipApp.WarpDestinationEscape, error) {
	return shipApp.WarpDestinationEscape{SellsFuel: s.leavable}, nil
}

// warpOrbitMediator serves the atomic orbit command the executor issues before a
// warp. Nothing else on the warp path reaches the mediator in these scenarios.
type warpOrbitMediator struct{}

func (warpOrbitMediator) Send(_ context.Context, _ mediator.Request) (mediator.Response, error) {
	return &shipTypes.OrbitShipResponse{Status: "in_orbit"}, nil
}
func (warpOrbitMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (warpOrbitMediator) RegisterMiddleware(mediator.Middleware)               {}

// stubWarpShipLoader resolves the hull by symbol, standing in for the ship repository.
type stubWarpShipLoader struct {
	ships map[string]*domainNavigation.Ship
	err   error
}

func (s *stubWarpShipLoader) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ships[symbol], nil
}

// stubWaypointSource lists a system's waypoints, standing in for the graph-backed
// fetch-through source the destination symbol is resolved against.
type stubWaypointSource struct {
	bySystem map[string][]*shared.Waypoint
	err      error
}

func (s *stubWaypointSource) ChartWaypoints(_ context.Context, systemSymbol string, _ shared.PlayerID) ([]*shared.Waypoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.bySystem[systemSymbol], nil
}

func warpTestWaypoint(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	waypoint, err := shared.NewWaypoint(symbol, x, y)
	require.NoError(t, err)
	return waypoint
}

// warpTestHull builds a hull at location with the given fuel. modules=nil produces
// the drive-less hull; a MODULE_WARP_DRIVE_I entry produces the warp-capable one.
func warpTestHull(t *testing.T, symbol string, fuelCurrent, fuelCapacity int, location *shared.Waypoint, warpCapable bool) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(fuelCurrent, fuelCapacity)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	var modules []*domainNavigation.ShipModule
	if warpCapable {
		modules = append(modules, domainNavigation.NewShipModule("MODULE_WARP_DRIVE_I", 0, 0, domainNavigation.ShipRequirements{}))
	}
	hull, err := domainNavigation.NewShip(
		symbol, shared.MustNewPlayerID(1), location, fuel, fuelCapacity, 40, cargo, 9,
		"FRAME_EXPLORER", "EXPLORER", modules, domainNavigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	return hull
}

// warpTestHandler wires the verb over a REAL RouteExecutor carrying warp support,
// returning the handler plus the API-boundary double for call-log assertions. The
// destination is stated leavable so these scenarios exercise the verb, not the
// onward-viability guard.
func warpTestHandler(t *testing.T, hull *domainNavigation.Ship, destinations map[string][]*shared.Waypoint, fuelAfter map[string]int, refusals ...error) (*WarpShipHandler, *fakeWarpAPI) {
	t.Helper()
	warpAPI := &fakeWarpAPI{fuelAfter: fuelAfter, refusals: refusals}
	executor := shipApp.NewRouteExecutor(nil, warpOrbitMediator{}, nil, nil, nil, nil, nil, shipApp.NewShipEventBus()).
		WithWarpSupport(warpAPI, nil, stubEscapeReader{leavable: true})
	loader := &stubWarpShipLoader{ships: map[string]*domainNavigation.Ship{hull.ShipSymbol(): hull}}
	return NewWarpShipHandler(executor, loader, &stubWaypointSource{bySystem: destinations}), warpAPI
}

// A warp-capable hull with fuel to spare reaches a waypoint in ANOTHER system: the
// verb resolves hull + destination, delegates the leg to the existing executor, and
// reports the completed move with the post-warp fuel the server reported.
func TestWarpShip_WarpCapableHullReachesDestinationInAnotherSystem(t *testing.T) {
	origin := warpTestWaypoint(t, "X1-YY85-ZD2D", 0, 0)
	destination := warpTestWaypoint(t, "X1-TY66-A1", 100, 0)
	hull := warpTestHull(t, "TORWIND-F6", 800, 800, origin, true)

	handler, warpAPI := warpTestHandler(t,
		hull,
		map[string][]*shared.Waypoint{"X1-TY66": {destination}},
		map[string]int{destination.Symbol: 700},
	)

	response, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Equal(t, []string{"X1-TY66-A1"}, warpAPI.calls, "the verb issues exactly one warp leg through the executor")
	require.Equal(t, "X1-TY66-A1", hull.CurrentLocation().Symbol)
	require.Equal(t, "X1-TY66", hull.CurrentLocation().SystemSymbol, "the hull ends physically IN the destination system")
	require.Equal(t, 700, hull.Fuel().Current, "the post-warp fuel state is folded back onto the hull")

	warped, ok := response.(*WarpShipResponse)
	require.True(t, ok, "returns a WarpShipResponse")
	require.Equal(t, "TORWIND-F6", warped.ShipSymbol)
	require.Equal(t, "X1-TY66-A1", warped.Destination)
	require.Equal(t, 700, warped.FuelRemaining, "the operator is told the fuel left — the measurement the verb exists for")
}

// A hull with no warp drive is refused, and the typed refusal reaches the caller
// intact: recoverable with errors.As and carrying its own message, never collapsed
// into a generic failure. No warp API call is made.
func TestWarpShip_DrivelessHullRefused_TypedErrorReachesCaller(t *testing.T) {
	origin := warpTestWaypoint(t, "X1-YY85-ZD2D", 0, 0)
	destination := warpTestWaypoint(t, "X1-TY66-A1", 100, 0)
	hull := warpTestHull(t, "TORWIND-19", 800, 800, origin, false)

	handler, warpAPI := warpTestHandler(t,
		hull,
		map[string][]*shared.Waypoint{"X1-TY66": {destination}},
		nil,
	)

	_, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-19",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	var noDrive *shipApp.ErrShipHasNoWarpDrive
	require.ErrorAs(t, err, &noDrive, "the typed no-warp-drive refusal survives the shell")
	require.Equal(t, "TORWIND-19", noDrive.ShipSymbol)
	require.Contains(t, err.Error(), "cannot warp: no warp drive module installed",
		"the operator reads WHY the hull was refused, verbatim")
	require.Empty(t, warpAPI.calls, "an ineligible hull costs zero API budget")
}

// A leg the SERVER refuses for fuel surfaces as the typed strand refusal, and every
// field it carries — required, available, capacity, reason — reaches the operator.
// That is the whole diagnostic value: refused, and by how much. The numbers are the
// server's own (831 required against a hull holding 800), taken from its pre-flight
// rejection, which is why they are trustworthy in a way a local estimate was not.
func TestWarpShip_ServerFuelRefusalSurfacesTypedFieldsToTheOperator(t *testing.T) {
	origin := warpTestWaypoint(t, "X1-YY85-ZD2D", 0, 0)
	destination := warpTestWaypoint(t, "X1-TY66-A1", 900, 0)
	hull := warpTestHull(t, "TORWIND-F6", 800, 800, origin, true)

	handler, warpAPI := warpTestHandler(t,
		hull,
		map[string][]*shared.Waypoint{"X1-TY66": {destination}},
		nil,
		fmt.Errorf("failed to warp ship: %w", &domainPorts.APIError{
			StatusCode: 400,
			Body:       `{"error":{"message":"Warp request failed. Ship TORWIND-F6 requires 31 more fuel for navigation.","code":4203,"data":{"fuelRequired":831,"fuelAvailable":800}}}`,
		}),
	)

	_, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	var strand *shipApp.ErrWarpWouldStrand
	require.ErrorAs(t, err, &strand, "the typed strand refusal survives the shell")
	require.Equal(t, "TORWIND-F6", strand.ShipSymbol)
	require.Equal(t, "X1-YY85-ZD2D", strand.From)
	require.Equal(t, "X1-TY66-A1", strand.To)
	require.Equal(t, 831, strand.Required, "the SERVER's requirement, not a locally guessed one")
	require.Equal(t, 800, strand.Available)
	require.Equal(t, 800, strand.Capacity)
	require.Equal(t, "server refused: leg costs more than a full tank", strand.Reason)
	require.Contains(t, err.Error(), "required 831 fuel, available 800, capacity 800",
		"the numbers reach the operator in the surfaced message, not just the type")
	require.Len(t, warpAPI.calls, 1, "the question is asked once and the refusal is terminal — no retry storm")
	require.Equal(t, "X1-YY85-ZD2D", hull.CurrentLocation().Symbol, "the hull never moved")
}

// A destination the hull could never LEAVE is refused before any warp call — the one
// strand the server cannot prevent, since it validates only that a leg is affordable.
func TestWarpShip_DeadEndDestinationRefusedBeforeAnyWarpCall(t *testing.T) {
	origin := warpTestWaypoint(t, "X1-YY85-ZD2D", 0, 0)
	destination := warpTestWaypoint(t, "X1-TY66-A1", 100, 0)
	hull := warpTestHull(t, "TORWIND-F6", 800, 800, origin, true)

	warpAPI := &fakeWarpAPI{}
	executor := shipApp.NewRouteExecutor(nil, warpOrbitMediator{}, nil, nil, nil, nil, nil, shipApp.NewShipEventBus()).
		WithWarpSupport(warpAPI, nil, stubEscapeReader{leavable: false})
	handler := NewWarpShipHandler(
		executor,
		&stubWarpShipLoader{ships: map[string]*domainNavigation.Ship{hull.ShipSymbol(): hull}},
		&stubWaypointSource{bySystem: map[string][]*shared.Waypoint{"X1-TY66": {destination}}},
	)

	_, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	var deadEnd *shipApp.ErrWarpDeadEnd
	require.ErrorAs(t, err, &deadEnd, "the typed dead-end refusal survives the shell")
	require.Equal(t, "X1-TY66", deadEnd.System, "the operator is told WHICH system has no way out")
	require.Empty(t, warpAPI.calls, "a one-way destination costs zero API budget")
	require.Equal(t, "X1-YY85-ZD2D", hull.CurrentLocation().Symbol, "the hull stays put")
}

// A destination the graph cannot resolve fails CLOSED: no warp is attempted and the
// operator is told which waypoint could not be placed. A warp is issued to a concrete
// waypoint, so an unresolvable one must never be guessed at.
func TestWarpShip_UnresolvableDestinationFailsClosed(t *testing.T) {
	origin := warpTestWaypoint(t, "X1-YY85-ZD2D", 0, 0)
	hull := warpTestHull(t, "TORWIND-F6", 800, 800, origin, true)

	handler, warpAPI := warpTestHandler(t, hull, map[string][]*shared.Waypoint{}, nil)

	_, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "X1-TY66-A1")
	require.Empty(t, warpAPI.calls, "an unresolved destination never becomes a warp")
}

// An unloadable hull is reported rather than silently skipped, so the container
// FAILS honestly instead of claiming a move that never happened.
func TestWarpShip_UnloadableHullReportedHonestly(t *testing.T) {
	handler := NewWarpShipHandler(
		shipApp.NewRouteExecutor(nil, warpOrbitMediator{}, nil, nil, nil, nil, nil, shipApp.NewShipEventBus()).
			WithWarpSupport(&fakeWarpAPI{}, nil, stubEscapeReader{leavable: true}),
		&stubWarpShipLoader{err: fmt.Errorf("db unavailable")},
		&stubWaypointSource{},
	)

	_, err := handler.Handle(context.Background(), &WarpShipCommand{
		ShipSymbol:  "TORWIND-F6",
		Destination: "X1-TY66-A1",
		PlayerID:    shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "db unavailable", "the underlying cause is preserved verbatim")
}

// A mis-dispatched command is rejected rather than panicking — the mediator contract
// is total, exactly as for the sibling route verb.
func TestWarpShip_RejectsWrongRequestType(t *testing.T) {
	handler := NewWarpShipHandler(
		shipApp.NewRouteExecutor(nil, warpOrbitMediator{}, nil, nil, nil, nil, nil, shipApp.NewShipEventBus()).
			WithWarpSupport(&fakeWarpAPI{}, nil, stubEscapeReader{leavable: true}),
		&stubWarpShipLoader{},
		&stubWaypointSource{},
	)

	_, err := handler.Handle(context.Background(), "not-a-warp-command")

	require.Error(t, err)
	require.Contains(t, err.Error(), "WarpShipHandler", "the rejection names the handler that could not serve the request")
}
