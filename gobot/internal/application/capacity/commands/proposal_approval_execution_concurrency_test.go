package commands

// Concurrency hardening for the tiered-autonomy APPROVAL-EXECUTION path. The
// executor is STILL UNTRIGGERED — this only proves the sweep is safe for when a
// future arming lane drives it on a cadence.
//
// The hazard: ExecuteApproved pulls the approved-and-awaiting set from the
// source, then per proposal executes ExecuteCapital and marks it Executed. Two
// OVERLAPPING sweeps could both read the SAME still-approved proposal BEFORE
// either marks it Executed ⇒ ExecuteCapital fires twice ⇒ a DOUBLE capital
// spend. This test drives the executor's driving port (ExecuteApproved) from two
// overlapping goroutines and asserts at the driven-port boundary that the sole
// approved proposal is spent EXACTLY once and marked Executed EXACTLY once.
//
// Test budget: 1 new distinct behavior (concurrent sweeps ⇒ at-most-once
// execution) × 2 = 2 max; 1 written. The gate-bypass, verbatim-execute, and
// fail-closed-retry behaviors stay covered by proposal_approval_execution_test.go.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// fakeProposalStore is ONE backing store playing both approval ports — exactly as
// the production seam will (the source query and the Executed transition are two
// views of the same bead-status store). ApprovedProposals yields the
// approved-AND-not-yet-executed set; MarkExecuted removes a proposal from it (an
// executed proposal is no longer awaiting execution). Both ops are mutex-guarded
// so the fake behaves like a real transactional store and the test is -race clean.
type fakeProposalStore struct {
	mu       sync.Mutex
	approved map[string]capacity.Proposal
	order    []string
	executed []string
}

func newFakeProposalStore(proposals ...capacity.Proposal) *fakeProposalStore {
	s := &fakeProposalStore{approved: map[string]capacity.Proposal{}}
	for _, p := range proposals {
		s.approved[p.ID] = p
		s.order = append(s.order, p.ID)
	}
	return s
}

func (s *fakeProposalStore) ApprovedProposals(_ context.Context, _ int) ([]capacity.Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []capacity.Proposal{}
	for _, id := range s.order {
		if p, ok := s.approved[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *fakeProposalStore) MarkExecuted(_ context.Context, p capacity.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.approved, p.ID)
	s.executed = append(s.executed, p.ID)
	return nil
}

func (s *fakeProposalStore) executedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.executed)
}

// gatedCapitalActuator counts ExecuteCapital calls and PARKS the very first caller
// inside the spend (after it has read its proposal but before the sweep records
// the execution). That park is the deterministic race window: while the first
// sweep is held mid-spend, a second overlapping sweep gets its chance to re-read
// the still-approved proposal and double-spend. The fix must make the second sweep
// skip it. Parking only the first caller means the FIX (which serializes sweeps)
// never deadlocks — the second sweep simply never reaches ExecuteCapital.
type gatedCapitalActuator struct {
	mu      sync.Mutex
	capital int
	entered chan struct{}
	release chan struct{}
}

func newGatedCapitalActuator() *gatedCapitalActuator {
	return &gatedCapitalActuator{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedCapitalActuator) ExecuteCapital(_ context.Context, _ capacity.Action) error {
	g.mu.Lock()
	g.capital++
	first := g.capital == 1
	g.mu.Unlock()
	if first {
		close(g.entered) // first sweep has read its proposal and reached the spend, but has NOT yet marked it Executed
		<-g.release      // park so an overlapping sweep gets its chance to double-spend
	}
	return nil
}

func (g *gatedCapitalActuator) capitalCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capital
}

// The escalation-ladder verbs are unused here but required by capacity.Actuator.
func (g *gatedCapitalActuator) ReuseIdleHull(context.Context, capacity.Action) error { return nil }
func (g *gatedCapitalActuator) Rebalance(context.Context, capacity.Action) error     { return nil }
func (g *gatedCapitalActuator) AdjustBuffer(context.Context, capacity.Action) error  { return nil }

// Behavior: two OVERLAPPING ExecuteApproved sweeps execute the sole approved
// capital proposal EXACTLY once (and mark it Executed exactly once). With the
// double-execution bug the second sweep re-reads the still-approved proposal while
// the first is mid-spend and calls ExecuteCapital AGAIN (a double capital spend);
// the fix must serialize the sweeps so the second finds the proposal already
// executed and skips it.
func TestProposalApprovalExecutor_ConcurrentSweepsExecuteApprovedProposalExactlyOnce(t *testing.T) {
	proposal := approvedCapitalProposal()
	store := newFakeProposalStore(proposal)
	actuator := newGatedCapitalActuator()
	// The store backs BOTH the source and the recorder — the real single-store seam.
	executor := NewProposalApprovalExecutor(store, actuator, store)
	cal := capacity.DefaultCalibration()

	var sweeps sync.WaitGroup

	// Sweep A: reads the approved proposal and parks inside the capital spend,
	// having NOT yet marked it Executed.
	sweeps.Add(1)
	go func() {
		defer sweeps.Done()
		_, _ = executor.ExecuteApproved(context.Background(), 7, cal)
	}()
	<-actuator.entered

	// Sweep B overlaps A while the proposal is still approved. Under the bug it
	// double-spends; under the fix it is serialized behind A and skips the
	// already-executed proposal.
	sweeps.Add(1)
	go func() {
		defer sweeps.Done()
		_, _ = executor.ExecuteApproved(context.Background(), 7, cal)
	}()

	// Give the overlapping sweep its full chance to double-spend. Under the bug the
	// second ExecuteCapital lands within this window (capital → 2); under the fix B
	// is blocked behind A and capital can never exceed 1 no matter how long we wait,
	// so this bound only affects speed, never the verdict.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && actuator.capitalCalls() < 2 {
		time.Sleep(5 * time.Millisecond)
	}

	close(actuator.release) // let sweep A finish and record its execution
	sweeps.Wait()

	require.Equal(t, 1, actuator.capitalCalls(),
		"two overlapping approval sweeps must spend on the sole approved proposal EXACTLY once — a second concurrent ExecuteCapital is a double capital spend")
	require.Equal(t, 1, store.executedCount(),
		"the approved proposal must be marked Executed EXACTLY once")
}
