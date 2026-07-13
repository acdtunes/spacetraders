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

// sp-v5d1 / sp-j09q / sp-6zkg — the drain-hang cascade at the coordinator seam.
//
// The cascade (reproduced live twice 2026-07-13): a successful supply removes a hull's cargo
// server-side, but the daemon's cached cargo is not written back (sp-v5d1) -> the next tick sees
// phantom cargo and re-routes the hull to re-deliver (sp-j09q) -> the API rejects it with 400 code
// 4219 'ship has 0 units' -> the drain goes SILENT indefinitely (sp-6zkg), the coordinator stays
// RUNNING but never ticks again until a daemon bounce.
//
// The root (sp-v5d1) is fixed in the executor's post-supply write-back (production_executor.go). These
// tests pin the coordinator's SAFETY NET: a phantom-cargo 4219 must resync + defer (never fail, never
// loop), a wedged supply task must never freeze the whole drain goroutine (a per-task timeout bounds
// it), and a normal delivery still advances (sp-9ptm/2me2/yfzi intact).

// a 4219 error shaped exactly like the wrapped API response the construction supply path surfaces:
// the raw body {"error":{"code":4219,...}} is wrapped through client -> repository -> terminal.
func phantomCargo4219Error(ship, good string) error {
	return fmt.Errorf("failed to supply construction site %s with %s: failed to supply construction (system=X1-TEST): "+
		"API error (status 400): {\"error\":{\"code\":4219,\"message\":\"Ship %s cargo does not contain 80 unit(s) of %s. Ship has 0 unit(s).\"}}",
		constructionSiteWP, good, ship, good)
}

// sp-6zkg / sp-j09q (REPRO — the incident): a supply that fails with API 4219 'ship has 0 units'
// (a phantom-cargo re-delivery) must RESYNC the hull from the server and DEFER the task (RULINGS #1
// never-fail) — NOT fail it, and NOT leave it to re-route the empty hull forever. A generic delivery
// error still fails the task (that distinction is pinned by TestConstructionDrain_FailsTaskOnDeliveryError);
// only the 4219 phantom signal is recovered this way.
func TestConstructionDrain_PhantomCargo4219_ResyncsAndDefersWithoutFailing(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 200)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	// A laden hull (phantom 80) whose delivery the server rejects with 4219 — the exact incident.
	producer := &fakeConstructionProducer{deliverErr: phantomCargo4219Error("HAULER-7", "ADVANCED_CIRCUITRY")}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "ADVANCED_CIRCUITRY", 40))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("a 4219 phantom-cargo rejection must not fail the tick, got %v", err)
	}

	if task.Status() != manufacturing.TaskStatusPending || !task.IsDeferredConstruction() {
		t.Fatalf("a 4219 phantom-cargo rejection must DEFER the task (not fail it), got status=%s deferred=%v", task.Status(), task.IsDeferredConstruction())
	}
	if taskRepo.updated[task.ID()] == manufacturing.TaskStatusFailed {
		t.Fatal("a 4219 phantom-cargo rejection must NOT persist the task as FAILED (RULINGS #1 never-fail)")
	}
	if shipRepo.resyncCount() != 1 {
		t.Fatalf("expected the phantom hull resynced from the server exactly once (clears the desynced cache), got %d resyncs", shipRepo.resyncCount())
	}
	if pipeline.ConstructionProgress() != 0 {
		t.Fatalf("a rejected phantom delivery records no progress, got %.1f%%", pipeline.ConstructionProgress())
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 on a phantom-cargo defer, got %d", resp.TasksDrained)
	}
}

// sp-6zkg (keeps ticking): one hull's phantom-cargo 4219 must NOT stall the drain — a second, healthy
// task in the SAME tick is still sourced, delivered, and completed. Before the fix a 4219 on a hull
// already at the gate wedged group.Wait() and the whole tick went silent; now each task is bounded and
// isolated, so a bad task defers while its peers drain.
func TestConstructionDrain_PhantomCargo4219_KeepsTickingToOtherTasks(t *testing.T) {
	// Two distinct pipelines/materials so each task drains on its own hull under the concurrent dispatch.
	badPipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 200)
	badTask := readyConstructionTask(t, badPipeline, "ADVANCED_CIRCUITRY")
	goodPipeline := newDrainPipeline(t, "FAB_MATS", 100)
	goodTask := readyConstructionTask(t, goodPipeline, "FAB_MATS")

	// The producer 4219s ONLY the phantom good; the healthy good sources + delivers normally.
	producer := &phantomAwareProducer{
		phantomGood: "ADVANCED_CIRCUITRY",
		phantomErr:  phantomCargo4219Error("HAULER-PHANTOM", "ADVANCED_CIRCUITRY"),
		acquire:     40,
		delivered:   40,
	}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{badTask, goodTask}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{
		badPipeline.ID():  badPipeline,
		goodPipeline.ID(): goodPipeline,
	}}
	// A laden phantom hull for the bad task and an empty hull for the good task.
	shipRepo := newDrainShipRepo(
		ladenHauler(t, "HAULER-PHANTOM", "ADVANCED_CIRCUITRY", 40),
		newTestHauler(t, "HAULER-GOOD", nil),
	)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce must not error when one task 4219s, got %v", err)
	}

	// The phantom task deferred; the healthy task completed — the drain kept ticking past the bad one.
	if badTask.Status() != manufacturing.TaskStatusPending || !badTask.IsDeferredConstruction() {
		t.Fatalf("expected the phantom task DEFERRED, got status=%s deferred=%v", badTask.Status(), badTask.IsDeferredConstruction())
	}
	if goodTask.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the healthy task COMPLETED despite the peer's 4219, got %s", goodTask.Status())
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected exactly the healthy task drained (TasksDrained=1), got %d", resp.TasksDrained)
	}
}

// sp-j09q (no re-deliver loop): a hull whose REAL cargo is 0 (empty) is never routed to a phantom
// re-delivery — an empty hull on a dry source defers without any delivery attempt. This is the
// coordinator-side counterpart of the executor write-back: with the cache honest (cargo 0), PHASE-1
// deliver-on-hand is skipped, so there is no re-route-to-re-deliver.
func TestConstructionDrain_EmptyHull_NotRoutedToReDeliver(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 200)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := &fakeConstructionProducer{acquire: 0} // empty hull + dry source
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil)) // cargo 0 (honest, post-write-back)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := handler.drainOnce(context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 0 {
		t.Fatalf("an empty hull must not attempt a (phantom) delivery, got %+v", producer.deliverCalls)
	}
	if shipRepo.resyncCount() != 0 {
		t.Fatalf("a clean empty-hull defer needs no phantom resync, got %d", shipRepo.resyncCount())
	}
	if task.Status() != manufacturing.TaskStatusPending || !task.IsDeferredConstruction() {
		t.Fatalf("expected the empty-hull task deferred on the dry source, got %s (deferred=%v)", task.Status(), task.IsDeferredConstruction())
	}
}

// sp-6zkg (REPRO — the hang, the incident): a WEDGED supply task (a downstream op that never returns,
// even one that ignores ctx — the "silent for hours until a daemon bounce" signature) must NOT freeze
// the whole drain. With a per-task timeout, drainOnce ABANDONS the wedged task and returns; without it
// group.Wait() blocks forever. The watchdog proves the coordinator goroutine is freed (a bounded step),
// not left hung — pre-fix this test times out on the watchdog instead of the per-task deadline.
func TestConstructionDrain_WedgedSupplyTask_TimesOutInsteadOfHanging(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	// A producer whose sourcing BLOCKS indefinitely, ignoring ctx — models the wedged downstream op.
	producer := newBlockingProducer()
	defer producer.unblock() // release the orphaned goroutine at test end so it never leaks

	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil)) // empty hull -> PHASE-2 ProduceGood blocks

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.taskTimeout = 40 * time.Millisecond // tiny bound keeps the test fast

	returned := make(chan struct{})
	go func() {
		_, _ = handler.drainOnce(context.Background(), newDrainCommand())
		close(returned)
	}()

	select {
	case <-returned:
		// drainOnce returned despite the wedged task — the coordinator goroutine was NOT frozen.
	case <-time.After(2 * time.Second):
		t.Fatal("drainOnce did NOT return — a wedged supply task hung the whole drain (no per-task timeout): the sp-6zkg silent hang")
	}

	// Confirm the producer's sourcing was actually entered and blocked (the task genuinely wedged),
	// so the pass is due to the timeout bounding it, not the task never starting.
	if !producer.wasEntered() {
		t.Fatal("expected the wedged supply task to have started before the timeout abandoned it")
	}
}

// phantomAwareProducer 4219s exactly one good (modeling a phantom hull) while sourcing + delivering
// every other good normally, so a multi-task tick can prove the drain keeps ticking past a bad task.
type phantomAwareProducer struct {
	phantomGood string
	phantomErr  error
	acquire     int
	delivered   int
}

func (p *phantomAwareProducer) ProduceGood(_ context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	return &mfgServices.ProductionResult{QuantityAcquired: p.acquire}, nil
}

func (p *phantomAwareProducer) DeliverToConstructionSite(_ context.Context, _, good, _ string, _ shared.PlayerID) (int, error) {
	if good == p.phantomGood {
		return 0, p.phantomErr
	}
	return p.delivered, nil
}

// blockingProducer models a wedged downstream op: ProduceGood blocks on a channel, IGNORING ctx, until
// the test releases it — the worst case the per-task timeout must survive. entered signals the block
// was reached so the test can assert the task genuinely started.
type blockingProducer struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func newBlockingProducer() *blockingProducer {
	return &blockingProducer{release: make(chan struct{}), entered: make(chan struct{})}
}

func (p *blockingProducer) ProduceGood(_ context.Context, _ *navigation.Ship, _ *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	close(p.entered)
	<-p.release // block IGNORING ctx — the pathological wedge the timeout must bound
	return &mfgServices.ProductionResult{QuantityAcquired: 0}, nil
}

func (p *blockingProducer) DeliverToConstructionSite(_ context.Context, _, _, _ string, _ shared.PlayerID) (int, error) {
	return 0, nil
}

func (p *blockingProducer) unblock() { p.once.Do(func() { close(p.release) }) }

func (p *blockingProducer) wasEntered() bool {
	select {
	case <-p.entered:
		return true
	default:
		return false
	}
}
