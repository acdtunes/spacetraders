package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// KNOB LIVENESS, restart half (RULINGS #2). The delivery buy/resume floors are pattern-C
// live knobs persisted on the construction pipeline row. A `construction override
// --buy-floor/--resume-floor` write must survive a daemon bounce -- the pattern-B clobber
// this design explicitly avoids has to be pinned by a test, not just by a comment.
func TestConstructionPipelineDeliveryFloorsSurvivePersistReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I1", 1, 3, 5)
	pipeline.SetDeliveryFloors("LIMITED", "MODERATE")
	require.NoError(t, repo.Create(ctx, pipeline))

	// Reload from the DB -- the daemon-bounce equivalent.
	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.NotNil(t, reloaded)

	require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor(), "the live buy floor must survive a daemon restart")
	require.Equal(t, "MODERATE", reloaded.DeliveryResumeFloor(), "the live resume floor must survive a daemon restart")
}

// A LIVE re-tune (the `construction override` write path: load, set, Update) must also
// round-trip -- not only the value set before the first Create.
func TestConstructionPipelineDeliveryFloorsSurviveALiveRetune(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-RETUNE")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I2", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))

	live, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	live.SetDeliveryFloors("HIGH", "ABUNDANT")
	require.NoError(t, repo.Update(ctx, live))

	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.Equal(t, "HIGH", reloaded.DeliveryBuyFloor(), "a live buy-floor tune must persist")
	require.Equal(t, "ABUNDANT", reloaded.DeliveryResumeFloor(), "a live resume-floor tune must persist")
}

// UNSET is the ARMED DEFAULT, not an off switch. A pipeline created before this feature
// (and every pipeline created without the flags) reloads with empty floors, which the
// reader resolves to the MODERATE/HIGH defaults at the point of use. Empty must NOT be
// persisted as some sentinel that a later read mistakes for a real tune.
func TestConstructionPipelineDeliveryFloorsDefaultToUnsetAndRoundTripEmpty(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-UNSET")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I3", 1, 3, 5)
	require.Equal(t, "", pipeline.DeliveryBuyFloor(), "a new pipeline must default the buy floor to unset")
	require.Equal(t, "", pipeline.DeliveryResumeFloor(), "a new pipeline must default the resume floor to unset")
	require.NoError(t, repo.Create(ctx, pipeline))

	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.Equal(t, "", reloaded.DeliveryBuyFloor())
	require.Equal(t, "", reloaded.DeliveryResumeFloor())
}
