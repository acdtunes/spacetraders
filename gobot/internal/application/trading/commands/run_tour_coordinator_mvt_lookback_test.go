package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtLookbackFixture is repositionFixture with a real cross-system lane over it: X1-S1 sells
// IRON at 100 and the claim target X1-S2 IMPORTS it at 600, so the jump has a manifest to carry.
func mvtLookbackFixture() *tourFixture {
	fx := repositionFixture()
	fx.ask["X1-S1-A"]["IRON"] = 100
	fx.tv["X1-S1-A"]["IRON"] = 100
	fx.bid["X1-S2-B"]["IRON"] = 600
	fx.ask["X1-S2-B"]["IRON"] = 700
	fx.tv["X1-S2-B"]["IRON"] = 100
	fx.tradeType = map[string]map[string]string{"X1-S2-B": {"IRON": "IMPORT"}}
	return fx
}

// mvtLookbackRanked is the one X1-S2 candidate, priced past the jump-fee guard.
func mvtLookbackRanked() []mvt.ScoredSystem {
	return []mvt.ScoredSystem{{System: "X1-S2", Hops: 1, EntryWaypoint: "X1-S2-SRC", Score: 7, TravelPerUnit: 2, ExpectedLoadCredits: 50_000}}
}

// mvtLoggedManifest reports whether the loaded-manifest line was logged, and the units it named.
func mvtLoggedManifest(l *metaCapturingLogger) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.message, "manifest loaded") {
			units, _ := e.metadata["units"].(int)
			return true, units
		}
	}
	return false, 0
}

// mvtJumpLoaded reads player 1's committed-jump counter for one loaded label.
func mvtJumpLoaded(t *testing.T, loaded string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_tour_jump_loaded_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["player_id"] == "1" && labels["loaded"] == loaded {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// mvtInstallTourMetrics installs a private registry + tour collector for one test.
func mvtInstallTourMetrics(t *testing.T) {
	t.Helper()
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
}

// mvtLookbackHandler is mvtHandler with an API client, so the buy-time floor is armed.
func mvtLookbackHandler(t *testing.T, fx *tourFixture, api domainPorts.APIClient) *RunTourCoordinatorHandler {
	t.Helper()
	h := newTourHandlerWithAPI(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-MVT", 100000)}, api)
	now := time.Now()
	h.SetMVTPorts(newMVTFakeClaims(), &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtRichLane("X1-S1", 10, now), "X1-S2": mvtRichLane("X1-S2", 500, now),
	}}, &mvtFakeTransitions{})
	h.SetJumpTollReader(mvtFakeTolls{seconds: 1})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{}})
	h.SetRankerAgeCaps(mvtCaps())
	h.SetGateGraph(mvtTravelGraph())
	return h
}

// THE unlock: a claim jump must buy the departure export the claimed system imports BEFORE it
// flies — cargo bought after the flight is at the wrong end of the lane — and the booking must
// reach netBought/response or the run's obligation and economics go blind on it.
func TestMVTTravelTo_LoadsManifestBeforeTheJump(t *testing.T) {
	mvtInstallTourMetrics(t)
	fx := mvtLookbackFixture()
	buysAtJump := -1
	fx.jumpHook = func() {
		fx.mu.Lock()
		buysAtJump = fx.buys
		fx.mu.Unlock()
	}
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	netBought, resp := map[string]int{}, &RunTourCoordinatorResponse{}

	moved, err := h.mvtTravelTo(ctx, mvtCmd(t), resp, nil, netBought, mvtLookbackRanked(), mvtReasonEmpty, 0, tourPlanBudget{})

	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the claim jump to commit", moved, err)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("jumps = %v, want one jump to X1-S2", fx.jumps)
	}
	if buysAtJump != 1 {
		t.Fatalf("buys dispatched before the jump = %d, want 1 (the manifest must be aboard when the hull leaves)", buysAtJump)
	}
	if len(fx.buyCmds) != 1 || fx.buyCmds[0].GoodSymbol != "IRON" || fx.buyCmds[0].Units != 100 {
		t.Fatalf("buy commands = %+v, want one IRON x100 at the departure", fx.buyCmds)
	}
	if netBought["IRON"] != 100 {
		t.Fatalf("netBought[IRON] = %d, want the 100 units booked against the run's obligation", netBought["IRON"])
	}
	if resp.TotalSpent != 10_000 || resp.TradesExecuted != 1 {
		t.Fatalf("response = spent %d / trades %d, want 10000 / 1", resp.TotalSpent, resp.TradesExecuted)
	}
	logged, units := mvtLoggedManifest(logger)
	if !logged || units != 100 {
		t.Fatalf("the loaded-manifest line must name the units, got logged=%v units=%d", logged, units)
	}
	if got := mvtJumpLoaded(t, "true"); got != 1 {
		t.Fatalf("loaded=true jumps = %v, want 1 — the deadhead empty-rate must cover the loop", got)
	}
}

// Loaded-if-profitable, never forced: no floor-clearing lane, and the claim flies as it does today.
func TestMVTTravelTo_NoProfitableLane_JumpsEmpty(t *testing.T) {
	lowBid := mvtLookbackFixture()
	lowBid.bid["X1-S2-B"]["IRON"] = 90 // the destination bids UNDER the departure's ask

	for _, tc := range []struct {
		name string
		fx   *tourFixture
	}{
		{"destination bids below the origin ask", lowBid},
		{"no shared good across the systems", repositionFixture()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mvtInstallTourMetrics(t)
			h, _, _ := mvtHandler(t, tc.fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
			h.SetGateGraph(mvtTravelGraph())
			logger := &metaCapturingLogger{}
			ctx := common.WithLogger(context.Background(), logger)
			netBought, resp := map[string]int{}, &RunTourCoordinatorResponse{}

			moved, err := h.mvtTravelTo(ctx, mvtCmd(t), resp, nil, netBought, mvtLookbackRanked(), mvtReasonEmpty, 0, tourPlanBudget{})

			if err != nil || !moved || len(tc.fx.jumps) != 1 {
				t.Fatalf("moved=%v err=%v jumps=%v, want the jump to proceed", moved, err, tc.fx.jumps)
			}
			if len(tc.fx.buyCmds) != 0 || len(netBought) != 0 || resp.TotalSpent != 0 {
				t.Fatalf("nothing may be bought: buys=%+v netBought=%v spent=%d", tc.fx.buyCmds, netBought, resp.TotalSpent)
			}
			if logged, _ := mvtLoggedManifest(logger); logged {
				t.Fatal("an empty jump must not log a loaded manifest")
			}
			if got := mvtJumpLoaded(t, "false"); got != 1 {
				t.Fatalf("loaded=false jumps = %v, want the deadhead counted", got)
			}
		})
	}
}

// The buy-time working-capital floor (RULINGS #4) binds the load: no headroom, no buy, jump anyway.
func TestMVTTravelTo_ReserveFloorLoadsNothing(t *testing.T) {
	fx := mvtLookbackFixture()
	// Balance 1,000,050 against a 1,000,000 reserve → 50 headroom / 100 ask = 0 affordable units.
	h := mvtLookbackHandler(t, fx, &tourSeqAPIClient{balances: []int{1_000_050}})
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	netBought, resp := map[string]int{}, &RunTourCoordinatorResponse{}

	moved, err := h.mvtTravelTo(ctx, mvtCmd(t), resp, nil, netBought, mvtLookbackRanked(), mvtReasonEmpty, 0,
		tourPlanBudget{maxSpend: 10_000_000, reserve: 1_000_000})

	if err != nil || !moved || len(fx.jumps) != 1 {
		t.Fatalf("moved=%v err=%v jumps=%v, want the jump to proceed under the floor", moved, err, fx.jumps)
	}
	if fx.buys != 0 || resp.TotalSpent != 0 || len(netBought) != 0 {
		t.Fatalf("the floor must skip the load: buys=%d spent=%d netBought=%v", fx.buys, resp.TotalSpent, netBought)
	}
}

// The loader is opportunistic and must never be able to strand the claim: an unreadable
// market board loads nothing and the hull still reaches the ground it claimed.
func TestMVTTravelTo_ManifestDoesNotBlockTheJumpOnListingsError(t *testing.T) {
	fx := mvtLookbackFixture()
	fx.marketListErr = map[string]bool{"X1-S2": true}
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	netBought, resp := map[string]int{}, &RunTourCoordinatorResponse{}

	moved, err := h.mvtTravelTo(ctx, mvtCmd(t), resp, nil, netBought, mvtLookbackRanked(), mvtReasonEmpty, 0, tourPlanBudget{})

	if err != nil || !moved || len(fx.jumps) != 1 {
		t.Fatalf("moved=%v err=%v jumps=%v, want the jump to survive the unreadable board", moved, err, fx.jumps)
	}
	if fx.buys != 0 || resp.TotalSpent != 0 {
		t.Fatalf("an unreadable board buys nothing: buys=%d spent=%d", fx.buys, resp.TotalSpent)
	}
}

// The VOLUNTARY departure is the same crossing as a rescue, so it loads through the same seam.
func TestMVTAfterTour_VoluntaryDepartureCarriesManifest(t *testing.T) {
	fx := mvtLookbackFixture()
	h, claims, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	t0 := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ { // warm the tracker at 1 credit/unit — far below X1-S2's ground
		st.yield.Observe(1, 10, t0.Add(time.Duration(i)*time.Second))
	}
	netBought, resp := map[string]int{}, &RunTourCoordinatorResponse{}

	if err := h.mvtAfterTour(ctx, cmd, resp, netBought, tourPlanBudget{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}

	if c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v, want the departure to X1-S2", c, ok)
	}
	if netBought["IRON"] != 100 || resp.TotalSpent != 10_000 {
		t.Fatalf("the voluntary departure must carry the manifest: netBought=%v spent=%d", netBought, resp.TotalSpent)
	}
}

// What the destination re-plan is handed decides whether the manifest is ever sold: it has to
// reach the solver request as launch cargo, the held-liquidation the old path discharges through.
func TestMVTTravelTo_CarriedManifestReachesTheDestinationPlanAsLaunchCargo(t *testing.T) {
	fx := mvtLookbackFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	h.SetGateGraph(mvtTravelGraph())
	ctx := common.WithLogger(context.Background(), &metaCapturingLogger{})
	cmd := mvtCmd(t)

	if moved, err := h.mvtTravelTo(ctx, cmd, &RunTourCoordinatorResponse{}, nil, map[string]int{}, mvtLookbackRanked(), mvtReasonEmpty, 0, tourPlanBudget{}); err != nil || !moved {
		t.Fatalf("moved=%v err=%v, want the loaded jump", moved, err)
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		t.Fatalf("load ship: %v", err)
	}
	state := h.tourShipState(ship)
	if state.CurrentSystem != "X1-S2" || state.Cargo["IRON"] != 100 {
		t.Fatalf("solver request = %+v, want 100 IRON aboard in X1-S2", state)
	}
}
