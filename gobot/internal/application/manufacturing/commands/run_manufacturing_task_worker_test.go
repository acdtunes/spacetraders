package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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

// stubFailingExecutor fails every task with a fixed underlying error, simulating a
// non-construction task (e.g. COLLECT_SELL) dying on a concrete API/domain cause.
type stubFailingExecutor struct {
	taskType manufacturing.TaskType
	err      error
}

func (s stubFailingExecutor) TaskType() manufacturing.TaskType { return s.taskType }

func (s stubFailingExecutor) Execute(_ context.Context, _ mfgServices.TaskExecutionParams) error {
	return s.err
}

// capturedLogEntry records one logged line for assertions.
type capturedLogEntry struct {
	level   string
	message string
}

// capturingLogger records logged entries so tests can assert what reaches the
// container log stream. The renderer prints only level+message and DROPS the
// metadata map (container_runner.go), so a cause hidden in metadata never reaches
// an operator - the entire point of this regression.
type capturingLogger struct {
	entries []capturedLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, capturedLogEntry{level: level, message: message})
}

// A non-construction task that fails must surface the underlying error VERBATIM in the
// log MESSAGE - not only in a structured metadata field that the container-log renderer
// drops. Regression for sp-iqyq: the generic worker failure path logged a bare
// "Manufacturing task failed" with the real cause hidden in metadata, so a blind
// COLLECT_SELL failure (observed 05:35:38) never named its cause and diagnosis required a
// manual ship refresh + inference. sp-fi7q fixed only the construction supply call site;
// this generalizes the fix to EVERY task type with one wrap at the dispatch level, per the
// bead's "one wrap covers all current and future task types".
func TestWorker_SurfacesTaskFailureErrorVerbatim(t *testing.T) {
	task := manufacturing.NewCollectSellTask("pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "X1-TEST-M12", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip("SHIP-1"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}

	// The verbatim signature of the real blind failure: a server 4219 that never surfaced.
	underlyingErr := errors.New(`API error (status 4219): {"message":"cargo does not contain 40 units of FAB_MATS","code":4219}`)

	registry := mfgServices.NewTaskExecutorRegistry()
	registry.Register(stubFailingExecutor{taskType: manufacturing.TaskTypeCollectSell, err: underlyingErr})
	handler := NewRunManufacturingTaskWorkerHandler(registry, &capturingTaskRepo{})

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := handler.Handle(ctx, &RunManufacturingTaskWorkerCommand{
		ShipSymbol: "SHIP-1",
		Task:       task,
		PlayerID:   1,
	})
	if err != nil {
		t.Fatalf("Handle returned a transport error: %v", err)
	}
	if workerResp, ok := resp.(*RunManufacturingTaskWorkerResponse); !ok || workerResp.Success {
		t.Fatalf("expected a non-success response for a failed task, got %+v", resp)
	}

	var found bool
	for _, e := range logger.entries {
		if e.level == "ERROR" && strings.Contains(e.message, underlyingErr.Error()) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an ERROR log whose MESSAGE contains the verbatim cause %q; got entries=%+v", underlyingErr.Error(), logger.entries)
	}
}
