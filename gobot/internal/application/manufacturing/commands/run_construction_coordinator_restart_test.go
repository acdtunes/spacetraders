package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-1jpiw — a daemon restart must leave the construction pipeline making progress on the next
// tick, with no material stranded and no re-purchase.
//
// The incident (2026-07-24 05:01Z): a routine deploy cancelled a supply mid-delivery. The hull had
// already bought 20 ADVANCED_CIRCUITRY for 73,500 credits and was AT the gate. The cancellation
// unwound as a delivery ERROR, so the drain FAILED the task — spending a retry on a deploy — and the
// laden hull sat idle at the destination while the material stayed 0/400 for eight minutes. The
// abandon line blamed a 1h30m deadline that eight minutes had not come close to.
//
// Two properties are pinned here: a cancellation re-queues (never fails) its task, and a hull that
// already holds gate material is paired with THAT material's task so it unloads what was paid for
// instead of an empty hull re-buying it.

// shutdownAbortingProducer models a downstream sourcing/delivery op that HONOURS ctx: it blocks
// until the run's ctx is done, then returns ctx.Err() — how a live nav/market call unwinds when the
// daemon is stopped mid-flight. boughtUnits > 0 loads the hull BEFORE blocking, modelling material
// already paid for at the moment of the interruption (the 20 ADVANCED_CIRCUITRY the incident
// stranded). entered signals the block was reached so a test can time its cancellation.
type shutdownAbortingProducer struct {
	boughtUnits int
	entered     chan struct{}
	once        sync.Once
}

func newShutdownAbortingProducer(boughtUnits int) *shutdownAbortingProducer {
	return &shutdownAbortingProducer{boughtUnits: boughtUnits, entered: make(chan struct{})}
}

func (p *shutdownAbortingProducer) ProduceGood(ctx context.Context, ship *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	if p.boughtUnits > 0 {
		loadHull(ship, node.Good, p.boughtUnits)
	}
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *shutdownAbortingProducer) DeliverToConstructionSite(ctx context.Context, _, _, _ string, _ shared.PlayerID) (int, error) {
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return 0, ctx.Err()
}

// loadHull puts units of good aboard, modelling a market buy that landed before the interruption.
func loadHull(ship *navigation.Ship, good string, units int) {
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		return
	}
	cargo, err := shared.NewCargo(ship.CargoCapacity(), units, []*shared.CargoItem{item})
	if err != nil {
		return
	}
	ship.SetCargo(cargo)
}

// promotingConstructionActivator stands in for *services.TaskActivator at the ConstructionActivator
// port: it promotes PENDING DELIVER_TO_CONSTRUCTION tasks that still carry a source to READY. That
// is the one activator behaviour a re-queued task depends on; it is pinned for the real activator by
// TestActivateConstructionTasks_* in the services package.
type promotingConstructionActivator struct{ repo *drainStubTaskRepo }

func (a *promotingConstructionActivator) ActivateConstructionTasks(ctx context.Context) int {
	pending, err := a.repo.FindByStatus(ctx, 1, manufacturing.TaskStatusPending)
	if err != nil {
		return 0
	}
	promoted := 0
	for _, task := range pending {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction || task.IsDeferredConstruction() {
			continue
		}
		if task.MarkReady() != nil {
			continue
		}
		_ = a.repo.Update(ctx, task)
		promoted++
	}
	return promoted
}

// awaitCondition polls until cond holds, or fails the test. The abandon returns the TICK
// immediately — by design, so the coordinator keeps ticking — while the interrupted worker unwinds
// and re-queues its own task just after, on a detached ctx. A post-tick assertion has to wait for
// that unwind. cond must read through a synchronised seam (the stub repo's snapshot), never the
// task object the worker still owns.
func awaitCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// drainInterruptedTick runs one drain tick and cancels it the moment the producer is entered,
// modelling the deploy that killed the daemon mid-supply. It returns once the tick has abandoned.
func drainInterruptedTick(t *testing.T, handler *RunConstructionCoordinatorHandler, producer *shutdownAbortingProducer, logger *capturingLogger) {
	t.Helper()
	ctx, cancel := context.WithCancel(common.WithLogger(context.Background(), logger))
	defer cancel()
	go func() {
		<-producer.entered
		cancel()
	}()
	if _, err := handler.drainOnce(ctx, newDrainCommand()); err != nil {
		t.Fatalf("an interrupted tick must not error the whole drain, got %v", err)
	}
}

// REGRESSION (B1): a supply cancelled mid-flight is RE-QUEUED for the next activation, not failed.
// A cancellation is exogenous — the daemon stopped, the haul did nothing wrong — so it must not
// spend the task's retry budget. Before the fix the unwinding worker treated `context canceled` as
// a delivery failure: the task went FAILED with a retry charged, waited out a two-minute backoff
// sweep, and after three deploys was terminal FAILED with its material stranded aboard the hull.
func TestConstructionDrain_CancelledSupply_RequeuesTaskWithoutSpendingRetry(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 400)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := newShutdownAbortingProducer(0)
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-A", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	drainInterruptedTick(t, handler, producer, &capturingLogger{})

	awaitCondition(t, "the interrupted worker to persist its task", func() bool {
		_, ok := taskRepo.snapshot(task.ID())
		return ok
	})
	persisted, _ := taskRepo.snapshot(task.ID())

	if persisted.status != manufacturing.TaskStatusPending {
		t.Fatalf("an interrupted supply must be re-queued PENDING for the next activation, got %s", persisted.status)
	}
	if persisted.retryCount != 0 {
		t.Fatalf("a daemon stop is not a task fault and must spend no retry budget, got retryCount=%d", persisted.retryCount)
	}
	if persisted.deferred {
		t.Fatal("nothing about the source failed, so an interrupted task must keep its resolved source (not read as deferred)")
	}
}

// ACCEPTANCE (B1+B2): the whole invariant end-to-end. Tick 1 buys the material, is cancelled
// mid-delivery, and leaves the hull laden. Tick 2 — a fresh daemon — re-promotes the task, pairs it
// with the laden hull, and DELIVERS the units already paid for. No re-source, no re-buy, no
// stranded material. Before the fix tick 1 failed the task, so tick 2 promoted nothing and the
// 73,500 credits of circuitry rode an idle hull while the material stayed 0/400.
func TestConstructionDrain_InterruptedLadenHull_DeliversItsLoadOnTheNextTick(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 20)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	hauler := newTestHauler(t, "TORWIND-A", nil)
	shipRepo := newDrainShipRepo(hauler)

	// Tick 1: the buy lands (20 units aboard), then the deploy cancels the delivery.
	interrupted := newShutdownAbortingProducer(20)
	h1 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, interrupted, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	drainInterruptedTick(t, h1, interrupted, &capturingLogger{})
	awaitCondition(t, "the interrupted worker to persist its task", func() bool {
		_, ok := taskRepo.snapshot(task.ID())
		return ok
	})

	if onHandUnits(hauler, "ADVANCED_CIRCUITRY") != 20 {
		t.Fatalf("precondition: the interrupted hull must still hold the 20 units it paid for, got %d", onHandUnits(hauler, "ADVANCED_CIRCUITRY"))
	}

	// Tick 2: the restarted daemon. A fresh handler and producer, exactly as a new process rebuilds
	// its collaborators; the drain re-polls persistence and resumes.
	resumed := &fakeConstructionProducer{acquire: 20, delivered: 20}
	h2 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, resumed, staticActivator(&promotingConstructionActivator{repo: taskRepo}), &factoryFakeClock{})
	resp, err := h2.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("the tick after a restart must drain, got %v", err)
	}

	if resp.TasksDrained != 1 {
		t.Fatalf("the restarted tick must make progress on the interrupted material, got TasksDrained=%d", resp.TasksDrained)
	}
	if len(resumed.produceGoods) != 0 {
		t.Fatalf("a hull already holding gate material must deliver it, never re-source it — ProduceGood was called for %v (the 73,500-credit re-buy)", resumed.produceGoods)
	}
	if len(resumed.deliverCalls) != 1 || resumed.deliverCalls[0].ship != "TORWIND-A" || resumed.deliverCalls[0].good != "ADVANCED_CIRCUITRY" {
		t.Fatalf("expected the laden hull to unload its own material to the site, got %+v", resumed.deliverCalls)
	}
	if pipeline.ConstructionProgress() <= 0 {
		t.Fatalf("the stranded units must move the gate past 0%%, got %.1f%%", pipeline.ConstructionProgress())
	}
}

// B2 (adoption under contention): with several materials in flight, the hull that already HOLDS a
// material must be paired with THAT material's task. Pairing was index-order and cargo-blind, so the
// laden hull drew whichever task came up — the circuitry rode along undelivered while an empty hull
// re-bought the same material at market. Pool order puts the empty hull first, so index pairing
// mis-assigns unless cargo is consulted.
func TestConstructionDrain_PairsLadenHullWithItsOwnMaterialTask(t *testing.T) {
	circuitry := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 20)
	circuitryTask := readyConstructionTask(t, circuitry, "ADVANCED_CIRCUITRY")
	fabMats := newDrainPipeline(t, "FAB_MATS", 100)
	fabMatsTask := readyConstructionTask(t, fabMats, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 20}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{circuitryTask, fabMatsTask}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{
		circuitry.ID(): circuitry,
		fabMats.ID():   fabMats,
	}}
	shipRepo := newDrainShipRepo(
		newTestHauler(t, "TORWIND-9", nil),
		ladenHauler(t, "TORWIND-A", "ADVANCED_CIRCUITRY", 20),
	)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := handler.drainOnce(context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	for _, good := range producer.produceGoods {
		if good == "ADVANCED_CIRCUITRY" {
			t.Fatalf("the laden hull's material must be delivered from the hold, never re-bought — ProduceGood sourced %v", producer.produceGoods)
		}
	}
	delivered := false
	for _, call := range producer.deliverCalls {
		if call.good == "ADVANCED_CIRCUITRY" && call.ship == "TORWIND-A" {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("expected TORWIND-A to unload the ADVANCED_CIRCUITRY it holds, got %+v", producer.deliverCalls)
	}
}

// B4: an interruption is reported as an interruption. The abandon line blamed the per-task deadline
// unconditionally, so a deploy eight minutes into a 1h30m budget was logged as "exceeded 1h30m0s" —
// sending the next investigator hunting a slow haul that never happened.
func TestConstructionDrain_CancelledSupply_LogsCancellationNotTimeout(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 400)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := newShutdownAbortingProducer(0)
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-A", nil))
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	drainInterruptedTick(t, handler, producer, logger)

	abandon := findLogContaining(t, logger, "ABANDON", "abandon")
	if strings.Contains(strings.ToLower(abandon.message), "exceeded") {
		t.Fatalf("a cancellation must not be reported as a deadline the haul never reached, got: %s", abandon.message)
	}
	if !strings.Contains(strings.ToLower(abandon.message), "interrupted") {
		t.Fatalf("expected the abandon line to name the interruption, got: %s", abandon.message)
	}
}

// B3 (unchanged semantics): a GENUINE deadline is still a timeout — reported as one, still
// abandoned, and still charged to the retry budget. A task that really did outrun its deadline is
// evidence of a real problem, so the bounded-retry path that keeps it visible must survive the
// cancellation fix untouched.
func TestConstructionDrain_DeadlineExceeded_StillReportedAsTimeoutAndFailed(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := newShutdownAbortingProducer(0)
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.taskTimeout = 40 * time.Millisecond // the deadline fires; the parent ctx is never cancelled

	if _, err := handler.drainOnce(common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	abandon := findLogContaining(t, logger, "ABANDON", "abandon")
	if !strings.Contains(abandon.message, "exceeded 40ms") {
		t.Fatalf("a genuine deadline must still be reported as a timeout naming the budget, got: %s", abandon.message)
	}
	if abandon.level != "ERROR" {
		t.Fatalf("a genuine deadline stays an ERROR, got level %s", abandon.level)
	}

	awaitCondition(t, "the timed-out worker to persist its task", func() bool {
		_, ok := taskRepo.snapshot(task.ID())
		return ok
	})
	persisted, _ := taskRepo.snapshot(task.ID())
	if persisted.status != manufacturing.TaskStatusFailed {
		t.Fatalf("a genuine deadline still fails the task so the backoff sweep and retry ceiling keep it visible, got %s", persisted.status)
	}
	if persisted.retryCount != 1 {
		t.Fatalf("a genuine deadline still spends a retry, got retryCount=%d", persisted.retryCount)
	}
}

// B5 (idempotency): back-to-back restarts leave the leg re-queueable, not doubly wedged. Each
// interruption used to charge a retry, so three deploys exhausted maxRetries and stranded the leg
// terminal FAILED — the "third sighting" shape of this defect.
func TestConstructionDrain_RepeatedInterruptions_LeaveTaskRequeueableNotWedged(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 400)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-A", nil))
	activator := &promotingConstructionActivator{repo: taskRepo}

	for restart := 1; restart <= 3; restart++ {
		producer := newShutdownAbortingProducer(0)
		handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(activator), &factoryFakeClock{})
		drainInterruptedTick(t, handler, producer, &capturingLogger{})
		awaitCondition(t, "restart to re-queue its task", func() bool {
			persisted, ok := taskRepo.snapshot(task.ID())
			return ok && persisted.status == manufacturing.TaskStatusPending
		})
		persisted, _ := taskRepo.snapshot(task.ID())
		if persisted.retryCount != 0 {
			t.Fatalf("restart %d spent retry budget on a deploy (retryCount=%d) — three of these strand the leg terminal FAILED", restart, persisted.retryCount)
		}
	}

	// A fourth, uninterrupted tick still drains: the leg was never wedged.
	resumed := &fakeConstructionProducer{acquire: 40, delivered: 40}
	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, resumed, staticActivator(activator), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce after three restarts: %v", err)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("after three restarts the pipeline must still make progress, got TasksDrained=%d", resp.TasksDrained)
	}
}

// findLogContaining returns the first captured line whose message contains any of the substrings,
// failing the test when none does — so a missing line reports what was actually logged.
func findLogContaining(t *testing.T, logger *capturingLogger, substrings ...string) capturedLogEntry {
	t.Helper()
	var found capturedLogEntry
	awaitCondition(t, "the abandon line to be logged", func() bool {
		for _, entry := range logger.snapshot() {
			for _, substring := range substrings {
				if strings.Contains(entry.message, substring) {
					found = entry
					return true
				}
			}
		}
		return false
	})
	return found
}
