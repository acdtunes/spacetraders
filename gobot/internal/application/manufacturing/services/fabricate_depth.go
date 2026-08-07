package services

import "context"

// FACTORY_DOCTRINE X1 — fabricate depth cap.
//
// Without a cap the SupplyChainResolver fabricates any input that lacks a buyable market, recursing
// into its own sub-chain and so on with no bound. A depth-1 cap (fabricate the output, market-buy
// every input) is the other extreme, and it fails for a SCARCE mid-tree intermediate: buying one
// the whole system is short of depletes it further, explodes its price, and stalls the gate and
// every dependent factory. The default sits between the two, enabling scarcity-gated recursion.
//
// The cap is a SAFETY BACKSTOP, not the terminator. StrategySmart is the real terminator: it
// recurses ONLY into a SCARCE/LIMITED good that HAS a factory and BUYS an abundant one, so an
// all-abundant chain behaves exactly as a depth-1 cap would. The cap plus the resolver's
// cycle/visited guard bound the fan-out so a pathological tree cannot run away. disabled=true
// restores fully-unbounded recursion.

const (
	// defaultFabricateMaxDepth is the deepest a node may sit in the dependency tree and still be
	// FABRICATED; root is depth 0 and its direct inputs depth 1. StrategySmart gates WHICH goods
	// actually recurse — abundant ones still buy. A 0/absent config value resolves to this at the
	// point of use: the cap turns a guard ON and never moves money, only redirecting a node between
	// fabricate and market-buy, so live-by-default is correct (RULINGS #5). Operator-tunable via
	// fabricate_max_depth.
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

// WithFabricateDepthCap stamps the per-run fabricate depth-cap config onto ctx. A 0
// maxDepth resolves to defaultFabricateMaxDepth at the point of use; disabled=true is the
// emergency off-switch that restores the unbounded recursion (RULINGS #5). A resolver
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
