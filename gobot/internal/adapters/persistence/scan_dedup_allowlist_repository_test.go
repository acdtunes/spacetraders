package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// TestScanDedupAllowlistGORM_AddIsArmedRemove_RoundTrip is the adapter's real-I/O
// proof (Mandate 4): a ship added is armed, a removed ship is not, and neither
// player nor ship is confused with another — all against a real database
// (SQLite via AutoMigrate, the same engine class production Postgres uses),
// never a mock.
func TestScanDedupAllowlistGORM_AddIsArmedRemove_RoundTrip(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewScanDedupAllowlistGORM(db)
	ctx := context.Background()

	armed, err := repo.IsArmed(ctx, 1, "TREAT-1")
	require.NoError(t, err)
	require.False(t, armed, "a ship never added must read as disarmed — the default state")

	changed, err := repo.Add(ctx, 1, "TREAT-1")
	require.NoError(t, err)
	require.True(t, changed)

	armed, err = repo.IsArmed(ctx, 1, "TREAT-1")
	require.NoError(t, err)
	require.True(t, armed)

	// Scoped by player: the same ship symbol for a DIFFERENT player is unarmed.
	armed, err = repo.IsArmed(ctx, 2, "TREAT-1")
	require.NoError(t, err)
	require.False(t, armed, "the allowlist is scoped per player_id")

	// A sibling ship for the SAME player is untouched.
	armed, err = repo.IsArmed(ctx, 1, "OTHER-1")
	require.NoError(t, err)
	require.False(t, armed)

	changed, err = repo.Remove(ctx, 1, "TREAT-1")
	require.NoError(t, err)
	require.True(t, changed)

	armed, err = repo.IsArmed(ctx, 1, "TREAT-1")
	require.NoError(t, err)
	require.False(t, armed, "a removed ship must read as disarmed again")
}

// TestScanDedupAllowlistGORM_AddRemove_IdempotentNoOps proves re-adding an
// already-armed ship and removing an absent one are honest no-ops (changed =
// false), never errors — the CLI relies on this to report "already armed" /
// "not armed" distinctly from a fresh mutation.
func TestScanDedupAllowlistGORM_AddRemove_IdempotentNoOps(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewScanDedupAllowlistGORM(db)
	ctx := context.Background()

	changed, err := repo.Remove(ctx, 1, "NEVER-ARMED")
	require.NoError(t, err)
	require.False(t, changed, "removing a ship that was never armed is a no-op")

	changed, err = repo.Add(ctx, 1, "TREAT-2")
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = repo.Add(ctx, 1, "TREAT-2")
	require.NoError(t, err)
	require.False(t, changed, "re-adding an already-armed ship is a no-op")
}

// TestScanDedupAllowlistGORM_List_SortedAndScoped proves List returns exactly
// the armed set for one player, sorted, unaffected by another player's rows.
func TestScanDedupAllowlistGORM_List_SortedAndScoped(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewScanDedupAllowlistGORM(db)
	ctx := context.Background()

	for _, ship := range []string{"ZULU-1", "ALPHA-1", "MIKE-1"} {
		_, err := repo.Add(ctx, 1, ship)
		require.NoError(t, err)
	}
	_, err = repo.Add(ctx, 2, "OTHER-PLAYER-SHIP")
	require.NoError(t, err)

	ships, err := repo.List(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"ALPHA-1", "MIKE-1", "ZULU-1"}, ships)

	ships, err = repo.List(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"OTHER-PLAYER-SHIP"}, ships)
}
