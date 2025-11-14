# SpaceTraders V2 Bot - Market Tour System Analysis

## Overview
The bot codebase implements a sophisticated market tour system for multi-ship continuous market scouting with tour time balancing and market data freshness tracking. The system is split between the original bot (fully implemented) and bot-v2 (newer DDD architecture, awaiting market tour integration).

---

## 1. MARKET TOUR STRUCTURE & STORAGE

### Tour Architecture
Located in: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/`

**Core Files:**
- `core/market_scout.py` - ScoutCoordinator: Multi-ship tour orchestration
- `core/market_partitioning.py` - MarketPartitioner: Market-to-ship assignment strategies
- `core/scout_services/tour_time_estimator.py` - Tour time calculation with strategies
- `operations/scouting/tour_mode.py` - TourScoutMode: Tour execution logic
- `operations/scouting/market_data_service.py` - Market data collection and DB updates

### Tour Partition Assignment Data Class
```python
# From market_scout.py
@dataclass
class SubtourAssignment:
    """Assignment of markets to a ship"""
    ship: str                      # Ship symbol (e.g., "SHIP-1")
    markets: List[str]            # Waypoint symbols for this ship's tour
    tour_time_seconds: float      # Calculated tour duration
    daemon_id: str                # Associated daemon process ID
```

### Scout Coordinator State
```python
class ScoutCoordinator:
    def __init__(self, system, ships, token, player_id, config_file=None, ...):
        self.system: str                           # System symbol (e.g., "X1-HU87")
        self.ships: Set[str]                       # Set of ship symbols
        self.markets: List[str]                    # All markets in system
        self.assignments: Dict[str, SubtourAssignment]  # Ship → tour assignment
        self.graph: Dict                           # Navigation graph with waypoints
        self.exclude_markets: Set[str]             # Markets excluded from auto-discovery
```

### Partitioning Strategies
Four strategies supported:
1. **Greedy** - Assigns markets incrementally to minimize tour time variance
2. **KMeans** - Clusters markets geographically (compact tours)
3. **Geographic** - Slices markets by position
4. **OR-Tools** - Multi-vehicle optimization (best quality)

---

## 2. MARKET FRESHNESS TRACKING

### Database Schema (market_data table)
Location: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/core/database.py`

```sql
CREATE TABLE IF NOT EXISTS market_data (
    waypoint_symbol TEXT NOT NULL,
    good_symbol TEXT NOT NULL,
    supply TEXT,                  -- SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    activity TEXT,                -- WEAK, FAIR, STRONG, EXCESSIVE, RESTRICTED
    purchase_price INTEGER,        -- What market pays for ships to BUY (aka bid price)
    sell_price INTEGER,           -- What market offers for ships to SELL (ask price)
    trade_volume INTEGER,         -- Market liquidity depth
    last_updated TIMESTAMP NOT NULL,  -- CRITICAL: Freshness tracking
    updated_by_player INTEGER,    -- Player ID who updated
    PRIMARY KEY (waypoint_symbol, good_symbol),
    FOREIGN KEY (updated_by_player) REFERENCES players(player_id)
)

-- Indexes for freshness queries
CREATE INDEX idx_market_updated ON market_data(last_updated)
```

### Freshness Query Functions
From `core/market_repository.py`:

```python
# Get stale markets (older than threshold)
def get_stale_markets(
    max_age_hours: float,
    system: Optional[str] = None,
    db: Optional[Database] = None
) -> List[Dict]:
    """Return market entries older than provided age threshold"""
    cutoff = datetime.now(timezone.utc) - timedelta(hours=max_age_hours)
    # Query: last_updated < cutoff OR last_updated IS NULL

# Get recent updates
def get_recent_updates(
    system: Optional[str] = None,
    limit: int = 25,
    db: Optional[Database] = None
) -> List[Dict]:
    """Return most recent market updates"""
    # Query: ORDER BY last_updated DESC LIMIT limit

# Get specific good freshness
def get_waypoint_good(
    waypoint_symbol: str,
    good_symbol: str
) -> Optional[Dict]:
    # Returns: {..., 'last_updated': TIMESTAMP, ...}
```

### Freshness Update Pattern
From `operations/scouting/market_data_service.py`:

```python
class MarketDataService:
    def update_database(self, waypoint: str, trade_goods: List[Dict], timestamp: str) -> int:
        """
        Update market freshness for all goods at waypoint
        
        CRITICAL API FIELD MAPPING:
        - API.purchasePrice → DB.sell_price (what market asks for)
        - API.sellPrice → DB.purchase_price (what market bids)
        """
        with db.transaction():
            for good in trade_goods:
                db.update_market_data(
                    waypoint_symbol=waypoint,
                    good_symbol=good['symbol'],
                    supply=good.get('supply'),
                    activity=good.get('activity'),
                    purchase_price=good.get('sellPrice'),       # FLIP!
                    sell_price=good.get('purchasePrice'),       # FLIP!
                    trade_volume=good.get('tradeVolume'),
                    last_updated=timestamp,  # THIS is what tracks freshness
                    player_id=self.player_id
                )
        return goods_updated
```

---

## 3. DATA MODELS FOR TOURS & MARKET DATA

### Tour Time Calculation Models
From `core/scout_services/tour_time_estimator.py`:

```python
class TourTimeStrategy(ABC):
    """Abstract strategy for tour time calculation"""
    @abstractmethod
    def calculate_tour_time(
        self, 
        markets: List[str], 
        ship_data: Dict, 
        graph: Dict
    ) -> float:
        """Returns tour time in seconds"""

class EstimateTourTimeStrategy(TourTimeStrategy):
    """Fast estimate using nearest-neighbor heuristic + overhead"""
    # Formula: round((distance * 26) / engine_speed) + len(markets) * 22
    # - Distance: bounding box perimeter or nearest-neighbor tour
    # - Overhead: ~22 seconds per market (dock + API + orbit)

class CalculateTourTimeStrategy(TourTimeStrategy):
    """Precise calculation using OR-Tools TSP solver"""
    # Calculates from partition centroid
    # More accurate but slower

class TourTimeEstimator:
    """Service with configurable strategies"""
    def estimate_partition_tour_time(markets, ship_data) -> float
    def calculate_partition_tour_time(markets, ship_data) -> float
```

### Market Data Value Objects
From `domain/trading/value_objects.py`:

```python
@dataclass(frozen=True)
class TradeAction:
    """Single buy or sell action at a waypoint"""
    waypoint: Waypoint
    trade_good: str
    action_type: TradeActionType  # BUY or SELL
    units: int
    price_per_unit: int
    
    def total_value() -> int        # +credits for SELL, -credits for BUY
    def is_buy() -> bool
    def is_sell() -> bool

@dataclass(frozen=True)
class RouteSegment:
    """One leg of a multi-segment trade route"""
    from_waypoint: Waypoint
    to_waypoint: Waypoint
    distance: Distance
    fuel_cost: int
    actions_at_destination: Tuple[TradeAction, ...]
    
    def expected_profit() -> int
    def cargo_acquired() -> Dict[str, int]
    def cargo_sold() -> Dict[str, int]

@dataclass(frozen=True)
class RouteEvaluation:
    """Metrics for evaluating trade route quality"""
    total_profit: int
    total_cost: int
    total_revenue: int
    fuel_cost: int
    time_estimate_seconds: int
    distance_units: float
    segment_count: int
    
    def roi() -> float
    def profit_per_hour() -> float
    def profit_per_distance() -> float
```

### Market Partition Result Model
From `core/market_partitioning.py`:

```python
@dataclass
class PartitionResult:
    """Result of a market partitioning strategy"""
    partitions: Dict[str, List[str]]  # ship_symbol → [waypoints...]
    message: str | None               # Status message
```

---

## 4. CONTAINER DAEMON SYSTEM

### Daemon Types (bot-v2)
Location: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot-v2/src/spacetraders/adapters/primary/daemon/`

**Key Files:**
- `daemon_server.py` - JSON-RPC 2.0 server on Unix socket
- `command_container.py` - Executes CQRS commands N times
- `types.py` - Container metadata and enums
- `assignment_manager.py` - Ship-to-container assignment tracking
- `container_manager.py` - Container lifecycle management

### Container States
```python
class ContainerStatus(Enum):
    STARTING = "STARTING"
    RUNNING = "RUNNING"
    STOPPING = "STOPPING"
    STOPPED = "STOPPED"
    FAILED = "FAILED"

@dataclass
class ContainerInfo:
    container_id: str
    player_id: int
    container_type: str              # e.g., "CommandContainer", "MarketScoutContainer"
    status: ContainerStatus
    restart_policy: RestartPolicy
    restart_count: int
    max_restarts: int
    config: Dict[str, Any]           # Command config with params
    task: Optional[asyncio.Task]
    logs: list
    started_at: Optional[datetime]
    stopped_at: Optional[datetime]
    exit_code: Optional[int]
    exit_reason: Optional[str]
```

### Command Container Configuration
```python
# For executing tour-related commands
config = {
    'command_type': 'DockShipCommand' or 'module.path.CommandName',
    'params': {
        'ship_symbol': 'SHIP-1',
        'player_id': 1,
        # ... command-specific params
    },
    'iterations': 100  # Run command N times
}
```

### Ship Assignment Tracking (bot-v2)
Database table:
```sql
CREATE TABLE IF NOT EXISTS ship_assignments (
    ship_symbol TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    container_id TEXT,              -- Container using this ship
    operation TEXT,                 -- Operation type (e.g., "tour_scout")
    status TEXT DEFAULT 'idle',     -- idle, active, assigned
    assigned_at TIMESTAMP,
    released_at TIMESTAMP,
    release_reason TEXT,            -- Why assignment ended
    PRIMARY KEY (ship_symbol, player_id)
)
```

---

## 5. DATABASE PERSISTENCE PATTERNS

### Original Bot (SQLite with WAL mode)
Location: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/src/spacetraders_bot/core/database.py`

```python
class Database:
    def __init__(self, db_path: str | Path | None = None):
        self.db_path = Path(db_path) or paths.sqlite_path()
        self._init_database()
    
    @contextmanager
    def transaction(self):
        """Context manager for transactional writes"""
        conn = self._get_connection()
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
    
    # Tour cache methods
    def get_cached_tour(self, conn, system, markets, algorithm, start_waypoint) -> Optional[Dict]:
        """Get tour from cache if available"""
        return {
            'tour_order': [waypoints],
            'total_distance': float,
            'calculated_at': timestamp
        }
    
    def save_tour_cache(self, conn, system, markets, algorithm, tour_order, total_distance):
        """Save tour optimization result to cache"""
        # Saves to tour_cache table for reuse
    
    def update_market_data(self, conn, waypoint_symbol, good_symbol, supply, activity, 
                          purchase_price, sell_price, trade_volume, last_updated, player_id):
        """Update market freshness and prices"""
    
    def get_market_data(self, conn, waypoint_symbol) -> List[Dict]:
        """Get all market data for waypoint"""
```

### Bot-V2 (SQLite with DDD Architecture)
Location: `/Users/andres.camacho/Development/Personal/spacetradersV2/bot-v2/src/spacetraders/adapters/secondary/persistence/`

**Key Repository Files:**
- `database.py` - Core SQLite manager
- `ship_repository.py` - Ship aggregate queries
- `route_repository.py` - Route/tour storage
- `player_repository.py` - Player data persistence
- `mappers.py` - Domain model ↔ persistence mappers

```python
# Bot-v2 database tables (includes tour support)
Tables:
- players (agent metadata)
- ships (per-player ship state)
- routes (multi-waypoint navigation plans)
- system_graphs (shared universe graphs)
- ship_assignments (ship-to-container assignments)
- containers (daemon container metadata)
- container_logs (persistent logging)
```

---

## 6. MARKET DATA PRICE IMPACT MODELS

### Real-World Price Dynamics
From `core/market_repository.py`:

**Batch Purchase Cost Escalation (Buying):**
```python
SUPPLY_MULTIPLIERS = {
    "SCARCE": 2.0,      # Prices rise 2x faster
    "LIMITED": 1.5,
    "MODERATE": 1.0,
    "HIGH": 0.5,
    "ABUNDANT": 0.3
}

def calculate_batch_purchase_cost(base_price, units, trade_volume, supply):
    """
    Calculate total cost when buying in batches accounting for price escalation.
    
    Formula: price_multiplier = 1.0 + (escalation_rate * batch_num)
    escalation_rate = 0.05 * supply_multiplier
    
    Real-world calibration (2025-10-12):
    - X1-TX46-D42: 18u SHIP_PLATING (tradeVolume=6, LIMITED)
    - Batch 1: 3,941 cr → Batch 3: 4,580 cr (+16.2%)
    """
    return (total_cost, breakdown_with_batch_details)
```

**Batch Sale Revenue Degradation (Selling):**
```python
ACTIVITY_MULTIPLIERS = {
    "RESTRICTED": 1.5,
    "WEAK": 1.0,
    "GROWING": 0.7,
    "STRONG": 0.5,
    "EXCESSIVE": 0.3
}

def calculate_batch_sale_revenue(base_price, units, trade_volume, activity):
    """
    Calculate total revenue when selling in batches accounting for price degradation.
    
    Real-world observations show MINIMAL degradation (markets more stable):
    - ASSAULT_RIFLES: 21u / 10tv = 2.1x, WEAK → -0.5% actual
    - SHIP_PLATING: 18u / 6tv = 3x, WEAK → -2.9% actual (not -33%!)
    
    Formula: degradation = (units/trade_volume) * activity_multiplier
    """
    return (total_revenue, breakdown_with_batch_details)
```

---

## 7. KEY FILES FOR INTEGRATION WITH BOT-V2

### Critical Files to Integrate
```
Bot (Original) - Source of Market Tour Implementation:
├── core/market_scout.py                    [ScoutCoordinator, SubtourAssignment]
├── core/market_partitioning.py             [MarketPartitioner, PartitionResult]
├── core/scout_services/tour_time_estimator.py  [TourTimeEstimator strategies]
├── core/market_repository.py               [Freshness queries, price models]
├── core/database.py                        [market_data table, tour_cache]
├── operations/scouting/market_data_service.py  [MarketDataService]
├── operations/scouting/tour_mode.py        [TourScoutMode execution]
└── domain/trading/value_objects.py         [TradeAction, RouteSegment, RouteEvaluation]

Bot-V2 - Destination Architecture (DDD):
├── adapters/primary/daemon/              [Container system - EXISTS]
├── adapters/secondary/persistence/       [Repository layer - EXISTS]
├── configuration/container.py            [Dependency injection - NEEDS MARKET TOUR SETUP]
├── domain/navigation/                    [Route/tour domain models]
├── application/                          [CQRS commands/queries - NEEDS MARKET TOUR COMMANDS]
└── ports/outbound/                       [Port interfaces - NEEDS MARKET TOUR PORTS]
```

---

## 8. MARKET TOUR INTEGRATION CHECKLIST FOR BOT-V2

### Domain Layer (bot-v2)
- [ ] Create `domain/scouting/` with DDD models
  - [ ] `MarketTourAggregate` (root entity)
  - [ ] `SubtourAssignment` value object (parallel to bot original)
  - [ ] `MarketFreshness` value object (last_updated tracking)
  - [ ] Tour service interfaces

### Application Layer (bot-v2)
- [ ] Create `application/scouting/commands/`
  - [ ] `StartMarketTourCommand` (initialize scout coordinator)
  - [ ] `ReconfigureTourCommand` (rebalance tours)
  - [ ] `UpdateMarketDataCommand` (sync freshness)
- [ ] Create `application/scouting/queries/`
  - [ ] `GetMarketFreshnessQuery`
  - [ ] `GetTourAssignmentsQuery`
  - [ ] `GetStaleMarketsQuery`

### Adapters Layer (bot-v2)
- [ ] Update `adapters/secondary/persistence/database.py`
  - [ ] Add `market_data` table schema
  - [ ] Add `tour_cache` table schema
  - [ ] Add `tour_assignments` table schema
- [ ] Create `adapters/secondary/persistence/market_repository.py`
  - [ ] Implement freshness queries
  - [ ] Implement price impact models
- [ ] Create container types for tour execution
  - [ ] `MarketScoutContainer` type

### Configuration Layer (bot-v2)
- [ ] Update `configuration/container.py`
  - [ ] Register market scout services
  - [ ] Register tour time estimators
  - [ ] Register market partitioner strategies

---

## 9. DATA ACCESS PATTERNS FOR VISUALIZER

### Reading Tour Information
```python
# Get current tour assignments
with database.connection() as conn:
    # Query tour_cache table for recent tours
    tours = db.get_cached_tours(conn, system='X1-HU87')
    # Returns: [{'tour_order': [...], 'total_distance': 123.4, ...}]

# Get market freshness status
stale_markets = market_repo.get_stale_markets(max_age_hours=2.0, system='X1-HU87')
recent_updates = market_repo.get_recent_updates(system='X1-HU87', limit=25)

# Get specific waypoint market data
goods = market_repo.get_waypoint_goods('X1-HU87-B7')
# Returns: [{'waypoint_symbol', 'good_symbol', 'supply', 'activity', 
#            'purchase_price', 'sell_price', 'trade_volume', 'last_updated'}, ...]
```

### Reading Market Data Freshness
```python
# Get last_updated timestamp for a market entry
good = market_repo.get_waypoint_good('X1-HU87-B7', 'IRON_ORE')
last_updated = datetime.fromisoformat(good['last_updated'])  # ISO timestamp

# Calculate freshness age
from datetime import datetime, timezone, timedelta
now = datetime.now(timezone.utc)
age = (now - datetime.fromisoformat(good['last_updated'])).total_seconds() / 3600
print(f"Market data is {age:.1f} hours old")
```

---

## Summary

The market tour system is fully implemented in the original bot with:
- **Tour partitioning**: 4 strategies (greedy, kmeans, geographic, ortools)
- **Freshness tracking**: `last_updated` timestamp on every market data entry
- **Tour time estimation**: Strategy pattern with fast estimate and precise TSP calculation
- **Market data models**: Immutable value objects with price impact calculations
- **Container daemon**: JSON-RPC control for long-running tour operations
- **Persistence**: SQLite with tour caching for optimization reuse

For bot-v2 integration, the key is adapting these concepts to the DDD architecture while maintaining the data models and algorithms that have been battle-tested with real game data.

