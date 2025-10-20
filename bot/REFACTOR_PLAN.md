# Comprehensive Refactoring Plan - Module Naming & Structure

**Created:** 2025-10-20
**Goal:** Establish consistent naming conventions, eliminate name collisions, and properly organize code by domain
**Impact:** ~25-30 files affected
**Estimated Time:** 2-3 hours
**Risk Level:** MEDIUM (comprehensive but well-defined changes)

---

## Executive Summary

### Problems Being Solved

1. **Name Collisions** - `core/routing.py` vs `operations/routing.py` causes import ambiguity
2. **Confusing Names** - `scout_coordinator.py` vs `scout_coordination.py` - nobody knows which is which
3. **Grab-Bag Anti-Pattern** - `analysis.py` contains unrelated utilities that belong in domain modules
4. **Inconsistent Patterns** - No clear standard for naming core vs operations modules

### Proposed Solution

**3 Critical Renames + 1 Deletion with Redistribution:**

| Current | Action | New Location |
|---------|--------|--------------|
| `core/routing.py` | Rename | `core/route_planning.py` |
| `operations/scout_coordination.py` | Rename | `operations/scout_ops.py` |
| `operations/analysis.py` | **DELETE** | Redistribute to domain modules |
| - `find-fuel` command | Move | `operations/navigation.py::find_fuel_operation()` |
| - `distance` command | Move | `operations/navigation.py::calculate_distance_operation()` |
| - `find-mining` command | Move | `operations/mining.py::find_mining_opportunities_operation()` |

---

## Part 1: Naming Convention Standard

### Core Layer Naming Rules

**Purpose:** Core layer contains reusable library components

| Pattern | Example | When to Use |
|---------|---------|-------------|
| `{entity}_manager.py` | `assignment_manager.py` | Manages lifecycle of entities |
| `{entity}_coordinator.py` | `scout_coordinator.py` | Coordinates multiple entities |
| `{entity}_controller.py` | `ship_controller.py` | Controls entity state machines |
| `{entity}_validator.py` | `routing_validator.py` | Validates entity operations |
| `{entity}_provider.py` | `system_graph_provider.py` | Provides data or services |
| `{entity}_router.py` | `ortools_router.py` | Routes or optimizes paths |
| `{entity}_optimizer.py` | `ortools_mining_optimizer.py` | Optimizes assignments |
| `{domain}.py` | `database.py`, `utils.py` | Pure library code |
| `{domain}_config.py` | `routing_config.py` | Configuration management |
| `{domain}_data.py` | `market_data.py` | Data access layer |

### Operations Layer Naming Rules

**Purpose:** Operations layer contains CLI command implementations

| Pattern | Example | When to Use |
|---------|---------|-------------|
| `{domain}.py` | `contracts.py`, `fleet.py`, `mining.py` | Domain-specific operations |
| `{domain}s.py` | `assignments.py` | Managing multiple entities (plural) |
| `{domain}_ops.py` | `scout_ops.py` | When core has similar name (avoid collision) |
| `validate_{entity}.py` | `validate_routing.py` | Validation operations |
| `{specific}_trader.py` | `multileg_trader.py` | Specialized trading operations |
| `common.py` | `common.py` | Shared operation utilities |

**Anti-Patterns to Avoid:**
- ❌ `analysis.py` - Too vague, becomes grab-bag
- ❌ `utilities.py` - Same problem, attracts unrelated code
- ❌ Same name in core and operations without `_ops` suffix

---

## Part 2: Current State Analysis

### Core Modules (19 files)

```
✅ WELL-NAMED:
  - api_client.py
  - assignment_manager.py
  - daemon_manager.py
  - database.py
  - market_data.py
  - operation_controller.py
  - ortools_router.py
  - ortools_mining_optimizer.py
  - routing_config.py
  - routing_pause.py
  - routing_validator.py
  - scout_coordinator.py
  - ship_controller.py
  - smart_navigator.py
  - system_graph_provider.py
  - utils.py

❌ NEEDS RENAME:
  - routing.py → route_planning.py (collision with operations/routing.py)

❓ POTENTIALLY DEAD:
  - routing_legacy.py (check if used, delete if not)
  - market_partitioning.py (16.4% coverage, check if internal-only)
```

### Operations Modules (17 files)

```
✅ WELL-NAMED:
  - assignments.py (wraps assignment_manager)
  - captain_logging.py (domain-specific)
  - common.py (shared utilities)
  - contracts.py (domain-specific)
  - control.py (CircuitBreaker utility)
  - daemon.py (wraps daemon_manager)
  - fleet.py (domain-specific)
  - mining.py (domain-specific)
  - multileg_trader.py (specialized)
  - purchasing.py (domain-specific)
  - routing.py (domain-specific, will be OK after core rename)
  - validate_routing.py (validation operation)
  - waypoint_query.py (domain-specific)

❌ NEEDS RENAME:
  - scout_coordination.py → scout_ops.py (confusing with core/scout_coordinator.py)

❌ NEEDS DELETION + REDISTRIBUTION:
  - analysis.py → DELETE (redistribute to navigation.py and mining.py)

❓ POTENTIALLY REDUNDANT:
  - navigation.py (53 LOC, check if just wraps smart_navigator)
  - mining_optimizer.py (6.1% coverage, vs ortools_mining_optimizer.py 17.0%)
```

---

## Part 3: Detailed Changes

### Change 1: Rename core/routing.py → core/route_planning.py

**Rationale:**
- Eliminates name collision with operations/routing.py
- More descriptive: contains GraphBuilder, TourOptimizer, RouteOptimizer
- "route_planning" clearly indicates it's about planning routes, not executing them

**Files to Update:**

```python
# 1. Rename file
git mv src/spacetraders_bot/core/routing.py src/spacetraders_bot/core/route_planning.py

# 2. Update imports in these files:
src/spacetraders_bot/core/__init__.py
src/spacetraders_bot/core/smart_navigator.py
src/spacetraders_bot/core/ortools_mining_optimizer.py
src/spacetraders_bot/operations/routing.py
src/spacetraders_bot/operations/mining_optimizer.py
# Possibly others - search codebase for "from.*routing import"
```

**Import Changes:**
```python
# OLD:
from .routing import GraphBuilder, TourOptimizer, RouteOptimizer
from spacetraders_bot.core.routing import TourOptimizer

# NEW:
from .route_planning import GraphBuilder, TourOptimizer, RouteOptimizer
from spacetraders_bot.core.route_planning import TourOptimizer
```

**Risk:** MEDIUM - Core module rename affects multiple files
**Mitigation:** Search codebase thoroughly, run all tests after change

---

### Change 2: Rename operations/scout_coordination.py → operations/scout_ops.py

**Rationale:**
- Eliminates confusion with core/scout_coordinator.py
- "_ops" suffix clearly indicates operations layer
- "coordinator" stays in core where the ScoutCoordinator class lives
- Matches pattern: manager → ops (daemon_manager → daemon, but could be daemon_ops)

**Files to Update:**

```python
# 1. Rename file
git mv src/spacetraders_bot/operations/scout_coordination.py src/spacetraders_bot/operations/scout_ops.py

# 2. Update imports in these files:
src/spacetraders_bot/operations/__init__.py
src/spacetraders_bot/cli/main.py (if directly imported, usually not)
```

**Import Changes:**
```python
# OLD (in operations/__init__.py):
from .scout_coordination import (
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_start_operation,
    coordinator_status_operation,
    coordinator_stop_operation,
)

# NEW:
from .scout_ops import (
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_start_operation,
    coordinator_status_operation,
    coordinator_stop_operation,
)
```

**Risk:** LOW - Operations module rename, few dependencies
**Mitigation:** Update __init__.py exports, verify CLI still works

---

### Change 3: Delete analysis.py and Redistribute Functions

**Rationale:**
- "analysis.py" is a misnomer - contains unrelated utilities
- Violates single responsibility principle
- 483 lines, 1.5% coverage indicates it's rarely used
- Breaking into domain-specific modules improves discoverability and testability

#### 3a. Move find-fuel to operations/navigation.py

**Function:** `utilities_operation(args)` when `args.util_type == 'find-fuel'`
**Lines:** 17-69 in analysis.py (~53 lines)
**New Name:** `find_fuel_operation(args)`

**Implementation:**

```python
# operations/navigation.py

def find_fuel_operation(args):
    """
    Find nearest fuel station to a ship.

    CLI Command: util find-fuel --ship SHIP

    Args:
        args: Namespace with:
            - player_id: Player ID
            - ship: Ship symbol
            - log_level: Logging level (optional)

    Returns:
        int: 0 on success, 1 on failure
    """
    ship_name = args.ship
    log_file = setup_logging(f"find-fuel", ship_name, getattr(args, 'log_level', 'INFO'))

    api = get_api_client(args.player_id)

    print("=" * 70)
    print("FIND NEAREST FUEL STATION")
    print("=" * 70)

    # Get ship location
    ship = api.get_ship(args.ship)
    if not ship:
        print("❌ Failed to get ship status")
        return 1

    current_location = ship['nav']['waypointSymbol']
    system, _ = parse_waypoint_symbol(current_location)

    # Get current coordinates
    current_wp = api.get_waypoint(system, current_location)
    if not current_wp:
        print("❌ Failed to get waypoint data")
        return 1

    current_coords = {'x': current_wp['x'], 'y': current_wp['y']}

    print(f"Current location: {current_location} ({current_coords['x']}, {current_coords['y']})\n")

    # Get all marketplaces
    result = api.list_waypoints(system, limit=20, traits="MARKETPLACE")
    marketplaces = result.get('data', []) if result else []

    if not marketplaces:
        print("❌ No marketplaces found")
        return 1

    # Calculate distances
    fuel_stations = []
    for wp in marketplaces:
        wp_coords = {'x': wp['x'], 'y': wp['y']}
        distance = calculate_distance(current_coords, wp_coords)
        fuel_stations.append({
            'symbol': wp['symbol'],
            'type': wp['type'],
            'distance': distance
        })

    # Sort by distance
    fuel_stations.sort(key=lambda x: x['distance'])

    print("Nearest fuel stations:")
    print("-" * 70)
    for i, station in enumerate(fuel_stations[:10], 1):
        print(f"{i}. {station['symbol']:20} ({station['type']:20}) - {station['distance']:.0f} units")

    print("\n" + "=" * 70)
    return 0
```

**CLI Update Required:**
```python
# cli/main.py

# OLD:
elif args.operation == 'util':
    if args.util_type == 'find-fuel':
        return utilities_operation(args)

# NEW:
elif args.operation == 'find-fuel':
    from operations.navigation import find_fuel_operation
    return find_fuel_operation(args)

# Add new parser:
find_fuel_parser = subparsers.add_parser('find-fuel', help='Find nearest fuel station')
find_fuel_parser.add_argument('--player-id', type=int, required=True)
find_fuel_parser.add_argument('--ship', required=True)
```

---

#### 3b. Move distance to operations/navigation.py

**Function:** `utilities_operation(args)` when `args.util_type == 'distance'`
**Lines:** 71-98 in analysis.py (~28 lines)
**New Name:** `calculate_distance_operation(args)`

**Implementation:**

```python
# operations/navigation.py

def calculate_distance_operation(args):
    """
    Calculate distance between two waypoints.

    CLI Command: distance --waypoint1 W1 --waypoint2 W2

    Args:
        args: Namespace with:
            - player_id: Player ID
            - waypoint1: First waypoint symbol
            - waypoint2: Second waypoint symbol
            - log_level: Logging level (optional)

    Returns:
        int: 0 on success, 1 on failure
    """
    log_file = setup_logging("distance", "system", getattr(args, 'log_level', 'INFO'))

    api = get_api_client(args.player_id)

    print("=" * 70)
    print("CALCULATE DISTANCE")
    print("=" * 70)

    system1, wp1 = parse_waypoint_symbol(args.waypoint1)
    system2, wp2 = parse_waypoint_symbol(args.waypoint2)

    wp1_data = api.get_waypoint(system1, wp1)
    wp2_data = api.get_waypoint(system2, wp2)

    if not wp1_data or not wp2_data:
        print("❌ Failed to get waypoint data")
        return 1

    coords1 = {'x': wp1_data['x'], 'y': wp1_data['y']}
    coords2 = {'x': wp2_data['x'], 'y': wp2_data['y']}

    distance = calculate_distance(coords1, coords2)

    print(f"{args.waypoint1}: ({coords1['x']}, {coords1['y']})")
    print(f"{args.waypoint2}: ({coords2['x']}, {coords2['y']})")
    print(f"\nDistance: {distance:.1f} units")
    print(f"Fuel needed (CRUISE): ~{estimate_fuel_cost(distance, 'CRUISE')} units")
    print(f"Fuel needed (DRIFT): ~{estimate_fuel_cost(distance, 'DRIFT')} units")

    print("\n" + "=" * 70)
    return 0
```

**CLI Update Required:**
```python
# cli/main.py

# NEW parser:
distance_parser = subparsers.add_parser('distance', help='Calculate distance between waypoints')
distance_parser.add_argument('--player-id', type=int, required=True)
distance_parser.add_argument('--waypoint1', required=True, help='First waypoint symbol')
distance_parser.add_argument('--waypoint2', required=True, help='Second waypoint symbol')
```

---

#### 3c. Move find-mining to operations/mining.py

**Function:** `utilities_operation(args)` when `args.util_type == 'find-mining'`
**Lines:** 100-483 in analysis.py (~384 lines!)
**New Name:** `find_mining_opportunities_operation(args)`

**Implementation:**

```python
# operations/mining.py

def find_mining_opportunities_operation(args):
    """
    Find profitable mining asteroids in a system.

    Analyzes asteroids based on:
    - Mining traits (deposits, composition)
    - Market prices for materials
    - Travel distance and fuel costs
    - Expected profit per hour

    CLI Command: find-mining --system X1-HU87 [--ship SHIP]

    Args:
        args: Namespace with:
            - player_id: Player ID
            - system: System symbol
            - ship: Ship symbol (optional, for fuel/cargo calculations)
            - log_level: Logging level (optional)

    Returns:
        int: 0 on success, 1 on failure
    """
    log_file = setup_logging("find-mining", args.system, getattr(args, 'log_level', 'INFO'))

    api = get_api_client(args.player_id)

    print("=" * 70)
    print("FIND MINING OPPORTUNITIES")
    print("=" * 70)

    system = args.system

    # Get ship specs if provided
    ship_specs = None
    if hasattr(args, 'ship') and args.ship:
        ship_data = api.get_ship(args.ship)
        if ship_data:
            ship_specs = {
                'symbol': args.ship,
                'speed': ship_data['engine']['speed'],
                'fuel_capacity': ship_data['fuel']['capacity'],
                'cargo_capacity': ship_data['cargo']['capacity']
            }
            print(f"Ship: {args.ship}")
            print(f"  Speed: {ship_specs['speed']}")
            print(f"  Fuel: {ship_specs['fuel_capacity']}")
            print(f"  Cargo: {ship_specs['cargo_capacity']}\n")

    if not ship_specs:
        # Default to mining drone specs
        ship_specs = {
            'symbol': 'DEFAULT_MINING_DRONE',
            'speed': 9,
            'fuel_capacity': 80,
            'cargo_capacity': 15
        }
        print(f"Using default mining drone specs (speed: 9, fuel: 80, cargo: 15)\n")

    print(f"System: {system}\n")

    # [... rest of 384 lines implementing mining opportunity finder ...]
    # (Full implementation omitted for brevity - copy lines 100-483 from analysis.py)

    return 0
```

**CLI Update Required:**
```python
# cli/main.py

# NEW parser:
find_mining_parser = subparsers.add_parser('find-mining', help='Find mining opportunities')
find_mining_parser.add_argument('--player-id', type=int, required=True)
find_mining_parser.add_argument('--system', required=True, help='System symbol')
find_mining_parser.add_argument('--ship', help='Ship symbol (optional)')
```

---

#### 3d. Delete analysis.py

After redistributing all three functions:

```bash
# Delete the file
git rm src/spacetraders_bot/operations/analysis.py

# Remove from operations/__init__.py
# OLD:
from .analysis import utilities_operation

# NEW: (nothing - functions now in navigation.py and mining.py)
```

---

## Part 4: Implementation Checklist

### Phase 1: Preparation (5 minutes)

- [ ] Create feature branch: `git checkout -b refactor/consistent-naming`
- [ ] Ensure working directory is clean: `git status`
- [ ] Run full test suite to establish baseline: `pytest tests/`
- [ ] Document current test results

### Phase 2: Rename core/routing.py (20 minutes)

- [ ] Search for all imports: `grep -r "from.*routing import" src/`
- [ ] Document all files that import from core.routing
- [ ] Rename file: `git mv src/spacetraders_bot/core/routing.py src/spacetraders_bot/core/route_planning.py`
- [ ] Update `core/__init__.py` exports
- [ ] Update imports in:
  - [ ] `core/smart_navigator.py`
  - [ ] `core/ortools_mining_optimizer.py`
  - [ ] `operations/routing.py`
  - [ ] `operations/mining_optimizer.py`
  - [ ] Any other files found in search
- [ ] Run tests: `pytest tests/ -v`
- [ ] Fix any test failures
- [ ] Commit: `git commit -m "refactor(core): rename routing.py → route_planning.py"`

### Phase 3: Rename operations/scout_coordination.py (10 minutes)

- [ ] Rename file: `git mv src/spacetraders_bot/operations/scout_coordination.py src/spacetraders_bot/operations/scout_ops.py`
- [ ] Update `operations/__init__.py` imports
- [ ] Update any CLI imports (if direct)
- [ ] Run tests: `pytest tests/ -v`
- [ ] Commit: `git commit -m "refactor(ops): rename scout_coordination.py → scout_ops.py"`

### Phase 4: Redistribute analysis.py - Part A (30 minutes)

**Move find-fuel and distance to navigation.py:**

- [ ] Open `operations/navigation.py`
- [ ] Add `find_fuel_operation()` function (copy from analysis.py lines 17-69)
- [ ] Add `calculate_distance_operation()` function (copy from analysis.py lines 71-98)
- [ ] Add required imports to navigation.py:
  - [ ] `from spacetraders_bot.core.utils import calculate_distance, estimate_fuel_cost, parse_waypoint_symbol`
  - [ ] `from spacetraders_bot.operations.common import get_api_client, setup_logging`
- [ ] Update `operations/__init__.py`:
  - [ ] Add exports for `find_fuel_operation`, `calculate_distance_operation`
- [ ] Update `cli/main.py`:
  - [ ] Remove `util` command with find-fuel and distance sub-commands
  - [ ] Add `find-fuel` command parser
  - [ ] Add `distance` command parser
  - [ ] Add command routing for both
- [ ] Test manually:
  - [ ] `python spacetraders_bot.py find-fuel --player-id 6 --ship SHIP-1`
  - [ ] `python spacetraders_bot.py distance --player-id 6 --waypoint1 W1 --waypoint2 W2`
- [ ] Commit: `git commit -m "refactor(nav): move find-fuel and distance from analysis.py"`

### Phase 5: Redistribute analysis.py - Part B (30 minutes)

**Move find-mining to mining.py:**

- [ ] Open `operations/mining.py`
- [ ] Add `find_mining_opportunities_operation()` function (copy from analysis.py lines 100-483)
- [ ] Ensure all required imports are present
- [ ] Update `operations/__init__.py`:
  - [ ] Add export for `find_mining_opportunities_operation`
- [ ] Update `cli/main.py`:
  - [ ] Remove find-mining from util command
  - [ ] Add `find-mining` command parser
  - [ ] Add command routing
- [ ] Test manually:
  - [ ] `python spacetraders_bot.py find-mining --player-id 6 --system X1-HU87`
- [ ] Commit: `git commit -m "refactor(mining): move find-mining from analysis.py"`

### Phase 6: Delete analysis.py (5 minutes)

- [ ] Verify all three functions have been moved
- [ ] Delete file: `git rm src/spacetraders_bot/operations/analysis.py`
- [ ] Remove from `operations/__init__.py`:
  - [ ] Remove `from .analysis import utilities_operation`
  - [ ] Remove `utilities_operation` from `__all__`
- [ ] Run tests: `pytest tests/ -v`
- [ ] Commit: `git commit -m "refactor(ops): delete analysis.py (functions redistributed)"`

### Phase 7: Update Documentation (15 minutes)

- [ ] Update `ARCHITECTURE.md`:
  - [ ] Update module names in diagrams
  - [ ] Update dependency graphs
  - [ ] Update module lists
- [ ] Update `README.md` if modules are mentioned
- [ ] Update `MODULE_USAGE_ANALYSIS.md` with new names
- [ ] Create `NAMING_CONVENTIONS.md` documenting standards
- [ ] Commit: `git commit -m "docs: update for module renames"`

### Phase 8: Final Verification (10 minutes)

- [ ] Run full test suite: `pytest tests/ -v --cov`
- [ ] Check coverage hasn't decreased
- [ ] Test all renamed CLI commands manually:
  - [ ] Scout coordinator commands still work
  - [ ] find-fuel command works
  - [ ] distance command works
  - [ ] find-mining command works
- [ ] Check for any missed imports: `grep -r "scout_coordination\|from.*routing import" src/`
- [ ] Review all commits
- [ ] Squash if needed: `git rebase -i HEAD~7`

### Phase 9: Merge (5 minutes)

- [ ] Push branch: `git push origin refactor/consistent-naming`
- [ ] Create PR with detailed description
- [ ] Wait for CI/tests
- [ ] Merge to main

---

## Part 5: Risk Assessment & Mitigation

### High Risk Changes

**1. Rename core/routing.py → core/route_planning.py**

**Risk:** Core module used by many files, breaking changes possible
**Impact:** Import errors, test failures
**Probability:** MEDIUM
**Mitigation:**
- Comprehensive search for all imports before making change
- Update all imports atomically in single commit
- Run full test suite immediately after
- Have rollback plan (git revert)

**2. Redistribute analysis.py (384 lines to mining.py)**

**Risk:** Large function move, potential for copy errors
**Impact:** Mining functionality broken
**Probability:** LOW (copy-paste, not rewrite)
**Mitigation:**
- Copy function exactly as-is (no modifications)
- Test manually before committing
- Keep analysis.py until all functions verified working
- Can revert individual commits if needed

### Medium Risk Changes

**3. CLI command changes (util → separate commands)**

**Risk:** Users have scripts calling old commands
**Impact:** Broken user scripts
**Probability:** MEDIUM
**Mitigation:**
- Document breaking changes clearly
- Consider keeping `util` command as deprecated alias temporarily
- Add migration notes to CHANGELOG

### Low Risk Changes

**4. Rename operations/scout_coordination.py → operations/scout_ops.py**

**Risk:** Operations layer, fewer dependencies
**Impact:** Import errors in __init__.py
**Probability:** LOW
**Mitigation:**
- Simple rename with clear import updates
- Easy to verify and rollback

---

## Part 6: Testing Strategy

### Automated Tests

**Before Refactor:**
```bash
pytest tests/ -v --cov=src --cov-report=html
# Document coverage baseline
```

**After Each Phase:**
```bash
pytest tests/ -v
# Ensure no regressions
```

**After Complete Refactor:**
```bash
pytest tests/ -v --cov=src --cov-report=html
# Verify coverage maintained or improved
```

### Manual Testing

**Navigation Commands:**
```bash
# Test find-fuel
python spacetraders_bot.py find-fuel --player-id 6 --ship SHIP-1

# Test distance
python spacetraders_bot.py distance --player-id 6 \
  --waypoint1 X1-HU87-A1 --waypoint2 X1-HU87-B9
```

**Mining Commands:**
```bash
# Test find-mining
python spacetraders_bot.py find-mining --player-id 6 --system X1-HU87

# Test find-mining with ship
python spacetraders_bot.py find-mining --player-id 6 \
  --system X1-HU87 --ship SHIP-1
```

**Scout Coordinator Commands:**
```bash
# Verify coordinator commands still work
python spacetraders_bot.py scout-coordinator start --player-id 6 \
  --system X1-HU87 --ships SHIP-1,SHIP-2

python spacetraders_bot.py scout-coordinator status --player-id 6
```

### Integration Tests

- [ ] Run mining operation end-to-end
- [ ] Run scouting operation end-to-end
- [ ] Verify all CLI commands in help text
- [ ] Check MCP server still works (if applicable)

---

## Part 7: Rollback Plan

If something goes wrong at any phase:

### Immediate Rollback (During Refactor)

```bash
# If in middle of uncommitted changes:
git restore .
git clean -fd

# If committed but not pushed:
git reset --hard HEAD~N  # N = number of commits to undo

# If pushed but not merged:
git revert <commit-hash>
```

### Post-Merge Rollback

```bash
# Revert merge commit:
git revert -m 1 <merge-commit-hash>

# Or create fix-forward commit:
# Manually undo changes and commit fix
```

---

## Part 8: Success Criteria

### Must Have (Blocking)

- [ ] All tests passing (no regressions)
- [ ] No name collisions (routing.py issue resolved)
- [ ] No confusing names (scout_coordination vs scout_coordinator resolved)
- [ ] analysis.py deleted, functions work in new locations
- [ ] All CLI commands functional
- [ ] Documentation updated

### Nice to Have (Non-blocking)

- [ ] Test coverage maintained or improved
- [ ] Code organization improved (domain-aligned)
- [ ] Naming conventions documented
- [ ] CONTRIBUTING.md created

---

## Part 9: Post-Refactor Tasks

### Immediate (Same Session)

- [ ] Update CHANGELOG.md with breaking changes
- [ ] Tag release if merging to main
- [ ] Notify team of CLI command changes
- [ ] Update any deployment scripts

### Follow-Up (Next Session)

- [ ] Add BDD tests for redistributed functions
- [ ] Check routing_legacy.py for deletion
- [ ] Evaluate mining_optimizer.py vs ortools_mining_optimizer.py
- [ ] Consider adding navigation.feature tests
- [ ] Review other potential naming improvements

---

## Part 10: Communication Plan

### Before Starting

**Team Notification:**
```
🚨 Breaking Changes Incoming: Module Refactor

We're renaming several modules for consistency:
- core/routing.py → core/route_planning.py
- operations/scout_coordination.py → operations/scout_ops.py
- operations/analysis.py → DELETED (split into navigation.py and mining.py)

CLI command changes:
- util find-fuel → find-fuel
- util distance → distance
- util find-mining → find-mining

Timeline: Today (2025-10-20), ~2-3 hours
Impact: Medium (import changes, CLI commands)
```

### During Refactor

- Post progress updates in team chat
- Note any unexpected issues
- Request review if uncertain

### After Completion

**Summary Post:**
```
✅ Module Refactor Complete

Changes:
✓ Fixed routing.py name collision
✓ Fixed scout coordinator naming confusion
✓ Eliminated analysis.py grab-bag
✓ Established naming conventions

Migration guide: See REFACTOR_PLAN.md
All tests passing: [link to CI]
```

---

## Appendix A: Files Affected (Estimated)

### Core Layer (4 files)
- `src/spacetraders_bot/core/routing.py` → RENAMED
- `src/spacetraders_bot/core/__init__.py` → UPDATED
- `src/spacetraders_bot/core/smart_navigator.py` → UPDATED
- `src/spacetraders_bot/core/ortools_mining_optimizer.py` → UPDATED

### Operations Layer (6 files)
- `src/spacetraders_bot/operations/scout_coordination.py` → RENAMED
- `src/spacetraders_bot/operations/analysis.py` → DELETED
- `src/spacetraders_bot/operations/navigation.py` → UPDATED (add 2 functions)
- `src/spacetraders_bot/operations/mining.py` → UPDATED (add 1 function)
- `src/spacetraders_bot/operations/__init__.py` → UPDATED
- `src/spacetraders_bot/operations/routing.py` → UPDATED (imports)

### CLI Layer (1 file)
- `src/spacetraders_bot/cli/main.py` → UPDATED (command routing)

### Documentation (3 files)
- `ARCHITECTURE.md` → UPDATED
- `MODULE_USAGE_ANALYSIS.md` → UPDATED
- `NAMING_CONVENTIONS.md` → CREATED

### Tests (TBD)
- Any tests importing renamed modules → UPDATED

**Total: ~15-20 files affected**

---

## Appendix B: Import Update Examples

### Example 1: core/smart_navigator.py

```python
# BEFORE:
from .routing import TourOptimizer
from .ortools_router import ORToolsRouter

# AFTER:
from .route_planning import TourOptimizer
from .ortools_router import ORToolsRouter
```

### Example 2: operations/routing.py

```python
# BEFORE:
from spacetraders_bot.core.routing import GraphBuilder, TourOptimizer, RouteOptimizer

# AFTER:
from spacetraders_bot.core.route_planning import GraphBuilder, TourOptimizer, RouteOptimizer
```

### Example 3: operations/__init__.py

```python
# BEFORE:
from .analysis import utilities_operation
from .scout_coordination import (
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_start_operation,
    coordinator_status_operation,
    coordinator_stop_operation,
)

# AFTER:
from .navigation import (
    navigate_operation,
    find_fuel_operation,
    calculate_distance_operation,
)
from .mining import (
    mining_operation,
    find_mining_opportunities_operation,
)
from .scout_ops import (
    coordinator_add_ship_operation,
    coordinator_remove_ship_operation,
    coordinator_start_operation,
    coordinator_status_operation,
    coordinator_stop_operation,
)
```

---

**END OF REFACTOR PLAN**

**Generated with Claude Code**
**Reviewed and approved for execution**
