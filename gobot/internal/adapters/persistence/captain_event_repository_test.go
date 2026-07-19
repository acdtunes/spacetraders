package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func setupCaptainEventRepo(t *testing.T) (*persistence.GormCaptainEventRepository, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewGormCaptainEventRepository(db), player.ID
}

func TestCaptainEventLifecycle(t *testing.T) {
	repo, playerID := setupCaptainEventRepo(t)
	ctx := context.Background()

	e := &captain.Event{Type: captain.EventWorkflowFailed, Ship: "SHIP-1",
		PlayerID: playerID, Payload: `{"error":"boom"}`}
	require.NoError(t, repo.Record(ctx, e))

	got, err := repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, captain.EventWorkflowFailed, got[0].Type)
	require.Equal(t, "SHIP-1", got[0].Ship)
	require.NotZero(t, got[0].ID)
	require.Nil(t, got[0].ProcessedAt)

	dup, err := repo.HasUnprocessed(ctx, playerID, captain.EventWorkflowFailed, "SHIP-1")
	require.NoError(t, err)
	require.True(t, dup)

	require.NoError(t, repo.MarkProcessed(ctx, []int64{got[0].ID}, time.Now()))
	got, err = repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Empty(t, got)

	dup, err = repo.HasUnprocessed(ctx, playerID, captain.EventWorkflowFailed, "SHIP-1")
	require.NoError(t, err)
	require.False(t, dup)
}

func TestFindUnprocessedOrdersOldestFirstAndScopesPlayer(t *testing.T) {
	repo, playerID := setupCaptainEventRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventShipIdle, Ship: "A", PlayerID: playerID}))
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventShipIdle, Ship: "B", PlayerID: playerID}))

	got, err := repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "A", got[0].Ship)

	other, err := repo.FindUnprocessed(ctx, playerID+999, 10)
	require.NoError(t, err)
	require.Empty(t, other)
}

// TestLatestByTypeReturnsNilWhenNoneExists proves the zero-migration baseline
// query signals "no prior event" as (nil, nil) rather than an error,
// so RecordDeployIfChanged can treat a fresh player/event-type pair as a
// first-boot case cleanly.
func TestLatestByTypeReturnsNilWhenNoneExists(t *testing.T) {
	repo, playerID := setupCaptainEventRepo(t)
	ctx := context.Background()

	got, err := repo.LatestByType(ctx, playerID, captain.EventDeployCompleted)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestLatestByTypeReturnsNewestAndScopesByPlayerAndType proves LatestByType
// picks the most recently created row (not first-inserted, tie-broken by ID
// since CreatedAt precision alone is not reliable across fast inserts),
// ignores other event types, and ignores other players' events of the same
// type — the three properties RecordDeployIfChanged's baseline comparison
// depends on.
func TestLatestByTypeReturnsNewestAndScopesByPlayerAndType(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	other := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&other).Error)
	repo := persistence.NewGormCaptainEventRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventDeployCompleted, PlayerID: player.ID, Payload: `{"commit":"aaa"}`}))
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventShipIdle, Ship: "X", PlayerID: player.ID}))
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventDeployCompleted, PlayerID: player.ID, Payload: `{"commit":"bbb"}`}))
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventDeployCompleted, PlayerID: other.ID, Payload: `{"commit":"zzz"}`}))

	got, err := repo.LatestByType(ctx, player.ID, captain.EventDeployCompleted)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, `{"commit":"bbb"}`, got.Payload, "must return the newest deploy.completed for this player, not the first, an unrelated type, or another player's")
}
