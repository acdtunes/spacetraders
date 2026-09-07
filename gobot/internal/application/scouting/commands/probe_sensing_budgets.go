package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// probe_sensing_budgets.go resolves one tick's per-pass BURST budgets from the measured API
// saturation, and names them for the heartbeat. The engine's constants are sized for a
// fleet at its request ceiling; this is where the headroom below it becomes throughput
// (parkedsensing.PacedBudget), in ONE place, so no pass can drift into its own idea of how
// loaded the fleet is.

// APISaturationReader is how this coordinator reads live request-budget pressure, in
// permille of a fully committed budget. Structurally identical to the tour coordinator's
// port and satisfied by the same adapter; declared here rather than imported so a sensing
// loop does not reach into the trading application package for one method.
type APISaturationReader interface {
	SaturationPermille(ctx context.Context) int
}

// sensingBudgets is one tick's resolved burst budget for every paced pass, plus the
// saturation reading they were all derived from.
type sensingBudgets struct {
	// permille is the reading, carried for the heartbeat so an operator can see the
	// input beside the budgets it produced.
	permille int
	gate     int
	expand   int
	place    int
	yards    int
	presence int
	reap     int
}

// resolveSensingBudgets scales every paced pass off one saturation reading.
//
// THE ZERO READING IS FULL HEADROOM HERE, WHICH IS THE OPPOSITE OF THE TOUR OBJECTIVE'S
// FAIL-OPEN, and the inversion is deliberate rather than accidental. The estimator
// collapses "nobody queued" and "no opinion" into the same 0 (trading.SaturationPermille),
// and the tour reads that as "do not price the request budget at all". This consumer's
// question is the mirror of it — how much budget is going spare — so 0 means spare, and an
// unwired or silent estimator gives the engine its full headroom rather than pacing it as
// though the limiter were saturated. Nothing here spends credits, so there is no guard to
// fail closed: the worst case is a burst of reads against a limiter that queues them,
// which the limiter itself already handles.
func resolveSensingBudgets(permille, headroomMultiple int) sensingBudgets {
	paced := func(base int) int { return parkedsensing.PacedBudget(base, permille, headroomMultiple) }
	return sensingBudgets{
		permille: permille,
		gate:     paced(parkedsensing.MaxGateReads),
		expand:   paced(parkedsensing.MaxExpansionActions),
		place:    paced(parkedsensing.DefaultMaxPlacementActions),
		yards:    paced(parkedsensing.MaxYardCatalogReads),
		presence: paced(parkedsensing.MaxYardPresenceDispatches),
		reap:     paced(parkedsensing.DefaultMaxReaps),
	}
}

// saturationPermille takes this tick's reading. A nil reader is a WIRING GAP that reads as
// a fully idle budget — see resolveSensingBudgets for why that is the safe direction here.
func (h *RunProbeSensingCoordinatorHandler) saturationPermille(ctx context.Context) int {
	if h.apiSaturation == nil {
		return 0
	}
	return h.apiSaturation.SaturationPermille(ctx)
}

// budgetsFor resolves this tick's budgets for one player from the live reading and the
// operator's headroom multiple.
func (h *RunProbeSensingCoordinatorHandler) budgetsFor(ctx context.Context, cfg sensingConfig) sensingBudgets {
	return resolveSensingBudgets(h.saturationPermille(ctx), cfg.ExpansionHeadroomMultiple)
}
