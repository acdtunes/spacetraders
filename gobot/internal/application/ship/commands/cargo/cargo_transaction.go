package cargo

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	shipPkg "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// CargoTransactionCommand represents a unified command for cargo transactions (purchase or sell).
//
// This unified command replaces separate PurchaseCargoCommand and SellCargoCommand,
// reducing duplication and enabling extensibility through the Strategy pattern.
//
// The specific transaction type (purchase vs sell) is determined by the strategy
// injected into the handler.
//
// Business rules enforced:
//   - Ship must be docked at a marketplace
//   - Transaction-specific preconditions are validated by the strategy
//   - Automatically splits large transactions based on market limits
//
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type CargoTransactionCommand struct {
	ShipSymbol string          // Ship symbol (e.g., "SHIP-1")
	GoodSymbol string          // Trade good symbol (e.g., "IRON_ORE")
	Units      int             // Total units to transaction
	PlayerID   shared.PlayerID // Player ID for authorization

	// MinBidPerUnit is the per-tranche SELL floor: before each sell
	// tranche the handler re-reads the LIVE bid and, if it has fallen below this
	// per-unit floor, ABORTS the remaining tranches and leaves the rest aboard
	// (FloorAborted). It is the fix for the H50 co-dump — five tranches sold for 27
	// credits after the bid crashed 19,950→4. 0 disables the floor entirely, so
	// every non-arb caller (contract delivery, manufacturing, refuel) is unchanged.
	// The arb executor arms it with ceil(fraction × quoted bid); see the arb
	// coordinator. Ignored for purchases.
	MinBidPerUnit int

	// MaxAskPerUnit is the mirror of MinBidPerUnit for the BUY side: the
	// per-tranche buy CEILING. Before each purchase tranche the handler re-reads the
	// LIVE ask and, if it has laddered ABOVE this per-unit ceiling, ABORTS the
	// remaining tranches and leaves the rest unbought (CeilingAborted). It is the fix
	// for the stale-ask buy — SHIP_PARTS bought at D39 while the ask laddered
	// 3,985→4,942→~7k inside a single dispatch, realising −3,430/u. A single pre-buy
	// margin check cannot see the ladder that a multi-tranche buy walks up itself;
	// this re-reads per tranche. 0 disables the ceiling entirely, so every non-arb
	// caller (contract delivery, manufacturing, refuel) is unchanged. The arb/circuit
	// executors arm it with the max ask that still clears the lane's justifying margin
	// (quoted dest bid − min-margin); see the arb coordinator. Ignored for sells.
	MaxAskPerUnit int
}

// CargoTransactionResponse contains the unified results of a cargo transaction.
//
// TransactionCount indicates how many API calls were made to complete the operation.
// This is typically > 1 when the requested units exceed the market's transaction limit.
type CargoTransactionResponse struct {
	TotalAmount      int // Total credits (cost for purchase, revenue for sell)
	UnitsProcessed   int // Total units (added for purchase, sold for sell)
	TransactionCount int // Number of API transactions executed

	// FloorAborted is true when the per-tranche sell floor stopped the
	// sale early: the live bid fell below MinBidPerUnit, so the remaining units
	// were held aboard rather than dumped. UnitsProcessed then reports only what
	// sold before the abort. FloorObservedBid is the live bid that tripped the
	// floor (0 when it could not be read — a fail-closed abort). Both stay zero
	// for an unfloored transaction.
	FloorAborted     bool
	FloorObservedBid int

	// CeilingAborted is true when the per-tranche buy ceiling stopped the
	// purchase early: the live ask rose above MaxAskPerUnit, so the remaining units
	// were left unbought. UnitsProcessed then reports only what was bought before the
	// abort. CeilingObservedAsk is the live ask that tripped the ceiling (0 when it
	// could not be read — a fail-closed abort). Both stay zero for an uncapped buy.
	CeilingAborted     bool
	CeilingObservedAsk int

	// Reserved is true when the sell was refused because the good is
	// reserved as do-not-sell on the hull (a staged outfitting module, or an
	// operator-protected good). No API call is made and no ledger row is written;
	// UnitsProcessed and TotalAmount stay zero and the cargo is held aboard. Only
	// ever set on a sell.
	Reserved bool
}

// CargoTransactionHandler orchestrates cargo transaction operations using the Strategy pattern.
//
// This handler unifies the logic shared by PurchaseCargoHandler and SellCargoHandler.
//
// Key responsibilities:
//   - Validate ship is docked
//   - Load player token
//   - Delegate validation to strategy (cargo space vs cargo availability)
//   - Fetch transaction limit from market
//   - Execute transactions in batches via strategy
//   - Accumulate results
//   - Record financial transactions in ledger
//
// The handler is Open/Closed:
//   - Open for extension: New transaction types (trade, donate) can be added by implementing CargoTransactionStrategy
//   - Closed for modification: Handler logic doesn't change when adding new transaction types
type CargoTransactionHandler struct {
	strategy        strategies.CargoTransactionStrategy
	shipRepo        navigation.ShipRepository
	playerRepo      player.PlayerRepository
	marketRepo      scoutingQuery.MarketRepository
	apiClient       domainPorts.APIClient
	mediator        common.Mediator
	marketRefresher MarketRefresher // Optional: refreshes market data after transactions

	// impactNonce is the per-trade counter that spreads the impact-scan sampling
	// evenly across every market and hull this shared handler serves: each
	// post-trade scan decision consumes the next value, so no single lane is ever
	// permanently sampled-in or -out. Atomic because the handler is a daemon singleton
	// dispatched concurrently across hulls.
	impactNonce atomic.Uint64
}

// NewCargoTransactionHandler creates a new cargo transaction handler with the given strategy.
//
// Different transaction types are created by injecting different strategies:
//   - NewCargoTransactionHandler(NewPurchaseStrategy(...)) - for purchases
//   - NewCargoTransactionHandler(NewSellStrategy(...)) - for sales
//   - NewCargoTransactionHandler(NewTradeStrategy(...)) - future: for trades
//
// The marketRefresher is optional - if nil, market data will not be refreshed after transactions.
func NewCargoTransactionHandler(
	strategy strategies.CargoTransactionStrategy,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	marketRepo scoutingQuery.MarketRepository,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
	marketRefresher MarketRefresher,
) *CargoTransactionHandler {
	return &CargoTransactionHandler{
		strategy:        strategy,
		shipRepo:        shipRepo,
		playerRepo:      playerRepo,
		marketRepo:      marketRepo,
		apiClient:       apiClient,
		mediator:        mediator,
		marketRefresher: marketRefresher,
	}
}

// Handle executes the cargo transaction command with automatic transaction splitting.
//
// The method follows a consistent flow:
//  1. Retrieve player token from context
//  2. Load ship from repository
//  3. Validate ship is docked
//  4. Delegate precondition validation to strategy
//  5. Fetch current player balance (for ledger)
//  6. Determine transaction limit from market
//  7. Execute transactions in batches
//  8. Record transaction in ledger (async)
//  9. Return accumulated results
func (h *CargoTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*CargoTransactionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	token, err := h.getPlayerToken(ctx)
	if err != nil {
		return nil, err
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if err := h.validateShipDocked(ship); err != nil {
		return nil, err
	}

	if err := h.strategy.ValidatePreconditions(ship, cmd.GoodSymbol, cmd.Units); err != nil {
		return nil, err
	}

	// Reserved-cargo money guard: a coordinator (tour/arb/circuit/held-
	// liquidation), manufacturing, or the CLI must NEVER sell cargo the hull has
	// reserved as do-not-sell — ship hardware bought for outfitting (MODULE_*/MOUNT_*
	// by default) that rides a working hull only to be installed. This is the single
	// choke point every sell funnels through: the sale is refused (no API call, no
	// ledger row, zero units) rather than executed, and the cargo is held aboard.
	// The default classification is pure code, so the module guard holds even when a
	// hull's override state is unreadable (fail-closed, RULINGS #4). Buys are never
	// guarded — a module must be bought before it can be installed.
	if h.strategy.GetTransactionType() == "sell" && ship.IsCargoReserved(cmd.GoodSymbol) {
		logging.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
			"Sell of %s on %s skipped: cargo is reserved (do-not-sell) - held aboard",
			cmd.GoodSymbol, cmd.ShipSymbol), map[string]interface{}{
			"action": "reserved_cargo_skip", "ship_symbol": cmd.ShipSymbol,
			"good": cmd.GoodSymbol, "reason": "reserved",
		})
		return &CargoTransactionResponse{Reserved: true}, nil
	}

	transactionLimit := h.getTransactionLimit(ctx, ship, cmd)
	waypointSymbol := ship.CurrentLocation().Symbol

	response, err := h.executeTransactions(ctx, cmd, token, transactionLimit, waypointSymbol)
	if err != nil {
		return nil, err
	}

	// Note: Ledger recording now happens inside executeTransactions after each batch
	// This ensures partial purchases are recorded even if later batches fail

	return response, nil
}

// getPlayerToken retrieves the player token from the context.
func (h *CargoTransactionHandler) getPlayerToken(ctx context.Context) (string, error) {
	return common.PlayerTokenFromContext(ctx)
}

// loadShip loads the ship from the repository by symbol and player ID.
func (h *CargoTransactionHandler) loadShip(ctx context.Context, cmd *CargoTransactionCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

// validateShipDocked ensures the ship is docked before allowing cargo transactions.
func (h *CargoTransactionHandler) validateShipDocked(ship *navigation.Ship) error {
	if !ship.IsDocked() {
		return fmt.Errorf("ship must be docked to perform cargo transactions")
	}
	return nil
}

// getTransactionLimit retrieves the market's transaction limit for the good.
//
// This limit determines how many units can be transacted in a single API call.
// The handler automatically splits transactions that exceed this limit.
func (h *CargoTransactionHandler) getTransactionLimit(ctx context.Context, ship *navigation.Ship, cmd *CargoTransactionCommand) int {
	waypointSymbol := ship.CurrentLocation().Symbol
	return shipPkg.GetTransactionLimit(ctx, h.marketRepo, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID.Value(), cmd.Units)
}

// executeTransactions performs the cargo transaction in batches, respecting market limits.
//
// The method:
//  1. Splits the total units into batches based on transaction limit
//  2. Executes each batch via the strategy
//  3. Updates ship cargo and persists to DB
//  4. Records ledger entry immediately after each successful batch
//  5. Accumulates results (total amount, units processed, transaction count)
//  6. Returns error on first failure with partial success information (partial success is already recorded)
//
// OPTIMIZATION: waypointSymbol is passed from caller to avoid duplicate ship API load.
// Balance tracking is skipped to avoid GetAgent API call - transactions still recorded.
//
// The net cargo delta this transaction produces is accumulated (unitsProcessed for
// cmd.GoodSymbol) and persisted once, after all batches, via SaveWithRetry so a
// concurrent writer's nav/fuel/other-cargo update on the same hull is re-applied
// rather than last-write-wins clobbered. The persist closure reads the fresh row,
// so no pre-loaded ship snapshot is needed here.
func (h *CargoTransactionHandler) executeTransactions(ctx context.Context, cmd *CargoTransactionCommand, token string, transactionLimit int, waypointSymbol string) (*CargoTransactionResponse, error) {
	totalAmount := 0
	unitsProcessed := 0
	transactionCount := 0
	unitsRemaining := cmd.Units

	transactionType := h.strategy.GetTransactionType()
	floorAborted := false
	floorObservedBid := 0
	ceilingAborted := false
	ceilingObservedAsk := 0

	// serverGoodUnits is the AUTHORITATIVE on-hand count of cmd.GoodSymbol
	// once this transaction is done, learned from a 4219 cargo-shortfall rejection.
	// -1 means the server never corrected us, so the persist applies its own delta as
	// before. shortfallExhausted stops the tranche loop after a clamped retry: the
	// server told us the hold's true depth and we just sold it.
	serverGoodUnits := -1
	shortfallExhausted := false

	// OPTIMIZATION: Skip balance fetch (saves 1 API call)
	// Ledger entries will have balance=0 but transaction amounts are still tracked
	// Always pass 0: the ledger handler derives and serializes the running
	// balance itself. Caller-side chaining from a zero baseline wrote garbage
	// balances on every multi-batch trip (the recurring L28 false alarms).
	const runningBalance = 0

	for unitsRemaining > 0 {
		if tripped, liveBid := h.sellFloorTripped(ctx, cmd, transactionType, waypointSymbol, unitsRemaining); tripped {
			floorAborted = true
			floorObservedBid = liveBid
			break
		}
		if tripped, liveAsk := h.buyCeilingTripped(ctx, cmd, transactionType, waypointSymbol, unitsRemaining); tripped {
			ceilingAborted = true
			ceilingObservedAsk = liveAsk
			break
		}

		unitsToProcess := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToProcess, token)
		if err != nil {
			// SERVER-CARGO RECONCILE: a sell the API rejects with 4219
			// ("cargo does not contain N unit(s) ... Ship has M unit(s)") states the
			// hull's true on-hand count in the payload. The tranche is clamped DOWN to
			// what the server says is aboard and retried once, so a 71→11 rejection
			// still books 11 units of revenue instead of none. Discarding the correction
			// aborts the whole sale at zero transactions, killing the tour and releasing
			// a fully laden hull whose hold is then dumped below the profit floor.
			shortfall := h.retrySellClampedToServerCargo(ctx, cmd, token, unitsToProcess, err)
			if shortfall.known {
				// Heal the cache to the count the server just gave us even when no
				// retry was possible or the retry failed — the stale belief that
				// produced this rejection is exactly what must not survive it.
				serverGoodUnits = shortfall.onHand
			}
			if shortfall.units == 0 || shortfall.err != nil {
				// Nothing sellable aboard, an unreadable rejection, or (b) a clamped
				// retry that failed on its own merits: the transaction fails, but the
				// units earlier tranches DID sell are written back first.
				failure := err
				if shortfall.err != nil {
					failure = shortfall.err
				}
				h.persistCargoDelta(ctx, cmd, transactionType, unitsProcessed, serverGoodUnits)
				return nil, fmt.Errorf("partial failure: failed to %s cargo after %d successful transactions (%d units processed, %d credits): %w",
					transactionType, transactionCount, unitsProcessed, totalAmount, failure)
			}
			// The clamped retry sold what the server said was aboard, so this good is
			// now exhausted: account this tranche and stop — never ask for another.
			result = shortfall.result
			unitsToProcess = shortfall.units
			serverGoodUnits = shortfall.onHand - result.UnitsProcessed
			if serverGoodUnits < 0 {
				serverGoodUnits = 0
			}
			shortfallExhausted = true
		}

		totalAmount += result.TotalAmount
		unitsProcessed += result.UnitsProcessed
		transactionCount++
		unitsRemaining -= unitsToProcess

		// Record ledger entry immediately after each successful batch.
		// The API returns the agent's post-transaction credits in-band per
		// batch; each recorded row re-anchors the ledger to that truth so the
		// running balance can never fork from the live API.
		batchResponse := &CargoTransactionResponse{
			TotalAmount:      result.TotalAmount,
			UnitsProcessed:   result.UnitsProcessed,
			TransactionCount: 1,
		}
		h.recordCargoTransaction(ctx, cmd, waypointSymbol, batchResponse, runningBalance, result.AgentCredits)

		if shortfallExhausted {
			break
		}
	}

	h.persistCargoDelta(ctx, cmd, transactionType, unitsProcessed, serverGoodUnits)

	// Refresh market data once after all batches complete (not per-batch)
	// This reduces API calls from 2N to N+1 for N batches
	h.refreshMarketData(ctx, cmd, waypointSymbol)

	return &CargoTransactionResponse{
		TotalAmount:        totalAmount,
		UnitsProcessed:     unitsProcessed,
		TransactionCount:   transactionCount,
		FloorAborted:       floorAborted,
		FloorObservedBid:   floorObservedBid,
		CeilingAborted:     ceilingAborted,
		CeilingObservedAsk: ceilingObservedAsk,
	}, nil
}
