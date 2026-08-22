package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// FRESHNESS, NOT EXPLICIT CLEANUP: the repository has no delete path, so there is none to test here.

func setupPendingScalingRepo(t *testing.T) (*persistence.PendingScalingReservationRepository, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return persistence.NewPendingScalingReservationRepository(db), db
}

func TestPendingScalingReservation_NoRowPublished_ReadsZero(t *testing.T) {
	repo, _ := setupPendingScalingRepo(t)
	amount, err := repo.PendingReservation(context.Background(), 11)
	require.NoError(t, err)
	require.Equal(t, int64(0), amount, "no row published yet must read as nothing pending")
}

func TestPendingScalingReservation_PublishThenRead_ReturnsTarget(t *testing.T) {
	repo, _ := setupPendingScalingRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Publish(ctx, 11, 508_193))

	amount, err := repo.PendingReservation(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, int64(508_193), amount)
}

// A re-publish must UPSERT, never insert a second row, and the read must reflect the latest target.
func TestPendingScalingReservation_Republish_OverwritesTargetAndStaysOneRow(t *testing.T) {
	repo, db := setupPendingScalingRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Publish(ctx, 11, 400_000))
	require.NoError(t, repo.Publish(ctx, 11, 508_193))

	amount, err := repo.PendingReservation(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, int64(508_193), amount, "the read must reflect the latest published target")

	var count int64
	require.NoError(t, db.Model(&persistence.PendingScalingReservationModel{}).Where("player_id = ?", 11).Count(&count).Error)
	require.Equal(t, int64(1), count, "a republish must UPSERT, never insert a second row")
}

func TestPendingScalingReservation_ScopedPerPlayer(t *testing.T) {
	repo, _ := setupPendingScalingRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Publish(ctx, 11, 508_193))

	amount, err := repo.PendingReservation(ctx, 22)
	require.NoError(t, err)
	require.Equal(t, int64(0), amount, "player 22 must not see player 11's reservation")
}

// Inserts the row directly (bypassing Publish) with an updated_at outside the default window, so
// the test does not have to sleep out the real 90s window.
func TestPendingScalingReservation_StaleRow_ReadsAsNothingPending(t *testing.T) {
	repo, db := setupPendingScalingRepo(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.PendingScalingReservationModel{
		PlayerID: 11, TargetAmount: 508_193,
		UpdatedAt: time.Now().Add(-100 * time.Second), // older than the 90s default window
	}).Error)

	amount, err := repo.PendingReservation(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, int64(0), amount, "a row older than the staleness window must read as nothing pending")
}

func TestPendingScalingReservation_FreshRow_ReadsTheTarget(t *testing.T) {
	repo, db := setupPendingScalingRepo(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.PendingScalingReservationModel{
		PlayerID: 11, TargetAmount: 508_193,
		UpdatedAt: time.Now().Add(-10 * time.Second), // well inside the 90s default window
	}).Error)

	amount, err := repo.PendingReservation(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, int64(508_193), amount, "a row inside the staleness window must read its target")
}
