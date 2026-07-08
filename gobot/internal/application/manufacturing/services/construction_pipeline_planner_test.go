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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "SCARCE")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 2, 5, "", "")
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

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 2, 5, "", "")
	if err != nil {
		t.Fatalf("StartOrResume must fabricate a fabricable-only material: %v", err)
	}

	ironAcquire := findTaskByGood(result.Pipeline, "IRON")
	if ironAcquire == nil || ironAcquire.TaskType() != manufacturing.TaskTypeAcquireDeliver {
		t.Fatalf("expected an ACQUIRE_DELIVER task for the IRON input, got %+v", ironAcquire)
	}
	if ironAcquire.SourceMarket() != ironWp || ironAcquire.FactorySymbol() != factoryWp {
		t.Errorf("expected IRON acquired from %s and delivered to factory %s, got source=%s factory=%s",
			ironWp, factoryWp, ironAcquire.SourceMarket(), ironAcquire.FactorySymbol())
	}

	machineryDeliver := findTaskByGood(result.Pipeline, "MACHINERY")
	if machineryDeliver == nil || machineryDeliver.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Fatalf("expected a DELIVER_TO_CONSTRUCTION task for MACHINERY, got %+v", machineryDeliver)
	}
	if machineryDeliver.FactorySymbol() != factoryWp {
		t.Errorf("expected MACHINERY collected from factory %s, got %s", factoryWp, machineryDeliver.FactorySymbol())
	}
	if machineryDeliver.IsDeferredConstruction() {
		t.Error("fabricable material must not be deferred - it was fabricated within the ceiling")
	}
}

func TestStartOrResume_NewPipeline_PersistsAndStartsTasks(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "")
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
