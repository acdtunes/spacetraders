package commands

// run_tour_coordinator_lookback.go — sp-ed4i: look-back loading kills deadhead jumps.
//
// A margins-death reposition (sp-zhii, run_tour_coordinator_reposition.go) rotates a hull
// off its own tapped-out ground to a fresh one — but RepositionToWaypoint is a pure empty
// movement, so 34% of cross-system transitions arrived EMPTY (trade-analyst 2026-07-11:
// 31/90 in 6h; the HU21->UQ16 corridor 7/7 empty even though HU21 EXPORTS parts/plating/
// adv_circ that UQ16 IMPORTS). The solver never carried them because its margins-death plan
// scoped to the DEPARTURE system taps out its in-system arb; the profitable lane is the
// cross-system one, and the departure exports were left on the dock.
//
// This is the seam: at reposition-commit time the coordinator ALREADY holds BOTH systems'
// fresh listings (the hull's current system, and the ranked candidate whose listings the
// candidate scan read via collectSystemListings). So BEFORE the empty jump, enumerate
// departure EXPORT rows x destination IMPORT rows, buy the best floor-clearing manifest,
// and let the post-jump re-plan liquidate it as launch cargo (sp-m5kv held-liquidation) at
// the destination's import bids. Every existing money guard is applied UNTOUCHED (RULINGS
// #4): the min-margin floor on the cached spread, the working-capital reserve at buy time
// (sp-agzj), the live-ask ceiling (sp-9mkf), hold capacity, and the tour's max-spend cap.
// No candidate clears the floors -> jump empty exactly as today (loaded-if-profitable,
// never forced).

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// lookbackExportType is the GoodListing.TradeType value a look-back destination must NOT
// carry: an exporter's Bid is a low sellback price, not a real import demand (sp-9mkf). It
// mirrors domain/trading.tradeTypeExport, sourced from the market constant so the value
// cannot drift.
var lookbackExportType = string(market.TradeTypeExport)

// lookbackItem is one good in a look-back manifest: buy Units of Good at SourceWaypoint in
// the departure system (paying ~SourceAsk), carry it across the jump, and liquidate it at
// the destination system's import market (~DestBid). SourceAsk/DestBid are the CACHED
// prices the manifest was sized from; the executor re-verifies the buy live (sp-9mkf) and
// the post-jump re-plan re-verifies the sell live, so a moved price never trades stale.
type lookbackItem struct {
	Good           string
	SourceWaypoint string
	Units          int
	SourceAsk      int
	DestBid        int
}

// buildLookbackManifest pairs departure-system buyable rows (src) against destination-system
// import rows (dest) into a hold-capped, floor-cleared, best-spread-first manifest — the
// pure core of look-back loading (sp-ed4i), computed exactly like a cross-system slice of
// trading.RankSpreads so it inherits that ranker's discipline:
//
//   - SINK discipline (sp-9mkf): a destination row of TradeType EXPORT is NEVER a sell sink
//     (an exporter's Bid is a low sellback price, not a real import demand). IMPORT/EXCHANGE
//     and unknown/empty types stay eligible (fail-open on missing data), mirroring
//     bestLaneForGood so this can never reintroduce the C37 dump.
//   - SOURCE side: any market with a positive Ask is a valid buy (exporters have the low
//     asks); no trade-type restriction, matching how lanes buy.
//   - FLOOR: profit/unit = destBid - srcAsk must clear max(1, minMargin) — the tour's own
//     per-run min-margin gate (RULINGS #4), the same floor the solver applies.
//   - DEPTH: each good is capped at min(srcVolume, destVolume) — one market tranche, priced
//     at the live quote with NO decay, so a shallow look-back load matches the solver's
//     tranche-0 economics rather than the deep-dump depths its A-cap guards against.
//   - RANK + FILL: goods are ordered by capped spread (spread x volumeCap) desc, then raw
//     spread desc, then good asc (RankSpreads' tie-break), and greedily packed into holdCap.
//
// Returns nil when no good clears the floor or the hold is zero — the caller then jumps
// empty exactly as today.
func buildLookbackManifest(src, dest []trading.GoodListing, holdCap, minMargin int) []lookbackItem {
	if holdCap <= 0 {
		return nil
	}
	floor := minMargin
	if floor < 1 {
		floor = 1 // mirror the solver's max(1, min_margin) — a zero floor still bars a zero spread
	}

	// Best (lowest-ask) buyable source per good, and its trade volume.
	type srcRow struct {
		waypoint string
		ask      int
		volume   int
	}
	bestSrc := map[string]srcRow{}
	for _, l := range src {
		if l.Ask <= 0 {
			continue
		}
		if cur, ok := bestSrc[l.Good]; !ok || l.Ask < cur.ask {
			bestSrc[l.Good] = srcRow{waypoint: l.Waypoint, ask: l.Ask, volume: l.Volume}
		}
	}

	// Best (highest-bid) IMPORT sink per good in the destination system.
	type destRow struct {
		bid    int
		volume int
	}
	bestDest := map[string]destRow{}
	for _, l := range dest {
		if l.Bid <= 0 {
			continue
		}
		if l.TradeType == lookbackExportType {
			continue // sp-9mkf: an exporter's bid is not an import sink
		}
		if cur, ok := bestDest[l.Good]; !ok || l.Bid > cur.bid {
			bestDest[l.Good] = destRow{bid: l.Bid, volume: l.Volume}
		}
	}

	// Rank floor-clearing cross-system lanes exactly like RankSpreads' output would.
	lanes := make([]trading.ArbitrageLane, 0, len(bestSrc))
	for good, s := range bestSrc {
		d, ok := bestDest[good]
		if !ok {
			continue
		}
		spread := d.bid - s.ask
		if spread < floor {
			continue
		}
		volumeCap := s.volume
		if d.volume < volumeCap {
			volumeCap = d.volume
		}
		if volumeCap <= 0 {
			continue
		}
		lanes = append(lanes, trading.ArbitrageLane{
			Good:           good,
			SourceWaypoint: s.waypoint,
			SourceAsk:      s.ask,
			DestBid:        d.bid,
			SpreadPerUnit:  spread,
			VolumeCap:      volumeCap,
			CappedSpread:   spread * volumeCap,
		})
	}
	if len(lanes) == 0 {
		return nil
	}
	sortLookbackLanes(lanes)

	// Greedily pack the hold best-lane-first; a shallow single-tranche load per good.
	manifest := make([]lookbackItem, 0, len(lanes))
	remaining := holdCap
	for _, l := range lanes {
		if remaining <= 0 {
			break
		}
		units := l.VolumeCap
		if units > remaining {
			units = remaining
		}
		manifest = append(manifest, lookbackItem{
			Good:           l.Good,
			SourceWaypoint: l.SourceWaypoint,
			Units:          units,
			SourceAsk:      l.SourceAsk,
			DestBid:        l.DestBid,
		})
		remaining -= units
	}
	return manifest
}

// loadLookbackManifest is the sp-ed4i deadhead fix at the reposition seam: BEFORE the
// margins-death jump from fromSystem to the chosen destination, buy the best floor-clearing
// manifest of fromSystem exports the destination imports, so the crossing carries value
// instead of flying empty. The post-jump re-plan liquidates the load as launch cargo
// (sp-m5kv held-liquidation) at the destination's live import bids.
//
// It reuses the shared trade-route buy primitives (travel/dock/observeGood/reserveHeadroom/
// purchaseWithCeiling) so every existing money guard applies UNTOUCHED (RULINGS #4): the
// hold cap, the tour's max-spend cap, the working-capital reserve at buy time (sp-agzj), and
// the live-ask ceiling (sp-9mkf) — the ceiling is margin-preserving (never above
// destBid-floor), so even a drifted live ask can only buy at a price that still clears the
// min-margin against the cached sink bid. It BOOKS each buy into netBought / response so the
// stranded-cargo veto and the run economics stay honest, and it never FORCES a buy: an
// unreadable balance, a ceiling abort, or no floor-clearing lane simply loads less (or
// nothing) and the jump proceeds. Returns the total units loaded (0 = an empty jump).
//
// Best-effort throughout: a listings read error, a nav/dock/observe failure, or a purchase
// error on one good is logged and skipped — the reposition rescue is the primary goal and
// must never be blocked by an opportunistic load. Cargo actually bought before such a skip
// still rides the jump (booked in netBought) and liquidates at the destination.
func (h *RunTourCoordinatorHandler) loadLookbackManifest(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	fromSystem, toSystem string,
	maxSpend, reserve int64,
) int {
	logger := common.LoggerFromContext(ctx)
	now := h.clock.Now()

	srcRaw, serr := h.legs.collectSystemListings(ctx, fromSystem, cmd.PlayerID)
	destRaw, derr := h.legs.collectSystemListings(ctx, toSystem, cmd.PlayerID)
	if serr != nil || derr != nil {
		logger.Log("INFO", fmt.Sprintf("Look-back: skipped (listings unreadable: from=%v dest=%v) - jumping empty", serr, derr), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "from_system": fromSystem, "to_system": toSystem,
		})
		return 0
	}
	src := freshListings(srcRaw, now, maxListingAge)
	dst := freshListings(destRaw, now, maxListingAge)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0
	}
	manifest := buildLookbackManifest(src, dst, ship.AvailableCargoSpace(), cmd.MinMargin)
	if len(manifest) == 0 {
		logger.Log("INFO", fmt.Sprintf("Look-back: no %s export clears the min-margin floor into a %s import sink (candidates src=%d dst=%d) - jumping empty", fromSystem, toSystem, len(src), len(dst)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "from_system": fromSystem, "to_system": toSystem,
			"src_listings": len(src), "dst_listings": len(dst),
		})
		return 0
	}

	loaded := 0
	spentBefore := response.TotalSpent // realized look-back spend is the delta on response.TotalSpent
	var bought []string
	for _, item := range manifest {
		spent := response.TotalSpent - spentBefore
		if maxSpend > 0 && spent >= maxSpend {
			break // the tour's cumulative spend cap is exhausted (RULINGS #6)
		}
		units := h.buyLookbackItem(ctx, cmd, response, netBought, item, maxSpend-spent, maxSpend, reserve)
		if units <= 0 {
			continue
		}
		loaded += units
		bought = append(bought, fmt.Sprintf("%dx%s", units, item.Good))
	}

	if loaded == 0 {
		logger.Log("INFO", fmt.Sprintf("Look-back: manifest of %d lane(s) cleared the floor but bought nothing (guards/ceilings bound it) - jumping empty", len(manifest)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "from_system": fromSystem, "to_system": toSystem, "manifest_lanes": len(manifest),
		})
		return 0
	}
	logger.Log("INFO", fmt.Sprintf("Look-back: loaded %d unit(s) [%s] at %s for the jump to %s (deadhead avoided)", loaded, strings.Join(bought, ", "), fromSystem, toSystem), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "from_system": fromSystem, "to_system": toSystem,
		"units_loaded": loaded, "manifest": strings.Join(bought, ", "),
	})
	return loaded
}

// buyLookbackItem navigates to one manifest good's source waypoint, docks, and buys it under
// the full guard stack (RULINGS #4), returning the units actually bought (0 on any skip). It
// mirrors executeBuy's sizing and floor discipline but sources its prices from the cached
// manifest rather than a solver leg, and it re-verifies the live ask via a margin-preserving
// purchaseWithCeiling so a drifted ask can only trade at a price still clearing the floor.
func (h *RunTourCoordinatorHandler) buyLookbackItem(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	item lookbackItem,
	spendRemaining, maxSpend, reserve int64,
) int {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0
	}
	ship, err = h.legs.travel(ctx, ship, item.SourceWaypoint, cmd.PlayerID)
	if err != nil {
		logger.Log("INFO", fmt.Sprintf("Look-back: could not reach source %s for %s (%v) - skipping this good", item.SourceWaypoint, item.Good, err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": item.Good, "waypoint": item.SourceWaypoint,
		})
		return 0
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return 0
	}

	live, oerr := h.legs.observeGood(ctx, item.SourceWaypoint, item.Good, cmd.PlayerID)
	if oerr != nil {
		return 0 // no live price → cannot verify → don't buy (fail closed on the load)
	}
	liveAsk := live.SellPrice()
	if liveAsk <= 0 {
		return 0
	}

	units := item.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv
	}
	if maxSpend > 0 {
		if spendRemaining <= 0 {
			return 0
		}
		if affordable := int(spendRemaining / int64(liveAsk)); affordable < units {
			units = affordable
		}
	}
	if units <= 0 {
		return 0
	}

	// Working-capital spend floor at BUY time (sp-agzj / RULINGS #4): shrink the tranche to
	// what the reserve can still afford, skip if even one unit pierces it, and fail CLOSED
	// (no spend) if the live balance cannot be read. No live client wired → guard off
	// (the optional-port contract every nil-apiClient test relies on).
	headroom, liveBalance, guardOn, readable := h.legs.reserveHeadroom(ctx, int(reserve))
	if guardOn && !readable {
		logger.Log("WARNING", fmt.Sprintf("Look-back: live balance unreadable buying %s @ %d (reserve %d) - not spending (fail-closed)", item.Good, liveAsk, reserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": item.Good, "ask": liveAsk, "reserve": reserve,
		})
		return 0
	}
	if guardOn {
		floorMaxUnits := headroom / liveAsk
		if floorMaxUnits <= 0 {
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "skip")
			logger.Log("WARNING", fmt.Sprintf("Look-back: buy of %s @ %d would breach the working-capital floor (balance %d, reserve %d) - skipping this good", item.Good, liveAsk, liveBalance, reserve), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "good": item.Good, "ask": liveAsk, "live_balance": liveBalance, "reserve": reserve,
			})
			return 0
		}
		if floorMaxUnits < units {
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "shrink")
			units = floorMaxUnits
		}
	}

	// Margin-preserving live-ask ceiling (sp-9mkf): the buy may drift up to the tour's
	// price tolerance over the cached ask, but NEVER above destBid-floor — so even at the
	// ceiling the load still clears the min-margin against the cached sink bid. The sell
	// side is re-verified live at the destination by the post-jump re-plan.
	floor := cmd.MinMargin
	if floor < 1 {
		floor = 1
	}
	maxAsk := item.SourceAsk + item.SourceAsk*tourPriceTolerancePct/100
	if marginCeil := item.DestBid - floor; marginCeil < maxAsk {
		maxAsk = marginCeil
	}
	if maxAsk <= 0 {
		return 0
	}

	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, item.Good, units, cmd.PlayerID, maxAsk)
	if err != nil {
		logger.Log("INFO", fmt.Sprintf("Look-back: purchase of %d %s at %s failed (%v) - skipping this good", units, item.Good, item.SourceWaypoint, err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": item.Good, "waypoint": item.SourceWaypoint,
		})
		return 0
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("INFO", fmt.Sprintf("Look-back: ceiling aborted %s (live ask %d > ceiling %d) - skipping this good", item.Good, buyResp.CeilingObservedAsk, maxAsk), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": item.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAsk,
		})
		return 0
	}
	response.TotalSpent += int64(buyResp.TotalCost)
	response.TradesExecuted++
	netBought[item.Good] += buyResp.UnitsAdded
	logger.Log("INFO", fmt.Sprintf("Look-back: bought %d %s at %s (cost %d) for the jump", buyResp.UnitsAdded, item.Good, item.SourceWaypoint, buyResp.TotalCost), nil)
	return buyResp.UnitsAdded
}

// sortLookbackLanes orders lanes by capped spread desc, then raw per-unit spread desc, then
// good asc — RankSpreads' exact tie-break chain, so look-back packing prefers the same
// deep-then-fat lanes the in-system ranker would.
func sortLookbackLanes(lanes []trading.ArbitrageLane) {
	// Small n (goods tradeable across two systems); a simple insertion keeps the tie-break
	// identical to trading.RankSpreads without importing sort semantics that differ.
	for i := 1; i < len(lanes); i++ {
		for j := i; j > 0 && lookbackLess(lanes[j], lanes[j-1]); j-- {
			lanes[j], lanes[j-1] = lanes[j-1], lanes[j]
		}
	}
}

// lookbackLess reports whether a should rank BEFORE b (RankSpreads order).
func lookbackLess(a, b trading.ArbitrageLane) bool {
	if a.CappedSpread != b.CappedSpread {
		return a.CappedSpread > b.CappedSpread
	}
	if a.SpreadPerUnit != b.SpreadPerUnit {
		return a.SpreadPerUnit > b.SpreadPerUnit
	}
	return a.Good < b.Good
}
