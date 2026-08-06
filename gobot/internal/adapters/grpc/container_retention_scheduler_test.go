package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// fakePruner records the cutoff it was asked for and returns a scripted result.
type fakePruner struct {
	mu      sync.Mutex
	cutoffs []time.Time
	deleted map[container.ContainerStatus]int64
	err     error
}

func (p *fakePruner) PruneTerminalContainers(_ context.Context, olderThan time.Time) (map[container.ContainerStatus]int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cutoffs = append(p.cutoffs, olderThan)
	return p.deleted, p.err
}

func (p *fakePruner) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cutoffs)
}

type recordedMetric struct {
	status  string
	deleted int64
}

// newTestScheduler builds a scheduler with every seam injected: fake clock, fake pruner,
// captured log sink, captured metric sink.
func newTestScheduler(p *fakePruner, now time.Time, window time.Duration) (*ContainerRetentionScheduler, *[]string, *[]recordedMetric) {
	var logs []string
	var recorded []recordedMetric
	s := &ContainerRetentionScheduler{
		pruner:   p,
		window:   window,
		interval: time.Hour,
		now:      func() time.Time { return now },
		logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		record: func(status string, deleted int64) {
			recorded = append(recorded, recordedMetric{status: status, deleted: deleted})
		},
		stopCh: make(chan struct{}),
	}
	return s, &logs, &recorded
}

// OBSERVABILITY (sp-72gmi). A silent pruner is indistinguishable from a broken one, and what it
// deletes is the evidence someone will later go looking for — so "did retention take it, and in
// what state?" must be answerable from the record alone.
func TestRetentionSweep_ReportsWhatItDeletedByStatus(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	p := &fakePruner{deleted: map[container.ContainerStatus]int64{
		container.ContainerStatusFailed:    34_279,
		container.ContainerStatusStopped:   30_016,
		container.ContainerStatusCompleted: 18_876,
	}}
	s, logs, recorded := newTestScheduler(p, now, 14*24*time.Hour)

	s.sweep(context.Background())

	if len(*logs) != 1 {
		t.Fatalf("expected exactly one sweep log line, got %d: %v", len(*logs), *logs)
	}
	line := (*logs)[0]
	for _, want := range []string{"83171", "FAILED=34279", "STOPPED=30016", "COMPLETED=18876"} {
		if !strings.Contains(line, want) {
			t.Errorf("sweep log is missing %q — the per-status breakdown is what makes a vanished container "+
				"traceable to retention rather than to a bug.\ngot: %s", want, line)
		}
	}

	if len(*recorded) != 3 {
		t.Fatalf("expected one metric sample per status, got %d: %+v", len(*recorded), *recorded)
	}
	byStatus := map[string]int64{}
	for _, r := range *recorded {
		byStatus[r.status] = r.deleted
	}
	if byStatus["FAILED"] != 34_279 || byStatus["STOPPED"] != 30_016 || byStatus["COMPLETED"] != 18_876 {
		t.Errorf("metric samples do not match the deletions: %+v", byStatus)
	}
}

// A sweep that deletes nothing must still say so. "FAILED=0" is the statement that the sweep RAN
// and found nothing; silence is the statement that it may not have run at all, and those must not
// look the same.
func TestRetentionSweep_AnEmptySweepIsStillReported(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	p := &fakePruner{deleted: map[container.ContainerStatus]int64{
		container.ContainerStatusFailed:    0,
		container.ContainerStatusStopped:   0,
		container.ContainerStatusCompleted: 0,
	}}
	s, logs, recorded := newTestScheduler(p, now, 14*24*time.Hour)

	s.sweep(context.Background())

	if len(*logs) != 1 || !strings.Contains((*logs)[0], "FAILED=0") {
		t.Fatalf("an empty sweep must still report itself, naming each status at zero; got: %v", *logs)
	}
	if len(*recorded) != 3 {
		t.Fatalf("an empty sweep must still emit a sample per status, so the metric proves liveness; got %+v", *recorded)
	}
}

// A partial sweep is still a real deletion. If the pruner fails after removing some rows, those
// rows are gone — reporting only the error would leave them unaccounted for.
func TestRetentionSweep_ReportsRowsDeletedBeforeAFailure(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	p := &fakePruner{
		deleted: map[container.ContainerStatus]int64{container.ContainerStatusFailed: 12},
		err:     fmt.Errorf("connection reset"),
	}
	s, logs, recorded := newTestScheduler(p, now, 14*24*time.Hour)

	s.sweep(context.Background())

	if len(*logs) != 1 || !strings.Contains((*logs)[0], "FAILED") {
		t.Fatalf("a failed sweep must be logged as a failure; got: %v", *logs)
	}
	if !strings.Contains((*logs)[0], "12") {
		t.Errorf("a failed sweep must still account for the rows it DID delete; got: %s", (*logs)[0])
	}
	if len(*recorded) != 1 || (*recorded)[0].deleted != 12 {
		t.Errorf("rows deleted before the failure must still be recorded; got %+v", *recorded)
	}
}

// The cutoff handed to the pruner is now minus the window — the property that decides what
// survives. An inverted sign here would delete everything EXCEPT the old rows.
func TestRetentionSweep_CutoffIsNowMinusTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	window := 14 * 24 * time.Hour
	p := &fakePruner{deleted: map[container.ContainerStatus]int64{}}
	s, _, _ := newTestScheduler(p, now, window)

	s.sweep(context.Background())

	if len(p.cutoffs) != 1 {
		t.Fatalf("expected one prune call, got %d", len(p.cutoffs))
	}
	if want := now.Add(-window); !p.cutoffs[0].Equal(want) {
		t.Fatalf("cutoff = %s, want %s (now minus the window)", p.cutoffs[0], want)
	}
}

// Run must sweep IMMEDIATELY, before waiting out the first interval. A daemon that restarts more
// often than the interval would otherwise never prune at all — and a crash-looping daemon is
// exactly the condition that fills this table.
func TestRetentionScheduler_SweepsOnceAtStartWithoutWaitingForTheFirstTick(t *testing.T) {
	p := &fakePruner{deleted: map[container.ContainerStatus]int64{}}
	s, _, _ := newTestScheduler(p, time.Now(), 14*24*time.Hour)
	s.interval = time.Hour // far longer than this test will wait

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for p.calls() == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("Run did not sweep at start — with a 1h interval a daemon restarting more often than " +
				"that would never prune, which is precisely the crash-loop case")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run must return nil on ctx cancellation (the supervise layer reads that as a clean stop), got %v", err)
	}
}
