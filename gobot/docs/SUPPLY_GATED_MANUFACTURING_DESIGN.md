# Supply-Gated Manufacturing System Design

## Executive Summary

This document describes a major refactoring of the manufacturing system to address a fundamental financial flaw that caused **1.55M credits in losses** during a 1h 37m operation. The core issue is that the system purchased inputs from SCARCE markets (highest prices) instead of waiting for HIGH/ABUNDANT supply (lowest prices).

---

## Problem Analysis

### Root Cause: Market Spread Economics

SpaceTraders markets have a **50-58% buy-sell spread**:
- When you **BUY**, you pay the `sell_price` (what market charges you)
- When you **SELL**, you receive the `purchase_price` (what market pays you)

| Good | Buy Price | Sell Price | Spread Loss |
|------|-----------|------------|-------------|
| ELECTRONICS | 5,951 | 2,520 | **57.7%** |
| EQUIPMENT | 5,377 | 2,330 | **56.7%** |
| FABRICS | 3,925 | 1,707 | **56.5%** |
| FERTILIZERS | 457 | 197 | **56.9%** |

### Financial Impact

During the failed operation:
- **66 ACQUIRE_DELIVER tasks** lost **1,591,553 credits**
- **2 COLLECT_SELL tasks** earned **42,840 credits**
- **Net loss: 1,548,713 credits**

### Why It Failed

1. **No supply check on source markets**: Tasks were created immediately regardless of supply level
2. **Bought from SCARCE markets**: 83% of purchases were from SCARCE supply (highest prices)
3. **Production imbalance**: Too many input purchases, too few output sales
4. **Mixed task queues**: ACQUIRE_DELIVER and COLLECT_SELL competed for the same ships

---

## Solution Architecture

### Core Principles

1. **Supply-Gated Acquisition**: Only buy when source market has HIGH/ABUNDANT supply
2. **Raw Materials Exception**: Ores, crystals, gases bypass supply gating (can't be fabricated)
3. **Separated Pipelines**: Fabrication (limited) vs Collection (unlimited)
4. **Event-Driven Design**: MarketScan events trigger task creation, no polling
5. **Separate Queues**: ACQUIRE_DELIVER and COLLECT_SELL have independent queues

### Pipeline Types

```
┌─────────────────────────────────────────────────────────────────┐
│                    MANUFACTURING COORDINATOR                     │
├─────────────────────────────┬───────────────────────────────────┤
│   FABRICATION PIPELINES     │     COLLECTION PIPELINES          │
│   (max_pipelines limit)     │     (unlimited)                   │
├─────────────────────────────┼───────────────────────────────────┤
│ • ACQUIRE_DELIVER tasks     │ • COLLECT_SELL tasks              │
│ • Feed inputs to factories  │ • Collect factory output          │
│ • Supply-gated (HIGH/ABUN)  │ • Opportunity-driven              │
│ • Completes when factory    │ • Completes after each sale       │
│   output reaches HIGH       │ • Generates revenue               │
└─────────────────────────────┴───────────────────────────────────┘
```

---

## Detailed Design

### Phase 1: Pipeline Type Distinction

**Goal**: Track fabrication vs collection pipelines separately

**Domain Changes** (`internal/domain/manufacturing/pipeline.go`):

```go
type PipelineType string

const (
    PipelineTypeFabrication PipelineType = "FABRICATION"  // Counted toward max_pipelines
    PipelineTypeCollection  PipelineType = "COLLECTION"   // Unlimited
)

type ManufacturingPipeline struct {
    // ... existing fields ...
    pipelineType   PipelineType
}

func NewFabricationPipeline(...) *ManufacturingPipeline
func NewCollectionPipeline(...) *ManufacturingPipeline
func (p *ManufacturingPipeline) PipelineType() PipelineType
```

### Phase 2: Dual Queue Architecture

**Goal**: Separate queues for different task types

**New Component** (`internal/application/trading/services/dual_task_queue.go`):

```go
type DualTaskQueue struct {
    mu               sync.RWMutex
    fabricationQueue *TaskQueue  // ACQUIRE_DELIVER tasks
    collectionQueue  *TaskQueue  // COLLECT_SELL tasks
}

// Routes task to appropriate queue based on TaskType
func (q *DualTaskQueue) Enqueue(task *ManufacturingTask) error

// Returns ready ACQUIRE_DELIVER tasks sorted by priority
func (q *DualTaskQueue) GetReadyFabricationTasks() []*ManufacturingTask

// Returns ready COLLECT_SELL tasks sorted by priority
func (q *DualTaskQueue) GetReadyCollectionTasks() []*ManufacturingTask

// Combined for backward compatibility
func (q *DualTaskQueue) GetReadyTasks() []*ManufacturingTask
```

**Benefits**:
- Independent priority ordering per task type
- No starvation between task types
- Cleaner assignment logic

### Phase 3: Supply-Gated ACQUIRE_DELIVER

**Goal**: Only create tasks when source market has good supply

**Architecture**:

```
┌──────────────┐     ┌─────────────────────┐     ┌──────────────────┐
│ Scout Ships  │────▶│ MarketScan Event    │────▶│ SourceSupply     │
│ scan markets │     │ (waypoint, supply)  │     │ Monitor          │
└──────────────┘     └─────────────────────┘     └────────┬─────────┘
                                                          │
                     ┌────────────────────────────────────┘
                     ▼
        ┌────────────────────────────┐
        │ Check deferred tasks       │
        │ for this market            │
        ├────────────────────────────┤
        │ If supply HIGH/ABUNDANT:   │
        │ • Create ACQUIRE_DELIVER   │
        │ • Add to fabrication queue │
        │                            │
        │ If supply SCARCE/LIMITED:  │
        │ • Keep task deferred       │
        └────────────────────────────┘
```

**New Component** (`internal/application/trading/services/source_supply_monitor.go`):

```go
type DeferredAcquireDeliverTask struct {
    PipelineID     string
    PlayerID       int
    Good           string
    SourceMarket   string
    FactorySymbol  string
    IsRawMaterial  bool      // Bypass supply gating if true
    CreatedAt      time.Time
}

type SourceSupplyMonitor struct {
    marketRepo    market.MarketRepository
    taskRepo      manufacturing.TaskRepository
    taskQueue     *DualTaskQueue
    deferredTasks map[string][]*DeferredAcquireDeliverTask  // market -> tasks
}

// Called when a scout scans a market (event-driven)
func (m *SourceSupplyMonitor) OnMarketScanned(ctx context.Context, waypointSymbol string) error

// Adds task to deferred queue (raw materials execute immediately)
func (m *SourceSupplyMonitor) DeferTask(task *DeferredAcquireDeliverTask) error
```

**Raw Materials** (bypass supply gating - must be purchased):

| Category | Goods |
|----------|-------|
| Ores | IRON_ORE, COPPER_ORE, ALUMINUM_ORE, PLATINUM_ORE, GOLD_ORE, SILVER_ORE, URANITE_ORE, MERITIUM_ORE |
| Crystals | SILICON_CRYSTALS, QUARTZ_SAND |
| Ice/Gases | AMMONIA_ICE, LIQUID_HYDROGEN, LIQUID_NITROGEN, HYDROCARBON |
| Gems | DIAMONDS, PRECIOUS_STONES |
| Other | BOTANICAL_SPECIMENS, EXOTIC_MATTER, ICE_WATER |

**Detection** (from `internal/domain/goods/supply_chain.go`):
```go
func IsRawMaterial(good string) bool {
    _, exists := ExportToImportMap[good]
    return !exists  // No production recipe = raw material
}
```

### Phase 4: Collection Opportunity Discovery

**Goal**: Automatically discover and exploit factory output

**New Component** (`internal/application/trading/services/collection_opportunity_finder.go`):

```go
type CollectionOpportunity struct {
    Good           string
    FactorySymbol  string   // EXPORT market with HIGH/ABUNDANT
    SellMarket     string   // IMPORT market with SCARCE/LIMITED
    FactorySupply  string
    SellPrice      int
    ExpectedProfit int
}

type CollectionOpportunityFinder struct {
    marketRepo   market.MarketRepository
    pipelineRepo manufacturing.PipelineRepository
}

func (f *CollectionOpportunityFinder) FindOpportunities(
    ctx context.Context,
    systemSymbol string,
    playerID int,
) ([]*CollectionOpportunity, error)
```

**Algorithm**:
1. Query all markets in system from database
2. Find EXPORT markets with HIGH/ABUNDANT supply (factories with goods to collect)
3. Find IMPORT markets with SCARCE/LIMITED supply + WEAK activity (buyers)
4. Match exports to imports, calculate expected profit
5. Skip goods with active collection pipeline (prevent duplicates)
6. Return opportunities sorted by profit

**Integration** (modify `pipeline_lifecycle_manager.go`):
```go
func (m *PipelineLifecycleManager) ScanAndCreatePipelines(...) (int, error) {
    // Only count FABRICATION pipelines toward limit
    fabricationCount := m.countFabricationPipelines()

    if fabricationCount < params.MaxPipelines {
        // Create fabrication pipelines as before
        m.scanForFabricationOpportunities(ctx, params)
    }

    // ALWAYS scan for collection opportunities (unlimited)
    m.scanForCollectionOpportunities(ctx, params)
}
```

### Phase 5: Supply-Aware Market Selection

**Goal**: Find best source markets considering supply level

**Enhancement** (`internal/application/goods/services/market_locator.go`):

```go
func (l *MarketLocator) FindExportMarketWithGoodSupply(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*MarketLocatorResult, error) {
    markets, err := l.marketRepo.FindMarketsExporting(ctx, good, systemSymbol, playerID)
    if err != nil {
        return nil, err
    }

    // Filter to HIGH or ABUNDANT supply only
    var goodSupplyMarkets []*MarketData
    for _, m := range markets {
        if m.Supply == "HIGH" || m.Supply == "ABUNDANT" {
            goodSupplyMarkets = append(goodSupplyMarkets, m)
        }
    }

    if len(goodSupplyMarkets) == 0 {
        return nil, nil  // No market with good supply
    }

    // Score: ABUNDANT > HIGH, then by price (lower is better)
    sort.Slice(goodSupplyMarkets, func(i, j int) bool {
        // ABUNDANT beats HIGH
        if goodSupplyMarkets[i].Supply != goodSupplyMarkets[j].Supply {
            return goodSupplyMarkets[i].Supply == "ABUNDANT"
        }
        // Otherwise, lower price wins
        return goodSupplyMarkets[i].SellPrice < goodSupplyMarkets[j].SellPrice
    })

    return &MarketLocatorResult{
        WaypointSymbol: goodSupplyMarkets[0].WaypointSymbol,
        Price:          goodSupplyMarkets[0].SellPrice,
        Supply:         goodSupplyMarkets[0].Supply,
    }, nil
}
```

### Phase 6: Database Schema Changes

**Migration** (`migrations/XXX_add_pipeline_type.up.sql`):

```sql
-- Add pipeline type column
ALTER TABLE manufacturing_pipelines
ADD COLUMN pipeline_type VARCHAR(20) NOT NULL DEFAULT 'FABRICATION';

-- Add indexes for efficient queries
CREATE INDEX idx_pipelines_type ON manufacturing_pipelines(pipeline_type);
CREATE INDEX idx_pipelines_type_status ON manufacturing_pipelines(pipeline_type, status);

-- Add deferred tasks table for supply-gated acquisition
CREATE TABLE deferred_acquire_deliver_tasks (
    id VARCHAR(64) PRIMARY KEY,
    pipeline_id VARCHAR(64) NOT NULL,
    player_id INTEGER NOT NULL,
    good VARCHAR(64) NOT NULL,
    source_market VARCHAR(64) NOT NULL,
    factory_symbol VARCHAR(64) NOT NULL,
    is_raw_material BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    FOREIGN KEY (pipeline_id) REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    FOREIGN KEY (player_id) REFERENCES players(id)
);

CREATE INDEX idx_deferred_tasks_market ON deferred_acquire_deliver_tasks(source_market);
CREATE INDEX idx_deferred_tasks_player ON deferred_acquire_deliver_tasks(player_id);
```

**Repository Updates** (`internal/domain/manufacturing/ports.go`):

```go
type PipelineRepository interface {
    // ... existing methods ...

    // Count only fabrication pipelines (for max_pipelines limit)
    CountActiveFabricationPipelines(ctx context.Context, playerID int) (int, error)

    // Check if collection pipeline exists for product
    FindActiveCollectionForProduct(ctx context.Context, playerID int, productGood string) (*ManufacturingPipeline, error)
}

type DeferredTaskRepository interface {
    // Save deferred task
    Save(ctx context.Context, task *DeferredAcquireDeliverTask) error

    // Find deferred tasks by source market
    FindBySourceMarket(ctx context.Context, playerID int, waypointSymbol string) ([]*DeferredAcquireDeliverTask, error)

    // Delete when task is created
    Delete(ctx context.Context, taskID string) error
}
```

---

## Pipeline Completion Criteria

### Fabrication Pipeline (ACQUIRE_DELIVER)

**Completes when**: Factory output reaches HIGH or ABUNDANT supply

**Rationale**:
- HIGH/ABUNDANT output indicates factory is well-fed
- More deliveries won't produce more output immediately
- Frees up the pipeline slot for other products

**Lifecycle**:
```
Created → Delivering Inputs → Factory Producing → Output HIGH → COMPLETED
                                                       ↓
                                              (Collection pipeline
                                               discovers opportunity)
```

### Collection Pipeline (COLLECT_SELL)

**Completes when**: Single sale transaction completes

**Rationale**:
- Each collection is a discrete profit opportunity
- Unlimited pipelines allow parallel collection from multiple factories
- Opportunity finder creates new pipelines as opportunities appear

**Lifecycle**:
```
Opportunity Found → Pipeline Created → COLLECT_SELL Task → Sale Complete → COMPLETED
```

---

## Flow Diagrams

### ACQUIRE_DELIVER Flow (Supply-Gated)

```
Pipeline Created
       │
       ▼
┌──────────────────┐
│ For each input:  │
│ Check source     │
│ market supply    │
└────────┬─────────┘
         │
    ┌────┴────┐
    │         │
    ▼         ▼
HIGH/ABUN   SCARCE/LIMITED
    │         │
    ▼         ▼
Create      Defer Task
Task        (wait for
    │       MarketScan)
    ▼         │
Add to        │
Queue         │
    │         │
    ▼         │
Ship      ◀───┘
Assigned     (when supply
    │         improves)
    ▼
Execute:
Buy → Deliver
    │
    ▼
Task Complete
```

### COLLECT_SELL Flow (Opportunity-Driven)

```
Opportunity Finder
(periodic scan)
       │
       ▼
┌──────────────────┐
│ Find factories   │
│ with HIGH/ABUN   │
│ output supply    │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ Find buyers with │
│ SCARCE/LIMITED   │
│ demand           │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ Calculate profit │
│ Skip duplicates  │
└────────┬─────────┘
         │
         ▼
Create Collection
Pipeline + Task
       │
       ▼
Add to Collection
Queue
       │
       ▼
Ship Assigned
       │
       ▼
Execute:
Collect → Sell
       │
       ▼
Pipeline COMPLETED
(revenue generated)
```

---

## Implementation Order

| Phase | Description | Dependencies |
|-------|-------------|--------------|
| 1 | Pipeline Type | None (foundation) |
| 2 | Dual Queue | Phase 1 |
| 3 | Supply-Gated ACQUIRE_DELIVER | Phase 1, 2 |
| 4 | Collection Opportunity Finder | Phase 1, 2 |
| 5 | Supply-Aware Market Selection | Phase 3 |
| 6 | Database Schema | Phase 1 |

---

## Files to Modify

| File | Changes |
|------|---------|
| `internal/domain/manufacturing/pipeline.go` | Add PipelineType enum and field |
| `internal/domain/manufacturing/ports.go` | Add repository methods |
| `internal/application/trading/services/task_queue.go` | Reference for DualTaskQueue |
| `internal/application/trading/services/dual_task_queue.go` | **NEW**: Dual queue implementation |
| `internal/application/trading/services/source_supply_monitor.go` | **NEW**: Supply-gated task creation |
| `internal/application/trading/services/collection_opportunity_finder.go` | **NEW**: Collection discovery |
| `internal/application/trading/services/pipeline_planner.go` | Defer tasks when supply not good |
| `internal/application/trading/services/manufacturing/pipeline_lifecycle_manager.go` | Separate pipeline counting |
| `internal/application/trading/services/manufacturing/task_assignment_manager.go` | Use DualTaskQueue |
| `internal/application/goods/services/market_locator.go` | Add supply-aware selection |
| `internal/adapters/persistence/manufacturing_pipeline_repository.go` | Add pipeline_type support |
| `migrations/XXX_add_pipeline_type.up.sql` | **NEW**: Schema migration |

---

## Expected Outcomes

### Before (Current System)
- Buy inputs at any supply level (often SCARCE = highest prices)
- 50-58% loss on every buy-sell cycle
- Mixed queues cause task type starvation
- Collection gated artificially, not opportunity-driven

### After (New System)
- Buy only when supply is HIGH/ABUNDANT (lowest prices)
- Raw materials bypass gating (no alternative)
- Separate queues prevent starvation
- Collection discovered automatically when profitable
- Fabrication limited, collection unlimited
- Revenue from collection funds fabrication

### Financial Projection

Based on the loss analysis:
- **Current spread loss**: 50-58% per transaction
- **With HIGH/ABUNDANT supply**: Spread reduced to ~20-30% (lower buy prices)
- **Collection-first strategy**: Generate revenue before spending on fabrication

---

## Appendix: Supply Level Economics

### SpaceTraders Market Pricing

| Supply Level | Price Drift | Buy Recommendation |
|--------------|-------------|---------------------|
| SCARCE | +30-70% | NEVER BUY |
| LIMITED | +15-30% | AVOID |
| MODERATE | 0-15% | CAUTIOUS |
| HIGH | -10 to 0% | GOOD |
| ABUNDANT | -20 to -10% | BEST |

### Activity Level Impact

| Activity | Price Volatility | Trade Recommendation |
|----------|-----------------|----------------------|
| WEAK | Low (5-14%) | Safe to trade |
| RESTRICTED | Very Low (2-5%) | Best for selling |
| GROWING | High (33%) | Avoid |
| STRONG | Medium (20%) | Cautious |

*Data derived from actual market behavior analysis during the failed operation.*
