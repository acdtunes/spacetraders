package contract

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-u4tv per-trip live-profitability gate -------------------------------
//
// The idle-arb dispatcher launches one arb leg (one buy->sell round trip) per
// candidate lane per pass. Before sp-u4tv it launched any lane whose raw spread
// cleared a flat absolute floor (default 1/unit) with NO fuel subtraction and NO
// relative floor — so the fleet's OWN repeated buys walking a thin EXPORT price up
// (and its dumps walking the IMPORT price down) sailed past the check and it flew
// net-negative-after-fuel legs on the thin tail (observed: POLYNUCLEOTIDES +33/u).
//
// The gate re-prices EVERY pass from the freshly-read ask/bid (never a cached
// spread): net_per_u = (bid - ask) - fuel_per_u, launch ONLY IF
// net_per_u >= max(MinNetProfitPerUnit, ceil(NetProfitFraction × ask)). A lane the
// fleet inflated below the floor is skipped that pass and AUTO-RE-ENTERS the next
// pass its price recovers — a skip never marks the lane mutex, so re-entry is not
// blocked by this dispatcher's own prior refusal.

// profitFixture builds a dispatcher over hub(0,0) + sink(0,50) trading one good,
// on a MUTABLE market map so a test can inflate the hub ask (self-inflation) or
// decay the sink bid (dump decay) between passes and drive the per-trip re-price.
// hubAsk is what the hull PAYS to buy at the hub (hub SellPrice); sinkBid is what
// it RECEIVES selling at the sink (sink PurchasePrice) — the only two prices the
// hub->sink lane turns on.
type profitFixture struct {
	d        *IdleArbDispatcher
	repo     *idleArbFakeShipRepo
	launcher *fakeIdleArbLauncher
	markets  *idleArbFakeMarketRepo
	hub      *shared.Waypoint
	sink     *shared.Waypoint
}

func newProfitFixture(t *testing.T, hulls int, cfg IdleArbConfig, good string, hubAsk, sinkBid int) *profitFixture {
	t.Helper()
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	sink := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)

	repo := &idleArbFakeShipRepo{}
	for i := 0; i < hulls; i++ {
		repo.ships = append(repo.ships, idleArbHull(t, fmt.Sprintf("TORWIND-%d", i+1), hub, testFleet))
	}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{hub.Symbol: hub, sink.Symbol: sink}}
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{}}
	clock := shared.NewRealClock()
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	f := &profitFixture{
		d:        NewIdleArbDispatcher(repo, markets, graph, launcher, nil, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg),
		repo:     repo,
		launcher: launcher,
		markets:  markets,
		hub:      hub,
		sink:     sink,
	}
	f.priceHubAsk(t, good, hubAsk)
	f.priceSinkBid(t, good, sinkBid)
	return f
}

// priceHubAsk re-points the hub's single good so the hull pays hubAsk to buy it.
// The hub's own bid is irrelevant to the hub->sink lane, so it is left just below
// the ask (a realistic exporter spread).
func (f *profitFixture) priceHubAsk(t *testing.T, good string, hubAsk int) {
	t.Helper()
	f.markets.markets[f.hub.Symbol] = marketAt(t, f.hub.Symbol, tradeGood(t, good, hubAsk-1, hubAsk))
}

// priceSinkBid re-points the sink's single good so the hull receives sinkBid
// selling into it. The sink's own ask is irrelevant to the hub->sink lane.
func (f *profitFixture) priceSinkBid(t *testing.T, good string, sinkBid int) {
	t.Helper()
	f.markets.markets[f.sink.Symbol] = marketAt(t, f.sink.Symbol, tradeGood(t, good, sinkBid, sinkBid+1))
}

// defaultProfitCfg is a config that exercises the documented default floor
// (net >= max(100, 20% of buy) after ~35/u fuel) with a single dispatchable hull.
func defaultProfitCfg() IdleArbConfig { return IdleArbConfig{ReserveHulls: 1} }

// The core regression (sp-u4tv): a thin lane the fleet's own buying has inflated
// is STOPPED before it goes net-negative. Modeled as two snapshots of the same
// lane's price walk (self-inflation happens across repeated trips, not within
// one): the base-price snapshot launches; the inflated-price snapshot does NOT
// — and it is refused while its net is still POSITIVE (well before a loss),
// because the floor bites at +100/u, not 0.
func TestArb_ThinLaneInflation_StopsBeforeNetNegative(t *testing.T) {
	// Base price: bid 500, ask 280 -> gross 220, net 220-35=185 >= floor 100 -> fly.
	base := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 280, 500)
	if launched := base.d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("a healthy thin lane (net 185/u) must fly, got %d launches", launched)
	}

	// Inflated buy: bid 500, ask 420 -> gross 80, net 80-35=45. Still POSITIVE, but
	// below the 100/u floor -> the dispatcher STOPS the lane before it loses money.
	inflated := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 420, 500)
	if launched := inflated.d.DispatchOnce(context.Background()); launched != 0 || len(inflated.launcher.launches) != 0 {
		t.Fatalf("a lane inflated below the net floor (net +45/u, still positive) must be stopped BEFORE it goes negative, got %d launches", launched)
	}
	if inflated.d.skipUnprofitable == 0 {
		t.Fatalf("stopping a below-floor lane must be attributed to the profitability gate, got skipUnprofitable=%d", inflated.d.skipUnprofitable)
	}
}

// The sell-side mirror (sp-u4tv): a lane the fleet's own dumps have DECAYED — the
// sink bid walked down while the buy held — is stopped the same way, before it goes
// net-negative. Same net_per_u = bid − ask − fuel comparison, driven from the sink
// side. Proves the guard catches both self-inflation (ask up) and dump decay (bid down).
func TestArb_SinkDecay_StopsTradingWhenBidFalls(t *testing.T) {
	// Healthy sink: ask 280, bid 500 -> net 185 >= floor 100 -> fly.
	healthy := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 280, 500)
	if launched := healthy.d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("a healthy sink bid must fly, got %d launches", launched)
	}

	// Decayed sink: ask 280, bid 340 -> gross 60, net 25. Still positive, below the
	// 100/u floor -> stopped before the dump turns net-negative.
	decayed := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 280, 340)
	if launched := decayed.d.DispatchOnce(context.Background()); launched != 0 || len(decayed.launcher.launches) != 0 {
		t.Fatalf("a sink whose bid decayed below the net floor must be stopped, got %d launches", launched)
	}
	if decayed.d.skipUnprofitable == 0 {
		t.Fatalf("the decayed-sink refusal must be attributed to the profitability gate, got %d", decayed.d.skipUnprofitable)
	}
}

// Self-regulating (sp-u4tv acceptance): a lane skipped for want of net margin
// AUTO-RE-ENTERS the pass its price recovers. Because a profitability skip never
// marks the lane mutex, the recovered lane is free to fly — the dispatcher's own
// prior refusal never latches it off.
func TestArb_AutoReEntersOnRecovery(t *testing.T) {
	f := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 420, 500) // inflated: net +45 < 100

	if launched := f.d.DispatchOnce(context.Background()); launched != 0 {
		t.Fatalf("pass 1 (inflated) must skip the below-floor lane, got %d launches", launched)
	}

	// The source recovers: the export ask falls back to its healthy level.
	f.priceHubAsk(t, "POLYNUCLEOTIDES", 280) // net 185 >= 100

	if launched := f.d.DispatchOnce(context.Background()); launched != 1 || len(f.launcher.launches) != 1 {
		t.Fatalf("the recovered lane must AUTO-RE-ENTER and fly next pass, got %d launches", launched)
	}
}

// The RELATIVE floor (sp-u4tv): net >= 20% of the buy price is what stops a
// HIGH-PRICED good with a thin absolute spread from clearing on the flat 100/u
// floor alone. The expensive lane's net (265) is actually HIGHER than the
// cheap lane's (230), yet only the expensive lane is refused (floor 1000 vs
// floor 100) — proving the guard scales with price, not raw net magnitude.
func TestArb_PctFloorBindsOnHighPricedThinSpread(t *testing.T) {
	// High-priced thin spread: ask 5000, bid 5300 -> gross 300, net 265. The %
	// floor 20%×5000 = 1000 binds; 265 < 1000 -> refused.
	expensive := newProfitFixture(t, 2, defaultProfitCfg(), "PLATINUM", 5000, 5300)
	if launched := expensive.d.DispatchOnce(context.Background()); launched != 0 || len(expensive.launcher.launches) != 0 {
		t.Fatalf("a high-priced thin spread (net 265 but only 5%% of buy) must be refused by the %% floor, got %d launches", launched)
	}
	if expensive.d.skipUnprofitable == 0 {
		t.Fatalf("the %%-floor refusal must be attributed to the profitability gate, got %d", expensive.d.skipUnprofitable)
	}

	// A lower net/u (230) at a cheap buy: ask 280, bid 545 -> gross 265, net 230.
	// The % floor is only 20%×280 = 56, so the flat 100 floor governs and 230 clears.
	cheap := newProfitFixture(t, 2, defaultProfitCfg(), "PLATINUM", 280, 545)
	if launched := cheap.d.DispatchOnce(context.Background()); launched != 1 {
		t.Fatalf("the same net/u at a cheap buy price must fly (flat floor governs, %% floor slack), got %d launches", launched)
	}
}

// The gate scores the completed ROUND-TRIP spread, never a mid-round-trip cash
// position (bead MEASUREMENT NOTE): a lane whose per-unit BUY outlay is large —
// a big treasury dip between the buy and the sell — still flies when its net
// round-trip margin clears the floor. A gate that alarmed on the buy-leg outlay
// would wrongly refuse this profitable leg.
func TestArb_NoGateOnMidRoundTrip(t *testing.T) {
	// ask 5000 (a large per-unit outlay), bid 8000 -> gross 3000, net 2965; the %
	// floor is 1000 and 2965 clears it. The buy leg alone is 5000/u * hold — a deep
	// mid-round-trip dip — yet the round-trip net is strongly positive.
	f := newProfitFixture(t, 2, defaultProfitCfg(), "MEDICINE", 5000, 8000)

	if launched := f.d.DispatchOnce(context.Background()); launched != 1 || len(f.launcher.launches) != 1 {
		t.Fatalf("a net-positive round trip must fly regardless of the size of its buy-leg outlay (no mid-round-trip treasury gate), got %d launches", launched)
	}
}

// A below-floor refusal surfaces in the per-candidate verdict log and the harvest
// summary (message text — the CLI drops metadata maps), so the thin-tail leak the
// gate now plugs is legible where the analyst scan is diffed.
func TestArb_UnprofitableLane_SurfacesInLogs(t *testing.T) {
	logger := &idleArbCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	f := newProfitFixture(t, 2, defaultProfitCfg(), "POLYNUCLEOTIDES", 420, 500) // net +45 < 100
	f.d.DispatchOnce(ctx)

	candidate := logger.messageWithPrefix(t, "Idle-arb candidate:")
	if !strings.Contains(candidate, "verdict skipped:unprofitable") {
		t.Fatalf("candidate line must show skipped:unprofitable, got: %s", candidate)
	}
	summary := logger.messageWithPrefix(t, "Idle-arb harvest:")
	if !strings.Contains(summary, "unprofitable") {
		t.Fatalf("harvest summary must carry the unprofitable skip count, got: %s", summary)
	}
}

// The profitability floor knobs take their documented defaults when unset, so the
// money guard is DEFAULT-ON (fail-closed philosophy: a config that forgets it does
// not silently disable it), matching the sibling MarginVerifyFraction/Blacklist.
func TestIdleArbConfig_ProfitFloorDefaults(t *testing.T) {
	cfg := IdleArbConfig{}.WithDefaults()
	if cfg.MinNetProfitPerUnit != DefaultIdleArbMinNetProfit {
		t.Errorf("MinNetProfitPerUnit default = %d, want %d", cfg.MinNetProfitPerUnit, DefaultIdleArbMinNetProfit)
	}
	if cfg.NetProfitFraction != DefaultIdleArbNetProfitFraction {
		t.Errorf("NetProfitFraction default = %v, want %v", cfg.NetProfitFraction, DefaultIdleArbNetProfitFraction)
	}
	if cfg.FuelCostPerUnit != DefaultIdleArbFuelCostPerUnit {
		t.Errorf("FuelCostPerUnit default = %d, want %d", cfg.FuelCostPerUnit, DefaultIdleArbFuelCostPerUnit)
	}
	// An explicitly-set floor is preserved (a captain retune is honored).
	custom := IdleArbConfig{MinNetProfitPerUnit: 250, NetProfitFraction: 0.30, FuelCostPerUnit: 50}.WithDefaults()
	if custom.MinNetProfitPerUnit != 250 || custom.NetProfitFraction != 0.30 || custom.FuelCostPerUnit != 50 {
		t.Errorf("explicit floor knobs must be preserved, got %+v", custom)
	}
}
