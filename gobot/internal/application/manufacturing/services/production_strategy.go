package services

import "context"

// sp-yfzi — ctx-scoped PRODUCTION acquisition strategy (re-enables scarcity-gated recursion,
// reversing the sp-jav2 X1 buy-all-inputs posture).
//
// The SupplyChainResolver is a boot SINGLETON shared across every concurrent factory and
// construction container, and its own default strategy (r.strategy) is StrategyPreferBuy — the
// ESTIMATION default the demand finder and siting scanners rely on (they price a chain by what it
// would cost to buy, not fabricate). The PRODUCTION paths (goods_factory + construction) must
// instead resolve on StrategySmart so a SCARCE/LIMITED intermediate that HAS a factory is
// FABRICATED (recursing its sub-chain to relieve the scarcity) while an ABUNDANT one is still
// BOUGHT (recursion terminates). Mutating the singleton (SetStrategy) would race sibling
// containers running different strategies, so — exactly like the fabricate depth cap
// (WithFabricateDepthCap) and the per-good overrides (WithGoodGatingOverrides) — the per-run
// strategy rides on ctx (per-Handle, race-free), NOT a struct field.
//
// A caller that never stamps it (the demand/siting estimators, and every existing test that builds
// a command directly) reads "" and the resolver falls back to r.strategy (prefer-buy) — the
// no-stamp, byte-identical-to-today path. A per-good override (sp-sdyo, WithGoodGatingOverrides)
// still wins over this global run-strategy at the point of decision.
type productionStrategyCtxKey struct{}

// DefaultProductionStrategy is the strategy the PRODUCTION command builders default to when the
// captain has not set [manufacturing] production_strategy (sp-yfzi, Admiral directive 2026-07-13):
// scarcity-gated recursion runs ON in production without the captain naming it. This only names the
// default an absent/empty config value resolves to; the knob stays operator-tunable and
// dial-back-able (RULINGS #5) — a captain can pin "prefer-buy" to restore the sp-jav2 posture.
const DefaultProductionStrategy = string(StrategySmart)

// WithProductionStrategy stamps the per-run production acquisition strategy onto ctx (sp-yfzi). An
// empty string is a no-op at the point of use: the resolver falls back to its own default strategy,
// so estimators and directly-built commands stay byte-identical to today.
func WithProductionStrategy(ctx context.Context, strategy string) context.Context {
	return context.WithValue(ctx, productionStrategyCtxKey{}, strategy)
}

// productionStrategyFromContext reads the per-run production strategy off ctx, returning "" when
// none was stamped (so the resolver cleanly falls back to its own default strategy).
func productionStrategyFromContext(ctx context.Context) string {
	s, _ := ctx.Value(productionStrategyCtxKey{}).(string)
	return s
}
