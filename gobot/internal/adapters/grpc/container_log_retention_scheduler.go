package grpc

import (
	"context"
	"log"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// containerLogRetentionInterval is how often the sweep runs. Daily, for the same reason the
// containers sweep is daily: the shortest window it enforces is 48 hours, so the cadence only has
// to be small against that, and each sweep is a bounded walk rather than anything worth doing more
// often.
const containerLogRetentionInterval = 24 * time.Hour

// containerLogPruner is the storage side of the sweep, injected so tests drive it without a
// database.
type containerLogPruner interface {
	ProtectedContainerLogIDs(ctx context.Context) ([]int, error)
	PruneContainerLogs(ctx context.Context, opts persistence.ContainerLogPruneOptions) (persistence.ContainerLogPruneResult, error)
}

// ContainerLogRetentionScheduler periodically deletes container_logs rows past their level's
// retention window (sp-p1jo4). It mirrors ContainerRetentionScheduler's shape exactly — a timer
// loop whose body runs under supervise.Guard, halting promptly on ctx cancellation or Stop() —
// because it is the same job on the table next door, and the containers sweep is the version of
// this that has already been proven in production.
//
// It sweeps ONCE AT START before entering the loop, for the containers sweep's reason: a daemon
// that restarts more often than the interval would otherwise never prune at all, and a
// crash-looping daemon writes MORE log lines, not fewer.
//
// Every sweep is reported, including the ones that delete nothing — a pruner that logs only when
// it acts cannot be told apart from a pruner that is not running, and here the thing it deletes is
// literally the record someone will later go looking for.
type ContainerLogRetentionScheduler struct {
	pruner          containerLogPruner
	transientWindow time.Duration
	problemWindow   time.Duration
	batchSize       int
	maxBatches      int
	interval        time.Duration
	now             func() time.Time
	logf            func(format string, args ...interface{})
	record          func(class string, deleted int64)
	stopCh          chan struct{}
}

// NewContainerLogRetentionScheduler builds the sweep over the given repository using the
// operator's resolved config. It returns nil when the sweep is disabled, which the lifecycle's
// existing nil check reads as "do not launch"; with no config present the sweep is ARMED on its
// documented defaults (RULINGS #22).
func NewContainerLogRetentionScheduler(
	repo *persistence.GormContainerLogRepository,
	cfg config.ContainerLogRetentionConfig,
) *ContainerLogRetentionScheduler {
	if !cfg.Enabled() {
		return nil
	}
	return &ContainerLogRetentionScheduler{
		pruner:          repo,
		transientWindow: cfg.ResolvedWindow(),
		problemWindow:   cfg.ResolvedProblemWindow(),
		batchSize:       cfg.ResolvedBatchSize(),
		maxBatches:      cfg.ResolvedMaxBatches(),
		interval:        containerLogRetentionInterval,
		now:             time.Now,
		logf:            log.Printf,
		record:          metrics.RecordContainerLogRetentionDeleted,
		stopCh:          make(chan struct{}),
	}
}

// Run blocks, sweeping at start and then every interval, until ctx is canceled or Stop() is
// called. Returns nil in both cases — the supervise layer treats a nil return as a clean stop.
func (s *ContainerLogRetentionScheduler) Run(ctx context.Context) error {
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
func (s *ContainerLogRetentionScheduler) sweepGuarded(ctx context.Context) {
	supervise.Guard("container-log-retention", func() {
		s.sweep(ctx)
	})
}

// sweep deletes one window's worth of container logs and reports the result.
func (s *ContainerLogRetentionScheduler) sweep(ctx context.Context) {
	now := s.now()
	transientCutoff := now.Add(-s.transientWindow)
	problemCutoff := now.Add(-s.problemWindow)

	// FAIL CLOSED ON AN UNREADABLE PROTECTION SET. These ids are the newest log line of every
	// live container — the liveness watchdog's only progress signal. Without them the sweep
	// cannot tell which rows are load-bearing, and deleting anyway could leave a hung tour
	// invisible to the watchdog that exists to kill it. Retention is irreversible and one extra
	// day of rows costs nothing, so the sweep skips rather than proceeds blind.
	protected, err := s.pruner.ProtectedContainerLogIDs(ctx)
	if err != nil {
		s.logf("Container log retention sweep SKIPPED: could not read the live-container protection set, "+
			"and sweeping without it risks deleting the last log line a hung tour would be detected by: %v", err)
		return
	}

	result, err := s.pruner.PruneContainerLogs(ctx, persistence.ContainerLogPruneOptions{
		TransientCutoff: transientCutoff,
		ProblemCutoff:   problemCutoff,
		BatchSize:       s.batchSize,
		MaxBatches:      s.maxBatches,
		ProtectedIDs:    protected,
	})

	// The counts are reported even on error: the sweep deletes batch by batch and returns what
	// it managed before failing, so a partial sweep is still a real deletion that someone
	// chasing a missing log line needs accounted for.
	s.record("transient", result.Transient)
	s.record("problem", result.Problem)

	if err != nil {
		s.logf("Container log retention sweep FAILED after deleting %d row(s) in %d batch(es) "+
			"(transient=%d older than %s, problem=%d older than %s): %v",
			result.Total(), result.Batches, result.Transient,
			transientCutoff.UTC().Format(time.RFC3339), result.Problem,
			problemCutoff.UTC().Format(time.RFC3339), err)
		return
	}

	truncated := ""
	if result.ReachedMaxBatches {
		truncated = " — STOPPED ON THE BATCH BOUND with work remaining; the next sweep continues"
	}
	s.logf("Container log retention sweep ok: deleted %d row(s) in %d batch(es), largest statement %d row(s) "+
		"(transient=%d older than %s, problem=%d older than %s, %d protected live-container line(s) held)%s",
		result.Total(), result.Batches, result.LargestBatch,
		result.Transient, transientCutoff.UTC().Format(time.RFC3339),
		result.Problem, problemCutoff.UTC().Format(time.RFC3339),
		len(protected), truncated)
}

// Stop halts Run. The daemon itself stops the loop via runCtx cancellation, so this is primarily
// the explicit test seam — mirroring ContainerRetentionScheduler.Stop.
func (s *ContainerLogRetentionScheduler) Stop() {
	close(s.stopCh)
}
