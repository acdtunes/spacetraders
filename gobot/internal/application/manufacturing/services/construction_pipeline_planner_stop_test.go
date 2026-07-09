package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stopStubShipRepo serves ships by symbol and records every Save() call so
// tests can assert which ships were force-released by Stop(). Embeds the
// domain interface so any unexpected method call panics loudly.
type stopStubShipRepo struct {
	navigation.ShipRepository

	ships map[string]*navigation.Ship
	saved []*navigation.Ship
}

func (r *stopStubShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	ship, ok := r.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship %s not found", symbol)
	}
	return ship, nil
}

func (r *stopStubShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.saved = append(r.saved, ship)
	return nil
}

// newStopTestAssignedShip builds an idle ship at a fixed waypoint and
// immediately assigns it to containerID, simulating a ship an in-flight
// construction task claimed before the pipeline was stopped.
func newStopTestAssignedShip(t *testing.T, symbol, containerID string, clock shared.Clock) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(30, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(plannerTestMarket, 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		wp,
		fuel,
		100,
		30,
		cargo,
		30,
		"FRAME_FRIGATE",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	if err := ship.AssignToContainer(containerID, clock); err != nil {
		t.Fatalf("assign ship to container: %v", err)
	}
	return ship
}

func newStopPlannerUnderTest(pipelineRepo *plannerStubPipelineRepo, taskRepo *plannerStubTaskRepo, shipRepo *stopStubShipRepo, clock shared.Clock) *ConstructionPipelinePlanner {
	return NewConstructionPipelinePlanner(
		pipelineRepo,
		taskRepo,
		nil,
		nil,
		shipRepo,
		clock,
	)
}

// TestStop_ActivePipeline_CancelsPipelineAndCancellableTasks is the primary
// contract for sp-yzrv: `construction stop <site>` must terminalize the
// pipeline (so it stops spawning new tasks) and cancel every PENDING/READY/
// ASSIGNED task, releasing any ship an ASSIGNED task had claimed. A task that
// is already EXECUTING is left alone to finish or fail naturally - mirroring
// PipelineRecycler's shouldCancelTask policy.
func TestStop_ActivePipeline_CancelsPipelineAndCancellableTasks(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	pendingTask := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil)

	assignedTask := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, "ADVANCED_CIRCUITRY", plannerTestMarket, "", plannerTestSite, nil)
	if err := assignedTask.MarkReady(); err != nil {
		t.Fatalf("assignedTask.MarkReady: %v", err)
	}
	if err := assignedTask.AssignShip("HAULER-1"); err != nil {
		t.Fatalf("assignedTask.AssignShip: %v", err)
	}

	executingTask := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil)
	if err := executingTask.MarkReady(); err != nil {
		t.Fatalf("executingTask.MarkReady: %v", err)
	}
	if err := executingTask.AssignShip("HAULER-2"); err != nil {
		t.Fatalf("executingTask.AssignShip: %v", err)
	}
	if err := executingTask.StartExecution(); err != nil {
		t.Fatalf("executingTask.StartExecution: %v", err)
	}

	clock := &shared.MockClock{CurrentTime: time.Now()}
	assignedShip := newStopTestAssignedShip(t, "HAULER-1", assignedTask.ID(), clock)
	executingShip := newStopTestAssignedShip(t, "HAULER-2", executingTask.ID(), clock)

	pipelineRepo := &plannerStubPipelineRepo{existing: pipeline}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{
		pipeline.ID(): {pendingTask, assignedTask, executingTask},
	}}
	shipRepo := &stopStubShipRepo{ships: map[string]*navigation.Ship{
		"HAULER-1": assignedShip,
		"HAULER-2": executingShip,
	}}

	planner := newStopPlannerUnderTest(pipelineRepo, taskRepo, shipRepo, clock)

	result, err := planner.Stop(context.Background(), 1, plannerTestSite)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := result.Pipeline.Status(); got != manufacturing.PipelineStatusCancelled {
		t.Errorf("expected pipeline CANCELLED (terminal, no longer EXECUTING), got %s", got)
	}
	if !result.Pipeline.IsTerminal() {
		t.Error("expected pipeline to report IsTerminal() == true after Stop")
	}
	if got := len(pipelineRepo.updated); got != 1 {
		t.Errorf("expected pipeline to be persisted once via Update, got %d calls", got)
	}

	if result.TasksCancelled != 2 {
		t.Errorf("expected 2 tasks cancelled (pending + assigned), got %d", result.TasksCancelled)
	}

	if got := pendingTask.Status(); got != manufacturing.TaskStatusFailed {
		t.Errorf("expected PENDING task to be cancelled (FAILED), got %s", got)
	}
	if got := assignedTask.Status(); got != manufacturing.TaskStatusFailed {
		t.Errorf("expected ASSIGNED task to be cancelled (FAILED), got %s", got)
	}
	if got := executingTask.Status(); got != manufacturing.TaskStatusExecuting {
		t.Errorf("expected EXECUTING task to be left alone, got %s", got)
	}

	if got := len(taskRepo.updated); got != 2 {
		t.Errorf("expected 2 cancelled tasks persisted via Update, got %d", got)
	}

	// The ship an ASSIGNED (now-cancelled) task claimed must be force-released
	// so it re-enters coordinator discovery.
	releasedHauler1 := false
	for _, saved := range shipRepo.saved {
		if saved.ShipSymbol() == "HAULER-1" {
			releasedHauler1 = true
			if saved.IsAssigned() {
				t.Error("expected HAULER-1 to be released (unassigned) after Stop")
			}
		}
		if saved.ShipSymbol() == "HAULER-2" {
			t.Error("expected HAULER-2 (assigned to the EXECUTING task) to be left alone, but it was saved/released")
		}
	}
	if !releasedHauler1 {
		t.Error("expected HAULER-1 to be released via shipRepo.Save")
	}
}

// TestStop_NoActivePipeline_ReturnsClearError covers both "never started" and
// "already stopped" - FindByConstructionSite only returns non-terminal
// pipelines, so both cases surface identically as "no active pipeline",
// which is exactly the guard sp-yzrv asked for.
func TestStop_NoActivePipeline_ReturnsClearError(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	planner := newStopPlannerUnderTest(pipelineRepo, taskRepo, &stopStubShipRepo{ships: map[string]*navigation.Ship{}}, clock)

	result, err := planner.Stop(context.Background(), 1, plannerTestSite)
	if err == nil {
		t.Fatal("expected an error when no active construction pipeline exists, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
	if !strings.Contains(err.Error(), plannerTestSite) {
		t.Errorf("expected error to mention the construction site %q, got: %v", plannerTestSite, err)
	}
}

// TestStop_AlreadyStoppedPipeline_ReturnsClearError documents the terminal
// (already-cancelled) case explicitly, even though it shares a code path
// with TestStop_NoActivePipeline_ReturnsClearError via the repository's
// non-terminal-only filtering contract.
func TestStop_AlreadyStoppedPipeline_ReturnsClearError(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	if err := pipeline.Cancel(); err != nil {
		t.Fatalf("pipeline.Cancel: %v", err)
	}

	pipelineRepo := &plannerStubPipelineRepo{existing: pipeline}
	taskRepo := &plannerStubTaskRepo{}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	planner := newStopPlannerUnderTest(pipelineRepo, taskRepo, &stopStubShipRepo{ships: map[string]*navigation.Ship{}}, clock)

	result, err := planner.Stop(context.Background(), 1, plannerTestSite)
	if err == nil {
		t.Fatal("expected an error when the construction pipeline is already stopped, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
}

// TestStop_UnrelatedPipelineTasks_AreNotCancelled proves Stop only reaches
// for tasks under the target pipeline's own ID. taskRepo is a stub embedding
// the real interface with every other method nil, so a Stop() that widened
// its scope (e.g. to "all tasks for this player") would nil-panic here
// instead of silently touching another pipeline's work - construction
// pipelines share the mfg coordinator with FABRICATION/COLLECTION pipelines,
// so this isolation is the whole point of the guard.
func TestStop_UnrelatedPipelineTasks_AreNotCancelled(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	targetTask := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil)

	const unrelatedPipelineID = "unrelated-fabrication-pipeline"
	unrelatedTask := manufacturing.NewAcquireDeliverTask(unrelatedPipelineID, 1, "IRON_ORE", plannerTestMarket, "", nil)

	pipelineRepo := &plannerStubPipelineRepo{existing: pipeline}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{
		pipeline.ID():       {targetTask},
		unrelatedPipelineID: {unrelatedTask},
	}}
	clock := &shared.MockClock{CurrentTime: time.Now()}
	planner := newStopPlannerUnderTest(pipelineRepo, taskRepo, &stopStubShipRepo{ships: map[string]*navigation.Ship{}}, clock)

	if _, err := planner.Stop(context.Background(), 1, plannerTestSite); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := targetTask.Status(); got != manufacturing.TaskStatusFailed {
		t.Errorf("expected the target pipeline's task to be cancelled, got %s", got)
	}
	if got := unrelatedTask.Status(); got != manufacturing.TaskStatusPending {
		t.Errorf("expected the unrelated pipeline's task to be untouched (still PENDING), got %s", got)
	}
	for _, updated := range taskRepo.updated {
		if updated.ID() == unrelatedTask.ID() {
			t.Error("expected the unrelated pipeline's task to never be persisted via Update")
		}
	}
}
