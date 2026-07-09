package commands

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

// rankLanesWithGatePenalty must leave a same-system-only ranking untouched:
// with no cross-system lane present, no penalty ever applies, so the order
// RankSpreads already produced must survive unchanged.
func TestRankLanesWithGatePenalty_AllSameSystem_OrderUnchanged(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "A", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-AAA-2", SpreadPerUnit: 500, VolumeCap: 20, CappedSpread: 10000},
		{Good: "B", SourceWaypoint: "X1-AAA-3", DestWaypoint: "X1-AAA-4", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesWithGatePenalty(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "B" {
		t.Fatalf("expected order [A, B] unchanged, got %+v", got)
	}
}

// A cross-system lane must lose ranking priority to a same-system lane it
// only narrowly beat on raw spread - the penalty represents the jump
// cooldown's opportunity cost against an equally-profitable same-system
// lane. Critically, the ORIGINAL lane values (SpreadPerUnit, CappedSpread)
// must survive unmutated: the penalty affects ordering only, never the
// lane's real economics that ClearsFloor()/the executor read.
func TestRankLanesWithGatePenalty_CloseCall_SameSystemLaneWins(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// cross-system: raw spread 500, penalty 200 -> effective 300 * volCap 20 = 6000
		{Good: "X", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-1", SpreadPerUnit: 500, VolumeCap: 20, CappedSpread: 10000},
		// same-system: raw spread 400, no penalty -> effective 400 * volCap 20 = 8000 > 6000
		{Good: "Y", SourceWaypoint: "X1-AAA-2", DestWaypoint: "X1-AAA-3", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesWithGatePenalty(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "Y" || got[1].Good != "X" {
		t.Fatalf("expected penalty to flip order to [Y, X], got %+v", got)
	}
	// The demoted cross-system lane's REAL economics must be untouched.
	if got[1].SpreadPerUnit != 500 || got[1].CappedSpread != 10000 {
		t.Fatalf("cross-system lane's real spread/capped-spread must survive unmutated, got %+v", got[1])
	}
}

// A cross-system lane with an overwhelming lead must still win: the penalty
// is a proportionate tiebreaker for close calls, not a hard demotion of
// every cross-system lane regardless of size.
func TestRankLanesWithGatePenalty_OverwhelmingLead_CrossSystemStillWins(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		// cross-system: raw spread 2000, penalty 200 -> effective 1800 * volCap 20 = 36000
		{Good: "X", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-1", SpreadPerUnit: 2000, VolumeCap: 20, CappedSpread: 40000},
		// same-system: raw spread 400, no penalty -> effective 400 * volCap 20 = 8000
		{Good: "Y", SourceWaypoint: "X1-AAA-2", DestWaypoint: "X1-AAA-3", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	got := rankLanesWithGatePenalty(lanes, 0, "")

	if len(got) != 2 || got[0].Good != "X" || got[1].Good != "Y" {
		t.Fatalf("expected cross-system lane to still win despite penalty, got %+v", got)
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
	gateResp    *shipQueries.FindNearestJumpGateResponse
	gateErr     error
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
}

func (r *travelShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
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
	shipRepo := &travelShipRepo{ship: reloaded}
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
