package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtRichLane is credits of depth at 500/unit — clear of the ranker's shipped spread floor, so
// these fixtures keep meaning "there is money here" — over a fifth of the volume, which leaves
// each system's credits (and so the in-transit draw against them) exactly what it always was.
func mvtRichLane(system string, credits int, now time.Time) []mvt.LaneDepth {
	return mvtLane(system, "IRON", credits/5, 100, 600, now)
}

// mvtHandler wires a tour handler with MVT fakes over the shared reposition fixture
// (home X1-S1, neighbour X1-S2). depthS1/depthS2 are the credits of fresh depth in each.
func mvtHandler(t *testing.T, fx *tourFixture, planner *tourFakeRoutingClient, depthS1, depthS2 int) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: rfSeed("TOUR-MVT", 100000)})
	claims, trans := newMVTFakeClaims(), &mvtFakeTransitions{}
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{}
	if depthS1 > 0 {
		lanes["X1-S1"] = mvtRichLane("X1-S1", depthS1, now)
	}
	if depthS2 > 0 {
		lanes["X1-S2"] = mvtRichLane("X1-S2", depthS2, now)
	}
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: lanes}, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 1})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{}})
	h.SetRankerAgeCaps(mvtCaps())
	return h, claims, trans
}

// mvtTravelGraph is the stored X1-S1<->X1-S2 adjacency the ranker's reach walks, carrying the
// one-hop path the relaxed jump resolver answers so a CLAIM of X1-S2 really jumps.
func mvtTravelGraph() *fakeGateGraph {
	g := mvtStoredGraph([2]string{"X1-S1", "X1-S2"})
	g.path = []string{"X1-S1", "X1-S2"}
	return g
}

func mvtCmd(t *testing.T) *RunTourCoordinatorCommand {
	t.Helper()
	return &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MVT", PlayerID: 1, ContainerID: "ctr-mvt", Iterations: -1, MVTLoop: true,
		RepositionMinMargin: isolateLegacyReposition, PlacementDisabled: true,
		ModelArtifactPath: writeTourArtifact(t),
		YieldWindowSells:  8, YieldMinSells: 3, ClaimReachHops: 2, SpecialistCadenceMinutes: 60,
	}
}

func TestTourSystemsFrom_PinnedToClaimUnderMVT(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 10)
	cmd := mvtCmd(t)
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S1" {
		t.Fatalf("no claim → home only, got %v", got)
	}
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S1" || len(trans.rows) != 0 {
		t.Fatalf("claim at home → home only and no row, got %v rows=%v", got, mvtSeen(trans.rows))
	}
	old := mvtCmd(t)
	old.MVTLoop = false
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", old); len(got) < 2 {
		t.Fatalf("old path must keep home + one hop, got %v", got)
	}
}

// The claim says X1-S2 but the plan anchors on X1-S1, where the hull now stands after a
// mover that does not own the claim (disposal, offload, retirement, a tag flip). The scope
// follows the hull, never the stale claim, and the registry learns the new presence once.
func TestTourSystemsFrom_ClaimElsewhereRepinsToWhereTheHullStands(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 10)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, true)
	h.mvtState(cmd).claimed = "X1-S2"
	if got := h.tourSystemsFrom(ctx, "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S1" {
		t.Fatalf("scope must follow the hull, got %v", got)
	}
	if st := h.mvtState(cmd); st.claimed != "X1-S1" {
		t.Fatalf("claimed = %q, want re-pinned to X1-S1", st.claimed)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("registry must show the hull present in X1-S1, got %+v ok=%v", c, ok)
	}
	want := "TRADE>TRADE:" + mvtReasonRelocated + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
	if got := h.tourSystemsFrom(ctx, "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S1" || len(trans.rows) != 1 {
		t.Fatalf("a settled scope must not re-record: got %v rows=%v", got, mvtSeen(trans.rows))
	}
}

// The bootstrap alone (the loop's later empty exits are pinned elsewhere): a profitable home
// wins on zero travel and the hull stays.
func TestMVTBootstrap_StaysWhenHomeIsBest(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 10)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if err := h.mvtRecover(ctx, mvtCmd(t), &RunTourCoordinatorResponse{}, &repositionEpisode{}, tourPlanBudget{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("bootstrap must claim home with arrival stamped: %+v ok=%v", c, ok)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("hull must not move: jumps=%v", fx.jumps)
	}
	want := "CLAIM>TRADE:" + mvt.ReasonStay + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
}

func TestMVTBootstrap_ClaimsAndTravelsToRicherNeighbour(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v", c, ok)
	}
	if len(fx.jumps) == 0 {
		t.Fatal("hull must jump to X1-S2")
	}
	var seen []string
	for _, r := range trans.rows {
		seen = append(seen, string(r.From)+">"+string(r.To)+":"+r.Reason)
	}
	want := []string{"TRADE>CLAIM:bootstrap", "CLAIM>TRAVEL:claim", "TRAVEL>TRADE:arrived"}
	for i, w := range want {
		if i >= len(seen) || seen[i] != w {
			t.Fatalf("transitions = %v, want prefix %v", seen, want)
		}
	}
	if st := h.mvtState(mvtCmd(t)); st.claimed != "X1-S2" {
		t.Fatalf("claimed = %q", st.claimed)
	}
}

func TestMVTTravelFailure_ReleasesClaimAndStays(t *testing.T) {
	fx := repositionFixture()
	// The departure hop to the source gate fails, so the hull never leaves X1-S1.
	fx.navFail = map[string]bool{"X1-S1-GATE": true}
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); ok {
		t.Fatal("failed travel must release the claim")
	}
	found := false
	for _, r := range trans.rows {
		if r.Reason == mvtReasonTravelFailed && r.From == mvt.StateTravel && r.To == mvt.StateTrade && r.System == "X1-S1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no travel_failed transition in %+v", trans.rows)
	}
}

// mvtSeedClaim plants the registry row a restart would find for TOUR-MVT.
func mvtSeedClaim(claims *mvtFakeClaims, system string, at time.Time, arrived bool) {
	c := mvt.Claim{Hull: "TOUR-MVT", System: system, ClaimedAt: at}
	if arrived {
		c.ArrivedAt = &at
	}
	claims.rows["TOUR-MVT"] = c
}

// mvtSeen renders transitions as FROM>TO:reason@system for prefix/equality assertions.
func mvtSeen(rows []mvt.Transition) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r.From)+">"+string(r.To)+":"+r.Reason+"@"+r.System)
	}
	return out
}

var mvtSeededAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// The hull stands in X1-S2 with an arrived claim on X1-S2, and X1-S1 is richer and in reach: a
// recovery that wrongly bootstrapped would jump and write rows. The claim wins untouched.
func TestMVTRecover_ArrivedClaimPinsScopeWithoutTouchingRegistry(t *testing.T) {
	fx := repositionFixture()
	fx.location = "X1-S2-A"
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 10)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, true)
	cmd := mvtCmd(t)
	if err := h.mvtRecover(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, tourPlanBudget{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if st := h.mvtState(cmd); st.claimed != "X1-S2" {
		t.Fatalf("claimed = %q, want the claim's X1-S2", st.claimed)
	}
	if len(trans.rows) != 0 || len(fx.jumps) != 0 {
		t.Fatalf("an arrived claim must neither record nor move: rows=%v jumps=%v", mvtSeen(trans.rows), fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || !c.ClaimedAt.Equal(mvtSeededAt) || c.ArrivedAt == nil || !c.ArrivedAt.Equal(mvtSeededAt) {
		t.Fatalf("registry row must be untouched, got %+v ok=%v", c, ok)
	}
}

// The arrived claim says X1-S2 but the hull stands in X1-S1 (moved by a path that does not own
// the claim). Adopting it would pin the solver to a system the hull is not in, so the stale row
// is released and the loop bootstraps from where the hull really is.
func TestMVTRecover_ArrivedClaimElsewhereReleasesAndBootstraps(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, true)
	cmd := mvtCmd(t)
	if err := h.mvtRecover(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, tourPlanBudget{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if claims.released != 1 {
		t.Fatalf("the stale row must be released exactly once, got %d", claims.released)
	}
	want := []string{"TRADE>CLAIM:" + mvtReasonBootstrap + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S2", "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S2"}
	if got := mvtSeen(trans.rows); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil || c.ClaimedAt.Equal(mvtSeededAt) {
		t.Fatalf("bootstrap must claim afresh from X1-S1, got %+v ok=%v", c, ok)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("jumps = %v, want the bootstrap's one jump", fx.jumps)
	}
}

func TestMVTRecover_CompletedJumpResumeMarksArrived(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, false)
	cmd := mvtCmd(t)
	resumed := repositionEpisode{repositioned: true, fromSystem: "X1-S1", toSystem: "X1-S2"}
	if err := h.mvtRecover(ctx, cmd, &RunTourCoordinatorResponse{}, &resumed, tourPlanBudget{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil || !c.ClaimedAt.Equal(mvtSeededAt) {
		t.Fatalf("the seeded claim must be marked arrived in place, got %+v ok=%v", c, ok)
	}
	if st := h.mvtState(cmd); st.claimed != "X1-S2" {
		t.Fatalf("claimed = %q, want the resumed X1-S2", st.claimed)
	}
	want := "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S2"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("recovery itself must not fly: jumps=%v", fx.jumps)
	}
}

// No gate graph: the bootstrap ranks home alone, so releasing the stale X1-S2 claim ends in a
// stay at X1-S1 rather than a flight.
func TestMVTRecover_UnarrivedClaimWithoutResumeReleasesThenBootstraps(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 10)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, false)
	cmd := mvtCmd(t)
	if err := h.mvtRecover(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, tourPlanBudget{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if claims.released != 1 {
		t.Fatalf("the stale claim must be released exactly once, got %d", claims.released)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil || c.ClaimedAt.Equal(mvtSeededAt) {
		t.Fatalf("bootstrap must re-stamp home on a fresh row, got %+v ok=%v", c, ok)
	}
	if st := h.mvtState(cmd); st.claimed != "X1-S1" {
		t.Fatalf("claimed = %q, want the bootstrap's X1-S1", st.claimed)
	}
	want := "CLAIM>TRADE:" + mvt.ReasonStay + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
}

// A hull re-adopted mid-jump resumes through the old path's persisted episode, which carries no
// MVT hop bound, so the resume rides the old-path knob's default; recovery then closes the
// TRAVEL with the arrival on the claim the jump was flown for. The resumed jump spends the
// episode, so the dead ground it lands on cannot be left again before a productive tour.
func TestMVTRecover_ReadoptedMidJumpRidesOldPathBoundThenArrives(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, mvtDeadPlanner(), 10, 500)
	g := mvtTravelGraph()
	h.SetGateGraph(g)
	h.SetRepositionPersister(&fakeRepositionPersister{})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	mvtSeedClaim(claims, "X1-S2", mvtSeededAt, false)
	cmd := mvtCmd(t)
	cmd.RepositionInProgress, cmd.RepositionTargetSystem, cmd.RepositionTargetWaypoint = true, "X1-S2", "X1-S2-SRC"
	if _, err := h.Handle(ctx, cmd); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the resume must complete the one jump, got %v", fx.jumps)
	}
	if g.repositionBound != repositionJumpBoundDefault {
		t.Fatalf("resume bound = %d, want the old-path default %d", g.repositionBound, repositionJumpBoundDefault)
	}
	seen := mvtSeen(trans.rows)
	if want := "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S2"; len(seen) == 0 || seen[0] != want {
		t.Fatalf("first transition = %v, want %s", seen, want)
	}
	for _, r := range trans.rows {
		if r.Reason == mvtReasonBootstrap {
			t.Fatalf("a resumed claim must not bootstrap: %v", seen)
		}
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
}

// The jump lands but the gate->market hop fails: the hull stands in X1-S2, so the loop counts
// the arrival and pins there instead of pinning back to the origin and flying home.
func TestMVTTravelTo_PostJumpHopFailurePinsWhereTheHullStands(t *testing.T) {
	fx := repositionFixture()
	fx.navFail = map[string]bool{"X1-S2-SRC": true}
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1}}
	resp := &RunTourCoordinatorResponse{}
	moved, err := h.mvtTravelTo(ctx, cmd, resp, nil, ranked, mvtReasonBootstrap, 0, tourPlanBudget{})
	if err != nil || !moved || resp.Repositions != 1 {
		t.Fatalf("moved=%v err=%v repositions=%d, want the landed jump counted as an arrival", moved, err, resp.Repositions)
	}
	if fx.location != "X1-S2-GATE" {
		t.Fatalf("hull at %q, want stranded on the X1-S2 gate", fx.location)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
	if st := h.mvtState(cmd); st.claimed != "X1-S2" || st.travelFailures != 0 {
		t.Fatalf("claimed=%q failures=%d, want X1-S2 with no failure counted", st.claimed, st.travelFailures)
	}
	want := []string{"TRADE>CLAIM:" + mvtReasonBootstrap + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S2", "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S2"}
	if got := mvtSeen(trans.rows); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

// A graceful stop cancels the ctx while the hull is flying: that is an interrupted flight,
// not a failed one. The claim and the persisted destination stay for the restart to resume,
// nothing counts against the hull, and the run exits resumable with the ctx error.
func TestMVTTravelTo_StopMidFlightKeepsClaimForTheResume(t *testing.T) {
	fx := repositionFixture()
	ctx, cancel := context.WithCancel(common.WithLogger(context.Background(), &tradeCaptureLogger{}))
	defer cancel()
	fx.navHook = func(dest string) error {
		if dest != "X1-S1-GATE" {
			return nil
		}
		cancel()
		return ctx.Err()
	}
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	persister := &fakeRepositionPersister{}
	h.SetRepositionPersister(persister)
	cmd := mvtCmd(t)
	if _, err := h.Handle(ctx, cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("handle err = %v, want the ctx error surfaced resumable", err)
	}
	c, ok, _ := claims.Get(context.Background(), 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt != nil || claims.released != 0 {
		t.Fatalf("claim = %+v ok=%v released=%d, want X1-S2 retained unarrived", c, ok, claims.released)
	}
	states := persister.recorded()
	if len(states) == 0 || !states[len(states)-1].InProgress || states[len(states)-1].TargetSystem != "X1-S2" {
		t.Fatalf("persisted = %+v, want the in-flight destination left set", states)
	}
	if st := h.mvtState(cmd); st.travelFailures != 0 || st.holdSells != 0 {
		t.Fatalf("failures=%d hold=%d, want nothing counted", st.travelFailures, st.holdSells)
	}
	want := []string{"TRADE>CLAIM:" + mvtReasonBootstrap + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S2"}
	if got := mvtSeen(trans.rows); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("transitions = %v, want exactly %v", got, want)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("no jump may complete: %v", fx.jumps)
	}
}

// Every pre-jump failure releases the claim, pins the hull where it stands and counts toward
// the hold; the cap converts the streak into holdSells and resets it.
func TestMVTTravelTo_RepeatedFailuresHoldSells(t *testing.T) {
	fx := repositionFixture()
	fx.navFail = map[string]bool{"X1-S1-GATE": true}
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1}}
	for i := 1; i <= mvtTravelFailureCap; i++ {
		moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonEmpty, 0, tourPlanBudget{})
		if err != nil || moved {
			t.Fatalf("attempt %d: moved=%v err=%v", i, moved, err)
		}
		if _, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); ok {
			t.Fatalf("attempt %d must release the claim", i)
		}
		st := h.mvtState(cmd)
		if st.claimed != "X1-S1" {
			t.Fatalf("attempt %d: claimed = %q, want X1-S1", i, st.claimed)
		}
		if i < mvtTravelFailureCap && (st.travelFailures != i || st.holdSells != 0) {
			t.Fatalf("attempt %d: failures=%d hold=%d", i, st.travelFailures, st.holdSells)
		}
	}
	if st := h.mvtState(cmd); st.travelFailures != 0 || st.holdSells != cmd.YieldWindowSells {
		t.Fatalf("after the cap: failures=%d hold=%d, want 0 and %d", st.travelFailures, st.holdSells, cmd.YieldWindowSells)
	}
	// The hold also expires on the specialist cadence (60 min here), so dead ground with no
	// sells cannot hold the hull forever.
	if st := h.mvtState(cmd); !st.holding(time.Now()) || st.holdUntil.Before(time.Now().Add(59*time.Minute)) || st.holdUntil.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("after the cap: holding=%v holdUntil=%v, want a hold expiring one cadence from now", st.holding(time.Now()), st.holdUntil)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("no attempt may jump: %v", fx.jumps)
	}
	failed := 0
	for _, s := range mvtSeen(trans.rows) {
		if s == "TRAVEL>TRADE:"+mvtReasonTravelFailed+"@X1-S1" {
			failed++
		}
	}
	if failed != mvtTravelFailureCap {
		t.Fatalf("travel_failed rows pinned to X1-S1 = %d, want %d in %v", failed, mvtTravelFailureCap, mvtSeen(trans.rows))
	}
}

// Without SetMVTPorts the whole MVT branch must be inert: no scope pin, no registry
// dereference, and a productive tour followed by the empty-system exit runs to completion.
func TestMVTLoop_UnwiredPortsAreInert(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-MVT", 100000)})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}, tourPlanBudget{}); err != nil {
		t.Fatalf("after-tour: %v", err)
	}
	if st := h.mvtState(cmd); st.claimed != "" {
		t.Fatalf("unwired after-tour must not pin a scope, got %q", st.claimed)
	}
	if moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, []mvt.ScoredSystem{{System: "X1-S2"}}, mvtReasonEmpty, 0, tourPlanBudget{}); err != nil || moved {
		t.Fatalf("unwired travel: moved=%v err=%v", moved, err)
	}
	resp, err := h.Handle(ctx, mvtCmd(t))
	if err != nil {
		t.Fatalf("unwired MVT run: %v", err)
	}
	if r := tourResponse(t, resp); r.ToursCompleted != 1 || len(fx.jumps) != 0 {
		t.Fatalf("tours=%d jumps=%v, want one home tour and no flight", r.ToursCompleted, fx.jumps)
	}
}

func mvtSellLeg(good string, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: "TOUR-MVT", Waypoint: "X1-S1-SNK", Good: good, IsBuy: false,
		RealizedUnits: units, RealizedUnitPrice: price, RealizedAt: at, PlayerID: 1}
}

func mvtBuyLeg(good string, units, price int, at time.Time) trading.TourLegTelemetry {
	l := mvtSellLeg(good, units, price, at)
	l.IsBuy, l.Waypoint = true, "X1-S1-SRC"
	return l
}

func TestMVTObserveLeg_FIFOMarginFeedsTracker(t *testing.T) {
	fx := repositionFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 10)
	cmd := mvtCmd(t)
	cmd.YieldMinSells = 1
	t0 := time.Now()
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 10, 100, t0))
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 10, 200, t0.Add(time.Second)))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 15, 300, t0.Add(2*time.Second))) // (10×200 + 5×100)/15 = 166.67/unit
	h.mvtObserveLeg(cmd, mvtSellLeg("GOLD", 5, 999, t0.Add(3*time.Second)))  // no basis → ignored
	st := h.mvtState(cmd)
	est, ok := st.yield.Estimate()
	if !ok || est < 166.6 || est > 166.7 || st.yield.Sells() != 1 {
		t.Fatalf("estimate=%v ok=%v sells=%d", est, ok, st.yield.Sells())
	}
	old := mvtCmd(t)
	old.MVTLoop = false
	h.mvtObserveLeg(old, mvtBuyLeg("IRON", 1, 1, t0))
	if len(h.mvtState(old).basis["IRON"]) != 1 { // old cmd shares the hull symbol; only the flag differs
		t.Fatal("old path must not record lots")
	}
}

func TestMVTAfterTour_LeavesWhenYieldBelowAlternative(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 500)
	h.SetGateGraph(mvtTravelGraph())
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	t0 := time.Now().Add(-time.Hour) // in the past: the hull's own rate is credits over the span to now
	for i := 0; i < 3; i++ {         // warm: 1 credit/unit here, far below S2's 100/unit
		st.yield.Observe(1, 10, t0.Add(time.Duration(i)*time.Second))
	}
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}, tourPlanBudget{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v", c, ok)
	}
	if got := trans.rows[0]; got.Reason != mvt.ReasonYieldBelow || got.To != mvt.StateClaim || got.YieldHere != 1 {
		t.Fatalf("first transition = %+v", got)
	}
}

func TestMVTAfterTour_ColdStartStays(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	st.yield.Observe(1, 10, time.Now()) // 1 sell < yield_min_sells 3
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}, tourPlanBudget{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}
	if got := trans.last(t); got.Reason != mvt.ReasonColdStart || got.To != mvt.StateTrade || len(fx.jumps) != 0 {
		t.Fatalf("cold start must stay: %+v jumps=%v", got, fx.jumps)
	}
}

func TestMVTAfterTour_HoldAfterRepeatedTravelFailures(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	st.holdSells, st.holdUntil = 2, time.Now().Add(time.Hour)
	for i := 0; i < 3; i++ {
		st.yield.Observe(1, 10, time.Now())
	}
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	_ = h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}, tourPlanBudget{})
	if got := trans.last(t); got.Reason != mvtReasonHold || len(fx.jumps) != 0 {
		t.Fatalf("hold must block departure: %+v", got)
	}
	t0 := time.Now()
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 2, 1, t0))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(time.Second)))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(2*time.Second)))
	if st.holdSells != 0 {
		t.Fatalf("holdSells = %d after two sells", st.holdSells)
	}
}

func TestMVTEmptyPlan_ClaimsImmediatelyIgnoringColdStart(t *testing.T) {
	fx := repositionFixture()
	// Home is dead from the first plan; S2 is rich. No sells ever happen at home.
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			return infeasibleTour()
		}
		return feasiblePlan(600000, 600000)
	}}
	h, claims, trans := mvtHandler(t, fx, planner, 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" {
		t.Fatalf("empty home must claim S2: %+v ok=%v", c, ok)
	}
	seenEmpty := false
	for _, r := range trans.rows {
		if r.Reason == mvtReasonEmpty || r.Reason == mvtReasonBootstrap {
			seenEmpty = true
		}
	}
	if !seenEmpty {
		t.Fatalf("no empty/bootstrap CLAIM in %+v", trans.rows)
	}
}

// mvtDeadPlanner finds no plan anywhere, so every ground is solver-dead.
func mvtDeadPlanner() *tourFakeRoutingClient {
	return &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan { return infeasibleTour() }}
}

// The shared operator kill-switch stops MVT movement exactly as it stops the old rescues:
// the bootstrap and the empty exit both had a richer neighbour and both stay.
func TestMVTBootstrap_RepositionDisabledStaysWithoutJumping(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	cmd.RepositionDisabled = true
	resp, err := h.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if r := tourResponse(t, resp); r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("kill-switch must never jump: repositions=%d jumps=%v", r.Repositions, fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("hull must stay claimed where it stands, got %+v ok=%v", c, ok)
	}
	seen := mvtSeen(trans.rows)
	want := "CLAIM>TRADE:" + mvtReasonRepositionDisabled + "@X1-S1"
	refused := 0
	for _, s := range seen {
		if s == want {
			refused++
		}
	}
	if len(seen) == 0 || seen[0] != want || refused < 2 {
		t.Fatalf("bootstrap and the empty exit must both be refused as %s, got %v", want, seen)
	}
}

// A budget with no headroom refuses the jump the way it refuses the old reposition ranking.
func TestMVTTravelTo_BudgetWithoutHeadroomStays(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	h.SetTreasuryReader(&fakeTreasury{credits: 1})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	cmd.MaxSpend = 1000
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 7, TravelPerUnit: 2}}
	denied := tourPlanBudget{maxSpend: 1000, reserve: 5000}
	moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvt.ReasonYieldBelow, 3, denied)
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a stay", moved, err, fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want presence stamped at X1-S1", c, ok)
	}
	if len(trans.rows) != 1 || trans.rows[0].Reason != mvtReasonBudgetDenied || trans.rows[0].To != mvt.StateTrade ||
		trans.rows[0].YieldHere != 3 || trans.rows[0].BestAlternative != 7 || trans.rows[0].TravelCost != 2 {
		t.Fatalf("transitions = %+v, want one budget_denied stay carrying the numbers", trans.rows)
	}
	moved, err = h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvt.ReasonYieldBelow, 3, tourPlanBudget{maxSpend: 9000, reserve: 5000})
	if err != nil || !moved {
		t.Fatalf("headroom restored: moved=%v err=%v", moved, err)
	}
}

// The bootstrap prices its jump on the LIVE capital budget, as the loop prices a tour: a
// treasury at the working-capital reserve deploys nothing, so the richer neighbour is refused.
func TestMVTBootstrap_LiveTreasuryAtTheReserveDeniesTheJump(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, mvtDeadPlanner(), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	h.SetTreasuryReader(&fakeTreasury{credits: 300_000})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	cmd.WorkingCapitalReserve = 300_000
	if _, err := h.Handle(ctx, cmd); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("no headroom must never jump: %v", fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S1", c, ok)
	}
	if got := mvtSeen(trans.rows); len(got) == 0 || got[0] != "CLAIM>TRADE:"+mvtReasonBudgetDenied+"@X1-S1" {
		t.Fatalf("transitions = %v, want the bootstrap refused as budget_denied", got)
	}
}

// Home outranks the neighbour on ledger depth, yet three no-plan tours have proven it dead:
// the empty exit must leave for the neighbour, not re-elect the ground the solver refused.
// The second death, at the neighbour, is bounded by the episode: one rescue jump per run.
func TestMVTEmptyExit_LeavesSolverDeadGroundThatOutranksTheNeighbour(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, mvtDeadPlanner(), 500, 10)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	resp, err := h.Handle(ctx, mvtCmd(t))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("jumps = %v, want exactly one jump to X1-S2", fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
	if r := tourResponse(t, resp); r.ExitReason != tourExitStarvation {
		t.Fatalf("exit = %q, want %s after the bounded episode", r.ExitReason, tourExitStarvation)
	}
	seen := mvtSeen(trans.rows)
	want := []string{
		"CLAIM>TRADE:" + mvt.ReasonStay + "@X1-S1",
		"TRADE>CLAIM:" + mvtReasonEmpty + "@X1-S1",
		"CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S2",
		"TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S2",
		"CLAIM>TRADE:" + mvtReasonEpisodeSpent + "@X1-S2",
	}
	if len(seen) != len(want) {
		t.Fatalf("transitions = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("transition %d = %s, want %s (all: %v)", i, seen[i], want[i], seen)
		}
	}
}

// A hull holding cargo bought for this system's sinks never flies it to a richer system.
func TestMVTTravelTo_LadenHullStays(t *testing.T) {
	fx := repositionFixture()
	fx.cargo["G"] = 60
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1}}
	moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonBootstrap, 0, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a laden stay", moved, err, fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want presence stamped at X1-S1", c, ok)
	}
	want := "CLAIM>TRADE:" + mvtReasonLaden + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
	fx.cargo["G"] = 0
	if moved, err = h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonEmpty, 0, tourPlanBudget{}); err != nil || !moved {
		t.Fatalf("hold cleared: moved=%v err=%v", moved, err)
	}
}

// A hull re-adopted laden on a dead ground: the bootstrap stays, the disposal ladder sells the
// hold here, and only then does the empty exit claim the richer neighbour.
func TestMVTBootstrap_LadenHullDischargesBeforeClaiming(t *testing.T) {
	fx := repositionFixture()
	fx.cargo["G"] = 60
	fx.tradeType = map[string]map[string]string{"X1-S1-B": {"G": "IMPORT"}} // a real sink the disposal ladder may sell into
	h, claims, trans := mvtHandler(t, fx, mvtDeadPlanner(), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	resp, err := h.Handle(ctx, mvtCmd(t))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	seen := mvtSeen(trans.rows)
	laden := "CLAIM>TRADE:" + mvtReasonLaden + "@X1-S1"
	if len(seen) == 0 || seen[0] != laden {
		t.Fatalf("bootstrap must refuse to fly the hold: %v", seen)
	}
	if r := tourResponse(t, resp); r.StrandDisposalSales == 0 || fx.cargo["G"] != 0 {
		t.Fatalf("the hold must be sold here first: sales=%d cargo=%v", r.StrandDisposalSales, fx.cargo)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("jumps = %v, want the one empty-exit jump after the discharge", fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
}

// The hold after three travel failures binds the empty exit and the bootstrap too — not
// only the departure rule — and expires on the cadence, since a dead system has no sells
// to count it down. Sells still end it early.
func TestMVTHold_BlocksEmptyExitAndBootstrapUntilExpiry(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	mvtSeedClaim(claims, "X1-S1", mvtSeededAt, true)
	st := h.mvtState(cmd)
	st.claimed, st.holdSells, st.holdUntil = "X1-S1", 2, time.Now().Add(time.Hour)
	for _, reason := range []string{mvtReasonEmpty, mvtReasonBootstrap} {
		moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, reason, tourPlanBudget{})
		if err != nil || moved || len(fx.jumps) != 0 {
			t.Fatalf("%s while held: moved=%v err=%v jumps=%v, want a stay", reason, moved, err, fx.jumps)
		}
		if got := trans.last(t); got.Reason != mvtReasonHold || got.From != mvt.StateTrade || got.To != mvt.StateTrade || got.System != "X1-S1" {
			t.Fatalf("%s while held must record TRADE>TRADE:hold@X1-S1, got %+v", reason, got)
		}
	}
	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S1" || !c.ClaimedAt.Equal(mvtSeededAt) {
		t.Fatalf("a held hull must leave its claim untouched, got %+v ok=%v", c, ok)
	}
	st.holdUntil = time.Now().Add(-time.Second)
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved || len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expired hold: moved=%v err=%v jumps=%v, want the jump to X1-S2", moved, err, fx.jumps)
	}

	fx = repositionFixture()
	h, _, _ = mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	st = h.mvtState(cmd)
	st.claimed, st.holdSells, st.holdUntil = "X1-S1", 2, time.Now().Add(time.Hour)
	t0 := time.Now()
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 2, 1, t0))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(time.Second)))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(2*time.Second)))
	if st.holding(time.Now()) {
		t.Fatalf("two sells must end the hold before the clock does: holdSells=%d", st.holdSells)
	}
	if moved, err = h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{}); err != nil || !moved || len(fx.jumps) != 1 {
		t.Fatalf("hold ended by sells: moved=%v err=%v jumps=%v, want the jump", moved, err, fx.jumps)
	}
}

// mvtRetireHandler wires the MVT ports onto the retirement fixture handler, whose ship repo
// stamps the operator's mark on every read.
func mvtRetireHandler(t *testing.T, fx *tourFixture) (*RunTourCoordinatorHandler, *mvtFakeClaims) {
	t.Helper()
	repo := &retireMarkRepo{tourFakeShipRepo: &tourFakeShipRepo{fx: fx, t: t}, marked: true}
	h := newRetireTourHandler(t, fx, mvtDeadPlanner(), repo)
	claims := newMVTFakeClaims()
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{}}, &mvtFakeTransitions{})
	h.SetJumpTollReader(mvtFakeTolls{seconds: 1})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{}})
	h.SetRankerAgeCaps(mvtCaps())
	return h, claims
}

// Spec §3: the claim row is deleted on retirement, whether the hull stood down drained or
// still holding, so the system stops reading as occupied to every other ranker.
func TestMVTRetirement_StandDownReleasesTheClaim(t *testing.T) {
	undrainable := &tourFixture{
		cargo: map[string]int{"FIREARMS": 20}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		ask:     map[string]map[string]int{"X1-S1-A": {"WIDGETS": 50}},
		tv:      map[string]map[string]int{"X1-S1-A": {"WIDGETS": 1000}},
	}
	for _, tc := range []struct {
		exit string
		fx   *tourFixture
	}{{tourExitRetired, oneLaneFixture()}, {tourExitRetiredHolding, undrainable}} {
		h, claims := mvtRetireHandler(t, tc.fx)
		ctx := common.WithLogger(context.Background(), &propFloorCapturingLogger{})
		mvtSeedClaim(claims, "X1-S1", mvtSeededAt, true)
		cmd := mvtCmd(t)
		resp, err := h.Handle(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: handle: %v", tc.exit, err)
		}
		if r := tourResponse(t, resp); r.ExitReason != tc.exit {
			t.Fatalf("exit = %q, want %s", r.ExitReason, tc.exit)
		}
		if _, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); ok || claims.released != 1 {
			t.Fatalf("%s: the claim must be released exactly once, got present=%v released=%d", tc.exit, ok, claims.released)
		}
		if st := h.mvtState(cmd); st.claimed != "" {
			t.Fatalf("%s: claimed = %q, want cleared", tc.exit, st.claimed)
		}
	}
}

// A registry write failure after the TRADE>CLAIM and CLAIM>TRAVEL rows closes the sequence,
// so telemetry never shows a hull left in TRAVEL that never flew.
func TestMVTTravelTo_ClaimWriteFailureClosesTheSequence(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	claims.failUpsert = true
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 7, TravelPerUnit: 2}}
	moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonBootstrap, 3, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a stay", moved, err, fx.jumps)
	}
	want := []string{"TRADE>CLAIM:" + mvtReasonBootstrap + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S2", "CLAIM>TRADE:" + mvtReasonClaimWriteFailed + "@X1-S1"}
	if got := mvtSeen(trans.rows); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	if last := trans.last(t); last.YieldHere != 3 || last.BestAlternative != 7 || last.TravelCost != 2 {
		t.Fatalf("the closing row must carry the decision's numbers, got %+v", last)
	}
	if _, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); ok {
		t.Fatal("no claim may exist after the failed write")
	}
}

// mvtRepositions reads player 1's tour reposition counter for one outcome off the registry
// the test installed.
func mvtRepositions(t *testing.T, outcome string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_tour_repositions_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["player_id"] == "1" && labels["outcome"] == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// MVT flights feed the same reposition series every old-path mover does, so the reposition
// dashboards see trade-mvt movement and its failures.
func TestMVTTravelTo_FlightsFeedTheRepositionMetric(t *testing.T) {
	previous := metrics.Registry
	metrics.InitRegistry()
	collector := metrics.NewTourMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	metrics.SetGlobalTourCollector(collector)
	t.Cleanup(func() {
		metrics.SetGlobalTourCollector(nil)
		metrics.Registry = previous
	})
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	ranked := []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 1}}

	fx := repositionFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	if moved, err := h.mvtTravelTo(ctx, mvtCmd(t), &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonBootstrap, 0, tourPlanBudget{}); err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the jump", moved, err)
	}
	if got := mvtRepositions(t, "success"); got != 1 {
		t.Fatalf("success repositions = %v, want 1 after the arrival", got)
	}

	failing := repositionFixture()
	failing.navFail = map[string]bool{"X1-S1-GATE": true}
	h, _, _ = mvtHandler(t, failing, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	if moved, err := h.mvtTravelTo(ctx, mvtCmd(t), &RunTourCoordinatorResponse{}, nil, ranked, mvtReasonBootstrap, 0, tourPlanBudget{}); err != nil || moved {
		t.Fatalf("moved=%v err=%v, want a failed flight", moved, err)
	}
	if got := mvtRepositions(t, "failed"); got != 1 {
		t.Fatalf("failed repositions = %v, want 1 after the failed flight", got)
	}
}

// The yield tracker reads realised legs whether or not a telemetry sink is wired.
func TestRecordLeg_NilTelemetryStillFeedsTheYieldTracker(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), nil)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := mvtCmd(t)
	at := time.Now()
	h.recordLeg(ctx, cmd, trading.LegEngineSolver, leg("X1-S1-A", "X1-S1"), 0, buy("G", 10, 100), 10, 100, at)
	h.recordLeg(ctx, cmd, trading.LegEngineSolver, leg("X1-S1-B", "X1-S1"), 1, sell("G", 10, 200), 10, 200, at)
	if st := h.mvtState(cmd); st.yield.Sells() != 1 {
		t.Fatalf("sells = %d, want the sell observed with no telemetry sink", st.yield.Sells())
	}
}

// mvtChainHandler wires the stored X1-S1—X1-S2—X1-S3 chain over the reposition fixture with
// the given credits of fresh depth per system (absent = unpriced) and the jump path the
// relaxed resolver answers, so a CLAIM two hops out really flies both hops.
func mvtChainHandler(t *testing.T, fx *tourFixture, depths map[string]int, path ...string) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 0)
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{}
	for sys, d := range depths {
		lanes[sys] = mvtRichLane(sys, d, now)
	}
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: lanes}, trans)
	g := mvtStoredGraph([2]string{"X1-S1", "X1-S2"}, [2]string{"X1-S2", "X1-S3"})
	g.path = path
	h.SetGateGraph(g)
	return h, claims, trans
}

// mvtChainHandlerLanes is mvtChainHandler for a fixture that needs lanes built directly
// (e.g. a thin, non-mvtRichLane spread) rather than the rich credits-per-system shorthand.
func mvtChainHandlerLanes(t *testing.T, fx *tourFixture, lanes map[string][]mvt.LaneDepth, path ...string) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 0, 0)
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: lanes}, trans)
	g := mvtStoredGraph([2]string{"X1-S1", "X1-S2"}, [2]string{"X1-S2", "X1-S3"})
	g.path = path
	h.SetGateGraph(g)
	return h, claims, trans
}

// mvtEscalationCmd is mvtCmd at a one-hop reach with the escalation cap given.
func mvtEscalationCmd(t *testing.T, maxHops int) *RunTourCoordinatorCommand {
	t.Helper()
	cmd := mvtCmd(t)
	cmd.ClaimReachHops, cmd.ClaimReachMaxHops = 1, maxHops
	return cmd
}

// mvtEscalatedTo is the to_hops of the "reach escalated" line, or -1 when none was logged.
func mvtEscalatedTo(l *metaCapturingLogger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.message, "reach escalated") {
			n, _ := e.metadata["to_hops"].(int)
			return n
		}
	}
	return -1
}

// mvtRelaxedLog is the "spread floor relaxed" line's metadata, or nil when none was logged.
func mvtRelaxedLog(l *metaCapturingLogger) map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.message, "spread floor relaxed") {
			return e.metadata
		}
	}
	return nil
}

// mvtWantFlownToS3 pins the escalated two-hop claim: both jumps flown, X1-S3 claimed and
// arrived, the escalation logged at to_hops 2, and the three transitions led by reason.
func mvtWantFlownToS3(t *testing.T, fx *tourFixture, claims *mvtFakeClaims, trans *mvtFakeTransitions, logger *metaCapturingLogger, reason string) {
	t.Helper()
	if len(fx.jumps) != 2 || fx.jumps[0] != "X1-S2" || fx.jumps[1] != "X1-S3" {
		t.Fatalf("jumps = %v, want the two hops to X1-S3", fx.jumps)
	}
	c, ok, _ := claims.Get(context.Background(), 1, "TOUR-MVT")
	if !ok || c.System != "X1-S3" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S3 arrived", c, ok)
	}
	if got := mvtEscalatedTo(logger); got != 2 {
		t.Fatalf("reach escalated to_hops = %d, want 2 (X1-S3 is two hops out)", got)
	}
	want := []string{"TRADE>CLAIM:" + reason + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S3", "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S3"}
	if got := mvtSeen(trans.rows); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

// Nothing is priced within one hop and the solver has declared home dead: the empty exit
// widens the reach a hop at a time until a ranking offers an alternative, then claims it.
func TestMVTClaimAndTravel_EscalatesReachOnEmptyExit(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtChainHandler(t, fx, map[string]int{"X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	moved, err := h.mvtClaimAndTravel(ctx, mvtEscalationCmd(t, 3), &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the escalated claim flown", moved, err)
	}
	if len(fx.jumps) != 2 || fx.jumps[0] != "X1-S2" || fx.jumps[1] != "X1-S3" {
		t.Fatalf("jumps = %v, want the two hops to X1-S3", fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S3" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S3 arrived", c, ok)
	}
	if got := mvtEscalatedTo(logger); got != 2 {
		t.Fatalf("reach escalated to_hops = %d, want 2 (X1-S3 is two hops out)", got)
	}
	want := []string{"TRADE>CLAIM:" + mvtReasonEmpty + "@X1-S1", "CLAIM>TRAVEL:" + mvtReasonClaim + "@X1-S3", "TRAVEL>TRADE:" + mvtReasonArrived + "@X1-S3"}
	if got := mvtSeen(trans.rows); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

// A profitable home is an offer at the configured reach: the bootstrap stays on zero travel
// and never looks past it, however rich the ground two hops out.
func TestMVTClaimAndTravel_BootstrapDoesNotEscalatePastAProfitableHome(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtChainHandler(t, fx, map[string]int{"X1-S1": 500, "X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	moved, err := h.mvtClaimAndTravel(ctx, mvtEscalationCmd(t, 3), &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a stay", moved, err, fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want home stamped", c, ok)
	}
	if got := mvtEscalatedTo(logger); got != -1 {
		t.Fatalf("a profitable home must not escalate, yet reach escalated to %d", got)
	}
	want := "CLAIM>TRADE:" + mvt.ReasonStay + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
}

// Nothing priced within one hop of an unpriced home: an empty ranking is no offer, so the
// bootstrap widens too rather than settling for no_alternative on ground it has not seen.
func TestMVTClaimAndTravel_BootstrapEscalatesAnEmptyReach(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtChainHandler(t, fx, map[string]int{"X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	moved, err := h.mvtClaimAndTravel(ctx, mvtEscalationCmd(t, 3), &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonBootstrap, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the escalated claim flown", moved, err)
	}
	mvtWantFlownToS3(t, fx, claims, trans, logger, mvtReasonBootstrap)
}

// The cap binds: with the rich ground two hops out and the reach capped at one (or the cap
// unset below the reach), the empty exit finds nothing and stays.
func TestMVTClaimAndTravel_EscalationStopsAtMaxHops(t *testing.T) {
	for _, maxHops := range []int{1, 0} {
		fx := repositionFixture()
		h, _, trans := mvtChainHandler(t, fx, map[string]int{"X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
		logger := &metaCapturingLogger{}
		ctx := common.WithLogger(context.Background(), logger)
		moved, err := h.mvtClaimAndTravel(ctx, mvtEscalationCmd(t, maxHops), &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
		if err != nil || moved || len(fx.jumps) != 0 {
			t.Fatalf("max=%d: moved=%v err=%v jumps=%v, want a stay", maxHops, moved, err, fx.jumps)
		}
		if got := mvtEscalatedTo(logger); got != -1 {
			t.Fatalf("max=%d: reach must not escalate past the cap, yet escalated to %d", maxHops, got)
		}
		want := "CLAIM>TRADE:" + mvt.ReasonNoAlternative + "@X1-S1"
		if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
			t.Fatalf("max=%d: transitions = %v, want exactly [%s]", maxHops, got, want)
		}
	}
}

// The ledger still ranks home first, but the solver's three no-plan tours outrank its depth:
// the empty exit takes the one-hop alternative rather than re-electing dead ground.
func TestMVTClaimAndTravel_EmptyExitNeverStaysWhileAnAlternativeExists(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtChainHandler(t, fx, map[string]int{"X1-S1": 500, "X1-S2": 10}, "X1-S1", "X1-S2")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtEscalationCmd(t, 3)
	ship, _ := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if ranked, err := h.mvtRank(ctx, cmd, ship); err != nil || len(ranked) != 2 || ranked[0].System != "X1-S1" {
		t.Fatalf("precondition: home must outrank the neighbour, got %+v err=%v", ranked, err)
	}
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved || len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("moved=%v err=%v jumps=%v, want the one jump to X1-S2", moved, err, fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
	if got := mvtEscalatedTo(logger); got != -1 {
		t.Fatalf("an alternative at the configured reach must not escalate, yet escalated to %d", got)
	}
	if got := mvtSeen(trans.rows); len(got) == 0 || got[0] != "TRADE>CLAIM:"+mvtReasonEmpty+"@X1-S1" {
		t.Fatalf("transitions = %v, want the empty exit to lead", got)
	}
}

// Home still shows ledger depth, so the one-hop ranking is current-only. That is not an offer
// on the empty exit: stopping there would re-elect the dead ground as no_alternative and
// relaunch into the same starvation loop, so the reach must widen until an alternative ranks.
func TestMVTClaimAndTravel_EmptyExitEscalatesPastACurrentOnlyRanking(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtChainHandler(t, fx, map[string]int{"X1-S1": 500, "X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	moved, err := h.mvtClaimAndTravel(ctx, mvtEscalationCmd(t, 3), &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the escalated claim flown", moved, err)
	}
	mvtWantFlownToS3(t, fx, claims, trans, logger, mvtReasonEmpty)
}

// The departure rule compares a working system against the alternatives at the configured
// reach only: an empty one-hop ring is no_alternative even with rich ground two hops out.
func TestMVTAfterTour_DoesNotEscalate(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtChainHandler(t, fx, map[string]int{"X1-S3": 500}, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtEscalationCmd(t, 3)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ { // warm: 1 credit/unit here, far below S3's 100/unit
		st.yield.Observe(1, 10, t0.Add(time.Duration(i)*time.Second))
	}
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}, tourPlanBudget{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}
	if got := trans.last(t); got.Reason != mvt.ReasonNoAlternative || got.To != mvt.StateTrade || got.System != "X1-S1" {
		t.Fatalf("after-tour verdict = %+v, want TRADE>TRADE:no_alternative@X1-S1", got)
	}
	if len(fx.jumps) != 0 || mvtEscalatedTo(logger) != -1 {
		t.Fatalf("the departure rule must neither fly nor escalate: jumps=%v escalated=%d", fx.jumps, mvtEscalatedTo(logger))
	}
}

// The floor was meant to steer a hull toward rich ground, not to hold it in a drained pocket:
// with nothing clearing 200/unit anywhere in reach, the escalation relaxes to floor 0 once at
// the cap and takes the best thin (150/unit) alternative instead of idling (sp-htzl1.1).
func TestMVTClaimAndTravel_RelaxesFloorWhenNothingRichIsReachable(t *testing.T) {
	fx := repositionFixture()
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{
		"X1-S2": mvtLane("X1-S2", "IRON", 100, 100, 250, now), // 150/unit, one hop out
		"X1-S3": mvtLane("X1-S3", "IRON", 100, 100, 250, now), // 150/unit, two hops out
	}
	h, claims, trans := mvtChainHandlerLanes(t, fx, lanes, "X1-S1", "X1-S2")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtEscalationCmd(t, 2)
	cmd.RankerMinSpreadPerUnit = 200
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the relaxed claim flown", moved, err)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("jumps = %v, want the one hop to the nearer thin system X1-S2", fx.jumps)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S2 arrived", c, ok)
	}
	if got := mvtSeen(trans.rows); len(got) == 0 || got[0] != "TRADE>CLAIM:"+mvtReasonEmpty+"@X1-S1" {
		t.Fatalf("transitions = %v, want a CLAIM lead, not no_alternative", got)
	}
	meta := mvtRelaxedLog(logger)
	if meta == nil {
		t.Fatal("want a 'spread floor relaxed' log line")
	}
	if hops, _ := meta["hops"].(int); hops != 2 {
		t.Fatalf("relaxed hops = %v, want 2", meta["hops"])
	}
}

// A rich alternative two hops out ranks on its own at the floor: nothing thin competes for the
// choice and the relaxation never fires.
func TestMVTClaimAndTravel_DoesNotRelaxWhenARichSystemRanks(t *testing.T) {
	fx := repositionFixture()
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{
		"X1-S2": mvtLane("X1-S2", "IRON", 100, 100, 250, now), // 150/unit, below the floor
		"X1-S3": mvtRichLane("X1-S3", 500, now),               // 500/unit, clears the floor
	}
	h, claims, trans := mvtChainHandlerLanes(t, fx, lanes, "X1-S1", "X1-S2", "X1-S3")
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := mvtEscalationCmd(t, 2)
	cmd.RankerMinSpreadPerUnit = 200
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the rich claim flown", moved, err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S3" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v, want X1-S3 arrived", c, ok)
	}
	if got := mvtSeen(trans.rows); len(got) == 0 || got[0] != "TRADE>CLAIM:"+mvtReasonEmpty+"@X1-S1" {
		t.Fatalf("transitions = %v, want a CLAIM lead", got)
	}
	if meta := mvtRelaxedLog(logger); meta != nil {
		t.Fatalf("a ranked rich alternative must never trigger the relaxation: %+v", meta)
	}
}

// Nothing priced anywhere in reach: the relaxed re-rank at floor 0 still finds nothing, so
// no_alternative stands exactly as it did before the floor existed.
func TestMVTClaimAndTravel_RelaxationStillEmptyStaysNoAlternative(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtChainHandlerLanes(t, fx, map[string][]mvt.LaneDepth{}, "X1-S1", "X1-S2", "X1-S3")
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtEscalationCmd(t, 2)
	cmd.RankerMinSpreadPerUnit = 200
	moved, err := h.mvtClaimAndTravel(ctx, cmd, &RunTourCoordinatorResponse{}, &repositionEpisode{}, mvtReasonEmpty, tourPlanBudget{})
	if err != nil || moved || len(fx.jumps) != 0 {
		t.Fatalf("moved=%v err=%v jumps=%v, want a stay", moved, err, fx.jumps)
	}
	want := "CLAIM>TRADE:" + mvt.ReasonNoAlternative + "@X1-S1"
	if got := mvtSeen(trans.rows); len(got) != 1 || got[0] != want {
		t.Fatalf("transitions = %v, want exactly [%s]", got, want)
	}
}
