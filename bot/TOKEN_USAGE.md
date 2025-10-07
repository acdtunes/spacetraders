# Automatic Token Retrieval Guide

## Overview

The SpaceTraders bot **automatically retrieves and manages API tokens** from the database. You no longer need to pass tokens manually to every API call!

## How It Works

### Token Storage

When you create an AssignmentManager, your token is automatically stored in the database:

```python
from lib.assignment_manager import AssignmentManager

# Token is stored in database.players table
manager = AssignmentManager(
    agent_symbol="CMDR_AC_2025",
    token="YOUR_API_TOKEN_HERE"
)
```

**Database:**
```sql
-- Token is stored here
SELECT agent_symbol, token FROM players;
-- CMDR_AC_2025 | YOUR_API_TOKEN_HERE
```

### Automatic Retrieval

Once stored, you can create managers using **just the player_id** - the token is automatically retrieved:

```python
# Option 1: With agent_symbol + token (stores token)
manager1 = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token123")

# Option 2: With player_id only (retrieves token automatically!)
manager2 = AssignmentManager(player_id=1)

# Both have the same token
assert manager1.token == manager2.token  # ✅ True
```

### API Client Access

The managers provide automatic API client creation:

```python
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token123")

# Method 1: Using get_api_client()
api = manager.get_api_client()

# Method 2: Using .api property (recommended)
api = manager.api

# Use with ship controller
from lib.ship_controller import ShipController
ship = ShipController(api, "SHIP-1")
ship.navigate("X1-HU87-B9")
```

## Complete Workflow Examples

### Example 1: First Time Setup

```python
from lib.assignment_manager import AssignmentManager
from lib.ship_controller import ShipController

# 1. Initialize (stores token in database)
manager = AssignmentManager(
    agent_symbol="CMDR_AC_2025",
    token="YOUR_API_TOKEN"
)

# 2. Get API client automatically
api = manager.api

# 3. Control ships
ship = ShipController(api, "SHIP-1")
ship.dock()
ship.refuel()
ship.orbit()
```

### Example 2: Resuming Session

```python
from lib.assignment_manager import AssignmentManager

# No need to provide token again - it's retrieved from database!
manager = AssignmentManager(player_id=1)

# Token is already available
api = manager.api
ship = ShipController(api, "SHIP-2")
```

### Example 3: Multiple Players

```python
# Player 1
manager1 = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token1")
api1 = manager1.api
ship1 = ShipController(api1, "SHIP-1")

# Player 2
manager2 = AssignmentManager(agent_symbol="EXPLORER_BOT", token="token2")
api2 = manager2.api
ship2 = ShipController(api2, "SHIP-1")  # Same ship name, different player!

# Each API client uses the correct token
ship1.navigate("X1-HU87-B9")  # Uses token1
ship2.navigate("X1-AA11-C3")  # Uses token2
```

### Example 4: Using DaemonManager

```python
# DaemonManager also gets automatic token access
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="token123")

# Access daemon manager (shares same player_id and token)
daemon = manager.daemon_manager

# Daemon manager has token too!
print(daemon.agent_symbol)  # CMDR_AC_2025
print(daemon.token)         # token123
api = daemon.api            # Same API client!
```

## API Client Details

### Lazy Loading

API clients are created **only when first accessed** (lazy loading):

```python
manager = AssignmentManager(agent_symbol="CMDR", token="token")

# No API client created yet
print(manager._api_client)  # None

# First access creates it
api = manager.api
print(manager._api_client)  # <APIClient object>

# Subsequent accesses reuse the same instance
api2 = manager.api
assert api is api2  # ✅ Same object
```

### Accessing the Token Directly

If you need the raw token:

```python
manager = AssignmentManager(player_id=1)

# Access token directly
token = manager.token
print(f"Token: {token}")

# Use with custom API client
from lib.api_client import APIClient
custom_api = APIClient(token=manager.token)
```

## Complete Operation Example

```python
from lib.assignment_manager import AssignmentManager
from lib.ship_controller import ShipController
from lib.smart_navigator import SmartNavigator

# 1. Initialize manager (one-time setup per session)
manager = AssignmentManager(
    agent_symbol="CMDR_AC_2025",
    token="YOUR_API_TOKEN"
)

# 2. Get API client
api = manager.api

# 3. Assign ship
manager.assign("SHIP-1", "mining_operator", "miner-1", "mine")

# 4. Control ship
ship = ShipController(api, "SHIP-1")

# 5. Navigate with smart navigator
navigator = SmartNavigator(api, "X1-HU87")
navigator.execute_route(ship, "X1-HU87-B9")

# 6. Mine resources
ship.extract()
ship.extract()

# 7. Return and sell
navigator.execute_route(ship, "X1-HU87-B7")
ship.dock()
ship.sell_all()

# 8. Release ship when done
manager.release("SHIP-1")
```

## Migration from Manual Token Passing

### Before (Manual Tokens)

```python
# Old way - had to pass token everywhere
token = "YOUR_TOKEN"

api = APIClient(token=token)
ship = ShipController(api, "SHIP-1")

navigator = SmartNavigator(api, "X1-HU87")
```

### After (Automatic Retrieval)

```python
# New way - token managed automatically
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="YOUR_TOKEN")

# Use manager.api everywhere
ship = ShipController(manager.api, "SHIP-1")
navigator = SmartNavigator(manager.api, "X1-HU87")

# Or save reference
api = manager.api
ship = ShipController(api, "SHIP-1")
navigator = SmartNavigator(api, "X1-HU87")
```

## Token Security

### ⚠️ IMPORTANT: Tokens Are Stored in Plaintext

Tokens are currently stored **unencrypted** in the database:

```sql
-- Anyone with database access can read tokens
SELECT agent_symbol, token FROM players;
```

### Security Best Practices

**1. Restrict Database Access:**
```bash
# Only owner can read/write
chmod 600 data/spacetraders.db
```

**2. Use Environment Variables for Sensitive Operations:**
```python
import os

# Don't hardcode tokens in source files
manager = AssignmentManager(
    agent_symbol="CMDR_AC_2025",
    token=os.environ.get("SPACETRADERS_TOKEN")
)
```

**3. Gitignore the Database:**
```bash
# Add to .gitignore
echo "data/*.db" >> .gitignore
echo "data/*.db-*" >> .gitignore
```

**4. Regular Backups:**
```bash
# Backup database (includes tokens!)
sqlite3 data/spacetraders.db ".backup backup/$(date +%Y%m%d).db"

# Secure backups
chmod 600 backup/*.db
```

### Future: Token Encryption

If you need encryption, you can implement it at the application layer:

```python
from cryptography.fernet import Fernet
import os

# 1. Generate/load encryption key
key = os.environ.get('DB_ENCRYPTION_KEY', '').encode()
cipher = Fernet(key)

# 2. Encrypt before storing
encrypted_token = cipher.encrypt(b"YOUR_TOKEN").decode()
manager = AssignmentManager(agent_symbol="CMDR", token=encrypted_token)

# 3. Decrypt when retrieving
with manager.db.connection() as conn:
    player = manager.db.get_player(conn, "CMDR")
    decrypted_token = cipher.decrypt(player['token'].encode()).decode()
```

## Troubleshooting

### "Player not found"

```python
# Error: Player ID doesn't exist
manager = AssignmentManager(player_id=999)
# ValueError: Player ID 999 not found

# Solution: Use agent_symbol + token first
manager = AssignmentManager(agent_symbol="CMDR", token="token123")
print(f"Your player_id is: {manager.player_id}")
```

### "Must provide either (agent_symbol + token) OR player_id"

```python
# Error: No credentials provided
manager = AssignmentManager()
# ValueError: Must provide either...

# Solution: Provide credentials
manager = AssignmentManager(agent_symbol="CMDR", token="token123")
# OR
manager = AssignmentManager(player_id=1)
```

### "API client is None"

```python
# If daemon manager created without player existing
daemon = DaemonManager(player_id=999)  # Player doesn't exist
print(daemon.token)  # None
print(daemon.api)    # None

# Solution: Ensure player exists first
manager = AssignmentManager(agent_symbol="CMDR", token="token")
daemon = manager.daemon_manager  # Now has token
```

## Summary

✅ **Tokens are stored automatically** when you create AssignmentManager
✅ **Tokens are retrieved automatically** when you use player_id
✅ **API clients are created automatically** via `manager.api`
✅ **Tokens are shared** with DaemonManager
✅ **Lazy loading** prevents unnecessary API client creation
✅ **No encryption** - tokens stored in plaintext (secure your database!)

**Basic Pattern:**
```python
# One-time init
manager = AssignmentManager(agent_symbol="CMDR_AC_2025", token="YOUR_TOKEN")

# Use throughout your code
api = manager.api
ship = ShipController(api, "SHIP-1")
```

That's it! The token is handled automatically. 🎉
