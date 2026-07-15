package ship

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- Warp test doubles ----------------------------------------------------

// fakeWarpNavigator is the double at the ONE port a warp leg crosses to the API.
// It records every leg it is asked to warp (so a refused leg can be proven to
// have made NO warp call) and returns a CANNED post-warp fuel state per
// destination - the fuel numbers are injected by the test, never recomputed from
// the production cost formula, so a fuel assertion cannot be a circular echo of
// the code under test.
type fakeWarpNavigator struct {
	fuelAfter map[string]int // destination waypoint symbol -> ship fuel reported after warp
	calls     []string       // destination waypoint symbols, in call order
}

func (f *fakeWarpNavigator) Warp(_ context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	f.calls = append(f.calls, destination.Symbol)
	return &domainNavigation.Result{
		Destination:    destination.Symbol,
		ArrivalTimeStr: "", // empty => executor settles arrival immediately (no event wait)
		FuelCurrent:    f.fuelAfter[destination.Symbol],
		FuelCapacity:   ship.Fuel().Capacity,
	}, nil
}

// spyCharter records which systems chart-on-arrival was invoked for, so a test
// can assert the executor charts the RIGHT system after a warp lands.
type spyCharter struct {
	charted []string
}

func (s *spyCharter) ChartSystem(_ context.Context, systemSymbol string, _ shared.PlayerID) error {
	s.charted = append(s.charted, systemSymbol)
	return nil
}

// warpRefuelMediator satisfies common.Mediator for the atomic commands a warp
// leg's fuel-safety refuel issues (orbit, dock, refuel). Unlike route_executor's
// recordingMediator it mutates the SHIP's fuel to capacity on refuel - matching
// production, where the refuel handler updates ship state in memory - so the
// guard's post-refuel re-check reads a genuinely full tank. Refuel attempts are
// counted so "topped off before the next warp" is observable.
type warpRefuelMediator struct {
	refuels int
}

func (m *warpRefuelMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch cmd := request.(type) {
	case *types.OrbitShipCommand:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		m.refuels++
		if _, err := cmd.Ship.RefuelToFull(); err != nil {
			return nil, err
		}
		return &types.RefuelShipResponse{
			Status:       "refueled",
			CurrentFuel:  cmd.Ship.Fuel().Current,
			FuelCapacity: cmd.Ship.Fuel().Capacity,
		}, nil
	default:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	}
}

func (m *warpRefuelMediator) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (m *warpRefuelMediator) RegisterMiddleware(mediator.Middleware)               {}

// newWarpExplorerShip builds a warp-capable explorer (MODULE_WARP_DRIVE_I, fuel
// cap 800) at location, mirroring newExecutorTestShip but with the warp module
// installed. newExecutorTestShip (modules=nil) is reused as the drive-less hull.
func newWarpExplorerShip(t *testing.T, current, capacity int, location *shared.Waypoint) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	warpModule := domainNavigation.NewShipModule("MODULE_WARP_DRIVE_I", 0, 0, domainNavigation.ShipRequirements{})
	ship, err := domainNavigation.NewShip(
		"EXPLORER-1",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		capacity,
		40,
		cargo,
		9,
		"FRAME_EXPLORER",
		"EXPLORER",
		[]*domainNavigation.ShipModule{warpModule},
		domainNavigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// --- Tests ----------------------------------------------------------------

// TestExecuteWarpLeg_WarpsToReachableSystemWithAdequateFuel pins scenario 1: a
// warp-capable ship with fuel to spare warps to a waypoint in ANOTHER system and
// ends physically IN that system. Distance A(0,0)->B(100,0) is 100, cost 100 at
// the CRUISE rate; the full 800 tank covers it with no refuel.
func TestExecuteWarpLeg_WarpsToReachableSystemWithAdequateFuel(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

	ship := newWarpExplorerShip(t, 800, 800, origin)

	warp := &fakeWarpNavigator{fuelAfter: map[string]int{dest.Symbol: 700}}
	mediator := &warpRefuelMediator{}
	executor := NewRouteExecutor(nil, mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{})

	err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected warp to succeed with adequate fuel, got error: %v", err)
	}

	if len(warp.calls) != 1 || warp.calls[0] != dest.Symbol {
		t.Fatalf("expected exactly one warp to %s, got %v", dest.Symbol, warp.calls)
	}
	if ship.CurrentLocation().Symbol != dest.Symbol {
		t.Fatalf("expected ship at destination %s, got %s", dest.Symbol, ship.CurrentLocation().Symbol)
	}
	if ship.CurrentLocation().SystemSymbol != "X1-SYSB" {
		t.Fatalf("expected ship IN destination system X1-SYSB, got %s", ship.CurrentLocation().SystemSymbol)
	}
	if ship.Fuel().Current != 700 {
		t.Fatalf("expected post-warp fuel 700 (800 - 100 cruise cost), got %d", ship.Fuel().Current)
	}
	if mediator.refuels != 0 {
		t.Fatalf("expected no refuel on an already-fuelled leg, got %d", mediator.refuels)
	}
}

// TestExecuteWarpLeg_RefusesLegThatWouldStrand pins scenario 2, the key safety
// property: a leg the ship cannot safely complete is REFUSED before any warp API
// call. Two strand shapes are covered as one behaviour (parametrized): a leg that
// costs more than a full tank, and a leg the ship is too low on fuel for with no
// fuel station at the origin to top off. In both, no warp is issued and the ship
// stays put.
func TestExecuteWarpLeg_RefusesLegThatWouldStrand(t *testing.T) {
	cases := []struct {
		name        string
		fuelCurrent int
		originFuel  bool
		destX       float64
	}{
		{name: "cost exceeds full tank", fuelCurrent: 800, originFuel: true, destX: 900},
		{name: "too low with no fuel station at origin", fuelCurrent: 50, originFuel: false, destX: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
			origin.HasFuel = tc.originFuel
			dest := mustWaypoint(t, "X1-SYSB-B1", tc.destX, 0)

			ship := newWarpExplorerShip(t, tc.fuelCurrent, 800, origin)

			warp := &fakeWarpNavigator{fuelAfter: map[string]int{}}
			executor := NewRouteExecutor(nil, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
				WithWarpSupport(warp, &spyCharter{})

			err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
			if err == nil {
				t.Fatalf("expected warp to be refused as a strand risk, got nil error")
			}
			var strand *ErrWarpWouldStrand
			if !errors.As(err, &strand) {
				t.Fatalf("expected *ErrWarpWouldStrand so a caller can report unreachability, got %T: %v", err, err)
			}
			if len(warp.calls) != 0 {
				t.Fatalf("a refused leg must make NO warp API call, got %v", warp.calls)
			}
			if ship.CurrentLocation().Symbol != origin.Symbol {
				t.Fatalf("expected ship to stay at origin %s after refusal, got %s", origin.Symbol, ship.CurrentLocation().Symbol)
			}
		})
	}
}

// TestExecuteWarpRoute_MultiHopRefuelsBetweenLegs pins scenario 3: a two-hop warp
// route where the ship arrives leg 1 too low for leg 2 tops off at the (fuel-
// selling) arrival waypoint, then completes leg 2. leg1 A(0,0)->B(500,0) costs
// 500 (800->300 left); leg2 B(500,0)->C(500,700) costs 700, unaffordable at 300,
// so the guard refuels to 800 before warping (800->100 left).
func TestExecuteWarpRoute_MultiHopRefuelsBetweenLegs(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	hop1 := mustWaypoint(t, "X1-SYSB-B1", 500, 0)
	hop1.HasFuel = true // sells fuel: the top-off stop between legs
	hop2 := mustWaypoint(t, "X1-SYSC-C1", 500, 700)

	ship := newWarpExplorerShip(t, 800, 800, origin)

	warp := &fakeWarpNavigator{fuelAfter: map[string]int{
		hop1.Symbol: 300, // after leg 1
		hop2.Symbol: 100, // after leg 2 (from a topped-off tank)
	}}
	mediator := &warpRefuelMediator{}
	executor := NewRouteExecutor(nil, mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{})

	err := executor.ExecuteWarpRoute(context.Background(), ship, []*shared.Waypoint{hop1, hop2}, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected multi-hop warp route to succeed, got error: %v", err)
	}

	if !reflect.DeepEqual(warp.calls, []string{hop1.Symbol, hop2.Symbol}) {
		t.Fatalf("expected warps to %s then %s, got %v", hop1.Symbol, hop2.Symbol, warp.calls)
	}
	if mediator.refuels != 1 {
		t.Fatalf("expected exactly one refuel (before leg 2, when 300 fuel < 700 needed), got %d", mediator.refuels)
	}
	if ship.CurrentLocation().Symbol != hop2.Symbol {
		t.Fatalf("expected ship at final hop %s, got %s", hop2.Symbol, ship.CurrentLocation().Symbol)
	}
	if ship.CurrentLocation().SystemSymbol != "X1-SYSC" {
		t.Fatalf("expected ship IN final system X1-SYSC, got %s", ship.CurrentLocation().SystemSymbol)
	}
	if ship.Fuel().Current != 100 {
		t.Fatalf("expected 100 fuel after topped-off leg 2, got %d", ship.Fuel().Current)
	}
}

// TestExecuteWarpLeg_ChartsDestinationSystemOnArrival pins scenario 4 at the
// executor boundary: on a successful warp the executor delegates charting of the
// DESTINATION system to the SystemCharter. Asserting the ship also ended at the
// destination proves the chart fires on ARRIVAL, not speculatively before.
func TestExecuteWarpLeg_ChartsDestinationSystemOnArrival(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	dest := mustWaypoint(t, "X1-SYSB-B1", 120, 0)

	ship := newWarpExplorerShip(t, 800, 800, origin)

	warp := &fakeWarpNavigator{fuelAfter: map[string]int{dest.Symbol: 680}}
	charter := &spyCharter{}
	executor := NewRouteExecutor(nil, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, charter)

	err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected warp to succeed, got error: %v", err)
	}

	if !reflect.DeepEqual(charter.charted, []string{"X1-SYSB"}) {
		t.Fatalf("expected chart-on-arrival for destination system X1-SYSB, got %v", charter.charted)
	}
	if ship.CurrentLocation().SystemSymbol != "X1-SYSB" {
		t.Fatalf("expected ship to have arrived IN X1-SYSB before charting, got %s", ship.CurrentLocation().SystemSymbol)
	}
}

// TestExecuteWarpLeg_RefusesShipWithoutWarpDrive pins scenario 5, fail-closed: a
// ship with no warp drive module is refused with a typed error and NO warp API
// call - the executor never emits a warp the live API would reject. The drive-less
// hull is newExecutorTestShip (modules=nil).
func TestExecuteWarpLeg_RefusesShipWithoutWarpDrive(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

	ship := newExecutorTestShip(t, 800, 800, origin) // no modules => no warp drive

	warp := &fakeWarpNavigator{fuelAfter: map[string]int{dest.Symbol: 700}}
	executor := NewRouteExecutor(nil, &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{})

	err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
	if err == nil {
		t.Fatalf("expected a drive-less ship to be refused, got nil error")
	}
	var noDrive *ErrShipHasNoWarpDrive
	if !errors.As(err, &noDrive) {
		t.Fatalf("expected *ErrShipHasNoWarpDrive (fail-closed), got %T: %v", err, err)
	}
	if len(warp.calls) != 0 {
		t.Fatalf("a drive-less ship must make NO warp API call, got %v", warp.calls)
	}
	if ship.CurrentLocation().Symbol != origin.Symbol {
		t.Fatalf("expected ship to stay at origin, got %s", ship.CurrentLocation().Symbol)
	}
}
