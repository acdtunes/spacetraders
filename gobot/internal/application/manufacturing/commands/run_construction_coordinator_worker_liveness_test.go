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

// A supply worker outlives the tick that dispatched it, so the worker REGISTRY — not a joined
// errgroup — is what knows a hull is busy, and dispatchableHaulers refuses every hull it holds. An
// errgroup knew its goroutines had returned. The registry only knows what a worker tells it, and a
// worker can stop running without telling it anything: a panic on a goroutine no caller ever sees, or
// a wedge in a call that ignores ctx and sits outside every task deadline (the claim, the release).
// Either one retires that hull for the life of the process — its cargo undelivered, its slot under
// max_workers gone — while the drain goes on reporting RUNNING on capacity it no longer has.
//
// These tests pin the two guarantees that close it: every exit path deregisters, and a registration
// nobody deregisters expires. And the two things neither may cost: a haul still inside its own
// deadline is never reclaimed, and a worker that comes back after the drain gave it up takes nothing
// from whoever holds its hull now.

// panickingProducer blows up ONCE on the leg the test arms — a nil deref deep in sourcing or in a
// delivery — then behaves. That is the shape of a worker that stops existing mid-supply: the panic is
// raised on the task's own goroutine, where a caller never sees it.
type panickingProducer struct {
	mu             sync.Mutex
	panicOnSource  bool
	panicOnDeliver bool
	sourced        []string
	delivered      []string
	units          int
}

func (p *panickingProducer) ProduceGood(_ context.Context, ship *navigation.Ship, _ *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	p.mu.Lock()
	blowUp := p.panicOnSource
	p.panicOnSource = false
	p.sourced = append(p.sourced, ship.ShipSymbol())
	units := p.units
	p.mu.Unlock()
	if blowUp {
		panic("sourcing blew up on " + ship.ShipSymbol())
	}
	return &mfgServices.ProductionResult{QuantityAcquired: units}, nil
}

func (p *panickingProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, _, _ string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	blowUp := p.panicOnDeliver
	p.panicOnDeliver = false
	p.delivered = append(p.delivered, shipSymbol)
	units := p.units
	p.mu.Unlock()
	if blowUp {
		panic("delivery blew up on " + shipSymbol)
	}
	return units, nil
}

func (p *panickingProducer) sourcedHulls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sourced...)
}

func (p *panickingProducer) deliveredHulls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.delivered...)
}

// wedgedClaimShipRepo never returns from the FIRST ClaimShip until the test lets it — a DB write that
// hangs. That call sits BEFORE the worker's task deadline exists, so nothing bounds the goroutine
// parked in it: the registration it already holds is the only thing that can ever come back.
type wedgedClaimShipRepo struct {
	*drainFakeShipRepo
	entered     chan string
	returned    chan string
	release     chan struct{}
	wedgeOnce   sync.Once
	releaseOnce sync.Once

	releaseMu sync.Mutex
	releases  []string // every hull a release was ATTEMPTED on, guard or no guard
}

func newWedgedClaimShipRepo(ships ...*navigation.Ship) *wedgedClaimShipRepo {
	return &wedgedClaimShipRepo{
		drainFakeShipRepo: newDrainShipRepo(ships...),
		entered:           make(chan string, 4),
		returned:          make(chan string, 4),
		release:           make(chan struct{}),
	}
}

// SaveWithRetry records the ATTEMPT before delegating, so a release the drain should never have tried
// is visible here even though the repo's own CAS guard would have made it a no-op.
func (r *wedgedClaimShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	r.releaseMu.Lock()
	r.releases = append(r.releases, symbol)
	r.releaseMu.Unlock()
	return r.drainFakeShipRepo.SaveWithRetry(ctx, symbol, playerID, mutate)
}

func (r *wedgedClaimShipRepo) releaseCount(hull string) int {
	r.releaseMu.Lock()
	defer r.releaseMu.Unlock()
	count := 0
	for _, symbol := range r.releases {
		if symbol == hull {
			count++
		}
	}
	return count
}

func (r *wedgedClaimShipRepo) ClaimShip(ctx context.Context, symbol, containerID string, playerID shared.PlayerID, operation string) error {
	wedge := false
	r.wedgeOnce.Do(func() { wedge = true })
	if wedge {
		r.entered <- symbol
		<-r.release
		// The DB does NOT reject this late re-claim: ClaimShip is idempotent for the same container,
		// which is exactly why the registry has to be the guard.
		err := r.drainFakeShipRepo.ClaimShip(ctx, symbol, containerID, playerID, operation)
		r.returned <- symbol
		return err
	}
	return r.drainFakeShipRepo.ClaimShip(ctx, symbol, containerID, playerID, operation)
}

// releaseAll lets the wedged claim return. Safe to call twice so a test can release explicitly and
// still defer it as a leak guard.
func (r *wedgedClaimShipRepo) releaseAll() { r.releaseOnce.Do(func() { close(r.release) }) }

func (r *wedgedClaimShipRepo) awaitWedge(t *testing.T) string {
	t.Helper()
	select {
	case hull := <-r.entered:
		return hull
	case <-time.After(2 * time.Second):
		t.Fatal("no worker ever reached the claim: nothing is wedged, so this test proves nothing")
		return ""
	}
}

func (r *wedgedClaimShipRepo) awaitUnwedged(t *testing.T) {
	t.Helper()
	select {
	case <-r.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the wedged claim never returned after it was released")
	}
}

// A PANIC mid-supply must cost the drain that one supply and nothing else. The panic is raised on the
// task's own goroutine: unrecovered it takes the whole daemon down, and merely surviving it is not
// enough either — the hull has to come back, and so does the leg. Here the sweep re-stages the failed
// leg and the SAME hull works it to completion on the next tick.
func TestConstructionDrain_APanickingSupplyDoesNotRetireItsHull(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 40, 2)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &panickingProducer{panicOnSource: true, units: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&sweepingActivator{repo: taskRepo}), &factoryFakeClock{})
	cmd := newDrainCommand()

	// Tick 1: the only hull's supply panics in sourcing.
	if _, err := drainSettled(t, handler, context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if got := producer.sourcedHulls(); len(got) != 1 {
		t.Fatalf("expected the first supply to reach sourcing and blow up there, got %v", got)
	}

	// Tick 2: the swept leg is READY again and the hull must be free to work it.
	resp, err := drainSettled(t, handler, context.Background(), cmd)
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := producer.sourcedHulls(); len(got) != 2 || got[1] != "HAULER-7" {
		t.Fatalf("HAULER-7 must be dispatchable again after its worker panicked — a hull whose worker died is retired for the life of the process otherwise, got %v", got)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != 40 {
		t.Fatalf("expected the re-dispatched hull to deliver the full 40-unit bill, got %d", got)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected the recovered supply counted as drained, got %d", resp.TasksDrained)
	}
	if task.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the panicked leg swept, re-run and COMPLETED, got %s", task.Status())
	}
}

// THE LIVENESS BOUND: a worker wedged in a call that ignores ctx never reaches its deregistration and
// never reaches a task deadline either — the claim runs before one exists. Its registration is all
// that is left of it, and unbounded that registration retires the hull permanently. Past the worker's
// own deadline plus the grace, a tick takes the hull back.
func TestConstructionDrain_ReapsAHullFromAWorkerThatStoppedWithoutDeregistering(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 40, 2)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newWedgedClaimShipRepo(newTestHauler(t, "HAULER-7", nil))
	defer shipRepo.releaseAll()

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	// Tiny bounds keep the test fast; the ratio is production's — a grace on top of the task deadline.
	handler.taskTimeout = 20 * time.Millisecond
	handler.reapGrace = 30 * time.Millisecond
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	shipRepo.awaitWedge(t)

	// While the registration stands, the hull is correctly undispatchable — that is the mechanism
	// under test, not an accident of discovery.
	if held := dispatchTick(t, handler, cmd); held.NoWorkReason != noWorkNoIdleHauler {
		t.Fatalf("a hull a registered worker holds must not be dispatched, got %q", held.NoWorkReason)
	}

	// Past the wedged worker's deadline AND its grace.
	time.Sleep(300 * time.Millisecond)
	// The reclaimed hull's own haul must outlive the assertions below; its bound is stamped at
	// dispatch, so raising this leaves the wedged worker's expiry where it was.
	handler.taskTimeout = 30 * time.Second

	reaped := dispatchTick(t, handler, cmd)
	if reaped.NoWorkReason != "" {
		t.Fatalf("the wedged worker is long past its deadline and its grace: the drain must take the hull back rather than report %q forever", reaped.NoWorkReason)
	}
	if hull := producer.awaitSupplyStart(t); hull != "HAULER-7" {
		t.Fatalf("expected the reclaimed HAULER-7 back at work, got %s", hull)
	}
}

// The bound must reclaim a DEAD worker's hull without ever touching a live one. A construction haul
// legitimately runs for tens of minutes under a deadline measured in hours, and a drain that reclaims
// a hull mid-haul buys a second load against a bill the first is about to meet. Time passes and ticks
// keep coming; the haul keeps its hull.
func TestConstructionDrain_DoesNotReapAHaulInsideItsDeadline(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 400, 3)
	tasks := readyTasksOnPipeline(t, pipeline, "FAB_MATS", 3)

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: tasks}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.taskTimeout = 2 * time.Second
	handler.reapGrace = 2 * time.Second
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	producer.awaitSupplyStart(t)

	// Real time passes under a haul that is still well inside its deadline, and the ticks keep
	// running: neither the clock nor the tick count may cost it its hull.
	time.Sleep(250 * time.Millisecond)
	for tick := 2; tick <= 4; tick++ {
		if resp := dispatchTick(t, handler, cmd); resp.NoWorkReason != noWorkNoIdleHauler {
			t.Fatalf("tick %d reclaimed a hull from a haul still inside its deadline (it would buy a second load against the same bill), got %q", tick, resp.NoWorkReason)
		}
	}
	if started := producer.startedHulls(); len(started) != 1 {
		t.Fatalf("expected the one live haul untouched, got supplies on %v", started)
	}
}

// A hull whose worker died is still LADEN with what that worker paid for. The load has to reach the
// site: it is already bought, the site's bill has not moved for it, and a hull retired with it aboard
// strands the material and re-buys it later. A later tick unloads it before any buy.
func TestConstructionDrain_DeliversTheCargoOfAWorkerThatDied(t *testing.T) {
	const onHand = 30
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", onHand, 2)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &panickingProducer{panicOnDeliver: true, units: onHand}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "FAB_MATS", onHand))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&sweepingActivator{repo: taskRepo}), &factoryFakeClock{})
	cmd := newDrainCommand()

	// Tick 1: the laden hull's unload panics, killing the worker with the cargo still aboard.
	if _, err := drainSettled(t, handler, context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != 0 {
		t.Fatalf("precondition: the panicked delivery must have recorded nothing, got %d", got)
	}

	// Tick 2: the hull is dispatchable again and unloads what it is carrying.
	if _, err := drainSettled(t, handler, context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != onHand {
		t.Fatalf("expected the dead worker's %d on-hand units delivered by a later tick, got %d", onHand, got)
	}
	if got := producer.deliveredHulls(); len(got) != 2 {
		t.Fatalf("expected exactly one retry of the unload on the same hull, got %v", got)
	}
	if got := producer.sourcedHulls(); len(got) != 0 {
		t.Fatalf("the load was already bought and is aboard: the drain must deliver it, never re-buy it, got sourcing on %v", got)
	}
}

// A reaped worker is not a dead worker — it is a worker the drain STOPPED BELIEVING IN, and it can
// still come back. Its hull now belongs to someone else, so its late cleanup must take nothing: not
// the registration (which would free a hull mid-haul for a second concurrent supply), not the claim
// (which would put a laden hull back in the idle pool), and not a buy against a budget a later tick
// has already re-planned without it.
func TestConstructionDrain_AReapedWorkerTakesNothingFromItsSuccessor(t *testing.T) {
	pipeline := newTwoMaterialPipeline(t, 3, "FAB_MATS", "ADVANCED_CIRCUITRY")
	wedgedLeg := readyConstructionTask(t, pipeline, "FAB_MATS")
	successorLeg := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")
	// A leg that stays READY behind the successor, so the later ticks reach the hull question at all
	// instead of standing down on an empty queue.
	waitingLeg := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := newHeldSupplyProducer()
	defer producer.releaseAll()
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{wedgedLeg}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newWedgedClaimShipRepo(newTestHauler(t, "HAULER-7", nil))
	defer shipRepo.releaseAll()

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.taskTimeout = 20 * time.Millisecond
	handler.reapGrace = 30 * time.Millisecond
	cmd := newDrainCommand()

	dispatchTick(t, handler, cmd)
	shipRepo.awaitWedge(t)
	time.Sleep(300 * time.Millisecond)

	// The ready queue moves on while the worker is wedged — a different material is what is ready now
	// — but the wedged worker still holds its own leg, READY, in hand. That is what makes its buy live
	// rather than blocked by the task's own state, and what the registry has to refuse instead.
	replaceReadyTasks(taskRepo, successorLeg, waitingLeg)
	// The successor's haul must still be running when the reaped worker comes back.
	handler.taskTimeout = 30 * time.Second

	dispatchTick(t, handler, cmd)
	if hull := producer.awaitSupplyStart(t); hull != "HAULER-7" {
		t.Fatalf("expected the reclaimed hull handed to a successor, got %s", hull)
	}
	releasesBefore := shipRepo.releaseCount("HAULER-7")

	// The reaped worker's claim finally returns, and it unwinds while the successor hauls.
	shipRepo.releaseAll()
	shipRepo.awaitUnwedged(t)
	// Ticks the reaped worker's cleanup can race with: a cleanup that took the successor's hull shows
	// up here as a second concurrent supply, which is what this window is long enough to catch.
	for tick := 3; tick <= 6; tick++ {
		if resp := dispatchTick(t, handler, cmd); resp.NoWorkReason != noWorkNoIdleHauler {
			t.Fatalf("tick %d: the reaped worker's cleanup freed a hull its successor is still hauling with, got %q", tick, resp.NoWorkReason)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if started := producer.startedHulls(); len(started) != 1 {
		t.Fatalf("the reaped worker's buy reservation went back to the pool and a later tick has re-planned without it: it must stand down rather than source, got supplies on %v", started)
	}
	if got := shipRepo.releaseCount("HAULER-7"); got != releasesBefore {
		t.Fatalf("the reaped worker released a hull its successor is still hauling with (%d releases, was %d) — a laden hull back in the idle pool", got, releasesBefore)
	}
}

// replaceReadyTasks swaps what the drain will poll next tick, under the repo's own lock — the workers
// a previous tick handed off are still running against it.
func replaceReadyTasks(repo *drainStubTaskRepo, tasks ...*manufacturing.ManufacturingTask) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.tasks = tasks
}
