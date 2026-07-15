package capacity

import (
	"context"
	"fmt"
)

// The NoOp components make a freshly-wired engine provably INERT end-to-end:
// the loop runs every phase, computes an empty desired topology, diffs zero
// actions, governs zero, and converges zero. Each sibling lane replaces
// exactly ONE of these in the daemon wiring with its real implementation; the
// chain stays inert until every rung is real AND the engine is explicitly
// started.
//
// The two write-capable seams (Actuator, ProposalChannel) FAIL LOUD when
// invoked instead of pretending success — an accidental invocation must
// surface, never silently "succeed".

// NoOpSensor returns an empty Signals snapshot (st-7ee replaces it).
type NoOpSensor struct{}

func (NoOpSensor) Sense(_ context.Context, playerID int) (Signals, error) {
	return Signals{PlayerID: playerID}, nil
}

// NoOpPlanner wants nothing: it returns the empty DesiredTopology every tick,
// so the loop provably emits zero actions end-to-end (st-hlw replaces it).
type NoOpPlanner struct{}

func (NoOpPlanner) ComputeDesired(_ context.Context, _ Signals, _ Calibration) (DesiredTopology, error) {
	return DesiredTopology{}, nil
}

// NoOpDiffer emits zero actions regardless of divergence (st-zr0 replaces it).
type NoOpDiffer struct{}

func (NoOpDiffer) Diff(_ context.Context, _ DesiredTopology, _ TopologySignals, _ Calibration) ([]Action, error) {
	return nil, nil
}

// NoOpGovernor approves nothing and proposes nothing (st-x00 replaces it).
type NoOpGovernor struct{}

func (NoOpGovernor) Govern(_ context.Context, _ []Action, _ EconomicsSignals, _ Calibration) (GovernResult, error) {
	return GovernResult{}, nil
}

// NoOpActuator refuses every verb loudly. It exists so the foundation wiring
// is complete without a single write path; st-5ig replaces it.
type NoOpActuator struct{}

func (NoOpActuator) ReuseIdleHull(_ context.Context, action Action) error {
	return noOpActuatorErr("ReuseIdleHull", action)
}

func (NoOpActuator) Rebalance(_ context.Context, action Action) error {
	return noOpActuatorErr("Rebalance", action)
}

func (NoOpActuator) AdjustBuffer(_ context.Context, action Action) error {
	return noOpActuatorErr("AdjustBuffer", action)
}

func (NoOpActuator) ExecuteCapital(_ context.Context, action Action) error {
	return noOpActuatorErr("ExecuteCapital", action)
}

func noOpActuatorErr(verb string, action Action) error {
	return fmt.Errorf("capacity actuator not wired: refusing %s(%s %s)", verb, action.Tier, action.Verb)
}

// NoOpProposalChannel refuses every submission loudly (st-0h8 replaces it).
type NoOpProposalChannel struct{}

func (NoOpProposalChannel) Submit(_ context.Context, proposal Proposal) error {
	return fmt.Errorf("capacity proposal channel not wired: refusing proposal %s (%s)", proposal.ID, proposal.Action.Verb)
}
