# Shipyard Package Refactoring Plan

**Date:** 2025-01-21
**Package:** `internal/application/shipyard/`
**Objective:** Extract smaller methods from large handlers, replacing comments with self-documenting code

## Current State

The package has 3 files with 2 requiring significant refactoring:

| File | Current State | Target State |
|------|--------------|--------------|
| `batch_purchase_ships.go` | 141-line `Handle()` method 🚨 | ~25 lines |
| `purchase_ship.go` | 121-line `Handle()` method 🚨 | ~30 lines |
| `get_shipyard_listings.go` | 50-line `Handle()` method ⚠️ | ~25 lines |

---

## Refactoring Strategy

### Principles
1. **Single Responsibility:** Each extracted method has one clear purpose
2. **Self-Documenting Names:** Replace inline comments with verb-based method names
3. **Preserve Behavior:** All tests must pass unchanged
4. **Improve Testability:** Smaller methods are easier to unit test
5. **Maintain Architecture:** Follow hexagonal architecture principles
6. **Eliminate Duplication:** Use existing utilities like `shared.ExtractSystemSymbol()`

### Execution Order
1. `batch_purchase_ships.go` (highest complexity, 141 lines)
2. `purchase_ship.go` (complex with multiple helper methods, 121 lines main + 59 + 58 lines helpers)
3. `get_shipyard_listings.go` (simpler transformation logic, 50 lines)
4. Add shared utilities (if patterns emerge)

---

## File 1: `batch_purchase_ships.go` (Highest Priority)

### Current Issues
- **141-line monolithic `Handle()` method** with multiple responsibilities
- Deeply nested logic with complex conditional branches
- Manual system symbol extraction instead of using existing utility
- Duplicate patterns with `purchase_ship.go` (credit checking, shipyard validation)
- 55-line section for calculating purchasable count
- 54-line purchase loop with partial success handling

### Refactoring Plan

#### Target Structure
```go
func (h *BatchPurchaseShipsHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)
    logger := common.LoggerFromContext(ctx)

    // Early validation
    if response := h.validatePurchaseRequest(cmd.Quantity, cmd.MaxBudget); response != nil {
        return response, nil
    }

    token, err := h.getPlayerToken(ctx, cmd.PlayerID)
    if err != nil { return nil, err }

    // Calculate how many ships we can purchase
    shipPrice, purchasableCount, shipyardWaypoint, err :=
        h.calculatePurchasableCount(ctx, cmd, token)
    if err != nil { return nil, err }

    // Execute batch purchase
    purchasedShips, totalSpent, err :=
        h.executePurchaseLoop(ctx, cmd, purchasableCount, shipyardWaypoint)
    if err != nil { return nil, err }

    return &BatchPurchaseShipsResponse{
        Ships:      purchasedShips,
        Count:      len(purchasedShips),
        TotalSpent: totalSpent,
    }, nil
}
```

#### Extracted Methods (8)

**1. `validatePurchaseRequest()` (lines 68-75 → extract)**
```go
// validatePurchaseRequest validates quantity and budget constraints
// Returns early-return response if validation fails, nil if valid
func (h *BatchPurchaseShipsHandler) validatePurchaseRequest(
    quantity int,
    maxBudget int,
) *BatchPurchaseShipsResponse
```
- **Responsibility:** Input validation with early returns
- **Replaces Comment:** Line 68 "Validate inputs"
- **Impact:** Removes validation boilerplate from main method
- **Returns:** Empty response if invalid, nil if valid

**2. `calculatePurchasableCount()` (lines 83-138 → extract)** ⚠️ CRITICAL
```go
// calculatePurchasableCount determines how many ships can be purchased
// Considers: requested quantity, budget constraints, agent credits
// Returns: ship price, purchasable count, shipyard waypoint, error
func (h *BatchPurchaseShipsHandler) calculatePurchasableCount(
    ctx context.Context,
    cmd *BatchPurchaseShipsCommand,
    token string,
) (shipPrice int, purchasableCount int, shipyardWaypoint string, err error)
```
- **Responsibility:** Complex calculation with API calls and constraint application
- **Replaces Comments:** Lines 83, 89, 122, 128
- **Impact:** Removes 55-line block (largest extraction in this file)
- **Implementation Details:**
  - Handles two branches: shipyard provided vs discovery on first purchase
  - Calls `getShipPriceFromShipyard()` helper
  - Calls `calculateMaxPurchasableShips()` helper
  - Returns all data needed for purchase loop

**3. `getShipPriceFromShipyard()` (lines 99-120 → extract)**
```go
// getShipPriceFromShipyard fetches shipyard data and gets price for ship type
// Returns: ship purchase price, error
func (h *BatchPurchaseShipsHandler) getShipPriceFromShipyard(
    ctx context.Context,
    systemSymbol string,
    waypointSymbol string,
    shipType string,
) (int, error)
```
- **Responsibility:** Mediator call to get listings + price lookup
- **Replaces Comment:** Line 99 "Get shipyard listings to determine price"
- **Impact:** Encapsulates 22-line API interaction
- **Note:** Similar pattern exists in `purchase_ship.go` - candidate for shared utility

**4. `calculateMaxPurchasableShips()` (lines 128-133 → extract)**
```go
// calculateMaxPurchasableShips applies all constraints to determine max purchasable count
// Returns: minimum of quantity requested, budget allows, credits allow
func (h *BatchPurchaseShipsHandler) calculateMaxPurchasableShips(
    requestedQuantity int,
    maxBudget int,
    agentCredits int,
    shipPrice int,
) int
```
- **Responsibility:** Pure calculation - applies 3 constraints
- **Replaces Comment:** Line 128 "Calculate how many ships we can actually purchase"
- **Impact:** Clear constraint application logic
- **Implementation:**
  ```go
  budgetAllows := maxBudget / shipPrice
  creditsAllow := agentCredits / shipPrice
  return min(requestedQuantity, budgetAllows, creditsAllow)
  ```

**5. `executePurchaseLoop()` (lines 140-194 → extract)** ⚠️ CRITICAL
```go
// executePurchaseLoop purchases ships one at a time up to purchasable count
// Handles partial success, captures shipyard location from first purchase
// Returns: purchased ships, total spent, error
func (h *BatchPurchaseShipsHandler) executePurchaseLoop(
    ctx context.Context,
    cmd *BatchPurchaseShipsCommand,
    purchasableCount int,
    shipyardWaypoint string,
) ([]*navigation.Ship, int, error)
```
- **Responsibility:** Iterative purchasing with state tracking
- **Replaces Comment:** Line 140 "Purchase ships one at a time"
- **Impact:** Removes 54-line loop from main method
- **Implementation Details:**
  - Calls `purchaseShip()` helper in loop
  - Captures shipyard location from first purchase (if not provided)
  - Checks `hasRemainingBudgetAndCredits()` after each purchase
  - Handles partial success gracefully

**6. `purchaseShip()` (lines 150-177 → extract)**
```go
// purchaseShip purchases a single ship via the PurchaseShipCommand
// Returns: purchase response, error
func (h *BatchPurchaseShipsHandler) purchaseShip(
    ctx context.Context,
    cmd *BatchPurchaseShipsCommand,
    shipyardWaypoint string,
) (*PurchaseShipResponse, error)
```
- **Responsibility:** Single ship purchase via mediator
- **Impact:** Encapsulates command creation, dispatch, type assertion
- **Implementation:**
  - Creates `PurchaseShipCommand`
  - Calls `h.mediator.Send()`
  - Type asserts response
  - Returns typed response

**7. `hasRemainingBudgetAndCredits()` (lines 185-193 → extract)**
```go
// hasRemainingBudgetAndCredits checks if we can afford another ship purchase
// Returns: true if both budget and credits allow another purchase
func (h *BatchPurchaseShipsHandler) hasRemainingBudgetAndCredits(
    totalSpent int,
    remainingCredits int,
    shipPrice int,
    maxBudget int,
) bool
```
- **Responsibility:** Loop continuation condition
- **Replaces Comment:** Line 185 "Check if we can afford another ship"
- **Impact:** Clear break condition logic
- **Implementation:**
  ```go
  return totalSpent+shipPrice <= maxBudget && remainingCredits >= shipPrice
  ```

**8. `getPlayerToken()` (lines 77-81 → extract as utility)**
```go
// getPlayerToken fetches the player's authentication token
// Returns: token string, error
func (h *BatchPurchaseShipsHandler) getPlayerToken(
    ctx context.Context,
    playerID common.PlayerID,
) (string, error)
```
- **Responsibility:** Token fetching via mediator
- **Note:** This pattern exists in multiple handlers - candidate for base handler utility

#### Code Quality Fixes

**Fix: Use Existing Utility (lines 92-98)**

**Current Code (WRONG):**
```go
// Extract system symbol from waypoint
systemSymbol := cmd.ShipyardWaypoint
lastHyphenIndex := -1
for i := len(systemSymbol) - 1; i >= 0; i-- {
    if systemSymbol[i] == '-' {
        lastHyphenIndex = i
        break
    }
}
systemSymbol = systemSymbol[:lastHyphenIndex]
```

**Replace With (RIGHT):**
```go
systemSymbol := shared.ExtractSystemSymbol(cmd.ShipyardWaypoint)
```

**Reason:** Line 137 in same package (`purchase_ship.go`) already uses `shared.ExtractSystemSymbol()`. Eliminate duplication and use proven utility.

---

## File 2: `purchase_ship.go` (High Priority)

### Current Issues
- **121-line `Handle()` method** with sequential preparation steps
- **59-line `discoverNearestShipyard()` helper** with complex filtering logic
- **58-line `convertShipDataToEntity()` helper** doing data transformation
- Code duplication with `batch_purchase_ships.go` (credit checking, shipyard validation)
- Sequential operations that could be better named (navigate → dock → validate)

### Refactoring Plan

#### Main Handler: `Handle()` (lines 70-190)

#### Target Structure
```go
func (h *PurchaseShipHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)
    logger := common.LoggerFromContext(ctx)

    token, err := h.getPlayerToken(ctx, cmd.PlayerID)
    if err != nil { return nil, err }

    purchasingShip, err := h.loadPurchasingShip(ctx, cmd, token)
    if err != nil { return nil, err }

    shipyardWaypoint, err := h.resolveShipyardWaypoint(ctx, cmd, purchasingShip, token)
    if err != nil { return nil, err }

    purchasingShip, err = h.prepareShipForPurchase(ctx, cmd, shipyardWaypoint, purchasingShip)
    if err != nil { return nil, err }

    purchasePrice, err := h.validateAndGetShipPrice(ctx, cmd, shipyardWaypoint)
    if err != nil { return nil, err }

    agentCredits, err := h.ensureSufficientCredits(ctx, token, purchasePrice)
    if err != nil { return nil, err }

    purchasedShipData, err := h.apiClient.PurchaseShip(ctx, cmd.ShipType, shipyardWaypoint, token)
    if err != nil { return nil, err }

    newShip, err := h.convertShipDataToEntity(ctx, purchasedShipData, cmd.PlayerID)
    if err != nil { return nil, err }

    return &PurchaseShipResponse{
        Ship:             newShip,
        PurchasingShip:   purchasingShip,
        AgentCredits:     agentCredits,
        TransactionPrice: purchasePrice,
    }, nil
}
```

#### Extracted Methods from Handle()

**1. `loadPurchasingShip()` (lines 82-86 → extract)**
```go
// loadPurchasingShip fetches the ship that will make the purchase
// Returns: purchasing ship entity, error
func (h *PurchaseShipHandler) loadPurchasingShip(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    token string,
) (*navigation.Ship, error)
```
- **Responsibility:** Load ship from API
- **Impact:** Separates data loading from business logic

**2. `resolveShipyardWaypoint()` (lines 88-96 → extract)**
```go
// resolveShipyardWaypoint determines the target shipyard (provided or auto-discovered)
// Returns: shipyard waypoint symbol, error
func (h *PurchaseShipHandler) resolveShipyardWaypoint(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    purchasingShip *navigation.Ship,
    token string,
) (string, error)
```
- **Responsibility:** Shipyard resolution logic
- **Replaces Comment:** Line 88 "Auto-discover shipyard if not provided"
- **Implementation:**
  ```go
  if cmd.ShipyardWaypoint != "" {
      return cmd.ShipyardWaypoint, nil
  }
  return h.discoverNearestShipyard(ctx, cmd, purchasingShip.Nav.Location, token)
  ```

**3. `prepareShipForPurchase()` (lines 98-133 → extract)** ⚠️ CRITICAL
```go
// prepareShipForPurchase ensures ship is at shipyard and docked
// Combines navigation and docking steps
// Returns: prepared ship (reloaded after movements), error
func (h *PurchaseShipHandler) prepareShipForPurchase(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    shipyardWaypoint string,
    purchasingShip *navigation.Ship,
) (*navigation.Ship, error)
```
- **Responsibility:** Ship positioning and state preparation
- **Replaces Comments:** Lines 98, 117
- **Impact:** Consolidates 36 lines into one cohesive operation
- **Implementation Details:**
  - Calls `navigateToShipyard()` if needed (lines 98-115)
  - Calls `dockShipIfNeeded()` (lines 117-133)
  - Returns final ship state ready for purchase

**4. `navigateToShipyard()` (lines 98-115 → extract as helper)**
```go
// navigateToShipyard moves ship to shipyard waypoint if not already there
// Returns: reloaded ship after navigation, error
func (h *PurchaseShipHandler) navigateToShipyard(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    shipyardWaypoint string,
    purchasingShip *navigation.Ship,
) (*navigation.Ship, error)
```
- **Responsibility:** Conditional navigation with ship reload
- **Replaces Comment:** Line 98 "Navigate to shipyard if not already there"
- **Impact:** 17 lines → single method call

**5. `dockShipIfNeeded()` (lines 117-133 → extract as helper)**
```go
// dockShipIfNeeded docks the ship if currently in orbit
// Returns: reloaded ship after docking, error
func (h *PurchaseShipHandler) dockShipIfNeeded(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    purchasingShip *navigation.Ship,
) (*navigation.Ship, error)
```
- **Responsibility:** Conditional docking with ship reload
- **Replaces Comment:** Line 117 "Dock ship if in orbit"
- **Impact:** 16 lines → single method call

**6. `validateAndGetShipPrice()` (lines 135-158 → extract)** ⚠️ MEDIUM
```go
// validateAndGetShipPrice gets shipyard listings and validates ship availability
// Returns: purchase price for ship type, error
func (h *PurchaseShipHandler) validateAndGetShipPrice(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    shipyardWaypoint string,
) (int, error)
```
- **Responsibility:** Shipyard validation and price retrieval
- **Replaces Comments:** Lines 135, 144
- **Impact:** 23 lines → single method call
- **Note:** Similar to `getShipPriceFromShipyard()` in batch_purchase_ships.go

**7. `ensureSufficientCredits()` (lines 160-169 → extract)**
```go
// ensureSufficientCredits validates player has enough credits for purchase
// Returns: agent credits after validation, error
func (h *PurchaseShipHandler) ensureSufficientCredits(
    ctx context.Context,
    token string,
    purchasePrice int,
) (int, error)
```
- **Responsibility:** Credit validation
- **Replaces Comment:** Line 160 "Validate player has sufficient credits"
- **Impact:** 10 lines → single method call
- **Note:** Similar pattern in batch_purchase_ships.go

#### Helper Method: `discoverNearestShipyard()` (lines 193-252)

#### Target Structure
```go
func (h *PurchaseShipHandler) discoverNearestShipyard(
    ctx context.Context,
    cmd *PurchaseShipCommand,
    currentLocation *shared.Waypoint,
    token string,
) (string, error) {
    systemSymbol := shared.ExtractSystemSymbol(currentLocation.Symbol)

    shipyardWaypoints, err := h.getShipyardWaypoints(ctx, systemSymbol)
    if err != nil { return "", err }

    candidates, err := h.filterShipyardsBySupportedType(
        ctx, shipyardWaypoints, systemSymbol, cmd.ShipType, token, currentLocation,
    )
    if err != nil { return "", err }

    if len(candidates) == 0 {
        return "", fmt.Errorf("no shipyards in system %s sell ship type %s", systemSymbol, cmd.ShipType)
    }

    return h.findNearestShipyard(candidates), nil
}
```

#### Extracted Methods from discoverNearestShipyard()

**8. `getShipyardWaypoints()` (lines 202-209 → extract)**
```go
// getShipyardWaypoints fetches all waypoints in system with SHIPYARD trait
// Returns: waypoint array, error
func (h *PurchaseShipHandler) getShipyardWaypoints(
    ctx context.Context,
    systemSymbol string,
) ([]*shared.Waypoint, error)
```
- **Responsibility:** Waypoint filtering by trait
- **Replaces Comment:** Line 199 "Find all waypoints with SHIPYARD trait"
- **Impact:** 8 lines → single method call

**9. `filterShipyardsBySupportedType()` (lines 218-237 → extract)** ⚠️ MEDIUM
```go
// filterShipyardsBySupportedType finds shipyards that sell the desired ship type
// Returns: array of shipyard candidates with distances
func (h *PurchaseShipHandler) filterShipyardsBySupportedType(
    ctx context.Context,
    waypoints []*shared.Waypoint,
    systemSymbol string,
    shipType string,
    token string,
    currentLocation *shared.Waypoint,
) ([]shipyardCandidate, error)
```
- **Responsibility:** Complex filtering with API calls
- **Impact:** 19 lines → single method call
- **Implementation Details:**
  - Loops through waypoints
  - Calls `doesShipyardSellType()` for each
  - Calculates distance for valid shipyards
  - Returns candidates array

**10. `doesShipyardSellType()` (lines 220-236 → extract)**
```go
// doesShipyardSellType checks if a specific shipyard sells the ship type
// Returns: true if shipyard supports type, false otherwise, error
func (h *PurchaseShipHandler) doesShipyardSellType(
    ctx context.Context,
    systemSymbol string,
    waypoint *shared.Waypoint,
    shipType string,
    token string,
) (bool, error)
```
- **Responsibility:** Single shipyard type validation
- **Impact:** Extracted from 19-line loop
- **Implementation:**
  - Fetches shipyard data via API
  - Checks if ShipTypes contains desired type
  - Returns boolean result

**11. `findNearestShipyard()` (lines 243-249 → extract)**
```go
// findNearestShipyard selects the closest shipyard from candidates
// Returns: waypoint symbol of nearest shipyard
func (h *PurchaseShipHandler) findNearestShipyard(
    candidates []shipyardCandidate,
) string
```
- **Responsibility:** Minimum distance calculation
- **Replaces Comment:** Line 243 "Find nearest shipyard"
- **Impact:** 7 lines → single method call
- **Implementation:** Simple loop finding min distance

#### Helper Method: `convertShipDataToEntity()` (lines 255-312)

#### Target Structure
```go
func (h *PurchaseShipHandler) convertShipDataToEntity(
    ctx context.Context,
    shipData *ShipData,
    playerID uint,
) (*navigation.Ship, error) {
    waypoint, err := h.getWaypointDetails(ctx, shipData.Nav.WaypointSymbol, playerID)
    if err != nil { return nil, err }

    cargoItems, err := h.convertInventoryItems(shipData.Cargo.Inventory)
    if err != nil { return nil, err }

    cargo, fuel, navStatus, err := h.createShipValueObjects(shipData, cargoItems)
    if err != nil { return nil, err }

    ship, err := navigation.NewShip(
        shipData.Symbol,
        shipData.Registration.Role,
        waypoint,
        navStatus,
        fuel,
        cargo,
        shipData.Cargo.Capacity,
        shipData.Fuel.Capacity,
    )
    if err != nil { return nil, err }

    return ship, nil
}
```

#### Extracted Methods from convertShipDataToEntity()

**12. `getWaypointDetails()` (lines 263-266 → extract)**
```go
// getWaypointDetails fetches waypoint data for ship's current location
// Returns: waypoint entity, error
func (h *PurchaseShipHandler) getWaypointDetails(
    ctx context.Context,
    waypointSymbol string,
    playerID uint,
) (*shared.Waypoint, error)
```
- **Responsibility:** Waypoint data fetching
- **Impact:** Separates data loading

**13. `convertInventoryItems()` (lines 268-276 → extract)** ⚠️ MEDIUM
```go
// convertInventoryItems converts API cargo data to domain cargo items
// Returns: cargo item array, error
func (h *PurchaseShipHandler) convertInventoryItems(
    inventoryData []*CargoItemData,
) ([]*shared.CargoItem, error)
```
- **Responsibility:** Data transformation loop
- **Replaces Comment:** Line 268 "Convert cargo inventory"
- **Impact:** 9 lines → single method call
- **Note:** Consider moving to shared repository/converter utility

**14. `createShipValueObjects()` (lines 278-291 → extract)**
```go
// createShipValueObjects creates domain value objects from API data
// Returns: cargo, fuel, navStatus value objects, error
func (h *PurchaseShipHandler) createShipValueObjects(
    shipData *ShipData,
    cargoItems []*shared.CargoItem,
) (*shared.Cargo, *shared.Fuel, navigation.NavStatus, error)
```
- **Responsibility:** Value object creation
- **Impact:** Consolidates 14 lines of object construction
- **Implementation:**
  - Creates Cargo from items array
  - Creates Fuel from current/capacity
  - Parses NavStatus string
  - Returns all three

#### Structural Improvements

**Move `shipyardCandidate` to file level (lines 212-215)**

**Current (WRONG):**
```go
func (h *PurchaseShipHandler) discoverNearestShipyard(...) {
    type shipyardCandidate struct {
        waypoint string
        distance int
    }
    candidates := []shipyardCandidate{}
    // ...
}
```

**Move To (RIGHT):**
```go
// shipyardCandidate represents a potential shipyard with its distance from current location
type shipyardCandidate struct {
    waypoint string
    distance int
}

func (h *PurchaseShipHandler) discoverNearestShipyard(...) {
    // ...
}
```

**Reason:** Makes the type reusable, improves testability, follows Go conventions

---

## File 3: `get_shipyard_listings.go` (Low Priority)

### Current Issues
- **50-line `Handle()` method** with inline data transformation
- Comments describing loops that could be method names
- Good structure overall, but room for minor improvements

### Refactoring Plan

#### Target Structure
```go
func (h *GetShipyardListingsHandler) Handle(ctx, request) (common.Response, error) {
    cmd := validateRequestType(request)

    token, err := h.getPlayerToken(ctx, cmd.PlayerID)
    if err != nil { return nil, err }

    shipyardData, err := h.apiClient.GetShipyard(ctx, cmd.SystemSymbol, cmd.WaypointSymbol, token)
    if err != nil { return nil, err }

    shipListings := h.convertShipListings(shipyardData.Ships)
    shipTypes := h.extractShipTypeStrings(shipyardData.ShipTypes)

    shipyardDomain, err := h.buildShipyardDomain(shipyardData, shipListings, shipTypes)
    if err != nil { return nil, err }

    return &GetShipyardListingsResponse{
        Shipyard: shipyardDomain,
    }, nil
}
```

#### Extracted Methods (3)

**1. `convertShipListings()` (lines 63-76 → extract)**
```go
// convertShipListings converts API ship listings to domain model
// Returns: array of domain ShipListing objects
func (h *GetShipyardListingsHandler) convertShipListings(
    apiShips []*ShipListingData,
) []shipyard.ShipListing
```
- **Responsibility:** Data transformation loop
- **Replaces Comment:** Line 62 "Convert API data to domain model"
- **Impact:** 14 lines → single method call
- **Implementation:**
  - Loops through API listings
  - Creates domain ShipListing for each
  - Returns array

**2. `extractShipTypeStrings()` (lines 78-81 → extract)**
```go
// extractShipTypeStrings extracts ship type names from API structures
// Returns: array of ship type strings
func (h *GetShipyardListingsHandler) extractShipTypeStrings(
    apiShipTypes []*ShipTypeData,
) []string
```
- **Responsibility:** Simple data extraction
- **Impact:** 4 lines → single method call
- **Implementation:** Maps `ShipTypeData.Type` to string array

**3. `buildShipyardDomain()` (lines 83-89 → extract)**
```go
// buildShipyardDomain constructs the domain Shipyard entity from API data
// Returns: shipyard domain object, error
func (h *GetShipyardListingsHandler) buildShipyardDomain(
    shipyardData *ShipyardData,
    shipListings []shipyard.ShipListing,
    shipTypes []string,
) (*shipyard.Shipyard, error)
```
- **Responsibility:** Domain object construction
- **Impact:** 7 lines → single method call
- **Implementation:** Calls `shipyard.NewShipyard()` constructor

**Severity:** LOW - File is already well-structured, extractions are optional for consistency

---

## Cross-File Patterns & Opportunities

### 1. **System Symbol Extraction** (DUPLICATION - FIX REQUIRED)

**Issue:** Manual parsing in batch_purchase_ships.go when utility exists

**Location 1 (WRONG):** `batch_purchase_ships.go` lines 92-98
```go
// Extract system symbol from waypoint
systemSymbol := cmd.ShipyardWaypoint
lastHyphenIndex := -1
for i := len(systemSymbol) - 1; i >= 0; i-- {
    if systemSymbol[i] == '-' {
        lastHyphenIndex = i
        break
    }
}
systemSymbol = systemSymbol[:lastHyphenIndex]
```

**Location 2 (RIGHT):** `purchase_ship.go` line 137
```go
systemSymbol := shared.ExtractSystemSymbol(currentLocation.Symbol)
```

**Action:** Replace manual parsing with `shared.ExtractSystemSymbol()` utility

---

### 2. **Player Token Retrieval** (SHARED PATTERN)

**Appears in:**
- `batch_purchase_ships.go` lines 77-81
- `purchase_ship.go` lines 76-80
- `get_shipyard_listings.go` lines 50-54

**Pattern:**
```go
response, err := h.mediator.Send(ctx, &GetPlayerQuery{ID: cmd.PlayerID})
if err != nil { return nil, err }
token := response.(*GetPlayerResponse).Player.Token
```

**Opportunity:** Extract to base handler utility or common helper
```go
func getPlayerToken(ctx context.Context, mediator Mediator, playerID PlayerID) (string, error)
```

---

### 3. **Shipyard Listings + Type Validation** (DUPLICATION)

**Location 1:** `batch_purchase_ships.go` lines 99-120
- Get shipyard listings via mediator
- Find ship type in listings
- Get purchase price
- 22 lines

**Location 2:** `purchase_ship.go` lines 135-158
- Get shipyard listings via mediator
- Validate ship type exists
- Get purchase price
- 23 lines

**Similarity:** Both perform same sequence with minor variations

**Opportunity:** Create shared helper method
```go
// validateShipTypeAvailableAtShipyard checks shipyard sells type and returns price
func validateShipTypeAvailableAtShipyard(
    ctx context.Context,
    mediator Mediator,
    systemSymbol, waypointSymbol, shipType string,
) (price int, err error)
```

**Benefit:** Eliminates 45 lines of duplicate code across two handlers

---

### 4. **Agent Credit Checking** (DUPLICATION)

**Location 1:** `batch_purchase_ships.go` lines 123-127
```go
agentResponse, err := h.apiClient.GetAgent(ctx, token)
if err != nil { return 0, 0, "", fmt.Errorf("failed to get agent data: %w", err) }
agentCredits := agentResponse.Credits
```

**Location 2:** `purchase_ship.go` lines 162-165
```go
agentResponse, err := h.apiClient.GetAgent(ctx, token)
if err != nil { return nil, fmt.Errorf("failed to get agent data: %w", err) }
agentCredits := agentResponse.Credits
```

**Similarity:** Identical API calls

**Opportunity:** Extract to handler helper or domain service
```go
// getAgentCredits fetches current agent credit balance
func (h *Handler) getAgentCredits(ctx context.Context, token string) (int, error)
```

---

### 5. **Ship Reloading After Commands** (PATTERN)

**Location 1:** `purchase_ship.go` lines 110-114
```go
// Reload ship after navigation
shipResponse, err := h.mediator.Send(ctx, &GetShipQuery{...})
if err != nil { return nil, err }
purchasingShip = shipResponse.(*GetShipResponse).Ship
```

**Location 2:** `purchase_ship.go` lines 128-132
```go
// Reload ship after docking
shipResponse, err := h.mediator.Send(ctx, &GetShipQuery{...})
if err != nil { return nil, err }
purchasingShip = shipResponse.(*GetShipResponse).Ship
```

**Pattern:** Every state-changing command requires ship reload

**Opportunity:** Create helper method
```go
// reloadShip fetches fresh ship state after modification
func (h *PurchaseShipHandler) reloadShip(
    ctx context.Context,
    shipSymbol string,
    playerID uint,
) (*navigation.Ship, error)
```

**Benefit:** Reduces 10-line blocks to 1-line calls

---

### 6. **Data Conversion Patterns** (CONSIDER SHARED UTILITIES)

**Inventory Conversion:** `purchase_ship.go` lines 268-276
- Converts API cargo data to domain CargoItem objects
- Similar patterns likely exist in ship query handlers

**Opportunity:** Create shared repository or converter utility
```go
// pkg/converters/ship_converter.go
func ConvertInventoryItems(apiItems []*CargoItemData) ([]*shared.CargoItem, error)
func ConvertShipDataToEntity(shipData *ShipData) (*navigation.Ship, error)
```

**Benefit:** Centralized conversion logic, easier to maintain

---

## Summary of Refactoring Priorities

### HIGH Priority (Largest Impact)

1. **batch_purchase_ships.go `Handle()`** - 141 lines → ~25 lines
   - Extract `calculatePurchasableCount()` (55 lines)
   - Extract `executePurchaseLoop()` (54 lines)
   - Fix: Replace manual system symbol parsing with utility
   - **Impact:** 82% reduction, eliminates duplication

2. **purchase_ship.go `Handle()`** - 121 lines → ~30 lines
   - Extract `prepareShipForPurchase()` (36 lines)
   - Extract `validateAndGetShipPrice()` (23 lines)
   - **Impact:** 75% reduction, clearer flow

### MEDIUM Priority (Helper Methods)

3. **purchase_ship.go `discoverNearestShipyard()`** - 59 lines → ~20 lines
   - Extract `filterShipyardsBySupportedType()` (19 lines)
   - Move `shipyardCandidate` to file level
   - **Impact:** 66% reduction, improved structure

4. **purchase_ship.go `convertShipDataToEntity()`** - 58 lines → ~30 lines
   - Extract `convertInventoryItems()` (9 lines)
   - Consider moving to shared utility
   - **Impact:** 48% reduction, potential reuse

### LOW Priority (Already Good)

5. **get_shipyard_listings.go `Handle()`** - 50 lines → ~25 lines
   - Extract `convertShipListings()` (14 lines)
   - Extract `extractShipTypeStrings()` (4 lines)
   - **Impact:** 50% reduction, consistency improvement

### Shared Utilities (Future Enhancement)

6. **Create common helpers** - Reduce boilerplate
   - `getPlayerToken()` helper (used 3x)
   - `validateShipTypeAvailableAtShipyard()` (eliminates 45 lines of duplication)
   - `reloadShip()` helper (used 2x in purchase_ship.go)
   - Ship data converters (centralized transformation)

---

## Implementation Checklist

### Phase 1: `batch_purchase_ships.go` (Highest Priority)
- [ ] Extract `validatePurchaseRequest()`
- [ ] Extract `getPlayerToken()`
- [ ] Extract `getShipPriceFromShipyard()`
- [ ] Extract `calculateMaxPurchasableShips()`
- [ ] Extract `calculatePurchasableCount()` (calls above helpers)
- [ ] Extract `purchaseShip()`
- [ ] Extract `hasRemainingBudgetAndCredits()`
- [ ] Extract `executePurchaseLoop()` (calls above helpers)
- [ ] Fix: Replace manual system symbol parsing (lines 92-98) with `shared.ExtractSystemSymbol()`
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 2: `purchase_ship.go` (Main Handler)
- [ ] Extract `loadPurchasingShip()`
- [ ] Extract `resolveShipyardWaypoint()`
- [ ] Extract `navigateToShipyard()`
- [ ] Extract `dockShipIfNeeded()`
- [ ] Extract `prepareShipForPurchase()` (calls above two methods)
- [ ] Extract `validateAndGetShipPrice()`
- [ ] Extract `ensureSufficientCredits()`
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 3: `purchase_ship.go` (Helper Methods)
- [ ] Move `shipyardCandidate` struct to file level
- [ ] Extract `getShipyardWaypoints()` from `discoverNearestShipyard()`
- [ ] Extract `doesShipyardSellType()` from `discoverNearestShipyard()`
- [ ] Extract `filterShipyardsBySupportedType()` from `discoverNearestShipyard()`
- [ ] Extract `findNearestShipyard()` from `discoverNearestShipyard()`
- [ ] Refactor `discoverNearestShipyard()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 4: `purchase_ship.go` (Conversion Method)
- [ ] Extract `getWaypointDetails()` from `convertShipDataToEntity()`
- [ ] Extract `convertInventoryItems()` from `convertShipDataToEntity()`
- [ ] Extract `createShipValueObjects()` from `convertShipDataToEntity()`
- [ ] Refactor `convertShipDataToEntity()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 5: `get_shipyard_listings.go`
- [ ] Extract `convertShipListings()`
- [ ] Extract `extractShipTypeStrings()`
- [ ] Extract `buildShipyardDomain()`
- [ ] Refactor main `Handle()` to use extracted methods
- [ ] Run tests: `make test-bdd`

### Phase 6: Final Verification
- [ ] Run full test suite: `make test`
- [ ] Run linter: `make lint`
- [ ] Format code: `make fmt`
- [ ] Verify all handlers follow single responsibility principle
- [ ] Verify no inline comments remain that could be method names
- [ ] Review extracted method names for clarity
- [ ] Check for any remaining code duplication

### Phase 7: Shared Utilities (Optional Future Enhancement)
- [ ] Extract `getPlayerToken()` to common helper
- [ ] Extract `validateShipTypeAvailableAtShipyard()` shared method
- [ ] Extract `reloadShip()` helper for purchase_ship.go
- [ ] Consider creating `pkg/converters/ship_converter.go` for data transformations
- [ ] Update both handlers to use shared utilities
- [ ] Run tests after each shared utility addition

---

## Success Criteria

### Quantitative
- `batch_purchase_ships.go Handle()`: 141 lines → ~25 lines (82% reduction)
- `purchase_ship.go Handle()`: 121 lines → ~30 lines (75% reduction)
- `purchase_ship.go discoverNearestShipyard()`: 59 lines → ~20 lines (66% reduction)
- `purchase_ship.go convertShipDataToEntity()`: 58 lines → ~30 lines (48% reduction)
- `get_shipyard_listings.go Handle()`: 50 lines → ~25 lines (50% reduction)
- **Total main methods:** 429 lines → ~130 lines (70% reduction in handler complexity)
- **Eliminate duplication:** 45+ lines of duplicate shipyard validation code
- **Fix bugs:** Remove manual system symbol parsing (7 lines of error-prone code)

### Qualitative
- ✅ All inline comments replaced with self-documenting method names
- ✅ Each method has single, clear responsibility
- ✅ Code duplication eliminated (system symbol parsing, shipyard validation, credit checking)
- ✅ Use existing utilities instead of reinventing (shared.ExtractSystemSymbol)
- ✅ All tests pass unchanged
- ✅ Code is easier to understand and maintain
- ✅ Improved testability (smaller methods)
- ✅ Follows hexagonal architecture principles
- ✅ Consistent patterns across handlers

---

## Future Enhancements (Out of Scope)

These patterns were identified but are not part of this refactoring:

1. **Shared player token utility** - Reduce token fetching boilerplate across all handlers
2. **Shared shipyard validation utility** - Eliminate 45+ lines of duplicate code
3. **Ship data converter package** - Centralize API → domain transformations
4. **Ship reload helper** - Simplify post-command ship refreshing
5. **Domain service for credit checking** - If credit validation becomes more complex
6. **Builder pattern for purchase commands** - If command construction needs more flexibility

These can be addressed in future refactorings if patterns continue to appear across multiple packages.

---

## Notes

### Key Differences from Scouting Package

1. **More Helper Methods:** purchase_ship.go has 3 large methods (Handle + 2 helpers) vs scouting's single large Handle
2. **More Duplication:** Significant code sharing opportunities between batch and single purchase handlers
3. **Data Conversion:** Heavy API → domain transformation that could be shared across packages
4. **Bug Fix Included:** Manual system symbol parsing should use existing utility

### Testing Strategy

Since the shipyard package deals with actual purchases (credits spent), thorough testing is critical:

1. **Run BDD tests after each phase** - Don't wait until the end
2. **Verify partial success handling** - Batch purchase loop must handle errors gracefully
3. **Test auto-discovery logic** - Nearest shipyard calculation is complex
4. **Validate data conversion** - Ship entity creation has many fields

### Architectural Benefits

After refactoring:
- **Better CQRS adherence** - Handlers become thin orchestrators
- **Improved testability** - Small methods are easier to unit test
- **Clearer domain boundaries** - Data conversion separated from business logic
- **DRY principle** - Eliminate duplicate shipyard validation and credit checking
- **Maintainability** - Future changes affect smaller, focused methods
