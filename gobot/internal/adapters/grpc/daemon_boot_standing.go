package grpc

// Admiral-selected launch model (a) — the construction-supply drain is a STANDING
// coordinator launched unconditionally at every daemon boot, mirroring how the other standing
// coordinators (probe-sensing, scout-post, bootstrap, ...) already
// auto-start. Bootstrap-EnsureRunning-only launch does not work here: with no active bootstrapper
// the ConstructionCoordinator never runs even once, and RecoverRunningContainers only
// re-adopts containers already PERSISTED as RUNNING, so it finds nothing to recover and a live
// gate-construction pipeline stays unsupplied forever. bootStandingCoordinatorTypes declares the
// boot-launch membership as data, mirroring executorContainerTypes in bootstrap_ports_gate.go.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// launchBootStandingAfterRecovery runs the boot-standing coordinator launches and the
// depot-registry reload on their OWN bounded context, derived from runCtx — never from
// the recovery phase's. Recovery of a large container fleet can exhaust its entire
// shared budget, and boot-standing is the standing coordinators' ONLY activation path
// (at a fresh-era boot, bootstrap's too): inheriting a spent deadline leaves them
// stopped until the next restart. The loud per-launch warnings inside stay loud.
func (s *DaemonServer) launchBootStandingAfterRecovery() {
	ctx, cancel := context.WithTimeout(s.runCtx, 30*time.Second)
	defer cancel()

	// Launch the boot-standing coordinators: unconditional, every boot, regardless
	// of whether a bootstrapper has ever run. Unlike RecoverRunningContainers, this is
	// safely re-runnable every boot — each launch goes through the idempotent EnsureRunning
	// path (skips if already RUNNING/PENDING), so a container just re-adopted by recovery
	// is left alone; only a genuinely-never-launched (or previously-stopped) standing
	// coordinator is started here.
	playerID := s.primaryPlayerID(ctx)
	s.ensureBootStandingCoordinators(ctx, playerID)

	// sp-u9xa: reload the contract-depot routing registry from the durable store on
	// boot (RULINGS #2). The Store owns no in-memory authority, so this re-derives the
	// registry entirely from persisted rows — a restart reconstructs the identical
	// routing the contract engine consults via LoadDepotRegistry. Pure read,
	// fail-open, safely re-runnable every boot; runs here (after recovery) so the boot
	// log reflects the same registry a re-adopted contract coordinator will route on.
	s.reloadDepotRegistryAtBoot(ctx, playerID)

	// Re-assert the player's durable agent identity (headquarters / starting_faction / account_id)
	// into players.metadata, so a row created by any path heals itself from its own /my/agent.
	//
	// Deliberately runs LAST, on its OWN bounded context. Placing it ahead of the launches is the
	// obvious choice — sensing reads players.metadata.headquarters in its cutover — and it is WRONG:
	// this is a live API call, boot-standing shares one 30s budget, and a hung /my/agent ahead of the
	// launches leaves ensureBootStandingCoordinators an expired context and launches NOTHING.
	// Running after costs at most ONE failed sensing tick on a cold row.
	s.syncAgentIdentityAtBoot(ctx, playerID)

	// Release any hull still dedicated to the DELETED probe-buyer fleet. Retiring a coordinator does
	// not rewrite a ships row, so its recruits would otherwise stay tagged for a fleet that no longer
	// exists — driven by nobody, and invisible to sensing's buy path, which admits only "" and
	// sensing_parked. Idempotent (after the first boot nothing matches) and fail-open. Runs here,
	// after the launches, for the same reason the identity sync does: a repair must never be able to
	// starve the coordinators of the shared boot budget.
	s.releaseRetiredProbeBuyerHulls(ctx, playerID)
}

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
	// The probe-sensing coordinator is the fleet's ONE standing sensing engine (successor of
	// the retired market-freshness sizer + frontier expansion pair): it must continuously hold
	// the whitelist-scoped footprint under its freshness target, rotate dormancy against
	// limiter pressure, and pace discovery — so it boot-launches unconditionally like the
	// construction drain. Its launch is idempotent (skips if already RUNNING/PENDING) and
	// every buy runs the shared fail-closed money-guard stack, so an armed auto-start is safe.
	container.ContainerTypeProbeSensingCoordinator,
	// sp-ov8z (epic sp-difa, Auto-pilot Phase 1 — the ARMING half of zero-intervention cold start):
	// the captain-bootstrap coordinator is the MASTER SWITCH of the cold-start machine. Boot-launched
	// unconditionally, it OBSERVES the live world each tick, DERIVES its phase (DATA/INCOME/GATE/
	// EXPANSION — never a stored cursor), drives a cold agent to the jump gate, and at the gate-built
	// EXPANSION hands the mature economy off to the fleet-autosizer + siting + worker-rebalancer, then
	// exits. A mid-era restart in a built world re-observes EXPANSION, ensures the autosizer is
	// running, and exits — so re-launching it every boot is a safe no-op. Its launch is idempotent
	// (skips if already RUNNING/PENDING). THIS is what removes the manual `workflow bootstrap` at
	// every era start.
	container.ContainerTypeBootstrapCoordinator,
	// The capacity reconciler, the fleet autosizer and the scout-post coordinator were all
	// DELETED. None is boot-standing, and nothing restart-surviving depends on them.
	// The opportunity relocator moves trade hulls toward better-earning ground. It belongs here for
	// the same reason the construction drain does — it discovers its own subjects per tick and needs
	// no launch parameter — and it must not sit behind an arming step, because a relocator nobody
	// launches never runs. THE PROBE-BUYER LESSON BELOW DOES NOT APPLY, and the difference is the
	// point: that one SPENT, so nothing had to notice it. This one spends NOTHING (movement through
	// existing primitives), and its worst tick is bounded by the concurrency cap. Boot-launching it
	// during a cold start is harmless: with no trade hulls it observes an empty fleet and re-derives.
	container.ContainerTypeOpportunityRelocator,
	// The probe-buyer-fleet coordinator was a member until it was RETIRED and DELETED. Boot-standing
	// is what made its cost unbounded: nothing had to launch it, so nothing had to notice it before
	// it had bought a fleet's worth of probes. Probe supply belongs to the sensing coordinator above.
}

// ensureBootStandingCoordinators launches every boot-standing coordinator type not already
// running, for the given player. Safe to call every boot: each type's launch is idempotent, so a
// restart adopts the existing container instead of double-launching. A launch failure is logged
// and non-fatal — one type's failure must never block another's launch attempt, and must never
// fail daemon startup.
func (s *DaemonServer) ensureBootStandingCoordinators(ctx context.Context, playerID int) {
	// Genesis cold-boot guard. On a fresh DB with no player row,
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
		case container.ContainerTypeProbeSensingCoordinator:
			s.ensureProbeSensingStanding(ctx, playerID)
		case container.ContainerTypeBootstrapCoordinator:
			s.ensureBootstrapStanding(ctx, playerID)
		case container.ContainerTypeOpportunityRelocator:
			s.ensureOpportunityRelocatorStanding(ctx, playerID)
		}
	}
}

// ensureOpportunityRelocatorStanding launches the standing opportunity relocator (sp-zvywu Part 2)
// when none is already running for the player. Idempotent via the same containerTypeRunning
// pre-check every other standing coordinator uses, so a warm restart re-adopts the existing one (via
// RecoverRunningContainers) rather than double-launching a twin whose second reconcile loop would
// race the first over the same hulls and the same concurrency budget. All-default launch (RULINGS
// #5): the reconciler fills in its documented thresholds and caps. A launch failure is logged and
// non-fatal — it must never fail daemon startup.
func (s *DaemonServer) ensureOpportunityRelocatorStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeOpportunityRelocator)
	if err != nil {
		fmt.Printf("Warning: failed to check opportunity relocator state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.OpportunityRelocatorCoordinator(ctx, playerID); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing opportunity relocator: %v\n", lerr)
	}
}

// ensureBootstrapStanding launches the standing captain-bootstrap coordinator (sp-ov8z) when none is
// already running for the player. Idempotent via the same containerTypeRunning pre-check the
// market-freshness sizer uses, so a warm restart re-adopts the existing one (via
// RecoverRunningContainers) instead of double-launching. The agent symbol is resolved from the player
// row because the bootstrap threads it into the GATE hand-off. A launch failure is logged and non-fatal.
func (s *DaemonServer) ensureBootstrapStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeBootstrapCoordinator)
	if err != nil {
		fmt.Printf("Warning: failed to check bootstrap coordinator state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.BootstrapCoordinator(ctx, playerID, s.agentSymbolForPlayer(ctx, playerID)); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing bootstrap coordinator: %v\n", lerr)
	}
}

// ensureProbeSensingStanding launches the standing probe-sensing coordinator when none is already
// running for the player. Idempotent via the same containerTypeRunning pre-check the other standing
// coordinators use, so a warm restart re-adopts the existing one (via RecoverRunningContainers)
// instead of double-launching. All-default launch (RULINGS #5): the coordinator fills in its
// documented defaults; the knobs are live via `tune --operation sensing`. A launch failure is
// logged and non-fatal.
func (s *DaemonServer) ensureProbeSensingStanding(ctx context.Context, playerID int) {
	running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeProbeSensingCoordinator)
	if err != nil {
		fmt.Printf("Warning: failed to check probe-sensing coordinator state: %v\n", err)
		return
	}
	if running {
		return
	}
	if _, lerr := s.ProbeSensingCoordinator(ctx, playerID); lerr != nil {
		fmt.Printf("Warning: failed to launch boot-standing probe-sensing coordinator: %v\n", lerr)
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
