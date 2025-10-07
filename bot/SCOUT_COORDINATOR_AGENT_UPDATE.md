# Scout Coordinator Agent Template Update

**Date:** 2025-10-06  
**Status:** ✅ Complete

## Summary

Updated the scout-coordinator agent template to use the new MCP tools for multi-ship market scouting coordination.

## What Changed

### 1. New Agent Template Created
**File:** `/Users/andres.camacho/Development/Personal/spacetradersV2/bot/.claude/agents/scout-coordinator.md`

**Key Updates:**
- ✅ Uses `mcp__spacetraders-bot__bot_scout_coordinator_start` instead of deprecated approaches
- ✅ Removed all references to `bot_scout_markets` (single-ship tool)
- ✅ Added clear instructions for using the scout coordinator MCP tools
- ✅ Emphasized comma-separated ships parameter format: `"SHIP-1,SHIP-2,SHIP-3"`
- ✅ Always uses `"2opt"` algorithm for best route optimization
- ✅ Includes complete workflow from ship acquisition to shutdown
- ✅ Added performance expectations table (1 ship vs 3 ships vs 5 ships)
- ✅ Comprehensive error handling section

### 2. Agent Template Structure

**Sections included:**
1. **Startup Checklist** - Verify player_id, load state, find probes
2. **Mission Statement** - Deploy continuous multi-ship market scouting
3. **How Scout Coordinator Works** - Explains geographic partitioning algorithm
4. **MCP Toolbelt** - All relevant bot_scout_coordinator_* tools
5. **Operating Procedure** - 4-phase workflow (Acquisition → Deployment → Monitoring → Shutdown)
6. **Key Parameters** - Ships format, algorithm selection, probe identification
7. **Decision Rules** - When to use coordinator vs market analyst
8. **Performance Expectations** - Table showing staleness vs ship count
9. **Example Workflow** - Complete 3-ship deployment scenario
10. **Error Handling** - Common issues and solutions
11. **Reporting Template** - Structured deployment reports
12. **Completion Checklist** - Verify before marking complete

### 3. Key Instructions Emphasized

**Critical points highlighted:**

#### Ships Parameter Format
```python
# ✅ CORRECT
ships="SHIP-1,SHIP-2,SHIP-3"  # Comma-separated string

# ❌ WRONG  
ships=["SHIP-1", "SHIP-2", "SHIP-3"]  # Arrays not supported!
```

#### Algorithm Selection
```python
algorithm="2opt"  # Always use 2opt for best optimization
```

#### MCP Tools to Use
```python
# Deploy coordinator
mcp__spacetraders-bot__bot_scout_coordinator_start(
    player_id=PLAYER_ID,
    system="X1-HU87",
    ships="SHIP-1,SHIP-2,SHIP-3",
    algorithm="2opt"
)

# Check status
mcp__spacetraders-bot__bot_scout_coordinator_status(system="X1-HU87")

# Stop coordinator
mcp__spacetraders-bot__bot_scout_coordinator_stop(system="X1-HU87")
```

#### What Coordinator Does Automatically
- ✅ Loads system graph
- ✅ Partitions markets geographically (no overlap)
- ✅ Optimizes each ship's subtour with 2-opt
- ✅ Spawns continuous scout daemons
- ✅ Monitors daemon health every 30s
- ✅ Auto-restarts failed scouts

### 4. Performance Benefits Table

| Probe Count | Markets | Tour Time | Data Freshness |
|-------------|---------|-----------|----------------|
| 1 ship      | 25      | ~25 min   | <25 min        |
| 3 ships     | 25      | ~9 min    | <10 min        |
| 5 ships     | 25      | ~5 min    | <6 min         |

### 5. Complete Example Workflow

Included detailed 6-step workflow:
1. Find probes with `bot_assignments_find(cargo_min=0)`
2. Deploy coordinator with `bot_scout_coordinator_start`
3. Register assignments for each probe
4. Report deployment metrics to Flag Captain
5. Monitor with `bot_scout_coordinator_status`
6. Shutdown with `bot_scout_coordinator_stop` and release ships

## Files Modified

### Created
- `.claude/agents/scout-coordinator.md` - New agent template with MCP tools

### Unchanged (Reference Only)
- `.claude/agents/old/scout-coordinator.md` - Old template (kept for reference)
- `SCOUT_COORDINATOR_SUMMARY.md` - Implementation summary (still accurate)
- `agents/first_mate_prompt.md` - Already has correct guidance

## Migration Notes

### For Agent Users
1. **Old approach (deprecated):**
   ```python
   # Single-ship scouting
   bot_scout_markets(ship="SHIP-1", system="X1-HU87")
   ```

2. **New approach (use this):**
   ```python
   # Multi-ship coordinated scouting
   bot_scout_coordinator_start(
       player_id=6,
       system="X1-HU87", 
       ships="SHIP-1,SHIP-2,SHIP-3",
       algorithm="2opt"
   )
   ```

### Benefits of New Approach
- ✅ **No overlap** - Geographic partitioning ensures ships don't duplicate work
- ✅ **Continuous operation** - Tours restart immediately (no gaps)
- ✅ **Fresh intelligence** - Multiple ships = faster market coverage
- ✅ **Fault tolerance** - Auto-monitors and restarts failed scouts
- ✅ **Simple API** - One call deploys entire fleet

## Testing Recommendations

1. **Test with 1 ship** - Verify basic deployment works
2. **Test with 3 ships** - Verify geographic partitioning
3. **Test status command** - Verify monitoring works
4. **Test stop command** - Verify graceful shutdown
5. **Test ship assignments** - Verify registry integration

## Next Steps

1. ✅ Agent template updated with new MCP tools
2. ⏳ Test scout-coordinator agent with real deployment
3. ⏳ Update MCP_SERVER_README.md with coordinator examples (optional)
4. ⏳ Monitor production usage and gather feedback

## Summary

The scout-coordinator agent is now fully updated to use the new `bot_scout_coordinator_*` MCP tools. The template provides clear, step-by-step guidance for:
- Finding and assigning probe ships
- Deploying the coordinator with proper parameters
- Monitoring multi-ship scouting operations
- Graceful shutdown and ship release

All references to deprecated single-ship scouting have been removed. The agent now leverages the geographic partitioning and continuous operation features of the Scout Coordinator library.
