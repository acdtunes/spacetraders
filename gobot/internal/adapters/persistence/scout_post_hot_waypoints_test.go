package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The stage-2 hot set persists through the full-row Upsert and reads back
// through both ListActive and the tour's narrow HotWaypoints seam. A stage-1
// post (no set) keeps the column NULL, so every pre-existing row reads as
// unrestricted.
func TestScoutPostRepo_HotWaypointsRoundTripAndNarrowRead(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-HOT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA", AgentSymbol: "SP-HOT", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	post := &domainScouting.ScoutPost{
		PlayerID:        player.ID,
		SystemSymbol:    "X1-HOT",
		FreshnessTarget: 15 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		Hulls:           1,
		HotWaypoints:    []string{"X1-HOT-A1", "X1-HOT-C3"},
	}
	require.NoError(t, repo.Upsert(ctx, post))

	listed, err := repo.ListActive(ctx, player.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, []string{"X1-HOT-A1", "X1-HOT-C3"}, listed[0].HotWaypoints,
		"the hot set must survive the Upsert/ListActive round trip")

	hot, err := repo.HotWaypoints(ctx, player.ID, "X1-HOT")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-HOT-A1", "X1-HOT-C3"}, hot)

	// Dropping back to stage 1 is the same full-row write with the set cleared,
	// and it clears the COLUMN to NULL — not to a "[]" that a raw reader could
	// mistake for a restriction.
	post.HotWaypoints = nil
	require.NoError(t, repo.Upsert(ctx, post))
	hot, err = repo.HotWaypoints(ctx, player.ID, "X1-HOT")
	require.NoError(t, err)
	require.Empty(t, hot)
	var raw *string
	require.NoError(t, db.Raw("SELECT hot_waypoints FROM scout_posts WHERE system_symbol = ?", "X1-HOT").Scan(&raw).Error)
	require.Nil(t, raw, "an empty hot set must persist as NULL")
}

// The sweep-once exemption lives at the read seam too: even a sweep row that
// adversarially CARRIES a hot set reads empty — a sweep's one pass is the
// system's first scan and must see everything. A missing post reads empty for
// the same reason.
func TestScoutPostRepo_HotWaypoints_SweepOnceAndMissingReadEmpty(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-HOT2", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA2", AgentSymbol: "SP-HOT2", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{
		PlayerID:        player.ID,
		SystemSymbol:    "X1-SWP",
		FreshnessTarget: 15 * time.Minute,
		Kind:            domainScouting.PostKindSweepOnce,
		Hulls:           1,
		HotWaypoints:    []string{"X1-SWP-A1"}, // adversarial: the field should never be stamped on a sweep
	}))

	hot, err := repo.HotWaypoints(ctx, player.ID, "X1-SWP")
	require.NoError(t, err)
	require.Empty(t, hot, "a sweep-once post must read unrestricted whatever its row carries")

	hot, err = repo.HotWaypoints(ctx, player.ID, "X1-NOWHERE")
	require.NoError(t, err)
	require.Empty(t, hot, "a missing post is stage 1 — no restriction")
}

// Scope adversaries: another player's hot set must never restrict THIS
// player's tour, and a between-eras gap reads unrestricted rather than
// erroring — both mirror the IsDormant seam.
func TestScoutPostRepo_HotWaypoints_ScopedToPlayerAndOpenEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := persistence.PlayerModel{AgentSymbol: "SP-MINE2", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&mine).Error)
	rival := persistence.PlayerModel{AgentSymbol: "SP-RIVL2", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)
	era := persistence.EraModel{Name: "SP-ERA3", AgentSymbol: "SP-MINE2", PlayerID: mine.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{
		PlayerID:        rival.ID,
		SystemSymbol:    "X1-SHARED",
		FreshnessTarget: 15 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		Hulls:           1,
		HotWaypoints:    []string{"X1-SHARED-A1"},
	}))

	hot, err := repo.HotWaypoints(ctx, mine.ID, "X1-SHARED")
	require.NoError(t, err)
	require.Empty(t, hot, "the rival's hot set leaked across the player scope")

	noEraDB, err := database.NewTestConnection()
	require.NoError(t, err)
	noEraRepo := persistence.NewGormScoutPostRepository(noEraDB)
	hot, err = noEraRepo.HotWaypoints(ctx, 1, "X1-ANY")
	require.NoError(t, err)
	require.Empty(t, hot, "the between-eras gap reads unrestricted, mirroring ListActive/IsDormant")
}
