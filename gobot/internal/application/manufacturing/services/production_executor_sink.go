package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// minOutputSellMarginFactor is the bid>=basis loss floor enforced on every
// fabricated-OUTPUT sale. The harvested product is sold at the resale
// sink the chain-margin guard priced only if that sink's live bid is at least the
// unit basis — the factory ask we paid to harvest — times this factor. 1.0 = strict
// breakeven: the output leg never realizes a loss. It is the last-line backstop to
// the chain-margin guard and the bp6f #3 crushed-sink harvest guard, checked
// at the actual point of the output sale where the sink bid may have decayed since
// production started (the −258k MEDICINE incident: guard cleared vs sink A1@5,248, the
// worker instead dumped the output at the factory's own ~1,560 bid). Below the floor
// the output is HELD (parked), never dumped. Tunable per ruling #5; kept at breakeven
// so a healthy sink is never over-restricted.
const minOutputSellMarginFactor = 1.0

// SetConstructionRepo wires the construction supply API the DeliverToConstructionSite terminal
// delivers gate materials through. The daemon calls this when it builds the executor
// for the construction-supply drain; leaving it unset keeps the terminal unavailable, which is
// exactly what every non-construction caller (goods factory, tour, arb) wants.
func (e *ProductionExecutor) SetConstructionRepo(repo manufacturing.ConstructionSiteRepository) {
	e.constructionRepo = repo
}

// SellFabricatedOutputAtSink binds the fabricated OUTPUT sale to the resale sink the
// chain-margin guard priced — NEVER the factory/buy market — closing the
// guard-vs-execution divergence that bled −258k on MEDICINE: the guard
// cleared a chain against sink A1@5,248 while execution accumulated the output at the
// factory D39 and dumped it THERE via the make-room path, laddering D39's own bid down
// to ~1,560 (far below the ~3,100 harvest cost) and re-buying.
//
// The sink is re-derived LIVE at sell time via the IDENTICAL MarketLocator.FindImportMarket
// call the guard and the bp6f #3 harvest guard use, so execution sells where the guard
// planned rather than at the ship's current market. It enforces the bid>=basis loss floor
// (minOutputSellMarginFactor): if no sink can be priced, or the sink's live bid is below
// the unit basis (the factory ask we paid to harvest) times the floor, the output is HELD
// onboard (parked, zero sold) with the numbers in the message text — the fabricated good
// is retried on a later pass, never dumped at a loss. It never falls back to the current market.
//
// Only the FINAL product is sold this way (the coordinator calls it for the root
// fabrication node on a resale run); an intermediate feed is delivered to its parent fab
// and inputs-only leaves the output in factory stock, so both skip this leg. It also
// drains any output a prior parked sell left onboard, so a recovered sink clears the
// backlog. Returns realized sell revenue (0 when parked).
func (e *ProductionExecutor) SellFabricatedOutputAtSink(
	ctx context.Context,
	shipSymbol string,
	good string,
	unitBasis int, // credits paid per unit to harvest (the factory ask) — the loss-floor basis
	systemSymbol string,
	playerID shared.PlayerID,
	opContext *shared.OperationContext,
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	// Units of the output actually onboard right now — this cycle's fresh harvest plus
	// any the ship still carries from a prior parked sell.
	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to reload ship before output sale: %w", err)
	}
	units := onboardUnits(ship, good)
	if units <= 0 {
		return 0, nil // nothing harvested to sell (e.g. inputs-only, or a skipped harvest)
	}

	sink := e.solventResaleSink(ctx, good, systemSymbol, units, unitBasis, playerID)
	if sink == nil {
		return 0, nil
	}

	// Fly the sell leg to the sink and sell THERE. Factory legs are in-system by design,
	// so this is a NavigateAndDock (never a jump).
	docked, err := e.NavigateAndDock(ctx, shipSymbol, sink.WaypointSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to navigate to resale sink %s for %s: %w", sink.WaypointSymbol, good, err)
	}

	sellUnits := onboardUnits(docked, good)
	if sellUnits <= 0 {
		return 0, nil
	}

	sellCmd := &shipCargo.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      sellUnits,
		PlayerID:   playerID,
	}
	resp, err := e.mediator.Send(ctx, sellCmd)
	if err != nil {
		return 0, fmt.Errorf("failed to sell %s at resale sink %s: %w", good, sink.WaypointSymbol, err)
	}
	sellResp, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return 0, fmt.Errorf("unexpected response type selling %s at resale sink %s", good, sink.WaypointSymbol)
	}

	logger.Log("INFO", fmt.Sprintf(
		"Sold %d %s at resale sink %s for %d credits (bid %d/u >= basis %d/u) — bound to the guard's sink, not the factory market",
		sellResp.UnitsSold, good, sink.WaypointSymbol, sellResp.TotalRevenue, sink.Price, unitBasis,
	), map[string]interface{}{
		"good": good, "units": sellResp.UnitsSold, "revenue": sellResp.TotalRevenue,
		"sink": sink.WaypointSymbol, "sink_bid": sink.Price, "basis": unitBasis,
	})
	return sellResp.TotalRevenue, nil
}

// solventResaleSink re-derives the resale sink LIVE — the same call the guard priced against — and
// enforces the bid>=basis loss floor on the sell dispatch. A vanished, unpriceable or crushed sink
// returns nil, which PARKS the output onboard: it is NEVER dumped at the current (factory/buy)
// market, which is exactly how a harvest turns into a realized loss.
func (e *ProductionExecutor) solventResaleSink(ctx context.Context, good, systemSymbol string, units, unitBasis int, playerID shared.PlayerID) *MarketLocatorResult {
	logger := common.LoggerFromContext(ctx)

	sink, err := e.marketLocator.FindImportMarket(ctx, good, systemSymbol, playerID.Value())
	if err != nil || sink == nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Holding %d %s onboard: no priceable resale sink (basis %d/u) — NOT selling at the factory/buy market, will retry next pass: %v",
			units, good, unitBasis, err,
		), map[string]interface{}{
			"action": "output_sell_parked", "reason": "no_sink", "good": good, "units": units, "basis": unitBasis,
		})
		return nil
	}

	floor := int(float64(unitBasis) * minOutputSellMarginFactor)
	if sink.Price < floor {
		logger.Log("WARNING", fmt.Sprintf(
			"Holding %d %s onboard: resale sink %s bid %d below loss floor %d (basis %d/u × %.2f) — parking, NOT dumping at the factory. stages[hold %s bid%d<floor%d]",
			units, good, sink.WaypointSymbol, sink.Price, floor, unitBasis, minOutputSellMarginFactor, good, sink.Price, floor,
		), map[string]interface{}{
			"action": "output_sell_parked", "reason": "bid_below_basis",
			"good": good, "units": units, "sink": sink.WaypointSymbol, "sink_bid": sink.Price, "basis": unitBasis, "floor": floor,
		})
		return nil
	}
	return sink
}

// DeliverToConstructionSite flies an ALREADY-SOURCED hauler to a jump-gate construction site and
// supplies whatever it carries of good via the construction supply API, returning the units the
// site accepted. It is the delivery TERMINAL of the construction-supply drain: the drain
// sources the material into the hull with ProduceGood (the shared engine — no duplicate sourcing
// logic), then hands off here to deliver. Modeled structurally on SellFabricatedOutputAtSink — the
// sale terminal's twin — reusing NavigateAndDock so a laden hull reaches a CONFIRMED-DOCKED state at
// the site before the supply fires, rather than resurrecting the deleted parallel coordinator's own
// navigation. Recovered from the acquire->navigate->supply->record leg of the sp-jav2-deleted
// DeliverToConstructionExecutor (ef2281b8), minus the acquire (now ProduceGood's job) and the
// task/pipeline bookkeeping (now the drain's job).
//
// A hull carrying nothing of good is a clean no-op (0 delivered, no flight) — the drain only reaches
// here after a non-zero ProduceGood, but the guard keeps a stale/empty hull from flying uselessly.
func (e *ProductionExecutor) DeliverToConstructionSite(
	ctx context.Context,
	shipSymbol string,
	good string,
	site string,
	playerID shared.PlayerID,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	if e.constructionRepo == nil {
		return 0, fmt.Errorf("construction repository not wired: cannot supply %s to %s", good, site)
	}

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to reload ship before construction delivery: %w", err)
	}
	if onboardUnits(ship, good) <= 0 {
		return 0, nil // nothing of this material onboard — nothing to deliver
	}

	// Fly the delivery leg to the site and dock. Construction legs are in-system by design, so
	// this is a NavigateAndDock (never a jump), returning only once CONFIRMED docked at the site.
	docked, err := e.NavigateAndDock(ctx, shipSymbol, site, playerID)
	if err != nil {
		// A navigate error does NOT imply the hull is elsewhere. The leg ends with work that
		// happens ONCE THE HULL HAS LANDED — a fuel top-up for its next trip, most of all — and
		// a transient failure there surfaces here as a navigate error while the hull is standing
		// on the site with the cargo aboard. Delivering costs no fuel and no flight, so giving
		// up at that point abandons a delivery that has already physically happened and carries
		// the material away again. Re-read position rather than inferring it from the error: if
		// the hull is on site, supply; otherwise the error stands.
		onSite, reloadErr := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if reloadErr != nil || onSite == nil || onSite.CurrentLocation() == nil || onSite.CurrentLocation().Symbol != site {
			return 0, fmt.Errorf("failed to navigate to construction site %s for %s: %w", site, good, err)
		}
		logger.Log("WARNING", fmt.Sprintf("Navigation to %s reported an error but %s is already on site - delivering anyway", site, shipSymbol), map[string]interface{}{
			"ship": shipSymbol, "construction_site": site, "good": good, "error": err.Error(),
		})
		docked = onSite
	}

	units := onboardUnits(docked, good)
	if units <= 0 {
		return 0, nil // arrived empty (e.g. cargo shed en route) — nothing to supply
	}

	result, err := e.constructionRepo.SupplyMaterial(ctx, shipSymbol, site, good, units, playerID.Value())
	if err != nil {
		// Surface the underlying supply error VERBATIM in the message so it reaches the container
		// log stream (structured map fields are dropped by the renderer). Recovered from the
		// deleted executor's supply-error handling.
		logger.Log("ERROR", fmt.Sprintf("Construction supply failed for %s at %s: %v", good, site, err), map[string]interface{}{
			"ship": shipSymbol, "construction_site": site, "good": good, "units": units,
		})
		return 0, fmt.Errorf("failed to supply construction site %s with %s: %w", site, good, err)
	}

	logger.Log("INFO", fmt.Sprintf("Supplied %d %s to construction site %s", result.UnitsDelivered, good, site), map[string]interface{}{
		"ship": shipSymbol, "construction_site": site, "good": good, "units_delivered": result.UnitsDelivered,
	})

	e.writeBackDeliveredCargo(ctx, shipSymbol, good, result.UnitsDelivered, playerID)
	return result.UnitsDelivered, nil
}

// writeBackDeliveredCargo decrements the daemon's cached hold by what the server just accepted, via
// the single-writer CAS path (RULINGS #3), so the NEXT drain tick reads the REAL emptied hull rather
// than PHANTOM cargo it would re-route to re-deliver.
//
// Idempotent under CAS re-apply: it removes only what the FRESH row still holds, so a concurrent
// writer or a `ship refresh` that already reconciled the hull leaves nothing to remove. Best-effort:
// the supply already committed server-side, so a failure is logged and the cache reconciles on the
// next sync.
func (e *ProductionExecutor) writeBackDeliveredCargo(ctx context.Context, shipSymbol, good string, delivered int, playerID shared.PlayerID) {
	if delivered <= 0 {
		return
	}
	if _, _, wbErr := e.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(sh *navigation.Ship) (bool, error) {
			cargo := sh.Cargo()
			if cargo == nil {
				return false, nil
			}
			have := cargo.GetItemUnits(good)
			if have <= 0 {
				return false, nil // already reconciled — no phantom to clear
			}
			remove := delivered
			if remove > have {
				remove = have // fresh row holds fewer than we delivered; strip only what is actually there
			}
			if err := sh.RemoveCargo(good, remove); err != nil {
				return false, err
			}
			return true, nil
		}); wbErr != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Post-supply cargo write-back failed for %s (%d %s) — cache may briefly show phantom until next sync: %v", shipSymbol, delivered, good, wbErr), map[string]interface{}{
			"ship": shipSymbol, "good": good, "units_delivered": delivered,
		})
	}
}
