package commands

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// These tests cover the sp-ev0n concurrent-hull cap: runWorkersBounded fans out one
// worker per node but never runs more than `workerCap` at once, so a live cap change
// converges the factory's concurrent hull draw to N on the next pass. The bound is
// exercised directly with a blocking fake worker (no ship repo / market / mediator),
// so the assertions are deterministic — they never depend on a sleep race.

// fakeWorkerCapProvider is an in-memory FactoryWorkerCapProvider for resolveEffectiveWorkerCap.
type fakeWorkerCapProvider struct {
	cap int
	ok  bool
	err error
}

func (f *fakeWorkerCapProvider) WorkerCap(_ context.Context, _ string, _ int) (int, bool, error) {
	return f.cap, f.ok, f.err
}

// capProbe records the peak number of workers that ran concurrently. Workers block
// on `release` so the test can pin the exact concurrency the cap permits before
// letting them finish — deterministic, no timing guesswork.
type capProbe struct {
	current int32
	peak    int32
	release chan struct{}
}

func newCapProbe() *capProbe { return &capProbe{release: make(chan struct{})} }

func (p *capProbe) worker(_ context.Context, _ *goods.SupplyChainNode) (*mfgServices.ProductionResult, error) {
	c := atomic.AddInt32(&p.current, 1)
	for {
		old := atomic.LoadInt32(&p.peak)
		if c <= old || atomic.CompareAndSwapInt32(&p.peak, old, c) {
			break
		}
	}
	<-p.release
	atomic.AddInt32(&p.current, -1)
	return &mfgServices.ProductionResult{QuantityAcquired: 1}, nil
}

// waitForCurrent blocks until the live concurrent-worker count reaches want, or fails
// the test on timeout (which is exactly what a too-small cap would cause).
func waitForCurrent(t *testing.T, p *capProbe, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&p.current) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d worker(s) ran concurrently; cap never let %d run at once", atomic.LoadInt32(&p.current), want)
}

func nBuyLeaves(n int) []*goods.SupplyChainNode {
	nodes := make([]*goods.SupplyChainNode, n)
	for i := range nodes {
		nodes[i] = buyLeaf(testInputGood)
	}
	return nodes
}

// driveBoundedFanout runs runWorkersBounded on nodeCount nodes at the given cap,
// pins concurrency at expectedConcurrent, and returns the observed peak plus the
// number of nodes that completed.
func driveBoundedFanout(t *testing.T, workerCap, nodeCount, expectedConcurrent int) (peak, completed int) {
	t.Helper()
	h := &RunFactoryCoordinatorHandler{}
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	probe := newCapProbe()
	nodes := nBuyLeaves(nodeCount)

	type outcome struct {
		results []*mfgServices.ProductionResult
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := h.runWorkersBounded(ctx, workerCap, nodes, probe.worker)
		done <- outcome{results: results, err: err}
	}()

	// The cap must ALLOW exactly expectedConcurrent workers to run at once.
	waitForCurrent(t, probe, int32(expectedConcurrent))
	// Give any erroneously-unblocked extra worker a moment to bump the count, then
	// confirm the cap held it at expectedConcurrent (proves it is not exceeded).
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(expectedConcurrent), atomic.LoadInt32(&probe.current),
		"more workers ran concurrently than the cap permits")

	close(probe.release)
	out := <-done
	require.NoError(t, out.err)
	return int(atomic.LoadInt32(&probe.peak)), len(out.results)
}

// Acceptance: cap=2 → at most 2 hulls run at once, all 5 nodes still processed.
func TestRunWorkersBounded_CapTwo_ConvergesToTwoConcurrent(t *testing.T) {
	peak, completed := driveBoundedFanout(t, 2, 5, 2)
	require.Equal(t, 2, peak, "cap=2 must hold concurrent workers at 2")
	require.Equal(t, 5, completed, "every node must still be processed under the cap")
}

// Acceptance: raising the cap scales the fan-out up — cap=4 lets 4 run at once.
func TestRunWorkersBounded_CapFour_ScalesUp(t *testing.T) {
	peak, completed := driveBoundedFanout(t, 4, 5, 4)
	require.Equal(t, 4, peak, "cap=4 must allow 4 concurrent workers (scale-up)")
	require.Equal(t, 5, completed)
}

// cap<=0 is unbounded: every node runs concurrently (the pre-sp-ev0n fan-out).
func TestRunWorkersBounded_ZeroCap_Unbounded(t *testing.T) {
	peak, completed := driveBoundedFanout(t, 0, 5, 5)
	require.Equal(t, 5, peak, "cap<=0 must leave the fan-out unbounded")
	require.Equal(t, 5, completed)
}

// A worker failure PARKS its node (excluded from results) but does NOT abort the run
// — the sp-vsfn policy must survive the extraction into runWorkersBounded.
func TestRunWorkersBounded_WorkerError_ParksNodeNotRun(t *testing.T) {
	h := &RunFactoryCoordinatorHandler{}
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	nodes := nBuyLeaves(3)
	var calls int32
	worker := func(_ context.Context, n *goods.SupplyChainNode) (*mfgServices.ProductionResult, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, errors.New("transient market read failure")
		}
		return &mfgServices.ProductionResult{QuantityAcquired: 1}, nil
	}

	results, err := h.runWorkersBounded(ctx, 2, nodes, worker)
	require.NoError(t, err, "a non-shutdown worker error must not abort the run")
	require.Len(t, results, 2, "the failed node is parked (excluded), the other two complete")
}

// A container-shutdown signal (context cancel) ABORTS the run — the one error the
// park policy must not swallow.
func TestRunWorkersBounded_ShutdownSignal_AbortsRun(t *testing.T) {
	h := &RunFactoryCoordinatorHandler{}
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	nodes := nBuyLeaves(3)
	worker := func(_ context.Context, _ *goods.SupplyChainNode) (*mfgServices.ProductionResult, error) {
		return nil, context.Canceled
	}

	_, err := h.runWorkersBounded(ctx, 2, nodes, worker)
	require.ErrorIs(t, err, context.Canceled, "a shutdown signal must abort the run, not park")
}

// resolveEffectiveWorkerCap: the live provider override wins over the launch cap;
// a nil provider, a read error, or no live override falls back to the launch cap.
func TestResolveEffectiveWorkerCap(t *testing.T) {
	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	cmd := &RunFactoryCoordinatorCommand{ContainerID: testContainerID, PlayerID: 1, WorkerCap: 3}

	t.Run("nil provider falls back to launch cap", func(t *testing.T) {
		h := &RunFactoryCoordinatorHandler{}
		require.Equal(t, 3, h.resolveEffectiveWorkerCap(ctx, cmd))
	})

	t.Run("live override wins", func(t *testing.T) {
		h := &RunFactoryCoordinatorHandler{workerCapProvider: &fakeWorkerCapProvider{cap: 2, ok: true}}
		require.Equal(t, 2, h.resolveEffectiveWorkerCap(ctx, cmd))
	})

	t.Run("no live override falls back to launch cap", func(t *testing.T) {
		h := &RunFactoryCoordinatorHandler{workerCapProvider: &fakeWorkerCapProvider{ok: false}}
		require.Equal(t, 3, h.resolveEffectiveWorkerCap(ctx, cmd))
	})

	t.Run("read error falls back to launch cap", func(t *testing.T) {
		h := &RunFactoryCoordinatorHandler{workerCapProvider: &fakeWorkerCapProvider{err: errors.New("db down")}}
		require.Equal(t, 3, h.resolveEffectiveWorkerCap(ctx, cmd))
	})
}
