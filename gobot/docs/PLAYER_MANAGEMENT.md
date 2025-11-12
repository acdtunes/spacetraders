# Player Management

The SpaceTraders Go bot supports multiple players/agents with secure token management and flexible player selection.

## Overview

### Key Concepts

- **Player**: An authenticated SpaceTraders agent with their own fleet, credits, and API token
- **Token**: JWT authentication token from SpaceTraders API (stored in database, never in config files)
- **Default Player**: User preference for which player to use when not explicitly specified

### Architecture

```
┌─────────────────────┐
│  CLI Commands       │  --player-id or --agent flags
└──────────┬──────────┘
           │
           v
┌─────────────────────┐
│  PlayerResolver     │  Priority-based selection
└──────────┬──────────┘
           │
           v
┌─────────────────────┐
│  Database           │  Secure token storage
│  (players table)    │
└─────────────────────┘
```

## Player Registration

### Registering a New Player

First, obtain a token from SpaceTraders API, then register it:

```bash
# Register with agent symbol and token
spacetraders player register \
  --agent ENDURANCE \
  --token eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9... \
  --faction COSMIC

# Output:
✓ Player registered successfully
  Agent Symbol: ENDURANCE
  Player ID:    1
  Faction:      COSMIC

Set as default player with: spacetraders config set-player --agent ENDURANCE
```

### Obtaining a SpaceTraders Token

1. Visit https://spacetraders.io
2. Create a new agent or log in
3. Copy your JWT token from the dashboard
4. Register it with the command above

## Listing Players

View all registered players:

```bash
spacetraders player list
```

Output:
```
ID  AGENT SYMBOL  CREDITS  CREATED
--  ------------  -------  -------
1   ENDURANCE     5000000  2025-11-10
2   COOPER        3500000  2025-11-09
```

## Player Information

Get detailed information about a specific player:

```bash
# By player ID
spacetraders player info --player-id 1

# By agent symbol
spacetraders player info --agent ENDURANCE
```

Output:
```
Player Information
==================

Player ID:     1
Agent Symbol:  ENDURANCE
Credits:       5000000
Faction:       COSMIC
Headquarters:  X1-TS98-A1
Account ID:    abcd1234-5678-90ef-ghij-klmnopqrstuv

Token: eyJhbGciOiJSUzI1NiIs...
```

## Default Player Configuration

### Setting Default Player

Set a default player to avoid specifying `--player-id` or `--agent` on every command:

```bash
# Set by player ID
spacetraders config set-player --player-id 1

# Set by agent symbol
spacetraders config set-player --agent ENDURANCE
```

Output:
```
✓ Default player set successfully
  Player ID:    1
  Agent Symbol: ENDURANCE

Commands will now use this player by default.
Override with --player-id or --agent flags.
```

### Viewing Default Player

```bash
spacetraders config show
```

The output includes your default player setting:
```
User Preferences:
  Config file:      /Users/you/.spacetraders/config.json
  Default Player:   ID=1
```

### Clearing Default Player

Remove the default player setting:

```bash
spacetraders config clear-player
```

After clearing, you must specify `--player-id` or `--agent` for all commands.

## Player Selection Priority

When multiple players are registered, the system resolves which player to use with this priority:

1. **`--player-id` flag** (highest priority)
   ```bash
   spacetraders navigate --player-id 2 --ship SHIP-1 --destination X1-ABC-D1
   ```

2. **`--agent` flag**
   ```bash
   spacetraders navigate --agent COOPER --ship SHIP-1 --destination X1-ABC-D1
   ```

3. **Default player from config** (`~/.spacetraders/config.json`)
   ```bash
   # Uses default player set with 'config set-player'
   spacetraders navigate --ship SHIP-1 --destination X1-ABC-D1
   ```

4. **Auto-select if only one player** (TODO: not yet implemented)

5. **Error if ambiguous**
   ```
   Error: no player specified: use --player-id or --agent flag, or set default player with 'config set-player'
   ```

## Token Security

### Storage

- **Database**: Tokens are stored in the `players` table in plain text
- **Config Files**: Tokens are NEVER stored in `~/.spacetraders/config.json` or `config.yaml`
- **User Config**: Only stores default player reference (ID or agent symbol)

### Access Control

- Database access requires proper PostgreSQL credentials
- Use environment variables or secrets management for database passwords
- Never commit database connection strings with passwords to version control

### Best Practices

1. **Development**: Use `.env` file (not committed to git)
   ```bash
   DATABASE_URL=postgresql://spacetraders:dev_password@localhost:5432/spacetraders
   ```

2. **Production**: Use environment variables or secrets manager
   ```bash
   export DATABASE_URL=postgresql://user:password@prod-db:5432/spacetraders
   ```

3. **Docker**: Pass secrets via environment or Docker secrets
   ```yaml
   services:
     daemon:
       environment:
         - DATABASE_URL=${DATABASE_URL}
   ```

## Database Schema

The `players` table stores player information:

```sql
CREATE TABLE players (
    player_id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_symbol TEXT UNIQUE NOT NULL,
    token TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    last_active TIMESTAMP,
    metadata TEXT,  -- JSON: headquarters, faction, accountId
    credits INTEGER DEFAULT 0
);

CREATE INDEX idx_player_agent ON players(agent_symbol);
```

## Command Reference

### Register Player

```bash
spacetraders player register --agent <symbol> --token <jwt> [--faction <faction>]
```

Options:
- `--agent`: Agent symbol (required)
- `--token`: SpaceTraders API JWT token (required)
- `--faction`: Starting faction (optional)

### List Players

```bash
spacetraders player list
```

No options required.

### Player Info

```bash
spacetraders player info --player-id <id>
spacetraders player info --agent <symbol>
```

Options (one required):
- `--player-id`: Player ID
- `--agent`: Agent symbol

### Set Default Player

```bash
spacetraders config set-player --player-id <id>
spacetraders config set-player --agent <symbol>
```

Options (one required):
- `--player-id`: Player ID
- `--agent`: Agent symbol

### Clear Default Player

```bash
spacetraders config clear-player
```

No options required.

## Multi-Player Workflows

### Scenario 1: Single Player (Simple)

```bash
# Register your player
spacetraders player register --agent ENDURANCE --token <jwt>

# Set as default
spacetraders config set-player --agent ENDURANCE

# All commands now use ENDURANCE automatically
spacetraders navigate --ship SHIP-1 --destination X1-ABC-D1
spacetraders dock --ship SHIP-1
```

### Scenario 2: Multiple Players (Explicit Selection)

```bash
# Register multiple players
spacetraders player register --agent ENDURANCE --token <jwt1>
spacetraders player register --agent COOPER --token <jwt2>

# Use different players for different commands
spacetraders navigate --agent ENDURANCE --ship SHIP-1 --destination X1-ABC-D1
spacetraders navigate --agent COOPER --ship SHIP-2 --destination X1-XYZ-Z9
```

### Scenario 3: Multiple Players (Default + Override)

```bash
# Set default player
spacetraders config set-player --agent ENDURANCE

# Most commands use ENDURANCE
spacetraders navigate --ship SHIP-1 --destination X1-ABC-D1

# Override for specific commands
spacetraders navigate --agent COOPER --ship SHIP-2 --destination X1-XYZ-Z9
```

## Troubleshooting

### Player Not Found

**Error**: `player with ID 1 not found` or `player with agent 'ENDURANCE' not found`

**Solutions**:
1. List all players: `spacetraders player list`
2. Verify player ID or agent symbol
3. Check database connection

### Invalid Token

**Error**: `failed to register player: invalid token`

**Solutions**:
1. Verify token is a valid JWT from SpaceTraders API
2. Check token hasn't expired
3. Ensure you copied the complete token

### Duplicate Agent

**Error**: `failed to save player: UNIQUE constraint failed`

**Solution**: Agent symbol must be unique. Each SpaceTraders agent can only be registered once.

### No Default Player

**Error**: `no player specified: use --player-id or --agent flag`

**Solutions**:
1. Set default player: `spacetraders config set-player --agent <symbol>`
2. Or specify player explicitly: `--player-id <id>` or `--agent <symbol>`

### Database Connection Failed

**Error**: `failed to connect to database: connection refused`

**Solutions**:
1. Verify database is running
2. Check DATABASE_URL is correct
3. Verify database credentials
4. See [Configuration Guide](CONFIGURATION.md) for database setup

## Examples

### Complete Setup Workflow

```bash
# 1. Set up database connection
export DATABASE_URL=postgresql://spacetraders:dev_password@localhost:5432/spacetraders

# 2. Register your SpaceTraders agent
spacetraders player register \
  --agent ENDURANCE \
  --token eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9... \
  --faction COSMIC

# 3. Set as default player
spacetraders config set-player --agent ENDURANCE

# 4. Verify setup
spacetraders config show
spacetraders player info --agent ENDURANCE

# 5. Start using commands (player auto-selected)
spacetraders navigate --ship ENDURANCE-1 --destination X1-TS98-B1
```

## Next Steps

- Learn about [Configuration Management](CONFIGURATION.md)
- See [Command Reference](COMMANDS.md) for all available commands
- Review [Architecture](ARCHITECTURE.md) for system design
