# Arbitrage Cargo Management Implementation Plan

## Overview

Add intelligent cargo management to the arbitrage executor to handle ships that have existing cargo before starting an arbitrage trade. The system should evaluate cargo value and either sell it (if valuable) or jettison it (if not), ensuring ships always have space for new arbitrage opportunities.

## Problem Statement

Currently, the arbitrage executor fails with "ship has no available cargo space" when a ship has ANY existing cargo, preventing it from executing profitable opportunities. This occurs when:
- Ships complete contract deliveries and have remaining cargo
- Previous arbitrage trades leave residual goods
- Ships are manually loaded with cargo for other operations

**Current Behavior:**
```go
// Step 3: Purchase cargo (with safety checks)
availableSpace := ship.Cargo().AvailableCapacity()
if availableSpace <= 0 {
    executionErr = fmt.Errorf("ship has no available cargo space")
    return nil, executionErr
}
```

**Issue:** This check only triggers when cargo is FULL (availableSpace <= 0), but ships with ANY cargo still cause issues.

## Requirements

### Functional Requirements

1. **Detect Existing Cargo**
   - Check if ship has ANY cargo before purchase (not just when full)
   - Trigger cargo management for ANY non-empty cargo hold

2. **Cargo Value Calculation**
   - Query current market data at the ship's location (buy market)
   - Calculate total value: sum(item.Units × market.PurchasePrice)
   - PurchasePrice = what the market PAYS US when we sell to them

3. **Value-Based Decision Making**
   - **Threshold:** 20,000 credits
   - **If value >= 20K:** Sell all cargo at current market
   - **If value < 20K:** Jettison all cargo (not worth the transaction time)

4. **Complete Cargo Clearing**
   - Process ALL cargo items (iterate through Inventory)
   - Reload ship state after clearing to verify success
   - Fail gracefully if cargo can't be cleared

### Non-Functional Requirements

1. **Performance:** Cargo clearing should add minimal latency (<5 seconds for sell, <2 seconds for jettison)
2. **Logging:** Log cargo value calculation, decision rationale, and clearing actions
3. **Safety:** Never proceed with arbitrage if cargo clearing fails
4. **Atomicity:** Cargo clearing is all-or-nothing (if any item fails, abort trade)

## Architecture

### Component Changes

#### 1. ArbitrageExecutor Structure
**File:** `internal/application/trading/services/arbitrage_executor.go`

**Add Field:**
```go
type ArbitrageExecutor struct {
    mediator    common.Mediator
    shipRepo    navigation.ShipRepository
    logRepo     trading.ArbitrageExecutionLogRepository
    marketRepo  scoutingQueries.MarketRepository  // NEW: For querying prices
    purchaseMu  sync.Mutex
}
```

**Update Constructor:**
```go
func NewArbitrageExecutor(
    mediator common.Mediator,
    shipRepo navigation.ShipRepository,
    logRepo trading.ArbitrageExecutionLogRepository,
    marketRepo scoutingQueries.MarketRepository,  // NEW
) *ArbitrageExecutor {
    return &ArbitrageExecutor{
        mediator:   mediator,
        shipRepo:   shipRepo,
        logRepo:    logRepo,
        marketRepo: marketRepo,  // NEW
    }
}
```

#### 2. New Method: handleExistingCargo()

**Signature:**
```go
func (e *ArbitrageExecutor) handleExistingCargo(
    ctx context.Context,
    ship *navigation.Ship,
    playerID shared.PlayerID,
    logger *logging.Logger,
) error
```

**Logic Flow:**
```
1. Get ship's current cargo inventory
2. Query market data at ship's current location
3. For each cargo item:
   a. Find matching TradeGood in market
   b. Calculate item value: units × good.PurchasePrice()
   c. Sum total cargo value
4. Log cargo details and total value
5. If totalValue >= 20000:
   - Log decision: "Selling valuable cargo"
   - For each cargo item:
     - Send CargoTransactionCommand (sell strategy)
   - Return nil on success
6. Else:
   - Log decision: "Jettisoning low-value cargo"
   - For each cargo item:
     - Send JettisonCargoCommand
   - Return nil on success
7. On any error, return error with context
```

**Key Implementation Details:**

```go
// Calculate total cargo value at current market
totalValue := 0
waypointSymbol := ship.CurrentLocation().Symbol

// Query market data
marketQuery := &scoutingQueries.GetMarketDataQuery{
    PlayerID:       playerID,
    WaypointSymbol: waypointSymbol,
}
marketResp, err := e.mediator.Send(ctx, marketQuery)
if err != nil {
    return fmt.Errorf("failed to query market for cargo valuation: %w", err)
}
marketData := marketResp.(*scoutingQueries.GetMarketDataResponse).Market

// Calculate value for each cargo item
cargo := ship.Cargo()
for _, item := range cargo.Inventory {
    good := marketData.FindGood(item.Symbol)
    if good == nil {
        logger.Log("WARN", "Market doesn't trade cargo item", map[string]interface{}{
            "item":   item.Symbol,
            "market": waypointSymbol,
        })
        continue
    }

    itemValue := item.Units * good.PurchasePrice()
    totalValue += itemValue

    logger.Log("DEBUG", "Cargo item valuation", map[string]interface{}{
        "good":       item.Symbol,
        "units":      item.Units,
        "unit_price": good.PurchasePrice(),
        "item_value": itemValue,
    })
}

logger.Log("INFO", "Cargo value calculated", map[string]interface{}{
    "total_value": totalValue,
    "threshold":   20000,
    "items":       len(cargo.Inventory),
})

const valueThreshold = 20000

if totalValue >= valueThreshold {
    // Sell cargo (we're already docked at buy market)
    logger.Log("INFO", "Selling valuable cargo before arbitrage", map[string]interface{}{
        "cargo_value": totalValue,
        "ship":        ship.ShipSymbol(),
    })

    for _, item := range cargo.Inventory {
        sellCmd := &shipCmd.CargoTransactionCommand{
            ShipSymbol: ship.ShipSymbol(),
            GoodSymbol: item.Symbol,
            Units:      item.Units,
            PlayerID:   playerID,
        }

        _, err := e.mediator.Send(ctx, sellCmd)
        if err != nil {
            return fmt.Errorf("failed to sell cargo item %s: %w", item.Symbol, err)
        }

        logger.Log("INFO", "Cargo item sold", map[string]interface{}{
            "good":  item.Symbol,
            "units": item.Units,
        })
    }
} else {
    // Jettison cargo (not worth selling)
    logger.Log("INFO", "Jettisoning low-value cargo before arbitrage", map[string]interface{}{
        "cargo_value": totalValue,
        "ship":        ship.ShipSymbol(),
    })

    for _, item := range cargo.Inventory {
        jettisonCmd := &shipCmd.JettisonCargoCommand{
            ShipSymbol: ship.ShipSymbol(),
            GoodSymbol: item.Symbol,
            Units:      item.Units,
            PlayerID:   playerID,
        }

        _, err := e.mediator.Send(ctx, jettisonCmd)
        if err != nil {
            return fmt.Errorf("failed to jettison cargo item %s: %w", item.Symbol, err)
        }

        logger.Log("INFO", "Cargo item jettisoned", map[string]interface{}{
            "good":  item.Symbol,
            "units": item.Units,
        })
    }
}

return nil
```

#### 3. Modify Execute() Method

**Location:** Line 164-169 in `arbitrage_executor.go`

**Current Code:**
```go
// Step 3: Purchase cargo (with safety checks)
availableSpace := ship.Cargo().AvailableCapacity()
if availableSpace <= 0 {
    executionErr = fmt.Errorf("ship has no available cargo space")
    return nil, executionErr
}
```

**New Code:**
```go
// Step 3: Ensure ship has cargo space (clear existing cargo if needed)
if !ship.Cargo().IsEmpty() {
    logger.Log("INFO", "Ship has existing cargo, clearing before arbitrage", map[string]interface{}{
        "ship":         ship.ShipSymbol(),
        "cargo_units":  ship.Cargo().Units,
        "cargo_items":  len(ship.Cargo().Inventory),
    })

    // Handle existing cargo: sell if valuable (>=20K), else jettison
    err := e.handleExistingCargo(ctx, ship, playerID, logger)
    if err != nil {
        executionErr = fmt.Errorf("failed to clear existing cargo: %w", err)
        return nil, executionErr
    }

    // Reload ship to get updated cargo status after clearing
    ship, err = e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerIDValue)
    if err != nil {
        executionErr = fmt.Errorf("failed to reload ship after clearing cargo: %w", err)
        return nil, executionErr
    }

    logger.Log("INFO", "Cargo cleared successfully", map[string]interface{}{
        "ship":        ship.ShipSymbol(),
        "cargo_units": ship.Cargo().Units,
    })
}

// Verify we now have space
availableSpace := ship.Cargo().AvailableCapacity()
if availableSpace <= 0 {
    executionErr = fmt.Errorf("ship still has no cargo space after clearing")
    return nil, executionErr
}
```

### Instantiation Sites

Update all locations where `ArbitrageExecutor` is created:

**File:** `internal/application/setup/handler_registry.go` (likely location)

**Before:**
```go
arbitrageExecutor := services.NewArbitrageExecutor(
    mediator,
    shipRepo,
    arbitrageLogRepo,
)
```

**After:**
```go
arbitrageExecutor := services.NewArbitrageExecutor(
    mediator,
    shipRepo,
    arbitrageLogRepo,
    marketRepo,  // NEW: Pass market repository
)
```

## Testing Strategy

### Unit Tests (BDD)

Create new feature file: `test/bdd/features/application/arbitrage_cargo_management.feature`

**Scenarios:**
1. Ship with empty cargo proceeds normally
2. Ship with valuable cargo (>= 20K) sells before arbitrage
3. Ship with low-value cargo (< 20K) jettisons before arbitrage
4. Ship with cargo at non-trading market jettisons (market doesn't buy those goods)
5. Cargo clearing fails - arbitrage aborts gracefully

**Example Scenario:**
```gherkin
Feature: Arbitrage Cargo Management

  Scenario: Ship with valuable cargo sells before arbitrage
    Given a ship "SHIP-1" with cargo:
      | good      | units | market_price |
      | IRON_ORE  | 50    | 300          |
      | COPPER    | 30    | 200          |
    And the cargo total value is 21000 credits
    And the ship is docked at a market that buys these goods
    When the arbitrage executor processes the ship
    Then the cargo should be sold for 21000 credits
    And the ship cargo should be empty
    And the arbitrage purchase should proceed

  Scenario: Ship with low-value cargo jettisons before arbitrage
    Given a ship "SHIP-1" with cargo:
      | good      | units | market_price |
      | FERTILIZERS | 10  | 100          |
    And the cargo total value is 1000 credits
    When the arbitrage executor processes the ship
    Then all cargo should be jettisoned
    And the ship cargo should be empty
    And the arbitrage purchase should proceed
```

### Integration Testing

1. **Manual Test:** Start coordinator with ship that has cargo
2. **Monitor Logs:** Verify cargo handling decisions and actions
3. **Database Check:** Verify cargo transactions are recorded in ledger
4. **Monitor Performance:** Ensure cargo clearing adds <5 seconds to trade time

## Deployment Plan

### Phase 1: Development & Testing
1. Implement `handleExistingCargo()` method
2. Modify `Execute()` to call cargo handler
3. Update constructor and instantiation sites
4. Write BDD tests
5. Run test suite locally

### Phase 2: Build & Deploy
1. **Stop Current Coordinator:** `./bin/spacetraders container stop arbitrage_coordinator-X1-YZ19-b51b2e60`
2. **Build Daemon:** `make build-daemon`
3. **Restart Daemon:** `pkill spacetraders-daemon && nohup ./bin/spacetraders-daemon > /tmp/daemon.log 2>&1 &`
4. **Start New Coordinator:** `./bin/spacetraders arbitrage start --player-id 12 --system X1-YZ19 --max-workers 5 --min-margin 50`

### Phase 3: Monitoring
1. **Watch for cargo clearing events** in logs
2. **Verify transactions** in ledger for sold cargo
3. **Monitor execution log** for any cargo-related failures
4. **Track performance impact** on trade duration

## Rollback Plan

If cargo management causes issues:

1. **Immediate:** Stop coordinator
2. **Revert Code:** `git revert <commit-hash>`
3. **Rebuild:** `make build-daemon`
4. **Redeploy:** Restart daemon and coordinator
5. **Verify:** Old behavior (fail on cargo) restored

## Success Criteria

1. ✅ Ships with cargo no longer fail with "no available cargo space"
2. ✅ Valuable cargo (>= 20K) is sold, generating revenue
3. ✅ Low-value cargo (< 20K) is jettisoned quickly
4. ✅ All cargo transactions appear in ledger
5. ✅ Arbitrage trades complete successfully after cargo clearing
6. ✅ No performance degradation (< 5 second overhead)
7. ✅ No false negatives (empty ships still work normally)

## Future Enhancements

1. **Smart Valuation:** Consider opportunity cost of selling vs current arbitrage profit
2. **Partial Clearing:** Only clear enough cargo to fit arbitrage purchase
3. **Cargo Routing:** If cargo value is high, route to better sell market first
4. **ML Integration:** Record cargo clearing decisions for future optimization

## References

- **Cargo Domain Object:** `internal/domain/shared/cargo.go`
- **Market Domain Object:** `internal/domain/market/market.go`
- **Cargo Transaction Command:** `internal/application/ship/commands/cargo_transaction.go`
- **Jettison Cargo Command:** `internal/application/ship/commands/jettison_cargo.go`
- **Market Query:** `internal/application/scouting/queries/get_market_data.go`
