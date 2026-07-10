package services

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// Type aliases for convenience
type DeliverContractCommand = contractTypes.DeliverContractCommand
type DeliverContractResponse = contractTypes.DeliverContractResponse
type RunWorkflowResponse = contractTypes.RunWorkflowResponse

// DeliveryExecutor handles contract delivery execution including purchasing and delivering cargo
type DeliveryExecutor struct {
	mediator     common.Mediator
	shipRepo     navigation.ShipRepository
	cargoManager *CargoManager

	// Inventory-first sourcing collaborators (sp-dchv Lane D). Wired together via
	// WithInventorySource, or all nil — a nil finder disables inventory-first and
	// the executor uses the market path byte-identical (existing tests construct
	// the executor with no options and are unaffected).
	invFinder          appContract.InventorySourceFinder
	storageCoordinator storage.StorageCoordinator
	apiClient          domainPorts.APIClient
}

// DeliveryExecutorOption configures optional collaborators without breaking the
// positional constructor the existing tests use.
type DeliveryExecutorOption func(*DeliveryExecutor)

// WithInventorySource enables inventory-first contract sourcing (sp-dchv Lane D):
// before each market buy the executor withdraws the good from an in-system
// warehouse at zero ask when one holds it. A nil finder is a no-op (market-only),
// so callers may forward optional wiring unconditionally.
func WithInventorySource(finder appContract.InventorySourceFinder, coordinator storage.StorageCoordinator, apiClient domainPorts.APIClient) DeliveryExecutorOption {
	return func(e *DeliveryExecutor) {
		e.invFinder = finder
		e.storageCoordinator = coordinator
		e.apiClient = apiClient
	}
}

// NewDeliveryExecutor creates a new delivery executor service
func NewDeliveryExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	cargoManager *CargoManager,
	opts ...DeliveryExecutorOption,
) *DeliveryExecutor {
	e := &DeliveryExecutor{
		mediator:     mediator,
		shipRepo:     shipRepo,
		cargoManager: cargoManager,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ProcessAllDeliveries processes all deliveries in a contract
func (e *DeliveryExecutor) ProcessAllDeliveries(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	containerID string, // Container ID for operation context linking
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	// Create operation context for transaction linking and add to context
	if containerID != "" {
		opContext := shared.NewOperationContext(containerID, "contract_workflow")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	logger.Log("INFO", "Contract deliveries processing started", map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"action":         "process_deliveries",
		"contract_id":    contract.ContractID(),
		"delivery_count": len(contract.Terms().Deliveries),
	})

	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		logger.Log("INFO", "Contract delivery status", map[string]interface{}{
			"ship_symbol":     shipSymbol,
			"action":          "check_delivery",
			"trade_symbol":    delivery.TradeSymbol,
			"units_required":  delivery.UnitsRequired,
			"units_fulfilled": delivery.UnitsFulfilled,
			"units_remaining": unitsRemaining,
		})

		if unitsRemaining == 0 {
			logger.Log("INFO", "Contract delivery already fulfilled", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"action":       "skip_delivery",
				"trade_symbol": delivery.TradeSymbol,
			})
			continue
		}

		logger.Log("INFO", "Contract delivery processing initiated", map[string]interface{}{
			"ship_symbol":  shipSymbol,
			"action":       "process_delivery",
			"trade_symbol": delivery.TradeSymbol,
		})

		var err error
		contract, err = e.ProcessSingleDelivery(ctx, shipSymbol, playerID, contract, delivery, profitabilityResp, result, nil)
		if err != nil {
			return nil, err
		}
	}

	return contract, nil
}

// ProcessSingleDelivery sources and delivers ONE contract good to completion.
//
// The cargo hold is finite, so a good whose requirement exceeds one hold takes
// several source->deliver trips. This loops that leg — buy a load, deliver it,
// re-read the good's registration from the deliver RESPONSE, repeat — until the
// good is fully registered. Before sp-2ei3 this ran the leg exactly once and
// returned a partial contract; RunWorkflowHandler then fulfilled that partial
// state and crashed on "deliveries not complete", and the coordinator's
// crash-respawn re-entered with the same wrong assumption — the livelock.
//
// It stops short of completion in exactly two honest, never-a-skip ways: a
// ladder-cap sourcing halt (deliver what's aboard, park the runaway remainder
// for the coordinator's defer gate to re-project) and a no-progress pass (the
// remainder can't be sourced/delivered right now — park rather than spin). Both
// return a partial contract; the caller's fulfill guard leaves it unfulfilled.
func (e *DeliveryExecutor) ProcessSingleDelivery(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	delivery domainContract.Delivery,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	// currentDelivery holds the good's live delivered/required. It starts from
	// what the coordinator handed us and is re-read from each deliver response
	// (the authoritative in-band contract state), so every completion/progress
	// test below runs on truth, not a cached "delivered" belief.
	currentDelivery := delivery

	for {
		unitsRemaining := currentDelivery.UnitsRequired - currentDelivery.UnitsFulfilled
		if unitsRemaining <= 0 {
			return contract, nil
		}

		ship, currentUnits, err := e.cargoManager.ReloadShipState(ctx, shipSymbol, playerID, currentDelivery.TradeSymbol)
		if err != nil {
			return nil, err
		}

		ship, currentUnits, err = e.cargoManager.JettisonWrongCargoIfNeeded(ctx, ship, currentDelivery.TradeSymbol, currentUnits, unitsRemaining, playerID)
		if err != nil {
			return nil, err
		}

		unitsToPurchase := e.cargoManager.CalculatePurchaseNeeds(ctx, shipSymbol, currentDelivery.TradeSymbol, unitsRemaining, currentUnits)

		sourcingHalted := false
		if unitsToPurchase > 0 {
			// INVENTORY-FIRST (sp-dchv Lane D): withdraw the good from an in-system
			// warehouse at zero ask before any market buy. This runs BEFORE the
			// profitability lookup so a stocked good sources even before scouts
			// have priced a market for it. Fail-open (RULINGS #1): a warehouse
			// read/transfer error is logged and falls through to the market path,
			// never parking the contract. A withdrawal that lands ANY units aboard
			// short-circuits the market buy THIS trip; the outer loop delivers them
			// and re-sources the remainder (inventory again until drained, then
			// market) — the sp-2ei3 two-phase, re-entered by re-consulting the
			// warehouse each trip.
			withdrew, invShip, invErr := e.trySourceFromInventory(ctx, shipSymbol, playerID, ship, currentDelivery, unitsToPurchase, profitabilityResp)
			if invErr != nil {
				logger.Log("WARNING", fmt.Sprintf(
					"Inventory-first sourcing for %s errored (%v); falling through to the market path (never-skip, RULINGS #1)",
					currentDelivery.TradeSymbol, invErr), map[string]interface{}{
					"ship_symbol":  shipSymbol,
					"action":       "inventory_sourcing_failopen",
					"trade_symbol": currentDelivery.TradeSymbol,
				})
			}
			if withdrew {
				ship = invShip
			} else {
				profitResult, err := profitabilityResultOrErr(profitabilityResp, currentDelivery.TradeSymbol)
				if err != nil {
					return nil, err
				}
				// The evaluation's cached ask for this good is the basis the sourcing
				// defer gate projected against; the purchase loop's ladder cap
				// (sp-1z2h) stops buying when realized prices run away from it.
				projectedUnitAsk := profitResult.MarketPrices[currentDelivery.TradeSymbol]
				ship, sourcingHalted, err = e.ExecutePurchaseLoop(ctx, shipSymbol, playerID, ship, currentDelivery.TradeSymbol, unitsToPurchase, profitResult.CheapestMarketWaypoint, projectedUnitAsk, result, opContext)
				if err != nil {
					// PARK, don't crash (sp-vwhi): a 4600 mid-purchase is a treasury
					// state, not a bug. Enrich the sentinel with the numbers an
					// operator needs and log ONCE at WARNING before returning it
					// unchanged - RunWorkflowHandler.Handle converts this into a
					// clean (nil-error) exit so the container doesn't crashloop.
					// The dynamic-discovery fleet coordinator re-picks-up this
					// contract on its next pass, which is the resume mechanism.
					var insufficientErr *ErrInsufficientCredits
					if errors.As(err, &insufficientErr) {
						insufficientErr.CreditsNeeded = profitResult.PurchaseCost
						insufficientErr.CreditsAvailable = e.lookupLiveCredits(ctx, playerID)
						logger.Log("WARNING", insufficientErr.Error(), map[string]interface{}{
							"ship_symbol":       shipSymbol,
							"action":            "parked",
							"reason":            "insufficient_credits",
							"trade_symbol":      currentDelivery.TradeSymbol,
							"units_attempted":   insufficientErr.UnitsAttempted,
							"credits_needed":    insufficientErr.CreditsNeeded,
							"credits_available": insufficientErr.CreditsAvailable,
						})
					}
					return nil, err
				}
			}
		}

		fulfilledBefore := currentDelivery.UnitsFulfilled

		contract, err = e.DeliverContractCargo(ctx, shipSymbol, playerID, contract, ship, currentDelivery)
		if err != nil {
			return nil, err
		}

		// Re-read the good's registration from the deliver response (the loop's
		// source of truth). A nil contract only happens in unit fakes that skip
		// the deliver; the progress guard below then parks the iteration.
		if contract != nil {
			if updated, ok := findDelivery(contract, currentDelivery.TradeSymbol); ok {
				currentDelivery = updated
			}
		}

		if currentDelivery.UnitsFulfilled >= currentDelivery.UnitsRequired {
			return contract, nil
		}

		if sourcingHalted {
			// The ladder cap already WARNING-logged the runaway ask and the
			// unsourced remainder. Deliver-what's-aboard has run; park the
			// remainder for the coordinator's defer gate rather than looping
			// and re-laddering the same ask (never a skip).
			logger.Log("INFO", "Delivery leg parked partial after ladder-cap sourcing halt; remainder re-projects next coordinator pass", map[string]interface{}{
				"ship_symbol":     shipSymbol,
				"action":          "delivery_leg_sourcing_halt_park",
				"trade_symbol":    currentDelivery.TradeSymbol,
				"units_fulfilled": currentDelivery.UnitsFulfilled,
				"units_required":  currentDelivery.UnitsRequired,
			})
			return contract, nil
		}

		if currentDelivery.UnitsFulfilled == fulfilledBefore {
			// No forward progress this pass — the remainder could not be
			// sourced/delivered now. Park honestly for coordinator re-projection
			// rather than spin (never a skip).
			logger.Log("INFO", "Delivery leg made no progress this pass; parking for coordinator re-projection", map[string]interface{}{
				"ship_symbol":     shipSymbol,
				"action":          "delivery_leg_no_progress_park",
				"trade_symbol":    currentDelivery.TradeSymbol,
				"units_fulfilled": currentDelivery.UnitsFulfilled,
				"units_required":  currentDelivery.UnitsRequired,
			})
			return contract, nil
		}
		// Forward progress but still partial — loop for the next cargo-load.
	}
}

// findDelivery returns the contract's live Delivery for the given good and
// whether it was found. The delivery leg uses it to re-read delivered/required
// straight off the deliver response after each trip.
func findDelivery(contract *domainContract.Contract, tradeSymbol string) (domainContract.Delivery, bool) {
	for _, d := range contract.Terms().Deliveries {
		if d.TradeSymbol == tradeSymbol {
			return d, true
		}
	}
	return domainContract.Delivery{}, false
}

// lookupLiveCredits fetches a fresh treasury snapshot for the WARNING log
// enrichment above. Returns -1 if the live lookup itself fails, so the log
// message still emits (with an explicit sentinel value) rather than being
// lost to a second error.
func (e *DeliveryExecutor) lookupLiveCredits(ctx context.Context, playerID shared.PlayerID) int {
	pid := playerID.Value()
	resp, err := e.mediator.Send(ctx, &playerQueries.GetPlayerQuery{PlayerID: &pid})
	if err != nil {
		return -1
	}
	playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
	if !ok || playerResp.Player == nil {
		return -1
	}
	return playerResp.Player.Credits
}

// ExecutePurchaseLoop executes the multi-trip purchase loop.
//
// projectedUnitAsk is the cached ask the profitability evaluation (and the
// coordinator's sourcing defer gate, sp-1z2h) based its projection on; 0
// disables the ladder cap (no basis to compare against). When a trip realizes
// worse than SourcingLadderCapNumer/Denom (1.5×) of that basis, the loop stops
// buying and delivers what is aboard — the −891k ELECTRONICS incident was
// exactly this shape, a buyer laddering a SCARCE ask upward tranche after
// tranche until the contract filled at any price. The undelivered remainder is
// re-picked-up by the coordinator's next pass, where the defer gate re-projects
// it at live prices (and parks it if still negative). Nothing is skipped.
func (e *DeliveryExecutor) ExecutePurchaseLoop(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	unitsToPurchase int,
	cheapestMarket string,
	projectedUnitAsk int,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, bool, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Cheapest market identified", map[string]interface{}{
		"ship_symbol":   shipSymbol,
		"action":        "identify_market",
		"market_symbol": cheapestMarket,
		"trade_symbol":  tradeSymbol,
	})

	trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
	result.TotalTrips += trips
	logger.Log("INFO", "Multi-trip purchase initiated", map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"action":       "start_multi_trip",
		"trips_needed": trips,
		"trade_symbol": tradeSymbol,
	})

	// sourcingHalted propagates a ladder-cap decision (stop buying a runaway
	// ask) up to the delivery leg so it parks the remainder instead of looping
	// and re-laddering. A full hold ends this loop without setting it.
	sourcingHalted := false
	for trip := 0; trip < trips; trip++ {
		var stop bool
		var err error
		ship, unitsToPurchase, stop, sourcingHalted, err = e.executeSinglePurchaseTrip(ctx, shipSymbol, playerID, ship, tradeSymbol, cheapestMarket, unitsToPurchase, projectedUnitAsk, opContext)
		if err != nil {
			return nil, false, err
		}
		if stop {
			break
		}
	}

	return ship, sourcingHalted, nil
}

// executeSinglePurchaseTrip executes a single purchase trip. It returns two
// distinct stop signals: `stop` ends the trips loop for ANY reason (a full hold
// or a ladder breach), while `sourcingHalted` is set ONLY by a ladder breach —
// a deliberate decision to stop feeding a runaway ask, which the delivery leg
// must honor by delivering what's aboard and parking the remainder rather than
// looping and re-laddering the same ask. A full hold (`stop` without
// `sourcingHalted`) is normal: it just means "go deliver, then come back".
func (e *DeliveryExecutor) executeSinglePurchaseTrip(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	cheapestMarket string,
	unitsToPurchase int,
	projectedUnitAsk int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, int, bool, bool, error) {
	logger := common.LoggerFromContext(ctx)

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

	if unitsThisTrip <= 0 {
		logger.Log("WARNING", "Purchase loop terminated due to no cargo space", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "purchase_loop_ended",
			"reason":      "no_cargo_space",
		})
		return ship, unitsToPurchase, true, false, nil
	}

	var err error
	ship, err = e.navigateAndDock(ctx, shipSymbol, cheapestMarket, playerID)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("failed to navigate to market: %w", err)
	}

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: tradeSymbol,
		Units:      unitsThisTrip,
		PlayerID:   playerID,
	}

	purchaseResp, err := e.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		if IsInsufficientCreditsError(err) {
			return nil, 0, false, false, &ErrInsufficientCredits{
				ShipSymbol:     shipSymbol,
				TradeSymbol:    tradeSymbol,
				UnitsAttempted: unitsThisTrip,
				Cause:          err,
			}
		}
		return nil, 0, false, false, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	unitsToPurchase -= unitsThisTrip

	// SOURCING LADDER CAP (sp-1z2h): stop feeding an ask that has run away
	// from the projected basis. The tranche just bought stays aboard and gets
	// delivered; only FURTHER buying stops — the remainder re-gates through the
	// coordinator's defer projection at live prices.
	ladderBreached, realizedPerUnit := sourcingLadderBreached(purchaseResp, projectedUnitAsk)
	if ladderBreached {
		logger.Log("WARNING", fmt.Sprintf(
			"Sourcing ladder cap: trip realized %d/unit exceeds %d/%dx projected ask %d for %s at %s - halting purchases with %d units still unsourced, delivering partial load (remainder re-projects through the defer gate next coordinator pass; never-skip stands)",
			realizedPerUnit, appContract.SourcingLadderCapNumer, appContract.SourcingLadderCapDenom,
			projectedUnitAsk, tradeSymbol, cheapestMarket, unitsToPurchase,
		), map[string]interface{}{
			"ship_symbol":       shipSymbol,
			"action":            "sourcing_ladder_cap",
			"trade_symbol":      tradeSymbol,
			"market":            cheapestMarket,
			"realized_per_unit": realizedPerUnit,
			"projected_ask":     projectedUnitAsk,
			"units_unsourced":   unitsToPurchase,
		})
	}

	ship, err = e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("failed to reload ship after purchase: %w", err)
	}

	return ship, unitsToPurchase, ladderBreached, ladderBreached, nil
}

// sourcingLadderBreached reports whether the trip's realized per-unit price ran
// past SourcingLadderCapNumer/Denom (1.5×) of the projected ask, and what it
// realized. A zero/unknown basis, a non-PurchaseCargoResponse, or a zero-unit
// buy never breach (nothing meaningful to compare).
func sourcingLadderBreached(purchaseResp common.Response, projectedUnitAsk int) (bool, int) {
	if projectedUnitAsk <= 0 {
		return false, 0
	}
	resp, ok := purchaseResp.(*shipCargo.PurchaseCargoResponse)
	if !ok || resp == nil || resp.UnitsAdded <= 0 {
		return false, 0
	}
	realizedPerUnit := resp.TotalCost / resp.UnitsAdded
	return realizedPerUnit*appContract.SourcingLadderCapDenom > projectedUnitAsk*appContract.SourcingLadderCapNumer, realizedPerUnit
}

// DeliverContractCargo delivers cargo to the contract destination
func (e *DeliveryExecutor) DeliverContractCargo(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	ship *navigation.Ship,
	delivery domainContract.Delivery,
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship before delivery: %w", err)
	}

	// Calculate how many units to deliver - cap at remaining units needed
	unitsInCargo := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	unitsToDeliver := unitsInCargo
	if unitsToDeliver > unitsRemaining {
		unitsToDeliver = unitsRemaining
	}

	logger.Log("INFO", "Contract cargo delivery initiated", map[string]interface{}{
		"ship_symbol":      shipSymbol,
		"action":           "deliver_cargo",
		"trade_symbol":     delivery.TradeSymbol,
		"units_in_cargo":   unitsInCargo,
		"units_remaining":  unitsRemaining,
		"units_to_deliver": unitsToDeliver,
	})

	if unitsToDeliver == 0 {
		return contract, nil
	}

	ship, err = e.navigateAndDock(ctx, shipSymbol, delivery.DestinationSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to delivery: %w", err)
	}

	deliverCmd := &DeliverContractCommand{
		ContractID:  contract.ContractID(),
		ShipSymbol:  shipSymbol,
		TradeSymbol: delivery.TradeSymbol,
		Units:       unitsToDeliver,
		PlayerID:    playerID,
	}

	deliverResp, err := e.mediator.Send(ctx, deliverCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver cargo: %w", err)
	}

	deliverResult := deliverResp.(*DeliverContractResponse)
	return deliverResult.Contract, nil
}

// navigateAndDock navigates to destination and docks in one operation
func (e *DeliveryExecutor) navigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	ship, err := e.navigateToWaypoint(ctx, shipSymbol, destination, playerID)
	if err != nil {
		return nil, err
	}

	if err := e.dockShip(ctx, ship, playerID); err != nil {
		return nil, err
	}

	return ship, nil
}

// navigateToWaypoint navigates ship to destination and returns updated ship state
func (e *DeliveryExecutor) navigateToWaypoint(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	// Use HIGH-LEVEL NavigateRouteCommand (handles route planning, refueling, multi-hop, idempotency)
	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}

	resp, err := e.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	navResp := resp.(*shipNav.NavigateRouteResponse)
	return navResp.Ship, nil
}

// dockShip docks ship (idempotent)
func (e *DeliveryExecutor) dockShip(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) error {
	dockCmd := &shipTypes.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}

	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	return nil
}

// profitabilityResultOrErr validates the profitability response before use.
// The workflow handler treats profitability-evaluation failures as non-fatal,
// so a nil response reaches purchasing when no market data exists yet; the
// old unchecked assertion panicked the whole daemon (see captain incident
// 2026-07-02). A purchase without market data must fail the container, not
// the process.
func profitabilityResultOrErr(resp common.Response, good string) (*contractQueries.ProfitabilityResult, error) {
	result, ok := resp.(*contractQueries.ProfitabilityResult)
	if !ok || result == nil {
		return nil, fmt.Errorf("cannot plan purchase of %s: no profitability/market data available (scout markets first)", good)
	}
	return result, nil
}

// trySourceFromInventory attempts to fill this source trip from an in-system
// warehouse (sp-dchv Lane D) instead of buying at market. It returns
// withdrew=true (with the reloaded ship) when it lands at least one unit aboard,
// so the caller skips the market buy for this trip and lets the delivery loop
// re-source the remainder. It returns withdrew=false for EVERY no-inventory case
// (feature off, no stock, drained mid-flight) so the caller uses the market
// path, and a non-nil error only for an unexpected failure the caller logs and
// still treats as fail-open (never a skip, RULINGS #1).
//
// Withdrawal mirrors the proven manufacturing STORAGE_ACQUIRE_DELIVER shape
// (TryReserveCargo -> TransferCargo -> ConfirmTransfer) but BOUNDS the take to
// what the contract needs this trip: it reserves what the storage ship holds,
// transfers only min(reserved, hold space, units needed), and releases the
// excess reservation so other workers/contracts are not starved. The warehouse
// hull is Lane B's dedicated, claimed ship (RULINGS #7) — the contract worker
// only transfers from it, never claims it. The per-ship reservation is atomic
// (TryReserveCargo holds the storage-ship mutex), so two contracts racing the
// same units cannot double-claim: one reserves, the other sees them gone and
// falls through.
func (e *DeliveryExecutor) trySourceFromInventory(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	delivery domainContract.Delivery,
	unitsToPurchase int,
	profitabilityResp common.Response,
) (bool, *navigation.Ship, error) {
	// Not wired (existing tests / feature off) -> market path, no error.
	if e.invFinder == nil || e.storageCoordinator == nil || e.apiClient == nil {
		return false, ship, nil
	}

	logger := common.LoggerFromContext(ctx)
	good := delivery.TradeSymbol
	deliverySystem := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

	// Decision read: in-system warehouse stock for this good? (in-system only,
	// RULINGS #14 — the finder never returns an out-of-system warehouse.)
	src := e.invFinder.FindInSystemInventory(ctx, playerID.Value(), deliverySystem, good)
	if src == nil {
		return false, ship, nil // no inventory -> market path
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		return false, ship, nil // no hold space -> let the market path decide
	}
	want := utils.Min(availableSpace, unitsToPurchase)
	if want <= 0 {
		return false, ship, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return false, ship, fmt.Errorf("no player token for inventory withdrawal: %w", err)
	}

	// Fly to the warehouse (in-system) and stay in orbit for the ship-to-ship
	// transfer.
	ship, err = e.navigateToWaypoint(ctx, shipSymbol, src.StorageWaypoint, playerID)
	if err != nil {
		return false, ship, fmt.Errorf("navigate to warehouse %s: %w", src.StorageWaypoint, err)
	}
	if err := e.orbitForTransfer(ctx, ship, playerID); err != nil {
		return false, ship, fmt.Errorf("orbit at warehouse %s: %w", src.StorageWaypoint, err)
	}

	// Reserve from the warehouse's storage ship(s). A drain between the finder
	// read and here yields no reservation -> fall through to market (fail-open).
	storageShip, reserved := e.reserveFromWarehouse(src.OperationID, good)
	if storageShip == nil || reserved <= 0 {
		return false, ship, nil
	}

	toMove := utils.Min(reserved, want)

	// Align nav state before the ship-to-ship transfer (sp-5qs1). This is a WITHDRAWAL:
	// the warehouse hull (storageShip) is the stationary source, the contract worker
	// (shipSymbol) is the visitor. SpaceTraders rejects the transfer with API 4271 unless
	// both hulls share a nav state, so the visitor is orbited/docked to match the warehouse
	// (never moved); a 4271 race is re-aligned and retried once rather than crashing.
	if _, _, err := common.AlignAndTransferCargo(ctx, e.apiClient, storageShip.ShipSymbol(), shipSymbol, storageShip.ShipSymbol(), good, toMove, token); err != nil {
		// Release the whole reservation and fall through to market (fail-open).
		if cancelErr := storageShip.CancelReservation(good, reserved); cancelErr != nil {
			logger.Log("ERROR", "Inventory withdrawal: failed to cancel reservation after transfer error", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"storage_ship": storageShip.ShipSymbol(),
				"error":        cancelErr.Error(),
			})
		}
		return false, ship, fmt.Errorf("transfer %d %s from warehouse ship %s: %w", toMove, good, storageShip.ShipSymbol(), err)
	}

	// Commit the moved units; release any over-reservation for other workers.
	if err := storageShip.ConfirmTransfer(good, toMove); err != nil {
		logger.Log("ERROR", "Inventory withdrawal: confirm transfer failed (cargo already moved)", map[string]interface{}{
			"ship_symbol":  shipSymbol,
			"storage_ship": storageShip.ShipSymbol(),
			"error":        err.Error(),
		})
	}
	if excess := reserved - toMove; excess > 0 {
		if err := storageShip.CancelReservation(good, excess); err != nil {
			logger.Log("WARN", "Inventory withdrawal: failed to release over-reservation", map[string]interface{}{
				"storage_ship": storageShip.ShipSymbol(),
				"excess":       excess,
				"error":        err.Error(),
			})
		}
	}

	// Persist both ships' cargo state (mirror manufacturing).
	if _, err := e.shipRepo.SyncShipFromAPI(ctx, storageShip.ShipSymbol(), playerID); err != nil {
		logger.Log("WARN", "Inventory withdrawal: failed to sync storage ship after transfer", map[string]interface{}{
			"storage_ship": storageShip.ShipSymbol(),
			"error":        err.Error(),
		})
	}
	reloaded, err := e.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID)
	if err != nil {
		return false, ship, fmt.Errorf("sync hauler after withdrawal: %w", err)
	}

	// Honest accounting (sp-dchv): withdrawn goods cost the contract engine ZERO
	// at withdrawal (basis sunk at deposit). The market ask this trip AVOIDED is
	// the realized-savings line the captain reads. marketAsk is best-effort (0
	// when no market has been priced for the good yet).
	marketAsk := marketAskBestEffort(profitabilityResp, good)
	logger.Log("INFO", fmt.Sprintf(
		"Sourced %d %s from warehouse ship %s at zero ask (market would have cost %d @ %d/unit) - realized savings, contract sourcing cost 0",
		toMove, good, storageShip.ShipSymbol(), marketAsk*toMove, marketAsk,
	), map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"action":          "inventory_withdrawal",
		"trade_symbol":    good,
		"units_withdrawn": toMove,
		"storage_op":      src.OperationID,
		"market_ask":      marketAsk,
		"savings":         marketAsk * toMove,
	})

	return true, reloaded, nil
}

// reserveFromWarehouse reserves all unreserved units of good on the first
// storage ship in the operation that holds any, returning that ship and the
// amount reserved (0 if none). The caller MUST ConfirmTransfer the moved units
// and CancelReservation any remainder.
func (e *DeliveryExecutor) reserveFromWarehouse(operationID, good string) (*storage.StorageShip, int) {
	for _, s := range e.storageCoordinator.GetStorageShipsForOperation(operationID) {
		if s == nil {
			continue
		}
		reserved, err := s.TryReserveCargo(good, 1)
		if err == nil && reserved > 0 {
			return s, reserved
		}
	}
	return nil, 0
}

// orbitForTransfer ensures the ship is in orbit (not docked) so a ship-to-ship
// cargo transfer can run at the warehouse waypoint. A ship already in orbit is a
// no-op.
func (e *DeliveryExecutor) orbitForTransfer(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	if ship != nil && !ship.IsDocked() {
		return nil
	}
	orbitCmd := &shipTypes.OrbitShipCommand{Ship: ship, PlayerID: playerID}
	if _, err := e.mediator.Send(ctx, orbitCmd); err != nil {
		return err
	}
	return nil
}

// marketAskBestEffort returns the profitability evaluation's cached market ask
// for good, or 0 when no profitability/market data is available. Used only for
// the savings log line, so it never errors (an unpriced good still withdraws).
func marketAskBestEffort(resp common.Response, good string) int {
	pr, ok := resp.(*contractQueries.ProfitabilityResult)
	if !ok || pr == nil {
		return 0
	}
	return pr.MarketPrices[good]
}
