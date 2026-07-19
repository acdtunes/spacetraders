// Package liquidation provides a standing, workflow-neutral mechanism for clearing
// stranded leftover cargo off an idle hull so it can re-enter candidacy.
//
// A hull ends a trading/contract workflow with leftover cargo aboard on many exit
// paths (sell-floor abort, margin abort, max-spend abort, error exits). The
// contract spawn filter (contract.FilterUnrelatedCargo) then parks
// any laden hull out of candidacy, so a fleet-wide crop of strands jams the contract
// pool to zero fulfillments. This package is the self-clearing leg: a one-shot worker
// container the contract coordinator spawns on each parked-with-cargo hull. It mirrors
// the manufacturing OrphanedCargoHandler doctrine (sell at the best in-system bid;
// jettison only as a last resort below a value floor) but as a per-hull worker rather
// than a task, so ANY coordinator can dispatch it. It is deliberately tiny and depends
// only on the ship repository, the market repository, and the mediator's existing
// navigate/dock/sell/jettison commands — it writes no new ship I/O (RULINGS: reuse).
package liquidation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// LiquidateCargoCommand asks the worker to clear every leftover lot off ShipSymbol,
// selling at the best in-system bid and — only when MinJettisonValue authorizes it —
// jettisoning genuinely stuck low-value cargo as a last resort.
type LiquidateCargoCommand struct {
	PlayerID   shared.PlayerID
	ShipSymbol string

	// MinJettisonValue is the value floor (bid * units, in credits) below which a lot
	// may be jettisoned as a LAST resort. 0 (the default) disables jettison entirely,
	// so nothing is ever destroyed without an explicit captain-set threshold: a lot
	// with no in-system bid is HELD, and a lot with a bid is always SOLD (value
	// recovered, never dumped — RULINGS #5). A positive value opts in: a lot
	// whose recoverable value is below it (including an unsellable lot, value 0) is
	// jettisoned to free the hull.
	MinJettisonValue int

	// CoordinatorID names the coordinator that spawned this worker as a managed
	// one-shot (twin of the worker_ferry pattern): persisted into the container config
	// so daemon restart recovery SKIPS it and leaves reclaim to the coordinator.
	CoordinatorID string
}

// LiquidateCargoResponse reports the disposition of the hull's hold. Because the
// worker is one-shot, it is observed only on completion.
type LiquidateCargoResponse struct {
	ShipSymbol      string
	UnitsSold       int
	TotalRevenue    int
	UnitsJettisoned int
	UnitsHeld       int // lots left aboard (unreachable/unsellable and jettison off)
}

// LiquidateCargoHandler executes the one-shot liquidation. Its dependencies are the
// same narrow set the manufacturing orphaned-cargo handler uses: the ship repository
// (server-truth reconcile), the market repository (best in-system bid), and the
// mediator (the existing navigate/dock/sell/jettison commands).
type LiquidateCargoHandler struct {
	shipRepo   navigation.ShipRepository
	marketRepo market.MarketRepository
	mediator   common.Mediator
}

// NewLiquidateCargoHandler wires the worker with its three driven ports.
func NewLiquidateCargoHandler(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	mediator common.Mediator,
) *LiquidateCargoHandler {
	return &LiquidateCargoHandler{shipRepo: shipRepo, marketRepo: marketRepo, mediator: mediator}
}

// Handle clears the hull's hold. A sync failure fails the container honestly (the
// runner releases the claim; the coordinator re-evaluates next pass) rather than
// acting on an unverifiable cargo snapshot. Every per-lot obstacle (no market,
// unreachable sink, sell/dock error) is a HOLD, not a container failure — holding
// protects value and lets the container exit cleanly so the hull is released.
func (h *LiquidateCargoHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*LiquidateCargoCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type %T", request)
	}
	logger := common.LoggerFromContext(ctx)

	// Stamp this worker's operation context so its SELL_CARGO row and every refuel the
	// sink-hop navigate fires inherit operation_type="liquidation" instead of the
	// 'manual' fallback: the navigate/sell legs are ctx-transparent (they record
	// whatever operation context rides the ctx). A worker spawned without a
	// CoordinatorID (direct/CLI) yields a nil context and stays 'manual'. Mirrors how
	// every sibling coordinator tags its writes at the boundary (arb/tour/stocker/…).
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.CoordinatorID, "liquidation"))

	// Reconcile against the server before touching cargo: a cached non-empty hold can
	// be a phantom foreign-cargo desync, and a lot count is about to drive real
	// sell/jettison I/O. Failing closed here is honest — better to fail the
	// container and retry than sell or dump against a state we cannot confirm.
	ship, err := h.shipRepo.SyncShipFromAPI(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("liquidation reconcile of %s failed: %w", cmd.ShipSymbol, err)
	}

	response := &LiquidateCargoResponse{ShipSymbol: cmd.ShipSymbol}
	cargo := ship.Cargo()
	if cargo == nil || cargo.IsEmpty() {
		logger.Log("INFO", fmt.Sprintf("Liquidation: %s holds nothing - already clear, re-entering candidacy", cmd.ShipSymbol), map[string]interface{}{
			"action":      "liquidation_noop_empty",
			"ship_symbol": cmd.ShipSymbol,
		})
		return response, nil
	}

	currentWaypoint := ""
	if loc := ship.CurrentLocation(); loc != nil {
		currentWaypoint = loc.Symbol
	}
	system := shared.ExtractSystemSymbol(currentWaypoint)

	// Snapshot the inventory before mutating it (each sell changes server hold state).
	lots := make([]*shared.CargoItem, 0, len(cargo.Inventory))
	for _, it := range cargo.Inventory {
		if it != nil && it.Units > 0 {
			lots = append(lots, it)
		}
	}

	for _, lot := range lots {
		good, units := lot.Symbol, lot.Units

		best, mErr := h.marketRepo.FindBestMarketBuying(ctx, good, system, cmd.PlayerID.Value())
		if mErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Liquidation: could not read best in-system bid for %d %s aboard %s: %v - treating as no market", units, good, cmd.ShipSymbol, mErr), map[string]interface{}{
				"action":      "liquidation_market_lookup_failed",
				"ship_symbol": cmd.ShipSymbol,
				"good":        good,
			})
			best = nil
		}

		hasBid := best != nil && best.PurchasePrice > 0
		recoverableValue := 0
		if hasBid {
			recoverableValue = best.PurchasePrice * units
		}
		// Jettison is a last resort, and only when the captain has set a floor: a lot
		// is dump-eligible only if its recoverable value is BELOW that floor (an
		// unsellable lot has value 0, so any positive floor makes it eligible). With
		// the default floor 0, nothing is ever eligible — value is never dumped.
		jettisonEligible := cmd.MinJettisonValue > 0 && recoverableValue < cmd.MinJettisonValue

		switch {
		case hasBid && !jettisonEligible:
			newWaypoint, sold, revenue, sellErr := h.sellLot(ctx, cmd, good, units, best.WaypointSymbol, currentWaypoint)
			currentWaypoint = newWaypoint
			if sellErr != nil {
				logger.Log("WARNING", fmt.Sprintf("Liquidation: holding %d %s aboard %s - could not sell at %s: %v", units, good, cmd.ShipSymbol, best.WaypointSymbol, sellErr), map[string]interface{}{
					"action":      "liquidation_hold_sell_failed",
					"ship_symbol": cmd.ShipSymbol,
					"good":        good,
					"sink":        best.WaypointSymbol,
				})
				response.UnitsHeld += units
				continue
			}
			response.UnitsSold += sold
			response.TotalRevenue += revenue
			if held := units - sold; held > 0 {
				response.UnitsHeld += held
			}
			logger.Log("INFO", fmt.Sprintf("Liquidation: sold %d %s off %s at %s (recovered %d cr)", sold, good, cmd.ShipSymbol, best.WaypointSymbol, revenue), map[string]interface{}{
				"action":      "liquidation_sold",
				"ship_symbol": cmd.ShipSymbol,
				"good":        good,
				"units_sold":  sold,
				"revenue":     revenue,
			})

		case jettisonEligible:
			if err := h.jettisonLot(ctx, cmd, good, units); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Liquidation: holding %d %s aboard %s - jettison failed: %v", units, good, cmd.ShipSymbol, err), map[string]interface{}{
					"action":      "liquidation_hold_jettison_failed",
					"ship_symbol": cmd.ShipSymbol,
					"good":        good,
				})
				response.UnitsHeld += units
				continue
			}
			response.UnitsJettisoned += units
			logger.Log("INFO", fmt.Sprintf("Liquidation: jettisoned %d %s off %s (last resort: recoverable value %d < floor %d)", units, good, cmd.ShipSymbol, recoverableValue, cmd.MinJettisonValue), map[string]interface{}{
				"action":      "liquidation_jettisoned",
				"ship_symbol": cmd.ShipSymbol,
				"good":        good,
				"units":       units,
				"value":       recoverableValue,
				"floor":       cmd.MinJettisonValue,
			})

		default:
			// No in-system bid and jettison disabled: protect the value, hold and log.
			response.UnitsHeld += units
			logger.Log("INFO", fmt.Sprintf("Liquidation: holding %d %s aboard %s - no in-system market bids it and jettison is off (min_jettison_value=0); value preserved", units, good, cmd.ShipSymbol), map[string]interface{}{
				"action":      "liquidation_hold_no_market",
				"ship_symbol": cmd.ShipSymbol,
				"good":        good,
				"units":       units,
			})
		}
	}

	return response, nil
}

// sellLot navigates the hull to the sink (a no-op when already there — the ladder's
// sell-in-place rung), docks, and sells the whole lot with NO floor (MinBidPerUnit=0):
// liquidation recovers sunk cost, so the bid-floor discipline that guards BUYS does not
// apply. A navigate/dock/sell error is returned so the caller holds the lot rather than
// forcing a dump; movement/fuel guards live inside NavigateRouteCommand and surface here
// as that error (RULINGS #4: costs respect guards; unaffordable-to-move => hold).
func (h *LiquidateCargoHandler) sellLot(
	ctx context.Context,
	cmd *LiquidateCargoCommand,
	good string,
	units int,
	sink string,
	currentWaypoint string,
) (newWaypoint string, sold int, revenue int, err error) {
	if sink != currentWaypoint {
		if _, navErr := h.mediator.Send(ctx, &navCmd.NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: sink,
			PlayerID:    cmd.PlayerID,
		}); navErr != nil {
			return currentWaypoint, 0, 0, fmt.Errorf("navigate to %s: %w", sink, navErr)
		}
		currentWaypoint = sink
	}

	if _, dockErr := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   cmd.PlayerID,
	}); dockErr != nil {
		return currentWaypoint, 0, 0, fmt.Errorf("dock at %s: %w", sink, dockErr)
	}

	resp, sellErr := h.mediator.Send(ctx, &shipCargo.SellCargoCommand{
		ShipSymbol:    cmd.ShipSymbol,
		GoodSymbol:    good,
		Units:         units,
		PlayerID:      cmd.PlayerID,
		MinBidPerUnit: 0,
	})
	if sellErr != nil {
		return currentWaypoint, 0, 0, fmt.Errorf("sell %d %s: %w", units, good, sellErr)
	}
	sr, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return currentWaypoint, 0, 0, fmt.Errorf("unexpected sell response type %T", resp)
	}
	return currentWaypoint, sr.UnitsSold, sr.TotalRevenue, nil
}

// jettisonLot dumps a lot via the existing jettison command (the last-resort path).
func (h *LiquidateCargoHandler) jettisonLot(ctx context.Context, cmd *LiquidateCargoCommand, good string, units int) error {
	_, err := h.mediator.Send(ctx, &shipCargo.JettisonCargoCommand{
		ShipSymbol: cmd.ShipSymbol,
		PlayerID:   cmd.PlayerID,
		GoodSymbol: good,
		Units:      units,
	})
	return err
}
