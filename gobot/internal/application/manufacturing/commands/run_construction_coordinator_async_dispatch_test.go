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

// A construction haul must not set the cadence of everything else the drain does.
//
// Measured live (era 5, 2026-07-25): the tick joined every supply it dispatched, so it ran for as
// long as the slowest haul. 05:51 dispatched two haulers and the tick did not return until 07:17.
// For those 86 minutes nothing else in the coordinator happened: an ADVANCED_CIRCUITRY task that
// FAILED at 05:50 was sweep-eligible from 05:52 and sat FAILED the whole time, a hull bought during
// the window was never assigned, and a laden hauler idled at the gate holding ~73.5k of circuitry.
// Two of four gate workers, for over an hour, on every long haul.
//
// The tick now HANDS OFF its supplies: workers outlive it, and activation, the retry sweep, site
// reconciliation and hull discovery run on the tick's own cadence underneath them. These tests pin
// what that decoupling must never cost — no hull worked by two workers, no more workers in flight
// than max_workers allows, and no unit bought twice because the first load has not landed yet.

// heldSupplyProducer holds every supply it starts until the test releases it, so one tick's workers
// are still in flight while later ticks run. It records which hull each supply ran on and how many
// overlapped, and honours ctx so a cancelled or abandoned worker still unwinds. Acquired and
// delivered units both follow the buy cap the drain stamped, so the pipeline's recorded progress is
// the direct observable for "the lots never bought past the requirement".
type heldSupplyProducer struct {
	mu       sync.Mutex
	starts   []string       // one entry per supply started, in arrival order
	fills    map[string]int // hull -> the buy cap the drain stamped for its lot
	inFlight int
	peak     int

	started     chan string // one entry per supply started, for sequencing ticks
	release     chan struct{}
	releaseOnce sync.Once
}

func newHeldSupplyProducer() *heldSupplyProducer {
	return &heldSupplyProducer{
		fills:   map[string]int{},
		started: make(chan string, 32),
		release: make(chan struct{}),
	}
}

// releaseAll lets every held supply finish. Safe to call twice, so a test can release explicitly and
// still defer it as a leak guard.
func (p *heldSupplyProducer) releaseAll() { p.releaseOnce.Do(func() { close(p.release) }) }

func (p *heldSupplyProducer) ProduceGood(ctx context.Context, ship *navigation.Ship, _ *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	fill, _, _ := mfgServices.HullFillTargetFromContext(ctx)
	p.mu.Lock()
	p.starts = append(p.starts, ship.ShipSymbol())
	p.fills[ship.ShipSymbol()] = fill
	p.inFlight++
	if p.inFlight > p.peak {
		p.peak = p.inFlight
	}
	p.mu.Unlock()
	p.started <- ship.ShipSymbol()

	select {
	case <-p.release:
	case <-ctx.Done():
	}

	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return &mfgServices.ProductionResult{QuantityAcquired: fill}, nil
}

func (p *heldSupplyProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, _, _ string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fills[shipSymbol], nil
}

// startedHulls is the ordered list of hulls a supply ran on, under lock.
func (p *heldSupplyProducer) startedHulls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.starts...)
}

func (p *heldSupplyProducer) peakInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// awaitSupplyStart blocks until the next supply reports it is under way, so a test sequences its
// ticks against real in-flight work rather than a sleep.
func (p *heldSupplyProducer) awaitSupplyStart(t *testing.T) string {
	t.Helper()
	select {
	case hull := <-p.started:
		return hull
	case <-time.After(2 * time.Second):
		t.Fatal("no supply ever started")
		return ""
	}
}

// dispatchTick runs one tick and fails the test if it does not return promptly — the property under
// test in this file. A drain that joins its supplies blocks here for as long as the haul lasts, so
// the defect reads as a failure instead of a hang.
func dispatchTick(t *testing.T, handler *RunConstructionCoordinatorHandler, cmd *RunConstructionCoordinatorCommand) *RunConstructionCoordinatorResponse {
	t.Helper()
	type outcome struct {
		resp *RunConstructionCoordinatorResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := handler.drainOnce(context.Background(), cmd)
		done <- outcome{resp, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("drainOnce: %v", got.err)
		}
		return got.resp
	case <-time.After(2 * time.Second):
		t.Fatal("the tick did not return while its supply was still hauling — activation, the retry sweep and hull discovery are held behind the haul")
		return nil
	}
}

// sweepingActivator stands in for *services.TaskActivator at the ConstructionActivator port. It does
// the two things ActivateConstructionTasks does on every pass, in the same order and through the same
// domain transitions: FIRST sweep retryable FAILED DELIVER_TO_CONSTRUCTION tasks back to PENDING
// (ResetForRetry), THEN promote PENDING ones to READY. The backoff clock, the retry ceiling and the
// pipeline-status filter are the real activator's own and are pinned in the services package by
// TestActivateConstructionTasks_*; what matters here is that the pass gets to RUN at all.
type sweepingActivator struct {
	repo   *drainStubTaskRepo
	mu     sync.Mutex
	passes int
}

func (a *sweepingActivator) ActivateConstructionTasks(ctx context.Context) int {
	a.mu.Lock()
	a.passes++
	a.mu.Unlock()

	failed, err := a.repo.FindByStatus(ctx, 1, manufacturing.TaskStatusFailed)
	if err != nil {
		return 0
	}
	for _, task := range failed {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction || !task.CanRetry() {
			continue
		}
		if task.ResetForRetry() == nil {
			_ = a.repo.Update(ctx, task)
		}
	}

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

func (a *sweepingActivator) passCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.passes
}

// failedConstructionTask builds a task that has already FAILED once and is still retryable — the
// ADVANCED_CIRCUITRY leg the incident left dead for 86 minutes.
func failedConstructionTask(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good string) *manufacturing.ManufacturingTask {
	t.Helper()
	task := readyConstructionTask(t, pipeline, good)
	if err := task.AssignShip("TORWIND-B"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err := task.Fail("sourcing ADVANCED_CIRCUITRY failed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if task.Status() != manufacturing.TaskStatusFailed || !task.CanRetry() {
		t.Fatalf("precondition: the task must be FAILED and still retryable, got %s (canRetry=%v)", task.Status(), task.CanRetry())
	}
	return task
}

// THE REGRESSION: a FAILED-but-retryable construction task is swept back into the queue while a long
// supply is still hauling. The sweep is the first thing ActivateConstructionTasks does and the tick
// is the only thing that calls it, so a tick that joins its hauls makes the sweep's two-minute
// backoff meaningless — its real period becomes the length of a haul. Against a joining drain the
// first tick never returns and this fails on dispatchTick's watchdog.
func TestConstructionDrain_SweepsFailedTaskWhileALongSupplyIsInFlight(t *testing.T) {
	pipeline := newTwoMaterialPipeline(t, 1, "FAB_MATS", "ADVANCED_CIRCUITRY")
	hauling := readyConstructionTask(t, pipeline, "FAB_MATS")
	stranded := failedConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{hauling, stranded}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-A", nil))
	activator := &sweepingActivator{repo: taskRepo}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(activator), &factoryFakeClock{})
	cmd := newDrainCommand()

	// Tick 1 starts the long haul and hands it off.
	dispatchTick(t, handler, cmd)
	producer.awaitSupplyStart(t)

	// Tick 2, with that haul still in flight, must run the activation pass — and its retry sweep.
	dispatchTick(t, handler, cmd)

	if activator.passCount() < 2 {
		t.Fatalf("expected an activation pass on every tick, got %d while a supply was in flight", activator.passCount())
	}
	if stranded.Status() == manufacturing.TaskStatusFailed {
		t.Fatal("the retryable FAILED leg was never swept: the sweep only runs inside a tick, so a tick held behind a haul leaves it dead for the length of that haul")
	}
	if stranded.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected the swept leg back in the queue as READY, got %s", stranded.Status())
	}
}

// A hull that becomes available while a haul is in flight is put to work on the next tick, not when
// the haul lands. In the incident a 371k gate hauler was bought mid-window and its assigned_at stayed
// empty until the blocked tick finally returned.
func TestConstructionDrain_DispatchesAHullThatArrivesMidHaul(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 400, 2)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 2)

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-B", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	if hull := producer.awaitSupplyStart(t); hull != "TORWIND-B" {
		t.Fatalf("expected the first supply on the only hull TORWIND-B, got %s", hull)
	}

	// The freshly purchased gate hauler joins the fleet mid-haul.
	shipRepo.mu.Lock()
	shipRepo.ships = append(shipRepo.ships, newTestHauler(t, "TORWIND-C", nil))
	shipRepo.mu.Unlock()

	dispatchTick(t, handler, cmd)
	if hull := producer.awaitSupplyStart(t); hull != "TORWIND-C" {
		t.Fatalf("expected the newly available hull TORWIND-C put to work while TORWIND-B still hauls, got %s", hull)
	}
	if peak := producer.peakInFlight(); peak != 2 {
		t.Fatalf("expected both hulls supplying at once (the new one did not wait for the haul to land), got peak %d", peak)
	}
	if got := claimedSymbols(shipRepo); !got["TORWIND-B"] || !got["TORWIND-C"] {
		t.Fatalf("expected both hulls claimed, got %v", got)
	}
}

// NO DOUBLE-CLAIM: a hull a worker is already hauling with is never handed to a second worker. The DB
// is no guard here — ClaimShip treats a re-claim by the SAME container as idempotent success — so the
// drain must refuse the hull itself, and refuse it before it ever tries.
func TestConstructionDrain_NeverDispatchesAHullTwiceConcurrently(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 400, 3)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 3)

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	// One hull, still reported idle by discovery after it is claimed — the shape that lets a later
	// tick re-dispatch it if the drain has no authority of its own on the question.
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-A", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	producer.awaitSupplyStart(t)

	second := dispatchTick(t, handler, cmd)
	third := dispatchTick(t, handler, cmd)

	// Join everything either tick started before judging what ran. A worker is registered at dispatch
	// and joined here, so a second supply cannot escape this assertion by simply not having reached
	// the producer yet.
	producer.releaseAll()
	handler.awaitSupplies(cmd.ContainerID)

	if started := producer.startedHulls(); len(started) != 1 {
		t.Fatalf("TORWIND-A is mid-haul: a later tick must not start a second supply on it, got %v", started)
	}
	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("expected the hull claimed exactly once while its supply runs, got %d claims", got)
	}
	for i, resp := range []*RunConstructionCoordinatorResponse{second, third} {
		if resp.NoWorkReason != noWorkNoIdleHauler {
			t.Fatalf("tick %d had no dispatchable hull and must say so, got %q", i+2, resp.NoWorkReason)
		}
	}
}

// NO DOUBLE-BUY (RULINGS #4): a tick must not buy units an in-flight worker has already paid for. The
// delivered counter only moves once the server accepts a supply, so a load bought and still in the
// air leaves the bill looking untouched — and a tick sizing its buys off that bill would buy the same
// units again, against a requirement the first load is about to meet. Bill 40, one hull-load: the
// second tick must source nothing.
func TestConstructionDrain_DoesNotReBuyWhatAnInFlightSupplyAlreadyPaidFor(t *testing.T) {
	const bill = 40 // exactly one hull-load
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", bill, 3)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 3)

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 3)...)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	producer.awaitSupplyStart(t)

	// Idle hulls, ready tasks and free worker slots all remain — only the outstanding bill does not.
	dispatchTick(t, handler, cmd)

	// Join everything either tick started before judging what was bought: a second buy is registered
	// at dispatch and joined here, so it cannot slip past by not having reached the market yet.
	producer.releaseAll()
	handler.awaitSupplies(cmd.ContainerID)

	if started := producer.startedHulls(); len(started) != 1 {
		t.Fatalf("the whole %d-unit bill is already bought and in the air: a second buy can never be delivered, got supplies on %v", bill, started)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != bill {
		t.Fatalf("expected exactly the %d-unit bill delivered, got %d", bill, got)
	}
}

// A saturated tick is still a working tick: it activates, sweeps and RECONCILES the delivered
// counters against the live site before it declines to dispatch. Reconciliation sits ahead of every
// path that can consult a bill, so the tick that does start a supply has already sized it against
// server truth (the sp-duxru ordering), and a long run of saturated ticks keeps the counters — and
// the gate percentage read off the same row — honest instead of frozen.
func TestConstructionDrain_SaturatedTick_StillReconcilesAgainstTheLiveSite(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 400, 1)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 2)

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(nDrainHaulers(t, 2)...)
	site := liveSite("FAB_MATS", 400, 0)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	producer.awaitSupplyStart(t)

	saturated := dispatchTick(t, handler, cmd)

	if saturated.NoWorkReason != noWorkWorkersSaturated {
		t.Fatalf("a tick whose only worker slot is in flight must report %q, got %q", noWorkWorkersSaturated, saturated.NoWorkReason)
	}
	if site.readCount() < 2 {
		t.Fatalf("expected the live site read on the saturated tick too (the counters drift while hauls land), got %d reads", site.readCount())
	}

	producer.releaseAll()
	handler.awaitSupplies(cmd.ContainerID)
	if started := producer.startedHulls(); len(started) != 1 {
		t.Fatalf("max_workers=1 must leave exactly one supply in flight, got %v", started)
	}
}
