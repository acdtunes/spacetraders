package cargo

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
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

// MarketRefresher defines the interface for refreshing market data after transactions.
// This interface allows the CargoTransactionHandler to refresh prices without
// creating import cycles with scouting/commands.
type MarketRefresher interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

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

	// MinBidPerUnit (sp-lbbm) is the per-tranche SELL floor: before each sell
	// tranche the handler re-reads the LIVE bid and, if it has fallen below this
	// per-unit floor, ABORTS the remaining tranches and leaves the rest aboard
	// (FloorAborted). It is the fix for the H50 co-dump — five tranches sold for 27
	// credits after the bid crashed 19,950→4. 0 disables the floor entirely, so
	// every non-arb caller (contract delivery, manufacturing, refuel) is unchanged.
	// The arb executor arms it with ceil(fraction × quoted bid); see the arb
	// coordinator. Ignored for purchases.
	MinBidPerUnit int

	// MaxAskPerUnit (sp-9mkf) is the mirror of MinBidPerUnit for the BUY side: the
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

	// FloorAborted (sp-lbbm) is true when the per-tranche sell floor stopped the
	// sale early: the live bid fell below MinBidPerUnit, so the remaining units
	// were held aboard rather than dumped. UnitsProcessed then reports only what
	// sold before the abort. FloorObservedBid is the live bid that tripped the
	// floor (0 when it could not be read — a fail-closed abort). Both stay zero
	// for an unfloored transaction.
	FloorAborted     bool
	FloorObservedBid int

	// CeilingAborted (sp-9mkf) is true when the per-tranche buy ceiling stopped the
	// purchase early: the live ask rose above MaxAskPerUnit, so the remaining units
	// were left unbought. UnitsProcessed then reports only what was bought before the
	// abort. CeilingObservedAsk is the live ask that tripped the ceiling (0 when it
	// could not be read — a fail-closed abort). Both stay zero for an uncapped buy.
	CeilingAborted     bool
	CeilingObservedAsk int

	// Reserved (sp-1vhv) is true when the sell was refused because the good is
	// reserved as do-not-sell on the hull (a staged outfitting module, or an
	// operator-protected good). No API call is made and no ledger row is written;
	// UnitsProcessed and TotalAmount stay zero and the cargo is held aboard. Only
	// ever set on a sell.
	Reserved bool
}

// CargoTransactionHandler orchestrates cargo transaction operations using the Strategy pattern.
//
// This handler unifies the logic previously duplicated between PurchaseCargoHandler
// and SellCargoHandler (~90% code duplication eliminated).
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

	// Reserved-cargo money guard (sp-1vhv): a coordinator (tour/arb/circuit/held-
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
// rather than last-write-wins clobbered (sp-wa7c). The pre-loaded ship snapshot is
// therefore no longer needed here — the persist closure reads the fresh row.
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

	// OPTIMIZATION: Skip balance fetch (saves 1 API call)
	// Ledger entries will have balance=0 but transaction amounts are still tracked
	// Always pass 0: the ledger handler derives and serializes the running
	// balance itself. Caller-side chaining from a zero baseline wrote garbage
	// balances on every multi-batch trip (the recurring L28 false alarms).
	const runningBalance = 0

	for unitsRemaining > 0 {
		// PER-TRANCHE SELL FLOOR (sp-lbbm): before every sell tranche, re-read the
		// LIVE bid and abort the remainder if it has fallen below the armed floor —
		// so a bid our own earlier tranches (or a colliding hull) crushed is never
		// dumped into. The remainder stays aboard for later liquidation. Only sells
		// with MinBidPerUnit>0 arm it; every other caller runs the loop unchanged.
		// Fails CLOSED: a live bid we cannot read (ok=false) holds the remainder too.
		if transactionType == "sell" && cmd.MinBidPerUnit > 0 {
			liveBid, ok := h.liveBidForFloor(ctx, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID)
			if !ok || liveBid < cmd.MinBidPerUnit {
				floorAborted = true
				floorObservedBid = liveBid
				logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
					"Sell floor tripped for %s at %s: live bid %d < floor %d/unit (readable=%t) - aborting remaining %d units, held aboard",
					cmd.GoodSymbol, waypointSymbol, liveBid, cmd.MinBidPerUnit, ok, unitsRemaining), map[string]interface{}{
					"action": "sell_floor_abort", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
					"waypoint": waypointSymbol, "live_bid": liveBid, "floor": cmd.MinBidPerUnit,
					"bid_readable": ok, "units_held": unitsRemaining,
				})
				break
			}
		}

		// PER-TRANCHE BUY CEILING (sp-9mkf): the mirror of the sell floor. Before every
		// buy tranche, re-read the LIVE ask and abort the remainder if it has laddered
		// ABOVE the armed ceiling — so a source ask our own earlier tranches (or a
		// colliding hull) walked up is never bought into above the price that justifies
		// the lane. The remainder is simply left unbought. Only buys with MaxAskPerUnit>0
		// arm it; every other caller runs the loop unchanged. Fails CLOSED: a live ask we
		// cannot read (ok=false) holds the remainder too.
		if transactionType == "purchase" && cmd.MaxAskPerUnit > 0 {
			liveAsk, ok := h.liveAskForCeiling(ctx, waypointSymbol, cmd.GoodSymbol, cmd.PlayerID)
			if !ok || liveAsk > cmd.MaxAskPerUnit {
				ceilingAborted = true
				ceilingObservedAsk = liveAsk
				logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
					"Buy ceiling tripped for %s at %s: live ask %d > ceiling %d/unit (readable=%t) - aborting remaining %d units, left unbought",
					cmd.GoodSymbol, waypointSymbol, liveAsk, cmd.MaxAskPerUnit, ok, unitsRemaining), map[string]interface{}{
					"action": "buy_ceiling_abort", "ship_symbol": cmd.ShipSymbol, "good": cmd.GoodSymbol,
					"waypoint": waypointSymbol, "live_ask": liveAsk, "ceiling": cmd.MaxAskPerUnit,
					"ask_readable": ok, "units_unbought": unitsRemaining,
				})
				break
			}
		}

		unitsToProcess := utils.Min(unitsRemaining, transactionLimit)

		result, err := h.strategy.Execute(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToProcess, token)
		if err != nil {
			// Return error but partial success is already recorded in ledger
			return nil, fmt.Errorf("partial failure: failed to %s cargo after %d successful transactions (%d units processed, %d credits): %w",
				transactionType, transactionCount, unitsProcessed, totalAmount, err)
		}

		totalAmount += result.TotalAmount
		unitsProcessed += result.UnitsProcessed
		transactionCount++
		unitsRemaining -= unitsToProcess

		// Record ledger entry immediately after each successful batch.
		// The API returns the agent's post-transaction credits in-band per
		// batch; each recorded row re-anchors the ledger to that truth so the
		// running balance can never fork from the live API (sp-sc6u).
		batchResponse := &CargoTransactionResponse{
			TotalAmount:      result.TotalAmount,
			UnitsProcessed:   result.UnitsProcessed,
			TransactionCount: 1,
		}
		h.recordCargoTransaction(ctx, cmd, waypointSymbol, batchResponse, runningBalance, result.AgentCredits)
	}

	// Persist the cargo delta this transaction produced onto the FRESH ship row
	// under CAS-retry (sp-wa7c): on a concurrent-writer version conflict the
	// closure re-loads the fresh row and re-applies ONLY this op's own cargo
	// delta for cmd.GoodSymbol, so a colliding writer's nav/fuel/other-cargo
	// update survives instead of being last-write-wins clobbered (the reported
	// cargo desync). This is the single field this transaction owns; the closure
	// touches nothing else. A zero-unit transaction (e.g. floor/ceiling-aborted
	// before any tranche) is not persisted — no spurious version bump. The
	// persist error is intentionally not fatal: the API transaction already
	// committed and the daemon cache reconciles from the API on the next sync
	// (unchanged from the prior best-effort Save).
	if unitsProcessed > 0 {
		_, _, _ = h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if transactionType == "purchase" {
					_ = sh.ReceiveCargo(&shared.CargoItem{Symbol: cmd.GoodSymbol, Units: unitsProcessed})
				} else {
					_ = sh.RemoveCargo(cmd.GoodSymbol, unitsProcessed)
				}
				return true, nil
			})
	}

	// Refresh market data once after all batches complete (not per-batch)
	// This reduces API calls from 2N to N+1 for N batches
	h.refreshMarketData(ctx, cmd.PlayerID, waypointSymbol)

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

// liveBidForFloor reads the current per-unit bid for good at waypoint for the
// sp-lbbm per-tranche sell floor. When a market refresher is wired it live-
// refreshes first and fails CLOSED (ok=false) if the refresh errors — a tranche
// whose live bid cannot be verified must not be sold. With no refresher wired it
// reads the cached bid (fail-open on the missing port, matching the arb buy
// guard's optional-port contract). ok=false on any inability to read a bid, so
// the caller holds the remainder rather than dump it blind.
func (h *CargoTransactionHandler) liveBidForFloor(ctx context.Context, waypoint, good string, playerID shared.PlayerID) (int, bool) {
	if h.marketRefresher != nil {
		if err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypoint); err != nil {
			return 0, false
		}
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil || mkt == nil {
		return 0, false
	}
	g := mkt.FindGood(good)
	if g == nil {
		return 0, false
	}
	return g.PurchasePrice(), true
}

// liveAskForCeiling reads the current per-unit ask for good at waypoint for the
// sp-9mkf per-tranche buy ceiling — the exact mirror of liveBidForFloor. When a
// market refresher is wired it live-refreshes first and fails CLOSED (ok=false) if
// the refresh errors — a tranche whose live ask cannot be verified must not be
// bought. With no refresher wired it reads the cached ask (fail-open on the missing
// port, matching liveBidForFloor). ok=false on any inability to read an ask, so the
// caller holds the remainder (does not buy) rather than purchase blind above the ceiling.
func (h *CargoTransactionHandler) liveAskForCeiling(ctx context.Context, waypoint, good string, playerID shared.PlayerID) (int, bool) {
	if h.marketRefresher != nil {
		if err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypoint); err != nil {
			return 0, false
		}
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID.Value())
	if err != nil || mkt == nil {
		return 0, false
	}
	g := mkt.FindGood(good)
	if g == nil {
		return 0, false
	}
	return g.SellPrice(), true // market SELL price = the ASK the hull pays to buy
}

// recordCargoTransaction records the cargo transaction in the ledger
func (h *CargoTransactionHandler) recordCargoTransaction(
	ctx context.Context,
	cmd *CargoTransactionCommand,
	waypointSymbol string,
	response *CargoTransactionResponse,
	balanceBefore int,
	authoritativeBalance *int,
) {
	logger := logging.LoggerFromContext(ctx)

	// Skip recording if amount is zero (transaction validation requires amount != 0)
	if response.TotalAmount == 0 {
		logger.Log("DEBUG", "Skipping ledger entry for zero-amount transaction", map[string]interface{}{
			"ship":  cmd.ShipSymbol,
			"good":  cmd.GoodSymbol,
			"units": response.UnitsProcessed,
		})
		return
	}

	// Determine transaction type and amount sign
	transactionTypeStr := strings.ToUpper(h.strategy.GetTransactionType())
	var ledgerTxType string
	var amount int
	var balanceAfter int

	if transactionTypeStr == "PURCHASE" {
		ledgerTxType = "PURCHASE_CARGO"
		amount = -response.TotalAmount // Negative for expense
		balanceAfter = balanceBefore - response.TotalAmount
	} else if transactionTypeStr == "SELL" {
		ledgerTxType = "SELL_CARGO"
		amount = response.TotalAmount // Positive for income
		balanceAfter = balanceBefore + response.TotalAmount
	} else {
		logger.Log("ERROR", "Unknown transaction type for ledger recording", map[string]interface{}{
			"type": transactionTypeStr,
		})
		return
	}

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":       agentSymbol,
		"ship_symbol": cmd.ShipSymbol,
		"good_symbol": cmd.GoodSymbol,
		"units":       response.UnitsProcessed,
		"waypoint":    waypointSymbol,
	}

	// sp-br0m: tag a factory input buy with the a5j7 selector branch (ELIGIBLE | RESCUE |
	// era-end | disabled) that chose its source, recorded beside good_symbol so the analyst can
	// grade A1 (supply-first compliance) and split legal RESCUE buys from violations straight
	// from the transactions table. Only the input-buy path stamps the branch onto ctx
	// (production_executor.buyGood); every other caller through this shared recorder — trade,
	// tour, arb, contract delivery, refuel, the fabricated-output harvest — leaves it unset, so
	// the key is simply absent on their rows and their metadata is unchanged.
	if branch, ok := shared.SelectorBranchFromContext(ctx); ok {
		metadata["selector_branch"] = branch
	}

	// Create record transaction command. AuthoritativeBalance carries the
	// in-band agent.credits (when present) so the ledger anchors on API truth
	// rather than the zero-baseline reconstruction below.
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             cmd.PlayerID.Value(),
		TransactionType:      ledgerTxType,
		Amount:               amount,
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("%s %d units of %s at %s", transactionTypeStr, response.UnitsProcessed, cmd.GoodSymbol, waypointSymbol),
		Metadata:             metadata,
	}

	// Propagate operation context if present in the context
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		// No operation context - mark as manual transaction
		recordCmd.OperationType = "manual"
	}

	// Record transaction via mediator
	_, err = h.mediator.Send(context.Background(), recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record cargo transaction in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      cmd.ShipSymbol,
			"good":      cmd.GoodSymbol,
			"amount":    response.TotalAmount,
			"player_id": cmd.PlayerID.Value(),
		})
	} else {
		logger.Log("DEBUG", "Cargo transaction recorded in ledger", map[string]interface{}{
			"ship":   cmd.ShipSymbol,
			"good":   cmd.GoodSymbol,
			"amount": response.TotalAmount,
			"type":   ledgerTxType,
		})
	}
}

// refreshMarketData triggers a market data refresh after a successful transaction.
// This ensures that price data remains up-to-date after buy/sell operations.
// The refresh is non-blocking - errors are logged but don't fail the transaction.
//
// OPTIMIZATION: Skip refresh when called from manufacturing operations (context flag).
// Manufacturing scans markets separately and doesn't need immediate refresh after each sell.
func (h *CargoTransactionHandler) refreshMarketData(ctx context.Context, playerID shared.PlayerID, waypointSymbol string) {
	// Skip if no market refresher is configured
	if h.marketRefresher == nil {
		return
	}

	// OPTIMIZATION: Skip market refresh for manufacturing operations
	// They scan markets independently and don't need post-transaction refresh
	if shared.SkipMarketRefreshFromContext(ctx) {
		return
	}

	logger := logging.LoggerFromContext(ctx)

	err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypointSymbol)
	if err != nil {
		// Log error but don't fail the transaction - market refresh is non-critical
		logger.Log("WARN", "Failed to refresh market data after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
			"error":    err.Error(),
		})
	} else {
		logger.Log("DEBUG", "Market data refreshed after transaction", map[string]interface{}{
			"waypoint": waypointSymbol,
		})
	}
}
