package commands

// RunShipyardBackfillCoordinator (sp-rhju Part 3) is the standing CATCH-UP SWEEP that
// closes the shipyard-scan blind spot. Historically the shipyard scan rode only scout
// MARKET tours (sp-42ow), so it lagged the depth frontier: a system the frontier CHARTED
// (its waypoint set swept, so its SHIPYARD trait is known) but that no market tour ever
// toured stayed unscanned — 55 charted shipyards, only 10 scanned, a 45-system blind spot
// the heavy-freighter yard the autosizer hunts (fail-closed on a 21-heavy shortfall) may
// already sit in.
//
// Each tick it enumerates the CHARTED-but-UNSCANNED shipyard systems, drops those already
// covered by a live post, and declares a bounded, DEEPER-FIRST batch of single-hull
// sweep-once posts through the SAME repository seam the frontier coordinator and the
// `scout posts add` RPC use. The scout-post reconciler relays an idle probe to each; the
// probe's arrival rides the sp-rhju decoupled shipyard scan (Part 1) and persists the row,
// and a heavy listing fires the existing once-per-era heavy_yard_discovered event unchanged.
//
// It NEVER moves a probe and NEVER claims a hull (RULINGS #7) — declaration only, exactly
// like the frontier coordinator. The dispatch is bounded per cycle by min(rate knob, idle
// probe supply) so the sweep never dispatches all at once and never declares more posts
// than there are idle probes to man them (it must not starve freshness/depth of hulls).
// Idempotent and self-quiescing: once the blind spot is drained there are no unscanned
// systems to declare and the coordinator is a silent no-op.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// backfillDefaultTickSeconds paces the standing loop. The sweep is a low-priority
	// catch-up, so a relaxed default keeps it well clear of the scan/freshness budget.
	backfillDefaultTickSeconds = 120

	// backfillDefaultMaxDispatchesPerCycle is the per-cycle declaration cap (the rate
	// limit). Bounded small so the 45-system blind spot drains over a handful of cycles
	// rather than flooding the reconciler with posts in one tick — the "doesn't dispatch
	// all at once" guard. Live-tunable (ShipyardBackfillTunableDefaults).
	backfillDefaultMaxDispatchesPerCycle = 5

	// backfillDefaultMaxHops is the enumeration REACH default (the backfill_max_hops knob):
	// how deep into the gate graph the sweep hunts charted-but-unscanned shipyards. A CHARTED
	// shipyard is BY DEFINITION already in the gate graph and relay-reachable (the scout relay
	// crosses many hops), so the default is a large FULL-GRAPH horizon — deliberately NOT the
	// shallow frontier-expansion bound (~3) nor the expendable-probe reposition horizon (12)
	// that left the ~39 deeper in-graph charted yards invisible to the sweep (sp-b8lf: 43
	// in-graph unscanned, only ~18 within the old bound). It is BOUNDED (never unbounded) so it
	// can never runaway — the BFS terminates at the finite charted graph long before this cap —
	// and live-tunable DOWN to cap the per-cycle enumeration cost. Live-tunable
	// (ShipyardBackfillTunableDefaults); resolved live > launch > default per tick.
	backfillDefaultMaxHops = 1000

	// backfillFreshnessTarget is the sweep-once post's freshness target — irrelevant to
	// a one-pass sweep (it retires after the sweep) but required by the post shape;
	// mirrors the frontier coordinator's declared-post default.
	backfillFreshnessTarget = 60 * time.Minute
)

// ChartedShipyardSystem is one system whose swept waypoints reveal a SHIPYARD trait —
// a known-shipyard system that may or may not have been shipyard-scanned yet. Hops is its
// gate-hop depth from the fleet's anchor set, the deeper-first ordering key.
type ChartedShipyardSystem struct {
	SystemSymbol     string
	ShipyardWaypoint string
	Hops             int
}

// ChartedShipyardEnumerator lists every system known (from swept waypoint traits) to hold
// a shipyard — the CHARTED-shipyard set. Read-only driven port; the production adapter
// reads the waypoints table (traits carry SHIPYARD) and annotates each with its gate depth.
//
// maxHops is the enumeration REACH supplied per call by the coordinator (the live-tunable
// backfill_max_hops knob), NOT baked into the adapter: a CHARTED shipyard is by definition
// in the gate graph and relay-reachable, so the coordinator passes a large full-graph horizon
// and a deep in-graph charted yard is enumerated rather than dropped as "unreachable" merely
// for sitting past a shallow bound (sp-b8lf).
type ChartedShipyardEnumerator interface {
	ChartedShipyardSystems(ctx context.Context, playerID int, maxHops int) ([]ChartedShipyardSystem, error)
}

// ScannedShipyardReader lists the systems already present in the shipyard-inventory store —
// the SCANNED set the backfill excludes. Read-only driven port; the production adapter reads
// distinct system_symbol from shipyard_inventory (era-scoped).
type ScannedShipyardReader interface {
	ScannedSystems(ctx context.Context, playerID int) ([]string, error)
}

// IdleProbeCounter reports how many idle, relay-able scout hulls the reconciler could man a
// declared backfill post with — the supply bound. Read-only driven port; the production
// adapter counts idle scout-type ships.
type IdleProbeCounter interface {
	IdleProbeCount(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// RunShipyardBackfillCoordinatorCommand launches the standing backfill sweep for a player.
// Like the sibling coordinators it runs an infinite reconcile loop inside one Handle() call.
type RunShipyardBackfillCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int

	// MaxDispatchesPerCycle is the per-cycle declaration cap (rate limit). <= 0 → default.
	MaxDispatchesPerCycle int

	// MaxHops is the enumeration REACH (the backfill_max_hops knob): how deep into the gate
	// graph the sweep looks for charted-but-unscanned shipyards. <= 0 → default (full gate
	// graph). A charted shipyard is by definition in-graph + relay-reachable, so the default
	// reaches the whole graph, NOT the shallow frontier-expansion horizon (sp-b8lf).
	MaxHops int
}

// RunShipyardBackfillCoordinatorResponse reports reconcile progress (observed only on
// shutdown, since the loop is infinite).
type RunShipyardBackfillCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunShipyardBackfillCoordinatorHandler reconciles the charted-but-unscanned shipyard set
// against idle probe supply each tick. Registered singleton: it holds no per-player mutable
// state, deriving every decision fresh from the injected reads (RULINGS #2).
type RunShipyardBackfillCoordinatorHandler struct {
	enumerator ChartedShipyardEnumerator
	scanned    ScannedShipyardReader
	probes     IdleProbeCounter
	postRepo   domainScouting.ScoutPostRepository
	clock      shared.Clock

	// liveConfig snapshots the container's own persisted config at each tick start so a
	// `tune` of the rate knob lands on the next tick with no restart. Optional-injection:
	// nil keeps the launch-frozen value.
	liveConfig liveconfig.Reader
}

// NewRunShipyardBackfillCoordinatorHandler wires the coordinator. A nil clock defaults to
// the real clock (production).
func NewRunShipyardBackfillCoordinatorHandler(
	enumerator ChartedShipyardEnumerator,
	scanned ScannedShipyardReader,
	probes IdleProbeCounter,
	postRepo domainScouting.ScoutPostRepository,
	clock shared.Clock,
) *RunShipyardBackfillCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunShipyardBackfillCoordinatorHandler{
		enumerator: enumerator,
		scanned:    scanned,
		probes:     probes,
		postRepo:   postRepo,
		clock:      clock,
	}
}

// SetLiveConfigReader wires the per-tick live-config snapshot source, making the rate knob
// honor `tune` on the next tick. Leaving it unset keeps the knob launch-frozen.
func (h *RunShipyardBackfillCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// ShipyardBackfillTunableDefaults maps every LIVE-tunable backfill knob to its documented
// default — the daemon's tune bounds registry reads this map, so the defaults-of-record stay
// next to the consts they mirror (the Sizer/Frontier/ScoutPost idiom).
func ShipyardBackfillTunableDefaults() map[string]int {
	return map[string]int{
		"max_dispatches_per_cycle": backfillDefaultMaxDispatchesPerCycle,
		"backfill_max_hops":        backfillDefaultMaxHops,
	}
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunShipyardBackfillCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunShipyardBackfillCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = backfillDefaultTickSeconds * time.Second
	}

	result := &RunShipyardBackfillCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Shipyard backfill coordinator starting (tick %s)", tick), map[string]interface{}{
		"action":       "shipyard_backfill_start",
		"container_id": cmd.ContainerID,
	})

	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		err := h.ReconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Shipyard backfill reconcile failed: %v", err), nil)
		}
		if _, crossed := errMon.Note("reconcile", errString(err)); crossed {
			logger.Log("ERROR", "Shipyard backfill reconcile stuck in an error loop", map[string]interface{}{
				"action":       "shipyard_backfill_error_loop",
				"container_id": cmd.ContainerID,
			})
		}
		result.Ticks++

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// ReconcileOnce is one sweep pass — the unit the tests drive directly. It ENUMERATES the
// charted-shipyard systems, EXCLUDES the already-scanned ones (the blind-spot residue) and
// the already-posted ones (in-flight work), orders the remainder DEEPER-FIRST, and declares
// up to min(rate, idle-probe supply) single-hull sweep-once posts. It is idempotent: a
// restart re-derives everything from persisted state, and a system already scanned or posted
// is never (re-)declared.
func (h *RunShipyardBackfillCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunShipyardBackfillCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()

	charted, err := h.enumerator.ChartedShipyardSystems(ctx, playerID, h.resolveMaxHops(ctx, cmd))
	if err != nil {
		return fmt.Errorf("failed to enumerate charted shipyards: %w", err)
	}

	scannedSet, err := h.scannedSet(ctx, playerID)
	if err != nil {
		return err
	}
	postedSet, err := h.postedSet(ctx, playerID)
	if err != nil {
		return err
	}

	targets := h.backfillTargets(charted, scannedSet, postedSet)

	dispatchable, err := h.dispatchBudget(ctx, cmd)
	if err != nil {
		return err
	}

	declared := 0
	for _, target := range targets {
		if declared >= dispatchable {
			break
		}
		if err := h.declareSweepOncePost(ctx, cmd, target); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Shipyard backfill failed to declare sweep-once post %s: %v", target.SystemSymbol, err), nil)
			continue
		}
		declared++
		logger.Log("INFO", fmt.Sprintf("Shipyard backfill dispatched sweep-once scan of %s at %s (%d hops deep)", target.SystemSymbol, target.ShipyardWaypoint, target.Hops), map[string]interface{}{
			"action":        "shipyard_backfill_dispatch",
			"system_symbol": target.SystemSymbol,
			"waypoint":      target.ShipyardWaypoint,
			"hops":          target.Hops,
		})
	}

	// Observability: the catch-up progress the operator watches close (N dispatched, M
	// still-blind this cycle). Once remaining hits 0 the blind spot is drained and the
	// coordinator is a silent no-op.
	remaining := len(targets) - declared
	logger.Log("INFO", fmt.Sprintf("Shipyard backfill cycle: %d charted shipyards, %d unscanned+uncovered, dispatched %d, %d remaining", len(charted), len(targets), declared, remaining), map[string]interface{}{
		"action":       "shipyard_backfill_cycle",
		"charted":      len(charted),
		"blind":        len(targets),
		"dispatched":   declared,
		"remaining":    remaining,
		"container_id": cmd.ContainerID,
	})
	return nil
}

// scannedSet reads the already-scanned system set (the exclusion the blind-spot residue is
// defined against).
func (h *RunShipyardBackfillCoordinatorHandler) scannedSet(ctx context.Context, playerID int) (map[string]bool, error) {
	scanned, err := h.scanned.ScannedSystems(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read scanned shipyard systems: %w", err)
	}
	set := make(map[string]bool, len(scanned))
	for _, s := range scanned {
		set[s] = true
	}
	return set, nil
}

// postedSet reads the systems already covered by a live post — in-flight work the sweep
// skips so the bounded budget advances to NEW blind-spot systems each tick.
func (h *RunShipyardBackfillCoordinatorHandler) postedSet(ctx context.Context, playerID int) (map[string]bool, error) {
	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list active scout posts: %w", err)
	}
	set := make(map[string]bool, len(posts))
	for _, post := range posts {
		set[post.SystemSymbol] = true
	}
	return set, nil
}

// backfillTargets is the charted-shipyard set minus the scanned residue and the in-flight
// posts, ordered DEEPER-FIRST (more hops = further out = likelier to hold a heavy/bulk yard),
// with a deterministic system-symbol tiebreak so the bounded head is stable across ticks.
// The scanned exclusion here is the mutation point: removing it re-sweeps every already-known
// yard every cycle.
func (h *RunShipyardBackfillCoordinatorHandler) backfillTargets(charted []ChartedShipyardSystem, scannedSet, postedSet map[string]bool) []ChartedShipyardSystem {
	targets := make([]ChartedShipyardSystem, 0, len(charted))
	for _, system := range charted {
		if scannedSet[system.SystemSymbol] {
			continue
		}
		if postedSet[system.SystemSymbol] {
			continue
		}
		targets = append(targets, system)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Hops != targets[j].Hops {
			return targets[i].Hops > targets[j].Hops
		}
		return targets[i].SystemSymbol < targets[j].SystemSymbol
	})
	return targets
}

// dispatchBudget is this cycle's declaration allowance: min(rate knob, idle probe supply).
// The rate knob keeps the sweep from flooding the reconciler; the idle bound keeps it from
// declaring more posts than there are hulls to man, so it never starves freshness/depth.
func (h *RunShipyardBackfillCoordinatorHandler) dispatchBudget(ctx context.Context, cmd *RunShipyardBackfillCoordinatorCommand) (int, error) {
	rate := h.resolveMaxDispatches(ctx, cmd)
	idle, err := h.probes.IdleProbeCount(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to count idle probes: %w", err)
	}
	if idle < rate {
		return idle, nil
	}
	return rate, nil
}

// declareSweepOncePost writes a single-hull sweep-once post for the target system through the
// SAME repository seam the frontier coordinator and `scout posts add` use, keyed by (player,
// system) so a re-declare is idempotent. The reconciler relays an idle probe to it; the
// probe's arrival rides the decoupled shipyard scan (Part 1).
func (h *RunShipyardBackfillCoordinatorHandler) declareSweepOncePost(ctx context.Context, cmd *RunShipyardBackfillCoordinatorCommand, target ChartedShipyardSystem) error {
	return h.postRepo.Upsert(ctx, &domainScouting.ScoutPost{
		PlayerID:        cmd.PlayerID.Value(),
		SystemSymbol:    target.SystemSymbol,
		FreshnessTarget: backfillFreshnessTarget,
		Kind:            domainScouting.PostKindSweepOnce,
		Hulls:           1,
		CreatedAt:       h.clock.Now(),
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resolveMaxDispatches resolves the effective per-cycle cap from the tick's live-config
// snapshot (authoritative when present) or the launch command, falling back to the default.
func (h *RunShipyardBackfillCoordinatorHandler) resolveMaxDispatches(ctx context.Context, cmd *RunShipyardBackfillCoordinatorCommand) int {
	value := cmd.MaxDispatchesPerCycle
	if h.liveConfig != nil {
		if snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value()); err == nil && snap != nil {
			value = snap.PositiveIntOrZero("max_dispatches_per_cycle")
		}
	}
	if value <= 0 {
		value = backfillDefaultMaxDispatchesPerCycle
	}
	return value
}

// resolveMaxHops resolves the effective enumeration REACH from the tick's live-config snapshot
// (authoritative when present) or the launch command, falling back to the full-graph default.
// Mirrors resolveMaxDispatches: the launch value and the live column are the SAME store (the
// persisted config the tune verb writes and buildShipyardBackfillCoordinatorCommand reads), so
// a live reader subsumes the launch value; the launch tier applies only when no live reader is
// wired. A charted shipyard is in-graph + relay-reachable, so an unset knob resolves to the full
// gate graph — never the shallow frontier bound that left the deep charted yards invisible.
func (h *RunShipyardBackfillCoordinatorHandler) resolveMaxHops(ctx context.Context, cmd *RunShipyardBackfillCoordinatorCommand) int {
	value := cmd.MaxHops
	if h.liveConfig != nil {
		if snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value()); err == nil && snap != nil {
			value = snap.PositiveIntOrZero("backfill_max_hops")
		}
	}
	if value <= 0 {
		value = backfillDefaultMaxHops
	}
	return value
}
