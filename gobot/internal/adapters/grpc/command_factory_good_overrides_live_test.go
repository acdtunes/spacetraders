package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-sdyo: the per-good buy-gating override map is supplied at goods_factory launch as a JSON
// string under the good_gating_overrides config key, decoded into the built command's
// GoodGatingOverrides field. This is the mandated end-to-end round-trip pin, exercising the REAL
// recovery rebuild (buildCommandForType → resolveManufacturingConfig → buildGoodsFactoryCoordinator
// Command). Unlike the global [manufacturing] knobs, this is a PER-LAUNCH key: it is NOT in
// manufacturingConfigKeys, so resolveManufacturingConfig leaves it intact and a daemon bounce keeps
// the overrides (RULINGS #2).

// A launch config carrying a good_gating_overrides JSON blob must produce a command whose
// GoodGatingOverrides field decodes every field of the override — and it survives the recovery
// rebuild's resolveManufacturingConfig pass (RULINGS #2: a daemon bounce keeps the overrides).
func TestGoodsFactoryDecodesGoodGatingOverridesFromLaunchConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	overrides := manufacturing.GoodGatingOverrides{
		"SILICON_CRYSTALS": {Strategy: "prefer-buy", PriceCeilingMult: 3.0, MinSupply: "SCARCE"},
	}

	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"good_gating_overrides": overrides.Encode(),
	}))

	require.Len(t, cmd.GoodGatingOverrides, 1, "the per-launch override must survive the recovery rebuild (RULINGS #2)")
	ov := cmd.GoodGatingOverrides["SILICON_CRYSTALS"]
	require.Equal(t, "prefer-buy", ov.Strategy)
	require.Equal(t, 3.0, ov.PriceCeilingMult)
	require.Equal(t, "SCARCE", ov.MinSupply)
}

// Regression: with no good_gating_overrides key the command carries a nil map — every good stays on
// the global gates, byte-identical to today.
func TestGoodsFactoryNoGoodGatingOverridesIsNil(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.Nil(t, cmd.GoodGatingOverrides, "an absent override key must leave every good on the global gates")
}

// A malformed override blob degrades to nil (no overrides) — the guard-tightening default that
// keeps every good on the global gates, matching the lenient Optional* family.
func TestGoodsFactoryMalformedGoodGatingOverridesDegradesToNil(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"good_gating_overrides": "{not valid json",
	}))
	require.Nil(t, cmd.GoodGatingOverrides, "a malformed override blob must degrade to nil (global gates), not crash the build")
}
