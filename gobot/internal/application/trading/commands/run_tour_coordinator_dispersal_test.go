package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Spawn dispersal. Hulls are born at one yard, so a stack of trade hulls shares that
// system and drains its local margins the moment they start touring. Waiting out the
// breathing retries there re-prices a ground the stack has already exhausted; when the
// system is crowded AND this hull found no plan, the reposition discovery already armed
// for margins-death is the right answer immediately.

// spawnDispersalPlanner returns no plan at the crowded home ground and a floor-clearing
// tour at the reachable neighbour, so the only thing under test is WHEN the rescue is
// reached.
func spawnDispersalPlanner() *tourFakeRoutingClient {
	s2Calls := 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S2" {
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2()
			}
		}
		return infeasibleTour()
	}}
}

// homePlansBeforeLeaving counts the plans priced at the home ground before the first one
// priced anywhere else — the number of times the hull re-tried a system it had already
// found nothing in.
func homePlansBeforeLeaving(positions []string, home string) int {
	for i, p := range positions {
		if shared.ExtractSystemSymbol(p) != home {
			return i
		}
	}
	return len(positions)
}

func crowdedHulls(system string, n int) []activeHull {
	hulls := make([]activeHull, 0, n)
	for i := 0; i < n; i++ {
		hulls = append(hulls, activeHull{system: system, fleet: tradeFleet})
	}
	return hulls
}

// THE UNLOCK. A no-plan verdict on a system this hull shares with a stack of trade hulls
// goes straight to reposition discovery — one plan priced at home, then the candidate
// pre-flight and the jump.
func TestTour_CrowdedGround_DispersesWithoutTheBreathingRetries(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = crowdedHulls("X1-S1", defaultSpawnDispersalMinOtherHulls)
	planner := spawnDispersalPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SPAWN", PlayerID: 1, ContainerID: "ctr-spawn", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("dispersal run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if got := homePlansBeforeLeaving(planner.positions, "X1-S1"); got != 1 {
		t.Fatalf("a crowded ground with no plan must reach reposition discovery immediately, got %d home plans first (%v)", got, planner.positions)
	}
	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition off the crowded ground, got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected one jump to the fresh ground, got %v", fx.jumps)
	}
}

// An UNCROWDED ground is unchanged: the hull breathes out its full starvation streak
// before the same rescue runs, because a system it does not share may simply be between
// cycles.
func TestTour_UncrowdedGround_KeepsTheBreathingRetries(t *testing.T) {
	fx := repositionFixture()
	planner := spawnDispersalPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ALONE", PlayerID: 1, ContainerID: "ctr-alone", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if got := homePlansBeforeLeaving(planner.positions, "X1-S1"); got != tourStarvationLimit {
		t.Fatalf("an uncrowded ground must keep the full breathing streak, got %d home plans first (%v)", got, planner.positions)
	}
	if r.Repositions != 1 {
		t.Fatalf("expected the margins-death reposition to still rescue the run, got %d", r.Repositions)
	}
}

// One hull short of the threshold is not a stack: the dispersal nudge must not fire, so a
// lightly-shared system keeps today's behaviour exactly.
func TestTour_BelowTheCrowdThreshold_KeepsTheBreathingRetries(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = crowdedHulls("X1-S1", defaultSpawnDispersalMinOtherHulls-1)
	planner := spawnDispersalPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PAIR", PlayerID: 1, ContainerID: "ctr-pair", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got := homePlansBeforeLeaving(planner.positions, "X1-S1"); got != tourStarvationLimit {
		t.Fatalf("below the threshold the breathing streak must stand, got %d home plans first (%v)", got, planner.positions)
	}
}

// Non-trade hulls sharing the system are not the herd that drains a trading ground, so
// they never trip the nudge.
func TestTour_NonTradeHullsDoNotCountAsACrowd(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = crowdedHulls("X1-S1", defaultSpawnDispersalMinOtherHulls+2)
	for i := range fx.activeHulls {
		fx.activeHulls[i].fleet = "contract"
	}
	planner := spawnDispersalPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MIXED", PlayerID: 1, ContainerID: "ctr-mixed", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got := homePlansBeforeLeaving(planner.positions, "X1-S1"); got != tourStarvationLimit {
		t.Fatalf("only trade hulls form the herd, got %d home plans first (%v)", got, planner.positions)
	}
}

// The nudge asks the reposition engine ONCE per episode. A ranking costs a planner call
// per candidate, so re-asking on every breathing retry would multiply the solver fan-out on
// exactly the ground that has nothing to offer — the confirmed margins-death rescue still
// ranks again, against market data re-read since.
func TestTour_CrowdedGround_AsksTheRescueOncePerEpisode(t *testing.T) {
	fx := repositionFixture()
	fx.activeHulls = crowdedHulls("X1-S1", defaultSpawnDispersalMinOtherHulls)
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // nowhere is worth the jump, so every ranking declines
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ONCE", PlayerID: 1, ContainerID: "ctr-once", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	rankings := 0
	for _, p := range planner.positions {
		if shared.ExtractSystemSymbol(p) == "X1-S2" {
			rankings++
		}
	}
	if rankings != 2 {
		t.Fatalf("expected one dispersal ranking plus the confirmed margins-death ranking, got %d (%v)", rankings, planner.positions)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("a ground with nowhere better to be must still exit honestly, got %q", r.ExitReason)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("nothing cleared the relocation floor, so the hull must not have moved: %v", fx.jumps)
	}
}

// The threshold is an operator lever: a tuned value on the live trade-fleet surface
// governs, and an untuned surface resolves the documented default.
func TestSpawnDispersalMinOtherHulls_ResolvesFromTheLiveTuneSurface(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	if got := h.spawnDispersalMinOtherHulls(context.Background(), 1); got != defaultSpawnDispersalMinOtherHulls {
		t.Errorf("unwired resolver must give the documented default, got %d", got)
	}

	h.SetMarketFreshness(NewMarketFreshness(nil, &fakeFloorSource{
		config: liveconfig.Snapshot{TuneKeySpawnDispersalMinOtherHulls: 7},
	}, nil))
	if got := h.spawnDispersalMinOtherHulls(context.Background(), 1); got != 7 {
		t.Errorf("a tuned threshold must govern, got %d", got)
	}
}
