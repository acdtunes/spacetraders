# TODO List

## Captain Log Integration

**Issue:** Captain log (`agents/void_hunter/docs/captain-log.md`) only records OPERATION_STARTED events but not progress, completion, or errors.

**Root Cause:** Operations (mining, scout, contract) don't integrate with captain log system. They only log to daemon logs.

**Fix Required:**
1. Modify `operations/contracts.py` to log:
   - OPERATION_COMPLETED when contract fulfilled
   - CRITICAL_ERROR if circuit breaker triggers or delivery fails
   - PERFORMANCE_SUMMARY with credits earned, time taken

2. Modify `operations/routing.py` (scout_markets_operation) to log:
   - OPERATION_COMPLETED when tour finishes
   - PERFORMANCE_SUMMARY with markets scouted, tour distance, time

3. Modify `operations/mining.py` to log:
   - OPERATION_COMPLETED when cycles complete
   - CRITICAL_ERROR if fuel emergency or permanent failures
   - PERFORMANCE_SUMMARY with profit, yield rate, cycles completed

**Implementation:** Each operation should call captain log MCP tools at key milestones:
```python
# At completion
mcp.captain_log_entry(
    agent=agent_callsign,
    entry_type='OPERATION_COMPLETED',
    operator=operator_name,
    ship=ship_symbol
)

# At errors
mcp.captain_log_entry(
    agent=agent_callsign,
    entry_type='CRITICAL_ERROR',
    operator=operator_name,
    ship=ship_symbol,
    error=error_description,
    resolution=resolution_action
)
```

**Priority:** Medium - Captain log exists but isn't providing the high-level mission tracking it's designed for.

**Files to modify:**
- `operations/contracts.py`
- `operations/routing.py` (scout_markets_operation)
- `operations/mining.py`

---

## ✅ Contract Mining Intelligence - Phase 1 (COMPLETED)

**Issue:** Contract fulfillment with `--mine-from` option didn't check if markets were selling the required resource cheaper/faster than mining.

**Fix Implemented (2025-10-05):**
- Added market data query BEFORE starting mining
- Calculates opportunity cost (mining time × 50k cr/hr) vs purchase cost
- Automatically switches to buying if cheaper/faster upfront

**Files Modified:**
- `operations/contracts.py` lines 103-150

---

## ✅ Contract Mining Intelligence - Phase 2 (COMPLETED)

**Issue:** Market data was only checked once at the start, not continuously as new markets were discovered by scout operations.

**Fix Implemented (2025-10-05):**
- Contract fulfillment now checks market_data BEFORE EACH mining extraction
- Re-evaluates buy vs mine decision with latest data every cycle
- Switches to buying mid-mining if scout discovers better prices
- Properly isolated in `operations/contracts.py` (not in general mining.py)

**Files Modified:**
- `operations/contracts.py` lines 152-241 (inline mining loop with continuous monitoring)
