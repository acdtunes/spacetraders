# Multi-Player Database Architecture

## Overview

The SpaceTraders bot database now supports **multiple players sharing a single database**. Each player has their own ships, daemons, and transactions, while sharing the same universe (system graphs, market data).

## Schema Design

### Player Segregation Model

**Per-Player Data:**
- Ships & Assignments
- Daemons (background processes)
- Transaction History

**Shared Data:**
- System Graphs (all players see same universe)
- Waypoints & Edges
- Market Data (all players contribute/see same prices)

### Core Tables

#### `players` - Player Registry

```sql
CREATE TABLE players (
    player_id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_symbol TEXT UNIQUE NOT NULL,           -- e.g., "CMDR_AC_2025"
    token TEXT NOT NULL,                          -- API token (encrypted in production!)
    created_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP,
    metadata TEXT                                 -- JSON for custom data
);
```

**Usage:**
```python
# Register/update player
with db.transaction() as conn:
    player_id = db.create_player(conn, "CMDR_AC_2025", "token123")

# Get player
with db.connection() as conn:
    player = db.get_player(conn, "CMDR_AC_2025")
    player_id = player['player_id']
```

#### `ship_assignments` - Per-Player Ship Tracking

```sql
CREATE TABLE ship_assignments (
    ship_symbol TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    assigned_to TEXT,
    daemon_id TEXT,
    operation TEXT,
    status TEXT NOT NULL DEFAULT 'idle',
    assigned_at TIMESTAMP,
    released_at TIMESTAMP,
    release_reason TEXT,
    metadata TEXT,
    PRIMARY KEY (ship_symbol, player_id),
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
);
```

**Key Points:**
- **Composite Primary Key:** (ship_symbol, player_id) allows different players to have ships with same name
- **Isolation:** Player A's "SHIP-1" is completely separate from Player B's "SHIP-1"
- **Cascade Delete:** If player is deleted, all their ship assignments are removed

**Usage:**
```python
# Assign ship for specific player
with db.transaction() as conn:
    db.assign_ship(conn, player_id=1, ship_symbol="SHIP-1",
                   assigned_to="operator", daemon_id="miner-1", operation="mine")

# List ships for specific player
with db.connection() as conn:
    ships = db.list_ship_assignments(conn, player_id=1)
```

#### `daemons` - Per-Player Background Processes

```sql
CREATE TABLE daemons (
    daemon_id TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    pid INTEGER,
    command TEXT NOT NULL,
    started_at TIMESTAMP NOT NULL,
    stopped_at TIMESTAMP,
    status TEXT NOT NULL DEFAULT 'running',
    log_file TEXT,
    err_file TEXT,
    PRIMARY KEY (daemon_id, player_id),
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
);
```

**Key Points:**
- **Composite Primary Key:** (daemon_id, player_id) - players can have same daemon IDs
- **Process Isolation:** Each player's daemons tracked separately
- **Automatic Cleanup:** Deleting player removes all their daemons

**Usage:**
```python
# Create daemon for player
with db.transaction() as conn:
    db.create_daemon(conn, player_id=1, daemon_id="miner-ship1",
                     pid=12345, command=["python3", "bot.py", "mine"],
                     log_file="/logs/miner.log", err_file="/logs/miner.err")

# List daemons for player
with db.connection() as conn:
    daemons = db.list_daemons(conn, player_id=1, status='running')
```

#### `system_graphs` - SHARED Universe Data

```sql
CREATE TABLE system_graphs (
    system_symbol TEXT PRIMARY KEY,              -- e.g., "X1-HU87"
    graph_data TEXT NOT NULL,                    -- JSON blob
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
```

**Key Points:**
- **No player_id** - Shared across ALL players
- **Single Source of Truth:** One graph per system
- **All players contribute:** Any player can update/build graphs

**Usage:**
```python
# Save graph (any player can do this)
with db.transaction() as conn:
    db.save_system_graph(conn, "X1-HU87", graph_data)

# Load graph (all players see same data)
with db.connection() as conn:
    graph = db.get_system_graph(conn, "X1-HU87")
```

#### `market_data` - SHARED Market Intelligence

```sql
CREATE TABLE market_data (
    waypoint_symbol TEXT NOT NULL,
    good_symbol TEXT NOT NULL,
    supply TEXT,
    activity TEXT,
    purchase_price INTEGER,
    sell_price INTEGER,
    trade_volume INTEGER,
    last_updated TIMESTAMP NOT NULL,
    updated_by_player INTEGER,                   -- Track who last updated
    PRIMARY KEY (waypoint_symbol, good_symbol),
    FOREIGN KEY (updated_by_player) REFERENCES players(player_id) ON DELETE SET NULL
);
```

**Key Points:**
- **Shared Data:** All players see same market prices
- **Collaborative Intelligence:** Players contribute market updates
- **Attribution:** `updated_by_player` tracks data source
- **Latest Wins:** Last update is current price

**Usage:**
```python
# Update market data (contributes to shared pool)
with db.transaction() as conn:
    db.update_market_data(conn, player_id=1, waypoint_symbol="X1-HU87-B7",
                          good_symbol="IRON_ORE", supply="ABUNDANT",
                          activity="GROWING", purchase_price=10,
                          sell_price=15, trade_volume=1000)

# Get market data (all players see same)
with db.connection() as conn:
    market = db.get_market_data(conn, "X1-HU87-B7", "IRON_ORE")
```

#### `market_transactions` - Per-Player Transaction History

```sql
CREATE TABLE market_transactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    player_id INTEGER NOT NULL,
    ship_symbol TEXT NOT NULL,
    waypoint_symbol TEXT NOT NULL,
    good_symbol TEXT NOT NULL,
    transaction_type TEXT NOT NULL,
    units INTEGER NOT NULL,
    price_per_unit INTEGER NOT NULL,
    total_cost INTEGER NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
);
```

**Key Points:**
- **Private History:** Each player only sees their own transactions
- **Audit Trail:** Complete record of all buys/sells
- **Analytics:** Query your own trading performance

**Usage:**
```python
# Record transaction
with db.transaction() as conn:
    db.record_transaction(conn, player_id=1, ship_symbol="SHIP-1",
                          waypoint_symbol="X1-HU87-B7", good_symbol="IRON_ORE",
                          transaction_type="SELL", units=50,
                          price_per_unit=15, total_cost=750)

# Get player's transaction history
with db.connection() as conn:
    transactions = db.get_transactions(conn, player_id=1, limit=100)
```

## Usage Examples

### Multi-Player Scenario

```python
from lib.database import get_database

db = get_database()

# === Player 1: CMDR_AC_2025 ===
with db.transaction() as conn:
    p1_id = db.create_player(conn, "CMDR_AC_2025", "token_p1")

    # Assign ship
    db.assign_ship(conn, p1_id, "SHIP-1", "mining_op", "miner-1", "mine")

    # Create daemon
    db.create_daemon(conn, p1_id, "miner-1", 12345,
                     ["python3", "bot.py", "mine"],
                     "logs/p1_miner.log", "logs/p1_miner.err")

    # Update shared market data
    db.update_market_data(conn, p1_id, "X1-HU87-B7", "IRON_ORE",
                          "ABUNDANT", "GROWING", 10, 15, 1000)

# === Player 2: EXPLORER_BOT ===
with db.transaction() as conn:
    p2_id = db.create_player(conn, "EXPLORER_BOT", "token_p2")

    # Same ship name, different player - NO CONFLICT
    db.assign_ship(conn, p2_id, "SHIP-1", "trading_op", "trader-1", "trade")

    # Access shared market data (sees Player 1's contribution)
    market = db.get_market_data(conn, "X1-HU87-B7")
    # Shows IRON_ORE data from Player 1

# === Isolation Verification ===
with db.connection() as conn:
    # Player 1's ships (only sees their own)
    p1_ships = db.list_ship_assignments(conn, p1_id)
    # Returns: [{"ship_symbol": "SHIP-1", "operation": "mine", ...}]

    # Player 2's ships (only sees their own)
    p2_ships = db.list_ship_assignments(conn, p2_id)
    # Returns: [{"ship_symbol": "SHIP-1", "operation": "trade", ...}]

    # Both have "SHIP-1" but completely isolated!
```

## Integration with Assignment/Daemon Managers

### Updated Managers

**AssignmentManager** and **DaemonManager** now require `player_id`:

```python
# OLD (single player)
manager = AssignmentManager()
manager.assign("SHIP-1", "operator", "daemon-1", "mine")

# NEW (multi-player) - NEEDS UPDATE
# Will be updated to:
manager = AssignmentManager(player_id=1)
manager.assign("SHIP-1", "operator", "daemon-1", "mine")
```

**NOTE:** Assignment and Daemon managers still need to be updated to work with the new multi-player database API.

## Migration Strategy

### Existing Users

For users with existing single-player data:

1. **Default Player:** Migration creates a default player with existing agent symbol
2. **Auto-Association:** All existing ships/daemons assigned to default player
3. **Backward Compatible:** Managers can work with player_id parameter

```bash
# Migration will create default player
python3 migrate_to_database.py

# Output:
# Created default player: CMDR_AC_2025 (player_id=1)
# Migrated 5 ships to player_id=1
# Migrated 3 daemons to player_id=1
```

### Adding New Players

```python
# Add a new player to shared database
with db.transaction() as conn:
    new_player_id = db.create_player(conn, "EXPLORER_BOT", "token_xyz")

print(f"New player added: {new_player_id}")
# Now EXPLORER_BOT can start operations without conflicting with CMDR_AC_2025
```

## Security Considerations

### Token Storage

**⚠️ IMPORTANT:** Tokens are stored in plaintext in the database!

**For Production:**
```python
import base64
from cryptography.fernet import Fernet

# Encrypt token before storing
key = Fernet.generate_key()  # Store this securely!
cipher = Fernet(key)
encrypted_token = cipher.encrypt(token.encode()).decode()

# Store encrypted
db.create_player(conn, "CMDR_AC_2025", encrypted_token)

# Decrypt when using
decrypted_token = cipher.decrypt(encrypted_token.encode()).decode()
```

### Access Control

Currently no access control between players in database. Consider adding:

```python
# Future: Restrict operations to player's own data
def assign_ship(self, conn, requesting_player_id, target_player_id, ship_symbol, ...):
    if requesting_player_id != target_player_id:
        raise PermissionError("Cannot modify another player's ships")
    # ... proceed with assignment
```

## Query Examples

### Cross-Player Analytics

```sql
-- Find all active mining operations (any player)
SELECT p.agent_symbol, sa.ship_symbol, sa.daemon_id
FROM ship_assignments sa
JOIN players p ON sa.player_id = p.player_id
WHERE sa.operation = 'mine' AND sa.status = 'active';

-- Market data freshness by player
SELECT p.agent_symbol, COUNT(*) as updates_contributed
FROM market_data md
JOIN players p ON md.updated_by_player = p.player_id
GROUP BY p.agent_symbol;

-- Player activity summary
SELECT
    p.agent_symbol,
    COUNT(DISTINCT sa.ship_symbol) as total_ships,
    COUNT(DISTINCT d.daemon_id) as active_daemons,
    COUNT(mt.id) as total_transactions
FROM players p
LEFT JOIN ship_assignments sa ON p.player_id = sa.player_id
LEFT JOIN daemons d ON p.player_id = d.player_id AND d.status = 'running'
LEFT JOIN market_transactions mt ON p.player_id = mt.player_id
GROUP BY p.player_id;
```

## Next Steps

1. ✅ Database schema updated for multi-player
2. ⚠️ Update `AssignmentManager` to accept player_id
3. ⚠️ Update `DaemonManager` to accept player_id
4. ⚠️ Update all operations to pass player_id
5. ⚠️ Update migration script to create default player
6. ⚠️ Add player selection to CLI (`--player` or `--agent`)
7. ⚠️ Test multi-player scenarios

## Benefits

✅ **True Multi-Tenancy:** Multiple agents share single database
✅ **Data Isolation:** Each player's operations independent
✅ **Collaborative Intelligence:** Shared market/graph data
✅ **Scalability:** Add players without database duplication
✅ **Analytics:** Cross-player insights possible
