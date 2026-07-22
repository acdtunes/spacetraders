package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-9f24o — the construction drain ran a good's ENTIRE supply chain as ONE monolithic supplyTask
// under a FLAT 30m timeout. A shallow buy-and-haul good (ADVANCED_CIRCUITRY) finished; a deep
// fabricate chain (FAB_MATS: buy inputs -> fabricate IRON -> feed factory -> buy FAB_MATS -> haul,
// all scarce + geographically spread) could NOT complete in 30m, so it abandoned before the first
// delivery and churned forever (0 deliveries, gate stuck). These tests pin the fix: the per-task
// timeout SCALES with the material's supply-chain DEPTH (deep chains get headroom, shallow goods stay
// byte-identical), and the cleanup persist on abandon survives the cancelled task context.

// newDrainPipelineDepth builds a construction pipeline with an explicit SupplyChainDepth so the
// depth-scaling can be exercised (the shared newDrainPipeline hardcodes depth 1 = shallow).
func newDrainPipelineDepth(t *testing.T, good string, targetQty, depth int) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, depth, 1)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(good, targetQty)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

// PART A — the pure scaling core: depth<=1 keeps the flat base (shallow byte-identical); a deeper
// chain gets base*depth, clamped to a ceiling so a genuine hang stays bounded.
func TestDepthScaledTimeout_ShallowKeepsBaseDeepScalesClamped(t *testing.T) {
	base := 30 * time.Minute
	ceiling := 2 * time.Hour

	if got := depthScaledTimeout(base, 1, ceiling); got != base {
		t.Fatalf("a shallow (depth 1) good must keep the flat base %s (byte-identical), got %s", base, got)
	}
	if got := depthScaledTimeout(base, 0, ceiling); got != base {
		t.Fatalf("a defensive depth 0 must keep the flat base %s, got %s", base, got)
	}
	if got := depthScaledTimeout(base, 3, ceiling); got != 90*time.Minute {
		t.Fatalf("a depth-3 chain must scale to base*3 = 90m, got %s", got)
	}
	if got := depthScaledTimeout(base, 10, ceiling); got != ceiling {
		t.Fatalf("base*depth past the ceiling must clamp to %s, got %s", ceiling, got)
	}
}

// PART A — staticSupplyChainDepth derives the material's fabrication depth from the STATIC recipe
// graph, cycle-guarded (the graph has cycles: IRON_ORE->EXPLOSIVES->LIQUID_*->MACHINERY->IRON->
// IRON_ORE) and bounded by the resolver's fabricate depth cap, so it matches the depth the resolver
// actually walks.
func TestStaticSupplyChainDepth_FabMatsDeep_LeafShallow_CycleSafe(t *testing.T) {
	// FAB_MATS = {IRON, QUARTZ_SAND}; both chains reach the cap -> depth == cap for cap<=structural.
	if got := staticSupplyChainDepth("FAB_MATS", 3); got != 3 {
		t.Fatalf("FAB_MATS at cap 3 must be depth 3, got %d", got)
	}
	// The cap is honored: a shallower cap yields a shallower depth (a depth-1 pipeline stays shallow).
	if got := staticSupplyChainDepth("FAB_MATS", 1); got != 1 {
		t.Fatalf("FAB_MATS at cap 1 must be depth 1 (cap honored), got %d", got)
	}
	// A good with no recipe inputs is a leaf -> depth 1 regardless of cap.
	if got := staticSupplyChainDepth("NOT_A_REAL_GOOD", 3); got != 1 {
		t.Fatalf("a good with no recipe inputs must be depth 1, got %d", got)
	}
	// Cycle safety: MACHINERY sits on the MACHINERY->IRON->...->MACHINERY cycle; the walk must
	// TERMINATE and stay within [1,cap] (proving the path-visited guard + hard cap bound recursion).
	if got := staticSupplyChainDepth("MACHINERY", 3); got < 1 || got > 3 {
		t.Fatalf("MACHINERY (on a recipe cycle) must terminate with depth in [1,3], got %d", got)
	}
}

// PART A — supplyTaskChainDepth: a BUY task (no factory) is buy+haul = depth 1 (shallow,
// byte-identical); a FABRICATE task takes the good's chain depth bounded by the pipeline's cap.
func TestSupplyTaskChainDepth_BuyShallow_FabricateDeep(t *testing.T) {
	depth3Pipeline := newDrainPipelineDepth(t, "FAB_MATS", 200, 3)
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{depth3Pipeline.ID(): depth3Pipeline}}
	handler := NewRunConstructionCoordinatorHandler(&drainStubTaskRepo{}, pipelineRepo, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	// BUY: FactorySymbol "" (planner resolved a market). Depth 1 regardless of the good's recipe.
	buyTask := manufacturing.NewDeliverToConstructionTask(depth3Pipeline.ID(), 1, "FAB_MATS", "X1-SRC", "", constructionSiteWP, nil)
	if got := handler.supplyTaskChainDepth(context.Background(), cmd, buyTask); got != 1 {
		t.Fatalf("a BUY task (no factory) must be shallow depth 1, got %d", got)
	}

	// FABRICATE: FactorySymbol set. Depth = FAB_MATS chain depth capped by the depth-3 pipeline = 3.
	fabTask := manufacturing.NewDeliverToConstructionTask(depth3Pipeline.ID(), 1, "FAB_MATS", "", "X1-FACTORY", constructionSiteWP, nil)
	if got := handler.supplyTaskChainDepth(context.Background(), cmd, fabTask); got != 3 {
		t.Fatalf("a FABRICATE FAB_MATS task on a depth-3 pipeline must be depth 3, got %d", got)
	}
}

// PART A (headline) — scaledSupplyTaskTimeout end-to-end: a deep fabricate chain resolves a LONGER
// deadline than a shallow buy-and-haul, while the shallow path stays at the flat 30m default. This is
// the sp-9f24o regression fence: today the resolver is flat 30m regardless of depth.
func TestScaledSupplyTaskTimeout_DeepFabricateGetsHeadroom_ShallowUnchanged(t *testing.T) {
	depth3Pipeline := newDrainPipelineDepth(t, "FAB_MATS", 200, 3)
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{depth3Pipeline.ID(): depth3Pipeline}}
	handler := NewRunConstructionCoordinatorHandler(&drainStubTaskRepo{}, pipelineRepo, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	cmd := newDrainCommand()

	buyTask := manufacturing.NewDeliverToConstructionTask(depth3Pipeline.ID(), 1, "FAB_MATS", "X1-SRC", "", constructionSiteWP, nil)
	shallow := handler.scaledSupplyTaskTimeout(context.Background(), cmd, buyTask)
	if shallow != constructionSupplyTaskDefaultTimeout {
		t.Fatalf("a shallow buy task must keep the flat 30m default, got %s", shallow)
	}

	fabTask := manufacturing.NewDeliverToConstructionTask(depth3Pipeline.ID(), 1, "FAB_MATS", "", "X1-FACTORY", constructionSiteWP, nil)
	deep := handler.scaledSupplyTaskTimeout(context.Background(), cmd, fabTask)
	if deep <= shallow {
		t.Fatalf("a deep fabricate chain must resolve a LONGER deadline than a shallow buy (%s), got %s", shallow, deep)
	}
	if deep != 90*time.Minute {
		t.Fatalf("a depth-3 fabricate chain at the 30m default must resolve base*3 = 90m, got %s", deep)
	}
}

// ctxAwareTaskRepo models the real GORM repo's ctx handling: an Update through a CANCELLED context
// FAILS (the 'context canceled' the abandon path logs); a live context records the status. This lets
// a test prove the cleanup persist survives an abandon only if it writes through a NON-cancelled ctx.
type ctxAwareTaskRepo struct {
	manufacturing.TaskRepository
	recorded map[string]manufacturing.TaskStatus
}

func (r *ctxAwareTaskRepo) Update(ctx context.Context, task *manufacturing.ManufacturingTask) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("failed to update task: %w", err)
	}
	if r.recorded == nil {
		r.recorded = make(map[string]manufacturing.TaskStatus)
	}
	r.recorded[task.ID()] = task.Status()
	return nil
}

func executingConstructionTask(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good string) *manufacturing.ManufacturingTask {
	t.Helper()
	task := readyConstructionTask(t, pipeline, good)
	if err := task.AssignShip("HAULER-ABANDON"); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	return task
}

// PART B — on abandon the supplyTask runs under a CANCELLED taskCtx; failTask then persisted the FAIL
// through that cancelled ctx and failed ('Could not persist failed construction task ...: context
// canceled'), leaving the task in limbo. The cleanup persist must use a DETACHED context so it
// survives the abandon.
func TestFailTask_PersistsThroughCancelledContext(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := executingConstructionTask(t, pipeline, "FAB_MATS")
	repo := &ctxAwareTaskRepo{}
	handler := NewRunConstructionCoordinatorHandler(repo, &drainStubPipelineRepo{}, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // the abandon signature: the task/tick ctx is already cancelled

	handler.failTask(cancelled, task, "sourcing exceeded the deadline")

	if repo.recorded[task.ID()] != manufacturing.TaskStatusFailed {
		t.Fatalf("failTask must persist FAILED even when the incoming ctx is cancelled (detached cleanup ctx), got %q", repo.recorded[task.ID()])
	}
}

// PART B — the sibling defer path: a timed-out unsourceable task must PARK (PENDING, deferred) via a
// detached context so the SupplyMonitor re-activates it, instead of failing to persist and forcing a
// blind restart.
func TestDeferTask_PersistsThroughCancelledContext(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := executingConstructionTask(t, pipeline, "FAB_MATS")
	repo := &ctxAwareTaskRepo{}
	handler := NewRunConstructionCoordinatorHandler(repo, &drainStubPipelineRepo{}, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	handler.deferTask(cancelled, task)

	if repo.recorded[task.ID()] != manufacturing.TaskStatusPending {
		t.Fatalf("deferTask must persist the parked PENDING status even when the incoming ctx is cancelled, got %q", repo.recorded[task.ID()])
	}
	if !task.IsDeferredConstruction() {
		t.Fatal("deferTask must leave the task in the deferred-construction signature")
	}
}

// PART B — persistCleanupCtx is byte-identical on the happy path: a LIVE ctx is returned unchanged
// (the write still runs on the request ctx), so only the abandon/cancel case diverges to a detached
// write.
func TestPersistCleanupCtx_LiveCtxUnchanged(t *testing.T) {
	ctx := context.Background()
	got, cancel := persistCleanupCtx(ctx)
	defer cancel()
	if got != ctx {
		t.Fatal("a live ctx must be returned unchanged (happy-path byte-identical)")
	}
}
