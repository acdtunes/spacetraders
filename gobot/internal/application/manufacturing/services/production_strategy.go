package services

import "context"

// Ctx-scoped PRODUCTION acquisition strategy.
//
// The SupplyChainResolver is a boot SINGLETON shared across every concurrent factory and
// construction container, and its own default strategy is StrategyPreferBuy — the ESTIMATION
// default the demand finder and siting scanners rely on, since they price a chain by what it would
// cost to BUY rather than fabricate. The PRODUCTION paths must instead resolve on StrategySmart, so
// a SCARCE/LIMITED intermediate that HAS a factory is FABRICATED (recursing its sub-chain to
// relieve the scarcity) while an ABUNDANT one is still BOUGHT and the recursion terminates.
// Mutating the singleton would race sibling containers running different strategies, so the per-run
// strategy rides on ctx, NOT a struct field — the same idiom as the fabricate depth cap and the
// per-good overrides.
//
// A caller that never stamps it (the demand/siting estimators) reads "" and the resolver falls back
// to its own default. A per-good override still wins over this global run-strategy at the point of
// decision.
type productionStrategyCtxKey struct{}

// DefaultProductionStrategy is the strategy the PRODUCTION command builders default to when the
// captain has not set [manufacturing] production_strategy: scarcity-gated recursion runs ON in
// production without the captain naming it. This names only the default an absent/empty config
// value resolves to; the knob stays operator-tunable and dial-back-able (RULINGS #5).
const DefaultProductionStrategy = string(StrategySmart)

// WithProductionStrategy stamps the per-run production acquisition strategy onto ctx. An
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
