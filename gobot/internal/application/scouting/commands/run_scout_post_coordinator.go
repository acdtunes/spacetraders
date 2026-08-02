package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
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
	defaultMarketDriftThreshold  = 2
	defaultMarketDriftMaxAgeSecs = 3600
	defaultMarketDriftMaxAge     = defaultMarketDriftMaxAgeSecs * time.Second

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
	// longer scans). It is a MINIMUM debounce: manningStallWindowCycles raises it to the
	// post's own circuit period, the soonest that age could possibly improve, so this value
	// binds only on a post whose circuit is shorter than a couple of ticks.
	// CorrectionCap bounds how many re-mans one post gets before the watchdog backs off and
	// leaves the persisted captain event to carry it to the operator, instead of churning a
	// tour on an unreachable market forever.
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

	// RespawnAttemptCap bounds how many CONSECUTIVE respawns of a post's dead tour the
	// coordinator performs before PARKING the post for a backoff window instead
	// ([scouting] respawn_attempt_cap). A tour that finally runs healthy resets the
	// count. <= 0 uses defaultScoutRespawnAttemptCap, mirroring TickIntervalSecs.
	RespawnAttemptCap int

	// ManningStallCycles and ManningStallCorrectionCap tune the manning watchdog
	// (LIVE-tunable via SetLiveConfigReader): the MINIMUM number of CONSECUTIVE reconcile
	// cycles a fully-manned standing post must breach its freshness target without its
	// worst-case market age improving before the watchdog re-mans it — raised per post to
	// its own circuit period, the soonest that age could improve — and the number of
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
	// stallCycles is the consecutive breach-without-improvement count (measured against
	// the post's circuit window); stallCorrections is how many re-mans this post has had (the K
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

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunScoutPostCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunScoutPostCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := scoutPostTick(cmd)

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

// scoutPostTick is the reconcile cadence: the launch value, or the documented default when
// unset (RULINGS #5). The manning watchdog reads it too, to express its per-post window — a
// duration — in the cycles its knob counts.
func scoutPostTick(cmd *RunScoutPostCoordinatorCommand) time.Duration {
	if tick := time.Duration(cmd.TickIntervalSecs) * time.Second; tick > 0 {
		return tick
	}
	return defaultScoutPostTickSeconds * time.Second
}

// scoutAvgHop is the circuit-model average per-market hop cost (nav + scan dwell): the
// undersized warning projects a post's circuit with it, and the manning watchdog sizes its
// stall window from the same projection, so both judge a post by ONE number.
func scoutAvgHop(cmd *RunScoutPostCoordinatorCommand) time.Duration {
	if hop := time.Duration(cmd.UndersizedAvgHopSecs) * time.Second; hop > 0 {
		return hop
	}
	return defaultUndersizedAvgHop
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

	// Pass 0.5 (zombie-worker sweep): stop any RUNNING coordinator-spawned tour or
	// relay that NO post slot references and reclaim its hull. Every other pass is
	// post-driven and Pass 0 frees hulls only under a DEAD container, so a removed
	// post's still-running tour (iterations=-1) would otherwise scan forever. Runs
	// BEFORE the empty-table fast exit: removing the LAST post is exactly when its
	// tour needs sweeping.
	h.sweepZombieScoutWorkers(ctx, cmd, posts)

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

	states, err := h.containerStateSets(ctx, cmd)
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
	// improving for a full circuit period). It runs AFTER the partition /
	// single-hull-freshness teardowns and BEFORE the manning passes, so a torn-down
	// stalled post is re-manned this SAME tick. A no-op when no census reader is wired.
	h.remanStalledPosts(ctx, cmd, posts)

	// Pass 1: manned slots.
	for _, post := range posts {
		if h.reconcileMannedSlots(ctx, cmd, post, states, removed) {
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
		h.reconcileRepositioningSlots(ctx, cmd, post, states)
	}

	// Pass 2: man the unmanned slots, standing posts first. A post inside its respawn-cap
	// backoff window is skipped here so a persistently-crashing tour is not respawned every tick.
	targets := h.unmannedSlotTargets(posts, removed)
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

	// Pass 2a: man every slot that has an idle satellite ALREADY in its system.
	stillUnmanned := h.manSlotsFromInSystemIdle(ctx, cmd, targets, &idleSats)

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

// containerStates is one tick's snapshot of the coordinator's spawned containers, by ID.
type containerStates struct {
	running   map[string]bool
	completed map[string]bool
	failed    map[string]bool
}

// containerStateSets reads the three container-status sets one pass needs. The FAILED set
// lets pass 1.5 distinguish a relay that DIED (unroutable — arm the long failure cooldown
// and rotate the probe) from one that ARRIVED (reset the streak) or was merely
// restart-interrupted (keep the short floor).
func (h *RunScoutPostCoordinatorHandler) containerStateSets(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) (states containerStates, err error) {
	for _, set := range []struct {
		status string
		into   *map[string]bool
	}{
		{"RUNNING", &states.running},
		{"COMPLETED", &states.completed},
		{"FAILED", &states.failed},
	} {
		ids, ferr := h.containerIDSet(ctx, cmd, set.status)
		if ferr != nil {
			return containerStates{}, ferr
		}
		*set.into = ids
	}
	return states, nil
}

// manSlotsFromInSystemIdle mans every slot that already has an idle satellite in its own
// system and returns the slots left unmanned. Doing this for ALL slots before any reposition
// guarantees an in-system satellite is never repositioned AWAY from a slot it could man
// locally.
func (h *RunScoutPostCoordinatorHandler) manSlotsFromInSystemIdle(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, targets []slotTarget, idleSats *[]*navigation.Ship) []slotTarget {
	logger := common.LoggerFromContext(ctx)
	stillUnmanned := make([]slotTarget, 0, len(targets))
	for _, tgt := range targets {
		systemSymbol := tgt.post.SystemSymbol
		idx := selectInSystemSatellite(*idleSats, systemSymbol)
		if idx < 0 {
			stillUnmanned = append(stillUnmanned, tgt)
			continue
		}

		markets, err := h.slotMarkets(ctx, tgt.post, tgt.slot)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for post %s: %v", systemSymbol, err), nil)
			continue
		}
		if len(markets) == 0 {
			// Nothing to scan (uncharted / no marketplace waypoints, or an un-partitioned
			// multi-probe slot). Don't burn the in-system satellite's claim on a zero-market
			// tour — leave it idle in system. Repositioning cannot help (the problem is
			// markets, not hull location), so this slot is NOT a 2b candidate.
			logger.Log("INFO", fmt.Sprintf("No markets to scan for post %s slot yet — leaving unmanned this tick", systemSymbol), nil)
			continue
		}

		sat := (*idleSats)[idx]
		*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)
		shipSymbol := sat.ShipSymbol()

		tourID, err := h.spawnTour(ctx, cmd, tgt.post, shipSymbol, markets)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to man post %s with %s: %v", systemSymbol, shipSymbol, err), nil)
			continue
		}

		tgt.slot.SetAssignedHull(shipSymbol)
		tgt.slot.SetTourContainerID(tourID)
		if err := h.postRepo.Upsert(ctx, tgt.post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Manned post %s but failed to persist assignment: %v", systemSymbol, err), nil)
		}
		if tgt.post.HullBudget() <= 1 && tgt.post.Kind == domainScouting.PostKindStanding {
			// Baseline the freshness snapshot to what this tour actually launched with,
			// so the NEXT tick's drift check compares against reality instead of an
			// empty/stale snapshot.
			h.setSingleHullSnapshot(driftKey(cmd.PlayerID.Value(), systemSymbol), markets)
		}
	}
	return stillUnmanned
}

// reconcileMannedSlots runs pass 1 over one post's slots. It returns true when the
// post was retired (a completed sweep-once), so the caller skips it in later passes.
func (h *RunScoutPostCoordinatorHandler) reconcileMannedSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	states containerStates,
	removed map[string]bool,
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
		if tourID != "" && states.running[tourID] {
			sawHealthy = true
			continue
		}

		// A sweep-once post whose tour COMPLETED has done its one job: release the hull and
		// retire the post so its satellite flows to the next unmanned post. Sweep-once is
		// always single-hull (HullBudget clamps it), so this is the only slot.
		if post.Kind == domainScouting.PostKindSweepOnce && tourID != "" && states.completed[tourID] {
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

// accountRespawn advances or resets a post's PERSISTED consecutive-respawn counter after one
// reconcile pass over its slots, and parks the post once the counter exhausts the cap.
// A tour observed HEALTHY this tick (sawHealthy) means the last spawn survived, so the streak
// resets and any park is lifted — the cap counts CONSECUTIVE failures, not lifetime. A dead tour
// respawned this tick (sawRespawn) advances the streak; on reaching the cap the post is parked for
// defaultRespawnParkWindow (RespawnParkedUntil) instead of respawned yet again, and the exhaustion
// is logged (naturally rate-limited to one line per window, since a parked post spawns nothing to
// respawn until the window elapses and it retries once). Both fields persist so the cap survives a
// daemon restart rather than the crash-loop resuming at tick cadence. Healthy wins over respawn,
// so any one live slot resets a multi-hull post's whole-post streak.
func (h *RunScoutPostCoordinatorHandler) accountRespawn(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, sawHealthy, sawRespawn bool) {
	attemptCap := resolveRespawnCap(cmd)
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

// isRespawnParked reports whether a post is currently inside its respawn-cap backoff
// window — it exhausted the consecutive-respawn cap and is not yet due for a retry. Pass 2 skips
// such a post so a persistently-crashing tour is not respawned every tick. A zero deadline (never
// capped, or reset by a healthy tour / market-drift re-cut) reads false.
func (h *RunScoutPostCoordinatorHandler) isRespawnParked(post *domainScouting.ScoutPost) bool {
	return !post.RespawnParkedUntil.IsZero() && h.clock.Now().Before(post.RespawnParkedUntil)
}

// unmannedSlotTargets collects every slot that pass 2 should man: unmanned, not
// repositioning, in a non-retired, non-respawn-parked post. Standing posts sort before sweep-once
// (the freshness backbone is manned first), deterministic by system within a kind, and
// primary-before-extra within a post — so manning order is stable and testable.
func (h *RunScoutPostCoordinatorHandler) unmannedSlotTargets(posts []*domainScouting.ScoutPost, removed map[string]bool) []slotTarget {
	ordered := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		if h.isRespawnParked(post) {
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
