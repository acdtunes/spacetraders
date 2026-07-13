package grpc

// This file holds the GATE-phase concrete adapters (Slice 3, sp-ysgb.2) that bind the bootstrap
// coordinator's GATE ports to existing daemon capabilities — building NO new construction, fabrication,
// or fleet logic (the captain-verification reuse gate). Each adapter is a thin wrapper over a surface
// that already ships: `construction start` (StartConstructionPipeline), the manufacturing/goods-factory
// coordinator (the construction executor), fleet dedication (AssignFleet), and the standing-coordinator
// launches (FleetAutosizer / Siting / WorkerRebalancer).
//
// EXECUTOR NOTE (reported to the harbormaster): construction pipelines are worked by the
// manufacturing/goods-factory coordinator (see operations.go isManufacturingCoordinatorType — "Construction
// supply pipelines run through this same coordinator"). Post-sp-jav2 there is no clean bootstrap-launchable
// "start the manufacturing coordinator" verb, so EnsureRunning is best-effort and the L57 adoption bounce
// uses StopContainer + the daemon's restart-recovery re-adoption (which re-scans and adopts the fresh
// pipeline). In the normal era the executor is a standing coordinator already running, so the common GATE
// path is the bounce, which is wired exactly.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// manufacturingFleetTag is the dedicated-fleet tag the manufacturing/construction executor's worker
	// pool selects on (mirrors the "manufacturing" tag in navigation/ports.go). A hull carrying it is a
	// gate-construction worker: repurposing re-tags a contract hauler to it, and the observer counts it
	// as a GateWorker.
	manufacturingFleetTag = "manufacturing"
	// jumpGateWaypointType is the waypoint type of a jump gate — the GATE-site discovery scans the home
	// system for this type and picks the one whose construction site is not yet complete.
	jumpGateWaypointType = "JUMP_GATE"
)

// executorContainerTypes are the container types that run the construction executor (the manufacturing /
// goods-factory coordinator). A running container of any of these means the executor is up.
var executorContainerTypes = []container.ContainerType{
	container.ContainerTypeManufacturingCoordinator,
	container.ContainerType("goods_factory_coordinator"),
}

// containerTypeRunning reports whether a RUNNING-or-PENDING container of any of the given types exists
// for the player (the generic form of contractFleetCoordinatorRunning).
func containerTypeRunning(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int, types ...container.ContainerType) (bool, error) {
	id, err := firstContainerIDOfType(ctx, repo, playerID, types...)
	return id != "", err
}

// firstContainerIDOfType returns the ID of the first RUNNING-or-PENDING container matching any given type
// (for the adoption bounce, which stops that container so the daemon re-adopts it), or "" if none.
func firstContainerIDOfType(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int, types ...container.ContainerType) (string, error) {
	for _, st := range []string{string(container.ContainerStatusRunning), string(container.ContainerStatusPending)} {
		summaries, err := repo.ListByStatusSimple(ctx, st, &playerID)
		if err != nil {
			return "", err
		}
		for _, s := range summaries {
			for _, t := range types {
				if s.ContainerType == string(t) {
					return s.ID, nil
				}
			}
		}
	}
	return "", nil
}

// bootstrapGateSnapshot is the observer's GATE read: it discovers the home-system jump-gate construction
// site and reads its progress + pipeline shape (reusing GetConstructionStatus). Every field is
// best-effort — a miss leaves it zero-valued, which the reconciler reads fail-safe (an unknown site holds
// GATE on no_gate_site; 0% never falsely completes).
type bootstrapGateSnapshot struct {
	Site          string
	Started       bool
	Complete      bool
	Percent       float64
	MaterialChain int
	Adopted       bool
}

// readBootstrapGateSnapshot discovers the under-construction jump gate in the home system and reads its
// construction status. Reuses the construction-site API + GetConstructionStatus; builds nothing new.
func (s *DaemonServer) readBootstrapGateSnapshot(ctx context.Context, homeSystem string, playerID int) bootstrapGateSnapshot {
	var snap bootstrapGateSnapshot
	if homeSystem == "" || s.waypointRepo == nil {
		return snap
	}

	wps, err := s.waypointRepo.ListBySystem(ctx, homeSystem)
	if err != nil {
		return snap
	}
	constructionRepo := api.NewConstructionSiteRepository(s.getAPIClient(), s.playerRepo)

	// Scan the home-system jump gates into (symbol, complete) observations, capturing each site's
	// progress. Stop at the FIRST under-construction gate — it is the active drive target regardless of
	// what follows (prefer-incomplete), so the read profile matches the pre-refactor early-return.
	var scans []gateSiteScan
	progressBySymbol := map[string]float64{}
	for _, wp := range wps {
		if wp == nil || wp.Type != jumpGateWaypointType {
			continue
		}
		site, serr := constructionRepo.FindByWaypoint(ctx, wp.Symbol, playerID)
		if serr != nil || site == nil {
			continue
		}
		complete := site.IsComplete()
		scans = append(scans, gateSiteScan{Symbol: wp.Symbol, Complete: complete})
		progressBySymbol[wp.Symbol] = site.Progress()
		if !complete {
			break
		}
	}

	target, complete, found := selectGateTarget(scans)
	if !found {
		return snap
	}
	snap.Site = target

	// A built gate is not an active drive target, but it MUST still be observed as complete so
	// obs.ConstructionComplete stays true and derivePhase returns COMPLETE instead of regressing out of
	// GATE (st-drm.19 BUG B: the old `continue` dropped a finished gate, emptying the snapshot).
	if complete {
		snap.Started = true
		snap.Complete = true
		snap.Percent = 100
		return snap
	}

	// The active under-construction gate: read its live progress + pipeline shape.
	snap.Percent = progressBySymbol[target]

	// Pipeline shape: whether `construction start` has created a pipeline for this site, its progress,
	// and how many producing material chains it reveals (the worker-sizing target).
	if status, gerr := s.GetConstructionStatus(ctx, target, playerID); gerr == nil && status != nil {
		if status.PipelineID != nil && *status.PipelineID != "" {
			snap.Started = true
			// Adopted ⇒ the executor has picked the pipeline up. Bootstrap bounces the executor only
			// while a started pipeline is NOT adopted.
			snap.Adopted = pipelineStatusAdopted(status.PipelineStatus)
		}
		snap.MaterialChain = len(status.Materials)
		if status.PipelineProgress != nil {
			snap.Percent = *status.PipelineProgress
		}
		snap.Complete = status.IsComplete
	}
	return snap
}

// gateSiteScan is one home-system JUMP_GATE waypoint's completion state, read from its construction site.
// readBootstrapGateSnapshot builds one per scanned gate and selectGateTarget picks the drive target.
type gateSiteScan struct {
	Symbol   string
	Complete bool
}

// selectGateTarget picks the GATE-phase observation target from the scanned home-system jump gates, in
// priority order: the FIRST still-under-construction gate is the active drive target (complete=false); if
// every scanned gate is already built, the FIRST complete gate is returned (complete=true) so the phase
// still observes COMPLETE. This is the st-drm.19 BUG B fix: a prior era's built gate never masks a live
// under-construction one, but a truly finished gate is never dropped (which would empty the snapshot and
// regress derivePhase out of GATE). found=false only when there is no gate site at all, which holds GATE
// fail-safe (no_gate_site). Pure (no infra), so it is unit-testable in isolation.
func selectGateTarget(scans []gateSiteScan) (symbol string, complete bool, found bool) {
	completeSymbol := ""
	completeFound := false
	for _, scan := range scans {
		if scan.Symbol == "" {
			continue
		}
		if !scan.Complete {
			return scan.Symbol, false, true
		}
		if !completeFound {
			completeFound = true
			completeSymbol = scan.Symbol
		}
	}
	if completeFound {
		return completeSymbol, true, true
	}
	return "", false, false
}

// pipelineStatusAdopted reports whether a construction pipeline's status means the executor has ADOPTED
// it (has taken ownership of its tasks). It is matched to the REAL manufacturing pipeline enum
// (domain/manufacturing/pipeline.go), whose lifecycle is PLANNING -> EXECUTING -> {COMPLETED, FAILED,
// CANCELLED}: a freshly-created pipeline is PLANNING (tasks are still being created, the executor has
// NOT yet picked it up), so it is NOT adopted and the coordinator must bounce the executor to force a
// re-scan + adoption (captain L57). EXECUTING (actively worked) and the terminal COMPLETED/FAILED/
// CANCELLED (the executor already engaged it) are adopted, so a healthy or finished pipeline is never
// needlessly bounced. An empty/absent OR unrecognized status is treated as NOT adopted — the fail-safe
// direction, since skipping a needed bounce stalls the gate while a spurious bounce self-corrects.
func pipelineStatusAdopted(status *string) bool {
	if status == nil || *status == "" {
		return false
	}
	switch manufacturing.PipelineStatus(*status) {
	case manufacturing.PipelineStatusExecuting,
		manufacturing.PipelineStatusCompleted,
		manufacturing.PipelineStatusFailed,
		manufacturing.PipelineStatusCancelled:
		return true
	default:
		// PLANNING (fresh, not yet adopted) or any unrecognized status -> bounce to force adoption.
		return false
	}
}

// --- ConstructionManager: `construction start <site>` (idempotent — resumes an existing pipeline) ---

type bootstrapConstructionManager struct{ server *DaemonServer }

func (c *bootstrapConstructionManager) Start(ctx context.Context, playerID int, site string) error {
	system := shared.ExtractSystemSymbol(site)
	// depth 0 (full production), maxWorkers 0 (unset → planner default), minSupply "" (default floor).
	_, err := c.server.StartConstructionPipeline(ctx, site, playerID, 0, 0, system, "")
	return err
}

// --- ManufacturingController: ensure the executor is running + the L57 adoption bounce ---

type bootstrapManufacturingController struct{ server *DaemonServer }

// EnsureRunning is best-effort: post-sp-jav2 there is no clean bootstrap-launchable "start the
// manufacturing coordinator" verb, so when the executor is down this returns a clear error that the GATE
// phase surfaces as a loud blocker (the captain launches the standing manufacturing coordinator). In the
// normal era the executor is already running, so this is rarely reached — the common path is the bounce.
func (m *bootstrapManufacturingController) EnsureRunning(ctx context.Context, playerID int) error {
	running, err := containerTypeRunning(ctx, m.server.containerRepo, playerID, executorContainerTypes...)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	return fmt.Errorf("construction executor (manufacturing coordinator) is not running and bootstrap has no launch verb for it post-sp-jav2 — launch the standing manufacturing coordinator")
}

// BounceForAdoption restarts the running executor so it re-scans and ADOPTS the freshly-created pipeline
// (captain L57): stop the container, and the daemon's restart-recovery re-adopts it from its persisted
// launch config. This is the wired, correct adoption path.
func (m *bootstrapManufacturingController) BounceForAdoption(ctx context.Context, playerID int) error {
	id, err := firstContainerIDOfType(ctx, m.server.containerRepo, playerID, executorContainerTypes...)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("no running construction executor to bounce for adoption")
	}
	return m.server.StopContainer(id) // daemon restart-recovery re-adopts → re-scans → adopts the pipeline
}

// --- GateWorkerAcquirer: buy a worker hull and dedicate it to the manufacturing fleet ---

type bootstrapGateWorkerAcquirer struct {
	*bootstrapAcquirer
	shipRepo navigation.ShipRepository
}

// BuyForConstruction buys one hull (reuse the asset-agnostic batch-purchase path) and dedicates it to the
// manufacturing fleet so the executor claims it as a gate worker.
func (a *bootstrapGateWorkerAcquirer) BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.bootstrapAcquirer.Buy(ctx, playerID, shipType, yard)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, perr := shared.NewPlayerID(playerID)
	if perr != nil {
		return bought, perr
	}
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, manufacturingFleetTag, pid); derr != nil {
		return bought, derr
	}
	return bought, nil
}

// --- HandoffLauncher: launch the standing coordinators at COMPLETE (autosizer + siting + rebalancer) ---

type bootstrapHandoffLauncher struct{ server *DaemonServer }

func (h *bootstrapHandoffLauncher) LaunchAutosizer(ctx context.Context, playerID int, agentSymbol string) error {
	running, err := containerTypeRunning(ctx, h.server.containerRepo, playerID, container.ContainerTypeFleetAutosizer)
	if err != nil {
		return err
	}
	if running {
		return nil // idempotent: already handed off
	}
	_, err = h.server.FleetAutosizerCoordinator(ctx, playerID, agentSymbol)
	return err
}

// LaunchStandingCoordinators launches the rest of the mature economy's standing brains (siting +
// worker-rebalancer), each idempotent on its own container type so a re-run never double-launches.
func (h *bootstrapHandoffLauncher) LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error {
	if running, err := containerTypeRunning(ctx, h.server.containerRepo, playerID, container.ContainerTypeSitingCoordinator); err != nil {
		return err
	} else if !running {
		if _, err := h.server.SitingCoordinator(ctx, playerID, agentSymbol); err != nil {
			return err
		}
	}
	if running, err := containerTypeRunning(ctx, h.server.containerRepo, playerID, container.ContainerTypeWorkerRebalancerCoordinator); err != nil {
		return err
	} else if !running {
		if _, err := h.server.WorkerRebalancerCoordinator(ctx, playerID, agentSymbol, false); err != nil {
			return err
		}
	}
	return nil
}
