package commands

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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

// travelMediator records every JumpShipCommand and NavigateRouteCommand it
// receives so tests can assert on which verb travel() actually dispatched,
// without ever inspecting travel()'s private control flow directly.
type travelMediator struct {
	jumps     []*navCmd.JumpShipCommand
	navigates []*navCmd.NavigateRouteCommand
	jumpResp  *navCmd.JumpShipResponse
	jumpErr   error
}

func (m *travelMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
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
// ETA-scaled budget (not a flat buffer), and return a ship reloaded from
// the repository rather than the stale pre-jump pointer.
func TestTravel_CrossSystem_JumpsWithSkipClaim_WaitsScaledCooldown_ReloadsShip(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
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
	if len(mediator.navigates) != 0 {
		t.Fatalf("cross-system travel must never dispatch a NavigateRouteCommand, got %d", len(mediator.navigates))
	}

	wantBudget := calculateCooldownWaitBudget(60*time.Second, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)
	if len(clock.slept) != 1 || clock.slept[0] != wantBudget {
		t.Fatalf("expected a single sleep of %v (the ETA-scaled cooldown budget), got %v", wantBudget, clock.slept)
	}

	if got != reloaded {
		t.Fatal("expected travel() to return the RELOADED ship, not the stale pre-jump pointer")
	}
}

// A jump failure must surface as a wrapped error, not a panic or a silent
// fallback to the stale ship - and must never reach the cooldown wait.
func TestTravel_CrossSystem_JumpFails_ReturnsWrappedError(t *testing.T) {
	ship := newTravelShipAt(t, "HAULER-1", "X1-AAA-DOCK")
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
