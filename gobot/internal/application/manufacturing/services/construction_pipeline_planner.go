package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

// StartOrResumeResult contains the result of starting or resuming a pipeline.
type StartOrResumeResult struct {
	Pipeline  *manufacturing.ManufacturingPipeline
	IsResumed bool // true if resuming existing, false if newly created

	// DeferredMaterials names every material (trade symbol) that could not be
	// sourced this call, in the same order the pipeline's materials were
	// planned/loaded. Planning is never all-or-nothing (sp-ooba): a deferred
	// material still gets a visible PENDING task that the SupplyMonitor
	// re-sources later, but the operator needs the name surfaced here rather
	// than a generic "no market" message (sp-560b) to go source it manually.
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
//   - minSupply: caller-set EXPORT sourcing floor (sp-ezz9/sp-j2hq), e.g.
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

	// 1. IDEMPOTENCY CHECK: Check if pipeline already exists for this construction site
	existingPipeline, err := p.pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing pipeline: %w", err)
	}

	if existingPipeline != nil {
		// A pipeline row alone doesn't mean the pipeline is healthy: its tasks
		// may have been reaped (e.g. by daemon-restart recovery), leaving an
		// EXECUTING pipeline that can never deliver, complete, or fail. Only
		// resume if there is still at least one incomplete task to execute.
		persistedTasks, err := p.taskRepo.FindByPipelineID(ctx, existingPipeline.ID())
		if err != nil {
			return nil, fmt.Errorf("failed to load tasks for existing pipeline %s: %w", existingPipeline.ID(), err)
		}

		hasIncompleteTasks := false
		for _, task := range persistedTasks {
			if !task.IsTerminal() {
				hasIncompleteTasks = true
				break
			}
		}

		if hasIncompleteTasks {
			existingPipeline.SetTasks(persistedTasks)
			// Only touch the persisted floor/overrides when the caller supplied a genuine,
			// changed value: an empty minSupply (or empty override map) means "the flag wasn't
			// passed on this resume call" and must not clobber a value set earlier.
			needsUpdate := false
			if minSupply != "" && minSupply != existingPipeline.MinSupply() {
				existingPipeline.SetMinSupply(minSupply)
				needsUpdate = true
			}
			// sp-sdyo: a resumed launch that supplies per-good overrides updates the persisted map
			// (e.g. re-tuning a bottleneck's floor); an empty map leaves the existing overrides intact.
			if len(goodOverrides) > 0 {
				existingPipeline.SetGoodOverrides(goodOverrides)
				needsUpdate = true
			}
			if needsUpdate {
				if err := p.pipelineRepo.Update(ctx, existingPipeline); err != nil {
					return nil, fmt.Errorf("failed to persist updated sourcing config for pipeline %s: %w", existingPipeline.ID(), err)
				}
			}

			// sp-560b: a resumed pipeline's deferred materials live only in its
			// persisted tasks (the local deferredMaterials slice below only
			// exists during initial planning), so scan for them here too - the
			// operator re-running `construction start` on an in-progress
			// pipeline deserves the same by-name visibility as a fresh plan.
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

		// Stale empty pipeline: terminalize it so FindByConstructionSite stops
		// returning it, then fall through to plan a fresh pipeline.
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
	}

	// 2. AUTO-DISCOVERY: Fetch construction requirements from API
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

	// 3. Get unfulfilled materials (only materials that still need delivery)
	unfulfilledMaterials := constructionSiteData.UnfulfilledMaterials()
	if len(unfulfilledMaterials) == 0 {
		return nil, fmt.Errorf("construction site %s has no remaining materials to deliver", constructionSite)
	}

	logger.Log("INFO", "Found construction materials to deliver", map[string]interface{}{
		"construction_site": constructionSite,
		"materials_count":   len(unfulfilledMaterials),
	})

	// 4. Create pipeline
	pipeline := manufacturing.NewConstructionPipeline(constructionSite, playerID, supplyChainDepth, maxWorkers)
	// Persist the floor on the entity itself (sp-j2hq), not just pass it
	// transiently to planMaterial below - a material that defers during THIS
	// pass is recovered later by reading the floor back off the pipeline row.
	pipeline.SetMinSupply(minSupply)
	// sp-sdyo: persist the per-good override map on the entity too, so a per-good sourcing-floor
	// override survives a restart and is re-read by the deferred-material recovery loop, not just
	// consumed during this initial planning pass (RULINGS #2).
	pipeline.SetGoodOverrides(goodOverrides)

	// 5. Add material targets to pipeline
	for _, mat := range unfulfilledMaterials {
		remaining := mat.Remaining()
		materialTarget := manufacturing.NewConstructionMaterialTarget(mat.TradeSymbol(), remaining)
		if err := pipeline.AddMaterial(materialTarget); err != nil {
			return nil, fmt.Errorf("failed to add material %s: %w", mat.TradeSymbol(), err)
		}

		logger.Log("INFO", "Added material target to pipeline", map[string]interface{}{
			"material":  mat.TradeSymbol(),
			"remaining": remaining,
		})
	}

	// 6. Determine system symbol - use provided value or derive from waypoint
	if systemSymbol == "" {
		systemSymbol = extractSystemSymbol(constructionSite)
	}

	// 7. Plan each material INDEPENDENTLY. This is the core of sp-r900: planning
	// is no longer all-or-nothing. A material that cannot be sourced right now is
	// DEFERRED (a visible PENDING task), not a fatal error - so the pipeline still
	// saves and dispatches every sourceable material while the deferred one waits.
	// The SupplyMonitor re-sources deferred tasks when supply regenerates.
	deferredMaterials := make([]string, 0)
	for _, mat := range unfulfilledMaterials {
		// sp-sdyo: resolve the EXPORT sourcing floor per material — a per-good override loosens the
		// floor for a single bottleneck (e.g. down to SCARCE) while every other material keeps the
		// pipeline's global floor unchanged.
		matMinSupply := goodOverrides.MinSupplyFor(mat.TradeSymbol(), minSupply)
		staged, deferred, err := p.planMaterial(ctx, pipeline.ID(), mat.TradeSymbol(), systemSymbol, constructionSite, supplyChainDepth, playerID, matMinSupply)
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
				"construction_site": constructionSite,
				"remaining":         mat.Remaining(),
			})
		}
	}
	if len(deferredMaterials) > 0 {
		logger.Log("INFO", "Construction pipeline planned with deferred materials", map[string]interface{}{
			"construction_site":  constructionSite,
			"deferred_materials": deferredMaterials,
			"sourceable_count":   len(unfulfilledMaterials) - len(deferredMaterials),
		})
	}

	// 8. Start pipeline so dependency-free tasks become READY and the running
	// coordinator can pick them up without waiting for a daemon restart.
	if err := pipeline.Start(); err != nil {
		return nil, fmt.Errorf("failed to start pipeline: %w", err)
	}

	// 9. Persist pipeline and its tasks (the coordinator reads tasks from the
	// database, so unpersisted tasks would leave the pipeline permanently idle)
	if err := p.pipelineRepo.Create(ctx, pipeline); err != nil {
		return nil, fmt.Errorf("failed to save pipeline: %w", err)
	}
	if tasks := pipeline.Tasks(); len(tasks) > 0 {
		if err := p.taskRepo.CreateBatch(ctx, tasks); err != nil {
			return nil, fmt.Errorf("failed to save pipeline tasks: %w", err)
		}
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

// StopResult contains the result of stopping a construction pipeline.
type StopResult struct {
	Pipeline       *manufacturing.ManufacturingPipeline
	TasksCancelled int
}

// Stop cancels the active construction pipeline for a site (sp-yzrv). It:
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
	ship, err := p.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("failed to load ship %s for release: %v", shipSymbol, err), nil)
		return
	}
	ship.ForceRelease(reason, p.clock)
	if err := p.shipRepo.Save(ctx, ship); err != nil {
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
//     is not raw, and every input is itself sourceable).
//  3. Otherwise DEFER: stage a PENDING DELIVER_TO_CONSTRUCTION with no source
//     that the SupplyMonitor re-sources when supply regenerates.
//
// A non-nil error is returned only for infrastructure failures; an unsourceable
// material is reported via deferred=true, never as an error.
func (p *ConstructionPipelinePlanner) planMaterial(
	ctx context.Context,
	pipelineID string,
	targetGood string,
	systemSymbol string,
	constructionSite string,
	supplyChainDepth int,
	playerID int,
	minSupply string,
) (staged []*manufacturing.ManufacturingTask, deferred bool, err error) {
	logger := common.LoggerFromContext(ctx)

	// 1. Prefer buying the final good directly (cheapest sourceable path),
	//    regardless of the depth flag - depth only caps how deep we fabricate.
	source, err := p.marketLocator.FindConstructionSource(ctx, targetGood, systemSymbol, playerID, manufacturing.SupplyLevel(minSupply))
	if err != nil {
		return nil, false, fmt.Errorf("failed to locate buy source for %s: %w", targetGood, err)
	}
	if source != nil {
		task := manufacturing.NewDeliverToConstructionTask(
			pipelineID, playerID, targetGood,
			source.WaypointSymbol, // sourceMarket (buy here)
			"",                    // factorySymbol (not collecting from a factory)
			constructionSite,
			[]string{}, // no dependencies
		)
		logger.Log("DEBUG", "Planned construction buy (direct)", map[string]interface{}{
			"good":              targetGood,
			"source_market":     source.WaypointSymbol,
			"supply":            source.Supply,
			"construction_site": constructionSite,
		})
		return []*manufacturing.ManufacturingTask{task}, false, nil
	}

	// 2. Not buyable. Fabricate within the depth ceiling when permitted.
	//    depth >= 3 is a "buy final only" ceiling; raw materials cannot be made.
	if supplyChainDepth < 3 && !goods.IsRawMaterial(targetGood) {
		fabTasks, ok, ferr := p.planFabrication(ctx, pipelineID, targetGood, systemSymbol, constructionSite, supplyChainDepth, playerID)
		if ferr != nil {
			return nil, false, ferr
		}
		if ok {
			logger.Log("DEBUG", "Planned construction material via fabrication", map[string]interface{}{
				"good":              targetGood,
				"tasks":             len(fabTasks),
				"depth_ceiling":     supplyChainDepth,
				"construction_site": constructionSite,
			})
			return fabTasks, false, nil
		}
	}

	// 3. Neither buyable nor fabricable now - DEFER with a visible PENDING task.
	deferredTask := manufacturing.NewDeliverToConstructionTask(
		pipelineID, playerID, targetGood,
		"", // no source yet - SupplyMonitor re-sources when supply regenerates
		"", // no factory
		constructionSite,
		[]string{},
	)
	return []*manufacturing.ManufacturingTask{deferredTask}, true, nil
}

// planFabrication stages the fabrication of targetGood as a SINGLE dependency-free
// DELIVER_TO_CONSTRUCTION task carrying the factory (sp-qmp8). ok=false (with a nil error) means
// a factory or an input is not sourceable right now, so the whole material should be deferred
// rather than partially planned. Only infrastructure failures are returned as errors.
//
// It does NOT stage separate ACQUIRE_DELIVER input legs. The construction drain executes the
// delivery task by driving ProduceGood(Fabricate) on the shared engine, which buys the inputs,
// feeds the factory, and harvests the output itself — one engine (the sp-jav2 regression restore).
// Staging input legs here would only create a dependency the thin drain never satisfies, blocking
// the delivery task forever (the exact orphaned-legs state this bead fixes). The buy-vs-produce
// DECISION is unchanged: planMaterial still fabricates only a non-buyable good within the depth
// ceiling; this method only stops decomposing that decision into legs.
func (p *ConstructionPipelinePlanner) planFabrication(
	ctx context.Context,
	pipelineID string,
	targetGood string,
	systemSymbol string,
	constructionSite string,
	supplyChainDepth int,
	playerID int,
) (staged []*manufacturing.ManufacturingTask, ok bool, err error) {
	inputs := goods.GetRequiredInputs(targetGood)
	if len(inputs) == 0 {
		return nil, false, nil // no recipe - not fabricable, defer
	}

	// The factory must EXPORT targetGood AND IMPORT every input. A missing factory
	// (or a transient lookup miss) means we cannot fabricate now - defer.
	factory, ferr := p.marketLocator.FindFactoryForProduction(ctx, targetGood, inputs, systemSymbol, playerID)
	if ferr != nil {
		return nil, false, nil
	}

	// Every immediate input must be buyable NOW, or the drain cannot feed the factory this
	// pass — defer the whole material (the SupplyMonitor re-sources it when supply regenerates).
	// This mirrors the sourceability gate the old per-input staging enforced, minus the task
	// creation: the drain's ProduceGood(Fabricate) buys these inputs directly (one-level
	// fabrication), so verifying the immediate inputs is the feasibility check that matches how
	// the drain executes.
	for _, input := range inputs {
		src, serr := p.marketLocator.FindExportMarketBySupplyPriority(ctx, input, systemSymbol, playerID)
		if serr != nil || src == nil {
			return nil, false, nil // an input is unsourceable - defer whole material
		}
	}

	// A single dependency-free DELIVER_TO_CONSTRUCTION task carrying the factory: the drain
	// fabricates the good there and delivers it to the site. No input-leg dependencies, so it
	// becomes READY the moment the pipeline starts.
	deliverTask := manufacturing.NewDeliverToConstructionTask(
		pipelineID, playerID, targetGood,
		"",                     // sourceMarket (fabricated at the factory, not bought)
		factory.WaypointSymbol, // factorySymbol
		constructionSite,
		nil, // no input-leg dependencies — ProduceGood(Fabricate) sources the inputs
	)
	return []*manufacturing.ManufacturingTask{deliverTask}, true, nil
}

// extractSystemSymbol extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5").
func extractSystemSymbol(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}
