package commands

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtJumpFeeRefusals renders every jump-fee refusal line the run wrote as system@gate_fee.
func mvtJumpFeeRefusals(l *metaCapturingLogger) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.entries {
		if !strings.Contains(e.message, "jump fee guard refused") {
			continue
		}
		sys, _ := e.metadata["system"].(string)
		fee, _ := e.metadata["gate_fee"].(int64)
		out = append(out, sys+"@"+strconv.FormatInt(fee, 10))
	}
	return out
}

// mvtJumpFeeHandler is mvtHandler with an unpriced home, a rich X1-S2 one hop out and the
// given fee charged by the gate the hull would leave — the incident's shape: a hot gate the
// ranker still elects to cross because the yield estimate at the far end is rich.
//
// The neighbour carries 100 units at 500/unit against a 100-unit hold, so one load there is
// expected to make 50,000 credits and the guard's threshold is share% of that.
func mvtJumpFeeHandler(t *testing.T, fx *tourFixture, fee int64) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 500)
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": fee}})
	h.SetGateGraph(mvtTravelGraph())
	return h, claims, trans
}

// THE INCIDENT. The gate out of X1-S1 charges 300k, one load at X1-S2 is worth 50k, and the
// ranker still names X1-S2 because it is the only ground with depth. The guard refuses the
// claim before anything is written: no registry row at the target, no flight, one stay.
func TestMVTTravelTo_JumpFeeGuardRefusesExpensiveJump(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtJumpFeeHandler(t, fx, 300_000)
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 20

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want the jump refused", moved, err, fx.jumps)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S1" {
		t.Fatalf("claim = %+v ok=%v, want the hull still claiming X1-S1", c, ok)
	}
	if got := trans.last(t); got.Reason != mvtReasonJumpFeeGuard || got.System != "X1-S1" {
		t.Fatalf("last transition = %+v, want %s at X1-S1", got, mvtReasonJumpFeeGuard)
	}
	if got := mvtJumpFeeRefusals(logger); len(got) != 1 || got[0] != "X1-S2@300000" {
		t.Fatalf("refusal lines = %v, want exactly one naming X1-S2 at 300000", got)
	}
}

// The knob is monotone and the guard only ever refuses: the same 40k fee that a 20% share
// rejects (40k > 10k) is flown at 100% (40k < the 50k load it buys).
func TestMVTTravelTo_JumpFeeGuardShareOneHundredNeverRefusesBelowTheLoad(t *testing.T) {
	fx := repositionFixture()
	h, claims, _ := mvtJumpFeeHandler(t, fx, 40_000)
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 100

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved || len(fx.jumps) != 1 {
		t.Fatalf("moved=%v err=%v jumps=%v, want the jump flown", moved, err, fx.jumps)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}

	tight := repositionFixture()
	h, _, trans := mvtJumpFeeHandler(t, tight, 40_000)
	cmd = mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 20
	if moved, err = h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{}); err != nil || moved {
		t.Fatalf("moved=%v err=%v, want the same fee refused at the shipped share", moved, err)
	}
	if got := trans.last(t); got.Reason != mvtReasonJumpFeeGuard {
		t.Fatalf("last transition = %+v, want %s", got, mvtReasonJumpFeeGuard)
	}
}

// A refusal is not a stay by itself: the guard drops the candidates it refuses and the hull
// takes the best jump that survives.
func TestMVTTravelTo_JumpFeeGuardPicksTheNextCandidateWhenTheBestFailsTheGuard(t *testing.T) {
	fx := repositionFixture()
	h, claims, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 500)
	h.SetGateGraph(mvtTravelGraph())
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 20
	// Rank never scores a load without a visit worth at least as much, so a hand-built ranking
	// carries both: the guard reads the visit, and a bare load estimate is not a ranking Rank
	// could have produced (sp-htzl1.7).
	ranked := []mvt.ScoredSystem{
		{System: "X1-S9", Hops: 1, EntryWaypoint: "X1-S9-SRC", Score: 9, GateFee: 300_000,
			ExpectedLoadCredits: 500_000, ExpectedVisitCredits: 500_000},
		{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1, GateFee: 5_000,
			ExpectedLoadCredits: 500_000, ExpectedVisitCredits: 500_000},
	}

	moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, map[string]int{}, ranked, mvtReasonBootstrap, 0, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the surviving candidate flown", moved, err)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v, want X1-S2", c, ok)
	}
	if got := mvtJumpFeeRefusals(logger); len(got) != 1 || got[0] != "X1-S9@300000" {
		t.Fatalf("refusal lines = %v, want exactly one naming X1-S9", got)
	}
}

// A candidate the ranker expects to earn NOTHING cannot justify any fee at all: the guard
// fails closed on it rather than dividing by a yield that is not there.
func TestMVTTravelTo_JumpFeeGuardRefusesAValuelessLoad(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1, GateFee: 1}}

	moved, err := h.mvtTravelTo(ctx, mvtCmd(t), &RunTourCoordinatorResponse{}, nil, map[string]int{}, ranked, mvtReasonBootstrap, 0, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a stay", moved, err, fx.jumps)
	}
	if got := trans.last(t); got.Reason != mvtReasonJumpFeeGuard {
		t.Fatalf("last transition = %+v, want %s", got, mvtReasonJumpFeeGuard)
	}
}

// The guard and the recently-left preference must not idle a hull between them: every
// candidate at one hop carries the SAME fee, so the guard discriminates on the load — and the
// richest load is exactly the ground the hull just drained. X1-S3 (50k a load) cannot pay the
// 120k two-hop fee; the demoted X1-S2 (490k a load) can pay its 60k, so it is flown rather than
// refused into a stay (sp-htzl1.5 review round 1).
func TestMVTClaimAndTravel_JumpFeeGuardFliesTheDrainedNeighbourRatherThanIdle(t *testing.T) {
	fx := repositionFixture()
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{
		"X1-S2": mvtLane("X1-S2", "IRON", 100, 100, 5000, now),
		"X1-S3": mvtRichLane("X1-S3", 500, now),
	}
	h, claims, _ := mvtChainHandlerLanes(t, fx, lanes, "X1-S1", "X1-S2")
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": 60_000}})
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 20
	h.mvtState(cmd).leftAt = map[string]time.Time{"X1-S2": now.Add(-5 * time.Minute)}

	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the affordable jump flown rather than a stay", moved, err)
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 — the one candidate the guard admits", c, ok)
	}
	if got := mvtJumpFeeRefusals(logger); len(got) != 1 || got[0] != "X1-S3@120000" {
		t.Fatalf("refusal lines = %v, want exactly one naming X1-S3 at 120000", got)
	}
}

// mvtDrainedNeighbourRun runs one CLAIM from a priced home over the drained X1-S2 one hop out
// and the fresh X1-S3 two hops out, each lane at the credits-per-unit given. The gate out of
// home charges 60k, so X1-S3 pays 120k and X1-S2 60k against 20% of what a load there earns.
func mvtDrainedNeighbourRun(t *testing.T, homeSpread, s2Spread, s3Spread int) (*tourFixture, *mvtFakeClaims, *mvtFakeTransitions, bool) {
	t.Helper()
	fx := repositionFixture()
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 100, 100, 100+homeSpread, now),
		"X1-S2": mvtLane("X1-S2", "IRON", 100, 100, 100+s2Spread, now),
		"X1-S3": mvtLane("X1-S3", "IRON", 100, 100, 100+s3Spread, now),
	}
	h, claims, trans := mvtChainHandlerLanes(t, fx, lanes, "X1-S1", "X1-S2")
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": 60_000}})
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtCmd(t)
	cmd.MVTJumpFeeMaxSharePct = 20
	h.mvtState(cmd).leftAt = map[string]time.Time{"X1-S2": now.Add(-5 * time.Minute)}
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil {
		t.Fatalf("claim and travel: %v", err)
	}
	return fx, claims, trans, moved
}

// With home itself priced, the demoted neighbour is taken only because the ranker scores it
// ABOVE standing still — the promotion is a fallback, never a widening.
func TestMVTClaimAndTravel_JumpFeeGuardPromotesTheDrainedNeighbourOverAPricedHome(t *testing.T) {
	_, claims, _, moved := mvtDrainedNeighbourRun(t, 700, 5000, 3000)
	if !moved {
		t.Fatal("moved=false, want the affordable X1-S2 promoted over a stay")
	}
	ctx := context.Background()
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v, want X1-S2", c, ok)
	}
}

// The same shape with the drained neighbour affordable but scoring BELOW home is a stay: a
// refusal must never send a hull to ground the ranker rates worse than where it stands.
func TestMVTClaimAndTravel_JumpFeeGuardNeverWidensBelowStandingStill(t *testing.T) {
	fx, claims, trans, moved := mvtDrainedNeighbourRun(t, 3000, 3000, 5000)
	if moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v jumps=%v, want a stay", moved, fx.jumps)
	}
	if c, ok, _ := claims.Get(context.Background(), 1, "TOUR-MVT"); !ok || c.System != "X1-S1" {
		t.Fatalf("claim = %+v ok=%v, want the hull still at X1-S1", c, ok)
	}
	if got := trans.last(t); got.Reason != mvtReasonJumpFeeGuard {
		t.Fatalf("last transition = %+v, want %s", got, mvtReasonJumpFeeGuard)
	}
}

// mvtVisitLegs is one hull's realised buy/sell inside ONE system, so ComputeFleetStats sees a
// single system visit worth margin credits. rfSeed's legs carry no waypoint at all, which is why
// every other MVT fixture reports no visits and so no fleet visit basis.
func mvtVisitLegs(margin int) []trading.TourLegTelemetry {
	base := time.Now().Add(-time.Hour)
	return []trading.TourLegTelemetry{
		{TourID: "visit", ShipSymbol: "TOUR-MVT", PlayerID: 1, Waypoint: "X1-S1-A", IsBuy: true,
			RealizedUnits: 100, RealizedUnitPrice: 1000, RealizedAt: base},
		{TourID: "visit", ShipSymbol: "TOUR-MVT", PlayerID: 1, Waypoint: "X1-S1-B", IsBuy: false,
			RealizedUnits: 100, RealizedUnitPrice: 1000 + margin/100, RealizedAt: base.Add(time.Minute)},
	}
}

// mvtVisitBasisHandler is the live incident's shape: an unpriced home, a THIN X1-S2 one hop out
// carrying 6 units at a 4751 spread (one load worth 28,506 — the live median), the gate out
// charging fee, and the fleet telemetry given.
func mvtVisitBasisHandler(t *testing.T, fx *tourFixture, fee int64, rows []trading.TourLegTelemetry) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rows})
	claims, trans := newMVTFakeClaims(), &mvtFakeTransitions{}
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S2": mvtLane("X1-S2", "IRON", 6, 100, 4851, time.Now()),
	}}, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 1})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": fee}})
	h.SetRankerAgeCaps(mvtCaps())
	h.SetGateGraph(mvtTravelGraph())
	return h, claims, trans
}

// THE MIS-SCALED BASIS. One matched load at the target is worth 28,506, but a whole visit earns
// the fleet 200,000 as the market replenishes — so an ordinary 10,524 gate is 5% of what the
// crossing buys, not 37%. At the shipped 20% share the old one-load basis refused it (17 hulls
// held 105 times in an hour); the visit basis admits it and still refuses the 300k shuttle fee.
// With no fleet stats at all the load estimate is the whole basis, exactly as it was.
func TestMVTTravelTo_JumpFeeGuardUsesVisitBasis(t *testing.T) {
	cases := []struct {
		name  string
		fee   int64
		share int
		rows  []trading.TourLegTelemetry
		want  bool
	}{
		{"ordinary gate against a 200k visit", 10_524, 20, mvtVisitLegs(200_000), true},
		{"shuttle fee against a 200k visit", 300_000, 20, mvtVisitLegs(200_000), false},
		{"no fleet stats, shipped share", 10_524, 20, rfSeed("TOUR-MVT", 100000), false},
		{"no fleet stats, emergency share", 10_524, 100, rfSeed("TOUR-MVT", 100000), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := repositionFixture()
			h, claims, trans := mvtVisitBasisHandler(t, fx, c.fee, c.rows)
			ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
			cmd := mvtCmd(t)
			cmd.MVTJumpFeeMaxSharePct = c.share

			moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, map[string]int{}, mvtReasonBootstrap, tourPlanBudget{})
			if err != nil || moved != c.want {
				t.Fatalf("moved=%v err=%v, want moved=%v", moved, err, c.want)
			}
			if !c.want {
				if got := trans.last(t); got.Reason != mvtReasonJumpFeeGuard {
					t.Fatalf("last transition = %+v, want %s", got, mvtReasonJumpFeeGuard)
				}
				return
			}
			if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
				t.Fatalf("claim = %+v ok=%v, want X1-S2", c, ok)
			}
		})
	}
}

// The shipped default binds without any configuration: an absent knob is 20%.
func TestMVTJumpFeeMaxShare_DefaultsToTheShippedShare(t *testing.T) {
	if got := mvtJumpFeeMaxShare(&RunTourCoordinatorCommand{}); got != DefaultMVTJumpFeeMaxSharePct {
		t.Fatalf("unset share = %d, want %d", got, DefaultMVTJumpFeeMaxSharePct)
	}
	if got := mvtJumpFeeMaxShare(&RunTourCoordinatorCommand{MVTJumpFeeMaxSharePct: 35}); got != 35 {
		t.Fatalf("configured share = %d, want 35", got)
	}
}
