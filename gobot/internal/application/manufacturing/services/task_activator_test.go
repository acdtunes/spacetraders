package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// activatorStubTaskRepo embeds the domain interface so only the methods the
// activator exercises need concrete implementations; any unexpected call
// panics on a nil-method deref.
type activatorStubTaskRepo struct {
	manufacturing.TaskRepository

	pending []*manufacturing.ManufacturingTask
	updated []*manufacturing.ManufacturingTask
}

func (r *activatorStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	if status != manufacturing.TaskStatusPending {
		return nil, nil
	}
	return r.pending, nil
}

func (r *activatorStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.updated = append(r.updated, task)
	return nil
}

// activatorStubPipelineRepo embeds the domain interface so only FindByID
// needs a concrete implementation.
type activatorStubPipelineRepo struct {
	manufacturing.PipelineRepository

	pipeline *manufacturing.ManufacturingPipeline
}

func (r *activatorStubPipelineRepo) FindByID(_ context.Context, _ string) (*manufacturing.ManufacturingPipeline, error) {
	return r.pipeline, nil
}

// activatorStubTaskQueue embeds the interface so only Enqueue needs a
// concrete implementation.
type activatorStubTaskQueue struct {
	ManufacturingTaskQueue

	enqueued []*manufacturing.ManufacturingTask
}

func (q *activatorStubTaskQueue) Enqueue(task *manufacturing.ManufacturingTask) {
	q.enqueued = append(q.enqueued, task)
}

// newActivatorUnderTest builds a TaskActivator via struct literal - there is
// no exported constructor; production code (supply_monitor.go) builds it the
// same way.
func newActivatorUnderTest(taskRepo *activatorStubTaskRepo, pipelineRepo *activatorStubPipelineRepo, taskQueue *activatorStubTaskQueue, marketLocator *MarketLocator) *TaskActivator {
	return &TaskActivator{
		taskRepo:      taskRepo,
		pipelineRepo:  pipelineRepo,
		taskQueue:     taskQueue,
		marketLocator: marketLocator,
		playerID:      1,
		notifier:      &taskReadyNotifier{},
	}
}

// sp-j2hq: a deferred DELIVER_TO_CONSTRUCTION task recovered by the poll-loop
// must be sourced against the pipeline's persisted --min-supply floor, not a
// hardcoded default MODERATE floor - otherwise a floor set at planning time
// (or on a later resumed `construction start --min-supply X`) is silently
// dropped the moment a material actually needs the recovery path.
func TestActivateConstructionTasks_DeferredTask_UsesPersistedMinSupplyFloor(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	pipeline.SetMinSupply("SCARCE")
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil,
	)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{circuitryScarce},
		markets: map[string]*market.Market{
			circuitryScarce: newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 1 {
		t.Fatalf("expected 1 task activated (SCARCE floor accepts the export), got %d", activated)
	}
	if deferredTask.SourceMarket() != circuitryScarce {
		t.Errorf("expected deferred task sourced from SCARCE exporter %s, got %q", circuitryScarce, deferredTask.SourceMarket())
	}
	if deferredTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected deferred task to become READY, got %s", deferredTask.Status())
	}
	if len(taskQueue.enqueued) != 1 {
		t.Errorf("expected the recovered task to be enqueued, got %d enqueued", len(taskQueue.enqueued))
	}
}

// Regression/pin test: an unset (empty) --min-supply floor must still
// default to MODERATE in the recovery path, so a SCARCE-only exporter is
// correctly left deferred (not silently accepted just because we now read
// the floor from the pipeline instead of a hardcoded literal).
func TestActivateConstructionTasks_DeferredTask_UnsetFloorDefaultsToModerate(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	// MinSupply deliberately left unset ("").
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil,
	)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{circuitryScarce},
		markets: map[string]*market.Market{
			circuitryScarce: newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 0 {
		t.Fatalf("expected 0 tasks activated (SCARCE exporter is below the default MODERATE floor), got %d", activated)
	}
	if deferredTask.SourceMarket() != "" {
		t.Errorf("expected deferred task to remain unsourced, got %q", deferredTask.SourceMarket())
	}
	if deferredTask.Status() != manufacturing.TaskStatusPending {
		t.Errorf("expected deferred task to remain PENDING, got %s", deferredTask.Status())
	}
	if len(taskQueue.enqueued) != 0 {
		t.Errorf("expected nothing enqueued, got %d", len(taskQueue.enqueued))
	}
}

// sp-9p87s — the buy-only construction deadlock: the drain buys a gate material from the factory's
// own EXPORT market without feeding it, depleting the export MODERATE->SCARCE, so the next deferred
// DELIVER_TO_CONSTRUCTION task has no buy source that clears the floor and sits PENDING forever (the
// old recovery only ever assigned a BUY source). The recovery must resolve a FACTORY for a
// manufacturable good and set it (fabricate) so the task goes READY and the drain fabricates it (buys
// inputs, feeds the factory, harvests) instead of buying the depleted export cold.
func TestActivateConstructionTasks_DeferredManufacturable_RecoversAsFabricate(t *testing.T) {
	const factoryWp = "X1-PZ28-FAC"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	// An explicit MODERATE floor: the factory's own SCARCE export can't clear it, so ONLY the
	// factory-fabricate recovery can un-stick this task — proving the fix, not an incidental buy.
	pipeline.SetMinSupply("MODERATE")
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "", "", plannerTestSite, nil,
	)
	if !deferredTask.IsDeferredConstruction() {
		t.Fatal("precondition: task must start deferred")
	}

	// A FACTORY for FAB_MATS: EXPORTS FAB_MATS (SCARCE — below the MODERATE buy floor) and IMPORTS its
	// inputs IRON + QUARTZ_SAND, so FindFactoryForProduction resolves it while FindConstructionSource can't.
	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{factoryWp},
		markets: map[string]*market.Market{
			factoryWp: newFabricationFactoryMarket(t, factoryWp, "FAB_MATS", "IRON", "QUARTZ_SAND"),
		},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 1 {
		t.Fatalf("expected the deferred manufacturable material recovered (1 activated), got %d", activated)
	}
	if deferredTask.FactorySymbol() != factoryWp {
		t.Errorf("expected the deferred task recovered as a FABRICATE carrying factory %s, got factory=%q", factoryWp, deferredTask.FactorySymbol())
	}
	if deferredTask.SourceMarket() != "" {
		t.Errorf("a fabricate recovery must carry NO buy source (produced, not bought), got %q", deferredTask.SourceMarket())
	}
	if deferredTask.IsDeferredConstruction() {
		t.Error("expected the task no longer deferred after factory recovery")
	}
	if deferredTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected the recovered task READY, got %s", deferredTask.Status())
	}
	if len(taskQueue.enqueued) != 1 {
		t.Errorf("expected the recovered task enqueued, got %d", len(taskQueue.enqueued))
	}
}

// sp-9p87s fallback (unchanged): a deferred good with NO factory to fabricate at AND no buy source
// stays deferred (PENDING, re-tried on a later poll) — the fix never spends and never fails closed.
func TestActivateConstructionTasks_DeferredNoFactoryNoBuy_StaysDeferred(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "", "", plannerTestSite, nil,
	)

	// No markets at all: no factory to fabricate at, no market to buy from.
	marketRepo := &plannerStubMarketRepo{markets: map[string]*market.Market{}}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 0 {
		t.Fatalf("expected the task to stay deferred (no factory, no buy source), got %d activated", activated)
	}
	if deferredTask.FactorySymbol() != "" || deferredTask.SourceMarket() != "" {
		t.Errorf("expected no factory and no source assigned, got factory=%q source=%q", deferredTask.FactorySymbol(), deferredTask.SourceMarket())
	}
	if !deferredTask.IsDeferredConstruction() {
		t.Error("expected the task to remain deferred")
	}
	if deferredTask.Status() != manufacturing.TaskStatusPending {
		t.Errorf("expected the task to remain PENDING, got %s", deferredTask.Status())
	}
}

// sp-9p87s precedence — factory-first falls through to BUY: a manufacturable good whose only market
// is a plain EXPORT (no factory importing its inputs) has no fabrication factory to resolve, so the
// recovery falls back to the buy-source path (source set, factory empty), never a phantom fabricate.
func TestActivateConstructionTasks_DeferredManufacturable_NoFactory_RecoversViaBuy(t *testing.T) {
	const buyMarket = "X1-PZ28-BUY"

	pipeline := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipeline.ID(), 1, "FAB_MATS", "", "", plannerTestSite, nil,
	)

	// A plain FAB_MATS EXPORT at MODERATE that does NOT import FAB_MATS' inputs (IRON, QUARTZ_SAND), so
	// it is NOT a fabrication factory — FindFactoryForProduction skips it and only the buy path recovers.
	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{buyMarket},
		markets:         map[string]*market.Market{buyMarket: newTradeTypeMarket(t, buyMarket, "FAB_MATS", "MODERATE", "STRONG", market.TradeTypeExport, 100)},
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	taskRepo := &activatorStubTaskRepo{pending: []*manufacturing.ManufacturingTask{deferredTask}}
	pipelineRepo := &activatorStubPipelineRepo{pipeline: pipeline}
	taskQueue := &activatorStubTaskQueue{}
	activator := newActivatorUnderTest(taskRepo, pipelineRepo, taskQueue, marketLocator)

	activated := activator.ActivateConstructionTasks(context.Background())

	if activated != 1 {
		t.Fatalf("expected the good recovered via buy (1 activated), got %d", activated)
	}
	if deferredTask.SourceMarket() != buyMarket {
		t.Errorf("expected FAB_MATS recovered as a BUY from %s, got source=%q", buyMarket, deferredTask.SourceMarket())
	}
	if deferredTask.FactorySymbol() != "" {
		t.Errorf("with no fabrication factory, recovery must fall back to BUY (factory must stay empty), got factory=%q", deferredTask.FactorySymbol())
	}
	if deferredTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected the recovered task READY, got %s", deferredTask.Status())
	}
}
