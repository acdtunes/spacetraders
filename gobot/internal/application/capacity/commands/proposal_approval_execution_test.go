package commands

// Behavioral tests for the tiered-autonomy APPROVAL-EXECUTION path. This is the
// ONLY route a tier-4 capital action executes under v1:
// CONVERGE structurally refuses every Approved tier-4 at/over the threshold (see
// run_capacity_reconciler_coordinator_test.go), so capital reaches
// Actuator.ExecuteCapital exclusively via a human-approved proposal, and only
// through the SAME invariant-4 gate (exceedsApprovalGate) + canonical verb/tier
// check CONVERGE uses — no second guard stack.
//
// Every test drives the executor's driving port (ExecuteApproved) and asserts at
// the driven-port boundaries: the spy actuator (was ExecuteCapital called, with
// what?) and the fake status recorder (was the proposal marked Executed?). The
// only doubles are at those ports; the gate logic under test is real.
//
// Test budget: 3 distinct behaviors × 2 = 6 max. 3 written (one parametrized):
//  1. an approved, well-formed capital proposal executes verbatim + is marked Executed
//  2. THE GATE PROOF: a proposal that is not a genuine, well-formed, gated tier-4
//     approval can NEVER reach ExecuteCapital (5 bypass vectors, all refused)
//  3. a failed capital drive is isolated and NOT marked Executed (retries later)

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// fakeApprovedSource is the approval seam: it yields the proposals a human/captain
// has approved and that await execution. A future arming lane backs it with the
// real captain approval signal.
type fakeApprovedSource struct {
	proposals []capacity.Proposal
	err       error
}

func (f *fakeApprovedSource) ApprovedProposals(_ context.Context, _ int) ([]capacity.Proposal, error) {
	return f.proposals, f.err
}

// fakeExecutionRecorder records which proposals were marked Executed.
type fakeExecutionRecorder struct {
	marked []string
	err    error
}

func (f *fakeExecutionRecorder) MarkExecuted(_ context.Context, p capacity.Proposal) error {
	if f.err != nil {
		return f.err
	}
	f.marked = append(f.marked, p.ID)
	return nil
}

func approvedCapitalProposal() capacity.Proposal {
	return capacity.Proposal{
		ID:       "prop-1",
		PlayerID: 7,
		Status:   capacity.ProposalApproved,
		Action: capacity.Action{
			Tier:                 capacity.TierCapital,
			Verb:                 capacity.VerbBuyHull,
			HubSymbol:            "X1-HUB-B",
			GapKind:              capacity.GapWorkerShort,
			EstimatedCostCredits: 120000,
			HullDelta:            1,
		},
	}
}

// Behavior: an approved, well-formed capital proposal executes its action
// VERBATIM via Actuator.ExecuteCapital — through the gate — and is then marked
// Executed.
func TestProposalApprovalExecutor_ExecutesApprovedCapitalThroughGate(t *testing.T) {
	actuator := newSpyActuator()
	proposal := approvedCapitalProposal()
	source := &fakeApprovedSource{proposals: []capacity.Proposal{proposal}}
	recorder := &fakeExecutionRecorder{}
	executor := NewProposalApprovalExecutor(source, actuator, recorder)

	report, err := executor.ExecuteApproved(context.Background(), 7, capacity.DefaultCalibration())
	require.NoError(t, err)

	require.Equal(t, []capacity.Action{proposal.Action}, actuator.calls(capacity.VerbBuyHull),
		"an approved proposal must execute its action VERBATIM via ExecuteCapital")
	require.Equal(t, []string{"prop-1"}, recorder.marked,
		"a successfully executed proposal must be marked Executed")
	require.Len(t, report.Executed, 1)
	require.Equal(t, capacity.ProposalExecuted, report.Executed[0].Status)
	require.Empty(t, report.Failures)
}

// Behavior (THE KEY GUARD PROOF): a proposal cannot bypass the capital gate.
// A tier-4 action reaches ExecuteCapital via the approval path ONLY when it is a
// genuine (Status==Approved), attributed (non-zero PlayerID), canonically-tiered,
// gated (tier-4 at/over threshold) capital action. Every other shape — an
// un-approved proposal, a zero-player proposal, a tier-mislabeled action, or a
// cheap action smuggled into the approval path — is REFUSED, and ExecuteCapital
// is never called.
func TestProposalApprovalExecutor_CannotBypassCapitalGate(t *testing.T) {
	cases := []struct {
		name     string
		proposal capacity.Proposal
		wantSub  string
	}{
		{
			name: "pending proposal has no human approval (invariant 4: no tier-4 without an approved proposal)",
			proposal: func() capacity.Proposal {
				p := approvedCapitalProposal()
				p.Status = capacity.ProposalPending
				return p
			}(),
			wantSub: "approv",
		},
		{
			name: "rejected proposal never executes",
			proposal: func() capacity.Proposal {
				p := approvedCapitalProposal()
				p.Status = capacity.ProposalRejected
				return p
			}(),
			wantSub: "approv",
		},
		{
			name: "zero PlayerID fails closed",
			proposal: func() capacity.Proposal {
				p := approvedCapitalProposal()
				p.PlayerID = 0
				return p
			}(),
			wantSub: "player",
		},
		{
			name: "tier mislabel cannot ride the capital path (buy_hull claiming tier-2)",
			proposal: func() capacity.Proposal {
				p := approvedCapitalProposal()
				p.Action.Tier = capacity.TierRebalance // buy_hull is canonically tier-4
				return p
			}(),
			wantSub: "verb/tier",
		},
		{
			name: "a cheap (non-gated) action smuggled into the approval path is refused",
			proposal: func() capacity.Proposal {
				p := approvedCapitalProposal()
				p.Action = capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}
				return p
			}(),
			wantSub: "gate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actuator := newSpyActuator()
			source := &fakeApprovedSource{proposals: []capacity.Proposal{tc.proposal}}
			recorder := &fakeExecutionRecorder{}
			executor := NewProposalApprovalExecutor(source, actuator, recorder)

			report, err := executor.ExecuteApproved(context.Background(), 7, capacity.DefaultCalibration())
			require.NoError(t, err, "a refused proposal is reported per-item, not a hard error")

			require.Zero(t, actuator.totalCalls(),
				"THE capital gate: a proposal that is not a genuine, well-formed, gated tier-4 approval must NEVER reach ExecuteCapital")
			require.Empty(t, recorder.marked, "a refused proposal must never be marked Executed")
			require.Len(t, report.Failures, 1)
			require.Contains(t, strings.ToLower(report.Failures[0]), tc.wantSub)
			require.Empty(t, report.Executed)
		})
	}
}

// Behavior: a capital drive that FAILS is isolated — the executor reports it and
// leaves the proposal Approved (NOT marked Executed) so the next approval sweep
// retries it. Statelessness: a failed buy simply reappears next pass.
func TestProposalApprovalExecutor_FailedDriveIsIsolatedAndNotMarkedExecuted(t *testing.T) {
	actuator := newSpyActuator()
	actuator.failVerb = capacity.VerbBuyHull // the capital drive errors (fail-closed stub, or a real buy error)
	proposal := approvedCapitalProposal()
	source := &fakeApprovedSource{proposals: []capacity.Proposal{proposal}}
	recorder := &fakeExecutionRecorder{}
	executor := NewProposalApprovalExecutor(source, actuator, recorder)

	report, err := executor.ExecuteApproved(context.Background(), 7, capacity.DefaultCalibration())
	require.NoError(t, err)

	require.Equal(t, []capacity.Action{proposal.Action}, actuator.calls(capacity.VerbBuyHull),
		"the gate passed on a genuine approval — ExecuteCapital WAS attempted")
	require.Empty(t, recorder.marked,
		"a proposal whose capital drive FAILED must NOT be marked Executed — it retries next approval sweep")
	require.Empty(t, report.Executed)
	require.Len(t, report.Failures, 1)
	require.Contains(t, report.Failures[0], "actuator boom")
}
