# Construction Pipeline Design

> **Status**: Design Document
> **Created**: 2025-12-03
> **Author**: Claude Code
> **Related**: Manufacturing System, Goods Factory

## Executive Summary

This document describes the design for a **Construction Pipeline** system that enables automated production and delivery of materials to jump gate construction sites. The system extends the existing manufacturing pipeline infrastructure with new task types, pipeline management, and delivery mechanisms specific to construction projects.

**Use Case**: The agent TORWIND has 24 ships stuck in system X1-FB5 because the jump gate X1-FB5-I61 is under construction. The gate requires:
- 1,600 FAB_MATS
- 400 ADVANCED_CIRCUITRY
- 1 QUANTUM_STABILIZERS (already fulfilled)

This system automates the entire supply chain from raw materials to final delivery.

---

## Table of Contents

1. [Problem Statement](#problem-statement)
2. [Goals and Non-Goals](#goals-and-non-goals)
3. [Supply Chain Analysis](#supply-chain-analysis)
4. [Architecture Overview](#architecture-overview)
5. [Domain Layer Changes](#domain-layer-changes)
6. [Application Layer Changes](#application-layer-changes)
7. [API Integration](#api-integration)
8. [CLI Interface](#cli-interface)
9. [Persistence Changes](#persistence-changes)
10. [Execution Flow](#execution-flow)
11. [Configuration Examples](#configuration-examples)
12. [Implementation Phases](#implementation-phases)
13. [Testing Strategy](#testing-strategy)
14. [Open Questions](#open-questions)

---

## Problem Statement

### Current Situation

The manufacturing system supports two pipeline types:
1. **FABRICATION** - Produces goods at factories for sale (profit-driven)
2. **COLLECTION** - Mines and sells raw materials (profit-driven)

Both are designed for profit generation through market sales. Neither supports:
- Delivering goods to construction sites
- Tracking delivery quotas (e.g., "deliver 1600 units total")
- Construction API integration (`POST /my/ships/{shipSymbol}/construction/supply`)

### Required Capabilities

1. **Construction Delivery**: Deliver produced goods to construction sites via the construction supply API
2. **Quota Tracking**: Track total delivered vs. required quantities
3. **Configurable Supply Depth**: Control how deep into the supply chain to go (full production vs. buy intermediates)
4. **Multi-Material Coordination**: Support concurrent pipelines for different materials (FAB_MATS and ADVANCED_CIRCUITRY)

---

## Goals and Non-Goals

### Goals

1. Create a `CONSTRUCTION` pipeline type for construction material delivery
2. Implement `DELIVER_TO_CONSTRUCTION` task type for construction site delivery
3. Support configurable supply chain depth (0=full, 1=raw only, 2=intermediates)
4. Provide CLI commands for construction pipeline management
5. Track delivery progress toward construction requirements
6. Integrate with existing manufacturing infrastructure

### Non-Goals

1. Automatic construction site discovery (user specifies site)
2. Multi-system construction coordination (single system per pipeline)
3. Automatic market selection for purchases (use existing logic)
4. Profit tracking for construction deliveries (not profit-driven)
5. Partial delivery handling (deliver full cargo loads)

---

## Supply Chain Analysis

### FAB_MATS Supply Chain

```
FAB_MATS (target: 1,600 units)
├── IRON (refined metal)
│   └── IRON_ORE (mineable raw material)
└── QUARTZ_SAND (mineable raw material)
```

**Production Path**:
1. Acquire IRON_ORE (mine or buy)
2. Acquire QUARTZ_SAND (mine or buy)
3. Deliver IRON_ORE to smelter → IRON
4. Deliver IRON + QUARTZ_SAND to factory → FAB_MATS
5. Deliver FAB_MATS to construction site

### ADVANCED_CIRCUITRY Supply Chain

```
ADVANCED_CIRCUITRY (target: 400 units)
├── ELECTRONICS (intermediate)
│   ├── SILICON_CRYSTALS (mineable)
│   └── COPPER (refined metal)
│       └── COPPER_ORE (mineable)
└── MICROPROCESSORS (intermediate)
    ├── SILICON_CRYSTALS (mineable)
    └── COPPER (refined metal)
        └── COPPER_ORE (mineable)
```

**Production Path**:
1. Acquire COPPER_ORE (mine or buy)
2. Acquire SILICON_CRYSTALS (mine or buy)
3. Deliver COPPER_ORE to smelter → COPPER
4. Deliver COPPER + SILICON_CRYSTALS to factory → ELECTRONICS
5. Deliver COPPER + SILICON_CRYSTALS to factory → MICROPROCESSORS
6. Deliver ELECTRONICS + MICROPROCESSORS to factory → ADVANCED_CIRCUITRY
7. Deliver ADVANCED_CIRCUITRY to construction site

### Supply Chain Depth Configuration

| Depth | Description | FAB_MATS Example |
|-------|-------------|------------------|
| 0 | Full self-production | Mine IRON_ORE, Mine QUARTZ_SAND, Smelt IRON, Fabricate FAB_MATS |
| 1 | Buy raw materials only | Buy IRON_ORE, Buy QUARTZ_SAND, Smelt IRON, Fabricate FAB_MATS |
| 2 | Buy intermediates | Buy IRON, Buy QUARTZ_SAND, Fabricate FAB_MATS |
| 3 | Buy final product | Buy FAB_MATS (no fabrication) |

---

## Architecture Overview

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                         CLI Layer                                    │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ construction.go                                               │   │
│  │ - construction start --site --depth (auto-discovers materials)│   │
│  │ - construction status --site                                  │   │
│  │ - construction list                                           │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      Application Layer                               │
│  ┌──────────────────────────┐  ┌─────────────────────────────────┐  │
│  │ ConstructionPipelinePlanner │  │ DeliverToConstructionExecutor  │  │
│  │ - Plan()                    │  │ - Execute()                     │  │
│  │ - walkSupplyChain()         │  │ - collectFromFactory()         │  │
│  │ - createTasks()             │  │ - deliverToConstruction()      │  │
│  └──────────────────────────┘  └─────────────────────────────────┘  │
│                                                                      │
│  ┌──────────────────────────┐  ┌─────────────────────────────────┐  │
│  │ TaskExecutorRegistry      │  │ PipelineOrchestrator            │  │
│  │ + DELIVER_TO_CONSTRUCTION │  │ + CONSTRUCTION pipeline type    │  │
│  └──────────────────────────┘  └─────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Domain Layer                                  │
│  ┌──────────────────────────┐  ┌─────────────────────────────────┐  │
│  │ Pipeline                  │  │ Task                            │  │
│  │ + type CONSTRUCTION       │  │ + type DELIVER_TO_CONSTRUCTION │  │
│  │ + constructionSite        │  │ + constructionSite             │  │
│  │ + targetQuantity          │  │ + targetGood                   │  │
│  │ + deliveredQuantity       │  │                                 │  │
│  │ + supplyChainDepth        │  │                                 │  │
│  └──────────────────────────┘  └─────────────────────────────────┘  │
│                                                                      │
│  ┌──────────────────────────┐                                       │
│  │ ConstructionSite          │                                       │
│  │ + waypointSymbol          │                                       │
│  │ + materials[]             │                                       │
│  │ + isComplete              │                                       │
│  └──────────────────────────┘                                       │
└─────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│                       Adapter Layer                                  │
│  ┌──────────────────────────┐  ┌─────────────────────────────────┐  │
│  │ SpaceTradersClient        │  │ ManufacturingPipelineRepository │  │
│  │ + SupplyConstruction()    │  │ + construction fields mapping  │  │
│  └──────────────────────────┘  └─────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

### Data Flow

```
1. User Request
   ./bin/spacetraders construction start --site X1-FB5-I61 --depth 1
   ↓
2. CLI parses command → gRPC to daemon (StartOrResumeConstructionPipeline)
   ↓
3. IDEMPOTENCY CHECK (in Daemon)
   - Query: FindByConstructionSite(ctx, "X1-FB5-I61", playerID)
   ↓
   ┌─────────────────────────────────────────────────────────────┐
   │ Pipeline EXISTS?                                            │
   │                                                              │
   │   YES → Resume existing pipeline                            │
   │         - Return existing pipeline ID                       │
   │         - Continue from current progress                    │
   │                                                              │
   │   NO  → Create new pipeline                                 │
   │         ↓                                                   │
   │         AUTO-DISCOVERY (fetch from API)                     │
   │         GET /systems/{sys}/waypoints/{wp}/construction      │
   │         ↓                                                   │
   │         Parse materials: FAB_MATS:1600, ADV_CIRC:400       │
   │         ↓                                                   │
   │         Create pipeline with materials                      │
   └─────────────────────────────────────────────────────────────┘
   ↓
4. ConstructionPipelinePlanner (for new pipelines)
   - For each material (FAB_MATS, ADVANCED_CIRCUITRY, etc.):
     - Walk supply chain to configured depth
     - Create dependency tree of tasks
     - Create ACQUIRE_DELIVER tasks for inputs
     - Create DELIVER_TO_CONSTRUCTION task for final delivery
   ↓
5. Pipeline Orchestrator
   - Manages task lifecycle
   - Assigns ships to tasks
   - Tracks completion
   ↓
6. Task Executors
   - ACQUIRE_DELIVER: Buy/collect inputs, deliver to factory
   - DELIVER_TO_CONSTRUCTION: Collect from factory, deliver to construction site
   ↓
7. SpaceTraders API
   - Market transactions
   - Factory deliveries
   - Construction supply endpoint
```

---

## Domain Layer Changes

### 1. Pipeline Type Extension

**File**: `internal/domain/manufacturing/pipeline.go`

```go
// PipelineType enum
const (
    PipelineTypeFabrication  PipelineType = "FABRICATION"
    PipelineTypeCollection   PipelineType = "COLLECTION"
    PipelineTypeConstruction PipelineType = "CONSTRUCTION"  // NEW
)

// New fields for Pipeline entity
// ConstructionMaterialTarget tracks a single material's delivery progress
type ConstructionMaterialTarget struct {
    tradeSymbol       string  // e.g., "FAB_MATS"
    targetQuantity    int     // e.g., 1600
    deliveredQuantity int     // e.g., 500 (delivered so far)
}

func (m *ConstructionMaterialTarget) TradeSymbol() string       { return m.tradeSymbol }
func (m *ConstructionMaterialTarget) TargetQuantity() int       { return m.targetQuantity }
func (m *ConstructionMaterialTarget) DeliveredQuantity() int    { return m.deliveredQuantity }
func (m *ConstructionMaterialTarget) RemainingQuantity() int    { return m.targetQuantity - m.deliveredQuantity }
func (m *ConstructionMaterialTarget) IsComplete() bool          { return m.deliveredQuantity >= m.targetQuantity }
func (m *ConstructionMaterialTarget) Progress() float64 {
    if m.targetQuantity == 0 {
        return 100.0
    }
    return float64(m.deliveredQuantity) / float64(m.targetQuantity) * 100
}

type Pipeline struct {
    // ... existing fields ...

    // Construction-specific fields
    constructionSite   string                        // Waypoint symbol of construction site (e.g., "X1-FB5-I61")
    materials          []ConstructionMaterialTarget  // Multiple goods with their quantities
    supplyChainDepth   int                           // How deep to go in supply chain (0=full, 1=raw, 2=intermediate)
}

// New methods
func (p *Pipeline) IsConstruction() bool {
    return p.pipelineType == PipelineTypeConstruction
}

// GetMaterial returns the material target for a given trade symbol
func (p *Pipeline) GetMaterial(tradeSymbol string) *ConstructionMaterialTarget {
    for i := range p.materials {
        if p.materials[i].tradeSymbol == tradeSymbol {
            return &p.materials[i]
        }
    }
    return nil
}

// ConstructionProgress returns overall progress across all materials (0-100)
func (p *Pipeline) ConstructionProgress() float64 {
    if len(p.materials) == 0 {
        return 0
    }
    totalRequired := 0
    totalDelivered := 0
    for _, mat := range p.materials {
        totalRequired += mat.targetQuantity
        totalDelivered += mat.deliveredQuantity
    }
    if totalRequired == 0 {
        return 100.0
    }
    return float64(totalDelivered) / float64(totalRequired) * 100
}

// MaterialProgress returns progress for a specific material
func (p *Pipeline) MaterialProgress(tradeSymbol string) float64 {
    mat := p.GetMaterial(tradeSymbol)
    if mat == nil {
        return 0
    }
    return mat.Progress()
}

// RemainingForMaterial returns remaining quantity for a specific material
func (p *Pipeline) RemainingForMaterial(tradeSymbol string) int {
    mat := p.GetMaterial(tradeSymbol)
    if mat == nil {
        return 0
    }
    return mat.RemainingQuantity()
}

// IsComplete returns true if all materials are fully delivered
func (p *Pipeline) IsConstructionComplete() bool {
    for _, mat := range p.materials {
        if !mat.IsComplete() {
            return false
        }
    }
    return len(p.materials) > 0
}

// RecordDelivery records a delivery of a specific material
func (p *Pipeline) RecordDelivery(tradeSymbol string, quantity int) error {
    if !p.IsConstruction() {
        return errors.New("can only record deliveries on construction pipelines")
    }
    mat := p.GetMaterial(tradeSymbol)
    if mat == nil {
        return fmt.Errorf("material %s not found in pipeline", tradeSymbol)
    }
    mat.deliveredQuantity += quantity

    // Check if all materials are complete
    if p.IsConstructionComplete() {
        p.status = PipelineStatusCompleted
    }
    return nil
}
```

### 2. Task Type Extension

**File**: `internal/domain/manufacturing/task.go`

```go
// TaskType enum
const (
    TaskTypeAcquireDeliver        TaskType = "ACQUIRE_DELIVER"
    TaskTypeCollectSell           TaskType = "COLLECT_SELL"
    TaskTypeLiquidate             TaskType = "LIQUIDATE"
    TaskTypeStorageAcquireDeliver TaskType = "STORAGE_ACQUIRE_DELIVER"
    TaskTypeDeliverToConstruction TaskType = "DELIVER_TO_CONSTRUCTION"  // NEW
)

// DELIVER_TO_CONSTRUCTION task phases
const (
    // Phase 1: Navigate to factory and collect produced goods
    PhaseCollectFromFactory = "COLLECT_FROM_FACTORY"

    // Phase 2: Navigate to construction site
    PhaseNavigateToConstruction = "NAVIGATE_TO_CONSTRUCTION"

    // Phase 3: Deliver goods via construction supply API
    PhaseSupplyConstruction = "SUPPLY_CONSTRUCTION"
)
```

### 3. Construction Site Entity

**File**: `internal/domain/manufacturing/construction_site.go` (NEW)

```go
package manufacturing

// ConstructionSite represents a construction project (e.g., jump gate under construction)
type ConstructionSite struct {
    waypointSymbol string
    waypointType   string  // JUMP_GATE, etc.
    materials      []ConstructionMaterial
    isComplete     bool
}

// ConstructionMaterial represents a required material for construction
type ConstructionMaterial struct {
    tradeSymbol string  // FAB_MATS, ADVANCED_CIRCUITRY, etc.
    required    int     // Total required
    fulfilled   int     // Already delivered
}

func NewConstructionSite(waypointSymbol, waypointType string, materials []ConstructionMaterial) *ConstructionSite {
    return &ConstructionSite{
        waypointSymbol: waypointSymbol,
        waypointType:   waypointType,
        materials:      materials,
        isComplete:     false,
    }
}

func (cs *ConstructionSite) WaypointSymbol() string { return cs.waypointSymbol }
func (cs *ConstructionSite) WaypointType() string   { return cs.waypointType }
func (cs *ConstructionSite) Materials() []ConstructionMaterial { return cs.materials }
func (cs *ConstructionSite) IsComplete() bool       { return cs.isComplete }

func (cs *ConstructionSite) GetMaterial(tradeSymbol string) *ConstructionMaterial {
    for i := range cs.materials {
        if cs.materials[i].tradeSymbol == tradeSymbol {
            return &cs.materials[i]
        }
    }
    return nil
}

func (cs *ConstructionSite) RemainingForMaterial(tradeSymbol string) int {
    mat := cs.GetMaterial(tradeSymbol)
    if mat == nil {
        return 0
    }
    return mat.required - mat.fulfilled
}

func (cs *ConstructionSite) Progress() float64 {
    totalRequired := 0
    totalFulfilled := 0
    for _, mat := range cs.materials {
        totalRequired += mat.required
        totalFulfilled += mat.fulfilled
    }
    if totalRequired == 0 {
        return 100.0
    }
    return float64(totalFulfilled) / float64(totalRequired) * 100
}
```

### 4. Port Interfaces

**File**: `internal/domain/manufacturing/ports.go`

```go
// PipelineRepository provides pipeline persistence
type PipelineRepository interface {
    // ... existing methods ...

    // FindByConstructionSite retrieves the pipeline for a specific construction site (for idempotency)
    // Returns nil, nil if no pipeline exists for this site
    FindByConstructionSite(ctx context.Context, constructionSiteSymbol string, playerID int) (*Pipeline, error)
}

// ConstructionSiteRepository provides access to construction site data
type ConstructionSiteRepository interface {
    // FindByWaypoint retrieves construction site information from API
    FindByWaypoint(ctx context.Context, waypointSymbol string, playerID int) (*ConstructionSite, error)

    // SupplyMaterial delivers materials to construction site
    SupplyMaterial(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, playerID int) (*ConstructionSupplyResult, error)
}

// ConstructionSupplyResult contains the result of a construction supply operation
type ConstructionSupplyResult struct {
    Construction *ConstructionSite
    Cargo        *shared.Cargo
}
```

---

## Application Layer Changes

### 1. Construction Pipeline Planner

**File**: `internal/application/manufacturing/services/construction_pipeline_planner.go` (NEW)

```go
package services

import (
    "context"
    "fmt"
    "strings"

    "github.com/andrescamacho/spacetraders-go/internal/domain/goods"
    "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ConstructionPipelinePlanner creates and manages construction pipelines
type ConstructionPipelinePlanner struct {
    pipelineRepo      manufacturing.PipelineRepository
    constructionRepo  manufacturing.ConstructionSiteRepository
    marketLocator     MarketLocator
    factoryLocator    FactoryLocator
    idGenerator       IDGenerator
}

func NewConstructionPipelinePlanner(
    pipelineRepo manufacturing.PipelineRepository,
    constructionRepo manufacturing.ConstructionSiteRepository,
    marketLocator MarketLocator,
    factoryLocator FactoryLocator,
    idGenerator IDGenerator,
) *ConstructionPipelinePlanner {
    return &ConstructionPipelinePlanner{
        pipelineRepo:     pipelineRepo,
        constructionRepo: constructionRepo,
        marketLocator:    marketLocator,
        factoryLocator:   factoryLocator,
        idGenerator:      idGenerator,
    }
}

// StartOrResumeResult contains the result of starting or resuming a pipeline
type StartOrResumeResult struct {
    Pipeline  *manufacturing.Pipeline
    IsResumed bool  // true if resuming existing, false if newly created
}

// StartOrResume starts a new construction pipeline or resumes an existing one (IDEMPOTENT)
// This is the main entry point for construction pipeline management
func (p *ConstructionPipelinePlanner) StartOrResume(
    ctx context.Context,
    playerID int,
    constructionSite string,  // e.g., "X1-FB5-I61"
    supplyChainDepth int,     // 0=full, 1=raw only, 2=intermediates
) (*StartOrResumeResult, error) {

    // 1. IDEMPOTENCY CHECK: Check if pipeline already exists for this construction site
    existingPipeline, err := p.pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
    if err != nil {
        return nil, fmt.Errorf("failed to check for existing pipeline: %w", err)
    }

    if existingPipeline != nil {
        // Pipeline exists - resume it
        return &StartOrResumeResult{
            Pipeline:  existingPipeline,
            IsResumed: true,
        }, nil
    }

    // 2. AUTO-DISCOVERY: Fetch construction requirements from API
    constructionSiteData, err := p.constructionRepo.FindByWaypoint(ctx, constructionSite, playerID)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch construction site data: %w", err)
    }

    if constructionSiteData.IsComplete() {
        return nil, fmt.Errorf("construction site %s is already complete", constructionSite)
    }

    // 3. Extract system symbol from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5")
    systemSymbol := extractSystemSymbol(constructionSite)

    // 4. Convert construction requirements to material targets
    // Only include materials that still need delivery (remaining > 0)
    var materials []manufacturing.ConstructionMaterialTarget
    for _, mat := range constructionSiteData.Materials() {
        remaining := mat.Required - mat.Fulfilled
        if remaining > 0 {
            materials = append(materials, manufacturing.ConstructionMaterialTarget{
                TradeSymbol:       mat.TradeSymbol,
                TargetQuantity:    remaining,  // Only need to deliver the remaining amount
                DeliveredQuantity: 0,          // Starting fresh for our pipeline
            })
        }
    }

    if len(materials) == 0 {
        return nil, fmt.Errorf("construction site %s has no remaining materials to deliver", constructionSite)
    }

    // 5. Create pipeline with auto-discovered materials
    pipeline := manufacturing.NewPipeline(
        p.idGenerator.Generate("construction"),
        playerID,
        materials[0].TradeSymbol,  // Primary target good (first material)
        systemSymbol,
        manufacturing.PipelineTypeConstruction,
    )

    // Set construction-specific fields
    pipeline.SetConstructionSite(constructionSite)
    pipeline.SetMaterials(materials)
    pipeline.SetSupplyChainDepth(supplyChainDepth)

    // 6. Create tasks for each material
    for _, mat := range materials {
        if err := p.createTasksForMaterial(ctx, pipeline, mat.TradeSymbol, mat.TargetQuantity, supplyChainDepth, constructionSite); err != nil {
            return nil, fmt.Errorf("failed to create tasks for %s: %w", mat.TradeSymbol, err)
        }
    }

    // 7. Persist pipeline
    if err := p.pipelineRepo.Save(ctx, pipeline); err != nil {
        return nil, fmt.Errorf("failed to save pipeline: %w", err)
    }

    return &StartOrResumeResult{
        Pipeline:  pipeline,
        IsResumed: false,
    }, nil
}

// createTasksForMaterial creates tasks for producing and delivering a single material
func (p *ConstructionPipelinePlanner) createTasksForMaterial(
    ctx context.Context,
    pipeline *manufacturing.Pipeline,
    targetGood string,
    targetQuantity int,
    supplyChainDepth int,
    constructionSite string,
) error {
    // Build dependency tree based on depth
    dependencyTree := p.buildDependencyTree(targetGood, supplyChainDepth, 0)

    // Create tasks from dependency tree
    tasks, err := p.createTasksFromTree(ctx, pipeline, dependencyTree, constructionSite)
    if err != nil {
        return fmt.Errorf("failed to create tasks: %w", err)
    }

    for _, task := range tasks {
        pipeline.AddTask(task)
    }

    return nil
}

// extractSystemSymbol extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5")
func extractSystemSymbol(waypointSymbol string) string {
    // Waypoint format: {sector}-{system}-{waypoint}, e.g., "X1-FB5-I61"
    // System format: {sector}-{system}, e.g., "X1-FB5"
    parts := strings.Split(waypointSymbol, "-")
    if len(parts) >= 2 {
        return parts[0] + "-" + parts[1]
    }
    return waypointSymbol
}

// buildDependencyTree recursively builds the supply chain tree
func (p *ConstructionPipelinePlanner) buildDependencyTree(good string, maxDepth, currentDepth int) *SupplyNode {
    node := &SupplyNode{
        Good:     good,
        Depth:    currentDepth,
        Children: make([]*SupplyNode, 0),
    }

    // Stop if we've reached max depth or good is raw material
    if currentDepth >= maxDepth || goods.IsRawMaterial(good) {
        node.AcquisitionMethod = "BUY"
        return node
    }

    // Get required inputs
    inputs := goods.GetRequiredInputs(good)
    if len(inputs) == 0 {
        node.AcquisitionMethod = "BUY"
        return node
    }

    node.AcquisitionMethod = "FABRICATE"
    for _, input := range inputs {
        child := p.buildDependencyTree(input, maxDepth, currentDepth+1)
        node.Children = append(node.Children, child)
    }

    return node
}

// SupplyNode represents a node in the supply chain tree
type SupplyNode struct {
    Good              string
    Depth             int
    AcquisitionMethod string  // "BUY" or "FABRICATE"
    Children          []*SupplyNode
}

// createTasksFromTree converts the dependency tree into executable tasks
func (p *ConstructionPipelinePlanner) createTasksFromTree(
    ctx context.Context,
    pipeline *manufacturing.Pipeline,
    root *SupplyNode,
    constructionSite string,
) ([]*manufacturing.Task, error) {
    tasks := make([]*manufacturing.Task, 0)

    // Post-order traversal: create tasks from leaves to root
    var traverse func(node *SupplyNode, parent *manufacturing.Task) error
    traverse = func(node *SupplyNode, parent *manufacturing.Task) error {
        // Process children first
        var childTasks []*manufacturing.Task
        for _, child := range node.Children {
            if err := traverse(child, nil); err != nil {
                return err
            }
        }

        // Create task for this node
        var task *manufacturing.Task
        var err error

        if node.Good == root.Good {
            // Root node: DELIVER_TO_CONSTRUCTION
            task, err = p.createDeliveryTask(ctx, pipeline, node.Good, constructionSite)
        } else if node.AcquisitionMethod == "BUY" {
            // Leaf nodes: ACQUIRE_DELIVER
            task, err = p.createAcquireTask(ctx, pipeline, node.Good)
        } else {
            // Intermediate nodes: ACQUIRE_DELIVER (collect from factory)
            task, err = p.createAcquireTask(ctx, pipeline, node.Good)
        }

        if err != nil {
            return err
        }

        // Set dependencies
        for _, childTask := range childTasks {
            task.AddDependency(childTask.ID())
        }

        tasks = append(tasks, task)
        return nil
    }

    if err := traverse(root, nil); err != nil {
        return nil, err
    }

    return tasks, nil
}
```

### 2. Deliver To Construction Executor

**File**: `internal/application/manufacturing/services/manufacturing/deliver_to_construction_executor.go` (NEW)

```go
package manufacturing

import (
    "context"
    "fmt"

    "github.com/andrescamacho/spacetraders-go/internal/application/common"
    navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
    "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
    "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// DeliverToConstructionExecutor handles DELIVER_TO_CONSTRUCTION tasks
type DeliverToConstructionExecutor struct {
    shipRepo           ShipRepository
    pipelineRepo       manufacturing.PipelineRepository
    constructionRepo   manufacturing.ConstructionSiteRepository
    apiClient          ports.APIClient
    mediator           common.Mediator
}

func NewDeliverToConstructionExecutor(
    shipRepo ShipRepository,
    pipelineRepo manufacturing.PipelineRepository,
    constructionRepo manufacturing.ConstructionSiteRepository,
    apiClient ports.APIClient,
    mediator common.Mediator,
) *DeliverToConstructionExecutor {
    return &DeliverToConstructionExecutor{
        shipRepo:         shipRepo,
        pipelineRepo:     pipelineRepo,
        constructionRepo: constructionRepo,
        apiClient:        apiClient,
        mediator:         mediator,
    }
}

func (e *DeliverToConstructionExecutor) Execute(
    ctx context.Context,
    task *manufacturing.Task,
    ship *navigation.Ship,
    playerID int,
) error {
    logger := common.LoggerFromContext(ctx)

    // Resume from current phase
    switch task.Phase() {
    case "", manufacturing.PhaseCollectFromFactory:
        if err := e.executeCollectPhase(ctx, task, ship, playerID, logger); err != nil {
            return err
        }
        fallthrough

    case manufacturing.PhaseNavigateToConstruction:
        if err := e.executeNavigatePhase(ctx, task, ship, playerID, logger); err != nil {
            return err
        }
        fallthrough

    case manufacturing.PhaseSupplyConstruction:
        if err := e.executeSupplyPhase(ctx, task, ship, playerID, logger); err != nil {
            return err
        }
    }

    return nil
}

// executeCollectPhase navigates to factory and collects produced goods
func (e *DeliverToConstructionExecutor) executeCollectPhase(
    ctx context.Context,
    task *manufacturing.Task,
    ship *navigation.Ship,
    playerID int,
    logger common.Logger,
) error {
    task.SetPhase(manufacturing.PhaseCollectFromFactory)

    factoryWaypoint := task.FactoryWaypoint()
    logger.Log("INFO", "Collecting goods from factory", map[string]interface{}{
        "ship":     ship.ShipSymbol(),
        "factory":  factoryWaypoint,
        "good":     task.TargetGood(),
    })

    // Navigate to factory if not already there
    if ship.CurrentLocation().Symbol != factoryWaypoint {
        navCmd := &navCmd.NavigateRouteCommand{
            ShipSymbol:  ship.ShipSymbol(),
            Destination: factoryWaypoint,
            PlayerID:    playerID,
        }
        if _, err := e.mediator.Send(ctx, navCmd); err != nil {
            return fmt.Errorf("failed to navigate to factory: %w", err)
        }

        // Reload ship after navigation
        ship, err = e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
        if err != nil {
            return err
        }
    }

    // Collect goods from factory
    // (Goods are added to cargo when factory completes production)
    // Verify cargo contains expected goods
    cargoAmount := ship.GetCargoQuantity(task.TargetGood())
    if cargoAmount == 0 {
        return fmt.Errorf("no %s in cargo to deliver", task.TargetGood())
    }

    logger.Log("INFO", "Collected goods from factory", map[string]interface{}{
        "ship":     ship.ShipSymbol(),
        "good":     task.TargetGood(),
        "quantity": cargoAmount,
    })

    return nil
}

// executeNavigatePhase navigates to construction site
func (e *DeliverToConstructionExecutor) executeNavigatePhase(
    ctx context.Context,
    task *manufacturing.Task,
    ship *navigation.Ship,
    playerID int,
    logger common.Logger,
) error {
    task.SetPhase(manufacturing.PhaseNavigateToConstruction)

    constructionSite := task.ConstructionSite()
    logger.Log("INFO", "Navigating to construction site", map[string]interface{}{
        "ship": ship.ShipSymbol(),
        "site": constructionSite,
    })

    if ship.CurrentLocation().Symbol != constructionSite {
        navCmd := &navCmd.NavigateRouteCommand{
            ShipSymbol:  ship.ShipSymbol(),
            Destination: constructionSite,
            PlayerID:    playerID,
        }
        if _, err := e.mediator.Send(ctx, navCmd); err != nil {
            return fmt.Errorf("failed to navigate to construction site: %w", err)
        }
    }

    return nil
}

// executeSupplyPhase delivers goods to construction site
func (e *DeliverToConstructionExecutor) executeSupplyPhase(
    ctx context.Context,
    task *manufacturing.Task,
    ship *navigation.Ship,
    playerID int,
    logger common.Logger,
) error {
    task.SetPhase(manufacturing.PhaseSupplyConstruction)

    // Reload ship for fresh cargo data
    ship, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
    if err != nil {
        return err
    }

    cargoAmount := ship.GetCargoQuantity(task.TargetGood())
    if cargoAmount == 0 {
        return fmt.Errorf("no %s in cargo to supply", task.TargetGood())
    }

    logger.Log("INFO", "Supplying construction site", map[string]interface{}{
        "ship":     ship.ShipSymbol(),
        "site":     task.ConstructionSite(),
        "good":     task.TargetGood(),
        "quantity": cargoAmount,
    })

    // Call construction supply API
    result, err := e.constructionRepo.SupplyMaterial(
        ctx,
        ship.ShipSymbol(),
        task.ConstructionSite(),
        task.TargetGood(),
        cargoAmount,
        playerID,
    )
    if err != nil {
        return fmt.Errorf("failed to supply construction: %w", err)
    }

    // Update pipeline with delivered quantity
    pipeline, err := e.pipelineRepo.FindByID(ctx, task.PipelineID(), playerID)
    if err != nil {
        return err
    }

    pipeline.RecordDelivery(cargoAmount)
    if err := e.pipelineRepo.Save(ctx, pipeline); err != nil {
        return err
    }

    logger.Log("INFO", "Construction supply successful", map[string]interface{}{
        "ship":              ship.ShipSymbol(),
        "delivered":         cargoAmount,
        "total_delivered":   pipeline.DeliveredQuantity(),
        "target":            pipeline.TargetQuantity(),
        "progress":          fmt.Sprintf("%.1f%%", pipeline.ConstructionProgress()),
    })

    return nil
}
```

### 3. Executor Registration

**File**: `internal/application/manufacturing/services/manufacturing/task_executor_registry.go`

Add to existing registry:

```go
func (r *TaskExecutorRegistry) GetExecutor(taskType manufacturing.TaskType) TaskExecutor {
    switch taskType {
    case manufacturing.TaskTypeAcquireDeliver:
        return r.acquireDeliverExecutor
    case manufacturing.TaskTypeCollectSell:
        return r.collectSellExecutor
    case manufacturing.TaskTypeLiquidate:
        return r.liquidateExecutor
    case manufacturing.TaskTypeStorageAcquireDeliver:
        return r.storageAcquireDeliverExecutor
    case manufacturing.TaskTypeDeliverToConstruction:  // NEW
        return r.deliverToConstructionExecutor
    default:
        return nil
    }
}
```

---

## API Integration

### Construction Supply API

**File**: `internal/adapters/api/spacetraders_client.go`

```go
// ConstructionSupplyResponse represents the API response
type ConstructionSupplyResponse struct {
    Data struct {
        Construction struct {
            Symbol     string `json:"symbol"`
            Materials  []struct {
                TradeSymbol string `json:"tradeSymbol"`
                Required    int    `json:"required"`
                Fulfilled   int    `json:"fulfilled"`
            } `json:"materials"`
            IsComplete bool `json:"isComplete"`
        } `json:"construction"`
        Cargo struct {
            Capacity  int `json:"capacity"`
            Units     int `json:"units"`
            Inventory []struct {
                Symbol string `json:"symbol"`
                Units  int    `json:"units"`
            } `json:"inventory"`
        } `json:"cargo"`
    } `json:"data"`
}

// SupplyConstruction delivers materials to a construction site
func (c *SpaceTradersClient) SupplyConstruction(
    ctx context.Context,
    shipSymbol string,
    waypointSymbol string,
    tradeSymbol string,
    units int,
    token string,
) (*ConstructionSupplyResponse, error) {
    endpoint := fmt.Sprintf("/my/ships/%s/construction/supply", shipSymbol)

    payload := map[string]interface{}{
        "shipSymbol":     shipSymbol,
        "waypointSymbol": waypointSymbol,
        "tradeSymbol":    tradeSymbol,
        "units":          units,
    }

    var response ConstructionSupplyResponse
    if err := c.post(ctx, endpoint, payload, &response, token); err != nil {
        return nil, fmt.Errorf("construction supply failed: %w", err)
    }

    return &response, nil
}

// GetConstruction retrieves construction site information
func (c *SpaceTradersClient) GetConstruction(
    ctx context.Context,
    systemSymbol string,
    waypointSymbol string,
    token string,
) (*ConstructionSite, error) {
    endpoint := fmt.Sprintf("/systems/%s/waypoints/%s/construction", systemSymbol, waypointSymbol)

    var response struct {
        Data struct {
            Symbol     string `json:"symbol"`
            Materials  []struct {
                TradeSymbol string `json:"tradeSymbol"`
                Required    int    `json:"required"`
                Fulfilled   int    `json:"fulfilled"`
            } `json:"materials"`
            IsComplete bool `json:"isComplete"`
        } `json:"data"`
    }

    if err := c.get(ctx, endpoint, &response, token); err != nil {
        return nil, fmt.Errorf("get construction failed: %w", err)
    }

    // Convert to domain entity
    materials := make([]ConstructionMaterial, len(response.Data.Materials))
    for i, m := range response.Data.Materials {
        materials[i] = ConstructionMaterial{
            TradeSymbol: m.TradeSymbol,
            Required:    m.Required,
            Fulfilled:   m.Fulfilled,
        }
    }

    return &ConstructionSite{
        WaypointSymbol: response.Data.Symbol,
        Materials:      materials,
        IsComplete:     response.Data.IsComplete,
    }, nil
}
```

---

## CLI Interface

### Construction Command

**File**: `internal/adapters/cli/construction.go` (NEW)

```go
package cli

import (
    "context"
    "fmt"
    "os"
    "text/tabwriter"

    "github.com/spf13/cobra"
)

var constructionCmd = &cobra.Command{
    Use:   "construction",
    Short: "Manage construction pipelines",
    Long: `Commands for managing construction material production and delivery.

Construction pipelines automate the production of materials needed for
building jump gates and other constructions.`,
}

var constructionStartCmd = &cobra.Command{
    Use:   "start",
    Short: "Start or resume a construction pipeline",
    Long: `Start or resume a construction pipeline for a construction site.

The command is IDEMPOTENT:
- If no pipeline exists for the site, creates a new one
- If a pipeline already exists, resumes it from where it left off
- Material requirements are AUTO-DISCOVERED from the API

The pipeline will:
1. Fetch construction requirements from the SpaceTraders API
2. Calculate remaining materials needed (already delivered is excluded)
3. Walk the supply chain to the configured depth for each material
4. Create tasks to acquire/produce required materials in parallel
5. Deliver all goods to the construction site

Supply chain depth:
  0 = Full self-production (mine all raw materials)
  1 = Buy raw materials only (mine nothing)
  2 = Buy intermediates (buy processed materials)
  3 = Buy final product (no fabrication)`,
    Example: `  # Start/resume construction pipeline (auto-discovers requirements)
  spacetraders construction start --site X1-FB5-I61 --depth 1

  # With full self-production
  spacetraders construction start --site X1-FB5-I61 --depth 0

  # Resume existing pipeline (same command, idempotent)
  spacetraders construction start --site X1-FB5-I61 --depth 1`,
    Run: runConstructionStart,
}

var constructionStatusCmd = &cobra.Command{
    Use:   "status",
    Short: "Check construction site status",
    Example: `  spacetraders construction status --site X1-FB5-I61`,
    Run: runConstructionStatus,
}

var constructionListCmd = &cobra.Command{
    Use:   "list",
    Short: "List active construction pipelines",
    Run:   runConstructionList,
}

// Flags
var (
    constructionSite  string
    constructionDepth int
)

func init() {
    // Start command flags (minimal - auto-discovers materials from API)
    constructionStartCmd.Flags().StringVar(&constructionSite, "site", "", "Construction site waypoint (required)")
    constructionStartCmd.Flags().IntVar(&constructionDepth, "depth", 1, "Supply chain depth (0=full, 1=raw, 2=intermediate)")
    constructionStartCmd.MarkFlagRequired("site")

    // Status command flags
    constructionStatusCmd.Flags().StringVar(&constructionSite, "site", "", "Construction site waypoint")
    constructionStatusCmd.MarkFlagRequired("site")

    // Add subcommands
    constructionCmd.AddCommand(constructionStartCmd)
    constructionCmd.AddCommand(constructionStatusCmd)
    constructionCmd.AddCommand(constructionListCmd)

    // Add to root
    rootCmd.AddCommand(constructionCmd)
}

func runConstructionStart(cmd *cobra.Command, args []string) {
    ctx := context.Background()
    client := getDaemonClient()

    playerID, err := resolvePlayerID(cmd)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }

    fmt.Printf("Starting/resuming construction pipeline for site: %s\n", constructionSite)
    fmt.Printf("Supply chain depth: %d\n\n", constructionDepth)

    // Daemon handles:
    // 1. Check if pipeline already exists for this site (idempotent)
    // 2. If exists: resume it
    // 3. If not: fetch construction requirements from API and create new pipeline
    result, err := client.StartOrResumeConstructionPipeline(
        ctx,
        playerID,
        constructionSite,
        constructionDepth,
    )
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error starting pipeline: %v\n", err)
        os.Exit(1)
    }

    fmt.Printf("Pipeline created successfully!\n")
    fmt.Printf("  Pipeline ID: %s\n", result.PipelineID)
    fmt.Printf("  Tasks:       %d\n", result.TaskCount)
    fmt.Printf("  Status:      %s\n", result.Status)
}

func runConstructionStatus(cmd *cobra.Command, args []string) {
    ctx := context.Background()
    client := getDaemonClient()

    playerID, err := resolvePlayerID(cmd)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }

    status, err := client.GetConstructionStatus(ctx, playerID, constructionSite)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error getting status: %v\n", err)
        os.Exit(1)
    }

    fmt.Printf("Construction Site: %s\n", status.WaypointSymbol)
    fmt.Printf("Complete: %v\n\n", status.IsComplete)

    w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
    fmt.Fprintln(w, "MATERIAL\tREQUIRED\tFULFILLED\tREMAINING\tPROGRESS")
    fmt.Fprintln(w, "--------\t--------\t---------\t---------\t--------")

    for _, mat := range status.Materials {
        remaining := mat.Required - mat.Fulfilled
        progress := float64(mat.Fulfilled) / float64(mat.Required) * 100
        fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%.1f%%\n",
            mat.TradeSymbol, mat.Required, mat.Fulfilled, remaining, progress)
    }
    w.Flush()
}

func runConstructionList(cmd *cobra.Command, args []string) {
    ctx := context.Background()
    client := getDaemonClient()

    playerID, err := resolvePlayerID(cmd)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }

    pipelines, err := client.ListConstructionPipelines(ctx, playerID)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error listing pipelines: %v\n", err)
        os.Exit(1)
    }

    if len(pipelines) == 0 {
        fmt.Println("No active construction pipelines.")
        return
    }

    w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
    fmt.Fprintln(w, "PIPELINE ID\tSITE\tGOOD\tDELIVERED\tTARGET\tPROGRESS\tSTATUS")
    fmt.Fprintln(w, "-----------\t----\t----\t---------\t------\t--------\t------")

    for _, p := range pipelines {
        progress := float64(p.DeliveredQuantity) / float64(p.TargetQuantity) * 100
        fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%.1f%%\t%s\n",
            p.ID, p.ConstructionSite, p.TargetGood,
            p.DeliveredQuantity, p.TargetQuantity, progress, p.Status)
    }
    w.Flush()
}
```

### CLI Usage Examples

```bash
# Check current construction status (fetches from API)
./bin/spacetraders construction status --site X1-FB5-I61 --player-id 14

# Start construction pipeline (auto-discovers material requirements)
./bin/spacetraders construction start --site X1-FB5-I61 --depth 1 --player-id 14

# Resume existing pipeline (same command - idempotent)
./bin/spacetraders construction start --site X1-FB5-I61 --depth 1 --player-id 14

# Start with full self-production (depth 0 = mine everything)
./bin/spacetraders construction start --site X1-FB5-I61 --depth 0 --player-id 14

# List all active construction pipelines
./bin/spacetraders construction list --player-id 14
```

---

## Persistence Changes

### Pipeline Model Update

**File**: `internal/adapters/persistence/models.go`

```go
type ManufacturingPipelineModel struct {
    // ... existing fields ...

    // Construction-specific fields
    ConstructionSite  string `gorm:"column:construction_site;size:255"`
    SupplyChainDepth  int    `gorm:"column:supply_chain_depth;default:0"`

    // Has-many relationship for materials
    Materials []ConstructionMaterialTargetModel `gorm:"foreignKey:PipelineID"`
}

// ConstructionMaterialTargetModel tracks delivery progress for each material
type ConstructionMaterialTargetModel struct {
    ID                uint   `gorm:"primaryKey;autoIncrement"`
    PipelineID        string `gorm:"column:pipeline_id;size:255;index"`
    TradeSymbol       string `gorm:"column:trade_symbol;size:50"`
    TargetQuantity    int    `gorm:"column:target_quantity"`
    DeliveredQuantity int    `gorm:"column:delivered_quantity;default:0"`
}

func (ConstructionMaterialTargetModel) TableName() string {
    return "construction_material_targets"
}
```

### Migration

**File**: `migrations/021_add_construction_pipeline_fields.up.sql`

```sql
-- Add construction-specific fields to manufacturing_pipelines
ALTER TABLE manufacturing_pipelines
ADD COLUMN construction_site VARCHAR(255),
ADD COLUMN supply_chain_depth INTEGER DEFAULT 0;

-- Add index for querying by construction site
CREATE INDEX idx_pipelines_construction_site
ON manufacturing_pipelines(construction_site)
WHERE construction_site IS NOT NULL;

-- Create separate table for construction material targets (multiple materials per pipeline)
CREATE TABLE construction_material_targets (
    id SERIAL PRIMARY KEY,
    pipeline_id VARCHAR(255) NOT NULL REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    trade_symbol VARCHAR(50) NOT NULL,
    target_quantity INTEGER NOT NULL,
    delivered_quantity INTEGER DEFAULT 0,
    UNIQUE(pipeline_id, trade_symbol)
);

-- Index for querying materials by pipeline
CREATE INDEX idx_material_targets_pipeline_id ON construction_material_targets(pipeline_id);

-- Add construction_site field to tasks
ALTER TABLE manufacturing_tasks
ADD COLUMN construction_site VARCHAR(255);
```

**File**: `migrations/021_add_construction_pipeline_fields.down.sql`

```sql
ALTER TABLE manufacturing_tasks DROP COLUMN IF EXISTS construction_site;
DROP TABLE IF EXISTS construction_material_targets;
DROP INDEX IF EXISTS idx_pipelines_construction_site;
ALTER TABLE manufacturing_pipelines DROP COLUMN IF EXISTS supply_chain_depth;
ALTER TABLE manufacturing_pipelines DROP COLUMN IF EXISTS construction_site;
```

### Repository Update

**File**: `internal/adapters/persistence/manufacturing_pipeline_repository.go`

```go
// FindByConstructionSite retrieves the pipeline for a specific construction site (for idempotency)
// Returns nil, nil if no pipeline exists for this site
func (r *ManufacturingPipelineRepository) FindByConstructionSite(
    ctx context.Context,
    constructionSiteSymbol string,
    playerID int,
) (*manufacturing.Pipeline, error) {
    var model ManufacturingPipelineModel

    err := r.db.WithContext(ctx).
        Preload("Materials").  // Load the materials relationship
        Where("construction_site = ? AND player_id = ? AND status NOT IN (?)",
            constructionSiteSymbol, playerID, []string{"COMPLETED", "FAILED"}).
        First(&model).Error

    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, nil  // No pipeline exists - return nil, nil (not an error)
        }
        return nil, fmt.Errorf("failed to find pipeline by construction site: %w", err)
    }

    return r.toDomain(&model), nil
}

func (r *ManufacturingPipelineRepository) toDomain(model *ManufacturingPipelineModel) *manufacturing.Pipeline {
    pipeline := manufacturing.NewPipeline(
        model.ID,
        model.PlayerID,
        model.TargetGood,
        model.SystemSymbol,
        manufacturing.PipelineType(model.PipelineType),
    )

    // Set construction-specific fields if present
    if model.ConstructionSite != "" {
        pipeline.SetConstructionSite(model.ConstructionSite)
        pipeline.SetSupplyChainDepth(model.SupplyChainDepth)

        // Load materials from joined table
        materials := make([]manufacturing.ConstructionMaterialTarget, len(model.Materials))
        for i, m := range model.Materials {
            materials[i] = manufacturing.ConstructionMaterialTarget{
                TradeSymbol:       m.TradeSymbol,
                TargetQuantity:    m.TargetQuantity,
                DeliveredQuantity: m.DeliveredQuantity,
            }
        }
        pipeline.SetMaterials(materials)
    }

    // ... rest of mapping ...

    return pipeline
}

func (r *ManufacturingPipelineRepository) toModel(pipeline *manufacturing.Pipeline) *ManufacturingPipelineModel {
    model := &ManufacturingPipelineModel{
        ID:           pipeline.ID(),
        PlayerID:     pipeline.PlayerID(),
        TargetGood:   pipeline.TargetGood(),
        SystemSymbol: pipeline.SystemSymbol(),
        PipelineType: string(pipeline.Type()),
        Status:       string(pipeline.Status()),
        // ... other fields ...

        // Construction-specific
        ConstructionSite:  pipeline.ConstructionSite(),
        TargetQuantity:    pipeline.TargetQuantity(),
        DeliveredQuantity: pipeline.DeliveredQuantity(),
        SupplyChainDepth:  pipeline.SupplyChainDepth(),
    }

    return model
}
```

---

## Execution Flow

### Complete Flow Diagram

```
┌──────────────────────────────────────────────────────────────────────────┐
│ 1. USER COMMAND (minimal flags - auto-discovers materials)                │
│    ./bin/spacetraders construction start --site X1-FB5-I61 --depth 1     │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 2. CLI → gRPC REQUEST                                                     │
│    StartOrResumeConstructionPipelineRequest {                             │
│      player_id: 14,                                                       │
│      site: "X1-FB5-I61",                                                 │
│      depth: 1                                                             │
│    }                                                                      │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 3. IDEMPOTENCY CHECK                                                      │
│    FindByConstructionSite(ctx, "X1-FB5-I61", playerID)                   │
│                                                                           │
│    ┌─ Pipeline exists? ─────────────────────────────────────────────┐    │
│    │ YES → Return existing pipeline (resume from current progress)  │    │
│    │ NO  → Continue to step 4                                       │    │
│    └────────────────────────────────────────────────────────────────┘    │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 4. AUTO-DISCOVERY (API call)                                              │
│    GET /systems/X1-FB5/waypoints/X1-FB5-I61/construction                 │
│                                                                           │
│    Response:                                                              │
│    {                                                                      │
│      materials: [                                                         │
│        {tradeSymbol: "FAB_MATS", required: 1600, fulfilled: 0},         │
│        {tradeSymbol: "ADVANCED_CIRCUITRY", required: 400, fulfilled: 0}, │
│        {tradeSymbol: "QUANTUM_STABILIZERS", required: 1, fulfilled: 1}   │
│      ]                                                                    │
│    }                                                                      │
│                                                                           │
│    Filters: Only materials with remaining > 0                            │
│    Result: FAB_MATS:1600, ADVANCED_CIRCUITRY:400                         │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 5. CONSTRUCTION PIPELINE PLANNER (for each material)                      │
│                                                                           │
│    For FAB_MATS (depth=1):                                               │
│       FAB_MATS (FABRICATE)                                               │
│       ├── IRON (FABRICATE)                                               │
│       │   └── IRON_ORE (BUY)    ← depth 1 reached                       │
│       └── QUARTZ_SAND (BUY)     ← raw material                          │
│                                                                           │
│    For ADVANCED_CIRCUITRY (depth=1):                                      │
│       ADVANCED_CIRCUITRY (FABRICATE)                                      │
│       ├── ELECTRONICS (FABRICATE)                                         │
│       │   ├── SILICON_CRYSTALS (BUY)                                     │
│       │   └── COPPER (FABRICATE)                                         │
│       │       └── COPPER_ORE (BUY)                                       │
│       └── MICROPROCESSORS (FABRICATE)                                     │
│           ├── SILICON_CRYSTALS (BUY)                                     │
│           └── COPPER (FABRICATE)                                         │
│               └── COPPER_ORE (BUY)                                       │
│                                                                           │
│    Creates tasks for BOTH materials:                                      │
│       - ACQUIRE_DELIVER: IRON_ORE → smelter                             │
│       - ACQUIRE_DELIVER: QUARTZ_SAND → factory                          │
│       - ACQUIRE_DELIVER: IRON → factory (waits for smelter)             │
│       - DELIVER_TO_CONSTRUCTION: FAB_MATS → X1-FB5-I61                  │
│       - ACQUIRE_DELIVER: COPPER_ORE → smelter                           │
│       - ACQUIRE_DELIVER: SILICON_CRYSTALS → factory                     │
│       - ... (more tasks for ADVANCED_CIRCUITRY)                          │
│       - DELIVER_TO_CONSTRUCTION: ADVANCED_CIRCUITRY → X1-FB5-I61        │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 6. PIPELINE ORCHESTRATOR                                                  │
│    - Pipeline status: EXECUTING                                           │
│    - Assigns available ships to READY tasks                              │
│    - Monitors task completion                                             │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 7. TASK EXECUTION (Parallel where possible)                              │
│                                                                           │
│    ┌─────────────────────┐   ┌─────────────────────┐                    │
│    │ Ship TORWIND-3      │   │ Ship TORWIND-5      │                    │
│    │ ACQUIRE_DELIVER     │   │ ACQUIRE_DELIVER     │                    │
│    │ Buy IRON_ORE        │   │ Buy QUARTZ_SAND     │                    │
│    │ → Smelter           │   │ → Factory           │                    │
│    └─────────┬───────────┘   └─────────┬───────────┘                    │
│              │                         │                                  │
│              ▼                         │                                  │
│    ┌─────────────────────┐             │                                 │
│    │ Ship TORWIND-3      │             │                                 │
│    │ ACQUIRE_DELIVER     │             │                                 │
│    │ Collect IRON        │◄────────────┘                                │
│    │ → Factory           │                                               │
│    └─────────┬───────────┘                                               │
│              ▼                                                            │
│    ┌─────────────────────────────────────────┐                           │
│    │ Ship TORWIND-7                          │                           │
│    │ DELIVER_TO_CONSTRUCTION                 │                           │
│    │ Phase 1: Collect FAB_MATS from factory  │                           │
│    │ Phase 2: Navigate to X1-FB5-I61         │                           │
│    │ Phase 3: Supply construction (API call) │                           │
│    └─────────────────────────────────────────┘                           │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 8. CONSTRUCTION SUPPLY API                                                │
│    POST /my/ships/TORWIND-7/construction/supply                          │
│    {                                                                      │
│      "shipSymbol": "TORWIND-7",                                          │
│      "waypointSymbol": "X1-FB5-I61",                                     │
│      "tradeSymbol": "FAB_MATS",                                          │
│      "units": 80  // cargo capacity                                      │
│    }                                                                      │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 9. PROGRESS TRACKING                                                      │
│    Pipeline.deliveredQuantity += 80                                      │
│    Progress: 80/1600 = 5%                                                │
│                                                                           │
│    Repeat steps 7-9 until all materials fully delivered                 │
└───────────────────────────────────────┬──────────────────────────────────┘
                                        ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ 10. COMPLETION                                                            │
│    Pipeline status: COMPLETED                                             │
│    Total delivered: 1600/1600 FAB_MATS                                   │
│    Jump gate X1-FB5-I61 construction progress updated                    │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Configuration Examples

### Example 1: Start Construction Pipeline (Auto-discovers all materials)

```bash
# Start construction pipeline - auto-discovers FAB_MATS:1600, ADVANCED_CIRCUITRY:400
./bin/spacetraders construction start \
  --site X1-FB5-I61 \
  --depth 1 \
  --player-id 14
```

The system automatically:
1. Fetches construction requirements from API
2. Filters out already-completed materials (e.g., QUANTUM_STABILIZERS)
3. Creates tasks for all remaining materials in a single pipeline

**Auto-discovered materials**:
- FAB_MATS: 1600 (remaining)
- ADVANCED_CIRCUITRY: 400 (remaining)

**Generated Tasks for FAB_MATS**:
1. `ACQUIRE_DELIVER`: Buy IRON_ORE → Smelter
2. `ACQUIRE_DELIVER`: Buy QUARTZ_SAND → Factory
3. `ACQUIRE_DELIVER`: Collect IRON (from smelter) → Factory
4. `DELIVER_TO_CONSTRUCTION`: Collect FAB_MATS → X1-FB5-I61

**Generated Tasks for ADVANCED_CIRCUITRY**:
1. `ACQUIRE_DELIVER`: Buy COPPER_ORE → Smelter
2. `ACQUIRE_DELIVER`: Buy SILICON_CRYSTALS → Factory
3. `ACQUIRE_DELIVER`: Collect COPPER → Factory
4. ... (more tasks for intermediates)
5. `DELIVER_TO_CONSTRUCTION`: Collect ADVANCED_CIRCUITRY → X1-FB5-I61

### Example 2: Full Self-Production (Depth 0)

```bash
# Full self-production - mine all raw materials
./bin/spacetraders construction start \
  --site X1-FB5-I61 \
  --depth 0 \
  --player-id 14
```

**Generated Tasks** (with mining):
1. `COLLECT_SELL` (modified): Mine COPPER_ORE → Smelter (no sell)
2. `COLLECT_SELL` (modified): Mine IRON_ORE → Smelter (no sell)
3. `COLLECT_SELL` (modified): Mine SILICON_CRYSTALS → Factory
4. `COLLECT_SELL` (modified): Mine QUARTZ_SAND → Factory
5. `ACQUIRE_DELIVER`: Collect COPPER → Factory
6. `ACQUIRE_DELIVER`: Collect IRON → Factory
7. ... (more intermediates)
8. `DELIVER_TO_CONSTRUCTION`: Collect FAB_MATS → X1-FB5-I61
9. `DELIVER_TO_CONSTRUCTION`: Collect ADVANCED_CIRCUITRY → X1-FB5-I61

### Example 3: Idempotent Resume

```bash
# First run: Creates new pipeline
./bin/spacetraders construction start --site X1-FB5-I61 --depth 1 --player-id 14
# Output: "Pipeline created: construction-abc123, Tasks: 12"

# Second run (after daemon restart, or any later time): Resumes existing
./bin/spacetraders construction start --site X1-FB5-I61 --depth 1 --player-id 14
# Output: "Resuming existing pipeline: construction-abc123, Progress: 25%"
```

The same command is idempotent - it creates a new pipeline only if none exists for the site.

---

## Implementation Phases

### Phase 1: Domain Layer (Week 1)
- [ ] Add CONSTRUCTION pipeline type
- [ ] Add construction-specific fields to Pipeline
- [ ] Add DELIVER_TO_CONSTRUCTION task type
- [ ] Create ConstructionSite entity
- [ ] Add ConstructionSiteRepository port

### Phase 2: API Integration (Week 1)
- [ ] Implement SupplyConstruction API method
- [ ] Implement GetConstruction API method
- [ ] Create ConstructionSiteRepository adapter

### Phase 3: Application Layer (Week 2)
- [ ] Implement ConstructionPipelinePlanner
- [ ] Implement DeliverToConstructionExecutor
- [ ] Register executor in TaskExecutorRegistry
- [ ] Integrate with PipelineOrchestrator

### Phase 4: Persistence (Week 2)
- [ ] Add migration for construction fields
- [ ] Update ManufacturingPipelineRepository
- [ ] Update ManufacturingTaskRepository

### Phase 5: CLI & gRPC (Week 3)
- [ ] Implement construction CLI command
- [ ] Add gRPC service methods
- [ ] Add daemon server handlers

### Phase 6: Testing & Validation (Week 3)
- [ ] Unit tests for domain entities
- [ ] Integration tests for pipeline planner
- [ ] End-to-end test with test construction site

---

## Testing Strategy

### Unit Tests

```go
// internal/domain/manufacturing/pipeline_test.go
func TestPipeline_RecordDelivery(t *testing.T) {
    pipeline := NewPipeline("test-1", 14, "FAB_MATS", "X1-FB5", PipelineTypeConstruction)
    pipeline.SetTargetQuantity(100)

    err := pipeline.RecordDelivery(50)
    assert.NoError(t, err)
    assert.Equal(t, 50, pipeline.DeliveredQuantity())
    assert.Equal(t, 50.0, pipeline.ConstructionProgress())
    assert.Equal(t, PipelineStatusExecuting, pipeline.Status())

    err = pipeline.RecordDelivery(50)
    assert.NoError(t, err)
    assert.Equal(t, 100, pipeline.DeliveredQuantity())
    assert.Equal(t, PipelineStatusCompleted, pipeline.Status())
}
```

### Integration Tests

```go
// internal/application/manufacturing/services/construction_pipeline_planner_test.go
func TestConstructionPipelinePlanner_Plan_FAB_MATS_Depth1(t *testing.T) {
    planner := setupTestPlanner()

    pipeline, err := planner.Plan(ctx, 14, "X1-FB5", "X1-FB5-I61", "FAB_MATS", 100, 1)

    assert.NoError(t, err)
    assert.Equal(t, PipelineTypeConstruction, pipeline.Type())
    assert.Equal(t, "X1-FB5-I61", pipeline.ConstructionSite())
    assert.Equal(t, 100, pipeline.TargetQuantity())

    tasks := pipeline.Tasks()
    // Should have: IRON_ORE→smelter, QUARTZ_SAND→factory, IRON→factory, FAB_MATS→construction
    assert.Len(t, tasks, 4)

    // Last task should be DELIVER_TO_CONSTRUCTION
    lastTask := tasks[len(tasks)-1]
    assert.Equal(t, TaskTypeDeliverToConstruction, lastTask.Type())
}
```

---

## Open Questions

1. **Ship Selection**: How should ships be selected for construction tasks?
   - Option A: Dedicated construction fleet
   - Option B: Share with existing manufacturing fleet
   - Option C: User configures via flag

2. **Factory Selection**: How to select which factory produces the final good?
   - Option A: Nearest factory with required inputs
   - Option B: Factory with highest supply level
   - Option C: User specifies

3. **Error Handling**: What happens if construction site becomes complete mid-delivery?
   - Option A: Cancel remaining tasks, mark pipeline complete
   - Option B: Sell excess materials
   - Option C: Store for future use

4. **Quantity Management**: How to handle partial cargo loads?
   - Current design: Always deliver full cargo
   - Alternative: Support partial deliveries for final batch

5. **Progress Persistence**: Should progress be persisted on each delivery?
   - Yes: Recoverable after daemon restart
   - No: Recalculate from API on restart

---

## Appendix: API Reference

### POST /my/ships/{shipSymbol}/construction/supply

**Request**:
```json
{
  "shipSymbol": "TORWIND-7",
  "waypointSymbol": "X1-FB5-I61",
  "tradeSymbol": "FAB_MATS",
  "units": 80
}
```

**Response**:
```json
{
  "data": {
    "construction": {
      "symbol": "X1-FB5-I61",
      "materials": [
        {"tradeSymbol": "FAB_MATS", "required": 1600, "fulfilled": 80},
        {"tradeSymbol": "ADVANCED_CIRCUITRY", "required": 400, "fulfilled": 0},
        {"tradeSymbol": "QUANTUM_STABILIZERS", "required": 1, "fulfilled": 1}
      ],
      "isComplete": false
    },
    "cargo": {
      "capacity": 80,
      "units": 0,
      "inventory": []
    }
  }
}
```

### GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/construction

**Response**:
```json
{
  "data": {
    "symbol": "X1-FB5-I61",
    "materials": [
      {"tradeSymbol": "FAB_MATS", "required": 1600, "fulfilled": 0},
      {"tradeSymbol": "ADVANCED_CIRCUITRY", "required": 400, "fulfilled": 0},
      {"tradeSymbol": "QUANTUM_STABILIZERS", "required": 1, "fulfilled": 1}
    ],
    "isComplete": false
  }
}
```
