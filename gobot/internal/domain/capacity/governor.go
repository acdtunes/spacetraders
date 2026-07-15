package capacity

import (
	"fmt"
	"time"
)

// Calibration is the engine's resolved calibration parameter set (spec:
// "Calibration params (config, not code)"). It flows from config.yaml's
// [capacity_reconciler] section through the launch config into every phase
// call each tick, so the planner (st-hlw), differ (st-zr0), and governor
// (st-x00) all read the SAME live numbers and none hardcodes an operational
// value. Zero-valued config fields resolve to these documented defaults in
// the application-layer resolver; a resolved Calibration always passes
// Validate.
type Calibration struct {
	// ReserveFloorCredits is the HARD treasury floor — the governor never
	// spends below it (protects operating runway). Default 50000, matching
	// the codebase's immutable reserve floor
	// (internal/application/common/reserve_floor.go).
	ReserveFloorCredits int64

	// SurplusFraction is f in the surplus-fraction drain: each cycle the
	// deployable capex budget is f × (treasury − floor). Self-scaling — it
	// auto-throttles near the floor. Default 0.25 (provisional until the
	// governor lane calibrates against prod history).
	SurplusFraction float64

	// PerDecisionCapPct caps a SINGLE capital decision at this percent of the
	// deployable budget. Default 25 (spec-locked).
	PerDecisionCapPct int

	// ROIPaybackHorizon is the window a capital item must pay itself back
	// within to clear the ROI gate. Default 24h (provisional).
	ROIPaybackHorizon time.Duration

	// AddThresholdPerHullCrHr is the per-hull-credits/hr floor a capacity add
	// must keep the fleet above: an add projected to drop fleet per-hull $/hr
	// below this (or below the current value — the absorption ceiling) is
	// refused. Default 0 = no explicit floor until the planner lane
	// calibrates one from prod history; the raise-the-average gate still
	// applies.
	AddThresholdPerHullCrHr float64

	// StockerCapacityBudget is the per-hub stocker-capacity budget (units of
	// buffered volume) the planner selects buffer goods under. Default 0 =
	// planner's own documented default until calibrated.
	StockerCapacityBudget int

	// TickInterval is the reconcile cadence. Default 300s — capacity topology
	// is strategic, not per-second.
	TickInterval time.Duration

	// ApprovalThresholdCredits is the capex-proposal approval threshold: a
	// tier-4 action costing at least this files a proposal instead of
	// auto-executing. Default 0 = EVERY tier-4 action requires approval
	// (tiered autonomy v1; raising it later is how the engine graduates).
	ApprovalThresholdCredits int64
}

// Calibration defaults. Exported so the config surface, the CLI, and the
// harness (st-6wa) can assert against the same numbers the resolver uses.
const (
	// DefaultReserveFloorCredits matches common.ImmutableReserveFloor (the
	// domain layer cannot import application/common; keep in lockstep).
	DefaultReserveFloorCredits int64 = 50000
	DefaultSurplusFraction           = 0.25
	DefaultPerDecisionCapPct         = 25
	DefaultROIPaybackHorizon         = 24 * time.Hour
	DefaultTickInterval              = 300 * time.Second
)

// DefaultCalibration is the engine's documented protective default set.
func DefaultCalibration() Calibration {
	return Calibration{
		ReserveFloorCredits:      DefaultReserveFloorCredits,
		SurplusFraction:          DefaultSurplusFraction,
		PerDecisionCapPct:        DefaultPerDecisionCapPct,
		ROIPaybackHorizon:        DefaultROIPaybackHorizon,
		AddThresholdPerHullCrHr:  0,
		StockerCapacityBudget:    0,
		TickInterval:             DefaultTickInterval,
		ApprovalThresholdCredits: 0,
	}
}

// Validate rejects a calibration that could unbound the governor or wedge the
// loop. The launch path fails LOUD on an invalid explicit value rather than
// silently clamping it.
func (c Calibration) Validate() error {
	if c.ReserveFloorCredits < 0 {
		return fmt.Errorf("reserve_floor must be >= 0, got %d", c.ReserveFloorCredits)
	}
	if c.SurplusFraction < 0 || c.SurplusFraction > 1 {
		return fmt.Errorf("surplus_fraction must be within [0,1], got %v", c.SurplusFraction)
	}
	if c.PerDecisionCapPct < 1 || c.PerDecisionCapPct > 100 {
		return fmt.Errorf("per_decision_cap_pct must be within [1,100], got %d", c.PerDecisionCapPct)
	}
	if c.ROIPaybackHorizon <= 0 {
		return fmt.Errorf("roi_payback_horizon must be > 0, got %v", c.ROIPaybackHorizon)
	}
	if c.AddThresholdPerHullCrHr < 0 {
		return fmt.Errorf("add_threshold_per_hull_cr_hr must be >= 0, got %v", c.AddThresholdPerHullCrHr)
	}
	if c.StockerCapacityBudget < 0 {
		return fmt.Errorf("stocker_capacity_budget must be >= 0, got %d", c.StockerCapacityBudget)
	}
	if c.TickInterval <= 0 {
		return fmt.Errorf("tick_interval must be > 0, got %v", c.TickInterval)
	}
	if c.ApprovalThresholdCredits < 0 {
		return fmt.Errorf("approval_threshold must be >= 0, got %d", c.ApprovalThresholdCredits)
	}
	return nil
}

// CapexBudget is one tick's resolved capital budget (spec: GOVERN). The
// governor lane (st-x00) computes it: Deployable = SurplusFraction ×
// (Treasury − ReserveFloor) floored at 0; PerDecisionCap = Deployable ×
// PerDecisionCapPct/100. Only tier-4 actions consume it; cheap tiers are free.
type CapexBudget struct {
	TreasuryCredits       int64
	ReserveFloorCredits   int64
	DeployableCredits     int64
	PerDecisionCapCredits int64
}

// CapexDecision is the governor's audited verdict on one capital action —
// every gate's arithmetic is preserved so a proposal (or a refusal) carries
// its evidence.
type CapexDecision struct {
	Action   Action
	Approved bool
	// Reason names the gate that blocked (or the arithmetic that cleared),
	// e.g. "cost 120000 > per-decision cap 75000".
	Reason string
	// Budget is the tick's budget this decision was judged against.
	Budget CapexBudget
}

// GovernResult is the GOVERN phase's output: which actions CONVERGE may
// execute now, which capital actions become proposals, and the full decision
// audit trail.
type GovernResult struct {
	// Approved is executed this tick by the actuator (cheap tiers pass
	// through; tier-4 appears here only under a future graduated autonomy).
	Approved []Action
	// Proposals is filed on the proposal channel for human approval.
	Proposals []Proposal
	// Decisions is the per-capital-action audit trail.
	Decisions []CapexDecision
}
