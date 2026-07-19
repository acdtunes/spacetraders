package capacity

import (
	"fmt"
	"time"
)

// Calibration is the engine's resolved calibration parameter set (spec:
// "Calibration params (config, not code)"). It flows from config.yaml's
// [capacity_reconciler] section through the launch config into every phase
// call each tick, so the planner, differ, and governor all read the SAME live
// numbers and none hardcodes an operational value. Zero-valued config fields
// resolve to these documented defaults in the application-layer resolver; a
// resolved Calibration always passes Validate.
//
// RE-SCOPE: the capital tier is now a THIN EMITTER (capex_emitter.go,
// capacity.CapexEmitter) that only accumulates the tier-4 gap and EMITS it as
// contract-delivery demand to the fleet autosizer — it computes NO capex
// budget and mints NO proposals. The fleet autosizer owns the ONE guard stack
// (reserve-floor-net, 25%-treasury, era payback, fleet ceilings, per-tick cap)
// that actually spends. Consequently four budget-math fields below —
// ReserveFloorCredits, SurplusFraction, PerDecisionCapPct, ROIPaybackHorizon — are
// RESERVED / currently-unused by the emitter: still resolved and range-validated at
// launch (harmless), but NO code reads them. DO NOT resurrect them as a second
// money gate — the fleet autosizer's cfg.Reserve is the SINGLE reserve authority,
// and a second reserve floor here would be a latent "two reserve floors" bug. The
// still-live knobs are AddThresholdPerHullCrHr + StockerCapacityBudget (planner),
// TickInterval (loop), and ApprovalThresholdCredits (the CONVERGE + approval-execution
// gate).
type Calibration struct {
	// ReserveFloorCredits — RESERVED / unused by the thin emitter (see type doc):
	// the emitter computes no budget, so nothing reads this. Default 50000,
	// matching the codebase's immutable reserve floor
	// (internal/application/common/reserve_floor.go). DO NOT wire a second
	// reserve gate off this — the fleet autosizer's cfg.Reserve is the ONE
	// reserve authority.
	ReserveFloorCredits int64

	// SurplusFraction — RESERVED / unused by the thin emitter (see type doc): no
	// deployable-budget drain runs here anymore. Default 0.25. The fleet
	// autosizer owns the 25%-treasury drain now.
	SurplusFraction float64

	// PerDecisionCapPct — RESERVED / unused by the thin emitter (see type doc): the
	// emitter caps nothing. Default 25. The fleet autosizer's per-tick cap is the
	// live spend limiter now.
	PerDecisionCapPct int

	// ROIPaybackHorizon — RESERVED / unused by the thin emitter (see type doc): no
	// ROI/payback gate runs here. Default 24h. The fleet autosizer's era-payback
	// guard judges payback now, fed the emitter's MarginalProjectedCrHr evidence.
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
// harness can assert against the same numbers the resolver uses.
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

// CapexBudget was one tick's resolved capital budget (Deployable = SurplusFraction
// × (Treasury − ReserveFloor) floored at 0; PerDecisionCap = Deployable ×
// PerDecisionCapPct/100). RESERVED / currently-unused: the thin emitter
// (capex_emitter.go) computes NO budget and leaves this type zero-valued — all
// money-gating lives in the fleet autosizer. Kept for the audit-trail shape and
// a possible future in-engine governor; nothing populates it today.
type CapexBudget struct {
	TreasuryCredits       int64
	ReserveFloorCredits   int64
	DeployableCredits     int64
	PerDecisionCapCredits int64
}

// CapexDecision is the emitter's audit note on one capital action. The thin
// emitter (capex_emitter.go) produces one per tier-4 action with Approved=false
// and Reason=capexEmitReason ("emitted as contract-delivery capital demand to
// the fleet autosizer …") — the reconciler neither executes nor proposes the
// action; the fleet autosizer's guard stack decides the buy.
type CapexDecision struct {
	Action Action
	// Approved is always false from the thin emitter (capital is emitted to
	// the fleet autosizer, never approved or proposed by the reconciler).
	Approved bool
	// Reason is the emit audit note (capexEmitReason).
	Reason string
	// Budget is left zero-valued by the thin emitter (no capex budget is computed).
	Budget CapexBudget
}

// GovernResult is the GOVERN phase's output. From the production thin emitter
// (capex_emitter.go): Approved carries the cheap tiers (1-3) verbatim; Proposals
// stays EMPTY (the emitter mints none — the tier-4 demand is emitted to the
// fleet autosizer instead); Decisions carries the per-capital-action emit audit
// trail.
type GovernResult struct {
	// Approved is executed this tick by the actuator (cheap tiers pass
	// through; tier-4 appears here only under a future graduated autonomy).
	Approved []Action
	// Proposals is filed on the proposal channel for human approval. The thin
	// emitter leaves this EMPTY — it emits capital demand to the fleet autosizer
	// rather than minting proposals (a future in-engine governor could
	// repopulate it).
	Proposals []Proposal
	// Decisions is the per-capital-action audit trail (emit notes from the emitter).
	Decisions []CapexDecision
}
