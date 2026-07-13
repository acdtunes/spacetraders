package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-382j: the thin construction-supply DRAIN. These tests drive drainOnce (the
// per-tick reconcile) directly — the Handle loop just calls it on a timer. The
// drain is tested at its true seam: sourcing + delivery are delegated to a
// ConstructionProducer (the shared ProductionExecutor in production), so the
// tests pin the DRAIN's orchestration — claim an idle hauler under the shared
// "manufacturing" identity, source, deliver, record pipeline progress, complete
// the task, and defer (never fail) an unsourceable material.

const constructionSiteWP = "X1-TEST-GATE"

// fakeConstructionProducer records ProduceGood + DeliverToConstructionSite and
// returns scripted results, so the drain's orchestration is exercised without a
// live executor (nav/market/API). *mfgServices.ProductionExecutor satisfies the
// same ConstructionProducer interface in production.
type fakeConstructionProducer struct {
	acquire      int // QuantityAcquired ProduceGood reports (0 models a dry/no-source market)
	delivered    int // units DeliverToConstructionSite reports the site accepted
	produceGoods []string
	deliverCalls []producerDeliverCall
	produceErr   error
	deliverErr   error
}

type producerDeliverCall struct{ ship, good, site string }

func (p *fakeConstructionProducer) ProduceGood(_ context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	p.produceGoods = append(p.produceGoods, node.Good)
	if p.produceErr != nil {
		return nil, p.produceErr
	}
	return &mfgServices.ProductionResult{QuantityAcquired: p.acquire}, nil
}

func (p *fakeConstructionProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, good, site string, _ shared.PlayerID) (int, error) {
	p.deliverCalls = append(p.deliverCalls, producerDeliverCall{ship: shipSymbol, good: good, site: site})
	if p.deliverErr != nil {
		return 0, p.deliverErr
	}
	return p.delivered, nil
}

// fakeConstructionActivator records that the surviving activator was invoked each
// tick (PENDING->READY promotion). Tests pre-mark tasks READY, so it returns 0.
type fakeConstructionActivator struct{ calls int }

func (a *fakeConstructionActivator) ActivateConstructionTasks(_ context.Context) int {
	a.calls++
	return 0
}

// staticActivator adapts a fixed activator to the per-player factory the handler takes.
func staticActivator(a ConstructionActivator) func(int) ConstructionActivator {
	return func(int) ConstructionActivator { return a }
}

// drainStubTaskRepo serves READY tasks and records status persistence. Embeds the
// interface so any unused method panics, keeping the stub honest.
type drainStubTaskRepo struct {
	manufacturing.TaskRepository
	tasks   []*manufacturing.ManufacturingTask
	updated map[string]manufacturing.TaskStatus
}

func (r *drainStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t.Status() == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *drainStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	if r.updated == nil {
		r.updated = make(map[string]manufacturing.TaskStatus)
	}
	r.updated[task.ID()] = task.Status()
	return nil
}

type drainStubPipelineRepo struct {
	manufacturing.PipelineRepository
	pipelines map[string]*manufacturing.ManufacturingPipeline
	updates   int
}

func (r *drainStubPipelineRepo) FindByID(_ context.Context, id string) (*manufacturing.ManufacturingPipeline, error) {
	return r.pipelines[id], nil
}

func (r *drainStubPipelineRepo) Update(_ context.Context, _ *manufacturing.ManufacturingPipeline) error {
	r.updates++
	return nil
}

// drainFakeShipRepo returns idle haulers to FindIdleLightHaulers, records atomic
// ClaimShip calls at the port boundary, and models per-tick release via
// FindByContainer/Save so a drained hull returns to the idle pool.
type drainFakeShipRepo struct {
	navigation.ShipRepository
	ships       []*navigation.Ship
	claims      []drainClaim
	claimErr    error
	byContainer map[string][]*navigation.Ship
}

type drainClaim struct{ symbol, containerID, operation string }

func newDrainShipRepo(ships ...*navigation.Ship) *drainFakeShipRepo {
	return &drainFakeShipRepo{ships: ships, byContainer: make(map[string][]*navigation.Ship)}
}

func (r *drainFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *drainFakeShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	if r.claimErr != nil {
		return r.claimErr
	}
	r.claims = append(r.claims, drainClaim{symbol: symbol, containerID: containerID, operation: operation})
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			r.byContainer[containerID] = append(r.byContainer[containerID], s)
		}
	}
	return nil
}

func (r *drainFakeShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.byContainer[containerID], nil
}

func (r *drainFakeShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

// newDrainPipeline builds an EXECUTING construction pipeline with a single
// material bill (good x targetQty), so RecordMaterialDelivery moves progress.
func newDrainPipeline(t *testing.T, good string, targetQty int) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 1)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(good, targetQty)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

func readyConstructionTask(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good string) *manufacturing.ManufacturingTask {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, good, "X1-TEST-SRC", "", constructionSiteWP, nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	return task
}

func newDrainCommand() *RunConstructionCoordinatorCommand {
	return &RunConstructionCoordinatorCommand{PlayerID: 1, SystemSymbol: testSystem, ContainerID: "construction-coordinator-1"}
}

// #1 — a READY DELIVER_TO_CONSTRUCTION task from an EXECUTING pipeline is drained:
// an idle hauler is claimed under the shared "manufacturing" identity, the material
// is sourced then delivered to the site, pipeline progress advances past 0%, and the
// task is completed and persisted.
func TestConstructionDrain_SuppliesReadyTask(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	activator := &fakeConstructionActivator{}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(activator), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if activator.calls != 1 {
		t.Fatalf("expected the surviving activator invoked once per tick, got %d", activator.calls)
	}
	if len(shipRepo.claims) != 1 {
		t.Fatalf("expected exactly one hauler claimed, got %d", len(shipRepo.claims))
	}
	claim := shipRepo.claims[0]
	if claim.symbol != "HAULER-7" || claim.operation != operationManufacturing || claim.containerID != "construction-coordinator-1" {
		t.Fatalf("hauler claimed under wrong identity: %+v", claim)
	}
	if len(producer.produceGoods) != 1 || producer.produceGoods[0] != "FAB_MATS" {
		t.Fatalf("expected FAB_MATS sourced via ProduceGood, got %v", producer.produceGoods)
	}
	if len(producer.deliverCalls) != 1 || producer.deliverCalls[0].good != "FAB_MATS" || producer.deliverCalls[0].site != constructionSiteWP {
		t.Fatalf("expected one delivery of FAB_MATS to the site, got %+v", producer.deliverCalls)
	}
	if pipeline.ConstructionProgress() <= 0 {
		t.Fatalf("expected pipeline progress to advance past 0%%, got %.1f%%", pipeline.ConstructionProgress())
	}
	if pipelineRepo.updates == 0 {
		t.Fatal("expected the pipeline's recorded delivery to be persisted")
	}
	if task.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected task COMPLETED, got %s", task.Status())
	}
	if taskRepo.updated[task.ID()] != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected COMPLETED persisted, got %q", taskRepo.updated[task.ID()])
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected TasksDrained=1, got %d", resp.TasksDrained)
	}
}

// #5 — a material with no buy source (ProduceGood acquires 0: dry/unsourceable) is
// PARKED back to a deferred PENDING state (RULINGS #1 never-skip), NOT delivered and
// NOT failed. It plugs straight into the SupplyMonitor re-sourcing path.
func TestConstructionDrain_DeferOnDryMarket(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 100)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := &fakeConstructionProducer{acquire: 0} // dry market: nothing sourced
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 0 {
		t.Fatalf("a dry material must not be delivered, got %d deliveries", len(producer.deliverCalls))
	}
	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected an unsourceable task PARKED to PENDING, got %s", task.Status())
	}
	if !task.IsDeferredConstruction() {
		t.Fatal("expected the parked task to be deferred (source cleared) for SupplyMonitor re-sourcing")
	}
	if pipeline.ConstructionProgress() != 0 {
		t.Fatalf("expected no progress on a deferred material, got %.1f%%", pipeline.ConstructionProgress())
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 on defer, got %d", resp.TasksDrained)
	}
}

// #4 — restart resilience (RULINGS #2): the drain holds no cross-tick task state;
// its worklist is rebuilt from persistence (FindByStatus) every tick. A brand-new
// coordinator instance over the same persisted repos resumes supply of a task that
// was still READY at restart.
func TestConstructionDrain_RestartResilient(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	first := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{first}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	// First coordinator instance drains the first delivery.
	h1 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := h1.drainOnce(context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("first drainOnce: %v", err)
	}
	if first.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("precondition: first task should be COMPLETED, got %s", first.Status())
	}

	// Restart: a follow-on delivery task was persisted READY before the crash.
	second := readyConstructionTask(t, pipeline, "FAB_MATS")
	taskRepo.tasks = append(taskRepo.tasks, second)

	// A brand-new coordinator instance (no carried state) over the SAME repos.
	h2 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp2, err := h2.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("post-restart drainOnce: %v", err)
	}
	if second.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the fresh instance to resume and complete the persisted task, got %s", second.Status())
	}
	if resp2.TasksDrained != 1 {
		t.Fatalf("expected the restarted drain to drain 1 task, got %d", resp2.TasksDrained)
	}
}

// When launched with no operating system (the bootstrap gate launches it that way),
// the drain derives the system from the construction site and restricts hauler
// discovery to it — an out-of-system hauler is NOT claimed for an in-system leg.
func TestConstructionDrain_DerivesSystemFromSiteWhenUnset(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100) // site X1-TEST-GATE (system X1-TEST)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	// An in-system hauler (claimable) and an out-of-system hauler (must be ignored).
	inSystem := newTestHauler(t, "HAULER-7", nil)                // at X1-TEST-FACTORY (X1-TEST)
	outOfSystem := newTestHaulerAt(t, "HAULER-8", "X1-OTHER-Z1") // at X1-OTHER
	shipRepo := newDrainShipRepo(outOfSystem, inSystem)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	// No SystemSymbol: the drain must derive X1-TEST from the site X1-TEST-GATE.
	resp, err := handler.drainOnce(context.Background(), &RunConstructionCoordinatorCommand{PlayerID: 1, ContainerID: "cc-1"})
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if len(shipRepo.claims) != 1 || shipRepo.claims[0].symbol != "HAULER-7" {
		t.Fatalf("expected only the in-system hauler claimed, got %+v", shipRepo.claims)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected the task drained by the in-system hauler, got %d", resp.TasksDrained)
	}
}

// A READY DELIVER_TO_CONSTRUCTION task whose pipeline is NOT yet EXECUTING (still
// PLANNING) must not be worked: the drain only serves EXECUTING pipelines.
func TestConstructionDrain_IgnoresNonExecutingPipelineTask(t *testing.T) {
	planning := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 1) // stays PLANNING
	task := readyConstructionTask(t, planning, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{planning.ID(): planning}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(shipRepo.claims) != 0 {
		t.Fatalf("a PLANNING pipeline's task must not claim a hauler, got %d claims", len(shipRepo.claims))
	}
	if len(producer.produceGoods) != 0 {
		t.Fatalf("a PLANNING pipeline's task must not be sourced, got %v", producer.produceGoods)
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 for a PLANNING pipeline, got %d", resp.TasksDrained)
	}
}

// A HARD sourcing error (distinct from a dry market, which defers) FAILS the task — it must
// NOT be silently deferred. This pins the fail-vs-defer distinction (RULINGS #1): a dry market
// is a transient supply gap (park PENDING), a produce error is a genuine failure (FAILED).
func TestConstructionDrain_FailsTaskOnSourcingError(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{produceErr: errors.New("market unreachable")}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("a task-level failure must not fail the whole tick, got %v", err)
	}
	if len(producer.deliverCalls) != 0 {
		t.Fatalf("delivery must not be attempted after a sourcing error, got %d", len(producer.deliverCalls))
	}
	if task.Status() != manufacturing.TaskStatusFailed {
		t.Fatalf("a hard sourcing error must FAIL the task (not defer), got %s", task.Status())
	}
	if taskRepo.updated[task.ID()] != manufacturing.TaskStatusFailed {
		t.Fatalf("expected FAILED persisted, got %q", taskRepo.updated[task.ID()])
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 on a failed task, got %d", resp.TasksDrained)
	}
}

// A hard delivery error (after a successful source) FAILS the task, after the delivery leg was
// attempted.
func TestConstructionDrain_FailsTaskOnDeliveryError(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, deliverErr: errors.New("supply API rejected")}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	_, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("a task-level failure must not fail the whole tick, got %v", err)
	}
	if len(producer.deliverCalls) != 1 {
		t.Fatalf("expected the delivery leg attempted once, got %d", len(producer.deliverCalls))
	}
	if task.Status() != manufacturing.TaskStatusFailed {
		t.Fatalf("a hard delivery error must FAIL the task, got %s", task.Status())
	}
	if pipeline.ConstructionProgress() != 0 {
		t.Fatalf("a failed delivery must record no progress, got %.1f%%", pipeline.ConstructionProgress())
	}
}

// An atomic claim rejected at the DB (a hull grabbed by another coordinator, or fleet-dedicated
// elsewhere — RULINGS #7) is skipped gracefully: the loop continues and no task is worked, but
// the drain does not abort the tick.
func TestConstructionDrain_SkipsRejectedClaim(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))
	shipRepo.claimErr = errors.New("ship dedicated to another fleet")

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("a rejected claim must not fail the tick, got %v", err)
	}
	if len(producer.produceGoods) != 0 {
		t.Fatalf("a rejected claim must not source anything, got %v", producer.produceGoods)
	}
	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("a task whose hauler claim was rejected must stay READY for retry, got %s", task.Status())
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 when the claim was rejected, got %d", resp.TasksDrained)
	}
}

// A tick with no ready construction tasks reports the no-work reason (so a parked drain proves
// it is alive and why), claiming nothing.
func TestConstructionDrain_NoReadyTasks_ReportsNoWork(t *testing.T) {
	taskRepo := &drainStubTaskRepo{}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if resp.NoWorkReason != noWorkNoReadyConstruction {
		t.Fatalf("expected no-ready-task reason %q, got %q", noWorkNoReadyConstruction, resp.NoWorkReason)
	}
	if len(shipRepo.claims) != 0 {
		t.Fatalf("no task means no claim, got %d", len(shipRepo.claims))
	}
}

// A ready task but no idle hauler reports the no-hauler reason.
func TestConstructionDrain_NoIdleHauler_ReportsNoWork(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo() // no haulers

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := handler.drainOnce(context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if resp.NoWorkReason != noWorkNoIdleHauler {
		t.Fatalf("expected no-idle-hauler reason %q, got %q", noWorkNoIdleHauler, resp.NoWorkReason)
	}
}

// sp-382j: the daemon now boot-launches this drain as a STANDING coordinator (MaxIterations=-1,
// mirroring GoodsFactoryCoordinator/StartGoodsFactory) so it runs continuously with no
// bootstrapper required — idling when there is no pipeline, ready to supply the moment one
// appears. Handle (the standing-loop entrypoint the daemon actually launches) must therefore idle
// CLEANLY on an empty queue: report the no-ready-task reason every tick, claim no hauler,
// source/deliver nothing, and stop ONLY on context cancellation (never error out or hang) — the
// exact steady state a boot-standing launch sits in whenever there is no gate pipeline. Unlike the
// drainOnce-level tests above, this drives Handle itself: previously nothing exercised the actual
// standing-loop entrypoint in isolation.
func TestConstructionDrain_IdlesCleanlyWhenNoPipeline(t *testing.T) {
	taskRepo := &drainStubTaskRepo{}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))
	activator := &fakeConstructionActivator{}
	producer := &fakeConstructionProducer{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(activator), &factoryFakeClock{})

	// Handle's per-tick wait uses real wall-clock time (time.After), not the injected Clock, so a
	// short real deadline stands in for the container's shutdown cancellation — a boot-standing
	// launch's MaxIterations=-1 means only ctx cancellation ever stops the loop. The tick interval
	// defaults to 30s (TickSeconds unset), so the deadline fires well before the second tick's
	// wait would even complete, keeping this test fast and exercising exactly one idle tick.
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	cmd := &RunConstructionCoordinatorCommand{PlayerID: 1, SystemSymbol: testSystem, ContainerID: "construction-coordinator-1", MaxIterations: -1}
	last, err := handler.Handle(ctx, cmd)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a standing drain to stop only via context cancellation, got %v", err)
	}

	resp, ok := last.(*RunConstructionCoordinatorResponse)
	if !ok {
		t.Fatalf("expected *RunConstructionCoordinatorResponse, got %T", last)
	}
	if resp.NoWorkReason != noWorkNoReadyConstruction {
		t.Fatalf("expected the idle tick to report %q, got %q", noWorkNoReadyConstruction, resp.NoWorkReason)
	}
	if len(shipRepo.claims) != 0 {
		t.Fatalf("an empty queue must claim no hauler, got %d claims", len(shipRepo.claims))
	}
	if len(producer.produceGoods) != 0 || len(producer.deliverCalls) != 0 {
		t.Fatalf("an empty queue must source/deliver nothing, got produce=%v deliver=%v", producer.produceGoods, producer.deliverCalls)
	}
	if activator.calls == 0 {
		t.Fatal("expected the surviving activator invoked at least once by the idle loop")
	}
}
