package manufacturing

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-c07v: this is the Design B (task/pipeline-based parallel manufacturing)
// half of the NO-CARGO-DUMP CLAIM GUARD - the factory-tree coordinator's half
// lives in commands/run_factory_coordinator_unrelated_cargo_test.go. AssignTasks
// serves the entire ready-task queue across every factory and good at once, so
// unlike the tree-scoped coordinator's filterUnrelatedCargo, there is no single
// "related goods" list to filter cargo against here. filterShipsWithRemainingCargo
// instead relies on HandleShipsWithExistingCargo having already had its one
// chance to match, liquidate, or jettison every placeable ship before this guard
// ever runs - see that function's doc comment in task_assignment_manager.go for
// the full control-flow argument. These tests pin the filter itself and the
// AssignTasks wiring that calls it.

// Pure-filter case mirroring the TORWIND-38 shape: a hull still holding cargo
// (because HandleShipsWithExistingCargo could not place it - no sell market, a
// saturated market, a duplicate LIQUIDATE task, or an assignment failure) is
// excluded entirely and the skip is logged with the ship, the held goods, and
// reason=unrelated_cargo - all in the MESSAGE TEXT, since the container-log
// renderer drops the metadata map (sp-iqyq convention).
func TestFilterShipsWithRemainingCargo_LadenHull_SkippedAndLogged(t *testing.T) {
	laden := newCargoHauler(t, "TORWIND-38", 40) // IRON_ORE:40

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	got := filterShipsWithRemainingCargo(ctx, map[string]*navigation.Ship{"TORWIND-38": laden})

	if len(got) != 0 {
		t.Fatalf("expected the laden hull to be excluded entirely, got %v", got)
	}

	found := false
	for _, e := range logger.entries {
		if strings.Contains(e.message, "TORWIND-38") && strings.Contains(e.message, "IRON_ORE") && strings.Contains(e.message, "unrelated_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a skip log naming ship=TORWIND-38, the held goods (IRON_ORE), and reason=unrelated_cargo in the MESSAGE TEXT, got entries: %+v", logger.entries)
	}
}

// Regression protection: an empty hull must keep claiming exactly as before the
// guard existed.
func TestFilterShipsWithRemainingCargo_EmptyHull_StillClaimable(t *testing.T) {
	clean := newCargoHauler(t, "CRAFTY-60", 0)

	got := filterShipsWithRemainingCargo(context.Background(), map[string]*navigation.Ship{"CRAFTY-60": clean})

	if len(got) != 1 || got["CRAFTY-60"] != clean {
		t.Fatalf("expected an empty hull to remain claimable (regression), got %v", got)
	}
}

// unrelatedCargoFakeShipRepo hands FindIdleLightHaulers a fixed idle fleet.
type unrelatedCargoFakeShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *unrelatedCargoFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

// unrelatedCargoPassthroughOrphanedHandler simulates HandleShipsWithExistingCargo
// finding no home for any cargo-bearing ship it's handed - every ship comes back
// exactly as it went in. This is the shape a failed placement leaves behind (no
// sell market found, the sell market saturated, a duplicate LIQUIDATE task
// already existed, or a DB/assignment error - see HandleShipsWithExistingCargo's
// control flow in orphaned_cargo_handler.go).
type unrelatedCargoPassthroughOrphanedHandler struct{}

func (unrelatedCargoPassthroughOrphanedHandler) HandleShipsWithExistingCargo(_ context.Context, params OrphanedCargoParams) (map[string]*navigation.Ship, error) {
	return params.IdleShips, nil
}

type unrelatedCargoFakeTaskQueue struct {
	ready []*manufacturing.ManufacturingTask
}

func (q *unrelatedCargoFakeTaskQueue) GetReadyTasks() []*manufacturing.ManufacturingTask {
	return q.ready
}

func (q *unrelatedCargoFakeTaskQueue) HasReadyTasksByType(_ manufacturing.TaskType) bool {
	return false
}

func (q *unrelatedCargoFakeTaskQueue) Remove(_ string) bool {
	return true
}

// unrelatedCargoFakeWorkerManager records which ship each assignment landed on,
// so the wiring test can prove the laden hull specifically was never chosen.
type unrelatedCargoFakeWorkerManager struct {
	assigned []AssignTaskParams
}

func (m *unrelatedCargoFakeWorkerManager) AssignTaskToShip(_ context.Context, params AssignTaskParams) error {
	m.assigned = append(m.assigned, params)
	return nil
}

func (m *unrelatedCargoFakeWorkerManager) HandleWorkerCompletion(_ context.Context, _ string) (*TaskCompletion, error) {
	return nil, nil
}

func (m *unrelatedCargoFakeWorkerManager) HandleTaskFailure(_ context.Context, _ TaskCompletion) error {
	return nil
}

// Wiring case: proves the guard is actually threaded into AssignTasks, not just
// correct in isolation. A laden TORWIND-38-shaped hull that the orphaned-cargo
// handler could not place sits alongside a clean hull; one ACQUIRE_DELIVER task
// is ready. Only the clean hull may receive it - handing the laden hull a fresh
// task would reproduce the exact incident (a task worker discovering the hold
// already occupied by cargo it doesn't own, unable to unload it under the
// no-cargo-dump claim guard, sp-wq7r, and burning a zero-unit no-op buy).
func TestAssignTasks_SkipsLadenHullLeftByOrphanedHandler_AssignsOnlyCleanHull(t *testing.T) {
	laden := newCargoHauler(t, "TORWIND-38", 40) // left holding IRON_ORE:40 by the orphaned handler
	clean := newCargoHauler(t, "CRAFTY-61", 0)

	shipRepo := &unrelatedCargoFakeShipRepo{ships: []*navigation.Ship{laden, clean}}
	taskQueue := &unrelatedCargoFakeTaskQueue{
		ready: []*manufacturing.ManufacturingTask{
			manufacturing.NewAcquireDeliverTask("pipe-1", 1, "COPPER_ORE", "MARKET-C", "FACTORY-B", nil),
		},
	}
	tracker := NewAssignmentTracker()
	workerManager := &unrelatedCargoFakeWorkerManager{}

	mgr := NewTaskAssignmentManager(
		nil, // taskRepo: every internal use is nil-guarded once a tracker is present
		shipRepo,
		taskQueue,
		NewShipSelector(nil),
		tracker,
		NewMarketConditionChecker(nil, nil),
		NewAssignmentReconciler(nil, tracker),
		manufacturing.NewWorkerReservationPolicy(),
		workerManager,
		unrelatedCargoPassthroughOrphanedHandler{},
	)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	assignedCount, err := mgr.AssignTasks(ctx, AssignParams{PlayerID: 1, MaxConcurrentTasks: 5})
	if err != nil {
		t.Fatalf("AssignTasks: %v", err)
	}

	if assignedCount != 1 {
		t.Fatalf("expected exactly 1 task assigned (only the clean hull is eligible), got %d", assignedCount)
	}
	if len(workerManager.assigned) != 1 || workerManager.assigned[0].Ship.ShipSymbol() != "CRAFTY-61" {
		t.Fatalf("expected the ready task assigned to CRAFTY-61 only, got %+v", workerManager.assigned)
	}

	found := false
	for _, e := range logger.entries {
		if strings.Contains(e.message, "TORWIND-38") && strings.Contains(e.message, "unrelated_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a skip log for TORWIND-38 naming reason=unrelated_cargo, got entries: %+v", logger.entries)
	}
}
