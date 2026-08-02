package grpc

// This file holds the GATE-phase concrete adapters (Slice 3, sp-ysgb.2) that bind the bootstrap
// coordinator's GATE ports to existing daemon capabilities — building NO new construction, fabrication,
// or fleet logic (the captain-verification reuse gate). Each adapter is a thin wrapper over a surface
// that already ships: `construction start` (StartConstructionPipeline), the construction-supply drain
// (the construction executor), fleet dedication (AssignFleet), and the standing fleet-autosizer launch.
//
// EXECUTOR NOTE: construction pipelines are worked by the dedicated construction-supply drain
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
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
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

// contractScalerTargetFor returns the contract auto-scaler's live ACHIEVABLE fleet target for the player
// (sp-gm7r) — min(the scaler's fixed plan slots, the scaler's live contract_fleet_max_hulls ceiling) — the
// bar bootstrap's GATE entry measures the full contract fleet (delivery haulers + depot hulls) against. It
// FAILS CLOSED at 0 when no scaler is running (an unknown target must never gate). Because the warehouse
// deepens to fill the ceiling budget, min(planSlots, ceiling) equals the scaler's own Target(planSize,
// ceiling) for any op with ≥1 park — so no role resolution is needed. It reads the scaler container's OWN
// config so the target tracks a live contract_fleet_max_hulls tune, mirroring the scaler's liveCeiling read;
// a config-read error falls back to the plan-vs-default-ceiling bound rather than gate blind.
func contractScalerTargetFor(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int) int {
	id, err := firstContainerIDOfType(ctx, repo, playerID, container.ContainerTypeContractScaler)
	if err != nil || id == "" {
		return 0 // fail-closed: no scaler ⇒ never gate on an unknown target
	}
	ceiling := contractScalerCmd.DefaultContractFleetMaxHulls
	if snap, serr := (&ContainerConfigReader{containerRepo: repo}).Snapshot(ctx, id, playerID); serr == nil {
		if v := snap.PositiveIntOrZero("contract_fleet_max_hulls"); v > 0 {
			ceiling = v
		}
	}
	planSlots := contractscaler.MaxDeliveryHulls + contractscaler.WarehouseUnits + contractscaler.StockerUnits
	return min(planSlots, ceiling)
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
	builtSite := ""
	for _, wp := range wps {
		if wp == nil || wp.Type != jumpGateWaypointType {
			continue
		}
		site, serr := constructionRepo.FindByWaypoint(ctx, wp.Symbol, playerID)
		if serr != nil || site == nil {
			continue
		}
		// A finished gate is never the construction TARGET — but it IS the EXPANSION world signal
		// (sp-feiy7): the live-API read means this era's home gate is genuinely built, so remember it.
		// Reporting it (below, when no under-construction gate supersedes it) is what makes
		// obs.ConstructionComplete MONOTONE: forgetting a built gate rather than recording it here
		// leaves the snapshot zero-valued, and a post-completion tick or restart re-derives
		// COLDSTART (phase flap).
		// Era-safe: FindByWaypoint is a live API read, so a prior era's gate can never appear here.
		if site.IsComplete() {
			builtSite = wp.Symbol
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
	// No under-construction gate, but the home gate IS built: report it as the terminal EXPANSION
	// signal so the derived phase stays monotone across post-completion ticks and restarts.
	// An under-construction gate (returned above) always supersedes — a built gate is never a target.
	if builtSite != "" {
		snap.Site = builtSite
		snap.Complete = true
		snap.Percent = 100
	}
	return snap
}

// constructionExecutorAdopted reports whether the gate pipeline is being worked. Adoption keys on
// a RUNNING construction executor (a ConstructionCoordinator container), NOT the pipeline-status
// string: the planner sets EXECUTING when it creates the pipeline, but that pipeline is
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

// EnsureRunning launches the standing construction-supply drain when it is down: a fresh
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

// --- GateSurplusReleaser: un-dedicate surplus IDLE manufacturing hulls to the idle pool (sp-mxflh) ---

// bootstrapGateSurplusReleaser implements the coordinator's GateSurplusReleaser: it un-dedicates the gate's
// surplus IDLE manufacturing hulls back to the UNDEDICATED idle pool (AssignFleet→"", the single write path,
// RULINGS #3), from where the contract scaler's IdleHullReclaimer adopts them into the contract fleet before
// it buys — the zero-buy re-balance. It mirrors contractScalerDeliveryReleaser (un-dedicate to the idle pool)
// and is the OPPOSITE direction of bootstrapWorkerRepurposer (which re-dedicates TO manufacturing). It
// RE-GUARDS every requested hull at release time — still manufacturing-dedicated, still idle, not in transit —
// so a hull that picked up a construction task since the observation is never yanked mid-task. Un-dedicate is
// FREE: no spend, never cushion-gated. (The command frigate can never appear here: the observer never counts
// it as a gate worker, so it is never in the requested set.)
type bootstrapGateSurplusReleaser struct{ shipRepo navigation.ShipRepository }

// ReleaseSurplusGateWorkers un-dedicates the requested manufacturing hulls that are still idle, returning how
// many it released. A fleet-read error surfaces (hold the release fail-closed); an AssignFleet error stops
// early and returns the partial count (a hull left dedicated is safe — re-balanced a later tick).
//
// It does NOT require cargo capacity, because the destination does its own check: the contract scaler's
// isReclaimable — the adopter of everything this drops in the idle pool — already refuses a 0-cargo hull.
// The trade redirect below has no such downstream filter and therefore carries the guard itself.
func (r *bootstrapGateSurplusReleaser) ReleaseSurplusGateWorkers(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	return r.retagGateWorkers(ctx, playerID, shipSymbols, "", false)
}

// ReleaseGateWorkersToTrade re-dedicates the requested manufacturing hulls to the TRADE fleet — the
// EXPANSION hand-off's redirect. It names a destination rather than clearing to the idle pool
// because at EXPANSION nothing would pick them up: the trade coordinator partitions on hulls ALREADY
// tagged "trade", the fleet autosizer tags only hulls it BUYS, and the capacity reconciler that once
// auto-pinned idle hulls was deleted (sp-y2ptq) — an un-dedicated gate hull would idle indefinitely
// unless the contract scaler happened to be mid-ramp. Same guards as the surplus release, plus the
// cargo-capacity floor (a 0-cargo hull cannot trade, and unlike the idle pool the trade fleet has no
// downstream predicate to reject it).
func (r *bootstrapGateSurplusReleaser) ReleaseGateWorkersToTrade(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	return r.retagGateWorkers(ctx, playerID, shipSymbols, tradeFleetTag, true)
}

// retagGateWorkers is the one guarded write both gate-worker re-dedications share, so their safety
// guards can never drift apart. It RE-GUARDS every requested hull against the LIVE fleet — still
// manufacturing-dedicated (never touch one re-tagged since the observation), still idle and not in
// transit (never yank a hull mid-delivery) — and writes through the single AssignFleet path (RULINGS
// #3). requireCargo additionally skips cargo-incapable hulls. Fail-closed on a fleet-read error (write
// nothing on an unknown fleet); an AssignFleet error stops early and returns the PARTIAL count, since a
// hull left construction-dedicated is safe and is retried on a later tick.
func (r *bootstrapGateSurplusReleaser) retagGateWorkers(ctx context.Context, playerID int, shipSymbols []string, targetFleet string, requireCargo bool) (int, error) {
	if len(shipSymbols) == 0 {
		return 0, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err // fail-closed: never write on an unknown fleet
	}
	requested := make(map[string]bool, len(shipSymbols))
	for _, symbol := range shipSymbols {
		requested[symbol] = true
	}
	released := 0
	for _, ship := range ships {
		if !requested[ship.ShipSymbol()] {
			continue // never touch a hull outside the selected set
		}
		if ship.DedicatedFleet() != manufacturingFleetTag {
			continue // re-tagged/adopted since the observation → skip
		}
		if !ship.IsIdle() || ship.IsInTransit() {
			continue // picked up a construction task since the observation → never yank mid-task
		}
		if requireCargo && ship.CargoCapacity() <= 0 {
			continue // cargo-incapable: a probe pinned to a hauling fleet can never earn there
		}
		if contract.IsCommandHull(ship) {
			continue // RULINGS #7: the flagship is excluded from every re-dedication path
		}
		if err := r.shipRepo.AssignFleet(ctx, ship.ShipSymbol(), targetFleet, pid); err != nil {
			return released, fmt.Errorf("re-dedicate gate worker %s → %q: %w", ship.ShipSymbol(), targetFleet, err)
		}
		released++
	}
	return released, nil
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

// LaunchContractScaler launches the standing dedicated contract auto-scaler during the cold-start
// scaling window (unconditional). Idempotent on its own container type — this running check IS
// where the once-only guarantee lives, so a per-tick re-call never double-launches a second ramp loop
// fighting the first over the same treasury cushion. The coordinator then survives restarts via the
// persisted-container recovery idiom (launched once, runs forever).
func (h *bootstrapHandoffLauncher) LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error {
	running, err := containerTypeRunning(ctx, h.server.containerRepo, playerID, container.ContainerTypeContractScaler)
	if err != nil {
		return err
	}
	if running {
		return nil // idempotent: already launched
	}
	_, err = h.server.ContractScalerCoordinator(ctx, playerID, agentSymbol)
	return err
}

// LaunchTradeFleetCoordinator launches the standing trade-fleet coordinator at the cold-start trade-seed
// (sp-192k4), so the freshly-seeded trade hull is picked up and put on a continuous tour. Idempotent on its
// own container type — a re-run (or a stale observation) never double-launches a second coordinator fighting
// the first over the same 'trade'-dedicated hulls. The coordinator then survives restarts via the
// persisted-container recovery idiom (launched once, runs forever) — mirroring LaunchContractScaler.
func (h *bootstrapHandoffLauncher) LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error {
	running, err := containerTypeRunning(ctx, h.server.containerRepo, playerID, container.ContainerTypeTradeFleetCoordinator)
	if err != nil {
		return err
	}
	if running {
		return nil // idempotent: already launched
	}
	_, err = h.server.TradeFleetCoordinator(ctx, playerID, agentSymbol)
	return err
}

// LaunchStandingCoordinators has nothing left to launch: it is retained returning nil so the
// bootstrap GATE hand-off's control flow and port contract are unchanged. The fleet-autosizer
// hand-off runs through its own dedicated launcher.
func (h *bootstrapHandoffLauncher) LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error {
	return nil
}
