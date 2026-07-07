package manufacturing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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
	supplyErr     error
}

func (r *constructionFakeSiteRepo) FindByWaypoint(_ context.Context, waypointSymbol string, _ int) (*manufacturing.ConstructionSite, error) {
	return manufacturing.NewConstructionSite(waypointSymbol, "JUMP_GATE", nil, false), nil
}

func (r *constructionFakeSiteRepo) SupplyMaterial(_ context.Context, _, waypointSymbol, tradeSymbol string, units int, _ int) (*manufacturing.ConstructionSupplyResult, error) {
	if r.supplyErr != nil {
		return nil, r.supplyErr
	}
	r.suppliedUnits = units
	r.suppliedGood = tradeSymbol
	return &manufacturing.ConstructionSupplyResult{
		Construction:   manufacturing.NewConstructionSite(waypointSymbol, "JUMP_GATE", nil, false),
		UnitsDelivered: units,
	}, nil
}

// captureLogEntry records a single logged line for assertions.
type captureLogEntry struct {
	level   string
	message string
}

// capturingLogger implements common.ContainerLogger and records entries so tests
// can assert on what would be rendered to the container log stream.
type capturingLogger struct {
	entries []captureLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, captureLogEntry{level: level, message: message})
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

// newConstructionTestShipWithCargo builds an in-orbit ship already holding
// `units` of `good`, so the executor skips the acquire phase and proceeds
// straight to the construction supply step.
func newConstructionTestShipWithCargo(t *testing.T, good string, units int) *navigation.Ship {
	t.Helper()
	waypoint, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	cargo, err := shared.NewCargo(40, units, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-7", shared.MustNewPlayerID(1), waypoint, fuel, 0, 40, cargo,
		9, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// When the construction supply API call fails, the executor must surface the
// underlying error VERBATIM at ERROR level so operators can see WHY a delivery
// died (e.g. a 404 route error) instead of an opaque "task failed". Regression
// for sp-fi7q: the executor previously returned the wrapped error without
// logging it, and the container log renderer drops structured map fields, so
// the real cause never reached the log stream. The verbatim error must appear
// in the log MESSAGE, not only a structured field.
func TestDeliverToConstruction_SurfacesSupplyErrorVerbatim(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-KA42-I53", []string{},
	)

	navigator := &constructionFakeNavigator{ship: newConstructionTestShipWithCargo(t, "FAB_MATS", 40)}
	supplyErr := errors.New(`API error (status 404): {"message":"Route POST:/my/ships/TORWIND-7/construction/supply not found","statusCode":404}`)
	siteRepo := &constructionFakeSiteRepo{supplyErr: supplyErr}
	logger := &capturingLogger{}

	executor := NewDeliverToConstructionExecutor(navigator, nil, siteRepo, &constructionFakePipelineRepo{})

	ctx := common.WithLogger(context.Background(), logger)
	err := executor.Execute(ctx, TaskExecutionParams{
		Task:       task,
		ShipSymbol: "TORWIND-7",
		PlayerID:   shared.MustNewPlayerID(1),
	})
	if err == nil {
		t.Fatalf("expected supply failure to propagate as an error")
	}

	var found bool
	for _, e := range logger.entries {
		if e.level == "ERROR" && strings.Contains(e.message, supplyErr.Error()) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an ERROR log whose message contains the verbatim supply error %q; got entries=%+v", supplyErr.Error(), logger.entries)
	}
}
