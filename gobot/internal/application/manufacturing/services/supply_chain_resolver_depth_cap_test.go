package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-jav2 / FACTORY_DOCTRINE X1: recursive fabricate ladders past depth-1 are dead weight —
// raw inputs are a negligible share of spend and market-buy was ruled permanently correct
// (sp-naw6). The resolver must therefore cap fabrication at the root: the target good is
// fabricated (lift output) and its inputs resolve to a market-BUY (buy inputs), never a
// recursive sub-chain. buyGood re-resolves the source at buy time and parks gracefully if none
// exists, so a market-less BUY node is safe.
//
// The suite drives BuildDependencyTree over a TWO-level fabricable chain
// (ADVANCED_CIRCUITRY -> ELECTRONICS -> {SILICON_CRYSTALS, COPPER}) where the depth-1 input
// ELECTRONICS is itself fabricable and NOT buyable — the only shape under which the runtime
// prefer-buy strategy would recurse. It pins that the cap collapses the depth-2 sub-chain to a
// BUY leaf, and that disabling the cap restores the original unbounded recursion.

// depthCapMarketRepo answers only the two reads the resolver makes. A good is fabricable
// in-system iff it has a factory entry; a good is buyable iff it has a buyable entry.
type depthCapMarketRepo struct {
	market.MarketRepository
	factories map[string]*market.FactoryResult
	buyable   map[string]*market.BestBuyingMarketResult
}

func (r *depthCapMarketRepo) FindFactoryForGood(_ context.Context, good, _ string, _ int) (*market.FactoryResult, error) {
	if f, ok := r.factories[good]; ok {
		return f, nil
	}
	return nil, nil
}

func (r *depthCapMarketRepo) FindBestMarketForBuying(_ context.Context, good, _ string, _ int) (*market.BestBuyingMarketResult, error) {
	if m, ok := r.buyable[good]; ok {
		return m, nil
	}
	return nil, nil
}

// twoLevelChainResolver wires the ADVANCED_CIRCUITRY -> ELECTRONICS -> {SILICON_CRYSTALS, COPPER}
// scenario: root and its input are fabricable; the two raw grandchildren are buyable; the
// depth-1 input ELECTRONICS is deliberately NOT buyable so the uncapped resolver fabricates it.
func twoLevelChainResolver() *SupplyChainResolver {
	supplyChainMap := map[string][]string{
		"ADVANCED_CIRCUITRY": {"ELECTRONICS"},
		"ELECTRONICS":        {"SILICON_CRYSTALS", "COPPER"},
		"SILICON_CRYSTALS":   {},
		"COPPER":             {},
	}
	repo := &depthCapMarketRepo{
		factories: map[string]*market.FactoryResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "X1-AA-AC", Supply: "MODERATE", Activity: "STRONG"},
			"ELECTRONICS":        {WaypointSymbol: "X1-AA-EL", Supply: "MODERATE", Activity: "STRONG"},
		},
		buyable: map[string]*market.BestBuyingMarketResult{
			"SILICON_CRYSTALS": {WaypointSymbol: "X1-AA-SC", Supply: "ABUNDANT", Activity: "STRONG", SellPrice: 10},
			"COPPER":           {WaypointSymbol: "X1-AA-CU", Supply: "ABUNDANT", Activity: "STRONG", SellPrice: 12},
		},
	}
	return NewSupplyChainResolver(supplyChainMap, repo)
}

// TestDepthCapCollapsesDepth2FabricateToBuy pins the cap-at-1 collapse: with maxDepth stamped to 1
// (the sp-jav2 X1 posture — no longer the default after sp-yfzi raised it to 3), a depth-1 input
// that would otherwise fabricate is resolved to a market-BUY leaf, and no depth-2 sub-chain is
// built. The cap value is now an explicit override rather than the default.
func TestDepthCapCollapsesDepth2FabricateToBuy(t *testing.T) {
	resolver := twoLevelChainResolver()

	ctx := WithFabricateDepthCap(context.Background(), 1, false) // sp-yfzi: pin the depth-1 cap explicitly
	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-AA", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree returned error: %v", err)
	}

	if root.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("root ADVANCED_CIRCUITRY: expected FABRICATE (lift output), got %s", root.AcquisitionMethod)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root: expected exactly 1 input (ELECTRONICS), got %d", len(root.Children))
	}

	input := root.Children[0]
	if input.Good != "ELECTRONICS" {
		t.Fatalf("depth-1 input: expected ELECTRONICS, got %s", input.Good)
	}
	if input.AcquisitionMethod != goods.AcquisitionBuy {
		t.Errorf("depth-1 input ELECTRONICS: expected BUY (cap forces market-buy), got %s", input.AcquisitionMethod)
	}
	if len(input.Children) != 0 {
		t.Errorf("depth-1 input ELECTRONICS: expected a BUY leaf with no sub-chain, got %d children", len(input.Children))
	}
	if depth := root.TotalDepth(); depth != 2 {
		t.Errorf("capped tree depth: expected 2 (output + bought inputs), got %d", depth)
	}
}

// TestDepthCapDisabledRestoresRecursion pins the emergency off-switch: with the cap disabled the
// resolver returns to its original unbounded behavior — the depth-1 input ELECTRONICS is
// fabricated and its own inputs are recursively resolved (a depth-2 sub-chain).
func TestDepthCapDisabledRestoresRecursion(t *testing.T) {
	resolver := twoLevelChainResolver()

	ctx := WithFabricateDepthCap(context.Background(), 0, true) // disabled
	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-AA", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree returned error: %v", err)
	}

	if len(root.Children) != 1 {
		t.Fatalf("root: expected exactly 1 input (ELECTRONICS), got %d", len(root.Children))
	}
	input := root.Children[0]
	if input.Good != "ELECTRONICS" || input.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("depth-1 input with cap disabled: expected ELECTRONICS FABRICATE, got %s %s", input.Good, input.AcquisitionMethod)
	}
	if len(input.Children) != 2 {
		t.Errorf("depth-1 input ELECTRONICS with cap disabled: expected its 2 inputs recursively resolved, got %d children", len(input.Children))
	}
	if depth := root.TotalDepth(); depth != 3 {
		t.Errorf("uncapped tree depth: expected 3 (output -> input -> raws), got %d", depth)
	}
}

// TestDepthCapTwoLetsInputFabricate pins that the cap is a configurable knob, not a hardcoded 1:
// raising maxDepth to 2 permits one more fabrication layer, so ELECTRONICS (depth 1) may
// fabricate while its raw inputs (depth 2) are bought.
func TestDepthCapTwoLetsInputFabricate(t *testing.T) {
	resolver := twoLevelChainResolver()

	ctx := WithFabricateDepthCap(context.Background(), 2, false)
	root, err := resolver.BuildDependencyTree(ctx, "ADVANCED_CIRCUITRY", "X1-AA", 1)
	if err != nil {
		t.Fatalf("BuildDependencyTree returned error: %v", err)
	}

	input := root.Children[0]
	if input.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Errorf("with maxDepth=2, depth-1 ELECTRONICS: expected FABRICATE, got %s", input.AcquisitionMethod)
	}
	if depth := root.TotalDepth(); depth != 3 {
		t.Errorf("maxDepth=2 tree depth: expected 3, got %d", depth)
	}
}
