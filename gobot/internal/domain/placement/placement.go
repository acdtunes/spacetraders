// Package placement is the pure, handler-free core of the Layer-B placement/relocation
// loop (spec: docs/superpowers/specs/2026-07-16-longer-trade-tours-and-placement-engine-design.md,
// score(x)=E_x-β·D_x). It mirrors the Capacity Reconciler's phase shape (domain/capacity/ports.go)
// at maybeReposition scale: a SENSE→PLAN→DIFF decision record over candidate systems, argmax on the
// deadhead-charged score, with a park floor that HOLDS a hull rather than chasing a marginal jump.
// CONVERGE is deliberately NOT here — the actual jump reuses the existing tour-reposition machinery.
//
// Pure and dependency-free: the handler assembles Evaluations (reading E_x from the planner and β
// from telemetry) and asks Decide/Score for the verdict, so every argmax / park-floor / stay case is
// unit-tested in isolation, with zero knowledge of ports, the clock, or the solver.
package placement

import "fmt"

// Phase names one placement-loop phase, in execution order. CONVERGE is intentionally absent — the
// jump is the existing tour-reposition machinery (persist-before-jump, the bounded stored-adjacency
// resolver), reused not rebuilt, so this package models only the decision.
type Phase string

const (
	PhaseSense Phase = "SENSE" // read β (fleet median realized $/hr) + discover candidates
	PhasePlan  Phase = "PLAN"  // price E_x per candidate (+ the current-system E_s pre-flight)
	PhaseDiff  Phase = "DIFF"  // argmax score(x)=E_x-β·D_x, park-floor hold, stay-vs-jump
)

// Evaluation is one scored candidate system (a foreign reposition target, or the current system as
// the stay option). The current system carries Hops=0 / DeadheadHours=0 — the D_s=0 identity that
// lets "stay" compete on equal footing with a charged jump. Feasible=false marks a candidate the
// planner declined (Reason carries why); an infeasible candidate never wins.
type Evaluation struct {
	System        string
	Waypoint      string
	EX            float64 // projected tour $/hr at this candidate (TourPlan.ProjectedCreditsPerHour)
	Hops          int     // gate-hops from the current system (0 for the current-system stay option)
	DeadheadHours float64 // (hops·crossSystemHopSeconds + replanAllowance)/3600 — the D_x charge basis
	Score         float64 // Score(EX, β, DeadheadHours) — set by the caller for feasible evals
	Feasible      bool
	Reason        string // why an infeasible candidate was rejected (planner reason / "planner-error")
}

// Decision is the phase-tagged DIFF record: the β and park floor the choice was made against, every
// candidate Evaluation (for the greppable decision log), and the verdict — a Winner to jump to, a
// Stay on the current ground, or a Hold below the park floor. Winner is nil for Hold and for an
// unreadable β. BetaReadable=false means Decide was handed a non-positive β and refused to invent
// one — the caller then falls back to the legacy static-floor engine (fresh-boot rescue preserved).
type Decision struct {
	Beta         float64
	BetaReadable bool
	ParkFloor    float64 // φ·β — the deadhead-charged score a jump must clear to be worth the antimatter
	Evaluations  []Evaluation
	Winner       *Evaluation
	Stay         bool // the current-system eval won the argmax (Hops=0) — hold this ground, no jump
	Hold         bool // no feasible candidate cleared the park floor — park below φ·β
	HoldReason   string
	FailedPhase  Phase
	Error        string
}

// Score is the Tier-0 linear deadhead charge: the projected candidate rate E_x minus β dollars per
// deadhead hour spent NOT trading (the one-way jump + the post-jump re-plan allowance). D=0 ⇒
// score == E_x (the current-system D_s=0 identity).
//
// Two dimensional caveats are carried here deliberately, because the
// decision log emits the raw quantities separately so calibration can see both:
//   - Tier-0 charges an implicit ~1h horizon: $/hr minus (β·hours)=$ is commensurable only under a
//     ~1-hour residency assumption; Tier-2 (∫rate_x(t)dt with absorption-depth residency) removes it.
//   - E_x is a PROJECTED-FRESH candidate rate while β is a REALIZED-NET fleet median — a HOT fleet
//     (high realized β) therefore holds more and a COLD fleet jumps more; every input is logged so
//     the φ and β window can be retuned on evidence rather than guesswork.
func Score(ex, beta, deadheadHours float64) float64 {
	return ex - beta*deadheadHours
}

// Decide is the DIFF phase: argmax score over the FEASIBLE evaluations INCLUDING the current system
// (Hops=0/D=0), with a park floor of φ·β. It returns Hold when the best score is below the floor (no
// ground — including staying — is worth acting on), Stay when the winning eval is the current system,
// and otherwise a foreign Winner to jump to. It NEVER invents a β: a non-positive β is reported as
// BetaReadable=false with no verdict, so the caller falls back to the legacy engine rather than
// deciding off a fabricated rate (fail-closed, mirroring MedianTourRate's ok=false).
func Decide(evals []Evaluation, beta, phi float64) Decision {
	decision := Decision{Beta: beta, ParkFloor: phi * beta, Evaluations: evals, FailedPhase: PhaseDiff}
	if beta <= 0 {
		decision.BetaReadable = false // never invent a β; the caller falls back to legacy
		decision.Error = "beta unreadable"
		return decision
	}
	decision.BetaReadable = true
	decision.FailedPhase = ""

	winner := argmaxFeasible(evals)
	if winner == nil {
		decision.Hold = true
		decision.HoldReason = "no feasible candidate to place onto"
		return decision
	}
	if winner.Score < decision.ParkFloor {
		decision.Hold = true
		decision.HoldReason = fmt.Sprintf("best score %.0f below park floor φ·β = %.2f·%.0f = %.0f", winner.Score, phi, beta, decision.ParkFloor)
		return decision
	}
	decision.Winner = winner
	if winner.Hops == 0 {
		decision.Stay = true // the current-system eval won — hold this ground rather than jump
	}
	return decision
}

// argmaxFeasible returns a pointer to the highest-Score feasible evaluation (nil when none is
// feasible). Ties keep the first seen — deterministic given the caller's stable candidate order.
func argmaxFeasible(evals []Evaluation) *Evaluation {
	var winner *Evaluation
	for i := range evals {
		if !evals[i].Feasible {
			continue
		}
		if winner == nil || evals[i].Score > winner.Score {
			winner = &evals[i]
		}
	}
	return winner
}

// PlacementValue is the spec seam for E_x (spec §Layer-B): the projected sustained value of placing
// a hull at a system. v1 wires the peak-$/hr impl (TourPlan.ProjectedCreditsPerHour) directly in the
// handler; the Tier-2 sustained model (∫rate_x(t)dt with absorption-depth residency) swaps in behind
// this same interface without touching Decide. feasible=false ⇒ the candidate is not placeable
// (reason carries why); exDollarsPerHour is meaningful only when feasible.
type PlacementValue interface {
	Value(system, waypoint string) (exDollarsPerHour float64, feasible bool, reason string)
}
