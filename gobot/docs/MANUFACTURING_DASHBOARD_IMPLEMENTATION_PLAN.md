# Manufacturing Dashboard Implementation Plan

## Overview

Create a real-time Grafana dashboard for monitoring the parallel manufacturing system, following existing metrics infrastructure patterns. This includes:

1. **New `manufacturing_metrics.go` collector** with Prometheus metrics
2. **New `manufacturing.json` Grafana dashboard** (dedicated manufacturing view)
3. **Updates to `market-dynamics.json`** (key manufacturing overview metrics)

## User Requirements

- **Dashboard scope**: Both new dedicated dashboard AND overview in market-dynamics
- **Data source**: Prometheus only (following existing patterns)
- **Priority visualizations**: Pipeline Health, Factory Supply, Ship Utilization, Economics, Supply/Activity changes over time by goods

---

## Part 1: Manufacturing Metrics Collector

### File: `internal/adapters/metrics/manufacturing_metrics.go`

**Follow pattern from:** `market_metrics.go` (635 lines)

### Metrics to Implement (25+ metrics organized by category)

#### 1. Pipeline Health Metrics (6 metrics)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `manufacturing_pipeline_running_total` | Gauge | player_id, product_good | Active pipelines by product |
| `manufacturing_pipeline_queue_depth` | Gauge | player_id | Total pipelines in queue |
| `manufacturing_pipeline_completed_total` | Counter | player_id, product_good, status | Completed pipelines (completed/failed/cancelled) |
| `manufacturing_pipeline_duration_seconds` | Histogram | player_id, product_good | Pipeline execution time |
| `manufacturing_pipeline_profit_credits` | Histogram | player_id, product_good | Profit per pipeline |

#### 2. Task Execution Metrics (6 metrics)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `manufacturing_tasks_pending_total` | Gauge | player_id, task_type | Tasks waiting for dependencies |
| `manufacturing_tasks_ready_total` | Gauge | player_id, task_type | Tasks ready to execute |
| `manufacturing_tasks_executing_total` | Gauge | player_id, task_type | Tasks currently running |
| `manufacturing_tasks_completed_total` | Counter | player_id, task_type, status | Completed tasks |
| `manufacturing_task_duration_seconds` | Histogram | player_id, task_type | Task execution time |
| `manufacturing_task_retry_total` | Counter | player_id, task_type | Task retry count |

#### 3. Factory Supply Metrics (5 metrics)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `manufacturing_factory_supply_level` | Gauge | player_id, factory_symbol, output_good, supply_level | Current supply at factory |
| `manufacturing_factory_inputs_delivered` | Gauge | player_id, factory_symbol, output_good | Input delivery progress (0-1) |
| `manufacturing_factory_ready_total` | Gauge | player_id | Count of factories ready for collection |
| `manufacturing_factory_cycles_total` | Counter | player_id, factory_symbol, output_good | Production cycles completed |
| `manufacturing_supply_transitions_total` | Counter | player_id, good_symbol, from_level, to_level | Supply level changes |

#### 4. Ship Utilization Metrics (4 metrics)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `manufacturing_ships_assigned_total` | Gauge | player_id | Ships assigned to manufacturing |
| `manufacturing_ships_idle_total` | Gauge | player_id | Ships available for tasks |
| `manufacturing_ship_task_duration_seconds` | Histogram | player_id, task_type | Ship task execution time |
| `manufacturing_ship_utilization_percent` | Gauge | player_id | Ship utilization ratio |

#### 5. Economic Metrics (4 metrics)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `manufacturing_cost_total` | Counter | player_id, cost_type | Cumulative costs (acquire/deliver) |
| `manufacturing_revenue_total` | Counter | player_id | Cumulative revenue from sales |
| `manufacturing_profit_rate` | Gauge | player_id | Credits per hour |
| `manufacturing_margin_percent` | Gauge | player_id, product_good | Profit margin by product |

### Collector Structure

```go
type ManufacturingMetricsCollector struct {
    db *gorm.DB

    // Pipeline Health Metrics
    pipelineRunningTotal    *prometheus.GaugeVec
    pipelineQueueDepth      *prometheus.GaugeVec
    pipelineCompletedTotal  *prometheus.CounterVec
    pipelineDurationSeconds *prometheus.HistogramVec
    pipelineProfitCredits   *prometheus.HistogramVec

    // Task Execution Metrics
    tasksPendingTotal       *prometheus.GaugeVec
    tasksReadyTotal         *prometheus.GaugeVec
    tasksExecutingTotal     *prometheus.GaugeVec
    tasksCompletedTotal     *prometheus.CounterVec
    taskDurationSeconds     *prometheus.HistogramVec
    taskRetryTotal          *prometheus.CounterVec

    // Factory Supply Metrics
    factorySupplyLevel        *prometheus.GaugeVec
    factoryInputsDelivered    *prometheus.GaugeVec
    factoryReadyTotal         *prometheus.GaugeVec
    factoryCyclesTotal        *prometheus.CounterVec
    supplyTransitionsTotal    *prometheus.CounterVec

    // Ship Utilization Metrics
    shipsAssignedTotal         *prometheus.GaugeVec
    shipsIdleTotal             *prometheus.GaugeVec
    shipTaskDurationSeconds    *prometheus.HistogramVec
    shipUtilizationPercent     *prometheus.GaugeVec

    // Economic Metrics
    costTotal         *prometheus.CounterVec
    revenueTotal      *prometheus.CounterVec
    profitRate        *prometheus.GaugeVec
    marginPercent     *prometheus.GaugeVec

    // Lifecycle
    ctx        context.Context
    cancelFunc context.CancelFunc
    wg         sync.WaitGroup

    // Configuration
    pollInterval time.Duration  // 30 seconds (faster than market's 60s)
}
```

### Constructor

```go
func NewManufacturingMetricsCollector(db *gorm.DB) *ManufacturingMetricsCollector {
    return &ManufacturingMetricsCollector{
        db: db,

        // Pipeline Health Metrics
        pipelineRunningTotal: prometheus.NewGaugeVec(
            prometheus.GaugeOpts{
                Namespace: "spacetraders",
                Subsystem: "daemon",
                Name:      "manufacturing_pipeline_running_total",
                Help:      "Number of currently running manufacturing pipelines",
            },
            []string{"player_id", "product_good"},
        ),

        pipelineQueueDepth: prometheus.NewGaugeVec(
            prometheus.GaugeOpts{
                Namespace: "spacetraders",
                Subsystem: "daemon",
                Name:      "manufacturing_pipeline_queue_depth",
                Help:      "Total pipelines in planning or executing state",
            },
            []string{"player_id"},
        ),

        pipelineCompletedTotal: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Namespace: "spacetraders",
                Subsystem: "daemon",
                Name:      "manufacturing_pipeline_completed_total",
                Help:      "Total completed manufacturing pipelines",
            },
            []string{"player_id", "product_good", "status"},
        ),

        pipelineDurationSeconds: prometheus.NewHistogramVec(
            prometheus.HistogramOpts{
                Namespace: "spacetraders",
                Subsystem: "daemon",
                Name:      "manufacturing_pipeline_duration_seconds",
                Help:      "Pipeline execution duration",
                Buckets:   []float64{60, 120, 300, 600, 900, 1200, 1800, 3600},
            },
            []string{"player_id", "product_good"},
        ),

        // ... (similar for all other metrics)

        pollInterval: 30 * time.Second,
    }
}
```

### Required Methods

```go
// Register registers all metrics with Prometheus
func (c *ManufacturingMetricsCollector) Register() error

// Start begins the polling goroutine
func (c *ManufacturingMetricsCollector) Start(ctx context.Context)

// Stop gracefully stops the collector
func (c *ManufacturingMetricsCollector) Stop()

// pollMetrics runs the periodic update loop
func (c *ManufacturingMetricsCollector) pollMetrics(interval time.Duration)

// updateAllMetrics updates all polling-based metrics
func (c *ManufacturingMetricsCollector) updateAllMetrics()

// Category-specific update methods
func (c *ManufacturingMetricsCollector) updatePipelineMetrics(playerID int)
func (c *ManufacturingMetricsCollector) updateTaskMetrics(playerID int)
func (c *ManufacturingMetricsCollector) updateFactoryMetrics(playerID int)
func (c *ManufacturingMetricsCollector) updateShipMetrics(playerID int)
func (c *ManufacturingMetricsCollector) updateEconomicMetrics(playerID int)

// Event recording methods (called from task worker/coordinator)
func (c *ManufacturingMetricsCollector) RecordPipelineCompletion(
    playerID int, productGood, status string, duration time.Duration, profit int)
func (c *ManufacturingMetricsCollector) RecordTaskCompletion(
    playerID int, taskType, status string, duration time.Duration)
func (c *ManufacturingMetricsCollector) RecordSupplyTransition(
    playerID int, good, fromLevel, toLevel string)
```

### SQL Queries for Polling

```sql
-- Pipeline status counts
SELECT product_good, status, COUNT(*) as count
FROM manufacturing_pipelines
WHERE player_id = ?
GROUP BY product_good, status;

-- Task status counts by type
SELECT task_type, status, COUNT(*) as count
FROM manufacturing_tasks
WHERE player_id = ?
GROUP BY task_type, status;

-- Factory states with supply levels
SELECT factory_symbol, output_good, current_supply,
       all_inputs_delivered, ready_for_collection,
       (SELECT COUNT(*) FROM jsonb_object_keys(delivered_inputs)) as delivered_count,
       jsonb_array_length(required_inputs) as required_count
FROM manufacturing_factory_states
WHERE player_id = ?;

-- Economic aggregates (last hour)
SELECT
    SUM(total_cost) as total_costs,
    SUM(total_revenue) as total_revenue,
    SUM(total_revenue - total_cost) as net_profit
FROM manufacturing_pipelines
WHERE player_id = ?
  AND status = 'COMPLETED'
  AND completed_at > NOW() - INTERVAL '1 hour';

-- Ship assignment counts
SELECT
    COUNT(DISTINCT mt.assigned_ship) as assigned_ships
FROM manufacturing_tasks mt
WHERE mt.player_id = ?
  AND mt.status IN ('ASSIGNED', 'EXECUTING')
  AND mt.assigned_ship IS NOT NULL;
```

---

## Part 2: Global Collector Integration

### File: `internal/adapters/metrics/prometheus_collector.go`

Add global accessor pattern (follow existing patterns):

```go
var globalManufacturingCollector *ManufacturingMetricsCollector

// SetGlobalManufacturingCollector sets the global manufacturing metrics collector
func SetGlobalManufacturingCollector(collector *ManufacturingMetricsCollector) {
    globalManufacturingCollector = collector
}

// RecordPipelineCompletion records a pipeline completion event
func RecordPipelineCompletion(playerID int, productGood, status string, duration time.Duration, profit int) {
    if globalManufacturingCollector != nil {
        globalManufacturingCollector.RecordPipelineCompletion(playerID, productGood, status, duration, profit)
    }
}

// RecordTaskCompletion records a task completion event
func RecordTaskCompletion(playerID int, taskType, status string, duration time.Duration) {
    if globalManufacturingCollector != nil {
        globalManufacturingCollector.RecordTaskCompletion(playerID, taskType, status, duration)
    }
}

// RecordSupplyTransition records a supply level change event
func RecordSupplyTransition(playerID int, good, fromLevel, toLevel string) {
    if globalManufacturingCollector != nil {
        globalManufacturingCollector.RecordSupplyTransition(playerID, good, fromLevel, toLevel)
    }
}
```

---

## Part 3: Daemon Server Integration

### File: `internal/adapters/grpc/daemon_server.go`

#### Add to struct (around line 74):

```go
manufacturingMetricsCollector *metrics.ManufacturingMetricsCollector
```

#### Add in `NewDaemonServer()` after market metrics (around line 216):

```go
// Create manufacturing metrics collector
mfgCollector := metrics.NewManufacturingMetricsCollector(db)
if err := mfgCollector.Register(); err != nil {
    listener.Close()
    return nil, fmt.Errorf("failed to register manufacturing metrics collector: %w", err)
}
metrics.SetGlobalManufacturingCollector(mfgCollector)
server.manufacturingMetricsCollector = mfgCollector
```

#### Add in `Start()` method (around line 267):

```go
// Start manufacturing metrics collector
if s.manufacturingMetricsCollector != nil {
    s.manufacturingMetricsCollector.Start(context.Background())
}
```

---

## Part 4: Manufacturing Dashboard (New)

### File: `configs/grafana/dashboards/manufacturing.json`

**Structure**: 5 row sections matching metric categories

### Row 1: Pipeline Overview (y=0)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Active Pipelines | Stat | 4 | x=0 | `sum(spacetraders_daemon_manufacturing_pipeline_running_total)` |
| Pipeline Completion Rate | Gauge | 4 | x=4 | `rate(..completed_total{status="COMPLETED"}[5m]) / rate(..completed_total[5m]) * 100` |
| Avg Pipeline Duration | Stat | 4 | x=8 | `histogram_quantile(0.5, rate(..duration_seconds_bucket[5m]))` |
| Pipeline Status Over Time | Timeseries | 12 | x=12 | `sum by (status) (spacetraders_daemon_manufacturing_pipeline_running_total)` |

### Row 2: Task Execution (y=9)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Task Queue Depth | BarGauge | 8 | x=0 | Tasks by status stacked |
| Task Completion Rate | Timeseries | 8 | x=8 | `rate(tasks_completed_total[5m])` by task_type |
| Task Duration Percentiles | Timeseries | 8 | x=16 | p50/p95 `histogram_quantile()` |

### Row 3: Factory Supply Monitoring (y=18)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Factory Supply Status | Table | 12 | x=0 | Current supply per factory/good (instant query) |
| Supply Level Distribution | Piechart | 6 | x=12 | `sum by (supply_level) (factory_supply_level)` |
| Ready for Collection | Stat | 6 | x=18 | `sum(factory_ready_total)` |

### Row 4: Ship Utilization (y=27)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Ship Utilization | Gauge | 6 | x=0 | `ship_utilization_percent` |
| Ships by Assignment | Piechart | 6 | x=6 | Assigned vs idle |
| Task Time Distribution | Timeseries | 12 | x=12 | Duration histogram by task_type |

### Row 5: Economics (y=36)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Profit Rate | Stat | 6 | x=0 | `manufacturing_profit_rate` (credits/hour) |
| Revenue vs Costs | Timeseries | 10 | x=6 | `rate(revenue_total[5m])` vs `rate(cost_total[5m])` |
| Profit Margin by Product | Table | 8 | x=16 | `margin_percent` grouped by product_good |

### Dashboard Metadata

```json
{
  "title": "SpaceTraders - Manufacturing Pipeline",
  "uid": "manufacturing-pipeline",
  "tags": ["spacetraders", "manufacturing", "production"],
  "time": {
    "from": "now-6h",
    "to": "now"
  },
  "refresh": "30s"
}
```

---

## Part 5: Market Dynamics Dashboard Updates

### File: `configs/grafana/dashboards/market-dynamics.json`

Add new row section **"Manufacturing Overview"** at the end (after Trading Opportunities row):

### New Row: Manufacturing Overview (y=45)

| Panel | Type | Width | Position | Metric/Query |
|-------|------|-------|----------|--------------|
| Active Manufacturing | Stat | 4 | x=0 | `sum(manufacturing_pipeline_running_total)` |
| Supply Level Transitions | Timeseries | 10 | x=4 | `rate(supply_transitions_total[5m])` by good_symbol |
| Manufacturing Profit Rate | Stat | 4 | x=14 | `sum(manufacturing_profit_rate)` |
| HIGH Supply Markets | Timeseries | 6 | x=18 | `sum(market_supply_distribution{supply_level="HIGH"})` over time |

---

## Part 6: Instrument Manufacturing Code

### 1. Task Worker Instrumentation

**File:** `internal/application/trading/commands/run_manufacturing_task_worker.go`

```go
// In Handle() method, after task completion:
func (h *ManufacturingTaskWorkerHandler) Handle(ctx context.Context, cmd *ManufacturingTaskWorkerCommand) error {
    startTime := time.Now()

    // ... existing task execution ...

    // Record metrics
    duration := time.Since(startTime)
    status := "success"
    if err != nil {
        status = "failure"
    }
    metrics.RecordTaskCompletion(cmd.PlayerID, string(task.TaskType()), status, duration)

    return err
}
```

### 2. Coordinator Instrumentation

**File:** `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go`

```go
// In handlePipelineCompletion():
func (c *ParallelManufacturingCoordinator) handlePipelineCompletion(ctx context.Context, pipeline *manufacturing.ManufacturingPipeline) {
    duration := time.Since(pipeline.StartedAt())
    profit := pipeline.NetProfit()

    metrics.RecordPipelineCompletion(
        c.playerID,
        pipeline.ProductGood(),
        string(pipeline.Status()),
        duration,
        profit,
    )
}
```

### 3. Supply Monitor Instrumentation

**File:** `internal/application/trading/services/supply_monitor.go`

```go
// In pollFactories() when supply level changes:
func (m *SupplyMonitor) pollFactories(ctx context.Context) {
    for _, factory := range pendingFactories {
        // ... existing supply check ...

        if supply != factory.PreviousSupply() {
            metrics.RecordSupplyTransition(
                m.playerID,
                factory.OutputGood(),
                factory.PreviousSupply(),
                supply,
            )
        }
    }
}
```

---

## Implementation Order

### Phase 1: Metrics Collector (Core)
1. Create `internal/adapters/metrics/manufacturing_metrics.go`
   - Define all metric fields
   - Implement constructor with metric initialization
   - Implement `Register()` method
2. Implement polling methods
   - `updatePipelineMetrics()`
   - `updateTaskMetrics()`
   - `updateFactoryMetrics()`
   - `updateShipMetrics()`
   - `updateEconomicMetrics()`
3. Implement event recording methods
   - `RecordPipelineCompletion()`
   - `RecordTaskCompletion()`
   - `RecordSupplyTransition()`

### Phase 2: Global Integration
4. Add global accessor functions to `prometheus_collector.go`
5. Wire up collector in `daemon_server.go`

### Phase 3: Dashboards
6. Create `configs/grafana/dashboards/manufacturing.json`
7. Add Manufacturing Overview row to `market-dynamics.json`

### Phase 4: Code Instrumentation
8. Add metrics recording to task worker
9. Add metrics recording to coordinator
10. Add metrics recording to supply monitor

### Phase 5: Testing
11. Run daemon with metrics enabled
12. Execute manufacturing operations
13. Verify metrics appear in Prometheus
14. Verify dashboards display data

---

## Files to Create/Modify

| File | Action | Purpose |
|------|--------|---------|
| `internal/adapters/metrics/manufacturing_metrics.go` | CREATE | Main metrics collector |
| `internal/adapters/metrics/prometheus_collector.go` | MODIFY | Add global accessor |
| `internal/adapters/grpc/daemon_server.go` | MODIFY | Wire up collector |
| `configs/grafana/dashboards/manufacturing.json` | CREATE | Dedicated dashboard |
| `configs/grafana/dashboards/market-dynamics.json` | MODIFY | Add overview row |
| `internal/application/trading/commands/run_manufacturing_task_worker.go` | MODIFY | Record task metrics |
| `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go` | MODIFY | Record pipeline metrics |
| `internal/application/trading/services/supply_monitor.go` | MODIFY | Record supply transitions |

---

## Grafana Dashboard Patterns Reference

From existing `market-dynamics.json`:

### Datasource Configuration
```json
{
  "datasource": {
    "type": "prometheus",
    "uid": "prometheus"
  }
}
```

### Row Panel
```json
{
  "collapsed": false,
  "gridPos": { "h": 1, "w": 24, "x": 0, "y": 0 },
  "id": 100,
  "title": "Row Title",
  "type": "row"
}
```

### Stat Panel
```json
{
  "type": "stat",
  "gridPos": { "h": 8, "w": 4, "x": 0, "y": 1 },
  "options": {
    "reduceOptions": {
      "calcs": ["lastNotNull"]
    }
  }
}
```

### Timeseries Panel
```json
{
  "type": "timeseries",
  "gridPos": { "h": 8, "w": 12, "x": 0, "y": 1 },
  "options": {
    "legend": {
      "calcs": ["last", "mean"],
      "displayMode": "table",
      "placement": "bottom"
    }
  }
}
```

### Piechart Panel
```json
{
  "type": "piechart",
  "options": {
    "displayLabels": ["percent"],
    "legend": {
      "displayMode": "table",
      "placement": "right",
      "values": ["value"]
    }
  }
}
```

---

## Histogram Buckets

Based on expected durations:

| Metric | Buckets |
|--------|---------|
| Pipeline Duration | 60s, 120s, 300s, 600s, 900s, 1200s, 1800s, 3600s |
| Task Duration | 10s, 30s, 60s, 120s, 300s, 600s |
| Ship Task Duration | 10s, 30s, 60s, 120s, 300s, 600s |
| Profit Credits | 1000, 5000, 10000, 50000, 100000, 500000 |

---

## Success Criteria

1. All 25+ metrics are registered and visible in Prometheus
2. Manufacturing dashboard shows real-time pipeline status
3. Task queue visualization shows pending/ready/executing counts
4. Factory supply levels update within 30 seconds of changes
5. Ship utilization percentage is accurate
6. Economic metrics track costs, revenue, and profit rate
7. Supply transitions appear on market-dynamics dashboard
8. No performance degradation (polling at 30s intervals)
