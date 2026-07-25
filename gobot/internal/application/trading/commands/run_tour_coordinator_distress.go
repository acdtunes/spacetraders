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

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

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
	sinks := bestLocalDistressSinks(freshListings(listings, h.clock.Now(), maxListingAge), held)
	if len(sinks) == 0 {
		return false, nil // no local buyer for any held good — distress cannot help
	}

	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods) // deterministic liquidation order (RULINGS #2)

	soldAny := false
	for _, good := range goods {
		sold, serr := h.distressSellGood(ctx, cmd, response, netBought, good, sinks[good])
		if serr != nil {
			// A partial dump may already be booked; return the resumable error so the runner
			// retries and the run re-plans cargo-aware from the lighter hold on the next pass.
			return false, serr
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

// distressSellGood liquidates one held good at its chosen current-system sink: move to the sink
// waypoint if the hull is not already there, dock, and sell up to the market's traded volume via
// the PLAIN floor-free sell() (RULINGS #4: sell-side cash recovery only, no buy guard touched).
// It books the recovered revenue, decrements netBought so the honest-completion stranded veto
// stays accurate (a good sold here is no longer aboard), and logs the deliberate below-floor
// recovery explicitly. Reserved cargo (a staged module / operator-protected good) is never sold
// (mirrors executeSell). It returns true only when units actually left the hull.
func (h *RunTourCoordinatorHandler) distressSellGood(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	good string,
	sink distressSink,
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
	logger.Log("WARNING", fmt.Sprintf("Distress liquidation (sp-2v69u): %s stuck laden with no fresh plan and no reachable sink - sold %d %s at %s for %d credits (bid %d/u, BELOW the profit floor) to free the hull; sunk-cost cash recovery, deliberate below-floor loss booked (sell-side only, RULINGS #4 buy guards untouched)", cmd.ShipSymbol, sellResp.UnitsSold, good, sink.waypoint, sellResp.TotalRevenue, sink.bid), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": good, "waypoint": sink.waypoint,
		"units_sold": sellResp.UnitsSold, "revenue": sellResp.TotalRevenue, "bid_per_unit": sink.bid,
		"action": "distress_liquidation", "bead": "sp-2v69u",
	})
	return true, nil
}
