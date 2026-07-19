package commands

// run_tour_coordinator_placement.go — sp-z7ng (epic sp-fguo, Layer-B): the armed placement/
// relocation scoring loop. It EVOLVES the sp-zhii margins-death reposition into the spec's
// score(x)=E_x−β·D_x argmax (SENSE→PLAN→DIFF; CONVERGE reuses the existing jump machinery), dormant
// behind PlacementScoreEnabled. DEFAULT-OFF: when unarmed the dispatch in maybeReposition is never
// taken and the legacy static-floor engine runs byte-identically. When armed but β is unreadable
// (no telemetry), this returns handled=false so maybeReposition falls THROUGH to the untouched
// legacy body — a margins-dead hull is still rescuable on a fresh boot with empty telemetry.
//
// Same-budget rule (no thundering herd): armed mode prices top-(N−1) foreign candidates + 1
// current-system E_s = N planner calls per episode, identical to legacy's K. Arming therefore
// cannot grow the solver load on a fleet-wide margins collapse.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/placement"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// placementBetaWindowMinutesDefault is the trailing window for the fleet rolling-median realized
	// tour $/hr (β) when the captain leaves placement_beta_window_minutes at 0 (spec: 60 min).
	placementBetaWindowMinutesDefault = 60

	// placementParkFloorPctDefault is φ×100 — a candidate's deadhead-charged score must clear φ·β to
	// be worth the jump, when placement_park_floor_pct is 0. 30 ⇒ φ=0.3 (spec line 70).
	placementParkFloorPctDefault = 30
)

// resolvePlacementBetaWindowMinutes applies the 0/absent → 60 rule (RULINGS #5), so the β window
// default lives in ONE place, mirroring resolveRepositionJumpBound.
func resolvePlacementBetaWindowMinutes(configured int) int {
	if configured <= 0 {
		return placementBetaWindowMinutesDefault
	}
	return configured
}

// resolvePlacementParkFloorPct applies the 0/absent → 30 rule (φ=0.3), the spec park-floor default.
func resolvePlacementParkFloorPct(configured int) int {
	if configured <= 0 {
		return placementParkFloorPctDefault
	}
	return configured
}

// resolvePlacementShortlist resolves the same-budget N: an explicit placement_shortlist_top_n wins;
// 0 defers to the resolved RepositionMaxCandidates (legacy's K, default 3), so arming prices exactly
// the legacy budget (top-(N−1) foreign + E_s = N = K). A captain who wants the full N foreign
// candidates PLUS E_s sets placement_shortlist_top_n = K+1 explicitly.
func resolvePlacementShortlist(configured, repositionMaxCandidates int) int {
	if configured > 0 {
		return configured
	}
	if repositionMaxCandidates > 0 {
		return repositionMaxCandidates
	}
	return repositionMaxCandidatesDefault
}

// maybeRepositionPlacement is the armed placement engine (sp-z7ng). It returns a tri-state:
//   - (handled=false, _, nil): β is unreadable — the caller falls THROUGH to the legacy engine.
//   - (handled=true, repositioned=false, nil): a Stay or a park-floor Hold — no jump, the caller
//     exits honestly (today's starvation flow).
//   - (handled=true, repositioned=true, nil): the hull jumped to the score argmax.
//   - (handled=true, false, err): an OPERATIONAL travel/load failure (resumable; the persisted
//     destination is left set so a restart resumes the jump — identical to the legacy contract).
func (h *RunTourCoordinatorHandler) maybeRepositionPlacement(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	netBought map[string]int,
	maxHops int,
	maxSpend, reserve int64,
	modelVersion string,
) (handled bool, repositioned bool, err error) {
	logger := common.LoggerFromContext(ctx)

	// SENSE: β = fleet rolling-median realized tour $/hr over the trailing window. Unreadable β
	// (nil telemetry, read error, or no computable tour) → fall through to the legacy engine.
	beta, betaReadable := h.senseBeta(ctx, cmd)
	if !betaReadable {
		logger.Log("INFO", fmt.Sprintf("Placement: β (fleet-median tour $/hr) unreadable for %s - falling back to the legacy static-floor reposition for this episode (fresh-boot rescue preserved)", cmd.ShipSymbol), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "bead": "sp-z7ng",
		})
		metrics.RecordTourPlacementDecision(cmd.PlayerID, "fallback_legacy")
		return false, false, nil
	}

	ship, serr := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if serr != nil {
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return true, false, serr
	}
	currentSystem := ship.CurrentLocation().SystemSymbol
	currentWaypoint := ship.CurrentLocation().Symbol

	// Candidate discovery via the EXISTING seam: 1-hop scan (+ sp-jeou multi-hop broadening) with
	// the 75-min freshListings staleness gate and the cappedSpread prerank shortlist baked in.
	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)

	// PLAN (same-budget rule): top-(N−1) foreign candidates by prerank + 1 current-system E_s.
	shortlist := resolvePlacementShortlist(cmd.PlacementShortlistTopN, cmd.RepositionMaxCandidates)
	foreignBudget := shortlist - 1
	if foreignBudget < 0 {
		foreignBudget = 0
	}
	evals := make([]placement.Evaluation, 0, shortlist)
	for index, candidate := range candidates {
		if index >= foreignBudget {
			break
		}
		evals = append(evals, h.evaluateForeignPlacement(ctx, ship, candidate, beta, maxHops, maxSpend, reserve, cmd, modelVersion))
	}
	// E_s: the current system as a clean-hold STAY option (Hops=0, D=0), commensurable with the
	// candidates' clean-hold E_x. Reusing the just-failed 3-strike (laden) plan would bias E_s low
	// and systematically over-trigger jumps, so it gets its own clean pre-flight.
	evals = append(evals, h.evaluateStayPlacement(ctx, ship, currentSystem, currentWaypoint, beta, maxHops, maxSpend, reserve, cmd, modelVersion))

	// DIFF: argmax score(x)=E_x−β·D_x, park floor φ·β.
	phi := float64(resolvePlacementParkFloorPct(cmd.PlacementParkFloorPct)) / 100
	decision := placement.Decide(evals, beta, phi)
	logPlacementDecision(logger, cmd.ShipSymbol, currentSystem, decision)

	if decision.Hold {
		metrics.RecordTourPlacementDecision(cmd.PlayerID, "hold_park_floor")
		return true, false, nil
	}
	if decision.Stay || decision.Winner == nil {
		metrics.RecordTourPlacementDecision(cmd.PlayerID, "stay")
		return true, false, nil
	}

	// CONVERGE: the winner is a foreign ground worth the jump — reuse the existing machinery.
	return h.convergePlacementJump(ctx, cmd, response, episode, netBought, currentSystem, decision.Winner, maxSpend, reserve)
}

// senseBeta reads the fleet rolling-median realized tour $/hr over the trailing window via the
// existing telemetry seam (ListByPlayer with the window as its since bound) + trading.MedianTourRate.
// Readable=false (nil repo, read error, no computable tour, or a non-positive median) ⇒ the caller
// falls back to the legacy engine — β is never invented (fail-closed, mirroring MedianTourRate).
//
// sp-461l (epic sp-g9td) cash-true audit: β STAYS on telemetry — it is a per-TOUR median that must be
// dimensionally commensurable with the per-candidate PROJECTED E_x (ProjectedCreditsPerHour) the score
// function subtracts it from, and the transactions ledger has no ship/tour column to reproduce it.
// sp-rd21's write-path fix (dropped buy legs now recorded) is what makes this honest: MedianTourRate
// now nets the once-missing buys, so β is the true (not ~2x-inflated) rate. The 60-min window is
// always fresh post-deploy, so the netting reconciles 1.00x.
func (h *RunTourCoordinatorHandler) senseBeta(ctx context.Context, cmd *RunTourCoordinatorCommand) (float64, bool) {
	if h.telemetry == nil {
		return 0, false
	}
	window := time.Duration(resolvePlacementBetaWindowMinutes(cmd.PlacementBetaWindowMinutes)) * time.Minute
	since := h.clock.Now().Add(-window)
	rows, err := h.telemetry.ListByPlayer(ctx, cmd.PlayerID, since)
	if err != nil {
		return 0, false
	}
	beta, ok := trading.MedianTourRate(rows)
	if !ok || beta <= 0 {
		return 0, false
	}
	return beta, true
}

// evaluateForeignPlacement scores one foreign candidate: hop-count-aware deadhead
// D_x=(hops·crossSystemHopSeconds + repositionReplanAllowanceSeconds)/3600, E_x from the clean-hold
// pre-flight, score=E_x−β·D_x. A hops<=0 sentinel from any future discovery path that forgot to
// stamp is defensively charged as 1 hop (never free).
func (h *RunTourCoordinatorHandler) evaluateForeignPlacement(
	ctx context.Context,
	ship *navigation.Ship,
	candidate repositionCandidate,
	beta float64,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) placement.Evaluation {
	hops := candidate.hops
	if hops <= 0 {
		hops = 1 // defensive: an unstamped candidate is charged one hop, never a free deadhead
	}
	deadheadHours := (float64(hops)*crossSystemHopSeconds + repositionReplanAllowanceSeconds) / 3600
	return h.scorePlacementCandidate(ctx, ship, candidate, hops, deadheadHours, beta, maxHops, maxSpend, reserve, cmd, modelVersion)
}

// evaluateStayPlacement scores the current system as the STAY option (Hops=0, D=0 — the D_s=0
// identity), a clean-hold pre-flight commensurable with the foreign candidates' E_x.
func (h *RunTourCoordinatorHandler) evaluateStayPlacement(
	ctx context.Context,
	ship *navigation.Ship,
	currentSystem, currentWaypoint string,
	beta float64,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) placement.Evaluation {
	stay := repositionCandidate{system: currentSystem, waypoint: currentWaypoint, hops: 0}
	return h.scorePlacementCandidate(ctx, ship, stay, 0, 0, beta, maxHops, maxSpend, reserve, cmd, modelVersion)
}

// scorePlacementCandidate runs the E_x pre-flight (the spec's PlacementValue for v1 — peak $/hr via
// the existing planAtCandidate clean-hold pre-flight) and composes score(x)=E_x−β·D_x. An infeasible
// pre-flight yields a non-feasible Evaluation carrying the planner's own rejection reason (which
// never wins the argmax).
func (h *RunTourCoordinatorHandler) scorePlacementCandidate(
	ctx context.Context,
	ship *navigation.Ship,
	candidate repositionCandidate,
	hops int,
	deadheadHours, beta float64,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) placement.Evaluation {
	evaluation := placement.Evaluation{System: candidate.system, Waypoint: candidate.waypoint, Hops: hops, DeadheadHours: deadheadHours}
	plan, perr := h.planAtCandidate(ctx, ship, candidate, maxHops, maxSpend, reserve, cmd, modelVersion)
	if perr == nil && plan != nil && plan.Feasible {
		evaluation.EX = plan.ProjectedCreditsPerHour
		evaluation.Feasible = true
		evaluation.Score = placement.Score(evaluation.EX, beta, deadheadHours)
		return evaluation
	}
	evaluation.Reason = repositionCandidateReason(plan, perr)
	return evaluation
}

// convergePlacementJump commits the reposition to the score argmax through the EXISTING jump
// machinery, verbatim order (RULINGS #2 persist-before-jump → look-back load → bounded resolver →
// clear persisted flag → metrics → episode budget). A travel error leaves the persisted destination
// SET so a restart resumes the jump — identical to the legacy contract.
func (h *RunTourCoordinatorHandler) convergePlacementJump(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	netBought map[string]int,
	currentSystem string,
	winner *placement.Evaluation,
	maxSpend, reserve int64,
) (bool, bool, error) {
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: winner.System, TargetWaypoint: winner.Waypoint})
	loadedUnits := h.loadLookbackManifest(ctx, cmd, response, netBought, currentSystem, winner.System, maxSpend, reserve)
	jumpBound := resolveRepositionJumpBound(cmd.RepositionJumpBound)
	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, winner.Waypoint, cmd.PlayerID, jumpBound); terr != nil {
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return true, false, fmt.Errorf("placement reposition jump of %s to %s failed: %w", cmd.ShipSymbol, winner.Waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	metrics.RecordTourJumpLoaded(cmd.PlayerID, loadedUnits > 0)
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	metrics.RecordTourPlacementDecision(cmd.PlayerID, "jump")
	episode.repositioned = true
	episode.fromSystem = currentSystem
	episode.toSystem = winner.System
	response.Repositions++
	return true, true, nil
}

// logPlacementDecision emits the greppable one-line decision record (message text, which
// `container logs` keeps — the sp-149h/sp-iqyq renderer defect). It reports the RAW quantities
// SEPARATELY per candidate (E_x, hops, D_x, composed score) plus β and the φ·β park floor, so the
// Tier-0 dimensional heterogeneity (projected-fresh E_x vs realized-net β) is visible to calibration
// (sp-z7ng units finding), then the verdict.
func logPlacementDecision(logger common.ContainerLogger, shipSymbol, fromSystem string, decision placement.Decision) {
	parts := make([]string, 0, len(decision.Evaluations))
	for _, evaluation := range decision.Evaluations {
		if evaluation.Feasible {
			parts = append(parts, fmt.Sprintf("%s(hops=%d,ex=%.0f,d=%.3fh,score=%.0f)", evaluation.System, evaluation.Hops, evaluation.EX, evaluation.DeadheadHours, evaluation.Score))
			continue
		}
		reason := evaluation.Reason
		if reason == "" {
			reason = "infeasible"
		}
		parts = append(parts, fmt.Sprintf("%s(hops=%d,%s)", evaluation.System, evaluation.Hops, reason))
	}
	verdict := placementVerdictText(decision)
	logger.Log("INFO", fmt.Sprintf("Placement decision from %s [%s] - beta=%.0f/hr, park-floor phi*beta=%.0f, verdict %s", fromSystem, strings.Join(parts, ", "), decision.Beta, decision.ParkFloor, verdict), map[string]interface{}{
		"ship_symbol": shipSymbol, "from_system": fromSystem, "beta": decision.Beta, "park_floor": decision.ParkFloor, "bead": "sp-z7ng",
	})
}

// placementVerdictText renders the decision's verdict for the log line.
func placementVerdictText(decision placement.Decision) string {
	switch {
	case decision.Hold:
		return "hold_park_floor (" + decision.HoldReason + ")"
	case decision.Stay && decision.Winner != nil:
		return fmt.Sprintf("stay %s (score %.0f)", decision.Winner.System, decision.Winner.Score)
	case decision.Winner != nil:
		return fmt.Sprintf("jump %s (score %.0f)", decision.Winner.System, decision.Winner.Score)
	default:
		return "stay (no feasible candidate)"
	}
}
