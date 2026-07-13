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

// sp-01eh: the construction drain must dispatch its per-hull supplyTask workers CONCURRENTLY
// (bounded by the pipeline's max_workers), not serialize on one hull — the regression that put
// the Admiral gate ~4x further out. These tests pin the concurrency contract at the drainOnce
// seam using a synchronizing fake producer that proves how many workers are in flight at once.

// concurrentProbeProducer is a thread-safe ConstructionProducer that records the PEAK number of
// ProduceGood calls simultaneously in flight. When barrier > 0 it holds every call until
// `barrier` calls overlap (closing reached), then waits for the test to close release — a
// rendezvous that PROVES parallel dispatch: a serial drain never gets a second call in flight,
// so the barrier is never reached and the waiting test fails instead of the barrier hanging it.
type concurrentProbeProducer struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	produced    []string             // goods sourced, one entry per worker source
	delivered   []producerDeliverCall // deliveries, one entry per worker delivery

	barrier     int
	reached     chan struct{} // closed once `barrier` calls are concurrently in flight
	release     chan struct{} // test closes this to let held calls proceed
	reachedOnce sync.Once

	acquire        int
	deliveredUnits int
}

func newConcurrentProbeProducer(barrier, acquire, deliveredUnits int) *concurrentProbeProducer {
	return &concurrentProbeProducer{
		barrier:        barrier,
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
		acquire:        acquire,
		deliveredUnits: deliveredUnits,
	}
}

func (p *concurrentProbeProducer) ProduceGood(ctx context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	p.mu.Lock()
	p.produced = append(p.produced, node.Good)
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	hitBarrier := p.barrier > 0 && p.inFlight >= p.barrier
	p.mu.Unlock()

	if hitBarrier {
		p.reachedOnce.Do(func() { close(p.reached) })
	}
	if p.barrier > 0 {
		// Hold this worker in flight until the test releases it (or the tick is cancelled), so a
		// serial drain cannot advance past the first worker and the barrier stays unreached.
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}

	p.mu.Lock()
	p.inFlight--
	acq := p.acquire
	p.mu.Unlock()
	return &mfgServices.ProductionResult{QuantityAcquired: acq}, nil
}

func (p *concurrentProbeProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, good, site string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delivered = append(p.delivered, producerDeliverCall{ship: shipSymbol, good: good, site: site})
	return p.deliveredUnits, nil
}

func (p *concurrentProbeProducer) peakInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight
}

func (p *concurrentProbeProducer) sourceCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.produced)
}

func (p *concurrentProbeProducer) distinctDeliveryShips() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]struct{}{}
	for _, d := range p.delivered {
		seen[d.ship] = struct{}{}
	}
	return len(seen)
}

// newDrainPipelineWithWorkers builds an EXECUTING construction pipeline whose max_workers (the
// field vestigial until sp-01eh) is set to `workers`, with a large single-material bill so many
// worker deliveries accumulate without capping construction progress.
func newDrainPipelineWithWorkers(t *testing.T, good string, targetQty, workers int) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, workers)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(good, targetQty)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

func readyTasksOnPipeline(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good string, n int) []*manufacturing.ManufacturingTask {
	t.Helper()
	out := make([]*manufacturing.ManufacturingTask, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, readyConstructionTask(t, pipeline, good))
	}
	return out
}

func nDrainHaulers(t *testing.T, n int) []*navigation.Ship {
	t.Helper()
	ships := make([]*navigation.Ship, 0, n)
	// Number from 2 so no symbol ends in "-1": a "-1" suffix marks the command frigate
	// (domainContract.IsCommandHull), which FindIdleLightHaulers holds back as last-resort.
	for i := 0; i < n; i++ {
		ships = append(ships, newTestHauler(t, fmt.Sprintf("HAULER-%d", i+2), nil))
	}
	return ships
}

// #1 — the drain dispatches supplyTask workers CONCURRENTLY across the idle haulers it discovers.
// With 3 ready tasks, 3 idle haulers, and a pipeline that permits 3 workers, all three sources
// must be in flight at once. A serial drain (the regression) never overlaps two sources, so the
// barrier is never reached and this test fails.
func TestConstructionDrain_DispatchesConcurrently(t *testing.T) {
	const workers = 3
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 100000, workers)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 3)

	producer := newConcurrentProbeProducer(workers, 40, 40)
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 3)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

	type result struct {
		resp *RunConstructionCoordinatorResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := handler.drainOnce(context.Background(), newDrainCommand())
		done <- result{resp, err}
	}()

	timedOut := false
	select {
	case <-producer.reached:
	case <-time.After(2 * time.Second):
		timedOut = true
	}
	close(producer.release)
	res := <-done

	if timedOut {
		t.Fatalf("expected %d supplyTask workers in flight at once; the drain serialized on one hull (peak in-flight=%d)", workers, producer.peakInFlight())
	}
	if res.err != nil {
		t.Fatalf("drainOnce: %v", res.err)
	}
	if peak := producer.peakInFlight(); peak != workers {
		t.Fatalf("expected peak concurrency %d, got %d", workers, peak)
	}
	if res.resp.TasksDrained != 3 {
		t.Fatalf("expected all 3 tasks drained concurrently, got %d", res.resp.TasksDrained)
	}
	if got := shipRepo.claimCount(); got != 3 {
		t.Fatalf("expected 3 distinct hull claims, got %d", got)
	}
}

// #2 — max_workers CAPS concurrency (it was vestigial). With 5 ready tasks and 5 idle haulers but
// a pipeline max_workers of 2, at most 2 workers may run at once. The barrier of 2 proves >=2 run
// in parallel; the cap proves no more than 2 ever overlap (peak stays 2 while all 5 are drained).
func TestConstructionDrain_CapsConcurrencyAtMaxWorkers(t *testing.T) {
	const workers = 2
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 100000, workers)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 5)

	producer := newConcurrentProbeProducer(workers, 40, 40)
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 5)...)

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
		t.Fatalf("expected at least %d workers to run in parallel; peak in-flight=%d", workers, producer.peakInFlight())
	}
	if peak := producer.peakInFlight(); peak != workers {
		t.Fatalf("max_workers=%d must cap concurrency at %d, but peak in-flight was %d", workers, workers, peak)
	}
	if resp.TasksDrained != 5 {
		t.Fatalf("expected all 5 tasks eventually drained under the cap, got %d", resp.TasksDrained)
	}
}

// #3 — non-regression + race safety (run under -race): each parallel worker independently sources,
// delivers, and records progress for its OWN task+hull, and the shared pipeline progress reflects
// EVERY worker's delivery with no lost update. Three tasks on one pipeline, three haulers: three
// distinct atomic claims, three sources, three distinct deliveries, and 3x40=120 units recorded.
func TestConstructionDrain_PerWorkerSourcingAndProgress_NoRace(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 1000, 5)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 3)

	producer := newConcurrentProbeProducer(0, 40, 40) // barrier 0: never blocks
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 3)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if resp.TasksDrained != 3 {
		t.Fatalf("expected 3 tasks drained, got %d", resp.TasksDrained)
	}
	if got := producer.sourceCount(); got != 3 {
		t.Fatalf("expected each of 3 workers to source its material, got %d sources", got)
	}
	if got := producer.distinctDeliveryShips(); got != 3 {
		t.Fatalf("expected 3 distinct haulers to each deliver once, got %d", got)
	}
	if got := shipRepo.claimCount(); got != 3 {
		t.Fatalf("expected 3 distinct atomic claims (no double-claim), got %d", got)
	}
	// Every worker's delivery must land on the shared pipeline with no lost update: 3 x 40 = 120.
	mat := pipeline.GetMaterial("FAB_MATS")
	if mat == nil {
		t.Fatal("expected the FAB_MATS material target on the pipeline")
	}
	if mat.DeliveredQuantity() != 120 {
		t.Fatalf("expected 120 units recorded across all workers (no lost update), got %d", mat.DeliveredQuantity())
	}
	for _, task := range tasks {
		if task.Status() != manufacturing.TaskStatusCompleted {
			t.Fatalf("expected all worker tasks COMPLETED, got %s", task.Status())
		}
	}
}
