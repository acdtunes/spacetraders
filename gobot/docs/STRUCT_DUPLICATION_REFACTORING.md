# Struct Naming & Architecture Cleanup Plan

**Date:** 2025-11-20
**Priority:** Medium
**Estimated Effort:** 1-2 hours
**Risk Level:** Low

## Current Problems

### Problem #1: ContainerInfo Naming Collision (3 structs with same name)

**Impact:** Developer confusion, unclear which struct to use where

**Three different structs named `ContainerInfo` exist:**

1. **Domain Port DTO** (`internal/domain/daemon/ports.go:14`)
   - 4 fields: ID, PlayerID (int), Status, Type
   - Purpose: Daemon client communication

2. **Persistence Query Result** (`internal/adapters/persistence/container_repository.go:180`)
   - 3 fields: ID, ContainerType, Status
   - Purpose: Internal query result
   - **PROBLEM:** Same name, different fields, creates confusion

3. **CLI/Protobuf DTO** (`internal/adapters/cli/daemon_client.go:100`)
   - 10 fields including timestamps, iterations, metadata
   - Purpose: Mirrors protobuf message
   - PlayerID is int32 (protobuf requirement)

---

### Problem #2: PlayerID Type Inconsistency

**Impact:** Scattered type conversions, potential confusion

- Domain layer uses `int` (per CLAUDE.md standards)
- Protobuf/gRPC uses `int32` (gRPC requirement)
- Conversions happen inline throughout code

---

## Action Plan

### Task 1: Rename Persistence ContainerInfo → ContainerSummary

**Priority:** HIGH
**Effort:** 15 minutes
**Risk:** LOW (internal to persistence layer)

**Files to Modify:**

1. **`internal/adapters/persistence/container_repository.go`**

Line 180 - Rename struct:
```go
// Before
type ContainerInfo struct {
    ID            string
    ContainerType string
    Status        string
}

// After
type ContainerSummary struct {
    ID            string
    ContainerType string
    Status        string
}
```

Update all internal usages (should be ~3-5 references in same file)

**Test:** Run `make test-bdd-container` to verify

---

### Task 2: Add Documentation Comments

**Priority:** MEDIUM
**Effort:** 10 minutes
**Risk:** NONE (documentation only)

**Files to Modify:**

1. **`internal/domain/daemon/ports.go`** (line 14)
```go
// ContainerInfo represents container metadata for daemon client communication.
// This is a lightweight DTO used at the gRPC boundary.
type ContainerInfo struct {
    ID       string
    PlayerID int  // Domain standard int type
    Status   string
    Type     string
}
```

2. **`internal/adapters/persistence/container_repository.go`** (line 180)
```go
// ContainerSummary is an internal query result struct for simplified container lookups.
// For full container data, use the Container domain entity.
type ContainerSummary struct {  // Renamed from ContainerInfo in Task 1
    ID            string
    ContainerType string
    Status        string
}
```

3. **`internal/adapters/cli/daemon_client.go`** (line 100)
```go
// ContainerInfo mirrors the protobuf ContainerInfo message for CLI display.
// This struct includes all fields needed for user-facing container information.
// Note: PlayerID is int32 per protobuf requirements (converted from domain int).
type ContainerInfo struct {
    ContainerID      string
    ContainerType    string
    Status           string
    PlayerID         int32  // Protobuf int32 (convert from domain int)
    CreatedAt        string
    UpdatedAt        string
    CurrentIteration int32
    MaxIterations    int32
    RestartCount     int32
    Metadata         string
}
```

---

### Task 3: Centralize PlayerID Type Conversions

**Priority:** LOW (optional optimization)
**Effort:** 15 minutes
**Risk:** LOW

**New File:** `internal/adapters/grpc/type_converters.go`

```go
package grpc

// PlayerID conversion helpers for domain <-> protobuf boundary

// ToProtobufPlayerID converts domain int to protobuf int32
func ToProtobufPlayerID(domainID int) int32 {
    return int32(domainID)
}

// FromProtobufPlayerID converts protobuf int32 to domain int
func FromProtobufPlayerID(protoID int32) int {
    return int(protoID)
}
```

**Files to Update:**
- Search for inline `int32(playerID)` conversions in `internal/adapters/grpc/`
- Replace with helper function calls
- Estimated: 5-10 occurrences

**Test:** Run `make test` to verify all conversions still work

---

## Implementation Steps

### Step 1: Rename ContainerInfo in Persistence Layer (Task 1)
```bash
# 1. Edit file
vim internal/adapters/persistence/container_repository.go

# 2. Search and replace within file
# Change: type ContainerInfo struct
# To:     type ContainerSummary struct

# 3. Update all references in same file

# 4. Test
make test-bdd-container
```

### Step 2: Add Documentation (Task 2)
```bash
# Edit each file and add comments above struct definitions
vim internal/domain/daemon/ports.go
vim internal/adapters/persistence/container_repository.go
vim internal/adapters/cli/daemon_client.go

# No tests needed (documentation only)
```

### Step 3: (Optional) Centralize Conversions (Task 3)
```bash
# 1. Create new file
vim internal/adapters/grpc/type_converters.go

# 2. Add conversion helpers

# 3. Find all conversion sites
grep -r "int32(.*playerID)" internal/adapters/grpc/

# 4. Replace with helper calls

# 5. Test
make test
```

---

## Testing Strategy

### After Task 1 (Rename)
```bash
# Container tests
make test-bdd-container

# Full BDD suite
make test
```

### After Task 2 (Documentation)
```bash
# No tests needed - documentation only
# Verify with: go doc internal/domain/daemon ContainerInfo
```

### After Task 3 (Conversions)
```bash
# Full test suite to verify type conversions
make test

# Verify builds
make build
```

---

## Success Criteria

### Task 1 Success:
- [x] No struct named `ContainerInfo` in persistence layer
- [x] New `ContainerSummary` struct exists
- [x] All container BDD tests pass
- [x] No compilation errors

### Task 2 Success:
- [x] Each ContainerInfo/ContainerSummary has clear doc comment
- [x] Comments explain purpose and scope
- [x] Comments note type differences (int vs int32)

### Task 3 Success:
- [x] `type_converters.go` file exists with helper functions
- [x] No inline `int32()` conversions in gRPC layer
- [x] All tests pass
- [x] All binaries build

---

## Rollback Plan

### If Task 1 Breaks Tests:
```bash
git checkout internal/adapters/persistence/container_repository.go
```

### If Task 3 Causes Issues:
```bash
git checkout internal/adapters/grpc/
rm internal/adapters/grpc/type_converters.go
```

---

## Timeline

**Recommended Schedule:**

**Session 1 (30 minutes):**
- Task 1: Rename ContainerInfo → ContainerSummary
- Run tests
- Commit: `refactor: rename persistence ContainerInfo to ContainerSummary`

**Session 2 (15 minutes):**
- Task 2: Add documentation comments
- Commit: `docs: add clarifying comments to ContainerInfo variants`

**Session 3 (Optional, 20 minutes):**
- Task 3: Centralize type conversions
- Run full test suite
- Commit: `refactor: centralize PlayerID type conversions`

**Total Time:** 45-65 minutes active development

---

## Optional Future Work

### Consider These Naming Conventions:

**For DTOs at architectural boundaries:**
- Suffix with `DTO` for explicitness: `ContainerInfoDTO`
- Suffix with `Data` for API structures: `ContainerData`
- Suffix with `Info` for read-only summaries: `ContainerInfo`

**Example convention:**
```go
// Domain port (daemon communication)
type ContainerInfoDTO struct { ... }

// Persistence query result
type ContainerSummary struct { ... }

// Protobuf mirror (CLI)
type ContainerInfo struct { ... }  // Matches protobuf message name
```

**Decision:** Not included in current plan to minimize scope. Consider for future refactoring.

---

## Questions Before Starting

1. **Backward Compatibility:** Are there external consumers of `ContainerInfo` in persistence layer?
   - Answer: Internal only, safe to rename

2. **Test Coverage:** Do we have adequate tests for container operations?
   - Answer: Yes, BDD tests in `test/bdd/features/domain/container/`

3. **Team Coordination:** Is anyone else working in these files?
   - Answer: Check before starting

---

## Document Metadata

**Version:** 1.0 (Action Plan)
**Created:** 2025-11-20
**Target Completion:** 2025-11-20 or next available session
**Owner:** To be assigned
**Status:** Ready to implement
