package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-yfzi — scarcity-gated recursive PRODUCTION, re-enabled fleet-wide (reverses sp-jav2 X1).
//
// The resolver's own default strategy is prefer-buy (the estimation default). A production path
// stamps WithProductionStrategy(smart) on ctx; under smart the resolver FABRICATES a scarce
// intermediate that HAS a factory (recursing to relieve the scarcity) and BUYS an abundant one
// (recursion terminates). This suite drives the PUBLIC BuildDependencyTree and pins, all through
// the ctx-scoped strategy (NOT SetStrategy) and the RAISED depth-3 default:
//   - a SCARCE intermediate with a factory fabricates recursively;
//   - an ABUNDANT intermediate is bought (terminates);
//   - a SCARCE good with NO factory is bought, never dies (RULING #1);
//   - a MACHINERY<->IRON cycle terminates via the visited guard;
//   - the depth-3 backstop bounds a genuinely-deep scarce chain;
//   - a per-good override still wins over the run-strategy (sp-sdyo regression);
//   - an UN-stamped (estimator) call stays prefer-buy — byte-identical to today.

// smart stamps the production run-strategy (the fleet-wide production default) onto a background ctx.
func smartProductionCtx() context.Context {
	return WithProductionStrategy(context.Background(), DefaultProductionStrategy)
}

// scarceIntermediateResolver wires ADVANCED_CIRCUITRY -> ELECTRONICS -> {SILICON_CRYSTALS, COPPER}.
// ELECTRONICS is SCARCE and HAS a factory (the recursion target); the raw grandchildren are
// ABUNDANT (the natural terminators). factoryFor controls whether ELECTRONICS has a factory.
func scarceIntermediateResolver(electronicsSupply string, electronicsHasFactory bool) *SupplyChainResolver {
	supplyChainMap := map[string][]string{
		"ADVANCED_CIRCUITRY": {"ELECTRONICS"},
		"ELECTRONICS":        {"SILICON_CRYSTALS", "COPPER"},
		"SILICON_CRYSTALS":   {},
		"COPPER":             {},
	}
	factories := map[string]*market.FactoryResult{
		"ADVANCED_CIRCUITRY": {WaypointSymbol: "X1-PS-AC", Supply: "MODERATE", Activity: "STRONG"},
	}
	if electronicsHasFactory {
		factories["ELECTRONICS"] = &market.FactoryResult{WaypointSymbol: "X1-PS-EL", Supply: electronicsSupply, Activity: "STRONG"}
	}
	repo := &depthCapMarketRepo{
		factories: factories,
		buyable: map[string]*market.BestBuyingMarketResult{
			"ELECTRONICS":      {WaypointSymbol: "X1-PS-EL", Supply: electronicsSupply, Activity: "STRONG", SellPrice: 2595},
			"SILICON_CRYSTALS": {WaypointSymbol: "X1-PS-SC", Supply: supplyAbundant, Activity: "STRONG", SellPrice: 50},
			"COPPER":           {WaypointSymbol: "X1-PS-CU", Supply: supplyAbundant, Activity: "STRONG", SellPrice: 40},
		},
	}
	return NewSupplyChainResolver(supplyChainMap, repo)
}

// Under the ctx run-strategy smart + the RAISED depth-3 default, a SCARCE intermediate that has a
// factory FABRICATES (recursing into its own inputs) instead of being bought scarce.
func TestProductionStrategy_SmartFabricatesScarceIntermediate(t *testing.T) {
	resolver := scarceIntermediateResolver(supplyScarce, true)

	root, err := resolver.BuildDependencyTree(smartProductionCtx(), "ADVANCED_CIRCUITRY", "X1-PS", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	if root.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("root ADVANCED_CIRCUITRY must FABRICATE, got %s", root.AcquisitionMethod)
	}
	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("SCARCE ELECTRONICS with a factory must FABRICATE (recurse), got %+v", electronics)
	}
	if len(electronics.Children) != 2 {
		t.Fatalf("fabricated ELECTRONICS must recurse into its 2 inputs, got %d children", len(electronics.Children))
	}
	// The abundant grandchildren terminate the recursion as BUY leaves.
	for _, raw := range []string{"SILICON_CRYSTALS", "COPPER"} {
		child := childByGood(electronics, raw)
		if child == nil || child.AcquisitionMethod != goods.AcquisitionBuy {
			t.Fatalf("ABUNDANT %s must BUY (terminate), got %+v", raw, child)
		}
	}
}

// Regression: an ABUNDANT intermediate is BOUGHT even under smart — the recursion terminates there,
// so an all-abundant chain is byte-identical to the depth-1 era (no scarce good to fabricate).
func TestProductionStrategy_SmartBuysAbundantIntermediate(t *testing.T) {
	resolver := scarceIntermediateResolver(supplyAbundant, true)

	root, err := resolver.BuildDependencyTree(smartProductionCtx(), "ADVANCED_CIRCUITRY", "X1-PS", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("ABUNDANT ELECTRONICS must BUY (terminate), got %+v", electronics)
	}
	if len(electronics.Children) != 0 {
		t.Fatalf("a bought ABUNDANT intermediate must be a leaf, got %d children", len(electronics.Children))
	}
}

// RULING #1 — a SCARCE good with NO factory can only be bought (never fabricated, never dies): smart
// buys it despite the scarcity because there is no factory to relieve it.
func TestProductionStrategy_ScarceWithoutFactoryBuys(t *testing.T) {
	resolver := scarceIntermediateResolver(supplyScarce, false) // ELECTRONICS scarce, NO factory

	root, err := resolver.BuildDependencyTree(smartProductionCtx(), "ADVANCED_CIRCUITRY", "X1-PS", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("SCARCE ELECTRONICS with NO factory must BUY (never dies), got %+v", electronics)
	}
	if len(electronics.Children) != 0 {
		t.Fatalf("a factoryless scarce good must be a BUY leaf, got %d children", len(electronics.Children))
	}
}

// A MACHINERY<->IRON cycle (both scarce, both with factories) terminates via the resolver's
// visited guard rather than recursing forever — BuildDependencyTree surfaces ErrCircularDependency.
func TestProductionStrategy_CycleTerminatesViaVisitedGuard(t *testing.T) {
	supplyChainMap := map[string][]string{
		"MACHINERY": {"IRON"},
		"IRON":      {"MACHINERY"},
	}
	repo := &depthCapMarketRepo{
		factories: map[string]*market.FactoryResult{
			"MACHINERY": {WaypointSymbol: "X1-CY-MA", Supply: supplyScarce, Activity: "STRONG"},
			"IRON":      {WaypointSymbol: "X1-CY-IR", Supply: supplyScarce, Activity: "STRONG"},
		},
		buyable: map[string]*market.BestBuyingMarketResult{
			"MACHINERY": {WaypointSymbol: "X1-CY-MA", Supply: supplyScarce, Activity: "STRONG", SellPrice: 1000},
			"IRON":      {WaypointSymbol: "X1-CY-IR", Supply: supplyScarce, Activity: "STRONG", SellPrice: 900},
		},
	}
	resolver := NewSupplyChainResolver(supplyChainMap, repo)

	_, err := resolver.BuildDependencyTree(smartProductionCtx(), "MACHINERY", "X1-CY", 1)
	if err == nil {
		t.Fatal("a MACHINERY<->IRON cycle must terminate with an error, not build a tree")
	}
	if _, ok := err.(*goods.ErrCircularDependency); !ok {
		t.Fatalf("expected ErrCircularDependency, got %T: %v", err, err)
	}
}

// The depth-3 backstop bounds a genuinely-deep all-scarce chain: A->B->C->D->E where every good is
// scarce with a factory. Smart would fabricate each, but the cap forces the node at depth 3 (D) to a
// BUY leaf, so E is never reached and the tree depth is exactly 4.
func TestProductionStrategy_DepthThreeBackstopBoundsDeepChain(t *testing.T) {
	supplyChainMap := map[string][]string{
		"A": {"B"}, "B": {"C"}, "C": {"D"}, "D": {"E"}, "E": {},
	}
	factories := map[string]*market.FactoryResult{}
	buyable := map[string]*market.BestBuyingMarketResult{}
	for _, g := range []string{"A", "B", "C", "D", "E"} {
		factories[g] = &market.FactoryResult{WaypointSymbol: "X1-DP-" + g, Supply: supplyScarce, Activity: "STRONG"}
		buyable[g] = &market.BestBuyingMarketResult{WaypointSymbol: "X1-DP-" + g, Supply: supplyScarce, Activity: "STRONG", SellPrice: 100}
	}
	resolver := NewSupplyChainResolver(supplyChainMap, &depthCapMarketRepo{factories: factories, buyable: buyable})

	root, err := resolver.BuildDependencyTree(smartProductionCtx(), "A", "X1-DP", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	// Walk A(fab) -> B(fab) -> C(fab) -> D(buy leaf).
	b := childByGood(root, "B")
	c := childByGood(b, "C")
	d := childByGood(c, "D")
	if d == nil || d.AcquisitionMethod != goods.AcquisitionBuy || !d.IsLeaf() {
		t.Fatalf("the depth-3 node D must be forced to a BUY leaf (backstop), got %+v", d)
	}
	if depth := root.TotalDepth(); depth != 4 {
		t.Errorf("depth-3 cap must bound the tree at depth 4 (A->B->C->D), got %d", depth)
	}
}

// sp-sdyo regression — a per-good {strategy:prefer-buy} override wins OVER the smart run-strategy:
// the overridden SCARCE good flips to a BUY leaf while the run-strategy still governs the rest.
func TestProductionStrategy_PerGoodOverrideWinsOverRunStrategy(t *testing.T) {
	resolver := scarceIntermediateResolver(supplyScarce, true)
	overrides := manufacturing.GoodGatingOverrides{"ELECTRONICS": {Strategy: string(StrategyPreferBuy)}}
	ctx := WithGoodGatingOverrides(smartProductionCtx(), overrides)

	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-PS", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("per-good prefer-buy override must flip SCARCE ELECTRONICS to BUY, got %+v", electronics)
	}
	if len(electronics.Children) != 0 {
		t.Fatalf("the overridden good must be a BUY leaf, got %d children", len(electronics.Children))
	}
}

// An estimator call (no WithProductionStrategy stamp) keeps the resolver's own prefer-buy default:
// a SCARCE intermediate with a factory is BOUGHT, byte-identical to the pre-sp-yfzi resolver, so the
// demand finder and siting scanners are unaffected.
func TestProductionStrategy_UnstampedEstimatorStaysPreferBuy(t *testing.T) {
	resolver := scarceIntermediateResolver(supplyScarce, true)

	root, err := resolver.BuildDependencyTree(context.Background(), "ADVANCED_CIRCUITRY", "X1-PS", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}
	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("un-stamped (estimator) call must keep prefer-buy: SCARCE ELECTRONICS BOUGHT, got %+v", electronics)
	}
}
