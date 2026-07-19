package grpc

// sp-382j: Admiral-selected launch model (a) — the construction-supply drain is now a STANDING
// coordinator launched unconditionally at every daemon boot, mirroring how the other standing
// coordinators (GoodsFactoryCoordinator/StartGoodsFactory, SitingCoordinator, ...) already
// auto-start. Before this, launch was bootstrap-EnsureRunning-only: with no active bootstrapper
// the ConstructionCoordinator never ran even once, so RecoverRunningContainers (which only
// re-adopts containers already PERSISTED as RUNNING) found nothing to recover, leaving a live
// gate-construction pipeline unsupplied forever. bootStandingCoordinatorTypes declares the
// boot-launch membership as data, mirroring executorContainerTypes in bootstrap_ports_gate.go.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootStandingCoordinatorTypes are the container types launched unconditionally at every daemon
// boot (Start()), regardless of whether a bootstrapper has ever run. Each launch reuses the
// idempotent EnsureRunning path (skips if already RUNNING/PENDING), so a restart never
// double-launches — it simply re-adopts the container already running from the prior boot.
//
// The STANDING stocker (sp-k1ka) is deliberately NOT a member here: unlike the player-scoped
// construction drain (which discovers idle haulers per-tick and needs no launch parameters), a
// stocker is pinned to a SPECIFIC dedicated hull + home warehouse, so there is nothing to
// unconditionally boot-launch without captain-supplied config. Its "survives restart" comes
// instead from the persisted `standing` launch config + RecoverRunningContainers, which re-adopts
// the RUNNING stocker row and rebuilds it STANDING via buildCommandForType (RULINGS #2). The
// captain launches it once (`workflow stocker --standing`); it then self-sustains and re-adopts
// across restarts with no manual relaunch.
var bootStandingCoordinatorTypes = []container.ContainerType{
	container.ContainerTypeConstructionCoordinator,
	// sp-orgp: the market-freshness auto-sizer is a genuinely STANDING coordinator (it must
	// hold every market under an SLA continuously), so it boot-launches unconditionally like
	// the construction drain. Its launch is idempotent (skips if already RUNNING/PENDING) and
	// every action it takes is guarded — money guards on buys, and a fail-safe that never
	// mass-retires on an empty census — so an armed auto-start is safe.
	container.ContainerTypeMarketFreshnessSizer,
	// sp-ov8z (epic sp-difa, Auto-pilot Phase 1 — the ARMING half of zero-intervention cold start):
	// the captain-bootstrap coordinator is the MASTER SWITCH of the cold-start machine. Boot-launched
	// unconditionally, it OBSERVES the live world each tick, DERIVES its phase (DATA/INCOME/GATE/
	// COMPLETE — never a stored cursor), drives a cold agent to the jump gate, and at GATE hands the
	// mature economy off to the fleet-autosizer + siting + worker-rebalancer, then exits. A mid-era
	// restart in a built world re-observes COMPLETE, ensures the autosizer is running, and exits — so
	// re-launching it every boot is a safe no-op. Its launch is idempotent (skips if already RUNNING/
	// PENDING). THIS is what removes the manual `workflow bootstrap` at every era start.
	container.ContainerTypeBootstrapCoordinator,
	// sp-ov8z: the capacity reconciler is the only standing brain with NO other auto-launch path — the
	// bootstrap GATE hand-off launches the autosizer/siting/rebalancer but NOT this — so it must be
	// boot-standing for a zero-intervention cold start. It reconciles the contract-delivery topology
	// toward a computed desired topology and IDLES when converged (an empty desired during cold start ⇒
	// no actions); tier-4 capital stays gated behind the human-approved proposal path (no auto-buy).
	// Auto-arming it is safe ONLY because sp-2jrz (stop-is-complete-retire) landed: a deploy re-adopts a
	// live reconciler unchanged (the INTERRUPTED path never retires), and a decommission STOP retires it
	// cleanly. This DELIBERATELY reverses the earlier "st-fyr deploy-inert hard requirement". Its launch
	// is idempotent (skips if already RUNNING/PENDING); its own creation path also refuses a second live
	// reconciler. FLAGGED (sp-ov8z): once boot-standing, a bare STOP no longer decommissions it across a
	// restart — boot re-launches it — so a durable decommission additionally needs config dry_run/disable
	// (cf. the sp-udgc demand-driven-boot-guard pattern).
	//
	// The fleet autosizer is DELIBERATELY NOT a member: the bootstrap GATE hand-off already launches it
	// at the mature-economy phase; boot-standing it would launch it prematurely during DATA/INCOME.
	container.ContainerTypeCapacityReconciler,
	// sp-9ujl (epic sp-difa, Auto-pilot Phase 1): the scout-post coordinator MANS the standing
	// freshness posts the MarketFreshnessSizer (above) only DECLARES — each tick it assigns a probe to
	// every unmanned slot (SetAssignedHull), partitions the system's markets across the post's hulls,
	// and drives the P90 rescans + idle-probe re-tasking. Without it a cold-start post stays UNMANNED
	// (assigned_hull/tour_container_id/primary_partition all empty), so freshness coverage has no
	// standing owner — the sizer's declarer is armed but its manner never was. Same profile as the
	// members above: genuinely standing and self-adopting (sp-cxpq: persisted container_id re-adopted by
	// RecoverRunningContainers across restart). Launch is idempotent — the boot path skips when one is
	// already RUNNING/PENDING, and its creation path's own double-launch guard (sp-9ujl) refuses a twin
	// whose second reconcile loop would fight the first over the same posts and idle probes.
	container.ContainerTypeScoutPostCoordinator,
}

// ensureBootStandingCoordinators launches every boot-standing coordinator type not already
// running, for the given player. Safe to call every boot: each type's launch is idempotent, so a
// restart adopts the existing container instead of double-launching. A launch failure is logged
// and non-fatal — one type's failure must never block another's launch attempt, and must never
// fail daemon startup.
func (s *DaemonServer) ensureBootStandingCoordinators(ctx context.Context, playerID int) {
	// sp-ls7x: genesis cold-boot guard. On a fresh DB with no player row,
	// primaryPlayerID() returns 0; every standing coordinator is player-scoped and
	// building one with id 0 hits MustNewPlayerID(0), which panics. Skip them until
	// a player exists — the next boot after registration launches them. No-op for
	// the normal path (playerID>0 behaves exactly as before).
	if playerID <= 0 {
		fmt.Println("No player yet - skipping boot-standing coordinators (genesis cold-boot)")
		return
	}

	for _, ct := range bootStandingCoordinatorTypes {
		switch ct {
		case container.ContainerTypeConstructionCoordinator:
			mc := &bootstrapManufacturingController{server: s}
			if err := mc.EnsureRunning(ctx, playerID); err != nil {
				fmt.Printf("Warning: failed to launch boot-standing construction coordinator: %v\n", err)
			}
		case container.ContainerTypeMarketFreshnessSizer:
			// Idempotent: skip if a sizer is already RUNNING/PENDING (a warm restart re-adopts
			// it from its persisted config via RecoverRunningContainers instead). All-default
			// knobs (RULINGS #5); the coordinator fills in its documented defaults.
			running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeMarketFreshnessSizer)
			if err != nil {
				fmt.Printf("Warning: failed to check market-freshness sizer state: %v\n", err)
			} else if !running {
				if _, lerr := s.MarketFreshnessSizerCoordinator(ctx, playerID, 0, false, 0, 0, 0, 0, 0); lerr != nil {
					fmt.Printf("Warning: failed to launch boot-standing market-freshness sizer: %v\n", lerr)
				}
			}
		case container.ContainerTypeBootstrapCoordinator:
			s.ensureBootstrapStanding(ctx, playerID)
		case container.ContainerTypeCapacityReconciler:
			s.ensureCapacityReconcilerStanding(ctx, playerID)
		case container.ContainerTypeScoutPostCoordinator:
			s.ensureScoutPostStanding(ctx, playerID)
		}
	}

	// sp-hoc6: ALONGSIDE the construction drain, keep the gate's source EXPORT-factories fed with their
	// import inputs (standing InputsOnly goods_factory feeders) so the drain's buying of the gate output
	// stays under the buy-ceiling (sp-layd) instead of depleting export supply. Config-driven feeder set
	// (RULINGS #5), idempotent, and restart-resilient the same way the drain is — the feeders are
	// goods_factory coordinators re-adopted by RecoverRunningContainers, and this pass skips any already
	// running (RULINGS #2). Runs here, after recovery, so a warm restart re-adopts rather than duplicates.
	s.ensureGateSourceFeeders(ctx, playerID)
}

// ensureBootstrapStanding launches the standing captain-bootstrap coordinator (sp-ov8z) when none is
// already running for the player. Idempotent via the same containerTypeRunning pre-check the
// market-freshness sizer uses, so a warm restart re-adopts the existing one (via
// RecoverRunningContainers) instead of double-launching. Auto-armed (dryRun=false) — config.yaml
// [bootstrap] dry_run can still force observe-only. The agent symbol is resolved from the player row
// because the bootstrap threads it into the GATE hand-off. A launch failure is logged and non-fatal.
func (s *DaemonServer) ensureBootstrapStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeBootstrapCoordinator)
	if err != nil {
		fmt.Printf("Warning: failed to check bootstrap coordinator state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.BootstrapCoordinator(ctx, playerID, s.agentSymbolForPlayer(ctx, playerID), false); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing bootstrap coordinator: %v\n", lerr)
	}
}

// ensureCapacityReconcilerStanding launches the standing capacity reconciler (sp-ov8z) when none is
// already running for the player. Idempotent via the containerTypeRunning pre-check (the reconciler's
// own creation path also refuses a second live reconciler), so a warm restart re-adopts the existing
// one instead of double-launching. Auto-armed (dryRun=false) — config.yaml [capacity_reconciler]
// dry_run can still force observe-only. A launch failure is logged and non-fatal.
func (s *DaemonServer) ensureCapacityReconcilerStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeCapacityReconciler)
	if err != nil {
		fmt.Printf("Warning: failed to check capacity reconciler state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.CapacityReconcilerCoordinator(ctx, playerID, false); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing capacity reconciler: %v\n", lerr)
	}
}

// ensureScoutPostStanding launches the standing scout-post coordinator (sp-9ujl) when none is already
// running for the player. Idempotent via the containerTypeRunning pre-check (the coordinator's own
// creation path also refuses a second live instance), so a warm restart re-adopts the existing one via
// RecoverRunningContainers instead of double-launching. tickIntervalSecs=0 uses the coordinator's
// documented default (RULINGS #5); the [scouting] config.yaml knobs are injected in buildCommandForType
// (resolveScoutingConfig). A launch failure is logged and non-fatal.
func (s *DaemonServer) ensureScoutPostStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeScoutPostCoordinator)
	if err != nil {
		fmt.Printf("Warning: failed to check scout-post coordinator state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.ScoutPostCoordinator(ctx, playerID, 0); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing scout-post coordinator: %v\n", lerr)
	}
}

// agentSymbolForPlayer resolves the agent symbol for a player at boot — needed by the coordinators
// whose launch threads it into a downstream hand-off (bootstrap → GATE hand-off). Best-effort: at a
// real boot the player row exists and this resolves the symbol; a lookup miss (nil repo / not found)
// yields "" rather than blocking the launch (the coordinator is keyed by player id, not the symbol).
func (s *DaemonServer) agentSymbolForPlayer(ctx context.Context, playerID int) string {
	if s.playerRepo == nil {
		return ""
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return ""
	}
	p, err := s.playerRepo.FindByID(ctx, pid)
	if err != nil || p == nil {
		return ""
	}
	return p.AgentSymbol
}
