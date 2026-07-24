package manufacturing

import "testing"

// sp-9p87s — UpdateFactorySymbol assigns a fabrication factory to a deferred construction delivery so
// the buy-only deadlock recovery can un-stick it. Setting the factory makes the task no longer
// IsDeferredConstruction(), so the activator marks it READY and the drain fabricates it (mirrors how
// UpdateSourceMarket un-defers a task by assigning a buy source).
func TestUpdateFactorySymbol_SetsFactoryOnPendingConstructionTask_MakesUndeferred(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "", "", "X1-TEST-I67", nil)
	if !task.IsDeferredConstruction() {
		t.Fatal("precondition: a no-source construction task must start deferred")
	}

	if err := task.UpdateFactorySymbol("X1-TEST-FAC"); err != nil {
		t.Fatalf("UpdateFactorySymbol: %v", err)
	}

	if task.FactorySymbol() != "X1-TEST-FAC" {
		t.Errorf("expected factory X1-TEST-FAC, got %q", task.FactorySymbol())
	}
	if task.IsDeferredConstruction() {
		t.Error("a construction task carrying a factory must no longer be deferred (the drain can fabricate it)")
	}
}

// UpdateFactorySymbol is only allowed for PENDING tasks (like UpdateSourceMarket): assigning a
// factory to an in-flight task would disrupt work already in progress.
func TestUpdateFactorySymbol_RejectsNonPendingTask(t *testing.T) {
	task := NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", nil)
	_ = task.MarkReady()
	_ = task.AssignShip("SHIP-1")
	_ = task.StartExecution()

	if err := task.UpdateFactorySymbol("X1-TEST-FAC"); err == nil {
		t.Fatal("expected UpdateFactorySymbol to reject a non-PENDING (in-flight) task")
	}
	if task.FactorySymbol() != "" {
		t.Errorf("a rejected update must not mutate the factory, got %q", task.FactorySymbol())
	}
}

// Factory recovery is construction-only: a non-construction task (e.g. ACQUIRE_DELIVER, whose
// factorySymbol is its delivery target) must be rejected rather than have its factory rewritten.
func TestUpdateFactorySymbol_RejectsNonConstructionTask(t *testing.T) {
	task := NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-TEST-F45", "X1-TEST-FAC", nil)

	if err := task.UpdateFactorySymbol("X1-TEST-OTHER"); err == nil {
		t.Fatal("expected UpdateFactorySymbol to reject a non-construction task")
	}
	if task.FactorySymbol() != "X1-TEST-FAC" {
		t.Errorf("a rejected update must not mutate the factory, got %q", task.FactorySymbol())
	}
}
