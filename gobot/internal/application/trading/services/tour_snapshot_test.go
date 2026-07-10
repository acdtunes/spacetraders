package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// snapFakeMarketRepo serves a fixed set of markets keyed by waypoint symbol.
type snapFakeMarketRepo struct {
	market.MarketRepository
	order   map[string][]string        // system -> market waypoint order
	markets map[string]*market.Market   // waypoint -> market
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

func mustGood(t *testing.T, sym string, bid, ask, tv int, supply, activity string, tt market.TradeType) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(sym, &supply, &activity, bid, ask, tv, tt)
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

// A stale market row (older than maxAge) is excluded from the snapshot, and only
// snapshot-market waypoints get coordinates — the exact D39 field mapping is
// asserted so a swapped bid/ask or dropped tier can't slip through.
func TestBuildTourSnapshot_ExcludesStaleAndAssemblesCoords(t *testing.T) {
	now := time.Date(2026, 7, 9, 22, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Minute) // within the 75-min cap
	stale := now.Add(-2 * time.Hour)    // beyond the cap

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
		[]string{"X1-NK36"}, 1, now, 75*time.Minute)
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
			// sp-9mkf (Bug 3): D39 MEDICINE is an EXPORT good, so its sink-side Bid is
			// zeroed (an exporter is never a sell destination). The Ask (a valid buy
			// source) still maps through exactly — a swapped bid/ask still trips here.
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
