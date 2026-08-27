package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// RESTART-RESUME OF THE IN-FLIGHT SELL LEG (RULINGS #2).
//
// A laden hull re-adopted mid-leg finishes the discharge it was already flying before it plans
// anything, on the same rule the reposition resume follows: write the destination into the
// container config the instant it is chosen, so a re-adopted run continues toward the SAME
// ground rather than re-deciding at whatever position the bounce left it in. The alternative is
// a hull sitting on cargo it has already paid for while a planner round trip it did not need
// decides where the cargo goes instead.
//
// WHAT IS PERSISTED IS THE MINIMUM THAT CANNOT BE RECOMPUTED: the sink waypoint, and which
// goods the leg is discharging there. Units come from the hull's own hold, the price and the
// traded volume from a live market read, and whether the leg is still worth flying from the
// current snapshot. A stored basis or unit count could only drift away from reads that already
// exist — which is also why staleness needs no age knob here: the leg is re-priced, not trusted.
//
// THE BUY SIDE IS DELIBERATELY NOT PERSISTED. Resuming a sell realises cargo already bought —
// the money is spent and the only open question is where it lands. Resuming a buy would commit
// fresh money against a price nobody re-verified, the direction RULINGS #4 forbids. So the tail
// of the tour is always re-planned against live prices; only the leg in flight is finished.

// TourLegState is the durable slice of the tour leg a hull is flying: the sink it is heading
// for and the goods it is carrying there. Persisted into the container config at leg start and
// re-read by the recovery rebuild. The zero value is the CLEAR — no leg in flight — which is
// what a leg that discharges nothing writes.
type TourLegState struct {
	// Waypoint is the leg's destination market. Empty clears the state.
	Waypoint string
	// Goods is the comma-joined, sorted set of goods this leg sells at Waypoint which the
	// hull was already holding when it departed. A single string because that is what the
	// container config stores and what the rebuild reads back; the coordinator splits it.
	Goods string
}

// TourLegStatePersister durably records the leg a hull is flying (keyed by container) so a
// restart-rebuilt run can finish it rather than idle through a re-plan first. The daemon backs
// this with the container config — the same map the recovery rebuild reads. Mirrors the
// RepositionStatePersister contract: a returned error is advisory (resume durability, never a
// spend or movement guard), so the caller logs it and keeps touring.
type TourLegStatePersister interface {
	PersistTourLegState(ctx context.Context, containerID string, playerID int, state TourLegState) error
}

// SetTourLegPersister wires the durable in-flight-leg store. Left unset (nil), nothing is
// written and a restart re-plans from the hull's current position exactly as before —
// fail-open, matching the sibling optional-port contract.
func (h *RunTourCoordinatorHandler) SetTourLegPersister(p TourLegStatePersister) {
	h.tourLegPersister = p
}

// persistTourLeg writes the leg in flight (or its clearing) into the container config.
// Best-effort: no persister wired (tests, pre-wiring) or no container id → a no-op; a
// persistence error is advisory, so it is logged and swallowed. Mirrors persistReposition.
func (h *RunTourCoordinatorHandler) persistTourLeg(ctx context.Context, cmd *RunTourCoordinatorCommand, state TourLegState) {
	if h.tourLegPersister == nil || cmd.ContainerID == "" {
		return
	}
	if err := h.tourLegPersister.PersistTourLegState(ctx, cmd.ContainerID, cmd.PlayerID, state); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to persist the in-flight tour leg (restart-resume durability degraded, run continues): %v", err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "waypoint": state.Waypoint, "error": err.Error(),
		})
	}
}

// legSellState derives the durable state for a leg about to be flown: the goods it SELLS at
// its waypoint which the hull is already carrying. A leg that discharges nothing the hull holds
// yields the zero value (the clear), so a leg already flown can never be resumed a second time.
//
// DEPOSIT tranches are excluded: a haul-to-storage transfer has no market bid to re-read, so it
// is not something the resume — which is a market sale — could finish.
func legSellState(leg routing.TourLeg, held map[string]int) TourLegState {
	goods := make([]string, 0, len(leg.Trades))
	seen := map[string]bool{}
	for _, trade := range leg.Trades {
		if trade.IsBuy || trade.IsDeposit || seen[trade.Good] || held[trade.Good] <= 0 {
			continue
		}
		seen[trade.Good] = true
		goods = append(goods, trade.Good)
	}
	if len(goods) == 0 {
		return TourLegState{}
	}
	sort.Strings(goods) // one leg, one deterministic string — the config value must not churn on map order
	return TourLegState{Waypoint: leg.Waypoint, Goods: strings.Join(goods, ",")}
}

// resumableGoods intersects the persisted goods with what the hull is actually holding now.
// The hold is the record: a good the config names but the hull no longer carries was already
// discharged, and one it carries that the leg never named belongs to the re-plan.
func resumableGoods(persisted string, held map[string]int) []string {
	var goods []string
	for _, good := range strings.Split(persisted, ",") {
		good = strings.TrimSpace(good)
		if good == "" || held[good] <= 0 {
			continue
		}
		goods = append(goods, good)
	}
	sort.Strings(goods)
	return goods
}

// resumeInFlightTourLeg finishes the sell leg the hull was flying when the daemon bounced:
// travel to the persisted sink, sell the goods that leg was carrying there, then clear the
// state and let the ordinary loop re-plan the tail from the new position.
//
// It DECLINES — leaving the run byte-identical to a plain re-plan — whenever the leg is not
// worth finishing: no persisted leg, an unreadable hull, nothing left aboard that the leg was
// carrying, unreadable markets, or no fresh positive non-EXPORT bid still standing at the sink.
// That last check is what makes an age knob unnecessary: the WAYPOINT is durable, "is it still
// worth flying to" is recomputed from the live snapshot on every resume.
//
// A non-nil error is a resumable travel/sell failure the runner retries; the persisted leg is
// left intact in that case so the retry resumes it again.
func (h *RunTourCoordinatorHandler) resumeInFlightTourLeg(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) error {
	if cmd.TourLegWaypoint == "" || cmd.TourLegGoods == "" {
		return nil
	}
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil // unreadable hull proves nothing — re-plan exactly as before
	}
	// tourShipState withholds reserved cargo, so a resume can never sell a staged module the
	// executor would refuse anyway.
	goods := resumableGoods(cmd.TourLegGoods, h.tourShipState(ship).Cargo)
	if len(goods) == 0 {
		h.persistTourLeg(ctx, cmd, TourLegState{})
		return nil // the leg already discharged, or the hull came up empty
	}

	held := map[string]int{}
	for _, good := range goods {
		held[good] = 1 // membership is all bestLocalDistressSinks reads; units come off the hull at sale
	}
	listings, lerr := h.legs.collectSystemListings(ctx, waypointSystem(cmd.TourLegWaypoint), cmd.PlayerID)
	if lerr != nil {
		h.persistTourLeg(ctx, cmd, TourLegState{})
		return nil // markets unreadable — never fly on an unreadable price (RULINGS #4)
	}
	// Same sink test the cash-recovery paths use, narrowed to the ONE waypoint the leg named:
	// a fresh listing with a positive, non-EXPORT bid. An exporter's bid is a sellback, never a
	// real buyer, and a stale row is not a price we will cross a system on.
	sinks := bestLocalDistressSinks(
		freshListings(listingsAt(listings, cmd.TourLegWaypoint), h.clock.Now(), h.listingMaxAge(ctx, cmd.PlayerID)),
		held,
	)
	if len(sinks) == 0 {
		h.persistTourLeg(ctx, cmd, TourLegState{})
		logger.Log("INFO", fmt.Sprintf("Tour leg resume declined: %s no longer bids for %s - re-planning from where the hull stands", cmd.TourLegWaypoint, strings.Join(goods, ", ")), map[string]interface{}{
			"action": "tour_leg_resume_declined", "ship_symbol": cmd.ShipSymbol,
			"waypoint": cmd.TourLegWaypoint, "goods": goods,
		})
		return nil
	}

	logger.Log("INFO", fmt.Sprintf("Tour leg resume: re-adopted mid-leg toward %s carrying %s after a restart - finishing the sell before re-planning (RULINGS #2)", cmd.TourLegWaypoint, strings.Join(goods, ", ")), map[string]interface{}{
		"action": "tour_leg_resume", "ship_symbol": cmd.ShipSymbol,
		"container_id": cmd.ContainerID, "waypoint": cmd.TourLegWaypoint, "goods": goods,
	})

	// Accumulate the sale into this sink's recovery shadow exactly as a plan leg does, so the
	// crush a resumed sale causes is visible to every other engine netting against this sink.
	// Nil when no ledger is wired.
	legSells := h.newLegSells()
	sold := false
	for _, good := range goods {
		sink, bids := sinks[good]
		if !bids {
			continue // this good's bid is gone; the re-plan finds it somewhere else
		}
		ok, serr := h.resumeSellGood(ctx, cmd, response, netBought, good, sink.waypoint, legSells)
		if serr != nil {
			// A partial discharge may already be booked; the persisted leg stays so the
			// runner's retry resumes what is left.
			return serr
		}
		sold = sold || ok
	}
	h.convertLegShadows(ctx, cmd, cmd.TourLegWaypoint, legSells)
	// Cleared whether or not the market took anything: the hull has now stood at the sink and
	// read it live, so a second restart must re-decide from there rather than re-fly this leg.
	h.persistTourLeg(ctx, cmd, TourLegState{})
	if sold {
		response.LegsExecuted++
		response.ResumedLegs++
	}
	return nil
}

// resumeSellGood discharges one good at the leg's sink: move there if the hull is not already
// standing on it, dock, and sell up to the market's traded volume. It books the revenue,
// discharges the purchase obligation so the honest-completion veto stays accurate, and records
// the sale in tour telemetry with a ZERO plan basis — the leg's projection was deliberately not
// persisted, and inventing one here would file a resumed sale as evidence about the market
// model (the exact ambiguity the engine column exists to remove).
//
// It re-reads the good LIVE at the dock (the same read a plan leg's execution makes) rather
// than trading on the cached listing that justified the flight: the cached row decides whether
// to GO, the live one decides what the sale is worth. Sell-side only — no buy guard is touched
// and no money is committed (RULINGS #4).
func (h *RunTourCoordinatorHandler) resumeSellGood(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	good string,
	waypoint string,
	legSells map[string]*tourSinkSale,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	if ship.IsCargoReserved(good) {
		return false, nil // do-not-sell (staged module / operator-protected)
	}
	if ship, err = h.moveToResumeSink(ctx, cmd, ship, waypoint); err != nil {
		return false, err
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return false, fmt.Errorf("tour leg resume dock of %s at %s failed: %w", cmd.ShipSymbol, waypoint, err)
	}

	live, oerr := h.legs.observeGood(ctx, waypoint, good, cmd.PlayerID)
	if oerr != nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg resume: no live price for %s at %s - carrying it into the re-plan: %v", good, waypoint, oerr), map[string]interface{}{
			"good": good, "waypoint": waypoint, "error": oerr.Error(),
		})
		return false, nil
	}
	if live.SellPrice() <= 0 {
		return false, nil // the bid vanished between the snapshot and the dock — carry it
	}
	units := 0
	if cargo := ship.Cargo(); cargo != nil {
		units = cargo.GetItemUnits(good)
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv // per-sale absorption cap, mirroring executeSell
	}
	if units <= 0 {
		return false, nil
	}

	plannedAt := h.clock.Now()
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("tour leg resume sell of %d %s at %s failed: %w", units, good, waypoint, err)
	}
	if sellResp.UnitsSold <= 0 {
		return false, nil
	}

	response.TotalRevenue += int64(sellResp.TotalRevenue)
	response.TradesExecuted++
	dischargePurchaseObligation(netBought, good, sellResp.UnitsSold)
	h.noteRecentSell(cmd.ShipSymbol, waypoint, good)
	h.noteSinkSale(legSells, good, sellResp.UnitsSold, live)
	h.recordLeg(ctx, cmd,
		trading.LegEngineResume,
		routing.TourLeg{Waypoint: waypoint},
		trading.ResumeLegIndex,
		routing.TourTrade{Good: good, IsBuy: false},
		sellResp.UnitsSold,
		realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold),
		plannedAt,
	)
	logger.Log("INFO", fmt.Sprintf("Tour leg resume: %s sold %d %s at %s for %d credits - the leg the restart interrupted is finished", cmd.ShipSymbol, sellResp.UnitsSold, good, waypoint, sellResp.TotalRevenue), map[string]interface{}{
		"action": "tour_leg_resume_sold", "ship_symbol": cmd.ShipSymbol, "good": good,
		"waypoint": waypoint, "units_sold": sellResp.UnitsSold, "revenue": sellResp.TotalRevenue,
	})
	return true, nil
}

// moveToResumeSink walks the hull to the leg's sink when it is not already standing there. The
// resume rides the ordinary travel machinery, which crosses gates and rides cooldowns, so a leg
// interrupted mid-crossing completes over the same route it was already flying.
func (h *RunTourCoordinatorHandler) moveToResumeSink(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	ship *navigation.Ship,
	waypoint string,
) (*navigation.Ship, error) {
	if ship.CurrentLocation() != nil && ship.CurrentLocation().Symbol == waypoint {
		return ship, nil
	}
	moved, err := h.legs.travel(ctx, ship, waypoint, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("tour leg resume move of %s to %s failed: %w", cmd.ShipSymbol, waypoint, err)
	}
	return moved, nil
}
