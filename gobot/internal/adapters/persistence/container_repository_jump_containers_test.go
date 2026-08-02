package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// ListJumpContainersForShip backs jump_ship's post-claim reap, which DELETES
// what this returns. Every row it hands back is a row that is about to be destroyed, so
// the query's scoping is the whole safety property — it is tested here against the real
// schema rather than only through the handler's stub.
func TestListJumpContainersForShip_MatchesOnConfigHullNotIDPrefix(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewContainerRepository(db)

	player := persistence.PlayerModel{AgentSymbol: "JUMP-REAP-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	other := persistence.PlayerModel{AgentSymbol: "JUMP-REAP-OTHER", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&other).Error)

	now := time.Now()
	mk := func(id string, playerID int, containerType, config, status string) {
		require.NoError(t, db.Create(&persistence.ContainerModel{
			ID: id, PlayerID: playerID,
			ContainerType: containerType, CommandType: "jump_ship",
			Status: status, Config: config, StartedAt: &now,
		}).Error)
	}

	// The hull under test: a legacy deterministic row and a per-attempt row, and a
	// STOPPED one - status is deliberately unscoped, because a jump row's status never
	// advances and a claim-break can terminalize a leaked row without removing it.
	mk("ship-jump-TORWIND-2", player.ID, "JUMP", `{"ship_symbol":"TORWIND-2","destination":"X1-AA"}`, "PENDING")
	mk("ship-jump-TORWIND-2-1753800000000000000", player.ID, "JUMP", `{"ship_symbol":"TORWIND-2"}`, "STOPPED")

	// THE PREFIX HAZARD. "ship-jump-TORWIND-2" is a string prefix of
	// "ship-jump-TORWIND-23-...", so an ID-prefix match would return this neighbour's
	// row - and the reap would delete another hull's live claim record.
	mk("ship-jump-TORWIND-23-1753800000000000001", player.ID, "JUMP", `{"ship_symbol":"TORWIND-23"}`, "PENDING")

	// Not a JUMP container, though its config names the same hull.
	mk("tour-run-TORWIND-2-abc", player.ID, "TOUR", `{"ship_symbol":"TORWIND-2"}`, "RUNNING")

	// A JUMP row for the same hull symbol under a DIFFERENT player.
	mk("ship-jump-TORWIND-2-other", other.ID, "JUMP", `{"ship_symbol":"TORWIND-2"}`, "PENDING")

	// An unparseable config names no hull and must be skipped, not error the whole read:
	// one corrupt row must never make a hull unreapable.
	mk("ship-jump-corrupt", player.ID, "JUMP", `{not json`, "PENDING")

	got, err := repo.ListJumpContainersForShip(context.Background(), "TORWIND-2", player.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"ship-jump-TORWIND-2",
		"ship-jump-TORWIND-2-1753800000000000000",
	}, got, "the reap must see this hull's JUMP rows in any status, and nothing else")

	// Stated as its own assertion because it is the fleet-damaging case: the neighbour
	// must be absent for the RIGHT reason, not merely absent from a short list.
	require.NotContains(t, got, "ship-jump-TORWIND-23-1753800000000000001",
		"an ID-prefix match would reap TORWIND-23's claim record during TORWIND-2's jump")

	// The neighbour can still find its own row - the scoping is a filter, not a blind spot.
	neighbour, err := repo.ListJumpContainersForShip(context.Background(), "TORWIND-23", player.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"ship-jump-TORWIND-23-1753800000000000001"}, neighbour)

	// A hull with nothing stranded reads empty, not an error.
	none, err := repo.ListJumpContainersForShip(context.Background(), "TORWIND-999", player.ID)
	require.NoError(t, err)
	require.Empty(t, none)
}
