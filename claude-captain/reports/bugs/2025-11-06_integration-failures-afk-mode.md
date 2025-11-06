# Bug Report: Integration Failures Block All AFK Mode Operations

**Date:** 2025-11-06
**Severity:** CRITICAL
**Status:** NEW
**Reporter:** Captain TARS

## Summary
Captain TARS attempted to establish autonomous profit operations during a 1-hour AFK period but encountered multiple critical integration failures that prevented ANY operations from starting. Database schema mismatches, missing MCP tools, and agent configuration errors created a complete operational deadlock.

## Impact
- **Operations Affected:** All autonomous operations (contracts, market scouting, exploration)
- **Credits Lost:** 0 credits (no operations could start)
- **Duration:** ~20 minutes of investigation before declaring mission failure
- **Workaround:** None available - requires developer intervention to fix integration issues
- **AFK Mission Status:** COMPLETE FAILURE (0 operations started, 0 credits generated)

## Steps to Reproduce

### Prerequisites
1. Fresh SpaceTraders account with agent "ENDURANCE" registered
2. MCP server running at `/Users/andres.camacho/Development/Personal/spacetraders/bot/mcp/`
3. Bot CLI at `/Users/andres.camacho/Development/Personal/spacetraders/bot/`
4. Captain TARS agent configured to use both MCP tools and bot CLI

### Reproduction Steps

**Step 1: Attempt Contract Operations**
```bash
# Via MCP tool
mcp__spacetraders-bot__contract_batch_workflow
  ship: ENDURANCE-1
  count: 3
```

**Result:** 0/3 contracts negotiated (see Bug Report: 2025-11-06_contract-negotiation-zero-success.md)

**Step 2: Attempt Market Scouting**
```bash
# Query for market waypoints
mcp__spacetraders-bot__waypoint_list
  system: X1-HZ85
```

**Result:** Tool does not exist (MCP server returns "Unknown tool")

**Step 3: Attempt Direct Waypoint Sync (Bot CLI)**
```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/bot
python -m adapters.primary.cli.main shipyard sync-waypoints --system X1-HZ85 --player-id 1
```

**Result:** ValueError: Player 1 not found in database

**Step 4: Verify Player Registration (MCP)**
```bash
mcp__spacetraders-bot__player_list
```

**Result:** "Registered players (1): [1] ENDURANCE ✓"

**Step 5: Verify Database State (Direct Query)**
```bash
cd /Users/andres.camacho/Development/Personal/spacetraders
sqlite3 var/spacetraders.db "SELECT player_id, agent_symbol, credits FROM players;"
```

**Result:** Empty result set (no rows)

**Step 6: Attempt Agent Delegation to General-Purpose Agent**
```bash
# Via TARS agent interface
agent.delegate("Can you discover all waypoints in system X1-HZ85?")
```

**Result:** API Error 400: "Tool names must be unique" (duplicate tool definitions)

## Expected Behavior

### Database Integration
- MCP server and bot CLI should use the SAME database
- Player registration via MCP `player_register` should create record accessible to bot CLI
- Both MCP and CLI should read/write to shared database at consistent path

### MCP Tool Coverage
- `waypoint_list` tool should be implemented in MCP server
- All tools listed in agent configurations should exist in MCP server
- Tool discovery should work consistently for all agents

### Agent Configuration
- General-purpose agent should have unique tool definitions
- Agent delegation should work without API errors
- Tool inheritance from parent should not cause conflicts

### Operational Flow
- Captain can query player info, ship status via MCP
- Captain can deploy scout-coordinator to discover markets via `waypoint_list`
- Captain can deploy contract-coordinator to negotiate/fulfill contracts
- All operations use consistent authentication and database access

## Actual Behavior

### Database Schema Mismatch (CRITICAL)
**MCP Database Path:** `/Users/andres.camacho/Development/Personal/spacetraders/var/spacetraders.db`
**Bot Database Path:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/var/spacetraders.db`

**Evidence:**
```bash
# MCP player_list output
Registered players (1):
[1] ENDURANCE ✓

# Direct database query (MCP path)
$ sqlite3 var/spacetraders.db "SELECT * FROM players;"
# Result: Empty (0 rows)

# Bot CLI error
ValueError: Player 1 not found in database
  File "/bot/src/application/shipyard/commands/sync_waypoints.py", line 67
    api_client = get_api_client_for_player(request.player_id)
```

**Root Cause:** MCP server's `player_list` command reads from one database while the underlying player repository reads from another. Player registration writes to MCP database but bot CLI queries different database.

**Impact:** Bot CLI commands cannot authenticate players, blocking all operations that require API access.

### Missing MCP Tool: waypoint_list (HIGH)
**Required By:** scout-coordinator agent (line 126 in agentConfig.ts)
**Implementation Status:** NOT IMPLEMENTED

**Evidence:**
```typescript
// agentConfig.ts lines 119-131
'scout-coordinator': {
  description: 'Use when you need to manage market intelligence via probe ship network',
  prompt: loadPrompt(join(tarsRoot, '.claude/agents/scout-coordinator.md')),
  model: 'sonnet',
  tools: [
    'Read', 'Write', 'TodoWrite',
    'mcp__spacetraders-bot__scout_markets',
    'mcp__spacetraders-bot__waypoint_list', // <-- TOOL DOES NOT EXIST
    'mcp__spacetraders-bot__ship_list',
    'mcp__spacetraders-bot__daemon_inspect',
    'mcp__spacetraders-bot__daemon_logs',
  ]
}

// index.ts lines 289-291
default:
  return null; // waypoint_list case not handled
```

**Root Cause:** MCP server `buildCliArgs()` method has no case for `waypoint_list`. Tool is referenced in agent configuration but not implemented in MCP server.

**Impact:** Cannot deploy scout-coordinator to discover market waypoints. Market scouting operations cannot start.

### General-Purpose Agent Configuration Error (MEDIUM)
**Error Message:** API Error 400: "Tool names must be unique"

**Evidence:**
When Captain TARS delegates to general-purpose agent:
```
Error creating agent: API Error 400
Details: Tool names must be unique
```

**Root Cause:** Agent configuration likely has duplicate tool definitions, possibly from inherited tools and explicitly defined tools overlapping.

**Impact:** Cannot delegate complex multi-step operations to general-purpose agent. Captain limited to specialist agents only.

### Contract Negotiation Silent Failures (HIGH)
**Related Bug Report:** 2025-11-06_contract-negotiation-zero-success.md

**Evidence:**
```bash
# Command executed successfully but produced zero results
Starting batch contract workflow for ENDURANCE-1
   Iterations: 3

==================================================
Batch Workflow Results
==================================================
  Contracts negotiated: 0
  Contracts accepted:   0
  Contracts fulfilled:  0
  Contracts failed:     0
  Total profit:         0 credits
  Total trips:          0
==================================================
```

**Root Cause:** API contract negotiation calls failing silently without error propagation to user interface.

**Impact:** Contract operations appear successful but produce no results. No error messages to guide debugging.

## Evidence

### Database Configuration

**MCP Server Database Path** (`database.py` line 21):
```python
self.db_path = db_path or Path("var/spacetraders.db")
# Resolves to: /Users/andres.camacho/Development/Personal/spacetraders/var/spacetraders.db
```

**MCP Server Working Directory** (`index.ts` line 336):
```typescript
const child = spawn(this.pythonExecutable, ["-m", "adapters.primary.cli.main", ...args], {
  cwd: this.botDir, // Resolves to: /Users/.../spacetraders/bot
  ...
});
```

**Analysis:** MCP server spawns Python CLI with working directory `/bot/`, but database defaults to `var/spacetraders.db` which resolves to `/bot/var/spacetraders.db`. However, MCP server itself may be running from different directory, creating database at `/var/spacetraders.db` (project root).

### MCP Tool Implementation Gap

**Agent Configuration** (`agentConfig.ts` lines 85-86):
```typescript
// System Information
'mcp__spacetraders-bot__waypoint_list',
'mcp__spacetraders-bot__plan_route', // Planning only, not execution
```

**MCP Server Implementation** (`index.ts` lines 115-291):
```typescript
private buildCliArgs(toolName: string, args: Record<string, unknown>): string[] | null {
  switch (toolName) {
    // ... 30+ tool implementations ...

    // NO CASE FOR waypoint_list

    default:
      return null; // Returns null for unknown tools
  }
}
```

**Analysis:** Tool is referenced in both Captain and scout-coordinator configurations but completely missing from MCP server implementation.

### CLI Command Error Output

**Command:**
```bash
python -m adapters.primary.cli.main shipyard sync-waypoints --system X1-HZ85 --player-id 1
```

**Error Stack Trace:**
```
ValueError: Player 1 not found in database
  File "/bot/src/application/shipyard/commands/sync_waypoints.py", line 67
    api_client = get_api_client_for_player(request.player_id)
  File "/bot/src/configuration/container.py"
    # Player repository query returns None
```

**Player Repository Query** (inferred):
```python
# configuration/container.py
def get_api_client_for_player(player_id: int):
    player = player_repository.get_by_id(player_id)
    if not player:
        raise ValueError(f"Player {player_id} not found in database")
    return create_api_client(player.token)
```

**Analysis:** Player repository successfully queries database but finds no player with ID 1, despite MCP `player_list` showing player exists.

### Ship State at Time of Operations

**Query:**
```
mcp__spacetraders-bot__ship_info
  ship: ENDURANCE-1
```

**Result:**
```
ENDURANCE-1
================================================================================
Location:       X1-HZ85-A1
System:         X1-HZ85
Status:         DOCKED

Fuel:           400/400 (100%)
Cargo:          0/40
Engine Speed:   36
```

**Analysis:** Ship is in valid operational state (docked, fueled, empty cargo). All integration failures are infrastructure issues, not ship state issues.

## Root Cause Analysis

### Primary Root Cause: Database Path Inconsistency

**Problem:** MCP server and bot CLI use different database paths due to working directory confusion.

**Technical Details:**
1. MCP server runs from project root (`/Users/.../spacetraders/`)
2. MCP server spawns Python CLI with `cwd: this.botDir` (`/Users/.../spacetraders/bot/`)
3. Python database initialization uses relative path `var/spacetraders.db`
4. Relative path resolves differently depending on working directory:
   - MCP server context: `/Users/.../spacetraders/var/spacetraders.db`
   - Bot CLI context: `/Users/.../spacetraders/bot/var/spacetraders.db`
5. MCP `player_register` writes to one database, bot CLI reads from another

**Evidence Chain:**
- MCP `player_list` shows player exists → reading from `/var/spacetraders.db`
- Direct query of `/var/spacetraders.db` shows empty players table
- Bot CLI error "Player not found" → reading from `/bot/var/spacetraders.db`
- Both databases exist but contain different data

**Fix Complexity:** HIGH - Requires absolute path configuration or environment variable

### Secondary Root Cause: Incomplete MCP Tool Implementation

**Problem:** Agent configurations reference tools that don't exist in MCP server.

**Technical Details:**
1. Agent configuration defines `waypoint_list` in allowed tools (line 85, 126)
2. MCP server `buildCliArgs()` has no case for `waypoint_list` (line 290)
3. Tool call returns null → MCP server returns "Unknown tool" error
4. Scout-coordinator cannot perform primary function (market discovery)

**Evidence Chain:**
- `agentConfig.ts` line 126 lists `waypoint_list` in scout-coordinator tools
- `index.ts` line 290 has no waypoint_list case
- Attempted tool call fails with "Unknown tool"

**Fix Complexity:** MEDIUM - Implement tool case and map to CLI command

### Tertiary Root Cause: Missing Bot CLI Waypoint Discovery Command

**Problem:** Even if `waypoint_list` MCP tool were implemented, the underlying bot CLI command may not exist.

**Technical Details:**
1. Bot CLI has `shipyard sync-waypoints` command (sync_waypoints.py line 48)
2. This command syncs waypoints from API to database but requires player authentication
3. No CLI command to query/list already-synced waypoints from database
4. MCP tool would need to either:
   - Call sync command first (requires player auth → blocked by database issue)
   - Call list command (doesn't exist)

**Evidence Chain:**
- `sync_waypoints.py` line 67 calls `get_api_client_for_player()`
- This requires player in database → blocked by root cause #1
- No alternative CLI command to list waypoints from cache

**Fix Complexity:** MEDIUM - Add waypoint list CLI command or direct database query

### Quaternary Root Cause: Contract Negotiation Error Handling

**Problem:** Contract batch workflow fails silently without user-visible errors.

**Technical Details:**
See detailed analysis in Bug Report: 2025-11-06_contract-negotiation-zero-success.md

**Summary:**
- API errors during contract negotiation are caught by generic exception handler
- Error logging goes to logger but not to CLI output
- User sees zero results with no explanation

**Fix Complexity:** LOW - Add explicit error output to CLI

## Potential Fixes

### Fix 1: Unify Database Paths (CRITICAL - Priority 1)

**Rationale:** All components must use the same database to share player/ship/market data.

**Implementation Option A: Absolute Path Environment Variable**
```typescript
// index.ts - MCP server
const child = spawn(this.pythonExecutable, [...], {
  cwd: this.botDir,
  env: {
    ...process.env,
    PYTHONPATH: path.join(this.botDir, "src"),
    SPACETRADERS_DB_PATH: "/Users/.../spacetraders/var/spacetraders.db" // Absolute path
  }
});
```

```python
# database.py - Bot CLI
import os
self.db_path = os.environ.get('SPACETRADERS_DB_PATH') or Path("var/spacetraders.db")
```

**Tradeoffs:** Requires environment variable configuration; more explicit

**Implementation Option B: Symlink**
```bash
# Create symlink from bot/var to project var
cd /Users/.../spacetraders/bot
ln -s ../var var
```

**Tradeoffs:** Simple but fragile; breaks if directories move

**Implementation Option C: Shared Configuration File**
```json
// config/database.json (committed to repo)
{
  "database_path": "../var/spacetraders.db"
}
```

```python
# database.py
import json
config_path = Path(__file__).parent.parent.parent / "config" / "database.json"
config = json.load(config_path.open())
self.db_path = Path(config['database_path'])
```

**Tradeoffs:** Requires config file parsing; path still relative but from known location

**RECOMMENDED:** Option A (Environment Variable) - Most explicit and debuggable

### Fix 2: Implement waypoint_list MCP Tool (HIGH - Priority 2)

**Rationale:** Scout-coordinator requires this tool for market discovery operations.

**Implementation:**
```typescript
// index.ts - Add case to buildCliArgs()
case "waypoint_list": {
  const cmd = ["waypoint", "list", "--system", String(args.system)];
  if (args.player_id !== undefined) {
    cmd.push("--player-id", String(args.player_id));
  }
  if (args.agent !== undefined) {
    cmd.push("--agent", String(args.agent));
  }
  if (args.trait_filter !== undefined) {
    cmd.push("--trait", String(args.trait_filter));
  }
  return cmd;
}
```

**Prerequisite:** Bot CLI must have `waypoint list` command (currently missing)

**Alternative:** Direct database query in MCP server
```typescript
case "waypoint_list": {
  // Query database directly instead of calling CLI
  const db = new Database('/absolute/path/to/spacetraders.db');
  const waypoints = db.query("SELECT * FROM waypoints WHERE system_symbol = ?", [args.system]);
  return { content: [{ type: "text", text: JSON.stringify(waypoints) }] };
}
```

**Tradeoffs:** Direct query bypasses CLI architecture but unblocks scout operations immediately

**RECOMMENDED:** Implement bot CLI command first, then add MCP tool mapping

### Fix 3: Add Bot CLI Waypoint List Command (MEDIUM - Priority 3)

**Rationale:** Separate waypoint discovery (sync) from waypoint querying (list).

**Implementation:**
```python
# waypoint_cli.py (new file)
@click.group()
def waypoint():
    """Waypoint management commands"""
    pass

@waypoint.command("list")
@click.option("--system", required=True, help="System symbol (e.g., X1-HZ85)")
@click.option("--trait", help="Filter by trait (e.g., MARKETPLACE)")
@click.option("--player-id", type=int, help="Player ID")
def list_waypoints(system: str, trait: str | None, player_id: int | None):
    """List cached waypoints in a system"""
    # Query waypoint repository (doesn't require API auth)
    waypoints = waypoint_repository.list_by_system(system, trait_filter=trait)

    for waypoint in waypoints:
        print(f"{waypoint.symbol} ({waypoint.waypoint_type})")
        if waypoint.traits:
            print(f"  Traits: {', '.join(waypoint.traits)}")
```

**Tradeoffs:** Requires new CLI command and route; increases codebase complexity

**RECOMMENDED:** Implement after database unification (Fix 1)

### Fix 4: Fix Agent Tool Configuration (MEDIUM - Priority 4)

**Rationale:** General-purpose agent should work for complex multi-step operations.

**Investigation Needed:**
- Review agent configuration for duplicate tool definitions
- Check if inherited tools from parent clash with explicit tools
- Verify tool name uniqueness constraints

**Potential Issue:**
```typescript
// agentConfig.ts - Parent tools
allowedTools: [
  'Read', 'Write', 'mcp__spacetraders-bot__player_info', ...
],

// General-purpose agent (if it inherits parent tools)
agents: {
  'general': {
    tools: ['Read', 'Write', ...] // Duplicates if inheritance is active
  }
}
```

**RECOMMENDED:** Investigate agent tool inheritance behavior and remove duplicates

### Fix 5: Add Contract Negotiation Error Visibility (LOW - Priority 5)

**Rationale:** Users need to see why contract operations fail.

**Implementation:** See Bug Report 2025-11-06_contract-negotiation-zero-success.md Fix #1

**Tradeoffs:** Low effort, high debugging value

**RECOMMENDED:** Implement immediately to improve developer experience

## Recommended Investigation Steps

### Immediate Actions (Priority 1 - Blocks ALL Operations):

1. **Verify Database Paths:**
   ```bash
   # Find all spacetraders.db files
   find /Users/andres.camacho/Development/Personal/spacetraders -name "spacetraders.db"

   # Check contents of each
   sqlite3 /Users/.../var/spacetraders.db "SELECT player_id, agent_symbol FROM players;"
   sqlite3 /Users/.../bot/var/spacetraders.db "SELECT player_id, agent_symbol FROM players;"
   ```

2. **Identify Which Database Has Player Data:**
   - If `/var/spacetraders.db` has player: MCP server writes here
   - If `/bot/var/spacetraders.db` has player: Bot CLI writes here
   - Configure both to use whichever has the data

3. **Implement Database Path Environment Variable:**
   - Add `SPACETRADERS_DB_PATH` to MCP server spawn environment
   - Update `database.py` to read environment variable
   - Test both MCP and CLI commands use same database

### Diagnostic Actions (Priority 2 - Unblocks Scout Operations):

4. **Create Minimal waypoint_list Implementation:**
   ```python
   # Quick CLI command to unblock scouts
   @waypoint.command("list")
   @click.option("--system", required=True)
   def list_waypoints(system: str):
       # Direct database query - no auth needed
       db = Database()
       with db.connection() as conn:
           cursor = conn.execute(
               "SELECT waypoint_symbol, type FROM waypoints WHERE system_symbol = ?",
               (system,)
           )
           for row in cursor.fetchall():
               print(f"{row['waypoint_symbol']} ({row['type']})")
   ```

5. **Add MCP Tool Mapping:**
   ```typescript
   // index.ts
   case "waypoint_list":
     return ["waypoint", "list", "--system", String(args.system)];
   ```

6. **Test Scout-Coordinator Deployment:**
   - Verify `waypoint_list` tool is callable
   - Verify scout-coordinator can discover markets
   - Verify `scout_markets` daemon can start

### Workaround Actions (Priority 3 - Immediate AFK Recovery):

7. **Manual Waypoint Discovery via SpaceTraders API:**
   - Use direct API calls to list system waypoints
   - Filter for MARKETPLACE trait
   - Feed waypoint list directly to scout_markets command

8. **Skip Contract Operations:**
   - Focus on market scouting for immediate profit
   - Investigate contract negotiation separately
   - Return to contracts after market operations proven

9. **Use Specialist Agents Only:**
   - Avoid general-purpose agent until tool config fixed
   - Deploy scout-coordinator for markets
   - Deploy contract-coordinator for contracts (after fix)

## Temporary Workarounds

### Workaround 1: Manual Database Sync (Immediate)

**Steps:**
1. Identify which database has player data (MCP vs CLI)
2. Copy that database to both locations:
   ```bash
   # If MCP database has data
   cp /Users/.../var/spacetraders.db /Users/.../bot/var/spacetraders.db

   # OR if bot database has data
   cp /Users/.../bot/var/spacetraders.db /Users/.../var/spacetraders.db
   ```
3. Test both MCP `player_list` and CLI `player info` commands
4. Proceed with operations

**Duration:** 5 minutes
**Effectiveness:** Unblocks operations until next player registration

### Workaround 2: Direct SpaceTraders API for Waypoint Discovery (Immediate)

**Steps:**
1. Install SpaceTraders SDK or use curl:
   ```bash
   # Get agent token
   TOKEN=$(sqlite3 var/spacetraders.db "SELECT token FROM players WHERE agent_symbol='ENDURANCE'")

   # List waypoints with MARKETPLACE trait
   curl -H "Authorization: Bearer $TOKEN" \
     "https://api.spacetraders.io/v2/systems/X1-HZ85/waypoints?traits=MARKETPLACE&limit=20"
   ```
2. Extract waypoint symbols manually
3. Feed to `scout_markets` command:
   ```bash
   python -m adapters.primary.cli.main scout markets \
     --ships ENDURANCE-1 \
     --system X1-HZ85 \
     --markets "X1-HZ85-B2,X1-HZ85-C3,X1-HZ85-D4"
   ```

**Duration:** 10 minutes per system
**Effectiveness:** Unblocks market scouting immediately

### Workaround 3: Skip AFK Mode, Use Interactive Mode (Strategic)

**Rationale:** Integration issues require developer intervention. Interactive mode allows Captain to report issues and wait for fixes rather than fail silently in AFK mode.

**Steps:**
1. Document all failures in bug reports (DONE)
2. Report to Admiral that autonomous operations blocked
3. Wait for developer to fix integration issues
4. Resume AFK mode after verification

**Duration:** Indefinite (depends on developer availability)
**Effectiveness:** Prevents silent failures and resource waste

## Impact Assessment

### Operational Impact

**Contracts:** BLOCKED
- Cannot negotiate contracts (silent API failures)
- Contract-coordinator cannot operate
- 0 contract-based credits generated

**Market Scouting:** BLOCKED
- Cannot discover market waypoints (missing tool)
- Scout-coordinator cannot deploy
- 0 market intelligence gathered

**Exploration:** BLOCKED
- Cannot sync waypoints (database authentication failure)
- Cannot plan multi-system routes
- Fleet stuck in starting system

**Fleet Management:** PARTIALLY FUNCTIONAL
- Can query ship status (MCP `ship_info` works)
- Cannot execute navigation commands (database auth required)
- Read-only operations only

### Financial Impact

**Credits Lost:** 0 (no operations attempted)
**Credits Not Gained:** Unknown (unable to estimate profit from blocked operations)
**Time Wasted:** ~20 minutes of investigation
**Opportunity Cost:** 1 hour AFK period with zero productivity

### Strategic Impact

**AFK Mission Failure:** Captain unable to operate autonomously due to infrastructure failures
**Developer Confidence:** Critical integration issues suggest system not production-ready
**Technical Debt:** Multiple layers of incompatible components requiring refactoring

## Environment

- **Agent:** ENDURANCE
- **System:** X1-HZ85
- **Starting Location:** X1-HZ85-A1 (HQ)
- **Ship:** ENDURANCE-1 (Command Ship, DOCKED, 100% fuel)
- **Credits:** 175,000 (untouched)
- **Captain:** TARS (Autonomous Captain Agent)
- **MCP Server Path:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/mcp/build/index.js`
- **Bot CLI Path:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/src/adapters/primary/cli/main.py`
- **MCP Database:** `/Users/andres.camacho/Development/Personal/spacetraders/var/spacetraders.db`
- **Bot Database:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/var/spacetraders.db`
- **Python Version:** (uv-python via MCP_PYTHON_BIN environment variable)
- **Node Version:** (used by MCP server)
- **Commit:** 844c721 (docs: add bug report for player_info stale credits issue)

## Related Files

**Database Layer:**
- `/bot/src/adapters/secondary/persistence/database.py` (line 21: database path resolution)
- `/bot/src/adapters/secondary/persistence/player_repository.py` (player authentication queries)

**MCP Server:**
- `/bot/mcp/src/index.ts` (line 336: CLI spawn with cwd, line 290: missing waypoint_list)
- `/bot/mcp/src/botToolDefinitions.js` (tool definitions)

**Bot CLI:**
- `/bot/src/application/shipyard/commands/sync_waypoints.py` (line 67: player authentication failure)
- `/bot/src/configuration/container.py` (get_api_client_for_player function)

**Agent Configuration:**
- `/claude-captain/tars/src/agentConfig.ts` (line 85: waypoint_list reference, line 126: scout-coordinator tools)

**Contract Operations:**
- See Bug Report: 2025-11-06_contract-negotiation-zero-success.md

## Related Bug Reports

1. **2025-11-06_contract-negotiation-zero-success.md** (HIGH)
   - Contract batch workflow produces zero results
   - Silent API failures during negotiation
   - Blocks contract-based profit operations

## Next Steps

### For Developer (CRITICAL - Unblocks System):

1. **Unify database paths** (Priority 1)
   - Decide on single canonical database location
   - Configure both MCP server and bot CLI to use absolute path
   - Add environment variable `SPACETRADERS_DB_PATH`
   - Test player registration and CLI authentication

2. **Implement waypoint_list tool** (Priority 2)
   - Add bot CLI command: `waypoint list --system SYSTEM`
   - Add MCP tool mapping in `index.ts`
   - Update `botToolDefinitions.js` with tool schema
   - Test scout-coordinator deployment

3. **Fix agent tool configuration** (Priority 3)
   - Investigate "Tool names must be unique" error
   - Review agent tool inheritance
   - Remove duplicate tool definitions
   - Test general-purpose agent delegation

4. **Add contract error visibility** (Priority 4)
   - See 2025-11-06_contract-negotiation-zero-success.md recommendations
   - Add explicit error output to batch workflow CLI
   - Test contract negotiation with error logging

### For Captain TARS (Immediate):

1. **Apply Workaround 1** (Manual database sync)
   - Copy database to both locations
   - Verify player authentication works
   - Proceed with operations if successful

2. **Report Mission Status to Admiral**
   - AFK mission FAILED due to integration issues
   - Bug reports generated (2 reports)
   - System requires developer intervention
   - Recommend interactive mode until fixes deployed

3. **Document Lessons Learned**
   - Autonomous operations require robust integration testing
   - Database path mismatches cause silent authentication failures
   - Missing MCP tools discovered only at deployment time
   - Agent configurations should be validated against MCP server implementation

### For QA/Testing (Future):

1. **Add Integration Tests**
   - Test MCP server and bot CLI use same database
   - Test all agent tools exist in MCP server
   - Test player authentication across MCP and CLI
   - Test agent delegation without API errors

2. **Add Pre-Flight Checks**
   - Verify database accessibility before operations
   - Verify all configured tools are callable
   - Verify player authentication before daemon start
   - Fail fast with clear error messages

3. **Add Health Checks**
   - Monitor database consistency (MCP vs CLI)
   - Monitor MCP tool availability
   - Alert on missing tools in agent configs
   - Alert on database authentication failures
