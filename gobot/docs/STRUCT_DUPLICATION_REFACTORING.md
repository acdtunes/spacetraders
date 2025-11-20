# Struct Duplication Refactoring Plan

**Date:** 2025-11-20
**Status:** In Progress
**Estimated Effort:** 7-11 hours (Phase 2.3 + Phase 3.1 completed: ~2 hours)
**Risk Level:** Medium

## Executive Summary

This document outlines a comprehensive plan to eliminate struct duplication across the domain and application layers. Analysis identified **7 major categories of duplication** affecting **25+ struct definitions**, representing opportunities to reduce code by 200-300 lines while improving architectural clarity.

**Completed:**
- **Phase 2.3**: CargoItem consolidation - 3 struct variants reduced to 1 canonical `shared.CargoItem`
- **Phase 3.1**: Market/TradeGood Domain Unification - Migrated from `domain/trading/` to `domain/market/` as canonical implementation

---

## Findings

### 1. ✅ COMPLETED: CargoItem (3 variants) - Consolidated to `shared.CargoItem`

**Previous State:** 3 duplicate CargoItem definitions across domain/shared, domain/mining, and domain/navigation
**Resolution:** All code now uses `shared.CargoItem` as the single source of truth
**Files Modified:** 5 files (mining/cargo_transfer_request.go, navigation/ports.go, api/client.go, api/ship_repository.go, test helpers)
**Status:** ✅ Complete

---

### 2. ✅ COMPLETED: Market/TradeGood Domain Duplication (2 complete implementations) - Consolidated to `domain/market`

**Current State:**

Two parallel implementations of the same domain concept:

#### Implementation A: `domain/trading/` (Older, Simpler)

**`domain/trading/market.go` (lines 6-18):**
```go
type TradeGood struct {
    Symbol        string
    Supply        string
    SellPrice     int
    PurchasePrice int
    TradeVolume   int
}

type Market struct {
    waypointSymbol string
    tradeGoods     []TradeGood
}
```

**Usage:** MarketRepository interface, contract workflows

#### Implementation B: `domain/market/` (Newer, More Sophisticated)

**`domain/market/trade_good.go` (lines 12-19):**
```go
type TradeGood struct {
    symbol        string        // private field
    supply        *string       // nullable
    activity      *string       // nullable (NEW)
    purchasePrice int
    sellPrice     int
    tradeVolume   int
}
```

**`domain/market/market.go` (lines 9-13):**
```go
type Market struct {
    waypointSymbol string
    tradeGoods     []TradeGood
    lastUpdated    time.Time    // NEW - timestamp tracking
}
```

**Features:**
- Immutability guarantees
- Timestamp tracking
- Activity monitoring
- Better validation

**Usage:** Mining operations, market scanning

**Issues:**
- Two complete domain packages doing the same job
- Split usage creates confusion about which to use
- `domain/market/` has superior design but lower adoption

**Previous State:** Two parallel implementations of Market/TradeGood across domain/trading and domain/market
**Resolution:** Migrated all code to use `domain/market` as the single source of truth, deleted `domain/trading` package
**Files Modified:**
- Created `internal/domain/market/ports.go` with MarketRepository interface
- Updated `internal/adapters/persistence/market_repository.go` to return `market.*` types
- Renamed `internal/adapters/persistence/trading_market_repository_adapter.go` to use `market.*` types
- Updated 6 application layer files (contract handlers, mining handlers, trading handlers)
- Updated `cmd/spacetraders-daemon/main.go` to use `NewMarketRepositoryAdapter`
- Deleted `internal/domain/trading/` package entirely
**Tests:** All BDD tests pass, binaries build successfully
**Status:** ✅ Complete

---

### 3. ✅ COMPLETED: Container (2 variants with naming conflict) - Renamed to `ContainerInfo`

**Previous State:**

| Location | Type | Purpose |
|----------|------|---------|
| `domain/container/container.go` (line 60) | Full entity (16 fields) | Complete lifecycle management |
| `domain/daemon/ports.go` (line 14) | DTO (4 fields) | Daemon client communication |

**Resolution:** Renamed `daemon.Container` to `daemon.ContainerInfo`, standardized `PlayerID` to `int`
**Files Modified:**
- `internal/domain/daemon/ports.go` - Renamed struct and fixed type
- `internal/adapters/grpc/daemon_client_local.go` - Updated return types and construction
- `internal/adapters/grpc/daemon_client_grpc.go` - Updated return types and construction
- `test/helpers/mock_daemon_client.go` - Updated type alias and all references
**Tests:** Pending verification
**Status:** ✅ Complete

**Previous Issues (RESOLVED):**
- ~~Same name, different purposes → confusion~~ ✅ Resolved by rename
- ~~`PlayerID` type inconsistency: `uint` vs `int`~~ ✅ Resolved by standardizing on `int`
- ~~DTO should have distinct name to clarify architectural boundary~~ ✅ Resolved with `ContainerInfo` name

---

### 4. MODERATE: RouteSegment (3 variants)

**Current State:**

| Location | Fields | Purpose |
|----------|--------|---------|
| `domain/navigation/route.go` (21-29) | Rich domain object with Waypoint pointers | Domain entity |
| `application/mining/mining_coordinator_command.go` (39-45) | String symbols, string FlightMode | DTO for mining |
| `adapters/cli/daemon_client.go` (75-81) | String symbols, string FlightMode | DTO for CLI |

**Issues:**
- Variants 2 and 3 are **100% IDENTICAL**
- Both used for route serialization/display
- No shared DTO definition

**Impact:** MEDIUM - duplicate DTOs in different layers

---

### 5. MODERATE: Payment/Delivery Structs (3 identical sets)

**Current State:**

| Location | Structs | Purpose |
|----------|---------|---------|
| `domain/contract/contract.go` (8-18) | Payment, Delivery | Domain value objects |
| `domain/contract/ports.go` (29-39) | PaymentData, DeliveryData | Contract persistence DTOs |
| `infrastructure/ports/api_client.go` (76-86) | PaymentData, DeliveryData | API communication DTOs |

**Issues:**
- Sets 2 and 3 are **100% IDENTICAL**
- Complete duplication of DTO definitions
- No code reuse between API client and contract ports

**Impact:** HIGH - affects contract workflows and API integration

---

### 6. MODERATE: ContractTerms (3 variants)

**Current State:**

| Location | Lines | Differences |
|----------|-------|-------------|
| `domain/contract/contract.go` | 20-25 | Uses domain types (Payment, Delivery) |
| `domain/contract/ports.go` | 22-28 | Uses DTOs (PaymentData, DeliveryData) |
| `infrastructure/ports/api_client.go` | 69-74 | Uses DTOs, different field ordering |

**Issues:**
- Variants 2 and 3 are functionally identical
- Resolves automatically with Payment/Delivery cleanup

**Impact:** MEDIUM - tied to Issue #5

---

### 7. MINOR: ShipRoute (2 identical variants)

**Current State:**

| Location | Fields | Purpose |
|----------|--------|---------|
| `application/mining/mining_coordinator_command.go` (48-54) | 6 fields | Dry-run route display |
| `adapters/cli/daemon_client.go` (83-89) | 6 fields | Dry-run route display |

**Issues:**
- **100% IDENTICAL** structures
- Same purpose, different architectural layers
- Should be in common location

**Impact:** LOW - complete duplicate but limited scope

---

## Refactoring Plan

### Phase 1: Quick Wins (Low Risk, 1-2 hours)

#### 1.1 Rename Container DTO
**File:** `internal/domain/daemon/ports.go:14`

**Changes:**
```go
// Before
type Container struct {
    ID       string
    PlayerID uint
    Status   string
    Type     string
}

// After
type ContainerInfo struct {
    ID       string
    PlayerID int  // Changed from uint for consistency
    Status   string
    Type     string
}
```

**Files to Update:**
- `internal/domain/daemon/ports.go` - rename struct
- `internal/adapters/cli/daemon_client.go` - update references
- `internal/adapters/grpc/daemon_server.go` - update references

**Tests:** BDD tests in `test/bdd/features/domain/container/`

---

#### 1.2 Consolidate ShipRoute
**Action:** Create shared DTO in application layer

**New File:** `internal/application/common/route_dto.go`
```go
package common

// ShipRouteDTO represents a complete route for a ship including all segments
type ShipRouteDTO struct {
    ShipSymbol string
    ShipType   string
    Segments   []RouteSegmentDTO
    TotalFuel  int
    TotalTime  int // seconds
}
```

**Files to Delete From:**
- `internal/application/mining/mining_coordinator_command.go:48-54`
- `internal/adapters/cli/daemon_client.go:83-89`

**Files to Update:**
- Import and use `common.ShipRouteDTO` in both locations

**Tests:** Mining coordinator BDD tests

---

#### 1.3 Create Shared RouteSegmentDTO
**Action:** Add to `internal/application/common/route_dto.go`

```go
// RouteSegmentDTO represents a single segment of a route for serialization
type RouteSegmentDTO struct {
    From       string
    To         string
    FlightMode string
    FuelCost   int
    TravelTime int // seconds
}
```

**Add Conversion Method:** `internal/domain/navigation/route.go`
```go
// ToDTO converts a domain RouteSegment to a DTO for serialization
func (rs *RouteSegment) ToDTO() RouteSegmentDTO {
    return RouteSegmentDTO{
        From:       rs.FromWaypoint.Symbol,
        To:         rs.ToWaypoint.Symbol,
        FlightMode: string(rs.FlightMode),
        FuelCost:   rs.FuelRequired,
        TravelTime: rs.TravelTime,
    }
}
```

**Files to Delete From:**
- `internal/application/mining/mining_coordinator_command.go:39-45`
- `internal/adapters/cli/daemon_client.go:75-81`

**Files to Update:**
- Update imports to use `common.RouteSegmentDTO`
- Update route serialization code to use `ToDTO()` method

**Tests:** Route entity BDD tests, navigation handler tests

---

### Phase 2: DTO Cleanup (Medium Risk, 2-3 hours)

#### 2.1 Consolidate Payment/Delivery DTOs
**Action:** Delete duplicates from API client, use contract ports versions

**Files to Modify:**

1. **Delete from:** `internal/infrastructure/ports/api_client.go`
   - Remove `PaymentData` struct (lines 76-79)
   - Remove `DeliveryData` struct (lines 81-86)

2. **Update imports in:** `internal/infrastructure/ports/api_client.go`
   ```go
   import (
       "github.com/staffordwilliams/spacetraders-gobot/internal/domain/contract"
   )
   ```

3. **Update all references:**
   - Replace `PaymentData` → `contract.PaymentData`
   - Replace `DeliveryData` → `contract.DeliveryData`

**Files Affected:**
- `internal/infrastructure/ports/api_client.go`
- `internal/adapters/api/spacetraders_client.go` (API client implementation)

**Tests:** Contract BDD tests, API integration tests

---

#### 2.2 Consolidate ContractTerms DTO
**Action:** Tied to 2.1, resolves automatically

**Files to Modify:**
1. **Delete from:** `internal/infrastructure/ports/api_client.go`
   - Remove `ContractTermsData` struct (lines 69-74)

2. **Update references:**
   - Replace with `contract.ContractTermsData` from ports.go

**Tests:** Contract workflow BDD tests

---

### Phase 3: Major Refactoring (High Impact, 4-6 hours)

#### 3.1 Market/TradeGood Domain Unification
**Action:** Migrate from `domain/trading/` to `domain/market/` as canonical implementation

**Justification:**
- `domain/market/` has superior design:
  - Immutability guarantees
  - Timestamp tracking (`lastUpdated`)
  - Activity monitoring
  - Better validation
  - Private fields with getters
- Already used by mining operations (newest code)

**Migration Steps:**

##### Step 1: Update Repository Interface
**File:** `internal/domain/trading/ports.go` → Move to `internal/domain/market/ports.go`

```go
// Before (domain/trading/ports.go)
type MarketRepository interface {
    GetMarket(waypointSymbol string) (*Market, error)
    SaveMarket(market *Market) error
}

// After (domain/market/ports.go) - add to existing file
type MarketRepository interface {
    GetMarket(waypointSymbol string) (*Market, error)
    SaveMarket(market *Market) error
    // Existing methods already here
}
```

##### Step 2: Update Repository Implementation
**File:** `internal/adapters/persistence/market_repository.go`

```go
// Update imports
import (
    "github.com/staffordwilliams/spacetraders-gobot/internal/domain/market"  // Changed
)

// Update all method signatures to use market.Market and market.TradeGood
```

##### Step 3: Update Contract Domain
**Files:**
- `internal/domain/contract/contract.go`
- `internal/application/contract/commands/*.go`

```go
// Update imports
import (
    "github.com/staffordwilliams/spacetraders-gobot/internal/domain/market"  // Changed from trading
)

// Update all references from trading.Market → market.Market
```

##### Step 4: Update API Client
**File:** `internal/adapters/api/spacetraders_client.go`

```go
// Update market-related methods to return market.Market instead of trading.Market
func (c *SpaceTradersClient) GetMarket(waypointSymbol string) (*market.Market, error) {
    // Implementation
}
```

##### Step 5: Delete Old Implementation
**Files to Delete:**
- `internal/domain/trading/market.go` (entire file)
- `internal/domain/trading/ports.go` (if empty after moving interface)
- Potentially delete entire `internal/domain/trading/` package if no other content

##### Step 6: Update All Imports
**Search and Replace Across Codebase:**
```bash
# Find all imports
grep -r "domain/trading" internal/

# Replace with
"domain/market"
```

**Files Likely Affected (estimate 10-15 files):**
- Contract command handlers
- Mining operation workflows
- API client implementations
- Repository implementations
- CLI commands for market operations

**Tests to Update:**
- Contract BDD tests
- Market scanning tests
- Mining operation tests
- Any tests that create or manipulate Market/TradeGood entities

**Migration Checklist:**
- [x] Move MarketRepository interface to domain/market/ports.go
- [x] Update market_repository.go implementation
- [x] Update contract domain imports
- [x] Update mining operation imports
- [x] Update API client return types
- [x] Update all command handlers
- [x] Run full BDD test suite
- [x] Delete domain/trading/ package
- [x] Update documentation

---

## Testing Strategy

### After Each Phase

1. **Run relevant BDD test suites:**
   ```bash
   # Phase 1
   make test-bdd-container
   make test-bdd-route
   make test-bdd-navigate

   # Phase 2
   make test-bdd-ship
   go test ./test/bdd/... -v -godog.filter="cargo"

   # Phase 3
   make test  # Full suite
   ```

2. **Manual verification:**
   - Build all binaries: `make build`
   - Run daemon: `make run-daemon`
   - Test CLI commands: `./bin/spacetraders ship list`

3. **Regression checks:**
   - Mining operations still work
   - Contract workflows still work
   - Navigation still works
   - Market data retrieval still works

---

## Risk Assessment

### Phase 1: LOW RISK
- Isolated changes
- Mostly naming/organizational
- Easy to rollback

### Phase 2: MEDIUM RISK
- Touches DTO serialization boundaries
- API integration points involved
- Requires careful testing of data flow

### Phase 3: HIGH RISK
- Affects core domain model
- Multiple bounded contexts involved
- Requires extensive test updates
- Potential for runtime errors if missed references

---

## Rollback Strategy

### Git Strategy
Create feature branch for each phase:
```bash
git checkout -b refactor/phase1-quick-wins
# Complete Phase 1
git commit -m "refactor: Phase 1 - Container rename, consolidate routes"

git checkout -b refactor/phase2-dto-cleanup
# Complete Phase 2
git commit -m "refactor: Phase 2 - Consolidate DTOs"

git checkout -b refactor/phase3-market-unification
# Complete Phase 3
git commit -m "refactor: Phase 3 - Unify market domain"
```

### If Issues Arise
- Each phase is independently committable
- Can rollback individual phases via `git revert`
- Can pause between phases to stabilize

---

## Success Metrics

### Quantitative
- **Structs eliminated:** 6 of 10-12 completed (CargoItem variants: 3, Market/TradeGood: 2, Container: 1 renamed)
- **Files modified:** 18 of ~20 (persistence, application layer, daemon main, adapter implementations)
- **Packages deleted:** 1 (`internal/domain/trading`)
- **Lines of code reduced:** ~160 of 200-300 target
- **Test pass rate:** Pending verification (Container refactoring)

### Qualitative
- Clearer architectural boundaries (domain vs DTO)
- Single source of truth for each concept
- Reduced cognitive load (fewer duplicate definitions)
- Better adherence to hexagonal architecture principles

---

## Implementation Timeline

### Recommended Schedule

**Week 1:**
- Day 1-2: Phase 1 (Quick Wins)
- Day 3: Phase 2.1-2.2 (Payment/Delivery/ContractTerms)
- Day 4: Phase 2.3 (CargoItem)
- Day 5: Testing and stabilization

**Week 2:**
- Day 1-2: Phase 3 (Market unification) - preparation and migration
- Day 3: Phase 3 (continued) - test updates
- Day 4: Full regression testing
- Day 5: Documentation updates and final review

**Total Calendar Time:** 2 weeks (10 business days)
**Active Development Time:** 7-11 hours

---

## Post-Refactoring

### Documentation Updates
- Update `CLAUDE.md` with new canonical struct locations
- Update `ARCHITECTURE.md` if it exists
- Add migration notes for future developers

### Code Review Checklist
- [ ] No duplicate struct definitions remain
- [ ] All imports updated correctly
- [ ] All BDD tests passing
- [ ] No breaking changes to public APIs
- [ ] Documentation reflects new structure
- [ ] No orphaned files or dead code

---

## Questions & Considerations

### Before Starting

1. **Backward Compatibility:** Are there external consumers of these structs?
2. **API Versioning:** Do any structs affect external API contracts?
3. **Database Migrations:** Do any changes affect persisted data structures?
4. **Team Coordination:** Are other developers working in affected areas?

### Design Decisions to Validate

1. **CargoItem:** Confirm that all three use cases can use the full struct (Symbol, Name, Description, Units)
2. **Market Unification:** Validate that `domain/market/` implementation meets all use cases from `domain/trading/`
3. **DTO Naming:** Confirm naming convention for DTOs (e.g., `ContainerInfo` vs `ContainerDTO`)

---

## Appendix: File Reference

### Files to Create
1. `internal/application/common/route_dto.go` (new file)

### Files to Delete
1. `internal/domain/trading/market.go` (Phase 3)
2. Potentially entire `internal/domain/trading/` package (Phase 3)

### Files to Modify (Complete List)

#### Phase 1 (3-5 files)
- `internal/domain/daemon/ports.go`
- `internal/adapters/cli/daemon_client.go`
- `internal/adapters/grpc/daemon_server.go`
- `internal/application/mining/mining_coordinator_command.go`
- `internal/domain/navigation/route.go`

#### Phase 2 (6-8 files)
- `internal/infrastructure/ports/api_client.go`
- `internal/adapters/api/spacetraders_client.go`
- `internal/domain/mining/cargo_transfer_request.go`
- `internal/domain/mining/mining_operation.go`
- `internal/domain/navigation/ports.go`
- `internal/adapters/persistence/ship_repository.go`
- Contract command handlers (multiple files)

#### Phase 3 (10-15 files)
- `internal/domain/market/ports.go`
- `internal/adapters/persistence/market_repository.go`
- `internal/domain/contract/contract.go`
- `internal/application/contract/commands/*.go`
- `internal/adapters/api/spacetraders_client.go`
- All files importing `domain/trading`
- Contract BDD test files
- Market BDD test files
- Mining operation test files

**Total Estimated Files:** ~20-25 files

---

## References

- Hexagonal Architecture principles (CLAUDE.md)
- CQRS pattern implementation (ARCHITECTURE.md)
- Domain-Driven Design value object patterns
- Go best practices for struct composition

---

**Document Version:** 1.0
**Last Updated:** 2025-11-20
**Author:** Claude Code Analysis
**Review Status:** Pending stakeholder review
