package commands

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vr9q — the construction drain's fan-out must SCALE to the whole idle pool and must not let one
// slow hull collapse concurrency. Two coupled defects, both proven live (9 dedicated hulls +
// --max-workers 8 + 2 materials → peaked at 1 lot-task/tick, 0 delivery in 12 min):
//
//	#1 — the per-tick lot ceiling was min(pool, max(#materials, max_workers)); with few materials
//	     (#materials ≤ max_workers, the common case) that capped lots at max_workers, so the errgroup
//	     had NO queue and never fanned past the instantaneous handful — and the pool itself was starved
//	     by sp-e55b (dedicated hulls invisible). The fix taps the whole pool: min(pool, remaining-lots).
//	#2 — with lots ≤ max_workers there is no top-up queue, so once a long ADV feed holds one worker
//	     slot the fast lanes finish and idle, dropping effective concurrency toward 1. The fix mints
//	     enough lots that freed slots keep pulling the next lot (continuous top-up), bounded by
//	     max_workers concurrency (errgroup SetLimit) and by the material's remaining requirement (the
//	     over-supply guard).

// nDedicatedHaulers builds n idle in-system HAULERs all pinned to `fleet`, for the sp-vr9q scale test.
// Numbered from 2 so no symbol ends in "-1" (which would mark the command frigate).
func nDedicatedHaulers(t *testing.T, n int, fleet string) []*navigation.Ship {
	t.Helper()
	ships := make([]*navigation.Ship, 0, n)
	for i := 0; i < n; i++ {
		ships = append(ships, newTestHaulerInFleet(t, fmt.Sprintf("GATE-HAULER-%d", i+2), fleet))
	}
	return ships
}

// sp-vr9q #1 (SCALE TO POOL): 9 idle DEDICATED manufacturing hulls, 2 materials, max_workers 8. The
// drain must fan the 2 materials across the whole dedicated pool and lift concurrency to max_workers=8
// — not the ~1-2 the live incident peaked at. A drain that still discovered only the (excluded)
// opportunistic pool sees 0 hulls and never reaches a barrier of 8, so the test times out and reports
// the regression (peak in-flight stuck at 0/1).
func TestConstructionDrain_ScalesConcurrencyToMaxWorkersAcrossDedicatedPool(t *testing.T) {
	const workers = 8
	pipeline := newTwoMaterialPipeline(t, workers, "FAB_MATS", "ADVANCED_CIRCUITRY")
	tasks := []*manufacturing.ManufacturingTask{
		readyConstructionTask(t, pipeline, "FAB_MATS"),
		readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY"),
	}

	producer := newConcurrentProbeProducer(workers, 40, 40) // barrier: hold until 8 sources overlap
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDedicatedHaulers(t, 9, operationManufacturing)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

	done := make(chan *RunConstructionCoordinatorResponse, 1)
	go func() {
		resp, _ := handler.drainOnce(context.Background(), newDrainCommand())
		done <- resp
	}()

	timedOut := false
	select {
	case <-producer.reached:
	case <-time.After(2 * time.Second):
		timedOut = true
	}
	close(producer.release)
	resp := <-done

	if timedOut {
		t.Fatalf("expected %d dedicated haulers dispatched concurrently for 2 materials; the drain never saw its dedicated fleet / stayed capped (peak in-flight=%d) — the sp-e55b+sp-vr9q incident", workers, producer.peakInFlight())
	}
	if peak := producer.peakInFlight(); peak != workers {
		t.Fatalf("expected concurrency lifted to max_workers=%d across the dedicated pool (not ~1-2), got peak %d", workers, peak)
	}
	if got := shipRepo.claimCount(); got != 9 {
		t.Fatalf("expected the whole 9-hull dedicated pool tapped (lots minted to min(pool, remaining)), got %d claims", got)
	}
	if resp.TasksDrained <= 2 {
		t.Fatalf("expected >2 lot-tasks drained (fan-out past #materials=2 across the pool), got %d", resp.TasksDrained)
	}
}

// slowLaneTopUpProducer blocks the ProduceGood of ONE designated slow hull (a long multi-leg ADV feed)
// until the test releases it, while every other hull sources+delivers immediately and signals each
// non-slow delivery on a buffered channel. It honours the drain's per-lot fill target (captured from
// ctx) so the delivered units respect the over-supply guard — proving top-up without over-buying.
type slowLaneTopUpProducer struct {
	mu            sync.Mutex
	slowShip      string
	release       chan struct{}
	fastDelivered chan string    // one entry per non-slow delivery (buffered)
	fillByShip    map[string]int // ship → fill target the drain stamped (the per-lot buy cap)
	deliveredSum  int            // total units delivered across all lanes (over-supply observable)
}

func (p *slowLaneTopUpProducer) ProduceGood(ctx context.Context, ship *navigation.Ship, _ *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	fill, _, _ := mfgServices.HullFillTargetFromContext(ctx)
	p.mu.Lock()
	p.fillByShip[ship.ShipSymbol()] = fill
	p.mu.Unlock()
	if ship.ShipSymbol() == p.slowShip {
		// Block this lane as a long ADV feed would — but honour cancellation so the tick's per-task
		// timeout / ctx still governs (never an unbounded ctx-ignoring wedge here).
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return &mfgServices.ProductionResult{QuantityAcquired: fill}, nil
}

func (p *slowLaneTopUpProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, _, _ string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	units := p.fillByShip[shipSymbol]
	p.deliveredSum += units
	p.mu.Unlock()
	if shipSymbol != p.slowShip {
		p.fastDelivered <- shipSymbol
	}
	return units, nil
}

func (p *slowLaneTopUpProducer) totalDelivered() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deliveredSum
}

// sp-vr9q #2 (SLOW HULL DOES NOT STARVE THE LANES — continuous top-up): one material with a
// 6-hull-load bill, 6 idle haulers, max_workers 3. The FIRST hull's source blocks (a long ADV feed).
// The other five lanes must STILL be dispatched — the two free worker slots keep pulling the next lot
// as each fast hull frees — so five non-slow deliveries land while the slow hull is still blocked.
// Before the fix the ceiling capped lots at max_workers=3, so only two fast lanes ran and then the pool
// idled behind the slow hull (concurrency held at 1); the test times out waiting for the 3rd delivery.
func TestConstructionDrain_SlowHullDoesNotStarveOtherLanes_TopUpContinues(t *testing.T) {
	const remaining = 240 // 6 hull-loads (hull cap 40)
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", remaining, 3)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &slowLaneTopUpProducer{
		slowShip:      "GATE-HAULER-SLOW",
		release:       make(chan struct{}),
		fastDelivered: make(chan string, 6),
		fillByShip:    map[string]int{},
	}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	// The slow hull is listed FIRST, so it is paired with the original ready task's lot and dispatched
	// in the first worker wave (it genuinely holds a slot while the fast lanes must top up around it).
	ships := []*navigation.Ship{newTestHauler(t, "GATE-HAULER-SLOW", nil)}
	for i := 0; i < 5; i++ {
		ships = append(ships, newTestHauler(t, fmt.Sprintf("GATE-HAULER-%d", i+2), nil))
	}
	shipRepo := newDrainShipRepo(ships...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

	done := make(chan *RunConstructionCoordinatorResponse, 1)
	go func() {
		resp, _ := handler.drainOnce(context.Background(), newDrainCommand())
		done <- resp
	}()

	// While the slow hull is blocked, the five OTHER lanes must all be dispatched via top-up.
	deadline := time.After(2 * time.Second)
	fast := 0
collect:
	for fast < 5 {
		select {
		case <-producer.fastDelivered:
			fast++
		case <-deadline:
			break collect
		}
	}
	close(producer.release) // release the slow lane so drainOnce can finish
	resp := <-done

	if fast < 5 {
		t.Fatalf("a slow hull starved the other lanes: only %d of 5 non-slow lanes were dispatched while it blocked — the drain held concurrency at 1 instead of topping up the freed slots", fast)
	}
	// Over-supply guard preserved: the concurrent lots together never buy past the 240-unit requirement.
	if got := producer.totalDelivered(); got > remaining {
		t.Fatalf("over-supply: the fanned lots delivered %d units past the %d-unit requirement", got, remaining)
	}
	if resp.TasksDrained < 5 {
		t.Fatalf("expected the freed lanes to keep draining (≥5 lot-tasks), got %d", resp.TasksDrained)
	}
}
