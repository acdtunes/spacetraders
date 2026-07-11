package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// srcRow / impRow / expRow build GoodListing fixtures for the pure manifest tests: a
// buyable source row (Ask), an IMPORT sink (Bid, a real demand), and an EXPORT sink (Bid,
// a low sellback that must NEVER be a look-back destination — sp-9mkf).
func gl(good, wp, tradeType string, bid, ask, vol int) trading.GoodListing {
	return trading.GoodListing{Good: good, Waypoint: wp, TradeType: tradeType, Bid: bid, Ask: ask, Volume: vol}
}

// The core look-back pairing: a departure EXPORT (buyable) paired with a destination IMPORT
// (a real sink) whose spread clears the floor becomes a manifest item sized to the shallow
// single-tranche volume cap.
func TestBuildLookbackManifest_PairsDepartureExportToDestinationImport(t *testing.T) {
	src := []trading.GoodListing{gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 30)}
	dest := []trading.GoodListing{gl("PARTS", "UQ16-A1", "IMPORT", 300, 999, 20)}

	manifest := buildLookbackManifest(src, dest, 100, 10)

	if len(manifest) != 1 {
		t.Fatalf("expected one manifest item (PARTS export->import), got %d: %+v", len(manifest), manifest)
	}
	item := manifest[0]
	if item.Good != "PARTS" || item.SourceWaypoint != "HU21-D46" {
		t.Fatalf("manifest item = %+v, want PARTS from HU21-D46", item)
	}
	if item.Units != 20 { // min(srcVol 30, destVol 20) — the shallower absorption bound
		t.Fatalf("units = %d, want 20 (min of source/dest volume)", item.Units)
	}
	if item.SourceAsk != 100 || item.DestBid != 300 {
		t.Fatalf("prices = ask %d / bid %d, want 100 / 300", item.SourceAsk, item.DestBid)
	}
}

// sp-9mkf sink discipline: a destination EXPORT row is a low sellback price, not a real
// import sink, so it must NEVER be paired as a look-back destination — otherwise look-back
// would reintroduce the C37 dump the tour snapshot filter exists to prevent.
func TestBuildLookbackManifest_NeverSellsIntoAnExporterBid(t *testing.T) {
	src := []trading.GoodListing{gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 30)}
	// The destination only EXPORTS PARTS (its bid is a sellback, not a demand).
	dest := []trading.GoodListing{gl("PARTS", "UQ16-D9", "EXPORT", 300, 120, 20)}

	manifest := buildLookbackManifest(src, dest, 100, 10)

	if len(manifest) != 0 {
		t.Fatalf("an EXPORT destination bid must never be a look-back sink (sp-9mkf), got %+v", manifest)
	}
}

// The min-margin floor (RULINGS #4) gates the manifest: a spread under the tour's per-run
// floor is not loaded, exactly as the solver would refuse it.
func TestBuildLookbackManifest_RejectsBelowFloorSpread(t *testing.T) {
	src := []trading.GoodListing{gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 30)}
	dest := []trading.GoodListing{gl("PARTS", "UQ16-A1", "IMPORT", 250, 999, 20)} // spread 150

	if m := buildLookbackManifest(src, dest, 100, 200); len(m) != 0 { // floor 200 > spread 150
		t.Fatalf("a below-floor spread must not load, got %+v", m)
	}
	if m := buildLookbackManifest(src, dest, 100, 150); len(m) != 1 { // floor 150 == spread 150 clears
		t.Fatalf("a spread meeting the floor must load, got %+v", m)
	}
}

// Hold capacity bounds the total manifest: with two floor-clearing goods, the richer
// (higher capped-spread) lane fills first and the hold caps the rest.
func TestBuildLookbackManifest_RanksByCappedSpreadAndFillsHold(t *testing.T) {
	src := []trading.GoodListing{
		gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 40),   // spread 200, cap 40 -> capped 8000
		gl("PLATING", "HU21-D47", "EXPORT", 30, 200, 40), // spread 400, cap 40 -> capped 16000 (richer)
	}
	dest := []trading.GoodListing{
		gl("PARTS", "UQ16-A1", "IMPORT", 300, 999, 40),
		gl("PLATING", "UQ16-A2", "IMPORT", 600, 999, 40),
	}

	manifest := buildLookbackManifest(src, dest, 50, 10) // hold only fits 50 of the 80 available

	if len(manifest) != 2 {
		t.Fatalf("expected both goods to enter the manifest, got %d: %+v", len(manifest), manifest)
	}
	// PLATING (capped 16000) must be packed FIRST and take its full 40; PARTS gets the last 10.
	if manifest[0].Good != "PLATING" || manifest[0].Units != 40 {
		t.Fatalf("richest lane must fill first at full depth, got %+v", manifest[0])
	}
	if manifest[1].Good != "PARTS" || manifest[1].Units != 10 {
		t.Fatalf("the hold's remaining 10 must go to PARTS, got %+v", manifest[1])
	}
}

// A zero hold (or no cross-system lane) yields no manifest — the caller jumps empty.
func TestBuildLookbackManifest_EmptyWhenNoLaneOrNoHold(t *testing.T) {
	src := []trading.GoodListing{gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 30)}
	dest := []trading.GoodListing{gl("FUEL", "UQ16-A1", "IMPORT", 300, 999, 20)} // different good

	if m := buildLookbackManifest(src, dest, 100, 10); len(m) != 0 {
		t.Fatalf("no shared good = no manifest, got %+v", m)
	}
	good := []trading.GoodListing{gl("PARTS", "UQ16-A1", "IMPORT", 300, 999, 20)}
	if m := buildLookbackManifest(src, good, 0, 10); len(m) != 0 {
		t.Fatalf("zero hold = no manifest, got %+v", m)
	}
}

// --- integration: the reposition jump carries the manifest (the HU21->UQ16 replay) ---

// lookbackFixture is the sp-ed4i deadhead replay: the hull taps out its home system X1-HU21
// (in-system arb dies) but X1-HU21 EXPORTS a good X1-UQ16 IMPORTS — so the margins-death
// reposition to X1-UQ16 should now LOAD that export before jumping instead of flying empty.
// X1-HU21-A: home in-system arb good G (buy 100 / sell 200) + the export PARTS (ask 100).
// X1-UQ16-B: the fresh-ground good H (buy 100 / sell 300) + the PARTS IMPORT sink (bid 300).
func lookbackFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-HU21-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-HU21": {"X1-HU21-A", "X1-HU21-B"},
			"X1-UQ16": {"X1-UQ16-A", "X1-UQ16-B"},
		},
		bid: map[string]map[string]int{
			"X1-HU21-B": {"G": 200},
			"X1-UQ16-B": {"H": 300, "PARTS": 300}, // UQ16 IMPORTS PARTS
		},
		ask: map[string]map[string]int{
			"X1-HU21-A": {"G": 100, "PARTS": 100}, // HU21 EXPORTS PARTS (buyable)
			"X1-HU21-B": {"G": 200},
			"X1-UQ16-A": {"H": 100},
			"X1-UQ16-B": {"H": 300, "PARTS": 300},
		},
		tv: map[string]map[string]int{
			"X1-HU21-A": {"G": 1000, "PARTS": 40},
			"X1-HU21-B": {"G": 1000},
			"X1-UQ16-A": {"H": 1000},
			"X1-UQ16-B": {"H": 1000, "PARTS": 40},
		},
		// PARTS is a real IMPORT sink at UQ16-B; everything else defaults to EXPORT.
		tradeType: map[string]map[string]string{
			"X1-UQ16-B": {"PARTS": "IMPORT"},
		},
		neighbors: map[string][]string{"X1-HU21": {"X1-UQ16"}},
	}
}

// THE sp-ed4i unlock. A margins-death reposition from a system that EXPORTS a good the
// destination IMPORTS must LOAD that export before the jump (look-back loading) so the
// cross-system transition carries value instead of flying empty. The post-jump re-plan
// liquidates the carried manifest at the destination's import bid.
func TestTour_Reposition_LoadsLookbackManifestBeforeJump(t *testing.T) {
	fx := lookbackFixture()
	homeCalls, destCalls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-HU21":
			homeCalls++
			if homeCalls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
					leg("X1-HU21-A", "X1-HU21", buy("G", 40, 100)),
					leg("X1-HU21-B", "X1-HU21", sell("G", 40, 200)),
				}}
			}
			return infeasibleTour() // margins die at home (calls 2,3,4 → 3-strike)
		case "X1-UQ16":
			destCalls++
			// The pre-flight (empty hull) clears the reposition floor so the jump commits.
			// After the jump the hull carries the PARTS manifest → liquidate it as launch
			// cargo (held-liquidation), exactly as the real solver's tourShipState path does.
			plan := &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
				leg("X1-UQ16-A", "X1-UQ16", buy("H", 40, 100)),
				leg("X1-UQ16-B", "X1-UQ16", sell("H", 40, 300)),
			}}
			if parts := ship.Cargo["PARTS"]; parts > 0 {
				plan.Legs = append([]routing.TourLeg{
					leg("X1-UQ16-B", "X1-UQ16", sell("PARTS", parts, 300)),
				}, plan.Legs...)
				plan.HeldLiquidation = int64(parts * 300)
			}
			if destCalls >= 3 {
				return infeasibleTour() // eventually the fresh ground taps too → honest exit
			}
			return plan
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LB", PlayerID: 1, ContainerID: "ctr-lb", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("look-back run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition, got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-UQ16" {
		t.Fatalf("expected one jump to X1-UQ16, got %v", fx.jumps)
	}
	// THE deadhead fix: PARTS must be BOUGHT at the departure system BEFORE the jump.
	fx.mu.Lock()
	timeline := strings.Join(fx.timeline, ",")
	fx.mu.Unlock()
	buyIdx := strings.Index(timeline, "BUY:PARTS")
	if buyIdx < 0 {
		t.Fatalf("look-back must BUY the PARTS export before jumping, timeline=%q", timeline)
	}
	// The PARTS buy must precede the destination's own H trades (i.e. it happened at the
	// departure, pre-jump), and PARTS must be liquidated at the destination (no strand).
	if !strings.Contains(timeline, "SELL:PARTS") {
		t.Fatalf("the carried PARTS manifest must liquidate at the destination, timeline=%q", timeline)
	}
	if r.CargoStranded {
		t.Fatalf("the look-back manifest must be sold at the destination, not stranded: %s", r.CargoStrandedReason)
	}
}

// No cross-system lane clears the floors → the jump flies empty exactly as pre-sp-ed4i
// (loaded-if-profitable, never forced). No look-back buy happens before the jump.
func TestTour_Reposition_NoProfitableManifest_JumpsEmpty(t *testing.T) {
	fx := lookbackFixture()
	// Kill the look-back lane: UQ16 no longer imports PARTS (drop the sink).
	delete(fx.bid["X1-UQ16-B"], "PARTS")
	delete(fx.ask["X1-UQ16-B"], "PARTS")
	delete(fx.tradeType["X1-UQ16-B"], "PARTS")

	homeCalls, destCalls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-HU21":
			homeCalls++
			if homeCalls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
					leg("X1-HU21-A", "X1-HU21", buy("G", 40, 100)),
					leg("X1-HU21-B", "X1-HU21", sell("G", 40, 200)),
				}}
			}
			return infeasibleTour()
		case "X1-UQ16":
			destCalls++
			if destCalls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
					leg("X1-UQ16-A", "X1-UQ16", buy("H", 40, 100)),
					leg("X1-UQ16-B", "X1-UQ16", sell("H", 40, 300)),
				}}
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-EMPTY", PlayerID: 1, ContainerID: "ctr-empty", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("empty-jump run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 || len(fx.jumps) != 1 {
		t.Fatalf("expected exactly one reposition jump, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	fx.mu.Lock()
	timeline := strings.Join(fx.timeline, ",")
	fx.mu.Unlock()
	if strings.Contains(timeline, "BUY:PARTS") {
		t.Fatalf("no floor-clearing manifest exists — the jump must fly empty, timeline=%q", timeline)
	}
}

// Guard: the working-capital reserve (sp-agzj / RULINGS #4) binds the look-back buy exactly
// as it binds a plan buy — a live balance whose headroom cannot afford even one unit skips
// the load (no spend), so look-back never spends past the reserve floor. Driven directly on
// loadLookbackManifest with a low-balance live client, isolating the guard from the run loop.
func TestTour_Lookback_ReserveFloorSkipsBuy(t *testing.T) {
	fx := lookbackFixture()
	// Balance 1,000,050 with a 1,000,000 reserve → 50 headroom / 100 ask = 0 affordable units.
	api := &tourSeqAPIClient{balances: []int{1_000_050}}
	h := newTourHandlerWithAPI(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{}, api)
	ctx := auth.WithPlayerToken(context.Background(), "TOUR-LB-FLOOR")
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LB-FLOOR", PlayerID: 1, ContainerID: "ctr-lb-floor"}
	resp := &RunTourCoordinatorResponse{}

	loaded := h.loadLookbackManifest(ctx, cmd, resp, map[string]int{}, "X1-HU21", "X1-UQ16", 10_000_000, 1_000_000)

	if loaded != 0 {
		t.Fatalf("the working-capital floor must skip the look-back buy (headroom 50 < ask 100), got %d units loaded", loaded)
	}
	if resp.TotalSpent != 0 {
		t.Fatalf("a floor-skipped look-back load spends nothing, got %d", resp.TotalSpent)
	}
	fx.mu.Lock()
	buys := fx.buys
	fx.mu.Unlock()
	if buys != 0 {
		t.Fatalf("no purchase may dispatch when the reserve floor skips the load, got %d buys", buys)
	}
}

// Guard: the live-ask ceiling (sp-9mkf) bounds the look-back buy. A source whose LIVE ask
// has drifted above the margin-preserving ceiling (never above destBid-floor) aborts the
// buy rather than overpaying into a load that no longer clears the margin — the same ceiling
// the plan buy arms. Driven directly on buyLookbackItem with a hand-built item whose cached
// ask (100) diverges from the drifted live ask (400).
func TestTour_Lookback_LiveAskCeilingAbortsBuy(t *testing.T) {
	fx := lookbackFixture()
	fx.ask["X1-HU21-A"]["PARTS"] = 400 // live ask drifted to 400; the manifest was built at 100
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LB-CEIL", PlayerID: 1, ContainerID: "ctr-lb-ceil", MinMargin: 1}
	resp := &RunTourCoordinatorResponse{}
	// Cached prices: ask 100, sink bid 300 → ceiling = min(100*1.15=115, 300-1=299) = 115.
	item := lookbackItem{Good: "PARTS", SourceWaypoint: "X1-HU21-A", Units: 20, SourceAsk: 100, DestBid: 300}

	got := h.buyLookbackItem(context.Background(), cmd, resp, map[string]int{}, item, 10_000_000, 10_000_000, 0)

	if got != 0 {
		t.Fatalf("a live ask (400) above the margin-preserving ceiling (115) must abort the buy, got %d units", got)
	}
	fx.mu.Lock()
	buys := fx.buys
	fx.mu.Unlock()
	if buys != 0 {
		t.Fatalf("the ceiling abort must dispatch no completed purchase, got %d buys", buys)
	}
	if resp.TotalSpent != 0 {
		t.Fatalf("a ceiling-aborted look-back buy spends nothing, got %d", resp.TotalSpent)
	}
}
