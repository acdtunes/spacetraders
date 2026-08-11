// Package gategraph resolves multi-jump routes over the persisted cross-system
// jump-gate adjacency. It is the fix for the single-edge assumption
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

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateAPI is the narrow slice of the SpaceTraders API the gate graph needs: the
// live gate-connections fetch, plus the per-gate construction probe that learns
// whether a connected gate is still being built. Narrowing the
// dependency (vs. the full ports.APIClient) states exactly what this service
// touches and keeps the fetch-through path unit-testable with a tiny fake.
type gateAPI interface {
	GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.JumpGateData, error)
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.WaypointDetail, error)
	// CreateChart PUBLICLY charts the ship's current waypoint. Charting a gate once
	// makes every future GetJumpGate on it succeed WITHOUT a ship present, so an uncharted
	// frontier gate stops being live-re-read (and 400ing) on each jump-out. Best-effort: the
	// ChartPresentGate caller swallows an already-charted (4230) or any other failure.
	CreateChart(ctx context.Context, shipSymbol, token string) (*ports.ChartResult, error)
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

// longHaulPathfindBudget is the hard wall-clock ceiling on the long-haul reposition PATHFIND
// (PathWithinJumpsStoredThenVerify) — the stored-adjacency PLAN plus the chosen-path construction
// VERIFY, both of which complete BEFORE any jump. It is defense-in-depth atop the sp-0o9ub
// latency fix that already made the normal plan ~11s: no FUTURE slowness (e.g. an API rate-limit
// hanging the chosen-path construction probes) can ever silently stall a hull's planning step again.
// 90s is deliberately generous vs the ~11s normal case — it fires ONLY on a pathological stall, and
// maps that stall to ErrUnroutable so the worker's reachability fallback skips the lane. It bounds
// ONLY the pathfind; the multi-hop FLIGHT that follows (jumps + minutes-long cooldowns) is NOT
// bounded here and routinely exceeds this.
const longHaulPathfindBudget = 90 * time.Second

// maxChosenPathConstructionProbes caps how many LIVE construction probes ONE pathfind may spend
// re-verifying its chosen path, so that cost stays flat instead of tracking hop count — a probe
// per hop makes the 7–12-hop lanes the only ones that exhaust longHaulPathfindBudget, i.e. the
// engine vetoes exactly the reach it exists for. The allowance is spent on the EARLIEST hops whose
// stored build state is unverified: a plan-time read is closest to hop-time truth for the jump
// about to happen, and every later hop is another jump-plus-cooldown further out. Probed or not,
// every hop is still covered at hop time by the jump API's own refusal of an unbuilt destination
// gate (error 4262, jump_ship.go) — the authority this bound leans on.
const maxChosenPathConstructionProbes = 2

// unchartedTrait is the waypoint trait the SpaceTraders API stamps on an unswept waypoint.
// A JUMP_GATE that still carries it has no readable /jump-gate endpoint without a ship
// present — a no-ship GetJumpGate on it 400s — so it is the is-charted precondition the
// doomed-call fix reads off the system graph before deciding to make the live call.
const unchartedTrait = "UNCHARTED"

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
// drive the negative-result re-probe schedule for unreadable gates.
type Service struct {
	store         system.GateEdgeRepository
	apiClient     gateAPI
	graphProvider system.ISystemGraphProvider
	playerRepo    player.PlayerRepository
	clock         shared.Clock
	backoff       BackoffSchedule
	// pathfindBudget is the wall-clock ceiling the long-haul reposition pathfind
	// (PathWithinJumpsStoredThenVerify) is wrapped in — longHaulPathfindBudget in production, a
	// short duration in tests via WithPathfindBudget so a hung construction probe surfaces as
	// ErrUnroutable fast. Bounds ONLY the pathfind, never the flight.
	pathfindBudget time.Duration
	// skipUnchartedFetch is the doomed-call precondition: when true (default),
	// a remote, no-ship Connections read whose OWN gate is still UNCHARTED (per the system
	// graph we already hold) SKIPS the live GetJumpGate — that call is guaranteed to 400
	// ("uncharted, no ship present"), so issuing it is pure rate-limit waste. The gate is
	// entered into the backoff exactly as a real 400 would, so routing behaviour is
	// unchanged (the BFS excludes it either way); only the wasted 400 disappears. Set false
	// (WithSkipUnchartedFetch) to restore the previous probe-then-backoff behaviour byte-for-
	// byte — the staged-rollout escape hatch. The precondition applies ONLY to the graph-
	// resolved gate on the no-present-ship path: ChartPresentGate (a hull IS on the gate, so
	// it reads fine) always bypasses it, preserving the frontier self-heal.
	skipUnchartedFetch bool
	// mediator dispatches the charting reward to the ledger. Nil in the CLI, where there is
	// no ledger to write to; the recorder then logs and records nothing.
	mediator common.Mediator
}

// Option customizes a Service at construction (functional options keep the 4-arg
// constructor stable for the many existing call sites while letting the daemon inject
// the configured backoff schedule and, in tests, a controllable clock).
type Option func(*Service)

// WithBackoff sets the unreadable-gate re-probe schedule, wired from config.
func WithBackoff(b BackoffSchedule) Option {
	return func(s *Service) { s.backoff = b }
}

// WithClock injects the clock the backoff windows are measured against — the real clock
// in production, a MockClock in tests that need to advance past a backoff window.
func WithClock(c shared.Clock) Option {
	return func(s *Service) { s.clock = c }
}

// WithPathfindBudget overrides the long-haul reposition pathfind wall-clock ceiling
// (longHaulPathfindBudget, sp-if4lx). Production keeps the default; tests inject a short duration to
// exercise the deadline without a real 90s wait.
func WithPathfindBudget(d time.Duration) Option {
	return func(s *Service) { s.pathfindBudget = d }
}

// WithSkipUnchartedGateFetch toggles the doomed-call precondition. Default is
// ON (skip the live GetJumpGate on an UNCHARTED origin gate — it would only 400). Passing
// false restores the legacy probe-then-backoff behaviour, wired from config as the staged-
// rollout reversibility switch. See Service.skipUnchartedFetch.
func WithSkipUnchartedFetch(skip bool) Option {
	return func(s *Service) { s.skipUnchartedFetch = skip }
}

// WithLedgerMediator wires the dispatcher the charting reward is recorded through. Omitted,
// the service still charts — it just cannot write the reward to the ledger.
func WithLedgerMediator(m common.Mediator) Option {
	return func(s *Service) { s.mediator = m }
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
		store:              store,
		apiClient:          apiClient,
		graphProvider:      graphProvider,
		playerRepo:         playerRepo,
		clock:              shared.NewRealClock(),
		backoff:            DefaultBackoffSchedule,
		pathfindBudget:     longHaulPathfindBudget,
		skipUnchartedFetch: true, // doomed-call precondition ON by default
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Connections returns systemSymbol's directly-reachable neighbor edges,
// fetch-through: a fresh cache hit is returned as-is; a miss, a condemned set, or a set
// carrying ANY row past its own freshness window triggers a single live GetJumpGate fetch
// which is then persisted (replacing the system's old set) and returned. Errors are
// surfaced, never swallowed — a routability guard that cannot read the graph must fail
// closed, not fail open.
//
// THIS IS WHERE THE BUILD-COMPLETION RE-PROBE LIVES. The store's `ok` is now a
// whole-set verdict measured against the healthy window alone, so a still-building exit
// past its shorter 2h window arrives here as a HIT (its built siblings are current and must
// stay routable — that separation is what un-walled the map). The 2h clock therefore has to
// be read off the PER-ROW Stale flag instead: early-returning on the bare `ok` would hold a
// "still building" verdict for a full day and refuse a route that had since opened, which is
// the exact failure the shorter window exists to prevent. A fully-current set — including one
// whose under-construction row is still inside its own window, already a probe's verdict —
// costs zero API.
func (s *Service) Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error) {
	edges, ok, err := s.store.Edges(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if ok && !anyEdgeStale(edges) {
		return edges, nil
	}
	// A miss/stale set would normally re-fetch. But an UNREADABLE gate (one whose live
	// fetch keeps 400ing) is a persisted miss FOREVER — re-fetching it every reconcile
	// tick is the storm (1 req/s of guaranteed 400s). Honor the negative-result
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
	// presentShip=false: this is the REMOTE read (no hull on the gate), so an uncharted
	// origin gate is subject to the doomed-call precondition.
	return s.fetchAndStore(ctx, systemSymbol, playerID, false)
}

// ChartPresentGate is the PRESENCE-FORCED gate read: a hull physically
// standing on systemSymbol's own jump gate is the ONE moment its outbound connections
// are readable (a remote read with no ship present 400s, code 4001). It deliberately
// BYPASSES the negative-result backoff short-circuit that Connections honors —
// a plain Connections would skip an already-latched system even with a ship on its gate,
// the exact catch-22 that leaves a frontier gate uncharted forever. On a now-succeeding
// present read, fetchAndStore -> store.Replace deletes every row for the system INCLUDING
// the backoff marker (self-heal, gate_edge_repository.go), so the latch clears itself. It
// stays honest at the two boundaries that matter:
//   - GUARD 1 (idempotent): an already-charted system (Edges is a fresh, non-empty hit whose
//     every row is inside its own freshness window) early-returns with ZERO API — an arrival on
//     a known system costs one store read. A row PAST its own window still drives the read:
//     a hull standing on the gate is the one moment the read is guaranteed to succeed, so it is
//     the best possible moment to settle a pending build, and idempotence must not swallow it.
//   - GUARD 2 (negative cache intact): a present read that STILL fails re-enters the
//     backoff unchanged via fetchAndStore's enterBackoff, so this never defeats sp-4bm3;
//     only genuine ship-present success heals the latch.
//
// On that same store-miss (uncharted-to-us) branch it ALSO PUBLICLY charts the gate from the
// present hull: reading the gate stores OUR edge copy but leaves the gate uncharted-
// public, so every future jump-OUT re-reads it live (GetJumpGate) and 400s whenever no hull is
// on the gate. CreateChart makes the gate GetJumpGate-readable forever without a ship present,
// collapsing that re-read storm. Charting is best-effort and idempotent-by-GUARD-1 (a later
// arrival on a now-charted system store-hits and never re-charts) — see chartPresentWaypoint.
//
// It is best-effort from the caller's side: charting must never fail a trade/nav leg, so
// callers (travelWithJumpBound.chartArrivedGate, the reconcile sweep) log and
// swallow the error. The error is surfaced here so those callers can log the cause.
func (s *Service) ChartPresentGate(ctx context.Context, systemSymbol, shipSymbol string, playerID int) ([]system.GateEdge, error) {
	if edges, ok, err := s.store.Edges(ctx, systemSymbol); err != nil {
		return nil, err
	} else if ok && len(edges) > 0 && !anyEdgeStale(edges) {
		return edges, nil
	}
	// Store MISS ⇒ this gate is UNCHARTED-to-us. PUBLICLY chart it from the present hull BEFORE
	// the edge read, so the durable public chart (the sp-lv2n win) is not contingent on the read
	// succeeding. Best-effort and gated to THIS present-ship branch only — the remote fetch-
	// through path (Connections) has no hull on the gate and must never attempt a chart. GUARD 1
	// above is the idempotence key: a later arrival on a now-charted-by-us system returns there
	// and never re-charts, so each gate is charted at most once (no wasted call, no error-spam).
	s.chartPresentWaypoint(ctx, shipSymbol, playerID)
	// presentShip=true: a hull IS on the gate, so the read succeeds even for an
	// UNCHARTED gate (and we just charted it). BYPASS the doomed-call precondition —
	// applying it here would defeat the frontier self-heal.
	return s.fetchAndStore(ctx, systemSymbol, playerID, true)
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
//
// A chart that actually lands PAYS, so its reward is recorded before this returns — the credits
// are already in the balance, and an unrecorded inflow leaves a gap in the ledger chain.
func (s *Service) chartPresentWaypoint(ctx context.Context, shipSymbol string, playerID int) {
	if shipSymbol == "" {
		return
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
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
	result, err := s.apiClient.CreateChart(ctx, shipSymbol, token)
	if err != nil {
		if isAlreadyCharted(err) {
			return // benign: the gate is already publicly charted — nothing to do, nothing to log
		}
		logger.Log("INFO", fmt.Sprintf("chart-present: CreateChart from %s failed (non-fatal): %v", shipSymbol, err), map[string]interface{}{
			"action": "gate_public_chart_failed",
			"ship":   shipSymbol,
			"error":  err.Error(),
		})
		return
	}
	ledgerCommands.RecordChartReward(ctx, s.mediator, pid, shipSymbol, result)
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

// maxGateSyncReadAttempts bounds how many times a SYNC-time gate refresh re-reads a gate's
// live connections when the read comes back a successful-but-EMPTY 200 (sp-dmxy5, the
// sync-time analog of sp-hguq3's execution-time re-read). The SpaceTraders jump-gate
// endpoint intermittently returns a 200 OK with an empty/incomplete connections list — a
// transient, eventually-consistent read the API client's status-code retry never catches
// (an empty 200 is not a 429/5xx, so it never reaches here as a retryable error). Copying
// that empty set verbatim and Replace()ing the cache would erase a charted gate's real,
// previously-good edges until the next successful sync (~24h). Re-reading a bounded few
// times recovers the real set (it reappears on the next read); a genuinely-connectionless
// gate simply re-reads empty and syncs empty (bounded cost, no infinite re-read).
const maxGateSyncReadAttempts = 3

// gateSyncReadRetryBackoff is the short settle between bounded re-reads of a gate whose live
// connections came back empty (sp-dmxy5) — enough for an eventually-consistent backend to
// converge without stalling the reconcile. Applied ONLY between re-reads (never after the
// final attempt, never on the happy path), via the service clock so tests advance it
// instantly. Mirrors jump_ship.go's jumpGateReadRetryBackoff; combined with the API client's
// own rate limiter, the extra reads happen only on the (rare) empty-read path.
const gateSyncReadRetryBackoff = 750 * time.Millisecond

// fetchAndStore resolves systemSymbol's own gate, fetches its live connections,
// persists the fresh edge set, and returns it. The gate is resolved from the
// store first (a neighbor may have recorded it — this is the path that expands an
// UNCHARTED system), falling back to the charted system's graph.
func (s *Service) fetchAndStore(ctx context.Context, systemSymbol string, playerID int, presentShip bool) ([]system.GateEdge, error) {
	token, err := s.token(ctx, playerID)
	if err != nil {
		return nil, err
	}

	gateWaypoint, ok, err := s.store.GateWaypointOf(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if !ok {
		var charted bool
		gateWaypoint, charted, err = s.gateFromGraph(ctx, systemSymbol, playerID)
		if err != nil {
			return nil, err
		}
		// Doomed-call precondition: a graph-resolved gate that is still
		// UNCHARTED will 400 a no-ship GetJumpGate ("uncharted, no ship present"). On the
		// REMOTE read (no hull present) skip that guaranteed-failing call entirely and enter
		// the backoff exactly as a real 400 would — 0 API, identical routing outcome
		// (the BFS excludes the node either way). The present-ship path (charted==irrelevant,
		// a hull makes it readable) passes presentShip=true and never reaches here.
		if s.skipUnchartedFetch && !presentShip && !charted {
			cause := fmt.Errorf("gate %s is uncharted (no ship present) — a live GetJumpGate would 400", gateWaypoint)
			s.enterBackoff(ctx, systemSymbol, gateWaypoint, cause)
			return nil, fmt.Errorf("%w for %s (%s): uncharted, skipped doomed live fetch", ErrGateUnreadable, systemSymbol, gateWaypoint)
		}
	}

	gateData, err := s.readConnectionsBounded(ctx, systemSymbol, gateWaypoint, token)
	if err != nil {
		// A per-system fetch failure (a frontier gate the API refuses, 400 "no ship
		// present") is NOT a whole-route failure: tag it ErrGateUnreadable so the BFS
		// excludes just this node and continues.
		//
		// Only a PERMANENT client error (a terminal 4xx — the API's verdict that this
		// waypoint has no readable gate: uncharted / no ship present / not a gate)
		// records/extends the persisted negative-result backoff, so a doomed gate is not
		// re-probed every tick — the enter/extend INFO line is logged there, once
		// per transition. A TRANSIENT failure (5xx / network / retry-exhausted, which never
		// surfaces as a *ports.APIError) must NOT poison the cache: leaving it un-backed-off
		// lets the next miss re-probe it, so a momentary API blip never suppresses a real
		// gate for the whole 5m→30m→2h window.
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

	// sp-dmxy5: an empty edge set that survived the bounded re-reads must not clobber a
	// known-good cached set. Connections are static within an era (an era change is handled
	// by era scoping, which drops the prior set), so a charted gate we already hold real
	// edges for reading empty is the intermittent empty-200, not a real topology change —
	// refuse the destructive Replace and keep the good set visible. A gate with no prior
	// cache (a first sync, or a genuinely-connectionless gate) still persists the empty set.
	if len(edges) == 0 {
		if prior, ok := s.priorNonEmptyEdges(ctx, systemSymbol); ok {
			logger.Log("INFO", "Gate read came back empty but a good cached edge set exists — refusing to overwrite (transient empty-200, sp-dmxy5)", map[string]interface{}{
				"action":     "gate_sync_empty_read_refused",
				"system":     systemSymbol,
				"gate":       gateWaypoint,
				"kept_edges": len(prior),
			})
			return prior, nil
		}
	}

	if err := s.store.Replace(ctx, systemSymbol, edges); err != nil {
		return nil, err
	}
	return edges, nil
}

// readConnectionsBounded fetches a gate's live connections, re-reading a bounded number of
// times (maxGateSyncReadAttempts) when the read is a successful-but-EMPTY 200 — the
// intermittent incomplete read that would otherwise overwrite a charted gate's real edges
// with nothing (sp-dmxy5). The happy path (a non-empty first read, the overwhelming common
// case) returns immediately with exactly ONE read — zero overhead. A hard GetJumpGate error
// is returned immediately for the caller's existing backoff-on-permanent handling (the client
// already retried a transient status, so re-reading here would not help). A gate genuinely
// returning empty on every attempt returns the final empty snapshot — the caller then refuses
// to clobber a good cache, or syncs empty when there is none.
func (s *Service) readConnectionsBounded(ctx context.Context, systemSymbol, gateWaypoint, token string) (*ports.JumpGateData, error) {
	logger := logging.LoggerFromContext(ctx)
	var gateData *ports.JumpGateData
	for attempt := 0; attempt < maxGateSyncReadAttempts; attempt++ {
		data, err := s.apiClient.GetJumpGate(ctx, systemSymbol, gateWaypoint, token)
		if err != nil {
			return nil, err
		}
		gateData = data
		if len(data.Connections) > 0 {
			return data, nil
		}
		if attempt < maxGateSyncReadAttempts-1 {
			logger.Log("INFO", "Gate connections came back empty at sync — re-reading (transient incomplete 200, sp-dmxy5)", map[string]interface{}{
				"action":  "gate_sync_reread",
				"system":  systemSymbol,
				"gate":    gateWaypoint,
				"attempt": attempt + 1,
			})
			s.clock.Sleep(gateSyncReadRetryBackoff)
		}
	}
	return gateData, nil
}

// priorNonEmptyEdges reports systemSymbol's currently-cached NON-EMPTY edge set (if any),
// read from the persisted adjacency REGARDLESS of staleness — the known-good set an empty
// live read must not erase (sp-dmxy5). Consulted ONLY when a bounded re-read still came back
// empty: connections are static within an era, so a charted gate reading empty while we
// already hold real edges for it is the intermittent empty-200, never a real topology change.
// A store read failure, or no prior set, degrades to ok=false so the caller persists the fresh
// (empty) read — the previous behaviour, never a new failure mode (the bounded re-read is the
// primary recovery; this is the belt-and-suspenders net for the rare all-reads-empty case).
// The returned edges are a clean copy with Adjacency's raw Stale flag cleared — a routing read
// must never surface it.
func (s *Service) priorNonEmptyEdges(ctx context.Context, systemSymbol string) ([]system.GateEdge, bool) {
	adjacency, err := s.store.Adjacency(ctx)
	if err != nil {
		return nil, false
	}
	cached := adjacency[systemSymbol]
	if len(cached) == 0 {
		return nil, false
	}
	edges := make([]system.GateEdge, 0, len(cached))
	for _, e := range cached {
		edges = append(edges, system.GateEdge{
			ConnectedSystem:   e.ConnectedSystem,
			GateWaypoint:      e.GateWaypoint,
			UnderConstruction: e.UnderConstruction,
		})
	}
	return edges, true
}

// isPermanentGateAbsence reports whether a GetJumpGate failure is the API's PERMANENT verdict
// that this waypoint has no readable gate — a terminal 4xx (uncharted / no ship present / not a
// gate). Only such a permanent failure is negative-cached: a TRANSIENT failure (5xx /
// network / retry-exhausted) never surfaces as a *ports.APIError, so it declines the cache and is
// re-probed on the next miss instead of being suppressed for the whole backoff window. Matching a
// typed status (not the error string) keeps the classification robust against message wording.
func isPermanentGateAbsence(err error) bool {
	var apiErr *ports.APIError
	return errors.As(err, &apiErr) && apiErr.IsClientError()
}

// enterBackoff persists (or extends) the negative-result backoff for an unreadable gate
// and logs the ONE INFO line the operator sees — carrying the attempt count and the
// computed next-probe time. It fires only when a live probe actually failed
// (once per backoff transition, at 5m/30m/2h boundaries), so the log is a handful of
// lines per gate per day instead of the ~2,880 the old per-tick "will re-probe next
// fetch" line produced. A persistence failure is logged and swallowed: the gate is still
// excluded from the build, and the worst case degrades to the previous behavior (re-probe
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
// when no stored neighbor edge has already recorded the gate. It also reports whether
// that gate is CHARTED (no UNCHARTED trait): the graph builder populates each waypoint's
// traits[] from the API, so this is the same is-charted precondition the server itself
// checks — read for free from data we already hold, before any live call.
func (s *Service) gateFromGraph(ctx context.Context, systemSymbol string, playerID int) (string, bool, error) {
	graphResult, err := s.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", false, fmt.Errorf("failed to get system graph for %s: %w", systemSymbol, err)
	}
	for _, waypoint := range graphResult.Graph.Waypoints {
		if waypoint.IsJumpGate() {
			return waypoint.Symbol, !waypoint.HasTrait(unchartedTrait), nil
		}
	}
	return "", false, fmt.Errorf("no jump gate found in system %s", systemSymbol)
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
// build and the search continues on the readable subgraph; it is never
// routed through. Path returns an ErrUnroutable-wrapped error naming both systems
// when no route exists within MaxJumpPath (including when the only route required an
// excluded gate), or an underlying store/token error otherwise (fail closed).
func (s *Service) Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error) {
	return s.PathWithinJumps(ctx, fromSystem, toSystem, playerID, MaxJumpPath)
}

// PathWithinJumps is Path with a CALLER-SUPPLIED jump bound instead of the hardcoded MaxJumpPath
// — same strict fetch-through neighbor closure, same fail-closed unreadable-gate discipline, same
// under-construction exclusion, same bfsPath. It exists for the ONE strict caller that must reach
// deeper than MaxJumpPath=5: the long-haul arb heavy repositioning to a far multi-hop exotic
// source. It stays STRICT (fetch-through, fail-closed) — a laden heavy still refuses an
// unreadable frontier gate — so it is emphatically NOT the RELAXED probe/scout RepositionPath
// (which routes PAST unreadable gates over stored adjacency). The large bound is isolated to the
// long-haul reposition wiring; every other strict caller keeps MaxJumpPath via Path. maxJumps <= 0
// degrades to MaxJumpPath so a mis-wired caller can never accidentally get a zero-bound search
// (mirrors RepositionPath's defensive fallback).
func (s *Service) PathWithinJumps(ctx context.Context, fromSystem, toSystem string, playerID, maxJumps int) ([]string, error) {
	if maxJumps <= 0 {
		maxJumps = MaxJumpPath
	}
	return bfsPath(fromSystem, toSystem, maxJumps, func(systemSymbol string) ([]string, error) {
		edges, err := s.Connections(ctx, systemSymbol, playerID)
		if err != nil {
			// One system's gate is unreadable (sp-qxa4 — a frontier gate the API
			// refuses, "no ship present"). Exclude it as a routing-THROUGH node —
			// fail-closed, since its onward gates are unverified — but CONTINUE the
			// search over the readable subgraph instead of aborting the whole build.
			// A route that genuinely REQUIRES this node then ends ErrUnroutable (an
			// honest no-path), never silently rerouted through the unverified gate.
			// Any OTHER error (store/DB, token) still fails the whole search closed.
			// The exclusion is SILENT here: logging it every BFS traversal is
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
//     fails closed on an unreadable gate because its onward gates are unverified;
//     but a probe's whole purpose is to REACH that frontier, and a frontier gate is
//     unreadable precisely because no probe has arrived to read it — the catch-22 a
//     fail-closed router can never re-admit. Routing over the stored adjacency (which
//     retains an unreadable gate's last-known edges) breaks it: the probe hops the known
//     topology, and the coordinator's chart-on-arrival re-reads each gate the hull lands
//     on (sp-bcsu — travelWithJumpBound.chartArrivedGate -> ChartPresentGate, a PRESENT-ship
//     read that self-heals the latch), so each successful reposition SHRINKS the unreadable
//     set. RepositionPath ITSELF does this WITHOUT any live probe — Adjacency is a store
//     read, so the negative-result backoff is fully honored here; we route PAST
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
			// Never route INTO an under-construction gate. Unlike Path, an
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

// PathWithinJumpsStoredThenVerify is the "plan cheap, verify the chosen path" resolver for the
// long-haul heavy reposition to a FAR source. It is the latency fix for the residual
// ~20-min cold-cache stall the STRICT PathWithinJumps left: that resolver probes construction with a
// live GetWaypoint PER EDGE across the WHOLE bound-25 BFS frontier, hundreds of probes on a cold
// cache. This resolver instead:
//
//  1. PLANS the shortest route over the PERSISTED stored adjacency via RepositionPath — a pure store
//     read, NO fetch-through, NO per-edge probe. Long-haul targets are CHARTED (discovery ranks over
//     stored adjacency post-sp-yginc), so the sp-qxa4 "unreadable frontier" concern that made
//     sp-e059j pick the strict resolver does not apply here.
//  2. VERIFIES construction on the CHOSEN path from the STORED build state the plan already
//     filtered on, spending a BOUNDED allowance of live GetWaypoint probes
//     (maxChosenPathConstructionProbes) on the earliest hops whose stored row is UNVERIFIED —
//     never one per hop, so a deep lane costs no more to verify than a shallow one. This closes
//     the one trap a pure stored-adjacency swap would open: a stored edge whose gate has SINCE
//     gone under construction lets planning succeed and the jump fail at hop time — a failure the
//     planning-time reachability fallback (which triggers on ErrUnroutable) would NOT catch, so
//     the hull could LOOP on that gate. Both signals fail CLOSED: a stored under-construction flag
//     is refused outright, and gateUnderConstruction reads an unreadable gate as under construction.
//
// A bad gate on the chosen path (under construction, or unreadable → fail closed) ends the PLAN in
// ErrUnroutable, so the episode's reachability fallback skips this lane to the next reachable one —
// it deliberately does NOT route AROUND the bad gate to a longer alternate (skipping the LANE is the
// point). A clean path flies; jump_ship.go's source/destination construction checks and the live
// jump API remain the authoritative fail-closed backstop at hop time regardless of this plan.
//
// It reuses RepositionPath (the plan), gateUnderConstruction (the verify), bfsPath (via
// RepositionPath), and ErrUnroutable; it never fetches through, so the strict Path/PathWithinJumps
// used by tour/manual/arb at MaxJumpPath=5 are untouched. maxJumps <= 0 degrades to MaxJumpPath via
// RepositionPath's own fallback.
func (s *Service) PathWithinJumpsStoredThenVerify(ctx context.Context, fromSystem, toSystem string, playerID, maxJumps int) ([]string, error) {
	// DEFENSE-IN-DEPTH: bound ONLY the PATHFIND — the stored-adjacency plan plus the
	// chosen-path construction verify, both of which return BEFORE any jump — with a generous
	// wall-clock ceiling. A pathological stall (e.g. an API rate-limit hanging a construction probe)
	// then surfaces as ErrUnroutable so the worker's reachability fallback skips this lane, instead
	// of silently stalling the hull. The multi-hop FLIGHT that follows is emphatically NOT bounded
	// here (it routinely runs minutes) — cancel() ends this deadline the instant the plan returns.
	dctx, cancel := context.WithTimeout(ctx, s.pathfindBudget)
	defer cancel()

	path, err := s.storedThenVerify(dctx, fromSystem, toSystem, playerID, maxJumps)
	if err == nil {
		return path, nil
	}
	// A genuine PARENT cancel (a real shutdown) must PROPAGATE as a cancel — never be masked as a
	// skippable unroutable lane, even though the fail-closed verify may have already turned the
	// aborted probe into an ErrUnroutable. Check the parent FIRST; only OUR budget's deadline (the
	// parent still live, dctx alone expired) maps to ErrUnroutable so the worker skips to the next
	// lane. Any other error (a genuine bad-gate ErrUnroutable, a store failure) propagates verbatim.
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, fmt.Errorf("reposition pathfind %s→%s canceled: %w", fromSystem, toSystem, ctx.Err())
	}
	if errors.Is(dctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w from %s to %s: pathfind exceeded the %s budget (pathological planning stall)", ErrUnroutable, fromSystem, toSystem, s.pathfindBudget)
	}
	return nil, err
}

// storedThenVerify is the unbounded core of PathWithinJumpsStoredThenVerify: PLAN over the stored
// adjacency, then VERIFY construction on only the chosen path. The public method wraps it in the
// pathfind budget and passes the bounded context here, so the RepositionPath store read
// and every per-gate GetWaypoint construction probe observe that deadline.
func (s *Service) storedThenVerify(ctx context.Context, fromSystem, toSystem string, playerID, maxJumps int) ([]string, error) {
	// PLAN over the stored adjacency (no probe). An unroutable/store-error verdict propagates
	// verbatim — fail closed, and no chosen path means nothing to verify (zero probes).
	path, err := s.RepositionPath(ctx, fromSystem, toSystem, maxJumps)
	if err != nil {
		return nil, err
	}
	if len(path) <= 1 {
		return path, nil // same-system / zero-hop plan: no gate to jump into, nothing to verify
	}

	token, err := s.token(ctx, playerID)
	if err != nil {
		return nil, err
	}
	// Re-read the stored adjacency to resolve each chosen hop's gate waypoint (the neighbor edge
	// carries it). A pure store read — the same source RepositionPath just planned from — so the
	// chosen edges are guaranteed present; a store failure fails CLOSED.
	adjacency, err := s.store.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("reposition verify: read stored adjacency for %s→%s: %w", fromSystem, toSystem, err)
	}

	logger := logging.LoggerFromContext(ctx)
	probesLeft := maxChosenPathConstructionProbes
	for i := 0; i < len(path)-1; i++ {
		edge, ok := chosenHopEdge(adjacency, path[i], path[i+1])
		// A missing edge (an adjacency that shifted under the plan), or a stored flag that already
		// says the gate is building, fails the plan closed before any live call is considered.
		if !ok || edge.UnderConstruction {
			return nil, unroutableHop(fromSystem, toSystem, path[i+1])
		}
		// A FRESH row already carries the verdict of a live probe taken inside its own freshness
		// window (a shorter one while a gate is building), so re-probing it buys nothing. Spend the
		// bounded allowance only on rows whose build state is UNVERIFIED.
		if !edge.Stale || probesLeft == 0 {
			continue
		}
		probesLeft--
		if s.gateUnderConstruction(ctx, path[i+1], edge.GateWaypoint, token, logger) {
			return nil, unroutableHop(fromSystem, toSystem, path[i+1])
		}
	}
	return path, nil
}

// unroutableHop is the single verdict the chosen-path verify returns for a hop it cannot trust —
// the gate is building, its live read failed, or no stored edge backs it. The lane is SKIPPED, not
// routed around: reaching for a longer alternate is the strict resolver's job.
func unroutableHop(fromSystem, toSystem, intoSystem string) error {
	return fmt.Errorf("%w from %s to %s: gate into %s is under construction or unreadable (verified on the chosen path)", ErrUnroutable, fromSystem, toSystem, intoSystem)
}

// chosenHopEdge finds the stored edge backing the hop fromSystem→toSystem, carried on fromSystem's
// neighbor edge: the gate the jump lands on, plus the build state and freshness the verify decides
// from. Reports ok=false when no such edge is stored (a route resolved against an adjacency that
// has since shifted), so the caller fails closed rather than trust a guessed waypoint.
func chosenHopEdge(adjacency map[string][]system.GateEdge, fromSystem, toSystem string) (system.GateEdge, bool) {
	for _, e := range adjacency[fromSystem] {
		if e.ConnectedSystem == toSystem {
			return e, true
		}
	}
	return system.GateEdge{}, false
}

// Routable reports whether a route from→to exists within the strict MaxJumpPath=5. A DEFINITIVE
// unroutable verdict is (false, nil) — the caller refuses the spend but this is not an operational
// error; a store/fetch failure surfaces as (false, err) so the caller fails closed. Same-system is
// trivially routable. It delegates to RoutableWithinJumps at MaxJumpPath, so the two are provably
// one resolver — every existing Routable caller (stocker, trade, one-shot arb Guard-0) is unchanged.
func (s *Service) Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error) {
	return s.RoutableWithinJumps(ctx, fromSystem, toSystem, playerID, MaxJumpPath)
}

// RoutableWithinJumps is Routable with a CALLER-SUPPLIED jump bound: the same (bool, error)
// contract resolved over the SAME strict PathWithinJumps primitive (no forked path logic), just at a
// deeper reach. It exists for the ONE routability caller that must align its check past MaxJumpPath=5 —
// the long-haul arb Guard-0, whose sell leg is 6-12 gate hops from its source (the far exotic sinks
// discovery ranks and the reposition flies; the bound-5 Routable vetoed every one at buy time and the
// hull deadheaded home empty). A DEFINITIVE unroutable verdict — no path within maxJumps, including a
// route that required an excluded (unreadable / under-construction) gate — is (false, nil), the clean
// pre-buy veto; a store/fetch failure is (false, err) so the caller fails CLOSED (RULINGS #4 — this
// aligns the horizon, it does NOT weaken the fence: at 25 a genuinely unroutable lane is still refused).
// Same-system is trivially routable. maxJumps <= 0 degrades to MaxJumpPath (inherited from
// PathWithinJumps' defensive fallback), so a mis-wired caller can never widen the horizon by accident.
func (s *Service) RoutableWithinJumps(ctx context.Context, fromSystem, toSystem string, playerID, maxJumps int) (bool, error) {
	if fromSystem == toSystem {
		return true, nil
	}
	_, err := s.PathWithinJumps(ctx, fromSystem, toSystem, playerID, maxJumps)
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

// StoredHopDistances resolves the gate-hop distance from fromSystem to each requested target
// in ONE breadth-first walk of the stored adjacency, bounded to maxJumps. It answers the
// multi-target question ("how far is this system from each of those?") that the single-target
// resolvers can only answer one fetch-through search at a time.
//
// It is a PURE STORE READ — one Adjacency query, no fetch-through, ever. That is the whole
// point: the strict resolvers reach their neighbours through Connections, which fetches a
// missing or stale set LIVE, so using them to prove reach spends the API budget on topology
// exactly where topology is least cached. Here an uncached system is simply a DEAD END, so a
// cache miss costs a refusal instead of a request.
//
// Refusal is expressed by ABSENCE: a target further than maxJumps, unreachable over built
// gates, or reachable only through uncached or stale topology is missing from the result, and
// callers must read a missing target as unproven. Two exclusions keep that verdict no laxer
// than the STRICT resolver the executor actually flies (RULINGS #4):
//   - an under-construction edge is impassable (a jump into an unbuilt gate fails at hop time);
//   - a system whose stored PASSABLE edges are STALE is not expanded THROUGH. Their onward gates
//     are unverified, and staleness is precisely where Connections would re-fetch and may then
//     refuse — so trusting them here could prove a route the executor cannot resolve. Arriving
//     AT such a system is still resolved: only the edge into it must be verified for that.
//     An IMPASSABLE row's staleness does not condemn its system: the walk was never going to
//     traverse it, so its expiry withdraws no reach (see anyPassableEdgeStale).
//
// Distances are exact within the bound: the walk is depth-bounded but NOT breadth-bounded, so
// a dense neighbourhood around fromSystem can never exhaust a discovery budget and leave a
// genuinely-near target looking unreachable. maxJumps <= 0 degrades to MaxJumpPath, mirroring
// the other bounded resolvers, so a mis-wired caller can never get a zero-bound search. A
// store read failure fails CLOSED (a real error, never an empty "nothing is reachable").
//
// This is the PROOF-grade walk, for callers deciding whether a hull may be committed to a
// route. A caller that only needs a distance to RANK with wants StoredRankingDistances, whose
// staleness rule is accurate rather than conservative; choose between them deliberately.
func (s *Service) StoredHopDistances(ctx context.Context, fromSystem string, targets []string, maxJumps int) (map[string]int, error) {
	return s.storedDistances(ctx, fromSystem, targets, maxJumps, false)
}

// StoredRankingDistances answers the same multi-target distance question as StoredHopDistances,
// over the same pure store read with the same depth bound and the same zero-API guarantee, and
// differs in exactly one rule: it expands THROUGH a system whose stored edge set is past its
// freshness window, rather than treating that system as a dead end.
//
// That is a RANKING primitive, never a reach proof. The distinction is what the answer is used
// for. A reach proof commits a hull: an unverified route that turns out to be unflyable strands
// it holding cargo, so there staleness must refuse. A ranking distance commits nothing — the
// crossing is priced either way, and the executor still resolves the real route through the
// strict fetch-through resolver at flight time. Refusing a stale set there does not make the
// answer safer, only less accurate, and it charges a near crossing as though it were beyond the
// horizon.
//
// Reading a stale set is sound for a price because of which way its errors run. A gate's build
// state moves only from under-construction to built, so an edge recorded as built is still
// built however old the row; and a set that has since GAINED an edge makes the computed
// distance an OVER-estimate, never an under-estimate. Both harmless directions for a rank.
//
// It is laxer about FRESHNESS and about nothing else (RULINGS #4). An under-construction edge
// stays impassable — a jump into an unbuilt gate fails at hop time whatever the row's age — a
// system that was never cached is still a dead end rather than an assumed connection, and
// unreadable-gate markers never enter the adjacency at all, so this can never route past a gate
// the executor cannot read. A store read failure still fails CLOSED.
func (s *Service) StoredRankingDistances(ctx context.Context, fromSystem string, targets []string, maxJumps int) (map[string]int, error) {
	return s.storedDistances(ctx, fromSystem, targets, maxJumps, true)
}

// storedDistances is the one breadth-first walk behind both distance resolvers. throughStale is
// their ONLY difference, so the proof-grade verdict cannot drift from the ranking one by
// accident — there is no second copy of the traversal to fall out of step.
func (s *Service) storedDistances(ctx context.Context, fromSystem string, targets []string, maxJumps int, throughStale bool) (map[string]int, error) {
	if maxJumps <= 0 {
		maxJumps = MaxJumpPath
	}
	wanted := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t != "" {
			wanted[t] = true
		}
	}
	distances := make(map[string]int, len(wanted))
	if len(wanted) == 0 || fromSystem == "" {
		return distances, nil
	}
	adjacency, err := s.store.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("stored distances from %s: failed to read stored gate adjacency: %w", fromSystem, err)
	}
	if wanted[fromSystem] {
		distances[fromSystem] = 0
	}
	visited := map[string]bool{fromSystem: true}
	frontier := []string{fromSystem}
	for depth := 1; depth <= maxJumps && len(frontier) > 0; depth++ {
		var next []string
		for _, current := range frontier {
			// An uncached system has no edges to expand and dead-ends here of its own accord;
			// a stale one is dropped unless the caller ranks rather than proves, its onward
			// gates being past verification. Staleness is judged on the rows that could
			// actually be TRAVERSED — an impassable under-construction row withdraws nothing
			// and must not condemn the built exits it sits beside (see anyPassableEdgeStale).
			edges := adjacency[current]
			if !throughStale && anyPassableEdgeStale(edges) {
				continue
			}
			for _, e := range edges {
				if e.ConnectedSystem == "" || e.UnderConstruction || visited[e.ConnectedSystem] {
					continue
				}
				visited[e.ConnectedSystem] = true
				if wanted[e.ConnectedSystem] {
					distances[e.ConnectedSystem] = depth
				}
				next = append(next, e.ConnectedSystem)
			}
		}
		frontier = next
	}
	return distances, nil
}

// anyEdgeStale reports whether ANY row in a stored edge set is past its own freshness window.
// This is the RE-PROBE question — "should the fetch-through resolver go and look again?" — and it
// deliberately counts an under-construction row, whose shorter window exists precisely so a build
// COMPLETION is noticed same-era rather than held for a day.
//
// Contrast anyPassableEdgeStale, the ROUTING question. The two were one predicate and had to be
// split: a still-building exit must drive a re-probe (this) without condemning the system it
// leaves from (that).
func anyEdgeStale(edges []system.GateEdge) bool {
	for _, e := range edges {
		if e.Stale {
			return true
		}
	}
	return false
}

// anyPassableEdgeStale reports whether any row the walk could actually TRAVERSE is past its
// freshness window — the ROUTING question, "is this system unsafe to expand THROUGH?".
//
// An under-construction row is excluded from the count because it is impassable whatever its age:
// the walk already refuses to traverse it, so its expiry withdraws nothing that was ever usable
// and cannot make the system's OTHER exits less verified. Counting it is what walled the map off.
// A system's rows share one synced_at, so an under-construction row is stale on a 2h schedule
// while its built siblings sit comfortably inside 24h — and every system holding one still-building
// exit was dropped as unverifiable every 2h, refusing routes over its perfectly current built gates.
//
// A stale BUILT row still condemns the system, and that is the whole standard of proof here: those
// rows ARE traversal candidates, their onward gates are genuinely past verification, and the
// fetch-through resolver the executor flies would go back to the API and may then refuse. Trusting
// them would prove a route that cannot be resolved at flight time (RULINGS #4).
func anyPassableEdgeStale(edges []system.GateEdge) bool {
	for _, e := range edges {
		if e.Stale && !e.UnderConstruction {
			return true
		}
	}
	return false
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
