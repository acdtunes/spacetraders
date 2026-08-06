package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// retentionRow seeds one container in a given status, stopped a given age ago.
func retentionRow(t *testing.T, db *gorm.DB, playerID int, id string, status container.ContainerStatus, age time.Duration) {
	t.Helper()
	stopped := time.Now().UTC().Add(-age)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID,
		ContainerType: "WORKER", CommandType: "contract_work",
		Status:    string(status),
		StartedAt: &stopped, StoppedAt: &stopped,
	}).Error)
}

func retentionDB(t *testing.T) (*gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, player.ID
}

func containerIDs(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	var rows []persistence.ContainerModel
	require.NoError(t, db.Find(&rows).Error)
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

// THE CONTRACT (sp-72gmi). Two edges decide whether this pruner is safe, and they are the two
// asserted first: a live container is never taken no matter how old it is, and a terminal
// container inside the window is never taken no matter what state it is in.
//
// Age alone must not be sufficient, and status alone must not be sufficient. Only both.
func TestPruneTerminalContainers_KeepsLiveRowsAtAnyAgeAndRecentRowsAtAnyStatus(t *testing.T) {
	db, playerID := retentionDB(t)
	beyond := persistence.ContainerRetentionWindow + 24*time.Hour
	inside := persistence.ContainerRetentionWindow - 24*time.Hour

	// OLD but LIVE — must all survive. RUNNING is work in progress; INTERRUPTED is the daemon's
	// own restart-recovery queue; PENDING and STOPPING are mid-lifecycle.
	retentionRow(t, db, playerID, "old-running", container.ContainerStatusRunning, beyond)
	retentionRow(t, db, playerID, "old-interrupted", container.ContainerStatusInterrupted, beyond)
	retentionRow(t, db, playerID, "old-pending", container.ContainerStatusPending, beyond)
	retentionRow(t, db, playerID, "old-stopping", container.ContainerStatusStopping, beyond)

	// TERMINAL but RECENT — must all survive. This is the forensics half: an incident that ran
	// last night is still inside the window and its rows are the evidence.
	retentionRow(t, db, playerID, "recent-failed", container.ContainerStatusFailed, inside)
	retentionRow(t, db, playerID, "recent-stopped", container.ContainerStatusStopped, inside)
	retentionRow(t, db, playerID, "recent-completed", container.ContainerStatusCompleted, inside)

	// TERMINAL and OLD — the only rows that may be taken.
	retentionRow(t, db, playerID, "old-failed", container.ContainerStatusFailed, beyond)
	retentionRow(t, db, playerID, "old-stopped", container.ContainerStatusStopped, beyond)
	retentionRow(t, db, playerID, "old-completed", container.ContainerStatusCompleted, beyond)

	repo := persistence.NewContainerRepository(db)
	deleted, err := repo.PruneTerminalContainers(context.Background(), time.Now().UTC().Add(-persistence.ContainerRetentionWindow))
	require.NoError(t, err)

	surviving := containerIDs(t, db)

	for _, id := range []string{"old-running", "old-interrupted", "old-pending", "old-stopping"} {
		require.True(t, surviving[id],
			"%s was pruned: a LIVE container must survive regardless of age — deleting one destroys work in "+
				"progress, and INTERRUPTED is the daemon's own recovery queue", id)
	}
	for _, id := range []string{"recent-failed", "recent-stopped", "recent-completed"} {
		require.True(t, surviving[id],
			"%s was pruned: a terminal container INSIDE the window must survive — those rows are the evidence "+
				"an incident is diagnosed from, which is the whole reason the window is 14 days", id)
	}
	for _, id := range []string{"old-failed", "old-stopped", "old-completed"} {
		require.False(t, surviving[id], "%s should have been pruned: terminal and past the window", id)
	}

	// Acceptance: the count is reported PER STATUS, so a sweep is auditable rather than silent.
	require.Equal(t, int64(1), deleted[container.ContainerStatusFailed])
	require.Equal(t, int64(1), deleted[container.ContainerStatusStopped])
	require.Equal(t, int64(1), deleted[container.ContainerStatusCompleted])
	require.Len(t, deleted, 3, "every terminal status must be reported, including the ones that deleted nothing")
}

// A row that cannot be DATED is never pruned. Refusing to delete what we cannot place in time is
// the safe direction for an irreversible operation — and a NULL stopped_at is exactly what a
// container killed mid-flight leaves behind, which is the kind most worth keeping.
func TestPruneTerminalContainers_NeverPrunesAnUndatableRow(t *testing.T) {
	db, playerID := retentionDB(t)

	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "undatable", PlayerID: playerID,
		ContainerType: "WORKER", CommandType: "contract_work",
		Status: string(container.ContainerStatusFailed),
		// No StartedAt, no StoppedAt.
	}).Error)

	repo := persistence.NewContainerRepository(db)
	deleted, err := repo.PruneTerminalContainers(context.Background(), time.Now().UTC())
	require.NoError(t, err)

	require.True(t, containerIDs(t, db)["undatable"],
		"a row with neither stopped_at nor started_at was pruned; it cannot be dated, so it cannot be shown to be old")
	require.Equal(t, int64(0), deleted[container.ContainerStatusFailed])
}

// A terminal row whose stop was never recorded still ages out, dated by started_at. Without the
// fallback these rows would accumulate forever — the exact unbounded growth the window exists to
// stop, reintroduced through a NULL.
func TestPruneTerminalContainers_FallsBackToStartedAtWhenStopWasNeverRecorded(t *testing.T) {
	db, playerID := retentionDB(t)

	started := time.Now().UTC().Add(-persistence.ContainerRetentionWindow - 24*time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "no-stop-recorded", PlayerID: playerID,
		ContainerType: "WORKER", CommandType: "contract_work",
		Status:    string(container.ContainerStatusFailed),
		StartedAt: &started,
	}).Error)

	repo := persistence.NewContainerRepository(db)
	deleted, err := repo.PruneTerminalContainers(context.Background(), time.Now().UTC().Add(-persistence.ContainerRetentionWindow))
	require.NoError(t, err)

	require.False(t, containerIDs(t, db)["no-stop-recorded"],
		"a terminal row with no stopped_at must still age out on started_at, or NULLs reintroduce unbounded growth")
	require.Equal(t, int64(1), deleted[container.ContainerStatusFailed])
}

// The window is a forensics budget and the number is load-bearing, so it is pinned. Anyone
// changing it should have to change this line and argue with the reasoning on the constant.
func TestContainerRetentionWindow_IsFourteenDays(t *testing.T) {
	require.Equal(t, 14*24*time.Hour, persistence.ContainerRetentionWindow,
		"the window is a forensics budget: it must stay long enough that an incident beginning on a Friday "+
			"is still diagnosable the following week")
}
