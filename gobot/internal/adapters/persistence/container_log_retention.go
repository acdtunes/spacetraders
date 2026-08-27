package persistence

import (
	"context"
	"fmt"
	"time"
)

// ContainerLogProblemLevels are the levels held for the LONG retention window (sp-p1jo4). BOTH
// SPELLINGS OF WARNING ARE PRESENT IN PRODUCTION DATA; listing one would demote the other. Anything
// unlisted is chatter, including levels that do not exist yet — the default must be the cheap side,
// or one new level name quietly restores unbounded growth.
var ContainerLogProblemLevels = []string{"ERROR", "WARN", "WARNING"}

// terminalContainerStatusStrings renders terminalContainerStatuses for SQL, so the protection
// query's complement cannot drift from the container pruner's list.
func terminalContainerStatusStrings() []string {
	out := make([]string, 0, len(terminalContainerStatuses))
	for _, s := range terminalContainerStatuses {
		out = append(out, string(s))
	}
	return out
}

// ContainerLogPruneOptions describes one retention sweep over container_logs.
type ContainerLogPruneOptions struct {
	// TransientCutoff is the oldest timestamp a non-problem line may keep, ProblemCutoff the
	// same edge for problem levels. Rows STRICTLY older go; a row exactly on its cutoff survives.
	TransientCutoff time.Time
	ProblemCutoff   time.Time

	// BatchSize bounds the rows a single DELETE may take; MaxBatches bounds the whole sweep.
	// ProtectedIDs (from ProtectedContainerLogIDs) are rows never deleted at any age.
	BatchSize  int
	MaxBatches int

	ProtectedIDs []int
}

// ContainerLogPruneResult is what one sweep did, in enough detail that a vanished log line is
// traceable to retention rather than a bug.
type ContainerLogPruneResult struct {
	// Rows deleted under the short and long windows, and the id windows deleted from.
	Transient int64
	Problem   int64
	Batches   int

	// LargestBatch is the most rows any SINGLE statement removed — the number proving the sweep
	// stayed off the hot path, and the only place one giant DELETE would show up.
	LargestBatch int64

	// ReachedMaxBatches records that the sweep stopped on its own bound with work left; a
	// permanent backlog otherwise looks like a table simply that size.
	ReachedMaxBatches bool
}

func (r ContainerLogPruneResult) Total() int64 { return r.Transient + r.Problem }

// ProtectedContainerLogIDs returns the id of the newest log line of every container NOT in a
// terminal state (sp-p1jo4).
//
// WHY RETENTION NEEDS THIS. LatestLogTimestamps derives per-container liveness from the newest log
// line, and a container ABSENT from that map is left alone — fail-closed on unknown progress. So
// deleting a live container's whole trail would make a genuinely hung tour stop looking hung:
// retention would disable the watchdog rather than the watchdog reporting a stall. TERMINAL
// containers get nothing — that would leave one immortal row per container that ever ran. The join
// drives from the small filtered containers side into idx_container_logs_container.
func (r *GormContainerLogRepository) ProtectedContainerLogIDs(ctx context.Context) ([]int, error) {
	var ids []int
	err := r.db.WithContext(ctx).
		Model(&ContainerLogModel{}).
		Select("MAX(container_logs.id)").
		Joins("JOIN containers ON containers.id = container_logs.container_id AND containers.player_id = container_logs.player_id").
		Where("containers.status NOT IN ?", terminalContainerStatusStrings()).
		Group("container_logs.container_id, container_logs.player_id").
		Order("MAX(container_logs.id)").
		Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("failed to resolve protected container log ids: %w", err)
	}
	return ids, nil
}

// PruneContainerLogs deletes rows past their level's retention window in BOUNDED BATCHES.
//
// WHY IT WALKS THE PRIMARY KEY RATHER THAN FILTERING ON TIMESTAMP. container_logs is indexed on
// (id) and (container_id, player_id) only, so a LIMIT-bounded `WHERE timestamp < cutoff` is a seq
// scan — and batching that shape is QUADRATIC, because every batch restarts at page zero and
// re-reads the dead tuples its predecessors left. A timestamp index was rejected: it would put a
// CREATE INDEX on a multi-million-row table into the daemon's BOOT path, where parallel
// maintenance is exactly what sp-5y0bp's shared-memory ceiling was killing.
//
// So the sweep keysets forward along the primary key instead. Log ids rise with time because the
// insert path only appends, so id order is a time order and the walk stops at the first row inside
// the window. Each pass deletes only within one id window: work is linear in rows removed and no
// statement can exceed BatchSize.
func (r *GormContainerLogRepository) PruneContainerLogs(
	ctx context.Context,
	opts ContainerLogPruneOptions,
) (ContainerLogPruneResult, error) {
	var result ContainerLogPruneResult

	if opts.BatchSize <= 0 || opts.MaxBatches <= 0 {
		return result, fmt.Errorf("container log prune requires positive BatchSize and MaxBatches, got %d/%d",
			opts.BatchSize, opts.MaxBatches)
	}

	// Stop at whichever cutoff is NEWER: past it no row of either class is deletable, and taking
	// the newer keeps the condition correct under any configuration written.
	stopAt := opts.TransientCutoff
	if opts.ProblemCutoff.After(stopAt) {
		stopAt = opts.ProblemCutoff
	}

	protected := make(map[int]bool, len(opts.ProtectedIDs))
	for _, id := range opts.ProtectedIDs {
		protected[id] = true
	}

	lastID := 0
	for result.Batches < opts.MaxBatches {
		// One id window: row 0's timestamp answers "anything left?", the last row's id bounds
		// the DELETE and seeds the next window.
		var window []ContainerLogModel
		if err := r.db.WithContext(ctx).
			Model(&ContainerLogModel{}).
			Select("id, timestamp").
			Where("id > ?", lastID).
			Order("id").
			Limit(opts.BatchSize).
			Find(&window).Error; err != nil {
			return result, fmt.Errorf("failed to read container log id window above %d: %w", lastID, err)
		}
		if len(window) == 0 {
			return result, nil
		}

		// Everything from here is inside some window: a steady-state exit costing one indexed
		// read, because "found nothing" must be cheap on a live table.
		if !window[0].Timestamp.Before(stopAt) {
			return result, nil
		}

		highID := window[len(window)-1].ID

		// Protected rows, listed rather than filtered in SQL: the set is tiny and keeping it out
		// of the predicate leaves the DELETE a plain indexed range.
		var hold []int
		for _, row := range window {
			if protected[row.ID] {
				hold = append(hold, row.ID)
			}
		}

		transient, err := r.deleteLogRange(ctx, lastID, highID, opts.TransientCutoff, hold, false)
		if err != nil {
			return result, err
		}
		problem, err := r.deleteLogRange(ctx, lastID, highID, opts.ProblemCutoff, hold, true)
		if err != nil {
			result.Transient += transient
			return result, err
		}

		result.Transient += transient
		result.Problem += problem
		if transient > result.LargestBatch {
			result.LargestBatch = transient
		}
		if problem > result.LargestBatch {
			result.LargestBatch = problem
		}
		if transient > 0 || problem > 0 {
			result.Batches++
		}

		lastID = highID
	}

	result.ReachedMaxBatches = true
	return result, nil
}

// deleteLogRange removes one class's rows from a single id window. The classes are separate
// statements because each carries its own cutoff and the per-class counts make a sweep auditable.
// The `id > lo AND id <= hi` bound keeps the statement small: hi comes from a window of at most
// BatchSize rows, so the DELETE cannot exceed it however far behind retention is.
func (r *GormContainerLogRepository) deleteLogRange(
	ctx context.Context,
	lowID, highID int,
	cutoff time.Time,
	hold []int,
	problemLevels bool,
) (int64, error) {
	query := r.db.WithContext(ctx).
		Where("id > ? AND id <= ?", lowID, highID).
		Where("timestamp < ?", cutoff)

	if problemLevels {
		query = query.Where("level IN ?", ContainerLogProblemLevels)
	} else {
		query = query.Where("level NOT IN ?", ContainerLogProblemLevels)
	}

	if len(hold) > 0 {
		query = query.Where("id NOT IN ?", hold)
	}

	result := query.Delete(&ContainerLogModel{})
	if result.Error != nil {
		class := "transient"
		if problemLevels {
			class = "problem"
		}
		return 0, fmt.Errorf("failed to prune %s container logs in id range (%d, %d]: %w",
			class, lowID, highID, result.Error)
	}
	return result.RowsAffected, nil
}
