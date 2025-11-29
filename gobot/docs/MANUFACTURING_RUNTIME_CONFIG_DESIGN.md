# Manufacturing Runtime Configuration Design

## Overview

This document describes the design for adding runtime configuration management to the manufacturing coordinator. This enables operators to dynamically adjust pipeline capacity and supply thresholds without restarting the coordinator.

## Goals

1. **Dynamic Pipeline Capacity** - Add/remove pipeline slots at runtime
2. **Configurable Collection Thresholds** - Parametrize minimum supply for COLLECT_SELL operations
3. **Configurable Acquisition Thresholds** - Parametrize minimum supply for ACQUIRE_DELIVER operations
4. **Live Updates** - Changes take effect immediately on running coordinators
5. **Persistence** - Configuration survives daemon restarts
6. **Per-Coordinator Scope** - Each coordinator can have independent settings

## Current State Analysis

### Hardcoded Values

| Location | Current Value | Purpose |
|----------|---------------|---------|
| `factory_state.go:19` | `const RequiredSupplyLevel = "HIGH"` | Minimum supply to mark factory ready for collection |
| `supply_level.go:50-52` | `AllowsPurchase()` blocks only SCARCE | Minimum supply to allow purchases |
| `task_readiness_spec.go:82-85` | Returns `SupplyLevelAbundant` | Minimum supply to START a COLLECT_SELL task |
| `task_readiness_spec.go:90-95` | Returns `SupplyLevelHigh` | Minimum supply to EXECUTE a COLLECT_SELL task |

### Current Container Config Storage

Container configuration is stored as JSON in the `containers.config` column:

```go
map[string]interface{}{
    "system_symbol": systemSymbol,
    "min_price":     minPrice,
    "max_workers":   maxWorkers,
    "max_pipelines": maxPipelines,
    "min_balance":   minBalance,
    "container_id":  containerID,
    "mode":          "parallel_task_based",
    "strategy":      strategy,
}
```

This existing mechanism will be extended to store the new configurable thresholds.

---

## Architecture

### Component Diagram

```
                                    ┌─────────────────────────────────────────┐
                                    │              CLI Layer                  │
                                    │                                         │
                                    │  manufacturing config show --container  │
                                    │  manufacturing config set --container   │
                                    └────────────────┬────────────────────────┘
                                                     │ gRPC
                                                     ▼
┌────────────────────────────────────────────────────────────────────────────────┐
│                              Daemon Server                                      │
│                                                                                 │
│  ┌─────────────────────┐    ┌─────────────────────────────────────────────┐   │
│  │ ConfigUpdateChans   │◄───│ UpdateManufacturingConfig RPC Handler       │   │
│  │ map[containerID]    │    │                                             │   │
│  │   chan Config       │    │ 1. Load current config from DB              │   │
│  └──────────┬──────────┘    │ 2. Apply partial updates                    │   │
│             │               │ 3. Persist to containers.config             │   │
│             │               │ 4. Send to ConfigUpdateChan                 │   │
│             ▼               └─────────────────────────────────────────────┘   │
│  ┌─────────────────────┐                                                       │
│  │ Running Coordinator │                                                       │
│  │                     │                                                       │
│  │ select {            │                                                       │
│  │   case cfg :=       │                                                       │
│  │     <-configChan:   │                                                       │
│  │     applyConfig()   │                                                       │
│  │ }                   │                                                       │
│  └─────────────────────┘                                                       │
└────────────────────────────────────────────────────────────────────────────────┘
```

### Data Flow

1. **CLI Command** → User runs `manufacturing config set --container X --max-pipelines 5`
2. **gRPC Request** → DaemonClient sends `UpdateManufacturingConfigRequest`
3. **Handler** → Loads current config, applies updates, persists to DB
4. **Notification** → Sends new config through registered channel
5. **Coordinator** → Receives config, updates internal state
6. **Immediate Effect** → Next pipeline scan uses new `max_pipelines`

---

## Domain Model

### ManufacturingConfig Entity

**File: `internal/domain/manufacturing/config.go`**

```go
// ManufacturingConfig holds runtime-configurable manufacturing parameters.
// This is a value object - all update methods return new instances.
type ManufacturingConfig struct {
    // Pipeline capacity
    maxPipelines int
    maxWorkers   int

    // Supply thresholds for COLLECT operations (factory output)
    // StartSupply: Minimum to assign a ship to the task
    // ExecuteSupply: Minimum to actually perform the collection on arrival
    collectMinStartSupply   SupplyLevel
    collectMinExecuteSupply SupplyLevel

    // Supply thresholds for ACQUIRE operations (source market)
    acquireMinStartSupply   SupplyLevel
    acquireMinExecuteSupply SupplyLevel

    // Other settings
    minPurchasePrice int
    strategy         string
}
```

#### Default Values

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| `maxPipelines` | 3 | Conservative to avoid overcommitting resources |
| `maxWorkers` | 5 | Balances throughput with API rate limits |
| `collectMinStartSupply` | ABUNDANT | Provides buffer for supply drops during transit |
| `collectMinExecuteSupply` | HIGH | More lenient on arrival - supply may have dropped |
| `acquireMinStartSupply` | MODERATE | Allows purchasing from moderately stocked markets |
| `acquireMinExecuteSupply` | LIMITED | Permits acquiring even from LIMITED markets |

#### Methods

```go
// Construction
func NewManufacturingConfig() *ManufacturingConfig
func NewManufacturingConfigFromMap(config map[string]interface{}) *ManufacturingConfig

// Persistence
func (c *ManufacturingConfig) ToMap() map[string]interface{}

// Immutable updates (return new instances)
func (c *ManufacturingConfig) WithMaxPipelines(max int) *ManufacturingConfig
func (c *ManufacturingConfig) WithMaxWorkers(max int) *ManufacturingConfig
func (c *ManufacturingConfig) WithCollectSupplyThresholds(start, execute SupplyLevel) *ManufacturingConfig
func (c *ManufacturingConfig) WithAcquireSupplyThresholds(start, execute SupplyLevel) *ManufacturingConfig

// Getters
func (c *ManufacturingConfig) MaxPipelines() int
func (c *ManufacturingConfig) MaxWorkers() int
func (c *ManufacturingConfig) CollectMinStartSupply() SupplyLevel
func (c *ManufacturingConfig) CollectMinExecuteSupply() SupplyLevel
func (c *ManufacturingConfig) AcquireMinStartSupply() SupplyLevel
func (c *ManufacturingConfig) AcquireMinExecuteSupply() SupplyLevel
```

### SupplyLevel Enhancement

**File: `internal/domain/manufacturing/supply_level.go`**

Add comparison method:

```go
// MeetsOrExceeds returns true if this supply level is >= the given minimum.
// Order: SCARCE(1) < LIMITED(2) < MODERATE(3) < HIGH(4) < ABUNDANT(5)
func (s SupplyLevel) MeetsOrExceeds(min SupplyLevel) bool {
    return s.Order() >= min.Order()
}
```

---

## Updated Components

### TaskReadinessSpecification

**File: `internal/domain/manufacturing/task_readiness_spec.go`**

Transform from stateless to configurable:

```go
type TaskReadinessSpecification struct {
    config *ManufacturingConfig
    mu     sync.RWMutex // Thread-safe config updates
}

// NewTaskReadinessSpecificationWithConfig creates spec with custom config
func NewTaskReadinessSpecificationWithConfig(config *ManufacturingConfig) *TaskReadinessSpecification {
    return &TaskReadinessSpecification{config: config}
}

// UpdateConfig atomically updates the configuration
func (s *TaskReadinessSpecification) UpdateConfig(config *ManufacturingConfig) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.config = config
}

// GetMinimumStartSupply returns configurable threshold
func (s *TaskReadinessSpecification) GetMinimumStartSupply(taskType TaskType) SupplyLevel {
    s.mu.RLock()
    defer s.mu.RUnlock()

    switch taskType {
    case TaskTypeCollectSell:
        return s.config.CollectMinStartSupply()
    case TaskTypeAcquireDeliver:
        return s.config.AcquireMinStartSupply()
    default:
        return SupplyLevelModerate
    }
}

// GetMinimumExecuteSupply returns configurable threshold
func (s *TaskReadinessSpecification) GetMinimumExecuteSupply(taskType TaskType) SupplyLevel {
    s.mu.RLock()
    defer s.mu.RUnlock()

    switch taskType {
    case TaskTypeCollectSell:
        return s.config.CollectMinExecuteSupply()
    case TaskTypeAcquireDeliver:
        return s.config.AcquireMinExecuteSupply()
    default:
        return SupplyLevelLimited
    }
}
```

### FactoryState

**File: `internal/domain/manufacturing/factory_state.go`**

Remove hardcoded constant and use config:

```go
// REMOVE: const RequiredSupplyLevel = "HIGH"

// FactoryStateTracker now holds a config reference
type FactoryStateTracker struct {
    mu     sync.RWMutex
    states map[string]*FactoryState
    config *ManufacturingConfig // NEW: Shared config reference
}

// SetConfig updates the config for all factory states
func (t *FactoryStateTracker) SetConfig(config *ManufacturingConfig) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.config = config
}

// checkReadyForCollection uses configurable threshold
func (f *FactoryState) checkReadyForCollection(minSupply SupplyLevel) {
    currentLevel := ParseSupplyLevel(f.currentSupply)

    if currentLevel.MeetsOrExceeds(minSupply) {
        if !f.readyForCollection {
            f.readyForCollection = true
            now := time.Now()
            f.readyAt = &now
        }
    } else {
        f.readyForCollection = false
        f.readyAt = nil
    }
}
```

---

## gRPC API

### Protocol Buffer Definitions

**File: `pkg/proto/daemon/daemon.proto`**

```protobuf
// ManufacturingConfig represents the current configuration state
message ManufacturingConfig {
    int32 max_pipelines = 1;
    int32 max_workers = 2;
    string collect_min_start_supply = 3;   // SCARCE|LIMITED|MODERATE|HIGH|ABUNDANT
    string collect_min_execute_supply = 4;
    string acquire_min_start_supply = 5;
    string acquire_min_execute_supply = 6;
    int32 min_purchase_price = 7;
    string strategy = 8;
}

// UpdateManufacturingConfigRequest allows partial updates
// Only set fields will be applied
message UpdateManufacturingConfigRequest {
    string container_id = 1;
    int32 player_id = 2;

    // Optional fields - only set fields are applied
    optional int32 max_pipelines = 3;
    optional int32 max_workers = 4;
    optional string collect_min_start_supply = 5;
    optional string collect_min_execute_supply = 6;
    optional string acquire_min_start_supply = 7;
    optional string acquire_min_execute_supply = 8;
}

message UpdateManufacturingConfigResponse {
    bool success = 1;
    string message = 2;
    ManufacturingConfig current_config = 3;
}

message GetManufacturingConfigRequest {
    string container_id = 1;
    int32 player_id = 2;
}

message GetManufacturingConfigResponse {
    ManufacturingConfig config = 1;
    string container_status = 2;  // RUNNING, STOPPED, etc.
}

// Add to DaemonService
service DaemonService {
    // ... existing RPCs ...

    // Get current configuration for a manufacturing coordinator
    rpc GetManufacturingConfig(GetManufacturingConfigRequest)
        returns (GetManufacturingConfigResponse);

    // Update configuration (partial updates supported)
    rpc UpdateManufacturingConfig(UpdateManufacturingConfigRequest)
        returns (UpdateManufacturingConfigResponse);
}
```

### Service Implementation

**File: `internal/adapters/grpc/daemon_service_impl.go`**

```go
func (s *daemonServiceImpl) UpdateManufacturingConfig(
    ctx context.Context,
    req *pb.UpdateManufacturingConfigRequest,
) (*pb.UpdateManufacturingConfigResponse, error) {
    // 1. Load current config from container
    container, err := s.containerRepo.FindByID(ctx, req.ContainerId, int(req.PlayerId))
    if err != nil {
        return nil, status.Errorf(codes.NotFound, "container not found: %v", err)
    }

    // 2. Parse existing config
    currentConfig := manufacturing.NewManufacturingConfigFromMap(container.Config())

    // 3. Apply partial updates (only set fields)
    if req.MaxPipelines != nil {
        currentConfig = currentConfig.WithMaxPipelines(int(*req.MaxPipelines))
    }
    if req.MaxWorkers != nil {
        currentConfig = currentConfig.WithMaxWorkers(int(*req.MaxWorkers))
    }
    if req.CollectMinStartSupply != nil {
        // Validate supply level
        startSupply := manufacturing.ParseSupplyLevel(*req.CollectMinStartSupply)
        execSupply := currentConfig.CollectMinExecuteSupply()
        if req.CollectMinExecuteSupply != nil {
            execSupply = manufacturing.ParseSupplyLevel(*req.CollectMinExecuteSupply)
        }
        currentConfig = currentConfig.WithCollectSupplyThresholds(startSupply, execSupply)
    }
    // ... similar for acquire thresholds ...

    // 4. Persist updated config to database
    if err := s.containerRepo.UpdateConfig(ctx, req.ContainerId, int(req.PlayerId),
        currentConfig.ToMap()); err != nil {
        return nil, status.Errorf(codes.Internal, "failed to persist config: %v", err)
    }

    // 5. Notify running coordinator (if active)
    if err := s.daemon.SendConfigUpdate(req.ContainerId, currentConfig); err != nil {
        // Log but don't fail - config is persisted, will apply on restart
        log.Printf("WARN: Failed to notify running coordinator: %v", err)
    }

    return &pb.UpdateManufacturingConfigResponse{
        Success:       true,
        Message:       "Configuration updated successfully",
        CurrentConfig: configToProto(currentConfig),
    }, nil
}
```

---

## CLI Commands

### Command Structure

```
manufacturing
├── scan          (existing)
├── start         (existing)
└── config        (NEW)
    ├── show      Show current configuration
    └── set       Update configuration
```

### Show Command

```bash
./bin/spacetraders manufacturing config show \
    --container parallel-mfg-X1-YZ19-abc123 \
    --player-id 12
```

**Output:**
```
Manufacturing Coordinator Configuration
=======================================
Container:     parallel-mfg-X1-YZ19-abc123
Status:        RUNNING
System:        X1-YZ19

Pipeline Settings:
  Max Pipelines:        5
  Max Workers:          10

Collection Thresholds (COLLECT_SELL tasks):
  Min Start Supply:     ABUNDANT
  Min Execute Supply:   HIGH

Acquisition Thresholds (ACQUIRE_DELIVER tasks):
  Min Start Supply:     MODERATE
  Min Execute Supply:   LIMITED

Other Settings:
  Min Purchase Price:   1000
  Strategy:             prefer-fabricate
```

### Set Command

```bash
# Increase pipeline capacity
./bin/spacetraders manufacturing config set \
    --container parallel-mfg-X1-YZ19-abc123 \
    --player-id 12 \
    --max-pipelines 5

# Conservative collection (require ABUNDANT supply)
./bin/spacetraders manufacturing config set \
    --container parallel-mfg-X1-YZ19-abc123 \
    --player-id 12 \
    --collect-start-supply ABUNDANT \
    --collect-execute-supply HIGH

# Aggressive acquisition (allow LIMITED markets)
./bin/spacetraders manufacturing config set \
    --container parallel-mfg-X1-YZ19-abc123 \
    --player-id 12 \
    --acquire-start-supply LIMITED \
    --acquire-execute-supply LIMITED

# Multiple settings at once
./bin/spacetraders manufacturing config set \
    --container parallel-mfg-X1-YZ19-abc123 \
    --player-id 12 \
    --max-pipelines 5 \
    --max-workers 8 \
    --collect-start-supply HIGH
```

### Flag Definitions

| Flag | Type | Description |
|------|------|-------------|
| `--container` | string | Container ID (required) |
| `--player-id` | int | Player ID (required) |
| `--max-pipelines` | int | Maximum concurrent pipelines |
| `--max-workers` | int | Maximum concurrent workers |
| `--collect-start-supply` | string | Min supply to START collect tasks |
| `--collect-execute-supply` | string | Min supply to EXECUTE collect tasks |
| `--acquire-start-supply` | string | Min supply to START acquire tasks |
| `--acquire-execute-supply` | string | Min supply to EXECUTE acquire tasks |

**Valid supply levels:** SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT

---

## Coordinator Integration

### Config Channel Setup

**File: `internal/adapters/grpc/container_ops_manufacturing.go`**

```go
func (s *DaemonServer) ParallelManufacturingCoordinator(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    // ... other params ...
) (string, error) {
    // Create buffered channel (1 to avoid blocking sender)
    configUpdateChan := make(chan *manufacturing.ManufacturingConfig, 1)

    // Register BEFORE starting coordinator
    s.RegisterConfigUpdateChannel(containerID, configUpdateChan)

    // Build command with channel
    cmd := &commands.RunParallelManufacturingCoordinatorCommand{
        // ... existing fields ...
        ConfigUpdateChan: configUpdateChan,
    }

    // Start with deferred cleanup
    go func() {
        defer s.UnregisterConfigUpdateChannel(containerID)
        runner.Start()
    }()

    return containerID, nil
}
```

### Coordinator Main Loop

**File: `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go`**

```go
func (h *RunParallelManufacturingCoordinatorHandler) Handle(
    ctx context.Context,
    request common.Request,
) (common.Response, error) {
    cmd := request.(*RunParallelManufacturingCoordinatorCommand)

    // Initialize config from command parameters
    config := h.buildInitialConfig(cmd)

    // Create configurable services
    readinessSpec := manufacturing.NewTaskReadinessSpecificationWithConfig(config)
    h.factoryTracker.SetConfig(config)

    for {
        select {
        case <-ctx.Done():
            return &RunParallelManufacturingCoordinatorResponse{}, nil

        case newConfig := <-cmd.ConfigUpdateChan:
            if newConfig != nil {
                config = newConfig
                h.applyConfigUpdate(config, readinessSpec)
                logger.Log("INFO", "Configuration updated", config.ToMap())
            }

        case <-opportunityScanTicker.C:
            // Uses config.MaxPipelines()
            h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
                MaxPipelines: config.MaxPipelines(),
                // ...
            })

        // ... other cases ...
        }
    }
}

func (h *RunParallelManufacturingCoordinatorHandler) applyConfigUpdate(
    config *manufacturing.ManufacturingConfig,
    readinessSpec *manufacturing.TaskReadinessSpecification,
) {
    // Update readiness specification
    readinessSpec.UpdateConfig(config)

    // Update factory tracker
    h.factoryTracker.SetConfig(config)

    // Update any other services that depend on config
}
```

---

## Persistence

### Container Config Schema

The configuration is stored as JSON in the existing `containers.config` column:

```json
{
    "system_symbol": "X1-YZ19",
    "min_price": 1000,
    "max_workers": 5,
    "max_pipelines": 3,
    "min_balance": 0,
    "container_id": "parallel-mfg-X1-YZ19-abc123",
    "mode": "parallel_task_based",
    "strategy": "prefer-fabricate",

    // NEW fields
    "collect_min_start_supply": "ABUNDANT",
    "collect_min_execute_supply": "HIGH",
    "acquire_min_start_supply": "MODERATE",
    "acquire_min_execute_supply": "LIMITED"
}
```

### Recovery on Restart

When the daemon restarts and recovers containers:

1. Load container from database
2. Parse config JSON including new fields
3. Create `ManufacturingConfig` with `NewManufacturingConfigFromMap()`
4. Pass config to coordinator command
5. Coordinator uses persisted values instead of defaults

---

## Supply Level Reference

| Level | Order | Description | Use Cases |
|-------|-------|-------------|-----------|
| SCARCE | 1 | Nearly depleted | Never purchase (crashes market) |
| LIMITED | 2 | Low stock | Careful acquisition only |
| MODERATE | 3 | Normal stock | Standard operations |
| HIGH | 4 | Good stock | Factory ready for collection |
| ABUNDANT | 5 | Plentiful | Safe buffer for all operations |

### Threshold Semantics

**Start Supply** - Checked when assigning a ship to a task:
- Higher threshold = More conservative (wait for better conditions)
- Lower threshold = More aggressive (start tasks sooner)

**Execute Supply** - Checked when ship arrives to perform operation:
- Higher threshold = May abort if conditions degraded
- Lower threshold = More likely to complete despite changes

**Recommended Configurations:**

| Profile | Collect Start | Collect Exec | Acquire Start | Acquire Exec |
|---------|--------------|--------------|---------------|--------------|
| Conservative | ABUNDANT | HIGH | MODERATE | MODERATE |
| Balanced (default) | ABUNDANT | HIGH | MODERATE | LIMITED |
| Aggressive | HIGH | MODERATE | LIMITED | LIMITED |

---

## Files Modified

| File | Change |
|------|--------|
| `internal/domain/manufacturing/config.go` | **CREATE** - ManufacturingConfig entity |
| `internal/domain/manufacturing/supply_level.go` | ADD `MeetsOrExceeds()` method |
| `internal/domain/manufacturing/factory_state.go` | REMOVE constant, use config |
| `internal/domain/manufacturing/task_readiness_spec.go` | Make configurable |
| `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go` | Config channel, live updates |
| `internal/adapters/grpc/daemon_server.go` | Config channel registry |
| `internal/adapters/grpc/daemon_service_impl.go` | gRPC handlers |
| `internal/adapters/grpc/container_ops_manufacturing.go` | Channel setup |
| `internal/adapters/cli/manufacturing.go` | Add config subcommands |
| `internal/adapters/cli/daemon_client.go` | Client methods |
| `pkg/proto/daemon/daemon.proto` | Messages and RPCs |

---

## Testing Strategy

### Unit Tests

1. **ManufacturingConfig**
   - Construction with defaults
   - Construction from map
   - Immutable update methods
   - ToMap serialization

2. **SupplyLevel.MeetsOrExceeds()**
   - All ordering combinations
   - Edge cases (same level, unknown)

3. **TaskReadinessSpecification**
   - Returns configured thresholds
   - Thread-safe config updates

### Integration Tests

1. **Config Persistence**
   - Update config via gRPC
   - Restart daemon
   - Verify config loaded from DB

2. **Live Update Propagation**
   - Start coordinator
   - Update config via CLI
   - Verify coordinator receives update
   - Verify behavior changes

### Manual Testing

```bash
# 1. Start coordinator with defaults
./bin/spacetraders manufacturing start --system X1-YZ19 --player-id 12

# 2. Show initial config
./bin/spacetraders manufacturing config show --container <id> --player-id 12

# 3. Update config
./bin/spacetraders manufacturing config set --container <id> --player-id 12 \
    --max-pipelines 5 --collect-start-supply HIGH

# 4. Verify update in logs
./bin/spacetraders container logs <id> --player-id 12 | grep "Configuration updated"

# 5. Restart daemon and verify persistence
pkill spacetraders-daemon
./bin/spacetraders-daemon &
./bin/spacetraders manufacturing config show --container <id> --player-id 12
# Should show max_pipelines=5, collect_start_supply=HIGH
```

---

## Future Enhancements

1. **Config Presets** - Named configurations (conservative, aggressive, balanced)
2. **Auto-tuning** - Adjust thresholds based on market conditions
3. **Config History** - Track configuration changes over time
4. **Validation Rules** - Ensure start >= execute supply levels
5. **Global Defaults** - Player-wide default settings
