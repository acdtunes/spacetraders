package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtRankedHas reports whether the ranking names system.
func mvtRankedHas(ranked []mvt.ScoredSystem, system string) bool {
	for _, s := range ranked {
		if s.System == system {
			return true
		}
	}
	return false
}

// mvtRankedHead is the first system of a ranking, or "" when it is empty.
func mvtRankedHead(ranked []mvt.ScoredSystem) string {
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0].System
}

// A hull that drained X1-S2 minutes ago must not re-claim it the moment its neighbour empties
// — that shuttle is what turned a 128k gate fee into a 300k one. The preference SINKS drained
// ground rather than removing it, so nothing downstream of it can idle a hull: the fee guard
// can still fall back to it and a reach that offers nothing else still moves.
func TestMVTRankReach_DemotesRecentlyLeftSystem(t *testing.T) {
	rank := func(t *testing.T, depths map[string]int, leftAt map[string]time.Time) mvtRanking {
		t.Helper()
		fx := repositionFixture()
		h, _, _ := mvtChainHandler(t, fx, depths, "X1-S1", "X1-S2", "X1-S3")
		ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
		cmd := mvtCmd(t)
		h.mvtState(cmd).leftAt = leftAt
		ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			t.Fatalf("load ship: %v", err)
		}
		rk, err := h.mvtRankReach(ctx, cmd, ship, 2, mvtSpreadFloor(cmd))
		if err != nil {
			t.Fatalf("rank reach: %v", err)
		}
		return rk
	}
	rich := map[string]int{"X1-S2": 500, "X1-S3": 500}

	rk := rank(t, rich, map[string]time.Time{"X1-S2": time.Now().Add(-5 * time.Minute)})
	if rk.demoted != 1 || rk.preferred != 1 || mvtRankedHead(rk.best()) != "X1-S3" || !mvtRankedHas(rk.best(), "X1-S2") {
		t.Fatalf("ranked = %+v demoted = %d, want X1-S3 ahead of a still-reachable X1-S2", rk.best(), rk.demoted)
	}

	rk = rank(t, rich, map[string]time.Time{"X1-S2": time.Now().Add(-3 * time.Hour)})
	if rk.demoted != 0 || mvtRankedHead(rk.best()) != "X1-S2" {
		t.Fatalf("ranked = %+v demoted = %d, want X1-S2 back on top once the window has passed", rk.best(), rk.demoted)
	}

	rk = rank(t, map[string]int{"X1-S2": 500}, map[string]time.Time{"X1-S2": time.Now().Add(-5 * time.Minute)})
	if rk.preferred != 0 || mvtRankedHead(rk.best()) != "X1-S2" {
		t.Fatalf("ranked = %+v preferred = %d, want the only candidate taken rather than an idle hull", rk.best(), rk.preferred)
	}
}

// The escalation must be able to widen PAST a ring that offers only drained ground: at reach 1
// the just-emptied X1-S2 is the only candidate, so the hull looks two hops out and claims the
// rich X1-S3 instead of shuttling straight back (sp-htzl1.5 review round 1).
func TestMVTClaimAndTravel_EscalatesPastARecentlyLeftRing(t *testing.T) {
	fx := repositionFixture()
	h, claims, _ := mvtChainHandler(t, fx, map[string]int{"X1-S2": 500, "X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtEscalationCmd(t, 2)
	h.mvtState(cmd).leftAt = map[string]time.Time{"X1-S2": time.Now().Add(-5 * time.Minute)}

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the escalated claim flown", moved, err)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S3" {
		t.Fatalf("claim = %+v ok=%v, want X1-S3 rather than the ground just drained", c, ok)
	}
	if got := mvtEscalatedTo(logger); got != 2 {
		t.Fatalf("reach escalated to_hops = %d, want 2 (the drained ring is no offer)", got)
	}
}

// The preference is not a bound: with the drained X1-S2 the only ground anywhere inside the cap
// that beats a thin home, the hull flies back to it rather than sitting still.
func TestMVTClaimAndTravel_TakesTheRecentlyLeftSystemAtTheCap(t *testing.T) {
	fx := repositionFixture()
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 100, 100, 400, now),
		"X1-S2": mvtLane("X1-S2", "IRON", 100, 100, 1100, now),
	}
	h, claims, _ := mvtChainHandlerLanes(t, fx, lanes, "X1-S1", "X1-S2")
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtEscalationCmd(t, 2)
	h.mvtState(cmd).leftAt = map[string]time.Time{"X1-S2": now.Add(-5 * time.Minute)}

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the drained neighbour taken back rather than an idle hull", moved, err)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v, want X1-S2", c, ok)
	}
}

// The exclusion is fed by departures: a flight stamps the system the hull LEFT, never the one
// it claimed.
func TestMVTTravelTo_StampsLeftAtOnDeparture(t *testing.T) {
	fx := repositionFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtCmd(t)

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the jump flown", moved, err)
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	defer st.mu.Unlock()
	at, ok := st.leftAt["X1-S1"]
	if !ok || time.Since(at) > time.Minute {
		t.Fatalf("leftAt = %v (present %v), want X1-S1 stamped at the departure", st.leftAt, ok)
	}
	if _, claimed := st.leftAt["X1-S2"]; claimed {
		t.Fatalf("leftAt = %v, want nothing stamped for the system just claimed", st.leftAt)
	}
}

// The shipped default binds without any configuration.
func TestMVTRecentlyLeftWindow_DefaultsToTheShippedMinutes(t *testing.T) {
	if got := mvtRecentlyLeftWindow(&RunTourCoordinatorCommand{}); got != time.Duration(DefaultMVTRecentlyLeftMinutes)*time.Minute {
		t.Fatalf("unset window = %v, want %d minutes", got, DefaultMVTRecentlyLeftMinutes)
	}
	if got := mvtRecentlyLeftWindow(&RunTourCoordinatorCommand{MVTRecentlyLeftMinutes: 15}); got != 15*time.Minute {
		t.Fatalf("configured window = %v, want 15m", got)
	}
}
