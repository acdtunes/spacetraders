package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FabricationTreeResolver builds the scarcity-gated supply-chain dependency tree for a candidate
// fabrication material — the SAME engine (*SupplyChainResolver) the construction drain executes
// (sp-yfzi). planFabrication uses it as its per-input FEASIBILITY oracle: a material is
// feasible to fabricate iff the resolver can build a COMPLETE tree for it within the depth ceiling
// — every scarce intermediate that has a factory recurses, and every leaf resolves to a buyable
// market. It is an OPTIONAL collaborator wired by SetTreeResolver: left unset (nil), planFabrication
// falls back to the previous gate (every IMMEDIATE input must be buyable at MODERATE+ now).
// *SupplyChainResolver satisfies it via BuildDependencyTree.
type FabricationTreeResolver interface {
	BuildDependencyTree(ctx context.Context, targetGood, systemSymbol string, playerID int) (*goods.SupplyChainNode, error)
}

// ConstructionPipelinePlanner creates and manages construction pipelines.
// It handles:
//   - Idempotent creation (checks for existing pipeline)
//   - Auto-discovery of construction site requirements
//   - Task creation based on supply chain depth
//   - DELIVER_TO_CONSTRUCTION tasks for final delivery
type ConstructionPipelinePlanner struct {
	pipelineRepo     manufacturing.PipelineRepository
	taskRepo         manufacturing.TaskRepository
	constructionRepo manufacturing.ConstructionSiteRepository
	marketLocator    *MarketLocator
	shipRepo         navigation.ShipRepository
	clock            shared.Clock
	// treeResolver is the fabrication FEASIBILITY oracle — the SAME scarcity-gated
	// SupplyChainResolver the drain runs. Optional (wired by SetTreeResolver); nil falls
	// back to the before "every immediate input buyable at MODERATE+" gate.
	treeResolver FabricationTreeResolver
}

// NewConstructionPipelinePlanner creates a new construction pipeline planner.
// shipRepo and clock are only exercised by Stop() (to force-release ships an
// ASSIGNED task had claimed); callers that never call Stop() may pass nil for
// shipRepo. clock defaults to the real system clock when nil.
func NewConstructionPipelinePlanner(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	constructionRepo manufacturing.ConstructionSiteRepository,
	marketLocator *MarketLocator,
	shipRepo navigation.ShipRepository,
	clock shared.Clock,
) *ConstructionPipelinePlanner {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ConstructionPipelinePlanner{
		pipelineRepo:     pipelineRepo,
		taskRepo:         taskRepo,
		constructionRepo: constructionRepo,
		marketLocator:    marketLocator,
		shipRepo:         shipRepo,
		clock:            clock,
	}
}

// SetTreeResolver wires the scarcity-gated supply-chain resolver so planFabrication gates
// a material's feasibility on "SOURCEABLE within the depth ceiling" (buyable OR producible) — the
// SAME verdict the recursive drain reaches — rather than on "every immediate input buyable at
// MODERATE+ now", which defers a whole material when a deep input is scarce but producible.
// Optional — the daemon injects the shared SupplyChainResolver singleton; left unset the
// planner uses the previous fallback. A setter (not a constructor arg) keeps the existing planner
// constructor and its in-package tests unchanged (nil → fallback → byte-identical).
func (p *ConstructionPipelinePlanner) SetTreeResolver(resolver FabricationTreeResolver) {
	p.treeResolver = resolver
}

// admissionFloor resolves the pipeline's EXPORT admission floor (unified gate-fill). A
// pipeline with NO explicit operator floor (empty minSupply) defaults to SCARCE — margin-blind
// admission — so a gate material whose only source is SCARCE (e.g. ADVANCED_CIRCUITRY@D42) is
// admitted and promoted automatically, no manual --min-supply. An explicit floor (non-empty
// minSupply) always wins. This floor is persisted on the pipeline, so the deferred-material recovery
// loop (task_activator.pipelineMinSupply) reads the SAME SCARCE default — one source of truth for
// planning AND activation.
func (p *ConstructionPipelinePlanner) admissionFloor(minSupply string) string {
	if minSupply == "" {
		return string(manufacturing.SupplyLevelScarce)
	}
	return minSupply
}

// StartOrResumeResult contains the result of starting or resuming a pipeline.
type StartOrResumeResult struct {
	Pipeline  *manufacturing.ManufacturingPipeline
	IsResumed bool // true if resuming existing, false if newly created

	// DeferredMaterials names every material (trade symbol) that could not be
	// sourced this call, in the same order the pipeline's materials were
	// planned/loaded. Planning is never all-or-nothing: a deferred
	// material still gets a visible PENDING task that the SupplyMonitor
	// re-sources later, but the operator needs the name surfaced here rather
	// than a generic "no market" message to go source it manually.
	// Empty (nil) when every material was sourced.
	DeferredMaterials []string
}

// StartOrResume starts a new construction pipeline or resumes an existing one.
// This method is IDEMPOTENT - calling it multiple times with the same construction site
// will return the existing pipeline instead of creating a new one.
//
// Parameters:
//   - constructionSite: The waypoint symbol of the construction site (e.g., "X1-FB5-I61")
//   - supplyChainDepth: How deep to go in the supply chain (0=full, 1=raw only, 2=intermediates, 3=buy final)
//   - maxWorkers: Maximum parallel workers (0=unlimited, default 5)
//   - systemSymbol: System to search for markets (empty string = derive from constructionSite)
//   - minSupply: caller-set EXPORT sourcing floor, e.g.
//     "SCARCE". Empty string = "flag not passed this call" and never clobbers
//     an already-persisted floor; it does NOT mean "reset to MODERATE". The
//     floor is persisted on the pipeline (both for a new plan and when
//     resuming an existing one) so the deferred-material recovery poll-loop
//     (task_activator.go) can read it later, not just this initial pass.
func (p *ConstructionPipelinePlanner) StartOrResume(
	ctx context.Context,
	playerID int,
	constructionSite string,
	supplyChainDepth int,
	maxWorkers int,
	systemSymbol string,
	minSupply string,
	goodOverrides manufacturing.GoodGatingOverrides,
) (*StartOrResumeResult, error) {
	logger := common.LoggerFromContext(ctx)

	existingPipeline, err := p.pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing pipeline: %w", err)
	}

	if existingPipeline != nil {
		resumed, err := p.resumeExisting(ctx, existingPipeline, constructionSite, maxWorkers, minSupply, goodOverrides)
		if err != nil {
			return nil, err
		}
		if resumed != nil {
			return resumed, nil
		}
	}

	logger.Log("INFO", "Fetching construction site requirements", map[string]interface{}{
		"construction_site": constructionSite,
		"player_id":         playerID,
	})

	constructionSiteData, err := p.constructionRepo.FindByWaypoint(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch construction site data: %w", err)
	}

	if constructionSiteData.IsComplete() {
		return nil, fmt.Errorf("construction site %s is already complete", constructionSite)
	}

	unfulfilledMaterials := constructionSiteData.UnfulfilledMaterials()
	if len(unfulfilledMaterials) == 0 {
		return nil, fmt.Errorf("construction site %s has no remaining materials to deliver", constructionSite)
	}

	logger.Log("INFO", "Found construction materials to deliver", map[string]interface{}{
		"construction_site": constructionSite,
		"materials_count":   len(unfulfilledMaterials),
	})

	pipeline := manufacturing.NewConstructionPipeline(constructionSite, playerID, supplyChainDepth, maxWorkers)
	// Resolve the admission floor ONCE, and persist it (and the per-good overrides) on the entity
	// rather than only passing it to planMaterial: a material that defers during THIS pass is
	// recovered later by reading the floor back off the pipeline row (RULINGS #2).
	effectiveMinSupply := p.admissionFloor(minSupply)
	pipeline.SetMinSupply(effectiveMinSupply)
	pipeline.SetGoodOverrides(goodOverrides)

	if err := addMaterialTargets(ctx, pipeline, unfulfilledMaterials); err != nil {
		return nil, err
	}

	if systemSymbol == "" {
		systemSymbol = extractSystemSymbol(constructionSite)
	}

	plan := materialPlan{
		pipelineID:       pipeline.ID(),
		systemSymbol:     systemSymbol,
		constructionSite: constructionSite,
		supplyChainDepth: supplyChainDepth,
		playerID:         playerID,
		minSupply:        effectiveMinSupply,
		goodOverrides:    goodOverrides,
	}
	deferredMaterials, err := p.planMaterials(ctx, pipeline, unfulfilledMaterials, plan)
	if err != nil {
		return nil, err
	}

	// Start before persisting so dependency-free tasks are already READY and the running
	// coordinator picks them up without waiting for a daemon restart.
	if err := pipeline.Start(); err != nil {
		return nil, fmt.Errorf("failed to start pipeline: %w", err)
	}
	if err := p.persistPlannedPipeline(ctx, pipeline); err != nil {
		return nil, err
	}

	logger.Log("INFO", "Created new construction pipeline", map[string]interface{}{
		"pipeline_id":        pipeline.ID(),
		"construction_site":  constructionSite,
		"materials_count":    len(unfulfilledMaterials),
		"task_count":         pipeline.TaskCount(),
		"supply_chain_depth": supplyChainDepth,
	})

	return &StartOrResumeResult{
		Pipeline:          pipeline,
		IsResumed:         false,
		DeferredMaterials: deferredMaterials,
	}, nil
}

// resumeExisting adopts a pipeline that already exists for this construction site, or terminalizes
// it and returns nil so the caller re-plans from scratch.
//
// A pipeline ROW alone does not mean the pipeline is healthy: its tasks may have been reaped by
// daemon-restart recovery, leaving an EXECUTING pipeline that can never deliver, complete, or fail.
func (p *ConstructionPipelinePlanner) resumeExisting(
	ctx context.Context,
	existingPipeline *manufacturing.ManufacturingPipeline,
	constructionSite string,
	maxWorkers int,
	minSupply string,
	goodOverrides manufacturing.GoodGatingOverrides,
) (*StartOrResumeResult, error) {
	logger := common.LoggerFromContext(ctx)

	persistedTasks, err := p.taskRepo.FindByPipelineID(ctx, existingPipeline.ID())
	if err != nil {
		return nil, fmt.Errorf("failed to load tasks for existing pipeline %s: %w", existingPipeline.ID(), err)
	}

	if !hasIncompleteTask(persistedTasks) {
		// Terminalize so FindByConstructionSite stops returning it, then let the caller re-plan.
		if err := existingPipeline.Fail("re-planned: pipeline had no incomplete tasks"); err != nil {
			return nil, fmt.Errorf("failed to terminalize stale construction pipeline %s: %w", existingPipeline.ID(), err)
		}
		if err := p.pipelineRepo.Update(ctx, existingPipeline); err != nil {
			return nil, fmt.Errorf("failed to persist terminalized construction pipeline %s: %w", existingPipeline.ID(), err)
		}
		logger.Log("WARN", "Existing construction pipeline had no incomplete tasks - marked FAILED, re-planning", map[string]interface{}{
			"pipeline_id":       existingPipeline.ID(),
			"construction_site": constructionSite,
		})
		return nil, nil
	}

	existingPipeline.SetTasks(persistedTasks)
	if applyResumedSourcingConfig(existingPipeline, minSupply, maxWorkers, goodOverrides) {
		if err := p.pipelineRepo.Update(ctx, existingPipeline); err != nil {
			return nil, fmt.Errorf("failed to persist updated sourcing config for pipeline %s: %w", existingPipeline.ID(), err)
		}
	}

	// A resumed pipeline's deferred materials live only in its persisted tasks, so scan for them
	// here too: re-running `construction start` on an in-progress pipeline deserves the same
	// by-name visibility as a fresh plan.
	resumedDeferred := make([]string, 0)
	for _, task := range persistedTasks {
		if task.IsDeferredConstruction() {
			resumedDeferred = append(resumedDeferred, task.Good())
		}
	}

	logger.Log("INFO", "Resuming existing construction pipeline", map[string]interface{}{
		"pipeline_id":       existingPipeline.ID(),
		"construction_site": constructionSite,
		"status":            existingPipeline.Status(),
		"task_count":        existingPipeline.TaskCount(),
		"progress":          fmt.Sprintf("%.1f%%", existingPipeline.ConstructionProgress()),
	})
	return &StartOrResumeResult{
		Pipeline:          existingPipeline,
		IsResumed:         true,
		DeferredMaterials: resumedDeferred,
	}, nil
}

func hasIncompleteTask(tasks []*manufacturing.ManufacturingTask) bool {
	for _, task := range tasks {
		if !task.IsTerminal() {
			return true
		}
	}
	return false
}

// applyResumedSourcingConfig folds this launch's flags into an already-persisted pipeline and
// reports whether anything changed. An empty minSupply or override map means "the flag wasn't
// passed on this call" and must never clobber a value set earlier; 0 is maxWorkers' unset sentinel,
// so an idempotent re-run cannot overwrite a live-tuned cap.
func applyResumedSourcingConfig(
	pipeline *manufacturing.ManufacturingPipeline,
	minSupply string,
	maxWorkers int,
	goodOverrides manufacturing.GoodGatingOverrides,
) bool {
	needsUpdate := false
	if minSupply != "" && minSupply != pipeline.MinSupply() {
		pipeline.SetMinSupply(minSupply)
		needsUpdate = true
	}
	// A resume that STILL has no explicit floor is upgraded to the SCARCE admission default so the
	// deferred-material recovery loop promotes it automatically. Only FILLS an empty floor, so an
	// explicit operator floor still wins.
	if pipeline.MinSupply() == "" {
		pipeline.SetMinSupply(string(manufacturing.SupplyLevelScarce))
		needsUpdate = true
	}
	if len(goodOverrides) > 0 {
		pipeline.SetGoodOverrides(goodOverrides)
		needsUpdate = true
	}
	if maxWorkers > 0 && maxWorkers != pipeline.MaxWorkers() {
		pipeline.SetMaxWorkers(maxWorkers)
		needsUpdate = true
	}
	return needsUpdate
}

func addMaterialTargets(ctx context.Context, pipeline *manufacturing.ManufacturingPipeline, materials []manufacturing.ConstructionMaterial) error {
	logger := common.LoggerFromContext(ctx)
	for _, mat := range materials {
		remaining := mat.Remaining()
		materialTarget := manufacturing.NewConstructionMaterialTarget(mat.TradeSymbol(), remaining)
		if err := pipeline.AddMaterial(materialTarget); err != nil {
			return fmt.Errorf("failed to add material %s: %w", mat.TradeSymbol(), err)
		}

		logger.Log("INFO", "Added material target to pipeline", map[string]interface{}{
			"material":  mat.TradeSymbol(),
			"remaining": remaining,
		})
	}
	return nil
}

// materialPlan is the planning context every construction material is planned under: which
// pipeline and site it belongs to, how deep fabrication may go, and the supply gating in force.
type materialPlan struct {
	pipelineID       string
	systemSymbol     string
	constructionSite string
	supplyChainDepth int
	playerID         int
	minSupply        string
	goodOverrides    manufacturing.GoodGatingOverrides
}

// forGood applies the per-good override, loosening the floor for a single bottleneck while every
// other material keeps the pipeline's global floor.
func (m materialPlan) forGood(good string) materialPlan {
	m.minSupply = m.goodOverrides.MinSupplyFor(good, m.minSupply)
	return m
}

// planMaterials plans each material INDEPENDENTLY: one that cannot be sourced right now is
// DEFERRED (a visible PENDING task), never a failure of the whole plan. Returns those names.
func (p *ConstructionPipelinePlanner) planMaterials(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	materials []manufacturing.ConstructionMaterial,
	plan materialPlan,
) ([]string, error) {
	logger := common.LoggerFromContext(ctx)

	deferredMaterials := make([]string, 0)
	for _, mat := range materials {
		staged, deferred, err := p.planMaterial(ctx, mat.TradeSymbol(), plan.forGood(mat.TradeSymbol()))
		if err != nil {
			return nil, fmt.Errorf("failed to plan material %s: %w", mat.TradeSymbol(), err)
		}
		for _, task := range staged {
			if err := pipeline.AddTask(task); err != nil {
				return nil, fmt.Errorf("failed to add task for %s: %w", mat.TradeSymbol(), err)
			}
		}
		if deferred {
			deferredMaterials = append(deferredMaterials, mat.TradeSymbol())
			logger.Log("WARN", "Construction material deferred - no buy source yet, will recover when supply regenerates", map[string]interface{}{
				"material":          mat.TradeSymbol(),
				"construction_site": plan.constructionSite,
				"remaining":         mat.Remaining(),
			})
		}
	}
	if len(deferredMaterials) > 0 {
		logger.Log("INFO", "Construction pipeline planned with deferred materials", map[string]interface{}{
			"construction_site":  plan.constructionSite,
			"deferred_materials": deferredMaterials,
			"sourceable_count":   len(materials) - len(deferredMaterials),
		})
	}
	return deferredMaterials, nil
}

// persistPlannedPipeline saves the pipeline AND its tasks: the coordinator reads tasks from the
// database, so unpersisted tasks would leave the pipeline permanently idle.
func (p *ConstructionPipelinePlanner) persistPlannedPipeline(ctx context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	if err := p.pipelineRepo.Create(ctx, pipeline); err != nil {
		return fmt.Errorf("failed to save pipeline: %w", err)
	}
	if tasks := pipeline.Tasks(); len(tasks) > 0 {
		if err := p.taskRepo.CreateBatch(ctx, tasks); err != nil {
			return fmt.Errorf("failed to save pipeline tasks: %w", err)
		}
	}
	return nil
}

// StopResult contains the result of stopping a construction pipeline.
type StopResult struct {
	Pipeline       *manufacturing.ManufacturingPipeline
	TasksCancelled int
}

// Stop cancels the active construction pipeline for a site. It:
//  1. Looks up the active (non-terminal) CONSTRUCTION pipeline for the site -
//     FindByConstructionSite only ever returns PLANNING/EXECUTING pipelines,
//     so "no pipeline" and "already stopped" both surface as the same clear
//     error here, which is the idempotency guard the caller needs.
//  2. Cancels every cancellable task (PENDING/READY/ASSIGNED) belonging to
//     THIS pipeline only - construction pipelines share the mfg coordinator
//     with FABRICATION/COLLECTION pipelines, so Stop must never reach beyond
//     tasks keyed under this exact pipeline ID. A task already EXECUTING is
//     left alone to finish or fail naturally, mirroring PipelineRecycler.
//  3. Force-releases any ship an ASSIGNED (now-cancelled) task had claimed,
//     so the ship re-enters coordinator discovery immediately.
//  4. Cancels the pipeline itself, which is the authoritative signal that
//     stops new tasks from being spawned.
//
// Task/ship cleanup failures are logged and soft-failed (best-effort) - the
// core "stop" contract is satisfied by cancelling the pipeline, which is a
// hard error since it is what actually halts new task spawning.
func (p *ConstructionPipelinePlanner) Stop(ctx context.Context, playerID int, constructionSite string) (*StopResult, error) {
	logger := common.LoggerFromContext(ctx)

	pipeline, err := p.pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to look up construction pipeline for %s: %w", constructionSite, err)
	}
	if pipeline == nil {
		return nil, fmt.Errorf("no active construction pipeline for site %s", constructionSite)
	}

	tasksCancelled := 0
	tasks, err := p.taskRepo.FindByPipelineID(ctx, pipeline.ID())
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("failed to load tasks for construction pipeline %s: %v", pipeline.ID(), err), nil)
	}
	for _, task := range tasks {
		if !isCancellableConstructionTask(task) {
			continue
		}
		if shipSymbol := task.AssignedShip(); shipSymbol != "" {
			p.releaseShip(ctx, shipSymbol, playerID, "construction pipeline stopped")
		}
		if err := task.Cancel("construction pipeline stopped"); err != nil {
			logger.Log("WARN", fmt.Sprintf("failed to cancel construction task %s: %v", task.ID(), err), nil)
			continue
		}
		if err := p.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", fmt.Sprintf("failed to persist cancelled construction task %s: %v", task.ID(), err), nil)
			continue
		}
		tasksCancelled++
	}

	if err := pipeline.Cancel(); err != nil {
		return nil, fmt.Errorf("failed to cancel construction pipeline %s: %w", pipeline.ID(), err)
	}
	if err := p.pipelineRepo.Update(ctx, pipeline); err != nil {
		return nil, fmt.Errorf("failed to persist cancelled construction pipeline %s: %w", pipeline.ID(), err)
	}

	logger.Log("INFO", "Stopped construction pipeline", map[string]interface{}{
		"pipeline_id":       pipeline.ID(),
		"construction_site": constructionSite,
		"tasks_cancelled":   tasksCancelled,
	})

	return &StopResult{Pipeline: pipeline, TasksCancelled: tasksCancelled}, nil
}

// isCancellableConstructionTask reports whether a task is safe to cancel:
// PENDING, READY, and ASSIGNED tasks haven't started irreversible work yet.
// EXECUTING tasks are deliberately left to complete or fail naturally.
func isCancellableConstructionTask(task *manufacturing.ManufacturingTask) bool {
	switch task.Status() {
	case manufacturing.TaskStatusPending, manufacturing.TaskStatusReady, manufacturing.TaskStatusAssigned:
		return true
	default:
		return false
	}
}

// releaseShip force-releases a ship from its current assignment. Best-effort:
// failures are logged, not propagated, since the pipeline/task cancellation
// already satisfies the core "stop" contract.
func (p *ConstructionPipelinePlanner) releaseShip(ctx context.Context, shipSymbol string, playerID int, reason string) {
	logger := common.LoggerFromContext(ctx)
	if p.shipRepo == nil {
		return
	}
	// Release under CAS-retry: re-apply ForceRelease on the FRESH row. A plain Save
	// that loses the version race takes its ownership columns from the row, so a
	// release persisted that way would silently leave the claim standing.
	if _, _, err := p.shipRepo.SaveWithRetry(ctx, shipSymbol, shared.MustNewPlayerID(playerID),
		func(sh *navigation.Ship) (bool, error) {
			sh.ForceRelease(reason, p.clock)
			return true, nil
		}); err != nil {
		logger.Log("WARN", fmt.Sprintf("failed to save ship %s release: %v", shipSymbol, err), nil)
	}
}

// planMaterial plans the tasks needed to source and deliver ONE construction
// material, returning the staged tasks (added to the pipeline by the caller) and
// whether the material had to be deferred.
//
// Depth is a per-material CEILING, not a global switch. For each material it
// selects the cheapest SOURCEABLE path:
//  1. BUY the final good directly when a buy source exists (an EXPORT market at
//     MODERATE+, or - via FindConstructionSource - an IMPORT/EXCHANGE holding
//     ABUNDANT/HIGH accumulated stock). This is preferred: one hop, no chain.
//  2. Otherwise FABRICATE within the depth ceiling (only when depth < 3, the good
//     is not raw, and every input is SOURCEABLE within that ceiling — buyable now OR
//     itself producible from sourceable inputs, sp-3bza).
//  3. Otherwise DEFER: stage a PENDING DELIVER_TO_CONSTRUCTION with no source
//     that the SupplyMonitor re-sources when supply regenerates.
//
// A non-nil error is returned only for infrastructure failures; an unsourceable
// material is reported via deferred=true, never as an error.
func (p *ConstructionPipelinePlanner) planMaterial(
	ctx context.Context,
	targetGood string,
	plan materialPlan,
) (staged []*manufacturing.ManufacturingTask, deferred bool, err error) {
	logger := common.LoggerFromContext(ctx)

	// 1. Prefer buying the final good directly (cheapest sourceable path),
	//    regardless of the depth flag - depth only caps how deep we fabricate.
	source, err := p.marketLocator.FindConstructionSource(ctx, targetGood, plan.systemSymbol, plan.playerID, manufacturing.SupplyLevel(plan.minSupply))
	if err != nil {
		return nil, false, fmt.Errorf("failed to locate buy source for %s: %w", targetGood, err)
	}
	if source != nil {
		task := manufacturing.NewDeliverToConstructionTask(
			plan.pipelineID, plan.playerID, targetGood,
			source.WaypointSymbol, // sourceMarket (buy here)
			"",                    // factorySymbol (not collecting from a factory)
			plan.constructionSite,
			[]string{}, // no dependencies
		)
		logger.Log("DEBUG", "Planned construction buy (direct)", map[string]interface{}{
			"good":              targetGood,
			"source_market":     source.WaypointSymbol,
			"supply":            source.Supply,
			"construction_site": plan.constructionSite,
		})
		return []*manufacturing.ManufacturingTask{task}, false, nil
	}

	// 2. Not buyable. Fabricate within the depth ceiling when permitted.
	//    depth >= 3 is a "buy final only" ceiling; raw materials cannot be made.
	if plan.supplyChainDepth < 3 && !goods.IsRawMaterial(targetGood) {
		fabTasks, ok := p.planFabrication(ctx, targetGood, plan)
		if ok {
			logger.Log("DEBUG", "Planned construction material via fabrication", map[string]interface{}{
				"good":              targetGood,
				"tasks":             len(fabTasks),
				"depth_ceiling":     plan.supplyChainDepth,
				"construction_site": plan.constructionSite,
			})
			return fabTasks, false, nil
		}
	}

	// 3. Neither buyable nor fabricable now - DEFER with a visible PENDING task.
	deferredTask := manufacturing.NewDeliverToConstructionTask(
		plan.pipelineID, plan.playerID, targetGood,
		"", // no source yet - SupplyMonitor re-sources when supply regenerates
		"", // no factory
		plan.constructionSite,
		[]string{},
	)
	return []*manufacturing.ManufacturingTask{deferredTask}, true, nil
}

// planFabrication stages the fabrication of targetGood as a SINGLE dependency-free
// DELIVER_TO_CONSTRUCTION task carrying the factory. ok=false means
// a factory or an input is not sourceable within the depth ceiling, so the whole material should be
// deferred rather than partially planned. There is no failure mode: a factory-lookup error is
// itself a defer (we cannot fabricate now), so the step reports ok, never an error.
//
// It does NOT stage separate ACQUIRE_DELIVER input legs. The construction drain executes the
// delivery task by driving ProduceGood(Fabricate) on the shared engine, which sources the inputs
// (buying OR recursively producing them), feeds the factory, and harvests the output
// itself — one engine. Staging input legs here would only create a
// dependency the thin drain never satisfies, orphaning those legs and blocking the delivery task
// forever. The buy-vs-produce DECISION lives in planMaterial, which
// fabricates only a non-buyable good within the depth ceiling; this method only declines to
// decompose that decision into legs.
//
// The FEASIBILITY gate that decides stage-vs-defer is "every input is SOURCEABLE
// within the depth ceiling" (buyable OR producible), NOT "every immediate input buyable
// at MODERATE+ now": the latter defers a whole material whenever a deep input is scarce — even
// when the recursive drain could PRODUCE that input (e.g. ADVANCED_CIRCUITRY deferred
// because ELECTRONICS is SCARCE, though ELECTRONICS is fabricable from buyable SILICON+COPPER),
// stalling the gate leg. fabricationInputsSourceable defers only a TRULY unsourceable input.
func (p *ConstructionPipelinePlanner) planFabrication(
	ctx context.Context,
	targetGood string,
	plan materialPlan,
) (staged []*manufacturing.ManufacturingTask, ok bool) {
	inputs := goods.GetRequiredInputs(targetGood)
	if len(inputs) == 0 {
		return nil, false // no recipe - not fabricable, defer
	}

	// The factory must EXPORT targetGood AND IMPORT every input. A missing factory
	// (or a transient lookup miss) means we cannot fabricate now - defer.
	factory, ferr := p.marketLocator.FindFactoryForProduction(ctx, targetGood, inputs, plan.systemSymbol, plan.playerID)
	if ferr != nil {
		return nil, false
	}

	// FEASIBILITY: defer the whole material only when an input is TRULY unsourceable
	// within the depth ceiling — no market AND no producible path. A scarce-but-producible input
	// (the gate-critical ELECTRONICS case) is FEASIBLE, because the drain will produce it.
	if !p.fabricationInputsSourceable(ctx, targetGood, plan, inputs) {
		return nil, false // an input is unsourceable within depth - defer whole material
	}

	// A single dependency-free DELIVER_TO_CONSTRUCTION task carrying the factory: the drain
	// fabricates the good there and delivers it to the site. No input-leg dependencies, so it
	// becomes READY the moment the pipeline starts.
	deliverTask := manufacturing.NewDeliverToConstructionTask(
		plan.pipelineID, plan.playerID, targetGood,
		"",                     // sourceMarket (fabricated at the factory, not bought)
		factory.WaypointSymbol, // factorySymbol
		plan.constructionSite,
		nil, // no input-leg dependencies — ProduceGood(Fabricate) sources the inputs
	)
	return []*manufacturing.ManufacturingTask{deliverTask}, true
}

// fabricationInputsSourceable reports whether every input of targetGood can be SOURCED — bought OR
// produced — within the depth ceiling, the feasibility gate that decides stage-vs-defer.
//
// When a tree resolver is wired (the daemon injects the shared SupplyChainResolver — the SAME
// engine the construction drain runs, sp-yfzi), it asks the resolver to build the full
// scarcity-gated dependency tree for targetGood under the drain's EXACT settings: the smart
// production strategy (a scarce intermediate that has a factory recurses; an abundant one is
// bought), the pipeline's SupplyChainDepth as the fabricate depth cap (WithFabricateDepthCap —
// resolveFabricationTree in the drain passes the identical value), and the pipeline's per-good
// overrides. A tree that builds without error means every scarce intermediate with a factory
// recurses and every leaf resolves to a buyable market, so the drain WILL be able to source each
// input — FEASIBLE. The resolver errors only when an input is genuinely unsourceable within depth
// (no market AND no producible path), which is the one case we still DEFER (RULINGS #1: a deferred
// material stays a visible PENDING task the SupplyMonitor re-sources; the pipeline never fails).
// Reusing the resolver (not a hand-rolled MODERATE+ check) is what keeps the planner's verdict
// aligned with what the drain will actually execute — the drain buys leaves at ANY supply tier via
// FindBestMarketForBuying, so a MODERATE+-only immediate-input check would wrongly defer a material
// whose deep raws are only LIMITED/SCARCE but still buyable.
//
// When no resolver is wired (nil — a planner built without SetTreeResolver, and the in-package
// tests that never inject one), it falls back to the previous gate: every IMMEDIATE
// input must be buyable at MODERATE+ now.
func (p *ConstructionPipelinePlanner) fabricationInputsSourceable(
	ctx context.Context,
	targetGood string,
	plan materialPlan,
	immediateInputs []string,
) bool {
	if p.treeResolver != nil {
		// Stamp the drain's exact tree-build settings (mirrors run_construction_coordinator.go's
		// resolveFabricationTree) so the planner's feasibility verdict equals the drain's execution.
		buildCtx := WithProductionStrategy(ctx, DefaultProductionStrategy)
		buildCtx = WithFabricateDepthCap(buildCtx, plan.supplyChainDepth, false)
		buildCtx = WithGoodGatingOverrides(buildCtx, plan.goodOverrides)
		tree, terr := p.treeResolver.BuildDependencyTree(buildCtx, targetGood, plan.systemSymbol, plan.playerID)
		return terr == nil && tree != nil
	}

	// Fallback (no resolver wired): the previous gate — every immediate input buyable at
	// MODERATE+ now, or the material defers.
	for _, input := range immediateInputs {
		src, serr := p.marketLocator.FindExportMarketBySupplyPriority(ctx, input, plan.systemSymbol, plan.playerID)
		if serr != nil || src == nil {
			return false
		}
	}
	return true
}
