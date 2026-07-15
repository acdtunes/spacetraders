package capacity

import "time"

// Proposal is one capital action awaiting (or past) human approval — the
// tiered-autonomy gate's artifact (spec: the capex tier emits a proposal —
// bead + captain nudge — carrying the ROI evidence; on approval it executes
// via the same actuator primitives). The proposal-channel lane (st-0h8) owns
// filing and the approval-execution path; the governor lane (st-x00) mints
// these.
type Proposal struct {
	// ID uniquely names the proposal (bead-friendly; stable across re-files
	// of the same gap so an unapproved proposal is not duplicated every tick).
	ID string
	// PlayerID is the owning player. The governor MAY leave it ZERO — its
	// Govern inputs carry no player identity — and the reconcile loop stamps
	// the reconciling player's ID before ProposalChannel.Submit; a non-zero
	// value passes verbatim. Submit therefore always sees a real player.
	PlayerID int
	// Action is the tier-4 action verbatim — approval executes EXACTLY this
	// via Actuator.ExecuteCapital.
	Action    Action
	Evidence  ROIEvidence
	Status    ProposalStatus
	CreatedAt time.Time
}

// ROIEvidence is the arithmetic a proposal carries so the approver judges
// from evidence, not vibes.
//
// The governor derives the gain rate WITHOUT division, from its Govern
// inputs alone: ProjectedGainPerHour = after×(n+d) − before×n, where
// after = Action.ProjectedPerHullCrHr, before =
// EconomicsSignals.FleetPerHullCrHr, n = EconomicsSignals.FleetHullCount
// (st-7ee fills), d = Action.HullDelta (st-zr0 fills).
//
// Cold-start convention (bootstrapper INCOME reuse, 0-2 hulls): when
// FleetHullCount == 0 or the derived gain is non-positive, the payback is
// UNDEFINED — such an action is PROPOSAL-ONLY (never auto-approved, whatever
// the threshold) and the Narrative says why.
type ROIEvidence struct {
	CostCredits int64
	// ProjectedGainPerHour is the credits/hr the action is projected to add.
	ProjectedGainPerHour float64
	// PaybackHorizon is the gate horizon the item must pay back within.
	PaybackHorizon time.Duration
	// ProjectedPaybackHours is cost ÷ gain.
	ProjectedPaybackHours float64
	// FleetPerHullCrHrBefore/After bracket the north-star metric — the add
	// must RAISE it (the absorption ceiling).
	FleetPerHullCrHrBefore float64
	FleetPerHullCrHrAfter  float64
	// Narrative is the human-readable evidence summary for the bead/nudge.
	Narrative string
}

// ProposalStatus is a proposal's lifecycle state.
type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalApproved ProposalStatus = "approved"
	ProposalRejected ProposalStatus = "rejected"
	ProposalExecuted ProposalStatus = "executed"
	ProposalExpired  ProposalStatus = "expired"
)
