package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-sdyo — per-good buy-gating overrides on the ctx-carried gates.
//
// The two ctx-carried gates (the SupplyChainResolver's per-node buy-vs-fabricate strategy and the
// ProductionExecutor's per-tranche input price ceiling) both run on BOOT SINGLETONS shared across
// every concurrent factory container. A per-good override map is per-run config, so — exactly like
// the input price ceiling (WithInputPriceCeiling), the working-capital reserve (WithConfiguredReserve)
// and the fabricate depth cap (WithFabricateDepthCap) — it rides on ctx (per-Handle, race-free), not
// a struct field (which would race between sibling factories running different overrides).
//
// The factory coordinator stamps this once per Handle from its persisted launch config. A caller
// that never stamps it (tests, and the demand/siting estimators) reads an empty map, so every good
// falls back to its GLOBAL default — the no-override, byte-identical-to-today path.
type goodGatingOverridesCtxKey struct{}

// WithGoodGatingOverrides stamps the per-good override map onto ctx (sp-sdyo). An empty/nil map is a
// no-op at the point of use: every good falls back to the global gate default.
func WithGoodGatingOverrides(ctx context.Context, overrides manufacturing.GoodGatingOverrides) context.Context {
	return context.WithValue(ctx, goodGatingOverridesCtxKey{}, overrides)
}

// goodGatingOverridesFromContext reads the per-good override map off ctx, returning an empty map
// when none was stamped (so lookups cleanly fall back to the global default).
func goodGatingOverridesFromContext(ctx context.Context) manufacturing.GoodGatingOverrides {
	if o, ok := ctx.Value(goodGatingOverridesCtxKey{}).(manufacturing.GoodGatingOverrides); ok {
		return o
	}
	return nil
}
