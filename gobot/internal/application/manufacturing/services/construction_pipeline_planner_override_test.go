package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-sdyo gate 3 (minSupply SOURCING FLOOR) — per-good construction sourcing-floor override.
//
// The construction pipeline's EXPORT sourcing floor is GLOBAL (default MODERATE). A per-good
// override lowers the floor for a single bottleneck material so it can be sourced from a SCARCE
// market, while every other material keeps the global floor. This suite drives the planner over a
// site with a SCARCE-only bottleneck (ADVANCED_CIRCUITRY) and an ABUNDANT normal good (FAB_MATS)
// and pins: without an override the SCARCE good DEFERS (rejected at the MODERATE floor); a per-good
// {minSupply:SCARCE} override sources it (surgical unstick) while the normal good is byte-identical;
// and the override is persisted on the pipeline for restart-resilience (RULINGS #2).

const (
	ovFabWP  = "X1-PZ28-F56"
	ovCircWP = "X1-PZ28-D40"
)

func overrideConstructionMarketRepo(t *testing.T) *plannerStubMarketRepo {
	t.Helper()
	return &plannerStubMarketRepo{
		marketWaypoints: []string{ovFabWP, ovCircWP},
		markets: map[string]*market.Market{
			ovFabWP:  newTradeTypeMarket(t, ovFabWP, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
			ovCircWP: newTradeTypeMarket(t, ovCircWP, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// Baseline (no override): the SCARCE-only ADVANCED_CIRCUITRY DEFERS under the global MODERATE floor
// (a SCARCE export is rejected), while the ABUNDANT FAB_MATS is sourced.
func TestConstructionOverride_BaselineDefersScarceGood(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))

	// depth 3 = "buy final only": fabrication is skipped, so an unbuyable material can only DEFER.
	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !contains(result.DeferredMaterials, "ADVANCED_CIRCUITRY") {
		t.Fatalf("baseline MODERATE floor: SCARCE ADVANCED_CIRCUITRY must DEFER, got deferred=%v", result.DeferredMaterials)
	}
	if contains(result.DeferredMaterials, "FAB_MATS") {
		t.Fatalf("ABUNDANT FAB_MATS must be sourced, not deferred, got deferred=%v", result.DeferredMaterials)
	}
}

// SURGICAL UNSTICK (acceptance): a per-good {minSupply:SCARCE} override sources the SCARCE
// bottleneck (no longer deferred) while the normal FAB_MATS is byte-identical (never deferred), and
// the override map is persisted on the pipeline row so a daemon bounce keeps it (RULINGS #2).
func TestConstructionOverride_PerGoodFloorSourcesBottleneckOnly(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))

	overrides := manufacturing.GoodGatingOverrides{"ADVANCED_CIRCUITRY": {MinSupply: "SCARCE"}}
	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", overrides)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	// The overridden bottleneck is now sourced (down to SCARCE), not deferred.
	if contains(result.DeferredMaterials, "ADVANCED_CIRCUITRY") {
		t.Fatalf("a {minSupply:SCARCE} override must SOURCE the SCARCE good, but it deferred: %v", result.DeferredMaterials)
	}
	circTask := findTaskByGood(result.Pipeline, "ADVANCED_CIRCUITRY")
	if circTask == nil || circTask.SourceMarket() != ovCircWP {
		t.Fatalf("the overridden good must be planned as a direct buy from its SCARCE source %s, got %+v", ovCircWP, circTask)
	}
	// Regression: the non-overridden good keeps the global floor — never deferred, unchanged.
	if contains(result.DeferredMaterials, "FAB_MATS") {
		t.Fatalf("the non-overridden FAB_MATS must be byte-identical (sourced), got deferred=%v", result.DeferredMaterials)
	}

	// RULINGS #2: the override map is persisted on the pipeline row so it survives a restart and is
	// re-read by the deferred-material recovery loop.
	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected exactly 1 pipeline persisted, got %d", len(pipelineRepo.created))
	}
	persisted := pipelineRepo.created[0].GoodOverrides()
	if persisted["ADVANCED_CIRCUITRY"].MinSupply != "SCARCE" {
		t.Fatalf("the persisted pipeline must carry the per-good override for restart-resilience, got %+v", persisted)
	}
}
