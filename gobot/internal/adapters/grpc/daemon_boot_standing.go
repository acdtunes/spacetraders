package grpc

// sp-382j: Admiral-selected launch model (a) — the construction-supply drain is now a STANDING
// coordinator launched unconditionally at every daemon boot, mirroring how the other standing
// coordinators (probe-sensing, scout-post, bootstrap, ...) already
// auto-start. Before this, launch was bootstrap-EnsureRunning-only: with no active bootstrapper
// the ConstructionCoordinator never ran even once, so RecoverRunningContainers (which only
// re-adopts containers already PERSISTED as RUNNING) found nothing to recover, leaving a live
// gate-construction pipeline unsupplied forever. bootStandingCoordinatorTypes declares the
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

	// Launch the boot-standing coordinators (sp-382j): unconditional, every boot, regardless
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

	// sp-0eufi: re-assert the player's durable agent identity (headquarters / starting_faction /
	// account_id) into players.metadata, so a row created by any path — including one that predates
	// the identity write entirely — heals itself from its own /my/agent.
	//
	// Deliberately runs LAST, and on its OWN bounded context. The obvious placement is ahead of the
	// launches, since probe-sensing reads players.metadata.headquarters in its CUTOVER and a missing
	// key aborts its whole first tick. That ordering is WRONG, and
	// TestLaunchBootStandingAfterRecovery_SurvivesExhaustedRecoveryContext proves it: this is a live
	// API call, boot-standing shares one 30s budget, and a slow or hung /my/agent ahead of the
	// launches consumes it — leaving ensureBootStandingCoordinators an expired context and launching
	// NOTHING. Buying one clean sensing tick at the risk of starting no coordinators at all is a bad
	// trade, and it would break the very sensing engine this fix exists to revive.
	//
	// Running after costs at most ONE failed sensing tick on a cold row: the engine re-reconciles
	// every 30s, the read is idempotent, and the error it logs now names the key and the remedy.
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
	// sp-y2ptq (epic sp-9le3x): the capacity reconciler was DELETED (dedicated contract scaler replaces
	// it; jump gate COMPLETE). It is no longer boot-standing — nothing restart-surviving depends on it.
	//
	// The fleet autosizer is DELIBERATELY NOT a member: the bootstrap GATE hand-off already launches it
	// at the mature-economy phase; boot-standing it would launch it prematurely during DATA/INCOME.
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
	// sp-zvywu Part 2: the opportunity relocator is the standing meta-planner that moves trade hulls
	// toward measurably better ground. It belongs here for the same reason the construction drain does
	// — it discovers its own subjects per tick (every trade hull, every reachable region) and needs no
	// captain-supplied launch parameter — and it MUST be here rather than behind an arming step,
	// because a relocator nobody launches is a relocator that never runs, and 90% of charted space is
	// unpriced ground it exists to reach. Launch is idempotent (skipped when one is already
	// RUNNING/PENDING) and the container re-adopts across restarts via RecoverRunningContainers.
	//
	// THE PROBE-BUYER LESSON (below) DOES NOT APPLY, and the difference is the point: what made that
	// coordinator's cost unbounded was that it SPENT, so nothing had to notice it before it had bought
	// 9 probes for 245,316 credits. The relocator spends NOTHING — it moves hulls through the existing
	// occupancy/reposition primitives — and its worst tick is bounded by max_concurrent_relocations (2)
	// hulls in transit, each behind a 1.5x uplift bar, an NPV floor, a 90-minute per-hull cooldown and
	// the anti-herd cap. Its cost is bounded travel time, not credits (RULINGS #4 untouched).
	//
	// Boot-launching it during a COLD start is harmless, unlike the fleet autosizer (deliberately not a
	// member, because it would buy prematurely during DATA/INCOME): with no trade-dedicated hulls yet,
	// the relocator observes an empty fleet, relocates nothing, and re-derives 15 minutes later.
	container.ContainerTypeOpportunityRelocator,
	// The probe-buyer-fleet coordinator (sp-f082y) was a member here until it was RETIRED and
	// DELETED (Admiral 2026-07-28). Boot-standing it is precisely what made its cost unbounded:
	// nothing had to launch it, so nothing had to notice it, and the first tick after bootstrap
	// reached EXPANSION it bought 9 probes for 245,316 credits in five minutes. Probe supply belongs
	// to the sensing coordinator above, which buys only what its own placements need and reuses
	// hulls it already owns first.
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
		case container.ContainerTypeProbeSensingCoordinator:
			s.ensureProbeSensingStanding(ctx, playerID)
		case container.ContainerTypeBootstrapCoordinator:
			s.ensureBootstrapStanding(ctx, playerID)
		case container.ContainerTypeScoutPostCoordinator:
			s.ensureScoutPostStanding(ctx, playerID)
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
