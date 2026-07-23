package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func setupContainerLogRepo(t *testing.T) (*persistence.GormContainerLogRepository, int, string) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	container := persistence.ContainerModel{ID: "test-container-1", PlayerID: player.ID, Status: "RUNNING"}
	require.NoError(t, db.Create(&container).Error)

	return persistence.NewGormContainerLogRepository(db, nil), player.ID, container.ID
}

// TestGetLogsReturnsNewestNInDescendingOrder locks down the contract the CLI's
// `container logs --tail` relies on: GetLogs(limit) must fetch the N most
// recent entries (ORDER BY timestamp DESC LIMIT N), not the N oldest.
func TestGetLogsReturnsNewestNInDescendingOrder(t *testing.T) {
	repo, playerID, containerID := setupContainerLogRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Log(ctx, containerID, playerID,
			fmt.Sprintf("msg-%d", i), "INFO", nil))
	}

	logs, err := repo.GetLogs(ctx, containerID, playerID, 3, nil, nil)
	require.NoError(t, err)
	require.Len(t, logs, 3)

	// The 3 most recently written messages (msg-4, msg-3, msg-2) must come
	// back, newest first — NOT the oldest 3 (msg-0, msg-1, msg-2).
	require.Equal(t, "msg-4", logs[0].Message)
	require.Equal(t, "msg-3", logs[1].Message)
	require.Equal(t, "msg-2", logs[2].Message)
}

// TestLatestLogTimestampsReturnsNewestPerContainer locks down the sp-m3122 liveness signal:
// for each container it returns the timestamp of its NEWEST log line (MAX, not MIN), keyed per
// container, and a container with no logs is ABSENT from the map (so the watchdog leaves an
// unreadable tour alone). Uses a MockClock so the "newest" timestamps are exact.
func TestLatestLogTimestampsReturnsNewestPerContainer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	for _, id := range []string{"cA", "cB", "cC"} {
		require.NoError(t, db.Create(&persistence.ContainerModel{ID: id, PlayerID: player.ID, Status: "RUNNING"}).Error)
	}

	base := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: base}
	repo := persistence.NewGormContainerLogRepository(db, clock)
	ctx := context.Background()

	// container cA: three distinct progress lines; the NEWEST is at base+10m.
	clock.CurrentTime = base
	require.NoError(t, repo.Log(ctx, "cA", player.ID, "planned", "INFO", nil))
	clock.CurrentTime = base.Add(5 * time.Minute)
	require.NoError(t, repo.Log(ctx, "cA", player.ID, "navigated", "INFO", nil))
	aLatest := base.Add(10 * time.Minute)
	clock.CurrentTime = aLatest
	require.NoError(t, repo.Log(ctx, "cA", player.ID, "sold", "INFO", nil))

	// container cB: a single line at base+2m.
	bLatest := base.Add(2 * time.Minute)
	clock.CurrentTime = bLatest
	require.NoError(t, repo.Log(ctx, "cB", player.ID, "planned", "INFO", nil))

	// container cC: no logs at all.

	got, err := repo.LatestLogTimestamps(ctx, player.ID, []string{"cA", "cB", "cC"})
	require.NoError(t, err)

	require.Len(t, got, 2, "cC has no logs and must be absent from the map")
	require.WithinDuration(t, aLatest, got["cA"], time.Second, "cA's latest must be its NEWEST line (base+10m), not an earlier one")
	require.WithinDuration(t, bLatest, got["cB"], time.Second)
	_, hasC := got["cC"]
	require.False(t, hasC, "a container with no logs must not appear (unknown progress)")
}
