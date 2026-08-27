package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// fakeLogPruner records what it was asked for and returns a scripted result.
type fakeLogPruner struct {
	mu           sync.Mutex
	opts         []persistence.ContainerLogPruneOptions
	protectCalls int
	protected    []int
	protectErr   error
	result       persistence.ContainerLogPruneResult
	err          error
}

func (p *fakeLogPruner) ProtectedContainerLogIDs(context.Context) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protectCalls++
	return p.protected, p.protectErr
}

func (p *fakeLogPruner) PruneContainerLogs(_ context.Context, opts persistence.ContainerLogPruneOptions) (persistence.ContainerLogPruneResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts = append(p.opts, opts)
	return p.result, p.err
}

func (p *fakeLogPruner) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.opts)
}

func newTestLogScheduler(p *fakeLogPruner, now time.Time) (*ContainerLogRetentionScheduler, *[]string, *[]recordedMetric) {
	var logs []string
	var recorded []recordedMetric
	s := &ContainerLogRetentionScheduler{
		pruner:          p,
		transientWindow: 48 * time.Hour,
		problemWindow:   14 * 24 * time.Hour,
		batchSize:       10_000,
		maxBatches:      2_000,
		interval:        time.Hour,
		now:             func() time.Time { return now },
		logf: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
		record: func(class string, deleted int64) {
			recorded = append(recorded, recordedMetric{status: class, deleted: deleted})
		},
		stopCh: make(chan struct{}),
	}
	return s, &logs, &recorded
}

// Each class gets its own cutoff: now minus its own window. Collapsing them — or inverting the
// sign — would either delete the incident spine at 48h or keep the chatter for a fortnight, and
// the whole affordability argument for the short window rests on the split.
func TestContainerLogRetentionSweep_CutoffIsNowMinusEachClassWindow(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{}
	s, _, _ := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if len(p.opts) != 1 {
		t.Fatalf("expected one prune call, got %d", len(p.opts))
	}
	got := p.opts[0]
	if want := now.Add(-48 * time.Hour); !got.TransientCutoff.Equal(want) {
		t.Errorf("TransientCutoff = %s, want %s", got.TransientCutoff, want)
	}
	if want := now.Add(-14 * 24 * time.Hour); !got.ProblemCutoff.Equal(want) {
		t.Errorf("ProblemCutoff = %s, want %s", got.ProblemCutoff, want)
	}
	if got.BatchSize != 10_000 || got.MaxBatches != 2_000 {
		t.Errorf("the sweep must carry its batch bounds into the pruner; got batch=%d max=%d",
			got.BatchSize, got.MaxBatches)
	}
}

// FAIL CLOSED ON AN UNREADABLE PROTECTION SET. The protected ids are the newest log line of every
// live container — the liveness watchdog's only progress signal. If that set cannot be read, the
// sweep does not know which rows are load-bearing, and deleting anyway could silently disable the
// hung-tour killer. Retention is irreversible and a day of extra rows costs nothing, so an
// unreadable protection set aborts the sweep rather than proceeding without it.
func TestContainerLogRetentionSweep_AbortsWhenTheProtectionSetCannotBeRead(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{protectErr: fmt.Errorf("connection reset")}
	s, logs, _ := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if p.calls() != 0 {
		t.Fatal("the sweep deleted rows without knowing which ones the liveness watchdog depends on; " +
			"an unreadable protection set must abort the sweep, not soften it")
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "connection reset") {
		t.Fatalf("an aborted sweep must say why; got: %v", *logs)
	}
}

// The protection set the pruner is given is exactly the one the repository reported. Dropping it
// on the way through would be indistinguishable from having no protection at all.
func TestContainerLogRetentionSweep_HandsTheProtectionSetToThePruner(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{protected: []int{7, 11, 13}}
	s, _, _ := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if len(p.opts) != 1 {
		t.Fatalf("expected one prune call, got %d", len(p.opts))
	}
	if len(p.opts[0].ProtectedIDs) != 3 {
		t.Fatalf("ProtectedIDs = %v, want the three ids the repository reported", p.opts[0].ProtectedIDs)
	}
}

// OBSERVABILITY, inherited from the containers sweep: a silent pruner cannot be told apart from a
// broken one, and what it deletes is exactly the evidence someone will later go looking for. Every
// sweep reports, INCLUDING the ones that delete nothing.
func TestContainerLogRetentionSweep_AnEmptySweepIsStillReported(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{}
	s, logs, recorded := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if len(*logs) != 1 || !strings.Contains((*logs)[0], "transient=0") {
		t.Fatalf("an empty sweep must still report itself, naming each class at zero; got: %v", *logs)
	}
	if len(*recorded) != 2 {
		t.Fatalf("an empty sweep must still emit a sample per class, so the metric proves liveness; got %+v", *recorded)
	}
}

func TestContainerLogRetentionSweep_ReportsWhatItDeletedByClass(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{result: persistence.ContainerLogPruneResult{
		Transient: 416_000, Problem: 240, Batches: 42, LargestBatch: 10_000,
	}}
	s, logs, recorded := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if len(*logs) != 1 {
		t.Fatalf("expected exactly one sweep log line, got %d: %v", len(*logs), *logs)
	}
	for _, want := range []string{"416240", "transient=416000", "problem=240", "42 batch"} {
		if !strings.Contains((*logs)[0], want) {
			t.Errorf("sweep log is missing %q — the per-class breakdown and the batch count are what make "+
				"a vanished log line traceable to retention rather than to a bug.\ngot: %s", want, (*logs)[0])
		}
	}
	byClass := map[string]int64{}
	for _, r := range *recorded {
		byClass[r.status] = r.deleted
	}
	if byClass["transient"] != 416_000 || byClass["problem"] != 240 {
		t.Errorf("metric samples do not match the deletions: %+v", byClass)
	}
}

// A partial sweep is still a real deletion: rows removed before a failure are gone, and reporting
// only the error would leave them unaccounted for.
func TestContainerLogRetentionSweep_ReportsRowsDeletedBeforeAFailure(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{
		result: persistence.ContainerLogPruneResult{Transient: 12, Batches: 1},
		err:    fmt.Errorf("connection reset"),
	}
	s, logs, recorded := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if len(*logs) != 1 || !strings.Contains((*logs)[0], "FAILED") {
		t.Fatalf("a failed sweep must be logged as a failure; got: %v", *logs)
	}
	if !strings.Contains((*logs)[0], "12") {
		t.Errorf("a failed sweep must still account for the rows it DID delete; got: %s", (*logs)[0])
	}
	if len(*recorded) != 2 {
		t.Errorf("rows deleted before the failure must still be recorded; got %+v", *recorded)
	}
}

// A sweep cut short by MaxBatches must be visible as such: a backlog that never clears otherwise
// looks exactly like a table that is simply that size.
func TestContainerLogRetentionSweep_SaysWhenTheBatchBoundCutItShort(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p := &fakeLogPruner{result: persistence.ContainerLogPruneResult{
		Transient: 20_000_000, Batches: 2_000, ReachedMaxBatches: true,
	}}
	s, logs, _ := newTestLogScheduler(p, now)

	s.sweep(context.Background())

	if !strings.Contains((*logs)[0], "BATCH BOUND") {
		t.Fatalf("a truncated sweep must name the bound that stopped it; got: %s", (*logs)[0])
	}
}

// Run must sweep IMMEDIATELY, before waiting out the first interval — the same reasoning as the
// containers sweep: a daemon restarting more often than the interval would otherwise never prune,
// and a daemon that restarts a lot is exactly the condition that fills this table.
func TestContainerLogRetentionScheduler_SweepsOnceAtStartWithoutWaitingForTheFirstTick(t *testing.T) {
	p := &fakeLogPruner{}
	s, _, _ := newTestLogScheduler(p, time.Now())
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
