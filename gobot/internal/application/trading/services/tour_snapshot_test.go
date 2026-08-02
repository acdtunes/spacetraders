package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// snapFakeMarketRepo serves a fixed set of markets keyed by waypoint symbol.
type snapFakeMarketRepo struct {
	market.MarketRepository
	order   map[string][]string       // system -> market waypoint order
	markets map[string]*market.Market // waypoint -> market
}

func (r *snapFakeMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return r.order[systemSymbol], nil
}

func (r *snapFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	return r.markets[waypointSymbol], nil
}

// snapFakeWaypointRepo serves era-scoped coordinates per system.
type snapFakeWaypointRepo struct {
	system.WaypointRepository
	byS map[string][]*shared.Waypoint
}

func (r *snapFakeWaypointRepo) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	return r.byS[systemSymbol], nil
}

// mustGood builds one TradeGood from a (bid, ask) pair. purchase_price carries the ASK (what we PAY
// buying FROM the market, the larger of the two); sell_price carries the BID (what we RECEIVE selling
// TO it, the smaller). Every fixture below therefore quotes ask > bid, the only shape a real market
// takes. This helper fed bid into purchasePrice and ask into sellPrice until sp-en5h7; BuildTourSnapshot
// now refuses an ask-below-bid good outright, so that inversion drops every row instead of cancelling out.
func mustGood(t *testing.T, sym string, bid, ask, tv int, supply, activity string, tt market.TradeType) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(sym, &supply, &activity, ask, bid, tv, tt)
	if err != nil {
		t.Fatalf("NewTradeGood(%s): %v", sym, err)
	}
	return *g
}

func mustMarket(t *testing.T, wp string, updated time.Time, goods ...market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(wp, goods, updated)
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", wp, err)
	}
	return m
}

func mustWaypoint(t *testing.T, sym string, x, y float64) *shared.Waypoint {
	t.Helper()
	w, err := shared.NewWaypoint(sym, x, y)
	if err != nil {
		t.Fatalf("NewWaypoint(%s): %v", sym, err)
	}
	return w
}

// A stale good row (older than its activity's cap) is excluded from the snapshot, and
// only snapshot-market waypoints get coordinates — the exact D39 field mapping is
// asserted so a swapped bid/ask or dropped tier can't slip through. C37's SHIP_PARTS
// is RESTRICTED (180-min cap), so a -4h observation is past it.
func TestBuildTourSnapshot_ExcludesStaleAndAssemblesCoords(t *testing.T) {
	now := time.Date(2026, 7, 9, 22, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Minute) // within K79 FUEL's STRONG 30-min cap
	stale := now.Add(-4 * time.Hour)    // past C37 SHIP_PARTS's RESTRICTED 180-min cap

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-NK36": {"X1-NK36-D39", "X1-NK36-K79", "X1-NK36-C37"}},
		markets: map[string]*market.Market{
			"X1-NK36-D39": mustMarket(t, "X1-NK36-D39", now,
				mustGood(t, "MEDICINE", 1844, 1900, 20, "LIMITED", "WEAK", market.TradeTypeExport)),
			"X1-NK36-K79": mustMarket(t, "X1-NK36-K79", fresh,
				mustGood(t, "FUEL", 90, 100, 40, "ABUNDANT", "STRONG", market.TradeTypeImport)),
			"X1-NK36-C37": mustMarket(t, "X1-NK36-C37", stale,
				mustGood(t, "SHIP_PARTS", 500, 600, 6, "SCARCE", "RESTRICTED", market.TradeTypeExport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{
		"X1-NK36": {
			mustWaypoint(t, "X1-NK36-D39", 7, -3),
			mustWaypoint(t, "X1-NK36-K79", 1, 2),
			mustWaypoint(t, "X1-NK36-C37", 5, 5),
			mustWaypoint(t, "X1-NK36-GATE", 0, 0), // non-market → excluded
		},
	}}

	snapshot, waypoints, err := BuildTourSnapshot(context.Background(), repo, wps,
		[]string{"X1-NK36"}, 1, now, trading.DefaultRankerAgeCaps())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	if len(snapshot) != 2 {
		t.Fatalf("expected 2 snapshot rows (C37 stale-excluded), got %d: %+v", len(snapshot), snapshot)
	}
	var med, hasFuel bool
	for _, s := range snapshot {
		switch s.Good {
		case "MEDICINE":
			med = true
			// D39 MEDICINE is an EXPORT good, so its sink-side Bid is zeroed (an exporter
			// is never a sell destination). The Ask (a valid buy source) still maps
			// through exactly — a swapped bid/ask still trips here.
			if s.Waypoint != "X1-NK36-D39" || s.System != "X1-NK36" || s.Supply != "LIMITED" ||
				s.Activity != "WEAK" || s.Ask != 1900 || s.Bid != 0 || s.TradeVolume != 20 {
				t.Fatalf("D39 MEDICINE mapping wrong: %+v", s)
			}
			if !s.ObservedAt.Equal(now) {
				t.Fatalf("D39 ObservedAt = %v, want %v", s.ObservedAt, now)
			}
		case "FUEL":
			hasFuel = true
		case "SHIP_PARTS":
			t.Fatalf("stale C37 SHIP_PARTS leaked into snapshot: %+v", s)
		}
	}
	if !med || !hasFuel {
		t.Fatalf("expected MEDICINE and FUEL rows, med=%v fuel=%v", med, hasFuel)
	}

	// Coordinates only for the two snapshot-market waypoints (D39, K79); the stale
	// C37 and the non-market GATE are excluded.
	if len(waypoints) != 2 {
		t.Fatalf("expected 2 waypoint coords, got %d: %+v", len(waypoints), waypoints)
	}
	coords := map[string][2]int{}
	for _, w := range waypoints {
		if w.System != "X1-NK36" {
			t.Fatalf("waypoint system wrong: %+v", w)
		}
		coords[w.Symbol] = [2]int{w.X, w.Y}
	}
	if coords["X1-NK36-D39"] != [2]int{7, -3} || coords["X1-NK36-K79"] != [2]int{1, 2} {
		t.Fatalf("coords wrong: %+v", coords)
	}
	if _, bad := coords["X1-NK36-C37"]; bad {
		t.Fatalf("stale C37 coords leaked: %+v", coords)
	}
}

// TestBuildTourSnapshot_PerGoodActivityStaleness proves the staleness gate is
// per-GOOD, not per-market: a single market observed once, holding a WEAK
// good and a STRONG good, keeps the WEAK row and drops the STRONG one at the SAME
// observation age — so a market with mixed activities is neither wholly kept nor wholly
// dropped.
func TestBuildTourSnapshot_PerGoodActivityStaleness(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-90 * time.Minute) // past STRONG's 30m cap, within WEAK's 480m cap

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-MIX": {"X1-MIX-M1"}},
		markets: map[string]*market.Market{
			"X1-MIX-M1": mustMarket(t, "X1-MIX-M1", observed,
				mustGood(t, "STABLE_GOOD", 1000, 1100, 20, "LIMITED", "WEAK", market.TradeTypeImport),
				mustGood(t, "FAST_GOOD", 500, 600, 20, "MODERATE", "STRONG", market.TradeTypeImport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{"X1-MIX": {mustWaypoint(t, "X1-MIX-M1", 1, 1)}}}

	snapshot, _, err := BuildTourSnapshot(context.Background(), repo, wps, []string{"X1-MIX"}, 1, now, trading.DefaultRankerAgeCaps())
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}
	if len(snapshot) != 1 {
		t.Fatalf("expected only the WEAK good retained at 90m (STRONG dropped), got %d rows: %+v", len(snapshot), snapshot)
	}
	if snapshot[0].Good != "STABLE_GOOD" || snapshot[0].Activity != "WEAK" {
		t.Fatalf("expected the WEAK STABLE_GOOD retained, got %+v", snapshot[0])
	}
}

// TestBuildTourSnapshot_StaleDrop_IncrementsExclusionCounter proves the counter
// increments once PER dropped stale lane, labeled by system — so a market-rich
// system silently aging out of the plan is visible on tour_lanes_stale_excluded_total,
// not just absent.
func TestBuildTourSnapshot_StaleDrop_IncrementsExclusionCounter(t *testing.T) {
	prevReg := metrics.Registry
	t.Cleanup(func() {
		metrics.Registry = prevReg
		metrics.SetGlobalTourStalenessCollector(nil)
	})
	metrics.InitRegistry()
	coll := metrics.NewTourStalenessMetricsCollector()
	if err := coll.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	metrics.SetGlobalTourStalenessCollector(coll)

	now := time.Date(2026, 7, 9, 22, 0, 0, 0, time.UTC)
	// -9h is past BOTH stale goods' activity caps: ST1 MEDICINE is WEAK (480-min cap)
	// and ST2 SHIP_PARTS is RESTRICTED (180-min cap), so both drop and count as 2.
	stale := now.Add(-9 * time.Hour)

	// One fresh + two stale markets: the two stale drops must count as 2 for the system.
	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-NK36": {"X1-NK36-FRESH", "X1-NK36-ST1", "X1-NK36-ST2"}},
		markets: map[string]*market.Market{
			"X1-NK36-FRESH": mustMarket(t, "X1-NK36-FRESH", now,
				mustGood(t, "FUEL", 90, 100, 40, "ABUNDANT", "STRONG", market.TradeTypeImport)),
			"X1-NK36-ST1": mustMarket(t, "X1-NK36-ST1", stale,
				mustGood(t, "MEDICINE", 1844, 1900, 20, "LIMITED", "WEAK", market.TradeTypeExport)),
			"X1-NK36-ST2": mustMarket(t, "X1-NK36-ST2", stale,
				mustGood(t, "SHIP_PARTS", 500, 600, 6, "SCARCE", "RESTRICTED", market.TradeTypeExport)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{"X1-NK36": {mustWaypoint(t, "X1-NK36-FRESH", 1, 2)}}}

	if _, _, err := BuildTourSnapshot(context.Background(), repo, wps, []string{"X1-NK36"}, 1, now, trading.DefaultRankerAgeCaps()); err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	if got := gatherStaleExcluded(t, "X1-NK36"); got != 2 {
		t.Fatalf("tour_lanes_stale_excluded_total{system=X1-NK36} = %v, want 2", got)
	}
}

// gatherStaleExcluded reads the stale-exclusion counter value for a system off the
// package Registry via Gather() — method-call based so the test needs no dto import.
func gatherStaleExcluded(t *testing.T, system string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_tour_lanes_stale_excluded_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "system" && lp.GetValue() == system {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
