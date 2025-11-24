# Market Price History Implementation Plan

## Executive Summary

**Problem:** The arbitrage system lost -55,050 credits on SHIP_PLATING trades because market data was used without considering price volatility. While the data was only 7 minutes old (not 3 hours - that was a timezone bug), SHIP_PLATING prices changed dramatically between scan and execution.

**Solution:** Implement market price history tracking to detect volatility patterns, enabling:
1. ML-based volatility prediction
2. Data-driven opportunity scoring with volatility penalties
3. Automated identification of unreliable goods/markets

## Requirements

### Functional Requirements
- **FR1:** Record price changes for all goods at all markets
- **FR2:** Track prices (purchase_price, sell_price), supply, activity, and trade_volume
- **FR3:** Only record when prices actually change (deduplication)
- **FR4:** Retain unlimited history for long-term ML training
- **FR5:** Support time-series queries for volatility analysis
- **FR6:** Calculate volatility metrics (price drift, standard deviation, change frequency)

### Non-Functional Requirements
- **NFR1:** Minimal performance impact on market scanning
- **NFR2:** Efficient storage with appropriate indexing
- **NFR3:** Thread-safe concurrent inserts from multiple scout ships
- **NFR4:** Support future ML training dataset exports

## Architecture

### Data Flow

```
Scout Ship → MarketScanner → GetMarketData API
                ↓
        Compare with current market_data
                ↓
        If prices changed → Insert into market_price_history
                ↓
        Update market_data table
```

### Database Schema

#### New Table: `market_price_history`

```sql
CREATE TABLE market_price_history (
    id                SERIAL PRIMARY KEY,
    waypoint_symbol   VARCHAR(50) NOT NULL,
    good_symbol       VARCHAR(100) NOT NULL,
    player_id         INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,

    -- Price data
    purchase_price    INTEGER NOT NULL,  -- What market pays us to sell
    sell_price        INTEGER NOT NULL,  -- What market charges us to buy

    -- Market conditions
    supply            VARCHAR(20),       -- ABUNDANT, MODERATE, SCARCE, etc.
    activity          VARCHAR(20),       -- GROWING, RESTRICTED, etc.
    trade_volume      INTEGER NOT NULL,

    -- Timestamp
    recorded_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    -- Indexes for time-series queries
    CONSTRAINT fk_market_history_player FOREIGN KEY (player_id)
        REFERENCES players(id) ON UPDATE CASCADE ON DELETE CASCADE
);

-- Index for time-series queries on specific market/good pairs
CREATE INDEX idx_market_history_waypoint_good_time
    ON market_price_history(waypoint_symbol, good_symbol, recorded_at DESC);

-- Index for good-specific volatility analysis
CREATE INDEX idx_market_history_good_time
    ON market_price_history(good_symbol, recorded_at DESC);

-- Index for recent history queries
CREATE INDEX idx_market_history_recorded_at
    ON market_price_history(recorded_at DESC);

-- Index for player-specific queries
CREATE INDEX idx_market_history_player
    ON market_price_history(player_id);
```

**Storage Estimates:**
- Row size: ~150 bytes
- 100 markets × 20 goods × 10 price changes/day = 20,000 rows/day
- 30 days = 600,000 rows ≈ 90 MB/month
- 1 year = 7.3M rows ≈ 1.1 GB/year

### Domain Layer

#### Entity: `MarketPriceHistory`

```go
package market

import (
    "time"
    "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketPriceHistory represents a point-in-time snapshot of market prices
// Used for volatility analysis and ML training
type MarketPriceHistory struct {
    id              int
    waypointSymbol  string
    goodSymbol      string
    playerID        shared.PlayerID
    purchasePrice   int    // What market pays us
    sellPrice       int    // What market charges us
    supply          Supply
    activity        Activity
    tradeVolume     int
    recordedAt      time.Time
}

// NewMarketPriceHistory creates a new price history entry
func NewMarketPriceHistory(
    waypointSymbol string,
    goodSymbol string,
    playerID shared.PlayerID,
    purchasePrice int,
    sellPrice int,
    supply Supply,
    activity Activity,
    tradeVolume int,
) (*MarketPriceHistory, error) {
    // Validation
    if waypointSymbol == "" {
        return nil, ErrInvalidWaypointSymbol
    }
    if goodSymbol == "" {
        return nil, ErrInvalidGoodSymbol
    }
    if purchasePrice < 0 || sellPrice < 0 {
        return nil, ErrInvalidPrice
    }

    return &MarketPriceHistory{
        waypointSymbol: waypointSymbol,
        goodSymbol:     goodSymbol,
        playerID:       playerID,
        purchasePrice:  purchasePrice,
        sellPrice:      sellPrice,
        supply:         supply,
        activity:       activity,
        tradeVolume:    tradeVolume,
        recordedAt:     time.Now(),
    }, nil
}

// Getters (immutable entity)
func (h *MarketPriceHistory) ID() int { return h.id }
func (h *MarketPriceHistory) WaypointSymbol() string { return h.waypointSymbol }
func (h *MarketPriceHistory) GoodSymbol() string { return h.goodSymbol }
func (h *MarketPriceHistory) PlayerID() shared.PlayerID { return h.playerID }
func (h *MarketPriceHistory) PurchasePrice() int { return h.purchasePrice }
func (h *MarketPriceHistory) SellPrice() int { return h.sellPrice }
func (h *MarketPriceHistory) Supply() Supply { return h.supply }
func (h *MarketPriceHistory) Activity() Activity { return h.activity }
func (h *MarketPriceHistory) TradeVolume() int { return h.tradeVolume }
func (h *MarketPriceHistory) RecordedAt() time.Time { return h.recordedAt }

// PriceSpread returns the bid-ask spread percentage
func (h *MarketPriceHistory) PriceSpread() float64 {
    if h.purchasePrice == 0 {
        return 0
    }
    return float64(h.sellPrice-h.purchasePrice) / float64(h.purchasePrice) * 100
}
```

#### Repository Interface

```go
package market

import (
    "context"
    "time"
)

// MarketPriceHistoryRepository defines persistence operations for price history
type MarketPriceHistoryRepository interface {
    // RecordPriceChange persists a new price history entry
    RecordPriceChange(ctx context.Context, history *MarketPriceHistory) error

    // GetPriceHistory retrieves price history for a specific market/good pair
    GetPriceHistory(
        ctx context.Context,
        waypointSymbol string,
        goodSymbol string,
        since time.Time,
        limit int,
    ) ([]*MarketPriceHistory, error)

    // GetVolatilityMetrics calculates price volatility statistics for a good
    // Returns: mean price, std deviation, max price change %, change frequency
    GetVolatilityMetrics(
        ctx context.Context,
        goodSymbol string,
        windowHours int,
    ) (*VolatilityMetrics, error)

    // FindMostVolatileGoods identifies goods with highest price drift
    FindMostVolatileGoods(
        ctx context.Context,
        limit int,
        windowHours int,
    ) ([]*GoodVolatility, error)

    // GetMarketStability calculates how stable a specific market is for a good
    GetMarketStability(
        ctx context.Context,
        waypointSymbol string,
        goodSymbol string,
        windowHours int,
    ) (*MarketStability, error)
}

// VolatilityMetrics represents price volatility statistics
type VolatilityMetrics struct {
    GoodSymbol       string
    MeanPrice        float64
    StdDeviation     float64
    MaxPriceChange   float64  // Percentage
    ChangeFrequency  float64  // Changes per hour
    SampleSize       int
}

// GoodVolatility represents volatility ranking for a good
type GoodVolatility struct {
    GoodSymbol      string
    VolatilityScore float64
    ChangeCount     int
}

// MarketStability represents stability metrics for a market/good pair
type MarketStability struct {
    WaypointSymbol  string
    GoodSymbol      string
    StabilityScore  float64  // 0-100, higher is more stable
    PriceRange      int      // Max - Min price
    AvgChangeSize   float64  // Average price change percentage
}
```

### Persistence Layer

#### GORM Model

```go
package persistence

import (
    "time"
)

// MarketPriceHistoryModel represents the market_price_history table
type MarketPriceHistoryModel struct {
    ID              int        `gorm:"column:id;primaryKey;autoIncrement"`
    WaypointSymbol  string     `gorm:"column:waypoint_symbol;size:50;not null;index:idx_market_history_waypoint_good_time"`
    GoodSymbol      string     `gorm:"column:good_symbol;size:100;not null;index:idx_market_history_waypoint_good_time,idx_market_history_good_time"`
    PlayerID        int        `gorm:"column:player_id;not null;index:idx_market_history_player"`
    Player          *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
    PurchasePrice   int        `gorm:"column:purchase_price;not null"`
    SellPrice       int        `gorm:"column:sell_price;not null"`
    Supply          *string    `gorm:"column:supply;size:20"`
    Activity        *string    `gorm:"column:activity;size:20"`
    TradeVolume     int        `gorm:"column:trade_volume;not null"`
    RecordedAt      time.Time  `gorm:"column:recorded_at;not null;default:now();index:idx_market_history_waypoint_good_time,idx_market_history_good_time,idx_market_history_recorded_at"`
}

func (MarketPriceHistoryModel) TableName() string {
    return "market_price_history"
}
```

#### Repository Implementation

```go
package persistence

import (
    "context"
    "fmt"
    "time"
    "gorm.io/gorm"
    "github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

type GormMarketPriceHistoryRepository struct {
    db *gorm.DB
}

func NewGormMarketPriceHistoryRepository(db *gorm.DB) *GormMarketPriceHistoryRepository {
    return &GormMarketPriceHistoryRepository{db: db}
}

func (r *GormMarketPriceHistoryRepository) RecordPriceChange(
    ctx context.Context,
    history *market.MarketPriceHistory,
) error {
    model := &MarketPriceHistoryModel{
        WaypointSymbol: history.WaypointSymbol(),
        GoodSymbol:     history.GoodSymbol(),
        PlayerID:       history.PlayerID().Value(),
        PurchasePrice:  history.PurchasePrice(),
        SellPrice:      history.SellPrice(),
        Supply:         stringPtr(string(history.Supply())),
        Activity:       stringPtr(string(history.Activity())),
        TradeVolume:    history.TradeVolume(),
        RecordedAt:     history.RecordedAt(),
    }

    result := r.db.WithContext(ctx).Create(model)
    if result.Error != nil {
        return fmt.Errorf("failed to record price change: %w", result.Error)
    }

    return nil
}

// ... other methods
```

### Application Layer

#### Market Scanner Integration

**File:** `internal/application/scouting/services/market_scanner.go`

**Modification:**

```go
type MarketScanner struct {
    marketRepo        market.MarketRepository
    priceHistoryRepo  market.MarketPriceHistoryRepository  // NEW
    apiClient         api.Client
    clock             shared.Clock
}

func (s *MarketScanner) ScanAndSave(
    ctx context.Context,
    waypointSymbol string,
    playerID int,
) error {
    logger := common.LoggerFromContext(ctx)

    // Fetch current market data from API
    apiMarketData, err := s.apiClient.GetMarketData(ctx, waypointSymbol)
    if err != nil {
        return fmt.Errorf("failed to fetch market data: %w", err)
    }

    // Get existing market data from database
    existingMarket, err := s.marketRepo.FindByWaypoint(ctx, waypointSymbol, playerID)
    if err != nil && !errors.Is(err, market.ErrMarketNotFound) {
        return fmt.Errorf("failed to query existing market data: %w", err)
    }

    // Convert API response to domain model
    newMarket := s.convertAPIToMarket(apiMarketData, playerID)

    // Record price changes in history
    if s.priceHistoryRepo != nil && existingMarket != nil {
        s.recordPriceChanges(ctx, existingMarket, newMarket, playerID)
    }

    // Save/update market data
    if existingMarket == nil {
        err = s.marketRepo.Save(ctx, newMarket)
    } else {
        err = s.marketRepo.Update(ctx, newMarket)
    }

    if err != nil {
        return fmt.Errorf("failed to save market data: %w", err)
    }

    logger.Log("INFO", "Successfully scanned and saved market data", map[string]interface{}{
        "waypoint": waypointSymbol,
        "goods":    len(newMarket.TradeGoods()),
    })

    return nil
}

// recordPriceChanges compares old and new market data and records changes
func (s *MarketScanner) recordPriceChanges(
    ctx context.Context,
    oldMarket *market.Market,
    newMarket *market.Market,
    playerID int,
) {
    logger := common.LoggerFromContext(ctx)

    // Build map of old goods for quick lookup
    oldGoods := make(map[string]*market.TradeGood)
    for _, good := range oldMarket.TradeGoods() {
        oldGoods[good.Symbol()] = good
    }

    // Compare each good in new market data
    for _, newGood := range newMarket.TradeGoods() {
        oldGood, exists := oldGoods[newGood.Symbol()]

        // Record if good is new or any field changed
        if !exists || s.pricesChanged(oldGood, newGood) {
            playerIDObj, err := shared.NewPlayerID(playerID)
            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Invalid player ID: %v", err), nil)
                continue
            }

            history, err := market.NewMarketPriceHistory(
                newMarket.WaypointSymbol(),
                newGood.Symbol(),
                playerIDObj,
                newGood.PurchasePrice(),
                newGood.SellPrice(),
                newGood.Supply(),
                newGood.Activity(),
                newGood.TradeVolume(),
            )

            if err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to create price history: %v", err), nil)
                continue
            }

            if err := s.priceHistoryRepo.RecordPriceChange(ctx, history); err != nil {
                logger.Log("ERROR", fmt.Sprintf("Failed to record price change: %v", err), nil)
            }
        }
    }
}

// pricesChanged checks if any relevant field changed
func (s *MarketScanner) pricesChanged(old, new *market.TradeGood) bool {
    return old.PurchasePrice() != new.PurchasePrice() ||
           old.SellPrice() != new.SellPrice() ||
           old.Supply() != new.Supply() ||
           old.Activity() != new.Activity() ||
           old.TradeVolume() != new.TradeVolume()
}
```

## Implementation Steps

### Phase 1: Database & Domain (Week 1)
1. Create migration `016_add_market_price_history.up.sql`
2. Run migration on development database
3. Create domain entity `market/market_price_history.go`
4. Add repository interface to `market/ports.go`
5. Create GORM model in `persistence/models.go`

### Phase 2: Persistence & Integration (Week 1-2)
1. Implement `GormMarketPriceHistoryRepository`
2. Add unit tests for repository methods
3. Modify `MarketScanner` to record price changes
4. Wire up repository in daemon server initialization
5. Integration test: scan markets and verify history is recorded

### Phase 3: Query & Analysis (Week 2)
1. Implement `GetVolatilityMetrics()`
2. Implement `FindMostVolatileGoods()`
3. Implement `GetMarketStability()`
4. Add CLI command: `spacetraders market volatility --good SHIP_PLATING`
5. Add CLI command: `spacetraders market history --waypoint X1-YZ19-D47 --good SHIP_PLATING`

### Phase 4: ML Integration (Week 3 - Future)
1. Create CSV export command for ML training
2. Integrate volatility scores into `ArbitrageAnalyzer`
3. Add staleness + volatility penalty to opportunity scoring
4. Train initial volatility prediction model (Python notebook)

## Success Metrics

### Phase 1-2 (Data Collection)
- ✅ History table populating from scout ships
- ✅ Only price changes recorded (no duplicates)
- ✅ Performance: <10ms overhead per market scan
- ✅ Storage: <100 MB/month

### Phase 3 (Analysis)
- ✅ Volatility metrics calculated correctly
- ✅ Can identify top 10 volatile goods
- ✅ Market stability scores correlate with price drift

### Phase 4 (ML & Scoring)
- ✅ Arbitrage system avoids high-volatility opportunities
- ✅ Execution logs show reduced margin_accuracy errors
- ✅ Net profit increases by >20% with volatility filtering

## Risk Mitigation

### Risk 1: Performance Impact on Market Scanning
**Mitigation:**
- Make history recording async (goroutine)
- Add database connection pooling
- Monitor scan latency metrics

### Risk 2: Storage Growth
**Mitigation:**
- Start with unlimited retention, monitor growth
- If needed, add TTL cleanup job (e.g., delete >90 days)
- Partition table by month for faster queries

### Risk 3: Race Conditions (Multiple Scouts)
**Mitigation:**
- Use database transactions
- Rely on SERIAL primary key for uniqueness
- Accept duplicate entries (they're time-ordered anyway)

## Open Questions

1. **Q:** Should we also track failed scans (when API returns error)?
   **A:** No - focus on successful price data only

2. **Q:** Should history be player-specific or shared across all players?
   **A:** Player-specific for now (matches existing market_data design)

3. **Q:** What volatility score threshold should trigger opportunity rejection?
   **A:** TBD - collect data first, analyze distribution, set threshold at Phase 4

## References

- Timezone fix: Migration 015 (timestamptz conversion)
- Price validation bug: `/internal/application/trading/services/arbitrage_executor.go:314`
- Execution logs: `/internal/domain/trading/arbitrage_execution_log.go`
- Market scanner: `/internal/application/scouting/services/market_scanner.go`
