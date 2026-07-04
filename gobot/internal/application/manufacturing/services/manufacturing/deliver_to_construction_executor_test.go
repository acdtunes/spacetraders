package manufacturing

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type constructionFakeNavigator struct {
	Navigator

	ship         *navigation.Ship
	destinations []string
}

func (n *constructionFakeNavigator) ReloadShip(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return n.ship, nil
}

func (n *constructionFakeNavigator) NavigateAndDock(_ context.Context, _ string, destination string, _ shared.PlayerID) (*navigation.Ship, error) {
	n.destinations = append(n.destinations, destination)
	return n.ship, nil
}

type constructionFakePurchaser struct {
	params     *PurchaseLoopParams
	unitsAdded int
}

func (p *constructionFakePurchaser) ExecutePurchaseLoop(_ context.Context, params PurchaseLoopParams) (*PurchaseResult, error) {
	p.params = &params
	return &PurchaseResult{TotalUnitsAdded: p.unitsAdded, TotalCost: 100}, nil
}

type constructionFakeSiteRepo struct {
	suppliedUnits int
	suppliedGood  string
}

func (r *constructionFakeSiteRepo) FindByWaypoint(_ context.Context, waypointSymbol string, _ int) (*manufacturing.ConstructionSite, error) {
	return manufacturing.NewConstructionSite(waypointSymbol, "JUMP_GATE", nil, false), nil
}

func (r *constructionFakeSiteRepo) SupplyMaterial(_ context.Context, _, waypointSymbol, tradeSymbol string, units int, _ int) (*manufacturing.ConstructionSupplyResult, error) {
	r.suppliedUnits = units
	r.suppliedGood = tradeSymbol
	return &manufacturing.ConstructionSupplyResult{
		Construction:   manufacturing.NewConstructionSite(waypointSymbol, "JUMP_GATE", nil, false),
		UnitsDelivered: units,
	}, nil
}

type constructionFakePipelineRepo struct {
	manufacturing.PipelineRepository
}

func (r *constructionFakePipelineRepo) FindByID(_ context.Context, _ string) (*manufacturing.ManufacturingPipeline, error) {
	return nil, nil
}

func newConstructionTestShip(t *testing.T) *navigation.Ship {
	t.Helper()
	waypoint, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TEST-1", shared.MustNewPlayerID(1), waypoint, fuel, 0, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// A DELIVER_TO_CONSTRUCTION task with an empty cargo hold and a source market
// must acquire the good (purchase loop at the source market) before delivering
// to the construction site. This is the bug: the executor failed immediately
// with "no cargo to deliver" for freshly planned buy-from-market tasks.
func TestDeliverToConstruction_AcquiresFromMarketWhenCargoEmpty(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)

	navigator := &constructionFakeNavigator{ship: newConstructionTestShip(t)}
	purchaser := &constructionFakePurchaser{unitsAdded: 40}
	siteRepo := &constructionFakeSiteRepo{}

	executor := NewDeliverToConstructionExecutor(navigator, purchaser, siteRepo, &constructionFakePipelineRepo{})

	err := executor.Execute(context.Background(), TaskExecutionParams{
		Task:       task,
		ShipSymbol: "TEST-1",
		PlayerID:   shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if purchaser.params == nil {
		t.Fatalf("expected purchase loop to run for empty cargo")
	}
	if purchaser.params.Market != "X1-TEST-F56" {
		t.Fatalf("expected purchase at source market X1-TEST-F56, got %s", purchaser.params.Market)
	}
	if siteRepo.suppliedUnits != 40 {
		t.Fatalf("expected 40 units supplied to construction site, got %d", siteRepo.suppliedUnits)
	}
	if siteRepo.suppliedGood != "FAB_MATS" {
		t.Fatalf("expected FAB_MATS supplied, got %s", siteRepo.suppliedGood)
	}
	want := []string{"X1-TEST-F56", "X1-TEST-I67"}
	if len(navigator.destinations) != 2 || navigator.destinations[0] != want[0] || navigator.destinations[1] != want[1] {
		t.Fatalf("expected navigation to %v, got %v", want, navigator.destinations)
	}
}
