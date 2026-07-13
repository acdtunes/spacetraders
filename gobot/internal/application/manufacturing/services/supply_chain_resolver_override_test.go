package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-sdyo gate 1 (SUPPLY-STRATEGY) — per-good acquisition-strategy override.
//
// The resolver's strategy is GLOBAL; a per-good override on ctx lets a single bottleneck good use
// its own strategy while every other good keeps the global one. This suite drives the PUBLIC
// BuildDependencyTree over a two-input chain with the depth cap DISABLED (so the strategy — not the
// depth-1 buy-inputs cap — controls whether a fabricable input becomes a FABRICATE subtree or a BUY
// leaf) and pins: under global Smart a SCARCE fabricable input FABRICATES; a {strategy:prefer-buy}
// override flips THAT good to a BUY leaf (surgical unstick); and a second, non-overridden MODERATE
// good is byte-identical across both runs.

// overrideChainResolver wires ADVANCED_CIRCUITRY -> {ELECTRONICS, COPPER}; ELECTRONICS -> {SILICON}.
// ELECTRONICS is the SCARCE bottleneck (fabricable: recipe + factory + buyable@SCARCE); COPPER is a
// normal MODERATE good (fabricable, buyable@MODERATE). Global strategy = Smart.
func overrideChainResolver() *SupplyChainResolver {
	supplyChainMap := map[string][]string{
		"ADVANCED_CIRCUITRY": {"ELECTRONICS", "COPPER"},
		"ELECTRONICS":        {"SILICON_CRYSTALS"},
		"COPPER":             {"COPPER_ORE"},
		"SILICON_CRYSTALS":   {},
		"COPPER_ORE":         {},
	}
	repo := &depthCapMarketRepo{
		factories: map[string]*market.FactoryResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "X1-OV-AC", Supply: "MODERATE", Activity: "STRONG"},
			"ELECTRONICS":        {WaypointSymbol: "X1-OV-EL", Supply: "MODERATE", Activity: "STRONG"},
			"COPPER":             {WaypointSymbol: "X1-OV-CU", Supply: "MODERATE", Activity: "STRONG"},
		},
		buyable: map[string]*market.BestBuyingMarketResult{
			"ELECTRONICS":      {WaypointSymbol: "X1-OV-EL", Supply: supplyScarce, Activity: "STRONG", SellPrice: 5000},
			"COPPER":           {WaypointSymbol: "X1-OV-CU", Supply: supplyModerate, Activity: "STRONG", SellPrice: 300},
			"SILICON_CRYSTALS": {WaypointSymbol: "X1-OV-SC", Supply: supplyAbundant, Activity: "STRONG", SellPrice: 50},
			"COPPER_ORE":       {WaypointSymbol: "X1-OV-CO", Supply: supplyAbundant, Activity: "STRONG", SellPrice: 40},
		},
	}
	r := NewSupplyChainResolver(supplyChainMap, repo)
	r.SetStrategy(StrategySmart)
	return r
}

func childByGood(root *goods.SupplyChainNode, good string) *goods.SupplyChainNode {
	for _, c := range root.Children {
		if c.Good == good {
			return c
		}
	}
	return nil
}

// Baseline (no override): under global Smart with the cap disabled, the SCARCE bottleneck
// ELECTRONICS FABRICATES (Smart fabricates SCARCE/LIMITED) and the MODERATE COPPER is BOUGHT.
func TestStrategyOverride_BaselineSmartFabricatesScarceInput(t *testing.T) {
	resolver := overrideChainResolver()
	ctx := WithFabricateDepthCap(context.Background(), 0, true) // cap disabled so strategy controls the shape

	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-OV", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}

	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("baseline Smart: SCARCE ELECTRONICS must FABRICATE, got %+v", electronics)
	}
	if len(electronics.Children) == 0 {
		t.Fatalf("baseline Smart: fabricated ELECTRONICS must recurse into its inputs, got a leaf")
	}
	copper := childByGood(root, "COPPER")
	if copper == nil || copper.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("baseline Smart: MODERATE COPPER must BUY, got %+v", copper)
	}
}

// SURGICAL UNSTICK (acceptance): a {strategy:prefer-buy} override on the SCARCE bottleneck flips it
// to a BUY leaf (bought aggressively despite SCARCE), while the non-overridden MODERATE COPPER is
// byte-identical to the baseline (still a BUY leaf at the same waypoint) — Smart still governs it.
func TestStrategyOverride_PreferBuyFlipsBottleneckOnly(t *testing.T) {
	resolver := overrideChainResolver()
	overrides := manufacturing.GoodGatingOverrides{"ELECTRONICS": {Strategy: "prefer-buy"}}
	ctx := WithGoodGatingOverrides(WithFabricateDepthCap(context.Background(), 0, true), overrides)

	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-OV", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree error: %v", err)
	}

	electronics := childByGood(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("override prefer-buy: SCARCE ELECTRONICS must flip to BUY, got %+v", electronics)
	}
	if len(electronics.Children) != 0 {
		t.Fatalf("override prefer-buy: bought ELECTRONICS must be a leaf (no fabricate sub-chain), got %d children", len(electronics.Children))
	}

	// Regression: the non-overridden MODERATE good is unchanged — still BOUGHT, same waypoint.
	copper := childByGood(root, "COPPER")
	if copper == nil || copper.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("override on ELECTRONICS must NOT change COPPER: expected BUY, got %+v", copper)
	}
	if copper.WaypointSymbol != "X1-OV-CU" {
		t.Fatalf("non-overridden COPPER must resolve its usual source (byte-identical), got %q", copper.WaypointSymbol)
	}
}
