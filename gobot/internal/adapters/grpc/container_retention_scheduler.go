package grpc

import (
	"context"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// containerRetentionInterval is how often the sweep runs. Daily: the window it enforces is
// fourteen days, so the cadence only has to be small against that, and a sweep is a handful of
// indexed DELETEs rather than anything worth doing more often.
const containerRetentionInterval = 24 * time.Hour

// containerRetentionPruner is the storage side of the sweep, injected so tests drive it without
// a database.
type containerRetentionPruner interface {
	PruneTerminalContainers(ctx context.Context, olderThan time.Time) (map[container.ContainerStatus]int64, error)
}

// ContainerRetentionScheduler periodically deletes terminal container rows older than
// persistence.ContainerRetentionWindow (sp-72gmi). It mirrors ShipResyncScheduler's shape: a
// timer loop whose body runs under supervise.Guard, halting promptly on ctx cancellation or
// Stop().
//
// It sweeps ONCE AT START before entering the loop. A daemon that restarts more often than the
// interval would otherwise never prune at all — and a crash-looping daemon is exactly the
// condition that fills this table.
//
// Every sweep is reported, including the ones that delete nothing. A pruner that logs only when
// it acts cannot be told apart from a pruner that is not running, which matters here more than
// usual: the thing it deletes is the evidence someone will later go looking for, so "did
// retention take it?" has to be answerable from the record.
type ContainerRetentionScheduler struct {
	pruner   containerRetentionPruner
	window   time.Duration
	interval time.Duration
	now      func() time.Time
	logf     func(format string, args ...interface{})
	record   func(status string, deleted int64)
	stopCh   chan struct{}
}

// NewContainerRetentionScheduler builds the sweep over the given repository, using the
// production window and cadence.
func NewContainerRetentionScheduler(repo *persistence.ContainerRepositoryGORM) *ContainerRetentionScheduler {
	return &ContainerRetentionScheduler{
		pruner:   repo,
		window:   persistence.ContainerRetentionWindow,
		interval: containerRetentionInterval,
		now:      time.Now,
		logf:     log.Printf,
		record:   metrics.RecordContainerRetentionDeleted,
		stopCh:   make(chan struct{}),
	}
}

// Run blocks, sweeping at start and then every interval, until ctx is canceled or Stop() is
// called. Returns nil in both cases — the supervise layer treats a nil return as a clean stop.
func (s *ContainerRetentionScheduler) Run(ctx context.Context) error {
	s.sweepGuarded(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-ticker.C:
			s.sweepGuarded(ctx)
		}
	}
}

// sweepGuarded runs one sweep under panic isolation, so a failure in one pass cannot take the
// daemon down with it.
func (s *ContainerRetentionScheduler) sweepGuarded(ctx context.Context) {
	supervise.Guard("container-retention", func() {
		s.sweep(ctx)
	})
}

// sweep deletes one window's worth of terminal containers and reports the result.
func (s *ContainerRetentionScheduler) sweep(ctx context.Context) {
	cutoff := s.now().Add(-s.window)

	deleted, err := s.pruner.PruneTerminalContainers(ctx, cutoff)

	// The counts are reported even on error: PruneTerminalContainers deletes per status and
	// returns what it managed before failing, so a partial sweep is still a real deletion that
	// someone chasing a missing container needs accounted for.
	total := int64(0)
	for _, status := range sortedStatuses(deleted) {
		n := deleted[status]
		s.record(string(status), n)
		total += n
	}

	if err != nil {
		s.logf("Container retention sweep FAILED after deleting %d row(s) older than %s: %v",
			total, cutoff.UTC().Format(time.RFC3339), err)
		return
	}

	s.logf("Container retention sweep ok: deleted %d row(s) older than %s (%s)",
		total, cutoff.UTC().Format(time.RFC3339), describeDeletions(deleted))
}

// sortedStatuses gives the sweep a stable report order, so successive log lines are comparable
// and the test does not depend on map iteration order.
func sortedStatuses(deleted map[container.ContainerStatus]int64) []container.ContainerStatus {
	out := make([]container.ContainerStatus, 0, len(deleted))
	for status := range deleted {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// describeDeletions renders the per-status breakdown for the log line. Every status is named
// even at zero — "FAILED=0" says the sweep considered FAILED and found nothing, which is a
// different statement from FAILED being absent.
func describeDeletions(deleted map[container.ContainerStatus]int64) string {
	out := ""
	for _, status := range sortedStatuses(deleted) {
		if out != "" {
			out += " "
		}
		out += string(status) + "=" + strconv.FormatInt(deleted[status], 10)
	}
	if out == "" {
		return "no statuses swept"
	}
	return out
}

// Stop halts Run. The daemon itself stops the loop via runCtx cancellation, so this is
// primarily the explicit test seam — mirroring ShipResyncScheduler.Stop.
func (s *ContainerRetentionScheduler) Stop() {
	close(s.stopCh)
}
