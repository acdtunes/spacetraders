package ship

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- Warp test doubles ----------------------------------------------------

// fakeWarpNavigator is the double at the ONE port a warp leg crosses to the API.
// It records every leg it is asked to warp (so a refused leg can be proven to
// have made NO warp call) and returns a CANNED post-warp fuel state per
// destination - the fuel numbers are injected by the test, never recomputed from
// the production cost formula, so a fuel assertion cannot be a circular echo of
// the code under test.
//
// refusals is the queue of errors successive Warp calls return before the canned
// success: it is how a test drives the SERVER's authoritative fuel refusal
// through the executor without an HTTP round trip.
type fakeWarpNavigator struct {
	fuelAfter map[string]int // destination waypoint symbol -> ship fuel reported after warp
	refusals  []error        // consumed one per call; a nil entry means "this call succeeds"
	calls     []string       // destination waypoint symbols, in call order
}

func (f *fakeWarpNavigator) Warp(_ context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
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
		ArrivalTimeStr: "", // empty => executor settles arrival immediately (no event wait)
		FuelCurrent:    f.fuelAfter[destination.Symbol],
		FuelCapacity:   ship.Fuel().Capacity,
	}, nil
}

// warpInsufficientFuel reproduces the live API's pre-flight warp refusal verbatim:
// a 400 carrying code 4203 and the SERVER's own fuelRequired/fuelAvailable, wrapped
// exactly as the API client wraps it. Both observed refusals (831/800 and 1144/798)
// have this shape. The hull does not move, so a test using it asserts on a ship that
// is still at its origin.
func warpInsufficientFuel(required, available int) error {
	body := fmt.Sprintf(
		`{"error":{"message":"Warp request failed. Ship EXPLORER-1 requires %d more fuel for navigation.","code":4203,"data":{"fuelRequired":%d,"fuelAvailable":%d}}}`,
		required-available, required, available,
	)
	return fmt.Errorf("failed to warp ship: %w", &domainPorts.APIError{StatusCode: 400, Body: body})
}

// fakeEscapeReader is the double at the onward-viability port. A system it was not
// told about reads as the ZERO escape state (no fuel, no built gate) - a dead end -
// so every test must state its destination's escape facts explicitly and no test can
// pass a viability check by omission.
type fakeEscapeReader struct {
	escapes map[string]WarpDestinationEscape
	err     error    // when set, the destination state is UNREADABLE
	asked   []string // system symbols, in call order
}

func (f *fakeEscapeReader) EscapeOptions(_ context.Context, systemSymbol string, _ shared.PlayerID) (WarpDestinationEscape, error) {
	f.asked = append(f.asked, systemSymbol)
	if f.err != nil {
		return WarpDestinationEscape{}, f.err
	}
	return f.escapes[systemSymbol], nil
}

// escapableSystems builds an escape reader for which each named system sells fuel -
// the "the hull can leave again" baseline every test that is NOT about onward
// viability needs, so those tests exercise the behaviour they actually pin.
func escapableSystems(systems ...string) *fakeEscapeReader {
	escapes := make(map[string]WarpDestinationEscape, len(systems))
	for _, system := range systems {
		escapes[system] = WarpDestinationEscape{SellsFuel: true}
	}
	return &fakeEscapeReader{escapes: escapes}
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
// leg's refuel issues (orbit, dock, refuel). Unlike route_executor's
// recordingMediator it mutates the SHIP's fuel on refuel - matching production,
// where the refuel handler updates ship state in memory - so a post-refuel
// re-check reads a genuinely changed tank. Refuel attempts are counted so
// "topped off before the next warp" is observable.
//
// fillTo, when non-empty, is the tank level successive refuels reach - a market
// whose fuel stock is FINITE, which is the only way a topped-off hull can still
// meet the server's fuel requirement short. Empty (the default) fills to capacity.
type warpRefuelMediator struct {
	refuels int
	fillTo  []int
}

func (m *warpRefuelMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch cmd := request.(type) {
	case *types.OrbitShipCommand:
		return &types.OrbitShipResponse{Status: "in_orbit"}, nil
	case *types.DockShipCommand:
		return &types.DockShipResponse{Status: "docked"}, nil
	case *types.RefuelShipCommand:
		m.refuels++
		if err := m.fill(cmd.Ship); err != nil {
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

func (m *warpRefuelMediator) fill(ship *domainNavigation.Ship) error {
	if len(m.fillTo) == 0 {
		_, err := ship.RefuelToFull()
		return err
	}
	level := m.fillTo[0]
	m.fillTo = m.fillTo[1:]
	return ship.UpdateFuelFromAPI(level, ship.Fuel().Capacity)
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
// warp-capable ship with a full tank warps to a waypoint in ANOTHER system and
// ends physically IN that system, with the post-warp fuel the SERVER reported
// folded onto the hull.
func TestExecuteWarpLeg_WarpsToReachableSystemWithAdequateFuel(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

	ship := newWarpExplorerShip(t, 800, 800, origin)

	warp := &fakeWarpNavigator{fuelAfter: map[string]int{dest.Symbol: 700}}
	mediator := &warpRefuelMediator{}
	executor := NewRouteExecutor(warpRepoFor(ship), mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-SYSB"))

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

// TestExecuteWarpLeg_TakesTheFuelVerdictFromTheServer pins the deletion of the
// local fuel prediction. The old guard priced these legs itself and refused them
// without asking; the executor now ASKS, and the server's pre-flight 4203 refusal -
// carrying ITS OWN fuelRequired/fuelAvailable - is what the caller sees. The
// required figures asserted below are the server's, unrelated to any distance in
// these fixtures, so they cannot be an echo of a local formula.
//
// Every shape of refusal the executor cannot satisfy is TERMINAL, and each is
// parametrized here: the requirement exceeds a full tank, the origin has no fuel
// station, and a refuel that leaves the tank still short. In all three the leg is
// asked exactly ONCE and then abandoned - never a blind retry burning the
// container's restart budget.
func TestExecuteWarpLeg_TakesTheFuelVerdictFromTheServer(t *testing.T) {
	cases := []struct {
		name        string
		originFuel  bool
		fuelCurrent int
		fillTo      []int
		required    int
		available   int
		wantRefuels int
		wantReason  string
	}{
		{
			name:        "requirement exceeds a full tank",
			originFuel:  true,
			fuelCurrent: 800,
			required:    831,
			available:   800,
			wantRefuels: 0,
			wantReason:  "server refused: leg costs more than a full tank",
		},
		{
			name:        "origin sells no fuel to top off with",
			originFuel:  false,
			fuelCurrent: 400,
			required:    700,
			available:   400,
			wantRefuels: 0,
			wantReason:  "server refused: insufficient fuel and no fuel station at origin to refuel",
		},
		{
			name:        "refueling leaves the tank still short of the requirement",
			originFuel:  true,
			fuelCurrent: 400,
			fillTo:      []int{600, 650}, // a fuel stop whose stock runs out short
			required:    700,
			available:   600,
			wantRefuels: 2,
			wantReason:  "server refused: still insufficient fuel after refueling",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
			origin.HasFuel = tc.originFuel
			dest := mustWaypoint(t, "X1-SYSB-B1", 900, 0)

			ship := newWarpExplorerShip(t, tc.fuelCurrent, 800, origin)

			warp := &fakeWarpNavigator{
				fuelAfter: map[string]int{},
				refusals:  []error{warpInsufficientFuel(tc.required, tc.available)},
			}
			mediator := &warpRefuelMediator{fillTo: tc.fillTo}
			executor := NewRouteExecutor(warpRepoFor(ship), mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
				WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-SYSB"))

			err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
			if err == nil {
				t.Fatalf("expected the server's fuel refusal to fail the leg, got nil error")
			}
			var strand *ErrWarpWouldStrand
			if !errors.As(err, &strand) {
				t.Fatalf("expected *ErrWarpWouldStrand so a caller can report unreachability, got %T: %v", err, err)
			}
			if strand.Required != tc.required {
				t.Fatalf("expected the SERVER's requirement %d carried through, got %d", tc.required, strand.Required)
			}
			if strand.Reason != tc.wantReason {
				t.Fatalf("expected reason %q, got %q", tc.wantReason, strand.Reason)
			}
			if len(warp.calls) != 1 {
				t.Fatalf("expected exactly one warp call - the question asked once, no retry storm - got %v", warp.calls)
			}
			if mediator.refuels != tc.wantRefuels {
				t.Fatalf("expected %d refuels, got %d", tc.wantRefuels, mediator.refuels)
			}
			if ship.CurrentLocation().Symbol != origin.Symbol {
				t.Fatalf("expected ship to stay at origin %s after the server's refusal, got %s", origin.Symbol, ship.CurrentLocation().Symbol)
			}
		})
	}
}

// TestExecuteWarpLeg_RefuelsToTheServersRequirementAndRetriesOnce pins the
// actionable half of the 4203 contract. The origin sells fuel but its stock is
// finite, so the pre-warp top-off only reaches 600 of an 800 tank and the server
// refuses the leg needing 700. That refusal is not a blind restart: the executor
// refuels again to the server's stated requirement and retries EXACTLY once, which
// succeeds. Two warp calls and no more is the anti-retry-storm assertion.
func TestExecuteWarpLeg_RefuelsToTheServersRequirementAndRetriesOnce(t *testing.T) {
	origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
	origin.HasFuel = true
	dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

	ship := newWarpExplorerShip(t, 400, 800, origin)

	warp := &fakeWarpNavigator{
		fuelAfter: map[string]int{dest.Symbol: 100},
		refusals:  []error{warpInsufficientFuel(700, 600)},
	}
	mediator := &warpRefuelMediator{fillTo: []int{600, 800}} // market restocks between refuels
	executor := NewRouteExecutor(warpRepoFor(ship), mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-SYSB"))

	err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected the refuelled retry to complete the leg, got error: %v", err)
	}
	if len(warp.calls) != 2 {
		t.Fatalf("expected the refused leg to be retried exactly once (2 warp calls), got %v", warp.calls)
	}
	if mediator.refuels != 2 {
		t.Fatalf("expected the pre-warp top-off plus one refuel to the server's requirement, got %d refuels", mediator.refuels)
	}
	if ship.CurrentLocation().Symbol != dest.Symbol {
		t.Fatalf("expected ship at destination %s after the retry, got %s", dest.Symbol, ship.CurrentLocation().Symbol)
	}
}

// TestExecuteWarpLeg_RefusesDestinationTheHullCouldNeverLeave pins the NEW guard -
// the one safety question the server does not answer. It validates that a hull can
// AFFORD a leg; it will happily land one somewhere with no fuel to buy and no built
// gate to jump out of. Three refusals are covered as one behaviour: the live
// near-miss (a destination with zero fuel whose only exit gate is still under
// construction), an UNREADABLE destination, and an unwired viability reader. All
// three fail CLOSED - refused before any warp call, ship untouched.
func TestExecuteWarpLeg_RefusesDestinationTheHullCouldNeverLeave(t *testing.T) {
	cases := []struct {
		name   string
		escape *fakeEscapeReader
		wired  bool
	}{
		{
			name:   "no fuel and the exit gate is under construction",
			escape: &fakeEscapeReader{escapes: map[string]WarpDestinationEscape{"X1-SYSB": {SellsFuel: false, HasBuiltGate: false}}},
			wired:  true,
		},
		{
			name:   "destination escape state unreadable",
			escape: &fakeEscapeReader{err: errors.New("gate adjacency read failed")},
			wired:  true,
		},
		{
			name:  "no viability reader wired",
			wired: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
			origin.HasFuel = true
			dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

			ship := newWarpExplorerShip(t, 800, 800, origin)

			warp := &fakeWarpNavigator{fuelAfter: map[string]int{}}
			executor := NewRouteExecutor(warpRepoFor(ship), &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{})
			if tc.wired {
				executor = executor.WithWarpSupport(warp, &spyCharter{}, tc.escape)
			} else {
				executor = executor.WithWarpSupport(warp, &spyCharter{}, nil)
			}

			err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
			if err == nil {
				t.Fatalf("expected the warp to be refused as a one-way trip, got nil error")
			}
			if len(warp.calls) != 0 {
				t.Fatalf("a destination refused as unleavable must make NO warp API call, got %v", warp.calls)
			}
			if ship.CurrentLocation().Symbol != origin.Symbol {
				t.Fatalf("expected ship to stay at origin %s, got %s", origin.Symbol, ship.CurrentLocation().Symbol)
			}
		})
	}
}

// TestExecuteWarpLeg_AllowsDestinationWithAWayOut is the other half of the onward-
// viability behaviour: a destination the hull CAN leave is warped to. Either escape
// alone suffices - a market to buy fuel from, or a built jump gate to leave on with
// an empty tank - so all three combinations are parametrized. Without this the
// guard could pass its refusal tests by refusing everything.
func TestExecuteWarpLeg_AllowsDestinationWithAWayOut(t *testing.T) {
	cases := []struct {
		name   string
		escape WarpDestinationEscape
	}{
		{name: "sells fuel", escape: WarpDestinationEscape{SellsFuel: true}},
		{name: "built jump gate to leave on", escape: WarpDestinationEscape{HasBuiltGate: true}},
		{name: "both", escape: WarpDestinationEscape{SellsFuel: true, HasBuiltGate: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := mustWaypoint(t, "X1-SYSA-A1", 0, 0)
			dest := mustWaypoint(t, "X1-SYSB-B1", 100, 0)

			ship := newWarpExplorerShip(t, 800, 800, origin)

			warp := &fakeWarpNavigator{fuelAfter: map[string]int{dest.Symbol: 700}}
			escape := &fakeEscapeReader{escapes: map[string]WarpDestinationEscape{"X1-SYSB": tc.escape}}
			executor := NewRouteExecutor(warpRepoFor(ship), &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
				WithWarpSupport(warp, &spyCharter{}, escape)

			err := executor.ExecuteWarpLeg(context.Background(), ship, dest, shared.MustNewPlayerID(1))
			if err != nil {
				t.Fatalf("expected a leavable destination to be warped to, got error: %v", err)
			}
			if !reflect.DeepEqual(escape.asked, []string{"X1-SYSB"}) {
				t.Fatalf("expected the DESTINATION system's escape state to be read, got %v", escape.asked)
			}
			if ship.CurrentLocation().Symbol != dest.Symbol {
				t.Fatalf("expected ship at destination %s, got %s", dest.Symbol, ship.CurrentLocation().Symbol)
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
	executor := NewRouteExecutor(warpRepoFor(ship), mediator, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-SYSB", "X1-SYSC"))

	err := executor.ExecuteWarpRoute(context.Background(), ship, []*shared.Waypoint{hop1, hop2}, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("expected multi-hop warp route to succeed, got error: %v", err)
	}

	if !reflect.DeepEqual(warp.calls, []string{hop1.Symbol, hop2.Symbol}) {
		t.Fatalf("expected warps to %s then %s, got %v", hop1.Symbol, hop2.Symbol, warp.calls)
	}
	if mediator.refuels != 1 {
		t.Fatalf("expected exactly one refuel (the top-off at fuel-selling hop1, where the tank sat at 300 of 800), got %d", mediator.refuels)
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
	executor := NewRouteExecutor(warpRepoFor(ship), &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, charter, escapableSystems("X1-SYSB"))

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
	executor := NewRouteExecutor(warpRepoFor(ship), &warpRefuelMediator{}, nil, nil, nil, nil, nil, stubSubscriber{}).
		WithWarpSupport(warp, &spyCharter{}, escapableSystems("X1-SYSB"))

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
