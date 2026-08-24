package commands

// Spawn dispersal. Hulls of a class are bought at one yard, so they all begin touring from
// the same system and drain its local margins between them. The starvation streak treats a
// no-plan verdict as possibly transient and breathes — a sound default, because a lone
// hull's ground really does cycle rich and tapped — but on a system a stack of trade hulls
// is already working, re-pricing it is re-asking a question the stack has answered.
//
// This nudge changes only WHEN the existing rescue is reached: a no-plan verdict on a
// crowded ground goes straight to the reposition discovery that margins-death would have
// reached three plans later. It commits nothing of its own — the same kill switch, the same
// one-move-per-episode budget, the same anti-herd exclusion and the same relocation floor
// decide whether the hull actually moves, and a rescue that declines leaves the streak
// running exactly as before.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// TuneKeySpawnDispersalMinOtherHulls is how many OTHER trade hulls must share a hull's
// system before a no-plan verdict there skips the breathing retries: `spacetraders tune
// --operation tour spawn_dispersal_min_other_hulls N`.
const TuneKeySpawnDispersalMinOtherHulls = "spawn_dispersal_min_other_hulls"

// defaultSpawnDispersalMinOtherHulls is the documented default of
// TuneKeySpawnDispersalMinOtherHulls. It sits above the population a system merely shared in
// passing carries — that ground still deserves its breathing retries — and below a yard
// stack, which must be broken up before every hull in it has burnt a full streak on the
// same dead market.
const defaultSpawnDispersalMinOtherHulls = 3

// spawnDispersalMinOtherHulls resolves the live crowd threshold, falling back to the
// documented default whenever the tune surface has nothing positive to say.
func (h *RunTourCoordinatorHandler) spawnDispersalMinOtherHulls(ctx context.Context, playerID int) int {
	if tuned, ok := h.freshness.TunedInt(ctx, playerID, TuneKeySpawnDispersalMinOtherHulls); ok {
		return tuned
	}
	return defaultSpawnDispersalMinOtherHulls
}

// maybeDisperseFromCrowdedGround runs the margins-death reposition EARLY when this hull
// found no plan on a ground it shares with a stack of trade hulls, and reports whether the
// hull moved.
//
// Scoped to a CONTINUOUS run with an infeasible verdict: a finite run flies the tours it was
// asked for, and a feasible plan that flew zero trades is an execution problem, which moving
// does not fix. Every uncertainty answers "not crowded" — an unreadable hull or fleet is not
// evidence of a stack, and the cost of guessing wrong is a jump nothing asked for.
func (h *RunTourCoordinatorHandler) maybeDisperseFromCrowdedGround(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	netBought map[string]int,
	budget tourPlanBudget,
	continuous, feasible bool,
) (bool, error) {
	if !continuous || feasible || cmd.RepositionDisabled || episode.repositioned || episode.dispersalTried {
		return false, nil
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return false, nil
	}
	currentSystem := ship.CurrentLocation().SystemSymbol
	counts, ok := h.tradeHullsBySystem(ctx, cmd.PlayerID, cmd.ShipSymbol)
	if !ok {
		return false, nil
	}
	others, threshold := counts[currentSystem], h.spawnDispersalMinOtherHulls(ctx, cmd.PlayerID)
	if others < threshold {
		return false, nil
	}
	episode.dispersalTried = true
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Spawn dispersal: %s found no plan at %s and shares it with %d other trade hulls (threshold %d) - going straight to reposition discovery instead of re-pricing a ground the stack has already drained", cmd.ShipSymbol, currentSystem, others, threshold), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "current_system": currentSystem,
		"other_trade_hulls": others, "threshold": threshold, "trigger": "spawn_dispersal",
	})
	return h.maybeReposition(ctx, cmd, response, episode, netBought, budget)
}
