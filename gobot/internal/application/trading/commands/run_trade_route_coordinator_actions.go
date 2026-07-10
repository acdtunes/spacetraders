package commands

// run_trade_route_coordinator_actions.go — ship I/O primitives: load, navigate, dock (nav-cache race), purchase, sell (sp-wads move-only split).

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// loadShip loads the hull the daemon container runner already claimed for this
// circuit. It does NOT claim or release: the runner owns the assignment lifecycle
// (createShipAssignments on start via the container's ship_symbol metadata,
// releaseShipAssignments on completion/crash/cancel), so the hull is force-released
// on every terminal path without this handler touching it (sp-zewt). The idle-gap
// discipline — only fly a genuinely idle hull, never steal one — is enforced ahead of
// time at DaemonServer.StartTradeRoute, before the container is persisted.
func (h *RunTradeRouteCoordinatorHandler) loadShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	return ship, nil
}

func (h *RunTradeRouteCoordinatorHandler) navigate(ctx context.Context, ship *navigation.Ship, destination string, playerID int) error {
	_, err := h.mediator.Send(ctx, &navCmd.NavigateRouteCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	})
	return err
}

// dock docks the hull at its current waypoint, surviving the nav-cache race the goods
// factory hit (sp-n7yp): right after arrival the ship's cached nav_status can still
// read IN_TRANSIT, so the domain EnsureDocked rejects the dock ("cannot dock while in
// transit"). Rather than fail — and strand the circuit at zero visits — it reconciles
// the hull against the live API (SyncShipFromAPI clears the stale IN_TRANSIT once the
// arrival has actually landed) and retries, bounded by tradeRouteDockRetryLimit so a
// genuinely-undockable hull can never spin forever.
//
// Every attempt is dispatched by SHIP SYMBOL (nil Ship), never the coordinator's
// cached hull: passing the cached ship makes LoadShip return the stale IN_TRANSIT
// snapshot and the resync a no-op (the exact subtlety sp-n7yp's dockAndConfirm
// documents) — by symbol the handler reloads the freshly-synced nav_status each try.
// A dock that keeps failing returns its cause VERBATIM so the caller aborts the
// circuit self-diagnosingly instead of swallowing it (sp-ynuf).
func (h *RunTradeRouteCoordinatorHandler) dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	logger := common.LoggerFromContext(ctx)
	pid := shared.MustNewPlayerID(playerID)
	shipSymbol := ship.ShipSymbol()

	var lastErr error
	for attempt := 0; attempt <= tradeRouteDockRetryLimit; attempt++ {
		_, err := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol, // nil Ship: force a fresh reload of the true persisted nav_status
			PlayerID:   pid,
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == tradeRouteDockRetryLimit {
			break
		}
		// Most likely the nav-cache race: the arrival event has not yet flipped the
		// cached IN_TRANSIT to IN_ORBIT. Reconcile against the live API to refresh
		// nav_status, then retry. Bounded, so a genuine failure still surfaces below.
		logger.Log("WARNING", fmt.Sprintf("Dock of %s failed (attempt %d/%d): %v - resyncing from API and retrying", shipSymbol, attempt+1, tradeRouteDockRetryLimit+1, err), map[string]interface{}{
			"ship_symbol": shipSymbol, "attempt": attempt + 1, "error": err.Error(),
		})
		if _, serr := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, pid); serr != nil {
			return fmt.Errorf("dock of %s failed (%v); resync from API also failed: %w", shipSymbol, err, serr)
		}
	}
	return fmt.Errorf("dock of %s still failing after %d resync retries: %w", shipSymbol, tradeRouteDockRetryLimit, lastErr)
}

func (h *RunTradeRouteCoordinatorHandler) purchase(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.PurchaseCargoResponse, error) {
	return h.purchaseWithCeiling(ctx, shipSymbol, good, units, playerID, 0)
}

// purchaseWithCeiling buys like purchase, but arms the per-tranche buy ceiling
// (sp-9mkf): maxAskPerUnit>0 makes the underlying handler re-verify the live ask
// before each tranche and abort the remainder (left unbought,
// PurchaseCargoResponse.CeilingAborted) if it rises above the ceiling. It is the
// buy-side mirror of sellWithFloor and the fix for the stale-ask ladder (SHIP_PARTS
// bought at D39 as the ask ran 3,985→~7k inside one dispatch). maxAskPerUnit==0 is
// exactly the plain buy, so purchase() and the manufacturing/contract callers are
// unchanged.
func (h *RunTradeRouteCoordinatorHandler) purchaseWithCeiling(ctx context.Context, shipSymbol, good string, units, playerID, maxAskPerUnit int) (*shipCargo.PurchaseCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.PurchaseCargoCommand{
		ShipSymbol:    shipSymbol,
		GoodSymbol:    good,
		Units:         units,
		PlayerID:      shared.MustNewPlayerID(playerID),
		MaxAskPerUnit: maxAskPerUnit,
	})
	if err != nil {
		return nil, err
	}
	pr, ok := resp.(*shipCargo.PurchaseCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected purchase response type %T", resp)
	}
	return pr, nil
}

func (h *RunTradeRouteCoordinatorHandler) sell(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.SellCargoResponse, error) {
	return h.sellWithFloor(ctx, shipSymbol, good, units, playerID, 0)
}

// sellWithFloor sells like sell, but arms the per-tranche sell floor (sp-lbbm):
// minBidPerUnit>0 makes the underlying handler re-verify the live bid before each
// tranche and abort the remainder (held aboard, SellCargoResponse.FloorAborted)
// if it falls below the floor. minBidPerUnit==0 is exactly the plain sell, so
// sell() and the trade-route/tour callers are unchanged.
func (h *RunTradeRouteCoordinatorHandler) sellWithFloor(ctx context.Context, shipSymbol, good string, units, playerID, minBidPerUnit int) (*shipCargo.SellCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.SellCargoCommand{
		ShipSymbol:    shipSymbol,
		GoodSymbol:    good,
		Units:         units,
		PlayerID:      shared.MustNewPlayerID(playerID),
		MinBidPerUnit: minBidPerUnit,
	})
	if err != nil {
		return nil, err
	}
	sr, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected sell response type %T", resp)
	}
	return sr, nil
}
