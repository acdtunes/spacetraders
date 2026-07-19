package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// FindActiveCoordinatorByType orders by heartbeat_at DESC so the freshest
// coordinator wins among multiple active rows — GORM's First() otherwise falls
// back to primary-key order, not recency.
func TestFindActiveCoordinatorByType_ReturnsLatestHeartbeatWhenMultipleActive(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "COORD-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	const coordType = "CONTRACT_FLEET_COORDINATOR"
	now := time.Now()
	older := now.Add(-1 * time.Hour)

	// The stale coordinator has the smaller id AND the older heartbeat, so an
	// unordered First() (primary-key order → id ASC) returns THIS one — the bug.
	staleHeartbeat := older
	stale := persistence.ContainerModel{
		ID: "coord-aaa-stale", PlayerID: player.ID,
		ContainerType: coordType, CommandType: "contract_fleet_coordinator",
		Status: "RUNNING", HeartbeatAt: &staleHeartbeat, StartedAt: &older,
	}
	require.NoError(t, db.Create(&stale).Error)

	freshHeartbeat := now
	fresh := persistence.ContainerModel{
		ID: "coord-zzz-fresh", PlayerID: player.ID,
		ContainerType: coordType, CommandType: "contract_fleet_coordinator",
		Status: "RUNNING", HeartbeatAt: &freshHeartbeat, StartedAt: &now,
	}
	require.NoError(t, db.Create(&fresh).Error)

	repo := persistence.NewContainerRepository(db)
	model, err := repo.FindActiveCoordinatorByType(context.Background(), coordType, player.ID)
	require.NoError(t, err)
	require.NotNil(t, model)
	require.Equal(t, "coord-zzz-fresh", model.ID,
		"with multiple active coordinators the latest-heartbeat row must win (deterministic tie-break)")
}
