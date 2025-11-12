# Market Liquidity Experiment System - Design Document

**Version:** 1.0
**Date:** 2025-11-10
**Status:** Design Phase

---

## Table of Contents

1. [Overview](#overview)
2. [Experimental Design](#experimental-design)
3. [Architecture](#architecture)
4. [Database Schema](#database-schema)
5. [Component Specifications](#component-specifications)
6. [Work Distribution Algorithm](#work-distribution-algorithm)
7. [API Integrations](#api-integrations)
8. [CLI Interface](#cli-interface)
9. [Monitoring & Analysis](#monitoring--analysis)
10. [Implementation Phases](#implementation-phases)
11. [Testing Strategy](#testing-strategy)
12. [Performance Estimates](#performance-estimates)
13. [Design Decisions](#design-decisions)

---

## Overview

### Purpose

The Market Liquidity Experiment System is designed to systematically gather empirical data about market dynamics in SpaceTraders. It measures how transaction sizes, supply levels, and market activity affect price fluctuations and liquidity across different marketplaces.

### Goals

1. **Supply Depletion Analysis**: Measure how buying transactions affect supply levels at markets with different characteristics (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)
2. **Supply Saturation Analysis**: Measure how selling transactions affect supply levels and market absorption capacity
3. **Cross-Market Price Dynamics**: Test buy/sell operations at different market pairs to understand how supply/activity characteristics affect pricing
4. **Batch Size Effects**: Determine if transaction size has linear or non-linear impact on prices
5. **Market Liquidity Understanding**: Characterize how different markets respond to buy vs sell pressure
6. **Data Collection**: Store detailed transaction data for statistical analysis, model building, and future trading strategy development

### Key Features

- **Multi-ship coordination**: Multiple ships work in parallel for faster data collection
- **Dynamic work distribution**: Ships automatically claim work from shared queue
- **Automatic load balancing**: Fast ships naturally complete more work
- **Fault tolerance**: System continues if individual ships fail
- **Real-time monitoring**: Track progress and performance per ship
- **Comprehensive results**: Unified dataset across all ships for analysis

---

## Experimental Design

### Core Hypothesis

Market behavior depends on:
- **Supply level**: SCARCE markets are less liquid than ABUNDANT markets
- **Activity level**: STRONG activity markets absorb transactions better than WEAK
- **Transaction size**: Large orders (relative to trade_volume) have disproportionate impact
- **Market characteristics**: Different supply/activity combinations respond differently to buy vs sell pressure

### Experimental Variables

**Independent Variables:**
- Market supply level (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)
- Market activity level (WEAK, GROWING, STRONG, RESTRICTED)
- Transaction size (as fraction of trade_volume: 0.1, 0.25, 0.5, 1.0)
- Operation type (BUY vs SELL)
- Market pairing (buy market vs sell market characteristics)

**Dependent Variables:**
- Price change (price_after - price_before)
- Price impact percentage ((price_after - price_before) / price_before × 100)
- Supply level change (categorical shift: MODERATE → LIMITED)
- Price differential between markets (cross-market comparison)

### Test Matrix Structure

**For each trade good (e.g., IRON_ORE, COPPER, QUARTZ):**

1. **Market Selection**: Select 4-8 representative markets with diverse supply/activity
   - Example: SCARCE/WEAK, LIMITED/GROWING, MODERATE/STRONG, ABUNDANT/STRONG, etc.

2. **Pair Generation**: Generate all ordered market pairs (N × (N-1))
   - 6 markets → 30 pairs
   - Each pair: (buy_market, sell_market)
   - Order matters: (A→B) ≠ (B→A)

3. **Per Pair Testing**:
   - **LOOP until all batch sizes/iterations completed:**
     - Navigate to buy_market → Dock
     - **BUY PHASE** - Buy until cargo full or all purchases complete:
       - For each remaining batch size [0.1, 0.25, 0.5, 1.0] of trade_volume:
         - For each remaining iteration (1 to N, default 3):
           - Buy goods (record before/after state)
           - Track actual units bought
           - If cargo full: stop buying, proceed to sell
     - Navigate to sell_market → Dock
     - **SELL PHASE** - Sell everything we just bought:
       - For each batch size/iteration we bought:
         - Sell the exact units we bought in that iteration
         - Record before/after state
     - If not all buys complete: loop back to buy_market

**Total transactions per pair:**
- 4 batch sizes × 3 iterations × 2 operations = 24 transactions

**Navigation per pair:**
- Minimum: 2 navigations (if cargo capacity sufficient for all buys)
- Maximum: 2N navigations where N = number of cycles needed (if cargo repeatedly fills)

**Example system scale:**
- 12 goods × 6 markets/good × 30 pairs/good = 2,160 pairs
- 2,160 pairs × 24 transactions = 51,840 total transactions

### Measurement Protocol

**Buy Phase (Supply Depletion):**
1. Record market state BEFORE: supply, activity, purchase_price, sell_price, trade_volume
2. Execute purchase transaction
3. Record market state AFTER: supply, purchase_price, sell_price
4. Calculate impact: price_impact_percent, supply_change
5. Store in database with operation='BUY'

**Sell Phase (Supply Saturation):**
1. Record market state BEFORE: supply, activity, purchase_price, sell_price, trade_volume
2. Execute sell transaction
3. Record market state AFTER: supply, purchase_price, sell_price
4. Calculate impact: price_impact_percent, supply_change
5. Store in database with operation='SELL'

---

## Architecture

### System Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         User / CLI                               │
└─────────────────────┬───────────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│        MarketLiquidityExperimentCommand (Coordinator)            │
│  - Generate run_id                                               │
│  - Discover all goods in system                                  │
│  - Select representative markets per good                        │
│  - Generate all market pairs                                     │
│  - Populate work queue (all pairs as PENDING)                    │
│  - Launch worker containers (one per ship)                       │
└─────────────────────┬───────────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Database: Work Queue                           │
│  - experiment_work_queue table                                   │
│  - Stores all market pairs with status (PENDING/CLAIMED/etc.)    │
└────────┬─────────────────────────────────┬──────────────────────┘
         │                                 │
    ┌────▼─────┐                      ┌───▼──────┐
    │ Ship 1   │                      │ Ship N   │
    │ Worker   │  ...                 │ Worker   │
    │Container │                      │Container │
    └────┬─────┘                      └───┬──────┘
         │                                │
         └────────────┬───────────────────┘
                      ▼
         ┌────────────────────────────┐
         │  While work available:     │
         │  1. Claim next pair (FIFO) │
         │  2. Execute experiment     │
         │  3. Mark complete          │
         │  4. Loop                   │
         └────────────┬───────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│              Database: Results (market_experiments)              │
│  - Shared run_id across all ships                               │
│  - Transactions from all ships unified                           │
└─────────────────────────────────────────────────────────────────┘
```

### Design Pattern: Dynamic Work Queue

**Coordinator Role:**
- Populates work queue with all tasks
- Launches worker containers
- Does NOT manage task distribution (handled by database)

**Worker Role:**
- Independently claims next available work
- Executes experiment
- Marks work complete/failed
- Repeats until queue empty

**Database Role:**
- Atomic work claiming (prevents double-assignment)
- Central coordination point
- No explicit coordinator process needed

### Advantages of This Pattern

1. **Automatic Load Balancing**: Fast ships naturally claim more work
2. **Fault Tolerance**: Failed ships don't block others
3. **Simplicity**: No complex work distribution algorithms needed
4. **Scalability**: Can add more ships mid-experiment
5. **Resumability**: Can restart experiment; ships pick up unclaimed work
6. **Visibility**: Real-time progress via database queries

---

## Database Schema

### Table: `experiment_work_queue`

Manages work distribution across ships.

```sql
CREATE TABLE experiment_work_queue (
    queue_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    player_id INTEGER NOT NULL REFERENCES players(player_id),

    -- Market pair definition
    pair_id TEXT NOT NULL,                    -- "IRON_ORE:X1-A1:X1-B2"
    good_symbol TEXT NOT NULL,                -- "IRON_ORE"
    buy_market TEXT NOT NULL,                 -- "X1-GZ7-A1"
    sell_market TEXT NOT NULL,                -- "X1-GZ7-B2"

    -- Work status
    status TEXT NOT NULL,                     -- PENDING, CLAIMED, COMPLETED, FAILED
    claimed_by TEXT,                          -- ship_symbol (e.g., "HAULER-1")
    claimed_at TIMESTAMP,
    completed_at TIMESTAMP,

    -- Error tracking
    attempts INTEGER DEFAULT 0,
    error_message TEXT,

    created_at TIMESTAMP NOT NULL,

    INDEX idx_work_queue_run_status (run_id, status)
);
```

**Status Values:**
- `PENDING`: Available for claiming
- `CLAIMED`: Assigned to a ship, in progress
- `COMPLETED`: Successfully finished
- `FAILED`: Encountered error, marked failed

**Key Operations:**
- `enqueue_pairs()`: Bulk insert all pairs as PENDING
- `claim_next_pair()`: Atomic UPDATE to claim next PENDING (FIFO)
- `mark_complete()`: Update status to COMPLETED
- `mark_failed()`: Update status to FAILED with error message

### Table: `market_experiments`

Stores experimental results from all transactions.

```sql
CREATE TABLE market_experiments (
    experiment_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    player_id INTEGER NOT NULL REFERENCES players(player_id),
    ship_symbol TEXT NOT NULL,                -- Which ship performed this
    good_symbol TEXT NOT NULL,

    -- Market pair (matches work queue)
    pair_id TEXT NOT NULL,                    -- Links to work queue entry
    buy_market TEXT,
    sell_market TEXT,

    -- Transaction details
    operation TEXT NOT NULL,                  -- 'BUY' or 'SELL'
    iteration INTEGER NOT NULL,               -- Which iteration (1-3)
    batch_size_fraction REAL,                 -- 0.1, 0.25, 0.5, 1.0
    units INTEGER NOT NULL,
    price_per_unit INTEGER NOT NULL,
    total_credits INTEGER NOT NULL,

    -- Market state BEFORE transaction
    supply_before TEXT,                       -- SCARCE, LIMITED, etc.
    activity_before TEXT,                     -- WEAK, GROWING, etc.
    trade_volume_before INTEGER,
    price_before INTEGER,

    -- Market state AFTER transaction
    supply_after TEXT,
    activity_after TEXT,
    price_after INTEGER,

    -- Calculated metrics
    supply_change TEXT,                       -- e.g., "MODERATE→LIMITED"
    price_impact_percent REAL,                -- (after-before)/before × 100

    timestamp TIMESTAMP NOT NULL,

    INDEX idx_experiments_run (run_id),
    INDEX idx_experiments_ship (run_id, ship_symbol),
    INDEX idx_experiments_good (run_id, good_symbol)
);
```

**Key Queries:**
- Get all results for a run
- Get per-ship performance
- Analyze by supply level
- Analyze by operation type (BUY vs SELL)
- Find profitable pairs

---

## Component Specifications

### 1. SellCargoCommand

**Location:** `src/application/trading/commands/sell_cargo.py`

```python
@dataclass(frozen=True)
class SellCargoCommand(Request[Dict]):
    ship_symbol: str
    trade_symbol: str
    units: int
    player_id: int

class SellCargoHandler(RequestHandler[SellCargoCommand, Dict]):
    def __init__(self, api_client_factory):
        self._api_client_factory = api_client_factory

    async def handle(self, request: SellCargoCommand) -> Dict:
        """Sell cargo at current market.

        Returns:
            {
                'agent': {...},          # Updated agent (credits)
                'cargo': {...},          # Updated ship cargo
                'transaction': {         # Transaction details
                    'shipSymbol': str,
                    'tradeSymbol': str,
                    'type': 'SELL',
                    'units': int,
                    'pricePerUnit': int,
                    'totalPrice': int,
                    'timestamp': str
                }
            }
        """
        api_client = self._api_client_factory(request.player_id)
        return api_client.sell_cargo(
            ship_symbol=request.ship_symbol,
            trade_symbol=request.trade_symbol,
            units=request.units
        )
```

### 2. MarketSelector

**Location:** `src/application/trading/services/market_selector.py`

```python
@dataclass(frozen=True)
class MarketPair:
    pair_id: str              # "IRON_ORE:X1-A1:X1-B2"
    good_symbol: str          # "IRON_ORE"
    buy_market: str           # "X1-GZ7-A1"
    sell_market: str          # "X1-GZ7-B2"

class MarketSelector:
    def __init__(self, market_repository, waypoint_repository):
        self._market_repo = market_repository
        self._waypoint_repo = waypoint_repository

    def select_representative_markets(
        self,
        system: str,
        good: str,
        player_id: int
    ) -> List[Market]:
        """Select diverse markets for testing.

        Strategy:
        1. Query all markets in system selling this good
        2. Group by (supply, activity) combinations
        3. Select 1-2 markets per combination (prefer higher trade_volume)
        4. Aim for 4-8 markets total

        Returns:
            List of Market objects with diverse characteristics
        """
        pass

    def generate_market_pairs(
        self,
        markets: List[Market],
        good: str
    ) -> List[MarketPair]:
        """Generate all ordered market pairs.

        For N markets, generates N × (N-1) pairs.
        Each pair represents: buy at market A, sell at market B.

        Returns:
            List of MarketPair objects
        """
        pairs = []
        for buy_market in markets:
            for sell_market in markets:
                if buy_market.waypoint != sell_market.waypoint:
                    pairs.append(MarketPair(
                        pair_id=f"{good}:{buy_market.waypoint}:{sell_market.waypoint}",
                        good_symbol=good,
                        buy_market=buy_market.waypoint,
                        sell_market=sell_market.waypoint
                    ))
        return pairs
```

### 3. Navigation: NavigateShipCommand

**Important:** All ship navigation in the experiment uses the existing **NavigateShipCommand**, which handles all navigation complexity automatically.

**Location:** `src/application/navigation/commands/navigate_ship.py` (already exists)

**Key Features:**
- **Automatic route planning**: Uses OR-Tools routing engine to find optimal path
- **Fuel management**: Calculates fuel requirements for entire route
- **Automatic refueling**: Stops at fuel stations when needed
- **State transitions**: Handles orbit/dock transitions automatically
- **Multi-waypoint routes**: Can navigate across system boundaries

**Usage in Experiment:**
```python
# Navigate to market (handles everything automatically)
await self._mediator.send_async(NavigateShipCommand(
    ship_symbol=request.ship_symbol,
    destination=waypoint_symbol,  # e.g., "X1-GZ7-A1"
    player_id=request.player_id
))

# Ship arrives at destination, in orbit
# Now can dock
await self._mediator.send_async(DockShipCommand(
    ship_symbol=request.ship_symbol,
    player_id=request.player_id
))
```

**What NavigateShipCommand Does Internally:**
1. Plans route using system graph (shortest path)
2. Checks fuel requirements vs ship's current fuel
3. If insufficient fuel, plans refueling stops
4. Executes navigation (handles IN_TRANSIT status)
5. Waits for arrival
6. Returns when ship reaches destination (status: IN_ORBIT)

**Why This Matters for Experiment:**
- Workers don't need to manage fuel manually
- No need to calculate routes or find refueling stops
- Navigation failures are handled gracefully
- Consistent navigation behavior across all workers

**Timing Implications:**
- Navigation duration varies by distance (30-120 seconds typical)
- Refueling stops add extra time if needed
- This is already factored into performance estimates

### 4. WorkQueueRepository

**Location:** `src/adapters/secondary/persistence/work_queue_repository.py`

**Purpose:** Manages the dynamic work queue using database-based coordination.

```python
class WorkQueueRepository:
    def __init__(self, database):
        self._db = database

    def enqueue_pairs(
        self,
        run_id: str,
        player_id: int,
        pairs: List[MarketPair]
    ):
        """Bulk insert all pairs as PENDING."""
        with self._db.get_connection() as conn:
            conn.executemany("""
                INSERT INTO experiment_work_queue
                (run_id, player_id, pair_id, good_symbol, buy_market, sell_market,
                 status, created_at)
                VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)
            """, [(run_id, player_id, p.pair_id, p.good_symbol, p.buy_market,
                   p.sell_market, datetime.now(timezone.utc)) for p in pairs])

    def claim_next_pair(
        self,
        run_id: str,
        ship_symbol: str
    ) -> Optional[MarketPair]:
        """Atomically claim next PENDING pair (FIFO).

        Uses atomic UPDATE...WHERE...RETURNING pattern to prevent race conditions.

        Returns:
            MarketPair if available, None if queue empty
        """
        with self._db.get_connection() as conn:
            # PostgreSQL supports UPDATE...RETURNING directly
            # For SQLite compatibility in tests, use two queries with transaction

            cursor = conn.execute("""
                SELECT queue_id, pair_id, good_symbol, buy_market, sell_market
                FROM experiment_work_queue
                WHERE run_id = ? AND status = 'PENDING'
                ORDER BY queue_id ASC
                LIMIT 1
            """, (run_id,))

            row = cursor.fetchone()
            if not row:
                return None

            queue_id = row[0]

            # Claim it atomically
            conn.execute("""
                UPDATE experiment_work_queue
                SET status = 'CLAIMED',
                    claimed_by = ?,
                    claimed_at = ?,
                    attempts = attempts + 1
                WHERE queue_id = ?
            """, (ship_symbol, datetime.now(timezone.utc), queue_id))

            conn.commit()

            return MarketPair(
                pair_id=row[1],
                good_symbol=row[2],
                buy_market=row[3],
                sell_market=row[4]
            )

    def mark_complete(self, queue_id: int):
        """Mark pair as COMPLETED."""
        with self._db.get_connection() as conn:
            conn.execute("""
                UPDATE experiment_work_queue
                SET status = 'COMPLETED',
                    completed_at = ?
                WHERE queue_id = ?
            """, (datetime.now(timezone.utc), queue_id))
            conn.commit()

    def mark_failed(self, queue_id: int, error: str):
        """Mark pair as FAILED with error message."""
        with self._db.get_connection() as conn:
            conn.execute("""
                UPDATE experiment_work_queue
                SET status = 'FAILED',
                    error_message = ?,
                    completed_at = ?
                WHERE queue_id = ?
            """, (error, datetime.now(timezone.utc), queue_id))
            conn.commit()

    def get_queue_status(self, run_id: str) -> Dict[str, int]:
        """Get count of pairs by status."""
        with self._db.get_connection() as conn:
            cursor = conn.execute("""
                SELECT status, COUNT(*)
                FROM experiment_work_queue
                WHERE run_id = ?
                GROUP BY status
            """, (run_id,))

            return dict(cursor.fetchall())

    def get_ship_progress(self, run_id: str) -> Dict[str, int]:
        """Get pairs completed per ship."""
        with self._db.get_connection() as conn:
            cursor = conn.execute("""
                SELECT claimed_by, COUNT(*)
                FROM experiment_work_queue
                WHERE run_id = ? AND status = 'COMPLETED'
                GROUP BY claimed_by
            """, (run_id,))

            return dict(cursor.fetchall())
```

### 5. ExperimentRepository

**Location:** `src/adapters/secondary/persistence/experiment_repository.py`

**Purpose:** Stores and retrieves experiment transaction results.

```python
class ExperimentRepository:
    def __init__(self, database):
        self._db = database

    def record_transaction(self, data: Dict):
        """Store a single experiment transaction.

        Args:
            data: Dictionary with all transaction fields:
                - run_id, player_id, ship_symbol, pair_id
                - good_symbol, buy_market, sell_market
                - operation, iteration, batch_size_fraction
                - units, price_per_unit, total_credits
                - supply_before, supply_after, supply_change
                - activity_before, trade_volume_before
                - price_before, price_after, price_impact_percent
                - timestamp
        """
        with self._db.get_connection() as conn:
            conn.execute("""
                INSERT INTO market_experiments (
                    run_id, player_id, ship_symbol, pair_id,
                    good_symbol, buy_market, sell_market,
                    operation, iteration, batch_size_fraction,
                    units, price_per_unit, total_credits,
                    supply_before, supply_after, supply_change,
                    activity_before, trade_volume_before,
                    price_before, price_after, price_impact_percent,
                    timestamp
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """, (
                data['run_id'], data['player_id'], data['ship_symbol'], data['pair_id'],
                data['good_symbol'], data['buy_market'], data['sell_market'],
                data['operation'], data['iteration'], data['batch_size_fraction'],
                data['units'], data['price_per_unit'], data['total_credits'],
                data['supply_before'], data['supply_after'], data['supply_change'],
                data['activity_before'], data['trade_volume_before'],
                data['price_before'], data['price_after'], data['price_impact_percent'],
                data['timestamp']
            ))
            conn.commit()

    def get_transaction_count(self, run_id: str) -> int:
        """Get total transaction count for a run."""
        with self._db.get_connection() as conn:
            cursor = conn.execute("""
                SELECT COUNT(*) FROM market_experiments WHERE run_id = ?
            """, (run_id,))
            return cursor.fetchone()[0]
```

### 6. ShipExperimentWorkerCommand

**Location:** `src/application/trading/commands/ship_experiment_worker.py`

```python
@dataclass(frozen=True)
class ShipExperimentWorkerCommand(Request[Dict]):
    run_id: str
    ship_symbol: str
    player_id: int
    iterations_per_batch: int
    batch_size_fractions: List[float]

class ShipExperimentWorkerHandler(RequestHandler):
    def __init__(
        self,
        work_queue_repo,
        experiment_repo,
        market_repo,
        mediator
    ):
        self._work_queue = work_queue_repo
        self._experiment_repo = experiment_repo
        self._market_repo = market_repo
        self._mediator = mediator

    async def handle(self, request: ShipExperimentWorkerCommand) -> Dict:
        """Worker loop: claim pairs until queue empty."""
        pairs_completed = 0
        pairs_failed = 0

        while True:
            # Atomically claim next pair
            pair = self._work_queue.claim_next_pair(
                request.run_id,
                request.ship_symbol
            )

            if pair is None:
                # Queue empty - we're done
                logger.info(f"Ship {request.ship_symbol}: Queue empty, stopping")
                break

            logger.info(f"Ship {request.ship_symbol}: Starting pair {pair.pair_id}")

            try:
                # Execute full experiment on this pair
                await self._execute_pair_experiment(request, pair)

                # Mark complete
                self._work_queue.mark_complete(pair.queue_id)
                pairs_completed += 1

                logger.info(f"Ship {request.ship_symbol}: Completed {pair.pair_id} "
                           f"({pairs_completed} total)")

            except Exception as e:
                # Mark failed, continue to next pair
                self._work_queue.mark_failed(pair.queue_id, str(e))
                pairs_failed += 1
                logger.error(f"Ship {request.ship_symbol}: Failed {pair.pair_id}: {e}")

        return {
            'ship_symbol': request.ship_symbol,
            'pairs_completed': pairs_completed,
            'pairs_failed': pairs_failed
        }

    async def _execute_pair_experiment(
        self,
        request: ShipExperimentWorkerCommand,
        pair: MarketPair
    ):
        """Execute buy/sell experiment for one market pair.

        Handles cargo capacity by looping:
        1. Buy until cargo full or all purchases complete
        2. Sell everything
        3. If more buys pending, return to buy market and repeat

        Ensures ALL batch sizes/iterations are tested even with limited cargo.
        """

        # Track which buys are pending
        pending_buys = [
            (batch_fraction, iteration)
            for batch_fraction in request.batch_size_fractions
            for iteration in range(1, request.iterations_per_batch + 1)
        ]

        # Get initial market data for trade volume info
        system = extract_system_from_waypoint(pair.buy_market)

        # Loop until all buys complete
        while pending_buys:
            # Track what we buy in this cycle (for selling later)
            units_bought_this_cycle = {}

            # === BUY PHASE ===
            # Navigate to buy market
            await self._mediator.send_async(NavigateShipCommand(
                ship_symbol=request.ship_symbol,
                destination=pair.buy_market,
                player_id=request.player_id
            ))

            # Dock
            await self._mediator.send_async(DockShipCommand(
                ship_symbol=request.ship_symbol,
                player_id=request.player_id
            ))

            # Get market data
            market_data = await self._mediator.send_async(GetMarketQuery(
                system=system,
                waypoint=pair.buy_market,
                player_id=request.player_id
            ))

            # Find trade good in market
            trade_good = next(
                (g for g in market_data.trade_goods if g.symbol == pair.good_symbol),
                None
            )

            if not trade_good:
                raise ValueError(f"Good {pair.good_symbol} not available at {pair.buy_market}")

            # Execute purchases for pending buys (until cargo full)
            buys_completed_this_cycle = []
            cargo_full = False

            for (batch_fraction, iteration) in pending_buys:
                if cargo_full:
                    break  # Stop buying, proceed to sell

                units_to_buy = int(trade_good.trade_volume * batch_fraction)

                # Get market state BEFORE
                market_before = await self._mediator.send_async(
                    GetMarketQuery(
                        system=system,
                        waypoint=pair.buy_market,
                        player_id=request.player_id
                    )
                )

                good_before = next(
                    g for g in market_before.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Execute purchase
                try:
                    result = await self._mediator.send_async(PurchaseCargoCommand(
                        ship_symbol=request.ship_symbol,
                        trade_symbol=pair.good_symbol,
                        units=units_to_buy,
                        player_id=request.player_id
                    ))

                    # Track actual units bought for this cycle
                    units_actually_bought = result['transaction']['units']
                    units_bought_this_cycle[(batch_fraction, iteration)] = units_actually_bought

                    # Mark this buy as complete
                    buys_completed_this_cycle.append((batch_fraction, iteration))

                except Exception as e:
                    # Cargo full - stop buying, proceed to sell what we have
                    if 'cargo' in str(e).lower() or 'capacity' in str(e).lower():
                        logger.info(f"Cargo full at {pair.buy_market}, proceeding to sell")
                        cargo_full = True
                        break
                    else:
                        # Other error - log and continue
                        logger.error(f"Buy failed at {pair.buy_market}: {e}")
                        buys_completed_this_cycle.append((batch_fraction, iteration))
                        units_bought_this_cycle[(batch_fraction, iteration)] = 0
                        continue

                # Get market state AFTER
                market_after = await self._mediator.send_async(
                    GetMarketQuery(
                        system=system,
                        waypoint=pair.buy_market,
                        player_id=request.player_id
                    )
                )

                good_after = next(
                    g for g in market_after.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Calculate impact
                price_impact = (
                    (good_after.sell_price - good_before.sell_price)
                    / good_before.sell_price * 100
                )

                supply_change = f"{good_before.supply}→{good_after.supply}"

                # Record transaction
                self._experiment_repo.record_transaction({
                    'run_id': request.run_id,
                    'player_id': request.player_id,
                    'ship_symbol': request.ship_symbol,
                    'pair_id': pair.pair_id,
                    'good_symbol': pair.good_symbol,
                    'buy_market': pair.buy_market,
                    'sell_market': pair.sell_market,
                    'operation': 'BUY',
                    'iteration': iteration,
                    'batch_size_fraction': batch_fraction,
                    'units': units_actually_bought,
                    'price_per_unit': result['transaction']['pricePerUnit'],
                    'total_credits': result['transaction']['totalPrice'],
                    'supply_before': good_before.supply,
                    'activity_before': good_before.activity,
                    'trade_volume_before': good_before.trade_volume,
                    'price_before': good_before.sell_price,
                    'supply_after': good_after.supply,
                    'activity_after': good_after.activity,
                    'price_after': good_after.sell_price,
                    'supply_change': supply_change,
                    'price_impact_percent': price_impact,
                    'timestamp': datetime.now(timezone.utc)
                })

            # Remove completed buys from pending list
            for buy in buys_completed_this_cycle:
                pending_buys.remove(buy)

            # === NAVIGATE TO SELL MARKET ===
            await self._mediator.send_async(NavigateShipCommand(
                ship_symbol=request.ship_symbol,
                destination=pair.sell_market,
                player_id=request.player_id
            ))

            await self._mediator.send_async(DockShipCommand(
                ship_symbol=request.ship_symbol,
                player_id=request.player_id
            ))

            # === SELL PHASE ===
            # Sell the exact units we bought in this cycle
            for (batch_fraction, iteration), units_to_sell in units_bought_this_cycle.items():
                if units_to_sell == 0:
                    # Nothing to sell (buy failed)
                    continue

                market_before = await self._mediator.send_async(
                    GetMarketQuery(
                        system=system,
                        waypoint=pair.sell_market,
                        player_id=request.player_id
                    )
                )

                good_before = next(
                    g for g in market_before.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Execute sell
                result = await self._mediator.send_async(SellCargoCommand(
                    ship_symbol=request.ship_symbol,
                    trade_symbol=pair.good_symbol,
                    units=units_to_sell,
                    player_id=request.player_id
                ))

                market_after = await self._mediator.send_async(
                    GetMarketQuery(
                        system=system,
                        waypoint=pair.sell_market,
                        player_id=request.player_id
                    )
                )

                good_after = next(
                    g for g in market_after.trade_goods
                    if g.symbol == pair.good_symbol
                )

                # Calculate impact (for sell, use purchase_price)
                price_impact = (
                    (good_after.purchase_price - good_before.purchase_price)
                    / good_before.purchase_price * 100
                )

                supply_change = f"{good_before.supply}→{good_after.supply}"

                # Record transaction
                self._experiment_repo.record_transaction({
                    'run_id': request.run_id,
                    'player_id': request.player_id,
                    'ship_symbol': request.ship_symbol,
                    'pair_id': pair.pair_id,
                    'good_symbol': pair.good_symbol,
                    'buy_market': pair.buy_market,
                    'sell_market': pair.sell_market,
                    'operation': 'SELL',
                    'iteration': iteration,
                    'batch_size_fraction': batch_fraction,
                    'units': units_to_sell,
                    'price_per_unit': result['transaction']['pricePerUnit'],
                    'total_credits': result['transaction']['totalPrice'],
                    'supply_before': good_before.supply,
                    'activity_before': good_before.activity,
                    'trade_volume_before': good_before.trade_volume,
                    'price_before': good_before.purchase_price,
                    'supply_after': good_after.supply,
                    'activity_after': good_after.activity,
                    'price_after': good_after.purchase_price,
                    'supply_change': supply_change,
                    'price_impact_percent': price_impact,
                    'timestamp': datetime.now(timezone.utc)
                })

            # Cargo now empty, loop continues if more buys pending
```

### 7. MarketLiquidityExperimentCommand

**Location:** `src/application/trading/commands/market_liquidity_experiment.py`

```python
@dataclass(frozen=True)
class MarketLiquidityExperimentCommand(Request[Dict]):
    ship_symbols: List[str]
    player_id: int
    system_symbol: str
    iterations_per_batch: int = 3
    batch_size_fractions: List[float] = field(
        default_factory=lambda: [0.1, 0.25, 0.5, 1.0]
    )

class MarketLiquidityExperimentHandler(RequestHandler):
    def __init__(
        self,
        market_selector,
        work_queue_repo,
        market_repo,
        daemon_client
    ):
        self._market_selector = market_selector
        self._work_queue = work_queue_repo
        self._market_repo = market_repo
        self._daemon_client = daemon_client

    async def handle(self, request: MarketLiquidityExperimentCommand) -> Dict:
        """Coordinator: populate queue and launch workers."""

        # 1. Generate unique run_id
        run_id = str(uuid.uuid4())

        logger.info(f"Starting liquidity experiment: run_id={run_id}")
        logger.info(f"Fleet: {len(request.ship_symbols)} ships")
        logger.info(f"System: {request.system_symbol}")

        # 2. Discover all trade goods in system
        all_goods = self._discover_goods_in_system(
            request.system_symbol,
            request.player_id
        )

        logger.info(f"Discovered {len(all_goods)} goods")

        # 3. Generate all market pairs
        all_pairs = []

        for good in all_goods:
            # Select representative markets
            markets = self._market_selector.select_representative_markets(
                request.system_symbol,
                good,
                request.player_id
            )

            # Generate pairs
            pairs = self._market_selector.generate_market_pairs(markets, good)
            all_pairs.extend(pairs)

            logger.info(f"  {good}: {len(markets)} markets → {len(pairs)} pairs")

        logger.info(f"Total: {len(all_pairs)} market pairs")

        # 4. Populate work queue
        self._work_queue.enqueue_pairs(run_id, request.player_id, all_pairs)

        logger.info(f"Work queue populated: {len(all_pairs)} PENDING")

        # 5. Create daemon container for each ship
        container_ids = []

        for ship_symbol in request.ship_symbols:
            worker_command = ShipExperimentWorkerCommand(
                run_id=run_id,
                ship_symbol=ship_symbol,
                player_id=request.player_id,
                iterations_per_batch=request.iterations_per_batch,
                batch_size_fractions=request.batch_size_fractions
            )

            container_id = self._daemon_client.create_container(
                command=worker_command,
                ship_symbol=ship_symbol,
                player_id=request.player_id
            )

            container_ids.append(container_id)
            logger.info(f"Created worker container: {ship_symbol} → {container_id}")

        return {
            'run_id': run_id,
            'container_ids': container_ids,
            'total_pairs': len(all_pairs),
            'ships': len(request.ship_symbols),
            'goods': len(all_goods)
        }

    def _discover_goods_in_system(self, system: str, player_id: int) -> List[str]:
        """Get all unique trade goods across all markets in system."""
        markets = self._market_repo.list_markets_in_system(
            system,
            player_id,
            max_age_minutes=60
        )

        goods = set()
        for market in markets:
            for trade_good in market.trade_goods:
                goods.add(trade_good.symbol)

        return sorted(list(goods))
```

---

## Work Distribution Algorithm

### Atomic Claiming Pattern

The work queue uses an **atomic claim operation** to prevent race conditions:

```sql
-- Worker attempts to claim next pair
BEGIN TRANSACTION;

-- Find next PENDING pair (FIFO)
SELECT queue_id, pair_id, good_symbol, buy_market, sell_market
FROM experiment_work_queue
WHERE run_id = ? AND status = 'PENDING'
ORDER BY queue_id ASC
LIMIT 1;

-- If found, atomically claim it
UPDATE experiment_work_queue
SET status = 'CLAIMED',
    claimed_by = ?,
    claimed_at = NOW(),
    attempts = attempts + 1
WHERE queue_id = ?;

COMMIT;
```

**Key Properties:**
- **Atomic**: Transaction ensures no two ships claim same pair
- **FIFO**: ORDER BY queue_id ASC ensures oldest work claimed first
- **Idempotent**: Can retry safely; claim either succeeds or returns NULL
- **Lock-free**: No explicit locking needed; database handles concurrency

### Worker Loop Pseudocode

```python
while True:
    pair = work_queue.claim_next_pair(run_id, ship_symbol)

    if pair is None:
        # Queue empty
        break

    try:
        execute_experiment(pair)
        work_queue.mark_complete(pair.queue_id)
    except Exception as e:
        work_queue.mark_failed(pair.queue_id, str(e))
        # Continue to next pair
```

### Load Balancing Behavior

**Scenario: 3 ships with different speeds**
- Ship A: Fast (completes pairs quickly)
- Ship B: Medium (moderate speed)
- Ship C: Slow (takes longer per pair)

**Result:**
- Ship A completes more pairs (fast)
- Ship B completes moderate number
- Ship C completes fewer pairs (slow)
- Total work shared proportionally to speed

**Load balancing is automatic** - fast ships naturally do more work, no explicit rebalancing needed.

---

## API Integrations

### New API Endpoint: Sell Cargo

**SpaceTraders API v2:**
```
POST /my/ships/{shipSymbol}/sell
Content-Type: application/json

{
  "symbol": "IRON_ORE",
  "units": 50
}
```

**Response:**
```json
{
  "data": {
    "agent": {
      "accountId": "...",
      "symbol": "AGENT-1",
      "headquarters": "X1-GZ7-A1",
      "credits": 155000,
      "startingFaction": "COSMIC"
    },
    "cargo": {
      "capacity": 40,
      "units": 0,
      "inventory": []
    },
    "transaction": {
      "waypointSymbol": "X1-GZ7-B2",
      "shipSymbol": "AGENT-1-HAULER-1",
      "tradeSymbol": "IRON_ORE",
      "type": "SELL",
      "units": 50,
      "pricePerUnit": 120,
      "totalPrice": 6000,
      "timestamp": "2023-11-08T12:34:56.789Z"
    }
  }
}
```

### API Client Implementation

**Interface:** `src/ports/outbound/api_client.py`
```python
@abstractmethod
def sell_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
    """Sell cargo at market. Ship must be docked.

    Returns:
        {
            'agent': {...},
            'cargo': {...},
            'transaction': {...}
        }
    """
    pass
```

**Implementation:** `src/adapters/secondary/api/client.py`
```python
def sell_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
    response = self._request(
        "POST",
        f"/my/ships/{ship_symbol}/sell",
        json={"symbol": trade_symbol, "units": units}
    )
    return response['data']
```

---

## CLI Interface

### Main Experiment Command

```bash
spacetraders experiment liquidity \
  --ships HAULER-1,HAULER-2,HAULER-3 \
  --system X1-GZ7 \
  [--iterations-per-batch 3] \
  [--batch-sizes 0.1,0.25,0.5,1.0]
```

**Options:**
- `--ships`: Comma-separated list of ship symbols (required)
- `--system`: System symbol to test (required)
- `--iterations-per-batch`: How many times to repeat each batch size (default: 3)
- `--batch-sizes`: Comma-separated batch size fractions (default: 0.1,0.25,0.5,1.0)

**Output:**
```
Starting multi-ship liquidity experiment...

Fleet: 3 ships (HAULER-1, HAULER-2, HAULER-3)
System: X1-GZ7
Discovered: 12 goods
  IRON_ORE: 6 markets → 30 pairs
  COPPER: 5 markets → 20 pairs
  QUARTZ: 4 markets → 12 pairs
  ...
Total: 2,256 market pairs

Work queue populated: 2,256 pairs (PENDING)

Created worker containers:
- HAULER-1: container_abc123
- HAULER-2: container_def456
- HAULER-3: container_ghi789

Experiment run_id: 550e8400-e29b-41d4-a716-446655440000

Ships will dynamically claim pairs until queue empty.

Monitor progress:
  spacetraders experiment status --run-id 550e8400-e29b-41d4-a716-446655440000
  spacetraders daemon logs container_abc123
```

### Status Command

```bash
spacetraders experiment status --run-id <UUID>
```

**Output:**
```
Experiment: 550e8400-e29b-41d4-a716-446655440000
Progress: 1420/2256 pairs (62.9%)

Ship Performance:
- HAULER-2: 512 pairs ████████████████░░░░ (36%)
- HAULER-1: 487 pairs ███████████████░░░░░ (34%)
- HAULER-3: 421 pairs █████████████░░░░░░░ (30%)

Queue Status:
- Completed: 1420
- In Progress: 3
- Pending: 833
- Failed: 0
```

### Accessing Experiment Data

All experiment data is stored in the PostgreSQL database. Query it directly:

```bash
# Connect to database
psql spacetraders

# View all experiments
SELECT run_id, COUNT(*) as transactions, MIN(timestamp) as started, MAX(timestamp) as last_update
FROM market_experiments
GROUP BY run_id
ORDER BY started DESC;

# Get all data for specific experiment
SELECT * FROM market_experiments WHERE run_id = '550e8400-e29b-41d4-a716-446655440000';

# Export to CSV using psql
\copy (SELECT * FROM market_experiments WHERE run_id = '550e8400-e29b-41d4-a716-446655440000') TO 'results.csv' CSV HEADER;
```

**Analyze with your preferred tools:**
- SQL queries directly on PostgreSQL
- Python pandas: `pd.read_sql()`
- R: `dbConnect()`
- Any database client: DBeaver, pgAdmin, etc.

**No export commands needed** - data is already accessible in the database!

---

## Monitoring & Analysis

### Real-Time Progress Tracking

**Database Query:**
```sql
SELECT
    COUNT(*) FILTER (WHERE status = 'PENDING') as pending,
    COUNT(*) FILTER (WHERE status = 'CLAIMED') as in_progress,
    COUNT(*) FILTER (WHERE status = 'COMPLETED') as completed,
    COUNT(*) FILTER (WHERE status = 'FAILED') as failed
FROM experiment_work_queue
WHERE run_id = ?;
```

**Per-Ship Performance:**
```sql
SELECT
    claimed_by as ship,
    COUNT(*) as pairs_completed
FROM experiment_work_queue
WHERE run_id = ? AND status = 'COMPLETED'
GROUP BY claimed_by
ORDER BY pairs_completed DESC;
```

### Raw Data Queries

**Get All Experiment Transactions:**
```sql
SELECT *
FROM market_experiments
WHERE run_id = ?
ORDER BY timestamp ASC;
```

**Export to CSV (via application code):**
```python
# Application layer will provide CSV export functionality
# Queries all transactions and writes to CSV file
# No complex aggregations - just raw data export
```

**Note:** Complex analysis queries (aggregations, correlations, statistical analysis) should be performed on exported data using external tools. This keeps the application simple and allows for flexible analysis approaches.

---

## Implementation Phases

### Phase 1: Foundation

**Files to Create:**
1. `src/application/trading/commands/sell_cargo.py`
   - SellCargoCommand + Handler

**Files to Modify:**
2. `src/ports/outbound/api_client.py`
   - Add `sell_cargo()` abstract method

3. `src/adapters/secondary/api/client.py`
   - Implement `sell_cargo()`

4. `src/configuration/container.py`
   - Register SellCargoHandler

**Testing:**
- BDD feature: `sell_cargo.feature`
- Scenario: Sell cargo at market
- Scenario: Sell with insufficient inventory

**Validation:**
- Can sell cargo via CLI test command
- Transaction recorded correctly

### Phase 2: Database Schema

**Files to Modify:**
1. `src/adapters/secondary/persistence/models.py`
   - Add `experiment_work_queue` table
   - Add `market_experiments` table

**Files to Create:**
2. `src/adapters/secondary/persistence/work_queue_repository.py`
   - Atomic claim operations
   - Status queries

3. `src/adapters/secondary/persistence/experiment_repository.py`
   - Record transactions
   - Result queries

4. `src/configuration/container.py`
   - Register repositories

**Testing:**
- Unit tests for repository methods
- Test atomic claiming with concurrent access

**Validation:**
- Can enqueue pairs
- Can atomically claim pairs (no double-assignment)
- Can record experiment results

### Phase 3: Market Selection Services

**Files to Create:**
1. `src/application/trading/services/market_selector.py`
   - `select_representative_markets()`
   - `generate_market_pairs()`

2. `src/configuration/container.py`
   - Register service

**Testing:**
- BDD feature: `market_selection.feature`
- Scenario: Select diverse markets for good
- Scenario: Generate all market pairs

**Validation:**
- Returns markets with different supply/activity
- Generates N×(N-1) pairs correctly

### Phase 4: Worker Command

**Files to Create:**
1. `src/application/trading/commands/ship_experiment_worker.py`
   - ShipExperimentWorkerCommand + Handler
   - Worker loop with claim/execute/mark pattern
   - Buy phase logic
   - Sell phase logic

2. `src/configuration/container.py`
   - Register handler

**Testing:**
- BDD feature: `ship_worker_loop.feature`
- Scenario: Worker claims pair, executes, marks complete
- Scenario: Worker stops when queue empty
- Scenario: Worker continues after pair failure

**Validation:**
- Worker can claim pairs
- Buy/sell experiments execute correctly
- Results recorded in database
- Worker stops gracefully when done

### Phase 5: Coordinator Command

**Files to Create:**
1. `src/application/trading/commands/market_liquidity_experiment.py`
   - MarketLiquidityExperimentCommand + Handler
   - Discover goods
   - Generate pairs
   - Populate queue
   - Launch workers

2. `src/configuration/container.py`
   - Register handler

**Testing:**
- BDD feature: `multi_ship_coordination.feature`
- Scenario: Coordinator populates queue and launches workers
- Integration test: 2 ships, 10 pairs

**Validation:**
- Queue populated correctly
- Worker containers created
- Ships start claiming work

### Phase 6: CLI Integration

**Files to Create:**
1. `src/adapters/primary/cli/experiment_cli.py`
   - `liquidity` command
   - `status` command
   - `export` command
   - `analyze` commands

**Files to Modify:**
2. `src/adapters/primary/cli/main.py`
   - Wire experiment subcommand group

**Testing:**
- Manual CLI testing
- Integration test end-to-end

**Validation:**
- Can start experiment from CLI
- Can monitor status
- Can view results

### Phase 7: Status Monitoring

**Files to Create:**
1. `src/application/trading/queries/get_experiment_status.py`

2. Update `experiment_cli.py` with status command

**Testing:**
- Test status query shows correct progress
- Test with multiple ships running

**Validation:**
- Status shows real-time progress
- Per-ship performance visible
- Queue status accurate

### Phase 8: Polish & Documentation

- Add logging throughout
- Error handling refinement
- Update CLAUDE.md with experiment usage
- Integration testing with real API

---

## Testing Strategy

### BDD Features

#### 1. `sell_cargo.feature`
```gherkin
Feature: Sell Cargo at Market
  As a trader
  I want to sell cargo at markets
  So that I can realize profits from purchases

  Scenario: Successfully sell cargo at market
    Given a player with agent "TEST-AGENT"
    And a ship "TEST-1" docked at market "X1-A1"
    And the ship has 50 units of "IRON_ORE" in cargo
    When I sell 50 units of "IRON_ORE" from ship "TEST-1"
    Then the ship cargo contains 0 units of "IRON_ORE"
    And the player credits increased by the sale amount

  Scenario: Cannot sell cargo not in inventory
    Given a ship "TEST-1" docked at market "X1-A1"
    And the ship has 0 units of "IRON_ORE" in cargo
    When I attempt to sell 50 units of "IRON_ORE"
    Then the command should fail with "insufficient inventory"
```

#### 2. `market_selection.feature`
```gherkin
Feature: Market Selection for Experiments

  Scenario: Select representative markets for a good
    Given a system "X1-GZ7" with 20 markets
    And 8 markets sell "IRON_ORE" with various supply levels
    When I select representative markets for "IRON_ORE"
    Then I should get 4-8 markets
    And the markets should have diverse supply levels
    And the markets should have diverse activity levels

  Scenario: Generate market pairs
    Given 5 representative markets
    When I generate market pairs
    Then I should get 20 pairs (5 × 4)
    And each pair should have different buy and sell markets
```

#### 3. `work_queue_operations.feature`
```gherkin
Feature: Work Queue Operations

  Scenario: Enqueue market pairs
    Given an experiment run with run_id "test-123"
    And 100 market pairs
    When I enqueue all pairs
    Then all 100 pairs should be PENDING

  Scenario: Atomically claim next pair
    Given a work queue with 10 PENDING pairs
    When ship "HAULER-1" claims next pair
    Then the pair status should be CLAIMED
    And the pair claimed_by should be "HAULER-1"
    And 9 pairs should remain PENDING

  Scenario: No double-assignment of pairs
    Given a work queue with 1 PENDING pair
    When ship "HAULER-1" and ship "HAULER-2" claim simultaneously
    Then only one ship should successfully claim the pair
    And the other ship should receive None
```

#### 4. `ship_worker_loop.feature`
```gherkin
Feature: Ship Worker Loop

  Scenario: Worker processes pairs until queue empty
    Given an experiment run "test-123"
    And a work queue with 3 PENDING pairs
    And a worker ship "HAULER-1"
    When the worker starts
    Then it should claim and complete all 3 pairs
    And it should stop when queue is empty

  Scenario: Worker continues after pair failure
    Given a work queue with 5 pairs
    And pair 3 will cause an error
    When the worker processes pairs
    Then pairs 1, 2, 4, 5 should be COMPLETED
    And pair 3 should be FAILED with error message
```

#### 5. `multi_ship_coordination.feature`
```gherkin
Feature: Multi-Ship Coordination

  Scenario: Multiple ships process queue in parallel
    Given an experiment run "test-123"
    And a work queue with 30 pairs
    And 3 worker ships
    When all workers start simultaneously
    Then each ship should claim different pairs
    And all 30 pairs should eventually be COMPLETED
    And no pair should be claimed by multiple ships
```

### Integration Tests

**Full End-to-End Test:**
1. Start experiment with 2 ships
2. Queue has 10 pairs
3. Ships process in parallel
4. Verify all transactions recorded
5. Query results and validate aggregations
6. Check status command shows correct progress

---

## Performance Characteristics

### Scale Expectations

**Experiment Scope:**
- System with 12 goods, ~6 representative markets per good
- ~30 market pairs per good (N×(N-1) combinations)
- Total: ~2,160 market pairs
- Transactions per pair: 24 (4 batch sizes × 3 iterations × 2 operations)
- Total transactions: ~51,840

**Multi-Ship Parallelization:**
- Single ship: Long-running (multiple days)
- 3 ships: ~3× faster with automatic load balancing
- 6 ships: ~6× faster (near-linear scaling)
- Fast ships naturally complete more work than slow ships

**Bottlenecks:**
- Navigation time between markets (varies by distance and refueling needs)
- API rate limits (handled by existing rate limiter)
- Market price update frequency (may not update on every transaction)

**Load Balancing:**
- Dynamic work queue provides automatic load balancing
- Ships claim work independently - no coordination overhead
- No idle time wasted - ships work until queue empty

---

## Design Decisions

### 1. Why Dynamic Work Queue vs VRP Pre-Assignment?

**Decision:** Use dynamic work queue with database-based coordination

**Rationale:**
- **Simpler**: No VRP optimization needed
- **Automatic load balancing**: Fast ships get more work naturally
- **Fault tolerant**: Ships can fail without blocking others
- **Resumable**: Can stop/restart without losing state
- **Scalable**: Can add ships mid-experiment
- **Less code**: Database handles concurrency; no complex partitioning logic

**Trade-off:** May travel more total distance than VRP-optimized routes

**Conclusion:** Load balancing and fault tolerance benefits outweigh route optimization

### 2. Why FIFO Queue Strategy?

**Decision:** Use FIFO (first-in-first-out) for work claiming

**Alternatives considered:**
- Nearest pair to ship's location (minimize travel)
- Priority by supply/activity (test interesting markets first)

**Rationale:**
- **Simplest**: ORDER BY queue_id ASC
- **Predictable**: Easy to understand and debug
- **Fair**: All pairs treated equally
- **Database-native**: Efficient query with index

**Trade-off:** Ships may travel further than necessary

**Conclusion:** Simplicity and predictability more valuable than travel optimization

### 3. Why Single Shared run_id?

**Decision:** All ships share same run_id for a coordinated experiment

**Alternatives considered:**
- Separate run_id per ship (manual aggregation)
- Hierarchical run_ids (parent + children)

**Rationale:**
- **Unified dataset**: Simple queries for analysis
- **Natural aggregation**: GROUP BY without joins
- **Conceptual clarity**: One experiment = one run_id
- **Simpler UX**: User tracks single ID

**Trade-off:** Can't easily isolate per-ship results

**Solution:** Include ship_symbol in results table for filtering

### 4. Why Test All Market Pairs?

**Decision:** Generate N×(N-1) pairs for N markets (all combinations)

**Alternatives considered:**
- Test only profitable pairs (arbitrage-focused)
- Test only extreme pairs (SCARCE→ABUNDANT)

**Rationale:**
- **Comprehensive data**: Reveals unexpected patterns
- **Statistical validity**: More data points for analysis
- **Symmetric testing**: Both A→B and B→A tested
- **Discover surprises**: May find unintuitive arbitrage opportunities

**Trade-off:** Many pairs to test (time-consuming)

**Mitigation:** Use fewer representative markets per good (4-6 instead of all)

### 5. Why No VRP at All?

**Decision:** No routing optimization; ships claim arbitrary pairs

**Rationale:**
- **Work queue naturally distributes**: Ships claim when available
- **Geographic clustering unlikely to help**: Market pairs are random
- **VRP assumes static assignment**: Dynamic claiming is incompatible
- **Complexity not justified**: Routing overhead likely exceeds savings

**Trade-off:** Sub-optimal total distance traveled

**Conclusion:** For long-running experiments, navigation time is minor compared to transaction time

### 6. Why Test Cross-Market (Buy at A, Sell at B)?

**Decision:** Always buy at one market, sell at another (never same market)

**Alternatives considered:**
- Buy and sell at same market (matched pairs)
- Both strategies independently

**Rationale:**
- **Real-world behavior**: Traders buy low, sell high at different locations
- **Market dynamics comparison**: See how different supply/activity markets affect pricing
- **Separate measurements**: Isolate depletion (buy) and saturation (sell) effects at different markets
- **Supply tracking**: Understand how transactions affect markets with different characteristics

**Trade-off:** More navigation time

**Conclusion:** Cross-market testing provides richer insights into market behavior

### 7. Why Multiple Batch Sizes?

**Decision:** Test at [0.1, 0.25, 0.5, 1.0] of trade_volume

**Rationale:**
- **Non-linearity detection**: See if large orders have disproportionate impact
- **Market depth**: Understand how much can be traded before price moves
- **Practical guidance**: Inform trading strategy (small orders vs big orders)

**Trade-off:** 4× more transactions per pair

**Conclusion:** Essential for understanding market liquidity

### 8. Why Multiple Iterations per Batch?

**Decision:** Repeat each batch size 3 times

**Rationale:**
- **Statistical validity**: Measure variance
- **Outlier detection**: Identify anomalies
- **Confidence intervals**: Calculate uncertainty

**Trade-off:** 3× more transactions per batch size

**Conclusion:** Scientific rigor requires repeated measurements

---

## Appendix: File Manifest

### New Files (8 total)

**Application Layer (6 files):**
1. `src/application/trading/commands/sell_cargo.py`
2. `src/application/trading/commands/ship_experiment_worker.py`
3. `src/application/trading/commands/market_liquidity_experiment.py`
4. `src/application/trading/queries/get_experiment_status.py`
5. `src/application/trading/services/market_selector.py`

**Infrastructure Layer (2 files):**
6. `src/adapters/secondary/persistence/work_queue_repository.py`
7. `src/adapters/secondary/persistence/experiment_repository.py`

**Primary Adapter (1 file):**
8. `src/adapters/primary/cli/experiment_cli.py`

### Modified Files (5 total)

1. `src/ports/outbound/api_client.py` - Add sell_cargo interface
2. `src/adapters/secondary/api/client.py` - Implement sell_cargo
3. `src/adapters/secondary/persistence/models.py` - Add 2 tables
4. `src/adapters/primary/cli/main.py` - Wire experiment CLI
5. `src/configuration/container.py` - Register all handlers/services/repos

---

**End of Design Document**
