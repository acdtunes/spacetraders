# Feature Proposal: Database Path Unification

**Date:** 2025-11-06
**Priority:** CRITICAL
**Category:** BUG_FIX
**Status:** PROPOSED

## Problem Statement

MCP server and bot CLI use different database paths due to working directory inconsistency, causing all authenticated operations to fail with "Player not found in database" despite player being successfully registered.

## Current Behavior

**How it works now:**
1. MCP server runs from project root: `/Users/.../spacetraders/`
2. MCP spawns Python CLI with `cwd: /Users/.../spacetraders/bot/`
3. Database path configured as relative: `var/spacetraders.db`
4. Path resolves differently based on working directory:
   - MCP context → `/spacetraders/var/spacetraders.db`
   - CLI context → `/spacetraders/bot/var/spacetraders.db`
5. Player registration via MCP writes to `/spacetraders/var/`
6. CLI commands read from `/spacetraders/bot/var/`
7. Result: Two separate databases with different data

**Evidence:**
```bash
# MCP player_list shows player exists
Registered players (1): [1] ENDURANCE

# Direct query of MCP database
sqlite3 /spacetraders/var/spacetraders.db "SELECT * FROM players;"
# Returns: Empty (0 rows)

# CLI command fails
python -m adapters.primary.cli.main contract batch --ship ENDURANCE-1
# Error: Player 1 not found in database
```

**Metrics showing the problem:**
- Authentication success rate: 0%
- Operations blocked: 100% (all authenticated operations)
- Revenue generation: 0 credits/hour

## Impact

- **Credits/Hour Impact:** Blocks ALL revenue generation (contracts, trading, mining)
- **Complexity:** HIGH (requires coordination between MCP server and bot CLI)
- **Dependencies:** Affects all operations requiring API authentication

## Proposed Solution

### Required Behavior

The system must ensure both MCP server and bot CLI use the SAME database file at all times.

### User Stories

**As Captain TARS,**
- I need player registration to persist across MCP and CLI tools
- So I can execute authenticated operations without "player not found" errors
- Expected: After registering player via MCP, CLI commands can authenticate using same player

**As a developer,**
- I need a single source of truth for database location
- So I can avoid database synchronization bugs
- Expected: One configuration point that both MCP and CLI respect

**As an operator,**
- I need clear error messages when database is unreachable
- So I can debug configuration issues quickly
- Expected: "Cannot find database at /path/to/db" instead of "Player not found"

### Acceptance Criteria

**Must:**
1. Both MCP server and bot CLI MUST use identical absolute path to database
2. Path MUST be configurable via environment variable
3. If environment variable not set, MUST use sensible default (project root `/var/`)
4. If database file doesn't exist at path, MUST create it automatically
5. Player registered via MCP `player_register` MUST be queryable via CLI `player info`
6. CLI commands requiring authentication MUST succeed after MCP player registration

**Should:**
7. Configuration SHOULD be validated at startup (check database path is writable)
8. Error messages SHOULD include actual database path used for debugging
9. Documentation SHOULD explain how to set database path

**Edge Cases:**
- **Multiple projects:** Different projects must not share database unless explicitly configured
  - Expected: Environment variable scoped to project, or config file in project root
- **Database migration:** Existing databases at old paths should be migrated
  - Expected: Migration script or clear instructions for manual copy
- **Permission denied:** Database path not writable
  - Expected: Clear error message at startup, not silent failure during operations

## Evidence

### Metrics Supporting This Proposal

**Current State:**
- Authentication success: 0/100 attempts (0%)
- Operations blocked: All contracts, trading, mining, navigation
- Revenue: 0 credits/hour

**Projected After Fix:**
- Authentication success: 100% (assuming player registered)
- Operations unblocked: All authenticated operations
- Revenue: 10-20K credits/hour (from contracts per strategies.md)

### Proven Strategy Reference

From strategies.md, lines 30-37:
> **Step 3 - Contract Operations:**
> - Start contract fulfillment with command ship immediately
> - Use contract_batch_workflow MCP tool
> - Contracts provide TWO revenue touchpoints
> - Target: Complete initial batch of 5-10 contracts

**Analysis:** Contract operations are Phase 1 strategy but completely blocked by database authentication issue. This is the #1 blocker for proven revenue generation strategy.

## Risks & Tradeoffs

### Risk 1: Breaking Existing Configurations
**Concern:** Users with working setups may break if we change default paths.

**Acceptable because:** Current setup is already broken (0% success rate). Any existing working configurations are using undocumented workarounds that should be formalized.

**Mitigation:**
- Provide migration script to copy database to canonical location
- Check both old and new paths at startup, warn if database exists at old path
- Document migration in release notes

### Risk 2: Environment Variable Not Set
**Concern:** If environment variable required but not set, operations fail silently.

**Acceptable because:** We provide sensible default (project root `/var/spacetraders.db`) that works in most cases.

**Mitigation:**
- Default path works for both MCP and CLI when launched from project root
- Validate database path at startup and fail fast with clear error
- Add health check command: `python -m cli.main system health`

### Risk 3: Multiple Databases in Development
**Concern:** Developers may accidentally create multiple databases during testing.

**Acceptable because:** Development confusion is preferable to production failures.

**Mitigation:**
- Add `system list-databases` command to find all database files
- Add warning in logs when database path differs from default
- Document recommended project structure in README

## Success Metrics

**How we'll know this worked:**

1. **Authentication success rate:** 0% → 100%
   - Measure: Player registered via MCP can authenticate in CLI commands

2. **Operations unblocked:** 0 → All authenticated operations working
   - Measure: contract_batch_workflow returns >0 negotiations

3. **Error clarity:** Silent failures → Clear error messages
   - Measure: Database path errors show actual path in message

4. **Revenue generation:** 0 → 10K+ credits/hour
   - Measure: Contracts completing successfully, credits increasing

## Alternatives Considered

### Alternative 1: Symlink
**Description:** Create symlink from `/bot/var/` to `/var/`

**Pros:**
- Simple, no code changes
- Works immediately

**Cons:**
- Fragile - breaks if directories move
- Not cross-platform (symlinks behave differently on Windows)
- Doesn't solve root cause
- Hidden magic that confuses developers

**Rejected because:** Not maintainable, platform-dependent, doesn't address configuration clarity.

### Alternative 2: Shared Configuration File
**Description:** Store database path in `config/database.json`, both MCP and CLI read from it

**Pros:**
- Explicit configuration
- Easy to version control
- Single source of truth

**Cons:**
- Requires parsing JSON in both TypeScript and Python
- Another file to maintain
- Path still relative unless carefully constructed

**Rejected because:** Environment variable is simpler and more standard for deployment configuration.

### Alternative 3: Database Path as CLI Argument
**Description:** Pass `--db-path` to every CLI command

**Pros:**
- Explicit per-command
- Maximum flexibility

**Cons:**
- Verbose, error-prone
- MCP server would need to pass it to every CLI spawn
- Easy to forget, leading to inconsistent paths

**Rejected because:** Too cumbersome, increases chance of user error.

## Recommendation

**IMPLEMENT IMMEDIATELY**

**Why:**
1. **CRITICAL blocker:** 100% of authenticated operations fail without this
2. **High impact:** Unblocks contracts (10-20K credits/hour), trading, mining, navigation
3. **Clear solution:** Environment variable is standard practice for deployment config
4. **Low risk:** Current state is already broken, can only improve
5. **Proven strategy:** Enables Phase 1 contract operations per strategies.md

**Priority:** CRITICAL - Nothing else matters until this is fixed

**Implementation Order:**
1. Add environment variable support to database.py (Python)
2. Update MCP server to set environment variable when spawning CLI (TypeScript)
3. Add database path validation at startup
4. Test player registration and CLI authentication end-to-end
5. Document configuration in README

**Estimated Effort:** 2-3 hours development + 1 hour testing

**Testing Checklist:**
- [ ] Register player via MCP player_register
- [ ] Query player via MCP player_info → Shows player data
- [ ] Execute CLI command requiring auth: `contract batch --ship X`
- [ ] Verify both read from same database (add log messages showing path)
- [ ] Test with environment variable set to custom path
- [ ] Test with environment variable unset (uses default)
- [ ] Verify database created automatically if doesn't exist
