package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
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
