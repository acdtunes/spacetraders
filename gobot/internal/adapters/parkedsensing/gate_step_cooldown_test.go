package parkedsensing_test

// A gate step must not spend a request re-learning a cooldown the ships row
// already holds.
//
// OBSERVED LIVE (sp-7e5j6, staging 2026-08-06 14:05-14:07Z, the first EXPANSION
// phase after gate completion). The walk's second step jumped, the API answered
// 409 code-4000 "Ship action is still on cooldown for 201 second(s)" with the
// expiry in the payload, and the placement was held and retried — on the NEXT
// tick, and the one after that, for the whole 262-second cooldown. Roughly 30
// doomed jumps per hull per cooldown, measured at 7-8 hulls a minute across three
// consecutive minutes: ~0.12 req/s, about a quarter of sensing's own pacer, spent
// against a hard 2 req/s account ceiling several coordinators contend for.
//
// The retry was never the bug. The ATTEMPT was: navigation.Ship.IsOnCooldown
// answers the same question from the hull's own row, for free, before the call.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// cooldownWorld stands the hull ON its gate — so every walk reaches step TWO, the
// jump — and answers the ships read with whatever cooldown the case is about.
//
// It COUNTS jumps rather than refusing them, because the assertion this file
// exists for is about a call that must never be made. A world that answered the
// jump with the live 409 would still pass a test that only checked the error.
type cooldownWorld struct {
	mu sync.Mutex

	gate string

	// ship is answered to GetShipQuery. nil + shipErr nil means "no ship in the
	// response" — the unreadable-row case.
	ship    *navigation.Ship
	shipErr error

	// jumps counts JumpShipCommand sends. THE deliverable: zero while on cooldown.
	jumps       int
	shipReads   int
	jumpTargets []string
}

func (w *cooldownWorld) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch req := request.(type) {
	case *shipQueries.FindNearestJumpGateQuery:
		gate, err := shared.NewWaypoint(w.gate, 0, 0)
		if err != nil {
			return nil, err
		}
		gate.Type = "JUMP_GATE"
		return &shipQueries.FindNearestJumpGateResponse{JumpGate: gate}, nil

	case *shipQueries.GetShipQuery:
		w.shipReads++
		if w.shipErr != nil {
			return nil, w.shipErr
		}
		return &shipQueries.GetShipResponse{Ship: w.ship}, nil

	case *shipNav.JumpShipCommand:
		w.jumps++
		w.jumpTargets = append(w.jumpTargets, req.DestinationSystem)
		return &shipNav.JumpShipResponse{Success: true, DestinationSystem: req.DestinationSystem}, nil
	}
	return nil, nil
}

func (*cooldownWorld) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (*cooldownWorld) RegisterMiddleware(mediator.Middleware)               {}

func (w *cooldownWorld) jumpCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.jumps
}

// probeWithCooldown builds a minimal valid sensing hull whose cooldown expires at
// expiry. A nil expiry means the row carries none at all.
func probeWithCooldown(t *testing.T, symbol string, expiry *time.Time) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(0, 0)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint("X1-AA-G1", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(testPlayerID), waypoint, fuel, 0, 0, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	if expiry != nil {
		ship.SetCooldown(*expiry)
	}
	return ship
}

func oneGateApart() stubGateNeighbours {
	return stubGateNeighbours{edges: map[string][]string{"X1-AA": {"X1-BB"}}}
}

// THE DELIVERABLE. No jump is attempted while IsOnCooldown() is true.
func TestRouteAcross_HoldsTheGateStepWhileTheHullIsOnCooldown(t *testing.T) {
	logger := &recordingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	expiry := time.Now().Add(201 * time.Second)
	world := &cooldownWorld{
		gate: "X1-AA-G1",
		ship: probeWithCooldown(t, "TORWINDSTG-12", &expiry),
	}
	mover := adapterSensing.NewMoverPort(world, oneGateApart())

	err := mover.RouteAcross(ctx, testPlayerID, "TORWINDSTG-12", "X1-AA-G1", "X1-BB-M1")

	require.Zero(t, world.jumpCount(),
		"a hull the ships row already reports on cooldown was jumped anyway — this is the ~30-doomed-calls-per-hull-per-cooldown "+
			"defect, ~0.12 req/s of a 2 req/s account ceiling spent re-learning a known expiry")
	require.Equal(t, 1, world.shipReads, "the cooldown must be read from the hull's own row, exactly once per step")

	// STILL AN ERROR: the walk did not advance, and the caller's hold-and-retry is
	// the handling a held step wants. Returning nil would have the placement
	// machine count a step that never happened.
	require.Error(t, err, "a held step must still be reported, so the placement holds its slot and retries next tick")

	held := logger.withAction("parked_sensing_gate_step_cooldown_hold")
	require.Len(t, held, 1, "a held step must be named, or a placement that stops advancing has no diagnostic at all")
	require.Equal(t, "INFO", held[0].level,
		"a cooldown hold is the healthy case — logged as a WARNING it made a working EXPANSION run look blocked")
	require.Equal(t, "TORWINDSTG-12", held[0].metadata["ship_symbol"])
	require.Equal(t, "X1-BB", held[0].metadata["next_system"])
	require.Equal(t, expiry.UTC().Format(time.RFC3339), held[0].metadata["cooldown_until"],
		"the hold must name WHEN it clears, so a reader can tell a 200-second wait from a stuck placement")
	require.Contains(t, held[0].message, "HELD")

	// The wording must be DISTINGUISHABLE. Half the harm was that a cooldown wore
	// the routing refusal's words ("the step from X into Y was refused"), which
	// reads like a gate/topology problem and sends the next reader to the wrong
	// subsystem.
	require.Empty(t, logger.withAction("parked_sensing_gate_step_failed"),
		"a cooldown hold was reported with the routing-refusal wording")
	require.Empty(t, logger.withAction("parked_sensing_gate_walk_unroutable"),
		"a cooldown hold was reported as an unroutable graph")
}

// NON-VACUITY. The very same fixture, with the cooldown expired, DOES jump — so
// the test above is observing the guard and not a walk that never reached step two.
func TestRouteAcross_JumpsOnceTheCooldownHasExpired(t *testing.T) {
	logger := &recordingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	expiry := time.Now().Add(-1 * time.Second)
	world := &cooldownWorld{
		gate: "X1-AA-G1",
		ship: probeWithCooldown(t, "TORWINDSTG-12", &expiry),
	}
	mover := adapterSensing.NewMoverPort(world, oneGateApart())

	require.NoError(t, mover.RouteAcross(ctx, testPlayerID, "TORWINDSTG-12", "X1-AA-G1", "X1-BB-M1"))

	require.Equal(t, 1, world.jumpCount(),
		"an expired cooldown must not hold the hull: IsOnCooldown is false for a past expiry, and a guard that blocked anyway "+
			"would freeze the walk instead of pacing it")
	require.Equal(t, []string{"X1-BB"}, world.jumpTargets)
	require.Empty(t, logger.withAction("parked_sensing_gate_step_cooldown_hold"))
}

// A hull with no cooldown on its row at all jumps, unchanged.
func TestRouteAcross_JumpsWhenTheRowCarriesNoCooldown(t *testing.T) {
	ctx := logging.WithLogger(context.Background(), &recordingLogger{})

	world := &cooldownWorld{gate: "X1-AA-G1", ship: probeWithCooldown(t, "PROBE-A", nil)}
	mover := adapterSensing.NewMoverPort(world, oneGateApart())

	require.NoError(t, mover.RouteAcross(ctx, testPlayerID, "PROBE-A", "X1-AA-G1", "X1-BB-M1"))
	require.Equal(t, 1, world.jumpCount(), "a hull with no cooldown must jump exactly as before")
}

// FAILS OPEN, and that direction is the safe one. An unreadable row leaves the
// walk exactly as it was before the guard existed — the jump is attempted and the
// API stays the authority that refuses it. Holding on ABSENCE would strand a hull
// forever: it would never jump, never arrive, and go on counting against the probe
// cap, which is the wedge this engine's fail-closed rules exist to avoid.
func TestRouteAcross_AttemptsTheJumpWhenTheCooldownCannotBeRead(t *testing.T) {
	ctx := logging.WithLogger(context.Background(), &recordingLogger{})

	for _, tc := range []struct {
		name  string
		world *cooldownWorld
	}{
		{"read error", &cooldownWorld{gate: "X1-AA-G1", shipErr: errors.New("ships table unavailable")}},
		{"no ship in the response", &cooldownWorld{gate: "X1-AA-G1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mover := adapterSensing.NewMoverPort(tc.world, oneGateApart())
			require.NoError(t, mover.RouteAcross(ctx, testPlayerID, "PROBE-A", "X1-AA-G1", "X1-BB-M1"))
			require.Equal(t, 1, tc.world.jumpCount(),
				"an unknown cooldown was treated as a known one: a hull whose row cannot be read would never jump again")
		})
	}
}

// THE HULL IS STILL FLOWN TO ITS GATE while cooling. A jump cooldown gates the
// jump ACTION only, so step ONE is productive work — refusing it would idle the
// hull for the whole cooldown and then need another tick to start walking.
func TestRouteAcross_StillFliesToTheGateWhileOnCooldown(t *testing.T) {
	ctx := logging.WithLogger(context.Background(), &recordingLogger{})

	expiry := time.Now().Add(201 * time.Second)
	// The hull is NOT standing on the gate, so the walk takes step one.
	world := &cooldownWorld{gate: "X1-AA-G1", ship: probeWithCooldown(t, "PROBE-A", &expiry)}
	mover := adapterSensing.NewMoverPort(world, oneGateApart())

	require.NoError(t, mover.RouteAcross(ctx, testPlayerID, "PROBE-A", "X1-AA-MARKET", "X1-BB-M1"))
	require.Zero(t, world.jumpCount(), "step one is a navigate, never a jump")
	require.Zero(t, world.shipReads,
		"the cooldown is consulted only on the branch that would JUMP — reading it on the flight-to-gate branch would "+
			"cost a query per tick for an answer that branch does not use")
}

// The charting seed walks gates through the SAME shared step, so it must hold on
// the same fact. The bead named only the placement path; the defect was in the
// helper both walkers share.
func TestSeedJumpTo_HoldsTheGateStepWhileTheHullIsOnCooldown(t *testing.T) {
	logger := &recordingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	expiry := time.Now().Add(201 * time.Second)
	world := &cooldownWorld{gate: "X1-AA-G1", ship: probeWithCooldown(t, "SEED-1", &expiry)}
	seed := adapterSensing.NewSeedCommandPort(world, nil, nil, nil, nil, oneGateApart())

	err := seed.JumpTo(ctx, testPlayerID, "SEED-1", "X1-AA-G1", "X1-BB")

	require.Zero(t, world.jumpCount(), "the charting seed shares stepThroughGate and must share its cooldown hold")
	require.Error(t, err, "a held seed hop must still be reported, so the errand holds and the next tick retries")
	require.Len(t, logger.withAction("parked_sensing_gate_step_cooldown_hold"), 1)
}
