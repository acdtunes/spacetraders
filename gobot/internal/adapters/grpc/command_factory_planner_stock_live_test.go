package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// C1 (sp-64je): planner-visible stock is LIVE BY DEFAULT (Admiral order: no
// dark-shipping fleet-wide). The ONLY knob is the emergency escape hatch
// planner_stock_disabled. These pin the end-to-end launch round-trip — live
// config.yaml → injectManufacturingConfig's launch-config write → the registry read
// in buildGoodsFactoryCoordinatorCommand → the built command — and the default-ON
// contract. The shared helpers live in command_factory_input_ceiling_live_test.go.

// ABSENT config → feature ACTIVE (the default-ON contract). With no
// planner_stock_disabled anywhere, the built goods_factory command carries
// PlannerStockDisabled=false, so the coordinator DEPOSITS (runs) on deploy without any
// enablement flip. This is the pin the Admiral's no-dark-shipping ruling mandates.
func TestGoodsFactoryPlannerStockLiveByDefault(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.False(t, cmd.PlannerStockDisabled,
		"absent config must leave planner-visible stock ACTIVE (LIVE by default) — no dark-shipping")
}

// The emergency escape hatch resolves live (RULINGS #5 off-switch).
func TestGoodsFactoryResolvesPlannerStockDisabledFromLiveConfig(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{PlannerStockDisabled: true})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(nil))
	require.True(t, cmd.PlannerStockDisabled)
}

// sp-ts82 live discipline: dropping the escape hatch from config.yaml must CLEAR a
// stale persisted planner_stock_disabled — otherwise a stale disable from a prior boot
// would silently keep the feature off after the captain removed it, re-opening the
// dark-shipping gap the Admiral killed.
func TestGoodsFactoryUnsetLiveClearsStalePersistedPlannerStockDisabled(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	cmd := buildRecoveredGoodsFactoryCommand(t, s, goodsFactoryLaunchConfig(map[string]interface{}{
		"planner_stock_disabled": true, // stale copy from a prior boot
	}))
	require.False(t, cmd.PlannerStockDisabled,
		"unset live must clear the stale persisted disable → feature ACTIVE")
}
