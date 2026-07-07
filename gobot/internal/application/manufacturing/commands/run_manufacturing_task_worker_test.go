package commands

import (
	"context"
	"fmt"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// stubDeferExecutor always signals a supply deferral, simulating the executor
// hitting 'no source to acquire from' during a supply dip.
type stubDeferExecutor struct{}

func (stubDeferExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeDeliverToConstruction
}

func (stubDeferExecutor) Execute(_ context.Context, _ mfgServices.TaskExecutionParams) error {
	return fmt.Errorf("%w: FAB_MATS", mfgServices.ErrDeferToSupply)
}

// capturingTaskRepo records the last persisted task so tests can assert the
// worker parked (rather than failed) the task.
type capturingTaskRepo struct {
	manufacturing.TaskRepository
	updated *manufacturing.ManufacturingTask
}

func (r *capturingTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.updated = task
	return nil
}

// A worker whose executor signals ErrDeferToSupply must PARK the task as a pending
// supply-deferral (re-sourced later by the SupplyMonitor) rather than failing it:
// no retry is consumed, the ship is released, and the task is left in a deferred
// PENDING state - so the coordinator is never told a task permanently failed and
// the pipeline is not terminalized by a supply dip (sp-hs2j).
func TestWorker_ParksTaskOnSupplyDeferral(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask("pipeline-1", 1, "FAB_MATS", "", "", "X1-TEST-I67", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip("SHIP-1"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}

	registry := mfgServices.NewTaskExecutorRegistry()
	registry.Register(stubDeferExecutor{})
	repo := &capturingTaskRepo{}
	handler := NewRunManufacturingTaskWorkerHandler(registry, repo)

	resp, err := handler.Handle(context.Background(), &RunManufacturingTaskWorkerCommand{
		ShipSymbol: "SHIP-1",
		Task:       task,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("Handle returned a transport error: %v", err)
	}

	workerResp, ok := resp.(*RunManufacturingTaskWorkerResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}
	if workerResp.Success {
		t.Fatalf("a parked task is not a success")
	}
	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected task PENDING (parked), got %s", task.Status())
	}
	if task.RetryCount() != 0 {
		t.Fatalf("park must not consume the retry budget, got retryCount=%d", task.RetryCount())
	}
	if task.AssignedShip() != "" {
		t.Fatalf("expected ship released after park, got %q", task.AssignedShip())
	}
	if !task.IsDeferredConstruction() {
		t.Fatalf("parked task must be a deferred construction task so the SupplyMonitor re-sources it")
	}
	if repo.updated == nil || repo.updated.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected the parked PENDING state to be persisted")
	}
}
