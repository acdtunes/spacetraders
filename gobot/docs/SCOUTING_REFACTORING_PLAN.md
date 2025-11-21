# Scouting Package Refactoring Plan

**Date:** 2025-01-21
**Package:** `internal/application/scouting/`
**Objective:** Extract smaller methods from large handlers, replacing comments with self-documenting code

## Current State

The package has 4 files with 3 requiring significant refactoring:

| File | Current State | Target State |
|------|--------------|--------------|
| `scout_markets.go` | 183-line `Handle()` method 🚨 | ~25 lines |
| `scout_tour.go` | 163-line `Handle()` method 🚨 | ~15 lines |
| `assign_scouting_fleet.go` | 78-line `Handle()` method ⚠️ | ~15 lines |
| `get_market_data.go` | Already well-structured ✓ | No changes |

---

## Refactoring Strategy

### Principles
1. **Single Responsibility:** Each extracted method has one clear purpose
2. **Self-Documenting Names:** Replace inline comments with verb-based method names
3. **Preserve Behavior:** All tests must pass unchanged
4. **Improve Testability:** Smaller methods are easier to unit test
5. **Maintain Architecture:** Follow hexagonal architecture principles

### Execution Order
1. `scout_markets.go` (highest complexity, most impact)
2. `scout_tour.go` (significant code duplication)
3. `assign_scouting_fleet.go` (depends on patterns from above)
4. Add shared utilities (after patterns are established)

---

## File 1: `scout_markets.go` (Highest Priority)

### Current Issues
- **183-line monolithic `Handle()` method** with 8+ distinct responsibilities
- Complex nested logic with multiple early returns
- Comments replacing method names (lines 70-230)
- Duplicate code patterns (assignment checking appears twice)

### Refactoring Plan

#### Target Structure
```go
func (h *ScoutMarketsHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)
    logger := common.LoggerFromContext(ctx)

    // Cleanup phase
    if err := h.stopExistingContainers(ctx, cmd); err != nil {
        return nil, err
    }

    // Identify reuse opportunities
    shipsWithContainers, reusedContainers, shipsNeedingContainers, err :=
        h.identifyContainerReuse(ctx, cmd.ShipSymbols, cmd.PlayerID)
    if err != nil { return nil, err }

    // Early return if all ships already have containers
    if response, shouldReturn, err := h.handleAllShipsHaveContainers(shipsWithContainers, reusedContainers); shouldReturn {
        return response, err
    }

    // Load ship data & graph
    shipConfigs, err := h.loadShipConfigurations(ctx, shipsNeedingContainers, cmd.PlayerID)
    if err != nil { return nil, err }

    waypointData, err := h.loadSystemGraph(ctx, cmd.SystemSymbol, cmd.PlayerID.Value())
    if err != nil { return nil, err }

    // Calculate assignments
    assignments, err := h.calculateMarketAssignments(ctx, shipsNeedingContainers, cmd.Markets, shipConfigs, waypointData)
    if err != nil { return nil, err }

    // Create containers
    newContainerIDs, err := h.createScoutContainers(ctx, assignments, cmd)
    if err != nil { return nil, err }

    return h.buildFinalResponse(reusedContainers, newContainerIDs, assignments, shipsWithContainers), nil
}
```

#### Extracted Methods (8)

**1. `stopExistingContainers()` (lines 73-110 → extract)**
```go
// stopExistingContainers stops all existing scouting containers and releases ship assignments
// Returns: error
func (h *ScoutMarketsHandler) stopExistingContainers(ctx context.Context, cmd *ScoutMarketsCommand) error
```
- **Responsibility:** Self-contained cleanup phase
- **Replaces Comments:** Lines 73, 81, 90
- **Impact:** Removes 38-line block from main method

**2. `identifyContainerReuse()` (lines 112-151 → extract)**
```go
// identifyContainerReuse determines which ships can reuse existing containers vs need new ones
// Returns: shipsWithContainers (symbol→containerID), reusedContainers (IDs), shipsNeedingContainers (symbols), error
func (h *ScoutMarketsHandler) identifyContainerReuse(
    ctx context.Context,
    shipSymbols []string,
    playerID uint,
) (map[string]string, []string, []string, error)
```
- **Responsibility:** Consolidates assignment queries and partitioning logic
- **Replaces Comments:** Lines 112, 118, 124
- **Impact:** Eliminates duplicate assignment query code (appears at lines 75-78 and 118-121)

**3. `handleAllShipsHaveContainers()` (lines 137-151 → extract)**
```go
// handleAllShipsHaveContainers handles the early return scenario when all ships already have containers
// Returns: response, shouldReturn (bool), error
func (h *ScoutMarketsHandler) handleAllShipsHaveContainers(
    shipsWithContainers map[string]string,
    reusedContainers []string,
) (*ScoutMarketsResponse, bool, error)
```
- **Responsibility:** Early return scenario extraction
- **Impact:** Makes the happy path clearer in main method

**4. `loadShipConfigurations()` (lines 154-167 → extract)**
```go
// loadShipConfigurations loads ship data and prepares routing configurations
// Returns: map of ship symbol → ShipConfigData, error
func (h *ScoutMarketsHandler) loadShipConfigurations(
    ctx context.Context,
    shipSymbols []string,
    playerID PlayerID,
) (map[string]*routing.ShipConfigData, error)
```
- **Responsibility:** Encapsulates ship data loading loop
- **Replaces Comments:** Lines 154, 160
- **Impact:** Removes inline struct creation from main flow

**5. `loadSystemGraph()` (lines 169-179 → extract)**
```go
// loadSystemGraph fetches the navigation graph and converts waypoints to routing format
// Returns: waypoint data array, error
func (h *ScoutMarketsHandler) loadSystemGraph(
    ctx context.Context,
    systemSymbol string,
    playerID uint,
) ([]*system.WaypointData, error)
```
- **Responsibility:** Combines graph fetching + conversion
- **Replaces Comments:** Line 169, 175
- **Impact:** Removes conversion loop from main method

**6. `calculateMarketAssignments()` (lines 182-208 → extract)**
```go
// calculateMarketAssignments determines which ships should visit which markets
// Uses VRP for multiple ships, direct assignment for single ship
// Returns: map of ship symbol → market symbols, error
func (h *ScoutMarketsHandler) calculateMarketAssignments(
    ctx context.Context,
    shipsNeedingContainers []string,
    markets []string,
    shipConfigs map[string]*routing.ShipConfigData,
    waypointData []*system.WaypointData,
) (map[string][]string, error)
```
- **Responsibility:** Encapsulates the VRP vs single-ship logic
- **Replaces Comments:** Lines 182, 188, 195
- **Impact:** Clearer separation of concerns

**7. `createScoutContainers()` (lines 211-228 → extract)**
```go
// createScoutContainers creates container instances for each ship assignment
// Returns: array of new container IDs, error
func (h *ScoutMarketsHandler) createScoutContainers(
    ctx context.Context,
    assignments map[string][]string,
    cmd *ScoutMarketsCommand,
) ([]string, error)
```
- **Responsibility:** Container creation loop
- **Replaces Comments:** Line 211
- **Impact:** Removes 18-line block

**8. `buildFinalResponse()` (lines 230-244 → extract)**
```go
// buildFinalResponse assembles the final response with all container and assignment details
// Returns: ScoutMarketsResponse
func (h *ScoutMarketsHandler) buildFinalResponse(
    reusedContainerIDs []string,
    newContainerIDs []string,
    assignments map[string][]string,
    shipsWithContainers map[string]string,
) *ScoutMarketsResponse
```
- **Responsibility:** Response assembly logic
- **Impact:** Separates data transformation from business logic

#### Additional Utilities

**Helper: `queryShipAssignment()` (eliminate duplication)**
```go
// queryShipAssignment queries the database for a ship's current container assignment
// Returns: assignment (nil if none), error
func (h *ScoutMarketsHandler) queryShipAssignment(
    ctx context.Context,
    shipSymbol string,
    playerID uint,
) (*container.ShipAssignment, error)
```
- **Fixes:** Duplicate code at lines 75-78 and 118-121

---

## File 2: `scout_tour.go`

### Current Issues
- **163-line monolithic `Handle()` method**
- Dual execution paths (single vs multi-market) with significant code duplication
- Nested loops with context handling
- Navigation + logging code repeated multiple times
- Comments explaining what could be method names

### Refactoring Plan

#### Target Structure
```go
func (h *ScoutTourHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)

    ship, tourOrder, response, err := h.loadShipAndPrepareTour(ctx, cmd)
    if err != nil { return nil, err }

    if len(tourOrder) == 1 {
        err = h.executeStationaryScout(ctx, cmd, ship, tourOrder[0], response)
    } else {
        err = h.executeMultiMarketTour(ctx, cmd, tourOrder, response)
    }

    return response, err
}
```

#### Extracted Methods (7)

**1. `loadShipAndPrepareTour()` (lines 61-73 → extract)**
```go
// loadShipAndPrepareTour loads ship data, rotates tour to start at current location, and initializes response
// Returns: ship, tourOrder, response, error
func (h *ScoutTourHandler) loadShipAndPrepareTour(
    ctx context.Context,
    cmd *ScoutTourCommand,
) (*navigation.Ship, []string, *ScoutTourResponse, error)
```
- **Responsibility:** Combines ship loading + tour rotation + response initialization
- **Impact:** Removes setup boilerplate from main method

**2. `executeStationaryScout()` (lines 76-172 → extract)**
```go
// executeStationaryScout executes a continuous scanning operation at a single market
// Navigates to market (if needed), then scans every 60 seconds until context is cancelled
// Returns: error
func (h *ScoutTourHandler) executeStationaryScout(
    ctx context.Context,
    cmd *ScoutTourCommand,
    ship *navigation.Ship,
    marketWaypoint string,
    response *ScoutTourResponse,
) error
```
- **Responsibility:** Entire single-market tour logic (97 lines!)
- **Impact:** Clear separation from multi-market logic

**3. `navigateToMarketIfNeeded()` (lines 82-107 → extract, used within stationary scout)**
```go
// navigateToMarketIfNeeded navigates ship to market if not already there
// Returns: error
func (h *ScoutTourHandler) navigateToMarketIfNeeded(
    ctx context.Context,
    ship *navigation.Ship,
    marketWaypoint string,
    playerID PlayerID,
) error
```
- **Responsibility:** Conditional navigation logic
- **Fixes:** Removes duplication with multi-market navigation (lines 179-203)

**4. `performInitialScan()` (lines 109-127 → extract, used within stationary scout)**
```go
// performInitialScan performs the first market scan when ship is already at market
// Returns: error
func (h *ScoutTourHandler) performInitialScan(
    ctx context.Context,
    playerID uint,
    marketWaypoint string,
) error
```
- **Responsibility:** Handles the "already at market" case
- **Impact:** Separates initial scan from continuous loop

**5. `continuousMarketScanning()` (lines 131-172 → extract, used within stationary scout)**
```go
// continuousMarketScanning runs a loop that scans the market every 60 seconds
// Continues until context is cancelled or an error occurs
// Returns: error
func (h *ScoutTourHandler) continuousMarketScanning(
    ctx context.Context,
    cmd *ScoutTourCommand,
    marketWaypoint string,
    response *ScoutTourResponse,
) error
```
- **Responsibility:** The 60-second scan loop
- **Impact:** Encapsulates context-aware sleep pattern

**6. `executeMultiMarketTour()` (lines 174-210 → extract)**
```go
// executeMultiMarketTour executes a tour visiting multiple markets in sequence
// Navigates to each market and scans once before moving to next
// Returns: error
func (h *ScoutTourHandler) executeMultiMarketTour(
    ctx context.Context,
    cmd *ScoutTourCommand,
    tourOrder []string,
    response *ScoutTourResponse,
) error
```
- **Responsibility:** Entire multi-market tour logic (37 lines)
- **Impact:** Parallel structure to `executeStationaryScout()`

**7. `navigateToMarket()` (lines 186-203 → extract, used within multi-market tour)**
```go
// navigateToMarket navigates ship to specified market waypoint
// Returns: navigation response, error
func (h *ScoutTourHandler) navigateToMarket(
    ctx context.Context,
    cmd *ScoutTourCommand,
    marketWaypoint string,
) (*NavigateRouteResponse, error)
```
- **Responsibility:** Reusable navigation logic
- **Impact:** Consolidates logging + mediator call
- **Fixes:** Eliminates duplication (nearly identical code at lines 83-106 and 179-203)

---

## File 3: `assign_scouting_fleet.go`

### Current Issues
- **78-line `Handle()` method** with multiple responsibilities
- Long sequential steps with inline comments
- Creates and configures another handler inline

### Refactoring Plan

#### Target Structure
```go
func (h *AssignScoutingFleetHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)

    ships, scoutShips, err := h.validateAndLoadShips(ctx, cmd)
    if err != nil { return nil, err }

    marketSymbols, err := h.loadAndFilterMarketplaces(ctx, cmd.SystemSymbol)
    if err != nil { return nil, err }

    scoutCmd := h.buildScoutMarketsCommand(cmd, extractShipSymbols(scoutShips), marketSymbols)

    scoutResult, err := h.executeScoutMarkets(ctx, scoutCmd)
    if err != nil { return nil, err }

    return h.buildResponse(extractShipSymbols(scoutShips), scoutResult), nil
}
```

#### Extracted Methods (4 + 1 utility)

**1. `validateAndLoadShips()` (lines 68-77 → extract)**
```go
// validateAndLoadShips loads all ships and filters for scout-capable ships
// Returns: allShips, scoutShips, error
func (h *AssignScoutingFleetHandler) validateAndLoadShips(
    ctx context.Context,
    cmd *AssignScoutingFleetCommand,
) ([]*navigation.Ship, []*navigation.Ship, error)
```
- **Responsibility:** Combines ship loading + filtering in one cohesive operation
- **Replaces Comments:** Lines 68, 73
- **Impact:** Single method for related operations

**2. `loadAndFilterMarketplaces()` (lines 79-95 → extract)**
```go
// loadAndFilterMarketplaces loads system marketplaces and filters out fuel stations
// Returns: market symbols (excluding fuel stations), error
func (h *AssignScoutingFleetHandler) loadAndFilterMarketplaces(
    ctx context.Context,
    systemSymbol string,
) ([]string, error)
```
- **Responsibility:** Combines marketplace loading, fuel station filtering, and symbol extraction
- **Replaces Comments:** Lines 79, 85, 91
- **Impact:** Eliminates 3 inline comments

**3. `buildScoutMarketsCommand()` (lines 97-111 → extract)**
```go
// buildScoutMarketsCommand constructs the ScoutMarketsCommand with all required parameters
// Returns: ScoutMarketsCommand
func (h *AssignScoutingFleetHandler) buildScoutMarketsCommand(
    cmd *AssignScoutingFleetCommand,
    shipSymbols []string,
    marketSymbols []string,
) *ScoutMarketsCommand
```
- **Responsibility:** Encapsulates the command building logic
- **Replaces Comments:** Line 97
- **Impact:** Name clearly states what it does

**4. `executeScoutMarkets()` (lines 113-130 → extract)**
```go
// executeScoutMarkets creates the ScoutMarketsHandler and executes the command
// Returns: ScoutMarketsResponse, error
func (h *AssignScoutingFleetHandler) executeScoutMarkets(
    ctx context.Context,
    scoutCmd *ScoutMarketsCommand,
) (*ScoutMarketsResponse, error)
```
- **Responsibility:** Handles handler creation, execution, and type assertion
- **Replaces Comments:** Lines 113, 121
- **Impact:** Consolidates error handling

**Utility: `extractShipSymbols()` (lines 98-101 → extract as package-level utility)**
```go
// extractShipSymbols extracts ship symbols from a slice of Ship entities
// Returns: array of ship symbols
func extractShipSymbols(ships []*navigation.Ship) []string
```
- **Responsibility:** Reusable ship symbol extraction
- **Location:** Move to package-level utility (could be in `internal/application/scouting/utils.go` or similar)
- **Impact:** Can be reused across handlers

---

## File 4: `get_market_data.go`

### Status
✅ **No changes needed** - Already well-structured

This file serves as a **reference example** of well-refactored handlers:
- Small, focused handlers (~15 lines each)
- Single responsibility per method
- Clear separation between two handlers
- No code duplication
- Self-documenting code

Use this as a model when refactoring the other handlers.

---

## Cross-File Patterns & Opportunities

### Pattern 1: Request Type Validation
**Appears in:** All 4 handlers

**Current Implementation:**
```go
cmd, ok := request.(*SomeCommand)
if !ok {
    return nil, fmt.Errorf("invalid request type")
}
```

**Opportunity:** Extract to shared utility (future enhancement):
```go
// internal/application/common/validation.go
func ValidateRequestType[T common.Request](request common.Request) (T, error)
```

### Pattern 2: Assignment Querying
**Appears in:** `scout_markets.go` (lines 75-78, 118-121)

**Issue:** Exact duplicate code

**Solution:** Extract to handler method (see `queryShipAssignment()` above)

### Pattern 3: Navigation Code
**Appears in:** `scout_tour.go` (lines 83-106 and 179-203)

**Issue:** Nearly identical navigation logic

**Solution:** Extract to `navigateToMarket()` helper (see method #7 above)

### Pattern 4: Structured Logging
**Appears in:** `scout_tour.go` (10+ times), `scout_markets.go` (5+ times)

**Current:** Verbose map literals with repetitive fields

**Opportunity:** Create structured log helpers (future enhancement):
```go
func logNavigation(logger *slog.Logger, action, shipSymbol, destination string, extras map[string]interface{})
func logMarketScan(logger *slog.Logger, shipSymbol, waypoint string, iteration int, err error)
```

---

## Testing Strategy

### Verification Approach
1. **Run full BDD test suite before refactoring**
   ```bash
   make test-bdd
   ```

2. **Run tests after each file refactoring**
   ```bash
   go test ./test/bdd/... -v -godog.filter="scout"
   ```

3. **Verify no behavior changes**
   - All existing tests should pass unchanged
   - No new tests needed (behavior is identical)

### Coverage
Current BDD tests in `test/bdd/features/` should cover:
- Application layer command handlers
- Integration scenarios

If tests fail after refactoring, it indicates:
- Incorrect extraction (logic changed)
- Missing error handling
- Incorrect parameter passing

---

## Implementation Checklist

### Phase 1: `scout_markets.go`
- [ ] Extract `stopExistingContainers()`
- [ ] Extract `identifyContainerReuse()`
- [ ] Extract `handleAllShipsHaveContainers()`
- [ ] Extract `loadShipConfigurations()`
- [ ] Extract `loadSystemGraph()`
- [ ] Extract `calculateMarketAssignments()`
- [ ] Extract `createScoutContainers()`
- [ ] Extract `buildFinalResponse()`
- [ ] Extract `queryShipAssignment()` (eliminate duplication)
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 2: `scout_tour.go`
- [ ] Extract `loadShipAndPrepareTour()`
- [ ] Extract `executeStationaryScout()`
- [ ] Extract `navigateToMarketIfNeeded()`
- [ ] Extract `performInitialScan()`
- [ ] Extract `continuousMarketScanning()`
- [ ] Extract `executeMultiMarketTour()`
- [ ] Extract `navigateToMarket()` (eliminate duplication)
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 3: `assign_scouting_fleet.go`
- [ ] Extract `validateAndLoadShips()`
- [ ] Extract `loadAndFilterMarketplaces()`
- [ ] Extract `buildScoutMarketsCommand()`
- [ ] Extract `executeScoutMarkets()`
- [ ] Extract `extractShipSymbols()` utility
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 4: Final Verification
- [ ] Run full test suite: `make test`
- [ ] Run linter: `make lint`
- [ ] Format code: `make fmt`
- [ ] Verify all handlers follow single responsibility principle
- [ ] Verify no inline comments remain that could be method names
- [ ] Review extracted method names for clarity

---

## Success Criteria

### Quantitative
- `scout_markets.go Handle()`: 183 lines → ~25 lines (86% reduction)
- `scout_tour.go Handle()`: 163 lines → ~15 lines (91% reduction)
- `assign_scouting_fleet.go Handle()`: 78 lines → ~15 lines (81% reduction)
- **Total:** 424 lines → ~55 lines (87% reduction in main handler complexity)

### Qualitative
- ✅ All inline comments replaced with self-documenting method names
- ✅ Each method has single, clear responsibility
- ✅ No code duplication remains
- ✅ All tests pass unchanged
- ✅ Code is easier to understand and maintain
- ✅ Improved testability (smaller methods)
- ✅ Follows hexagonal architecture principles

---

## Future Enhancements (Out of Scope)

These patterns were identified but are not part of this refactoring:

1. **Generic request type validation** - Create `ValidateRequestType[T]()` utility
2. **Structured logging helpers** - Reduce logging boilerplate
3. **Shared navigation utilities** - If navigation patterns emerge in other packages
4. **Builder pattern for commands** - If command construction becomes more complex

These can be addressed in future refactorings if patterns continue to appear across multiple packages.
