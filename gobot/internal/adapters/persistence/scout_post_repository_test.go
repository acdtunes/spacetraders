package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func newScoutPostTestRepo(t *testing.T) (*persistence.GormScoutPostRepository, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	era := persistence.EraModel{Name: "SP-ERA", AgentSymbol: "SP-AGENT", PlayerID: player.ID}
	require.NoError(t, db.Create(&era).Error)
	return persistence.NewGormScoutPostRepository(db), db, player.ID
}

func TestScoutPostRepo_UpsertThenListActive(t *testing.T) {
	repo, _, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	post := &domainScouting.ScoutPost{
		PlayerID:        playerID,
		SystemSymbol:    "X1-GZ7",
		FreshnessTarget: 45 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		AssignedHull:    "SAT-1",
		TourContainerID: "tour-1",
		CreatedAt:       time.Now(),
	}
	require.NoError(t, repo.Upsert(ctx, post))
	require.NotZero(t, post.ID, "Upsert sets the generated ID")

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	got := posts[0]
	require.Equal(t, "X1-GZ7", got.SystemSymbol)
	require.Equal(t, 45*time.Minute, got.FreshnessTarget)
	require.Equal(t, domainScouting.PostKindStanding, got.Kind)
	require.Equal(t, "SAT-1", got.AssignedHull)
	require.Equal(t, "tour-1", got.TourContainerID)
}

func TestScoutPostRepo_UpsertUpdatesInPlace(t *testing.T) {
	repo, _, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	first := &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-GZ7", FreshnessTarget: time.Hour, Kind: domainScouting.PostKindStanding}
	require.NoError(t, repo.Upsert(ctx, first))
	originalID := first.ID

	// A later Upsert of the same (player, system) updates the same row.
	second := &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-GZ7", FreshnessTarget: 30 * time.Minute, Kind: domainScouting.PostKindSweepOnce, AssignedHull: "SAT-9"}
	require.NoError(t, repo.Upsert(ctx, second))
	require.Equal(t, originalID, second.ID, "the same (player, system) row is reused, not duplicated")

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, posts, 1, "no duplicate row")
	require.Equal(t, 30*time.Minute, posts[0].FreshnessTarget)
	require.Equal(t, domainScouting.PostKindSweepOnce, posts[0].Kind)
	require.Equal(t, "SAT-9", posts[0].AssignedHull)
}

func TestScoutPostRepo_Remove(t *testing.T) {
	repo, _, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}))
	require.NoError(t, repo.Remove(ctx, playerID, "X1-GZ7"))

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Empty(t, posts)

	// Removing a nonexistent post is not an error.
	require.NoError(t, repo.Remove(ctx, playerID, "X1-NOPE"))
}

// A post stamped with a prior era is invisible to ListActive once that era closes
// and a new one opens — the sp-njpu cross-era guard for operational state.
func TestScoutPostRepo_ListActiveIsEraScoped(t *testing.T) {
	repo, db, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-OLD", Kind: domainScouting.PostKindStanding}))

	// Close the current era and open a fresh one (universe reset).
	require.NoError(t, db.Model(&persistence.EraModel{}).
		Where("player_id = ?", playerID).
		Update("closed_at", time.Now()).Error)

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Empty(t, posts, "a post from a closed era must not leak into the new era")

	// Opening a new era + re-adding revives the (player, system) row under the new era.
	newEra := persistence.EraModel{Name: "SP-ERA-2", AgentSymbol: "SP-AGENT", PlayerID: playerID}
	require.NoError(t, db.Create(&newEra).Error)
	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-OLD", Kind: domainScouting.PostKindStanding}))

	posts, err = repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, posts, 1, "re-adding in the new era revives the row")

	// And exactly one row exists — the re-add reused it rather than colliding on the
	// unique (player, system) index.
	var count int64
	require.NoError(t, db.Model(&persistence.ScoutPostModel{}).Where("player_id = ?", playerID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestScoutPostRepo_ListActiveEmptyWhenNoOpenEra(t *testing.T) {
	repo, db, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding}))

	require.NoError(t, db.Model(&persistence.EraModel{}).Where("player_id = ?", playerID).Update("closed_at", time.Now()).Error)

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Empty(t, posts, "no open era → no live posts")

	// Upsert without an open era is refused rather than writing an unscoped row.
	err = repo.Upsert(ctx, &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-NEW", Kind: domainScouting.PostKindStanding})
	require.Error(t, err)
}
