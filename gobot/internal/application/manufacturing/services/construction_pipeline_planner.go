package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// errMaterialUnsourceable is an internal sentinel signalling that a material (or
// one of its fabrication inputs) has no market with acceptable supply right now.
// It is never returned to callers: planMaterial converts it into a DEFERRED task
// so an unsourceable material never fails the whole pipeline.
var errMaterialUnsourceable = errors.New("material not sourceable at current supply")

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
}

// NewConstructionPipelinePlanner creates a new construction pipeline planner.
func NewConstructionPipelinePlanner(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	constructionRepo manufacturing.ConstructionSiteRepository,
	marketLocator *MarketLocator,
) *ConstructionPipelinePlanner {
	return &ConstructionPipelinePlanner{
		pipelineRepo:     pipelineRepo,
		taskRepo:         taskRepo,
		constructionRepo: constructionRepo,
		marketLocator:    marketLocator,
	}
}

// StartOrResumeResult contains the result of starting or resuming a pipeline.
type StartOrResumeResult struct {
	Pipeline  *manufacturing.ManufacturingPipeline
	IsResumed bool // true if resuming existing, false if newly created
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
			// Only touch the persisted floor when the caller supplied a genuine,
			// changed value: an empty minSupply means "the flag wasn't passed on
			// this resume call" and must not clobber a floor set earlier.
			if minSupply != "" && minSupply != existingPipeline.MinSupply() {
				existingPipeline.SetMinSupply(minSupply)
				if err := p.pipelineRepo.Update(ctx, existingPipeline); err != nil {
					return nil, fmt.Errorf("failed to persist updated min-supply floor for pipeline %s: %w", existingPipeline.ID(), err)
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
				Pipeline:  existingPipeline,
				IsResumed: true,
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
		staged, deferred, err := p.planMaterial(ctx, pipeline.ID(), mat.TradeSymbol(), systemSymbol, constructionSite, supplyChainDepth, playerID, minSupply)
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
		Pipeline:  pipeline,
		IsResumed: false,
	}, nil
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

// planFabrication stages the fabrication chain for targetGood into a local task
// list. ok=false (with a nil error) means a factory or an input is not sourceable
// right now, so the whole material should be deferred rather than partially
// planned. Only infrastructure failures are returned as errors.
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

	tasks := make([]*manufacturing.ManufacturingTask, 0, len(inputs)+1)
	inputTaskIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids, serr := p.stageInput(ctx, &tasks, pipelineID, input, systemSymbol, factory.WaypointSymbol, supplyChainDepth, playerID)
		if serr != nil {
			if errors.Is(serr, errMaterialUnsourceable) {
				return nil, false, nil // an input is unsourceable - defer whole material
			}
			return nil, false, serr
		}
		inputTaskIDs = append(inputTaskIDs, ids...)
	}

	// Collect the fabricated good from the factory and deliver it to construction.
	deliverTask := manufacturing.NewDeliverToConstructionTask(
		pipelineID, playerID, targetGood,
		"",                     // sourceMarket (collecting from factory, not buying)
		factory.WaypointSymbol, // factorySymbol
		constructionSite,
		inputTaskIDs, // depends on the input deliveries
	)
	tasks = append(tasks, deliverTask)
	return tasks, true, nil
}

// stageInput stages the acquisition of a single fabrication input. Within the
// depth ceiling it either buys the input directly (raw, or depth >= 2 "buy
// intermediates") or recurses to produce it. Returns errMaterialUnsourceable
// when the input cannot be sourced.
func (p *ConstructionPipelinePlanner) stageInput(
	ctx context.Context,
	tasks *[]*manufacturing.ManufacturingTask,
	pipelineID string,
	input string,
	systemSymbol string,
	factorySymbol string,
	supplyChainDepth int,
	playerID int,
) ([]string, error) {
	if goods.IsRawMaterial(input) || supplyChainDepth >= 2 {
		id, err := p.stageAcquireDeliver(ctx, tasks, pipelineID, input, systemSymbol, factorySymbol, playerID)
		if err != nil {
			return nil, err
		}
		return []string{id}, nil
	}
	// depth < 2: produce the input from its own inputs (recurse).
	return p.stageProduction(ctx, tasks, pipelineID, input, systemSymbol, factorySymbol, supplyChainDepth, playerID)
}

// stageAcquireDeliver stages an ACQUIRE_DELIVER task to buy an input from an
// export market and deliver it to a factory. Returns errMaterialUnsourceable
// when no market with acceptable supply exists.
func (p *ConstructionPipelinePlanner) stageAcquireDeliver(
	ctx context.Context,
	tasks *[]*manufacturing.ManufacturingTask,
	pipelineID string,
	good string,
	systemSymbol string,
	factorySymbol string,
	playerID int,
) (string, error) {
	market, err := p.marketLocator.FindExportMarketBySupplyPriority(ctx, good, systemSymbol, playerID)
	if err != nil || market == nil {
		return "", errMaterialUnsourceable
	}
	task := manufacturing.NewAcquireDeliverTask(pipelineID, playerID, good, market.WaypointSymbol, factorySymbol, nil)
	*tasks = append(*tasks, task)
	return task.ID(), nil
}

// stageProduction recursively stages the tasks to produce an intermediate good
// and deliver it to deliveryDestination. Returns errMaterialUnsourceable when
// any factory or input in the chain is not sourceable.
func (p *ConstructionPipelinePlanner) stageProduction(
	ctx context.Context,
	tasks *[]*manufacturing.ManufacturingTask,
	pipelineID string,
	good string,
	systemSymbol string,
	deliveryDestination string,
	supplyChainDepth int,
	playerID int,
) ([]string, error) {
	inputs := goods.GetRequiredInputs(good)
	if len(inputs) == 0 {
		// Raw material - buy and deliver directly.
		id, err := p.stageAcquireDeliver(ctx, tasks, pipelineID, good, systemSymbol, deliveryDestination, playerID)
		if err != nil {
			return nil, err
		}
		return []string{id}, nil
	}

	factory, ferr := p.marketLocator.FindFactoryForProduction(ctx, good, inputs, systemSymbol, playerID)
	if ferr != nil {
		return nil, errMaterialUnsourceable
	}

	inputTaskIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids, err := p.stageInput(ctx, tasks, pipelineID, input, systemSymbol, factory.WaypointSymbol, supplyChainDepth, playerID)
		if err != nil {
			return nil, err
		}
		inputTaskIDs = append(inputTaskIDs, ids...)
	}

	collectTask := manufacturing.NewAcquireDeliverTask(
		pipelineID, playerID, good,
		factory.WaypointSymbol, // collect from this factory
		deliveryDestination,    // deliver to next factory or construction site
		inputTaskIDs,           // wait for inputs
	)
	*tasks = append(*tasks, collectTask)
	return []string{collectTask.ID()}, nil
}

// extractSystemSymbol extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5").
func extractSystemSymbol(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}
