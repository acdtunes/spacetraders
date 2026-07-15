package capacity

import (
	"context"
	"time"
)

// Sensor collects one read-only Signals snapshot (spec: SENSE — daemon DB +
// live API, never a write). The SENSE lane (st-7ee) implements it.
type Sensor interface {
	Sense(ctx context.Context, playerID int) (Signals, error)
}

// Planner computes the desired topology from live signals (spec:
// Planner.ComputeDesired(signals) → DesiredTopology). Heuristic now (st-hlw);
// a solver slots in behind the same interface later. cal carries the live
// calibration (add-threshold, stocker budget) so policy numbers are config,
// not code.
type Planner interface {
	ComputeDesired(ctx context.Context, signals Signals, cal Calibration) (DesiredTopology, error)
}

// Differ turns desired-vs-actual divergence into an ordered action list,
// cheapest-lever-first (spec: DIFF + escalation ladder; the ordering is what
// preserves per-hull-$/hr). The DIFF lane (st-zr0) implements it. An empty
// desired topology MUST yield zero actions.
type Differ interface {
	Diff(ctx context.Context, desired DesiredTopology, actual TopologySignals, cal Calibration) ([]Action, error)
}

// Governor turns the DIFF action list into a GovernResult (spec: GOVERN). The
// PRODUCTION impl is the THIN EMITTER capacity.CapexEmitter (capex_emitter.go,
// st-x00 re-scoped by st-5le): cheap tiers (1-3) pass through to Approved verbatim,
// and the CAPITAL tier (4) is summed into one contract-delivery CapitalDemand and
// EMITTED to the sp-1txd fleet autosizer — which owns the SINGLE guard stack
// (reserve floor, 25%-treasury, era payback, fleet ceilings, per-tick cap) that
// actually spends. The emitter computes NO capex budget and mints NO proposals
// (GovernResult.Proposals stays empty); safety invariant 4 holds structurally
// because the reconciler neither executes nor proposes the capital action. The
// interface still permits a future in-engine governor that gates and proposes, but
// nothing wires one today.
type Governor interface {
	Govern(ctx context.Context, actions []Action, economics EconomicsSignals, cal Calibration) (GovernResult, error)
}

// Actuator is the thin wrapper over the EXISTING actuator primitives (fleet
// autosizer, launch siting, worker-rebalancer, depot-rebalance) — the
// reconciler never reinvents buy/move (spec: Interfaces / seams). One method
// per tier; each takes the full Action. The actuator lane (st-5ig) implements
// it.
type Actuator interface {
	// ReuseIdleHull executes a tier-1 reassignment of an idle hull.
	ReuseIdleHull(ctx context.Context, action Action) error
	// Rebalance executes a tier-2 reposition/rebalance of existing capacity.
	Rebalance(ctx context.Context, action Action) error
	// AdjustBuffer executes a tier-3 buffer whitelist/cap change.
	AdjustBuffer(ctx context.Context, action Action) error
	// ExecuteCapital executes a tier-4 capital action. Called ONLY
	// post-approval (by the proposal channel's approval-execution path, or by
	// the loop when the governor auto-approved under a graduated threshold).
	ExecuteCapital(ctx context.Context, action Action) error
}

// ProposalChannel files capital proposals for human approval (spec: bead +
// captain nudge carrying the ROI evidence). The proposal lane (st-0h8)
// implements it; its approval path executes an approved proposal's Action via
// Actuator.ExecuteCapital.
type ProposalChannel interface {
	Submit(ctx context.Context, proposal Proposal) error
}

// CapacityDomain scopes the engine to one capacity domain measured on the
// shared per-hull-$/hr yardstick (spec: contract-delivery now; arb /
// manufacturing plug in later behind this same seam). A domain bundles its
// own Sensor + Planner; DIFF/GOVERN/CONVERGE are domain-agnostic.
type CapacityDomain interface {
	// Name identifies the domain in logs, actions, and proposals.
	Name() string
	Sensor() Sensor
	Planner() Planner
}

// ContractDeliveryDomainName is the v1 domain's canonical name.
const ContractDeliveryDomainName = "contract_delivery"

// NewStaticDomain bundles a Sensor and Planner under a named CapacityDomain —
// the standard way to assemble a domain from independently-built components.
func NewStaticDomain(name string, sensor Sensor, planner Planner) CapacityDomain {
	return staticDomain{name: name, sensor: sensor, planner: planner}
}

type staticDomain struct {
	name    string
	sensor  Sensor
	planner Planner
}

func (d staticDomain) Name() string     { return d.name }
func (d staticDomain) Sensor() Sensor   { return d.sensor }
func (d staticDomain) Planner() Planner { return d.planner }

// KillSwitch reports the fleet-wide captain/DISABLED kill switch. The loop
// consults it at the TOP OF EVERY TICK (not just startup) and idles while
// engaged. Production wires the watchkeeper Workspace
// (internal/captain/workspace.go — os.Stat of <workspace>/DISABLED), the
// exact mechanism the supervisor's Tick already honors.
type KillSwitch interface {
	Disabled() bool
}

// Phase names one reconcile-loop phase, in execution order.
type Phase string

const (
	PhaseSense    Phase = "SENSE"
	PhasePlan     Phase = "PLAN"
	PhaseDiff     Phase = "DIFF"
	PhaseGovern   Phase = "GOVERN"
	PhaseConverge Phase = "CONVERGE"
)

// TickOutcome is the observable result of one reconcile pass — the loop's
// action log. The harness lane (st-6wa) asserts scenario convergence and the
// safety invariants (kill switch honored, zero unapproved capital execution)
// against the stream of these.
type TickOutcome struct {
	// Sequence is the 1-based tick counter within this run.
	Sequence int
	// At is the tick's start time on the injected clock.
	At time.Time
	// Idle reports the kill switch was engaged: NO phase ran this tick.
	Idle bool
	// FailedPhase is the phase that errored ("" = every phase completed).
	// The rest of that tick was skipped; the loop continued to the next tick.
	FailedPhase Phase
	// Error is the failing phase's error text (or the converge failures).
	Error string
	// ActionsExecuted lists the actions the actuator completed this tick.
	// In DryRun (observe-only) mode NOTHING is actuated, so this stays empty —
	// see WouldExecute for the planned set.
	ActionsExecuted []Action
	// ProposalsFiled lists the capital proposals submitted this tick. Empty in
	// DryRun mode (nothing is filed) — see WouldFile for the planned set.
	ProposalsFiled []Proposal
	// WouldExecute lists the approved actions a DryRun tick WOULD have executed
	// (it invoked no actuator verb). Always empty when the engine is armed
	// (DryRun=false) — the two sets are mutually exclusive, so an observer reads
	// "planned but not executed" from here and "executed" from ActionsExecuted.
	WouldExecute []Action
	// WouldFile lists the capital proposals a DryRun tick WOULD have filed (it
	// called ProposalChannel.Submit for none). Always empty when armed.
	WouldFile []Proposal
}

// TickObserver receives every tick's outcome. Optional seam for the
// autoscaler harness and the twin scenarios (st-6wa); production runs without
// one (outcomes are logged).
type TickObserver interface {
	ObserveTick(outcome TickOutcome)
}
