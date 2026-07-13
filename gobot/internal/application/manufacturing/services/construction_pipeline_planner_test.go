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
	updated         []*manufacturing.ManufacturingTask
}

func (r *plannerStubTaskRepo) FindByPipelineID(_ context.Context, pipelineID string) ([]*manufacturing.ManufacturingTask, error) {
	return r.tasksByPipeline[pipelineID], nil
}

func (r *plannerStubTaskRepo) CreateBatch(_ context.Context, tasks []*manufacturing.ManufacturingTask) error {
	r.createdBatches = append(r.createdBatches, tasks)
	return nil
}

func (r *plannerStubTaskRepo) Update(_ context.Context, task *manufacturing.ManufacturingTask) error {
	r.updated = append(r.updated, task)
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

func strptr(s string) *string { return &s }

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
		nil,
		nil,
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
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

// Reproduces the jump-gate blocker: the only exporter of ADVANCED_CIRCUITRY has
// MODERATE supply (X1-PZ28-D45 scenario). Planning must create the buy-and-deliver
// task from that market instead of aborting with "no market with good supply".
func TestStartOrResume_ModerateSupplyOnlyExporter_CreatesDeliverTask(t *testing.T) {
	const moderateMarket = "X1-PZ28-D45"

	abundant := "ABUNDANT"
	moderate := "MODERATE"
	strong := "STRONG"
	restricted := "RESTRICTED"

	fabMats, err := market.NewTradeGood("FAB_MATS", &abundant, &strong, 520, 500, 40, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(FAB_MATS): %v", err)
	}
	fabMatsMarket, err := market.NewMarket(plannerTestMarket, []market.TradeGood{*fabMats}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", plannerTestMarket, err)
	}

	circuitry, err := market.NewTradeGood("ADVANCED_CIRCUITRY", &moderate, &restricted, 1893, 1800, 20, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(ADVANCED_CIRCUITRY): %v", err)
	}
	circuitryMarket, err := market.NewMarket(moderateMarket, []market.TradeGood{*circuitry}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", moderateMarket, err)
	}

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket, moderateMarket},
		markets: map[string]*market.Market{
			plannerTestMarket: fabMatsMarket,
			moderateMarket:    circuitryMarket,
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must tolerate MODERATE supply for construction buys: %v", err)
	}

	if got := result.Pipeline.TaskCount(); got != 2 {
		t.Fatalf("expected 2 DELIVER_TO_CONSTRUCTION tasks (one per material), got %d", got)
	}
	circuitrySourced := false
	for _, task := range result.Pipeline.Tasks() {
		if task.Good() == "ADVANCED_CIRCUITRY" {
			circuitrySourced = true
			if task.SourceMarket() != moderateMarket {
				t.Errorf("expected ADVANCED_CIRCUITRY sourced from %s, got %s", moderateMarket, task.SourceMarket())
			}
		}
	}
	if !circuitrySourced {
		t.Error("expected a DELIVER_TO_CONSTRUCTION task for ADVANCED_CIRCUITRY")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
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
	if len(result.DeferredMaterials) != 0 {
		t.Errorf("expected no deferred materials when the resumed pipeline has no deferred task, got %v", result.DeferredMaterials)
	}
}

// sp-j2hq: StartOrResume's resume branch must persist an updated --min-supply
// floor onto the EXISTING pipeline, not just consume it once during the
// initial planning pass (sp-ezz9 only wired the new-pipeline path). Without
// this, a floor supplied on a later `construction start` call against an
// already-EXECUTING pipeline is silently dropped, so the deferred-material
// recovery poll-loop (task_activator.go) can never observe it.
func TestStartOrResume_ResumeWithNewMinSupply_PersistsFloorOnExistingPipeline(t *testing.T) {
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "SCARCE", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !result.IsResumed {
		t.Fatal("expected IsResumed=true for a pipeline with incomplete tasks")
	}
	if got := result.Pipeline.MinSupply(); got != "SCARCE" {
		t.Errorf("expected resumed pipeline's MinSupply floor to be updated to SCARCE, got %q", got)
	}
	foundUpdate := false
	for _, p := range pipelineRepo.updated {
		if p.ID() == existing.ID() && p.MinSupply() == "SCARCE" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Error("expected the updated MinSupply floor to be persisted via pipelineRepo.Update so recovery can see it")
	}
}

// Companion regression test: resuming WITHOUT specifying --min-supply (the
// CLI flag unset threads through as "") must NOT wipe out a floor that was
// set earlier - the resumed pipeline keeps sourcing at its original floor.
func TestStartOrResume_ResumeWithEmptyMinSupply_DoesNotClobberExistingFloor(t *testing.T) {
	existing := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	existing.SetMinSupply("SCARCE")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !result.IsResumed {
		t.Fatal("expected IsResumed=true for a pipeline with incomplete tasks")
	}
	if got := result.Pipeline.MinSupply(); got != "SCARCE" {
		t.Errorf("expected resuming with an empty minSupply to leave the existing SCARCE floor untouched, got %q", got)
	}
}

// sp-j2hq: a brand-new pipeline must also persist its caller-set --min-supply
// floor onto the entity (not just use it transiently while sourcing the
// initial materials) - otherwise a material that defers during THIS SAME
// initial planning pass would recover later at the wrong (default MODERATE)
// floor, because the deferred-material poll-loop reads the floor back off
// the persisted pipeline, not off this call's local minSupply argument.
func TestStartOrResume_NewPipeline_PersistsMinSupplyFloorForLaterRecovery(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket, circuitryScarce},
		markets: map[string]*market.Market{
			plannerTestMarket: newTradeTypeMarket(t, plannerTestMarket, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
			circuitryScarce:   newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "SCARCE", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if got := result.Pipeline.MinSupply(); got != "SCARCE" {
		t.Errorf("expected the new pipeline's MinSupply to be set to SCARCE, got %q", got)
	}
	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected exactly 1 pipeline persisted, got %d", len(pipelineRepo.created))
	}
	if got := pipelineRepo.created[0].MinSupply(); got != "SCARCE" {
		t.Errorf("expected the persisted pipeline row to carry MinSupply=SCARCE so later recovery can read it, got %q", got)
	}
}

// singleMaterialSite builds a construction site with one unfulfilled material.
func singleMaterialSite(good string, quantity int) *manufacturing.ConstructionSite {
	return manufacturing.NewConstructionSite(plannerTestSite, "JUMP_GATE", []manufacturing.ConstructionMaterial{
		manufacturing.NewConstructionMaterial(good, quantity, 0),
	}, false)
}

// findTaskByGood returns the first task in the pipeline for the given good.
func findTaskByGood(pipeline *manufacturing.ManufacturingPipeline, good string) *manufacturing.ManufacturingTask {
	for _, task := range pipeline.Tasks() {
		if task.Good() == good {
			return task
		}
	}
	return nil
}

// Field case (sp-r900): a construction bill where one material is ABUNDANT and
// sourceable (FAB_MATS) and another has no acceptable buy source
// (ADVANCED_CIRCUITRY, only a LIMITED exporter, no import stock). The pipeline
// must NOT fail all-or-nothing: it saves with a sourceable FAB_MATS task and a
// DEFERRED circuitry task that will recover when supply regenerates.
func TestStartOrResume_MixedSourceableAndUnsourceable_SavesWithDeferral(t *testing.T) {
	const circuitryLimited = "X1-PZ28-D40"

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket, circuitryLimited},
		markets: map[string]*market.Market{
			plannerTestMarket: newTradeTypeMarket(t, plannerTestMarket, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
			circuitryLimited:  newTradeTypeMarket(t, circuitryLimited, "ADVANCED_CIRCUITRY", "LIMITED", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must not fail when one material is unsourceable: %v", err)
	}

	if got := result.Pipeline.TaskCount(); got != 2 {
		t.Fatalf("expected 2 tasks (1 sourceable + 1 deferred), got %d", got)
	}

	fabTask := findTaskByGood(result.Pipeline, "FAB_MATS")
	if fabTask == nil {
		t.Fatal("expected a FAB_MATS task")
	}
	if fabTask.SourceMarket() != plannerTestMarket {
		t.Errorf("expected FAB_MATS sourced from %s, got %s", plannerTestMarket, fabTask.SourceMarket())
	}
	if fabTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected sourceable FAB_MATS task READY, got %s", fabTask.Status())
	}

	circTask := findTaskByGood(result.Pipeline, "ADVANCED_CIRCUITRY")
	if circTask == nil {
		t.Fatal("expected a deferred ADVANCED_CIRCUITRY task (must be visible, not dropped)")
	}
	if circTask.Status() != manufacturing.TaskStatusPending {
		t.Errorf("expected deferred circuitry task PENDING, got %s", circTask.Status())
	}
	if circTask.SourceMarket() != "" || circTask.FactorySymbol() != "" {
		t.Errorf("expected deferred circuitry task to have no source yet, got source=%q factory=%q", circTask.SourceMarket(), circTask.FactorySymbol())
	}
	if !circTask.IsDeferredConstruction() {
		t.Error("expected circuitry task to report IsDeferredConstruction()")
	}

	// The pipeline (with its sourceable task) must still be persisted and dispatched.
	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected pipeline to be saved, got %d created", len(pipelineRepo.created))
	}
	if len(taskRepo.createdBatches) == 0 || len(taskRepo.createdBatches[0]) != 2 {
		t.Fatalf("expected both tasks persisted via CreateBatch")
	}
	if result.Pipeline.Status() != manufacturing.PipelineStatusExecuting {
		t.Errorf("expected pipeline EXECUTING so the sourceable leg runs, got %s", result.Pipeline.Status())
	}
}

// sp-560b/sp-ooba: the caller (daemon gRPC + CLI) needs the unsourceable
// material's NAME surfaced on the result, not just a task-level signal buried
// in persisted state that never reaches `construction start` output. Same
// setup as TestStartOrResume_MixedSourceableAndUnsourceable_SavesWithDeferral,
// asserting on the new StartOrResumeResult.DeferredMaterials field.
func TestStartOrResume_MixedSourceableAndUnsourceable_ReportsDeferredMaterialByName(t *testing.T) {
	const circuitryLimited = "X1-PZ28-D40"

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket, circuitryLimited},
		markets: map[string]*market.Market{
			plannerTestMarket: newTradeTypeMarket(t, plannerTestMarket, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
			circuitryLimited:  newTradeTypeMarket(t, circuitryLimited, "ADVANCED_CIRCUITRY", "LIMITED", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must not fail when one material is unsourceable: %v", err)
	}

	if len(result.DeferredMaterials) != 1 || result.DeferredMaterials[0] != "ADVANCED_CIRCUITRY" {
		t.Errorf(`expected DeferredMaterials to name the unsourceable material ["ADVANCED_CIRCUITRY"], got %v`, result.DeferredMaterials)
	}
}

// A fully-sourceable plan must report zero deferred materials - the new field
// must not regress the happy path (sp-ooba: partial planning must be a
// no-op change when nothing is actually unsourceable).
func TestStartOrResume_FullySourceable_ReportsNoDeferredMaterials(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if len(result.DeferredMaterials) != 0 {
		t.Errorf("expected no deferred materials for a fully-sourceable plan, got %v", result.DeferredMaterials)
	}
}

// A fully-unsourceable plan must report EVERY material by name, not a single
// generic message - the operator needs to know exactly what to go source
// manually (sp-560b). The plan must still succeed and start (sp-ooba: never
// a hard abort), just with zero READY tasks until supply regenerates.
func TestStartOrResume_FullyUnsourceable_ReportsAllMaterialsByName(t *testing.T) {
	marketRepo := &plannerStubMarketRepo{}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must not fail even when every material is unsourceable: %v", err)
	}

	// Order matches how the construction site's materials were loaded
	// (FAB_MATS, ADVANCED_CIRCUITRY - see newPlannerTestConstructionSite),
	// per the DeferredMaterials doc comment's ordering guarantee.
	wantDeferred := []string{"FAB_MATS", "ADVANCED_CIRCUITRY"}
	if len(result.DeferredMaterials) != len(wantDeferred) {
		t.Fatalf("expected both materials reported as deferred, got %v", result.DeferredMaterials)
	}
	for i, want := range wantDeferred {
		if result.DeferredMaterials[i] != want {
			t.Errorf("expected DeferredMaterials[%d]=%s, got %v", i, want, result.DeferredMaterials)
		}
	}

	// Back up "zero READY tasks until supply regenerates": both materials'
	// tasks must actually be PENDING/deferred, not just named on the result.
	for _, good := range wantDeferred {
		task := findTaskByGood(result.Pipeline, good)
		if task == nil {
			t.Fatalf("expected a %s task", good)
		}
		if task.Status() != manufacturing.TaskStatusPending {
			t.Errorf("expected %s task PENDING (no source found), got %s", good, task.Status())
		}
		if !task.IsDeferredConstruction() {
			t.Errorf("expected %s task to report IsDeferredConstruction()", good)
		}
	}

	if result.Pipeline.Status() != manufacturing.PipelineStatusExecuting {
		t.Errorf("expected an all-deferred pipeline to still start (EXECUTING) so tasks recover when supply regenerates, got %s", result.Pipeline.Status())
	}
}

// The RESUME branch must also report deferred materials by name (sp-560b) -
// not just the new-pipeline branch. A resumed pipeline's deferred tasks come
// from persisted rows (e.g. after a daemon restart), so the planner must scan
// them via IsDeferredConstruction()/Good() rather than relying on the local
// slice that only exists during initial planning.
func TestStartOrResume_ResumeWithDeferredTask_ReportsDeferredMaterialByName(t *testing.T) {
	existing := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := existing.Start(); err != nil {
		t.Fatalf("existing.Start: %v", err)
	}
	readyTask := manufacturing.NewDeliverToConstructionTask(
		existing.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil,
	)
	deferredTask := manufacturing.NewDeliverToConstructionTask(
		existing.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil,
	)

	pipelineRepo := &plannerStubPipelineRepo{existing: existing}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{
		existing.ID(): {readyTask, deferredTask},
	}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !result.IsResumed {
		t.Fatal("expected IsResumed=true for a pipeline with incomplete tasks")
	}
	if len(result.DeferredMaterials) != 1 || result.DeferredMaterials[0] != "ADVANCED_CIRCUITRY" {
		t.Errorf(`expected resumed pipeline to report its persisted deferred task by name ["ADVANCED_CIRCUITRY"], got %v`, result.DeferredMaterials)
	}
}

// sp-ezz9: proves --min-supply threads all the way from StartOrResume down to
// the locator, not just that FindConstructionSource itself honors a floor in
// isolation (see market_locator_test.go). Same shape as
// TestStartOrResume_MixedSourceableAndUnsourceable_SavesWithDeferral above,
// but the circuitry market is SCARCE (below even that test's LIMITED, and
// below the default MODERATE floor) and StartOrResume is called with
// minSupply="SCARCE". Without the flag this market would defer exactly like
// the LIMITED case above; with the flag it must be sourced and READY instead.
func TestStartOrResume_MinSupplyFloor_ScarceExportSourcedInsteadOfDeferred(t *testing.T) {
	const circuitryScarce = "X1-PZ28-D40"

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket, circuitryScarce},
		markets: map[string]*market.Market{
			plannerTestMarket: newTradeTypeMarket(t, plannerTestMarket, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
			circuitryScarce:   newTradeTypeMarket(t, circuitryScarce, "ADVANCED_CIRCUITRY", "SCARCE", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "SCARCE", nil)
	if err != nil {
		t.Fatalf("StartOrResume must accept a SCARCE export source when minSupply=SCARCE: %v", err)
	}

	circTask := findTaskByGood(result.Pipeline, "ADVANCED_CIRCUITRY")
	if circTask == nil {
		t.Fatal("expected an ADVANCED_CIRCUITRY task")
	}
	if circTask.SourceMarket() != circuitryScarce {
		t.Errorf("expected ADVANCED_CIRCUITRY sourced from the SCARCE exporter %s, got %q", circuitryScarce, circTask.SourceMarket())
	}
	if circTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected ADVANCED_CIRCUITRY task READY (not deferred) with minSupply=SCARCE, got %s", circTask.Status())
	}
	if circTask.IsDeferredConstruction() {
		t.Error("expected ADVANCED_CIRCUITRY task to NOT report IsDeferredConstruction() when the SCARCE floor accepts it")
	}
}

// Per-material depth ceiling (sp-r900): at --depth 2 a material that is trivially
// BUYABLE (FAB_MATS ABUNDANT) must be bought directly, NOT fabricated. The old
// global switch fabricated FAB_MATS at depth 2 and died on its QUARTZ_SAND input.
func TestStartOrResume_BuyableMaterialBoughtEvenAtDepth2(t *testing.T) {
	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{plannerTestMarket},
		markets: map[string]*market.Market{
			plannerTestMarket: newTradeTypeMarket(t, plannerTestMarket, "FAB_MATS", "ABUNDANT", "STRONG", market.TradeTypeExport, 100),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, singleMaterialSite("FAB_MATS", 1600))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 2, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume at depth 2 must buy a buyable material: %v", err)
	}

	if got := result.Pipeline.TaskCount(); got != 1 {
		t.Fatalf("expected exactly 1 buy task (no fabrication), got %d", got)
	}
	task := result.Pipeline.Tasks()[0]
	if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Errorf("expected a direct DELIVER_TO_CONSTRUCTION buy, got %s", task.TaskType())
	}
	if task.SourceMarket() != plannerTestMarket {
		t.Errorf("expected FAB_MATS bought from %s, got %s", plannerTestMarket, task.SourceMarket())
	}
	for _, tk := range result.Pipeline.Tasks() {
		if tk.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			t.Errorf("buyable material must not be fabricated - unexpected ACQUIRE_DELIVER task for %s", tk.Good())
		}
	}
}

// Per-material depth ceiling (sp-r900): a material that is NOT buyable but IS
// fabricable from sourceable inputs must be fabricated within the depth ceiling.
// MACHINERY (not sold at MODERATE+) is fabricated from IRON (ABUNDANT) at depth 2.
//
// sp-qmp8: fabrication is now staged as a SINGLE dependency-free DELIVER_TO_CONSTRUCTION
// task carrying the factory — NO separate ACQUIRE_DELIVER input legs. The construction drain
// drives ProduceGood(Fabricate), which buys the inputs, feeds the factory, and harvests the
// output itself (one engine). The planner still DECIDES to fabricate (it verifies the factory
// exists and every input is sourceable) but does not decompose that into orphan-able legs. The
// dependency-free task must be READY immediately so the drain can pick it up.
func TestStartOrResume_FabricableOnlyMaterialFabricatedWithinCeiling(t *testing.T) {
	const factoryWp = "X1-PZ28-FAC"
	const ironWp = "X1-PZ28-IRN"

	// Factory: EXPORTS MACHINERY (LIMITED -> not buyable) and IMPORTS IRON.
	machineryExport, err := market.NewTradeGood("MACHINERY", strptr("LIMITED"), strptr("RESTRICTED"), 110, 100, 40, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(MACHINERY): %v", err)
	}
	ironImport, err := market.NewTradeGood("IRON", strptr("MODERATE"), strptr("WEAK"), 60, 50, 40, market.TradeTypeImport)
	if err != nil {
		t.Fatalf("NewTradeGood(IRON import): %v", err)
	}
	factoryMarket, err := market.NewMarket(factoryWp, []market.TradeGood{*machineryExport, *ironImport}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(factory): %v", err)
	}

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{factoryWp, ironWp},
		markets: map[string]*market.Market{
			factoryWp: factoryMarket,
			ironWp:    newTradeTypeMarket(t, ironWp, "IRON", "ABUNDANT", "WEAK", market.TradeTypeExport, 45),
		},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, singleMaterialSite("MACHINERY", 50))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 2, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must fabricate a fabricable-only material: %v", err)
	}

	// Exactly one task: the fabricate delivery. No ACQUIRE_DELIVER legs are staged — the drain
	// sources inputs itself via ProduceGood(Fabricate).
	if got := result.Pipeline.TaskCount(); got != 1 {
		t.Fatalf("expected exactly 1 DELIVER_TO_CONSTRUCTION task (no input legs), got %d", got)
	}
	for _, tk := range result.Pipeline.Tasks() {
		if tk.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			t.Fatalf("fabrication must NOT stage separate ACQUIRE_DELIVER legs anymore (sp-qmp8), got one for %s", tk.Good())
		}
	}

	machineryDeliver := findTaskByGood(result.Pipeline, "MACHINERY")
	if machineryDeliver == nil || machineryDeliver.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Fatalf("expected a DELIVER_TO_CONSTRUCTION task for MACHINERY, got %+v", machineryDeliver)
	}
	if machineryDeliver.FactorySymbol() != factoryWp {
		t.Errorf("expected MACHINERY fabricated at factory %s, got %s", factoryWp, machineryDeliver.FactorySymbol())
	}
	if machineryDeliver.SourceMarket() != "" {
		t.Errorf("a fabricate task must carry no source market (it is produced, not bought), got %q", machineryDeliver.SourceMarket())
	}
	if machineryDeliver.IsDeferredConstruction() {
		t.Error("fabricable material must not be deferred - it was fabricated within the ceiling")
	}
	// No input-leg dependencies, so the pipeline's Start() marked it READY and the drain can
	// execute it immediately (the orphaned-legs regression is gone).
	if len(machineryDeliver.DependsOn()) != 0 {
		t.Errorf("the fabricate task must have no dependencies (ProduceGood sources inputs), got %v", machineryDeliver.DependsOn())
	}
	if machineryDeliver.Status() != manufacturing.TaskStatusReady {
		t.Errorf("a dependency-free fabricate task must be READY after Start(), got %s", machineryDeliver.Status())
	}
}

// sp-qmp8: a fabricable-only material whose immediate input has NO market must DEFER (the whole
// material becomes a deferred PENDING task), not stage a fabricate task the drain could never
// feed. This is the sourceability gate the old per-input leg staging enforced, preserved after
// the switch to single-task fabrication.
func TestStartOrResume_FabricableMaterialDefersWhenInputUnsourceable(t *testing.T) {
	const factoryWp = "X1-PZ28-FAC"

	// Factory EXPORTS MACHINERY (LIMITED -> not buyable) and IMPORTS IRON, but NO market in the
	// system exports IRON, so the factory can never be fed.
	machineryExport, err := market.NewTradeGood("MACHINERY", strptr("LIMITED"), strptr("RESTRICTED"), 110, 100, 40, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(MACHINERY): %v", err)
	}
	ironImport, err := market.NewTradeGood("IRON", strptr("MODERATE"), strptr("WEAK"), 60, 50, 40, market.TradeTypeImport)
	if err != nil {
		t.Fatalf("NewTradeGood(IRON import): %v", err)
	}
	factoryMarket, err := market.NewMarket(factoryWp, []market.TradeGood{*machineryExport, *ironImport}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(factory): %v", err)
	}

	marketRepo := &plannerStubMarketRepo{
		marketWaypoints: []string{factoryWp}, // no IRON exporter anywhere
		markets:         map[string]*market.Market{factoryWp: factoryMarket},
	}

	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, marketRepo, singleMaterialSite("MACHINERY", 50))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 2, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume must defer (not error) an unfeedable fabrication: %v", err)
	}

	machineryDeliver := findTaskByGood(result.Pipeline, "MACHINERY")
	if machineryDeliver == nil {
		t.Fatal("expected a deferred MACHINERY task to still be staged")
	}
	if !machineryDeliver.IsDeferredConstruction() {
		t.Errorf("expected MACHINERY DEFERRED (input unsourceable), got source=%q factory=%q",
			machineryDeliver.SourceMarket(), machineryDeliver.FactorySymbol())
	}
	if len(result.DeferredMaterials) != 1 || result.DeferredMaterials[0] != "MACHINERY" {
		t.Errorf("expected MACHINERY reported as deferred, got %v", result.DeferredMaterials)
	}
}

func TestStartOrResume_NewPipeline_PersistsAndStartsTasks(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
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
