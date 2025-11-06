# Bug Report: player_info displays stale credits from database

**Date:** 2025-11-06T00:06:00Z
**Severity:** HIGH
**Status:** NEW
**Reporter:** Claude Code (on behalf of Admiral)

## Summary
The `player_info` MCP tool displays stale credits (0) from the local database instead of fetching fresh data from the SpaceTraders API, preventing TARS from making accurate strategic decisions.

## Impact
- **Operations Affected:** All strategic planning, contract evaluation, ship purchases
- **Credits Lost:** N/A (information accuracy issue, not financial loss)
- **Duration:** Since player registration
- **Workaround:** None currently - must manually check SpaceTraders API directly

## Steps to Reproduce
1. Register new player via MCP tool: `player_register(agent_symbol="ENDURANCE", token="<token>")`
2. SpaceTraders API responds with 175,000 starting credits
3. Call `player_info()` MCP tool
4. Observe output shows: `Credits: 0`

## Expected Behavior
The `player_info` command should:
1. Fetch latest agent data from SpaceTraders API endpoint `/my/agent`
2. Update local database cache with fresh values
3. Display accurate credits (175,000 for newly registered ENDURANCE agent)
4. Show additional agent info like headquarters location

## Actual Behavior
The `player_info` command:
1. Only queries local SQLite database
2. Returns stale/default credits value (0)
3. Never syncs with SpaceTraders API
4. Missing agent metadata like headquarters

## Evidence

### Command Output
```
Player 1:
  Agent: ENDURANCE
  Credits: 0
  Created: 2025-11-06T02:49:27.108973+00:00
  Last Active: 2025-11-06T02:49:27.108973+00:00
```

### Ship State
```
Ships (2):
--------------------------------------------------------------------------------
  ENDURANCE-1
    Location: X1-HZ85-A1
    Status: DOCKED
    Fuel: 100% (400/400)
    Cargo: 0/40

  ENDURANCE-2
    Location: X1-HZ85-H52
    Status: DOCKED
    Fuel: 0% (0/0)
    Cargo: 0/0
```

### API Client Evidence
API client has `get_agent()` method available but unused:
```python
# bot/src/adapters/secondary/api/client.py:68-69
def get_agent(self) -> Dict:
    return self._request("GET", "/my/agent")
```

### Query Handler Code
```python
# bot/src/application/player/queries/get_player.py
# GetPlayerQuery and GetPlayerByAgentQuery only query repository
# No API sync mechanism exists
```

## Root Cause Analysis
The player registration flow creates a database entry with default values (credits=0) but never fetches the actual agent data from the SpaceTraders API. The query handlers (`GetPlayerQuery`, `GetPlayerByAgentQuery`) are designed to only read from the repository without any API synchronization.

This is an architectural gap - there's no mechanism to sync player state from the API, unlike ships which have `SyncShipsCommand`.

## Potential Fixes

1. **Create SyncPlayerCommand (Recommended)**
   - Add `SyncPlayerCommand` similar to `SyncShipsCommand`
   - Fetches agent data from API using `get_agent()`
   - Updates repository with fresh credits, headquarters, etc.
   - Call automatically after registration and on `player_info` first access
   - **Rationale:** Consistent with existing ship sync pattern, clear separation of concerns

2. **Make Query Handler Call API Directly**
   - Modify `GetPlayerQuery` handler to call API if data is stale
   - Update repository before returning
   - **Tradeoff:** Mixes query and command responsibilities, violates CQRS

3. **Auto-sync on Registration**
   - Have `RegisterPlayerCommand` call `/my/agent` after creating database entry
   - Store all agent metadata immediately
   - **Tradeoff:** Only fixes initial registration, won't update credits as they change

## Recommended Implementation
```python
# bot/src/application/player/commands/sync_player.py
@dataclass(frozen=True)
class SyncPlayerCommand(Request[Player]):
    """Command to sync player data from API"""
    player_id: int

class SyncPlayerHandler(RequestHandler[SyncPlayerCommand, Player]):
    def __init__(self, player_repository: IPlayerRepository, api_client: ISpaceTradersAPI):
        self._player_repo = player_repository
        self._api = api_client

    async def handle(self, request: SyncPlayerCommand) -> Player:
        # 1. Get player from repo for token
        player = self._player_repo.get_by_id(request.player_id)

        # 2. Fetch latest agent data from API
        agent_data = self._api.get_agent()

        # 3. Update player with fresh data
        player.set_credits(agent_data['data']['credits'])
        player.set_headquarters(agent_data['data']['headquarters'])

        # 4. Persist updated player
        return self._player_repo.update(player)
```

## Environment
- Agent: ENDURANCE
- System: X1-HZ85
- Ships Involved: ENDURANCE-1, ENDURANCE-2
- MCP Tools Used: player_info, player_register, ship_list
- Container ID: N/A (not daemon-related)
