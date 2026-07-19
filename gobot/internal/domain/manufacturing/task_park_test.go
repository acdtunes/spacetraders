package manufacturing

import "testing"

// A construction delivery that reaches execution with no buy source is a transient
// supply gap, not a failure. ParkForResupply returns the EXECUTING task to PENDING
// so the SupplyMonitor can re-source it - WITHOUT consuming the retry budget, and
// releasing the ship so any ship can take it once supply recovers.
func TestParkForResupply_FromExecuting_ReturnsToPendingDeferredWithoutRetry(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "", "", "X1-TEST-I67", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip("SHIP-1"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}

	if err := task.ParkForResupply(); err != nil {
		t.Fatalf("ParkForResupply: %v", err)
	}

	if task.Status() != TaskStatusPending {
		t.Fatalf("expected PENDING after park, got %s", task.Status())
	}
	if task.RetryCount() != 0 {
		t.Fatalf("park must not consume the retry budget, got retryCount=%d", task.RetryCount())
	}
	if task.AssignedShip() != "" {
		t.Fatalf("expected ship released after park, got %q", task.AssignedShip())
	}
	if task.StartedAt() != nil {
		t.Fatalf("expected startedAt cleared after park")
	}
	if !task.IsDeferredConstruction() {
		t.Fatalf("a parked no-source construction task must be a deferred construction task, re-sourceable by the SupplyMonitor")
	}
}

// A retryable FAILED task that has burned all retries but is parked instead does
// not increment the retry count: parking is orthogonal to the retry budget.
func TestParkForResupply_DoesNotTouchRetryCount(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "", "", "X1-TEST-I67", nil)
	_ = task.MarkReady()
	_ = task.AssignShip("SHIP-1")
	_ = task.StartExecution()

	before := task.RetryCount()
	if err := task.ParkForResupply(); err != nil {
		t.Fatalf("ParkForResupply: %v", err)
	}
	if task.RetryCount() != before {
		t.Fatalf("retry count changed on park: before=%d after=%d", before, task.RetryCount())
	}
}

// A construction delivery whose source market turns out to be DRY at execution time
// must be reverted to the deferred/unsourceable signature so the SupplyMonitor
// re-sources it. ClearSourceForResupply drops both the source market and the factory,
// making an otherwise-sourced construction task IsDeferredConstruction().
func TestClearSourceForResupply_DropsSourceAndFactory_MakesDeferred(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "X1-TEST-F45", "", "X1-TEST-I67", nil)
	_ = task.MarkReady()
	_ = task.AssignShip("SHIP-1")
	_ = task.StartExecution()

	if task.IsDeferredConstruction() {
		t.Fatalf("precondition: a sourced construction task must not already be deferred")
	}

	if err := task.ClearSourceForResupply(); err != nil {
		t.Fatalf("ClearSourceForResupply: %v", err)
	}

	if task.SourceMarket() != "" {
		t.Fatalf("expected source market cleared, got %q", task.SourceMarket())
	}
	if task.FactorySymbol() != "" {
		t.Fatalf("expected factory cleared, got %q", task.FactorySymbol())
	}
	if !task.IsDeferredConstruction() {
		t.Fatalf("a source-cleared construction task must be IsDeferredConstruction() for the SupplyMonitor to re-source it")
	}
}

// ClearSourceForResupply is a construction-only reversion: a non-construction task
// (e.g. ACQUIRE_DELIVER) has no deferred-construction re-sourcing path and must be
// rejected rather than silently stripped of its source.
func TestClearSourceForResupply_RejectsNonConstructionTask(t *testing.T) {
	task := NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-TEST-F45", "X1-TEST-FAC", nil)

	if err := task.ClearSourceForResupply(); err == nil {
		t.Fatalf("expected ClearSourceForResupply to reject a non-construction task")
	}
	if task.SourceMarket() != "X1-TEST-F45" {
		t.Fatalf("a rejected clear must not mutate the source, got %q", task.SourceMarket())
	}
}

// Parking is only valid for in-flight tasks (EXECUTING or ASSIGNED). A terminal
// (COMPLETED) task cannot be parked.
func TestParkForResupply_RejectsTerminalTask(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", nil)
	_ = task.MarkReady()
	_ = task.AssignShip("SHIP-1")
	_ = task.StartExecution()
	if err := task.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if err := task.ParkForResupply(); err == nil {
		t.Fatalf("expected ParkForResupply to reject a COMPLETED task")
	}
}
