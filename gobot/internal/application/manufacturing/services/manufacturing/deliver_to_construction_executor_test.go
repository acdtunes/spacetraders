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

	// constructionResult, when set, is returned as supplyResult.Construction so
	// tests can control the site's remaining bill after the supply (which drives
	// the replenishment decision). Defaults to a site with no materials, which
	// reports nothing remaining and therefore triggers no replenishment.
	constructionResult *manufacturing.ConstructionSite
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
	construction := r.constructionResult
	if construction == nil {
		construction = manufacturing.NewConstructionSite(waypointSymbol, "JUMP_GATE", nil, false)
	}
	return &manufacturing.ConstructionSupplyResult{
		Construction:   construction,
		UnitsDelivered: units,
	}, nil
}

// recordingTaskRepo captures tasks created by the executor's replenishment loop.
type recordingTaskRepo struct {
	manufacturing.TaskRepository
	created []*manufacturing.ManufacturingTask
}

func (r *recordingTaskRepo) Create(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.created = append(r.created, task)
	return nil
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

	executor := NewDeliverToConstructionExecutor(navigator, purchaser, siteRepo, &constructionFakePipelineRepo{}, &recordingTaskRepo{})

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

// During a supply dip a construction delivery can reach execution with no buy
// source (its source was never assigned, or was cleared when the market dried up).
// This must NOT be a hard failure that burns the retry budget and terminalizes the
// pipeline. The executor signals a supply deferral (ErrDeferToSupply) so the worker
// parks the task to be re-sourced by the SupplyMonitor when supply recovers - the
// execution-layer twin of the planner's per-material deferral (sp-hs2j / sp-r900).
func TestDeliverToConstruction_NoSource_SignalsSupplyDeferral(t *testing.T) {
	// No source market AND no factory - the deferred/unsourceable signature.
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "", "", "X1-TEST-I67", []string{},
	)

	navigator := &constructionFakeNavigator{ship: newConstructionTestShip(t)} // empty cargo
	purchaser := &constructionFakePurchaser{unitsAdded: 40}
	siteRepo := &constructionFakeSiteRepo{}

	executor := NewDeliverToConstructionExecutor(navigator, purchaser, siteRepo, &constructionFakePipelineRepo{}, &recordingTaskRepo{})

	err := executor.Execute(context.Background(), TaskExecutionParams{
		Task:       task,
		ShipSymbol: "TEST-1",
		PlayerID:   shared.MustNewPlayerID(1),
	})
	if err == nil {
		t.Fatalf("expected a supply-deferral signal when no source is available")
	}
	if !errors.Is(err, ErrDeferToSupply) {
		t.Fatalf("expected error to wrap ErrDeferToSupply, got %v", err)
	}
	if purchaser.params != nil {
		t.Fatalf("must not attempt a purchase when there is no source to buy from")
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

	executor := NewDeliverToConstructionExecutor(navigator, nil, siteRepo, &constructionFakePipelineRepo{}, &recordingTaskRepo{})

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

// After a successful supply that leaves the site's bill for the delivered good
// unfinished (remaining > 0), the executor must enqueue the NEXT delivery task for
// that material so the pipeline keeps filling the bill without a manual re-plan.
// Regression for sp-b1np: the construction pipeline had no task replenishment and
// idled EXECUTING at partial fill after delivering one cargo load per material.
// The replenishment task must reuse the completed task's delivery spec (same good,
// source, and construction site) so it matches how the planner sizes deliveries.
func TestDeliverToConstruction_ReplenishesNextTaskWhenRemaining(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)

	navigator := &constructionFakeNavigator{ship: newConstructionTestShipWithCargo(t, "FAB_MATS", 40)}
	// After delivering 40, the site still needs 60 more FAB_MATS (100 required, 40 fulfilled).
	siteRepo := &constructionFakeSiteRepo{
		constructionResult: manufacturing.NewConstructionSite("X1-TEST-I67", "JUMP_GATE",
			[]manufacturing.ConstructionMaterial{
				manufacturing.NewConstructionMaterial("FAB_MATS", 100, 40),
			}, false),
	}
	taskRepo := &recordingTaskRepo{}

	executor := NewDeliverToConstructionExecutor(navigator, nil, siteRepo, &constructionFakePipelineRepo{}, taskRepo)

	if err := executor.Execute(context.Background(), TaskExecutionParams{
		Task:       task,
		ShipSymbol: "TORWIND-7",
		PlayerID:   shared.MustNewPlayerID(1),
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(taskRepo.created) != 1 {
		t.Fatalf("expected exactly 1 replenishment task created, got %d", len(taskRepo.created))
	}
	next := taskRepo.created[0]
	if next.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Fatalf("expected DELIVER_TO_CONSTRUCTION, got %s", next.TaskType())
	}
	if next.Good() != "FAB_MATS" {
		t.Fatalf("expected replenishment good FAB_MATS, got %s", next.Good())
	}
	if next.SourceMarket() != "X1-TEST-F56" {
		t.Fatalf("expected replenishment source market X1-TEST-F56 (reused from completed task), got %s", next.SourceMarket())
	}
	if next.ConstructionSite() != "X1-TEST-I67" {
		t.Fatalf("expected replenishment construction site X1-TEST-I67, got %s", next.ConstructionSite())
	}
	if next.PipelineID() != "pipeline-1" {
		t.Fatalf("expected replenishment task on pipeline-1, got %s", next.PipelineID())
	}
	if next.Status() != manufacturing.TaskStatusReady {
		t.Fatalf("expected replenishment task READY so the coordinator can assign it, got %s", next.Status())
	}
}

// When a successful supply completes the delivered material's bill (remaining == 0),
// the executor must NOT enqueue another task for it - otherwise the pipeline would
// keep buying loads it can no longer deliver and never settle. Regression for sp-b1np.
func TestDeliverToConstruction_NoReplenishWhenMaterialComplete(t *testing.T) {
	task := manufacturing.NewDeliverToConstructionTask(
		"pipeline-1", 1, "FAB_MATS", "X1-TEST-F56", "", "X1-TEST-I67", []string{},
	)

	navigator := &constructionFakeNavigator{ship: newConstructionTestShipWithCargo(t, "FAB_MATS", 40)}
	// After delivering 40, the site's FAB_MATS bill is fully met (40 required, 40 fulfilled).
	siteRepo := &constructionFakeSiteRepo{
		constructionResult: manufacturing.NewConstructionSite("X1-TEST-I67", "JUMP_GATE",
			[]manufacturing.ConstructionMaterial{
				manufacturing.NewConstructionMaterial("FAB_MATS", 40, 40),
			}, true),
	}
	taskRepo := &recordingTaskRepo{}

	executor := NewDeliverToConstructionExecutor(navigator, nil, siteRepo, &constructionFakePipelineRepo{}, taskRepo)

	if err := executor.Execute(context.Background(), TaskExecutionParams{
		Task:       task,
		ShipSymbol: "TORWIND-7",
		PlayerID:   shared.MustNewPlayerID(1),
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(taskRepo.created) != 0 {
		t.Fatalf("expected no replenishment task when the material is complete, got %d", len(taskRepo.created))
	}
}
