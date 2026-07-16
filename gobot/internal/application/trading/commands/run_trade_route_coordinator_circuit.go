package commands

// run_trade_route_coordinator_circuit.go — circuit execution: the disciplined visit/tranche loop and the finish-current-leg liquidation engine (sp-wads move-only split).

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// exitCause names why the visit loop stopped, for the finish-current-leg and
// stranded records: the failed leg's verbatim cause when one aborted, otherwise
// the orderly-exit shape (so a margin-death exit with carryover aboard still
// reports something meaningful).
func exitCause(response *RunTradeRouteCoordinatorResponse) string {
	if response.AbortReason != "" {
		return response.AbortReason
	}
	return "orderly circuit exit with cargo aboard"
}

// liquidateHeld is the finish-current-leg engine (sp-1hj5, sp-7yej invariant 1):
// deliver the held cargo to the lane's destination and sell it down to empty.
// Each attempt re-runs the full sell half — travel (idempotent when already
// there), dock, re-observe the importer, sell one tranche — so it recovers from
// exactly the failures that interrupt a leg: a transient jump/navigate error
// (the incident's 'Starting jump operation' → instant failure), the nav-cache
// dock race, an importer volume tick at zero. Consecutive-failure bounded
// (liquidationMaxFailures) with a clock backoff between failures; any sold
// tranche resets the count, so a deep hold draining 10 units a tick is progress,
// not failure. Sells here are liquidation of already-bought cargo — sunk cost
// being recovered — so the bid-floor discipline (a BUY gate) deliberately does
// not apply; revenue and units still land on the run's ledger.
func (h *RunTradeRouteCoordinatorHandler) liquidateHeld(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
	held int,
) (*navigation.Ship, int) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID

	failures := 0
	attemptFailed := func(stage string, cause error) {
		failures++
		outcome := "backing off and retrying"
		if failures >= liquidationMaxFailures {
			outcome = "giving up"
		}
		// Verbatim cause in the MESSAGE, not just the metadata the container-log
		// renderer drops (sp-ynuf/sp-iqyq convention).
		logger.Log("WARNING", fmt.Sprintf(
			"Finish-current-leg %s failed (attempt %d/%d): %v - %s",
			stage, failures, liquidationMaxFailures, cause, outcome),
			map[string]interface{}{
				"action": "finish_current_leg_retry",
				"stage":  stage,
				"held":   held,
				"error":  cause.Error(),
			})
		if failures < liquidationMaxFailures {
			h.clock.Sleep(liquidationRetryBackoff)
		}
	}

	for held > 0 && failures < liquidationMaxFailures {
		var err error
		ship, err = h.travel(ctx, ship, lane.DestWaypoint, playerID)
		if err != nil {
			attemptFailed("travel to destination", err)
			continue
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			attemptFailed("dock at destination", err)
			continue
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			attemptFailed("re-observe destination", err)
			continue
		}
		tranche := trading.VisitTranche(dstGood.TradeVolume(), held)
		if tranche <= 0 {
			attemptFailed("sell", fmt.Errorf("importer trade volume exhausted (%d) while holding %d %s", dstGood.TradeVolume(), held, lane.Good))
			continue
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, tranche, playerID)
		if err != nil {
			attemptFailed("sell", err)
			continue
		}
		if sellResp.UnitsSold <= 0 {
			attemptFailed("sell", fmt.Errorf("sell of %d %s reported zero units sold", tranche, lane.Good))
			continue
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		failures = 0 // progress resets the failure budget
		logger.Log("INFO", fmt.Sprintf("Finish-current-leg sold %d %s at %s (%d still aboard)",
			sellResp.UnitsSold, lane.Good, lane.DestWaypoint, held), map[string]interface{}{
			"action": "finish_current_leg_sell",
			"good":   lane.Good,
			"sold":   sellResp.UnitsSold,
			"held":   held,
		})
	}
	return ship, held
}

// flyVisits is the lane's visit loop: disciplined tranches until the destination
// bid falls below basis+1000, tradable volume dries up, the RUN's remaining
// visit budget is consumed, or a leg fails. It returns the current ship pointer
// and how many units bought this run are still aboard — the caller (runCircuit)
// owns what a non-empty hold means; no exit path here may claim the run.
func (h *RunTradeRouteCoordinatorHandler) flyVisits(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
	runMaxVisits int,
) (*navigation.Ship, int) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID
	reserve := cmd.WorkingCapitalReserve
	if reserve <= 0 {
		reserve = defaultWorkingCapitalReserve
	}

	held := 0
	circuitNetMargin := 0 // this circuit's own sell revenue minus its own buy cost (sp-bp6f fix #2)
	// The loop draws down the RUN's budget (response.Visits is run-cumulative,
	// sp-1hj5), not a per-lane allowance; i still indexes this circuit's own
	// attempts for the first-visit stale-ask check and the realized-margin gate.
	// Termination: every iteration either returns or completes a sell
	// (response.Visits++), so the condition strictly progresses.
	for i := 0; response.Visits < runMaxVisits; i++ {
		// Re-observe both ends: basis (source ask we pay) and the live dest bid.
		srcGood, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Source market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship, held
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Destination market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.DestWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship, held
		}

		basis := srcGood.SellPrice()       // ask: what we PAY buying from the source
		destBid := dstGood.PurchasePrice() // bid: what we RECEIVE selling to the dest

		// Bid-floor discipline: the edge is gone once the dest bid stops clearing
		// basis+1000. Stop here rather than grind the spread to nothing.
		if !trading.MarginAlive(destBid, basis) {
			logger.Log("INFO", "Margin dead - stopping circuit at the bid-floor", map[string]interface{}{
				"good": lane.Good, "dest_bid": destBid, "basis": basis, "floor": basis + trading.MinBidMargin,
			})
			return ship, held
		}

		// Size the tranche to the hull's AVAILABLE hold, not its total capacity: an idle
		// hull is not necessarily empty (a factory hauler benched mid-task, a pool hull
		// with leftover cargo), and the buy executor refuses any tranche larger than
		// AvailableCargoSpace. Sizing by CargoCapacity would overshoot the free hold on a
		// non-empty hull and get the buy rejected — a distinct zero-visit path from the
		// sp-2sam root cause (the navigate self-collision, fixed in the CLI runner), hardened
		// here so a residual-cargo hull still flies. AvailableCargoSpace already nets out the
		// residual cargo; held is this run's own bought-not-yet-sold units on top.
		cargoSpace := ship.AvailableCargoSpace() - held
		// Split the old "volume or hold exhausted" guard into its two distinct causes
		// (sp-xwa1). A HULL-side stall (no free hold) and a MARKET-side one (source
		// volume dried up) used to share one indistinguishable line, hiding WHICH it
		// was — the silent-cause defect the root cause named. Check the hold first: a
		// pre-buy cargo park is the same class as the pre-flight gate, parking with a
		// structured reason rather than buying a useless sliver as accumulated cargo
		// fills the hold. The outer loop reads CargoBlocked and stops the run.
		if cargoSpace < minFreeCargoForCircuit {
			response.CargoBlocked = true
			response.CargoBlockReason = fmt.Sprintf(
				"hull has %d free cargo unit(s) (holding %d bought this circuit), needs >=%d for %s",
				cargoSpace, held, minFreeCargoForCircuit, lane.Good)
			cargoBlockedLog(logger, lane.Good, minFreeCargoForCircuit, cargoSpace, "hold filled with cargo bought this circuit")
			return ship, held
		}
		buyUnits := trading.VisitTranche(srcGood.TradeVolume(), cargoSpace)
		if buyUnits <= 0 {
			logger.Log("INFO", "No tranche to buy (source market volume exhausted) - stopping circuit", map[string]interface{}{
				"good": lane.Good, "source_volume": srcGood.TradeVolume(), "cargo_space": cargoSpace,
			})
			return ship, held
		}

		// Working-capital spend floor (sp-bp6f): refuse the buy BEFORE traveling,
		// docking, or purchasing if it would drop live treasury below the reserve.
		// Must run here, ahead of Leg 1 committing to anything — a circuit that
		// already docked and spent before checking has defeated the floor's whole
		// purpose. Uses basis (this visit's live source ask, re-observed above),
		// not lane.SourceAsk (the STALE ranked basis), so the projected cost
		// reflects what the circuit is actually about to pay right now.
		if h.spendFloorBreached(ctx, buyUnits*basis, reserve, response) {
			return ship, held
		}

		// Leg 1: buy a tranche at the source (exporter). A cross-system lane
		// jumps instead of navigating (sp-wlev); travel reloads the ship
		// afterward so this pointer reflects its post-jump state.
		ship, err = h.travel(ctx, ship, lane.SourceWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to source %s failed: %v", lane.SourceWaypoint, err)
			logger.Log("WARNING", "Travel to source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at source %s failed: %v", lane.SourceWaypoint, err)
			// Put the verbatim cause in the MESSAGE — the container-log renderer drops the
			// metadata map, so a cause hidden in {"error": ...} never reaches an operator
			// (sp-ynuf defect 1, the sp-iqyq class). A blind dock failure now names itself.
			logger.Log("WARNING", fmt.Sprintf("Dock at source %s failed: %v - ending circuit", lane.SourceWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}

		// Live-verify the ranked basis before the FIRST buy (hazard b): the lane was
		// ranked from a market cache that can be many minutes stale. Now that the hull
		// is docked at the source (the API returns live prices only with a ship present),
		// re-read the source ask and abort if it has run away from the basis the lane
		// was ranked on — buying on a stale basis has realised a large loss (a -196k
		// precedent). Only the first visit re-verifies; later visits already re-observe.
		if i == 0 && h.staleAskAborts(ctx, lane, playerID, response) {
			return ship, held
		}

		// sp-9mkf: arm the per-tranche buy ceiling at the lane's margin floor — the max
		// ask that still clears destBid − MinBidMargin. The per-visit MarginAlive gate
		// above checked only this visit's FIRST live ask; this bounds the intra-buy ladder
		// a multi-tranche purchase walks up itself (the D39 stale-ask class), aborting the
		// remainder once a sub-tranche prices past the bid-floor.
		buyResp, err := h.purchaseWithCeiling(ctx, ship.ShipSymbol(), lane.Good, buyUnits, playerID, destBid-trading.MinBidMargin)
		if err != nil {
			response.AbortReason = fmt.Sprintf("purchase of %d %s at source %s failed: %v", buyUnits, lane.Good, lane.SourceWaypoint, err)
			logger.Log("WARNING", "Purchase failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
			// The source ask laddered past the bid-floor before any unit was bought — end
			// the circuit rather than fly an empty leg. Prior-visit held cargo (if any) is
			// finished by the epilogue.
			logger.Log("WARNING", fmt.Sprintf("Buy ceiling aborted the tranche for %s at %s: live ask %d > ceiling %d - ending circuit",
				lane.Good, lane.SourceWaypoint, buyResp.CeilingObservedAsk, destBid-trading.MinBidMargin), map[string]interface{}{
				"action": "circuit_buy_ceiling_abort", "good": lane.Good,
				"live_ask": buyResp.CeilingObservedAsk, "ceiling": destBid - trading.MinBidMargin,
			})
			return ship, held
		}
		held += buyResp.UnitsAdded
		response.TotalCost += buyResp.TotalCost
		circuitNetMargin -= buyResp.TotalCost

		// Leg 2: sell what we hold at the destination (importer). A
		// cross-system lane jumps instead of navigating (sp-wlev).
		ship, err = h.travel(ctx, ship, lane.DestWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			// Verbatim cause in the MESSAGE (sp-ynuf); the finish-current-leg
			// epilogue owns whether this becomes a recovered leg or the one
			// structured cargo_aboard_exit strand record (sp-1hj5).
			logger.Log("WARNING", fmt.Sprintf("Travel to destination %s failed with %d %s aboard: %v - finishing leg via liquidation", lane.DestWaypoint, held, lane.Good, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			// Verbatim cause in the MESSAGE (not the dropped metadata field) so a blind
			// dock-at-destination failure names itself — same defect-1 fix as the source leg.
			logger.Log("WARNING", fmt.Sprintf("Dock at destination %s failed (cargo aboard): %v - ending circuit", lane.DestWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		sellUnits := trading.VisitTranche(dstGood.TradeVolume(), held)
		if sellUnits <= 0 {
			// The importer has no tradable volume this tick while we hold cargo: not a
			// clean margin-death, so surface it rather than return silently (the one
			// early-return that used to vanish without a trace).
			response.AbortReason = fmt.Sprintf("destination %s has no sellable volume for %s while holding %d units", lane.DestWaypoint, lane.Good, held)
			logger.Log("INFO", fmt.Sprintf("No sellable tranche at destination %s (importer trade volume %d exhausted) with %d %s aboard - finishing leg via liquidation", lane.DestWaypoint, dstGood.TradeVolume(), held, lane.Good), nil)
			return ship, held
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, sellUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("sell of %d %s at destination %s failed (cargo aboard): %v", sellUnits, lane.Good, lane.DestWaypoint, err)
			logger.Log("WARNING", fmt.Sprintf("Sell of %d %s at destination %s failed with %d aboard: %v - finishing leg via liquidation", sellUnits, lane.Good, lane.DestWaypoint, held, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		circuitNetMargin += sellResp.TotalRevenue
		response.Visits++

		// sp-tl68 wire-in #2: record this leg's compression debt on the SHARED cooldown
		// ledger so the ranker down-weights this lane for ~tau (hours, not the old ~30min
		// assumption) and the fleet rotates to fresh lanes instead of re-hammering it.
		// Keyed by lane (buy, sell, good); U = units sold this visit, tv = the lane's
		// absorption cap (the same tv the ranker charges self-impact against). Best-effort:
		// a nil ledger (unwired) or non-positive units/cap is a no-op inside Accrue.
		if h.laneLedger != nil {
			h.laneLedger.Accrue(laneCooldownKey(lane), sellResp.UnitsSold, lane.VolumeCap, h.clock.Now())
		}

		// Per-circuit negative-margin abort (sp-bp6f fix #2): this circuit's own
		// realized fills - not the stale ranked spread - are what matter once
		// trading has actually started. Gate on i+1 (visit count) so one noisy
		// early fill can't trip it; negativeMarginAbortVisits consecutive visits
		// of net-negative realized margin means the lane itself has turned
		// loss-making, e.g. the incident's repeated tranches walking the source
		// ask up while the destination bid gets crushed.
		if i+1 >= negativeMarginAbortVisits && circuitNetMargin < 0 {
			response.NegativeMarginAbort = true
			response.RealizedCircuitMargin = circuitNetMargin
			logger.Log("WARNING", "Circuit realized margin ran negative - aborting before it compounds", map[string]interface{}{
				"good": lane.Good, "visits": response.Visits, "realized_margin": circuitNetMargin,
			})
			return ship, held
		}
	}

	// The loop condition ran out: the RUN's visit budget is consumed. This is a
	// leg boundary (Visits only advances on a completed sell); the outer loop's
	// budget check turns it into the clean exitReasonMaxVisits stop (sp-1hj5).
	logger.Log("INFO", "Run visit budget consumed - ending circuit at the leg boundary", map[string]interface{}{
		"good": lane.Good, "visits": response.Visits, "max_visits": runMaxVisits,
	})
	return ship, held
}
