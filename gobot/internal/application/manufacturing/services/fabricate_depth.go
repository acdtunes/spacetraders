package services

import "context"

// sp-jav2 / FACTORY_DOCTRINE X1 — fabricate depth cap.
//
// The SupplyChainResolver used to fabricate any input that lacked a buyable market, recursing
// into its own sub-chain and so on with no bound. That recursion is dead weight: raw inputs are
// ~0.29% of spend, and market-buy was ruled the permanently-correct acquisition (sp-naw6, whose
// flip conditions never fired across 397 markets). The furnace class of losses lived in the
// recursion, not in paying a market ask. The cap collapses every layer past the configured depth
// to a market-BUY: the target good is fabricated (lift output) and its inputs are bought (buy
// inputs), which the analyst brief established captures the entire realizable margin.

const (
	// defaultFabricateMaxDepth is the deepest a node may sit in the dependency tree and still be
	// FABRICATED. Root is depth 0, its direct inputs depth 1. With the default of 1, the root is
	// fabricated and every input resolves to a market-BUY — the "depth-1 (buy inputs, lift
	// output)" doctrine. A 0/absent config value resolves to this at the point of use: the cap
	// turns a guard ON (it never moves money), so a live-by-default is correct (RULINGS #5).
	defaultFabricateMaxDepth = 1
)

// fabricateDepthCtxKey carries the per-run depth-cap config from the factory coordinator down to
// the resolver. It rides on ctx (not a struct field) for the same reason as the input price
// ceiling: the SupplyChainResolver is a SINGLETON shared across every concurrent factory
// container, so a struct field would race between sibling factories running different config;
// ctx is per-Handle and race-free.
type fabricateDepthCtxKey struct{}

type fabricateDepthConfig struct {
	maxDepth int
	disabled bool
}

// WithFabricateDepthCap stamps the per-run fabricate depth-cap config onto ctx (sp-jav2). A 0
// maxDepth resolves to defaultFabricateMaxDepth at the point of use; disabled=true is the
// emergency off-switch that restores the original unbounded recursion (RULINGS #5). A resolver
// call that never stamps this (tests, and the demand/siting callers that build trees for
// estimation) keeps the cap at its default depth, enabled.
func WithFabricateDepthCap(ctx context.Context, maxDepth int, disabled bool) context.Context {
	return context.WithValue(ctx, fabricateDepthCtxKey{}, fabricateDepthConfig{maxDepth: maxDepth, disabled: disabled})
}

// fabricateDepthConfigFromContext reads the depth-cap config, resolving an absent/zero maxDepth
// to the live-by-default value.
func fabricateDepthConfigFromContext(ctx context.Context) fabricateDepthConfig {
	cfg, _ := ctx.Value(fabricateDepthCtxKey{}).(fabricateDepthConfig)
	if cfg.maxDepth <= 0 {
		cfg.maxDepth = defaultFabricateMaxDepth
	}
	return cfg
}
