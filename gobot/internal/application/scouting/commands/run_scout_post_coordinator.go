package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	shipqueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

const (
	// scoutPostFleet is the ClaimShip operation/fleet name a post-manned satellite
	// is claimed under. It gates poaching: ClaimShip rejects a hull dedicated to any
	// OTHER fleet (RULINGS #7), and an unpinned satellite claims normally. The claim
	// is occupancy, not a permanent pin — the satellite is never AssignFleet'd, so
	// releasing it (sweep-once completion, restart) returns it to the general pool.
	scoutPostFleet = "scout"

	// defaultScoutPostTickSeconds is the reconcile cadence when the launch config
	// leaves it unset (RULINGS #5: parametrized, not hardcoded at the call site).
	defaultScoutPostTickSeconds = 30

	// marketplaceTrait selects the waypoints a post's tour scans — the same trait
	// the VRP scout-all-markets path keys on.
	marketplaceTrait = "MARKETPLACE"

	// repositionRetryBackoff is a short in-memory floor armed at every dispatch, covering
	// the window between a relay ending and the next reconcile pass so a relay that ends
	// without an explicit FAILED verdict (restart-interrupted, fast opaque exit) does not
	// hot-loop re-dispatch. An explicit FAILED status arms the much longer
	// defaultRepositionFailureCooldown instead. Reset on restart: at most one immediate
	// retry after a daemon restart, never a storm.
	repositionRetryBackoff = 5 * time.Minute

	// defaultRepositionFailureCooldown bounds the retry wait after a FAILED reposition
	// relay when [scouting] reposition_failure_cooldown_secs is unset. Must stay well
	// above tick cadence, or a genuinely-unroutable post crash-loops the dispatcher. The
	// probe is freed to the next candidate on each failure, so one post's cooldown never
	// starves the others.
	defaultRepositionFailureCooldown = 30 * time.Minute

	// defaultScoutRespawnAttemptCap bounds CONSECUTIVE dead-tour respawns of one post
	// before the coordinator parks it instead of respawning again ([scouting]
	// respawn_attempt_cap unset). A tour that runs one healthy tick resets the count;
	// without the cap a persistently-crashing tour respawn-loops every tick forever.
	defaultScoutRespawnAttemptCap = 10

	// defaultRespawnParkWindow is how long a post that exhausted its respawn cap is
	// parked before exactly one retry. Persisted with the counter (RespawnParkedUntil) so
	// the park survives a restart instead of the crash-loop resuming immediately.
	defaultRespawnParkWindow = 30 * time.Minute

	// partitionAnchorFuelCapacity and partitionAnchorEngineSpeed feed the VRP partitioner
	// N identical anchored probe slots rather than real ship locations, so the resulting
	// partition stays STABLE regardless of which specific probes are present (re-partition
	// fires only on a hull-count change). Exact values don't matter: partitioning runs once
	// per budget change, not per tick.
	partitionAnchorFuelCapacity = 400
	partitionAnchorEngineSpeed  = 30

	// defaultMarketDriftThreshold and defaultMarketDriftMaxAge bound the debounced
	// market-set re-cut when unset. A market discovered after a post's tours are cut
	// belongs to no partition and goes permanently stale, so a partitioned (hulls>1) post
	// re-cuts once its discovered market set has drifted from its persisted partition
	// union by at least Threshold markets (additions and removals both count), or the
	// drift has been pending at least MaxAge — whichever fires first.
	defaultMarketDriftThreshold = 2
	defaultMarketDriftMaxAge    = 60 * time.Minute

	// defaultBudgetChangeDebounceCycles bounds the debounced hull-budget re-partition when
	// unset. The freshness sizer's per-post budget can oscillate ±1 cycle-to-cycle on
	// normal demand noise; an unconditional re-partition on every swing stops the post's
	// tours and re-scans its markets every tick. A budget change re-partitions only after
	// the SAME new value persists this many consecutive cycles — short enough to still act
	// well inside the 1h freshness SLA.
	defaultBudgetChangeDebounceCycles = 3

	// defaultUndersizedAvgHop and defaultUndersizedRewarnCooldown bound the
	// undersized-post warning (layer 1) when unset. avgHop is the circuit-model average
	// per-market hop cost (nav + scan dwell) used to project a post's circuit time; the
	// cooldown debounces the deferred warning so a persistently-undersized post re-queues
	// the event at most once per window.
	defaultUndersizedAvgHop         = 3 * time.Minute
	defaultUndersizedRewarnCooldown = 3 * time.Hour

	// defaultMaxRepositionJumps bounds the EXPENDABLE-probe reposition reach over the
	// stored adjacency ([scouting] max_reposition_jumps unset). Deliberately larger than
	// the strict heavy-hull cap (gategraph.MaxJumpPath=5): only the probe reposition
	// class, which routes past unreadable frontier gates, reaches this far.
	defaultMaxRepositionJumps = 12

	// defaultGateReconcileMaxDispatch bounds the gate-reconcile sweep to a small number of
	// relays per tick when unset — a conservative rate-budget default mirroring
	// defaultMaxRepositionJumps' 0 => default idiom.
	defaultGateReconcileMaxDispatch = 2

	// defaultScoutCrossSystemRelayEnabled is the cross-system reuse-relay master switch.
	// Int-mode rather than bool because the int tune registry treats 0 as "revert to
	// default", which a plain bool could never express. 0 (default) = in-system +
	// idle-reposition-only manning, byte-identical; > 0 = armed. Inert unless
	// SetProbeDemandReader is wired.
	defaultScoutCrossSystemRelayEnabled = 0
	// defaultScoutRelayMaxHops bounds the cross-system reuse relay reach (gate-hops) when
	// scout_relay_max_hops is unset. Probes are fuel_cap=0 gate-users that cannot
	// fuel-strand, so the reach is a router/config bound, not a physical one. Inert while
	// the relay is disabled.
	defaultScoutRelayMaxHops = 5

	// defaultManningStallCycles and defaultManningStallCorrectionCap bound the manning
	// watchdog when unset. The watchdog re-mans a standing post that reads
	// IsFullyManned() yet has produced no new scan telemetry — worst-case market age
	// breaches the post's own freshness target without improving — for this many
	// CONSECUTIVE cycles (the tour can be wedged: container reads RUNNING but the hull no
	// longer scans). CorrectionCap bounds how many re-mans one post gets before the
	// watchdog backs off and leaves the persisted captain event to carry it to the
	// operator, instead of churning a tour on an unreachable market forever.
	defaultManningStallCycles        = 4
	defaultManningStallCorrectionCap = 3
)

// RunScoutPostCoordinatorCommand launches the standing scout-post coordinator for a
// player. Like the contract fleet coordinator it runs an infinite reconcile loop
// inside a single Handle() call; the container wraps one iteration
// (CoordinatorOwnsIterations).
type RunScoutPostCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int

	// MarketDriftThreshold and MarketDriftMaxAgeSecs bound the debounced market-set
	// re-cut: a partitioned (hulls>1) post re-cuts once its discovered market set has
	// drifted from its persisted partition union by at least MarketDriftThreshold
	// markets, or the drift has been pending at least MarketDriftMaxAgeSecs seconds —
	// whichever fires first. <= 0 uses the coordinator's own default, mirroring
	// TickIntervalSecs.
	MarketDriftThreshold  int
	MarketDriftMaxAgeSecs int

	// BudgetChangeDebounceCycles bounds the debounced hull-budget re-partition: an
	// already-materialized post re-partitions on a hull-budget change only once the new
	// budget has persisted this many consecutive reconcile cycles, absorbing the
	// freshness sizer's demand-noise swings that would otherwise stop the post's tours
	// and re-scan its markets every tick. <= 0 uses the coordinator's own default,
	// mirroring MarketDriftThreshold.
	BudgetChangeDebounceCycles int

	// UndersizedAvgHopSecs and UndersizedRewarnCooldownSecs tune the undersized-post
	// warning (layer 1): the circuit-model average per-market hop cost, and how long a
	// fired warning suppresses a re-fire for the same system. <= 0 uses the
	// coordinator's own defaults, mirroring TickIntervalSecs.
	UndersizedAvgHopSecs         int
	UndersizedRewarnCooldownSecs int

	// StartJitterMaxSecs bounds a one-time deterministic phase offset waited out before
	// this coordinator's reconcile loop starts its first tick: derived from a stable
	// hash of ContainerID via stableJitter (shared with scout_tour.go, same package —
	// NOT math/rand) so the SAME container gets the SAME offset on every build,
	// including restart recovery. <= 0 uses defaultTourStartJitterMax, mirroring
	// TickIntervalSecs.
	//
	// Does NOT re-pace reconcileOnce's own per-post manning passes: a mass re-man fans
	// out through spawnTour into freshly-launched scout_tour containers, and each of
	// those already self-jitters its own first scan via the ShipSymbol-keyed offset, so
	// a synchronized sweep decoheres for free.
	StartJitterMaxSecs int

	// MaxRepositionJumps bounds the EXPENDABLE-probe reposition reach over the stored
	// adjacency ([scouting] max_reposition_jumps): the selection resolver and the
	// dispatched relay both route past unreadable frontier gates up to this many jumps.
	// <= 0 uses defaultMaxRepositionJumps, mirroring TickIntervalSecs.
	MaxRepositionJumps int

	// RepositionFailureCooldownSecs is how long a post whose reposition relay FAILED
	// waits before the coordinator retries repositioning to it ([scouting]
	// reposition_failure_cooldown_secs). On failure the coordinator arms this cooldown
	// on the post's slot, frees the probe, and services the NEXT candidate post this
	// tick instead of respawning the same corpse. <= 0 uses
	// defaultRepositionFailureCooldown, mirroring TickIntervalSecs.
	RepositionFailureCooldownSecs int

	// GateReconcileEnabled arms the RETROACTIVE gate-reconcile sweep (Part 2): a bounded
	// pass that dispatches LEFTOVER idle probes to market-known-but-gate-uncharted
	// frontier systems so Part 1's chart-on-arrival fills their gate_edges. DEFAULT OFF
	// (deploy-inert): the sweep moves probes and spends API budget, so it is opt-in.
	// Off => reconcileOnce is byte-for-byte the pre-Part-2 tick.
	GateReconcileEnabled bool

	// GateReconcileMaxDispatch HARD-CAPS how many gate-reconcile relays the sweep may
	// dispatch per tick — the rate-budget guard so the sweep can never burst the limiter
	// or starve trade hulls of it. <= 0 uses defaultGateReconcileMaxDispatch, mirroring
	// TickIntervalSecs' 0 => default idiom.
	GateReconcileMaxDispatch int

	// GateReconcileMarketlessDisabled reverts the widened gate-reconcile sweep to the
	// market-only backlog, dropping the traffic-markered MARKETLESS transit gates from
	// the target set. false/absent => LIVE (the widened scope is ON whenever
	// GateReconcileEnabled arms the sweep): the sweep also charts uncharted transit
	// systems a stale backoff marker proves traffic jumps THROUGH. Set true to pin
	// market-only without a redeploy. Requires SetUnreadableGateProvider wired to have
	// any effect.
	GateReconcileMarketlessDisabled bool

	// CoverageSpreadDisabled reverts the coverage-first manning order to depth-first
	// (all of a post's slots before the next post's). Default false = LIVE:
	// unmannedSlotTargets interleaves by slot tier so a scarce idle-probe pool spreads
	// one-per-uncovered-system before it piles a multi-hull post's extra slots, instead
	// of herding the whole probe group onto one target per cycle. The escape
	// ([scouting] coverage_spread_disabled) lets a captain pin depth-first without a
	// redeploy; not expected to be set in normal operation.
	CoverageSpreadDisabled bool

	// RespawnAttemptCap bounds how many CONSECUTIVE respawns of a post's dead tour the
	// coordinator performs before PARKING the post for a backoff window instead
	// ([scouting] respawn_attempt_cap). A tour that finally runs healthy resets the
	// count. <= 0 uses defaultScoutRespawnAttemptCap, mirroring TickIntervalSecs.
	RespawnAttemptCap int

	// RespawnCapDisabled turns the respawn-loop cap OFF, restoring
	// respawn-every-tick behavior ([scouting] respawn_cap_disabled). Default false =
	// LIVE: the cap is on. Disable escape so a captain can lift it without a redeploy;
	// not expected to be set in normal operation.
	RespawnCapDisabled bool

	// ManningStallCycles and ManningStallCorrectionCap tune the manning watchdog
	// (LIVE-tunable via SetLiveConfigReader): the number of CONSECUTIVE reconcile cycles
	// a fully-manned standing post must breach its freshness target without its
	// worst-case market age improving before the watchdog re-mans it, and the number of
	// re-mans of one post before the watchdog backs off (leaving the captain event to
	// carry it). <= 0 uses the coordinator's own defaults, mirroring TickIntervalSecs.
	// Both are registered in the daemon tune bounds registry as manning_stall_cycles /
	// manning_stall_correction_cap and read from the live config snapshot each tick, so
	// a `spacetraders tune` lands on the NEXT tick with no restart.
	ManningStallCycles        int
	ManningStallCorrectionCap int

	// ScoutCrossSystemRelayEnabled arms the CROSS-SYSTEM reuse relay ([scouting]
	// scout_cross_system_relay_enabled, an int-mode flag so it is cleanly live-tunable +
	// revert-able in the int tune registry): > 0 => when a declared post has NO
	// in-system satellite AND no idle probe is left to relay to it this tick, borrow ONE
	// surplus probe from an OVER-COVERED source system (its manning supply exceeds the
	// freshsizer demand) and relay it cross-system to the post; 0 (default) => in-system
	// + idle-reposition-only behavior, byte-identical. Requires SetProbeDemandReader
	// wired to have ANY effect. LIVE-tunable (ScoutPostTunableDefaults), read from the
	// live-config snapshot each tick.
	ScoutCrossSystemRelayEnabled int
	// ScoutRelayMaxHops bounds the cross-system reuse relay reach in gate-hops
	// ([scouting] scout_relay_max_hops): a surplus probe farther than this from the
	// target post is never borrowed. <= 0 uses defaultScoutRelayMaxHops. Inert while the
	// relay is disabled. LIVE-tunable (ScoutPostTunableDefaults), mirroring
	// MaxRepositionJumps' 0 => default idiom.
	ScoutRelayMaxHops int
}

// RunScoutPostCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type RunScoutPostCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// ContainerStatusQuery reads container lifecycle state so the reconciler can tell
// a live tour from a dead or completed one. Satisfied by the GORM container
// repository (ListByStatusSimple + ContainerStatus).
type ContainerStatusQuery interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)

	// ContainerStatus resolves a SINGLE container's status and existence, so the orphan
	// sweep can ask IsClaimOrphaned about the exact container a scout hull claims.
	// found=false means the row is gone. Satisfied by the GORM container repository's
	// ContainerStatus (the same per-ID read refresh_ship's stale-claim reconciler uses).
	ContainerStatus(ctx context.Context, containerID string, playerID shared.PlayerID) (string, bool, error)
}

// MarketWaypointProvider lists the marketplace waypoints in a system — the tour a
// post's hull flies. Satisfied by the GORM waypoint repository
// (ListBySystemWithTrait).
type MarketWaypointProvider interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// GateGraph resolves multi-jump routes over the persisted cross-system gate graph. The
// coordinator BFS-walks it to pick the FLEET-WIDE nearest idle satellite (fewest jump
// hops) to reposition to an unmanned post, and to fail closed when no satellite can
// reach it. Optional: nil disables repositioning entirely — the coordinator then parks
// a satellite-less post instead.
type GateGraph interface {
	// RepositionPath resolves the fleet-wide reposition route over the PERSISTED stored
	// adjacency bounded to maxJumps: it routes PAST an unreadable frontier gate instead
	// of dead-ending on it, reaching posts the strict fetch-through MaxJumpPath=5
	// rejects. Safe for the expendable scout class only; every arrival re-reads its gate
	// (chart-on-arrival), so the relaxation retires itself.
	RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error)
	// Adjacency returns every gate-CHARTED system's stored neighbor edges (era-scoped, a
	// pure store read — no live fetch). The gate-reconcile sweep reads its KEY SET as
	// "systems whose jump gate is already charted", and enumerates the retroactive
	// backlog as the market-known systems MINUS this set. *gategraph.Service satisfies it.
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// MarketFreshnessProvider computes, per POSTED system, the worst-case cached
// market-data staleness — MAX(now - last_updated) across that system's markets —
// backing the scout_freshness_actual_seconds gauge. One call per sweep covers every
// system for the player in a single query. Satisfied by the GORM market repository
// (MarketRepositoryGORM.MaxAgeSecondsBySystem). Optional: nil disables the gauge
// entirely (pure OBSERVATION, RULINGS #4).
type MarketFreshnessProvider interface {
	MaxAgeSecondsBySystem(ctx context.Context, playerID int) (map[string]float64, error)
}

// UnreadableGateProvider enumerates the persisted negative-result backoff markers:
// every era-scoped UNCHARTED gate a hull's live GetJumpGate 400'd on, mapped to the
// gate waypoint the marker recorded. A marker row exists ONLY because fleet traffic
// actually tried to route THROUGH that gate, so the set is intrinsically bounded to
// traffic-touched gates. This is the "an active route traverses this uncharted gate"
// signal the gate-reconcile sweep widens onto: it no longer needs the target to bear a
// market. Satisfied by the GORM gate-edge repository (GormGateEdgeRepository.
// UnreadableGates). Optional: nil leaves the sweep market-only.
type UnreadableGateProvider interface {
	UnreadableGates(ctx context.Context) (map[string]string, error)
}

// SystemProbeDemandReader answers a system's freshsizer probe DEMAND — the minimum
// scout-probe count that system needs to hold its markets within the freshness SLA.
// The cross-system reuse relay reads it to find OVER-COVERED source systems (manning
// supply > demand) it may borrow ONE surplus probe from, NEVER stripping a system
// below its need. Optional (SetProbeDemandReader): nil disables the cross-system relay
// entirely, so no probe is ever pulled off a manning tour. Production-backed by the
// SAME SystemsFreshness census the manning watchdog reads (CensusProbeDemandReader),
// so demand HONORS the freshsizer's age-driven raises: a system BREACHING its SLA
// reads a raised demand and is never raided.
type SystemProbeDemandReader interface {
	ProbeDemand(ctx context.Context, playerID int, systemSymbol string) (int, error)
}

// RunScoutPostCoordinatorHandler reconciles the desired-state posts table every
// tick. Each post has HullBudget() manning SLOTS — one for a single-hull post, N for
// a multi-probe post — and every slot is manned, repaired, and repositioned
// independently. A multi-probe post's markets are first partitioned into N DISJOINT
// per-probe tours via the existing VRP machinery and frozen (re-partitioned only on a
// hull-budget change); each slot then behaves exactly like a single-hull post over its
// partition. The reconciler respawns any tour that died, mans an unmanned slot by
// claiming an idle satellite ALREADY IN THE POST'S SYSTEM (manning is in-system-only),
// releases any assignment whose hull drifted out of system so it can be re-matched,
// retires completed sweep-once posts, and never poaches a pinned hull. When a slot has
// no in-system satellite it JUMP-ROUTES the fleet-wide nearest idle satellite to it. It
// is the freshness backbone the tour planner's age cap and the analyst board both ride on.
type RunScoutPostCoordinatorHandler struct {
	postRepo       domainScouting.ScoutPostRepository
	shipRepo       navigation.ShipRepository
	daemonClient   daemon.DaemonClient
	containerQuery ContainerStatusQuery
	marketProvider MarketWaypointProvider
	clock          shared.Clock

	// gateGraph resolves jump-hop distances for fleet-wide reposition selection. nil
	// disables repositioning (the post is parked instead), so it is wired via
	// SetGateGraph rather than the constructor — an unwired caller/test is unaffected.
	gateGraph GateGraph

	// graphProvider discovers a VIRGIN system's waypoints presence-free via the API when
	// the reposition target has zero KNOWN market waypoints, and supplies waypoint
	// coordinates to the VRP partitioner. It is the same cache-first
	// ISystemGraphProvider port scout_markets/assign_scouting_fleet use, and persists
	// discovered waypoints era-scoped via its BuildSystemGraph->Add path. nil disables
	// virgin discovery and leaves the partitioner without coordinates (it still
	// partitions, just without geometry) — wired via SetGraphProvider rather than the
	// constructor.
	graphProvider system.ISystemGraphProvider

	// routingClient solves the VRP that partitions a multi-probe post's markets into N
	// disjoint tours. Reuses the SAME PartitionFleet the `workflow scout-markets` verb
	// uses. nil disables partitioning: a multi-probe post then cannot materialize its
	// extra slots and parks (fail-closed), while single-hull posts are unaffected.
	// Wired via SetRoutingClient.
	routingClient routing.RoutingClient

	// marketFreshnessProvider supplies scout_freshness_actual_seconds' raw ages: MAX(now
	// - last_updated) per system with cached market rows, read once per sweep. nil
	// disables the gauge — pure OBSERVATION (RULINGS #4), never a decision input —
	// wired via SetMarketFreshnessProvider rather than the constructor.
	marketFreshnessProvider MarketFreshnessProvider

	// unreadableGateProvider widens the gate-reconcile sweep from market-only to any
	// traffic-markered uncharted gate: it lists the era-scoped backoff markers so the
	// sweep also charts marketless TRANSIT systems traders jump THROUGH. nil (the
	// default) leaves the sweep market-only, wired via SetUnreadableGateProvider rather
	// than the constructor.
	unreadableGateProvider UnreadableGateProvider

	// repositionBackoffUntil rate-limits reposition DISPATCH per post slot (key
	// playerID|system[|slotIndex] -> earliest next dispatch time) so a relay that fails
	// fast does not hot-loop re-dispatch. In-memory (reset on restart); guarded by
	// repositionMu since the handler is a registered singleton that could serve two
	// players' coordinator ticks concurrently.
	repositionMu           sync.Mutex
	repositionBackoffUntil map[string]time.Time

	// repositionFailures counts CONSECUTIVE reposition-relay failures per post slot
	// (same key shape as repositionBackoffUntil), so the failure log reports the Nth
	// attempt and a completed relay resets the streak. Guarded by repositionMu with the
	// deadline map it travels with. repositionBackoffLoggedUntil records the deadline of
	// the backoff episode already logged for a key, so a long cooldown is announced ONCE
	// (state change) rather than every tick. Both in-memory, reset on restart.
	repositionFailures           map[string]int
	repositionBackoffLoggedUntil map[string]time.Time

	// driftPendingSince tracks, per partitioned post (key playerID|system — driftKey,
	// since drift is a whole-post property), when its market set FIRST started
	// differing from its persisted partition union — the age half of the debounced
	// re-cut trigger. Cleared once a re-cut resolves the drift, the drift resolves on
	// its own, or the post reverts to single-hull. In-memory and reset on restart:
	// losing it only restarts the age countdown, never a stability violation — same
	// hulls + same markets never populates this map, so "zero re-cuts when stable" is
	// untouched. Guarded by driftMu for the same singleton-handler concurrency reason
	// as repositionMu.
	driftMu           sync.Mutex
	driftPendingSince map[string]time.Time

	// budgetChangePending tracks, per already-materialized post (driftKey shape), the
	// new hull budget the freshness sizer wants that DIFFERS from the post's
	// currently-cut partition, and how many CONSECUTIVE reconcile cycles that SAME new
	// budget has persisted — the debounce that absorbs the sizer's demand-noise budget
	// swings so a transient oscillation no longer tears down the post's tours and
	// re-scans its markets every tick. A new/changed target restarts the count, so a
	// budget that keeps flapping to different values never accumulates toward the
	// re-partition threshold. Cleared the moment the budget matches the cut partition
	// again or the re-partition fires. In-memory and reset on restart. Guarded by
	// budgetChangeMu for the same singleton-handler concurrency reason as driftMu.
	budgetChangeMu      sync.Mutex
	budgetChangePending map[string]budgetChangeState

	// singleHullMarketSnapshot and singleHullDriftPendingSince give a SINGLE-hull
	// standing post the same debounced market-set-drift respawn partitioned posts get.
	// A single-hull tour's market list is frozen at spawn time (ScoutTourCommand.
	// Markets, set once in spawnTour) and never re-read afterward by either scout_tour
	// execution mode, so a market discovered after spawn is never toured until the post
	// re-mans for an unrelated reason. ensureSingleHullFreshness closes that gap by
	// tearing down and re-manning the post once its live discovered set has drifted
	// from the snapshot taken at the post's last manning, reusing driftKey's key shape
	// and the SAME MarketDriftThreshold/MarketDriftMaxAgeSecs config. Two SEPARATE maps
	// — not two keys inside driftPendingSince/repositionBackoffUntil — because
	// ensurePartitions unconditionally clears driftPendingSince[driftKey(...)] every
	// tick for every budget<=1 post, which would wipe a shared entry before it could
	// ever accumulate age. In-memory and reset on restart: a lost snapshot is treated as
	// "adopt current markets, don't respawn" rather than maximal drift, so a restart
	// never triggers a respawn storm fleet-wide. Guarded by singleHullMu for the same
	// singleton-handler concurrency reason as driftMu.
	singleHullMu                sync.Mutex
	singleHullMarketSnapshot    map[string][]string
	singleHullDriftPendingSince map[string]time.Time

	// eventStore records the DEFERRED scout.post_undersized warning (layer 1) and
	// dedups it via HasSince. Optional (SetEventStore): nil leaves the warning off
	// entirely. Pure OBSERVATION seam: a store error never aborts a reconcile pass. The
	// manning watchdog reuses it for the scout.post_manning_stalled event.
	eventStore captain.EventStore

	// systemFreshnessReader supplies the manning watchdog's per-system census —
	// OldestAgeSeconds (worst-case market staleness) + MarketCount + CycleSamples — the
	// SAME SystemsFreshness port the market-freshness sizer reconciles against, so the
	// watchdog and the sizer judge a post against ONE consistent census. nil disables
	// the watchdog entirely (optional-injection, like SetGateGraph).
	systemFreshnessReader domainScouting.SystemFreshnessReader

	// liveConfig snapshots this container's OWN persisted config at each tick, so the
	// manning watchdog's manning_stall_* knobs honor `spacetraders tune` on the NEXT
	// tick with no restart — the same seam the freshness sizer uses.
	// Optional-injection: nil keeps those knobs launch-frozen (read straight from the
	// command).
	liveConfig liveconfig.Reader

	// stall* back the manning watchdog's in-memory, per-post (driftKey shape) debounce,
	// mirroring driftPendingSince: stallLastAgeSeconds is last tick's OldestAgeSeconds
	// (to detect an IMPROVEMENT — telemetry advancing — versus a frozen climb);
	// stallCycles is the consecutive breach-without-improvement count (the N-cycle
	// debounce); stallCorrections is how many re-mans this post has already had (the K
	// failed-correction backoff). All reset on restart: a lost baseline only re-earns
	// the debounce, never a spurious teardown — a post under its SLA never populates
	// these maps. Guarded by stallMu for the same singleton-handler concurrency reason
	// as driftMu.
	stallMu             sync.Mutex
	stallLastAgeSeconds map[string]float64
	stallCycles         map[string]int
	stallCorrections    map[string]int

	// probeDemandReader answers per-system freshsizer demand for the cross-system reuse
	// relay's over-covered check. nil DISABLES the relay (optional-injection via
	// SetProbeDemandReader, like SetGateGraph); the feature is inert until BOTH the flag
	// is armed AND this reader is wired.
	probeDemandReader SystemProbeDemandReader
}

// NewRunScoutPostCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production). The gate-graph resolver, graph provider, and
// routing client are optional and injected separately (SetGateGraph, SetGraphProvider,
// SetRoutingClient) — nil leaves repositioning / virgin discovery / partitioning
// disabled, so every pre-enry caller behaves as before.
func NewRunScoutPostCoordinatorHandler(
	postRepo domainScouting.ScoutPostRepository,
	shipRepo navigation.ShipRepository,
	daemonClient daemon.DaemonClient,
	containerQuery ContainerStatusQuery,
	marketProvider MarketWaypointProvider,
	clock shared.Clock,
) *RunScoutPostCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunScoutPostCoordinatorHandler{
		postRepo:                     postRepo,
		shipRepo:                     shipRepo,
		daemonClient:                 daemonClient,
		containerQuery:               containerQuery,
		marketProvider:               marketProvider,
		clock:                        clock,
		repositionBackoffUntil:       make(map[string]time.Time),
		repositionFailures:           make(map[string]int),
		repositionBackoffLoggedUntil: make(map[string]time.Time),
		driftPendingSince:            make(map[string]time.Time),
		budgetChangePending:          make(map[string]budgetChangeState),
		singleHullMarketSnapshot:     make(map[string][]string),
		singleHullDriftPendingSince:  make(map[string]time.Time),
		stallLastAgeSeconds:          make(map[string]float64),
		stallCycles:                  make(map[string]int),
		stallCorrections:             make(map[string]int),
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver. The daemon injects the same
// persisted, fetch-through gategraph.Service the trade-route circuit uses, so the
// reposition BFS and the circuit's travel() share one cache/graph. Optional-injection:
// nil (the default) leaves repositioning disabled and posts park instead.
func (h *RunScoutPostCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// SetGraphProvider wires the presence-free waypoint discoverer for virgin reposition
// targets and the coordinate source for the VRP partitioner. The daemon injects the
// same graphService the `waypoint` verb and the scout-markets planner use, so
// discovery shares one cache/graph and persists era-scoped exactly as every other
// charting path. Optional-injection: nil (the default) leaves posts parked instead.
func (h *RunScoutPostCoordinatorHandler) SetGraphProvider(g system.ISystemGraphProvider) {
	h.graphProvider = g
}

// SetRoutingClient wires the VRP fleet partitioner. The daemon injects the SAME
// routing client the scout-markets verb uses, so a multi-probe post's disjoint
// partition is solved by the routing service that already solves it.
// Optional-injection: nil leaves partitioning disabled, so single-hull posts are
// unaffected and a multi-probe post parks fail-closed until a client is wired.
func (h *RunScoutPostCoordinatorHandler) SetRoutingClient(c routing.RoutingClient) {
	h.routingClient = c
}

// SetEventStore wires the captain event outbox for the undersized-post warning
// (layer 1). The daemon injects the SAME store the watchkeeper reads, so a warning
// rides the next wake as a deferred event. Optional-injection: nil (the default)
// leaves the warning disabled.
func (h *RunScoutPostCoordinatorHandler) SetEventStore(s captain.EventStore) {
	h.eventStore = s
}

// SetMarketFreshnessProvider wires the scout_freshness_actual_seconds gauge's data
// source. The daemon injects the same GORM market repository the rest of the
// coordinator already reads through. Optional-injection: nil (the default) leaves the
// gauge unrecorded.
func (h *RunScoutPostCoordinatorHandler) SetMarketFreshnessProvider(p MarketFreshnessProvider) {
	h.marketFreshnessProvider = p
}

// SetUnreadableGateProvider wires the traffic-marker enumeration that widens the
// gate-reconcile sweep onto marketless transit gates. The daemon injects the SAME GORM
// gate-edge repository the gate graph reads through — one store, era-scoped.
// Optional-injection: nil (the default) leaves the sweep market-only.
func (h *RunScoutPostCoordinatorHandler) SetUnreadableGateProvider(p UnreadableGateProvider) {
	h.unreadableGateProvider = p
}

// SetSystemFreshnessReader wires the manning watchdog's per-system freshness census
// (SystemsFreshness). The daemon injects the SAME GORM market repository the freshness
// sizer reconciles against, so the watchdog and the sizer see one consistent census.
// Optional-injection: nil (the default) disables the watchdog.
func (h *RunScoutPostCoordinatorHandler) SetSystemFreshnessReader(r domainScouting.SystemFreshnessReader) {
	h.systemFreshnessReader = r
}

// SetProbeDemandReader wires the per-system freshsizer-demand source the cross-system
// reuse relay checks over-coverage against. The daemon injects CensusProbeDemandReader
// over the SAME SystemsFreshness census the watchdog reads. Optional-injection: nil
// (the default) disables the cross-system relay entirely — no probe is borrowed off a
// manning tour.
func (h *RunScoutPostCoordinatorHandler) SetProbeDemandReader(r SystemProbeDemandReader) {
	h.probeDemandReader = r
}

// SetLiveConfigReader wires the per-tick live-config snapshot source so the manning
// watchdog's manning_stall_* knobs honor `spacetraders tune` on the next tick. The
// daemon injects the SAME container-config-backed reader the freshness sizer uses.
// Optional-injection: nil (the default) leaves those knobs launch-frozen (read from
// the command).
func (h *RunScoutPostCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunScoutPostCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunScoutPostCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultScoutPostTickSeconds * time.Second
	}

	result := &RunScoutPostCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Scout post coordinator starting (tick %s)", tick), map[string]interface{}{
		"action":       "scout_post_coordinator_start",
		"container_id": cmd.ContainerID,
	})

	if !h.waitStartJitter(ctx, cmd) {
		return result, ctx.Err()
	}

	// errMon makes a reconcile pass that fails with the identical error every tick
	// observable: once the streak crosses DefaultStreakThreshold it emits a captain
	// event instead of just another ERROR line. One per Handle invocation so the streak
	// persists across ticks; noteReconcile keeps reconcileOnce — the tested unit —
	// unchanged, and reuses the already-wired eventStore as the recorder.
	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		err := h.reconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Scout post reconcile failed: %v", err), nil)
		}
		h.noteReconcile(ctx, cmd, errMon, err)
		result.Ticks++

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// waitStartJitter waits out this coordinator's deterministic start-of-loop phase
// offset before the reconcile loop's first tick, keyed on ContainerID — this
// coordinator's stable identity, unchanged across restart recovery. Reuses
// stableJitter/defaultTourStartJitterMax from scout_tour.go (same package): decoheres
// this coordinator's tick from other standing coordinators that might otherwise tick
// in lockstep. Returns false if ctx is cancelled during the wait, so the caller can
// return cleanly instead of entering a reconcile loop that was already asked to stop.
func (h *RunScoutPostCoordinatorHandler) waitStartJitter(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) bool {
	ceiling := time.Duration(cmd.StartJitterMaxSecs) * time.Second
	if ceiling <= 0 {
		ceiling = defaultTourStartJitterMax
	}
	jitter := stableJitter(cmd.ContainerID, ceiling)
	if jitter <= 0 {
		return true
	}
	return h.sleepInterruptibly(ctx, jitter)
}

// sleepInterruptibly waits for d on h.clock, returning true if the wait completed
// normally or false if ctx was cancelled first. Clock-injected so tests run on a
// MockClock with no wall-time cost — this handler's own private copy, mirroring the
// same idiom in scout_tour.go, run_factory_coordinator.go, and
// run_trade_route_coordinator_travel.go.
func (h *RunScoutPostCoordinatorHandler) sleepInterruptibly(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// slotTarget pairs an unmanned slot with its owning post so pass 2 can man or
// reposition it with the post's markets, priority, and freshness.
type slotTarget struct {
	post *domainScouting.ScoutPost
	slot domainScouting.ScoutSlotRef
}

// noteReconcile records one reconcile pass at the "reconcile" streak checkpoint: a nil
// err is a success that resets the streak; a non-nil err that repeats identically for
// DefaultStreakThreshold consecutive passes crosses and emits the coordinator
// error-loop captain event. It reuses the already-wired eventStore (captain.EventStore
// embeds EventRecorder) as the recorder — nil-safe when the store is unwired (tests).
// Per-post failures inside reconcileOnce are logged WARNING and swallowed there, so
// only a pass-level error — the genuine silent-stuck signal — is tracked here.
func (h *RunScoutPostCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.eventStore, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// reconcileOnce is one reconcile pass over the posts table. It is the unit the
// coordinator's tests drive directly (the Handle loop just calls it on a timer).
//
// A post has HullBudget() manning SLOTS: one for a single-hull post — the primary
// slot — or N for a multi-probe post, whose markets are first partitioned into N
// disjoint per-probe tours and frozen. Every pass below iterates SLOTS, not posts, so
// a dead probe on one slot heals without touching its siblings.
//
// Passes:
//   - Partition: (re)compute a multi-probe post's N disjoint partitions via VRP ONLY
//     when its hull budget changed (slot count != budget), and persist them — so a
//     restart reloads the frozen partitions and never re-tours, and a re-man reuses the same
//     partition.
//   - Pass 1 (manned slots): release any slot whose hull drifted out of the post's system
//     (repair — stop its tour, free the hull, clear the slot); retire a completed
//     sweep-once (release its hull, delete the post); free the hull of any other slot whose
//     tour is not running, clearing it so pass 2 re-mans it with the SAME partition. A
//     healthy in-system tour is left untouched.
//   - Pass 1.5 (repositioning slots): a slot with a relay in flight is left alone while its
//     container is RUNNING; when the relay ends the hull is reclaimed and the relay reference
//     cleared so pass 2 re-evaluates the slot.
//   - Pass 2a (in-system manning): claim an idle satellite ALREADY IN THE POST'S SYSTEM and
//     spawn its tour over the slot's markets (all markets for a single-hull post, the frozen
//     partition for a multi-probe slot). In-system only.
//   - Pass 2b (reposition): for a slot STILL unmanned, jump-route the FLEET-WIDE
//     nearest idle satellite to the post's system, then let the next tick's 2a man it.
func (h *RunScoutPostCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) error {
	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	if len(posts) == 0 {
		return nil
	}

	// Layer 1: warn (deferred) on any standing post whose circuit math cannot meet its
	// own freshness contract, BEFORE the manning passes — a pure observation over the
	// freshly-loaded post state that never mutates a post or aborts the tick.
	h.warnUndersizedPosts(ctx, cmd, posts)

	// Freshness gauge: pure OBSERVATION, so it runs unconditionally ahead of the
	// manning passes and can never affect them (RULINGS #4).
	h.recordScoutFreshness(ctx, cmd, posts)

	// Pass 0 (orphan sweep): free any scout hull whose owning container is orphaned but
	// that NO post slot references. Such a hull is invisible to both Pass 1 (no slot
	// points at it) and the idle scan (it is active, not idle), so it sits
	// claimed-but-driverless forever. Running BEFORE the manning passes returns it to
	// the idle pool in time for Pass 2a to re-seat it in its own system this same tick.
	h.sweepOrphanedScoutHulls(ctx, cmd, posts)

	running, err := h.containerIDSet(ctx, cmd, "RUNNING")
	if err != nil {
		return err
	}
	completed, err := h.containerIDSet(ctx, cmd, "COMPLETED")
	if err != nil {
		return err
	}
	// The FAILED set lets pass 1.5 distinguish a relay that DIED (unroutable — arm the
	// long failure cooldown and rotate the probe) from one that ARRIVED (reset the
	// streak) or was merely restart-interrupted (keep the short floor).
	failed, err := h.containerIDSet(ctx, cmd, "FAILED")
	if err != nil {
		return err
	}

	removed := make(map[string]bool)

	// Partition pass: materialize each multi-probe post's disjoint tours. A no-op for
	// single-hull posts and for multi-probe posts already partitioned at their current
	// budget — it re-partitions ONLY on a hull-budget change.
	//
	// ensureSingleHullFreshness runs right after: the single-hull mirror of the same
	// market-set-drift check, so a triggered teardown is picked up as "unmanned" by
	// pass 1/2 in THIS SAME tick, exactly like a partition re-cut is.
	for _, post := range posts {
		h.ensurePartitions(ctx, cmd, post)
		h.ensureSingleHullFreshness(ctx, cmd, post)
	}

	// Manning watchdog: re-man a standing post that reads IsFullyManned() yet has gone
	// silent (its worst-case market age has breached its freshness target without
	// improving for N consecutive cycles). It runs AFTER the partition /
	// single-hull-freshness teardowns and BEFORE the manning passes, so a torn-down
	// stalled post is re-manned this SAME tick. A no-op when no census reader is wired.
	h.remanStalledPosts(ctx, cmd, posts)

	// Pass 1: manned slots.
	for _, post := range posts {
		if h.reconcileMannedSlots(ctx, cmd, post, running, completed, removed) {
			continue // post retired (sweep-once complete)
		}
	}

	// Pass 1.5: repositioning slots. A relay in flight (RUNNING) owns its slot — pass 2
	// skips it. When the relay is no longer RUNNING it has landed, failed, or was
	// restart-interrupted; reclaim defensively and clear the relay reference so pass 2
	// re-evaluates the slot.
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		h.reconcileRepositioningSlots(ctx, cmd, post, running, completed, failed)
	}

	// Pass 2: man the unmanned slots, standing posts first. A post inside its respawn-cap
	// backoff window is skipped here so a persistently-crashing tour is not respawned every tick.
	_, respawnCapEnabled := resolveRespawnCap(cmd)
	targets := h.unmannedSlotTargets(posts, removed, cmd.CoverageSpreadDisabled, respawnCapEnabled)
	// The gate-reconcile sweep (Pass 3) also spends the LEFTOVER idle pool, so the fast
	// exit ("no unmanned slots => done") is preserved ONLY when the sweep is OFF. With
	// it armed the tick continues (fetching the idle pool) even when every slot is
	// manned — which is exactly when leftover probes are available to chart the backlog.
	if len(targets) == 0 && !cmd.GateReconcileEnabled {
		return nil
	}

	idleSats, err := h.idleScoutSatellites(ctx, cmd)
	if err != nil {
		return err
	}

	// Pass 2a: man every slot that has an idle satellite ALREADY in its system
	// (in-system-only manning). Doing this for ALL slots before any reposition guarantees an
	// in-system satellite is never repositioned AWAY from a slot that could man it locally.
	stillUnmanned := make([]slotTarget, 0, len(targets))
	for _, tgt := range targets {
		idx := selectInSystemSatellite(idleSats, tgt.post.SystemSymbol)
		if idx < 0 {
			stillUnmanned = append(stillUnmanned, tgt)
			continue
		}

		markets, err := h.slotMarkets(ctx, tgt.post, tgt.slot)
		if err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to discover markets for post %s: %v", tgt.post.SystemSymbol, err), nil)
			continue
		}
		if len(markets) == 0 {
			// Nothing to scan (uncharted / no marketplace waypoints, or an un-partitioned
			// multi-probe slot). Don't burn the in-system satellite's claim on a zero-market
			// tour — leave it idle in system. Repositioning cannot help (the problem is
			// markets, not hull location), so this slot is NOT a 2b candidate.
			common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("No markets to scan for post %s slot yet — leaving unmanned this tick", tgt.post.SystemSymbol), nil)
			continue
		}

		sat := idleSats[idx]
		idleSats = append(idleSats[:idx], idleSats[idx+1:]...)

		tourID, err := h.spawnTour(ctx, cmd, tgt.post, sat.ShipSymbol(), markets)
		if err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to man post %s with %s: %v", tgt.post.SystemSymbol, sat.ShipSymbol(), err), nil)
			continue
		}

		tgt.slot.SetAssignedHull(sat.ShipSymbol())
		tgt.slot.SetTourContainerID(tourID)
		if err := h.postRepo.Upsert(ctx, tgt.post); err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Manned post %s but failed to persist assignment: %v", tgt.post.SystemSymbol, err), nil)
		}
		if tgt.post.HullBudget() <= 1 && tgt.post.Kind == domainScouting.PostKindStanding {
			// Baseline the freshness snapshot to what this tour actually launched with,
			// so the NEXT tick's drift check compares against reality instead of an
			// empty/stale snapshot.
			h.setSingleHullSnapshot(driftKey(cmd.PlayerID.Value(), tgt.post.SystemSymbol), markets)
		}
	}

	// Pass 2b: jump-route the fleet-wide nearest idle satellite to each still-unmanned slot.
	// repositionUnmannedSlot fails closed — no gate graph, no idle satellite, no
	// reachable satellite, no known markets, or an active backoff parks the slot honest. When
	// there is NO idle satellite left this tick, the relay config below lets it borrow one
	// surplus probe from an over-covered system before parking (default OFF => park).
	// The relay knobs are resolved ONCE (a single live-config snapshot) and only when
	// there is unmanned work to do.
	relayCfg := scoutRelayConfig{maxHops: defaultScoutRelayMaxHops}
	if len(stillUnmanned) > 0 {
		relayCfg = resolveScoutRelayConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	}
	for _, tgt := range stillUnmanned {
		h.repositionUnmannedSlot(ctx, cmd, tgt.post, tgt.slot, &idleSats, posts, relayCfg)
	}

	// Pass 3: bounded retroactive gate-reconcile over the LEFTOVER idle probes —
	// dispatch a capped few to chart market-known-but-gate-uncharted frontier systems (Part 1
	// charts the gate on arrival). Self-guards on GateReconcileEnabled (default OFF), so this
	// is a no-op until armed; runs LAST so manning always has first claim on the idle pool.
	h.reconcileGateChartSweep(ctx, cmd, &idleSats)

	return nil
}

// releaseReasonScoutOrphanSwept marks a scout hull freed by the coordinator's orphan
// sweep: an active probe whose owning container is orphaned and which no post slot
// manages, returned to the idle pool for in-system re-seat. Distinct from
// stale_claim_reconciled (refresh-time) so the audit trail names WHICH reconciler
// freed the hull.
const releaseReasonScoutOrphanSwept = "scout_orphan_swept"

// sweepOrphanedScoutHulls frees scout hulls stranded active on an orphaned container
// that NO post slot references — see the Pass 0 comment in reconcileOnce. It reuses
// refresh_ship's IsClaimOrphaned verdict so the sweep and refresh-time reconciliation
// can never disagree on which claims are safe to reap, and it is pure best-effort: any
// read error is logged and skipped, never aborting the reconcile pass.
func (h *RunScoutPostCoordinatorHandler) sweepOrphanedScoutHulls(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	// Container IDs a post slot already owns — its tour OR its in-flight reposition relay.
	// A hull claimed through one of these is Pass 1 / Pass 1.5 territory (they reclaim it
	// against the post), so the sweep skips it regardless of that container's state: the
	// sweep touches ONLY fleet orphans whose post is gone, and is a strict no-op for every
	// post-referenced hull.
	postContainers := make(map[string]bool)
	for _, post := range posts {
		for _, slot := range post.Slots() {
			if t := slot.TourContainerID(); t != "" {
				postContainers[t] = true
			}
			if r := slot.RepositionContainerID(); r != "" {
				postContainers[r] = true
			}
		}
	}

	actives, err := h.shipRepo.FindActiveByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Scout orphan sweep skipped: failed to list active hulls: %v", err), nil)
		return
	}

	for _, ship := range actives {
		// Only expendable scout hulls; only real container claims. A captain reservation
		// is an active assignment with NO container — nothing to go stale — so it must
		// be excluded before the container lookup, exactly as refresh_ship's reconciler
		// does, or it would be reaped as "container gone".
		if !ship.IsScoutType() || !ship.IsAssigned() || ship.IsReservedByCaptain() {
			continue
		}
		containerID := ship.ContainerID()
		if postContainers[containerID] {
			continue // a post slot manages this claim — not the sweep's job
		}

		status, found, err := h.containerQuery.ContainerStatus(ctx, containerID, cmd.PlayerID)
		if err != nil {
			// Can't determine the owner's state — keep the claim (no positive orphan
			// evidence), mirroring refresh_ship's conservative stance. Next tick retries.
			continue
		}
		if !shipqueries.IsClaimOrphaned(status, found) {
			continue // RUNNING / INTERRUPTED / STOPPING — live or recoverable, never reap
		}

		shipSymbol := ship.ShipSymbol()
		// Release under CAS-retry: re-apply ForceRelease on the FRESH row so a
		// concurrent writer's cargo/nav update on the same hull survives instead of
		// being last-write-wins clobbered by the FindActiveByPlayer snapshot. Skip
		// unless the hull is still on THIS orphaned container (a concurrent release or
		// re-claim -> changed=false), so a hull that moved on is never swept out from
		// under its new owner (RULINGS #7).
		_, changed, saveErr := h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease(releaseReasonScoutOrphanSwept, h.clock)
				return true, nil
			})
		if saveErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Scout orphan sweep freed %s but failed to persist the release: %v", shipSymbol, saveErr), nil)
			continue
		}
		if !changed {
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Scout orphan swept: %s freed from orphaned container %s — returning to the idle pool for in-system re-seat", shipSymbol, containerID), map[string]interface{}{
			"action":             "scout_orphan_swept",
			"ship_symbol":        shipSymbol,
			"orphaned_container": containerID,
			"container_status":   status,
		})
	}
}

// warnUndersizedPosts emits a DEFERRED scout.post_undersized event for any STANDING
// post whose deterministic circuit math (markets / hulls x avgHop) cannot keep its
// markets within the post's own freshness target (layer 1). The event names the
// required hull count, so the fix (raise the budget) is spelled out.
//
// Scope: STANDING posts only (a sweep-once is a one-shot frontier pass with no standing
// freshness contract) with a positive freshness target and readable markets. It is pure
// observation: no post is mutated, a discovery/store error is swallowed (never aborts a
// reconcile), and with no event store wired (tests, pre-wiring) it is a no-op.
//
// Debounce (per post per condition-onset, not per 30s tick): a HasSince cooldown on any
// recent undersized event for the same system, processed or not — the same idiom the
// watchkeeper detectors use so a deferred event does not re-queue every tick while the
// post stays undersized.
func (h *RunScoutPostCoordinatorHandler) warnUndersizedPosts(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.eventStore == nil {
		return // events not wired — warning disabled (pre-k7q5 behavior).
	}
	logger := common.LoggerFromContext(ctx)

	avgHop := time.Duration(cmd.UndersizedAvgHopSecs) * time.Second
	if avgHop <= 0 {
		avgHop = defaultUndersizedAvgHop
	}
	cooldown := time.Duration(cmd.UndersizedRewarnCooldownSecs) * time.Second
	if cooldown <= 0 {
		cooldown = defaultUndersizedRewarnCooldown
	}
	now := h.clock.Now()

	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding {
			continue // sweep-once has no standing freshness contract to fail
		}
		freshness := post.FreshnessTarget
		if freshness <= 0 {
			continue // no contract to measure against — cannot assess
		}
		markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
		if err != nil {
			continue // transient discovery gap — never warn on missing data
		}
		hulls := post.HullBudget()
		if !domainScouting.IsUndersized(len(markets), hulls, avgHop, freshness) {
			continue
		}
		required := domainScouting.RequiredHulls(len(markets), avgHop, freshness)
		circuit := domainScouting.CircuitDuration(len(markets), hulls, avgHop)

		recent, err := h.eventStore.HasSince(ctx, cmd.PlayerID.Value(), captain.EventScoutPostUndersized, post.SystemSymbol, now.Add(-cooldown))
		if err != nil || recent {
			continue
		}
		_ = h.eventStore.Record(ctx, &captain.Event{
			Type: captain.EventScoutPostUndersized, Ship: post.SystemSymbol, PlayerID: cmd.PlayerID.Value(),
			Payload: fmt.Sprintf(`{"system":%q,"markets":%d,"hulls":%d,"required_hulls":%d,"freshness_secs":%d,"circuit_secs":%d}`,
				post.SystemSymbol, len(markets), hulls, required, int(freshness.Seconds()), int(circuit.Seconds())),
		})
		logger.Log("WARNING", fmt.Sprintf("Scout post %s undersized: %d markets over %d hull(s) ≈ %s circuit exceeds its %s freshness target — needs %d hulls", post.SystemSymbol, len(markets), hulls, circuit.Round(time.Second), freshness.Round(time.Second), required), map[string]interface{}{
			"action":         "scout_post_undersized",
			"system_symbol":  post.SystemSymbol,
			"markets":        len(markets),
			"hulls":          hulls,
			"required_hulls": required,
		})
	}
}

// recordScoutFreshness sets the scout_freshness_actual_seconds gauge for every POSTED
// system this pass is about to reconcile — i.e. exactly the systems in posts, one
// entry per active ScoutPost. A single provider read covers every system for the
// player (MarketFreshnessProvider.MaxAgeSecondsBySystem); a post whose system has no
// cached market rows yet simply has no entry in the returned map and is skipped this
// sweep — its gauge appears once a scan lands. Pure OBSERVATION (RULINGS #4): no
// provider wired, or a read error, is logged (once, not per-post) and the reconcile
// pass continues completely unaffected — a metrics gap must never block manning.
func (h *RunScoutPostCoordinatorHandler) recordScoutFreshness(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.marketFreshnessProvider == nil {
		return
	}
	playerID := cmd.PlayerID.Value()
	ages, err := h.marketFreshnessProvider.MaxAgeSecondsBySystem(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout freshness gauge: failed to compute market ages: %v", err), nil)
		return
	}
	for _, post := range posts {
		age, ok := ages[post.SystemSymbol]
		if !ok {
			continue // nothing cached for this system yet — no series this sweep
		}
		metrics.RecordScoutFreshness(playerID, post.SystemSymbol, age)
	}
}

// ScoutPostTunableDefaults maps every LIVE-tunable scout-post-coordinator knob to its
// documented default — the value that applies when neither the live container config
// nor the launch command carries a positive one. The daemon's tune bounds registry
// reads THIS map (mirroring SizerTunableDefaults), so the defaults-of-record stay in
// this file next to the consts they mirror, and the map's KEY SET is the contract for
// which keys the watchdog live-overlays per tick (resolveManningStallConfig).
func ScoutPostTunableDefaults() map[string]int {
	return map[string]int{
		"manning_stall_cycles":             defaultManningStallCycles,
		"manning_stall_correction_cap":     defaultManningStallCorrectionCap,
		"scout_cross_system_relay_enabled": defaultScoutCrossSystemRelayEnabled, // int-mode flag (0=off)
		"scout_relay_max_hops":             defaultScoutRelayMaxHops,
	}
}

// scoutRelayConfig is the cross-system reuse relay's resolved per-tick knobs.
type scoutRelayConfig struct {
	enabled bool
	maxHops int
}

// resolveScoutRelayConfig resolves the cross-system reuse relay's two knobs for one tick,
// mirroring resolveManningStallConfig's live-overlay + <= 0 -> default idiom. A non-nil live snapshot
// is AUTHORITATIVE (launch values share the same config column, so an untuned knob still reads its
// launch value): scout_cross_system_relay_enabled reads as a > 0 flag (so `tune ... 0` genuinely
// disarms), scout_relay_max_hops falls to defaultScoutRelayMaxHops when absent/zeroed. A nil snapshot
// (reader unwired, or the read failed) runs on the launch command — the fail-safe launch behavior.
func resolveScoutRelayConfig(cmd *RunScoutPostCoordinatorCommand, live liveconfig.Snapshot) scoutRelayConfig {
	enabledFlag := cmd.ScoutCrossSystemRelayEnabled
	maxHops := cmd.ScoutRelayMaxHops
	if live != nil {
		enabledFlag = live.PositiveIntOrZero("scout_cross_system_relay_enabled")
		maxHops = live.PositiveIntOrZero("scout_relay_max_hops")
	}
	if maxHops <= 0 {
		maxHops = defaultScoutRelayMaxHops
	}
	return scoutRelayConfig{enabled: enabledFlag > 0, maxHops: maxHops}
}

// resolveManningStallConfig resolves the watchdog's two knobs for one tick. live is
// the tick-start snapshot of the container's persisted config (nil when the reader is unwired
// or the read failed — the tick then runs on the launch command, the fail-safe launch
// behavior). For these TUNABLE knobs a non-nil snapshot is AUTHORITATIVE (launch values share
// the same config column, so an untuned knob still reads its launch value here); a
// zeroed/absent key falls to the documented default — the `tune <key> 0` revert. Mirrors
// resolveSizerConfig's live-overlay + the <= 0 -> default idiom this file uses everywhere.
func resolveManningStallConfig(cmd *RunScoutPostCoordinatorCommand, live liveconfig.Snapshot) (cycles, correctionCap int) {
	cycles = cmd.ManningStallCycles
	correctionCap = cmd.ManningStallCorrectionCap
	if live != nil {
		cycles = live.PositiveIntOrZero("manning_stall_cycles")
		correctionCap = live.PositiveIntOrZero("manning_stall_correction_cap")
	}
	if cycles <= 0 {
		cycles = defaultManningStallCycles
	}
	if correctionCap <= 0 {
		correctionCap = defaultManningStallCorrectionCap
	}
	return cycles, correctionCap
}

// liveConfigSnapshot takes the tick's live-config snapshot for the manning watchdog.
// A nil reader (not wired — tests, minimal boots) or a read error yields nil, which
// resolveManningStallConfig treats as "run this tick on the launch command" — the fail-safe
// launch behavior, never a half-applied config. The read is logged, not fatal: a transient DB
// gap must never kill the reconcile loop or churn a tour.
func (h *RunScoutPostCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout post live config unreadable — this tick's watchdog knobs run on launch values: %v", err), nil)
		return nil
	}
	return snap
}

// remanStalledPosts is the manning watchdog: it re-mans a standing post that reads
// IsFullyManned() yet has produced NO new scan telemetry for N consecutive reconcile cycles.
// The signal is the SystemsFreshness census's OldestAgeSeconds (worst-case market
// staleness): a fully-manned post whose worst-case age BREACHES its own FreshnessTarget and is
// NOT improving (no re-scan pulling it back — telemetry is not advancing) for N cycles has a
// wedged tour whose container may read RUNNING while its hull no longer scans, invisible to
// pass 1. The FreshnessTarget breach gate is what keeps a healthy, correctly-sized post (whose
// worst-case age stays within its own contract and improves each per-market scan) OUT of the
// watchdog's sights, so the short N-cycle debounce is a debounce, not the whole false-positive
// guard; the improvement check additionally spares a post that is over its SLA but already
// RECOVERING on its own.
//
// The corrective action REUSES tearDownSlots (the single-hull-freshness teardown): stop
// the wedged tour, reclaim the hull, clear the slot so THIS SAME tick's passes re-man it fresh —
// a different idle in-system hull if one is free, else the reclaimed hull on a fresh tour
// container. It never repositions an in-system hull or reinvents claiming.
//
// Anti-thrash: after each re-man the consecutive-cycle counter resets, so the next re-man is at
// least N cycles away (never every tick); and after ManningStallCorrectionCap re-mans that did
// not restore telemetry the watchdog BACKS OFF — it keeps emitting the deferred
// scout.post_manning_stalled event (so the stuck post stays VISIBLE) but stops churning a tour a
// genuinely unreachable market will only wedge again. Scope: standing, fully-manned posts with a
// positive freshness target and a census entry; an under-manned/unmanned post is the sizer's /
// normal manning's job and is explicitly left alone (forgetManningStall).
//
// Optional-injection: no census reader wired (nil) makes this a no-op. A census read
// error is logged and swallowed — a metrics gap must never abort a reconcile or tear
// down a tour on no evidence.
func (h *RunScoutPostCoordinatorHandler) remanStalledPosts(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.systemFreshnessReader == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	snapshots, err := h.systemFreshnessReader.SystemsFreshness(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Manning watchdog: failed to read the freshness census: %v — skipping this tick", err), nil)
		return
	}
	census := make(map[string]domainScouting.SystemFreshnessSnapshot, len(snapshots))
	for _, snap := range snapshots {
		census[snap.SystemSymbol] = snap
	}
	stallCycles, correctionCap := resolveManningStallConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	for _, post := range posts {
		key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

		// Scope: only a standing, FULLY-manned post with a freshness contract to breach.
		// An under-manned/unmanned post (or a sweep-once) is normal manning's / the
		// sizer's job.
		if post.Kind != domainScouting.PostKindStanding || !post.IsFullyManned() || post.FreshnessTarget <= 0 {
			h.forgetManningStall(key)
			continue
		}
		snap, ok := census[post.SystemSymbol]
		if !ok || snap.MarketCount <= 0 {
			h.forgetManningStall(key) // no census for this system yet — nothing to judge against
			continue
		}
		if !h.manningStallBreaching(key, snap.OldestAgeSeconds, post.FreshnessTarget) {
			continue // within its freshness contract, or worst-case age is improving (advancing)
		}
		if h.noteManningStallCycle(key) < stallCycles {
			continue // debounce: still below the N consecutive-cycle threshold
		}
		h.resetManningStallCycle(key) // rate-limit: the next re-man is another N cycles away
		attempts := h.manningStallCorrections(key)
		h.emitManningStalled(ctx, cmd, post, snap, stallCycles, attempts, correctionCap)
		if attempts >= correctionCap {
			continue // backed off — the event carries it, no more tour churn on an unreachable market
		}
		h.tearDownSlots(ctx, cmd, post)
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Manning watchdog re-manned post %s but failed to persist the teardown: %v", post.SystemSymbol, err), nil)
		}
		h.bumpManningStallCorrections(key)
	}
}

// manningStallBreaching records this tick's worst-case age for a post and reports whether it is
// a STALL cycle: the age BREACHES the post's freshness target AND did not IMPROVE
// (drop) since last tick. A within-target or improving age is not a stall — it clears the post's
// debounce (both the consecutive-cycle count and the correction backoff), so the watchdog only
// ever fires on a sustained, non-recovering breach. The age baseline is always refreshed so the
// next tick can detect an improvement; the counters, not the baseline, are cleared on the
// healthy path (keeping the baseline avoids a first-observation flicker between clear and note).
func (h *RunScoutPostCoordinatorHandler) manningStallBreaching(key string, ageSeconds float64, target time.Duration) bool {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallLastAgeSeconds == nil {
		h.stallLastAgeSeconds = make(map[string]float64)
	}
	prev, hadPrev := h.stallLastAgeSeconds[key]
	h.stallLastAgeSeconds[key] = ageSeconds
	improving := hadPrev && ageSeconds < prev
	if ageSeconds <= target.Seconds() || improving {
		delete(h.stallCycles, key)
		delete(h.stallCorrections, key)
		return false
	}
	return true
}

// noteManningStallCycle increments and returns a post's consecutive stall-cycle count:
// the debounce that requires N cycles of unbroken, non-improving SLA breach before a re-man.
func (h *RunScoutPostCoordinatorHandler) noteManningStallCycle(key string) int {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallCycles == nil {
		h.stallCycles = make(map[string]int)
	}
	h.stallCycles[key]++
	return h.stallCycles[key]
}

// resetManningStallCycle clears only a post's consecutive-cycle count after a re-man fires,
// so the next re-man must re-earn the full N-cycle window — paces corrections at one
// per window (anti-thrash). The correction count and age baseline are deliberately kept.
func (h *RunScoutPostCoordinatorHandler) resetManningStallCycle(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	delete(h.stallCycles, key)
}

// manningStallCorrections returns how many times the watchdog has already re-manned a post in
// the current stall episode — the failed-correction backoff counter.
func (h *RunScoutPostCoordinatorHandler) manningStallCorrections(key string) int {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	return h.stallCorrections[key]
}

// bumpManningStallCorrections records one more re-man of a post; once this reaches the
// correction cap the watchdog backs off to the event only.
func (h *RunScoutPostCoordinatorHandler) bumpManningStallCorrections(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallCorrections == nil {
		h.stallCorrections = make(map[string]int)
	}
	h.stallCorrections[key]++
}

// forgetManningStall drops a post's entire stall episode — its age baseline, cycle
// count, and correction count — when it falls out of the watchdog's scope (no longer standing,
// no longer fully manned, or no census). A later re-entry starts a fresh episode.
func (h *RunScoutPostCoordinatorHandler) forgetManningStall(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	delete(h.stallLastAgeSeconds, key)
	delete(h.stallCycles, key)
	delete(h.stallCorrections, key)
}

// emitManningStalled records the DEFERRED scout.post_manning_stalled captain event for a stalled
// post and logs it, so a silent fully-manned post is VISIBLE rather than quietly stale
// — and stays visible after the watchdog has backed off (backedOff carries that state). Mirrors
// warnUndersizedPosts' event idiom; a nil event store (unwired) is a no-op, so the re-man still
// happens without observability wired.
func (h *RunScoutPostCoordinatorHandler) emitManningStalled(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, snap domainScouting.SystemFreshnessSnapshot, stallCycles, attempts, correctionCap int) {
	backedOff := attempts >= correctionCap
	if h.eventStore != nil {
		_ = h.eventStore.Record(ctx, &captain.Event{
			Type: captain.EventScoutPostManningStalled, Ship: post.SystemSymbol, PlayerID: cmd.PlayerID.Value(),
			Payload: fmt.Sprintf(`{"system":%q,"markets":%d,"hulls":%d,"oldest_age_secs":%d,"freshness_secs":%d,"stall_cycles":%d,"cycle_samples":%d,"corrections":%d,"backed_off":%t}`,
				post.SystemSymbol, snap.MarketCount, post.HullBudget(), int(snap.OldestAgeSeconds), int(post.FreshnessTarget.Seconds()), stallCycles, snap.CycleSamples, attempts, backedOff),
		})
	}
	action := "scout_post_manning_stalled"
	verb := "re-manning it"
	if backedOff {
		verb = "backing off — the tour keeps going silent on a likely-unreachable market; needs operator attention"
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout post %s fully manned but silent: worst-case market age %ds past its %s freshness target, no new telemetry for %d cycles — %s", post.SystemSymbol, int(snap.OldestAgeSeconds), post.FreshnessTarget.Round(time.Second), stallCycles, verb), map[string]interface{}{
		"action":          action,
		"system_symbol":   post.SystemSymbol,
		"oldest_age_secs": int(snap.OldestAgeSeconds),
		"stall_cycles":    stallCycles,
		"corrections":     attempts,
		"backed_off":      backedOff,
	})
}

// reconcileMannedSlots runs pass 1 over one post's slots. It returns true when the
// post was retired (a completed sweep-once), so the caller skips it in later passes.
func (h *RunScoutPostCoordinatorHandler) reconcileMannedSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	running, completed, removed map[string]bool,
) bool {
	logger := common.LoggerFromContext(ctx)

	// Respawn accounting: track, across this post's slots, whether any tour was
	// observed HEALTHY (a spawn that survived — resets the consecutive-respawn streak) and
	// whether any dead tour was respawned this tick (advances it). Applied once per post after
	// the slot loop so a multi-hull post is accounted per-tick, not per-slot, and any one
	// healthy slot resets the whole post's streak.
	sawHealthy := false
	sawRespawn := false

	for _, slot := range post.Slots() {
		hull := slot.AssignedHull()
		if hull == "" {
			continue
		}
		tourID := slot.TourContainerID()

		// REPAIR: the assigned hull is no longer in the post's system. Its
		// in-system tour can never navigate the post's waypoints, so it crash-respawn-loops.
		// Release it unconditionally (even if momentarily RUNNING): stop the tour, free the
		// hull, clear the slot. Pass 2 then re-mans with an in-system satellite or parks.
		if h.hullOutOfSystem(ctx, cmd, hull, post.SystemSymbol) {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_respawn")
			logger.Log("INFO", fmt.Sprintf("Released cross-system assignment: hull %s is not in post %s's system — returned to pool for in-system re-matching", hull, post.SystemSymbol), map[string]interface{}{
				"action":        "scout_post_cross_system_repair",
				"system_symbol": post.SystemSymbol,
				"ship_symbol":   hull,
			})
			slot.SetAssignedHull("")
			slot.SetTourContainerID("")
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to clear cross-system assignment on post %s: %v", post.SystemSymbol, err), nil)
			}
			continue
		}

		// A live tour is healthy — never disturb it. The spawn that produced it survived to
		// this tick, so it resets any consecutive-respawn streak.
		if tourID != "" && running[tourID] {
			sawHealthy = true
			continue
		}

		// A sweep-once post whose tour COMPLETED has done its one job: release the hull and
		// retire the post so its satellite flows to the next unmanned post. Sweep-once is
		// always single-hull (HullBudget clamps it), so this is the only slot.
		if post.Kind == domainScouting.PostKindSweepOnce && tourID != "" && completed[tourID] {
			h.releaseHull(ctx, cmd, hull, "sweep_once_complete")
			if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to retire completed sweep-once post %s: %v", post.SystemSymbol, err), nil)
			} else {
				removed[post.SystemSymbol] = true
				logger.Log("INFO", fmt.Sprintf("Retired completed sweep-once post %s (hull %s released)", post.SystemSymbol, hull), map[string]interface{}{
					"action":        "scout_post_sweep_complete",
					"system_symbol": post.SystemSymbol,
				})
				return true
			}
			continue
		}

		// Otherwise the tour is dead/missing/crashed: free the hull and clear the slot. Pass 2
		// re-mans it — with this same hull, since it is idle in the post's system — over the
		// SAME partition (the slot's frozen markets are untouched), so it respawns within a tick.
		// This is the respawn the cap bounds: a tour crashing on a PERSISTENT reason lands
		// here every tick, so sawRespawn feeds accountRespawn below.
		h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_respawn")
		slot.SetAssignedHull("")
		slot.SetTourContainerID("")
		sawRespawn = true
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear assignment on post %s: %v", post.SystemSymbol, err), nil)
		}
	}

	// Advance or reset the persisted respawn-attempt counter and park the post once it
	// exhausts the cap, so a persistently-crashing tour stops respawn-looping at tick cadence.
	h.accountRespawn(ctx, cmd, post, sawHealthy, sawRespawn)
	return false
}

// resolveRespawnCap resolves the respawn-loop cap for this coordinator: whether the cap is
// LIVE (RespawnCapDisabled is the RULINGS #5 escape) and, when live, the consecutive-respawn
// ceiling ([scouting] respawn_attempt_cap, else defaultScoutRespawnAttemptCap). Mirrors
// repositionFailureCooldown's <= 0 -> default shape.
func resolveRespawnCap(cmd *RunScoutPostCoordinatorCommand) (attemptCap int, enabled bool) {
	if cmd.RespawnCapDisabled {
		return 0, false
	}
	if cmd.RespawnAttemptCap <= 0 {
		return defaultScoutRespawnAttemptCap, true
	}
	return cmd.RespawnAttemptCap, true
}

// accountRespawn advances or resets a post's PERSISTED consecutive-respawn counter after one
// reconcile pass over its slots, and parks the post once the counter exhausts the cap.
// A tour observed HEALTHY this tick (sawHealthy) means the last spawn survived, so the streak
// resets and any park is lifted — the cap counts CONSECUTIVE failures, not lifetime. A dead tour
// respawned this tick (sawRespawn) advances the streak; on reaching the cap the post is parked for
// defaultRespawnParkWindow (RespawnParkedUntil) instead of respawned yet again, and the exhaustion
// is logged (naturally rate-limited to one line per window, since a parked post spawns nothing to
// respawn until the window elapses and it retries once). Both fields persist so the cap survives a
// daemon restart rather than the crash-loop resuming at tick cadence. Healthy wins over respawn,
// so any one live slot resets a multi-hull post's whole-post streak. Disabled => a no-op,
// respawn-every-tick behavior.
func (h *RunScoutPostCoordinatorHandler) accountRespawn(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, sawHealthy, sawRespawn bool) {
	attemptCap, enabled := resolveRespawnCap(cmd)
	if !enabled {
		return
	}
	switch {
	case sawHealthy:
		if post.RespawnAttempts == 0 && post.RespawnParkedUntil.IsZero() {
			return // already clean — nothing to persist
		}
		post.RespawnAttempts = 0
		post.RespawnParkedUntil = time.Time{}
	case sawRespawn:
		post.RespawnAttempts++
		if post.RespawnAttempts < attemptCap {
			break
		}
		post.RespawnParkedUntil = h.clock.Now().Add(defaultRespawnParkWindow)
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout post %s exhausted its respawn cap (%d consecutive dead-tour respawns) — parking for %s; the tour keeps dying on a persistent reason and needs operator attention", post.SystemSymbol, post.RespawnAttempts, defaultRespawnParkWindow), map[string]interface{}{
			"action":           "scout_post_respawn_capped",
			"system_symbol":    post.SystemSymbol,
			"respawn_attempts": post.RespawnAttempts,
		})
	default:
		return // neither a respawn nor a healthy tour — nothing to account
	}
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to persist respawn accounting on post %s: %v", post.SystemSymbol, err), nil)
	}
}

// reconcileRepositioningSlots runs pass 1.5 over one post's slots: reclaim any ended relay
// and clear its reference so pass 2 re-evaluates the slot. The relay's TERMINAL
// disposition decides what happens to the post's dispatch cooldown:
//   - FAILED (the unroutable verdict): arm the LONG failure cooldown and count the attempt,
//     so the coordinator stops respawning the same corpse every few minutes and the freed
//     probe rotates to the next candidate this tick.
//   - COMPLETED (the probe arrived): reset the streak and clear any stale cooldown — pass 2a
//     mans it in-system, and a post that finally succeeded starts clean.
//   - neither (restart-interrupted / fast opaque exit): keep only the short dispatch floor
//     armed at dispatch — not a routing failure, so it never arms the long cooldown.
func (h *RunScoutPostCoordinatorHandler) reconcileRepositioningSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	running map[string]bool,
	completed map[string]bool,
	failed map[string]bool,
) {
	logger := common.LoggerFromContext(ctx)
	for _, slot := range post.Slots() {
		relayID := slot.RepositionContainerID()
		if slot.AssignedHull() != "" || relayID == "" {
			continue
		}
		if running[relayID] {
			continue // relay airborne — leave it; pass 2 skips this slot
		}

		h.reclaimHullFromContainer(ctx, cmd, relayID, "scout_reposition_ended")
		key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())
		switch {
		case failed[relayID]:
			cooldown := repositionFailureCooldown(cmd)
			attempt := h.noteRepositionFailure(key, cooldown)
			logger.Log("WARNING", fmt.Sprintf("Scout reposition relay for post %s FAILED (attempt %d, container %s) — cooling down %s, freeing the probe to the next candidate", post.SystemSymbol, attempt, relayID, cooldown), map[string]interface{}{
				"action":        "scout_reposition_failed",
				"system_symbol": post.SystemSymbol,
				"attempt":       attempt,
				"relay":         relayID,
				"cooldown_secs": int(cooldown.Seconds()),
			})
		case completed[relayID]:
			h.resetRepositionFailures(key)
			logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s arrived (container %s) — hull idle in-system, re-manning locally", post.SystemSymbol, relayID), map[string]interface{}{
				"action":        "scout_reposition_arrived",
				"system_symbol": post.SystemSymbol,
				"relay":         relayID,
			})
		default:
			logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s ended (container %s not running) — re-evaluating next tick", post.SystemSymbol, relayID), map[string]interface{}{
				"action":        "scout_reposition_relay_ended",
				"system_symbol": post.SystemSymbol,
			})
		}

		slot.SetRepositionContainerID("")
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear ended reposition relay on post %s: %v", post.SystemSymbol, err), nil)
		}
	}
}

// isRespawnParked reports whether a post is currently inside its respawn-cap backoff
// window — it exhausted the consecutive-respawn cap and is not yet due for a retry. Pass 2 skips
// such a post so a persistently-crashing tour is not respawned every tick. A zero deadline (never
// capped, or reset by a healthy tour / market-drift re-cut) reads false, and disabling the cap
// (RULINGS #5) reads false for every post, immediately lifting any park.
func (h *RunScoutPostCoordinatorHandler) isRespawnParked(post *domainScouting.ScoutPost, capEnabled bool) bool {
	return capEnabled && !post.RespawnParkedUntil.IsZero() && h.clock.Now().Before(post.RespawnParkedUntil)
}

// unmannedSlotTargets collects every slot that pass 2 should man: unmanned, not
// repositioning, in a non-retired, non-respawn-parked post. Standing posts sort before sweep-once
// (the freshness backbone is manned first), deterministic by system within a kind, and
// primary-before-extra within a post — so manning order is stable and testable.
func (h *RunScoutPostCoordinatorHandler) unmannedSlotTargets(posts []*domainScouting.ScoutPost, removed map[string]bool, spreadDisabled, respawnCapEnabled bool) []slotTarget {
	ordered := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		if h.isRespawnParked(post, respawnCapEnabled) {
			continue // parked in its respawn-cap backoff window — none of its slots man this tick
		}
		ordered = append(ordered, post)
	}
	sortPostsByPriority(ordered)

	// Group each post's unmanned slots in slot order, keeping the posts in priority
	// order. maxDepth is the deepest post's unmanned-slot count.
	perPost := make([][]slotTarget, 0, len(ordered))
	maxDepth := 0
	for _, post := range ordered {
		var slots []slotTarget
		for _, slot := range post.Slots() {
			if slot.AssignedHull() != "" || slot.RepositionContainerID() != "" {
				continue
			}
			slots = append(slots, slotTarget{post: post, slot: slot})
		}
		if len(slots) == 0 {
			continue
		}
		perPost = append(perPost, slots)
		if len(slots) > maxDepth {
			maxDepth = len(slots)
		}
	}

	targets := make([]slotTarget, 0, len(ordered))
	if spreadDisabled {
		// Depth-first (disable escape): every one of a post's slots before the next
		// post's — byte-identical for single-hull posts.
		for _, slots := range perPost {
			targets = append(targets, slots...)
		}
		return targets
	}

	// Coverage-first: interleave by slot TIER across the priority-ordered posts —
	// every post's first unmanned slot, THEN every post's second, and so on. With a scarce
	// idle-probe pool (pass 2b consumes one satellite per target in order) this spreads one
	// probe per uncovered system before piling a multi-hull post's extra slots, so distinct
	// high-value systems stop going dark while the pool drains into one post's N slots. The
	// FULL set of targets is unchanged — only the order — so once coverage is met a multi-hull
	// post still fills all its slots; single-hull-only fleets are unaffected (one tier).
	for depth := 0; depth < maxDepth; depth++ {
		for _, slots := range perPost {
			if depth < len(slots) {
				targets = append(targets, slots[depth])
			}
		}
	}
	return targets
}

// ensurePartitions materializes a multi-probe post's N DISJOINT market partitions.
// It is a no-op for a single-hull post (HullBudget 1 -> no partition, the primary
// tours all markets) and for a multi-probe post ALREADY partitioned at its current
// budget AND not yet drifted enough to re-cut — so it re-partitions on a hull-budget
// change (unconditional) or on a DEBOUNCED market-set drift (virgin discovery adds
// markets to a system after a post's tours are already cut, and a market discovered
// post-cut belongs to NO partition — it goes permanently stale even though the post
// reads fully manned; removals fold into the same check), never on every tick. On any
// re-cut of a running post it stops the existing tours/relays (their markets change),
// reclaims their hulls, and rebuilds the slots with fresh partitions; pass 2 re-mans
// them. Fails closed: no routing client, no markets, or a VRP error leaves an
// UNPARTITIONED post un-partitioned (it parks) and retries next tick — symmetrically,
// an ALREADY-stable-and-partitioned post hitting one of those same conditions just
// keeps touring its existing (possibly stale) partition rather than being torn down
// over a transient discovery hiccup or a missing routing client.
//
// API-BUDGET INVARIANT: partitioning changes WHERE probes scan, not HOW MUCH. Total
// scans/hour ~= markets / freshness-target regardless of N — N smaller partitions
// each paced to the freshness target (circuitPaceInterval, scout_tour.go) sum to one
// scan per market per freshness window. More probes buy fresher data, NOT more API calls.
func (h *RunScoutPostCoordinatorHandler) ensurePartitions(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	budget := post.HullBudget()
	key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

	// HULL-BUDGET DEBOUNCE. The freshness sizer's per-post budget can oscillate ±1 on
	// normal demand-noise; re-partitioning on every swing stops the post's tours and
	// RE-SCANS its markets — the API burn. Absorb a transient swing: on an
	// already-MATERIALIZED post (one with live slots/tours a re-cut would disrupt) act
	// on a budget that differs from the currently-cut partition only once the SAME new
	// budget has PERSISTED the debounce window; until then hold the current cut (budget
	// := physicalBudget), so the downstream logic keeps touring the existing partition
	// and still honors an independent market-set drift. A FIRST partition (a fresh,
	// un-materialized post) is never debounced — its budget lands immediately. A
	// genuine PERSISTENT change re-partitions once the window closes; a swing that
	// reverts clears the pending count and never re-cuts.
	physicalBudget := len(post.ExtraSlots) + 1
	materialized := len(post.ExtraSlots) > 0 || post.AssignedHull != "" || post.TourContainerID != "" || post.RepositionContainerID != ""
	switch {
	case budget == physicalBudget:
		h.clearBudgetChangePending(key) // stable, or a swing reverted — forget any pending change.
	case materialized:
		debounceCycles := cmd.BudgetChangeDebounceCycles
		if debounceCycles <= 0 {
			debounceCycles = defaultBudgetChangeDebounceCycles
		}
		if cycles := h.noteBudgetChangePending(key, budget); cycles < debounceCycles {
			logger.Log("INFO", fmt.Sprintf("Scout post %s hull budget %d→%d — below re-partition debounce (%d/%d cycles), holding current partition", post.SystemSymbol, physicalBudget, budget, cycles, debounceCycles), map[string]interface{}{
				"action":         "scout_post_budget_change_pending",
				"system_symbol":  post.SystemSymbol,
				"current_budget": physicalBudget,
				"pending_budget": budget,
				"pending_cycles": cycles,
			})
			budget = physicalBudget // hold this tick: keep touring the existing cut.
		} else {
			h.clearBudgetChangePending(key) // persisted — act on the change and reset the debounce.
		}
	}

	if budget <= 1 {
		// Single-hull (or sweep-once): a genuine single-hull post carries no partition
		// state and this is a no-op (byte-identical to pre-enry). But a post REDUCED from
		// multi-probe (hulls lowered to 1, or converted to sweep-once) still carries stale
		// extra slots / partition — tear them down so it reverts to the single-slot shape,
		// freeing the surplus probes to the pool. Pass 2 then re-mans the primary over ALL
		// markets.
		h.clearDriftPending(key) // no longer partitioned — any pending drift episode is moot.
		if len(post.ExtraSlots) > 0 || len(post.PrimaryPartition) > 0 {
			h.tearDownSlots(ctx, cmd, post)
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to revert post %s to single-hull: %v", post.SystemSymbol, err), nil)
				return
			}
			logger.Log("INFO", fmt.Sprintf("Scout post %s hull budget reduced to %d — reverted to single-hull, surplus probes freed", post.SystemSymbol, budget), map[string]interface{}{
				"action":        "scout_post_reverted_single_hull",
				"system_symbol": post.SystemSymbol,
			})
		}
		return
	}

	// stableAtBudget: already partitioned at this budget. A budget CHANGE always
	// re-cuts unconditionally (below); a stable budget only re-cuts if the market SET
	// has drifted enough to debounce-trigger — checked once the current market list is known.
	stableAtBudget := len(post.ExtraSlots) == budget-1

	if h.routingClient == nil {
		if stableAtBudget {
			return // can't check for drift without a routing client — the existing partition stands.
		}
		logger.Log("WARNING", fmt.Sprintf("Scout post %s wants %d hulls but no routing client is wired — cannot partition; parking", post.SystemSymbol, budget), map[string]interface{}{
			"action":        "scout_post_partition_unavailable",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets to partition post %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		if stableAtBudget {
			return // an already-partitioned post keeps touring its existing partition.
		}
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot partition, post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_post_partition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	driftTrigger := ""
	if stableAtBudget {
		drifted, oldMarketCount := marketSetDrift(post, markets)
		if len(drifted) == 0 {
			h.clearDriftPending(key)
			return // stable: same hulls, same markets — no re-cut.
		}

		threshold := cmd.MarketDriftThreshold
		if threshold <= 0 {
			threshold = defaultMarketDriftThreshold
		}
		maxAge := time.Duration(cmd.MarketDriftMaxAgeSecs) * time.Second
		if maxAge <= 0 {
			maxAge = defaultMarketDriftMaxAge
		}
		age := h.noteDriftPending(key)

		switch {
		case len(drifted) >= threshold:
			driftTrigger = "threshold"
		case age >= maxAge:
			driftTrigger = "age"
		default:
			// Below both triggers — debounce. Keep touring the existing (now slightly
			// stale) partition a while longer rather than thrash the fleet on every
			// single new/removed market.
			logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets) — below re-cut threshold, waiting", post.SystemSymbol, len(drifted)), map[string]interface{}{
				"action":         "scout_post_market_drift_pending",
				"system_symbol":  post.SystemSymbol,
				"drifted":        len(drifted),
				"drift_age_secs": int(age.Seconds()),
			})
			return
		}

		logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets, trigger=%s) — re-cutting partitions", post.SystemSymbol, len(drifted), driftTrigger), map[string]interface{}{
			"action":           "scout_post_market_drift_detected",
			"system_symbol":    post.SystemSymbol,
			"trigger":          driftTrigger,
			"drifted_markets":  len(drifted),
			"old_market_count": oldMarketCount,
			"new_market_count": len(markets),
			"drift_age_secs":   int(age.Seconds()),
		})
	}

	partitions, err := h.partitionMarkets(ctx, cmd, post.SystemSymbol, markets, budget)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("VRP partition of post %s into %d tours failed: %v — parking, retry next tick", post.SystemSymbol, budget, err), map[string]interface{}{
			"action":        "scout_post_partition_failed",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	// Re-partition: stop any existing tours/relays (their markets change) and reclaim their
	// hulls before overwriting the slots. On first partition (no slots yet) this is a no-op.
	repartition := len(post.ExtraSlots) > 0 || post.AssignedHull != "" || post.RepositionContainerID != ""
	h.tearDownSlots(ctx, cmd, post)

	post.PrimaryPartition = partitions[0]
	post.ExtraSlots = make([]domainScouting.ScoutPostSlot, budget-1)
	for i := 1; i < budget; i++ {
		post.ExtraSlots[i-1] = domainScouting.ScoutPostSlot{Partition: partitions[i]}
	}
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Partitioned post %s but failed to persist: %v", post.SystemSymbol, err), nil)
		return
	}
	h.clearDriftPending(key)

	action := "scout_post_partitioned"
	verb := "Partitioned"
	switch {
	case driftTrigger != "":
		action = "scout_post_repartitioned"
		verb = fmt.Sprintf("Re-cut (market-set drift, trigger=%s)", driftTrigger)
	case repartition:
		action = "scout_post_repartitioned"
		verb = "Re-partitioned (hull budget changed)"
	}
	logger.Log("INFO", fmt.Sprintf("%s scout post %s into %d disjoint tours over %d markets", verb, post.SystemSymbol, budget, len(markets)), map[string]interface{}{
		"action":        action,
		"system_symbol": post.SystemSymbol,
		"hulls":         budget,
		"markets":       len(markets),
	})
}

// ensureSingleHullFreshness is the single-hull mirror of ensurePartitions' market-set
// drift re-cut: a single-hull standing post's tour is spawned once with the system's
// market list AT THAT MOMENT (spawnTour<-slotMarkets<-discoverMarkets) and never
// re-reads it afterward — executeMultiMarketTour only re-derives markets at a respawn,
// and executeStationaryScout (chosen when the system had exactly one known market at
// spawn) has no circuit-boundary hook at all. A market discovered after spawn is
// therefore never toured until the post re-mans for an unrelated reason. This closes
// that gap the same way as the partitioned case: tear the post down (which pass 2
// immediately re-mans, and slotMarkets/discoverMarkets gives the new tour a FRESH
// market list) once the live discovered set has drifted from the snapshot taken at
// last manning by at least MarketDriftThreshold markets, or the drift has been pending
// at least MarketDriftMaxAgeSecs — reusing the same thresholds/config fields and
// diffMarketSets' set-diff semantics as the partitioned path rather than inventing new ones.
//
// Scoped to standing, single-hull, CURRENTLY MANNED posts only:
//   - multi-hull posts are ensurePartitions' job (skip: HullBudget() > 1);
//   - sweep-once posts are a one-shot frontier sweep that auto-retires on completion,
//     not a freshness target (skip: Kind != PostKindStanding);
//   - an unmanned/repositioning post has no live tour to go stale — pass 2a gives it a
//     fresh market list the moment it mans it (skip: AssignedHull/TourContainerID
//     empty).
func (h *RunScoutPostCoordinatorHandler) ensureSingleHullFreshness(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	if post.HullBudget() > 1 || post.Kind != domainScouting.PostKindStanding {
		return
	}
	if post.AssignedHull == "" || post.TourContainerID == "" {
		return
	}

	logger := common.LoggerFromContext(ctx)
	key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets to check freshness of post %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		return // transient discovery hiccup — leave the tour running, don't touch state.
	}

	snapshot, ok := h.singleHullSnapshot(key)
	if !ok {
		// No baseline yet — a fresh handler (daemon restart lost the in-memory map).
		// Adopt the CURRENT markets as the new baseline without respawning: an
		// already-healthy tour surviving a restart is not maximal drift, and treating
		// it as such would respawn every standing post fleet-wide on every restart
		// (mirrors driftPendingSince's own restart-safety rationale).
		h.setSingleHullSnapshot(key, markets)
		return
	}

	drifted, oldMarketCount := diffMarketSets(snapshot, markets)
	if len(drifted) == 0 {
		h.clearSingleHullDriftPending(key)
		return // stable: same markets — no respawn.
	}

	threshold := cmd.MarketDriftThreshold
	if threshold <= 0 {
		threshold = defaultMarketDriftThreshold
	}
	maxAge := time.Duration(cmd.MarketDriftMaxAgeSecs) * time.Second
	if maxAge <= 0 {
		maxAge = defaultMarketDriftMaxAge
	}
	age := h.noteSingleHullDriftPending(key)

	driftTrigger := ""
	switch {
	case len(drifted) >= threshold:
		driftTrigger = "threshold"
	case age >= maxAge:
		driftTrigger = "age"
	default:
		// Below both triggers — debounce, exactly like ensurePartitions: keep touring
		// the existing (now slightly stale) market list a while longer rather than
		// thrash the fleet on every single new/removed market.
		logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets) — below re-cut threshold, waiting", post.SystemSymbol, len(drifted)), map[string]interface{}{
			"action":         "scout_post_single_hull_drift_pending",
			"system_symbol":  post.SystemSymbol,
			"drifted":        len(drifted),
			"drift_age_secs": int(age.Seconds()),
		})
		return
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets, trigger=%s) — respawning single-hull tour", post.SystemSymbol, len(drifted), driftTrigger), map[string]interface{}{
		"action":           "scout_post_single_hull_market_drift_detected",
		"system_symbol":    post.SystemSymbol,
		"trigger":          driftTrigger,
		"drifted_markets":  len(drifted),
		"old_market_count": oldMarketCount,
		"new_market_count": len(markets),
		"drift_age_secs":   int(age.Seconds()),
	})

	h.tearDownSlots(ctx, cmd, post)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to persist single-hull freshness teardown for post %s: %v", post.SystemSymbol, err), nil)
	}
	h.clearSingleHullDriftPending(key)
	h.clearSingleHullSnapshot(key)
}

// marketSetDrift returns the symbols where a partitioned post's CURRENT discovered
// market set differs from the union of its persisted partitions, plus the size of
// that union (the "old market count" for the re-cut's observability log). A market
// discovered after the post was last cut belongs to no partition (an addition); a
// market still assigned to a partition but no longer discovered (a removal) is
// included identically — both fold into ONE set so additions and removals debounce
// the same way, with no special-casing. Sorted for a deterministic re-cut log and
// test assertions.
func marketSetDrift(post *domainScouting.ScoutPost, currentMarkets []string) (drifted []string, unionSize int) {
	union := make([]string, 0, len(post.PrimaryPartition))
	union = append(union, post.PrimaryPartition...)
	for _, slot := range post.ExtraSlots {
		union = append(union, slot.Partition...)
	}
	return diffMarketSets(union, currentMarkets)
}

// diffMarketSets is marketSetDrift's set-diff core, factored out so a SINGLE-hull
// post's freshness check (ensureSingleHullFreshness) can reuse the identical
// symmetric-difference semantics against its own snapshot baseline instead of a
// partitioned post's PrimaryPartition/ExtraSlots union — a single-hull post never
// carries a partition (see ScoutPost.PrimaryPartition's doc comment), so it has no
// union to read here. oldMarkets is the baseline (a partition union, or a prior
// discovered-markets snapshot); currentMarkets is the live discovered set. Returns the
// symbols present in one set but not the other — additions AND removals, no
// special-casing — sorted for a deterministic log/test assertions, plus the
// deduplicated size of oldMarkets for the caller's "old market count" observability
// log.
func diffMarketSets(oldMarkets, currentMarkets []string) (drifted []string, unionSize int) {
	old := make(map[string]bool, len(oldMarkets))
	for _, m := range oldMarkets {
		old[m] = true
	}
	current := make(map[string]bool, len(currentMarkets))
	for _, m := range currentMarkets {
		current[m] = true
	}

	for _, m := range currentMarkets {
		if !old[m] {
			drifted = append(drifted, m) // discovered, but not in the baseline yet
		}
	}
	for m := range old {
		if !current[m] {
			drifted = append(drifted, m) // in the baseline, but no longer discovered
		}
	}
	sort.Strings(drifted)
	return drifted, len(old)
}

// tearDownSlots stops every slot's tour/relay container and reclaims its hull ahead of
// a re-partition, then clears the assignments in memory. Best-effort: a hull the
// coordinator fails to reclaim here is reclaimed by pass 1 on a later tick.
func (h *RunScoutPostCoordinatorHandler) tearDownSlots(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	for _, slot := range post.Slots() {
		if tourID := slot.TourContainerID(); tourID != "" {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_repartition")
		}
		if relayID := slot.RepositionContainerID(); relayID != "" {
			_ = h.daemonClient.StopContainer(ctx, relayID)
			h.reclaimHullFromContainer(ctx, cmd, relayID, "scout_post_repartition")
		}
	}
	post.AssignedHull = ""
	post.TourContainerID = ""
	post.RepositionContainerID = ""
	post.PrimaryPartition = nil
	post.ExtraSlots = nil
	// A market-set re-cut is a genuine state change — the post's tour will fly a
	// different market list, so its consecutive-respawn streak (which measured failures
	// under the old state) no longer applies. Clear it and lift any respawn park so the
	// re-cut post gets a fresh chance.
	post.RespawnAttempts = 0
	post.RespawnParkedUntil = time.Time{}
}

// partitionMarkets solves the VRP that splits markets into n DISJOINT per-probe tours,
// reusing the SAME PartitionFleet the scout-markets verb uses. The N probes are
// synthetic slots anchored at a common waypoint (the lexicographically-smallest market),
// so the partition depends only on (n, markets, geometry) and is STABLE across which real
// probes are present; the caller freezes and persists the result. It guarantees complete,
// disjoint coverage: any market the VRP fails to place (e.g. the fallback mock's 1-per-ship
// stub) is appended to slot 0, so no market is silently dropped.
func (h *RunScoutPostCoordinatorHandler) partitionMarkets(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, systemSymbol string, markets []string, n int) ([][]string, error) {
	if h.routingClient == nil {
		return nil, fmt.Errorf("no routing client wired")
	}

	anchor := markets[0]
	for _, m := range markets[1:] {
		if m < anchor {
			anchor = m
		}
	}

	slotIDs := make([]string, n)
	configs := make(map[string]*routing.ShipConfigData, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-slot-%d", systemSymbol, i)
		slotIDs[i] = id
		configs[id] = &routing.ShipConfigData{
			CurrentLocation: anchor,
			FuelCapacity:    partitionAnchorFuelCapacity,
			EngineSpeed:     partitionAnchorEngineSpeed,
		}
	}

	var waypointData []*system.WaypointData
	if h.graphProvider != nil {
		if graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value()); err == nil {
			waypointData, _ = extractWaypointData(graphResult.Graph)
		}
	}

	resp, err := h.routingClient.PartitionFleet(ctx, &routing.VRPRequest{
		SystemSymbol:    systemSymbol,
		ShipSymbols:     slotIDs,
		MarketWaypoints: markets,
		ShipConfigs:     configs,
		AllWaypoints:    waypointData,
	})
	if err != nil {
		return nil, err
	}

	partitions := make([][]string, n)
	assigned := make(map[string]bool, len(markets))
	for i, id := range slotIDs {
		if tour, ok := resp.Assignments[id]; ok {
			for _, wp := range tour.Waypoints {
				if assigned[wp] {
					continue // keep partitions strictly disjoint
				}
				assigned[wp] = true
				partitions[i] = append(partitions[i], wp)
			}
		}
	}
	// Complete coverage: any market the VRP left unplaced goes to slot 0, so a partition
	// never silently drops a market (defense against a degraded/stub partitioner).
	for _, m := range markets {
		if !assigned[m] {
			partitions[0] = append(partitions[0], m)
			assigned[m] = true
		}
	}
	return partitions, nil
}

// slotMarkets returns the waypoints a slot's tour should scan: ALL the system's
// markets for a single-hull post (discovered fresh), or the slot's frozen partition
// for a multi-probe post.
func (h *RunScoutPostCoordinatorHandler) slotMarkets(ctx context.Context, post *domainScouting.ScoutPost, slot domainScouting.ScoutSlotRef) ([]string, error) {
	if post.HullBudget() <= 1 {
		return h.discoverMarkets(ctx, post.SystemSymbol)
	}
	return slot.Partition(), nil
}

// containerIDSet returns the set of container IDs in the given status for the player.
func (h *RunScoutPostCoordinatorHandler) containerIDSet(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, status string) (map[string]bool, error) {
	playerID := cmd.PlayerID.Value()
	summaries, err := h.containerQuery.ListByStatusSimple(ctx, status, &playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s containers: %w", status, err)
	}
	set := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		set[s.ID] = true
	}
	return set, nil
}

// idleScoutSatellites returns the idle SATELLITE-role hulls eligible to man a post:
// idle, scout-type, and not dedicated to some OTHER fleet. The dedication filter is
// the first line of the poach guard (RULINGS #7); ClaimShip enforces it atomically
// as the second. Non-satellite hulls (the command frigate, haulers) are never
// returned, so a post can never claim one.
func (h *RunScoutPostCoordinatorHandler) idleScoutSatellites(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) ([]*navigation.Ship, error) {
	ships, err := h.shipRepo.FindIdleByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find idle ships: %w", err)
	}
	var sats []*navigation.Ship
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != scoutPostFleet {
			continue // pinned to another fleet — never poach
		}
		sats = append(sats, ship)
	}
	// Deterministic order so selection is stable across ticks and testable.
	sort.Slice(sats, func(i, j int) bool {
		return sats[i].ShipSymbol() < sats[j].ShipSymbol()
	})
	return sats, nil
}

// deriveScanInterval computes a post's probe market-scan cadence from its
// freshness target: half the freshness window, clamped via clampScanInterval
// (scout_tour.go) to [scanIntervalFloor, scanIntervalCap] so
// neither an aggressive nor a lax freshness target can push the per-hull API cost
// outside the budgeted range. A zero/unset freshness target clamps to the floor,
// same as any other too-small value — the coordinator path has no "direct launch"
// default to fall back on.
func deriveScanInterval(freshness time.Duration) time.Duration {
	return clampScanInterval(freshness / 2)
}

// spawnTour persists a coordinator-managed scout_tour worker for hullSymbol over the
// slot's markets, atomically claims the hull to it, and starts it. The persisted config
// carries coordinator_id so restart recovery skips the tour and leaves respawning to this
// coordinator. Standing posts run an infinite tour; sweep-once posts a single one. The
// tour's ScanInterval is derived from the POST's freshness target, so every probe on a
// multi-probe post paces its own partition against one freshness target.
func (h *RunScoutPostCoordinatorHandler) spawnTour(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	hullSymbol string,
	markets []string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	iterations := -1 // standing: keep the system fresh forever
	if post.Kind == domainScouting.PostKindSweepOnce {
		iterations = 1 // one pass, then the post auto-retires
	}

	workerID := utils.GenerateContainerID("scout_tour", hullSymbol)
	tourCmd := &ScoutTourCommand{
		PlayerID:      cmd.PlayerID,
		ShipSymbol:    hullSymbol,
		Markets:       markets,
		Iterations:    iterations,
		CoordinatorID: cmd.ContainerID,
		ScanInterval:  deriveScanInterval(post.FreshnessTarget),
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindScoutTour, workerID, uint(cmd.PlayerID.Value()), tourCmd); err != nil {
		return "", fmt.Errorf("failed to persist scout tour worker: %w", err)
	}

	// Atomic claim: rejects a hull pinned to another fleet at the DB, so a pin racing
	// discovery can never be poached. %w so callers can distinguish a dedication
	// rejection from a transient failure.
	if err := h.shipRepo.ClaimShip(ctx, hullSymbol, workerID, cmd.PlayerID, scoutPostFleet); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to claim satellite %s: %w", hullSymbol, err)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindScoutTour, workerID); err != nil {
		h.releaseHull(ctx, cmd, hullSymbol, "scout_tour_start_failed")
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to start scout tour worker: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Manned scout post %s with %s (tour %s, %d markets, iterations %d)", post.SystemSymbol, hullSymbol, workerID, len(markets), iterations), map[string]interface{}{
		"action":        "scout_post_manned",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   hullSymbol,
		"container_id":  workerID,
		"kind":          string(post.Kind),
	})
	return workerID, nil
}

// repositionUnmannedSlot jump-routes the fleet-wide nearest idle satellite to a slot
// with no in-system hull. It FAILS CLOSED at every gap — no gate graph, no idle
// satellite, an active backoff, an unserviceable/undiscoverable virgin system, or no
// jump-routable satellite — by parking the slot honest and returning. On success it claims
// the satellite to a new reposition container, records the relay on the slot, and arms the
// per-slot dispatch backoff. idleSats is a pointer so a dispatched satellite is removed from
// the shared pool for the rest of this tick.
func (h *RunScoutPostCoordinatorHandler) repositionUnmannedSlot(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	slot domainScouting.ScoutSlotRef,
	idleSats *[]*navigation.Ship,
	sourcePosts []*domainScouting.ScoutPost,
	relayCfg scoutRelayConfig,
) {
	logger := common.LoggerFromContext(ctx)
	key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())

	// No gate graph wired, or no idle satellite left this tick → cannot idle-reposition.
	// With a gate graph but NO idle probe, try a CROSS-SYSTEM reuse relay from an
	// over-covered system's surplus BEFORE parking. maybeRelaySurplusProbe returns
	// false immediately when the relay is disabled (flag off or no demand reader
	// wired), so a disabled coordinator parks honest with the in-system reason and a
	// greppable message. A nil gate graph short-circuits before the relay (nothing to
	// route over).
	if h.gateGraph == nil || len(*idleSats) == 0 {
		if h.gateGraph != nil && len(*idleSats) == 0 &&
			h.maybeRelaySurplusProbe(ctx, cmd, post, slot, key, sourcePosts, relayCfg) {
			return
		}
		h.parkNoInSystemSatellite(ctx, post)
		return
	}

	// A recent relay for this slot failed — don't hot-loop re-dispatch. Announce the
	// skip ONCE per cooldown episode: noteRepositionBackoffLogged keys the announcement
	// on the exact backoff deadline, so a new failure (a later deadline) re-announces
	// once and a steady cooldown stays quiet.
	if h.repositionBackedOff(key) {
		if h.noteRepositionBackoffLogged(key) {
			logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: reposition backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
				"action":        "scout_reposition_backoff",
				"system_symbol": post.SystemSymbol,
			})
		}
		return
	}

	// travel() needs a concrete destination waypoint in the target system; any market
	// serves (the relay just lands the hull there and the next in-system tick's tour rotates
	// to start from wherever it sits). Use the whole system's markets, not the slot's
	// partition, so the destination logic is byte-identical to s232 for a single-hull post.
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for reposition target %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		// Virgin frontier system: discover its waypoints presence-free, then retry.
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return // parked honest by discoverVirginMarkets
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	// Fleet-wide nearest idle satellite by jump-hop count over the stored adjacency,
	// bounded to the expendable-probe reposition reach. Fail-closed on unroutable.
	maxJumps := resolveMaxRepositionJumps(cmd)
	idx, hops, ok := h.selectNearestSatelliteByHops(ctx, *idleSats, post.SystemSymbol, maxJumps)
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no satellite within %d reposition jumps of the fleet — parked (fail-closed)", post.SystemSymbol, maxJumps), map[string]interface{}{
			"action":        "scout_reposition_unroutable",
			"system_symbol": post.SystemSymbol,
			"max_jumps":     maxJumps,
		})
		return
	}

	sat := (*idleSats)[idx]
	*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)

	// A manning reposition never charts the gate (ChartGateOnArrival=false) — it only
	// moves the hull into the post's system; the gate-reconcile sweep owns gate charting.
	relayID, err := h.spawnReposition(ctx, cmd, sat.ShipSymbol(), destWaypoint, maxJumps, false)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch reposition of %s to post %s: %v", sat.ShipSymbol(), post.SystemSymbol, err), nil)
		return
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails,
	// the backoff still prevents an immediate second relay to this slot next tick.
	h.noteRepositionDispatch(key)
	slot.SetRepositionContainerID(relayID)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dispatched reposition for post %s but failed to persist relay reference: %v", post.SystemSymbol, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: repositioning %s (%d jump(s) over stored adjacency, ≤%d bound, relay %s) → %s", post.SystemSymbol, sat.ShipSymbol(), hops, maxJumps, relayID, destWaypoint), map[string]interface{}{
		"action":        "scout_reposition_dispatch",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   sat.ShipSymbol(),
		"jumps":         hops,
		"max_jumps":     maxJumps,
		"destination":   destWaypoint,
		"relay":         relayID,
	})
}

// discoverVirginMarkets resolves the bootstrap chicken-and-egg for a post whose system
// has ZERO known market waypoints: it DISCOVERS the system's waypoints presence-free
// via the graph provider's cache-first GetGraph, persisting them era-scoped, then
// re-reads. markets found -> returns them (the caller repositions this tick); none ->
// parks UNSERVICEABLE (charted but barren); API error -> parks fail-closed. It arms the
// per-slot dispatch backoff (key) BEFORE the API call, so a marketless or API-erroring
// system is probed at most ONCE per window. With no graph provider wired it logs the
// plain "no markets, cannot reposition" park.
func (h *RunScoutPostCoordinatorHandler) discoverVirginMarkets(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	key string,
) []string {
	logger := common.LoggerFromContext(ctx)

	if h.graphProvider == nil {
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot reposition (nothing to scan), post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	// Arm the backoff BEFORE the API call: whether discovery finds markets, finds none, or
	// errors, this system is not probed again until the window elapses.
	h.noteRepositionDispatch(key)

	if _, err := h.graphProvider.GetGraph(ctx, post.SystemSymbol, false, cmd.PlayerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Virgin-system waypoint discovery for reposition target %s failed: %v — post parks (fail-closed), retrying after backoff", post.SystemSymbol, err), map[string]interface{}{
			"action":        "scout_reposition_discovery_failed",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to re-read markets for %s after discovery: %v", post.SystemSymbol, err), nil)
		return nil
	}
	if len(markets) == 0 {
		logger.Log("INFO", fmt.Sprintf("%s has no marketplaces — post is unserviceable; consider removing", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_post_unserviceable",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	logger.Log("INFO", fmt.Sprintf("Discovered %d market waypoint(s) in virgin system %s — repositioning now", len(markets), post.SystemSymbol), map[string]interface{}{
		"action":        "scout_reposition_virgin_discovered",
		"system_symbol": post.SystemSymbol,
		"markets":       len(markets),
	})
	return markets
}

// resolveMaxRepositionJumps returns the expendable-probe reposition reach: the
// launch-config [scouting] max_reposition_jumps, or defaultMaxRepositionJumps when
// unset (RULINGS #5, the <= 0 -> default idiom this file uses for every other knob).
func resolveMaxRepositionJumps(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.MaxRepositionJumps <= 0 {
		return defaultMaxRepositionJumps
	}
	return cmd.MaxRepositionJumps
}

// resolveGateReconcileMaxDispatch returns the gate-reconcile per-tick relay cap: the
// launch config's GateReconcileMaxDispatch when positive, else defaultGateReconcileMaxDispatch
// (RULINGS #5, the <= 0 => default idiom). This is the rate-budget guard on the sweep.
func resolveGateReconcileMaxDispatch(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.GateReconcileMaxDispatch <= 0 {
		return defaultGateReconcileMaxDispatch
	}
	return cmd.GateReconcileMaxDispatch
}

// gateUnchartedMarketSystems is the retroactive-backlog enumeration: the systems that
// are MARKET-KNOWN (a key in marketAges — the market repository swept at least one of
// its markets) but GATE-UNCHARTED (NOT a key with real edges in charted — the Adjacency
// key set is exactly the systems whose jump gate is charted). This is the market-swept-but-
// gate-empty set the strict pathfinder fails closed on, that chart-on-arrival alone cannot
// reach (such a system is never revisited once swept — the chicken-and-egg). A system with
// an empty edge slice is treated as uncharted (a "connects nowhere" set is not a charted
// gate). Sorted for deterministic, stable dispatch order. Pure — no store, no API.
func gateUnchartedMarketSystems(marketAges map[string]float64, charted map[string][]system.GateEdge) []string {
	uncharted := make([]string, 0, len(marketAges))
	for systemSymbol := range marketAges {
		if len(charted[systemSymbol]) > 0 {
			continue // its jump gate is already charted — not part of the backlog
		}
		uncharted = append(uncharted, systemSymbol)
	}
	sort.Strings(uncharted)
	return uncharted
}

// gateChartSweepTargets GENERALIZES the market-only enumeration to a widened target set:
// the UNION of the market-known-but-gate-uncharted backlog (gateUnchartedMarketSystems) and the
// traffic-markered uncharted TRANSIT systems (markeredGates — the era-scoped backoff markers a
// stale route-through 400 left behind), each minus the already-charted set. Deduped (a system
// that is BOTH market-known and markered is one target, drawing one probe) and sorted for a
// deterministic, stable dispatch order. markeredGates nil (an unwired provider or the
// disable-escape) collapses this to exactly gateUnchartedMarketSystems, the market-only
// behavior. Pure — no store, no API.
func gateChartSweepTargets(marketAges map[string]float64, markeredGates map[string]string, charted map[string][]system.GateEdge) []string {
	seen := make(map[string]struct{}, len(marketAges)+len(markeredGates))
	for _, systemSymbol := range gateUnchartedMarketSystems(marketAges, charted) {
		seen[systemSymbol] = struct{}{}
	}
	for systemSymbol := range markeredGates {
		if len(charted[systemSymbol]) > 0 {
			continue // its jump gate is already charted — a lingering marker is not a target
		}
		seen[systemSymbol] = struct{}{}
	}
	targets := make([]string, 0, len(seen))
	for systemSymbol := range seen {
		targets = append(targets, systemSymbol)
	}
	sort.Strings(targets)
	return targets
}

// reconcileGateChartSweep is the RETROACTIVE gate-reconcile pass (Part 2): it dispatches
// up to a BOUNDED number of LEFTOVER idle probes to UNCHARTED frontier gates so each
// probe lands on that system's jump gate and Part 1's chart-on-arrival
// (chartArrivedGate -> ChartPresentGate) fills its gate_edges. The target set is the UNION of
// two sources (gateChartSweepTargets):
//   - the market-known-but-gate-uncharted backlog (a market-swept system with empty
//     gate_edges the strict pathfinder strands hulls on);
//   - the traffic-markered MARKETLESS transit gates: uncharted systems a stale backoff
//     marker proves fleet traffic jumps THROUGH (MarkUnreadable is written on a real GetJumpGate
//     400). The market-scoped enumeration structurally can NEVER reach these — a system's
//     market status is unknown until its gate is charted. A marketless dead-end no route
//     crosses is never markered, so the scope stays bounded to traffic-touched gates (NOT
//     all reachable uncharted gates — that over-exploration is rejected).
//
// A market target is aimed at any market waypoint (Part 1 charts the GATE on the pre-market
// arrival hop); a marketless target is aimed at the gate WAYPOINT the marker recorded.
//
// SAFETY (HARD API-budget constraint):
//   - DEFAULT OFF (self-guards on GateReconcileEnabled): deploy-inert until armed. The marketless
//     widening is additionally reversible live via GateReconcileMarketlessDisabled (default ON).
//   - HARD-CAPPED at resolveGateReconcileMaxDispatch relays per tick — never a burst.
//   - Runs on the LEFTOVER idle pool AFTER manning (idleSats already drained by Pass 2),
//     so it never starves a post of a probe; a dispatched probe is spliced out of the pool.
//   - Idempotent: ChartPresentGate's store-first guard (Part 1) makes an arrival on an
//     already-charted system cost ZERO API, so a redundant relay is cheap.
//   - Fail-closed at every gap (no gate graph / no freshness provider / read error / no
//     routable satellite / no destination), and the per-target dispatch backoff prevents churn.
func (h *RunScoutPostCoordinatorHandler) reconcileGateChartSweep(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, idleSats *[]*navigation.Ship) {
	if !cmd.GateReconcileEnabled || h.gateGraph == nil || h.marketFreshnessProvider == nil {
		return
	}
	if len(*idleSats) == 0 {
		return // no leftover probe to spend on the backlog this tick
	}
	logger := common.LoggerFromContext(ctx)

	marketAges, err := h.marketFreshnessProvider.MaxAgeSecondsBySystem(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("gate-reconcile: market enumeration failed — skipping sweep this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_enumeration_failed",
		})
		return
	}
	charted, err := h.gateGraph.Adjacency(ctx)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("gate-reconcile: gate adjacency read failed — skipping sweep this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_adjacency_failed",
		})
		return
	}

	markeredGates := h.markeredUnchartedGates(ctx, cmd)

	targets := gateChartSweepTargets(marketAges, markeredGates, charted)
	if len(targets) == 0 {
		return // frontier fully charted — nothing to reconcile
	}

	maxDispatch := resolveGateReconcileMaxDispatch(cmd)
	maxJumps := resolveMaxRepositionJumps(cmd)
	dispatched := 0
	for _, target := range targets {
		if dispatched >= maxDispatch {
			break // rate-budget cap reached — the rest waits for the next tick
		}
		if len(*idleSats) == 0 {
			break // idle pool exhausted
		}
		key := gateReconcileBackoffKey(cmd.PlayerID.Value(), target)
		if h.repositionBackedOff(key) {
			continue // a relay for this target is already in flight / recently dispatched
		}
		idx, hops, ok := h.selectNearestSatelliteByHops(ctx, *idleSats, target, maxJumps)
		if !ok {
			continue // no idle probe can jump-route to this frontier system this tick
		}
		destWaypoint, ok := h.resolveGateChartDestination(ctx, target, markeredGates)
		if !ok {
			continue // no market waypoint AND no recorded gate to aim at right now — fail closed
		}

		sat := (*idleSats)[idx]
		*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)

		// A 0-hop dispatch (the probe is ALREADY in the target system) must chart the gate
		// itself — travelWithJumpBound's same-system branch would otherwise navigate it to
		// a market and return before charting, so the backlog never drains. A multi-hop
		// dispatch (hops>0) already charts on the jump-arrival hop, so it stays the plain relay.
		relayID, err := h.spawnReposition(ctx, cmd, sat.ShipSymbol(), destWaypoint, maxJumps, hops == 0)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("gate-reconcile: failed to dispatch %s to chart %s: %v", sat.ShipSymbol(), target, err), map[string]interface{}{
				"action":        "gate_reconcile_dispatch_failed",
				"system_symbol": target,
				"ship_symbol":   sat.ShipSymbol(),
			})
			continue
		}
		h.noteRepositionDispatch(key)
		dispatched++
		logger.Log("INFO", fmt.Sprintf("gate-reconcile: repositioning %s → %s (%d jump(s), ≤%d bound, relay %s) to chart its jump gate on arrival", sat.ShipSymbol(), target, hops, maxJumps, relayID), map[string]interface{}{
			"action":        "gate_reconcile_dispatch",
			"system_symbol": target,
			"ship_symbol":   sat.ShipSymbol(),
			"jumps":         hops,
			"max_jumps":     maxJumps,
			"destination":   destWaypoint,
			"relay":         relayID,
		})
	}
}

// markeredUnchartedGates reads the traffic-marker set (system -> recorded gate waypoint)
// the widened sweep charts alongside the market backlog. It fails SAFE to a market-only
// sweep at every gap: an unwired provider (nil) or the GateReconcileMarketlessDisabled
// escape returns nil (no marketless targets), and a provider read error is logged and swallowed
// (market-only this tick, never an aborted sweep) — mirroring the marketFreshnessProvider's
// fail-open contract. nil is a valid, empty markered set for gateChartSweepTargets.
func (h *RunScoutPostCoordinatorHandler) markeredUnchartedGates(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) map[string]string {
	if h.unreadableGateProvider == nil || cmd.GateReconcileMarketlessDisabled {
		return nil // widening unwired or pinned off — market-only
	}
	gates, err := h.unreadableGateProvider.UnreadableGates(ctx)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("gate-reconcile: marker enumeration failed — charting market-only this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_marker_enumeration_failed",
		})
		return nil
	}
	return gates
}

// resolveGateChartDestination picks the waypoint to aim a charting probe at: any market in
// the target (Part 1 charts the GATE on the pre-market arrival hop) when one exists, else
// the gate WAYPOINT the backoff marker recorded (the marketless-transit path). Trying
// markets FIRST keeps a market target's destination stable. ok=false when neither is
// available — no market waypoint yet AND no recorded gate (or an empty-string marker) —
// so the caller fails closed for this target without consuming a probe.
func (h *RunScoutPostCoordinatorHandler) resolveGateChartDestination(ctx context.Context, target string, markeredGates map[string]string) (string, bool) {
	markets, err := h.discoverMarkets(ctx, target)
	if err == nil && len(markets) > 0 {
		return pickRepositionDestination(markets), true
	}
	if gateWaypoint := markeredGates[target]; gateWaypoint != "" {
		return gateWaypoint, true
	}
	return "", false
}

// gateReconcileBackoffKey is the per-target dispatch-backoff key for the gate-chart sweep.
// It is DISTINCT from a post slot's backoffKey (a "gatereconcile|" prefix) so a gate-reconcile
// relay and a post reposition to the same system never share a backoff window — the two are
// independent dispatch decisions that happen to reuse the shared repositionBackoffUntil map.
func gateReconcileBackoffKey(playerID int, systemSymbol string) string {
	return fmt.Sprintf("gatereconcile|%d|%s", playerID, systemSymbol)
}

// repositionFailureCooldown resolves the FAILED-relay cooldown: the launch config's
// [scouting] reposition_failure_cooldown_secs when positive, else the 30-min default. Mirrors
// resolveMaxRepositionJumps' <= 0 -> default shape.
func repositionFailureCooldown(cmd *RunScoutPostCoordinatorCommand) time.Duration {
	if cmd.RepositionFailureCooldownSecs <= 0 {
		return defaultRepositionFailureCooldown
	}
	return time.Duration(cmd.RepositionFailureCooldownSecs) * time.Second
}

// selectNearestSatelliteByHops returns the index (into idleSats) of the satellite
// FEWEST jump hops from postSystem, its hop count, and ok=false when none can be
// jump-routed there. Distance is the RepositionPath BFS length over the PERSISTED
// stored adjacency bounded to maxJumps — the expendable-probe resolver that routes
// PAST unreadable frontier gates and reaches the multi-jump posts the strict
// MaxJumpPath=5 rejects. A satellite whose route errors is skipped (fail-closed). idleSats
// is pre-sorted by symbol, and the comparison is strict (< bestHops), so the lowest-symbol
// satellite wins an equal-hops tie.
func (h *RunScoutPostCoordinatorHandler) selectNearestSatelliteByHops(
	ctx context.Context,
	idleSats []*navigation.Ship,
	postSystem string,
	maxJumps int,
) (idx int, hops int, ok bool) {
	logger := common.LoggerFromContext(ctx)
	bestIdx, bestHops := -1, 0
	for i, sat := range idleSats {
		loc := sat.CurrentLocation()
		if loc == nil {
			continue // unknown location — cannot route
		}
		path, err := h.gateGraph.RepositionPath(ctx, loc.SystemSymbol, postSystem, maxJumps)
		if err != nil {
			logger.Log("INFO", fmt.Sprintf("Reposition candidate %s → %s unroutable this tick (stored-adjacency, ≤%d jumps): %v", loc.SystemSymbol, postSystem, maxJumps, err), map[string]interface{}{
				"action":    "scout_reposition_candidate_unroutable",
				"from":      loc.SystemSymbol,
				"to":        postSystem,
				"max_jumps": maxJumps,
			})
			continue
		}
		candidateHops := len(path) - 1
		if bestIdx == -1 || candidateHops < bestHops {
			bestIdx, bestHops = i, candidateHops
		}
	}
	if bestIdx == -1 {
		return -1, 0, false
	}
	return bestIdx, bestHops, true
}

// spawnReposition persists a coordinator-managed scout_reposition worker for
// hullSymbol, atomically claims the hull to it (operation scoutPostFleet — the same
// poach guard the tour claim uses, RULINGS #7), and starts it. Mirrors spawnTour
// exactly (persist → claim → start, with rollback on each failure) so the reposition
// worker inherits the same restart-recovery semantics.
func (h *RunScoutPostCoordinatorHandler) spawnReposition(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	hullSymbol string,
	destinationWaypoint string,
	maxJumps int,
	chartGateOnArrival bool,
) (string, error) {
	workerID := utils.GenerateContainerID("scout_reposition", hullSymbol)
	repoCmd := &ScoutRepositionCommand{
		PlayerID:            cmd.PlayerID,
		ShipSymbol:          hullSymbol,
		DestinationWaypoint: destinationWaypoint,
		CoordinatorID:       cmd.ContainerID,
		MaxRepositionJumps:  maxJumps,
		// A gate-reconcile 0-hop dispatch charts the target gate on arrival; a plain
		// manning reposition (false) never detours to the gate.
		ChartGateOnArrival: chartGateOnArrival,
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindScoutReposition, workerID, uint(cmd.PlayerID.Value()), repoCmd); err != nil {
		return "", fmt.Errorf("failed to persist scout reposition worker: %w", err)
	}

	if err := h.shipRepo.ClaimShip(ctx, hullSymbol, workerID, cmd.PlayerID, scoutPostFleet); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to claim satellite %s for reposition: %w", hullSymbol, err)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindScoutReposition, workerID); err != nil {
		h.releaseHull(ctx, cmd, hullSymbol, "scout_reposition_start_failed")
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to start scout reposition worker: %w", err)
	}

	return workerID, nil
}

// releaseReasonCrossSystemReuseRelay stamps a hull freed from a manning tour to be relayed
// cross-system to a starved post, so the release ledger distinguishes a surplus donation
// from a dead-tour reclaim or a sweep-once retirement.
const releaseReasonCrossSystemReuseRelay = "scout_cross_system_reuse_relay"

// surplusProbeCandidate is one over-covered source post's donatable manning probe: the source
// system, the ship + its tour + the slot it mans, that system's manning supply (mannedCount) and
// freshsizer demand, and the gate-hops from the source to the target post. mannedCount + demand ride
// on the candidate so pickSurplusProbe's over-covered filter (the "never strip below demand" guard)
// is PURE over its inputs — unit-testable with no repo.
type surplusProbeCandidate struct {
	sourceSystem string
	shipSymbol   string
	tourID       string
	slotIndex    int
	mannedCount  int
	demand       int
	hops         int
}

// maybeRelaySurplusProbe is the CROSS-SYSTEM reuse relay: when a declared post has no
// in-system satellite AND the idle pool is spent, it borrows ONE surplus probe from an
// OVER-COVERED source system (manning supply > freshsizer demand) and relays it
// cross-system onto the post — reusing the SAME idle-reposition dispatch primitives
// (discoverMarkets -> pickRepositionDestination -> spawnReposition) and the
// per-slot backoff. It returns true when it OWNS the slot this tick (a relay dispatched, or an active
// backoff), false to fall through to the honest park. FAIL-SAFE by construction: disabled (flag off
// or no demand reader), no over-covered surplus within reach, an unreadable demand, or a dispatch
// error all park honest, never strip a system below its need, and never move a probe blind.
func (h *RunScoutPostCoordinatorHandler) maybeRelaySurplusProbe(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	slot domainScouting.ScoutSlotRef,
	key string,
	sourcePosts []*domainScouting.ScoutPost,
	relayCfg scoutRelayConfig,
) bool {
	// Disabled (the default) or no demand reader wired -> return false BEFORE any side
	// effect, so the caller parks honest. This is the whole default-OFF gate.
	if !relayCfg.enabled || h.probeDemandReader == nil {
		return false
	}
	logger := common.LoggerFromContext(ctx)

	// Share the idle-reposition per-slot dispatch backoff: a recent relay for this slot
	// (idle OR surplus) backs off both paths, so a torn-down source probe is never
	// re-torn-down every tick. Announce the skip once per cooldown episode. Backed off
	// => we OWN the slot (do NOT park).
	if h.repositionBackedOff(key) {
		if h.noteRepositionBackoffLogged(key) {
			logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: cross-system reuse relay backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
				"action":        "scout_cross_system_relay_backoff",
				"system_symbol": post.SystemSymbol,
			})
		}
		return true
	}

	// Resolve a destination waypoint in the TARGET system (any market; the relay just lands the
	// hull there and the next in-system tick's tour starts from wherever it sits) — identical to
	// the idle-reposition path, including the virgin discovery fallback.
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for cross-system relay target %s: %v", post.SystemSymbol, err), nil)
		return false
	}
	if len(markets) == 0 {
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return true // parked honest by discoverVirginMarkets (owns the slot this tick)
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	candidate, ok := pickSurplusProbe(h.gatherSurplusCandidates(ctx, cmd, sourcePosts, post.SystemSymbol, relayCfg.maxHops), relayCfg.maxHops)
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no surplus probe in an over-covered system within %d hops — parked (fail-closed)", post.SystemSymbol, relayCfg.maxHops), map[string]interface{}{
			"action":        "scout_cross_system_relay_no_surplus",
			"system_symbol": post.SystemSymbol,
			"max_hops":      relayCfg.maxHops,
		})
		return false // fall through to the honest park
	}

	// Tear down the chosen probe's source tour so its hull is reclaimable (the shared
	// teardown primitive, per slot), then relay it cross-system onto the target. The
	// source is over-covered, so losing one probe still leaves it at (or above) its
	// freshsizer demand.
	h.tearDownSurplusSource(ctx, cmd, sourcePosts, candidate)

	relayID, err := h.spawnReposition(ctx, cmd, candidate.shipSymbol, destWaypoint, relayCfg.maxHops, false)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch cross-system reuse relay of %s from %s to post %s: %v", candidate.shipSymbol, candidate.sourceSystem, post.SystemSymbol, err), nil)
		return false
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails, the backoff
	// still prevents an immediate second teardown+relay to this slot next tick.
	h.noteRepositionDispatch(key)
	slot.SetRepositionContainerID(relayID)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dispatched cross-system relay for post %s but failed to persist relay reference: %v", post.SystemSymbol, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: cross-system reuse relay repositioning %s from over-covered %s (%d probe(s) manning > %d demand, %d jump(s), ≤%d bound, relay %s) → %s", post.SystemSymbol, candidate.shipSymbol, candidate.sourceSystem, candidate.mannedCount, candidate.demand, candidate.hops, relayCfg.maxHops, relayID, destWaypoint), map[string]interface{}{
		"action":        "scout_cross_system_reuse_relay",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   candidate.shipSymbol,
		"source_system": candidate.sourceSystem,
		"manned_count":  candidate.mannedCount,
		"demand":        candidate.demand,
		"jumps":         candidate.hops,
		"max_hops":      relayCfg.maxHops,
		"destination":   destWaypoint,
		"relay":         relayID,
	})
	return true
}

// gatherSurplusCandidates resolves every OVER-COVERED source post's donatable probe: for
// each loaded post that is NOT the target, it reads the source system's freshsizer demand (cached
// per system so a re-read is free), picks a donatable manning slot, and measures the gate-hops to
// the target. A post at/under its demand, with unreadable/zero demand (cannot assess), with no
// manning slot to give, or out of reach is dropped — never raid a system blind or below its need.
func (h *RunScoutPostCoordinatorHandler) gatherSurplusCandidates(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, sourcePosts []*domainScouting.ScoutPost, targetSystem string, maxHops int) []surplusProbeCandidate {
	demandCache := make(map[string]int)
	out := make([]surplusProbeCandidate, 0, len(sourcePosts))
	for _, src := range sourcePosts {
		if src.SystemSymbol == targetSystem {
			continue // never borrow from the post we are trying to man
		}
		mannedCount := src.MannedCount()
		if mannedCount == 0 {
			continue // nothing manning here to donate
		}
		demand, ok := h.probeDemandCached(ctx, cmd, src.SystemSymbol, demandCache)
		if !ok || demand <= 0 {
			continue // demand unreadable / cannot assess ⇒ never raid blind
		}
		slotIndex, shipSymbol, tourID, has := firstDonatableSlot(src)
		if !has {
			continue
		}
		hops := h.hopsBetween(ctx, src.SystemSymbol, targetSystem, maxHops)
		if hops < 1 {
			continue // unreachable within the relay reach
		}
		out = append(out, surplusProbeCandidate{
			sourceSystem: src.SystemSymbol,
			shipSymbol:   shipSymbol,
			tourID:       tourID,
			slotIndex:    slotIndex,
			mannedCount:  mannedCount,
			demand:       demand,
			hops:         hops,
		})
	}
	return out
}

// pickSurplusProbe selects the probe to relay: the FEWEST-hop candidate that is within maxHops AND
// sits in an OVER-COVERED system (mannedCount strictly greater than demand — the "never strip a
// system below its freshsizer need" guard; a system at exactly its demand is left alone). Ties break
// on the lowest source system, then the lowest ship symbol, for determinism. Pure over its inputs
// (the demand + hops are pre-resolved onto each candidate), so the over-covered and reach guards are
// unit-testable with no store, census, or repo.
func pickSurplusProbe(candidates []surplusProbeCandidate, maxHops int) (surplusProbeCandidate, bool) {
	best := surplusProbeCandidate{}
	found := false
	for _, candidate := range candidates {
		if candidate.hops < 1 || candidate.hops > maxHops {
			continue // out of reach
		}
		if candidate.mannedCount <= candidate.demand {
			continue // NOT over-covered — taking one would strip the system to/below its demand
		}
		if !found ||
			candidate.hops < best.hops ||
			(candidate.hops == best.hops && candidate.sourceSystem < best.sourceSystem) ||
			(candidate.hops == best.hops && candidate.sourceSystem == best.sourceSystem && candidate.shipSymbol < best.shipSymbol) {
			best, found = candidate, true
		}
	}
	return best, found
}

// tearDownSurplusSource stops the donated probe's source tour and reclaims its hull (the
// shared teardown primitive, applied to ONE slot), then clears that slot and persists the source post so
// the donation is durable and the source post's next tick sees the slot honestly unmanned. Reuses
// reclaimHullFromContainer (the shared reclaim path) so a hull the stop races is still freed on a
// later tick. Best-effort: a persist failure is logged, not fatal (pass 1 reconciles it).
func (h *RunScoutPostCoordinatorHandler) tearDownSurplusSource(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, sourcePosts []*domainScouting.ScoutPost, candidate surplusProbeCandidate) {
	logger := common.LoggerFromContext(ctx)
	src := findPostBySystem(sourcePosts, candidate.sourceSystem)
	if src == nil {
		return // defensive: the source post vanished between selection and teardown
	}
	for _, slot := range src.Slots() {
		if slot.Index() != candidate.slotIndex {
			continue
		}
		if tourID := slot.TourContainerID(); tourID != "" {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, releaseReasonCrossSystemReuseRelay)
		} else {
			h.releaseHull(ctx, cmd, slot.AssignedHull(), releaseReasonCrossSystemReuseRelay)
		}
		slot.SetAssignedHull("")
		slot.SetTourContainerID("")
		break
	}
	if err := h.postRepo.Upsert(ctx, src); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Cross-system relay freed a probe from %s but failed to persist the donation: %v", candidate.sourceSystem, err), nil)
	}
}

// hopsBetween measures the gate-hop distance from fromSystem to toSystem over the stored adjacency
// bounded to maxJumps (the expendable-probe resolver), returning -1 when unroutable within the
// bound. Mirrors selectNearestSatelliteByHops' reach math.
func (h *RunScoutPostCoordinatorHandler) hopsBetween(ctx context.Context, fromSystem, toSystem string, maxJumps int) int {
	path, err := h.gateGraph.RepositionPath(ctx, fromSystem, toSystem, maxJumps)
	if err != nil || len(path) == 0 {
		return -1
	}
	return len(path) - 1
}

// probeDemandCached reads a system's freshsizer demand once per gather pass (cache keyed by system),
// so a cluster of source posts costs one demand read each. An unreadable demand returns ok=false so
// the caller drops the candidate rather than raiding a system whose need it cannot judge.
func (h *RunScoutPostCoordinatorHandler) probeDemandCached(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, systemSymbol string, cache map[string]int) (int, bool) {
	if v, seen := cache[systemSymbol]; seen {
		return v, v >= 0
	}
	demand, err := h.probeDemandReader.ProbeDemand(ctx, cmd.PlayerID.Value(), systemSymbol)
	if err != nil {
		cache[systemSymbol] = -1 // memoize the failure so a re-read is free within the pass
		return 0, false
	}
	cache[systemSymbol] = demand
	return demand, true
}

// firstDonatableSlot picks the manning slot a source post will donate: the HIGHEST-index
// manned slot (an extra slot before the primary), so a multi-hull post keeps its primary slot +
// partition intact and gives up an extra. Returns the slot index, its hull, its tour container, and
// whether one was found. A post with no manned slot yields has=false.
func firstDonatableSlot(post *domainScouting.ScoutPost) (slotIndex int, shipSymbol, tourID string, has bool) {
	for _, slot := range post.Slots() {
		if hull := slot.AssignedHull(); hull != "" {
			slotIndex, shipSymbol, tourID, has = slot.Index(), hull, slot.TourContainerID(), true
		}
	}
	return slotIndex, shipSymbol, tourID, has
}

// findPostBySystem returns the loaded post for a system, or nil.
func findPostBySystem(posts []*domainScouting.ScoutPost, systemSymbol string) *domainScouting.ScoutPost {
	for _, p := range posts {
		if p.SystemSymbol == systemSymbol {
			return p
		}
	}
	return nil
}

// CensusProbeDemandReader implements SystemProbeDemandReader as the freshsizer's per-system demand
// derived from the SAME SystemsFreshness census the manning-stall watchdog reads. Demand is
// FreshnessRequiredHulls(marketCount, cycle, sla, oldestAge): the freshsizer's own closed-loop model,
// so a system BREACHING its SLA reads a RAISED demand (and is never raided by the relay), while a
// comfortably-fresh over-provisioned core system reads a low demand (and can donate its surplus). A
// system ABSENT from the census reads demand 0 — "cannot assess" — which the coordinator treats as
// "do not raid", so a missing/stale census never strips a probe blind. cycle and sla are config
// (RULINGS #5); the daemon seeds them from the freshness sizer's defaults so the two agree.
type CensusProbeDemandReader struct {
	census domainScouting.SystemFreshnessReader
	cycle  time.Duration
	sla    time.Duration
}

// NewCensusProbeDemandReader wires the census-backed freshsizer-demand source. cycle is the seeded
// per-market scan cadence and sla the freshness target the demand is sized against; a non-positive
// value falls back to the freshness sizer's documented defaults so the reader is never degenerate.
func NewCensusProbeDemandReader(census domainScouting.SystemFreshnessReader, cycle, sla time.Duration) *CensusProbeDemandReader {
	if cycle <= 0 {
		cycle = defaultSeedCycleSeconds * time.Second
	}
	if sla <= 0 {
		sla = defaultSLASeconds * time.Second
	}
	return &CensusProbeDemandReader{census: census, cycle: cycle, sla: sla}
}

var _ SystemProbeDemandReader = (*CensusProbeDemandReader)(nil)

// ProbeDemand returns systemSymbol's freshsizer demand from the current census. A system with no
// census row (or no markets) reads 0 — the caller's "cannot assess ⇒ do not raid" signal.
func (r *CensusProbeDemandReader) ProbeDemand(ctx context.Context, playerID int, systemSymbol string) (int, error) {
	snapshots, err := r.census.SystemsFreshness(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("freshness census unreadable for probe demand: %w", err)
	}
	for _, snap := range snapshots {
		if snap.SystemSymbol != systemSymbol {
			continue
		}
		age := time.Duration(snap.OldestAgeSeconds * float64(time.Second))
		return domainScouting.FreshnessRequiredHulls(snap.MarketCount, r.cycle, r.sla, age), nil
	}
	return 0, nil // no census row ⇒ cannot assess (do not raid)
}

// parkNoInSystemSatellite logs the honest, system-scoped park reason for an unmanned
// slot that has no in-system satellite and cannot be repositioned (no gate graph, or no
// idle satellite left this tick). The message text is stable so `container logs` greps
// and park assertions keep matching.
func (h *RunScoutPostCoordinatorHandler) parkNoInSystemSatellite(ctx context.Context, post *domainScouting.ScoutPost) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
		"action":        "scout_post_unmanned_no_in_system_satellite",
		"system_symbol": post.SystemSymbol,
	})
}

// repositionBackedOff reports whether a reposition dispatch for key is currently within
// its backoff window. A nil/absent entry reads false.
func (h *RunScoutPostCoordinatorHandler) repositionBackedOff(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	return ok && h.clock.Now().Before(until)
}

// noteRepositionDispatch arms the per-slot dispatch backoff so the next dispatch for
// this slot waits out repositionRetryBackoff — the anti-hot-loop bound.
func (h *RunScoutPostCoordinatorHandler) noteRepositionDispatch(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	h.repositionBackoffUntil[key] = h.clock.Now().Add(repositionRetryBackoff)
}

// noteRepositionFailure records a FAILED reposition relay for key: it increments the
// consecutive-failure streak and arms the LONG failure cooldown — OVERRIDING the short dispatch
// floor — and returns the new streak count for the failure log. Rotation to the next candidate
// falls out of the backed-off slot being skipped without consuming the shared probe.
func (h *RunScoutPostCoordinatorHandler) noteRepositionFailure(key string, cooldown time.Duration) int {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	if h.repositionFailures == nil {
		h.repositionFailures = make(map[string]int)
	}
	h.repositionFailures[key]++
	h.repositionBackoffUntil[key] = h.clock.Now().Add(cooldown)
	return h.repositionFailures[key]
}

// resetRepositionFailures clears a post's failure streak, cooldown, and once-logged marker
// — called when a relay COMPLETES, so a post that finally succeeded starts clean and
// the next failure is counted from one. delete on a nil map is a no-op, so this is safe on the
// struct-literal handler the tests build.
func (h *RunScoutPostCoordinatorHandler) resetRepositionFailures(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	delete(h.repositionFailures, key)
	delete(h.repositionBackoffUntil, key)
	delete(h.repositionBackoffLoggedUntil, key)
}

// noteRepositionBackoffLogged reports whether the CURRENT backoff episode for key has not yet
// been announced, and marks it announced. It keys the marker on the exact backoff deadline,
// so each distinct cooldown window logs its skip reason exactly once rather than every tick.
// A new failure arms a later deadline, which reads as a new episode and logs once more.
func (h *RunScoutPostCoordinatorHandler) noteRepositionBackoffLogged(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	if !ok {
		return false
	}
	if logged, ok := h.repositionBackoffLoggedUntil[key]; ok && logged.Equal(until) {
		return false // this episode already announced
	}
	if h.repositionBackoffLoggedUntil == nil {
		h.repositionBackoffLoggedUntil = make(map[string]time.Time)
	}
	h.repositionBackoffLoggedUntil[key] = until
	return true
}

// backoffKey scopes the reposition backoff to (playerID, system, slot) so one player's
// relay to system S never rate-limits another player's post in the same-named system, and
// each slot of a multi-probe post repositions independently. The PRIMARY slot keeps the
// un-suffixed key, so a single-hull post's key shape is unchanged.
func backoffKey(playerID int, system string, slotIndex int) string {
	if slotIndex < 0 {
		return fmt.Sprintf("%d|%s", playerID, system)
	}
	return fmt.Sprintf("%d|%s|%d", playerID, system, slotIndex)
}

// driftKey scopes market-drift debounce tracking to (playerID, system) — the same
// un-suffixed shape as backoffKey's primary-slot form, since drift is a whole-post
// property (the market SET, not any one slot).
func driftKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// noteDriftPending records the FIRST tick a post's market set was seen drifting
// and returns how long the drift episode has been pending. A key already
// tracked keeps its original timestamp — the age accumulates across ticks until the
// re-cut fires or the drift resolves on its own.
func (h *RunScoutPostCoordinatorHandler) noteDriftPending(key string) time.Duration {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	if h.driftPendingSince == nil {
		h.driftPendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.driftPendingSince[key]
	if !ok {
		h.driftPendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// clearDriftPending forgets a post's pending-drift episode: called once a
// re-cut resolves it, the drift resolves on its own, or the post is no longer
// partitioned (reverted to single-hull). A nil/absent entry is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearDriftPending(key string) {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	delete(h.driftPendingSince, key)
}

// budgetChangeState is one post's pending hull-budget change: the new budget the
// sizer wants (differing from the post's cut partition) and how many consecutive reconcile
// cycles it has persisted. The re-partition fires only when cycles reaches the debounce.
type budgetChangeState struct {
	budget int
	cycles int
}

// noteBudgetChangePending records that the sizer wants newBudget for a post whose cut
// partition is a DIFFERENT size, and returns how many CONSECUTIVE reconcile cycles that SAME
// new budget has now persisted. A first sighting — or a change to yet another budget
// — restarts the count at 1, so a budget that keeps flapping between values never accumulates
// toward the re-partition threshold; only a STABLE new budget does. The single-value dedupe
// (state.budget != newBudget resets) is what makes the debounce absorb an OSCILLATION, not
// just a one-shot blip. Lazily initializes the map so the struct-literal test handlers (which
// never call the constructor) are safe, mirroring noteDriftPending.
func (h *RunScoutPostCoordinatorHandler) noteBudgetChangePending(key string, newBudget int) int {
	h.budgetChangeMu.Lock()
	defer h.budgetChangeMu.Unlock()
	if h.budgetChangePending == nil {
		h.budgetChangePending = make(map[string]budgetChangeState)
	}
	state, ok := h.budgetChangePending[key]
	if !ok || state.budget != newBudget {
		h.budgetChangePending[key] = budgetChangeState{budget: newBudget, cycles: 1}
		return 1
	}
	state.cycles++
	h.budgetChangePending[key] = state
	return state.cycles
}

// clearBudgetChangePending forgets a post's pending budget change: called the moment
// its budget matches the cut partition again (a swing reverted, or a real change was applied),
// so a later change starts a FRESH count rather than inheriting a stale one. A nil/absent entry
// is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearBudgetChangePending(key string) {
	h.budgetChangeMu.Lock()
	defer h.budgetChangeMu.Unlock()
	delete(h.budgetChangePending, key)
}

// singleHullSnapshot returns the market set a single-hull post was last (re-)manned
// with, and whether one is recorded yet. Absent after a fresh handler
// (daemon restart) or before the post's first successful manning.
func (h *RunScoutPostCoordinatorHandler) singleHullSnapshot(key string) ([]string, bool) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	markets, ok := h.singleHullMarketSnapshot[key]
	return markets, ok
}

// setSingleHullSnapshot records the market set a single-hull post is now toured
// against — called once when the post is freshly (re-)manned (pass 2a), and
// again when ensureSingleHullFreshness adopts a post's current markets as a fresh
// baseline (e.g. after a restart cleared the in-memory snapshot).
func (h *RunScoutPostCoordinatorHandler) setSingleHullSnapshot(key string, markets []string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	if h.singleHullMarketSnapshot == nil {
		h.singleHullMarketSnapshot = make(map[string][]string)
	}
	h.singleHullMarketSnapshot[key] = markets
}

// clearSingleHullSnapshot forgets a single-hull post's freshness baseline:
// called once a drift-triggered respawn resolves it. The post's next manning
// (pass 2a) sets a fresh one immediately, so this is momentary.
func (h *RunScoutPostCoordinatorHandler) clearSingleHullSnapshot(key string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	delete(h.singleHullMarketSnapshot, key)
}

// noteSingleHullDriftPending records the FIRST tick a single-hull post's market set
// was seen drifting from its snapshot and returns how long the drift episode
// has been pending — the single-hull mirror of noteDriftPending, backed by a SEPARATE
// map (see singleHullDriftPendingSince's doc comment on the handler struct for why it
// cannot share driftPendingSince).
func (h *RunScoutPostCoordinatorHandler) noteSingleHullDriftPending(key string) time.Duration {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	if h.singleHullDriftPendingSince == nil {
		h.singleHullDriftPendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.singleHullDriftPendingSince[key]
	if !ok {
		h.singleHullDriftPendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// clearSingleHullDriftPending forgets a single-hull post's pending-drift episode:
// called once a respawn resolves it, the drift resolves on its own, or the
// post is no longer single-hull-standing. A nil/absent entry is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearSingleHullDriftPending(key string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	delete(h.singleHullDriftPendingSince, key)
}

// pickRepositionDestination chooses the reposition target waypoint from a post's
// discovered markets — the lexicographically smallest, so the destination (and thus the
// dispatch log and the tests) is deterministic. Any market in the system serves. The
// caller has already ensured markets is non-empty.
func pickRepositionDestination(markets []string) string {
	best := markets[0]
	for _, m := range markets[1:] {
		if m < best {
			best = m
		}
	}
	return best
}

// hullOutOfSystem reports whether a hull is currently NOT in system — the cross-system
// -assignment defect the repair pass heals. It fails safe: a hull that cannot be
// loaded, or whose location is unknown, is treated as in-system so a transient lookup gap
// never triggers a spurious release.
func (h *RunScoutPostCoordinatorHandler) hullOutOfSystem(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, hullSymbol, systemSymbol string) bool {
	if hullSymbol == "" {
		return false
	}
	ship, err := h.shipRepo.FindBySymbol(ctx, hullSymbol, cmd.PlayerID)
	if err != nil {
		return false // unknown hull — never release on a lookup failure
	}
	loc := ship.CurrentLocation()
	if loc == nil {
		return false // unknown location — conservative, leave the assignment alone
	}
	return loc.SystemSymbol != systemSymbol
}

// reclaimHullFromContainer frees any ship still assigned to a (now dead) worker
// container, returning it to idle so pass 2 can re-claim it. Best-effort and DB-only —
// the contract ReclaimShipsFromInterruptedWorkers pattern, shared by dead tours and ended
// reposition relays (the reason distinguishes them in the log).
func (h *RunScoutPostCoordinatorHandler) reclaimHullFromContainer(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, containerID, reason string) {
	logger := common.LoggerFromContext(ctx)
	if containerID == "" {
		return
	}
	ships, err := h.shipRepo.FindByContainer(ctx, containerID, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load ships for dead container %s: %v", containerID, err), nil)
		return
	}
	for _, ship := range ships {
		if !ship.IsAssigned() {
			continue
		}
		shipSymbol := ship.ShipSymbol()
		// Reclaim under CAS-retry: re-apply ForceRelease on the FRESH row so a
		// concurrent writer's cargo/nav update on the same hull survives instead of
		// being last-write-wins clobbered by the FindByContainer snapshot. Skip unless
		// the hull is still on THIS container (a concurrent release or re-claim ->
		// changed=false), so a hull that moved on is never reclaimed out from under its
		// new owner (RULINGS #7).
		_, changed, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease(reason, h.clock)
				return true, nil
			})
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim hull %s from container %s: %v", shipSymbol, containerID, err), nil)
			continue
		}
		if !changed {
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed hull %s from container %s", shipSymbol, containerID), nil)
	}
}

// releaseHull frees a specific hull by symbol (sweep-once retirement, start-failure
// rollback). Best-effort.
func (h *RunScoutPostCoordinatorHandler) releaseHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, hullSymbol, reason string) {
	if hullSymbol == "" {
		return
	}
	logger := common.LoggerFromContext(ctx)
	// Release under CAS-retry: the closure re-applies ForceRelease on the
	// FRESH row so a concurrent writer's cargo/nav update on the same hull survives
	// instead of being last-write-wins clobbered, and skips the write when the hull
	// is already idle (changed=false, no spurious version bump).
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, hullSymbol, cmd.PlayerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() {
				return false, nil
			}
			sh.ForceRelease(reason, h.clock)
			return true, nil
		}); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release hull %s (%s): %v", hullSymbol, reason, err), nil)
	}
}

// discoverMarkets returns the marketplace waypoint symbols in a system — the tour a
// post's hull scans.
func (h *RunScoutPostCoordinatorHandler) discoverMarkets(ctx context.Context, systemSymbol string) ([]string, error) {
	waypoints, err := h.marketProvider.ListBySystemWithTrait(ctx, systemSymbol, marketplaceTrait)
	if err != nil {
		return nil, err
	}
	markets := make([]string, 0, len(waypoints))
	for _, wp := range waypoints {
		markets = append(markets, wp.Symbol)
	}
	return markets, nil
}

// sortPostsByPriority orders unmanned posts so standing posts (the freshness
// backbone) are manned before sweep-once frontier posts, deterministic by system
// within a kind.
func sortPostsByPriority(posts []*domainScouting.ScoutPost) {
	sort.Slice(posts, func(i, j int) bool {
		ki, kj := postKindRank(posts[i].Kind), postKindRank(posts[j].Kind)
		if ki != kj {
			return ki < kj
		}
		return posts[i].SystemSymbol < posts[j].SystemSymbol
	})
}

func postKindRank(kind domainScouting.PostKind) int {
	if kind == domainScouting.PostKindStanding {
		return 0
	}
	return 1
}

// selectInSystemSatellite returns the index of an idle satellite already in the
// post's system, or -1 if none. Cross-system matching is intentionally impossible:
// the scout_tour worker navigates in-system only, so a cross-system
// assignment crash-respawn-loops. A slot with no in-system satellite is a reposition
// candidate (2b). idleSats is pre-sorted, so the choice is deterministic.
func selectInSystemSatellite(idleSats []*navigation.Ship, systemSymbol string) int {
	for i, sat := range idleSats {
		if sat.CurrentLocation() != nil && sat.CurrentLocation().SystemSymbol == systemSymbol {
			return i
		}
	}
	return -1
}
