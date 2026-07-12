package commands

import "testing"

// mkt is a fixture helper: a market at waypoint wp in system sys sourcing the given good→price pairs.
func mkt(wp, sys string, goods map[string]int64) MarketSnapshot {
	m := MarketSnapshot{Waypoint: wp, System: sys}
	for g, p := range goods {
		m.Goods = append(m.Goods, MarketGood{Symbol: g, PurchasePrice: p})
	}
	return m
}

// --- coverage dominates: more contract goods sourced ranks first, even when pricier ---

func TestHubSelector_CoverageDominatesCheapness(t *testing.T) {
	markets := []MarketSnapshot{
		// A sources 1 contract good, cheaply.
		mkt("X1-A", "X1", map[string]int64{"IRON": 10, "COPPER": 999}),
		// B sources 2 contract goods, more expensively.
		mkt("X1-B", "X1", map[string]int64{"IRON": 500, "ALUMINUM": 500}),
	}
	hubs := selectContractHubs(markets, []string{"IRON", "ALUMINUM"})
	if len(hubs) != 2 {
		t.Fatalf("expected 2 viable hubs, got %d", len(hubs))
	}
	if hubs[0].Waypoint != "X1-B" {
		t.Fatalf("coverage must dominate: X1-B (covers 2) should rank first, got %s", hubs[0].Waypoint)
	}
	if hubs[0].Coverage != 2 || hubs[1].Coverage != 1 {
		t.Fatalf("coverage counts wrong: got %d, %d", hubs[0].Coverage, hubs[1].Coverage)
	}
}

// --- equal coverage → cheaper average sourcing cost ranks first ---

func TestHubSelector_CheapnessTiebreak(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-PRICEY", "X1", map[string]int64{"IRON": 800}),
		mkt("X1-CHEAP", "X1", map[string]int64{"IRON": 100}),
	}
	hubs := selectContractHubs(markets, []string{"IRON"})
	if hubs[0].Waypoint != "X1-CHEAP" {
		t.Fatalf("equal coverage: cheaper sourcing should rank first, got %s (cost %.0f)", hubs[0].Waypoint, hubs[0].AvgSourceCost)
	}
}

// --- equal coverage + equal cost → denser market (more sourceable goods) ranks first ---

func TestHubSelector_DensityTiebreak(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-THIN", "X1", map[string]int64{"IRON": 100}),
		mkt("X1-DENSE", "X1", map[string]int64{"IRON": 100, "GOLD": 200, "SILVER": 300}),
	}
	hubs := selectContractHubs(markets, []string{"IRON"})
	if hubs[0].Waypoint != "X1-DENSE" {
		t.Fatalf("equal coverage+cost: denser market should rank first, got %s", hubs[0].Waypoint)
	}
	if hubs[0].Density != 3 {
		t.Fatalf("dense market density should be 3 (all sourceable), got %d", hubs[0].Density)
	}
}

// --- fully equal hubs → stable deterministic order by waypoint asc (idempotent selection) ---

func TestHubSelector_WaypointStableTiebreak(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-Z", "X1", map[string]int64{"IRON": 100}),
		mkt("X1-A", "X1", map[string]int64{"IRON": 100}),
	}
	hubs := selectContractHubs(markets, []string{"IRON"})
	if hubs[0].Waypoint != "X1-A" || hubs[1].Waypoint != "X1-Z" {
		t.Fatalf("equal hubs must order by waypoint asc, got %s, %s", hubs[0].Waypoint, hubs[1].Waypoint)
	}
}

// --- viability gate: a market that sources no contract good is not a hub ---

func TestHubSelector_ViabilityGateDropsNonSourcing(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-USELESS", "X1", map[string]int64{"COPPER": 100, "TIN": 200}), // sources neither target
		mkt("X1-GOOD", "X1", map[string]int64{"IRON": 100}),
	}
	hubs := selectContractHubs(markets, []string{"IRON", "ALUMINUM"})
	if len(hubs) != 1 || hubs[0].Waypoint != "X1-GOOD" {
		t.Fatalf("only markets sourcing a contract good qualify; got %d hubs %+v", len(hubs), hubs)
	}
}

// --- fallback: no contract goods yet → rank by density then cheapness (generic dense+cheap hub) ---

func TestHubSelector_FallbackNoContractGoods(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-THIN", "X1", map[string]int64{"IRON": 50}),
		mkt("X1-DENSE", "X1", map[string]int64{"IRON": 400, "GOLD": 400, "SILVER": 400}),
	}
	hubs := selectContractHubs(markets, nil) // no contract signal
	if len(hubs) != 2 {
		t.Fatalf("fallback: all sourceable markets are viable, got %d", len(hubs))
	}
	// In fallback, coverage == density, so the denser market wins on coverage.
	if hubs[0].Waypoint != "X1-DENSE" {
		t.Fatalf("fallback should rank the dense market first, got %s", hubs[0].Waypoint)
	}
	if hubs[0].Coverage != 3 {
		t.Fatalf("fallback coverage should equal sourceable-good count (3), got %d", hubs[0].Coverage)
	}
}

// --- fail-closed: a good with a non-positive price does not count as sourceable ---

func TestHubSelector_ZeroPriceGoodNotSourceable(t *testing.T) {
	markets := []MarketSnapshot{
		mkt("X1-A", "X1", map[string]int64{"IRON": 0}), // 0-price IRON is not really sourceable
	}
	hubs := selectContractHubs(markets, []string{"IRON"})
	if len(hubs) != 0 {
		t.Fatalf("a 0-price good must not make a viable hub, got %d hubs", len(hubs))
	}
}

// --- empty market data → no hubs (fail-closed: the caller then buys nothing) ---

func TestHubSelector_EmptyMarketsNoHubs(t *testing.T) {
	if hubs := selectContractHubs(nil, []string{"IRON"}); len(hubs) != 0 {
		t.Fatalf("no markets → no hubs, got %d", len(hubs))
	}
}
