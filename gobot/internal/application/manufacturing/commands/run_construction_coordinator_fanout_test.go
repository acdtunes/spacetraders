package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-ubwi — the construction drain must parallelize PAST the number of distinct materials remaining.
// Before this bead the dispatch pairs each READY task 1:1 with a hauler and the pipeline stages exactly
// ONE task per material, so len(tasks) == #materials was a hard concurrency ceiling: with 2 materials
// only 2 haulers ever worked, making --max-workers dead. These tests pin the fan-out contract: each
// material fans into multiple concurrent lot-tasks (bounded by max_workers and by the material's own
// remaining requirement, so lots never over-supply), lifting the ceiling to the hauler pool / max_workers.

// newTwoMaterialPipeline builds one EXECUTING construction pipeline carrying TWO materials, each with a
// large bill, and the given max_workers — the realistic "2 materials remaining" gate the ceiling capped.
func newTwoMaterialPipeline(t *testing.T, workers int, goodsList ...string) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, workers)
	for _, good := range goodsList {
		if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(good, 100000)); err != nil {
			t.Fatalf("AddMaterial %s: %v", good, err)
		}
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

// (a) THE CEILING LIFT: 2 materials remaining, 5 idle haulers, max_workers 5. The drain must fan the 2
// materials into >2 concurrent lot-tasks and dispatch all 5 haulers at once. A drain still capped at
// len(tasks)==#materials==2 never reaches a barrier of 5 in-flight, so the test times out and reports
// the regression (peak in-flight stuck at 2).
func TestConstructionDrain_FansMaterialsToLiftHaulerCeiling(t *testing.T) {
	const workers = 5
	pipeline := newTwoMaterialPipeline(t, workers, "FAB_MATS", "ADVANCED_CIRCUITRY")
	tasks := []*manufacturing.ManufacturingTask{
		readyConstructionTask(t, pipeline, "FAB_MATS"),
		readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY"),
	}

	producer := newConcurrentProbeProducer(workers, 40, 40) // barrier: hold until 5 sources overlap
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
		t.Fatalf("expected %d haulers dispatched concurrently for 2 materials; the drain stayed capped at #materials (peak in-flight=%d) — the --max-workers-is-dead ceiling", workers, producer.peakInFlight())
	}
	if peak := producer.peakInFlight(); peak != workers {
		t.Fatalf("expected the ceiling lifted to max_workers=%d concurrent lot-tasks (not #materials=2), got peak %d", workers, peak)
	}
	if got := producer.sourceCount(); got != workers {
		t.Fatalf("expected %d concurrent lot-tasks sourced (fan-out past 2 materials), got %d", workers, got)
	}
	if got := shipRepo.claimCount(); got != workers {
		t.Fatalf("expected %d distinct hull claims across the fanned lot-tasks, got %d", workers, got)
	}
	if resp.TasksDrained <= 2 {
		t.Fatalf("expected >2 lot-tasks drained (ceiling lifted past #materials), got %d", resp.TasksDrained)
	}
}

// fillCapProbeProducer records the hull-fill target the drain stamps per lot (the buy cap) and honours
// it: it "acquires" and "delivers" exactly the capped units for the calling hull. So the pipeline's
// recorded progress equals the SUM of the per-lot buy caps — the direct observable for "the concurrent
// lots never buy past the material's remaining requirement".
type fillCapProbeProducer struct {
	mu         sync.Mutex
	fillByShip map[string]int
	sources    int
}

func (p *fillCapProbeProducer) ProduceGood(ctx context.Context, ship *navigation.Ship, _ *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	fill, _, _ := mfgServices.HullFillTargetFromContext(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fillByShip[ship.ShipSymbol()] = fill
	p.sources++
	return &mfgServices.ProductionResult{QuantityAcquired: fill}, nil
}

func (p *fillCapProbeProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, _, _ string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fillByShip[shipSymbol], nil
}

func (p *fillCapProbeProducer) sourceCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sources
}

// (b) NO OVER-SUPPLY: one material with only 50 units remaining, hull capacity 40, 5 idle haulers,
// max_workers 5. The fan-out must NOT dispatch one hull per hauler (5 lots = 5 hull-loads = 200 units
// bought against a 50-unit need). It must fan into ceil(50/40)=2 lots whose buy caps SUM to exactly the
// 50 units remaining (40 + 10), so the pipeline records exactly 50 — no over-supply past the requirement.
func TestConstructionDrain_FanoutDoesNotOverSupplyPastRequirement(t *testing.T) {
	const remaining = 50
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", remaining, 5)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fillCapProbeProducer{fillByShip: map[string]int{}}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 5)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := producer.sourceCalls(); got != 2 {
		t.Fatalf("a 50-unit material (hull cap 40) must fan into ceil(50/40)=2 lot-tasks, NOT one-per-hauler=5; got %d sources", got)
	}
	if got := shipRepo.claimCount(); got != 2 {
		t.Fatalf("expected only 2 haulers claimed for a 2-lot material (no over-dispatch), got %d", got)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != remaining {
		t.Fatalf("the concurrent lots must buy/deliver AT MOST the %d units remaining (caps 40+10), got %d recorded — over-supply", remaining, got)
	}
	if resp.TasksDrained != 2 {
		t.Fatalf("expected 2 lot-tasks drained, got %d", resp.TasksDrained)
	}
}

// (c) CONFIGURABLE TIMEOUT: the per-supply-task deadline must no longer be the hardcoded 10m that
// abandoned healthy multi-hop hauls at the finish line. The default is raised to 30m, and a config
// value threaded onto the command ([manufacturing].construction_supply_task_timeout_seconds) overrides it.
func TestConstructionDrain_SupplyTaskTimeoutIsConfigurable(t *testing.T) {
	handler := NewRunConstructionCoordinatorHandler(nil, nil, nil, nil, nil, &factoryFakeClock{})

	if got := handler.effectiveSupplyTaskTimeout(&RunConstructionCoordinatorCommand{}); got != 30*time.Minute {
		t.Fatalf("expected the default supply-task timeout raised to 30m (was a hardcoded 10m that abandoned legit long hauls), got %s", got)
	}
	cmd := &RunConstructionCoordinatorCommand{SupplyTaskTimeoutSeconds: 2700} // 45m
	if got := handler.effectiveSupplyTaskTimeout(cmd); got != 45*time.Minute {
		t.Fatalf("expected the configured 45m supply-task timeout to override the default, got %s", got)
	}
}
