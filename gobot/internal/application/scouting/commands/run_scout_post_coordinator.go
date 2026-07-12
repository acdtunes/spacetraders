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

	// repositionRetryBackoff is the SHORT floor armed at every dispatch (sp-s232): the
	// RUNNING-relay check already stops a second dispatch while a relay is airborne, and
	// this covers the brief window between a relay ending and the next reconcile pass so a
	// relay that ends WITHOUT a clear FAILED verdict (restart-interrupted, a fast opaque
	// exit) does not hot-loop re-dispatch. A relay that ends with an explicit FAILED status
	// arms the much longer, config-tunable defaultRepositionFailureCooldown instead
	// (noteRepositionFailure, sp-o34q) — the failure-aware cooldown that also rotates the
	// probe to the next candidate post. In-memory and reset on restart (conservative: at
	// most one immediate retry after a daemon restart, never a storm).
	repositionRetryBackoff = 5 * time.Minute

	// defaultRepositionFailureCooldown bounds how long a post whose reposition relay FAILED
	// waits before the coordinator retries repositioning to it when the launch config leaves
	// [scouting] reposition_failure_cooldown_secs unset (sp-o34q, RULINGS #5). 30 min is long
	// enough that a genuinely-unroutable post is retried on the order of the frontier's own
	// change cadence (a probe re-reading a gate, a new gate charted) rather than every 30s
	// tick — the fix for the post-deploy crash-loop that produced ~20 corpses / 15min against
	// a handful of dark posts. The probe is freed to the next candidate on each failure, so a
	// long cooldown on one post never starves the others.
	defaultRepositionFailureCooldown = 30 * time.Minute

	// defaultScoutRespawnAttemptCap bounds CONSECUTIVE dead-tour respawns of one post before
	// the coordinator parks it for a backoff window instead of respawning yet again (sp-py4n,
	// [scouting] respawn_attempt_cap) when the launch config leaves it unset (RULINGS #5). The
	// reconciler respawns any dead tour every ~30s tick, so a tour crashing on a PERSISTENT
	// non-cross-system reason would otherwise respawn-loop forever; 10 is ~5 min of respawns —
	// long enough to ride out a transient blip (a tour that ever runs one healthy tick resets
	// the count), short enough to stop a genuinely-broken post from flooding the fleet.
	defaultScoutRespawnAttemptCap = 10

	// defaultRespawnParkWindow is how long a post that exhausted its respawn cap is parked
	// before the coordinator retries it exactly once (sp-py4n). Mirrors
	// defaultRepositionFailureCooldown's philosophy: a persistently-crashing post is retried on
	// the order of the frontier's own change cadence rather than every 30s tick, so the freed
	// probe does useful work elsewhere in between. A retry that still dies re-arms the window; a
	// retry that finally runs healthy clears it. Persisted with the counter (RespawnParkedUntil),
	// so the park survives a restart rather than the crash-loop resuming immediately.
	defaultRespawnParkWindow = 30 * time.Minute

	// partitionAnchorFuelCapacity and partitionAnchorEngineSpeed are the synthetic
	// probe configs the VRP partitioner anchors at a common waypoint (sp-enry). The
	// partition is a division of a system's markets into N disjoint tours; it must be
	// STABLE across which specific probes are present (RULINGS: re-partition only on
	// hull-count change), so the partitioner is fed N identical anchored slots rather
	// than real ship locations, and the RESULT is frozen+persisted. These are typical
	// probe values; the exact numbers do not matter because the partition is computed
	// once per budget change, not re-optimized per tick.
	partitionAnchorFuelCapacity = 400
	partitionAnchorEngineSpeed  = 30

	// defaultMarketDriftThreshold and defaultMarketDriftMaxAge bound the DEBOUNCED
	// market-set re-cut when the launch config leaves them unset (sp-ykhl, RULINGS #5).
	// The market SET is a partition input exactly like the hull budget: nn0y virgin
	// discovery keeps adding markets to a system after a post's tours are already cut,
	// and a market discovered post-cut belongs to NO partition — it goes permanently
	// stale even though the post reads fully manned. A partitioned (hulls>1) post
	// re-cuts — the SAME hull-budget re-partition path, sp-enry — once its discovered
	// market set has DRIFTED from the union of its persisted partitions by at least
	// defaultMarketDriftThreshold markets (additions AND removals both count, no
	// special-casing a removal), OR the drift has been pending at least
	// defaultMarketDriftMaxAge — whichever fires first. Mirrors the hull-count
	// stability gate: same hulls + same markets is still zero re-cuts.
	defaultMarketDriftThreshold = 2
	defaultMarketDriftMaxAge    = 60 * time.Minute

	// defaultUndersizedAvgHop and defaultUndersizedRewarnCooldown bound the sp-k7q5
	// undersized-post warning (layer 1) when the launch config leaves them unset
	// (RULINGS #5). avgHop (~3min) is the Admiral circuit-model average per-market
	// hop cost (navigation + scan dwell) used to project a post's circuit time; the
	// cooldown debounces the DEFERRED warning so a persistently-undersized post
	// re-queues the event at most once per window, never every 30s tick.
	defaultUndersizedAvgHop         = 3 * time.Minute
	defaultUndersizedRewarnCooldown = 3 * time.Hour

	// defaultMaxRepositionJumps bounds the EXPENDABLE-probe reposition reach over the
	// stored adjacency (sp-8k9m, [scouting] max_reposition_jumps) when the launch config
	// leaves it unset (RULINGS #5). 12 covers the measured worst-case charted depth from
	// the probe supply to the darkest posts (KN67→SN21=6, →C81=9, →XN7=12); the strict
	// heavy-hull cap (gategraph.MaxJumpPath=5) is deliberately NOT raised — only the probe
	// reposition class, which routes past unreadable frontier gates, reaches this far.
	defaultMaxRepositionJumps = 12
)

// RunScoutPostCoordinatorCommand launches the standing scout-post coordinator for
// a player (sp-cxpq). Like the contract fleet coordinator it runs an infinite
// reconcile loop inside a single Handle() call; the container wraps one iteration
// (CoordinatorOwnsIterations).
type RunScoutPostCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int

	// MarketDriftThreshold and MarketDriftMaxAgeSecs bound the debounced market-set
	// re-cut (sp-ykhl): a partitioned (hulls>1) post re-cuts once its discovered
	// market set has drifted from its persisted partition union by at least
	// MarketDriftThreshold markets, or the drift has been pending at least
	// MarketDriftMaxAgeSecs seconds — whichever fires first. <= 0 uses the
	// coordinator's own default (RULINGS #5: parametrized, not hardcoded at the call
	// site) — mirrors TickIntervalSecs.
	MarketDriftThreshold  int
	MarketDriftMaxAgeSecs int

	// UndersizedAvgHopSecs and UndersizedRewarnCooldownSecs tune the sp-k7q5
	// undersized-post warning (layer 1, RULINGS #5): the circuit-model average
	// per-market hop cost, and how long a fired warning suppresses a re-fire for the
	// same system. <= 0 uses the coordinator's own defaults, mirroring TickIntervalSecs.
	UndersizedAvgHopSecs         int
	UndersizedRewarnCooldownSecs int

	// StartJitterMaxSecs bounds a one-time deterministic phase offset waited out before
	// this coordinator's reconcile loop starts its first tick (sp-x8i5): derived from a
	// stable hash of ContainerID via stableJitter (shared with scout_tour.go, same
	// package — NOT math/rand) so the SAME container gets the SAME offset in
	// [0, StartJitterMaxSecs) on every build, including restart recovery, where
	// ContainerID is preserved rather than regenerated. <= 0 uses
	// defaultTourStartJitterMax, mirroring TickIntervalSecs.
	//
	// This does NOT re-pace reconcileOnce's own per-post manning passes: a mass re-man
	// (e.g. every post unmanned right after a restart) fans out through spawnTour into
	// freshly-launched scout_tour containers, and each of THOSE already self-jitters its
	// own first scan via the ShipSymbol-keyed offset above — so a synchronized sweep at
	// the manning-pass level decoheres for free without touching reconcileOnce's loops.
	StartJitterMaxSecs int

	// MaxRepositionJumps bounds the EXPENDABLE-probe reposition reach over the stored
	// adjacency (sp-8k9m [scouting] max_reposition_jumps): the selection resolver and the
	// dispatched relay both route past unreadable frontier gates up to this many jumps,
	// reaching the 6-12-jump posts the strict fetch-through cap rejects. <= 0 uses
	// defaultMaxRepositionJumps, mirroring TickIntervalSecs.
	MaxRepositionJumps int

	// RepositionFailureCooldownSecs is how long a post whose reposition relay FAILED waits
	// before the coordinator retries repositioning to it (sp-o34q [scouting]
	// reposition_failure_cooldown_secs). On a relay failure the coordinator arms this cooldown
	// on the post's slot, frees the probe, and services the NEXT candidate post this tick
	// instead of respawning the same corpse — so a genuinely-unroutable post can no longer
	// crash-loop the dispatcher and flood the event queue. <= 0 uses
	// defaultRepositionFailureCooldown, mirroring TickIntervalSecs.
	RepositionFailureCooldownSecs int

	// CoverageSpreadDisabled reverts the sp-6ovd coverage-first manning order to the legacy
	// depth-first order (all of a post's slots before the next post's). Default false = LIVE:
	// unmannedSlotTargets interleaves by slot tier so a scarce idle-probe pool spreads
	// one-per-uncovered-system before it piles a multi-hull post's extra slots — the durable
	// fix for the reconciler herding the whole probe group onto one target per cycle while
	// distinct high-value systems stayed dark. The escape ([scouting] coverage_spread_disabled)
	// exists per RULINGS #5 so a captain can pin depth-first without a redeploy; it is not
	// expected to be set in normal operation.
	CoverageSpreadDisabled bool

	// RespawnAttemptCap bounds how many CONSECUTIVE respawns of a post's dead tour the
	// coordinator performs before PARKING the post for a backoff window instead (sp-py4n
	// [scouting] respawn_attempt_cap). A tour crashing on a PERSISTENT non-cross-system
	// reason would otherwise respawn-loop at tick cadence forever; a tour that finally runs
	// healthy resets the count. <= 0 uses defaultScoutRespawnAttemptCap, mirroring
	// TickIntervalSecs.
	RespawnAttemptCap int

	// RespawnCapDisabled turns the sp-py4n respawn-loop cap OFF, restoring the pre-py4n
	// respawn-every-tick behavior ([scouting] respawn_cap_disabled). Default false = LIVE:
	// the cap is on. RULINGS #5 disable escape so a captain can lift it without a redeploy;
	// not expected to be set in normal operation.
	RespawnCapDisabled bool
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

	// ContainerStatus resolves a SINGLE container's status and existence, so the
	// orphan sweep can ask IsClaimOrphaned about the exact container a scout hull
	// claims (sp-6zgs). found=false means the row is gone. Satisfied by the GORM
	// container repository's ContainerStatus (the same per-ID read refresh_ship's
	// stale-claim reconciler uses).
	ContainerStatus(ctx context.Context, containerID string, playerID shared.PlayerID) (string, bool, error)
}

// MarketWaypointProvider lists the marketplace waypoints in a system — the tour a
// post's hull flies. Satisfied by the GORM waypoint repository
// (ListBySystemWithTrait).
type MarketWaypointProvider interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// GateGraph resolves multi-jump routes over the persisted cross-system gate graph
// (sp-7gr2). The coordinator BFS-walks it to pick the FLEET-WIDE nearest idle
// satellite (fewest jump hops) to reposition to an unmanned post, and to fail closed
// when no satellite can reach it. Optional: nil disables repositioning entirely — the
// coordinator then parks a satellite-less post exactly as before (the sp-qxa4 park),
// so every pre-s232 caller/test behaves unchanged.
type GateGraph interface {
	// RepositionPath resolves the fleet-wide reposition route over the PERSISTED stored
	// adjacency bounded to maxJumps (sp-8k9m): it routes PAST an unreadable frontier gate
	// instead of dead-ending on it (the catch-22 a fetch-through Path can never re-admit —
	// a probe can't reach the frontier because the gate is unreadable, and the gate is
	// unreadable because no probe reached it), and reaches the 6-12-jump posts the strict
	// MaxJumpPath=5 rejects. Safe for the expendable scout class only; every arrival
	// re-reads its gate, so the relaxation retires itself.
	RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error)
}

// MarketFreshnessProvider computes, per POSTED system, the worst-case cached
// market-data staleness — MAX(now - last_updated) across that system's markets —
// backing the scout_freshness_actual_seconds gauge (sp-dp92 P7). One call per
// sweep covers every system for the player in a single query. Satisfied by the
// GORM market repository (MarketRepositoryGORM.MaxAgeSecondsBySystem). Optional:
// nil disables the gauge entirely (pure OBSERVATION, RULINGS #4) — every pre-dp92
// caller/test that never wires it is unaffected.
type MarketFreshnessProvider interface {
	MaxAgeSecondsBySystem(ctx context.Context, playerID int) (map[string]float64, error)
}

// RunScoutPostCoordinatorHandler reconciles the desired-state posts table every
// tick. Each post has HullBudget() manning SLOTS — one for a single-hull post, N for
// a multi-probe post (sp-enry) — and every slot is manned, repaired, and repositioned
// independently. A multi-probe post's markets are first partitioned into N DISJOINT
// per-probe tours via the existing VRP machinery and frozen (re-partitioned only on a
// hull-budget change); each slot then behaves exactly like a single-hull post over its
// partition. The reconciler respawns any tour that died, mans an unmanned slot by
// claiming an idle satellite ALREADY IN THE POST'S SYSTEM (manning is in-system-only,
// sp-qxa4), releases any assignment whose hull drifted out of system so it can be
// re-matched, retires completed sweep-once posts, and never poaches a pinned hull. When
// a slot has no in-system satellite it JUMP-ROUTES the fleet-wide nearest idle satellite
// to it (sp-s232). It is the freshness backbone the tour planner's age cap and the
// analyst board both ride on.
type RunScoutPostCoordinatorHandler struct {
	postRepo       domainScouting.ScoutPostRepository
	shipRepo       navigation.ShipRepository
	daemonClient   daemon.DaemonClient
	containerQuery ContainerStatusQuery
	marketProvider MarketWaypointProvider
	clock          shared.Clock

	// gateGraph resolves jump-hop distances for fleet-wide reposition selection
	// (sp-s232). nil disables repositioning (the sp-qxa4 park is preserved), so it is
	// wired via SetGateGraph rather than the constructor — every existing caller/test
	// that never wires it behaves exactly as before.
	gateGraph GateGraph

	// graphProvider discovers a VIRGIN system's waypoints presence-free via the API when
	// the reposition target has zero KNOWN market waypoints (sp-nn0y), and supplies
	// waypoint coordinates to the VRP partitioner (sp-enry). It is the same cache-first
	// ISystemGraphProvider port scout_markets/assign_scouting_fleet use, and persists
	// discovered waypoints era-scoped via its BuildSystemGraph→Add path (sp-vapw). nil
	// disables virgin discovery and leaves the partitioner without coordinates (it still
	// partitions, just without geometry) — so it is wired via SetGraphProvider rather than
	// the constructor, and every existing caller/test that never wires it behaves identically.
	graphProvider system.ISystemGraphProvider

	// routingClient solves the VRP that partitions a multi-probe post's markets into N
	// disjoint tours (sp-enry). Reuses the SAME PartitionFleet the `workflow scout-markets`
	// verb uses — the routing service already solves the partition problem. nil disables
	// partitioning: a multi-probe post then cannot materialize its extra slots and parks
	// (fail-closed), while single-hull posts (which never partition) are unaffected. Wired
	// via SetRoutingClient so every pre-enry caller/test that never wires it is unchanged.
	routingClient routing.RoutingClient

	// marketFreshnessProvider supplies scout_freshness_actual_seconds' raw ages
	// (sp-dp92 P7): MAX(now - last_updated) per system with cached market rows,
	// read once per sweep. nil disables the gauge — pure OBSERVATION (RULINGS #4),
	// never a decision input — so it is wired via SetMarketFreshnessProvider
	// rather than the constructor, mirroring SetGateGraph/SetRoutingClient; every
	// pre-dp92 caller/test that never wires it is unaffected.
	marketFreshnessProvider MarketFreshnessProvider

	// repositionBackoffUntil rate-limits reposition DISPATCH per post slot (key
	// playerID|system[|slotIndex] → earliest next dispatch time) so a relay that fails
	// fast does not hot-loop re-dispatch (sp-s232). A single-hull post keeps the pre-enry
	// un-suffixed key. In-memory (reset on restart); guarded by repositionMu since the
	// handler is a registered singleton that could serve two players' coordinator ticks
	// concurrently.
	repositionMu           sync.Mutex
	repositionBackoffUntil map[string]time.Time

	// repositionFailures counts CONSECUTIVE reposition-relay failures per post slot (same
	// key shape as repositionBackoffUntil), so the failure log reports the Nth attempt and a
	// completed relay resets the streak (sp-o34q). Guarded by repositionMu with the deadline
	// map it travels with. repositionBackoffLoggedUntil records the deadline of the backoff
	// episode already logged for a key, so a long cooldown is announced ONCE (state change)
	// rather than every 30s tick — the fix for the event flood. Both in-memory, reset on
	// restart (a lost streak only restarts the count; a lost log-marker re-announces once).
	repositionFailures           map[string]int
	repositionBackoffLoggedUntil map[string]time.Time

	// driftPendingSince tracks, per partitioned post (key playerID|system — driftKey,
	// the same un-suffixed shape as backoffKey's primary form, since drift is a
	// whole-post property), when its market set FIRST started differing from its
	// persisted partition union (sp-ykhl) — the age half of the debounced re-cut
	// trigger. Cleared once a re-cut resolves the drift, the drift resolves on its own
	// (e.g. a flapping discovery), or the post reverts to single-hull. In-memory and
	// reset on restart, mirroring repositionBackoffUntil: losing it only restarts the
	// age countdown (the count trigger is stateless and still fires unaffected), never
	// a stability violation — same hulls + same markets never populates this map, so
	// the sp-enry "zero re-cuts when stable" guarantee is untouched. Guarded by
	// driftMu for the same singleton-handler concurrency reason as repositionMu.
	driftMu           sync.Mutex
	driftPendingSince map[string]time.Time

	// singleHullMarketSnapshot and singleHullDriftPendingSince give a SINGLE-hull
	// standing post the same debounced market-set-drift respawn ykhl gave partitioned
	// posts (sp-tzqv). A single-hull tour's market list is frozen at spawn time
	// (ScoutTourCommand.Markets, set once in spawnTour) and never re-read afterward by
	// either scout_tour execution mode — including executeStationaryScout, which has
	// no circuit-boundary hook at all — so a market nn0y discovers after spawn is never
	// toured until the post re-mans for an unrelated reason. ensureSingleHullFreshness
	// closes that gap by tearing down and re-manning the post (which re-discovers
	// markets fresh, see slotMarkets) once its live discovered set has drifted from
	// the snapshot taken at the post's last manning, reusing driftKey's key shape and
	// the SAME MarketDriftThreshold/MarketDriftMaxAgeSecs config ykhl introduced. Two
	// SEPARATE maps — not two keys inside driftPendingSince/repositionBackoffUntil —
	// because ensurePartitions unconditionally clears driftPendingSince[driftKey(...)]
	// every tick for every budget<=1 post, which would wipe a shared entry before it
	// could ever accumulate age. In-memory and reset on restart: a lost snapshot is
	// treated as "adopt current markets, don't respawn" (see ensureSingleHullFreshness)
	// rather than maximal drift, so a restart never triggers a respawn storm across
	// every standing post fleet-wide. Guarded by singleHullMu for the same
	// singleton-handler concurrency reason as driftMu.
	singleHullMu                sync.Mutex
	singleHullMarketSnapshot    map[string][]string
	singleHullDriftPendingSince map[string]time.Time

	// eventStore records the DEFERRED scout.post_undersized warning (sp-k7q5 layer 1)
	// and dedups it via HasSince. Optional (SetEventStore): nil leaves the warning off
	// entirely — every pre-k7q5 caller/test that never wires it behaves exactly as
	// before, and the coordinator's manning/reconcile behavior is untouched. It is a
	// pure OBSERVATION seam: a store error never aborts a reconcile pass.
	eventStore captain.EventStore
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
		singleHullMarketSnapshot:     make(map[string][]string),
		singleHullDriftPendingSince:  make(map[string]time.Time),
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver (sp-s232). The daemon injects
// the same persisted, fetch-through gategraph.Service the trade-route circuit uses, so
// the reposition BFS and the circuit's travel() share one cache/graph. Mirrors the
// trade-route coordinator's SetGateGraph optional-injection idiom; nil (the default)
// leaves repositioning disabled and the sp-qxa4 park behavior intact.
func (h *RunScoutPostCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// SetGraphProvider wires the presence-free waypoint discoverer for virgin reposition
// targets (sp-nn0y) and the coordinate source for the VRP partitioner (sp-enry). The
// daemon injects the same graphService the `waypoint` verb and the scout-markets planner
// use, so discovery shares one cache/graph and persists era-scoped exactly as every other
// charting path. Mirrors SetGateGraph's optional-injection idiom; nil (the default) leaves
// the pre-nn0y park behavior intact.
func (h *RunScoutPostCoordinatorHandler) SetGraphProvider(g system.ISystemGraphProvider) {
	h.graphProvider = g
}

// SetRoutingClient wires the VRP fleet partitioner (sp-enry). The daemon injects the
// SAME routing client the scout-markets verb uses, so a multi-probe post's disjoint
// partition is solved by the routing service that already solves it. Optional-injection
// (like SetGateGraph): nil leaves partitioning disabled, so single-hull posts are
// unaffected and a multi-probe post parks fail-closed until a client is wired.
func (h *RunScoutPostCoordinatorHandler) SetRoutingClient(c routing.RoutingClient) {
	h.routingClient = c
}

// SetEventStore wires the captain event outbox for the undersized-post warning
// (sp-k7q5 layer 1). The daemon injects the SAME store the watchkeeper reads, so a
// warning rides the next wake as a deferred event. Optional-injection (like
// SetGateGraph): nil (the default) leaves the warning disabled and every pre-k7q5
// caller/test unchanged.
func (h *RunScoutPostCoordinatorHandler) SetEventStore(s captain.EventStore) {
	h.eventStore = s
}

// SetMarketFreshnessProvider wires the scout_freshness_actual_seconds gauge's data
// source (sp-dp92 P7). The daemon injects the same GORM market repository the rest
// of the coordinator already reads through. Optional-injection (like SetGateGraph):
// nil (the default) leaves the gauge unrecorded, so every pre-dp92 caller/test that
// never wires it behaves exactly as before.
func (h *RunScoutPostCoordinatorHandler) SetMarketFreshnessProvider(p MarketFreshnessProvider) {
	h.marketFreshnessProvider = p
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
	// observable (sp-e2l1): once the streak crosses DefaultStreakThreshold it emits a
	// captain event instead of just another ERROR line. One per Handle invocation so
	// the streak persists across ticks; noteReconcile keeps reconcileOnce — the tested
	// unit — unchanged, and reuses the already-wired eventStore as the recorder.
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
// offset (sp-x8i5) before the reconcile loop's first tick, keyed on ContainerID — this
// coordinator's stable identity, unchanged across restart recovery (unlike a freshly
// generated random value would be). Reuses stableJitter/defaultTourStartJitterMax from
// scout_tour.go (same package): decoheres this coordinator's tick from other standing
// coordinators (trade_fleet, worker_rebalancer, manufacturing) that might otherwise
// tick in lockstep. Returns false if ctx is cancelled during the wait, so the caller can
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
// normally or false if ctx was cancelled first (sp-x8i5). Clock-injected so tests run on
// a MockClock with no wall-time cost — this handler's own private copy, mirroring the
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
// reposition it with the post's markets, priority, and freshness (sp-enry).
type slotTarget struct {
	post *domainScouting.ScoutPost
	slot domainScouting.ScoutSlotRef
}

// noteReconcile records one reconcile pass at the "reconcile" streak checkpoint
// (sp-6wxq): a nil err is a success that resets the streak; a non-nil err that
// repeats identically for DefaultStreakThreshold consecutive passes crosses and
// emits the coordinator error-loop captain event. It reuses the already-wired
// eventStore (captain.EventStore embeds EventRecorder) as the recorder, so no new
// injection is needed — nil-safe when the store is unwired (tests), edge-triggered.
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
// A post has HullBudget() manning SLOTS (sp-enry): one for a single-hull post — the
// primary slot, byte-identical to the pre-enry behavior — or N for a multi-probe post,
// whose markets are first partitioned into N disjoint per-probe tours and frozen. Every
// pass below iterates SLOTS, not posts, so a dead probe on one slot heals without
// touching its siblings.
//
// Passes:
//   - Partition (sp-enry): (re)compute a multi-probe post's N disjoint partitions via VRP
//     ONLY when its hull budget changed (slot count != budget), and persist them — so a
//     restart reloads the frozen partitions and never re-tours, and a re-man reuses the same
//     partition.
//   - Pass 1 (manned slots): release any slot whose hull drifted out of the post's system
//     (sp-qxa4 repair — stop its tour, free the hull, clear the slot); retire a completed
//     sweep-once (release its hull, delete the post); free the hull of any other slot whose
//     tour is not running, clearing it so pass 2 re-mans it with the SAME partition. A
//     healthy in-system tour is left untouched.
//   - Pass 1.5 (repositioning slots): a slot with a relay in flight is left alone while its
//     container is RUNNING; when the relay ends the hull is reclaimed and the relay reference
//     cleared so pass 2 re-evaluates the slot.
//   - Pass 2a (in-system manning): claim an idle satellite ALREADY IN THE POST'S SYSTEM and
//     spawn its tour over the slot's markets (all markets for a single-hull post, the frozen
//     partition for a multi-probe slot). In-system only (sp-qxa4).
//   - Pass 2b (reposition, sp-s232): for a slot STILL unmanned, jump-route the FLEET-WIDE
//     nearest idle satellite to the post's system, then let the next tick's 2a man it.
func (h *RunScoutPostCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) error {
	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	if len(posts) == 0 {
		return nil
	}

	// sp-k7q5 layer 1: warn (deferred) on any standing post whose circuit math cannot
	// meet its own freshness contract, BEFORE the manning passes — a pure observation
	// over the freshly-loaded post state that never mutates a post or aborts the tick.
	h.warnUndersizedPosts(ctx, cmd, posts)

	// Freshness gauge (sp-dp92 P7): pure OBSERVATION, so it runs unconditionally
	// ahead of the manning passes and can never affect them (RULINGS #4).
	h.recordScoutFreshness(ctx, cmd, posts)

	// Pass 0 (orphan sweep, sp-6zgs): free any scout hull whose owning container is
	// orphaned but that NO post slot references — a probe stranded active when its post
	// died in a reconciler outage / reset (the KN67 far-cluster probes). Such a hull is
	// invisible to both Pass 1 (no slot points at it) and the idle scan (it is active,
	// not idle), so it sits claimed-but-driverless forever. Running BEFORE the manning
	// passes returns it to the idle pool in time for Pass 2a to re-seat it in its own
	// system this same tick — the captain's "same-system re-seat beats a 6-hop relay".
	h.sweepOrphanedScoutHulls(ctx, cmd, posts)

	running, err := h.containerIDSet(ctx, cmd, "RUNNING")
	if err != nil {
		return err
	}
	completed, err := h.containerIDSet(ctx, cmd, "COMPLETED")
	if err != nil {
		return err
	}
	// sp-o34q: the FAILED set lets pass 1.5 distinguish a relay that DIED (unroutable — arm
	// the long failure cooldown and rotate the probe) from one that ARRIVED (reset the streak)
	// or was merely restart-interrupted (keep the short floor). Without it every ended relay
	// looked identical and a dark post's corpse was respawned every few minutes.
	failed, err := h.containerIDSet(ctx, cmd, "FAILED")
	if err != nil {
		return err
	}

	removed := make(map[string]bool)

	// Partition pass (sp-enry): materialize each multi-probe post's disjoint tours.
	// A no-op for single-hull posts and for multi-probe posts already partitioned at
	// their current budget — it re-partitions ONLY on a hull-budget change.
	//
	// ensureSingleHullFreshness (sp-tzqv) runs right after: the single-hull mirror of
	// the same market-set-drift check, so a triggered teardown is picked up as
	// "unmanned" by pass 1/2 in THIS SAME tick, exactly like a partition re-cut is.
	for _, post := range posts {
		h.ensurePartitions(ctx, cmd, post)
		h.ensureSingleHullFreshness(ctx, cmd, post)
	}

	// Pass 1: manned slots.
	for _, post := range posts {
		if h.reconcileMannedSlots(ctx, cmd, post, running, completed, removed) {
			continue // post retired (sweep-once complete)
		}
	}

	// Pass 1.5: repositioning slots (sp-s232). A relay in flight (RUNNING) owns its slot —
	// pass 2 skips it. When the relay is no longer RUNNING it has landed, failed, or was
	// restart-interrupted; reclaim defensively and clear the relay reference so pass 2
	// re-evaluates the slot.
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		h.reconcileRepositioningSlots(ctx, cmd, post, running, completed, failed)
	}

	// Pass 2: man the unmanned slots, standing posts first. A post inside its sp-py4n respawn-cap
	// backoff window is skipped here so a persistently-crashing tour is not respawned every tick.
	_, respawnCapEnabled := resolveRespawnCap(cmd)
	targets := h.unmannedSlotTargets(posts, removed, cmd.CoverageSpreadDisabled, respawnCapEnabled)
	if len(targets) == 0 {
		return nil
	}

	idleSats, err := h.idleScoutSatellites(ctx, cmd)
	if err != nil {
		return err
	}

	// Pass 2a: man every slot that has an idle satellite ALREADY in its system (sp-qxa4
	// in-system-only manning). Doing this for ALL slots before any reposition guarantees an
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
			// Baseline the freshness snapshot to what this tour actually launched with
			// (sp-tzqv), so the NEXT tick's drift check compares against reality
			// instead of an empty/stale snapshot.
			h.setSingleHullSnapshot(driftKey(cmd.PlayerID.Value(), tgt.post.SystemSymbol), markets)
		}
	}

	// Pass 2b: jump-route the fleet-wide nearest idle satellite to each still-unmanned slot
	// (sp-s232). repositionUnmannedSlot fails closed — no gate graph, no idle satellite, no
	// reachable satellite, no known markets, or an active backoff parks the slot honest — so
	// with no gate graph wired this is exactly the pre-s232 park.
	for _, tgt := range stillUnmanned {
		h.repositionUnmannedSlot(ctx, cmd, tgt.post, tgt.slot, &idleSats)
	}

	return nil
}

// releaseReasonScoutOrphanSwept marks a scout hull freed by the coordinator's orphan
// sweep (sp-6zgs): an active probe whose owning container is orphaned and which no post
// slot manages, returned to the idle pool for in-system re-seat. Distinct from
// stale_claim_reconciled (refresh-time, sp-vjwb) so the audit trail names WHICH
// reconciler freed the hull.
const releaseReasonScoutOrphanSwept = "scout_orphan_swept"

// sweepOrphanedScoutHulls frees scout hulls stranded active on an orphaned container that
// NO post slot references (sp-6zgs) — see the Pass 0 comment in reconcileOnce. It reuses
// refresh_ship's IsClaimOrphaned verdict (sp-vjwb) so the sweep and refresh-time
// reconciliation can never disagree on which claims are safe to reap, and it is pure
// best-effort: any read error is logged and skipped, never aborting the reconcile pass.
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
		// Only expendable scout hulls; only real container claims. A captain reservation is
		// an active assignment with NO container (sp-i1ku) — nothing to go stale — so it must
		// be excluded before the container lookup, exactly as refresh_ship's reconciler does,
		// or it would be reaped as "container gone".
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

		ship.ForceRelease(releaseReasonScoutOrphanSwept, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Scout orphan sweep freed %s but failed to persist the release: %v", ship.ShipSymbol(), err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Scout orphan swept: %s freed from orphaned container %s — returning to the idle pool for in-system re-seat", ship.ShipSymbol(), containerID), map[string]interface{}{
			"action":             "scout_orphan_swept",
			"ship_symbol":        ship.ShipSymbol(),
			"orphaned_container": containerID,
			"container_status":   status,
		})
	}
}

// warnUndersizedPosts emits a DEFERRED scout.post_undersized event for any STANDING
// post whose deterministic circuit math (markets / hulls × avgHop) cannot keep its
// markets within the post's own freshness target (sp-k7q5 layer 1) — the structural
// defect that let XT71/UQ87 run 110-125-min-stale on a single probe while reading
// "fully manned" and alarming nothing. The event names the required hull count, so the
// fix (raise the budget) is spelled out.
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
			continue // sweep-once has no standing freshness contract to fail.
		}
		freshness := post.FreshnessTarget
		if freshness <= 0 {
			continue // no contract to measure against — cannot assess.
		}
		markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
		if err != nil {
			continue // transient discovery gap — never warn on missing data.
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

// recordScoutFreshness sets the scout_freshness_actual_seconds gauge (sp-dp92 P7)
// for every POSTED system this pass is about to reconcile — i.e. exactly the
// systems in posts, one entry per active ScoutPost. A single provider read covers
// every system for the player (MarketFreshnessProvider.MaxAgeSecondsBySystem); a
// post whose system has no cached market rows yet (freshly posted, nothing scanned
// this era) simply has no entry in the returned map and is skipped this sweep — its
// gauge appears once a scan lands. Pure OBSERVATION (RULINGS #4): no provider wired,
// or a read error, is logged (once, not per-post) and the reconcile pass continues
// completely unaffected — a metrics gap must never block manning.
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

// reconcileMannedSlots runs pass 1 over one post's slots. It returns true when the
// post was retired (a completed sweep-once), so the caller skips it in later passes.
func (h *RunScoutPostCoordinatorHandler) reconcileMannedSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	running, completed, removed map[string]bool,
) bool {
	logger := common.LoggerFromContext(ctx)

	// sp-py4n respawn accounting: track, across this post's slots, whether any tour was
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

		// REPAIR (sp-qxa4): the assigned hull is no longer in the post's system. Its
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
		// this tick, so it resets any consecutive-respawn streak (sp-py4n).
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
		// This is the respawn the sp-py4n cap bounds: a tour crashing on a PERSISTENT reason lands
		// here every tick, so sawRespawn feeds accountRespawn below.
		h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_respawn")
		slot.SetAssignedHull("")
		slot.SetTourContainerID("")
		sawRespawn = true
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear assignment on post %s: %v", post.SystemSymbol, err), nil)
		}
	}

	// sp-py4n: advance or reset the persisted respawn-attempt counter and park the post once it
	// exhausts the cap, so a persistently-crashing tour stops respawn-looping at tick cadence.
	h.accountRespawn(ctx, cmd, post, sawHealthy, sawRespawn)
	return false
}

// resolveRespawnCap resolves the sp-py4n respawn-loop cap for this coordinator: whether the cap is
// LIVE (RespawnCapDisabled is the RULINGS #5 escape) and, when live, the consecutive-respawn
// ceiling ([scouting] respawn_attempt_cap, else defaultScoutRespawnAttemptCap). Mirrors
// repositionFailureCooldown's <= 0 → default shape.
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
// reconcile pass over its slots, and parks the post once the counter exhausts the cap (sp-py4n).
// A tour observed HEALTHY this tick (sawHealthy) means the last spawn survived, so the streak
// resets and any park is lifted — the cap counts CONSECUTIVE failures, not lifetime. A dead tour
// respawned this tick (sawRespawn) advances the streak; on reaching the cap the post is parked for
// defaultRespawnParkWindow (RespawnParkedUntil) instead of respawned yet again, and the exhaustion
// is logged (naturally rate-limited to one line per window, since a parked post spawns nothing to
// respawn until the window elapses and it retries once). Both fields persist so the cap survives a
// daemon restart rather than the crash-loop resuming at tick cadence. Healthy wins over respawn,
// so any one live slot resets a multi-hull post's whole-post streak. Disabled ⇒ a no-op, the
// pre-py4n respawn-every-tick behavior.
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
// and clear its reference so pass 2 re-evaluates the slot (sp-s232). The relay's TERMINAL
// disposition (sp-o34q) decides what happens to the post's dispatch cooldown:
//   - FAILED (the unroutable verdict): arm the LONG failure cooldown and count the attempt,
//     so the coordinator stops respawning the same corpse every few minutes and the freed
//     probe rotates to the next candidate this tick (the fix for the ~20-corpses/15min
//     crash-loop).
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

// isRespawnParked reports whether a post is currently inside its sp-py4n respawn-cap backoff
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
			continue // sp-py4n: parked in its respawn-cap backoff window — none of its slots man this tick
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
		// Legacy depth-first (sp-6ovd disable escape): every one of a post's slots before
		// the next post's — the pre-fix order, byte-identical for single-hull posts.
		for _, slots := range perPost {
			targets = append(targets, slots...)
		}
		return targets
	}

	// sp-6ovd coverage-first: interleave by slot TIER across the priority-ordered posts —
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

// ensurePartitions materializes a multi-probe post's N DISJOINT market partitions
// (sp-enry). It is a no-op for a single-hull post (HullBudget 1 → no partition, the
// primary tours all markets) and for a multi-probe post ALREADY partitioned at its
// current budget AND not yet drifted enough to re-cut — so it re-partitions on a
// hull-budget change (unconditional, as before) or on a DEBOUNCED market-set drift
// (sp-ykhl: nn0y virgin discovery adds markets to a system after a post's tours are
// already cut, and a market discovered post-cut belongs to NO partition — it goes
// permanently stale even though the post reads fully manned; removals fold into the
// same check), never on every tick. On any re-cut of a running post it stops the
// existing tours/relays (their markets change), reclaims their hulls, and rebuilds
// the slots with fresh partitions; pass 2 re-mans them. Fails closed: no routing
// client, no markets, or a VRP error leaves an UNPARTITIONED post un-partitioned (it
// parks) and retries next tick — symmetrically, an ALREADY-stable-and-partitioned
// post hitting one of those same conditions just keeps touring its existing
// (possibly stale) partition rather than being torn down over a transient discovery
// hiccup or a missing routing client.
//
// API-BUDGET INVARIANT (documented per the bead): partitioning changes WHERE probes scan,
// not HOW MUCH. Total scans/hour ≈ markets / freshness-target regardless of N — N smaller
// partitions each paced to the freshness target (circuitPaceInterval, scout_tour.go) sum to
// one scan per market per freshness window. More probes buy fresher data, NOT more API calls.
func (h *RunScoutPostCoordinatorHandler) ensurePartitions(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	budget := post.HullBudget()
	key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

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
	// re-cuts unconditionally (below, as before sp-ykhl); a stable budget only re-cuts
	// if the market SET has drifted enough to debounce-trigger — checked once the
	// current market list is known.
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
			return // stable: same hulls, same markets — no re-cut (sp-enry invariant preserved).
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
// drift re-cut (sp-tzqv): a single-hull standing post's tour is spawned once with the
// system's market list AT THAT MOMENT (spawnTour←slotMarkets←discoverMarkets) and
// never re-reads it afterward — executeMultiMarketTour only re-derives markets at a
// respawn, and executeStationaryScout (chosen when the system had exactly one known
// market at spawn) has no circuit-boundary hook at all. A market nn0y discovers after
// spawn is therefore never toured until the post re-mans for an unrelated reason. This
// closes the gap the same way ykhl closed it for partitioned posts: tear the post down
// (which pass 2 immediately re-mans, and slotMarkets/discoverMarkets gives the new
// tour a FRESH market list) once the live discovered set has drifted from the snapshot
// taken at last manning by at least MarketDriftThreshold markets, or the drift has
// been pending at least MarketDriftMaxAgeSecs — reusing ykhl's exact thresholds/config
// fields and diffMarketSets' set-diff semantics rather than inventing new ones.
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
		return // stable: same markets — no respawn (mirrors the sp-enry/ykhl invariant).
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
// market set differs from the union of its persisted partitions (sp-ykhl), plus the
// size of that union (the "old market count" for the re-cut's observability log). A
// market discovered after the post was last cut belongs to no partition (an
// addition); a market still assigned to a partition but no longer discovered (a
// removal) is included identically — both fold into ONE set so additions and
// removals debounce the same way, with no special-casing (per the bead). Sorted for
// a deterministic re-cut log and test assertions.
func marketSetDrift(post *domainScouting.ScoutPost, currentMarkets []string) (drifted []string, unionSize int) {
	union := make([]string, 0, len(post.PrimaryPartition))
	union = append(union, post.PrimaryPartition...)
	for _, slot := range post.ExtraSlots {
		union = append(union, slot.Partition...)
	}
	return diffMarketSets(union, currentMarkets)
}

// diffMarketSets is marketSetDrift's set-diff core, factored out so a SINGLE-hull
// post's freshness check (ensureSingleHullFreshness, sp-tzqv) can reuse the identical
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
// a re-partition (sp-enry), then clears the assignments in memory. Best-effort: a hull
// the coordinator fails to reclaim here is reclaimed by pass 1 on a later tick.
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
	// sp-py4n: a market-set re-cut is a genuine STATE CHANGE — the post's tour will fly a
	// different market list, so its old consecutive-respawn streak (which measured failures under
	// the OLD state) no longer applies. Clear the counter and lift any respawn park so the re-cut
	// post gets a fresh chance rather than staying parked from a pre-drift crash-loop.
	post.RespawnAttempts = 0
	post.RespawnParkedUntil = time.Time{}
}

// partitionMarkets solves the VRP that splits markets into n DISJOINT per-probe tours
// (sp-enry), reusing the SAME PartitionFleet the scout-markets verb uses. The N probes are
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

// slotMarkets returns the waypoints a slot's tour should scan (sp-enry): ALL the
// system's markets for a single-hull post (the pre-enry behavior, discovered fresh), or
// the slot's frozen partition for a multi-probe post.
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
// freshness target (sp-zixw): half the freshness window, clamped via
// clampScanInterval (scout_tour.go) to [scanIntervalFloor, scanIntervalCap] so
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
// multi-probe post paces its own partition against one freshness target (sp-enry).
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

	// Atomic claim (l7h2): rejects a hull pinned to another fleet at the DB, so a
	// pin racing discovery can never be poached. %w so the poach-vector test can
	// distinguish a dedication rejection from a transient failure.
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
// with no in-system hull (sp-s232). It FAILS CLOSED at every gap — no gate graph, no idle
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
) {
	logger := common.LoggerFromContext(ctx)
	key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())

	// No gate graph wired, or no idle satellite left this tick → cannot reposition. Park
	// honest with the in-system reason (the pre-s232 / sp-qxa4 behavior and greppable message).
	if h.gateGraph == nil || len(*idleSats) == 0 {
		h.parkNoInSystemSatellite(ctx, post)
		return
	}

	// A recent relay for this slot failed — don't hot-loop re-dispatch (sp-py4n). Announce the
	// skip ONCE per cooldown episode (sp-o34q): a standing dark post used to log this on every
	// 30s tick, the ~20-events/15min flood the captain flagged. noteRepositionBackoffLogged
	// keys the announcement on the exact backoff deadline, so a new failure (a later deadline)
	// re-announces once and a steady cooldown stays quiet.
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
		// Virgin frontier system (sp-nn0y): discover its waypoints presence-free, then retry.
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return // parked honest by discoverVirginMarkets
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	// Fleet-wide nearest idle satellite by jump-hop count over the stored adjacency,
	// bounded to the expendable-probe reposition reach (sp-8k9m). Fail-closed on unroutable.
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

	relayID, err := h.spawnReposition(ctx, cmd, sat.ShipSymbol(), destWaypoint, maxJumps)
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

// discoverVirginMarkets resolves the s232 bootstrap chicken-and-egg for a post whose
// system has ZERO known market waypoints (sp-nn0y): it DISCOVERS the system's waypoints
// presence-free via the graph provider's cache-first GetGraph, persisting them era-scoped,
// then re-reads. markets found → returns them (the caller repositions this tick); none →
// parks UNSERVICEABLE (charted but barren); API error → parks fail-closed. It arms the
// per-slot dispatch backoff (key) BEFORE the API call, so a marketless or API-erroring
// system is probed at most ONCE per window. With no graph provider wired it logs the pre-nn0y
// park verbatim, so every existing caller/test is unaffected.
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

// resolveMaxRepositionJumps returns the expendable-probe reposition reach (sp-8k9m):
// the launch-config [scouting] max_reposition_jumps, or defaultMaxRepositionJumps when
// unset (RULINGS #5, the <= 0 → default idiom this file uses for every other knob).
func resolveMaxRepositionJumps(cmd *RunScoutPostCoordinatorCommand) int {
	if cmd.MaxRepositionJumps <= 0 {
		return defaultMaxRepositionJumps
	}
	return cmd.MaxRepositionJumps
}

// repositionFailureCooldown resolves the FAILED-relay cooldown (sp-o34q): the launch config's
// [scouting] reposition_failure_cooldown_secs when positive, else the 30-min default. Mirrors
// resolveMaxRepositionJumps' <= 0 → default shape.
func repositionFailureCooldown(cmd *RunScoutPostCoordinatorCommand) time.Duration {
	if cmd.RepositionFailureCooldownSecs <= 0 {
		return defaultRepositionFailureCooldown
	}
	return time.Duration(cmd.RepositionFailureCooldownSecs) * time.Second
}

// selectNearestSatelliteByHops returns the index (into idleSats) of the satellite
// FEWEST jump hops from postSystem, its hop count, and ok=false when none can be
// jump-routed there (sp-s232). Distance is the RepositionPath BFS length over the
// PERSISTED stored adjacency bounded to maxJumps (sp-8k9m) — the expendable-probe resolver
// that routes PAST unreadable frontier gates and reaches the 6-12-jump posts the strict
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
) (string, error) {
	workerID := utils.GenerateContainerID("scout_reposition", hullSymbol)
	repoCmd := &ScoutRepositionCommand{
		PlayerID:            cmd.PlayerID,
		ShipSymbol:          hullSymbol,
		DestinationWaypoint: destinationWaypoint,
		CoordinatorID:       cmd.ContainerID,
		MaxRepositionJumps:  maxJumps,
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

// parkNoInSystemSatellite logs the honest, system-scoped park reason for an unmanned
// slot that has no in-system satellite and cannot be repositioned (no gate graph, or no
// idle satellite left this tick). The message text is preserved verbatim from the
// pre-s232 park so `container logs` greps and the sp-qxa4 park assertions still match.
func (h *RunScoutPostCoordinatorHandler) parkNoInSystemSatellite(ctx context.Context, post *domainScouting.ScoutPost) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
		"action":        "scout_post_unmanned_no_in_system_satellite",
		"system_symbol": post.SystemSymbol,
	})
}

// repositionBackedOff reports whether a reposition dispatch for key is currently within
// its backoff window (sp-s232). A nil/absent entry reads false.
func (h *RunScoutPostCoordinatorHandler) repositionBackedOff(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	return ok && h.clock.Now().Before(until)
}

// noteRepositionDispatch arms the per-slot dispatch backoff (sp-s232) so the next
// dispatch for this slot waits out repositionRetryBackoff — the anti-hot-loop bound.
func (h *RunScoutPostCoordinatorHandler) noteRepositionDispatch(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	h.repositionBackoffUntil[key] = h.clock.Now().Add(repositionRetryBackoff)
}

// noteRepositionFailure records a FAILED reposition relay for key (sp-o34q): it increments the
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
// (sp-o34q) — called when a relay COMPLETES, so a post that finally succeeded starts clean and
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
// been announced, and marks it announced (sp-o34q). It keys the marker on the exact backoff
// deadline, so each distinct cooldown window logs its skip reason exactly once — the fix for the
// ~20-events/15min flood a standing dark post emitted every tick. A new failure arms a later
// deadline, which reads as a new episode and logs once more.
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
// each slot of a multi-probe post repositions independently (sp-enry). The PRIMARY slot
// keeps the pre-enry un-suffixed key, so a single-hull post is byte-identical to s232.
func backoffKey(playerID int, system string, slotIndex int) string {
	if slotIndex < 0 {
		return fmt.Sprintf("%d|%s", playerID, system)
	}
	return fmt.Sprintf("%d|%s|%d", playerID, system, slotIndex)
}

// driftKey scopes market-drift debounce tracking to (playerID, system) — the same
// un-suffixed shape as backoffKey's primary-slot form, since drift is a whole-post
// property (the market SET, not any one slot) (sp-ykhl).
func driftKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// noteDriftPending records the FIRST tick a post's market set was seen drifting
// (sp-ykhl) and returns how long the drift episode has been pending. A key already
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

// clearDriftPending forgets a post's pending-drift episode (sp-ykhl): called once a
// re-cut resolves it, the drift resolves on its own, or the post is no longer
// partitioned (reverted to single-hull). A nil/absent entry is a harmless no-op.
func (h *RunScoutPostCoordinatorHandler) clearDriftPending(key string) {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	delete(h.driftPendingSince, key)
}

// singleHullSnapshot returns the market set a single-hull post was last (re-)manned
// with, and whether one is recorded yet (sp-tzqv). Absent after a fresh handler
// (daemon restart) or before the post's first successful manning.
func (h *RunScoutPostCoordinatorHandler) singleHullSnapshot(key string) ([]string, bool) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	markets, ok := h.singleHullMarketSnapshot[key]
	return markets, ok
}

// setSingleHullSnapshot records the market set a single-hull post is now toured
// against (sp-tzqv) — called once when the post is freshly (re-)manned (pass 2a), and
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

// clearSingleHullSnapshot forgets a single-hull post's freshness baseline (sp-tzqv):
// called once a drift-triggered respawn resolves it. The post's next manning
// (pass 2a) sets a fresh one immediately, so this is momentary.
func (h *RunScoutPostCoordinatorHandler) clearSingleHullSnapshot(key string) {
	h.singleHullMu.Lock()
	defer h.singleHullMu.Unlock()
	delete(h.singleHullMarketSnapshot, key)
}

// noteSingleHullDriftPending records the FIRST tick a single-hull post's market set
// was seen drifting from its snapshot (sp-tzqv) and returns how long the drift episode
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

// clearSingleHullDriftPending forgets a single-hull post's pending-drift episode
// (sp-tzqv): called once a respawn resolves it, the drift resolves on its own, or the
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
// -assignment defect the repair pass heals (sp-qxa4). It fails safe: a hull that cannot be
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
		ship.ForceRelease(reason, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim hull %s from container %s: %v", ship.ShipSymbol(), containerID, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed hull %s from container %s", ship.ShipSymbol(), containerID), nil)
	}
}

// releaseHull frees a specific hull by symbol (sweep-once retirement, start-failure
// rollback). Best-effort.
func (h *RunScoutPostCoordinatorHandler) releaseHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, hullSymbol, reason string) {
	if hullSymbol == "" {
		return
	}
	logger := common.LoggerFromContext(ctx)
	ship, err := h.shipRepo.FindBySymbol(ctx, hullSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load hull %s to release (%s): %v", hullSymbol, reason, err), nil)
		return
	}
	if !ship.IsAssigned() {
		return
	}
	ship.ForceRelease(reason, h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
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
// post's system, or -1 if none. Cross-system matching is intentionally impossible
// (sp-qxa4): the scout_tour worker navigates in-system only, so a cross-system
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
