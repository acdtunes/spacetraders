package manufacturing

import "testing"

func addNoDependencyTask(t *testing.T, p *ManufacturingPipeline) *ManufacturingTask {
	t.Helper()
	task := NewAcquireDeliverTask(p.ID(), p.PlayerID(), "IRON", "MARKET-A", "FACTORY-A", nil)
	if err := p.AddTask(task); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	return task
}

func driveTaskToCompleted(t *testing.T, task *ManufacturingTask) {
	t.Helper()
	if err := task.AssignShip("SHIP-1"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err := task.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// sp-j2hq: a construction pipeline must remember the caller-set --min-supply
// EXPORT sourcing floor so it can be re-read later (by the deferred-material
// recovery poll-loop and by a resumed StartOrResume call), not just consumed
// once during the initial planning pass.
func TestConstructionPipelineMinSupplyDefaultsUnsetThenSettable(t *testing.T) {
	p := NewConstructionPipeline("X1-TEST-I1", 1, 3, 5)

	if got := p.MinSupply(); got != "" {
		t.Fatalf("expected a new construction pipeline to default MinSupply to \"\" (unset), got %q", got)
	}

	p.SetMinSupply("SCARCE")

	if got := p.MinSupply(); got != "SCARCE" {
		t.Fatalf("expected SetMinSupply(\"SCARCE\") to persist, got %q", got)
	}
}

func TestPipelineTaskStatsReflectTaskStatesAfterCompletion(t *testing.T) {
	p := NewPipeline("LASER_RIFLES", "MARKET-SELL", 100, 1)
	first := addNoDependencyTask(t, p)
	addNoDependencyTask(t, p)

	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	driveTaskToCompleted(t, first)

	if got := len(p.GetReadyTasks()); got != 1 {
		t.Fatalf("ground truth: expected 1 ready task, got %d", got)
	}
	if got := p.TaskCount(); got != 2 {
		t.Fatalf("TaskCount: expected 2, got %d", got)
	}
	if got := p.TasksDone(); got != 1 {
		t.Fatalf("TasksDone: expected 1, got %d", got)
	}
	if got := p.TasksReady(); got != 1 {
		t.Fatalf("TasksReady: expected 1, got %d", got)
	}
	if got := p.Progress(); got != 50 {
		t.Fatalf("Progress: expected 50, got %.0f", got)
	}
}
