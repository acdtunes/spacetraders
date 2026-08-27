package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// logRetentionDB seeds a player and returns the repository under test.
func logRetentionDB(t *testing.T) (*gorm.DB, *persistence.GormContainerLogRepository, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, persistence.NewGormContainerLogRepository(db, nil), player.ID
}

// seedContainer creates the parent row container_logs' foreign key requires.
func seedContainer(t *testing.T, db *gorm.DB, playerID int, id string, status container.ContainerStatus) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID,
		ContainerType: "WORKER", CommandType: "tour_run",
		Status:    string(status),
		StartedAt: &now,
	}).Error)
}

// seedLog writes one log line at a given age. Rows are created oldest-first so the autoincrement
// id rises with the timestamp, exactly as the production insert path produces them.
func seedLog(t *testing.T, db *gorm.DB, playerID int, containerID, level string, age time.Duration) int {
	t.Helper()
	row := &persistence.ContainerLogModel{
		ContainerID: containerID,
		PlayerID:    playerID,
		Timestamp:   time.Now().UTC().Add(-age),
		Level:       level,
		Message:     "line",
	}
	require.NoError(t, db.Create(row).Error)
	return row.ID
}

func remainingLogIDs(t *testing.T, db *gorm.DB) map[int]bool {
	t.Helper()
	var rows []persistence.ContainerLogModel
	require.NoError(t, db.Order("id").Find(&rows).Error)
	out := make(map[int]bool, len(rows))
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

func logCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.ContainerLogModel{}).Count(&n).Error)
	return n
}

// pruneOpts builds options with the production shape: a short window for chatter, a long one for
// problems, and a batch bound.
func pruneOpts(transient, problem time.Duration, batch int) persistence.ContainerLogPruneOptions {
	now := time.Now().UTC()
	return persistence.ContainerLogPruneOptions{
		TransientCutoff: now.Add(-transient),
		ProblemCutoff:   now.Add(-problem),
		BatchSize:       batch,
		MaxBatches:      1000,
	}
}

// THE BATCHING CONTRACT (sp-p1jo4). This is the property the whole design exists for: the table
// this sweeps is the LIVE trading database the tour planner reads, and a single unbounded
// `DELETE ... WHERE timestamp < cutoff` over the 2,050,646 rows the manual purge removed would
// hold row locks and spike I/O across the hot path for the duration. The purge that was done by
// hand used 250k-row batches with a pause between them; the automated sweep must batch too, or it
// reintroduces the outage it is meant to prevent.
//
// The assertion is on BOTH halves: the sweep took ceil(N/batch) passes, AND no single pass
// deleted more than batch rows. A sweep that reported many batches while issuing one giant
// statement would pass the first half alone.
func TestPruneContainerLogs_DeletesInBoundedBatchesNotOneUnboundedStatement(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-old", container.ContainerStatusStopped)

	const deletable = 25
	const batch = 10
	for i := 0; i < deletable; i++ {
		seedLog(t, db, playerID, "ct-old", "INFO", 72*time.Hour)
	}

	res, err := repo.PruneContainerLogs(context.Background(), pruneOpts(48*time.Hour, 14*24*time.Hour, batch))
	require.NoError(t, err)

	require.Equal(t, int64(deletable), res.Total(), "every row past the window should be gone")
	require.Equal(t, 3, res.Batches,
		"25 rows at a batch size of 10 must take ceil(25/10)=3 passes; one pass means one unbounded DELETE")
	require.LessOrEqual(t, res.LargestBatch, int64(batch),
		"a single statement deleted %d rows with a batch size of %d — the bound is what keeps the "+
			"sweep off the tour planner's back", res.LargestBatch, batch)
	require.Equal(t, int64(0), logCount(t, db))
}

// THE WINDOW BOUNDARY. Retention is irreversible, so the edge is inclusive-keep: a row whose
// timestamp is EXACTLY the cutoff is inside the window and survives. Only strictly-older rows go.
func TestPruneContainerLogs_KeepsARowSittingExactlyOnTheCutoff(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-edge", container.ContainerStatusStopped)

	// Seeded oldest-first, so the ids rise with the timestamps exactly as the append-only
	// production insert path produces them — the ordering the id walk reads as a time order.
	cutoff := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	justOlder := &persistence.ContainerLogModel{
		ContainerID: "ct-edge", PlayerID: playerID,
		Timestamp: cutoff.Add(-time.Second), Level: "INFO", Message: "one second past",
	}
	require.NoError(t, db.Create(justOlder).Error)
	onTheEdge := &persistence.ContainerLogModel{
		ContainerID: "ct-edge", PlayerID: playerID,
		Timestamp: cutoff, Level: "INFO", Message: "exactly at the cutoff",
	}
	require.NoError(t, db.Create(onTheEdge).Error)
	// A newer row so neither of the two above is its container's newest (see the protection test).
	seedLog(t, db, playerID, "ct-edge", "INFO", time.Minute)

	res, err := repo.PruneContainerLogs(context.Background(), persistence.ContainerLogPruneOptions{
		TransientCutoff: cutoff,
		ProblemCutoff:   cutoff.Add(-12 * 24 * time.Hour),
		BatchSize:       10,
		MaxBatches:      100,
	})
	require.NoError(t, err)

	surviving := remainingLogIDs(t, db)
	require.True(t, surviving[onTheEdge.ID],
		"a row AT the cutoff was deleted; the window is inclusive at its edge, and an off-by-one here "+
			"silently shortens every window the operator configures")
	require.False(t, surviving[justOlder.ID], "a row strictly older than the cutoff must go")
	require.Equal(t, int64(1), res.Total())
}

// A sweep with nothing to delete must be a genuine no-op: no DELETE issued at all. This is the
// steady-state shape once the backlog is gone, and it runs against the live trading DB — "found
// nothing" has to cost nothing.
func TestPruneContainerLogs_ASweepWithNothingToDeleteIssuesNoDeleteAtAll(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-fresh", container.ContainerStatusRunning)
	for i := 0; i < 5; i++ {
		seedLog(t, db, playerID, "ct-fresh", "INFO", time.Duration(i)*time.Minute)
	}

	res, err := repo.PruneContainerLogs(context.Background(), pruneOpts(48*time.Hour, 14*24*time.Hour, 10))
	require.NoError(t, err)

	require.Equal(t, int64(0), res.Total())
	require.Equal(t, 0, res.Batches,
		"a sweep that found nothing deletable must issue no DELETE; it walks the id index, sees the "+
			"first row is inside the window, and stops")
	require.Equal(t, int64(5), logCount(t, db))
}

// LEVEL-AWARE RETENTION (sp-p1jo4). Measured on the live table: INFO 85.3% + DEBUG 13.2% = 98.5%
// of rows, while ERROR + WARN + WARNING together are 1.5%. Keeping the problem levels for the full
// forensics budget therefore costs ~1.5% of the volume — which is what makes a 48h window on the
// chatter affordable at all. Both spellings of the warning level are present in production data
// and both must be honoured.
func TestPruneContainerLogs_KeepsProblemLevelsPastTheChatterWindow(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-mixed", container.ContainerStatusStopped)

	kept := map[string]int{}
	for _, level := range []string{"ERROR", "WARN", "WARNING"} {
		kept[level] = seedLog(t, db, playerID, "ct-mixed", level, 72*time.Hour)
	}
	swept := map[string]int{}
	for _, level := range []string{"INFO", "DEBUG", "TRACE", ""} {
		swept[level] = seedLog(t, db, playerID, "ct-mixed", level, 72*time.Hour)
	}
	// A problem line older than the LONG window is not immortal either.
	ancientError := seedLog(t, db, playerID, "ct-mixed", "ERROR", 15*24*time.Hour)
	seedLog(t, db, playerID, "ct-mixed", "INFO", time.Minute)

	res, err := repo.PruneContainerLogs(context.Background(), pruneOpts(48*time.Hour, 14*24*time.Hour, 100))
	require.NoError(t, err)

	surviving := remainingLogIDs(t, db)
	for level, id := range kept {
		require.True(t, surviving[id],
			"a %s line 72h old was swept: problem levels are 1.5%% of the volume and carry the incident "+
				"spine the captain reads with `container logs --level ERROR`", level)
	}
	for level, id := range swept {
		require.False(t, surviving[id],
			"a %q line 72h old survived the 48h chatter window; an unrecognised level must be treated as "+
				"chatter, or one new level name quietly restores unbounded growth", level)
	}
	require.False(t, surviving[ancientError], "a problem line past the LONG window must still age out")

	require.Equal(t, int64(4), res.Transient, "the four chatter lines")
	require.Equal(t, int64(1), res.Problem, "the one ancient ERROR")
}

// THE LIVENESS WATCHDOG MUST NOT BE BLINDED (sp-m3122 / sp-p1jo4). LatestLogTimestamps derives
// per-container liveness from the NEWEST container-log line, and a container absent from that map
// is left alone — fail-closed on unknown progress. If retention could delete a live container's
// entire trail, a genuinely hung tour would stop being visible as hung and would never be killed:
// retention would have disabled the watchdog rather than the watchdog reporting a stall.
//
// The newest line of every non-terminal container is therefore protected outright, at any age.
// It costs one row per live container (tens) and it makes that failure mode unreachable instead
// of merely unlikely.
func TestPruneContainerLogs_NeverDeletesAProtectedRow(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-silent", container.ContainerStatusRunning)

	older := seedLog(t, db, playerID, "ct-silent", "INFO", 90*time.Hour)
	newest := seedLog(t, db, playerID, "ct-silent", "INFO", 72*time.Hour)

	protected, err := repo.ProtectedContainerLogIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{newest}, protected,
		"the newest line of a RUNNING container is what the watchdog reads; it is the row that must be held")

	opts := pruneOpts(48*time.Hour, 14*24*time.Hour, 10)
	opts.ProtectedIDs = protected
	res, err := repo.PruneContainerLogs(context.Background(), opts)
	require.NoError(t, err)

	surviving := remainingLogIDs(t, db)
	require.True(t, surviving[newest],
		"retention deleted a RUNNING container's last log line — the watchdog now sees no progress signal "+
			"at all and will leave a hung tour running forever")
	require.False(t, surviving[older], "the rest of a live container's stale trail is still swept")
	require.Equal(t, int64(1), res.Total())
}

// A terminal container gets no protection: its logs are history, and exempting them would leave
// one immortal row per container that ever ran — slow unbounded growth wearing a safety label.
func TestProtectedContainerLogIDs_CoversOnlyNonTerminalContainers(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)

	live := []container.ContainerStatus{
		container.ContainerStatusRunning,
		container.ContainerStatusPending,
		container.ContainerStatusStopping,
		container.ContainerStatusInterrupted,
	}
	wantProtected := map[int]bool{}
	for i, status := range live {
		id := fmt.Sprintf("ct-live-%d", i)
		seedContainer(t, db, playerID, id, status)
		seedLog(t, db, playerID, id, "INFO", 10*time.Hour)
		wantProtected[seedLog(t, db, playerID, id, "INFO", 9*time.Hour)] = true
	}
	for i, status := range []container.ContainerStatus{
		container.ContainerStatusCompleted,
		container.ContainerStatusFailed,
		container.ContainerStatusStopped,
	} {
		id := fmt.Sprintf("ct-dead-%d", i)
		seedContainer(t, db, playerID, id, status)
		seedLog(t, db, playerID, id, "INFO", 9*time.Hour)
	}

	protected, err := repo.ProtectedContainerLogIDs(context.Background())
	require.NoError(t, err)

	require.Len(t, protected, len(live), "one protected row per non-terminal container, and no more")
	for _, id := range protected {
		require.True(t, wantProtected[id], "id %d is not the newest line of a live container", id)
	}
}

// MaxBatches bounds one sweep's total work so a huge backlog cannot turn a single pass into an
// hours-long I/O event. The leftover is not lost — the next daily sweep picks it up.
func TestPruneContainerLogs_StopsAtMaxBatchesAndSaysSo(t *testing.T) {
	db, repo, playerID := logRetentionDB(t)
	seedContainer(t, db, playerID, "ct-backlog", container.ContainerStatusStopped)
	for i := 0; i < 20; i++ {
		seedLog(t, db, playerID, "ct-backlog", "INFO", 72*time.Hour)
	}

	opts := pruneOpts(48*time.Hour, 14*24*time.Hour, 5)
	opts.MaxBatches = 2
	res, err := repo.PruneContainerLogs(context.Background(), opts)
	require.NoError(t, err)

	require.Equal(t, 2, res.Batches)
	require.Equal(t, int64(10), res.Total())
	require.True(t, res.ReachedMaxBatches,
		"a sweep cut short by its batch bound must report it, or a permanent backlog looks like a completed sweep")
	require.Equal(t, int64(10), logCount(t, db), "the remainder waits for the next sweep")
}
