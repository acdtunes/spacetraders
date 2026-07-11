package commands

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// calculateCooldownWaitBudget must mirror calculateArrivalWaitBudget's formula
// exactly (sp-ht1f pattern): budget = max(remaining*marginFactor, remaining+minMargin),
// with a negative remaining clamped to zero before either term is computed.
func TestCalculateCooldownWaitBudget_MirrorsArrivalWaitPattern(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		factor    float64
		minMargin time.Duration
		want      time.Duration
	}{
		{
			name:      "short cooldown - flat margin dominates",
			remaining: 60 * time.Second,
			factor:    1.25,
			minMargin: 10 * time.Second,
			want:      75 * time.Second, // scaled=75s > floor=70s
		},
		{
			name:      "longer cooldown - scaled factor dominates",
			remaining: 120 * time.Second,
			factor:    1.25,
			minMargin: 10 * time.Second,
			want:      150 * time.Second, // scaled=150s > floor=130s
		},
		{
			name:      "zero remaining - floor is just the margin",
			remaining: 0,
			factor:    1.25,
			minMargin: 10 * time.Second,
			want:      10 * time.Second,
		},
		{
			name:      "negative remaining clamped to zero before either term",
			remaining: -30 * time.Second,
			factor:    1.25,
			minMargin: 10 * time.Second,
			want:      10 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateCooldownWaitBudget(tt.remaining, tt.factor, tt.minMargin)
			if got != tt.want {
				t.Errorf("calculateCooldownWaitBudget(%v, %v, %v) = %v, want %v", tt.remaining, tt.factor, tt.minMargin, got, tt.want)
			}
		})
	}
}

// rankLanesByCircuitRate must leave a same-system-only ranking untouched:
// with no cross-system lane present every lane divides by the same in-system
// circuit baseline, so the order RankSpreads already produced must survive
// unchanged.
func TestRankLanesByCircuitRate_AllSameSystem_OrderUnchanged(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "A", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-AAA-2", SpreadPerUnit: 500, VolumeCap: 20, CappedSpread: 10000},
		{Good: "B", SourceWaypoint: "X1-AAA-3", DestWaypoint: "X1-AAA-4", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesByCircuitRate(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "B" {
		t.Fatalf("expected order [A, B] unchanged, got %+v", got)
	}
}

// A cross-system lane must lose ranking priority to a same-system lane it
// only narrowly beat on raw value — its circuit spends the round-trip
// jump+cooldown surcharge not trading, so a +12.5% value lead loses to the
// ~17.6% time premium on RATE (9,000/1.3067h ≈ 6,886/hr < 8,000/1.111h =
// 7,200/hr). Critically, the ORIGINAL lane values (SpreadPerUnit,
// CappedSpread) must survive unmutated: rate affects ordering only, never the
// lane's real economics that ClearsFloor()/the executor read.
func TestRankLanesByCircuitRate_CloseCall_SameSystemLaneWins(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// cross-system: value 450×20 = 9,000 over the surcharged circuit → ~6,886/hr
		{Good: "X", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-1", SpreadPerUnit: 450, VolumeCap: 20, CappedSpread: 9000},
		// same-system: value 400×20 = 8,000 over the baseline circuit → 7,200/hr
		{Good: "Y", SourceWaypoint: "X1-AAA-2", DestWaypoint: "X1-AAA-3", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesByCircuitRate(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "Y" || got[1].Good != "X" {
		t.Fatalf("expected the rate ranking to flip order to [Y, X], got %+v", got)
	}
	// The demoted cross-system lane's REAL economics must be untouched.
	if got[1].SpreadPerUnit != 450 || got[1].CappedSpread != 9000 {
		t.Fatalf("cross-system lane's real spread/capped-spread must survive unmutated, got %+v", got[1])
	}
}

// A cross-system lane with an overwhelming lead must still win: the gate
// surcharge is a proportionate time premium for close calls, not a hard
// demotion of every cross-system lane regardless of size.
func TestRankLanesByCircuitRate_OverwhelmingLead_CrossSystemStillWins(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// cross-system: value 2000×20 = 40,000 over the surcharged circuit → ~30,612/hr
		{Good: "X", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-1", SpreadPerUnit: 2000, VolumeCap: 20, CappedSpread: 40000},
		// same-system: value 400×20 = 8,000 over the baseline circuit → 7,200/hr
		{Good: "Y", SourceWaypoint: "X1-AAA-2", DestWaypoint: "X1-AAA-3", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesByCircuitRate(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "X" || got[1].Good != "Y" {
		t.Fatalf("expected cross-system lane to still win despite the surcharge, got %+v", got)
	}
}

// --- travel() ---

// travelMediator records every FindNearestJumpGateQuery, JumpShipCommand and
// NavigateRouteCommand it receives so tests can assert on which verb travel()
// actually dispatched (and in what order), without ever inspecting travel()'s
// private control flow directly. gateResp is the source jump gate the sp-5nqx
// departure hop resolves before a cross-system jump; gateErr forces that lookup
// to fail.
type travelMediator struct {
	jumps       []*navCmd.JumpShipCommand
	navigates   []*navCmd.NavigateRouteCommand
	gateQueries []*shipQueries.FindNearestJumpGateQuery
	jumpResp    *navCmd.JumpShipResponse
	jumpErr     error
	// jumpErrSeq, when non-empty, is consumed one entry per jump (front to back)
	// BEFORE falling back to jumpErr/jumpResp — a nil entry means "this jump
	// succeeds (return jumpResp)", a non-nil entry is returned as the jump error.
	// It lets a test make the FIRST jump 409 on cooldown and a later retry succeed
	// (sp-wc5h resume-ride). A nil/empty slice leaves every existing test unchanged.
	jumpErrSeq []error
	gateResp   *shipQueries.FindNearestJumpGateResponse
	gateErr    error
}

func (m *travelMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipQueries.FindNearestJumpGateQuery:
		m.gateQueries = append(m.gateQueries, cmd)
		if m.gateErr != nil {
			return nil, m.gateErr
		}
		return m.gateResp, nil
	case *navCmd.JumpShipCommand:
		m.jumps = append(m.jumps, cmd)
		if len(m.jumpErrSeq) > 0 {
			e := m.jumpErrSeq[0]
			m.jumpErrSeq = m.jumpErrSeq[1:]
			if e != nil {
				return nil, e
			}
			return m.jumpResp, nil
		}
		if m.jumpErr != nil {
			return nil, m.jumpErr
		}
		return m.jumpResp, nil
	case *navCmd.NavigateRouteCommand:
		m.navigates = append(m.navigates, cmd)
		return nil, nil
	default:
		return nil, nil
	}
}

// gateResponseAt builds a FindNearestJumpGateResponse pointing at gateSymbol, typed
// as a JUMP_GATE, for wiring the sp-5nqx departure-hop lookup in a travelMediator.
func gateResponseAt(t *testing.T, gateSymbol string) *shipQueries.FindNearestJumpGateResponse {
	t.Helper()
	wp, err := shared.NewWaypoint(gateSymbol, 0, 0)
	if err != nil {
		t.Fatalf("gate waypoint: %v", err)
	}
	wp.Type = "JUMP_GATE"
	return &shipQueries.FindNearestJumpGateResponse{
		JumpGate:     wp,
		SystemSymbol: shared.ExtractSystemSymbol(gateSymbol),
	}
}

func (m *travelMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *travelMediator) RegisterMiddleware(middleware common.Middleware) {}

// travelFakeClock captures every Sleep call instead of no-op'ing it, so
// cross-system tests can assert the wait budget actually used matches
// calculateCooldownWaitBudget's formula for the cooldown the jump reported.
type travelFakeClock struct {
	slept []time.Duration
}

func (c *travelFakeClock) Now() time.Time { return time.Now() }
func (c *travelFakeClock) Sleep(d time.Duration) {
	c.slept = append(c.slept, d)
}

// travelShipRepo serves a fixed ship on FindBySymbol - standing in for the
// POST-JUMP reload. Tests set `ship` to a DIFFERENT ship instance (already
// relocated to the destination system) than the one passed into travel(),
// so a passing assertion can only mean travel() actually used the reloaded
// pointer rather than returning its stale input.
type travelShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
	// syncedShip is what SyncShipFromAPI returns — the AUTHORITATIVE live position the
	// sp-trnp departure-hop re-confirmation reads after navigating to the source gate,
	// to defeat the nav-cache race that can complete the navigate before the hull truly
	// reaches the gate. A cross-system test that flies the departure hop sets it to the
	// SOURCE-gate ship (the hull genuinely reached the gate); nil falls back to `ship`.
	// syncCalls counts the resyncs so a test can prove the departure hop re-confirmed.
	syncedShip *navigation.Ship
	syncCalls  int
}

func (r *travelShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *travelShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.syncCalls++
	if r.syncedShip != nil {
		return r.syncedShip, nil
	}
	return r.ship, nil
}

func newTravelShipAt(t *testing.T, symbol, waypointSymbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// newTravelShipAtGate is newTravelShipAt but places the hull ON a JUMP_GATE-typed
// waypoint, so ship.CurrentLocation().IsJumpGate() is true and the sp-5nqx departure
// hop is skipped — the hull is already where the jump verb needs it.
func newTravelShipAtGate(t *testing.T, symbol, gateSymbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(gateSymbol, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	waypoint.Type = "JUMP_GATE"
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// A same-system destination must take the existing navigate fast path and
// never touch the jump verb - jumping is strictly for crossing a system
// boundary, never a substitute for an ordinary same-system leg.
func TestTravel_SameSystem_NavigatesWithoutJumping_ReturnsSameShipPointer(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	mediator := &travelMediator{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{}, nil, nil, nil, nil)

	got, err := handler.travel(context.Background(), ship, "X1-AAA-MARKET", 1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got != ship {
		t.Fatalf("expected the SAME ship pointer back on the same-system path, got a different pointer")
	}
	if len(mediator.navigates) != 1 {
		t.Fatalf("expected exactly one NavigateRouteCommand dispatched, got %d", len(mediator.navigates))
	}
	if len(mediator.jumps) != 0 {
		t.Fatalf("same-system travel must never dispatch a JumpShipCommand, got %d", len(mediator.jumps))
	}
}

// A cross-system destination must jump instead of navigate, opt out of the
// jump's own claim (the coordinator already holds this hull claimed for the
// whole circuit - sp-wlev), wait out the reported cooldown using the
// ETA-scaled budget (not a flat buffer), reload the ship from the repository
// rather than the stale pre-jump pointer, and THEN fly the final gate->waypoint
// hop: the jump lands the hull on the destination system's JUMP GATE, not on
// the destination waypoint's market, so without the hop the caller's dock+sell
// fire at the gate (which does not trade the good) and strand the whole load
// (sp-vzxu). The gate here (X1-BBB-GATE) is NOT the destination waypoint
// (X1-BBB-MARKET), so exactly one NavigateRouteCommand to the destination
// waypoint must follow the jump.
func TestTravel_CrossSystem_JumpsThenHopsGateToWaypoint_WaitsScaledCooldown_ReloadsShip(t *testing.T) {
	// The hull starts on a MARKET waypoint (not a gate), so travel() must fly the
	// sp-5nqx departure hop (source waypoint->gate) BEFORE the jump, then the sp-vzxu
	// arrival hop (dest gate->waypoint) AFTER it: two NavigateRouteCommands in order.
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")
	mediator := &travelMediator{
		gateResp: gateResponseAt(t, "X1-AAA-GATE"),
		jumpResp: &navCmd.JumpShipResponse{
			Success:           true,
			DestinationSystem: "X1-BBB",
			CooldownSeconds:   60,
		},
	}
	clock := &travelFakeClock{}
	// sp-trnp: after the departure-hop navigate, travel re-confirms the hull is truly on the
	// source gate via an authoritative resync. Here the hull genuinely reached X1-AAA-GATE,
	// so the resync returns it and travel proceeds to the jump.
	shipRepo := &travelShipRepo{ship: reloaded, syncedShip: newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, clock, nil)

	got, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(mediator.jumps) != 1 {
		t.Fatalf("expected exactly one JumpShipCommand dispatched, got %d", len(mediator.jumps))
	}
	jump := mediator.jumps[0]
	if jump.ShipSymbol != "HAULER-1" {
		t.Fatalf("expected jump for HAULER-1, got %q", jump.ShipSymbol)
	}
	if jump.DestinationSystem != "X1-BBB" {
		t.Fatalf("expected jump destination system X1-BBB, got %q", jump.DestinationSystem)
	}
	if !jump.SkipClaim {
		t.Fatal("expected SkipClaim=true - the coordinator already holds this hull claimed for the circuit")
	}

	// sp-5nqx departure hop: the driveless hull must be flown source waypoint->gate
	// FIRST, or the jump verb rejects it ("not at a jump gate"). Exactly one gate
	// lookup resolves the source gate to fly to.
	if len(mediator.gateQueries) != 1 {
		t.Fatalf("expected exactly one FindNearestJumpGateQuery for the departure hop, got %d", len(mediator.gateQueries))
	}
	if mediator.gateQueries[0].ShipSymbol != "HAULER-1" {
		t.Fatalf("the departure-hop gate lookup must be for HAULER-1, got %q", mediator.gateQueries[0].ShipSymbol)
	}

	// Two NavigateRouteCommands in order: [1] departure hop to the SOURCE gate
	// (sp-5nqx), [2] arrival hop to the DESTINATION waypoint (sp-vzxu). Without the
	// first the jump strands the bought tranche at the source; without the second the
	// sell fires at the destination gate (which does not trade the good).
	if len(mediator.navigates) != 2 {
		t.Fatalf("cross-system travel must fly BOTH the departure and arrival hops: expected exactly two NavigateRouteCommands, got %d", len(mediator.navigates))
	}
	if mediator.navigates[0].Destination != "X1-AAA-GATE" {
		t.Fatalf("the departure hop must target the SOURCE jump gate X1-AAA-GATE, got %q", mediator.navigates[0].Destination)
	}
	if mediator.navigates[1].Destination != "X1-BBB-MARKET" {
		t.Fatalf("the arrival hop must target the destination waypoint X1-BBB-MARKET, got %q", mediator.navigates[1].Destination)
	}
	if mediator.navigates[0].ShipSymbol != "HAULER-1" || mediator.navigates[1].ShipSymbol != "HAULER-1" {
		t.Fatalf("both hops must fly HAULER-1, got %q and %q", mediator.navigates[0].ShipSymbol, mediator.navigates[1].ShipSymbol)
	}

	wantBudget := calculateCooldownWaitBudget(60*time.Second, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)
	if len(clock.slept) != 1 || clock.slept[0] != wantBudget {
		t.Fatalf("expected a single sleep of %v (the ETA-scaled cooldown budget), got %v", wantBudget, clock.slept)
	}

	if got != reloaded {
		t.Fatal("expected travel() to return the RELOADED ship, not the stale pre-jump pointer")
	}
}

// GUARD on the sp-vzxu hop: when the jump lands the hull DIRECTLY on the
// destination waypoint (the lane's sink IS the system's gate waypoint), the
// gate->waypoint hop is redundant and must be skipped - a gate-market lane
// still costs exactly one jump and zero extra navigates. Here the reloaded ship
// sits on X1-BBB-GATE and the destination waypoint is X1-BBB-GATE, so travel()
// must NOT dispatch any NavigateRouteCommand.
func TestTravel_CrossSystem_JumpLandsOnDestinationWaypoint_SkipsRedundantHop(t *testing.T) {
	// The hull starts ON the source gate, so the sp-5nqx departure hop is skipped
	// (no gate lookup, no pre-jump navigate); the jump then lands it directly on the
	// destination waypoint (the lane's sink IS the gate), so the sp-vzxu arrival hop
	// is skipped too. A gate->gate lane costs exactly one jump and ZERO navigates.
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")
	mediator := &travelMediator{
		jumpResp: &navCmd.JumpShipResponse{
			Success:           true,
			DestinationSystem: "X1-BBB",
			CooldownSeconds:   60,
		},
	}
	clock := &travelFakeClock{}
	shipRepo := &travelShipRepo{ship: reloaded}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, clock, nil)

	got, err := handler.travel(context.Background(), ship, "X1-BBB-GATE", 1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(mediator.jumps) != 1 {
		t.Fatalf("expected exactly one JumpShipCommand dispatched, got %d", len(mediator.jumps))
	}
	if len(mediator.gateQueries) != 0 {
		t.Fatalf("hull already on the source gate - the departure hop's gate lookup must be skipped, got %d", len(mediator.gateQueries))
	}
	if len(mediator.navigates) != 0 {
		t.Fatalf("both hops must be skipped (hull started on a gate, jump landed on the destination waypoint), got %d NavigateRouteCommand(s)", len(mediator.navigates))
	}
	if got != reloaded {
		t.Fatal("expected travel() to return the RELOADED ship even when the hops are skipped")
	}
}

// A jump failure must surface as a wrapped error, not a panic or a silent
// fallback to the stale ship - and must never reach the cooldown wait.
func TestTravel_CrossSystem_JumpFails_ReturnsWrappedError(t *testing.T) {
	// Hull already on the source gate, so the departure hop is skipped and the jump
	// is reached directly: this isolates the JUMP failure from the departure lookup.
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	mediator := &travelMediator{jumpErr: context.DeadlineExceeded}
	clock := &travelFakeClock{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{}, nil, nil, clock, nil)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err == nil {
		t.Fatal("expected an error when the jump fails")
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a failed jump must never reach the cooldown wait, got %d sleeps", len(clock.slept))
	}
}

// sp-5nqx: when the hull already sits ON a jump gate, the departure hop is redundant
// and must be skipped - no gate lookup and no pre-jump navigate. The jump then lands
// the hull on the destination gate, and only the (sp-vzxu) arrival hop to the
// destination waypoint follows: exactly one NavigateRouteCommand, and it is the
// arrival hop, never a departure one.
func TestTravel_CrossSystem_AlreadyAtGate_SkipsDepartureHop(t *testing.T) {
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")
	mediator := &travelMediator{
		jumpResp: &navCmd.JumpShipResponse{
			Success:           true,
			DestinationSystem: "X1-BBB",
			CooldownSeconds:   60,
		},
	}
	clock := &travelFakeClock{}
	shipRepo := &travelShipRepo{ship: reloaded}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, nil, nil, clock, nil)

	got, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(mediator.jumps) != 1 {
		t.Fatalf("expected exactly one JumpShipCommand, got %d", len(mediator.jumps))
	}
	if len(mediator.gateQueries) != 0 {
		t.Fatalf("hull already on a gate - the departure hop's gate lookup must be skipped, got %d", len(mediator.gateQueries))
	}
	if len(mediator.navigates) != 1 {
		t.Fatalf("only the arrival hop should fire when the hull starts on a gate, got %d NavigateRouteCommand(s)", len(mediator.navigates))
	}
	if mediator.navigates[0].Destination != "X1-BBB-MARKET" {
		t.Fatalf("the single navigate must be the arrival hop to X1-BBB-MARKET, got %q", mediator.navigates[0].Destination)
	}
	if got != reloaded {
		t.Fatal("expected travel() to return the RELOADED ship")
	}
}

// --- sp-7gr2 multi-jump BFS ---

// fakeGateGraph is a canned GateGraph: Path returns a fixed hop path (or an
// error), Routable derives from it. It lets travel()'s multi-jump execution be
// tested without a store, API, or graph provider.
type fakeGateGraph struct {
	path    []string
	pathErr error
	// edges is the canned durable adjacency for Connections (sp-1ki5): originSystem ->
	// its neighbor edges. connErr, when set, makes Connections fail (the uncharted-origin
	// live-gate refusal the durable read normally survives). Absent origin -> nil edges.
	edges   map[string][]system.GateEdge
	connErr error

	// repositionPath / repositionPathErr, when set, are what RepositionPath returns (the
	// stored-adjacency probe/scout resolver, sp-8k9m); unset, RepositionPath mirrors Path
	// so every pre-8k9m test is unaffected. repositionBound records the last maxJumps it
	// was called with, so a test can assert the expendable-probe bound reached the resolver.
	repositionPath    []string
	repositionPathErr error
	repositionBound   int
}

func (f *fakeGateGraph) Path(ctx context.Context, from, to string, playerID int) ([]string, error) {
	return f.path, f.pathErr
}

func (f *fakeGateGraph) RepositionPath(ctx context.Context, from, to string, maxJumps int) ([]string, error) {
	f.repositionBound = maxJumps
	if f.repositionPath != nil || f.repositionPathErr != nil {
		return f.repositionPath, f.repositionPathErr
	}
	return f.path, f.pathErr
}

func (f *fakeGateGraph) Routable(ctx context.Context, from, to string, playerID int) (bool, error) {
	if f.pathErr != nil {
		return false, f.pathErr
	}
	return len(f.path) > 0, nil
}

func (f *fakeGateGraph) Connections(ctx context.Context, from string, playerID int) ([]system.GateEdge, error) {
	if f.connErr != nil {
		return nil, f.connErr
	}
	return f.edges[from], nil
}

// A THREE-jump destination (the incident: JP61 is KA42→PA3→UQ16→JP61, not one
// edge) must execute every hop in order, wait the cooldown BETWEEN each
// consecutive jump (three jumps → three settle waits on the fake clock), fly the
// departure hop ONCE at the source and the arrival hop ONCE at the final system,
// and return the reloaded hull. This is the core of the fix: travel() no longer
// assumes origin→dest is a single edge.
func TestTravel_MultiJump_ExecutesEveryHopWithCooldownWaits(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-KA42-DOCK")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-JP61-GATE")
	mediator := &travelMediator{
		gateResp: gateResponseAt(t, "X1-KA42-GATE"),
		jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 60},
	}
	clock := &travelFakeClock{}
	// sp-trnp: the departure-hop resync confirms the hull reached the SOURCE gate X1-KA42-GATE
	// before the first hop.
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{ship: reloaded, syncedShip: newTravelShipAtGate(t, "HAULER-1", "X1-KA42-GATE")}, nil, nil, clock, nil)
	handler.SetGateGraph(&fakeGateGraph{path: []string{"X1-KA42", "X1-PA3", "X1-UQ16", "X1-JP61"}})

	got, err := handler.travel(context.Background(), ship, "X1-JP61-MARKET", 1)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Three jumps, in path order, each opting out of its own claim.
	if len(mediator.jumps) != 3 {
		t.Fatalf("expected exactly three JumpShipCommands (KA42→PA3→UQ16→JP61), got %d", len(mediator.jumps))
	}
	wantDest := []string{"X1-PA3", "X1-UQ16", "X1-JP61"}
	for i, jump := range mediator.jumps {
		if jump.DestinationSystem != wantDest[i] {
			t.Fatalf("hop %d: expected jump to %s, got %s", i+1, wantDest[i], jump.DestinationSystem)
		}
		if !jump.SkipClaim {
			t.Fatalf("hop %d: expected SkipClaim=true (coordinator holds the claim)", i+1)
		}
	}

	// A cooldown settle follows EVERY jump (the wait is what lets the next jump
	// proceed) — three jumps, three waits, each the ETA-scaled budget.
	wantBudget := calculateCooldownWaitBudget(60*time.Second, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)
	if len(clock.slept) != 3 {
		t.Fatalf("expected three cooldown waits (one per jump), got %d: %v", len(clock.slept), clock.slept)
	}
	for i, slept := range clock.slept {
		if slept != wantBudget {
			t.Fatalf("wait %d: expected %v, got %v", i+1, wantBudget, slept)
		}
	}

	// Exactly ONE departure hop (source waypoint→gate) and ONE arrival hop (final
	// gate→destination waypoint) — intermediate hops land ON gates, needing no
	// navigate.
	if len(mediator.gateQueries) != 1 {
		t.Fatalf("expected exactly one departure-hop gate lookup, got %d", len(mediator.gateQueries))
	}
	if len(mediator.navigates) != 2 {
		t.Fatalf("expected exactly two navigates (departure + arrival), got %d", len(mediator.navigates))
	}
	if mediator.navigates[0].Destination != "X1-KA42-GATE" {
		t.Fatalf("departure hop must target the SOURCE gate X1-KA42-GATE, got %q", mediator.navigates[0].Destination)
	}
	if mediator.navigates[1].Destination != "X1-JP61-MARKET" {
		t.Fatalf("arrival hop must target the destination waypoint X1-JP61-MARKET, got %q", mediator.navigates[1].Destination)
	}
	if got != reloaded {
		t.Fatal("expected travel() to return the reloaded hull after the final jump")
	}
}

// An UNROUTABLE destination (the gate graph has no path) must abort travel() with
// the resolver's error and never dispatch a jump or wait a cooldown — the safety
// net behind the pre-buy routability guard.
func TestTravel_MultiJump_Unroutable_AbortsBeforeAnyJump(t *testing.T) {
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-KA42-GATE")
	mediator := &travelMediator{jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 60}}
	clock := &travelFakeClock{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{}, nil, nil, clock, nil)
	handler.SetGateGraph(&fakeGateGraph{pathErr: errors.New("no jump-gate route from X1-KA42 to X1-ZZZ within 5 jumps")})

	_, err := handler.travel(context.Background(), ship, "X1-ZZZ-MARKET", 1)
	if err == nil {
		t.Fatal("expected an error when the destination is unroutable")
	}
	if len(mediator.jumps) != 0 {
		t.Fatalf("an unroutable destination must never dispatch a jump, got %d", len(mediator.jumps))
	}
	if len(clock.slept) != 0 {
		t.Fatalf("an unroutable destination must never reach a cooldown wait, got %d", len(clock.slept))
	}
}

// sp-5nqx: if the source jump gate cannot be resolved, travel() must surface a wrapped
// error and NEVER dispatch the jump - a driveless hull that is not at a gate and cannot
// find one has no legal way to cross, and must fail loudly rather than fire a jump the
// verb will reject.
func TestTravel_CrossSystem_DepartureGateLookupFails_ReturnsWrappedError(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
	mediator := &travelMediator{gateErr: context.DeadlineExceeded}
	clock := &travelFakeClock{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{}, nil, nil, clock, nil)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err == nil {
		t.Fatal("expected an error when the source jump gate cannot be resolved")
	}
	if len(mediator.jumps) != 0 {
		t.Fatalf("a failed departure-gate lookup must never dispatch the jump, got %d", len(mediator.jumps))
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a failed departure hop must never reach the cooldown wait, got %d sleeps", len(clock.slept))
	}
}

// --- sp-wc5h: cooldown-death exit paths ---

// THE resume-death fix. A tour re-adopted mid-circuit after a daemon restart
// re-attempts its jump while the hull is STILL cooling down from the hop it made
// just before the restart, so the jump verb returns 409 code-4000 "still on
// cooldown". travel() must NOT surface that as a hard error — the container
// runner's ~155s restart budget cannot outlast a 226–775s jump cooldown, so the
// tour would crash FAILED and strand the hull idle (the incident:
// TORWIND-2B-a2856bfc crashed on a 325s cooldown). Instead travel() parses the
// remaining cooldown, waits it out, and retries the jump, resuming the circuit
// the moment it clears — then settles the successful jump's own cooldown.
func TestTravel_CrossSystem_JumpOnCooldown_RidesRemainingThenRetries(t *testing.T) {
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")
	cooldownErr := errors.New(`failed to execute jump: failed to jump ship: API error (status 409): {"error":{"code":4000,"message":"Ship action is still on cooldown for 42 second(s).","data":{"cooldown":{"shipSymbol":"HAULER-1","totalSeconds":352,"remainingSeconds":42,"expiration":"2026-07-10T12:00:00.000Z"}},"requestId":"x"}}`)
	mediator := &travelMediator{
		// First jump 409s on cooldown; the retry after the ride succeeds.
		jumpErrSeq: []error{cooldownErr, nil},
		jumpResp:   &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	clock := &travelFakeClock{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{ship: reloaded}, nil, nil, clock, nil)

	got, err := handler.travel(context.Background(), ship, "X1-BBB-GATE", 1)
	if err != nil {
		t.Fatalf("expected travel to ride the cooldown and succeed, got error: %v", err)
	}

	// Two jump attempts: the 409, then the successful retry.
	if len(mediator.jumps) != 2 {
		t.Fatalf("expected exactly two JumpShipCommands (409 then retry), got %d", len(mediator.jumps))
	}

	// Two sleeps: [1] the ride — reported remaining (42s) plus the jitter margin;
	// [2] the post-jump settle — the ETA-scaled budget for the successful jump's
	// own reported cooldown (60s).
	wantRide := 42*time.Second + DefaultCooldownMinMargin
	wantSettle := calculateCooldownWaitBudget(60*time.Second, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)
	if len(clock.slept) != 2 || clock.slept[0] != wantRide || clock.slept[1] != wantSettle {
		t.Fatalf("expected sleeps [ride %v, settle %v], got %v", wantRide, wantSettle, clock.slept)
	}

	if got != reloaded {
		t.Fatal("expected travel() to return the reloaded ship after riding the cooldown")
	}
}

// A non-cooldown jump failure must NOT be mistaken for a cooldown and ridden —
// it propagates on the first attempt (exactly one jump, no wait), so a genuine
// jump failure is never masked into an unbounded ride.
func TestTravel_CrossSystem_NonCooldownJumpError_PropagatesWithoutRiding(t *testing.T) {
	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	mediator := &travelMediator{jumpErr: errors.New("API error (status 422): waypointSymbol Required")}
	clock := &travelFakeClock{}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{}, nil, nil, clock, nil)

	_, err := handler.travel(context.Background(), ship, "X1-BBB-MARKET", 1)
	if err == nil {
		t.Fatal("expected a non-cooldown jump error to surface")
	}
	if len(mediator.jumps) != 1 {
		t.Fatalf("a non-cooldown error must propagate on the first attempt, got %d jumps", len(mediator.jumps))
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a non-cooldown error must never trigger a cooldown ride, got %d sleeps", len(clock.slept))
	}
}

// THE shutdown-death fix. The post-jump cooldown wait must be ctx-interruptible:
// a daemon shutdown while a tour is settling out a jump cooldown has to return
// promptly (so execute() takes its ctx-cancel resumable path and the hull is
// re-adopted next boot), not block the whole graceful window on a bare ~440s
// sleep and get force-killed mid-sleep (the pre-sp-wc5h tour-death shape).
func TestTravel_CrossSystem_CooldownWaitCancelled_ReturnsPromptly(t *testing.T) {
	clock := &travelBlockingClock{
		blockEntered: make(chan struct{}),
		release:      make(chan struct{}),
	}
	// Let the abandoned sleeper goroutine drain once assertions are done.
	defer close(clock.release)

	ship := newTravelShipAtGate(t, "HAULER-1", "X1-AAA-GATE")
	reloaded := newTravelShipAt(t, "HAULER-1", "X1-BBB-GATE")
	mediator := &travelMediator{
		jumpResp: &navCmd.JumpShipResponse{Success: true, DestinationSystem: "X1-BBB", CooldownSeconds: 60},
	}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{ship: reloaded}, nil, nil, clock, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := handler.travel(ctx, ship, "X1-BBB-GATE", 1)
		done <- err
	}()

	// Wait until travel() is parked inside the cooldown-wait sleep.
	select {
	case <-clock.blockEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("travel never entered the cooldown-wait sleep")
	}

	// Cancel, as daemon shutdown does. The wait must race ctx.Done and let travel()
	// return without waiting for the (never-released) sleep to finish.
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled after cancelling during the cooldown wait, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("travel did not return promptly after cancel during the cooldown wait — the sleep was not interruptible")
	}
}

// travelBlockingClock blocks inside Sleep until the test releases it, signalling
// entry exactly once so a test can deterministically cancel WHILE travel() is
// parked in the cooldown wait — no wall-clock race. Mirrors parkWaitBlockingClock
// (sp-l709, run_factory_coordinator_park_wait_interrupt_test.go).
type travelBlockingClock struct {
	enteredOnce  sync.Once
	blockEntered chan struct{}
	release      chan struct{}
}

func (c *travelBlockingClock) Now() time.Time { return time.Now() }
func (c *travelBlockingClock) Sleep(time.Duration) {
	c.enteredOnce.Do(func() { close(c.blockEntered) })
	<-c.release
}

// sp-8k9m: jumpPath selects the resolver by the reposition bound — 0 keeps the strict
// fetch-through Path (heavies/trade/arb, byte-for-byte unchanged), a positive bound routes
// over the stored-adjacency RepositionPath AND forwards the bound to it (the expendable
// probe reach that reaches posts past MaxJumpPath).
func TestJumpPath_BoundSelectsRepositionResolver(t *testing.T) {
	fake := &fakeGateGraph{
		path:           []string{"X1-A", "X1-B"},                 // strict Path result
		repositionPath: []string{"X1-A", "X1-M", "X1-N", "X1-Z"}, // stored-adjacency result
	}
	h := &RunTradeRouteCoordinatorHandler{gateGraph: fake}

	strict, err := h.jumpPath(context.Background(), "X1-A", "X1-Z", 1, 0)
	if err != nil {
		t.Fatalf("strict jumpPath errored: %v", err)
	}
	if !reflect.DeepEqual(strict, fake.path) {
		t.Fatalf("bound 0 must use strict Path %v, got %v", fake.path, strict)
	}

	relaxed, err := h.jumpPath(context.Background(), "X1-A", "X1-Z", 1, 9)
	if err != nil {
		t.Fatalf("reposition jumpPath errored: %v", err)
	}
	if !reflect.DeepEqual(relaxed, fake.repositionPath) {
		t.Fatalf("a positive bound must use RepositionPath %v, got %v", fake.repositionPath, relaxed)
	}
	if fake.repositionBound != 9 {
		t.Fatalf("the bound must reach the resolver, got %d", fake.repositionBound)
	}
}
