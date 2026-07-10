package commands

// run_trade_route_coordinator_guards.go — pre-buy money/stale guards: the working-capital spend floor and the stale-ask abort (sp-wads move-only split).

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// spendFloorBreached live-checks whether spending projectedCost right now would
// drop treasury below reserve (sp-bp6f), setting the abort fields on response
// and returning true if so — the caller must not proceed with the buy.
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
	if h.apiClient == nil {
		return false
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Could not resolve player token for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", "Could not read live treasury for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if agentData.Credits-projectedCost < reserve {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting circuit before spending", map[string]interface{}{
			"treasury": agentData.Credits, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = agentData.Credits
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
