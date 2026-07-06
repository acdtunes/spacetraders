package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// pinStubTaskRepo embeds the domain interface so only the methods the
// supply monitor exercises need concrete implementations; any unexpected call
// panics on a nil-method deref.
type pinStubTaskRepo struct {
	manufacturing.TaskRepository

	tasks   []*manufacturing.ManufacturingTask
	created []*manufacturing.ManufacturingTask
	updated map[string]manufacturing.TaskStatus
}

func (r *pinStubTaskRepo) FindByPipelineID(_ context.Context, pipelineID string) ([]*manufacturing.ManufacturingTask, error) {
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t.PipelineID() == pipelineID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *pinStubTaskRepo) FindByStatus(_ context.Context, _ int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	var out []*manufacturing.ManufacturingTask
	for _, t := range r.tasks {
		if t.Status() == status {
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *pinStubTaskRepo) FindByID(_ context.Context, id string) (*manufacturing.ManufacturingTask, error) {
	for _, t := range r.tasks {
		if t.ID() == id {
			return t, nil
		}
	}
	return nil, nil
}

func (r *pinStubTaskRepo) Create(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.created = append(r.created, task)
	return nil
}

func (r *pinStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	if r.updated == nil {
		r.updated = make(map[string]manufacturing.TaskStatus)
	}
	r.updated[task.ID()] = task.Status()
	return nil
}

type pinStubStorageOpRepo struct {
	storage.StorageOperationRepository

	operations []*storage.StorageOperation
}

func (r *pinStubStorageOpRepo) FindByGood(_ context.Context, _ int, good string) ([]*storage.StorageOperation, error) {
	var out []*storage.StorageOperation
	for _, op := range r.operations {
		if op.SupportsGood(good) {
			out = append(out, op)
		}
	}
	return out, nil
}

type pinStubEventPublisher struct {
	navigation.ShipEventPublisher

	readyEvents []navigation.TasksBecameReadyEvent
}

func (p *pinStubEventPublisher) PublishTasksBecameReady(event navigation.TasksBecameReadyEvent) {
	p.readyEvents = append(p.readyEvents, event)
}

type pinMonitorFixture struct {
	marketRepo   *plannerStubMarketRepo
	tracker      *manufacturing.FactoryStateTracker
	pipelineRepo *constructionStubPipelineRepo
	queue        *TaskQueue
	taskRepo     *pinStubTaskRepo
	locator      *MarketLocator
	storageOps   *pinStubStorageOpRepo
	publisher    *pinStubEventPublisher
}

func newPinMonitor(f *pinMonitorFixture) *SupplyMonitor {
	if f.tracker == nil {
		f.tracker = manufacturing.NewFactoryStateTracker()
	}
	if f.queue == nil {
		f.queue = NewTaskQueue()
	}
	var marketRepo market.MarketRepository
	if f.marketRepo != nil {
		marketRepo = f.marketRepo
	}
	var pipelineRepo manufacturing.PipelineRepository
	if f.pipelineRepo != nil {
		pipelineRepo = f.pipelineRepo
	}
	var taskRepo manufacturing.TaskRepository
	if f.taskRepo != nil {
		taskRepo = f.taskRepo
	}
	var storageOps storage.StorageOperationRepository
	if f.storageOps != nil {
		storageOps = f.storageOps
	}
	var publisher navigation.ShipEventPublisher
	if f.publisher != nil {
		publisher = f.publisher
	}
	return NewSupplyMonitor(
		marketRepo,
		f.tracker,
		nil,
		pipelineRepo,
		f.queue,
		taskRepo,
		NewSellMarketDistributor(marketRepo, taskRepo),
		f.locator,
		storageOps,
		publisher,
		time.Minute,
		1,
	)
}

func newExecutingManufacturingPipeline(t *testing.T, productGood string) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewPipeline(productGood, "X1-PIN-SELL", 100, 1)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

func pinPipelineRepo(pipelines ...*manufacturing.ManufacturingPipeline) *constructionStubPipelineRepo {
	byID := make(map[string]*manufacturing.ManufacturingPipeline, len(pipelines))
	for _, p := range pipelines {
		byID[p.ID()] = p
	}
	return &constructionStubPipelineRepo{pipelines: byID}
}

// Pin 1: a factory whose supply was HIGH (ready for collection) must have its
// ready flag reset when the market poll observes supply dropping to MODERATE.
func TestPollOnce_SupplyDropBelowHighResetsFactoryReadyFlag(t *testing.T) {
	pipeline := newExecutingManufacturingPipeline(t, "IRON")
	factory := manufacturing.NewFactoryState("X1-PIN-FAC", "IRON", pipeline.ID(), 1, []string{"IRON_ORE"})
	factory.UpdateSupply("HIGH")
	if !factory.IsReadyForCollection() {
		t.Fatalf("precondition: factory must be ready at HIGH supply")
	}
	tracker := manufacturing.NewFactoryStateTracker()
	tracker.LoadState(factory)

	f := &pinMonitorFixture{
		tracker: tracker,
		marketRepo: &plannerStubMarketRepo{
			markets: map[string]*market.Market{
				"X1-PIN-FAC": newAcquisitionTestMarket(t, "X1-PIN-FAC", []market.TradeGood{
					newAcquisitionTestTradeGood(t, "IRON", "MODERATE", "STRONG", market.TradeTypeExport),
				}),
			},
		},
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     &pinStubTaskRepo{},
	}

	newPinMonitor(f).PollOnce(context.Background())

	if factory.IsReadyForCollection() {
		t.Fatalf("expected ready flag reset after supply dropped below HIGH")
	}
	if factory.CurrentSupply() != "MODERATE" {
		t.Fatalf("expected current supply MODERATE, got %s", factory.CurrentSupply())
	}
}

// Pin 2: when factory supply drops HIGH->MODERATE, an ACQUIRE_DELIVER task for
// a low input is created only if no in-flight task already exists for it.
func TestPollOnce_CreatesAcquireDeliverOnlyWhenNoPendingTaskForInput(t *testing.T) {
	const (
		factorySymbol = "X1-PIN-FAC"
		exportSymbol  = "X1-PIN-E1"
	)
	cases := []struct {
		name        string
		withPending bool
		wantCreated int
	}{
		{name: "no pending task creates one", withPending: false, wantCreated: 1},
		{name: "in-flight task suppresses creation", withPending: true, wantCreated: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newExecutingManufacturingPipeline(t, "IRON")
			factory := manufacturing.NewFactoryState(factorySymbol, "IRON", pipeline.ID(), 1, []string{"IRON_ORE"})
			factory.UpdateSupply("HIGH")
			tracker := manufacturing.NewFactoryStateTracker()
			tracker.LoadState(factory)

			marketRepo := &plannerStubMarketRepo{
				marketWaypoints: []string{factorySymbol, exportSymbol},
				markets: map[string]*market.Market{
					factorySymbol: newAcquisitionTestMarket(t, factorySymbol, []market.TradeGood{
						newAcquisitionTestTradeGood(t, "IRON", "MODERATE", "STRONG", market.TradeTypeExport),
						newAcquisitionTestTradeGood(t, "IRON_ORE", "SCARCE", "STRONG", market.TradeTypeImport),
					}),
					exportSymbol: newAcquisitionTestMarket(t, exportSymbol, []market.TradeGood{
						newAcquisitionTestTradeGood(t, "IRON_ORE", "HIGH", "STRONG", market.TradeTypeExport),
					}),
				},
			}
			taskRepo := &pinStubTaskRepo{}
			if tc.withPending {
				taskRepo.tasks = append(taskRepo.tasks, manufacturing.NewAcquireDeliverTask(
					pipeline.ID(), 1, "IRON_ORE", exportSymbol, factorySymbol, nil,
				))
			}

			f := &pinMonitorFixture{
				tracker:      tracker,
				marketRepo:   marketRepo,
				pipelineRepo: pinPipelineRepo(pipeline),
				taskRepo:     taskRepo,
				locator:      NewMarketLocator(marketRepo, nil, nil, nil),
			}

			newPinMonitor(f).PollOnce(context.Background())

			if len(taskRepo.created) != tc.wantCreated {
				t.Fatalf("expected %d created tasks, got %d", tc.wantCreated, len(taskRepo.created))
			}
			if tc.wantCreated == 1 {
				created := taskRepo.created[0]
				if created.TaskType() != manufacturing.TaskTypeAcquireDeliver {
					t.Fatalf("expected ACQUIRE_DELIVER task, got %s", created.TaskType())
				}
				if created.Good() != "IRON_ORE" || created.SourceMarket() != exportSymbol {
					t.Fatalf("unexpected task routing: good=%s source=%s", created.Good(), created.SourceMarket())
				}
				if created.Status() != manufacturing.TaskStatusReady {
					t.Fatalf("expected READY task for HIGH source supply, got %s", created.Status())
				}
				if f.queue.GetTask(created.ID()) == nil {
					t.Fatalf("expected created task to be enqueued")
				}
			}
		})
	}
}

// Pin 3: when a running storage operation provides the input, the replenishment
// path must create STORAGE_ACQUIRE_DELIVER instead of a market ACQUIRE_DELIVER,
// even though an eligible export market exists.
func TestPollOnce_PrefersStorageOperationOverMarketBuy(t *testing.T) {
	const (
		factorySymbol = "X1-PIN-FAC"
		exportSymbol  = "X1-PIN-E1"
	)
	pipeline := newExecutingManufacturingPipeline(t, "IRON")
	factory := manufacturing.NewFactoryState(factorySymbol, "IRON", pipeline.ID(), 1, []string{"IRON_ORE"})
	factory.UpdateSupply("HIGH")
	tracker := manufacturing.NewFactoryStateTracker()
	tracker.LoadState(factory)

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{factorySymbol, exportSymbol},
		markets: map[string]*market.Market{
			factorySymbol: newAcquisitionTestMarket(t, factorySymbol, []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON", "MODERATE", "STRONG", market.TradeTypeExport),
				newAcquisitionTestTradeGood(t, "IRON_ORE", "SCARCE", "STRONG", market.TradeTypeImport),
			}),
			exportSymbol: newAcquisitionTestMarket(t, exportSymbol, []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON_ORE", "HIGH", "STRONG", market.TradeTypeExport),
			}),
		},
	}

	operation, err := storage.NewStorageOperation(
		"op-pin-1", 1, "X1-PIN-GAS", storage.OperationTypeGasSiphon,
		[]string{"EXTRACTOR-1"}, []string{"STORE-1"}, []string{"IRON_ORE"},
		shared.NewRealClock(),
	)
	if err != nil {
		t.Fatalf("NewStorageOperation: %v", err)
	}
	if err := operation.Start(); err != nil {
		t.Fatalf("operation.Start: %v", err)
	}

	taskRepo := &pinStubTaskRepo{}
	f := &pinMonitorFixture{
		tracker:      tracker,
		marketRepo:   marketRepo,
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     taskRepo,
		locator:      NewMarketLocator(marketRepo, nil, nil, nil),
		storageOps:   &pinStubStorageOpRepo{operations: []*storage.StorageOperation{operation}},
	}

	newPinMonitor(f).PollOnce(context.Background())

	if len(taskRepo.created) != 1 {
		t.Fatalf("expected exactly 1 created task, got %d", len(taskRepo.created))
	}
	created := taskRepo.created[0]
	if created.TaskType() != manufacturing.TaskTypeStorageAcquireDeliver {
		t.Fatalf("expected STORAGE_ACQUIRE_DELIVER task, got %s", created.TaskType())
	}
	if created.StorageOperationID() != "op-pin-1" {
		t.Fatalf("expected task bound to storage operation, got %q", created.StorageOperationID())
	}
}

// Pin 4a: ActivateSupplyGatedTasks readies and enqueues a PENDING
// ACQUIRE_DELIVER task once the source market shows acceptable supply.
func TestActivateSupplyGatedTasks_ActivatesAndEnqueuesTask(t *testing.T) {
	pipeline := newExecutingManufacturingPipeline(t, "COPPER")
	task := manufacturing.NewAcquireDeliverTask(pipeline.ID(), 1, "COPPER", "X1-PIN-SRC", "X1-PIN-FAC", nil)

	marketRepo := &plannerStubMarketRepo{
		markets: map[string]*market.Market{
			"X1-PIN-SRC": newAcquisitionTestMarket(t, "X1-PIN-SRC", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "COPPER", "HIGH", "STRONG", market.TradeTypeExport),
			}),
		},
	}
	taskRepo := &pinStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	f := &pinMonitorFixture{
		marketRepo:   marketRepo,
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     taskRepo,
	}
	monitor := newPinMonitor(f)

	activated := monitor.ActivateSupplyGatedTasks(context.Background())

	if activated != 1 {
		t.Fatalf("expected 1 activated task, got %d", activated)
	}
	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected task READY, got %s", task.Status())
	}
	if f.queue.GetTask(task.ID()) == nil {
		t.Fatalf("expected activated task to be enqueued")
	}
	if got := taskRepo.updated[task.ID()]; got != manufacturing.TaskStatusReady {
		t.Fatalf("expected READY persisted, got %q", got)
	}
}

// Pin 4b: ActivateCollectionPipelineTasks readies and enqueues a PENDING
// COLLECT_SELL task when the factory shows ABUNDANT supply and the sell
// market is not saturated.
func TestActivateCollectionPipelineTasks_ActivatesPendingWhenFactoryAbundant(t *testing.T) {
	pipeline := manufacturing.NewCollectionPipeline("IRON", "X1-PIN-SELL", 100, 1)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	task := manufacturing.NewCollectSellTask(pipeline.ID(), 1, "IRON", "X1-PIN-FAC", "X1-PIN-SELL", nil)

	marketRepo := &plannerStubMarketRepo{
		markets: map[string]*market.Market{
			"X1-PIN-FAC": newAcquisitionTestMarket(t, "X1-PIN-FAC", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON", "ABUNDANT", "STRONG", market.TradeTypeExport),
			}),
			"X1-PIN-SELL": newAcquisitionTestMarket(t, "X1-PIN-SELL", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON", "SCARCE", "STRONG", market.TradeTypeImport),
			}),
		},
	}
	taskRepo := &pinStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	f := &pinMonitorFixture{
		marketRepo:   marketRepo,
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     taskRepo,
	}
	monitor := newPinMonitor(f)

	activated := monitor.ActivateCollectionPipelineTasks(context.Background())

	if activated < 1 {
		t.Fatalf("expected at least 1 activation, got %d", activated)
	}
	if task.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected task READY, got %s", task.Status())
	}
	if f.queue.GetTask(task.ID()) == nil {
		t.Fatalf("expected task to be enqueued")
	}
}

// Pin 4c: ActivateCollectionPipelineTasks resets a READY COLLECT_SELL task
// back to PENDING (and does not enqueue it) when the sell market is saturated.
func TestActivateCollectionPipelineTasks_ResetsReadyTaskWhenSellMarketSaturated(t *testing.T) {
	pipeline := manufacturing.NewCollectionPipeline("IRON", "X1-PIN-SELL", 100, 1)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	task := manufacturing.NewCollectSellTask(pipeline.ID(), 1, "IRON", "X1-PIN-FAC", "X1-PIN-SELL", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	marketRepo := &plannerStubMarketRepo{
		markets: map[string]*market.Market{
			"X1-PIN-SELL": newAcquisitionTestMarket(t, "X1-PIN-SELL", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON", "HIGH", "STRONG", market.TradeTypeImport),
			}),
		},
	}
	taskRepo := &pinStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	f := &pinMonitorFixture{
		marketRepo:   marketRepo,
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     taskRepo,
	}
	monitor := newPinMonitor(f)

	activated := monitor.ActivateCollectionPipelineTasks(context.Background())

	if activated != 0 {
		t.Fatalf("expected 0 activations, got %d", activated)
	}
	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected task reset to PENDING, got %s", task.Status())
	}
	if f.queue.GetTask(task.ID()) != nil {
		t.Fatalf("saturated-market task must not be enqueued")
	}
	if got := taskRepo.updated[task.ID()]; got != manufacturing.TaskStatusPending {
		t.Fatalf("expected PENDING persisted, got %q", got)
	}
}

// Pin 4d: DeactivateSaturatedAcquireDeliverTasks resets a READY
// ACQUIRE_DELIVER task when the factory input supply is already HIGH.
func TestDeactivateSaturatedAcquireDeliverTasks_ResetsReadyTask(t *testing.T) {
	pipeline := newExecutingManufacturingPipeline(t, "IRON")
	task := manufacturing.NewAcquireDeliverTask(pipeline.ID(), 1, "IRON_ORE", "X1-PIN-SRC", "X1-PIN-FAC", nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	marketRepo := &plannerStubMarketRepo{
		markets: map[string]*market.Market{
			"X1-PIN-FAC": newAcquisitionTestMarket(t, "X1-PIN-FAC", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON_ORE", "HIGH", "STRONG", market.TradeTypeImport),
			}),
		},
	}
	taskRepo := &pinStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	f := &pinMonitorFixture{
		marketRepo: marketRepo,
		taskRepo:   taskRepo,
	}
	monitor := newPinMonitor(f)

	deactivated := monitor.DeactivateSaturatedAcquireDeliverTasks(context.Background())

	if deactivated != 1 {
		t.Fatalf("expected 1 deactivated task, got %d", deactivated)
	}
	if task.Status() != manufacturing.TaskStatusPending {
		t.Fatalf("expected task reset to PENDING, got %s", task.Status())
	}
	if got := taskRepo.updated[task.ID()]; got != manufacturing.TaskStatusPending {
		t.Fatalf("expected PENDING persisted, got %q", got)
	}
}

// Pin 5: a poll that activates a supply-gated task publishes exactly one
// TasksBecameReady event carrying the pipeline and player identifiers.
func TestPollOnce_PublishesTasksBecameReadyOnActivation(t *testing.T) {
	pipeline := newExecutingManufacturingPipeline(t, "COPPER")
	task := manufacturing.NewAcquireDeliverTask(pipeline.ID(), 1, "COPPER", "X1-PIN-SRC", "X1-PIN-FAC", nil)

	marketRepo := &plannerStubMarketRepo{
		markets: map[string]*market.Market{
			"X1-PIN-SRC": newAcquisitionTestMarket(t, "X1-PIN-SRC", []market.TradeGood{
				newAcquisitionTestTradeGood(t, "COPPER", "HIGH", "STRONG", market.TradeTypeExport),
			}),
		},
	}
	publisher := &pinStubEventPublisher{}
	f := &pinMonitorFixture{
		marketRepo:   marketRepo,
		pipelineRepo: pinPipelineRepo(pipeline),
		taskRepo:     &pinStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}},
		publisher:    publisher,
	}

	newPinMonitor(f).PollOnce(context.Background())

	if len(publisher.readyEvents) != 1 {
		t.Fatalf("expected exactly 1 TasksBecameReady event, got %d", len(publisher.readyEvents))
	}
	event := publisher.readyEvents[0]
	if event.PipelineID != pipeline.ID() || event.PlayerID != 1 {
		t.Fatalf("unexpected event payload: pipeline=%s player=%d", event.PipelineID, event.PlayerID)
	}
}
