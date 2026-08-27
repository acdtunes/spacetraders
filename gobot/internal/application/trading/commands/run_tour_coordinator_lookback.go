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
// and let the post-jump re-plan liquidate it as launch cargo (held-liquidation) at
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
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// lookbackLegIndex is the LegIndex stamped on a telemetry row for a look-back manifest
// buy (sp-rd21). It is NOT a solver-plan leg — it is an opportunistic pre-jump load at the
// reposition seam — so it carries a sentinel index rather than a 0..N plan position, making
// look-back buys greppable in tour_leg_telemetry while still reconciling with the
// PURCHASE_CARGO transactions they write.
//
// LegIndex IS NOW LOAD-BEARING, and the domain constant is the single source of truth for
// the sentinel. It was informational when only the netting readers consumed these rows (they
// group by good/tour and never by leg_index), but sp-fpgl2 made it the discriminator for
// PLAN BASIS: this index is what separates a cached-ask leg from a solver leg in both the
// drift metric's basis label and the sp-1ek0 graduation report. Changing it silently
// re-pools two populations that must not be averaged together.
const lookbackLegIndex = trading.LookbackLegIndex

// lookbackMinMarginDefault is the per-unit spread a look-back load must clear, for BOTH guards.
// Look-back packs the hold by capped spread, so depth wins and an unfloored bulk lane takes slots.
const lookbackMinMarginDefault = 33

func lookbackFloor(cmd *RunTourCoordinatorCommand) int {
	if cmd.LookbackMinMargin > 0 {
		return cmd.LookbackMinMargin
	}
	return lookbackMinMarginDefault
}

// lookbackSourceCallCreditsDefault is what one further SOURCE WAYPOINT has to add to a
// manifest, in credits of that manifest's own gross margin, to be worth the movement bundle
// it costs at a fully bound request budget — the same shared budget the tour selection
// surcharge prices, charged where a load decides how many markets to shop.
//
// SWEPT, NOT DERIVED, because the basis is optimistic: value here is undecayed spread x
// capped volume, before the live-ask re-verification, the reserve and the spend cap take
// their bites, so a charge reasoned from seconds-per-request onto realized credits lands on
// the wrong scale. TestReplay_LookbackSourceCharge rebuilds each reposition seam from the
// quotes readable at that instant and sweeps across it. The stopping rule is not the peak —
// credits-per-request improves well past here — but what a request is WORTH: above this a
// further step gives up more credits per request it frees than the fleet earns spending one.
//
// WATCH AND REVERT. The cost lands on UNITS: the hold loads less, at a better margin per
// unit. Revert if units per manifest fall while margin per unit does not rise.
const lookbackSourceCallCreditsDefault = 20_000

// lookbackVisitCharge resolves what a further source waypoint must earn for THIS load, from
// the armed price and how hard the request budget is actually binding.
//
// SATURATION SCALES IT, exactly as it scales the selection surcharge: at genuine headroom a
// request displaces nothing, so the charge is 0 and every source that adds value is bought.
// 0 IS THE FAIL-OPEN VALUE an unwired estimator, a thin window and a negative reading all
// land on, so there is one degrade path rather than three; a reading past the ceiling clamps;
// and a negative knob is the operator's disarm at every saturation.
func lookbackVisitCharge(cmd *RunTourCoordinatorCommand, saturationPermille int) int {
	price := lookbackSourceCallCreditsDefault
	if cmd.LookbackSourceCallCredits != 0 {
		price = cmd.LookbackSourceCallCredits
	}
	if price <= 0 || saturationPermille <= 0 {
		return 0
	}
	if saturationPermille > trading.APISaturationPermilleMax {
		saturationPermille = trading.APISaturationPermilleMax
	}
	return price * saturationPermille / trading.APISaturationPermilleMax
}

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

// filterBlocklistedListings drops every GoodListing whose good is in the blocklist —
// the look-back mirror of filterBlocklistedCargo for the fresh-listing buy universe. Applied to
// the buy-source rows before the manifest is built, so a blocklisted noise good is never a
// look-back purchase (look-back only BUYS from src, so barring it as a buy source bars it
// entirely). An empty/nil blocklist is a true no-op (the SAME slice), keeping the default
// look-back byte-identical.
func filterBlocklistedListings(rows []trading.GoodListing, block map[string]bool) []trading.GoodListing {
	if len(block) == 0 {
		return rows
	}
	kept := make([]trading.GoodListing, 0, len(rows))
	for _, r := range rows {
		if block[r.Good] {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// lookbackSourcing is what the manifest builder needs to know about the REQUESTS a manifest
// will spend, as distinct from the credits it will earn. A manifest is flown one source at a
// time — navigate, dock, orbit, then the buys — so its request bill is governed by how many
// DISTINCT waypoints it spans, and neither fact below is readable from listings.
type lookbackSourcing struct {
	// StandWaypoint is where the hull is at reposition-commit time. Sourcing there costs no
	// movement bundle — the navigate short-circuits at the destination and EnsureDocked is a
	// no-op on a docked hull. Empty means no free stop.
	StandWaypoint string
	// VisitCharge is the manifest value a FURTHER source must add to earn its movement bundle.
	// 0 prices no visit and keeps every source that adds value.
	VisitCharge int
}

// lookbackSrcRow is one buyable (waypoint, good) quote in the departure system.
type lookbackSrcRow struct {
	ask    int
	volume int
}

// lookbackDestRow is the best import sink for a good in the destination system.
type lookbackDestRow struct {
	bid    int
	volume int
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
// WHICH WAYPOINTS THE MANIFEST SPANS is the question this function also answers, because
// nothing else in the look-back path does. Taking each good's cheapest ask INDEPENDENTLY
// leaves the source set a by-product of per-good ask minimisation, and each waypoint it lands
// on costs a full movement bundle for the one or two goods it carries. So the set is PRUNED
// once priced: a source is dropped while the value LOST by re-sourcing its goods at the
// survivors is under sourcing.VisitCharge, cheapest loss first, never the free standing
// waypoint and never the last source; survivors come back GROUPED by waypoint so the flight
// docks each once rather than wherever the capped-spread order interleaves two.
//
// Pruning rather than admitting is what makes the disarmed path exact: the hold-capped pack
// is itself greedy, so a deep thin lane can displace a shallow rich one and make a LARGER
// source set worth less, which a forward loop would stop early on and quietly re-rank the
// manifest at a zero charge.
//
// Returns nil when no good clears the floor or the hold is zero — the caller then jumps
// empty exactly as today.
func buildLookbackManifest(src, dest []trading.GoodListing, holdCap, minMargin int, sourcing lookbackSourcing) []lookbackItem {
	if holdCap <= 0 {
		return nil
	}
	floor := minMargin
	if floor < 1 {
		floor = 1 // mirror the solver's max(1, min_margin) — a zero floor still bars a zero spread
	}

	// Every buyable quote, kept per waypoint: which waypoints a good can be sourced at is
	// exactly the choice the pruning makes, so it cannot be collapsed up front.
	byWaypoint := map[string]map[string]lookbackSrcRow{}
	for _, l := range src {
		if l.Ask <= 0 {
			continue
		}
		rows, ok := byWaypoint[l.Waypoint]
		if !ok {
			rows = map[string]lookbackSrcRow{}
			byWaypoint[l.Waypoint] = rows
		}
		if cur, seen := rows[l.Good]; !seen || l.Ask < cur.ask {
			rows[l.Good] = lookbackSrcRow{ask: l.Ask, volume: l.Volume}
		}
	}

	// Best (highest-bid) IMPORT sink per good in the destination system.
	bestDest := map[string]lookbackDestRow{}
	for _, l := range dest {
		if l.Bid <= 0 {
			continue
		}
		if l.TradeType == lookbackExportType {
			continue // sp-9mkf: an exporter's bid is not an import sink
		}
		if cur, ok := bestDest[l.Good]; !ok || l.Bid > cur.bid {
			bestDest[l.Good] = lookbackDestRow{bid: l.Bid, volume: l.Volume}
		}
	}

	admitted := make([]string, 0, len(byWaypoint))
	for waypoint := range byWaypoint {
		admitted = append(admitted, waypoint)
	}
	items, value := packLookbackFrom(byWaypoint, bestDest, admitted, holdCap, floor, sourcing.StandWaypoint)
	admitted = pruneLookbackSources(byWaypoint, bestDest, admitted, holdCap, floor, value, sourcing)
	if len(admitted) != len(byWaypoint) {
		items, _ = packLookbackFrom(byWaypoint, bestDest, admitted, holdCap, floor, sourcing.StandWaypoint)
	}
	if len(items) == 0 {
		return nil
	}
	return groupLookbackByWaypoint(items, sourcing.StandWaypoint)
}

// pruneLookbackSources drops source waypoints that cannot earn the movement bundle they cost,
// cheapest loss first. A non-positive charge prices no visit and returns the admitted set
// untouched — the single degrade path every unreadable, thin-window, slack-budget and
// operator-disarmed caller lands on.
//
// IT NEVER EMPTIES THE MANIFEST. Whether to carry anything across the jump is already decided
// by the min-margin floor and the guards below it, and a crossing that loads nothing is the
// deadhead look-back loading exists to end. So a drop leaving no items is refused however
// cheap, and this charge only answers the question it was posed: how many markets to shop.
func pruneLookbackSources(
	byWaypoint map[string]map[string]lookbackSrcRow,
	bestDest map[string]lookbackDestRow,
	admitted []string,
	holdCap, floor, value int,
	sourcing lookbackSourcing,
) []string {
	if sourcing.VisitCharge <= 0 {
		return admitted
	}
	kept := append([]string(nil), admitted...)
	trial := make([]string, 0, len(kept))
	for len(kept) > 0 {
		dropIdx, dropLoss := -1, 0
		for i, waypoint := range kept {
			if waypoint == sourcing.StandWaypoint {
				continue // a free stop saves nothing when dropped
			}
			trial = trial[:0]
			trial = append(trial, kept[:i]...)
			trial = append(trial, kept[i+1:]...)
			items, without := packLookbackFrom(byWaypoint, bestDest, trial, holdCap, floor, sourcing.StandWaypoint)
			if len(items) == 0 {
				continue // the last source standing: dropping it is a deadhead, not a saving
			}
			loss := value - without
			// Waypoint asc on an equal loss so one board always yields one manifest.
			if dropIdx < 0 || loss < dropLoss || (loss == dropLoss && waypoint < kept[dropIdx]) {
				dropIdx, dropLoss = i, loss
			}
		}
		if dropIdx < 0 || dropLoss >= sourcing.VisitCharge {
			break
		}
		kept = append(kept[:dropIdx], kept[dropIdx+1:]...)
		value -= dropLoss
	}
	return kept
}

// betterLookbackSource reports whether sourcing a good at waypoint beats sourcing it at cur.
// The ask decides it; on an EQUAL ask the hull's standing waypoint wins, because the same
// price bought where the hull already stands costs no movement bundle at all. Waypoint asc
// settles the rest, so one board always yields one manifest.
func betterLookbackSource(waypoint string, row lookbackSrcRow, curWaypoint string, cur lookbackSrcRow, standWaypoint string) bool {
	if row.ask != cur.ask {
		return row.ask < cur.ask
	}
	if standWaypoint != "" && (waypoint == standWaypoint) != (curWaypoint == standWaypoint) {
		return waypoint == standWaypoint
	}
	return waypoint < curWaypoint
}

// packLookbackFrom fills the hold from the admitted source waypoints only: each good at its
// best admitted source, floor-cleared, ranked by capped spread and greedily packed. The value
// it returns is the manifest's gross margin in credits — the quantity a further waypoint's
// movement bundle is priced against.
func packLookbackFrom(
	byWaypoint map[string]map[string]lookbackSrcRow,
	bestDest map[string]lookbackDestRow,
	admitted []string,
	holdCap, floor int,
	standWaypoint string,
) ([]lookbackItem, int) {
	type sourced struct {
		waypoint string
		row      lookbackSrcRow
	}
	best := map[string]sourced{}
	for _, waypoint := range admitted {
		for good, row := range byWaypoint[waypoint] {
			cur, ok := best[good]
			if ok && !betterLookbackSource(waypoint, row, cur.waypoint, cur.row, standWaypoint) {
				continue
			}
			best[good] = sourced{waypoint: waypoint, row: row}
		}
	}

	lanes := make([]trading.ArbitrageLane, 0, len(best))
	for good, s := range best {
		d, ok := bestDest[good]
		if !ok {
			continue
		}
		spread := d.bid - s.row.ask
		if spread < floor {
			continue
		}
		volumeCap := s.row.volume
		if d.volume < volumeCap {
			volumeCap = d.volume
		}
		if volumeCap <= 0 {
			continue
		}
		lanes = append(lanes, trading.ArbitrageLane{
			Good:           good,
			SourceWaypoint: s.waypoint,
			SourceAsk:      s.row.ask,
			DestBid:        d.bid,
			SpreadPerUnit:  spread,
			VolumeCap:      volumeCap,
			CappedSpread:   spread * volumeCap,
		})
	}
	if len(lanes) == 0 {
		return nil, 0
	}
	sortLookbackLanes(lanes)

	// Greedily pack the hold best-lane-first; a shallow single-tranche load per good.
	items := make([]lookbackItem, 0, len(lanes))
	remaining, value := holdCap, 0
	for _, l := range lanes {
		if remaining <= 0 {
			break
		}
		units := l.VolumeCap
		if units > remaining {
			units = remaining
		}
		items = append(items, lookbackItem{
			Good:           l.Good,
			SourceWaypoint: l.SourceWaypoint,
			Units:          units,
			SourceAsk:      l.SourceAsk,
			DestBid:        l.DestBid,
		})
		remaining -= units
		value += units * l.SpreadPerUnit
	}
	return items, value
}

// groupLookbackByWaypoint re-orders a packed manifest so every item at one source is
// contiguous, keeping the capped-spread rank WITHIN a source, so the flight pays one movement
// bundle per source instead of one per interleaving. The standing waypoint leads: it is free
// only while the hull has not left it, and buying there after a hop away costs the return
// navigate the co-location exists to avoid.
func groupLookbackByWaypoint(items []lookbackItem, standWaypoint string) []lookbackItem {
	order := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if item.SourceWaypoint == standWaypoint || seen[item.SourceWaypoint] {
			continue
		}
		seen[item.SourceWaypoint] = true
		order = append(order, item.SourceWaypoint)
	}
	if standWaypoint != "" {
		order = append([]string{standWaypoint}, order...)
	}

	grouped := make([]lookbackItem, 0, len(items))
	for _, waypoint := range order {
		for _, item := range items {
			if item.SourceWaypoint == waypoint {
				grouped = append(grouped, item)
			}
		}
	}
	if len(grouped) != len(items) {
		return items // every item's source is in the order above; never drop one
	}
	return grouped
}

// loadLookbackManifest is the sp-ed4i deadhead fix at the reposition seam: BEFORE the
// margins-death jump from fromSystem to the chosen destination, buy the best floor-clearing
// manifest of fromSystem exports the destination imports, so the crossing carries value
// instead of flying empty. The post-jump re-plan liquidates the load as launch cargo
// (held-liquidation) at the destination's live import bids.
//
// It reuses the shared trade-route buy primitives (travel/dock/observeGood/reserveHeadroom/
// purchaseWithCeiling) so every existing money guard applies UNTOUCHED (RULINGS #4): the
// hold cap, the tour's max-spend cap, the working-capital reserve at buy time, and
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
	maxAge := h.listingMaxAge(ctx, cmd.PlayerID)
	src := freshListings(srcRaw, now, maxAge)
	dst := freshListings(destRaw, now, maxAge)
	// Bar the effective blocklist from the look-back buy universe — the second tour
	// cargo-selection path, independent of the solver snapshot. No-op when it's empty, so the
	// default look-back is byte-identical.
	src = filterBlocklistedListings(src, h.effectiveCargoBlocklist(ctx, cmd.PlayerID))

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0
	}
	// The hull is standing somewhere when the reposition commits — normally the market its last
	// plan leg traded at. Sourcing there costs no movement bundle, so it is the one stop the
	// manifest gets for free, and every other source is priced against the request budget's
	// live pressure.
	sourcing := lookbackSourcing{
		StandWaypoint: ship.CurrentLocation().Symbol,
		VisitCharge:   lookbackVisitCharge(cmd, h.tourAPISaturation(ctx)),
	}
	manifest := buildLookbackManifest(src, dst, ship.AvailableCargoSpace(), lookbackFloor(cmd), sourcing)
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
	// Fresh per manifest item, so the bracket is never shared across purchases.
	dedupBracket := h.legs.startScanDedupBracket(ctx, cmd.ShipSymbol, cmd.PlayerID)
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
	dedupBracket = h.legs.confirmScanDedupArrival(dedupBracket)

	live, oerr := h.legs.observeGood(ctx, item.SourceWaypoint, item.Good, cmd.PlayerID)
	if oerr != nil {
		return 0 // no live price → cannot verify → don't buy (fail closed on the load)
	}
	liveAsk := live.PurchasePrice() // the ASK is purchase_price — what we pay (sp-en5h7)
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

	// Working-capital spend floor at BUY time (RULINGS #4): shrink the tranche to
	// what the reserve can still afford, skip if even one unit pierces it, and fail CLOSED
	// (no spend) if the live balance cannot be read. No live client wired → guard off
	// (the optional-port contract every nil-apiClient test relies on).
	headroom, liveBalance, guardOn, readable := h.legs.reserveHeadroom(ctx, cmd.PlayerID, int(reserve))
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
	floor := lookbackFloor(cmd)
	maxAsk := item.SourceAsk + item.SourceAsk*tourPriceTolerancePct/100
	if marginCeil := item.DestBid - floor; marginCeil < maxAsk {
		maxAsk = marginCeil
	}
	if maxAsk <= 0 {
		return 0
	}

	plannedAt := h.clock.Now()
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, item.Good, units, cmd.PlayerID, maxAsk, dedupBracket)
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
	// Record the look-back buy in tour telemetry exactly as
	// executeBuy records a plan leg — the FULL bought units and the volume-weighted realized
	// price — so the windowed telemetry-netting rate reconciles with the PURCHASE_CARGO
	// transactions this buy just wrote. Drop this record and look-back manifest buys are the ~1/3 of
	// buy legs silently absent from telemetry (their destination SELLS still logged as
	// launch-liquidation legs), the dominant cause of the ~2x realized-$/hr inflation. A
	// synthetic single-stop leg carries the source waypoint; item.Units/SourceAsk are the plan
	// basis (mirroring trade.Units/ExpectedUnitPrice) and lookbackLegIndex marks it a
	// reposition-manifest buy rather than a solver leg. Guarded on UnitsAdded>0 so a
	// zero-unit no-op writes no row — the same 1:1-with-transactions contract executeBuy keeps
	// (recordCargoTransaction skips zero-amount rows).
	if buyResp.UnitsAdded > 0 {
		h.recordLeg(ctx, cmd,
			trading.LegEngineLookback,
			routing.TourLeg{Waypoint: item.SourceWaypoint},
			lookbackLegIndex,
			routing.TourTrade{Good: item.Good, IsBuy: true, Units: item.Units, ExpectedUnitPrice: item.SourceAsk},
			buyResp.UnitsAdded,
			realizedUnitPrice(buyResp.TotalCost, buyResp.UnitsAdded),
			plannedAt,
		)
	}
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
