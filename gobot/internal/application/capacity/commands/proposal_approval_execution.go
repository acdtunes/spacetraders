package commands

// The tiered-autonomy APPROVAL-EXECUTION path (bead st-0h8, epic st-7zk).
//
// Under v1 tiered autonomy the CONVERGE backstop structurally refuses EVERY
// Approved tier-4 action at/over the approval threshold (exceedsApprovalGate),
// so a capital add NEVER executes straight from the governor. This is the ONLY
// sanctioned route it executes: a human/captain approves a filed proposal, and
// THIS path drives it — through the SAME invariant-4 gate (exceedsApprovalGate)
// and the SAME canonical verb→tier check (verbTierError) CONVERGE uses. Reusing
// those exact predicates (this file lives in the coordinator's package on
// purpose) means there is NO second guard stack to drift out of lockstep with
// the money gate.
//
// DEPLOY-INERT: st-0h8 builds and proves the mechanism; it does NOT wire a live
// trigger into the reconcile loop, and st-cpc (pre-arm cleanup) HARDENED it for
// concurrent sweeps WITHOUT arming it. A future arming lane backs
// ApprovedProposalSource with the real captain approval signal (a bead
// status/label transition) and drives ExecuteApproved on a cadence. Until then
// nothing is ever approved, so nothing executes — and the production
// Actuator.ExecuteCapital is itself still a fail-closed stub (st-5ig), a third
// independent layer between an approval and a real purchase.

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// ApprovedProposalSource yields the capital proposals a human/captain has
// APPROVED and that await execution. INTEGRATION POINT for a future arming lane:
// back this with the captain approval signal (a bead status/label transition — a
// proposal whose backing EventCapacityCapexProposal the captain declared/approved).
// st-0h8 proves the execution path against this seam; st-cpc serialized the sweep
// against double-execution (see ProposalApprovalExecutor.sweep). If a future arming
// lane ever fans execution across processes, add an atomic approved→in-flight claim
// HERE — the in-process sweep lock cannot serialize across separate instances.
type ApprovedProposalSource interface {
	ApprovedProposals(ctx context.Context, playerID int) ([]capacity.Proposal, error)
}

// ProposalExecutionRecorder marks a proposal Executed once its capital action
// has driven successfully (the Approved → Executed lifecycle transition).
type ProposalExecutionRecorder interface {
	MarkExecuted(ctx context.Context, proposal capacity.Proposal) error
}

// ProposalExecutionReport is one approval-sweep's audit: what executed and what
// was refused/failed (each with its reason). A refusal or a failed drive is a
// per-item outcome, never a hard error — the sweep processes the rest.
type ProposalExecutionReport struct {
	Executed []capacity.Proposal
	Failures []string
}

// ProposalApprovalExecutor drives approved capital proposals to execution
// through the invariant-4 gate.
type ProposalApprovalExecutor struct {
	source   ApprovedProposalSource
	actuator capacity.Actuator
	recorder ProposalExecutionRecorder

	// sweep serializes ExecuteApproved END-TO-END (the ApprovedProposals READ
	// included) so two OVERLAPPING sweeps can never both grab the same approved
	// proposal before either marks it Executed — the double capital-spend hazard
	// (st-cpc item 4). Wrapping the read is what makes it correct: a second sweep
	// reads the approved-and-awaiting set only AFTER the first sweep's MarkExecuted
	// has moved every executed proposal out of it, so the second finds nothing left
	// to re-spend. Preserves every st-0h8 invariant VERBATIM — the per-proposal path
	// (gate → verb/tier → ExecuteCapital → mark-on-success, skip-and-retry on
	// failure) is untouched; serialization only removes the interleave, adding no
	// per-proposal state.
	//
	// Assumes a SINGLE executor instance, which the ONE standing capacity reconciler
	// guarantees (the executor is a stateless in-process helper it drives, never
	// fanned out; double-launch is itself refused — TestCapacityReconcilerCoordinator
	// RefusesDoubleLaunch). If a future arming lane ever fans execution across
	// processes/instances, this in-process lock cannot serialize across them — see the
	// atomic-claim note on ApprovedProposalSource.
	sweep sync.Mutex
}

// NewProposalApprovalExecutor wires the executor to its approval source, the
// capital actuator, and the execution recorder.
func NewProposalApprovalExecutor(source ApprovedProposalSource, actuator capacity.Actuator, recorder ProposalExecutionRecorder) *ProposalApprovalExecutor {
	return &ProposalApprovalExecutor{source: source, actuator: actuator, recorder: recorder}
}

// ExecuteApproved drives every currently-approved capital proposal to execution
// through the gate, marking each Executed on a successful drive. A refused or
// failed proposal is recorded in the report and skipped (never marked), so it is
// re-attempted on the next sweep — the same statelessness the reconcile loop has.
//
// The whole sweep (the ApprovedProposals read through the last mark) runs under
// x.sweep, so two overlapping calls cannot both grab and double-execute one
// proposal — see the field doc for why serializing the read is the correctness
// point, and for the single-instance assumption.
func (x *ProposalApprovalExecutor) ExecuteApproved(ctx context.Context, playerID int, cal capacity.Calibration) (ProposalExecutionReport, error) {
	x.sweep.Lock()
	defer x.sweep.Unlock()

	proposals, err := x.source.ApprovedProposals(ctx, playerID)
	if err != nil {
		return ProposalExecutionReport{}, fmt.Errorf("read approved proposals: %w", err)
	}
	report := ProposalExecutionReport{}
	for _, proposal := range proposals {
		if err := x.execute(ctx, proposal, cal); err != nil {
			report.Failures = append(report.Failures, err.Error())
			continue
		}
		proposal.Status = capacity.ProposalExecuted
		if err := x.recorder.MarkExecuted(ctx, proposal); err != nil {
			report.Failures = append(report.Failures, fmt.Sprintf("proposal %s executed but not recorded: %v", proposal.ID, err))
			continue
		}
		report.Executed = append(report.Executed, proposal)
	}
	return report, nil
}

// execute drives ONE proposal past the invariant-4 gate to ExecuteCapital. Every
// clause is fail-closed and must hold before a single credit moves:
//
//  1. Status == Approved — invariant 4: NO tier-4 executes without a genuine
//     human approval. A pending/rejected/expired proposal is refused here.
//  2. PlayerID != 0 — an unattributed proposal is a wiring bug; refuse.
//  3. verbTierError == nil — the SAME canonical verb→tier check CONVERGE uses;
//     a tier-mislabeled action (buy_hull claiming tier-2) cannot ride this path.
//  4. exceedsApprovalGate == true — the SAME gate CONVERGE refuses Approved
//     tier-4 with. Here it is required POSITIVELY: only a genuinely gated tier-4
//     capital action (which HAS its approval) executes; a cheap/below-threshold
//     action smuggled into the approval path is refused (it belongs in CONVERGE).
//
// Together with CONVERGE's refusal of Approved tier-4, this makes the biconditional
// exact: a gated capital action reaches ExecuteCapital IFF it has an approved proposal.
func (x *ProposalApprovalExecutor) execute(ctx context.Context, proposal capacity.Proposal, cal capacity.Calibration) error {
	if proposal.Status != capacity.ProposalApproved {
		return fmt.Errorf("proposal %s: refusing to execute — status %q is not approved (invariant 4: no tier-4 without an approved proposal)", proposal.ID, proposal.Status)
	}
	if proposal.PlayerID == 0 {
		return fmt.Errorf("proposal %s: refusing to execute — zero PlayerID (fail-closed)", proposal.ID)
	}
	if err := verbTierError(proposal.Action); err != nil {
		return fmt.Errorf("proposal %s: %w", proposal.ID, err)
	}
	if !exceedsApprovalGate(proposal.Action, cal) {
		return fmt.Errorf("proposal %s: refusing to execute %s %s — not a gated capital action (cost %d < approval threshold %d): the approval path executes ONLY gated tier-4 (a cheap/below-threshold action auto-executes in CONVERGE, not here)",
			proposal.ID, proposal.Action.Tier, proposal.Action.Verb, proposal.Action.EstimatedCostCredits, cal.ApprovalThresholdCredits)
	}
	if err := x.actuator.ExecuteCapital(ctx, proposal.Action); err != nil {
		return fmt.Errorf("proposal %s: execute capital %s: %w", proposal.ID, proposal.Action.Verb, err)
	}
	return nil
}
