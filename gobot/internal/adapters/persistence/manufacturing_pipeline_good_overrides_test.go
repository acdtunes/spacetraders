package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-sdyo (RULINGS #2): the per-good buy-gating override map is persisted on the construction
// pipeline row and must survive a daemon bounce. This test saves a pipeline carrying a per-good
// override, reloads it through a fresh repository read (the restart-equivalent), and asserts every
// override field round-trips — so a per-good sourcing-floor loosening is not lost on restart.
func TestManufacturingPipelineGoodOverridesSurvivePersistReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "SDYO-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-SDYO-I1", 1, 3, 5)
	pipeline.SetMinSupply("MODERATE")
	pipeline.SetGoodOverrides(manufacturing.GoodGatingOverrides{
		"SILICON_CRYSTALS": {Strategy: "prefer-buy", PriceCeilingMult: 3.0, MinSupply: "SCARCE"},
	})
	require.NoError(t, repo.Create(ctx, pipeline))

	// Reload from the DB — the daemon-bounce equivalent.
	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.NotNil(t, reloaded)

	require.Equal(t, "MODERATE", reloaded.MinSupply(), "the global floor must round-trip")
	ov := reloaded.GoodOverrides()["SILICON_CRYSTALS"]
	require.Equal(t, "prefer-buy", ov.Strategy, "per-good strategy override must survive a restart")
	require.Equal(t, 3.0, ov.PriceCeilingMult, "per-good price-ceiling override must survive a restart")
	require.Equal(t, "SCARCE", ov.MinSupply, "per-good min-supply override must survive a restart")

	// The reloaded pipeline resolves the per-good floor the same way the task activator does.
	require.Equal(t, "SCARCE", reloaded.GoodOverrides().MinSupplyFor("SILICON_CRYSTALS", reloaded.MinSupply()))
	require.Equal(t, "MODERATE", reloaded.GoodOverrides().MinSupplyFor("FAB_MATS", reloaded.MinSupply()), "a non-overridden good keeps the global floor after reload")
}

// Regression: a pipeline with NO overrides round-trips to an empty map — the common case persists
// nothing extra and reloads clean.
func TestManufacturingPipelineNoOverridesRoundTripsEmpty(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "SDYO-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-SDYO-I2", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))

	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Empty(t, reloaded.GoodOverrides(), "a pipeline with no overrides must reload with an empty map")
}
