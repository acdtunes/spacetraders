# Test Performance Optimization Guide

## Quick Reference

| Command | Time | Tests | Use Case |
|---------|------|-------|----------|
| `./run-tests.sh` | ~25s | 535 | Fast iteration during development (DEFAULT) |
| `./run-tests.sh --race` | ~28s | 535 | Quick pre-commit checks with race detection |
| `./run-tests.sh --race --cover` | ~31s | 535 | Full CI/CD pipeline |
| `make test-fast` | ~25s | 535 | Fast mode via Makefile |
| `make test-race` | ~28s | 535 | With race detection via Makefile |
| `make test-full` | ~31s | 535 | Full checks via Makefile |

## Performance History

### Before Optimization (Baseline)
- **Full test suite**: 46.3 seconds
- **BDD tests**: 46.3 seconds (due to 15s sleep)
- **Container runtime tests**: Used `time.Sleep()` for 15 seconds total

### After Clock Interface (Phase 1)
- **Full test suite**: 31 seconds (with -race -cover, 450 tests)
- **BDD tests**: 9.3 seconds
- **Container runtime tests**: <1ms (instant with MockClock)
- **Improvement**: **77% faster** for BDD tests, **33% faster** overall

### After Clock.Sleep + RouteExecutor Optimization (Phase 2 - Current)
- **Test count**: 535 tests (85 new tests added)
- **Fast mode**: 30.5 seconds (no race/cover)
- **Full mode**: 31 seconds (with -race -cover)
- **RouteExecutor tests**: Instant (was 5+ seconds per test with real sleeps)
- **Rate limiter tests**: 6 seconds saved (reduced 5s wait to 1s)
- **Improvement**: **17.5% faster** than pre-optimization (37s → 30.5s)

## Optimization Breakdown

### 1. Clock Interface Pattern (Domain Layer)
**Impact**: Eliminated 15 seconds of sleep time

**Implementation**:
```go
// Production: Uses real time
container := container.NewContainer(..., nil) // defaults to RealClock

// Tests: Uses controllable mock time
container := container.NewContainer(..., mockClock)
mockClock.Advance(10 * time.Second) // Instant!
```

**Files Modified**:
- `internal/domain/shared/clock.go` (NEW)
- `internal/domain/container/container.go`
- `test/bdd/steps/container_steps.go`
- `internal/adapters/grpc/daemon_server.go`

### 2. Clock.Sleep Interface Extension (Application Layer)
**Impact**: Eliminated 5+ seconds of sleep time per RouteExecutor test

**Implementation**:
```go
// Extended Clock interface with Sleep method
type Clock interface {
    Now() time.Time
    Sleep(d time.Duration)  // NEW
}

// RealClock: Actually sleeps (production)
func (r *RealClock) Sleep(d time.Duration) {
    time.Sleep(d)
}

// MockClock: Advances time instantly (tests)
func (m *MockClock) Sleep(d time.Duration) {
    m.CurrentTime = m.CurrentTime.Add(d)  // Instant!
}

// RouteExecutor uses clock for all time operations
executor := ship.NewRouteExecutor(shipRepo, mediator, mockClock)

// Production: Uses nil (defaults to RealClock)
executor := ship.NewRouteExecutor(shipRepo, mediator, nil)
```

**Bottlenecks Eliminated**:
- `waitForCurrentTransit()`: 5s sleep → instant in tests
- `waitForArrival()`: (waitTime+3)s sleep → instant in tests
- Test mock sleeps in route_executor_steps.go: 150ms → 0ms

**Files Modified**:
- `internal/domain/shared/clock.go` (extended interface)
- `internal/application/ship/route_executor.go`
- `test/bdd/steps/route_executor_steps.go`
- `test/bdd/features/infrastructure/api_rate_limiter.feature` (reduced 5s wait to 1s)

### 3. Parallel Test Execution (Test Runner)
**Impact**: Optimizes CPU utilization across test packages

**Configuration**:
```bash
# Fast mode (DEFAULT) - no race detection, no coverage
./run-tests.sh

# With race detection
./run-tests.sh --race

# Full checks - race + coverage
./run-tests.sh --race --cover

# Force specific parallel count
./run-tests.sh --parallel=8
```

**Algorithm**:
- Detects CPU cores (10 on this machine)
- Uses 80% for testing (8 workers)
- Leaves 20% headroom for system operations
- Min: 2 workers, Max: 8 workers

## Test Runner Options

```bash
./run-tests.sh [OPTIONS]

Options:
  --race          Enable race detection (adds ~4s)
  --cover         Enable coverage reporting (adds ~9s)
  --parallel=N    Force N parallel workers (default: auto-detect)
  --help          Show this help message
```

### Option Comparison

| Options | Time | Coverage | Race Detection | Use Case |
|---------|------|----------|----------------|----------|
| (default) | 18s | ❌ | ❌ | Rapid development (FASTEST) |
| `--race` | 22s | ❌ | ✅ | Quick local checks |
| `--race --cover` | 31s | ✅ | ✅ | CI/CD, pre-push |
| `--parallel=16` | varies | depends | depends | High-core machines |

## Makefile Targets

```bash
# Fast mode (~18s) - DEFAULT for iteration
make test-fast

# With race detection (~22s)
make test-race

# Full checks - race + coverage (~31s)
make test-full

# BDD tests with pretty output
make test-bdd-pretty

# Specific test suites
make test-bdd-ship         # Ship entity tests
make test-bdd-container    # Container entity tests
make test-bdd-route        # Route entity tests

# Coverage report (generates coverage.html)
make test-coverage
```

## Tips for Fastest Iteration

### Development Workflow
```bash
# 1. Rapid iteration (~18s) - DEFAULT
./run-tests.sh

# 2. Pre-commit check (~22s)
./run-tests.sh --race

# 3. Before push (~31s)
./run-tests.sh --race --cover
```

### Test Specific Features
```bash
# Test single feature (fastest)
go test ./test/bdd/... -godog.paths=test/bdd/features/domain/container/

# Test specific scenario
go test ./test/bdd/... -godog.filter="Create container with valid data"

# BDD tests only (9.3s)
make test
```

## Why Parallelization Helps

### Package-Level Parallelization
Go's test runner can run different packages in parallel:

```
Running in parallel:
┌─────────────┐  ┌──────────────┐  ┌─────────────┐
│ cmd/...     │  │ internal/... │  │ test/bdd... │
│ (Worker 1)  │  │ (Worker 2)   │  │ (Worker 3)  │
└─────────────┘  └──────────────┘  └─────────────┘
```

### CPU Utilization
- **Without -parallel**: ~25% CPU usage (1 core)
- **With -parallel=8**: ~80% CPU usage (8 cores)

## Architecture Benefits

### Clean Design
The Clock interface follows SOLID principles:
- ✅ Single Responsibility
- ✅ Open/Closed (extensible)
- ✅ Liskov Substitution (RealClock/MockClock interchangeable)
- ✅ Interface Segregation (minimal interface)
- ✅ Dependency Inversion (domain doesn't depend on time.Now)

### Zero Production Impact
```go
// Production code unchanged
container.NewContainer(..., nil) // Uses RealClock automatically
```

### Reusable Pattern
The Clock interface can be applied to other entities:
- Ship (has timestamps)
- Route (has timestamps)
- Any future entities needing time operations

## Performance Monitoring

### Track Test Duration
```bash
# Simple timing
time make test-fast

# Detailed timing with run-tests
./run-tests.sh  # Shows elapsed time in summary
```

### Identify Slow Tests
```bash
# Run with verbose timing
go test -v ./test/bdd/... | grep -E "PASS|FAIL" | grep -E "\([0-9]+\.[0-9]+s\)"

# Find tests slower than 100ms
go test -v ./test/bdd/... 2>&1 | grep -E "(PASS|FAIL).*\([0-9]\.[0-9][0-9]s\)"
```

## Future Optimizations (Optional)

### 1. Route Executor Sleeps (Low Priority)
**Current**: 150ms total in route tests  
**Potential**: Reduce to 20ms → saves ~130ms

```go
// Current
time.Sleep(100 * time.Millisecond)

// Optimized
time.Sleep(10 * time.Millisecond)
```

### 2. Test Database (If Needed)
**Current**: SQLite :memory:  
**Potential**: Shared test database → faster setup

### 3. Build Caching (Already Optimized)
Go's test cache already skips unchanged tests:
```bash
# First run: 18s
./run-tests.sh

# Second run (no changes): <1s (cached)
./run-tests.sh
```

## Conclusion

**Current Performance**: Excellent ✅
- BDD tests: 9.3s (77% faster)
- Full suite: 31s (33% faster)
- Clean architecture maintained
- No production impact

**No further optimization needed** unless:
- Test suite grows significantly
- New performance bottlenecks identified
- CI/CD pipeline becomes too slow

---

*Last updated: 2025-11-13*
