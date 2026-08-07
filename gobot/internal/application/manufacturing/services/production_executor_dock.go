package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// productionDockConfirmAttempts bounds how many times NavigateAndDock will reload
// and re-issue a dock while waiting for the ship to reach a confirmed DOCKED
// state (arrival + persisted dock). Bounded so a wedged ship can never spin
// forever.
const productionDockConfirmAttempts = 10

// productionDockRetryLimit bounds how many times a cargo transaction that fails with a transient
// "must be docked" signal is re-docked and retried before the error is surfaced. Bounded so a
// genuinely undockable ship can never infinite-loop.
const productionDockRetryLimit = 3

// productionEmptyTrancheRetryLimit bounds how many times an input buy that comes back empty
// ("partial failure: ... 0 units processed" / API 400 — a market drained between the scout read and
// the buy) is retried before the tranche is skipped so the feeder run can continue. Bounded so a
// structurally-empty market can never infinite-loop.
const productionEmptyTrancheRetryLimit = 3

// productionEmptyTrancheRetryDelay is the backoff between empty-tranche retries,
// giving the market a chance to refill. It runs on the injected clock, so it is a
// no-op under the test clock.
const productionEmptyTrancheRetryDelay = 2 * time.Second

// onboardUnits sums how many units of good the ship currently holds.
func onboardUnits(ship *navigation.Ship, good string) int {
	units := 0
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == good {
			units += item.Units
		}
	}
	return units
}

// NavigateAndDock navigates to a waypoint and returns the ship only once it is
// CONFIRMED docked — the dock is actually persisted via the API, not merely
// flipped to DOCKED in memory.
func (e *ProductionExecutor) NavigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}
	if _, err := e.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", destination, err)
	}

	return e.dockAndConfirm(ctx, shipSymbol, destination, playerID)
}

// dockAndConfirm waits for the ship to arrive, issues a real (API-backed) dock,
// and returns only after re-reading a persisted DOCKED state. Bounded by
// productionDockConfirmAttempts so a wedged ship can never spin forever.
//
// Critically, it never acts on a ship it mutated in memory: each attempt reloads
// a fresh ship, and the dock is issued via a symbol-only DockShipCommand so the
// handler loads the true (IN_ORBIT) state and EnsureDocked reports a real change
// — otherwise the dock short-circuits to a no-op and the buy races an unpersisted
// dock.
func (e *ProductionExecutor) dockAndConfirm(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	var ship *navigation.Ship
	for attempt := 0; attempt < productionDockConfirmAttempts; attempt++ {
		reloaded, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
		ship = reloaded

		if ship.IsDocked() {
			return ship, nil // confirmed: persisted DOCKED
		}

		if ship.IsInTransit() {
			// Still travelling — wait for arrival, then re-read.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("dock wait cancelled: %w", ctx.Err())
			default:
				e.clock.Sleep(1 * time.Second)
			}
			continue
		}

		// Arrived and in orbit: issue a real dock. Pass ShipSymbol (nil Ship) so
		// DockShipHandler loads the true IN_ORBIT state, EnsureDocked reports a
		// change, and the API dock actually fires + persists.
		if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol,
			PlayerID:   playerID,
		}); err != nil {
			return nil, fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
		}
		// Honor cancellation between issuing the dock and re-reading; loop back
		// immediately to confirm the persisted state (no mandatory sleep on the
		// happy path).
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("dock wait cancelled: %w", err)
		}
	}

	if ship != nil && ship.IsInTransit() {
		return nil, fmt.Errorf("ship %s still in transit after %d attempts", shipSymbol, productionDockConfirmAttempts)
	}
	return nil, fmt.Errorf("ship %s did not reach a confirmed DOCKED state at %s after %d attempts", shipSymbol, destination, productionDockConfirmAttempts)
}

// isTransientDockStateError reports whether err is the recoverable "ship must be
// docked" signal — the local precondition error (cargo_transaction.go) or the
// API's 4214/4244 codes — rather than a genuine failure (insufficient funds, no
// cargo space, ...). Only these are safe to retry after re-docking.
func isTransientDockStateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "must be docked") ||
		strings.Contains(msg, "4214") ||
		strings.Contains(msg, "4244")
}

// isEmptyTrancheError reports whether err is the "bought nothing" signal from an
// input buy — the cargo handler's "partial failure: ... 0 units processed" wrapper
// (cargo_transaction.go), raised when the first tranche's API call fails because the
// market's supply was drained between the scout read and the buy (an empty /
// zero-volume tranche, surfaced by the API as a 400).
//
// A genuine funds shortfall also processes zero units and so carries the same phrase, but it is
// NOT an empty tranche and must surface as a real failure. It is excluded explicitly, so only a
// truly empty/zero-volume tranche is eligible for retry-then-skip.
func isEmptyTrancheError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "0 units processed") {
		return false
	}
	if strings.Contains(msg, "insufficient") {
		return false // genuine funds failure — must surface, never be silently skipped
	}
	return true
}

// purchaseWithDockRetry dispatches a PurchaseCargoCommand and, if it fails with a
// transient dock-state signal, reconciles the ship from the API (clearing any
// stale DOCKED cache entry that would make a re-dock a no-op — the subtlety
// NegotiateContractHandler documents), re-docks, and retries. Bounded by
// productionDockRetryLimit. A transient dock state must never crash the container; genuine
// failures surface immediately, unretried.
func (e *ProductionExecutor) purchaseWithDockRetry(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionDockRetryLimit; attempt++ {
		resp, err := e.mediator.Send(ctx, cmd)
		if err == nil {
			response, ok := resp.(*shipCargo.PurchaseCargoResponse)
			if !ok {
				return nil, fmt.Errorf("unexpected response type from purchase command")
			}
			return response, nil
		}

		if !isTransientDockStateError(err) {
			return nil, err // genuine failure — surface immediately
		}

		lastErr = err
		if attempt == productionDockRetryLimit {
			break
		}

		logger.Log("WARN", "Purchase hit a transient dock-state error; re-docking and retrying", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		if rerr := e.redockFromAPI(ctx, cmd.ShipSymbol, cmd.PlayerID); rerr != nil {
			return nil, fmt.Errorf("failed to re-dock after transient dock error: %w", rerr)
		}
	}

	return nil, fmt.Errorf("purchase still failing after %d dock retries: %w", productionDockRetryLimit, lastErr)
}

// purchaseInputWithEmptyTrancheGuard dispatches an input buy and survives an empty /
// zero-volume tranche instead of crashing the container (sp-q02m feeder crash #4).
//
// Dock-state transients are still absorbed by the inner purchaseWithDockRetry. If the
// buy comes back empty ("partial failure: ... 0 units processed" / API 400 — the
// market drained between the scout read and the buy), we bounded-retry in case the
// supply refills, then report a SKIP so the caller can continue with a zero-unit
// result rather than dying unrecoverably. Genuine failures (insufficient funds,
// no cargo space, exhausted dock retries) surface immediately.
//
// Returns:
//   - (resp, nil): a successful buy
//   - (nil,  nil): the tranche stayed empty across the retry bound — SKIP and continue
//   - (nil,  err): a genuine failure
func (e *ProductionExecutor) purchaseInputWithEmptyTrancheGuard(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionEmptyTrancheRetryLimit; attempt++ {
		// Honour container shutdown between attempts.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := e.purchaseWithDockRetry(ctx, cmd)
		if err == nil {
			return resp, nil
		}
		if !isEmptyTrancheError(err) {
			return nil, err // genuine failure — surface immediately, unretried
		}

		lastErr = err
		if attempt == productionEmptyTrancheRetryLimit {
			break
		}

		logger.Log("WARN", "Input buy hit an empty/zero-volume tranche; retrying in case supply refills", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		e.clock.Sleep(productionEmptyTrancheRetryDelay)
	}

	// The tranche stayed empty across the bound: report a skip so the feeder survives
	// (a permanently-empty market must not crash the container or infinite-loop).
	logger.Log("WARN", "Input tranche still empty after bounded retries — skipping to keep the feeder alive", map[string]interface{}{
		"ship":    cmd.ShipSymbol,
		"good":    cmd.GoodSymbol,
		"retries": productionEmptyTrancheRetryLimit,
		"error":   lastErr.Error(),
	})
	return nil, nil
}

// redockFromAPI reconciles the ship against the server (SyncShipFromAPI) so a
// stale DOCKED cache entry cannot make EnsureDocked a no-op, then issues a real
// dock via a symbol-only command. Mirrors the reactive re-dock in
// NegotiateContractHandler.
func (e *ProductionExecutor) redockFromAPI(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) error {
	if _, err := e.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		return fmt.Errorf("failed to refresh ship %s from API: %w", shipSymbol, err)
	}
	if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}); err != nil {
		return fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
	}
	return nil
}

// deliverInputs sells the hauled inputs to the factory the ship is docked at.
//
// Only goods this market actually takes are offered (marketBuys), and every good is
// independent: a refused sell is held aboard and the rest of the hold still delivers,
// and the revenue of whatever did sell is returned in full. Aborting the whole
// sourcing step on the first rejection poisons the hold — nothing reconciles a hold
// between lots, so the hull is disabled permanently.
//
// Nothing sellable is a no-op, not an error: it is indistinguishable from docking with an empty
// hold, and the production poll downstream carries its own bounds. Because every per-good failure
// is absorbed, the step has no failure mode of its own and returns no error. The operation context
// rides on ctx (stamped by ProduceGood).
//
// It reports both the revenue and the UNITS delivered. Units is what the gate FACTORY leg records
// against a starved factory: "sold something" and "sold 60 IRON" are different facts to an
// operator, and only the second one says whether the feed is keeping up.
//
// Units is summed from the SELL RESPONSE, never from the units offered. A market whose trade
// volume is below the offered lot fills it partially, and booking the offered figure would report
// a fed factory that is still starving — the "reported complete while nothing moved" class this
// codebase keeps rediscovering.
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) (revenue, units int) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0
	totalUnits := 0
	deliveredGoods := 0

	// The market the sell will actually transact against is the one the hull is docked
	// at, so the eligibility read is anchored to the ship's own location — the same
	// waypoint the cargo handler resolves its trade volume from.
	waypointSymbol := ship.CurrentLocation().Symbol
	listings, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID.Value())
	if err != nil || listings == nil {
		// FAIL CLOSED ON AN UNREADABLE ARRIVAL LISTING. This read is the SECOND of two,
		// at a different time from the first, and only this one can fail this way: the sp-b27a2
		// DEPARTURE guard (feedDestinationRefusedFor -> ValidateFeedDestination) already refuses to
		// fly to a destination whose listing will not read, and BOTH callers of this function run
		// it. So arriving with an unreadable listing is not a cold market — it is a TRANSIENT
		// failure in the window between departure and arrival. Narrow, but not unreachable.
		//
		// Offering nothing is the safe answer precisely because the filter below is the only thing
		// standing between the hold and the market's EXPORT bid. Withhold and the cargo rides one
		// more cycle and the next read decides; guess and a hull carrying the factory's OWN product
		// dumps it into that factory's export bid, laddering it down against us — the resale-sink
		// divergence the predicate exists to prevent. The costs are not symmetric, so the tie does
		// not go to the delivery.
		//
		// It is a NO-OP, not an error, exactly like a wholly-unsellable hold: the caller tolerates
		// a zero delivery (the fabricate path runs the factory on its own stock, the gate factory
		// leg defers and the next leg retries), and a full hull re-entering that leg now delivers
		// what it carries rather than parking.
		logger.Log("WARNING", fmt.Sprintf("Delivered nothing at %s: its market listing would not read on arrival, so there is no basis to judge what this market takes — holding the whole hold aboard rather than offering it blind, and retrying next cycle: %v", waypointSymbol, err), map[string]interface{}{
			"ship": ship.ShipSymbol(), "waypoint": waypointSymbol,
			"action": "delivery_withheld", "reason": "listing_unreadable",
		})
		return 0, 0
	}

	for _, item := range ship.Cargo().Inventory {
		if !marketBuys(listings, item.Symbol) {
			logger.Log("INFO", fmt.Sprintf("Holding %d units of %s aboard at %s — this market does not buy it", item.Units, item.Symbol, waypointSymbol), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol,
			})
			continue
		}

		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}

		sellResp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			// Cause in the MESSAGE: the container-log renderer drops metadata.
			logger.Log("WARNING", fmt.Sprintf("Could not deliver %d units of %s at %s — held aboard, delivering the rest of the hold: %v", item.Units, item.Symbol, waypointSymbol, err), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol, "error": err.Error(),
			})
			continue
		}

		response, ok := sellResp.(*shipCargo.SellCargoResponse)
		if !ok {
			logger.Log("WARNING", fmt.Sprintf("Unexpected response type delivering %s at %s — held aboard", item.Symbol, waypointSymbol), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol,
			})
			continue
		}

		totalRevenue += response.TotalRevenue
		totalUnits += response.UnitsSold
		deliveredGoods++

		logger.Log("INFO", fmt.Sprintf("Delivered input: %d units of %s (revenue: %d credits)", response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"input_good": item.Symbol,
			"units":      response.UnitsSold,
			"revenue":    response.TotalRevenue,
		})
	}

	if deliveredGoods == 0 && !ship.Cargo().IsEmpty() {
		logger.Log("WARNING", fmt.Sprintf("Delivered nothing at %s: none of the %d onboard good(s) are bought here — the hold rides on and production runs on factory stock", waypointSymbol, len(ship.Cargo().Inventory)), map[string]interface{}{
			"ship": ship.ShipSymbol(), "waypoint": waypointSymbol, "onboard_goods": len(ship.Cargo().Inventory),
		})
	}

	return totalRevenue, totalUnits
}

// marketBuys reports whether the market described by listings will take good off a
// hull: it must be listed there, as an IMPORT (a consumer) or an EXCHANGE (a trader) —
// the same sell-destination eligibility SellMarketDistributor applies. An EXPORT
// listing is the market's own product, and selling into it ladders its own bid down;
// that is the resale-sink divergence SellFabricatedOutputAtSink exists to prevent, so
// a factory's own output is never dumped back at the factory.
//
// A nil listing answers FALSE, because the filter's other job is to WITHHOLD: an EXPORT listing is
// the market's own bid, and dumping into it ladders that bid down against us, so nothing-to-read is
// not a reason to do the one thing the filter exists to prevent.
//
// That branch is a DEFENSIVE FALLBACK, NOT A LIVE GUARD, and the difference matters to anyone
// auditing it: both callers reach the same answer before getting here — ValidateFeedDestination
// refuses a nil destination with a named error, and deliverInputs withholds the whole hold on an
// unreadable arrival read — so it is UNREACHABLE. It is kept rather than deleted because deleting
// it lets a future caller that forgets the check silently fail OPEN.
//
// A MUTATION PROBE ON THIS LINE SURVIVES BY DESIGN. Flipping it to `true` kills no test, because
// deliverInputs' early return means the nil never arrives. That mutant is not a coverage hole and
// must not be "fixed" by weakening the caller to reach it; the behaviour is pinned at
// deliverInputs, where it is live.
func marketBuys(listings *market.Market, good string) bool {
	if listings == nil {
		return false
	}
	tradeGood := listings.FindGood(good)
	if tradeGood == nil {
		return false
	}
	return tradeGood.TradeType() != market.TradeTypeExport
}

// freeCargoSpace sells whatever is currently in the ship's hold at its current
// docked market so a full hold does not block an input purchase.
// Best-effort like deliverInputs: an item this market doesn't import is skipped
// rather than aborting the whole attempt, since the goal here is only to make
// room, not to guarantee every item sells. It offers the hold unfiltered — a
// rejection costs one call and the goal is space at any price — where
// deliverInputs pre-filters on the listing to avoid dumping into an export bid.
// Returns the reloaded ship reflecting whatever did sell.
//
// protectGood is a good this make-room path must NEVER sell here — the
// fabricated OUTPUT. The output is sold ONLY at the guard's resale sink
// (SellFabricatedOutputAtSink); dumping it at the current (factory/buy) market to
// make room is exactly the −258k MEDICINE incident, so the harvest path passes the
// output good and it is skipped. A parked sink therefore holds the output onboard
// instead of the next cycle's make-room silently dumping it. Empty string protects
// nothing (the input-buy path, which never carries the terminal product here).
func (e *ProductionExecutor) freeCargoSpace(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	protectGood string,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	if ship.Cargo().IsEmpty() {
		return nil, fmt.Errorf("hold reports full but carries no inventory (capacity %d) — nothing to unload", ship.Cargo().Capacity)
	}

	sold := 0
	for _, item := range ship.Cargo().Inventory {
		// Never dump the fabricated output at the current/buy market to make
		// room — it is sold only at the guard's resale sink. Skip it here.
		if protectGood != "" && item.Symbol == protectGood {
			logger.Log("INFO", fmt.Sprintf("Not unloading %d units of %s here to free space — the fabricated output is sold only at its resale sink, never dumped at the factory/buy market", item.Units, item.Symbol), map[string]interface{}{
				"good": item.Symbol,
				"ship": ship.ShipSymbol(),
			})
			continue
		}
		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}
		resp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Could not unload %s to free cargo space — market may not import it", item.Symbol), map[string]interface{}{
				"good":  item.Symbol,
				"ship":  ship.ShipSymbol(),
				"error": err.Error(),
			})
			continue
		}
		response, ok := resp.(*shipCargo.SellCargoResponse)
		if !ok {
			continue
		}
		sold += response.UnitsSold
		logger.Log("INFO", fmt.Sprintf("Unloaded %d units of %s to free cargo space", response.UnitsSold, item.Symbol), map[string]interface{}{
			"good":     item.Symbol,
			"quantity": response.UnitsSold,
			"revenue":  response.TotalRevenue,
		})
	}

	if sold == 0 {
		return nil, fmt.Errorf("market would not buy any of the %d onboard item(s)", len(ship.Cargo().Inventory))
	}

	reloaded, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after unloading cargo: %w", err)
	}
	return reloaded, nil
}
