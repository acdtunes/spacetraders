package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-yexq — unified gate-fill defaults the construction pipeline's ADMISSION floor to SCARCE.
//
// The gap: unified_gate_fill lowers the per-node PRODUCTION sourcing floor to SCARCE (lane B), but
// the pipeline's ADMISSION/activation floor (the --min-supply knob, default MODERATE) was NOT wired
// to the toggle — so a gate material whose only source is SCARCE (e.g. ADVANCED_CIRCUITRY@D42) was
// rejected at admission ("Deferred unsourceable") and never reached the margin-blind production
// floor, with the operator forced to type `--min-supply SCARCE` by hand.
//
// These drive the planner (StartOrResume, the driving port) over a site with a SCARCE-only
// ADVANCED_CIRCUITRY export at DEPTH 3 (buy-final-only, so the admission floor is the SOLE gate — no
// fabrication path to muddy the signal), asserting the observable outcome at the driven-port
// (market-repo) boundary: the SCARCE material's task is READY+sourced (admitted) vs PENDING+deferred,
// plus the floor the pipeline persists (which the deferred-material recovery loop reads back).

// AC1 (the fix): with unified gate-fill ON and NO manual --min-supply, the SCARCE export is ADMITTED
// (sourced + READY, not deferred), and the pipeline persists a SCARCE floor so the activation /
// recovery loop (task_activator.pipelineMinSupply) reads SCARCE too — promotion with no manual flag.
func TestStartOrResume_UnifiedGateFill_AdmitsScarceExportWithoutMinSupply(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))
	planner.SetUnifiedGateFill(true)

	// depth 3 = buy-final-only: an unbuyable material can only DEFER, so admission is the sole gate.
	// minSupply="" (flag not passed): the toggle must supply the SCARCE admission default.
	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if contains(result.DeferredMaterials, "ADVANCED_CIRCUITRY") {
		t.Fatalf("unified gate-fill must ADMIT the SCARCE export without a manual --min-supply, but it deferred: %v", result.DeferredMaterials)
	}
	circTask := findTaskByGood(result.Pipeline, "ADVANCED_CIRCUITRY")
	if circTask == nil {
		t.Fatal("expected an ADVANCED_CIRCUITRY task")
	}
	if circTask.SourceMarket() != ovCircWP {
		t.Errorf("expected ADVANCED_CIRCUITRY sourced from the SCARCE exporter %s, got %q", ovCircWP, circTask.SourceMarket())
	}
	if circTask.Status() != manufacturing.TaskStatusReady {
		t.Errorf("expected ADVANCED_CIRCUITRY READY (admitted), got %s", circTask.Status())
	}
	if circTask.IsDeferredConstruction() {
		t.Error("expected ADVANCED_CIRCUITRY NOT deferred under unified gate-fill")
	}
	// The persisted floor must be SCARCE: a material that DOES defer (dry market) later recovers via
	// task_activator, which reads the floor back off THIS persisted pipeline row — so persisting
	// SCARCE is what makes recovery promote it with no manual flag.
	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected exactly 1 pipeline persisted, got %d", len(pipelineRepo.created))
	}
	if got := pipelineRepo.created[0].MinSupply(); got != "SCARCE" {
		t.Errorf("expected unified gate-fill to persist a SCARCE admission floor for recovery, got %q", got)
	}
}

// AC2 (byte-identical OFF): the SAME scenario with the toggle OFF must DEFER the SCARCE export at the
// MODERATE default and persist NO floor — proving the fix is dark when unified_gate_fill is off.
func TestStartOrResume_UnifiedGateFillOff_DefersScarceExport(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))
	planner.SetUnifiedGateFill(false)

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !contains(result.DeferredMaterials, "ADVANCED_CIRCUITRY") {
		t.Fatalf("with unified gate-fill OFF the SCARCE export must DEFER at the MODERATE floor, got deferred=%v", result.DeferredMaterials)
	}
	if len(pipelineRepo.created) != 1 {
		t.Fatalf("expected exactly 1 pipeline persisted, got %d", len(pipelineRepo.created))
	}
	if got := pipelineRepo.created[0].MinSupply(); got != "" {
		t.Errorf("OFF must persist no floor (empty → MODERATE default downstream), got %q", got)
	}
}

// AC3 (explicit override wins): an explicit --min-supply=MODERATE must beat the toggle's SCARCE
// default — the operator can still FORCE a stricter floor even under unified gate-fill.
func TestStartOrResume_ExplicitMinSupplyBeatsUnifiedGateFillDefault(t *testing.T) {
	pipelineRepo := &plannerStubPipelineRepo{}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))
	planner.SetUnifiedGateFill(true)

	// Explicit MODERATE floor: even though the toggle would default SCARCE, the operator's MODERATE wins.
	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "MODERATE", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}

	if !contains(result.DeferredMaterials, "ADVANCED_CIRCUITRY") {
		t.Fatalf("an explicit --min-supply=MODERATE must override the toggle's SCARCE default and DEFER the SCARCE export, got deferred=%v", result.DeferredMaterials)
	}
	if got := pipelineRepo.created[0].MinSupply(); got != "MODERATE" {
		t.Errorf("explicit MODERATE floor must be persisted verbatim (not overwritten by the toggle), got %q", got)
	}
}

// Resume self-heal (the verified live scenario): an ALREADY-RUNNING gate pipeline created before this
// fix carries an empty (MODERATE-default) floor and its SCARCE material sits deferred. Re-running
// `construction start` (no --min-supply) under unified gate-fill must UPGRADE that empty floor to
// SCARCE and persist it, so the deferred-material recovery loop promotes the material with no manual
// flag — turning the manual `--min-supply SCARCE` workaround into the automatic default.
func TestStartOrResume_UnifiedGateFillResume_UpgradesEmptyFloorToScarce(t *testing.T) {
	existing := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	if err := existing.Start(); err != nil {
		t.Fatalf("existing.Start: %v", err)
	}
	// A persisted DEFERRED ADVANCED_CIRCUITRY task (no source) — the stuck live state; floor is empty.
	deferred := manufacturing.NewDeliverToConstructionTask(existing.ID(), 1, "ADVANCED_CIRCUITRY", "", "", plannerTestSite, nil)

	pipelineRepo := &plannerStubPipelineRepo{existing: existing}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{existing.ID(): {deferred}}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, overrideConstructionMarketRepo(t), newPlannerTestConstructionSite(t))
	planner.SetUnifiedGateFill(true)

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if !result.IsResumed {
		t.Fatal("expected IsResumed=true for a pipeline with incomplete tasks")
	}
	if got := result.Pipeline.MinSupply(); got != "SCARCE" {
		t.Errorf("unified gate-fill resume must upgrade an empty admission floor to SCARCE, got %q", got)
	}
	// Persisted via Update so the recovery loop reads SCARCE off the pipeline row.
	foundUpdate := false
	for _, p := range pipelineRepo.updated {
		if p.ID() == existing.ID() && p.MinSupply() == "SCARCE" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Error("expected the upgraded SCARCE floor to be persisted via pipelineRepo.Update for recovery")
	}
}

// Resume don't-clobber (explicit override preserved on resume): a resumed pipeline that already
// carries an EXPLICIT MODERATE floor must keep it under the toggle — the self-heal only FILLS an
// empty floor, it never overwrites an operator's explicit choice (explicit override wins, on resume
// too).
func TestStartOrResume_UnifiedGateFillResume_PreservesExplicitFloor(t *testing.T) {
	existing := manufacturing.NewConstructionPipeline(plannerTestSite, 1, 3, 5)
	existing.SetMinSupply("MODERATE")
	if err := existing.Start(); err != nil {
		t.Fatalf("existing.Start: %v", err)
	}
	pending := manufacturing.NewDeliverToConstructionTask(existing.ID(), 1, "FAB_MATS", plannerTestMarket, "", plannerTestSite, nil)

	pipelineRepo := &plannerStubPipelineRepo{existing: existing}
	taskRepo := &plannerStubTaskRepo{tasksByPipeline: map[string][]*manufacturing.ManufacturingTask{existing.ID(): {pending}}}
	planner := newPlannerUnderTest(pipelineRepo, taskRepo, newPlannerTestMarketRepo(t), newPlannerTestConstructionSite(t))
	planner.SetUnifiedGateFill(true)

	result, err := planner.StartOrResume(context.Background(), 1, plannerTestSite, 3, 5, "", "", nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if got := result.Pipeline.MinSupply(); got != "MODERATE" {
		t.Errorf("unified gate-fill must NOT clobber an explicit MODERATE floor on resume, got %q", got)
	}
}
