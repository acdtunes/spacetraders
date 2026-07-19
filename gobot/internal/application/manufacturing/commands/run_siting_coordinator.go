// Package commands — the factory SITING coordinator lives here alongside the factory chain
// coordinator it points. It is the standing "brain" that automates factory discovery,
// placement, and capacity planning: a per-player container running a slow (default 15min)
// 5-step reconcile tick —
//
//	SCAN     enumerate candidate (good, system) factory sites that pass the export-site
//	         hard gate + in-system input eligibility (supply-first) + data freshness.
//	SCORE    branchPL projection × tour-alignment − input-competition − staleness −
//	         worker-unreachability (a chain that cannot be manned scores down).
//	MAINTAIN pick the top-K portfolio (K = floor(workers / workers_per_chain)), subject
//	         to per-system and per-input-market concentration caps.
//	ACT      launch missing top-K chains THROUGH the existing guard stack (the launched
//	         goods_factory coordinator runs its own chain-margin, fail-closed sourcing,
//	         chain-P&L kill, and input-poison anti-cycle guards on its own iterations —
//	         guards veto at zero cost, never bypassed); retire chains that fall out of top-K
//	         via a clean container stop (with hysteresis to prevent thrash).
//	EMIT     for stale-but-promising candidates, post scout-demand so coverage refreshes
//	         them (reuses the captain scout-post-proposal channel).
//
// It COMPOSES with, and does not duplicate, the per-chain machinery: the chain-margin guard
// is a per-launch veto; the chain-P&L kill-switch is realized pruning; the input-poison
// anti-cycle is per-chain input-rest INSIDE a running chain (container-internal, not a
// cross-chain API — this coordinator drives PORTFOLIO membership, not per-chain pause). This
// is the brain that points the loops; the loops keep their own safety.
//
// Shape mirrors run_frontier_expansion_coordinator.go verbatim: a registered singleton
// handler with required repos + OPTIONAL setter-collaborators (nil disables that path), one
// infinite reconcile loop inside Handle(), resolveSitingConfig() resolving every <=0 knob to
// a documented protective default. Every decision is re-derived from persisted state each
// tick, so a daemon restart is transparent.
package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults: every operational value is a config key, filled here only when the
	// launch config leaves it unset — the Analyst owns the weights. Documented on
	// config.SitingConfig.
	defaultSitingTickSeconds        = 900 // 15min — siting is strategic, not per-second
	defaultSitingWorkersPerChain    = 3.5 // K = floor(workers / this)
	defaultSitingFreshnessMaxSecs   = 7200
	defaultSitingEmitStalenessSecs  = 1800
	defaultSitingWeightAlignment    = 1.0
	defaultSitingWeightCompetition  = 1.0
	defaultSitingWeightStaleness    = 1.0
	defaultSitingWeightReachability = 1.0
	defaultSitingMaxChainsSystem    = 3
	defaultSitingMaxChainsInput     = 2
	defaultSitingRetireHysteresis   = 2
	defaultSitingEffectSelfcheck    = 4
	defaultSitingScoutCooldownSecs  = 3600

	// rankingLogLimit bounds how many ranked candidates are logged per tick so the ranking
	// log stays readable on a large candidate set (the frontier rankingLogLimit idiom).
	sitingRankingLogLimit = 12
)

// SitingCandidate is one (good, system) factory site that passed SCAN's gates: an EXPORT
// market for the good exists in-system (hard gate), the recipe resolves with every feed
// input eligibly sourceable in-system (a5j7 supply-first), and the market data is fresh
// enough to trust. It carries the data age (for the staleness discount + the EMIT band)
// and its feed source markets (for the input-competition penalty + the per-input-market
// concentration cap).
type SitingCandidate struct {
	Good         string
	System       string
	DataAgeSecs  float64  // worst-case market-data age across the site's markets
	InputMarkets []string // BUY-leaf feed source waypoints the chain would draw from
}

// Key is the stable identity of a candidate site, used to diff desired vs running and to
// key per-candidate in-memory state (retire hysteresis).
func (c SitingCandidate) Key() string { return c.Good + "@" + c.System }

// ScoredCandidate is a SitingCandidate with its computed score components. Score is the
// value MAINTAIN ranks by; the components are retained for the ranking log and metrics.
type ScoredCandidate struct {
	SitingCandidate
	ProjectedPL    int     // branchPL per-pass absolute credits — a MONOTONIC ranking proxy
	TourAlignment  float64 // realized tour-throughput weight (1.0 neutral when unavailable)
	Competition    float64 // input-competition penalty (subtracted)
	Staleness      float64 // staleness discount (subtracted)
	Unreachability float64 // worker-unreachability penalty (subtracted)
	Score          float64 // final: PL × alignment − competition − staleness − unreachability
	Proceed        bool    // launch-guard verdict; false = chain-margin guard veto, excluded from top-K
}

// RunningChain is one factory chain currently live for the player (a goods_factory
// coordinator container), as ACT sees it when diffing running vs desired.
type RunningChain struct {
	FactoryID string
	Good      string
	System    string
}

// Key mirrors SitingCandidate.Key so a running chain and a candidate for the same
// (good, system) compare equal.
func (r RunningChain) Key() string { return r.Good + "@" + r.System }

// ChainProjection is the launch-guard verdict for a candidate chain (SCORE): the
// projected per-pass P&L and whether the guard would let it proceed. Proceed=false is the
// chain-margin guard's veto — the candidate is dropped from the portfolio at zero cost, not
// launched.
type ChainProjection struct {
	ProjectedPL int
	Proceed     bool
	Reason      string
}

// SitingScanParams carries the resolved SCAN thresholds the scanner needs.
type SitingScanParams struct {
	FreshnessMaxSecs float64
}

// --- Ports (interfaces the handler depends on; the daemon wires concrete impls, tests
// inject fakes — the frontier optional-collaborator idiom) ---

// SitingScanner enumerates the candidate sites the coordinator ranks (SCAN). The concrete
// impl (services.SitingScanner) hides the market-repo / resolver / locator joins behind one
// call: export-site hard gate + in-system input eligibility + freshness. Required — a nil
// scanner leaves the coordinator with nothing to rank (fail-safe: it does nothing).
type SitingScanner interface {
	ScanCandidates(ctx context.Context, playerID int, params SitingScanParams) ([]SitingCandidate, error)
}

// ChainProjector projects a candidate chain's per-pass P&L through the launch guard
// (SCORE). The concrete impl builds the dependency tree and calls ChainMarginGuard.Evaluate;
// Proceed=false is the veto. Required — a nil projector means no candidate can be priced, so
// none is launched (fail-closed).
type ChainProjector interface {
	Project(ctx context.Context, good, system string, playerID int) (ChainProjection, error)
}

// ChainController is the ACT surface: enumerate running chains, launch a chain (through the
// full guard stack via the goods_factory container), and retire one (clean container stop).
// Required — a nil controller means ACT cannot observe or change the portfolio.
type ChainController interface {
	RunningChains(ctx context.Context, playerID int) ([]RunningChain, error)
	Launch(ctx context.Context, good, system string, playerID int) (containerID string, err error)
	Retire(ctx context.Context, factoryID string, playerID int) error
}

// TourAlignmentProvider returns a tour-pull SIGNAL (>= 0) for a factory good in a system —
// how strongly tours realize that good (the stock-draw rate where available, else
// tour_leg_telemetry pass-by throughput). 0 means tours do not pull the good here. SCORE
// turns the signal into an alignment factor (1 + WeightTourAlignment × signal). Optional: a
// nil provider yields signal 0, so scoring falls back to branchPL alone (factor 1.0).
type TourAlignmentProvider interface {
	Alignment(ctx context.Context, playerID int, good, system string) (signal float64, err error)
}

// WorkerCounter returns the manufacturing worker-pool size for K-sizing
// (K = floor(workers / workers_per_chain)). Optional: a nil counter (or a configured TopK)
// bypasses derivation.
type WorkerCounter interface {
	CountWorkers(ctx context.Context, playerID int) (int, error)
}

// WorkerReachabilityProvider returns a staffing-reachability SIGNAL ∈ [0,1] for placing a
// factory chain in a system: how easily an idle manufacturing worker can man it. 1 = an idle
// worker is already in-system (distance 0, fully staffable); a value in (0,1) decays with the
// ferry hop-distance from the nearest idle-worker pool; 0 = NO idle worker can reach the
// system at all (no in-system idle worker AND no ferry path in). SCORE turns the signal into
// an unreachability penalty (weight × branchPL × (1 − signal)), so a chain that cannot be
// manned is deprioritized even when the launch guard clears it on margin: capacity without
// workers to run it is not enough. The concrete impl reuses the stored-adjacency ferry reach
// (gategraph RepositionPath) + the idle-worker locator (FindIdleLightHaulers) — it does NOT
// reinvent graph routing. Optional: a nil provider (or a read error) yields signal 1.0, so the
// term is neutral (no penalty) and scoring falls back to branchPL alone — a transient
// gate-graph read never nukes the portfolio.
type WorkerReachabilityProvider interface {
	Reachability(ctx context.Context, playerID int, system string) (signal float64, err error)
}

// ScoutDemandEmitter posts scout-demand for a stale-but-promising candidate system (EMIT).
// The concrete impl wraps the captain scout-post-proposal event channel (deduped via
// HasSince over the cooldown). Optional: a nil emitter disables EMIT.
type ScoutDemandEmitter interface {
	// EmitScoutDemand records a scout-demand signal for the system, deduped so at most one
	// fires per cooldown window. Returns true when a NEW demand was emitted this call.
	EmitScoutDemand(ctx context.Context, playerID int, system string, cooldown time.Duration, payload string) (bool, error)
}

// RunSitingCoordinatorCommand launches the standing siting coordinator for a player. Like
// the frontier / trade-fleet / scout-post coordinators it runs an infinite reconcile loop
// inside a single Handle() call; the container wraps it. All knobs are launch-config keys;
// <= 0 (or the zero value) falls back to the documented default, so the CLI passes only what
// it overrides.
type RunSitingCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	// Disabled is the escape hatch (from [manufacturing.siting] siting_disabled). Absent/false
	// = ACTIVE: unlike most features, this coordinator ships live by default, not dark.
	// Reconstructed as the negation of siting_disabled so an absent key reads as enabled
	// across a recovery from an old config that predates the key.
	Disabled bool
	// DryRun evaluates + logs every decision but takes no ACT action (watch mode).
	DryRun bool

	TickIntervalSecs         int
	TopK                     int
	WorkersPerChain          float64
	FreshnessMaxSecs         int
	EmitStalenessSecs        int
	WeightTourAlignment      float64
	WeightInputCompetition   float64
	WeightStaleness          float64
	WeightWorkerReachability float64
	MaxChainsPerSystem       int
	MaxChainsPerInputMarket  int
	RetireHysteresisTicks    int
	EffectSelfcheckTicks     int
	ScoutDemandCooldownSecs  int
}

// RunSitingCoordinatorResponse reports reconcile progress. Because the loop is infinite it
// is only observed on context cancellation (shutdown).
type RunSitingCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunSitingCoordinatorHandler reconciles the desired factory portfolio against the running
// one every tick. It is a registered singleton (one instance serves every player's ticks),
// so all decision inputs are derived fresh from the injected ports each pass; the only
// in-memory state is edge-trigger bookkeeping (retire hysteresis + the effect self-check
// streak), keyed by container ID so it stays per-coordinator.
type RunSitingCoordinatorHandler struct {
	scanner    SitingScanner
	projector  ChainProjector
	controller ChainController
	clock      shared.Clock

	// Optional collaborators wired via setters (the codebase's optional-injection idiom).
	alignment    TourAlignmentProvider
	workers      WorkerCounter
	emitter      ScoutDemandEmitter
	reachability WorkerReachabilityProvider

	mu    sync.Mutex
	state map[string]*sitingCoordinatorState // keyed by container ID
}

// sitingCoordinatorState is the per-coordinator in-memory edge-trigger bookkeeping.
type sitingCoordinatorState struct {
	// outOfTopK counts consecutive ticks each running chain (by Key) has been outside the
	// desired top-K, for the retire hysteresis.
	outOfTopK map[string]int
	// effect is the shared inert-loop detector (health.EffectTracker) that runs the
	// candidates-but-zero-effect self-check: it holds the no-effect streak and the one-shot
	// WARN dedup, re-arming on any productive/steady tick. Lazily built on first self-check
	// (the threshold comes from the resolved tick config); one per container so the streak is
	// per-coordinator, matching the singleton handler's per-container bookkeeping.
	effect *health.EffectTracker
	// unsizedWarned edge-triggers the "cannot size portfolio" WARN so a persistent worker-count
	// failure logs once, not every tick (reset when sizing recovers).
	unsizedWarned bool
}

// NewRunSitingCoordinatorHandler wires the coordinator. clock defaults to the real clock
// when nil (production). The tour-alignment provider, worker counter, and scout-demand
// emitter are optional and injected separately (SetTourAlignmentProvider, SetWorkerCounter,
// SetScoutDemandEmitter).
func NewRunSitingCoordinatorHandler(
	scanner SitingScanner,
	projector ChainProjector,
	controller ChainController,
	clock shared.Clock,
) *RunSitingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunSitingCoordinatorHandler{
		scanner:    scanner,
		projector:  projector,
		controller: controller,
		clock:      clock,
		state:      make(map[string]*sitingCoordinatorState),
	}
}

// SetTourAlignmentProvider wires the realized-tour-throughput signal for SCORE. Leaving it
// unset makes tour-alignment neutral (branchPL alone drives the ranking).
func (h *RunSitingCoordinatorHandler) SetTourAlignmentProvider(a TourAlignmentProvider) {
	h.alignment = a
}

// SetWorkerCounter wires the worker-pool size source for K-derivation. Leaving it unset
// (or setting TopK) bypasses derivation.
func (h *RunSitingCoordinatorHandler) SetWorkerCounter(w WorkerCounter) { h.workers = w }

// SetScoutDemandEmitter wires the scout-demand channel for EMIT. Leaving it unset disables
// EMIT (the coordinator still scans/scores/acts).
func (h *RunSitingCoordinatorHandler) SetScoutDemandEmitter(e ScoutDemandEmitter) { h.emitter = e }

// SetWorkerReachabilityProvider wires the staffing-reachability signal for SCORE. Leaving it
// unset makes the reachability term neutral (no penalty) — scoring ranks on branchPL ×
// alignment − competition − staleness alone, exactly as before the term landed.
func (h *RunSitingCoordinatorHandler) SetWorkerReachabilityProvider(r WorkerReachabilityProvider) {
	h.reachability = r
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunSitingCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunSitingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveSitingConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Siting coordinator starting (tick %s, dry_run=%v, disabled=%v)", cfg.Tick, cfg.DryRun, cfg.Disabled), map[string]interface{}{
		"action":       "siting_start",
		"container_id": cmd.ContainerID,
		"dry_run":      cfg.DryRun,
		"disabled":     cfg.Disabled,
	})

	result := &RunSitingCoordinatorResponse{Errors: []string{}}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Siting reconcile failed: %v", err), nil)
		}
		result.Ticks++

		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// sitingRunConfig is the launch command with every default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (the frontier resolveConfig idiom).
type sitingRunConfig struct {
	Disabled bool
	DryRun   bool
	Tick     time.Duration

	TopK            int // 0 = derive from the worker pool
	WorkersPerChain float64
	FreshnessMax    time.Duration
	EmitStaleness   time.Duration

	WeightAlignment    float64
	WeightCompetition  float64
	WeightStaleness    float64
	WeightReachability float64

	MaxChainsPerSystem      int
	MaxChainsPerInputMarket int
	RetireHysteresisTicks   int
	EffectSelfcheckTicks    int
	ScoutCooldown           time.Duration
}

func resolveSitingConfig(cmd *RunSitingCoordinatorCommand) sitingRunConfig {
	c := sitingRunConfig{
		Disabled:                cmd.Disabled,
		DryRun:                  cmd.DryRun,
		Tick:                    time.Duration(cmd.TickIntervalSecs) * time.Second,
		TopK:                    cmd.TopK,
		WorkersPerChain:         cmd.WorkersPerChain,
		FreshnessMax:            time.Duration(cmd.FreshnessMaxSecs) * time.Second,
		EmitStaleness:           time.Duration(cmd.EmitStalenessSecs) * time.Second,
		WeightAlignment:         cmd.WeightTourAlignment,
		WeightCompetition:       cmd.WeightInputCompetition,
		WeightStaleness:         cmd.WeightStaleness,
		WeightReachability:      cmd.WeightWorkerReachability,
		MaxChainsPerSystem:      cmd.MaxChainsPerSystem,
		MaxChainsPerInputMarket: cmd.MaxChainsPerInputMarket,
		RetireHysteresisTicks:   cmd.RetireHysteresisTicks,
		EffectSelfcheckTicks:    cmd.EffectSelfcheckTicks,
		ScoutCooldown:           time.Duration(cmd.ScoutDemandCooldownSecs) * time.Second,
	}
	if c.Tick <= 0 {
		c.Tick = defaultSitingTickSeconds * time.Second
	}
	if c.WorkersPerChain <= 0 {
		c.WorkersPerChain = defaultSitingWorkersPerChain
	}
	if c.FreshnessMax <= 0 {
		c.FreshnessMax = defaultSitingFreshnessMaxSecs * time.Second
	}
	if c.EmitStaleness <= 0 {
		c.EmitStaleness = defaultSitingEmitStalenessSecs * time.Second
	}
	if c.WeightAlignment <= 0 {
		c.WeightAlignment = defaultSitingWeightAlignment
	}
	if c.WeightCompetition <= 0 {
		c.WeightCompetition = defaultSitingWeightCompetition
	}
	if c.WeightStaleness <= 0 {
		c.WeightStaleness = defaultSitingWeightStaleness
	}
	if c.WeightReachability <= 0 {
		c.WeightReachability = defaultSitingWeightReachability
	}
	if c.MaxChainsPerSystem <= 0 {
		c.MaxChainsPerSystem = defaultSitingMaxChainsSystem
	}
	if c.MaxChainsPerInputMarket <= 0 {
		c.MaxChainsPerInputMarket = defaultSitingMaxChainsInput
	}
	if c.RetireHysteresisTicks <= 0 {
		c.RetireHysteresisTicks = defaultSitingRetireHysteresis
	}
	if c.EffectSelfcheckTicks <= 0 {
		c.EffectSelfcheckTicks = defaultSitingEffectSelfcheck
	}
	if c.ScoutCooldown <= 0 {
		c.ScoutCooldown = defaultSitingScoutCooldownSecs * time.Second
	}
	return c
}

// reconcileResult tallies one tick's effect for the effect self-check and metrics.
type reconcileResult struct {
	Candidates int
	Scored     int
	Desired    int
	Launched   int
	Retired    int
	Emitted    int
}

// Actions is the ACT+EMIT effect count the self-check watches (zero over N ticks → WARN).
func (r reconcileResult) Actions() int { return r.Launched + r.Retired + r.Emitted }

// reconcileOnce runs one full SCAN→SCORE→MAINTAIN→ACT→EMIT pass. It is the unit the tests
// drive directly; Handle just calls it on the tick.
func (h *RunSitingCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunSitingCoordinatorCommand) (reconcileResult, error) {
	cfg := resolveSitingConfig(cmd)
	logger := common.LoggerFromContext(ctx)

	// Boot-gate: the container stays resident when disabled so a config flip + restart
	// re-arms it with no manual relaunch, but it takes no action while stood down.
	if cfg.Disabled {
		return reconcileResult{}, nil
	}

	// SCAN
	candidates, err := h.scanner.ScanCandidates(ctx, cmd.PlayerID, SitingScanParams{FreshnessMaxSecs: cfg.FreshnessMax.Seconds()})
	if err != nil {
		return reconcileResult{}, fmt.Errorf("siting scan: %w", err)
	}
	res := reconcileResult{Candidates: len(candidates)}

	// SCORE
	scored := h.score(ctx, cmd, cfg, candidates)
	res.Scored = len(scored)

	// MAINTAIN — size the portfolio (K), then pick the top-K subject to concentration caps.
	k, sized := h.resolveK(ctx, cmd, cfg)
	var desired []ScoredCandidate
	if sized {
		desired = h.maintain(cfg, scored, k)
		// ACT — launch missing top-K / retire fallen (with hysteresis).
		launched, retired := h.act(ctx, cmd, cfg, desired)
		res.Launched, res.Retired = launched, retired
		h.noteSized(cmd.ContainerID)
	} else {
		// Cannot size the portfolio (no top_k override AND no readable worker count): do NOT
		// churn — launch/retire nothing this tick. EMIT still runs (it is K-independent).
		h.warnUnsized(ctx, cmd)
	}
	res.Desired = len(desired)

	// EMIT — scout-demand for stale-but-promising sites in the desired portfolio.
	res.Emitted = h.emit(ctx, cmd, cfg, desired)

	// Effect self-check: candidates but zero effect over N ticks → one WARN naming why.
	h.runSelfCheck(ctx, cmd, cfg, res)

	logger.Log("INFO", fmt.Sprintf("Siting tick: %d candidates, %d scored, %d launched, %d retired, %d scout-demands", res.Candidates, res.Scored, res.Launched, res.Retired, res.Emitted), map[string]interface{}{
		"action":       "siting_tick",
		"container_id": cmd.ContainerID,
		"candidates":   res.Candidates,
		"launched":     res.Launched,
		"retired":      res.Retired,
		"emitted":      res.Emitted,
	})
	return res, nil
}

// coordinatorState returns (creating if needed) the per-container edge-trigger bookkeeping.
func (h *RunSitingCoordinatorHandler) coordinatorState(containerID string) *sitingCoordinatorState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state[containerID]
	if st == nil {
		st = &sitingCoordinatorState{outOfTopK: make(map[string]int)}
		h.state[containerID] = st
	}
	return st
}
