package commands

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// supplyTaskTimeout is the per-task deadline, defaulting to constructionSupplyTaskDefaultTimeout.
func (h *RunConstructionCoordinatorHandler) supplyTaskTimeout() time.Duration {
	if h.taskTimeout > 0 {
		return h.taskTimeout
	}
	return constructionSupplyTaskDefaultTimeout
}

// effectiveSupplyTaskTimeout resolves the per-supplyTask BASE deadline for this run: always the
// handler default, with no per-launch override. scaledSupplyTaskTimeout scales it by the material's
// supply-chain depth.
func (h *RunConstructionCoordinatorHandler) effectiveSupplyTaskTimeout() time.Duration {
	return h.supplyTaskTimeout()
}

// scaledSupplyTaskTimeout resolves the per-supplyTask deadline scaled by the material's supply-chain
// DEPTH: a shallow buy-and-haul keeps the flat base (byte-identical); a deep fabricate chain (buy
// inputs -> fabricate -> feed factory -> buy output -> haul) legitimately needs more than one round
// trip, so it gets base*depth, clamped to constructionSupplyTaskMaxTimeout so a genuine hang stays
// bounded. base is effectiveSupplyTaskTimeout, the value depth multiplies.
func (h *RunConstructionCoordinatorHandler) scaledSupplyTaskTimeout(ctx context.Context, task *manufacturing.ManufacturingTask) time.Duration {
	base := h.effectiveSupplyTaskTimeout()
	depth := h.supplyTaskChainDepth(ctx, task)
	return depthScaledTimeout(base, depth, constructionSupplyTaskMaxTimeout)
}

// supplyTaskChainDepth is the effective fabrication depth this task will drive. The drain resolves the
// full scarcity-gated tree for EVERY material regardless of the planner's frozen buy/fabricate decision
// (unified gate-fill), so a task takes the good's static chain depth bounded by the pipeline's
// configured fabricate cap (the depth the resolver actually walks) — even a buy-planned good.
func (h *RunConstructionCoordinatorHandler) supplyTaskChainDepth(ctx context.Context, task *manufacturing.ManufacturingTask) int {
	depth := staticSupplyChainDepth(task.Good(), h.resolveChainDepthCap(ctx, task))
	if depth < 1 {
		depth = 1
	}
	return depth
}

// resolveChainDepthCap is the fabricate depth cap for this task's pipeline, resolved the SAME way
// resolveFabricationTree resolves it: the pipeline's SupplyChainDepth, or constructionDefaultChainDepth
// when unset (<=0). Bounding the timeout depth by this cap makes the timeout scale by the depth the
// resolver will actually fabricate down to (deeper inputs are market-bought, not fabricated).
func (h *RunConstructionCoordinatorHandler) resolveChainDepthCap(ctx context.Context, task *manufacturing.ManufacturingTask) int {
	depthCap := 0
	if task.PipelineID() != "" {
		if pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID()); err == nil && pipeline != nil {
			depthCap = pipeline.SupplyChainDepth()
		}
	}
	if depthCap <= 0 {
		depthCap = constructionDefaultChainDepth
	}
	return depthCap
}

// depthScaledTimeout scales base by chain depth, clamped to [base, ceiling]. depth<=1 returns base
// UNCHANGED (a shallow buy-and-haul keeps the flat default — byte-identical). A pure function so the
// scaling is unit-provable independent of pipeline/task plumbing.
func depthScaledTimeout(base time.Duration, depth int, ceiling time.Duration) time.Duration {
	if depth <= 1 {
		return base
	}
	scaled := time.Duration(depth) * base
	if ceiling > 0 && scaled > ceiling {
		return ceiling
	}
	return scaled
}

// staticSupplyChainDepth is the material's fabrication depth from the STATIC recipe graph
// (goods.GetRequiredInputs): how many tiers of fabricate-from-inputs sit under the good, bounded by
// maxDepth. A leaf/raw good (no recipe inputs) is depth 1. The recipe graph has CYCLES
// (IRON_ORE -> EXPLOSIVES -> LIQUID_* -> MACHINERY -> IRON -> IRON_ORE), so a PATH-visited guard treats
// a good already on the current path as a leaf, and maxDepth is the hard recursion bound — together
// they terminate the walk and cap it at the resolver's fabricate depth. Pure over the static map (no
// market data, no ctx), so it is a cheap, deterministic timeout input.
func staticSupplyChainDepth(good string, maxDepth int) int {
	return chainDepthWalk(good, maxDepth, map[string]bool{})
}

func chainDepthWalk(good string, budget int, path map[string]bool) int {
	if budget <= 0 || path[good] {
		return 0 // budget exhausted, or a cycle on the current path: no further fabricate tier here
	}
	inputs := goods.GetRequiredInputs(good)
	if len(inputs) == 0 {
		return 1 // raw/leaf: one tier (itself)
	}
	path[good] = true
	maxChild := 0
	for _, input := range inputs {
		if d := chainDepthWalk(input, budget-1, path); d > maxChild {
			maxChild = d
		}
	}
	delete(path, good)
	return 1 + maxChild
}
