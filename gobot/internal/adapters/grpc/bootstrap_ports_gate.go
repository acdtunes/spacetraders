package grpc

// This file holds the GATE-phase concrete adapters (Slice 3, sp-ysgb.2) that bind the bootstrap
// coordinator's GATE ports to existing daemon capabilities — building NO new construction, fabrication,
// or fleet logic (the captain-verification reuse gate). Each adapter is a thin wrapper over a surface
// that already ships: `construction start` (StartConstructionPipeline), the manufacturing/goods-factory
// coordinator (the construction executor), fleet dedication (AssignFleet), and the standing-coordinator
// launches (FleetAutosizer / Siting / WorkerRebalancer).
//
// EXECUTOR NOTE (sp-382j): construction pipelines are worked by the dedicated construction-supply drain
// (ContainerTypeConstructionCoordinator) — a thin drain on the shared ProductionExecutor engine that
// re-polls READY DELIVER_TO_CONSTRUCTION tasks every tick. EnsureRunning launches it when down (a fresh
// start immediately adopts the gate pipeline by re-polling), closing the post-sp-jav2 gap that left the
// gate EXECUTING@0% forever. Adoption keys on this drain RUNNING, not the pipeline-status string. The L57
// bounce is now effectively vestigial for construction (a re-polling drain adopts continuously, so the
// gate reaches EnsureRunning, never the bounce), but BounceForAdoption is kept wired for safety.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
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

// executorContainerTypes are the container types that run the construction executor. Post-sp-382j
// this is the dedicated construction-supply drain (ContainerTypeConstructionCoordinator) — NOT the
// vestigial manufacturing/goods-factory coordinators, which never worked construction pipelines. A
// running container of this type means the executor is up (and, because the drain re-polls its
// worklist every tick, actively adopting).
var executorContainerTypes = []container.ContainerType{
	container.ContainerTypeConstructionCoordinator,
}

// containerTypeRunning reports whether a RUNNING-or-PENDING container of any of the given types exists
// for the player (the generic form of contractFleetCoordinatorRunning).
func containerTypeRunning(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int, types ...container.ContainerType) (bool, error) {
	id, err := firstContainerIDOfType(ctx, repo, playerID, types...)
	return id != "", err
}

// matchesAnyContainerType reports whether containerType equals any of the given types.
func matchesAnyContainerType(containerType string, types []container.ContainerType) bool {
	for _, t := range types {
		if containerType == string(t) {
			return true
		}
	}
	return false
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
			if matchesAnyContainerType(s.ContainerType, types) {
				return s.ID, nil
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
	for _, wp := range wps {
		if wp == nil || wp.Type != jumpGateWaypointType {
			continue
		}
		site, serr := constructionRepo.FindByWaypoint(ctx, wp.Symbol, playerID)
		if serr != nil || site == nil {
			continue
		}
		// The gate site is the jump gate that still needs materials. A finished gate is skipped so a
		// prior era's built gate never reads as the current target.
		if site.IsComplete() {
			continue
		}
		snap.Site = wp.Symbol
		snap.Complete = false
		snap.Percent = site.Progress()

		// Pipeline shape: whether `construction start` has created a pipeline for this site, its progress,
		// and how many producing material chains it reveals (the worker-sizing target).
		if status, gerr := s.GetConstructionStatus(ctx, wp.Symbol, playerID); gerr == nil && status != nil {
			if status.PipelineID != nil && *status.PipelineID != "" {
				snap.Started = true
				// Adopted keys on a RUNNING construction executor, NOT the pipeline-status string
				// (sp-382j): the planner sets EXECUTING when it creates the pipeline, but that
				// pipeline is INERT until a drain is running. Reading EXECUTING as adopted (the old
				// pipelineStatusAdopted) silently skipped the launch and left the gate EXECUTING@0%
				// forever. A running ConstructionCoordinator re-polls and works the tasks, so its
				// presence is the adoption signal.
				running, _ := containerTypeRunning(ctx, s.containerRepo, playerID, executorContainerTypes...)
				snap.Adopted = constructionExecutorAdopted(running, status.PipelineStatus)
			}
			snap.MaterialChain = len(status.Materials)
			if status.PipelineProgress != nil {
				snap.Percent = *status.PipelineProgress
			}
			snap.Complete = status.IsComplete
		}
		return snap
	}
	return snap
}

// constructionExecutorAdopted reports whether the gate pipeline is being worked. Adoption keys on
// a RUNNING construction executor (a ConstructionCoordinator container), NOT the pipeline-status
// string (sp-382j): the planner sets EXECUTING when it creates the pipeline, but that pipeline is
// inert until a drain is running — reading EXECUTING as adopted was the false-adoption bug that
// silently skipped the launch. The drain re-polls its worklist every tick, so a running drain is
// continuously adopting; keying on live percent instead would thrash-bounce a legitimately
// supply-starved drain (a restart cannot conjure supply). pipelineStatus is kept in the signature
// for symmetry with the observation but is deliberately NOT consulted.
func constructionExecutorAdopted(coordinatorRunning bool, _ *string) bool {
	return coordinatorRunning
}

// --- ConstructionManager: `construction start <site>` (idempotent — resumes an existing pipeline) ---

type bootstrapConstructionManager struct{ server *DaemonServer }

func (c *bootstrapConstructionManager) Start(ctx context.Context, playerID int, site string) error {
	system := shared.ExtractSystemSymbol(site)
	// depth 0 (full production), maxWorkers 0 (unset → planner default), minSupply "" (default floor).
	_, err := c.server.StartConstructionPipeline(ctx, site, playerID, 0, 0, system, "", nil)
	return err
}

// --- ManufacturingController: ensure the executor is running + the L57 adoption bounce ---

type bootstrapManufacturingController struct{ server *DaemonServer }

// EnsureRunning launches the standing construction-supply drain (sp-382j) when it is down: a fresh
// start immediately begins re-polling READY DELIVER_TO_CONSTRUCTION tasks and adopting the gate
// pipeline. This closes the post-sp-jav2 gap — the GATE phase now has a real, launchable executor
// instead of a dead-end error. Idempotent: it launches only when no drain is already RUNNING/PENDING.
// The empty system lets the drain derive its operating system per-tick from each task's construction
// site, so the bootstrap need not resolve the home system here.
func (m *bootstrapManufacturingController) EnsureRunning(ctx context.Context, playerID int) error {
	running, err := containerTypeRunning(ctx, m.server.containerRepo, playerID, executorContainerTypes...)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	_, err = m.server.ConstructionCoordinator(ctx, playerID, "")
	return err
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

// --- WorkerRepurposer: re-dedicate a contract hauler to the manufacturing fleet (the executor claims it) ---

type bootstrapWorkerRepurposer struct{ shipRepo navigation.ShipRepository }

func (r *bootstrapWorkerRepurposer) RepurposeToConstruction(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	// Re-tag from the contract fleet to the manufacturing fleet — the executor's worker pool selects on
	// this tag, so the hull becomes a gate-construction worker (reuse fleet assign, no new logic).
	return r.shipRepo.AssignFleet(ctx, shipSymbol, manufacturingFleetTag, pid)
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
