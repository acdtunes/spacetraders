package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
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

	// Withdrawal instrumentation: a driven port that records each
	// warehouse→hauler buffer draw as a structured economic event so downstream
	// analysis can measure warehouse ROI (buffer hit-rate, served-from-buffer,
	// contract-leg-avoided). Optional — a nil recorder disables emission so existing
	// tests and any caller that has not wired it are byte-identical, mirroring the
	// fail-open inventory-first wiring above. withdrawalClock stamps the event's
	// timestamp; WithWithdrawalRecorder defaults it to a RealClock.
	withdrawalRecorder storage.WithdrawalRecorder
	withdrawalClock    shared.Clock

	// enforceSourceBuyFloor arms the PROACTIVE contract solvency reserve on the market
	// source-buy. Opt-in (WithSourceBuyFloor): OFF leaves the buy reactive-only (the
	// ErrInsufficientCredits 4600 park below), so every existing caller/test is
	// byte-identical; production wires it so a source-buy can never silently land
	// treasury above 0 but too low for the fleet to refuel.
	enforceSourceBuyFloor bool

	// spendLedger is the CROSS-OPERATION concurrent spend cap. The floor above is
	// per-buy and cannot see a sibling's uncommitted spend; this serialises the contract
	// source-buy against construction_supply and every other spender on the same treasury.
	// nil disables it — the optional-port contract every existing test relies on — and the
	// daemon wires the SAME ledger instance it gives the construction executor.
	spendLedger ConcurrentSpendLedger
}

// ConcurrentSpendLedger is the contract side's view of the shared spend cap. Declared here,
// at the consumer, so this package takes no dependency on the manufacturing package that
// declares the identical port; *persistence.SpendReservationLedgerGORM satisfies both.
//
// Reserve READS THE BUDGET ITSELF through readBudget rather than accepting a pre-read balance:
// a balance read before the call and a SUM taken during it can describe different instants,
// and a sibling that commits and releases in between falls into neither. See the
// implementation for the full argument.
type ConcurrentSpendLedger interface {
	Reserve(ctx context.Context, playerID int, containerID string, projectedCost int, readBudget func(context.Context) (credits int64, reserveFloor int, err error)) (reservationID string, ok bool, err error)
	Release(ctx context.Context, playerID int, reservationID string) error
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

// ProcessAllDeliveries processes all deliveries in a contract.
//
// deliverHeldOnly runs the DELIVER-HELD mode (sp-5jce2): register the load
// already aboard and stop, without a source trip. The fleet coordinator asks for
// it when the hull is a badly-placed partial holder standing on the delivery
// waypoint, so that load reaches the contract at zero travel and the well-placed
// hull takes the sourcing run. Ordinary runs pass false and are unaffected.
func (e *DeliveryExecutor) ProcessAllDeliveries(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	containerID string, // Container ID for operation context linking
	deliverHeldOnly bool,
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

		// <= 0, not == 0: the server's count can exceed what it required (the 2026-08-05
		// TORWIND contract read 94 against 47 after a crash-resumed double delivery), and an
		// exact-equality skip lets an over-delivered good fall through into the delivery leg,
		// which is where the ship read that wedged that agent lives. processSingleDelivery's
		// own loop guard is already <= 0, so this only ever ADDS a skip — it can never turn a
		// good with real work left into a skipped one (RULINGS #1).
		if unitsRemaining <= 0 {
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
		contract, err = e.processSingleDelivery(ctx, shipSymbol, playerID, contract, delivery, profitabilityResp, result, nil, deliverHeldOnly)
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
	return e.processSingleDelivery(ctx, shipSymbol, playerID, contract, delivery, profitabilityResp, result, opContext, false)
}

// processSingleDelivery is ProcessSingleDelivery's implementation plus the
// DELIVER-HELD mode switch (sp-5jce2). deliverHeldOnly=false is the ordinary
// source+deliver leg.
func (e *DeliveryExecutor) processSingleDelivery(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	delivery domainContract.Delivery,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext,
	deliverHeldOnly bool,
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

		// DELIVER-HELD MODE (sp-5jce2): this hull was dispatched only to register
		// the load it is already standing on, because a hull far closer to the
		// source is taking the sourcing run. Suppress the source trip — sourcing
		// from here is exactly the round trip the split exists to avoid — and let
		// the delivery below hand over what is aboard.
		if deliverHeldOnly && unitsToPurchase > 0 {
			logger.Log("INFO", fmt.Sprintf(
				"Deliver-held run for %s: registering the %d unit(s) of %s already aboard and skipping the %d-unit source trip — a hull closer to the source takes the remainder (sp-5jce2)",
				shipSymbol, currentUnits, currentDelivery.TradeSymbol, unitsToPurchase), map[string]interface{}{
				"ship_symbol":       shipSymbol,
				"action":            "deliver_held_skip_sourcing",
				"trade_symbol":      currentDelivery.TradeSymbol,
				"units_aboard":      currentUnits,
				"units_not_sourced": unitsToPurchase,
				"units_remaining":   unitsRemaining,
			})
			unitsToPurchase = 0
		}

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
			// market) — the two-phase, re-entered by re-consulting the
			// warehouse each trip.
			// contract is nil on some caller paths (e.g. the insufficient-credits and
			// ladder-breach flows drive the loop without a contract aggregate), so the
			// id is read nil-safely — a draw with no contract emits an empty (nullable)
			// contract id, matching the event schema.
			contractID := ""
			if contract != nil {
				contractID = contract.ContractID()
			}
			withdrew, invShip, invErr := e.trySourceFromInventory(ctx, shipSymbol, playerID, ship, currentDelivery, unitsToPurchase, profitabilityResp, contractID)
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
				ship, sourcingHalted, err = e.sourceFromMarket(ctx, shipSymbol, playerID, ship, currentDelivery, unitsToPurchase, profitabilityResp, result, opContext)
				if err != nil {
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

		// DELIVER-HELD MODE (sp-5jce2): the held load is registered, so this hull's
		// job is done — return WITHOUT looping into a source trip. Looping here
		// would re-source the remainder from the delivery waypoint, which is the
		// long round trip the split exists to avoid. The remainder is not skipped:
		// the coordinator's next pass sees this hull empty, stops short-circuiting
		// on it, and selects the source-nearest hull for what is left.
		if deliverHeldOnly {
			logger.Log("INFO", fmt.Sprintf(
				"Deliver-held run complete for %s: %d/%d units of %s registered at zero travel; the remainder re-selects onto the source-nearest hull next coordinator pass (sp-5jce2)",
				shipSymbol, currentDelivery.UnitsFulfilled, currentDelivery.UnitsRequired, currentDelivery.TradeSymbol), map[string]interface{}{
				"ship_symbol":     shipSymbol,
				"action":          "deliver_held_complete",
				"trade_symbol":    currentDelivery.TradeSymbol,
				"units_fulfilled": currentDelivery.UnitsFulfilled,
				"units_required":  currentDelivery.UnitsRequired,
			})
			return contract, nil
		}

		if sourcingHalted {
			// The halt cause already WARNING-logged itself. Park the remainder for the
			// coordinator's defer gate rather than re-buying the same ask.
			logDeliveryLegPark(logger, shipSymbol, "delivery_leg_sourcing_halt_park",
				"Delivery leg parked partial after sourcing halt (ladder cap or reserve-floor partial); remainder re-projects next coordinator pass",
				currentDelivery)
			return contract, nil
		}

		if currentDelivery.UnitsFulfilled == fulfilledBefore {
			logDeliveryLegPark(logger, shipSymbol, "delivery_leg_no_progress_park",
				"Delivery leg made no progress this pass; parking for coordinator re-projection",
				currentDelivery)
			return contract, nil
		}
		// Forward progress but still partial — loop for the next cargo-load.
	}
}

// sourceFromMarket buys at the cheapest priced market. The projected unit ask is the
// basis the defer gate evaluated; the purchase loop's ladder cap stops a runaway.
func (e *DeliveryExecutor) sourceFromMarket(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	currentDelivery domainContract.Delivery,
	unitsToPurchase int,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext,
) (*navigation.Ship, bool, error) {
	profitResult, err := profitabilityResultOrErr(profitabilityResp, currentDelivery.TradeSymbol)
	if err != nil {
		return nil, false, err
	}
	projectedUnitAsk := profitResult.MarketPrices[currentDelivery.TradeSymbol]
	ship, sourcingHalted, err := e.ExecutePurchaseLoop(ctx, shipSymbol, playerID, ship, currentDelivery.TradeSymbol, unitsToPurchase, profitResult.CheapestMarketWaypoint, projectedUnitAsk, result, opContext)
	if err != nil {
		return nil, false, e.noteInsufficientCredits(ctx, err, shipSymbol, currentDelivery.TradeSymbol, playerID, profitResult.PurchaseCost)
	}
	return ship, sourcingHalted, nil
}

func logDeliveryLegPark(logger common.ContainerLogger, shipSymbol, action, message string, delivery domainContract.Delivery) {
	logger.Log("INFO", message, map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"action":          action,
		"trade_symbol":    delivery.TradeSymbol,
		"units_fulfilled": delivery.UnitsFulfilled,
		"units_required":  delivery.UnitsRequired,
	})
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
