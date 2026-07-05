package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// plannerStubPipelineRepo embeds the domain interface so only the methods the
// planner exercises need concrete implementations; any unexpected call panics
// on a nil-method deref.
type plannerStubPipelineRepo struct {
	manufacturing.PipelineRepository

	existing *manufacturing.ManufacturingPipeline
	created  []*manufacturing.ManufacturingPipeline
	updated  []*manufacturing.ManufacturingPipeline
}

func (r *plannerStubPipelineRepo) FindByConstructionSite(_ context.Context, _ string, _ int) (*manufacturing.ManufacturingPipeline, error) {
	if r.existing == nil || r.existing.IsTerminal() {
		return nil, nil
	}
	return r.existing, nil
}

func (r *plannerStubPipelineRepo) Create(_ context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	r.created = append(r.created, pipeline)
	return nil
}

func (r *plannerStubPipelineRepo) Update(_ context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	r.updated = append(r.updated, pipeline)
	return nil
}

type plannerStubTaskRepo struct {
	manufacturing.TaskRepository

	tasksByPipeline map[string][]*manufacturing.ManufacturingTask
	createdBatches  [][]*manufacturing.ManufacturingTask
}

func (r *plannerStubTaskRepo) FindByPipelineID(_ context.Context, pipelineID string) ([]*manufacturing.ManufacturingTask, error) {
	return r.tasksByPipeline[pipelineID], nil
}

func (r *plannerStubTaskRepo) CreateBatch(_ context.Context, tasks []*manufacturing.ManufacturingTask) error {
	r.createdBatches = append(r.createdBatches, tasks)
	return nil
}

type plannerStubConstructionRepo struct {
	manufacturing.ConstructionSiteRepository

	site *manufacturing.ConstructionSite
}

func (r *plannerStubConstructionRepo) FindByWaypoint(_ context.Context, _ string, _ int) (*manufacturing.ConstructionSite, error) {
	return r.site, nil
}

type plannerStubMarketRepo struct {
	market.MarketRepository

	marketWaypoints []string
	markets         map[string]*market.Market
}

func (r *plannerStubMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return r.marketWaypoints, nil
}

func (r *plannerStubMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	return r.markets[waypointSymbol], nil
}

const (
	plannerTestSite   = "X1-PZ28-I67"
	plannerTestMarket = "X1-PZ28-F56"
)

func newPlannerTestConstructionSite(t *testing.T) *manufacturing.ConstructionSite {
	t.Helper()
	return manufacturing.NewConstructionSite(plannerTestSite, "JUMP_GATE", []manufacturing.ConstructionMaterial{
		manufacturing.NewConstructionMaterial("FAB_MATS", 1600, 0),
		manufacturing.NewConstructionMaterial("ADVANCED_CIRCUITRY", 400, 0),
	}, false)
}

func newPlannerTestMarketRepo(t *testing.T) *plannerStubMarketRepo {
	t.Helper()
	supply := "ABUNDANT"
	activity := "STRONG"

	goods := make([]market.TradeGood, 0, 2)
	for _, symbol := range []string{"FAB_MATS", "ADVANCED_CIRCUITRY"} {
		good, err := market.NewTradeGood(symbol, &supply, &activity, 100, 90, 40, market.TradeTypeExport)
		if err != nil {
			t.Fatalf("NewTradeGood(%s): %v", symbol, err)
		}
		goods = append(goods, *good)
	}

	m, err := market.NewMarket(plannerTestMarket, goods, time.Now())
	if err != nil {
		t.Fatalf("NewMarket: %v", err)
	}

	return &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket},
		markets:         map[string]*market.Market{plannerTestMarket: m},
	}
}

func newPlannerUnderTest(pipelineRepo *plannerStubPipelineRepo, taskRepo *plannerStubTaskRepo, marketRepo *plannerStubMarketRepo, site *manufacturing.ConstructionSite) *ConstructionPipelinePlanner {
	return NewConstructionPipelinePlanner(
		pipelineRepo,
		taskRepo,
		&plannerStubConstructionRepo{site: site},
		NewMarketLocator(marketRepo, nil, nil, nil),
	)
}

// Reproduces the bug: an EXECUTING CONSTRUCTION pipeline whose tasks were all
// reaped (0 rows in manufacturing_tasks) was resumed forever, silently ignoring
// the requested depth and permanently blocking the construction site.
func TestStartOrResume_EmptyExistingPipeline_TerminalizesAndRePlans(t *testing.T) {
	stale := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := stale.Start(); err != nil {
		t.Fatalf("stale.Start: %v", err)
	}

	pipelineRepo := &plannerStubPipelineRepo{existing: stale}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "")
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if result.IsResumed {
		t.Error("expected a re-planned pipeline, got IsResumed=true (stale empty pipeline resumed)")
	}
	if result.Pipeline.ID() == stale.ID() {
		t.Error("expected a fresh pipeline, got the stale empty pipeline")
	}
	if got := result.Pipeline.TaskCount(); got != 2 {
		t.Errorf("expected 2 DELIVER_TO_CONSTRUCTION tasks (one per material), got %d", got)
	}
	for _, task := range result.Pipeline.Tasks() {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
			t.Errorf("expected DELIVER_TO_CONSTRUCTION task, got %s", task.TaskType())
		}
	}

	// The stale pipeline must be terminalized and persisted so
	// FindByConstructionSite stops returning it.
	if !stale.IsTerminal() {
		t.Errorf("expected stale pipeline to be terminal, got status %s", stale.Status())
	}
	foundUpdate := false
	for _, p := range pipelineRepo.updated {
		if p.ID() == stale.ID() {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Error("expected stale pipeline terminal status to be persisted via Update")
	}

	// The re-planned tasks must be persisted - otherwise the coordinator
	// (which reads tasks from the DB) can never execute them.
	if len(taskRepo.createdBatches) == 0 {
		t.Fatal("expected re-planned tasks to be persisted via CreateBatch")
	}
	if got := len(taskRepo.createdBatches[0]); got != 2 {
		t.Errorf("expected 2 persisted tasks, got %d", got)
	}

	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected 1 new pipeline created, got %d", len(pipelineRepo.created))
	}
}

func TestStartOrResume_ExistingPipelineWithIncompleteTasks_Resumes(t *testing.T) {
	existing := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := existing.Start(); err != nil {
		t.Fatalf("existing.Start: %v", err)
	}
	pendingTask := manufacturing.NewDeliverToConstructionTask(
		existing.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil,
	)

	pipelineRepo := &plannerStubPipelineRepo{existing: existing}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{
		existing.ID(): {pendingTask},
	}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "")
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !result.IsResumed {
		t.Error("expected IsResumed=true for a pipeline with incomplete tasks")
	}
	if result.Pipeline.ID() != existing.ID() {
		t.Error("expected the existing pipeline to be returned")
	}
	if got := result.Pipeline.TaskCount(); got != 1 {
		t.Errorf("expected resumed pipeline to report its real persisted task count (1), got %d", got)
	}
	if existing.IsTerminal() {
		t.Errorf("healthy pipeline must not be terminalized, got status %s", existing.Status())
	}
	if len(pipelineRepo.created) != 0 {
		t.Errorf("expected no new pipeline, got %d", len(pipelineRepo.created))
	}
	if len(taskRepo.createdBatches) != 0 {
		t.Errorf("expected no new tasks persisted, got %d batches", len(taskRepo.createdBatches))
	}
}

func TestStartOrResume_NewPipeline_PersistsAndStartsTasks(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "")
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if result.IsResumed {
		t.Error("expected IsResumed=false for a brand-new pipeline")
	}
	if got := result.Pipeline.TaskCount(); got != 2 {
		t.Errorf("expected 2 tasks, got %d", got)
	}
	if len(taskRepo.createdBatches) == 0 {
		t.Fatal("expected planned tasks to be persisted via CreateBatch")
	}
	if got := len(taskRepo.createdBatches[0]); got != 2 {
		t.Errorf("expected 2 persisted tasks, got %d", got)
	}
	// The pipeline must be EXECUTING with dependency-free tasks READY so the
	// running coordinator can pick them up without a daemon restart.
	if got := result.Pipeline.Status(); got != manufacturing.PipelineStatusExecuting {
		t.Errorf("expected new pipeline to be started (EXECUTING), got %s", got)
	}
	for _, task := range result.Pipeline.Tasks() {
		if task.Status() != manufacturing.TaskStatusReady {
			t.Errorf("expected dependency-free construction task %s to be READY, got %s", task.Good(), task.Status())
		}
	}
}
