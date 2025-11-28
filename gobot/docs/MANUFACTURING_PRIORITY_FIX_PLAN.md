# Manufacturing Priority System Fix Plan

## Problem Summary

The manufacturing system experienced critical failures where revenue-generating COLLECT_SELL tasks were never assigned to workers, resulting in approximately 2.5M credits in losses over a single session. Despite 131 COLLECT_SELL tasks being READY in the queue, all 15 workers were exclusively assigned to ACQUIRE_DELIVER tasks.

## Root Cause Analysis

### 1. Priority Aging Backlog Problem

**Current Behavior:**
```
effective_priority = base_priority + (minutes_waiting * 2)
```

**The Issue:**
When COLLECT_SELL tasks are reset to PENDING (e.g., after daemon restart or factory depletion), they lose their age. When SupplyMonitor marks them READY again, they start with 0 minutes waiting.

Meanwhile, ACQUIRE_DELIVER tasks that have been READY for 100+ minutes have massive effective priorities:
- ACQUIRE_DELIVER: 10 + (102 * 2) = **214**
- COLLECT_SELL: 10 + (8 * 2) = **26**

Even with equal base priorities, the aging gap means COLLECT_SELL can never compete.

### 2. No Task Type Balancing

The current system has no mechanism to ensure both task types get workers. It purely relies on priority, which leads to starvation when one task type has an aging advantage.

### 3. Slow Task Execution Compounds the Problem

ACQUIRE_DELIVER tasks take 15-20 minutes each:
1. Navigate to source market (5+ min with multi-hop routes)
2. Purchase goods (multiple transactions with cooldowns)
3. Navigate to factory (5-8 min with refueling)
4. Deliver goods

During this time, no workers are freed for COLLECT_SELL tasks, and the aging gap continues to grow.

### 4. In-Memory State Sync Issues

The `assignedTasks` map can become out of sync with the database when:
- Tasks complete but callback fails
- Daemon restarts mid-operation
- Tasks fail without proper cleanup

This was partially addressed with `reconcileAssignedTasksWithDB()` but more robust handling is needed.

## Proposed Solutions

### Solution 1: Task Type Reservation (Recommended - High Impact)

Reserve a percentage of workers for each task type to prevent starvation.

**Implementation:**
```go
// In assignTasks()
const (
    MinCollectSellWorkers = 3  // Always reserve 3 workers for COLLECT_SELL
    MinAcquireDeliverWorkers = 3  // Always reserve 3 for ACQUIRE_DELIVER
)

func (h *Handler) assignTasks(ctx context.Context, ...) {
    // Count current assignments by type
    collectSellCount := h.countAssignedByType(TaskTypeCollectSell)
    acquireDeliverCount := h.countAssignedByType(TaskTypeAcquireDeliver)

    for _, task := range readyTasks {
        // Enforce minimum reservations
        if task.TaskType() == TaskTypeAcquireDeliver {
            // Skip if COLLECT_SELL is below minimum and has ready tasks
            if collectSellCount < MinCollectSellWorkers && h.hasReadyCollectSellTasks() {
                continue
            }
        }
        // ... rest of assignment logic
    }
}
```

**Pros:**
- Guarantees both task types always have workers
- Simple to implement and understand
- Configurable via constants

**Cons:**
- May not be optimal when one task type has no ready tasks

### Solution 2: Priority Ceiling with Age Decay

Cap the maximum effective priority from aging and add decay for very old tasks.

**Implementation:**
```go
const (
    MaxAgingBonus = 100  // Cap aging bonus at 100
    AgingDecayThreshold = 60  // Minutes before decay kicks in
)

func (t *Task) EffectivePriority() int {
    minutesWaiting := time.Since(t.ReadyAt).Minutes()

    // Calculate aging bonus with cap
    agingBonus := int(minutesWaiting * 2)
    if agingBonus > MaxAgingBonus {
        agingBonus = MaxAgingBonus
    }

    // Apply decay for very old tasks (indicates something is wrong)
    if minutesWaiting > AgingDecayThreshold {
        decayMinutes := minutesWaiting - AgingDecayThreshold
        agingBonus -= int(decayMinutes * 0.5)  // Decay at half rate
        if agingBonus < 0 {
            agingBonus = 0
        }
    }

    return t.BasePriority() + agingBonus
}
```

**Pros:**
- Prevents runaway priority accumulation
- Self-correcting for stuck tasks
- Works with existing priority system

**Cons:**
- More complex priority calculation
- May need tuning

### Solution 3: Round-Robin by Task Type

Alternate between task types when assigning workers.

**Implementation:**
```go
func (h *Handler) assignTasks(ctx context.Context, ...) {
    // Separate queues by type
    collectSellTasks := h.getReadyTasksByType(TaskTypeCollectSell)
    acquireDeliverTasks := h.getReadyTasksByType(TaskTypeAcquireDeliver)

    // Round-robin assignment
    csIdx, adIdx := 0, 0
    for len(idleShips) > 0 {
        // Alternate: COLLECT_SELL, ACQUIRE_DELIVER, COLLECT_SELL, ...
        if csIdx < len(collectSellTasks) {
            h.assignTask(collectSellTasks[csIdx], idleShips)
            csIdx++
        }
        if adIdx < len(acquireDeliverTasks) {
            h.assignTask(acquireDeliverTasks[adIdx], idleShips)
            adIdx++
        }
    }
}
```

**Pros:**
- Guaranteed fair distribution
- Simple to understand

**Cons:**
- Ignores priority entirely
- May not be optimal for throughput

### Solution 4: Revenue-Aware Priority Boost

Boost COLLECT_SELL priority when revenue rate drops.

**Implementation:**
```go
func (h *Handler) calculateRevenueBoost() int {
    // Check revenue in last 10 minutes
    recentRevenue := h.ledger.GetRevenueInWindow(10 * time.Minute)
    recentExpenses := h.ledger.GetExpensesInWindow(10 * time.Minute)

    // If expenses >> revenue, boost COLLECT_SELL
    if recentRevenue == 0 && recentExpenses > 100000 {
        return 200  // Emergency boost
    }
    if recentExpenses > recentRevenue * 2 {
        return 50  // Moderate boost
    }
    return 0
}

func (t *Task) EffectivePriority() int {
    base := t.BasePriority() + t.AgingBonus()

    if t.TaskType() == TaskTypeCollectSell {
        base += h.calculateRevenueBoost()
    }

    return base
}
```

**Pros:**
- Automatically responds to financial signals
- Self-correcting behavior

**Cons:**
- Requires ledger integration
- May be reactive rather than proactive

### Solution 5: Fresh Task Priority Boost

Give newly-READY tasks a temporary priority boost to compete with aged tasks.

**Implementation:**
```go
const FreshTaskBoost = 100  // Boost for tasks in first 5 minutes of being READY

func (t *Task) EffectivePriority() int {
    minutesWaiting := time.Since(t.ReadyAt).Minutes()

    base := t.BasePriority()

    // Fresh task boost (decays over 5 minutes)
    if minutesWaiting < 5 {
        freshBoost := int(FreshTaskBoost * (1 - minutesWaiting/5))
        base += freshBoost
    }

    // Normal aging
    base += int(minutesWaiting * 2)

    return base
}
```

**Pros:**
- Ensures newly-ready tasks get a chance immediately
- Natural decay prevents permanent advantage

**Cons:**
- Adds complexity
- May cause priority instability

## Recommended Implementation Order

### Phase 1: Immediate Fixes (High Priority)

1. **Implement Task Type Reservation (Solution 1)**
   - Ensures minimum workers for each task type
   - Prevents complete starvation
   - ~50 lines of code

2. **Add Priority Ceiling (Solution 2 - partial)**
   - Cap aging bonus at 100
   - Prevents runaway priorities
   - ~10 lines of code

### Phase 2: Monitoring & Alerting

3. **Add Revenue Rate Monitoring**
   - Alert when SELL_CARGO transactions drop to zero for 10+ minutes
   - Dashboard widget showing revenue vs expenses rate

4. **Add Task Starvation Metrics**
   - Track time since last task assignment by type
   - Alert when any task type goes 15+ minutes without assignment

### Phase 3: Advanced Optimization

5. **Implement Revenue-Aware Boost (Solution 4)**
   - Automatic priority adjustment based on financial signals
   - Requires thorough testing

6. **Consider Fresh Task Boost (Solution 5)**
   - Only if Phase 1 solutions prove insufficient

## Configuration Recommendations

Add these configuration options to allow runtime tuning:

```go
type ManufacturingConfig struct {
    // Worker reservation
    MinCollectSellWorkers    int  `env:"MFG_MIN_COLLECT_SELL_WORKERS" default:"3"`
    MinAcquireDeliverWorkers int  `env:"MFG_MIN_ACQUIRE_DELIVER_WORKERS" default:"3"`

    // Priority tuning
    MaxAgingBonus           int  `env:"MFG_MAX_AGING_BONUS" default:"100"`
    AgingRatePerMinute      int  `env:"MFG_AGING_RATE_PER_MINUTE" default:"2"`

    // Revenue monitoring
    RevenueAlertThreshold   int  `env:"MFG_REVENUE_ALERT_THRESHOLD" default:"0"`
    RevenueCheckInterval    time.Duration `env:"MFG_REVENUE_CHECK_INTERVAL" default:"5m"`
}
```

## Testing Strategy

1. **Unit Tests**
   - Priority calculation with various aging scenarios
   - Task type reservation logic
   - Revenue boost calculation

2. **Integration Tests**
   - Simulate aging backlog scenario
   - Verify COLLECT_SELL gets assigned with reservation
   - Test daemon restart recovery

3. **Load Tests**
   - Run with 15 workers for 1 hour
   - Verify balanced assignment counts
   - Measure revenue vs expenses

## Files to Modify

1. `internal/domain/manufacturing/task.go`
   - Add priority ceiling to `EffectivePriority()`
   - Add fresh task boost logic

2. `internal/application/trading/commands/run_parallel_manufacturing_coordinator.go`
   - Add task type reservation in `assignTasks()`
   - Add revenue monitoring integration

3. `internal/application/trading/services/task_queue.go`
   - Add methods for counting tasks by type
   - Add method for checking if task type has ready tasks

4. `internal/adapters/metrics/manufacturing_metrics.go`
   - Add starvation metrics
   - Add revenue rate tracking

## Success Criteria

After implementing these fixes:

1. **No task type should go more than 5 minutes without an assignment** when ready tasks exist
2. **Revenue rate should never drop to zero** for more than 10 minutes during active manufacturing
3. **Worker utilization should remain above 80%** (12+ of 15 workers active)
4. **Net profit should be positive** over any 30-minute window during stable operation

## Appendix: Debugging Commands

```bash
# Check task distribution
PGPASSWORD=dev_password psql -h localhost -U spacetraders -d spacetraders -c "
SELECT task_type, status, count(*)
FROM manufacturing_tasks
WHERE player_id = 12
GROUP BY task_type, status
ORDER BY task_type, status;
"

# Check effective priorities
PGPASSWORD=dev_password psql -h localhost -U spacetraders -d spacetraders -c "
SELECT task_type, priority,
       EXTRACT(EPOCH FROM (NOW() - ready_at))/60 as minutes_waiting,
       priority + (EXTRACT(EPOCH FROM (NOW() - ready_at))/60 * 2)::int as effective_priority
FROM manufacturing_tasks
WHERE player_id = 12 AND status = 'READY'
ORDER BY effective_priority DESC
LIMIT 20;
"

# Check recent revenue vs expenses
PGPASSWORD=dev_password psql -h localhost -U spacetraders -d spacetraders -c "
SELECT transaction_type, count(*), SUM(amount) as total
FROM transactions
WHERE player_id = 12
AND created_at > NOW() - INTERVAL '10 minutes'
GROUP BY transaction_type;
"

# Check worker assignments by task type
PGPASSWORD=dev_password psql -h localhost -U spacetraders -d spacetraders -c "
SELECT task_type, count(*) as assigned_workers
FROM manufacturing_tasks
WHERE player_id = 12 AND status = 'ASSIGNED'
GROUP BY task_type;
"
```
