# Feature Proposal: Waypoint Synchronization MCP Tool

**Date:** 2025-11-06 17:15
**Priority:** CRITICAL
**Category:** NEW_MCP_TOOL
**Status:** PROPOSED

## Problem Statement

Agent ENDURANCE (player_id: 1) cannot execute ANY autonomous operations requiring navigation because the waypoint cache is empty. This creates a bootstrap paradox that blocks all revenue-generating activities during AFK sessions.

**The Bootstrap Paradox:**
- Need waypoints in cache to plan routes
- Need routes to navigate ships
- Need navigation to discover waypoints
- But waypoint discovery requires navigation
- Result: Complete operational deadlock

## Current Behavior

### What Exists (Read-Only):
**MCP Tool:** `waypoint_list`
- Queries LOCAL cache only
- Returns empty list if cache unpopulated
- No ability to fetch from SpaceTraders API
- File: `bot/mcp/src/botToolDefinitions.ts` lines 218-248

**Internal Command:** `SyncSystemWaypointsCommand`
- EXISTS in bot internals: `bot/src/application/shipyard/commands/sync_waypoints.py`
- Fetches waypoints from SpaceTraders API
- Handles pagination (20 waypoints per page)
- Stores waypoints in cache via WaypointRepository
- **NOT exposed via MCP** - only accessible via direct CLI (if CLI command exists)

### Current Workarounds:
**None viable for autonomous operations.**

Manual intervention options:
1. Manual CLI: `python -m bot.cli waypoint sync X1-HZ85` (if command exists)
2. Manual script: Direct database population (bypasses bot architecture)
3. Manual navigation: Send ship to each waypoint to discover them (slow, expensive)

**All workarounds defeat the purpose of AFK autonomous mode.**

## Impact

### Operations Blocked:
**Contracts:** Cannot navigate to delivery waypoints or sourcing markets
- Tool: `contract_batch_workflow` unusable
- Strategy: Phase 1 contract strategy (strategies.md lines 30-47) BLOCKED
- Revenue Impact: 10-20K credits/contract lost opportunity

**Mining:** Cannot navigate to asteroid fields
- Strategy: Mining operations (strategies.md lines 99-205) BLOCKED
- Revenue Impact: Unknown credits/hour lost

**Scouting:** Cannot deploy probes to market waypoints
- Tool: `scout_markets` unusable
- Strategy: Market intelligence (strategies.md lines 257-284) BLOCKED
- Intelligence Impact: Zero price visibility

**Trading:** Cannot execute trade routes
- Strategy: Trade arbitrage (strategies.md lines 286-338) BLOCKED
- Revenue Impact: Highest profit strategy unavailable

**AFK Mode:** Completely non-functional
- Evidence: Strategic assessment report (2025-11-06_strategic-assessment_afk-mode-capability-gap.md)
- Result: 0 credits/hour, 0% fleet utilization over 20-minute test

### Metrics Supporting This Problem

**Current State (Agent ENDURANCE):**
```
System: X1-HZ85
Waypoints in cache: 0
Marketplaces known: 0
Asteroid fields known: 0
Fuel stations known: 0

Fleet Status:
- ENDURANCE-1: 40-cargo hauler, DOCKED at X1-HZ85-A1
- ENDURANCE-2: Solar probe, DOCKED at X1-HZ85-A1

Operations Running: 0
Credits/Hour: 0
Fleet Utilization: 0%
Success Rate: 0% (all navigation operations fail)
```

**Expected State (After Fix):**
```
System: X1-HZ85
Waypoints in cache: 40-60 (typical system size)
Marketplaces known: 5-8
Asteroid fields known: 3-5
Fuel stations known: 6-10

Operations Enabled:
- Contract delivery routes
- Mining navigation
- Market scouting tours
- Trade arbitrage routes

Expected Credits/Hour: 5-15K (Phase 1 contracts)
Expected Fleet Utilization: 60-80%
```

## Proposed Solution

### What We Need: `waypoint_sync` MCP Tool

**Purpose:** Populate waypoint cache from SpaceTraders API to enable navigation operations.

**User Stories:**

**Story 1: Bootstrap New System**
- As TARS Captain
- When starting operations in a new system
- I need to synchronize all waypoints from the SpaceTraders API
- So I can plan routes and navigate ships autonomously
- Expected: Waypoint cache populated with 40-60 waypoints in under 30 seconds

**Story 2: Enable AFK Operations**
- As Admiral leaving for AFK session
- When I leave TARS to operate autonomously
- I need waypoint cache to be populated
- So TARS can execute contracts, mining, and trading without manual intervention
- Expected: All navigation operations work without KeyError exceptions

**Story 3: System Expansion**
- As Fleet Manager
- When expanding operations to a new system
- I need to sync that system's waypoints
- So ships can navigate to new markets and resources
- Expected: Multi-system operations supported

### Required Information

**Input Parameters:**
- `system` (string, required): System symbol (e.g., "X1-HZ85")
- `player_id` (integer, optional): Player ID for API authentication
- `agent` (string, optional): Agent symbol as alternative to player_id

**Data Retrieved from SpaceTraders API:**
- Waypoint symbol (e.g., "X1-HZ85-A1")
- Waypoint type (PLANET, ASTEROID_FIELD, ORBITAL_STATION, JUMP_GATE, etc.)
- Coordinates (x, y)
- Traits (MARKETPLACE, SHIPYARD, FUEL_STATION, etc.)
- Orbitals (if waypoint has satellites)

**Data Stored in Cache:**
- All waypoint metadata above
- Derived fields: has_fuel boolean, has_marketplace boolean
- System association for fast lookup

### Expected Behavior

**Successful Sync:**
```
Syncing waypoints for system X1-HZ85...
✓ Fetched 47 waypoints from SpaceTraders API (3 pages)
✓ Stored in local cache

Waypoint Types Discovered:
- 12 PLANET
- 8 ASTEROID_FIELD
- 5 ORBITAL_STATION
- 3 JUMP_GATE
- 19 Other types

Key Locations:
- Marketplaces: 6 waypoints
- Shipyards: 2 waypoints
- Fuel stations: 8 waypoints

Cache ready for navigation operations.
```

**Already Synced (Idempotent):**
```
Syncing waypoints for system X1-HZ85...
✓ System already cached with 47 waypoints
✓ Refreshed waypoint data

No changes detected. Cache is up-to-date.
```

**API Error Handling:**
```
Syncing waypoints for system X1-HZ85...
✗ API Error: System not found (404)

Possible causes:
- Invalid system symbol
- System not yet discovered
- API connectivity issue

Cache unchanged.
```

**Authentication Error:**
```
Syncing waypoints for system X1-HZ85...
✗ Authentication failed: Player not found in database

Please ensure:
- Player registered via player_register
- Default player set via config_set_player
- Or specify player_id/agent parameter

Cache unchanged.
```

## Acceptance Criteria

### Functional Requirements:

**Must:**
1. Fetch ALL waypoints in specified system from SpaceTraders API
2. Handle pagination automatically (20 waypoints per page)
3. Store waypoints in local cache via WaypointRepository
4. Support player authentication via player_id or agent symbol
5. Return summary with waypoint count and type breakdown
6. Be idempotent (safe to run multiple times on same system)
7. Work from TARS without requiring manual CLI intervention

**Should:**
8. Display progress for large systems (e.g., "Fetching page 3/5...")
9. Highlight key locations (marketplaces, shipyards, fuel stations)
10. Complete within 30 seconds for typical systems (50 waypoints)

**Must Not:**
11. Duplicate waypoints in cache (use upsert logic)
12. Fail silently (provide clear error messages)
13. Leave cache in inconsistent state on errors (rollback if needed)

### Performance Requirements:

**API Efficiency:**
- Minimize API calls (batch pagination)
- Respect rate limits (standard SpaceTraders throttling)
- Cache results to avoid repeated syncs

**User Experience:**
- Display progress for operations >10 seconds
- Provide actionable error messages
- Return control to Captain immediately after sync

### Edge Cases:

**Edge Case 1: Empty System**
- Expected: "✓ Fetched 0 waypoints (system empty or new)"
- Handle gracefully, don't error

**Edge Case 2: Very Large System**
- Expected: "Fetching page 8/12... (160/240 waypoints)"
- Handle pagination up to 500+ waypoints

**Edge Case 3: Network Failure Mid-Sync**
- Expected: "✗ Network error after 20 waypoints. Retrying..."
- Retry logic or graceful partial failure

**Edge Case 4: Concurrent Sync Requests**
- Expected: Queue or skip duplicate sync
- Don't spawn multiple API calls for same system

**Edge Case 5: Cache Already Populated**
- Expected: Refresh/update existing waypoints (upsert)
- Don't error, don't duplicate

## Evidence

### Proven Strategy Reference

**From strategies.md, lines 18-29 (Phase 1: Intelligence Network):**
> "Deploy scout ships to cover major trade routes in your system... Let scouts run continuously to gather price trends."

**Requirement:** Scouts need waypoints to visit. Waypoint sync is prerequisite for Phase 1 strategy.

**From strategies.md, lines 30-36 (Contract Operations):**
> "Start contract fulfillment with command ship immediately... Contracts provide TWO revenue touchpoints: acceptance payment + delivery payment."

**Requirement:** Contract delivery needs navigation to delivery waypoint. Waypoint sync is prerequisite for contracts.

**From strategies.md, lines 262-267 (Market Intelligence):**
> "Price data requires ship presence at waypoints. Deploy inexpensive probe ships to monitor market conditions over time."

**Requirement:** Cannot deploy probes to markets if markets are unknown. Waypoint sync discovers marketplaces.

### Related Bug Reports

**Bug Report:** 2025-11-06_integration-failures-afk-mode.md, lines 128-151
- Problem: scout-coordinator references non-existent `waypoint_list` tool
- Impact: Market scouting operations fail at planning stage

**Bug Report:** Route planning KeyError (mentioned in feature request)
- Problem: `plan_route` crashes when waypoint cache empty
- Impact: Navigation operations fail before attempting movement

**Strategic Assessment:** 2025-11-06_strategic-assessment_afk-mode-capability-gap.md, lines 135-151
- Problem: "Market discovery: BLOCKED (no waypoint_list tool)"
- Evidence: 0% success rate on all autonomous operations during 20-minute test

## Risks & Tradeoffs

### Risk 1: API Rate Limiting
**Concern:** Syncing large systems (100+ waypoints) could hit SpaceTraders rate limits.

**Acceptable because:**
- Waypoint sync is one-time operation per system
- Subsequent operations use cache (no repeated API calls)
- Rate limit impact is bounded and predictable
- Alternative (manual discovery) is far slower

**Mitigation:**
- Display progress during multi-page fetches
- Respect standard rate limiting (built into API client)
- Allow operation to complete over 30-60 seconds if needed

### Risk 2: Stale Cache Data
**Concern:** Cached waypoints may become outdated if SpaceTraders adds/modifies waypoints.

**Acceptable because:**
- Waypoint metadata rarely changes in SpaceTraders
- Traits like MARKETPLACE are persistent
- Can re-sync system manually if needed
- Benefits (fast lookups) outweigh risks (rare staleness)

**Mitigation:**
- Tool is idempotent (safe to re-sync periodically)
- Consider weekly auto-sync for active systems
- Provide clear command to refresh cache

### Risk 3: Database Size Growth
**Concern:** Caching 10 systems × 50 waypoints = 500 database rows.

**Acceptable because:**
- Waypoint data is small (~500 bytes per waypoint)
- 500 waypoints = ~250KB total (negligible)
- Enables massive performance improvement (cache vs API)
- Database can handle thousands of waypoints easily

**Mitigation:**
- Use indexed queries for fast lookups
- Consider purging unused systems after 30 days
- Monitor database size during operations

## Success Metrics

### How We'll Know This Worked:

**Immediate Success:**
1. **Waypoint cache populated:** `waypoint_list` returns 40-60 waypoints for X1-HZ85
2. **Navigation unblocked:** `plan_route` works without KeyError
3. **Operations enabled:** `contract_batch_workflow` can navigate to delivery waypoints

**Operational Success (Within 1 Hour):**
4. **Scout deployment:** `scout_markets` creates running containers that visit waypoints
5. **Contract execution:** At least 1 contract delivered successfully
6. **Fleet utilization:** Ships move from 0% to 60%+ active time

**Strategic Success (Within AFK Session):**
7. **Revenue generation:** Credits/hour > 0 (targeting 5-15K for Phase 1)
8. **Zero navigation errors:** No KeyError or "waypoint not found" exceptions
9. **AFK viability:** Operations run autonomously for 60+ minutes without intervention

### User Experience Improvement:
- **Before:** TARS reports "cannot operate, waypoint cache empty" and enters standby
- **After:** TARS syncs waypoints automatically during initialization, then begins operations

### Performance Target:
- Sync 50 waypoints in under 30 seconds
- Enable navigation operations immediately after sync
- Zero subsequent API calls for waypoint lookups (all from cache)

## Alternatives Considered

### Alternative 1: Lazy Waypoint Discovery
**Description:** Discover waypoints as ships navigate, building cache incrementally.

**Rejected because:**
- Chicken-and-egg problem: need waypoints to navigate, need navigation to discover
- Slow cache population (hours vs seconds)
- Incomplete coverage (only discovers visited waypoints)
- Doesn't solve bootstrap problem for new systems

### Alternative 2: Manual CLI Sync Before AFK
**Description:** Admiral manually runs `python -m bot.cli waypoint sync` before leaving.

**Rejected because:**
- Defeats purpose of autonomous operations
- Error-prone (Admiral forgets to sync)
- Not scalable to multi-system operations
- TARS cannot self-heal if cache becomes empty

### Alternative 3: Embed Waypoint Data in Codebase
**Description:** Ship pre-populated waypoint data for common systems.

**Rejected because:**
- Stale data (waypoints change in SpaceTraders)
- Doesn't scale to new systems
- Maintenance burden (update data frequently)
- Violates single source of truth (SpaceTraders API is authority)

### Alternative 4: On-Demand Waypoint Fetch
**Description:** Fetch individual waypoint data when needed during operations.

**Rejected because:**
- High API call overhead (1 call per waypoint lookup)
- Slow performance (network latency on every navigation)
- Doesn't enable operations like "find nearest marketplace"
- Worse rate limiting impact than batch sync

## Implementation Guidance

### High-Level Approach

**Backend (Already Exists):**
- Command: `SyncSystemWaypointsCommand` in `bot/src/application/shipyard/commands/sync_waypoints.py`
- Handler: Fetches from API, handles pagination, stores in WaypointRepository
- This functionality is complete and tested

**Missing Pieces:**
1. **CLI Command:** Add `waypoint sync` command to CLI (similar to `waypoint list`)
2. **MCP Tool Definition:** Add tool definition to `botToolDefinitions.ts`
3. **CLI Integration:** Wire MCP tool to invoke CLI command via subprocess

**Pattern to Follow:**
- Reference: `contract_batch_workflow` (lines 290-316 in botToolDefinitions.ts)
- Pattern: MCP tool → subprocess call to CLI → CLI invokes mediator → handler executes

### Technical Considerations

**Authentication:**
- Must resolve player_id from agent symbol (if provided)
- Use default player if neither specified
- Return clear error if authentication fails

**Error Handling:**
- Catch API errors (404, 500, rate limits)
- Return human-readable error messages to Captain
- Don't crash MCP server on failures

**Idempotency:**
- Use upsert logic in WaypointRepository (update if exists, insert if new)
- Don't error if system already synced
- Allow intentional cache refresh

**Performance:**
- Display progress for multi-page fetches
- Use async/await for non-blocking operations
- Return summary immediately after completion

### Testing Strategy

**Unit Tests:**
- Test pagination logic (1 page, 3 pages, 10+ pages)
- Test empty system handling
- Test authentication failure cases
- Test API error handling

**Integration Tests:**
- Test end-to-end sync for real system
- Verify waypoints stored in database correctly
- Verify `waypoint_list` returns synced waypoints
- Verify `plan_route` works after sync

**Manual Tests:**
- Sync X1-HZ85 system for agent ENDURANCE
- Verify 40-60 waypoints in cache
- Execute `contract_batch_workflow` successfully
- Deploy `scout_markets` and verify navigation

## Recommendation

**IMPLEMENT IMMEDIATELY**

**Priority:** CRITICAL

**Justification:**
1. **Blocks all autonomous operations:** Without this tool, AFK mode is completely non-viable
2. **Proven strategy dependency:** Phase 1 strategy (strategies.md) requires navigation, which requires waypoints
3. **High ROI:** 2-4 hours implementation time unlocks ALL navigation-dependent operations
4. **Low complexity:** Backend command already exists, just needs MCP exposure
5. **Zero workarounds:** No viable alternatives for autonomous operations

**Impact Timeline:**
- **Immediate:** Unblocks navigation planning and route optimization
- **Within 1 hour:** Enables contract execution and scout deployment
- **Within AFK session:** Enables revenue generation (5-15K credits/hour Phase 1)

**Dependencies:**
- Requires database path unification fix (Priority 1 bug) to work reliably
- But can be implemented in parallel since it's independent code

**Deployment Strategy:**
1. Implement MCP tool and CLI command (2-4 hours)
2. Test with agent ENDURANCE in X1-HZ85 system
3. Verify integration with contract_batch_workflow
4. Deploy and document for TARS initialization workflow

**Success Criteria for Deployment:**
- Tool callable from TARS without errors
- Sync completes in <30 seconds for 50 waypoints
- Waypoint cache populated correctly
- Navigation operations work after sync
- Clear error messages on failures

This tool is the foundation for all autonomous navigation operations and must be implemented before AFK mode can be viable.
