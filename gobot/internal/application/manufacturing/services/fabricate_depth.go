package services

import "context"

// sp-jav2 / FACTORY_DOCTRINE X1 — fabricate depth cap (default RAISED by sp-yfzi).
//
// The SupplyChainResolver used to fabricate any input that lacked a buyable market, recursing
// into its own sub-chain and so on with no bound. sp-jav2 capped that at depth-1 (fabricate the
// output, market-buy every input) on sp-naw6's ruling that raw inputs are ~0.29% of spend and a
// market-buy always wins. That premise fails for a SCARCE mid-tree intermediate: buying an
// ELECTRONICS the whole system is short of depletes/explodes its price and stalls the gate and
// every dependent factory. sp-yfzi (Admiral directive 2026-07-13) consciously overrides the X1
// depth-1 cap and RAISES the default to 3, re-enabling scarcity-gated recursive production
// fleet-wide.
//
// The cap is now a SAFETY BACKSTOP, not the terminator. StrategySmart is the real terminator: it
// recurses ONLY into a SCARCE/LIMITED good that HAS a factory (fabricate to relieve the scarcity)
// and BUYS an abundant one (stop recursing) — so an all-abundant chain is byte-identical to the
// depth-1 era. The cap + the resolver's cycle/visited guard together bound the fan-out so a
// pathological tree can never runaway. disabled=true still restores the fully-unbounded recursion.

const (
	// defaultFabricateMaxDepth is the deepest a node may sit in the dependency tree and still be
	// FABRICATED. Root is depth 0, its direct inputs depth 1. sp-yfzi raised this from 1 to 3: the
	// resolver may now fabricate a scarce intermediate up to three layers down (StrategySmart gates
	// WHICH goods actually recurse — abundant ones still buy), instead of market-buying every input.
	// A 0/absent config value resolves to this at the point of use: the cap turns a guard ON (it
	// never moves money — it only redirects a node between fabricate and market-buy), so a
	// live-by-default is correct (RULINGS #5). It stays operator-tunable via fabricate_max_depth.
	defaultFabricateMaxDepth = 3
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
