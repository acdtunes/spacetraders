package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// stockerPick is one round-trip's chosen good: what to buy, where, and how much.
type stockerPick struct {
	Good           string
	ForeignMarket  string
	ForeignAsk     int
	HomeAsk        int
	SavingsPerUnit int
	UnitsShort     int
	Units          int // the haul size (capped by hold / space / ceiling / per-leg budget)
}

// stockerFunnel counts where a pass's miner rows fell out. The numbers are only meaningful
// together, as the single empty-pass verdict line.
type stockerFunnel struct {
	minerRows    int
	eligible     int
	unreachable  int
	afterFilters int
}

// pick chooses the most-needed good to stock. A PINNED depot (cmd.HomeSystemOnly, RULINGS
// #14) short-circuits to pickPinned (sp-rh1wi) BEFORE the demand miner is ever consulted:
// its supported_goods is a FIXED, analyst-defined set that the miner's live-contract-demand
// ranking has no reason to intersect, so it sources directly from the cheapest home market
// instead. Otherwise (the generic, cross-system stocker) it is the stock-eligible miner
// candidate with the highest (savings/u × units-short) that the warehouse buffers, clears
// the min-savings floor, is FRESH (75-min discipline), and whose haul fits the tightest of
// hold space, warehouse free space, the capital ceiling, and the per-leg budget. Returns
// ok=false (with a single verdict line) when nothing survives — an honest empty pass.
// Every money guard fails CLOSED (RULINGS #4): an unreadable balance stocks nothing.
func (h *RunStockerCoordinatorHandler) pick(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	reserve int64,
	maxAge time.Duration,
) (stockerPick, bool) {
	logger := common.LoggerFromContext(ctx)

	// Capital ceiling (10% of treasury, junior to the reserve). Unreadable balance →
	// stock nothing (fail closed, RULINGS #4).
	ceiling, known := h.capitalCeiling(ctx, cmd.PlayerID, reserve)
	if !known {
		logger.Log("WARNING", "Stocker: capital ceiling unreadable (treasury) - nothing to stock this pass (fail closed, RULINGS #4)", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
		})
		return stockerPick{}, false
	}
	if ceiling <= 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: capital ceiling is 0 (treasury at/below reserve %d) - nothing to stock this pass", reserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "reserve": reserve,
		})
		return stockerPick{}, false
	}

	// AGGREGATE warehouse free space across the co-located group (sp-5q2c: light-12 +
	// heavy-4B sum). Full — and an empty pass — only when EVERY member is full.
	freeSpace := tradingsvc.TotalFreeSpace(h.storageCoordinator, group)
	if freeSpace <= 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: warehouse group at %s full (0 aggregate free space across %d op(s)) - nothing to stock this pass", cmd.WarehouseWaypoint, len(group)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "group_size": len(group),
		})
		return stockerPick{}, false
	}

	// Hull hold capacity bounds the haul (the hull is empty here — laden hulls take the
	// resume-deposit path before pick).
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Stocker: could not load hull %s for pick: %v", cmd.ShipSymbol, err), map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "error": err.Error()})
		return stockerPick{}, false
	}
	hold := ship.AvailableCargoSpace()
	if hold <= 0 {
		return stockerPick{}, false
	}
	currentSystem := ship.CurrentLocation().SystemSymbol

	homeSystem := shared.ExtractSystemSymbol(cmd.WarehouseWaypoint)

	// sp-rh1wi: a PINNED depot (HomeSystemOnly, RULINGS #14) buffers a FIXED,
	// analyst-defined good set (contractscaler.FarSourceGoods) — never demand-mined or
	// value-ranked (RULINGS #6). Short-circuits BEFORE the demand miner is ever consulted,
	// so the generic (HomeSystemOnly==false) path below is completely untouched/byte-identical.
	if cmd.HomeSystemOnly {
		return h.pickPinned(ctx, cmd, group, homeSystem, currentSystem, hold, freeSpace, ceiling)
	}

	rows, err := h.demandMiner.Mine(ctx, homeSystem, cmd.PlayerID, nil, persistence.DemandMinerOptions{
		MinRecurrence: h.config.MinRecurrence, TopN: stockerMinerTopN, BuyLegSavingsPerUnit: h.config.BuyLegSavingsPerUnit,
		// sp-k2xav / RULINGS #14: the contract depot sources INTRA-system only — confine each
		// good's source market to homeSystem so a cheaper foreign market is never picked. The
		// generic stocker leaves this false (cross-system, unchanged).
		HomeSystemOnly: cmd.HomeSystemOnly,
	})
	if err != nil {
		logger.Log("WARNING", "Stocker: demand mining failed - nothing to stock this pass: "+err.Error(), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "home_system": homeSystem, "error": err.Error(),
		})
		return stockerPick{}, false
	}

	minSavings := h.config.MinSavingsPerUnit
	if minSavings <= 0 {
		minSavings = 1
	}
	allow := stringSet(h.config.Allowlist)
	block := stringSet(h.config.Blocklist)
	now := h.clock.Now()

	// Target-hysteresis (RULINGS #5): re-stage only once the shortfall reaches the
	// hysteresis floor, so a STANDING loop does not thrash on a 1-unit gap. Default 1 →
	// re-stage on any shortfall (unitsShort <= 0 excluded), the historical behavior.
	refillFloor := cmd.RefillHysteresis
	if refillFloor < 1 {
		refillFloor = 1
	}

	// Auto-cap knapsack: per-good target_units from live demand × residual-buy-leg
	// over Σ REAL hull capacity, re-solved every pass (RULINGS #2 re-derivable; "re-solved as
	// demand/fleet change"). A nil result means STAND ASIDE — cold start (thin history), an
	// explicit TargetPerGood override, or zero capacity — and the pre-existing per-good target
	// governs. When present, capTargets is authoritative: a good absent from it (e.g. a
	// central/hub-covered in-system good the optimizer refuses) gets target 0 and is skipped,
	// so no single good can crowd out the far/orphan goods the buffer exists to hold.
	var capTargets map[string]int
	if cmd.TargetPerGood <= 0 {
		capTargets = h.resolveWarehouseCaps(ctx, homeSystem, cmd.WarehouseWaypoint, group, rows)
	}

	var best stockerPick
	bestValue := 0
	funnel := stockerFunnel{minerRows: len(rows)}

	// Rows arrive stock-eligible-first, ranked by total projected savings; the stocker
	// re-ranks the survivors by (savings/u × units-short) — the most-needed-by-value good.
	for _, r := range rows {
		if !stockEligibleRow(r, minSavings) {
			continue
		}
		funnel.eligible++
		// Only stock goods the group actually BUFFERS: a good no co-located member
		// supports would strand (no contract worker could withdraw it). Fail closed.
		if !goodListed(r.Good, allow, block) || !tradingsvc.AnySupportsGood(group, r.Good) {
			continue
		}

		target := resolveStockTarget(r, cmd, capTargets)
		// Net the target against AGGREGATE on-hand across the group so a
		// sibling warehouse's stock is never invisible — the stocker stops buying once
		// the COMBINED inventory reaches target, not once any single hull does.
		unitsShort := target - tradingsvc.TotalCargoAvailable(h.storageCoordinator, group, r.Good)
		if unitsShort < refillFloor {
			continue // at/over target, or within the hysteresis band — nothing to re-stage
		}

		// Reachability: an unreachable-cheapest foreign market must never win
		// the need-rank — feeding it to buy()'s travel() unchecked crash-loops the hull
		// identically on every relaunch (TORWIND-38 repeatedly picked X1-PB12 from
		// X1-KA42, gate-unreachable within 5 jumps, while scout posts kept its ask
		// artificially "cheapest" forever). Consults the SAME gate graph and jump bound
		// travel()'s own jumpPath() uses — never a second reachability notion.
		if !h.foreignMarketReachable(ctx, currentSystem, r.ForeignMarket, cmd.PlayerID) {
			funnel.unreachable++
			continue
		}

		// Freshness (75-min discipline): a stale/gone foreign price is not a trustworthy
		// pick — skip rather than haul to it (the buy still live-verifies at the dock).
		if !h.foreignMarketFresh(ctx, r.ForeignMarket, r.Good, cmd.PlayerID, now, maxAge) {
			continue
		}
		funnel.afterFilters++

		// Cap the haul at the tightest of units-short, hold space, warehouse free space,
		// the capital ceiling (credits / ask), and the per-leg budget (credits / ask).
		units := min(unitsShort, hold)
		units = min(units, freeSpace)
		units = min(units, int(ceiling/int64(r.ForeignAsk)))
		if cmd.BudgetPerLeg > 0 {
			units = min(units, cmd.BudgetPerLeg/r.ForeignAsk)
		}
		if units <= 0 {
			continue // ceiling/budget/space exhausted for this good
		}

		value := r.ProjectedSavingsPerUnit * unitsShort
		if value > bestValue {
			bestValue = value
			best = stockerPick{
				Good:           r.Good,
				ForeignMarket:  r.ForeignMarket,
				ForeignAsk:     r.ForeignAsk,
				HomeAsk:        r.HomeAsk,
				SavingsPerUnit: r.ProjectedSavingsPerUnit,
				UnitsShort:     unitsShort,
				Units:          units,
			}
		}
	}

	if bestValue <= 0 {
		h.recordEmptyPass(ctx, cmd, group, freeSpace, ceiling, funnel)
		return stockerPick{}, false
	}

	// A productive pick: forget any prior no-reachable-source park (a later recurrence
	// re-logs as a state change, mirrors the tour's clearDepositParked on the
	// capital-available path).
	h.clearNoReachableSource(cmd.ShipSymbol)
	logger.Log("INFO", fmt.Sprintf(
		"stocking %s: %du short, buy@%s %d/u (savings %d/u), value %d, hauling %du (ceiling %d, free %d)",
		best.Good, best.UnitsShort, best.ForeignMarket, best.ForeignAsk, best.SavingsPerUnit, bestValue, best.Units, ceiling, freeSpace), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": best.Good, "units_short": best.UnitsShort,
		"foreign_market": best.ForeignMarket, "foreign_ask": best.ForeignAsk, "savings_per_unit": best.SavingsPerUnit,
		"value": bestValue, "haul_units": best.Units, "ceiling": ceiling, "free_space": freeSpace,
	})
	return best, true
}

// recordEmptyPass logs the one empty-pass verdict. unreachable>0 with nothing else survivable is
// the "no reachable source" park — de-duped per hull (state-change only) so a hull parked on an
// unreachable-only need-rank logs ONCE, not once per pass. Any OTHER empty-pass cause (at target /
// unaffordable / stale / unsupported) clears the remembered state so a LATER genuine
// no-reachable-source park re-logs fresh.
func (h *RunStockerCoordinatorHandler) recordEmptyPass(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	freeSpace int,
	ceiling int64,
	funnel stockerFunnel,
) {
	if funnel.unreachable > 0 {
		h.recordNoReachableSource(ctx, cmd.ShipSymbol, funnel.unreachable, funnel.minerRows)
	} else {
		h.clearNoReachableSource(cmd.ShipSymbol)
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Stocker verdict: nothing to stock — [warehouse=%s(group %d) free=%d ceiling=%d funnel: miner_rows=%d eligible=%d unreachable=%d after_filters=%d] (at target / unreachable / unaffordable / stale / unsupported)",
		cmd.WarehouseWaypoint, len(group), freeSpace, ceiling, funnel.minerRows, funnel.eligible, funnel.unreachable, funnel.afterFilters), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "group_size": len(group), "free_space": freeSpace,
		"ceiling": ceiling, "miner_rows": funnel.minerRows, "eligible": funnel.eligible, "unreachable": funnel.unreachable, "after_filters": funnel.afterFilters,
	})
}

// stockEligibleRow admits only rows with BOTH asks known and savings clearing the floor —
// no speculative stocking (RULINGS #6).
func stockEligibleRow(r persistence.DemandCandidate, minSavings int) bool {
	return r.StockEligible && r.ProjectedSavingsPerUnit >= minSavings && r.ForeignAsk > 0 && r.HomeAsk > 0
}

// goodListed applies the config allow/block lists; an empty allowlist admits everything the
// blocklist does not name.
func goodListed(good string, allow, block map[string]bool) bool {
	if len(allow) > 0 && !allow[good] {
		return false
	}
	return !block[good]
}

// resolveStockTarget picks the good's target inventory: an explicit per-good override, else
// the auto-cap knapsack's computed target_units when it solved (0 => not buffered => the
// caller's units-short guard skips the good), else raw live demand.
func resolveStockTarget(r persistence.DemandCandidate, cmd *RunStockerCoordinatorCommand, capTargets map[string]int) int {
	if cmd.TargetPerGood > 0 {
		return cmd.TargetPerGood
	}
	if capTargets != nil {
		return capTargets[r.Good]
	}
	return r.DemandUnits
}

// pickPinned is the PINNED-depot path (cmd.HomeSystemOnly, RULINGS #14): it buffers a
// FIXED, analyst-defined good set (contractscaler.FarSourceGoods) up to a flat per-good
// target, sourced DIRECTLY from the cheapest HOME-system market for each good — it never
// consults the demand miner and never gates on a savings-vs-anchor margin (sp-rh1wi). The
// demand miner's cross-system ranking is opportunistic-arbitrage logic that does not apply
// to a deliberately-pinned buffer: the depot's job is to hold FarSourceGoods ready for a
// contract-delivery hull to draw down, not to chase the best margin. Every money guard
// (ceiling, freeSpace, hold — all computed by the shared prefix in pick(), plus the
// reserve floor re-checked live at buy()) still binds identically (RULINGS #4); only the
// SELECTION and SAVINGS-GATE are bypassed.
//
// Freshness (the 75-min discipline foreignMarketFresh enforces for the generic path) is
// deliberately NOT applied here: a pinned good has exactly ONE home-market candidate (the
// cheapest home seller), unlike the miner's ranked list of alternatives, so filtering it
// out on staleness would starve the good FOREVER — the cache never refreshes because the
// hull is never dispatched to observe it. The dock-time live-ask ceiling (buy()'s sp-9mkf
// verify, unchanged) remains the real safety net: a stale cached price can at worst abort
// the buy at the dock, never cause an overspend. Narrow, documented relaxation.
//
// Ranks candidates by unitsShort (most-depleted-by-quantity) since there is no
// savings/anchor concept on this path (explicitly not computed, RULINGS #6/#9).
func (h *RunStockerCoordinatorHandler) pickPinned(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	homeSystem, currentSystem string,
	hold, freeSpace int,
	ceiling int64,
) (stockerPick, bool) {
	logger := common.LoggerFromContext(ctx)

	target := contractscaler.DepotUnitsPerGood
	if cmd.TargetPerGood > 0 {
		target = cmd.TargetPerGood
	}
	refillFloor := cmd.RefillHysteresis
	if refillFloor < 1 {
		refillFloor = 1
	}

	goods := pinnedSupportedGoods(group)

	var best stockerPick
	bestShort := 0
	considered, unreachable := 0, 0

	for _, good := range goods {
		// Net the target against AGGREGATE on-hand across the group so a
		// sibling warehouse's stock is never invisible.
		unitsShort := target - tradingsvc.TotalCargoAvailable(h.storageCoordinator, group, good)
		if unitsShort < refillFloor {
			continue // at/over target, or within the hysteresis band
		}
		considered++

		cheapest, err := h.marketRepo.FindCheapestMarketSelling(ctx, good, homeSystem, cmd.PlayerID)
		if err != nil || cheapest == nil || cheapest.Ask <= 0 {
			continue // no home-system seller found this pass - retry next pass (RULINGS #2)
		}

		// Reachability: same graph/bound travel() itself enforces. Same-system
		// is trivially true for a genuinely home-scoped result; kept for parity/safety.
		if !h.foreignMarketReachable(ctx, currentSystem, cheapest.WaypointSymbol, cmd.PlayerID) {
			unreachable++
			continue
		}

		units := min(unitsShort, hold)
		units = min(units, freeSpace)
		units = min(units, int(ceiling/int64(cheapest.Ask)))
		if cmd.BudgetPerLeg > 0 {
			units = min(units, cmd.BudgetPerLeg/cheapest.Ask)
		}
		if units <= 0 {
			continue // ceiling/budget/space exhausted for this good
		}

		if unitsShort > bestShort {
			bestShort = unitsShort
			best = stockerPick{
				Good:          good,
				ForeignMarket: cheapest.WaypointSymbol,
				ForeignAsk:    cheapest.Ask,
				UnitsShort:    unitsShort,
				Units:         units,
			}
		}
	}

	if bestShort <= 0 {
		if unreachable > 0 {
			h.recordNoReachableSource(ctx, cmd.ShipSymbol, unreachable, considered)
		} else {
			h.clearNoReachableSource(cmd.ShipSymbol)
		}
		logger.Log("INFO", fmt.Sprintf(
			"Stocker verdict (pinned): nothing to stock — [warehouse=%s(group %d) free=%d ceiling=%d funnel: supported_goods=%d considered=%d unreachable=%d] (at target / unreachable / unaffordable / no home seller)",
			cmd.WarehouseWaypoint, len(group), freeSpace, ceiling, len(goods), considered, unreachable), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "group_size": len(group), "free_space": freeSpace,
			"ceiling": ceiling, "supported_goods": len(goods), "considered": considered, "unreachable": unreachable,
		})
		return stockerPick{}, false
	}

	h.clearNoReachableSource(cmd.ShipSymbol)
	logger.Log("INFO", fmt.Sprintf(
		"stocking %s (pinned): %du short, buy@%s %d/u, hauling %du (ceiling %d, free %d)",
		best.Good, best.UnitsShort, best.ForeignMarket, best.ForeignAsk, best.Units, ceiling, freeSpace), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": best.Good, "units_short": best.UnitsShort,
		"foreign_market": best.ForeignMarket, "foreign_ask": best.ForeignAsk,
		"haul_units": best.Units, "ceiling": ceiling, "free_space": freeSpace,
	})
	return best, true
}

// pinnedSupportedGoods returns the union of every co-located warehouse member's OWN
// declared supported_goods — the pinned depot's candidate list comes DIRECTLY from the
// warehouse's own analyst-defined configuration (in production, exactly
// contractscaler.FarSourceGoods, since that is what seeds the depot warehouse's
// supported_goods at creation), never from a stocker-side constant re-derivation. This
// mirrors the generic path's own AnySupportsGood filter (a good no member supports would
// strand; no contract worker could withdraw it) and keeps the pinned path correct for any
// HomeSystemOnly warehouse, not hardcoded to one specific whitelist.
func pinnedSupportedGoods(group []*storage.StorageOperation) []string {
	seen := map[string]bool{}
	var goods []string
	for _, op := range group {
		for _, good := range op.SupportedGoods() {
			if !seen[good] {
				seen[good] = true
				goods = append(goods, good)
			}
		}
	}
	return goods
}
