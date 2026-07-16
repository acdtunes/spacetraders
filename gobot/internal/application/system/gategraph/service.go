// Package gategraph resolves multi-jump routes over the persisted cross-system
// jump-gate adjacency (sp-7gr2). It is the fix for the single-edge assumption
// that crashed a laden frigate at the home gate: travel() assumed origin→dest
// was ONE jump, but JP61 is three jumps from KA42 (PA3→UQ16→JP61). The service
// caches the API's own gate topology in a GateEdgeRepository, refreshes it
// lazily on miss/staleness, and walks it with a bounded BFS to produce the
// ordered hop path travel() executes and the routability check the pre-buy
// guard runs BEFORE spending.
package gategraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateAPI is the narrow slice of the SpaceTraders API the gate graph needs: the
// live gate-connections fetch, plus the per-gate construction probe that learns
// whether a connected gate is still being built (sp-8qhu). Narrowing the
// dependency (vs. the full ports.APIClient) states exactly what this service
// touches and keeps the fetch-through path unit-testable with a tiny fake.
type gateAPI interface {
	GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.JumpGateData, error)
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.WaypointDetail, error)
	// CreateChart PUBLICLY charts the ship's current waypoint (sp-lv2n). Charting a gate once
	// makes every future GetJumpGate on it succeed WITHOUT a ship present, so an uncharted
	// frontier gate stops being live-re-read (and 400ing) on each jump-out. Best-effort: the
	// ChartPresentGate caller swallows an already-charted (4230) or any other failure.
	CreateChart(ctx context.Context, shipSymbol, token string) error
}

// MaxJumpPath bounds how many jumps a strict (fetch-through) Path route may contain —
// the reach heavies/trade/arb are held to, capping the BFS against a pathological fetch
// storm over the uncharted frontier. Five was chosen when the charted cluster was a
// handful of systems wide; the frontier has since expanded and the deepest CHARTED routes
// now run 6–12 jumps (measured KN67→SN21=6, →C81=9, sp-8k9m), so a laden hull can no
// longer assume everything is within five. The EXPENDABLE probe/scout reposition class
// reaches those deeper posts via RepositionPath (its own [scouting] max_reposition_jumps
// bound over the stored adjacency); this strict cap stays 5 deliberately, because a fetch-
// through BFS deeper than this over unreadable frontier gates is exactly the storm it guards.
const MaxJumpPath = 5

// ErrUnroutable wraps every "no path exists within the bound" outcome so callers
// can distinguish a DEFINITIVE unroutable verdict (refuse the buy cleanly) from a
// store/fetch FAILURE (fail closed for a different reason). Both refuse a spend;
// only the latter is an operational error.
var ErrUnroutable = errors.New("no jump-gate route")

// ErrGateUnreadable marks a live gate-connections fetch that failed for ONE system
// — e.g. a frontier gate the API refuses with 400 "not accessible, no ship present"
// (sp-qxa4). It is deliberately distinct from a store/DB failure: the BFS treats an
// unreadable system as a DEAD-END and continues over the readable subgraph (one
// unreadable frontier gate must never abort an unrelated route), whereas a store
// error still fails the whole search closed. The system is left UNPERSISTED so the
// next fetch re-probes it. Fail-closed is preserved: an unreadable node is never
// routed THROUGH (its onward gates are unverified), so a route that genuinely
// requires it ends ErrUnroutable.
var ErrGateUnreadable = errors.New("jump-gate connections unreadable")

// BackoffSchedule is the exponential re-probe schedule for an unreadable gate
// (sp-ikx1): the nth consecutive failure waits Initial * Multiplier^(n-1), capped at
// Max. Injected from config (RULINGS #5); DefaultBackoffSchedule is the ruled fallback.
type BackoffSchedule struct {
	Initial    time.Duration
	Multiplier float64
	Max        time.Duration
}

// DefaultBackoffSchedule is the Admiral-ruled default: 5m → 30m → 2h (5m, 5m×6=30m,
// 30m×6=180m capped to 2h, then 2h). Config overrides it in production; this is what
// a Service built without WithBackoff uses so callers/tests need no wiring.
var DefaultBackoffSchedule = BackoffSchedule{Initial: 5 * time.Minute, Multiplier: 6, Max: 2 * time.Hour}

// durationFor returns the backoff window after the attempts'th consecutive failed probe
// (attempts is 1-based: 1 after the first failure). It multiplies up from Initial and
// caps at Max, breaking early on the cap so a large attempt count cannot overflow.
func (b BackoffSchedule) durationFor(attempts int) time.Duration {
	if attempts <= 1 {
		return b.Initial
	}
	d := b.Initial
	for i := 1; i < attempts; i++ {
		d = time.Duration(float64(d) * b.Multiplier)
		if d >= b.Max {
			return b.Max
		}
	}
	return d
}

// Service resolves and caches gate routes. Its dependencies mirror
// GetJumpGateConnectionsHandler's (apiClient for the live gate fetch, graphProvider
// to find a charted system's own gate, playerRepo for the token) plus the edge
// store that makes the topology persistent and multi-hop-walkable. clock and backoff
// drive the negative-result re-probe schedule for unreadable gates (sp-ikx1).
type Service struct {
	store         system.GateEdgeRepository
	apiClient     gateAPI
	graphProvider system.ISystemGraphProvider
	playerRepo    player.PlayerRepository
	clock         shared.Clock
	backoff       BackoffSchedule
}

// Option customizes a Service at construction (functional options keep the 4-arg
// constructor stable for the many existing call sites while letting the daemon inject
// the configured backoff schedule and, in tests, a controllable clock).
type Option func(*Service)

// WithBackoff sets the unreadable-gate re-probe schedule (sp-ikx1), wired from config.
func WithBackoff(b BackoffSchedule) Option {
	return func(s *Service) { s.backoff = b }
}

// WithClock injects the clock the backoff windows are measured against — the real clock
// in production, a MockClock in tests that need to advance past a backoff window.
func WithClock(c shared.Clock) Option {
	return func(s *Service) { s.clock = c }
}

// NewService wires the gate-graph service. Without options it uses the real clock and
// DefaultBackoffSchedule; the daemon passes WithBackoff(config) and tests pass WithClock.
func NewService(
	store system.GateEdgeRepository,
	apiClient gateAPI,
	graphProvider system.ISystemGraphProvider,
	playerRepo player.PlayerRepository,
	opts ...Option,
) *Service {
	s := &Service{
		store:         store,
		apiClient:     apiClient,
		graphProvider: graphProvider,
		playerRepo:    playerRepo,
		clock:         shared.NewRealClock(),
		backoff:       DefaultBackoffSchedule,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Connections returns systemSymbol's directly-reachable neighbor edges,
// fetch-through: a fresh cache hit is returned as-is; a miss or a stale set
// triggers a single live GetJumpGate fetch which is then persisted (replacing the
// system's old set) and returned. Errors are surfaced, never swallowed — a
// routability guard that cannot read the graph must fail closed, not fail open.
func (s *Service) Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error) {
	edges, ok, err := s.store.Edges(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if ok {
		return edges, nil
	}
	// A miss/stale set would normally re-fetch. But an UNREADABLE gate (one whose live
	// fetch keeps 400ing) is a persisted miss FOREVER — re-fetching it every reconcile
	// tick is the sp-ikx1 storm (1 req/s of guaranteed 400s). Honor the negative-result
	// backoff first: if the system is backed off and its next-probe time has not arrived,
	// skip the API call silently and report it unreadable, exactly as a real 400 would —
	// the BFS excludes the node either way. The backoff is persisted, so this holds across
	// a daemon restart instead of re-storming on boot (RULINGS #2).
	if attempts, lastProbe, backedOff, err := s.store.UnreadableState(ctx, systemSymbol); err != nil {
		return nil, err
	} else if backedOff {
		nextProbe := lastProbe.Add(s.backoff.durationFor(attempts))
		if s.clock.Now().Before(nextProbe) {
			return nil, fmt.Errorf("%w for %s (backing off, next probe %s)", ErrGateUnreadable, systemSymbol, nextProbe.Format(time.RFC3339))
		}
	}
	return s.fetchAndStore(ctx, systemSymbol, playerID)
}

// ChartPresentGate is the PRESENCE-FORCED gate read (sp-bcsu): a hull physically
// standing on systemSymbol's own jump gate is the ONE moment its outbound connections
// are readable (a remote read with no ship present 400s, code 4001). It deliberately
// BYPASSES the sp-ikx1 negative-result backoff short-circuit that Connections honors —
// a plain Connections would skip an already-latched system even with a ship on its gate,
// the exact catch-22 that leaves a frontier gate uncharted forever. On a now-succeeding
// present read, fetchAndStore -> store.Replace deletes every row for the system INCLUDING
// the backoff marker (self-heal, gate_edge_repository.go), so the latch clears itself. It
// stays honest at the two boundaries that matter:
//   - GUARD 1 (idempotent): an already-charted system (Edges is a fresh, non-empty hit)
//     early-returns with ZERO API — an arrival on a known system costs one store read.
//   - GUARD 2 (negative cache intact): a present read that STILL fails re-enters the
//     backoff unchanged via fetchAndStore's enterBackoff, so this never defeats sp-4bm3;
//     only genuine ship-present success heals the latch.
//
// On that same store-miss (uncharted-to-us) branch it ALSO PUBLICLY charts the gate from the
// present hull (sp-lv2n): reading the gate stores OUR edge copy but leaves the gate uncharted-
// public, so every future jump-OUT re-reads it live (GetJumpGate) and 400s whenever no hull is
// on the gate. CreateChart makes the gate GetJumpGate-readable forever without a ship present,
// collapsing that re-read storm. Charting is best-effort and idempotent-by-GUARD-1 (a later
// arrival on a now-charted system store-hits and never re-charts) — see chartPresentWaypoint.
//
// It is best-effort from the caller's side: charting must never fail a trade/nav leg, so
// callers (travelWithJumpBound.chartArrivedGate, the sp-bcsu reconcile sweep) log and
// swallow the error. The error is surfaced here so those callers can log the cause.
func (s *Service) ChartPresentGate(ctx context.Context, systemSymbol, shipSymbol string, playerID int) ([]system.GateEdge, error) {
	if edges, ok, err := s.store.Edges(ctx, systemSymbol); err != nil {
		return nil, err
	} else if ok && len(edges) > 0 {
		return edges, nil
	}
	// Store MISS ⇒ this gate is UNCHARTED-to-us. PUBLICLY chart it from the present hull BEFORE
	// the edge read, so the durable public chart (the sp-lv2n win) is not contingent on the read
	// succeeding. Best-effort and gated to THIS present-ship branch only — the remote fetch-
	// through path (Connections) has no hull on the gate and must never attempt a chart. GUARD 1
	// above is the idempotence key: a later arrival on a now-charted-by-us system returns there
	// and never re-charts, so each gate is charted at most once (no wasted call, no error-spam).
	s.chartPresentWaypoint(ctx, shipSymbol, playerID)
	return s.fetchAndStore(ctx, systemSymbol, playerID)
}

// chartPresentWaypoint best-effort PUBLICLY charts the waypoint the present hull is standing on
// (POST /my/ships/{ship}/chart, sp-lv2n) so the gate becomes GetJumpGate-readable forever without
// a ship present. NON-FATAL by contract (mirrors travelWithJumpBound.chartArrivedGate): every
// failure — an already-charted gate (4230, the benign no-op when another agent already charted
// it, or a race) OR any other error — is swallowed and can NEVER fail the present-gate read or
// the trade/nav leg that drove it. Only a genuine (non-benign) failure is logged, so an
// already-charted gate produces no error-spam. Called ONLY from ChartPresentGate's store-miss
// branch (an uncharted-to-us gate a present hull can chart); an empty ship symbol (no present
// hull to chart with) is skipped rather than posting a malformed /my/ships//chart.
func (s *Service) chartPresentWaypoint(ctx context.Context, shipSymbol string, playerID int) {
	if shipSymbol == "" {
		return
	}
	logger := logging.LoggerFromContext(ctx)
	token, err := s.token(ctx, playerID)
	if err != nil {
		logger.Log("INFO", fmt.Sprintf("chart-present: token unresolved, cannot public-chart from %s (non-fatal): %v", shipSymbol, err), map[string]interface{}{
			"action": "gate_public_chart_skipped",
			"ship":   shipSymbol,
			"error":  err.Error(),
		})
		return
	}
	if err := s.apiClient.CreateChart(ctx, shipSymbol, token); err != nil {
		if isAlreadyCharted(err) {
			return // benign: the gate is already publicly charted — nothing to do, nothing to log
		}
		logger.Log("INFO", fmt.Sprintf("chart-present: CreateChart from %s failed (non-fatal): %v", shipSymbol, err), map[string]interface{}{
			"action": "gate_public_chart_failed",
			"ship":   shipSymbol,
			"error":  err.Error(),
		})
	}
}

// isAlreadyCharted reports whether a CreateChart failure is the API's benign "waypoint already
// charted" verdict (HTTP 400, code 4230): the gate is ALREADY publicly charted (another agent
// beat us to it, or a race), so there is nothing to do and nothing to log. Matching the code and
// message substrings mirrors the jump_ship.go classifiers (isNotInOrbitError et al.) and is
// robust to the *APIError being %w-wrapped by the adapter's CreateChart. Every OTHER failure is
// a genuine (still non-fatal) chart failure the caller logs.
func isAlreadyCharted(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4230") || strings.Contains(msg, "already charted")
}

// fetchAndStore resolves systemSymbol's own gate, fetches its live connections,
// persists the fresh edge set, and returns it. The gate is resolved from the
// store first (a neighbor may have recorded it — this is the path that expands an
// UNCHARTED system), falling back to the charted system's graph.
func (s *Service) fetchAndStore(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error) {
	token, err := s.token(ctx, playerID)
	if err != nil {
		return nil, err
	}

	gateWaypoint, ok, err := s.store.GateWaypointOf(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if !ok {
		gateWaypoint, err = s.gateFromGraph(ctx, systemSymbol, playerID)
		if err != nil {
			return nil, err
		}
	}

	gateData, err := s.apiClient.GetJumpGate(ctx, systemSymbol, gateWaypoint, token)
	if err != nil {
		// A per-system fetch failure (a frontier gate the API refuses, 400 "no ship
		// present") is NOT a whole-route failure: tag it ErrGateUnreadable so the BFS
		// excludes just this node and continues (sp-qxa4).
		//
		// Only a PERMANENT client error (a terminal 4xx — the API's verdict that this
		// waypoint has no readable gate: uncharted / no ship present / not a gate)
		// records/extends the persisted negative-result backoff, so a doomed gate is not
		// re-probed every tick (sp-ikx1) — the enter/extend INFO line is logged there, once
		// per transition. A TRANSIENT failure (5xx / network / retry-exhausted, which never
		// surfaces as a *ports.APIError) must NOT poison the cache: leaving it un-backed-off
		// lets the next miss re-probe it, so a momentary API blip never suppresses a real
		// gate for the whole 5m→30m→2h window (sp-4bm3).
		if isPermanentGateAbsence(err) {
			s.enterBackoff(ctx, systemSymbol, gateWaypoint, err)
		}
		return nil, fmt.Errorf("%w for %s (%s): %v", ErrGateUnreadable, systemSymbol, gateWaypoint, err)
	}

	logger := logging.LoggerFromContext(ctx)
	seen := make(map[string]bool, len(gateData.Connections))
	edges := make([]system.GateEdge, 0, len(gateData.Connections))
	for _, connWaypoint := range gateData.Connections {
		connSystem := shared.ExtractSystemSymbol(connWaypoint)
		if connSystem == systemSymbol || seen[connSystem] {
			continue
		}
		seen[connSystem] = true
		edges = append(edges, system.GateEdge{
			ConnectedSystem:   connSystem,
			GateWaypoint:      connWaypoint,
			UnderConstruction: s.gateUnderConstruction(ctx, connSystem, connWaypoint, token, logger),
		})
	}

	if err := s.store.Replace(ctx, systemSymbol, edges); err != nil {
		return nil, err
	}
	return edges, nil
}

// isPermanentGateAbsence reports whether a GetJumpGate failure is the API's PERMANENT verdict
// that this waypoint has no readable gate — a terminal 4xx (uncharted / no ship present / not a
// gate). Only such a permanent failure is negative-cached (sp-4bm3): a TRANSIENT failure (5xx /
// network / retry-exhausted) never surfaces as a *ports.APIError, so it declines the cache and is
// re-probed on the next miss instead of being suppressed for the whole backoff window. Matching a
// typed status (not the error string) keeps the classification robust against message wording.
func isPermanentGateAbsence(err error) bool {
	var apiErr *ports.APIError
	return errors.As(err, &apiErr) && apiErr.IsClientError()
}

// enterBackoff persists (or extends) the negative-result backoff for an unreadable gate
// and logs the ONE INFO line the operator sees — carrying the attempt count and the
// computed next-probe time (sp-ikx1). It fires only when a live probe actually failed
// (once per backoff transition, at 5m/30m/2h boundaries), so the log is a handful of
// lines per gate per day instead of the ~2,880 the old per-tick "will re-probe next
// fetch" line produced. A persistence failure is logged and swallowed: the gate is still
// excluded from the build, and the worst case degrades to the pre-fix behavior (re-probe
// next tick), never a routing error.
func (s *Service) enterBackoff(ctx context.Context, systemSymbol, gateWaypoint string, cause error) {
	logger := logging.LoggerFromContext(ctx)
	attempts, err := s.store.MarkUnreadable(ctx, systemSymbol, gateWaypoint, s.clock.Now())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("gate %s unreadable but backoff could not be persisted — will re-probe next fetch", systemSymbol), map[string]interface{}{
			"action": "gate_graph_backoff_persist_failed",
			"system": systemSymbol,
			"error":  err.Error(),
		})
		return
	}
	nextProbe := s.clock.Now().Add(s.backoff.durationFor(attempts))
	logger.Log("INFO", fmt.Sprintf("gate %s unreadable — backing off (attempt %d), next probe %s", systemSymbol, attempts, nextProbe.Format(time.RFC3339)), map[string]interface{}{
		"action":        "gate_graph_unreadable_backoff",
		"system":        systemSymbol,
		"attempt":       attempts,
		"next_probe_at": nextProbe.Format(time.RFC3339),
		"cause":         cause.Error(),
	})
}

// gateUnderConstruction resolves a connected gate's build state with a per-gate
// waypoint read (the API's jump-gate connections list carries symbols only). It
// FAILS CLOSED: any read failure is treated as under-construction so an
// unknown-state gate is never routed through (sp-8qhu — routing into an unbuilt
// gate crashes a laden hull at the hop). The cause is logged verbatim so the
// harbormaster can see why an edge went dark. The underlying API client applies
// its own rate limiting, so the one-GET-per-edge refresh needs no extra throttle.
func (s *Service) gateUnderConstruction(ctx context.Context, connSystem, gateWaypoint, token string, logger logging.ContainerLogger) bool {
	detail, err := s.apiClient.GetWaypoint(ctx, connSystem, gateWaypoint, token)
	if err != nil {
		logger.Log("WARNING", "gate construction probe failed — treating edge as under construction (fail closed)", map[string]interface{}{
			"system": connSystem,
			"gate":   gateWaypoint,
			"error":  err.Error(),
		})
		return true
	}
	return detail.IsUnderConstruction
}

// gateFromGraph finds systemSymbol's own jump-gate waypoint via its system graph
// — the charted-system path (mirrors GetJumpGateConnectionsHandler). Only used
// when no stored neighbor edge has already recorded the gate.
func (s *Service) gateFromGraph(ctx context.Context, systemSymbol string, playerID int) (string, error) {
	graphResult, err := s.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", fmt.Errorf("failed to get system graph for %s: %w", systemSymbol, err)
	}
	for _, waypoint := range graphResult.Graph.Waypoints {
		if waypoint.IsJumpGate() {
			return waypoint.Symbol, nil
		}
	}
	return "", fmt.Errorf("no jump gate found in system %s", systemSymbol)
}

// token loads the player's API token for the live gate fetch.
func (s *Service) token(ctx context.Context, playerID int) (string, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", fmt.Errorf("invalid player id %d: %w", playerID, err)
	}
	playerEntity, err := s.playerRepo.FindByID(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("failed to load player %d: %w", playerID, err)
	}
	return playerEntity.Token, nil
}

// Path returns the ordered system hop path from fromSystem to toSystem inclusive
// (a single element when they are equal; ≥2 for a real cross-system route),
// resolved by a bounded BFS over the fetch-through adjacency. A single unreadable
// gate (ErrGateUnreadable — a frontier gate the API refuses) is excluded from the
// build and the search continues on the readable subgraph (sp-qxa4); it is never
// routed through. Path returns an ErrUnroutable-wrapped error naming both systems
// when no route exists within MaxJumpPath (including when the only route required an
// excluded gate), or an underlying store/token error otherwise (fail closed).
func (s *Service) Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error) {
	return bfsPath(fromSystem, toSystem, MaxJumpPath, func(systemSymbol string) ([]string, error) {
		edges, err := s.Connections(ctx, systemSymbol, playerID)
		if err != nil {
			// One system's gate is unreadable (sp-qxa4 — a frontier gate the API
			// refuses, "no ship present"). Exclude it as a routing-THROUGH node —
			// fail-closed, since its onward gates are unverified — but CONTINUE the
			// search over the readable subgraph instead of aborting the whole build.
			// A route that genuinely REQUIRES this node then ends ErrUnroutable (an
			// honest no-path), never silently rerouted through the unverified gate.
			// Any OTHER error (store/DB, token) still fails the whole search closed.
			// The exclusion is SILENT here (sp-ikx1): logging it every BFS traversal is
			// the 23k-line spam this fix removes — the operator signal is the single
			// enter/extend line enterBackoff emits when a probe actually fails.
			if errors.Is(err, ErrGateUnreadable) {
				return nil, nil
			}
			return nil, err
		}
		neighbors := make([]string, 0, len(edges))
		for _, e := range edges {
			// Never traverse INTO an under-construction gate: a jump to an unbuilt
			// gate fails at hop time (sp-8qhu — the BFS picked KA42→AF2(unbuilt)→…
			// over the equal-hop valid PA3 route and the laden frigate crashed at
			// hop 1). Filtering the forward targets is what keeps the search on a
			// fully-built route; if that makes the dest unreachable, the caller gets
			// ErrUnroutable and the pre-buy guard refuses the spend.
			if e.UnderConstruction {
				continue
			}
			neighbors = append(neighbors, e.ConnectedSystem)
		}
		return neighbors, nil
	})
}

// RepositionPath resolves the ordered system hop path from fromSystem to toSystem over
// the PERSISTED, era-scoped gate adjacency (a pure store read, NO fetch-through), bounded
// to maxJumps. It exists for the EXPENDABLE probe/scout reposition class ONLY (sp-8k9m) —
// heavies/trade/arb keep strict Path — and differs from Path in exactly two deliberate
// ways, both justified by that class:
//
//   - It routes PAST an unreadable frontier gate instead of dead-ending on it. Path
//     fails closed on an unreadable gate because its onward gates are unverified (sp-qxa4);
//     but a probe's whole purpose is to REACH that frontier, and a frontier gate is
//     unreadable precisely because no probe has arrived to read it — the catch-22 a
//     fail-closed router can never re-admit. Routing over the stored adjacency (which
//     retains an unreadable gate's last-known edges) breaks it: the probe hops the known
//     topology, and the coordinator's chart-on-arrival re-reads each gate the hull lands
//     on (sp-bcsu — travelWithJumpBound.chartArrivedGate -> ChartPresentGate, a PRESENT-ship
//     read that self-heals the latch), so each successful reposition SHRINKS the unreadable
//     set. RepositionPath ITSELF does this WITHOUT any live probe — Adjacency is a store
//     read, so the sp-ikx1 negative-result backoff is fully honored here; we route PAST
//     unreadable gates over stored edges, and the present-ship arrival read (never a remote
//     re-probe) is what actually re-charts them.
//   - It takes a caller-supplied bound (the [scouting] max_reposition_jumps config,
//     default 12) rather than the shared MaxJumpPath=5, because the expanded frontier's
//     posts sit 6–12 gate-jumps from the probe supply.
//
// under_construction edges are STILL excluded (a jump into an unbuilt gate crashes at hop
// time, sp-8qhu — a hazard just as real for a probe). maxJumps <= 0 falls back to
// MaxJumpPath. A store read failure fails CLOSED (a real error, never a clean unroutable).
func (s *Service) RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error) {
	if maxJumps <= 0 {
		maxJumps = MaxJumpPath
	}
	adjacency, err := s.store.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("reposition path: failed to read stored gate adjacency: %w", err)
	}
	return bfsPath(fromSystem, toSystem, maxJumps, func(systemSymbol string) ([]string, error) {
		edges := adjacency[systemSymbol]
		neighbors := make([]string, 0, len(edges))
		for _, e := range edges {
			// Never route INTO an under-construction gate (sp-8qhu). Unlike Path, an
			// UNREADABLE gate is NOT a dead-end here — its stored edges stand, so the
			// probe hops past it (and re-reads it on arrival).
			if e.UnderConstruction {
				continue
			}
			neighbors = append(neighbors, e.ConnectedSystem)
		}
		return neighbors, nil
	})
}

// Routable reports whether a route from→to exists. A DEFINITIVE unroutable
// verdict is (false, nil) — the caller refuses the spend but this is not an
// operational error; a store/fetch failure surfaces as (false, err) so the
// caller fails closed. Same-system is trivially routable.
func (s *Service) Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error) {
	if fromSystem == toSystem {
		return true, nil
	}
	_, err := s.Path(ctx, fromSystem, toSystem, playerID)
	if errors.Is(err, ErrUnroutable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Adjacency returns the stored gate adjacency (era-scoped), for the `system
// gates` overview. Pure store read — no fetch-through.
func (s *Service) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return s.store.Adjacency(ctx)
}

// bfsPath is the pure breadth-first search over a neighbor function, extracted so
// the traversal is unit-testable against an in-memory adjacency with no store,
// API, or clock. It returns the shortest hop path (fewest jumps) from→to
// inclusive, bounded to maxJumps jumps. A path of J jumps has J+1 elements; a
// node is expanded only while it still has room for another jump. from==to is a
// zero-jump path. Neighbor-function errors abort the search immediately (fetch
// failures must not masquerade as unroutable).
func bfsPath(from, to string, maxJumps int, neighbors func(string) ([]string, error)) ([]string, error) {
	if from == to {
		return []string{from}, nil
	}

	visited := map[string]bool{from: true}
	queue := [][]string{{from}}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]

		// A path already at the jump bound cannot be extended without exceeding it.
		if len(path)-1 >= maxJumps {
			continue
		}

		last := path[len(path)-1]
		ns, err := neighbors(last)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if visited[n] {
				continue
			}
			next := make([]string, len(path), len(path)+1)
			copy(next, path)
			next = append(next, n)
			if n == to {
				return next, nil
			}
			visited[n] = true
			queue = append(queue, next)
		}
	}

	return nil, fmt.Errorf("%w from %s to %s within %d jumps", ErrUnroutable, from, to, maxJumps)
}
