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

	cmd := newDrainCommand()
	// The tick hands its lot-tasks off and returns; the workers rendezvous at the barrier behind it.
	resp, err := handler.drainOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	timedOut := false
	select {
	case <-producer.reached:
	case <-time.After(2 * time.Second):
		timedOut = true
	}
	close(producer.release)
	resp.TasksDrained += handler.awaitSupplies(cmd.ContainerID)

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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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

// (c) DEFAULT TIMEOUT: the per-supply-task deadline must no longer be the hardcoded 10m that
// abandoned healthy multi-hop hauls at the finish line. The default is raised to 30m — a fixed
// const (sp-sxyx6 retired the former per-launch config override; "smart" strategy handling is now
// the sole path and the base is no longer operator-tunable).
func TestConstructionDrain_SupplyTaskTimeoutDefaultsTo30Minutes(t *testing.T) {
	handler := NewRunConstructionCoordinatorHandler(nil, nil, nil, nil, nil, &factoryFakeClock{})

	if got := handler.effectiveSupplyTaskTimeout(); got != 30*time.Minute {
		t.Fatalf("expected the default supply-task timeout raised to 30m (was a hardcoded 10m that abandoned legit long hauls), got %s", got)
	}
}

// perGoodProbeProducer counts ProduceGood calls per material, so a test can assert how many lots
// the dispatch plan gave each one.
type perGoodProbeProducer struct {
	mu    sync.Mutex
	calls map[string]int
}

func (p *perGoodProbeProducer) ProduceGood(ctx context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	fill, _, _ := mfgServices.HullFillTargetFromContext(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[node.Good]++
	return &mfgServices.ProductionResult{QuantityAcquired: fill}, nil
}

func (p *perGoodProbeProducer) DeliverToConstructionSite(_ context.Context, _, _, _ string, _ shared.PlayerID) (int, error) {
	return 0, nil
}

func (p *perGoodProbeProducer) callsFor(good string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[good]
}

// (d) PER-MATERIAL LOT CAP: an over-staged queue (two READY tasks for the same material) must still
// dispatch only ceil(remaining/hull-load) lots for it. FAB_MATS wants 200 units (5 lots at capacity
// 40) so the GLOBAL lot ceiling is 6 and cannot be what bounds the small material; only the
// per-material cap stops the second CIRCUITRY task, whose 30-unit bill supports exactly one lot.
// Without it the drain buys two hull-loads against a 30-unit need and starves FAB_MATS of a lot.
func TestConstructionDrain_OverStagedMaterialStillCapsItsLots(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 6)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget("FAB_MATS", 200)); err != nil {
		t.Fatalf("AddMaterial FAB_MATS: %v", err)
	}
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget("ADVANCED_CIRCUITRY", 30)); err != nil {
		t.Fatalf("AddMaterial ADVANCED_CIRCUITRY: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	tasks := []*manufacturing.ManufacturingTask{
		readyConstructionTask(t, pipeline, "FAB_MATS"),
		readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY"),
		readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY"),
	}

	producer := &perGoodProbeProducer{calls: map[string]int{}}
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 6)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := producer.callsFor("ADVANCED_CIRCUITRY"); got != 1 {
		t.Fatalf("a 30-unit material (hull cap 40) supports ceil(30/40)=1 lot however many READY tasks the queue staged; got %d", got)
	}
	if got := producer.callsFor("FAB_MATS"); got != 5 {
		t.Fatalf("the lot the over-staged material must not take belongs to FAB_MATS (ceil(200/40)=5); got %d", got)
	}
}
