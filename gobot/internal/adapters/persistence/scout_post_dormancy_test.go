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

// Dormant persists through the full-row Upsert (the caller owns every field —
// Upsert never merges) and reads back through both ListActive and the tour's
// narrow IsDormant seam. Only an explicitly dormant post parks a probe: a
// missing post and another player's post both read active.
func TestScoutPostRepo_DormantRoundTripAndIsDormant(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-DORM", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA", AgentSymbol: "SP-DORM", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	post := &domainScouting.ScoutPost{
		PlayerID:        player.ID,
		SystemSymbol:    "X1-DORM",
		FreshnessTarget: 15 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		Hulls:           1,
		Dormant:         true,
	}
	require.NoError(t, repo.Upsert(ctx, post))

	listed, err := repo.ListActive(ctx, player.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.True(t, listed[0].Dormant, "Dormant must survive the Upsert/ListActive round trip")

	dormant, err := repo.IsDormant(ctx, player.ID, "X1-DORM")
	require.NoError(t, err)
	require.True(t, dormant)

	// Waking the post is the same full-row write with the bit cleared.
	post.Dormant = false
	require.NoError(t, repo.Upsert(ctx, post))
	dormant, err = repo.IsDormant(ctx, player.ID, "X1-DORM")
	require.NoError(t, err)
	require.False(t, dormant)
}

// A system with no post is NOT dormant — nothing may park a probe but an
// explicit rotation bit.
func TestScoutPostRepo_IsDormant_MissingPostReadsActive(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-DORM2", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA2", AgentSymbol: "SP-DORM2", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)

	dormant, err := repo.IsDormant(context.Background(), player.ID, "X1-NOWHERE")
	require.NoError(t, err)
	require.False(t, dormant)
}

// Another player's dormant post must never park THIS player's probe: the
// adversarial rival row carries dormant=true and must not leak across the
// player scope.
func TestScoutPostRepo_IsDormant_OtherPlayersBitDoesNotLeak(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	mine := persistence.PlayerModel{AgentSymbol: "SP-MINE", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&mine).Error)
	rival := persistence.PlayerModel{AgentSymbol: "SP-RIVL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)
	era := persistence.EraModel{Name: "SP-ERA3", AgentSymbol: "SP-MINE", PlayerID: mine.ID}
	require.NoError(t, db.Create(&era).Error)
	repo := persistence.NewGormScoutPostRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{
		PlayerID:        rival.ID,
		SystemSymbol:    "X1-SHARED",
		FreshnessTarget: 15 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		Hulls:           1,
		Dormant:         true,
	}))

	dormant, err := repo.IsDormant(ctx, mine.ID, "X1-SHARED")
	require.NoError(t, err)
	require.False(t, dormant, "the rival's dormant bit leaked across the player scope")
}

// With no open era nothing is live, so nothing is dormant — the between-eras
// gap reads active rather than erroring, mirroring ListActive.
func TestScoutPostRepo_IsDormant_NoOpenEraReadsActive(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormScoutPostRepository(db)

	dormant, err := repo.IsDormant(context.Background(), 1, "X1-ANY")
	require.NoError(t, err)
	require.False(t, dormant)
}
