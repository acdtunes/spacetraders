package commands

import (
	"context"
	"errors"
	"reflect"
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

// The thin construction-supply DRAIN. These tests drive drainOnce (the
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
	// mu guards the recorders: the drain dispatches concurrent supply workers through ONE producer,
	// so unguarded appends both race and silently drop calls.
	mu            sync.Mutex
	acquire       int // QuantityAcquired ProduceGood reports (0 models a dry/no-source market)
	delivered     int // units DeliverToConstructionSite reports the site accepted (default per call)
	// zeroReason scripts ProduceGood's ZeroReason (sp-0u1yd): ZeroReasonCapitalDeclined models a
	// working-capital reserve decline (source was fine, treasury wasn't), as opposed to the default
	// ZeroReasonUnspecified a dry/no-source market reports. Meaningless unless acquire == 0.
	zeroReason mfgServices.AcquisitionZeroReason
	produceGoods  []string
	produceNodes  []*goods.SupplyChainNode // full node captured per call (acquisition method + children)
	produceCtxCon []bool                   // whether the ProduceGood ctx was marked construction-supply
	deliverCalls  []producerDeliverCall
	produceErr    error
	deliverErr    error
	// Capture the hull-fill target the drain stamped on ctx at ProduceGood, so a test can
	// assert the drain fills toward the outstanding bill rather than one trade-volume tranche.
	observedBill   int
	observedFillOK bool
	// callSeq is the ORDERED trace of "produce"/"deliver" across the run, so a test can
	// prove the deliver-on-hand leg runs BEFORE the source-buy attempt. deliveredSeq scripts the
	// units each successive DeliverToConstructionSite reports (on-hand load, then sourced load);
	// it falls back to `delivered` once exhausted, so existing single-delivery tests are unchanged.
	callSeq      []string
	deliveredSeq []int
}

type producerDeliverCall struct{ ship, good, site string }

func (p *fakeConstructionProducer) ProduceGood(ctx context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callSeq = append(p.callSeq, "produce")
	p.produceGoods = append(p.produceGoods, node.Good)
	if bill, _, ok := mfgServices.HullFillTargetFromContext(ctx); ok {
		p.observedBill = bill
		p.observedFillOK = true
	}
	p.produceNodes = append(p.produceNodes, node)
	p.produceCtxCon = append(p.produceCtxCon, shared.ConstructionSupplyFromContext(ctx))
	if p.produceErr != nil {
		return nil, p.produceErr
	}
	return &mfgServices.ProductionResult{QuantityAcquired: p.acquire, ZeroReason: p.zeroReason}, nil
}

func (p *fakeConstructionProducer) DeliverToConstructionSite(_ context.Context, shipSymbol, good, site string, _ shared.PlayerID) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callSeq = append(p.callSeq, "deliver")
	p.deliverCalls = append(p.deliverCalls, producerDeliverCall{ship: shipSymbol, good: good, site: site})
	if p.deliverErr != nil {
		return 0, p.deliverErr
	}
	if len(p.deliveredSeq) > 0 {
		n := p.deliveredSeq[0]
		p.deliveredSeq = p.deliveredSeq[1:]
		return n, nil
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
// interface so any unused method panics, keeping the stub honest. The mutex makes it
// safe for the concurrent drain workers that call Update in parallel.
type drainStubTaskRepo struct {
	manufacturing.TaskRepository
	mu        sync.Mutex
	tasks     []*manufacturing.ManufacturingTask
	updated   map[string]manufacturing.TaskStatus
	snapshots map[string]persistedTask
	created   []*manufacturing.ManufacturingTask
}

// persistedTask is what a task looked like when it was persisted, captured under r.mu. An abandoned
// tick returns while its interrupted worker is still unwinding, so a test asserting on that task
// must read it through this synchronised seam — touching the task object the worker still owns
// would race.
type persistedTask struct {
	status     manufacturing.TaskStatus
	retryCount int
	deferred   bool
	// actualQuantity is the units the completion recorded as MOVED. sp-1f0ex: this was never
	// written by anything, so it read 0 on every row in production and the stall watchdog could
	// never observe a delivery.
	actualQuantity int
}

// statusOf is the terminal status the drain persisted for task id, "" when it persisted none —
// which is exactly what a leg that skipped the completion machinery leaves behind.
//
// It reads the EXISTING updated recorder rather than adding a parallel one. Note that recorder is
// a MAP keyed by task ID, so there is no "last persisted task" to ask for: a caller must name the
// task it staged.
// FindByPipelineAndStatus serves the sp-63r4f progress watchdog on every drain tick. The embedded
// interface panics rather than returning nothing, and a nil-panic in a fixture is indistinguishable
// from a real defect in the code under test.
func (r *drainStubTaskRepo) FindByPipelineAndStatus(_ context.Context, pipelineID string, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t != nil && t.PipelineID() == pipelineID && t.Status() == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *drainStubTaskRepo) statusOf(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.updated[id])
}

// snapshot returns how the task looked at its last persist, under lock.
func (r *drainStubTaskRepo) snapshot(id string) (persistedTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	persisted, ok := r.snapshots[id]
	return persisted, ok
}

func (r *drainStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t.Status() == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *drainStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updated == nil {
		r.updated = make(map[string]manufacturing.TaskStatus)
	}
	if r.snapshots == nil {
		r.snapshots = make(map[string]persistedTask)
	}
	r.updated[task.ID()] = task.Status()
	r.snapshots[task.ID()] = persistedTask{
		status:         task.Status(),
		retryCount:     task.RetryCount(),
		deferred:       task.IsDeferredConstruction(),
		actualQuantity: task.ActualQuantity(),
	}
	return nil
}

// Create persists an enqueued replenishment task (PHASE-5 refill): it is recorded and appended
// to the ready worklist so a subsequent tick's FindByStatus picks it up, exactly as the real
// repo + drain loop behave. Locks r.mu (like Update/FindByStatus) so concurrent drain workers
// each enqueuing a replenishment task append safely — the real GORM repo serializes
// concurrent inserts at the DB, so this models that atomicity.
func (r *drainStubTaskRepo) Create(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, task)
	r.tasks = append(r.tasks, task)
	return nil
}

// drainStubPipelineRepo caches pipelines by ID. The mutex guards the shared map and the
// updates counter against the concurrent drain workers; the pipeline object's own
// read-modify-write (RecordMaterialDelivery) is serialized handler-side so no delivery is lost.
type drainStubPipelineRepo struct {
	manufacturing.PipelineRepository
	mu        sync.Mutex
	pipelines map[string]*manufacturing.ManufacturingPipeline
	updates   int
}

// FindByStatus serves the sp-63r4f progress watchdog, which runs on every drain tick. The embedded
// interface would panic here rather than return nothing, and a nil-panic in a fixture is
// indistinguishable from a real defect in the code under test.
func (r *drainStubPipelineRepo) FindByStatus(_ context.Context, _ int, statuses []manufacturing.PipelineStatus) ([]*manufacturing.ManufacturingPipeline, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wanted := make(map[manufacturing.PipelineStatus]bool, len(statuses))
	for _, s := range statuses {
		wanted[s] = true
	}
	var out []*manufacturing.ManufacturingPipeline
	for _, p := range r.pipelines {
		if p != nil && wanted[p.Status()] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *drainStubPipelineRepo) FindByID(_ context.Context, id string) (*manufacturing.ManufacturingPipeline, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pipelines[id], nil
}

func (r *drainStubPipelineRepo) Update(_ context.Context, _ *manufacturing.ManufacturingPipeline) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates++
	return nil
}

// drainFakeShipRepo returns idle haulers to FindIdleLightHaulers, records atomic
// ClaimShip calls at the port boundary, and models per-tick release via
// FindByContainer/Save so a drained hull returns to the idle pool.
type drainFakeShipRepo struct {
	navigation.ShipRepository
	mu          sync.Mutex
	ships       []*navigation.Ship
	claims      []drainClaim
	claimErr    error
	byContainer map[string][]*navigation.Ship
	resyncs     []string // sp-6zkg: hull symbols the drain forced a server resync on (SyncShipFromAPI)
	assigned    []fleetAssignment
	assignErr   error
}

type drainClaim struct{ symbol, containerID, operation string }

// fleetAssignment is one AssignFleet write — the SINGLE write path for dedicated_fleet
// (RULINGS #3). Recording it at the port boundary is how a test proves the reallocator writes
// through it and not through a general ship save, which preserveDedicatedFleetTag would revert.
type fleetAssignment struct{ ship, fleet string }

// AssignFleet records the re-tag AND applies it to the stored hull. The struct embeds the
// interface, so without this stub any reallocation would PANIC rather than be observed.
//
// Applying it is not decoration. The real repository writes dedicated_fleet and invalidates the
// ship-list cache, so a hull re-tagged early in a tick is read back with its NEW tag by that same
// tick's discovery — which is the whole reason the reallocation runs before selectHaulers. A fake
// that only recorded would leave the stored hull on its old tag, and then claimIdentityFor would
// mint the OLD identity while the DB row carried the new one: production rejects that claim
// (ClaimShip authorizes only when tag == operation) and the hull silently never works, but the
// fake would sail through it green.
func (r *drainFakeShipRepo) AssignFleet(_ context.Context, shipSymbol, fleet string, _ shared.PlayerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.assignErr != nil {
		return r.assignErr
	}
	r.assigned = append(r.assigned, fleetAssignment{ship: shipSymbol, fleet: fleet})
	for _, s := range r.ships {
		if s.ShipSymbol() == shipSymbol {
			s.SetDedicatedFleet(fleet)
			break
		}
	}
	return nil
}

func newDrainShipRepo(ships ...*navigation.Ship) *drainFakeShipRepo {
	return &drainFakeShipRepo{ships: ships, byContainer: make(map[string][]*navigation.Ship)}
}

func (r *drainFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ships, nil
}

// ClaimShip records the atomic claim. The mutex models the DB's atomicity so the concurrent
// drain workers each register a distinct claim without a data race (RULINGS #7).
func (r *drainFakeShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimErr != nil {
		return r.claimErr
	}
	// Mirror the real repository's atomic no-poach dedication guard (ship_repository.go
	// ClaimShip lines ~1225, RULINGS #7): a hull dedicated to a fleet OTHER than the claiming
	// operation is rejected. This makes the fake a faithful seam so a drain that ever tried to
	// poach a foreign-pinned hull is caught here, not silently recorded as a claim.
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			if f := s.DedicatedFleet(); f != "" && f != operation {
				return errors.New("ship dedicated to another fleet")
			}
			break
		}
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
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byContainer[containerID], nil
}

// claimCount returns the number of atomic claims recorded, under lock (for post-drain asserts).
func (r *drainFakeShipRepo) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.claims)
}

func (r *drainFakeShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

// SaveWithRetry models the real repo's CAS re-apply path that
// releaseClaims now routes through: find the hull fresh by symbol and apply the
// mutation in place. The stored *Ship is a shared pointer, so the re-applied
// change (e.g. a ForceRelease) is reflected for the next find — mirroring the
// no-op Save above, which likewise relied on the caller's in-place mutation. A
// symbol the fake doesn't hold reports changed=false (no mutate, no nil deref).
func (r *drainFakeShipRepo) SaveWithRetry(_ context.Context, symbol string, _ shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			changed, err := mutate(s)
			return s, changed, err
		}
	}
	return nil, false, nil
}

// SyncShipFromAPI records that the drain forced a server-truth resync of a hull (sp-6zkg): the
// recovery a phantom-cargo 4219 triggers so the desynced cache is reconciled before the task is
// deferred, instead of re-routing the (really empty) hull to re-deliver forever.
func (r *drainFakeShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resyncs = append(r.resyncs, symbol)
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			return s, nil
		}
	}
	return nil, nil
}

// resyncCount returns how many hull resyncs were recorded, under lock (for post-drain asserts).
func (r *drainFakeShipRepo) resyncCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.resyncs)
}

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

// readyFactoryFeedableConstructionTask is readyConstructionTask's sibling for a task a FACTORY-role
// hull genuinely has feeding work for: UNRESOLVED (neither sourceMarket nor factorySymbol set), the
// shape planMaterial/resourceDeferredConstructionTask (construction_pipeline_planner.go,
// task_activator_construction.go) leave a task in immediately before resolving it — and both of
// those set AT MOST ONE of sourceMarket/factorySymbol, never both. readyConstructionTask's own
// "X1-TEST-SRC" default is correct for its many OTHER callers (delivery-role legs and
// role-agnostic dispatch-mechanics tests, where a resolved buy source is exactly what the fixture
// wants) and must not change here.
//
// Load-bearing after sp-8epum: planDispatchLots declines to pair a gate.RoleFactory hull with a
// task whose SourceMarket() is already set, because production never hands a factory hull one. A
// factory-role end-to-end test staging a task carrying "X1-TEST-SRC" would be asserting against a
// pairing production never makes; this sibling keeps those tests representative.
func readyFactoryFeedableConstructionTask(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good string) *manufacturing.ManufacturingTask {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, good, "", "", constructionSiteWP, nil)
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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

// sp-2me2 — before sourcing, the drain stamps the material's OUTSTANDING bill (target minus
// delivered) as the hull-fill target on ctx, so the shared executor tops the hauler up toward
// capacity (bounded by the bill) instead of carrying one ~trade-volume tranche (~1/4 hull, 4x the
// round-trips). FAB_MATS here has a 100-unit target with 30 already delivered, so the drain must
// stamp a 70-unit fill target.
func TestConstructionDrain_StampsRemainingBillAsHullFillTarget(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	if err := pipeline.RecordMaterialDelivery("FAB_MATS", 30); err != nil {
		t.Fatalf("RecordMaterialDelivery: %v", err)
	}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if !producer.observedFillOK {
		t.Fatal("expected the drain to stamp a hull-fill target on ctx before sourcing the material")
	}
	if producer.observedBill != 70 {
		t.Fatalf("expected the outstanding bill (100 target - 30 delivered = 70) as the fill target, got %d", producer.observedBill)
	}
}

// sp-qmp8 (regression restore) — a FABRICATE-planned material (the DELIVER_TO_CONSTRUCTION task
// carries a factory) is sourced by PRODUCTION, not a market buy of the final good: the drain
// drives ProduceGood with an AcquisitionFabricate node whose children are the good's immediate
// inputs (each a market BUY), so the shared engine buys inputs → feeds the factory → harvests the
// output into the hauler, which is then delivered to the site. It also marks the run as
// construction supply so the engine's resale-margin guards are scoped out.
func TestConstructionDrain_FabricatePlannedMaterial_ProducesViaFabricateNode(t *testing.T) {
	const factoryWP = "X1-TEST-FACTORY"
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	// FactorySymbol set, no source market: the planner chose fabrication.
	task := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, "FAB_MATS", "", factoryWP, constructionSiteWP, nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.produceNodes) != 1 {
		t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.produceNodes))
	}
	node := producer.produceNodes[0]
	if node.Good != "FAB_MATS" || node.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("expected a FABRICATE node for FAB_MATS (NOT a market buy of the final good), got good=%s method=%s", node.Good, node.AcquisitionMethod)
	}
	// Children are the immediate inputs, each a market BUY — the engine sources these itself.
	gotInputs := map[string]goods.AcquisitionMethod{}
	for _, c := range node.Children {
		gotInputs[c.Good] = c.AcquisitionMethod
	}
	for _, want := range []string{"IRON", "QUARTZ_SAND"} {
		if gotInputs[want] != goods.AcquisitionBuy {
			t.Fatalf("expected input %s to be a BUY child of the fabricate node, got children %+v", want, node.Children)
		}
	}
	if !producer.produceCtxCon[0] {
		t.Fatal("expected the ProduceGood run marked as construction supply (resale-margin guards scoped out)")
	}
	if len(producer.deliverCalls) != 1 || producer.deliverCalls[0].good != "FAB_MATS" || producer.deliverCalls[0].site != constructionSiteWP {
		t.Fatalf("expected the harvested FAB_MATS delivered to the site, got %+v", producer.deliverCalls)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected TasksDrained=1, got %d", resp.TasksDrained)
	}
}

// sp-qmp8 (non-regression) — a BUYABLE material (the planner resolved a source market, no
// factory) still uses a direct AcquisitionBuy of the final good: the fabricate path must not
// change how buyable materials are sourced.
func TestConstructionDrain_BuyableMaterial_UsesBuyNode(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS") // source market set, factory ""

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.produceNodes) != 1 {
		t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.produceNodes))
	}
	node := producer.produceNodes[0]
	if node.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("a buyable material must use AcquisitionBuy, got %s", node.AcquisitionMethod)
	}
	if len(node.Children) != 0 {
		t.Fatalf("a direct buy node must have no fabrication children, got %+v", node.Children)
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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

// sp-0u1yd — a material whose SOURCE IS FINE but whose buy the working-capital reserve declined
// (ProduceGood acquires 0 with ZeroReasonCapitalDeclined) must NOT get the dry-market treatment
// TestConstructionDrain_DeferOnDryMarket pins above. It still parks PENDING this tick — the drain
// truly delivered nothing — but its resolved source survives, so it reads NOT deferred and the next
// activation pass (ActivateConstructionTasks's own short-circuit on !IsDeferredConstruction, pinned
// independently by TestActivateConstructionTasks_RetriesFailedDeliveries in the services package)
// republishes it straight to READY on the SAME source, without paying for a re-resolution that
// cannot fix a capital shortfall — and the log line names the real cause instead of "unsourceable".
func TestConstructionDrain_CapitalBlockedKeepsSourceAndSkipsResourcing(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 100)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")
	wantSource := task.SourceMarket() // "X1-TEST-SRC" (the readyConstructionTask fixture) — must survive

	producer := &fakeConstructionProducer{acquire: 0, zeroReason: mfgServices.ZeroReasonCapitalDeclined}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 0 {
		t.Fatalf("a capital-blocked material must not be delivered, got %d deliveries", len(producer.deliverCalls))
	}
	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected a capital-blocked task PARKED to PENDING (same recoverable park as a dry market), got %s", task.Status())
	}
	if task.IsDeferredConstruction() {
		t.Fatal("a capital-blocked park must NOT clear the resolved source — the source was fine, only the treasury wasn't; clearing it forces a re-resolution that cannot fix a capital shortfall")
	}
	if task.SourceMarket() != wantSource {
		t.Fatalf("expected the resolved source market preserved as %q, got %q", wantSource, task.SourceMarket())
	}
	if snap, ok := taskRepo.snapshot(task.ID()); !ok || snap.deferred {
		t.Fatalf("expected the persisted snapshot to read NOT deferred, got %+v (ok=%v)", snap, ok)
	}
	if pipeline.ConstructionProgress() != 0 {
		t.Fatalf("expected no progress on a capital-blocked material, got %.1f%%", pipeline.ConstructionProgress())
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 on a capital-blocked park, got %d", resp.TasksDrained)
	}

	joined := logger.joined()
	if strings.Contains(joined, "unsourceable") {
		t.Fatalf("a capital-blocked park must NOT be logged as unsourceable (misdiagnoses a treasury problem as a supply one), got: %s", joined)
	}
	if !strings.Contains(joined, "working-capital") {
		t.Fatalf("expected a log line naming the working-capital cause, got: %s", joined)
	}
}

// #4 — restart resilience (RULINGS #2): the drain holds no cross-tick task state;
// its worklist is rebuilt from persistence (FindByStatus) every tick. The PHASE-5 refill
// task is itself persisted (Create), so a brand-new coordinator instance over the same
// repos resumes the follow-on delivery — the refill survives a restart with no new
// cross-restart state (the pipeline's persisted bill drives it).
func TestConstructionDrain_RestartResilient(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	first := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{first}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	// First coordinator instance drains the first delivery (40/100). The bill is not met, so
	// it autonomously enqueues + persists the follow-on delivery task before the "crash".
	h1 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, h1, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("first drainOnce: %v", err)
	}
	if first.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("precondition: first task should be COMPLETED, got %s", first.Status())
	}
	if len(taskRepo.created) != 1 {
		t.Fatalf("precondition: expected one persisted follow-on task before restart, got %d", len(taskRepo.created))
	}
	followOn := taskRepo.created[0]

	// A brand-new coordinator instance (no carried state) over the SAME repos rebuilds its
	// worklist from persistence and resumes the follow-on task that was persisted READY.
	h2 := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp2, err := drainSettled(t, h2, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("post-restart drainOnce: %v", err)
	}
	if followOn.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the fresh instance to resume and complete the persisted follow-on task, got %s", followOn.Status())
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
	resp, err := drainSettled(t, handler, context.Background(), &RunConstructionCoordinatorCommand{PlayerID: 1, ContainerID: "cc-1"})
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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
	_, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
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
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if resp.NoWorkReason != noWorkNoIdleHauler {
		t.Fatalf("expected no-idle-hauler reason %q, got %q", noWorkNoIdleHauler, resp.NoWorkReason)
	}
}

// The daemon now boot-launches this drain as a STANDING coordinator (MaxIterations=-1,
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

// sp-utjr (PHASE-5 refill restore; regression from sp-jav2 ef2281b8): after a delivery that does
// NOT yet meet the material's bill, the drain autonomously enqueues the NEXT single-load
// DELIVER_TO_CONSTRUCTION task for that material — left READY for the next tick — so the gate keeps
// filling without a manual re-plan. This is the fix for the "one load per material then STALL"
// regression (the planner stages exactly one task/material; without this the pipeline sat EXECUTING
// below 100% with no actionable work). The follow-on reuses the completed task's resolved delivery
// spec via the same domain factory the planner uses, so the two paths cannot drift.
func TestConstructionDrain_EnqueuesReplenishmentWhenBillRemains(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100) // full bill: 100
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40} // one load = 40 < 100
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(taskRepo.created) != 1 {
		t.Fatalf("expected exactly one replenishment task enqueued while the bill remains, got %d", len(taskRepo.created))
	}
	next := taskRepo.created[0]
	if next.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Fatalf("expected a DELIVER_TO_CONSTRUCTION replenishment task, got %s", next.TaskType())
	}
	if next.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected the replenishment task READY for the next tick, got %s", next.Status())
	}
	if next.Good() != "FAB_MATS" || next.ConstructionSite() != constructionSiteWP || next.PipelineID() != pipeline.ID() {
		t.Fatalf("replenishment task did not reuse the delivery spec: good=%s site=%s pipeline=%s", next.Good(), next.ConstructionSite(), next.PipelineID())
	}
	if next.SourceMarket() != task.SourceMarket() {
		t.Fatalf("expected the replenishment task to reuse the source market %q, got %q", task.SourceMarket(), next.SourceMarket())
	}
}

// When a delivery MEETS the material's full bill (remaining == 0) the drain enqueues NO
// further task — the material is complete and the refill chain settles cleanly. Without this
// stop condition a met gate would spin forever.
func TestConstructionDrain_NoReplenishmentWhenBillMet(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 40) // full bill: 40
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40} // one load = 40 == bill
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(taskRepo.created) != 0 {
		t.Fatalf("a met material bill must enqueue no replenishment task, got %d", len(taskRepo.created))
	}
	if !pipeline.GetMaterial("FAB_MATS").IsComplete() {
		t.Fatal("precondition: the material bill should read complete after the full delivery")
	}
}

// The refill chain self-re-stages one cargo-load at a time across successive ticks until
// the material's FULL bill is met, then stops — the end-to-end continuous-fill behaviour that was
// lost. Bill 100, one load = 40: ticks deliver 40/80/120 (meeting the bill on the 3rd) then no
// ready work remains, so exactly two refill tasks are enqueued (after loads 1 and 2, none after
// the bill-meeting load 3). Each tick rebuilds its worklist from persistence, like the standing loop.
func TestConstructionDrain_DrivesFullBillAcrossDeliveries(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	deliveries := 0
	for tick := 0; tick < 10; tick++ { // bounded guard: the chain must settle well before this
		resp, err := drainSettled(t, handler, context.Background(), cmd)
		if err != nil {
			t.Fatalf("tick %d drainOnce: %v", tick, err)
		}
		if resp.TasksDrained == 0 {
			break // chain settled: no ready work remains
		}
		deliveries += resp.TasksDrained
	}

	if deliveries != 3 {
		t.Fatalf("expected the bill of 100 met in 3 loads of 40 (40/80/120), got %d deliveries", deliveries)
	}
	if !pipeline.GetMaterial("FAB_MATS").IsComplete() {
		t.Fatalf("expected the material bill fully met after the refill chain, progress %.1f%%", pipeline.ConstructionProgress())
	}
	if len(taskRepo.created) != 2 {
		t.Fatalf("expected exactly 2 refill tasks (after loads 1 and 2, none after the bill-meeting load 3), got %d", len(taskRepo.created))
	}
}

// ladenHauler builds an idle in-system hauler already CARRYING `units` of `good`, so a
// test can model a hull released mid-delivery that must unload its on-hand cargo. Capacity is the
// newTestHauler default (40), so `units` must be <= 40.
func ladenHauler(t *testing.T, symbol, good string, units int) *navigation.Ship {
	t.Helper()
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	return newTestHauler(t, symbol, []*shared.CargoItem{item})
}

// sp-9ptm (REPRO — the incident): a claimed hull that ALREADY HOLDS the construction material must
// UNLOAD it to the site even when the source is unbuyable and the fail-closed buy gate (sp-a5j7)
// parks. Unguarded, the drain evaluated ProduceGood FIRST and, on a dry source, parked the task
// WITHOUT ever delivering — stranding the on-hand cargo (TORWIND-8/-F sat idle+laden at F48 while
// the gate stalled at 160/1600). Now the on-hand units are delivered UNCONDITIONALLY (before any
// buy), pipeline progress is recorded, and the task advances instead of stranding.
func TestConstructionDrain_DeliversOnHandCargoWhenSourceParks(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100) // bill 100; hull carries 30, source is dry
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 0, delivered: 30} // acquire 0 == the sp-a5j7 park
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "FAB_MATS", 30))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 1 || producer.deliverCalls[0].good != "FAB_MATS" || producer.deliverCalls[0].site != constructionSiteWP {
		t.Fatalf("expected the 30 on-hand FAB_MATS delivered to the site despite the dry source, got %+v", producer.deliverCalls)
	}
	if len(producer.callSeq) == 0 || producer.callSeq[0] != "deliver" {
		t.Fatalf("expected the on-hand DELIVERY to run BEFORE any source-buy attempt, call order was %v", producer.callSeq)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != 30 {
		t.Fatalf("expected the 30 on-hand units recorded on the pipeline, got %d", got)
	}
	if pipelineRepo.updates == 0 {
		t.Fatal("expected the on-hand delivery persisted to the pipeline")
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected the laden hull's delivery to count as drained (not stranded), got %d", resp.TasksDrained)
	}
	if task.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the task to ADVANCE (completed; remainder re-staged), got %s", task.Status())
	}
}

// sp-9ptm (partial): a hull holding SOME of the material while the site still needs more delivers its
// on-hand load FIRST, then sources + delivers the remainder in the SAME pass — the buy path is still
// exercised (fail-closed, unchanged) for what the hull could not carry, and the hull-fill target for
// the sourced leg is the bill remaining AFTER the on-hand delivery.
func TestConstructionDrain_DeliversOnHandFirstThenSourcesRemainder(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100) // bill 100
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	// On-hand 30 delivered first; then a sourced 40-unit load delivered → 70 recorded, 30 still open.
	producer := &fakeConstructionProducer{acquire: 40, deliveredSeq: []int{30, 40}}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "FAB_MATS", 30))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if want := []string{"deliver", "produce", "deliver"}; !reflect.DeepEqual(producer.callSeq, want) {
		t.Fatalf("expected on-hand delivery FIRST, then source, then remainder delivery %v, got %v", want, producer.callSeq)
	}
	if len(producer.produceGoods) != 1 || producer.produceGoods[0] != "FAB_MATS" {
		t.Fatalf("expected the remainder sourced once via ProduceGood, got %v", producer.produceGoods)
	}
	if !producer.observedFillOK || producer.observedBill != 70 {
		t.Fatalf("expected the sourced leg's hull-fill target to be the POST-on-hand remainder (100-30=70), got %d (set=%v)", producer.observedBill, producer.observedFillOK)
	}
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != 70 {
		t.Fatalf("expected 30 on-hand + 40 sourced = 70 recorded, got %d", got)
	}
	if resp.TasksDrained != 1 {
		t.Fatalf("expected the task drained, got %d", resp.TasksDrained)
	}
	if task.Status() != manufacturing.TaskStatusCompleted {
		t.Fatalf("expected the task COMPLETED after on-hand + sourced delivery, got %s", task.Status())
	}
}

// sp-9ptm (regression — the fail-closed buy guard is intact): the deliver-on-hand bypass is keyed to
// the TASK's material. A hull laden only with an UNRELATED good (nothing of the needed material
// aboard) still parks on a dry source exactly as before (sp-a5j7 unchanged) — the bypass never
// fabricates a bogus delivery, and there is nothing to strand. (The literally-empty hull is covered
// by TestConstructionDrain_DeferOnDryMarket.)
func TestConstructionDrain_UnrelatedCargoDryMarket_StillDefers(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 0} // dry source (the sp-a5j7 park)
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "IRON", 20)) // holds IRON, task needs FAB_MATS

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 0 {
		t.Fatalf("a hull holding none of the needed material must not deliver, got %+v", producer.deliverCalls)
	}
	if task.Status() != manufacturing.TaskStatusPending || !task.IsDeferredConstruction() {
		t.Fatalf("expected the task PARKED (deferred) on the dry source, got %s (deferred=%v)", task.Status(), task.IsDeferredConstruction())
	}
	if pipeline.ConstructionProgress() != 0 {
		t.Fatalf("expected no progress, got %.1f%%", pipeline.ConstructionProgress())
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected TasksDrained=0 (nothing delivered, remainder parked), got %d", resp.TasksDrained)
	}
}

// sp-9ptm (regression — no over-delivery): a hull laden with a material whose site bill is ALREADY
// MET must not dump its cargo. The deliver-on-hand bypass is gated on the site still NEEDING the
// good, so a met bill skips it — the drain must not attempt a surplus supply the site would reject.
func TestConstructionDrain_BillMet_DoesNotOverDeliverOnHand(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 40)
	if err := pipeline.RecordMaterialDelivery("FAB_MATS", 40); err != nil { // bill fully met
		t.Fatalf("RecordMaterialDelivery: %v", err)
	}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 0, delivered: 40} // acquire 0: the source path cannot deliver either
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(ladenHauler(t, "HAULER-7", "FAB_MATS", 40))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.deliverCalls) != 0 {
		t.Fatalf("a met bill must NOT trigger an on-hand delivery (no over-supply), got %+v", producer.deliverCalls)
	}
	if !pipeline.GetMaterial("FAB_MATS").IsComplete() {
		t.Fatalf("expected the bill to remain complete/unchanged, progress %.1f%%", pipeline.ConstructionProgress())
	}
}
