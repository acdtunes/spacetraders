package commands

// run_tour_coordinator_distress.go — sp-2v69u TERTIARY: the last-resort backstop for a
// continuous-tour hull that is stuck LADEN and cannot be rescued any other way. It runs ONLY
// after BOTH the fresh-arb margins-death reposition (maybeReposition) AND the held-cargo offload
// (maybeOffloadHeldCargo) have declined — i.e. there is no profitable fresh tour anywhere
// reachable AND no reachable OTHER-system sink that will absorb the held load. In that corner a
// laden hull would otherwise exit Completed=true STILL FULL (its cargo was pre-held on a
// relaunch, so the honest-completion stranded veto — scoped to cargo BOUGHT this run — never
// fires), the coordinator relaunches a new tour container on the still-full hull, the same
// infeasible zero-trade plan starves out, and the hull churns relaunch-forever full: the
// TORWIND-59 shape (225 PLATINUM_ORE, ~200/u sinks nowhere reachable).
//
// The escalation is a BOUNDED distress liquidation: sell the held cargo at the best AVAILABLE bid
// in the hull's CURRENT system — a ground the offload never even evaluates (its candidate set is
// the current system's jump NEIGHBORS via buildRepositionCandidates, never the current system
// itself) — EVEN BELOW the normal profit floor. The cargo is sunk cost; recovering partial
// capital and freeing the hull beats churning full forever. This is a controlled SELL-FLOOR
// BYPASS for already-owned cargo: it recovers cash, it NEVER spends, and it touches no buy guard
// (RULINGS #4 is a buy-side/spend guard, entirely unaffected — the plain sell() the executor
// uses is itself floor-free by construction, minBidPerUnit=0). The realized recovery is logged
// explicitly at WARNING: the below-floor loss is booked honestly.
//
// BOUNDED / no-thrash (RULINGS #2): it shares the SAME kill-switch (RepositionDisabled) and the
// SAME one-action-per-episode budget (episode.repositioned) the PRIMARY offload and the fresh-arb
// reposition use, so an episode fires at most ONE of {reposition, offload, distress} and a
// productive tour resets it. After a distress sale the hull is EMPTY (or below the laden
// threshold), so the next pass's laden gate declines and it cannot re-dump — the liquidation is
// deterministic and happens once per stuck episode. Gated on the stuck-laden CONDITION itself
// (cargo > 50% capacity, the shared isLadenForOffload), not a separate on/off flag: a healthy or
// unladen hull never crosses it, and a hull whose margins-death was rescued upstream never
// reaches here — so it is ARMED with nothing dormant to turn on (Admiral's ruling, as with the
// offload).

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// liquidationLegIndexBase is the LegIndex floor stamped on a telemetry row for a liquidation
// sale — the exit sweep and the margins-death distress dump both. Like the look-back
// buy's lookbackLegIndex sentinel it is NOT a solver-plan leg, and the two sit on opposite sides
// of the plan for the same reason: the visualizer's lane aggregator sorts a tour's legs by
// leg_index and draws a directed lane between CONSECUTIVE legs, so a sentinel's SIGN decides
// which way the hop is drawn. A look-back manifest buy happens before the plan and sorts first
// (-1); a liquidation happens after the plan is exhausted and must sort last, or its hop would be
// drawn backwards — a fabricated lane, which is worse than the missing one. The base sits far
// above any plan length (tours run a handful of legs), and the sweep offsets from it per SINK
// VISITED, not per sale, so goods dumped at one waypoint collapse into one leg exactly as
// several trades at one plan leg do.
//
// The literal now lives on the domain DTO (as lookbackLegIndex's does) because the
// persistence layer needs it to attribute rows written before the engine column existed —
// a second copy here would have been free to drift away from the one the backfill reads.
const liquidationLegIndexBase = trading.LiquidationLegIndexBase

// liquidationLegIndex hands out the telemetry leg position for a liquidation sweep. A sweep sells
// its goods one at a time and flies between sinks, so the unit of a "leg" is the SINK VISIT, not
// the sale: goods dumped at one waypoint share an index (the lane aggregator folds them into a
// single stop, as it does several trades at one plan leg), and moving to a different sink opens
// the next one. Only a sale that actually landed advances the position — a decline never moved
// the hull, so the next sink still departs from where it really is.
type liquidationLegIndex struct {
	idx  int
	last string // the sink the hull has actually sold at, "" before the first sale
}

func newLiquidationLegIndex() *liquidationLegIndex {
	return &liquidationLegIndex{idx: liquidationLegIndexBase}
}

// at returns the leg position a sale at waypoint would occupy.
func (l *liquidationLegIndex) at(waypoint string) int {
	if l.last != "" && waypoint != l.last {
		return l.idx + 1
	}
	return l.idx
}

// arrived commits a landed sale at waypoint, opening a new position if it was a new sink.
func (l *liquidationLegIndex) arrived(waypoint string) {
	l.idx = l.at(waypoint)
	l.last = waypoint
}

// distressSink is the chosen liquidation target for one held good: the CURRENT-system market
// waypoint with the highest non-EXPORT bid, the bid itself (for the honest below-floor log), and
// that market's traded volume (the per-sale absorption cap, mirroring executeSell's
// live.TradeVolume() cap so a single distress sale never over-dumps a shallow market in one call).
type distressSink struct {
	waypoint string
	bid      int
	volume   int
}

// bestLocalDistressSinks picks, for each held good, the CURRENT-system market with the highest
// non-EXPORT bid to dump it into. An EXPORT market's bid is a low sellback, never a real buyer
// (mirrors heldCargoSinkValue / bestLaneForGood's sink filter), and a non-positive bid is no
// buyer at all — both are skipped. A held good with no positive, non-EXPORT listing anywhere in
// the current system is ABSENT from the result: nothing local will buy it, so the distress pass
// has nothing to do for it.
func bestLocalDistressSinks(listings []trading.GoodListing, held map[string]int) map[string]distressSink {
	sinks := map[string]distressSink{}
	for _, listing := range listings {
		units, wanted := held[listing.Good]
		if !wanted || units <= 0 {
			continue
		}
		if listing.Bid <= 0 || listing.TradeType == lookbackExportType {
			continue
		}
		if current, ok := sinks[listing.Good]; !ok || listing.Bid > current.bid {
			sinks[listing.Good] = distressSink{waypoint: listing.Waypoint, bid: listing.Bid, volume: listing.Volume}
		}
	}
	return sinks
}

// listingsAt narrows a system's listings to a single waypoint — the "sell only where you already
// stand" restriction the exit sweep applies under the RepositionDisabled kill-switch.
func listingsAt(listings []trading.GoodListing, waypoint string) []trading.GoodListing {
	out := make([]trading.GoodListing, 0, len(listings))
	for _, listing := range listings {
		if listing.Waypoint == waypoint {
			out = append(out, listing)
		}
	}
	return out
}

// maybeDistressLiquidate is the sp-2v69u TERTIARY last resort (see the file header). It returns
// true when it liquidated at least one held good — the caller then resets the starvation streak
// and re-plans the now-lighter hull — and false (byte-identical to no distress pass) when there
// is nothing to do: the kill-switch is thrown, the episode's rescue was already spent, the hull
// is not laden, or nothing in the current system will buy any held good. A non-nil error is a
// resumable operational failure (a travel/dock/sell the runner should retry).
func (h *RunTourCoordinatorHandler) maybeDistressLiquidate(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	episode *repositionEpisode,
	netBought map[string]int,
) (bool, error) {
	if cmd.RepositionDisabled {
		return false, nil // shares the fresh-arb/offload kill-switch
	}
	if episode.repositioned {
		return false, nil // one rescue action per episode — no repeated dumping (RULINGS #2)
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	if !isLadenForOffload(ship.CargoUnits(), ship.CargoCapacity()) {
		return false, nil // not stuck-laden — nothing worth a distress dump
	}
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return false, nil // only reserved cargo aboard — nothing sellable
	}

	currentSystem := ship.CurrentLocation().SystemSymbol
	listings, err := h.legs.collectSystemListings(ctx, currentSystem, cmd.PlayerID)
	if err != nil {
		return false, nil // current-system markets unreadable — exit honestly, no distress (fail-open)
	}
	sinks := bestLocalDistressSinks(freshListings(listings, h.clock.Now(), h.listingMaxAge(ctx, cmd.PlayerID)), held)
	if len(sinks) == 0 {
		return false, nil // no local buyer for any held good — distress cannot help
	}

	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods) // deterministic liquidation order (RULINGS #2)

	soldAny := false
	legs := newLiquidationLegIndex()
	for _, good := range goods {
		sink := sinks[good]
		sold, serr := h.distressSellGood(ctx, cmd, response, netBought, good, sink, legs.at(sink.waypoint))
		if serr != nil {
			// A partial dump may already be booked; return the resumable error so the runner
			// retries and the run re-plans cargo-aware from the lighter hold on the next pass.
			return false, serr
		}
		if sold {
			legs.arrived(sink.waypoint)
		}
		soldAny = soldAny || sold
	}
	if !soldAny {
		return false, nil
	}
	episode.repositioned = true // spend the episode's single rescue action
	response.DistressLiquidations++
	return true, nil
}

// liquidateStrandBeforeExit is the terminal enforcement of the hold invariant: a hull is NEVER
// RELEASED holding cargo a bid in its current system will still take. Left unenforced, the
// container terminalizes, ContainerRunner.releaseShipAssignments frees the hull on EVERY exit
// reason, and the load is marooned on an idle hull — dead capacity, invisible until someone
// eyeballs cargo, and residue the next tour to claim that hull inherits. It runs from the
// honest-exit epilogue, so every terminal path — margins death, denied capital, iterations
// exhausted, a fail-open no-plan, a re-plan budget exhausted mid-leg — shares it; a resumable
// exit never reaches it (the runner retries, and the purchase obligation is carried forward).
//
// SCOPE: the hull's WHOLE sellable hold, not just this run's outstanding purchase
// obligation. The netBought scope was the defect. TORWIND-A sat IN_ORBIT at X1-KP23-D41 — the
// very market bidding 4,141/u for the 20 MICROPROCESSORS aboard — released idle by its tour with
// 82,820 credits of delivered, sellable load still in the hold, because the purchase ledger is
// in-memory on the daemon-lifetime handler and a restart between the buy and the exit empties it
// (adoptPurchaseObligation returns nothing, so both this sweep and the strand veto declined).
// Who bought the load is the wrong question at this point: the container is ENDING, the hull is
// about to be released, and "the next tour will sell it as launch inventory" is only true while
// the hull stays assigned — which it does not. maybeDistressLiquidate, three functions up, has
// always cleared the whole hold (tourShipState) for exactly this reason; the narrow scope here
// was an inconsistency, not a protection. Reserved cargo (MODULE_*/MOUNT_* hardware, an operator
// `ship reserve-cargo` override) is withheld by tourShipState and re-checked in distressSellGood
// — that, not "the tour didn't buy it", is the do-not-sell mechanism.
//
// Deliberately NOT gated on the stuck-laden threshold or the one-action-per-episode budget the
// margins-death rescues share: those bound how often a hull may JUMP chasing better ground, and
// this neither jumps between systems nor repeats — the run is ending either way, and one unit of
// cargo strands a hull exactly as a full hold does. (TORWIND-A's 20 units would not have crossed
// the 50% laden gate anyway.) Sell-side cash recovery only (RULINGS #4 buy guards untouched);
// every decline is fail-OPEN, and whatever could not be sold is reported by reportResidualHold
// below and — for tour-bought units — vetoed by the strand check that follows.
func (h *RunTourCoordinatorHandler) liquidateStrandBeforeExit(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) {
	if ctx.Err() != nil {
		return // a cancelled run is resumable — never trade on the way out of a shutdown
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return // an unreadable hull cannot be proven laden; the veto fails closed on the ledger
	}
	// Everything the hull is carrying that anyone may sell — reserved cargo already withheld.
	held := h.tourShipState(ship).Cargo
	if len(held) == 0 {
		return
	}
	listings, err := h.legs.collectSystemListings(ctx, ship.CurrentLocation().SystemSymbol, cmd.PlayerID)
	if err != nil {
		h.reportResidualHold(ctx, cmd, held, ship.CurrentLocation().Symbol)
		return
	}
	// The RepositionDisabled kill-switch means the operator opted this hull out of the stuck-hull
	// rescue machinery — chasing grounds, and dumping below the floor to escape a stuck state.
	// Honour it as "this hull does not MOVE": narrow the sweep to the waypoint it is already
	// standing on. That leaves the operator's contract intact (a hull under the switch is never
	// sent hunting for a buyer, and never dumps at a distant near-worthless bid) while still
	// refusing the release — TORWIND-A was parked ON its own sink, so the damning case
	// needs no movement at all.
	if cmd.RepositionDisabled {
		listings = listingsAt(listings, ship.CurrentLocation().Symbol)
	}
	sinks := bestLocalDistressSinks(freshListings(listings, h.clock.Now(), h.listingMaxAge(ctx, cmd.PlayerID)), held)
	if len(sinks) == 0 {
		// Nothing here bids on the load. The strand is real: report it loudly so it is
		// greppable and hand-recoverable, and let the veto fail the run on tour-bought units.
		h.reportResidualHold(ctx, cmd, held, ship.CurrentLocation().Symbol)
		return
	}

	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods) // deterministic liquidation order (RULINGS #2)

	logger := common.LoggerFromContext(ctx)
	logger.Log("WARNING", fmt.Sprintf("%s is exiting holding cargo with a live bid at %s - clearing the hold before release rather than stranding it (%v)", cmd.ShipSymbol, ship.CurrentLocation().SystemSymbol, goods), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "system": ship.CurrentLocation().SystemSymbol,
		"goods": goods, "action": "strand_liquidation",
	})
	legs := newLiquidationLegIndex()
	for _, good := range goods {
		sink := sinks[good]
		sold, serr := h.distressSellGood(ctx, cmd, response, netBought, good, sink, legs.at(sink.waypoint))
		if serr != nil {
			logger.Log("WARNING", fmt.Sprintf("Exit liquidation of %s aboard %s failed - the remaining units report as stranded: %v", good, cmd.ShipSymbol, serr), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "good": good, "error": serr.Error(),
			})
			break
		}
		if sold {
			legs.arrived(sink.waypoint)
			response.ExitHoldLiquidations++
		}
	}
	// Re-read the hull and name whatever the sweep could not clear (a good with no local bid, a
	// market that absorbed less than the hold, a failed sale). This is the ONLY report on cargo
	// the tour did not buy: the strand veto stays scoped to tour purchases so a hull handed an
	// unsellable load is not wedged in permanent failure.
	if after, aerr := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); aerr == nil {
		h.reportResidualHold(ctx, cmd, h.tourShipState(after).Cargo, after.CurrentLocation().Symbol)
	}
}

// reportResidualHold names the sellable cargo a hull is about to be RELEASED still carrying, so
// the strand is greppable rather than invisible until someone eyeballs cargo (the sp-8zhit harm
// that hid 129,845 credits across two hulls). Pure observation — it never trades and never alters
// a verdict. Silent on an empty hold, which is the clean path every healthy tour takes.
func (h *RunTourCoordinatorHandler) reportResidualHold(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	held map[string]int,
	waypoint string,
) {
	if len(held) == 0 {
		return
	}
	parts := make([]string, 0, len(held))
	for good, units := range held {
		parts = append(parts, fmt.Sprintf("%d %s", units, good))
	}
	sort.Strings(parts) // deterministic message (RULINGS #2)
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("%s is being released still holding %s at %s - nothing in this system bids on it; the load is stranded on an idle hull until it is re-tasked or hand-recovered", cmd.ShipSymbol, strings.Join(parts, ", "), waypoint), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "waypoint": waypoint,
		"residual_hold": parts, "action": "residual_hold_on_release",
	})
}

// distressSellGood liquidates one held good at its chosen current-system sink: move to the sink
// waypoint if the hull is not already there, dock, and sell up to the market's traded volume via
// the PLAIN floor-free sell() (RULINGS #4: sell-side cash recovery only, no buy guard touched).
// It books the recovered revenue, decrements netBought so the honest-completion stranded veto
// stays accurate (a good sold here is no longer aboard), and logs the deliberate below-floor
// recovery explicitly. Reserved cargo (a staged module / operator-protected good) is never sold
// (mirrors executeSell). It returns true only when units actually left the hull.
//
// legIdx is the telemetry leg position for the sink being sold at (see liquidationLegIndexBase).
func (h *RunTourCoordinatorHandler) distressSellGood(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	good string,
	sink distressSink,
	legIdx int,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	if ship.IsCargoReserved(good) {
		return false, nil // do-not-sell (staged module / operator-protected)
	}
	units := 0
	if cargo := ship.Cargo(); cargo != nil {
		units = cargo.GetItemUnits(good)
	}
	if sink.volume > 0 && sink.volume < units {
		units = sink.volume // per-sale market-absorption cap (mirrors executeSell's TradeVolume cap)
	}
	if units <= 0 {
		return false, nil
	}

	if ship.CurrentLocation().Symbol != sink.waypoint {
		if ship, err = h.legs.travel(ctx, ship, sink.waypoint, cmd.PlayerID); err != nil {
			return false, fmt.Errorf("distress liquidation move of %s to %s failed: %w", cmd.ShipSymbol, sink.waypoint, err)
		}
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return false, fmt.Errorf("distress liquidation dock of %s at %s failed: %w", cmd.ShipSymbol, sink.waypoint, err)
	}

	plannedAt := h.clock.Now() // stamped immediately before the sale, exactly as executeSell does
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("distress liquidation sell of %d %s at %s failed: %w", units, good, sink.waypoint, err)
	}
	if sellResp.UnitsSold <= 0 {
		return false, nil // the market took nothing — no progress, no booking
	}

	response.TotalRevenue += int64(sellResp.TotalRevenue)
	response.TradesExecuted++
	dischargePurchaseObligation(netBought, good, sellResp.UnitsSold) // left the hull — no longer a stranded-veto candidate
	// Record the liquidation in tour telemetry exactly as executeSell records a plan
	// leg, so this path stays 1:1 with the SELL_CARGO transactions it just wrote. It was the one
	// sell path that did not, and it cost a quarter of all tour sell legs (55 of 222 over 24h),
	// leaving the galaxy view's lane aggregation blind to every liquidation hop. A synthetic
	// single-stop leg carries the SINK waypoint — where the cargo actually changed hands, which
	// is what the lane must be drawn from. There is no solver plan behind a liquidation, so the
	// plan basis is left at zero rather than invented: the drift readers skip a non-positive
	// basis, so a fabricated one would silently corrupt planned-vs-realized. recordLeg is
	// nil-safe and swallows its own error into a WARNING, so a recording miss stays a recording
	// miss and never touches the sale (RULINGS #4).
	h.recordLeg(ctx, cmd,
		trading.LegEngineLiquidation,
		routing.TourLeg{Waypoint: sink.waypoint},
		legIdx,
		routing.TourTrade{Good: good, IsBuy: false},
		sellResp.UnitsSold,
		realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold),
		plannedAt,
	)
	logger.Log("WARNING", fmt.Sprintf("Distress liquidation: %s sold %d %s at %s for %d credits (bid %d/u, BELOW the profit floor) to free the hull; sunk-cost cash recovery, deliberate below-floor loss booked (sell-side only, RULINGS #4 buy guards untouched)", cmd.ShipSymbol, sellResp.UnitsSold, good, sink.waypoint, sellResp.TotalRevenue, sink.bid), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": good, "waypoint": sink.waypoint,
		"units_sold": sellResp.UnitsSold, "revenue": sellResp.TotalRevenue, "bid_per_unit": sink.bid,
		"action": "distress_liquidation", "bead": "sp-2v69u",
	})
	return true, nil
}
