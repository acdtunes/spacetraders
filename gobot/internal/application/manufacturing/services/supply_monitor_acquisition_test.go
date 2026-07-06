package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// acquisitionStubTaskRepo embeds the domain interface so only the methods the
// supply monitor exercises need concrete implementations; any unexpected call
// panics on a nil-method deref.
type acquisitionStubTaskRepo struct {
	manufacturing.TaskRepository

	created []*manufacturing.ManufacturingTask
}

func (r *acquisitionStubTaskRepo) FindByPipelineID(_ context.Context, _ string) ([]*manufacturing.ManufacturingTask, error) {
	return nil, nil
}

func (r *acquisitionStubTaskRepo) FindByStatus(_ context.Context, _ int, _ manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	return nil, nil
}

func (r *acquisitionStubTaskRepo) Create(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.created = append(r.created, task)
	return nil
}

func newAcquisitionTestMarket(t *testing.T, waypointSymbol string, goods []market.TradeGood) *market.Market {
	t.Helper()
	m, err := market.NewMarket(waypointSymbol, goods, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", waypointSymbol, err)
	}
	return m
}

func newAcquisitionTestTradeGood(t *testing.T, symbol, supply, activity string, tradeType market.TradeType) market.TradeGood {
	t.Helper()
	good, err := market.NewTradeGood(symbol, &supply, &activity, 100, 90, 40, tradeType)
	if err != nil {
		t.Fatalf("NewTradeGood(%s): %v", symbol, err)
	}
	return *good
}

// Reproduces the bug: for a mineable raw input, FindExportMarketWithGoodSupply
// returns (nil, nil) when no HIGH/ABUNDANT exporter exists, but only err was
// checked before dereferencing exportMarket.WaypointSymbol - nil panic.
func TestPollOnce_RawInputWithoutGoodSupplyMarket_CreatesNoTaskWithoutPanic(t *testing.T) {
	const (
		factorySymbol = "X1-TEST-B2"
		exportSymbol  = "X1-TEST-E1"
	)

	pipeline := manufacturing.NewConstructionPipeline("X1-TEST-I67", 1, 3, 2)
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}

	factory := manufacturing.NewFactoryState(factorySymbol, "IRON", pipeline.ID(), 1, []string{"IRON_ORE"})
	if err := factory.RecordDelivery("IRON_ORE", 10, "SHIP-1"); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	tracker := manufacturing.NewFactoryStateTracker()
	tracker.LoadState(factory)

	// Factory market: output MODERATE (below target), input IRON_ORE imported.
	// The only IRON_ORE exporter in the system is SCARCE - no HIGH/ABUNDANT market.
	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{exportSymbol},
		markets: map[string]*market.Market{
			factorySymbol: newAcquisitionTestMarket(t, factorySymbol, []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON", "MODERATE", "STRONG", market.TradeTypeExport),
				newAcquisitionTestTradeGood(t, "IRON_ORE", "SCARCE", "STRONG", market.TradeTypeImport),
			}),
			exportSymbol: newAcquisitionTestMarket(t, exportSymbol, []market.TradeGood{
				newAcquisitionTestTradeGood(t, "IRON_ORE", "SCARCE", "WEAK", market.TradeTypeExport),
			}),
		},
	}

	taskRepo := &acquisitionStubTaskRepo{}
	pipelineRepo := &constructionStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}
	monitor := NewSupplyMonitor(
		marketRepo,
		tracker,
		nil, // factoryStateRepo
		pipelineRepo,
		NewTaskQueue(),
		taskRepo,
		NewMarketLocator(marketRepo, nil, nil, nil),
		nil, // storageOpRepo
		time.Minute,
		1,
	)

	monitor.PollOnce(context.Background())

	if len(taskRepo.created) != 0 {
		t.Fatalf("expected no ACQUIRE_DELIVER task when no acceptable export market exists, got %d", len(taskRepo.created))
	}
}
