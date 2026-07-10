package manufacturing

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// These tests pin the sp-8yf1 fix: extracted gas that fills a storage buffer must
// be sold (STORAGE COLLECT_SELL) when no factory consumes it, so siphons resume —
// while still deferring to a factory that IS actively pulling the gas via
// STORAGE_ACQUIRE_DELIVER. Before the fix, scanForCollectionOpportunities skipped
// ALL gas goods unconditionally (gasExtractionByproducts filter at the storage-sell
// site), so gas dead-ended in storage forever.

// gasDispMarketRepo exposes a single IMPORT buyer for one good.
type gasDispMarketRepo struct {
	market.MarketRepository
	buyerWaypoint string
	good          string
	buyPrice      int
}

func (m *gasDispMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return []string{m.buyerWaypoint}, nil
}

func (m *gasDispMarketRepo) GetMarketData(_ context.Context, waypoint string, _ int) (*market.Market, error) {
	tg, err := market.NewTradeGood(m.good, nil, nil, m.buyPrice, 0, 100, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypoint, []market.TradeGood{*tg}, time.Now())
}

// gasDispStorageRepo returns a fixed set of RUNNING storage operations.
type gasDispStorageRepo struct {
	storage.StorageOperationRepository
	ops []*storage.StorageOperation
}

func (s *gasDispStorageRepo) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	return s.ops, nil
}

// gasDispPipelineRepo records created pipelines; no active collection exists.
type gasDispPipelineRepo struct {
	manufacturing.PipelineRepository
	created []*manufacturing.ManufacturingPipeline
}

func (p *gasDispPipelineRepo) FindActiveCollectionForProduct(_ context.Context, _ int, _ string) (*manufacturing.ManufacturingPipeline, error) {
	return nil, nil
}

func (p *gasDispPipelineRepo) Create(_ context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	p.created = append(p.created, pipeline)
	return nil
}

func (p *gasDispPipelineRepo) Delete(_ context.Context, _ string) error { return nil }

// gasDispTaskRepo controls the in-flight task set (consumer detection) and records
// created tasks.
type gasDispTaskRepo struct {
	manufacturing.TaskRepository
	incomplete []*manufacturing.ManufacturingTask
	created    []*manufacturing.ManufacturingTask
}

func (t *gasDispTaskRepo) FindIncomplete(_ context.Context, _ int) ([]*manufacturing.ManufacturingTask, error) {
	return t.incomplete, nil
}

func (t *gasDispTaskRepo) Create(_ context.Context, task *manufacturing.ManufacturingTask) error {
	t.created = append(t.created, task)
	return nil
}

func runningGasOp(t *testing.T, good string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewStorageOperation(
		"gas-op-1", 1, "X1-TEST-GG", storage.OperationTypeGasSiphon,
		[]string{"SIPHON-1"}, []string{"STORAGE-1"}, []string{good}, nil,
	)
	if err != nil {
		t.Fatalf("NewStorageOperation: %v", err)
	}
	op.Start() // storage source must be RUNNING to be a valid disposition source
	return op
}

func newGasDispositionManager(finder *services.CollectionOpportunityFinder, pipelineRepo manufacturing.PipelineRepository, taskRepo manufacturing.TaskRepository, marketRepo market.MarketRepository, queue TaskEnqueuer) *PipelineLifecycleManager {
	return NewPipelineLifecycleManager(
		nil, finder, nil, queue, nil,
		pipelineRepo, taskRepo, nil, marketRepo,
		NewActivePipelineRegistry(),
		nil, nil, nil, nil, nil,
	)
}

// No factory consumes the gas -> the storage buffer is surplus -> it must be sold.
func TestScanForCollectionOpportunities_SellsGasWhenNoFactoryConsumer(t *testing.T) {
	const good = "LIQUID_HYDROGEN"

	marketRepo := &gasDispMarketRepo{buyerWaypoint: "X1-TEST-BUYER", good: good, buyPrice: 120}
	storageRepo := &gasDispStorageRepo{ops: []*storage.StorageOperation{runningGasOp(t, good)}}
	pipelineRepo := &gasDispPipelineRepo{}
	taskRepo := &gasDispTaskRepo{} // nothing in flight -> no consumer

	finder := services.NewCollectionOpportunityFinder(marketRepo, pipelineRepo).WithStorageRepo(storageRepo)
	queue := services.NewTaskQueue()

	manager := newGasDispositionManager(finder, pipelineRepo, taskRepo, marketRepo, queue)

	created := manager.scanForCollectionOpportunities(context.Background(), PipelineScanParams{
		SystemSymbol: "X1-TEST", PlayerID: 1, MaxCollectionPipelines: 0,
	})

	if created != 1 {
		t.Fatalf("expected 1 storage COLLECT_SELL pipeline for surplus gas, got %d", created)
	}
	if len(taskRepo.created) != 1 {
		t.Fatalf("expected 1 created task, got %d", len(taskRepo.created))
	}
	got := taskRepo.created[0]
	if got.Good() != good {
		t.Fatalf("expected COLLECT_SELL task for %s, got %s", good, got.Good())
	}
	if got.TaskType() != manufacturing.TaskTypeCollectSell {
		t.Fatalf("expected task type COLLECT_SELL, got %s", got.TaskType())
	}
	if !got.IsStorageBasedCollection() {
		t.Fatalf("expected a storage-based COLLECT_SELL (storage operation fields set)")
	}
}

// A factory IS pulling the gas from storage via STORAGE_ACQUIRE_DELIVER -> the
// storage-sell path defers to that delivery and creates no competing sale.
func TestScanForCollectionOpportunities_DefersGasToActiveFactoryConsumer(t *testing.T) {
	const good = "LIQUID_HYDROGEN"

	consumer := manufacturing.NewStorageAcquireDeliverTask(
		"pipeline-consumer", 1, good, "gas-op-1", "X1-TEST-GG", "X1-TEST-FACTORY", nil,
	)
	if err := consumer.MarkReady(); err != nil {
		t.Fatalf("MarkReady consumer: %v", err)
	}

	marketRepo := &gasDispMarketRepo{buyerWaypoint: "X1-TEST-BUYER", good: good, buyPrice: 120}
	storageRepo := &gasDispStorageRepo{ops: []*storage.StorageOperation{runningGasOp(t, good)}}
	pipelineRepo := &gasDispPipelineRepo{}
	taskRepo := &gasDispTaskRepo{incomplete: []*manufacturing.ManufacturingTask{consumer}}

	finder := services.NewCollectionOpportunityFinder(marketRepo, pipelineRepo).WithStorageRepo(storageRepo)
	queue := services.NewTaskQueue()

	manager := newGasDispositionManager(finder, pipelineRepo, taskRepo, marketRepo, queue)

	created := manager.scanForCollectionOpportunities(context.Background(), PipelineScanParams{
		SystemSymbol: "X1-TEST", PlayerID: 1, MaxCollectionPipelines: 0,
	})

	if created != 0 {
		t.Fatalf("expected gas sale deferred to active factory consumer (0 pipelines), got %d", created)
	}
	if len(taskRepo.created) != 0 {
		t.Fatalf("expected no COLLECT_SELL task created while a factory pulls the gas, got %d", len(taskRepo.created))
	}
}
