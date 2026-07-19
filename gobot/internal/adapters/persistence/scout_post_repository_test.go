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

// The respawn-loop cap's per-post counter and park deadline round-trip through the DB
// (RULINGS #2): the reconciler reloads the consecutive-respawn count and any park
// window on boot, so a persistently-crashing post stays capped across a daemon restart rather
// than the crash-loop resuming at tick cadence. A fresh post reads zero/unset.
func TestScoutPostRepo_RespawnCapFieldsRoundTrip(t *testing.T) {
	repo, _, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	parkedUntil := time.Now().Add(30 * time.Minute).UTC()
	capped := &domainScouting.ScoutPost{
		PlayerID:           playerID,
		SystemSymbol:       "X1-GZ7",
		FreshnessTarget:    time.Hour,
		Kind:               domainScouting.PostKindStanding,
		RespawnAttempts:    7,
		RespawnParkedUntil: parkedUntil,
		CreatedAt:          time.Now(),
	}
	require.NoError(t, repo.Upsert(ctx, capped))

	fresh := &domainScouting.ScoutPost{PlayerID: playerID, SystemSymbol: "X1-QW1", FreshnessTarget: time.Hour, Kind: domainScouting.PostKindStanding}
	require.NoError(t, repo.Upsert(ctx, fresh))

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	bySystem := map[string]*domainScouting.ScoutPost{}
	for _, p := range posts {
		bySystem[p.SystemSymbol] = p
	}

	gotCapped := bySystem["X1-GZ7"]
	require.Equal(t, 7, gotCapped.RespawnAttempts, "the consecutive-respawn count survives a DB round-trip")
	require.WithinDuration(t, parkedUntil, gotCapped.RespawnParkedUntil, time.Second, "the park deadline survives a DB round-trip")

	gotFresh := bySystem["X1-QW1"]
	require.Equal(t, 0, gotFresh.RespawnAttempts, "a never-capped post reads zero attempts")
	require.True(t, gotFresh.RespawnParkedUntil.IsZero(), "a never-parked post reads an unset (NULL) deadline")
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
// and a new one opens — the cross-era guard for operational state.
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

// A multi-hull post round-trips through the DB with its budget AND its frozen
// partitions intact: the primary slot's partition, and every extra slot's
// hull/tour/relay/partition. This is the persistence half of restart survival —
// a reloaded post carries the exact partitions so the reconciler re-adopts each
// probe without re-touring.
func TestScoutPostRepo_MultiHullRoundTrip(t *testing.T) {
	repo, _, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	post := &domainScouting.ScoutPost{
		PlayerID:         playerID,
		SystemSymbol:     "X1-KA42",
		FreshnessTarget:  30 * time.Minute,
		Kind:             domainScouting.PostKindStanding,
		Hulls:            3,
		AssignedHull:     "SAT-0",
		TourContainerID:  "tour-0",
		PrimaryPartition: []string{"X1-KA42-A1", "X1-KA42-A2"},
		ExtraSlots: []domainScouting.ScoutPostSlot{
			{AssignedHull: "SAT-1", TourContainerID: "tour-1", Partition: []string{"X1-KA42-B1", "X1-KA42-B2"}},
			{RepositionContainerID: "relay-2", Partition: []string{"X1-KA42-C1"}},
		},
		CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Upsert(ctx, post))

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	got := posts[0]

	require.Equal(t, 3, got.Hulls)
	require.Equal(t, "SAT-0", got.AssignedHull)
	require.Equal(t, []string{"X1-KA42-A1", "X1-KA42-A2"}, got.PrimaryPartition)
	require.Len(t, got.ExtraSlots, 2)
	require.Equal(t, "SAT-1", got.ExtraSlots[0].AssignedHull)
	require.Equal(t, "tour-1", got.ExtraSlots[0].TourContainerID)
	require.Equal(t, []string{"X1-KA42-B1", "X1-KA42-B2"}, got.ExtraSlots[0].Partition)
	require.Equal(t, "relay-2", got.ExtraSlots[1].RepositionContainerID)
	require.Equal(t, []string{"X1-KA42-C1"}, got.ExtraSlots[1].Partition)

	// The domain aggregates reconstruct correctly from the persisted row.
	require.Equal(t, 3, got.HullBudget())
	require.Equal(t, []string{"SAT-0", "SAT-1"}, got.MannedHulls())
}

// A single-hull post persists byte-identically to the pre-enry layout:
// Hulls defaults to 1 and the partition/extra-slot columns stay NULL, so it reads
// back with no partitions and no extra slots.
func TestScoutPostRepo_SingleHullNoPartitionColumns(t *testing.T) {
	repo, db, playerID := newScoutPostTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &domainScouting.ScoutPost{
		PlayerID:        playerID,
		SystemSymbol:    "X1-HU21",
		FreshnessTarget: 90 * time.Minute,
		Kind:            domainScouting.PostKindStanding,
		AssignedHull:    "SAT-9",
		TourContainerID: "tour-9",
		CreatedAt:       time.Now(),
	}))

	posts, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	require.Equal(t, 1, posts[0].Hulls, "an unset budget defaults to single-hull")
	require.Nil(t, posts[0].PrimaryPartition)
	require.Nil(t, posts[0].ExtraSlots)

	// The partition columns are NULL on disk — not "[]" — so a single-hull row is
	// byte-identical to a pre-enry row.
	var model persistence.ScoutPostModel
	require.NoError(t, db.Where("system_symbol = ?", "X1-HU21").First(&model).Error)
	require.Nil(t, model.PrimaryPartition)
	require.Nil(t, model.ExtraSlots)
	require.Equal(t, 1, model.Hulls)
}
