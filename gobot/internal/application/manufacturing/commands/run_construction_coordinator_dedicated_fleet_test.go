package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// sp-e55b — the construction drain must SEE and use its own dedicated manufacturing fleet.
//
// The incident (live 2026-07-13): 9 dedicated gate haulers idle + the drain peaked at 1 concurrent
// lot-task and delivered nothing for 12 minutes. Root cause: drainOnce discovered hulls ONLY via
// FindIdleLightHaulers, which by design EXCLUDES every dedicated hull
// (ship_pool_manager.go: `if ship.DedicatedFleet() != "" { continue }`), so the drain's own bought
// fleet was structurally INVISIBLE and it poached opportunistic un-dedicated hulls instead.
//
// The fix: discover FindIdleShipsByFleet(<dedicated fleet>) FIRST (dedicated-first), then supplement
// with the opportunistic FindIdleLightHaulers pool; claim under the dedicated identity so ClaimShip's
// atomic no-poach guard (RULINGS #7) still rejects a hull pinned to another fleet. These tests drive
// drainOnce (the true seam) and assert on the SET/COUNT of atomic claims — never on claim ORDER, which
// is non-deterministic under the concurrent errgroup dispatch.

// newTestHaulerInFleet builds an idle in-system HAULER carrying the given DedicatedFleet tag
// ("" = unpinned/opportunistic), for the sp-e55b dedicated-fleet discovery tests.
func newTestHaulerInFleet(t *testing.T, symbol, fleet string) *navigation.Ship {
	t.Helper()
	ship := newTestHauler(t, symbol, nil)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// claimedSymbols returns the SET of ship symbols the drain atomically claimed this tick, so a test
// asserts membership regardless of the non-deterministic claim order under concurrent dispatch.
func claimedSymbols(r *drainFakeShipRepo) map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.claims))
	for _, c := range r.claims {
		out[c.symbol] = true
	}
	return out
}

// sp-e55b (DISCOVERY — the core defect): with a single ready material-task and a hull-load-sized bill
// (one lot), BOTH an idle DEDICATED manufacturing hull (the drain's own gate hauler, pinned
// "manufacturing") and an idle opportunistic UNPINNED hull are available. The one lot must be worked by
// the DEDICATED hull — never the opportunistic one. Before the fix the drain consulted ONLY
// FindIdleLightHaulers, which excludes the dedicated hull, so it claimed the opportunistic hull instead.
func TestConstructionDrain_PrefersDedicatedFleetOverOpportunistic(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 40) // one hull-load bill → exactly one lot
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	// Opportunistic hull listed FIRST; the drain must STILL prefer the dedicated hull.
	opportunistic := newTestHaulerInFleet(t, "TORWIND-8", "")                 // unpinned former-trade hull, idle
	dedicated := newTestHaulerInFleet(t, "TORWIND-C", operationManufacturing) // gate hauler pinned "manufacturing"
	shipRepo := newDrainShipRepo(opportunistic, dedicated)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	claims := claimedSymbols(shipRepo)
	if len(claims) != 1 || !claims["TORWIND-C"] {
		t.Fatalf("expected the DEDICATED gate hauler TORWIND-C claimed for the single lot (its own fleet was invisible pre-fix); got claims %v", claims)
	}
	if claims["TORWIND-8"] {
		t.Fatalf("the opportunistic hull TORWIND-8 must not be poached while a dedicated hull is idle; got claims %v", claims)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected the task drained by the dedicated hull, got %d", resp.TasksDrained)
	}
}

// sp-e55b (NO-POACH, RULINGS #7): a hull pinned/dedicated to ANOTHER operation ("trade") is NEVER
// claimed by the construction drain, even when it is the only idle hull in-system and a task waits. The
// foreign hull is invisible to BOTH discovery pools (FindIdleLightHaulers excludes every tagged hull;
// FindIdleShipsByFleet matches only the drain's own tag), and the atomic ClaimShip guard is the
// backstop. This protects trade income: the drain must not yank a still-pinned trade hull to the gate.
func TestConstructionDrain_NeverPoachesForeignPinnedHull(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	// The only idle in-system hull is pinned to the TRADE fleet.
	tradePinned := newTestHaulerInFleet(t, "TORWIND-8", "trade")
	shipRepo := newDrainShipRepo(tradePinned)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 0 {
		t.Fatalf("a trade-pinned hull must never be claimed by construction, got %d claims", got)
	}
	if len(producer.produceGoods) != 0 {
		t.Fatalf("no claim means nothing sourced, got %v", producer.produceGoods)
	}
	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("the task must stay READY when only a foreign-pinned hull exists, got %s", task.Status())
	}
	if resp.NoWorkReason != noWorkNoIdleHauler {
		t.Fatalf("expected the no-idle-hauler reason (foreign hull excluded), got %q", resp.NoWorkReason)
	}
}

// sp-e55b (FALLBACK): opportunistic idle hulls SUPPLEMENT when dedicated capacity is insufficient. With
// two ready tasks but only ONE idle dedicated hull plus one idle unpinned hull, the drain must use BOTH
// — the dedicated hull AND the opportunistic hull — proving opportunistic hulls are still drafted (the
// default prefer-then-fallback mode, not exclusive). Before the fix only the opportunistic hull was
// visible, so the dedicated hull was never claimed.
func TestConstructionDrain_FallsBackToOpportunisticWhenDedicatedInsufficient(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 200)
	task1 := readyConstructionTask(t, pipeline, "FAB_MATS")
	task2 := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task1, task2}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	dedicated := newTestHaulerInFleet(t, "TORWIND-C", operationManufacturing)
	opportunistic := newTestHaulerInFleet(t, "TORWIND-8", "")
	shipRepo := newDrainShipRepo(opportunistic, dedicated) // opportunistic listed first

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	claims := claimedSymbols(shipRepo)
	if !claims["TORWIND-C"] {
		t.Fatalf("expected the DEDICATED hull claimed (its own fleet was invisible pre-fix); got claims %v", claims)
	}
	if !claims["TORWIND-8"] {
		t.Fatalf("expected the opportunistic hull claimed as fallback for the second task; got claims %v", claims)
	}
	if resp.TasksDrained != 2 {
		t.Fatalf("expected both tasks drained (dedicated + opportunistic), got %d", resp.TasksDrained)
	}
}

// sp-e55b (EXCLUSIVE MODE, opt-in knob RULINGS #5): with ExclusiveDedicatedFleet set and the dedicated
// fleet holding a member, the drain draws ONLY from its dedicated fleet and NEVER supplements from the
// opportunistic pool — even when the dedicated hull is unavailable (out-of-system) and an idle unpinned
// hull sits ready in-system. Mirrors the contract coordinator's sp-wq7r seal.
func TestConstructionDrain_ExclusiveModeNeverDraftsOpportunistic(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	// A dedicated member exists but is OUT-OF-SYSTEM (not dispatchable this tick); an idle in-system
	// unpinned hull is available. Exclusive mode must still refuse to draft the opportunistic hull.
	dedicatedElsewhere := newTestHaulerAt(t, "TORWIND-C", "X1-OTHER-Z1")
	dedicatedElsewhere.SetDedicatedFleet(operationManufacturing)
	opportunistic := newTestHaulerInFleet(t, "TORWIND-8", "")
	shipRepo := newDrainShipRepo(opportunistic, dedicatedElsewhere)

	cmd := &RunConstructionCoordinatorCommand{PlayerID: 1, SystemSymbol: testSystem, ContainerID: "cc-1", ExclusiveDedicatedFleet: true}
	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 0 {
		t.Fatalf("exclusive mode must not draft the opportunistic hull while a dedicated fleet exists, got %d claims", got)
	}
	if resp.NoWorkReason != noWorkNoIdleHauler {
		t.Fatalf("expected the no-idle-hauler reason under a sealed dedicated fleet, got %q", resp.NoWorkReason)
	}
}
