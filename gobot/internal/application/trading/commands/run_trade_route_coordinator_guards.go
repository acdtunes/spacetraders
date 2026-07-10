package commands

// run_trade_route_coordinator_guards.go — pre-buy money/stale guards: the working-capital spend floor and the stale-ask abort (sp-wads move-only split).

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// reserveHeadroom performs the single live-treasury read that BOTH working-capital
// money guards share (sp-agzj): the circuit's spendFloorBreached (abort-on-breach)
// and the tour's buy-time tranche shrink (executeBuy). It reports how many credits
// may be spent right now while keeping live treasury at or above reserve, so the two
// call sites make the same live read against the same fail-open/fail-closed contract
// rather than each rolling its own.
//
// Three outcomes, mirroring the original spendFloorBreached optional-port contract:
//   - available=false                 → no apiClient wired (guard disabled, the
//     contract most tests rely on); the caller proceeds UNCONSTRAINED.
//   - available=true, readable=false  → a client IS wired but the live read FAILED
//     (unresolvable token, or GetAgent itself erroring); the caller MUST fail CLOSED
//     — never spend on a balance it could not read (RULINGS #4). The read-failure
//     cause is logged here; the caller logs the action it took.
//   - available=true, readable=true   → headroom = liveBalance - reserve (may be
//     <= 0), and liveBalance is the live treasury the decision was made against.
func (h *RunTradeRouteCoordinatorHandler) reserveHeadroom(ctx context.Context, reserve int) (headroom, liveBalance int, available, readable bool) {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return 0, 0, false, false
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Could not resolve player token for working-capital spend-floor check (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		return 0, 0, true, false
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", "Could not read live treasury for working-capital spend-floor check (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		return 0, 0, true, false
	}

	return agentData.Credits - reserve, agentData.Credits, true, true
}

// spendFloorBreached live-checks whether spending projectedCost right now would
// drop treasury below reserve (sp-bp6f), setting the abort fields on response
// and returning true if so — the caller must not proceed with the buy. It is the
// circuit's abort-on-breach reading of the shared reserveHeadroom seam (sp-agzj).
//
// Fails OPEN when no apiClient is wired (h.apiClient == nil): the guard is
// simply unavailable, the same optional-port contract staleAskAborts already
// uses for marketRefresher, so callers that cannot supply a live API client
// (most tests) run without it rather than being forced to fake one.
//
// Fails CLOSED on every live-read failure (an unresolvable player token, or the
// GetAgent call itself erroring): unlike staleAskAborts' infrastructure-gap
// tolerance, a guard whose entire job is stopping the treasury from going
// negative must never let a buy through just because it went blind. An API
// hiccup here aborts the circuit instead of silently trading past the floor.
func (h *RunTradeRouteCoordinatorHandler) spendFloorBreached(
	ctx context.Context,
	projectedCost int,
	reserve int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	headroom, liveBalance, available, readable := h.reserveHeadroom(ctx, reserve)
	if !available {
		return false // no apiClient — guard unavailable, fail OPEN
	}
	if !readable {
		// reserveHeadroom already logged the read-failure cause; record the
		// circuit-level decision (a guard that went blind never trades past the floor).
		logger.Log("WARNING", "Working-capital spend-floor check went blind - aborting circuit (fail-closed)", nil)
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if headroom < projectedCost {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting circuit before spending", map[string]interface{}{
			"treasury": liveBalance, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = liveBalance
		response.ReserveFloor = reserve
		return true
	}

	return false
}

// staleAskAborts live-verifies the source ask before the first buy and reports
// whether the circuit must abort because the ask has moved beyond
// trading.StaleAskMovePercent from the basis the lane was ranked on (hazard b). The
// lane is ranked from a cache that can be many minutes stale; executing on a moved
// basis has realised large losses. It refreshes the source market from the API (the
// hull is docked there, so the API returns live prices), re-reads the ask, and
// compares it to lane.SourceAsk.
//
// Fail-open on infrastructure gaps, fail-closed only on a CONFIRMED move: with no
// refresher wired, or when the refresh/read itself fails, it proceeds on the ranked
// basis (a transient scan hiccup must not strand an otherwise-good circuit). Only a
// live ask that is actually present AND beyond tolerance aborts the run.
func (h *RunTradeRouteCoordinatorHandler) staleAskAborts(
	ctx context.Context,
	lane trading.ArbitrageLane,
	playerID int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.marketRefresher == nil {
		return false
	}

	if err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID), lane.SourceWaypoint); err != nil {
		logger.Log("WARNING", "Could not refresh source market to live-verify basis - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveSrc, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
	if err != nil {
		logger.Log("WARNING", "Could not read live source ask after refresh - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveAsk := liveSrc.SellPrice()
	if trading.AskMovedBeyondTolerance(liveAsk, lane.SourceAsk) {
		response.StaleAskAbort = true
		response.RankedSourceAsk = lane.SourceAsk
		response.LiveSourceAsk = liveAsk
		logger.Log("WARNING", "Source ask moved beyond tolerance since the lane was ranked - aborting circuit before first buy", map[string]interface{}{
			"good": lane.Good, "source": lane.SourceWaypoint,
			"ranked_ask": lane.SourceAsk, "live_ask": liveAsk, "tolerance_pct": trading.StaleAskMovePercent,
		})
		return true
	}

	logger.Log("INFO", "Live-verified source ask within tolerance - proceeding with the circuit", map[string]interface{}{
		"good": lane.Good, "source": lane.SourceWaypoint, "ranked_ask": lane.SourceAsk, "live_ask": liveAsk,
	})
	return false
}
