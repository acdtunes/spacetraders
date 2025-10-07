# Multi-Player Quickstart Guide

## Overview

The SpaceTraders bot now supports **multiple players sharing a single database**. Each player maintains their own ships, daemons, and transactions while sharing the same universe (system graphs and market data).

## Quick Start

### 1. Initialize Your Player

```python
from lib.assignment_manager import AssignmentManager
from lib.daemon_manager import DaemonManager

# Option A: Create/register player with agent symbol + token (stores token in database)
manager = AssignmentManager(
    agent_symbol="CMDR_AC_2025",
    token="YOUR_API_TOKEN"
)

# Option B: Use existing player_id (automatically retrieves token from database!)
manager = AssignmentManager(player_id=1)

# ✨ NEW: API client automatically available
api = manager.api  # Uses stored token automatically!
```

### 1a. Use the API Client

```python
from lib.ship_controller import ShipController

# API client is created automatically with your stored token
api = manager.api

# Use it for ship operations
ship = ShipController(api, "SHIP-1")
ship.dock()
ship.refuel()
ship.navigate("X1-HU87-B9")
```

**See `TOKEN_USAGE.md` for complete token management guide.**

### 2. Manage Ships

```python
# Assign ship
manager.assign("SHIP-1", "mining_operator", "miner-1", "mine")

# Check availability
if manager.is_available("SHIP-2"):
    manager.assign("SHIP-2", "trading_op", "trader-1", "trade")

# List all ships
ships = manager.list_all()
for ship, data in ships.items():
    print(f"{ship}: {data['operation']} ({data['status']})")

# Release ship
manager.release("SHIP-1", reason="operation_complete")
```

### 3. Manage Daemons

```python
# Daemon manager uses same player_id automatically
daemon = manager.daemon_manager

# Start daemon
daemon.start("miner-ship1", [
    "python3", "spacetraders_bot.py", "mine",
    "--ship", "SHIP-1",
    "--asteroid", "X1-HU87-B9"
])

# Check status
status = daemon.status("miner-ship1")
print(f"PID: {status['pid']}, Running: {status['is_running']}")

# Stop daemon
daemon.stop("miner-ship1")
```

## Multi-Player Examples

### Example 1: Multiple Players, Same Ship Names

```python
# Player 1
manager1 = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token1")
manager1.assign("SHIP-1", "mining_op", "miner-1", "mine")
manager1.assign("SHIP-2", "trading_op", "trader-1", "trade")

# Player 2 - CAN use same ship names (no conflict!)
manager2 = AssignmentManager(agent_symbol="EXPLORER_BOT", token="token2")
manager2.assign("SHIP-1", "scouting_op", "scout-1", "scout")
manager2.assign("SHIP-2", "mining_op", "miner-2", "mine")

# Each player sees only their own ships
print("Player 1:", list(manager1.list_all().keys()))  # ['SHIP-1', 'SHIP-2']
print("Player 2:", list(manager2.list_all().keys()))  # ['SHIP-1', 'SHIP-2']
```

### Example 2: Shared Market Intelligence

```python
from lib.database import get_database

db = get_database()

# Player 1 updates market data
with db.transaction() as conn:
    db.update_market_data(
        conn,
        player_id=1,
        waypoint_symbol="X1-HU87-B7",
        good_symbol="IRON_ORE",
        supply="ABUNDANT",
        activity="GROWING",
        purchase_price=10,
        sell_price=15,
        trade_volume=1000
    )

# Player 2 immediately sees it (shared data!)
with db.connection() as conn:
    market = db.get_market_data(conn, "X1-HU87-B7", "IRON_ORE")
    print(f"Price: {market[0]['sell_price']} credits")  # 15
```

### Example 3: Shared System Graphs

```python
from lib.routing import GraphBuilder
from lib.api_client import APIClient

# Player 1 builds graph
api1 = APIClient(token="token1")
builder1 = GraphBuilder(api1, db_path="data/spacetraders.db")
graph = builder1.build_system_graph("X1-HU87")

# Player 2 loads it immediately (shared!)
builder2 = GraphBuilder(api2, db_path="data/spacetraders.db")
graph = builder2.load_system_graph("X1-HU87")  # Same graph
```

## Migration from File-Based System

### Migrate Existing Data

```bash
# Migrate your existing data to multi-player database
python3 migrate_to_database.py \
  --agent CMDR_AC_2025 \
  --token YOUR_API_TOKEN \
  [--dry-run]

# Options:
#   --dry-run          Show what will be migrated
#   --skip-assignments Skip ship assignments
#   --skip-daemons     Skip daemon PIDs
#   --skip-graphs      Skip system graphs
#   --skip-markets     Skip market data
```

### What Gets Migrated

**Per-Player Data** (assigned to your player_id):
- ✅ Ship assignments
- ✅ Daemon PIDs
- ✅ Transaction history (if any)

**Shared Data** (all players can access):
- ✅ System graphs
- ✅ Market data

### After Migration

Old JSON files remain in place - you can delete them after verification:
```bash
# Verify migration worked
python3 -c "
from lib.assignment_manager import AssignmentManager
m = AssignmentManager(player_id=1)
print('Ships:', list(m.list_all().keys()))
"

# If all good, clean up old files
rm agents/cmdr_ac_2025/ship_assignments.json
rm operations/daemons/pids/*.json
```

## Database Schema (Quick Reference)

### Player Data (Isolated)

| Table | Composite Key | Purpose |
|-------|---------------|---------|
| `ship_assignments` | (ship_symbol, player_id) | Ship allocation |
| `daemons` | (daemon_id, player_id) | Background processes |
| `market_transactions` | (id, player_id) | Trading history |

### Shared Data (Collaborative)

| Table | Primary Key | Purpose |
|-------|-------------|---------|
| `players` | player_id | Player registry |
| `system_graphs` | system_symbol | Universe navigation |
| `waypoints` | waypoint_symbol | System locations |
| `graph_edges` | id | Navigation paths |
| `market_data` | (waypoint_symbol, good_symbol) | Market prices |

## Common Patterns

### Pattern 1: Initialize Once, Use Everywhere

```python
# Initialize at start of your bot
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token")

# Use throughout your application
manager.assign("SHIP-1", "op", "daemon-1", "mine")
manager.daemon_manager.start("daemon-1", ["python3", "mine.py"])
```

### Pattern 2: Query Player Info

```python
from lib.database import get_database

db = get_database()

# Get all players
with db.connection() as conn:
    players = db.list_players(conn)
    for p in players:
        print(f"{p['agent_symbol']} (ID={p['player_id']})")

# Get specific player
with db.connection() as conn:
    player = db.get_player(conn, "CMDR_AC_2025")
    print(f"Token: {player['token']}")
```

### Pattern 3: Cross-Player Queries (SQL)

```python
import sqlite3

conn = sqlite3.connect('data/spacetraders.db')
conn.row_factory = sqlite3.Row

# Find all active mining operations (any player)
cursor = conn.cursor()
cursor.execute("""
    SELECT p.agent_symbol, sa.ship_symbol, sa.daemon_id
    FROM ship_assignments sa
    JOIN players p ON sa.player_id = p.player_id
    WHERE sa.operation = 'mine' AND sa.status = 'active'
""")

for row in cursor.fetchall():
    print(f"{row['agent_symbol']}: {row['ship_symbol']}")

conn.close()
```

## Troubleshooting

### "Player already exists"

This is normal! The database automatically updates existing players:

```python
# First call creates player
m1 = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token")
# player_id = 1

# Second call updates player (same player_id)
m2 = AssignmentManager(agent_symbol="CMDR_AC_2025", token="new_token")
# player_id = 1 (updated token)
```

### "Must provide either (agent_symbol + token) OR player_id"

You forgot to pass player identification:

```python
# Wrong
manager = AssignmentManager()  # Error!

# Right (Option 1)
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token")

# Right (Option 2)
manager = AssignmentManager(player_id=1)
```

### "Ship already assigned"

This is a feature! Prevents double-booking:

```python
manager.assign("SHIP-1", "op1", "daemon-1", "mine")  # ✅ Success

# Same player tries to assign again
manager.assign("SHIP-1", "op2", "daemon-2", "trade")  # ❌ Already assigned!

# Release first, then reassign
manager.release("SHIP-1")
manager.assign("SHIP-1", "op2", "daemon-2", "trade")  # ✅ Success
```

## Security Notes

### Token Storage

⚠️ **WARNING:** Tokens are stored in plaintext in the database!

**For production:**
1. Encrypt tokens before storing
2. Use environment variables for keys
3. Restrict database file permissions: `chmod 600 data/spacetraders.db`

```python
# Example: Basic encryption (use proper key management in production!)
import base64

# Store encrypted
encrypted_token = base64.b64encode(token.encode()).decode()
manager = AssignmentManager(agent_symbol="CMDR", token=encrypted_token)

# Decrypt when using API
decrypted = base64.b64decode(encrypted_token).decode()
api = APIClient(token=decrypted)
```

### Database Access

SQLite with WAL mode supports concurrent reads but limited concurrent writes (~10/sec).

**Best practices:**
- Keep transactions short
- Avoid long-running queries inside transactions
- Use `connection()` for reads, `transaction()` for writes

```python
# Good: Short transaction
with db.transaction() as conn:
    db.assign_ship(conn, player_id, "SHIP-1", ...)

# Bad: Long transaction (holds lock too long)
with db.transaction() as conn:
    db.assign_ship(conn, player_id, "SHIP-1", ...)
    time.sleep(10)  # Don't do this!
    db.create_daemon(conn, player_id, ...)
```

## Performance Tips

### Batch Operations

```python
# Good: Single transaction for multiple operations
with db.transaction() as conn:
    for ship in ["SHIP-1", "SHIP-2", "SHIP-3"]:
        db.assign_ship(conn, player_id, ship, ...)

# Bad: Multiple transactions (slower)
for ship in ["SHIP-1", "SHIP-2", "SHIP-3"]:
    with db.transaction() as conn:
        db.assign_ship(conn, player_id, ship, ...)
```

### Connection Pooling

The database uses singleton pattern - no need to create multiple instances:

```python
# Good: Reuse singleton
db = get_database()  # First call creates instance
db2 = get_database()  # Returns same instance

# Bad: Don't do this
db1 = Database("data/spacetraders.db")
db2 = Database("data/spacetraders.db")  # Creates new connection
```

## Next Steps

1. ✅ Read `MULTI_PLAYER_ARCHITECTURE.md` for detailed schema
2. ✅ Run `migrate_to_database.py` to migrate existing data
3. ✅ Update your operations to use `AssignmentManager(agent_symbol=...)`
4. ✅ Test multi-player scenarios
5. ✅ Set up database backups: `sqlite3 db .backup backup.db`

## Support

- **Architecture Details:** See `MULTI_PLAYER_ARCHITECTURE.md`
- **Database API:** See `lib/database.py` docstrings
- **Migration Guide:** See `DATABASE_MIGRATION.md`
