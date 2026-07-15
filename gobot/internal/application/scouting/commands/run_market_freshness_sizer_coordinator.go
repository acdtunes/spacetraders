// Package commands: the market-freshness auto-sizer (sp-orgp). A standing daemon
// coordinator that keeps EVERY scanned market fresh within a configurable SLA by
// AUTO-SIZING and AUTO-BUYING probe capacity per system — the freshness analogue of the
// frontier expansion coordinator (sp-8w89), which auto-sizes/buys for post COVERAGE.
//
// It closes a loop no automation held before: scout posts were single-hull sweep_once,
// so freshness scaled AGAINST market count (one probe cannot re-walk a 26-market system
// inside a 1h window), leaving the most valuable systems the most stale. This coordinator
// MEASURES the demand, SIZES each market-bearing system's standing post, DECLARES/resizes
// it, and BUYS probes under the money guards — while ALL movement, manning, and market
// partitioning stay with the existing scout-post reconciler (sp-cxpq/sp-enry). It moves
// and claims NOTHING (RULINGS #7); its only writes are to the desired-state posts table
// and the guarded probe buy.
//
// Every tick, per market-bearing system:
//   - required_probes = ceil(markets × per_market_cycle / sla), where per_market_cycle is
//     MEASURED from live scan telemetry (not a constant), seeded until telemetry exists;
//   - the empirical worst-case market age is the CLOSED-LOOP ground truth: a system
//     breaching its SLA has its demand RAISED beyond the static model, a comfortably-fresh
//     one is allowed to RELEASE a probe (hysteresis prevents flapping);
//   - the standing post is declared (new system), promoted (a sweep_once that turned out to
//     hold markets), resized (through the manning-preserving hull-update seam), or retired
//     (its markets are gone — freeing its probes).
//
// The AGGREGATE demand across all systems drives one guarded probe buy per cycle, reusing
// the frontier guard stack (probebuy.GuardedProbeBuyer): idle + in-flight + manning probes
// all count as supply so it never over-buys (the sp-njwy lesson), and the ledger-derived
// cooldown it shares with the frontier coordinator serializes the two against each other.
//
// The loop is idempotent and restart-safe (RULINGS #2): every decision is re-derived from
// persisted state each tick (the census, posts, the ledger), so a restart never
// double-declares (Upsert is keyed by system) or double-buys (the cooldown reads the
// ledger, not memory). The coordinator persists NO new state of its own.
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
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// freshnessScoutFleetTag is the dedication tag scouting hulls carry. An idle/manning
	// satellite counts toward supply only when it is undedicated or already scout-tagged —
	// the same first-line poach filter the reconciler and frontier coordinator use.
	freshnessScoutFleetTag = "scout"

	// Config defaults (RULINGS #5: every operational value is a flag/config key, filled
	// here only when the launch config leaves it unset).
	defaultSizerTickSeconds = 60
	defaultSLASeconds       = 3600 // 1h freshness SLA
	defaultSeedCycleSeconds = 180  // seeded per-market cycle until telemetry exists
	defaultMinCycleSamples  = 3    // min consecutive-interval samples to trust a measured cycle
	// defaultWorstCycleSeconds is the worst-plausible per-market cycle bounding the market-count
	// CLAMP ceiling (sp-iupr issue 3b): a system can never be sized above RequiredHulls(markets,
	// this, sla), so a noisy-HIGH per-market reading cannot over-size a small-market system
	// (the ZY16 3-markets-sized-6 pathology). 30min is well above any real per-market hop yet
	// far below the noise readings, so it clamps only the noise, never a legitimate target.
	defaultWorstCycleSeconds = 1800
	// defaultCycleDampeningPercent shrinks a system's OWN noisy per-market cycle toward the
	// fleet-wide median (sp-iupr issue 3c) so equal-market systems converge instead of diverging
	// on measurement noise. 0 disables (pre-issue-3 pass-through); 100 pools fully to the median.
	defaultCycleDampeningPercent = 50
	defaultMaxProbesPerSystem    = 8 // per-system hull cap (bounds a runaway feedback raise)
	// defaultBreachResponsePercent scales the observed worst-case age fed to the sp-tor9
	// circuit-observed breach response. 100 sizes from the exact measured circuit (a trusted,
	// fully-manned breaching post is sized to ceil(current × age/sla)); > 100 buys headroom by
	// sizing for a proportionally worse effective age (a tighter effective SLA) when the circuit
	// under-measures in practice; < 100 damps the response. Bounded by MaxProbesPerSystem either way.
	defaultBreachResponsePercent = 100
	defaultReleaseSlackPercent   = 60 // release a feedback probe only below this % of the SLA (hysteresis)
	// defaultReleaseStableWindowSecs is how long a WARM post's measured surplus (desired <
	// current, under the SLA but past the slack line) must hold before one probe is shed to
	// the pool (sp-iupr bug 2). It debounces the shed so a one-cycle demand dip never releases
	// a hull the next tick re-buys; the buy cooldown is the second half of that anti-thrash.
	defaultReleaseStableWindowSecs = 300
	defaultSizerMaxProbeFleet      = 40              // total satellite cap
	defaultSizerMaxSpend           = 500000          // max probe spend within the trailing spend window (Admiral 2026-07-15: ~17 probes/hr ramp; 25%-treasury + fleet cap still bind)
	defaultSizerCooldown           = 1 * time.Minute // Admiral 2026-07-15: fast ramp; spend window + treasury/fleet caps still bound total buys
	defaultSizerSpendWindow        = 1 * time.Hour
)

// FleetReader is the narrow slice of the ship repository the sizer reads: the whole fleet,
// to count the scout-probe SUPPLY (idle + in-flight + manning) and the satellite fleet
// size the cap gates on. Read-only — the coordinator never writes ship state.
type FleetReader interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// ScoutPostHullUpdater is the NARROW, manning-preserving resize seam: it updates only a
// standing post's hull budget, never its assignment columns, so resizing a live post cannot
// clobber the manning the scout reconciler wrote to the same row. Optional (SetHullUpdater):
// when unwired the coordinator falls back to a full Upsert of the post object it read (which
// also preserves manning, but is exposed to a concurrent-write clobber the narrow update
// avoids). Structurally satisfied by the GORM scout-post repository.
type ScoutPostHullUpdater interface {
	UpdateHulls(ctx context.Context, playerID int, systemSymbol string, hulls int) error
}

// RunMarketFreshnessSizerCoordinatorCommand launches the standing coordinator for a player
// (sp-orgp). Like the other coordinators it runs an infinite reconcile loop inside a single
// Handle() call. All knobs are launch-config keys (RULINGS #5); <= 0 (or the zero value)
// falls back to the documented default.
type RunMarketFreshnessSizerCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int

	// DryRun evaluates every decision and logs what it WOULD do, but declares no post and
	// buys no probe (the captain watches a cycle before arming it).
	DryRun bool

	SLASeconds              int            // default freshness SLA in seconds
	SystemSLAOverrides      map[string]int // per-system SLA override (seconds)
	SeedCycleSeconds        int            // seeded per-market cycle until telemetry exists
	MinCycleSamples         int            // min samples to trust a measured cycle
	WorstCycleSeconds       int            // worst-plausible per-market cycle bounding the market-count clamp (sp-iupr issue 3b)
	CycleDampeningPercent   int            // shrinkage % of a system's own cycle toward the fleet median (sp-iupr issue 3c)
	MaxProbesPerSystem      int            // per-system hull cap
	BreachResponsePercent   int            // aggressiveness of the circuit-observed breach response (sp-tor9); 100 = exact measured circuit
	ReleaseSlackPercent     int            // release hysteresis: shed a probe only below this % of the SLA
	ReleaseStableWindowSecs int            // a warm surplus must hold this long before one probe is shed (sp-iupr)

	MaxProbeFleet        int // total satellite cap
	MaxSpendPerCycle     int // max probe spend within the trailing spend window
	PurchaseCooldownSecs int // min seconds between probe buys
	SpendWindowSecs      int // trailing window (seconds) the spend cap sums over
}

// RunMarketFreshnessSizerCoordinatorResponse reports reconcile progress. Because the loop
// is infinite it is only observed on context cancellation (shutdown).
type RunMarketFreshnessSizerCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunMarketFreshnessSizerCoordinatorHandler reconciles freshness demand against probe
// supply every tick. It is a registered singleton (one instance serves every player's
// ticks). Sizing, declaring, buying, and retiring are all derived FRESH from the injected
// ports each pass (RULINGS #2) — the sole in-memory state is releasePendingSince, the
// stable-window debounce that paces warm-surplus RELEASES (sp-iupr bug 2). That state is
// restart-CONSERVATIVE: a restart forgets the pending windows, so release is merely
// re-debounced (delayed), never double-applied — it can never over-release, only re-earn
// the window. It mirrors the scout reconciler's driftPendingSince idiom.
type RunMarketFreshnessSizerCoordinatorHandler struct {
	freshnessReader domainScouting.SystemFreshnessReader
	postRepo        domainScouting.ScoutPostRepository
	fleetRepo       FleetReader
	ledgerRepo      ledger.TransactionRepository
	clock           shared.Clock

	// treasury, purchaser, and hullUpdater are optional collaborators wired via setters
	// (the codebase's optional-injection idiom). A nil treasury or purchaser fails the
	// PURCHASE path closed; a nil hullUpdater falls back to full-Upsert resizes. Sizing,
	// declaring, and retiring need none of them, so the coordinator is always at least
	// partially useful.
	treasury    probebuy.TreasuryReader
	purchaser   probebuy.ProbePurchaser
	hullUpdater ScoutPostHullUpdater

	// captainEvents emits the coordinator error-loop event when a reconcile pass fails with
	// the identical error for DefaultStreakThreshold consecutive ticks. Optional-injection.
	captainEvents captain.EventRecorder

	// liveConfig snapshots the container's OWN persisted config at each tick start
	// (sp-vwek/sp-0z7f), so a `spacetraders tune` of a spend/cooldown/cap knob takes
	// effect on the NEXT tick with no restart. Optional-injection: nil keeps the
	// launch-frozen behavior byte-identical.
	liveConfig liveconfig.Reader

	// releaseMu guards releasePendingSince against the singleton-handler concurrency (many
	// players' ticks share one handler) — the same reason the scout reconciler guards
	// driftPendingSince. releasePendingSince records, per player|system, the first tick a
	// WARM post's measured surplus was seen, so the shed only fires once it has held for the
	// stable window (sp-iupr bug 2). A key is cleared the moment the surplus resolves.
	releaseMu           sync.Mutex
	releasePendingSince map[string]time.Time
}

// NewRunMarketFreshnessSizerCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production). The treasury reader, probe purchaser, and hull updater
// are optional and injected separately.
func NewRunMarketFreshnessSizerCoordinatorHandler(
	freshnessReader domainScouting.SystemFreshnessReader,
	postRepo domainScouting.ScoutPostRepository,
	fleetRepo FleetReader,
	ledgerRepo ledger.TransactionRepository,
	clock shared.Clock,
) *RunMarketFreshnessSizerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunMarketFreshnessSizerCoordinatorHandler{
		freshnessReader:     freshnessReader,
		postRepo:            postRepo,
		fleetRepo:           fleetRepo,
		ledgerRepo:          ledgerRepo,
		clock:               clock,
		releasePendingSince: make(map[string]time.Time),
	}
}

// SetTreasuryReader wires the live-treasury source for the 25% guard. Leaving it unset keeps
// the PURCHASE path fail-closed.
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetTreasuryReader(t probebuy.TreasuryReader) {
	h.treasury = t
}

// SetProbePurchaser wires the price-and-buy port over the existing purchase_ship machinery.
// Leaving it unset keeps the PURCHASE path fail-closed.
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetProbePurchaser(p probebuy.ProbePurchaser) {
	h.purchaser = p
}

// SetHullUpdater wires the narrow, manning-preserving resize seam. Leaving it unset falls
// back to a full Upsert on resize.
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetHullUpdater(u ScoutPostHullUpdater) {
	h.hullUpdater = u
}

// SetEventRecorder wires the captain outbox for the reconcile error-loop event.
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetLiveConfigReader wires the per-tick live-config snapshot source (sp-vwek), making
// the tunable knobs (SizerTunableDefaults) honor `spacetraders tune` on the next tick.
// Leaving it unset keeps every knob launch-frozen (the pre-sp-vwek behavior).
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunMarketFreshnessSizerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunMarketFreshnessSizerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultSizerTickSeconds * time.Second
	}

	result := &RunMarketFreshnessSizerCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Market-freshness auto-sizer starting (tick %s, dry_run=%v)", tick, cmd.DryRun), map[string]interface{}{
		"action":       "freshness_sizer_start",
		"container_id": cmd.ContainerID,
		"dry_run":      cmd.DryRun,
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
			logger.Log("ERROR", fmt.Sprintf("Freshness sizer reconcile failed: %v", err), nil)
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

// noteReconcile records one reconcile pass at the streak checkpoint: a nil err resets the
// streak; a non-nil err repeating identically for DefaultStreakThreshold passes emits the
// coordinator error-loop captain event. Edge-triggered and nil-safe on the recorder.
func (h *RunMarketFreshnessSizerCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// SizerTunableDefaults maps every LIVE-tunable freshness-sizer knob (sp-0z7f) to its
// documented default — the value that applies when neither the live container config
// nor the launch command carries a positive one. The daemon's tune bounds registry
// reads THIS map, so the defaults-of-record stay in this file next to the consts they
// mirror (including today's Admiral retunes: cooldown 1m, max spend 500k). The map's
// KEY SET is also the contract for which keys resolveSizerConfig live-overlays.
func SizerTunableDefaults() map[string]int {
	return map[string]int{
		"max_spend_per_cycle":        defaultSizerMaxSpend,
		"purchase_cooldown_secs":     int(defaultSizerCooldown / time.Second),
		"spend_window_secs":          int(defaultSizerSpendWindow / time.Second),
		"max_probe_fleet":            defaultSizerMaxProbeFleet,
		"max_probes_per_system":      defaultMaxProbesPerSystem,
		"sla_seconds":                defaultSLASeconds,
		"worst_cycle_seconds":        defaultWorstCycleSeconds,
		"cycle_dampening_percent":    defaultCycleDampeningPercent,
		"breach_response_percent":    defaultBreachResponsePercent,
		"release_slack_percent":      defaultReleaseSlackPercent,
		"release_stable_window_secs": defaultReleaseStableWindowSecs,
	}
}

// sizerConfig is the launch command with every default resolved.
type sizerConfig struct {
	DefaultSLA            time.Duration
	Overrides             map[string]time.Duration
	SeedCycle             time.Duration
	MinCycleSamples       int
	WorstCycle            time.Duration
	CycleDampeningPercent int
	MaxProbesPerSystem    int
	BreachResponsePercent int
	ReleaseSlackPercent   int
	ReleaseStableWindow   time.Duration
	Buy                   probebuy.Config
}

// resolveSizerConfig resolves one tick's effective config. live is the tick-start
// snapshot of the container's persisted config column (nil when unwired/unreadable).
// For the TUNABLE knobs (SizerTunableDefaults) a non-nil snapshot is AUTHORITATIVE:
// a positive value is the live value (the launch verb wrote its values into the same
// column, so untuned knobs still read their launch values here), and an absent/zeroed
// key means the documented default — the `tune <key> 0` revert. Only when there is NO
// snapshot does the launch command fill those knobs (fail-safe launch behavior). The
// non-tunable knobs always resolve from the launch command, unchanged.
func resolveSizerConfig(cmd *RunMarketFreshnessSizerCoordinatorCommand, live liveconfig.Snapshot) sizerConfig {
	c := sizerConfig{
		DefaultSLA:            time.Duration(cmd.SLASeconds) * time.Second,
		SeedCycle:             time.Duration(cmd.SeedCycleSeconds) * time.Second,
		MinCycleSamples:       cmd.MinCycleSamples,
		WorstCycle:            time.Duration(cmd.WorstCycleSeconds) * time.Second,
		CycleDampeningPercent: cmd.CycleDampeningPercent,
		MaxProbesPerSystem:    cmd.MaxProbesPerSystem,
		BreachResponsePercent: cmd.BreachResponsePercent,
		ReleaseSlackPercent:   cmd.ReleaseSlackPercent,
		ReleaseStableWindow:   time.Duration(cmd.ReleaseStableWindowSecs) * time.Second,
		Buy: probebuy.Config{
			MaxProbeFleet:    cmd.MaxProbeFleet,
			MaxSpendPerCycle: cmd.MaxSpendPerCycle,
			PurchaseCooldown: time.Duration(cmd.PurchaseCooldownSecs) * time.Second,
			SpendWindow:      time.Duration(cmd.SpendWindowSecs) * time.Second,
		},
	}
	if live != nil {
		c.DefaultSLA = time.Duration(live.PositiveIntOrZero("sla_seconds")) * time.Second
		c.WorstCycle = time.Duration(live.PositiveIntOrZero("worst_cycle_seconds")) * time.Second
		c.CycleDampeningPercent = live.PositiveIntOrZero("cycle_dampening_percent")
		c.MaxProbesPerSystem = live.PositiveIntOrZero("max_probes_per_system")
		c.BreachResponsePercent = live.PositiveIntOrZero("breach_response_percent")
		c.ReleaseSlackPercent = live.PositiveIntOrZero("release_slack_percent")
		c.ReleaseStableWindow = time.Duration(live.PositiveIntOrZero("release_stable_window_secs")) * time.Second
		c.Buy.MaxProbeFleet = live.PositiveIntOrZero("max_probe_fleet")
		c.Buy.MaxSpendPerCycle = live.PositiveIntOrZero("max_spend_per_cycle")
		c.Buy.PurchaseCooldown = time.Duration(live.PositiveIntOrZero("purchase_cooldown_secs")) * time.Second
		c.Buy.SpendWindow = time.Duration(live.PositiveIntOrZero("spend_window_secs")) * time.Second
	}
	if c.DefaultSLA <= 0 {
		c.DefaultSLA = defaultSLASeconds * time.Second
	}
	if c.SeedCycle <= 0 {
		c.SeedCycle = defaultSeedCycleSeconds * time.Second
	}
	if c.MinCycleSamples <= 0 {
		c.MinCycleSamples = defaultMinCycleSamples
	}
	if c.WorstCycle <= 0 {
		c.WorstCycle = defaultWorstCycleSeconds * time.Second
	}
	if c.CycleDampeningPercent <= 0 {
		c.CycleDampeningPercent = defaultCycleDampeningPercent
	}
	if c.MaxProbesPerSystem <= 0 {
		c.MaxProbesPerSystem = defaultMaxProbesPerSystem
	}
	if c.BreachResponsePercent <= 0 {
		c.BreachResponsePercent = defaultBreachResponsePercent
	}
	if c.ReleaseSlackPercent <= 0 {
		c.ReleaseSlackPercent = defaultReleaseSlackPercent
	}
	if c.ReleaseStableWindow <= 0 {
		c.ReleaseStableWindow = defaultReleaseStableWindowSecs * time.Second
	}
	if c.Buy.MaxProbeFleet <= 0 {
		c.Buy.MaxProbeFleet = defaultSizerMaxProbeFleet
	}
	if c.Buy.MaxSpendPerCycle <= 0 {
		c.Buy.MaxSpendPerCycle = defaultSizerMaxSpend
	}
	if c.Buy.PurchaseCooldown <= 0 {
		c.Buy.PurchaseCooldown = defaultSizerCooldown
	}
	if c.Buy.SpendWindow <= 0 {
		c.Buy.SpendWindow = defaultSizerSpendWindow
	}
	c.Overrides = make(map[string]time.Duration, len(cmd.SystemSLAOverrides))
	for system, secs := range cmd.SystemSLAOverrides {
		if secs > 0 {
			c.Overrides[system] = time.Duration(secs) * time.Second
		}
	}
	return c
}

func (c sizerConfig) slaFor(system string) time.Duration {
	if sla, ok := c.Overrides[system]; ok {
		return sla
	}
	return c.DefaultSLA
}

// liveConfigSnapshot takes the tick's live-config snapshot (sp-vwek). A nil reader
// (not wired — tests, minimal boots) or a read error yields nil, which
// resolveSizerConfig treats as "run this tick entirely on the launch command" — the
// fail-safe launch behavior, never a half-applied config. The read is logged, not
// fatal: a transient DB gap must not kill the reconcile loop.
func (h *RunMarketFreshnessSizerCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Live config unreadable — this tick runs on launch values: %v", err), nil)
		return nil
	}
	return snap
}

// ReconcileOnce is one reconcile pass — the unit the tests drive directly. It MEASURES the
// per-system freshness demand, SIZES each market-bearing system's standing post, and BUYS
// one probe when the aggregate demand outruns supply and every guard passes. The tick runs
// entirely on the live-config snapshot taken here; a knob tuned mid-tick lands next tick.
func (h *RunMarketFreshnessSizerCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)
	cfg := resolveSizerConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	snapshots, err := h.freshnessReader.SystemsFreshness(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to read system freshness: %w", err)
	}
	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	postBySystem := indexPostsBySystem(posts)
	globalCycle := aggregateMeasuredCycleSeconds(snapshots, cfg.MinCycleSamples)

	totalDemand := 0
	marketBearing := make(map[string]bool, len(snapshots))
	// neediest{System,Gap} tracks the market-bearing system with the LARGEST unmet probe gap
	// (desired − current) — the demand-proximal buy TARGET (sp-hej4). The aggregate buy lands one
	// undedicated probe for the reconciler to relay; naming the neediest system lets the purchaser
	// spawn it at the probe-yard NEAREST that system instead of the home yard. Empty (no positive
	// gap) is the no-target home-yard path.
	neediestSystem := ""
	neediestGap := 0
	for _, snap := range snapshots {
		if snap.MarketCount <= 0 {
			continue
		}
		marketBearing[snap.SystemSymbol] = true
		sla := cfg.slaFor(snap.SystemSymbol)
		cycle := resolveCycleSeconds(snap, globalCycle, cfg)
		existing := postBySystem[snap.SystemSymbol]
		current := 0
		fullyManned := false
		if existing != nil {
			current = existing.HullBudget()
			fullyManned = existing.IsFullyManned() // sp-iupr issue 3a: gates the empirical-age sanity floor.
		}
		desired := h.desiredHulls(releaseKey(cmd.PlayerID.Value(), snap.SystemSymbol), current, fullyManned, snap, sla, cycle, cfg)
		totalDemand += desired
		if gap := desired - current; gap > neediestGap {
			neediestGap = gap
			neediestSystem = snap.SystemSymbol
		}
		if !cmd.DryRun {
			h.applyPost(ctx, cmd, existing, snap.SystemSymbol, desired, sla)
		}
	}

	// AUTO-RESIZE / RELEASE: a standing post whose system dropped out of the census (its
	// markets retired) is removed, freeing its probes back to the pool.
	retired := h.retireMarketlessPosts(ctx, cmd, posts, marketBearing)

	// AGGREGATE BUY: one guarded probe buy when total freshness demand outruns supply.
	supply, err := h.scoutSupply(ctx, cmd)
	if err != nil {
		return err
	}
	buyer := probebuy.NewGuardedProbeBuyer(h.treasury, h.purchaser, h.ledgerRepo, h.clock, cfg.Buy)
	// Demand-proximal buy hint (sp-hej4): spawn the probe at the yard nearest the neediest system.
	// The sizer has no per-hop tuning knob of its own, so it applies the shared default penalty
	// (proximity-first). An empty neediestSystem is the home-yard path — unchanged.
	target := probebuy.ProbeTarget{System: neediestSystem, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits}
	outcome := buyer.MaybeBuy(ctx, cmd.PlayerID, totalDemand, supply, cmd.DryRun, target)

	logger.Log("INFO", fmt.Sprintf("Freshness sizer cycle: %d market-bearing systems, aggregate demand %d probes, supply %d, %d retired — %s", len(marketBearing), totalDemand, supply, retired, outcome.Reason), map[string]interface{}{
		"action":           "freshness_sizer_cycle",
		"systems":          len(marketBearing),
		"aggregate_demand": totalDemand,
		"supply":           supply,
		"retired":          retired,
		"dry_run":          cmd.DryRun,
		"outcome":          outcome.Reason,
	})
	if outcome.Bought {
		logger.Log("INFO", fmt.Sprintf("Freshness sizer bought probe %s for %d at %s (demand %d > supply %d) — landed undedicated, reconciler will relay", outcome.Symbol, outcome.Price, outcome.Yard, totalDemand, supply), map[string]interface{}{
			"action":      "freshness_probe_purchased",
			"ship_symbol": outcome.Symbol,
			"price":       outcome.Price,
			"yard":        outcome.Yard,
		})
	}
	return nil
}

// computeTarget is the per-system SIZE the sizer aims a post at, before release pacing. It runs
// an ordered pipeline: (1) the cycle-driven MODEL, where telemetry noise enters; (2) the
// sp-iupr issue-3b market-count CLAMP that bounds the noise; (3) the sp-tor9 CIRCUIT-OBSERVED
// BREACH RESPONSE that sizes a trusted, fully-manned post from its measured circuit; then the
// floor-of-1 and per-system cap.
//
// The two age-driven branches are deliberately DISJOINT (they must never collide):
//   - a TELEMETRY-STARVED system (its probes have not produced MinCycleSamples scan intervals)
//     has an age that reflects a MANNING failure, NOT a capacity shortfall — raising demand off
//     it only strands more probes (the issue-1 pathology). It stays on the static MARKET-COUNT
//     model (modelTarget) and is NEVER raised off the age;
//   - a TRUSTED, FULLY MANNED system is the OPPOSITE case: its worst-case age at the CURRENT hull
//     count is an honest circuit measurement, so the breach response sizes it straight from that
//     circuit. Gated on !starved, so it can never fire for the starved case above.
func computeTarget(snap domainScouting.SystemFreshnessSnapshot, sla, cycle time.Duration, cfg sizerConfig, current int, fullyManned bool) (target int, starved bool) {
	starved = snap.CycleSamples < cfg.MinCycleSamples

	// 1. MODEL — the cycle-driven estimate (starved: static market-count; trusted: sp-orgp
	//    closed loop corrected by empirical age).
	target = modelTarget(snap, sla, cycle, starved)

	// 2. MARKET-COUNT CLAMP (sp-iupr issue 3b) — bound the noise-driven model to what this
	//    market count could justify at the worst plausible cycle, capping a small-market system a
	//    noisy-high cycle over-sized (ZY16: 3 markets read as 6). The circuit response below is
	//    ground truth and is applied AFTER, so it may exceed this ceiling.
	target = domainScouting.ClampToMarketCount(target, snap.MarketCount, cfg.WorstCycle, sla)

	// 3. CIRCUIT-OBSERVED BREACH RESPONSE (sp-tor9) — supersedes the issue-3a +1 sanity floor with
	//    one coherent breach path. A TRUSTED, FULLY MANNED post's worst-case age at its CURRENT
	//    hull count directly measures its circuit period; the measured-cycle model cannot, because
	//    the pooled inter-scan interval deflates with probe count, collapsing the static estimate
	//    toward 1 on exactly the high-market systems that need many probes. Size to
	//    ceil(current × age/sla) (scaled by the breach-response knob): PROPORTIONAL to the breach
	//    on the way up (a 158min-at-60min post jumps toward coverage in one resize, not eight),
	//    and — because current × age ≈ markets × perMarketHop is CONSERVED as hulls change — a
	//    STABLE fixpoint at steady state (raising to it drops the age so the next tick re-derives
	//    the same target: no release-flap). It only RAISES here: a non-breaching post's circuit
	//    target is ≤ current, so max() leaves the model target untouched. DISJOINT from the starved
	//    branch by !starved (issue 1: a starved post's age is a manning signal, never a capacity
	//    one); the fullyManned gate keeps the age an HONEST reading — a partially-manned post's age
	//    reflects fewer working probes than its budget, so sizing off it would over-count.
	if !starved && fullyManned {
		effectiveAge := breachResponseAge(snap.OldestAgeSeconds, cfg.BreachResponsePercent)
		if circuitTarget := domainScouting.CircuitRequiredHulls(current, effectiveAge, sla); circuitTarget > target {
			target = circuitTarget
		}
	}

	if target < 1 {
		target = 1
	}
	if target > cfg.MaxProbesPerSystem {
		target = cfg.MaxProbesPerSystem
	}
	return target, starved
}

// breachResponseAge scales the observed worst-case age by the breach-response aggressiveness knob
// (sp-tor9) before it is fed to the circuit model — percent > 100 sizes for a proportionally
// WORSE effective age (equivalently, a tighter effective SLA), buying headroom against a circuit
// that under-measures in practice; 100 is the exact observed circuit; the coordinator's default
// chain guarantees a positive percent so this never zeroes the age.
func breachResponseAge(oldestAgeSeconds float64, breachResponsePercent int) time.Duration {
	scaledSeconds := oldestAgeSeconds * float64(breachResponsePercent) / 100
	return time.Duration(scaledSeconds * float64(time.Second))
}

// modelTarget is the cycle-driven size estimate before the issue-3 clamp and sanity floor. A
// telemetry-starved system uses the static market-count model (RequiredHulls) and is NOT age-
// raised (issue 1: its age is a manning signal); a trusted system uses the sp-orgp closed loop
// corrected by its empirical worst-case age.
func modelTarget(snap domainScouting.SystemFreshnessSnapshot, sla, cycle time.Duration, starved bool) int {
	if starved {
		return domainScouting.RequiredHulls(snap.MarketCount, cycle, sla)
	}
	age := time.Duration(snap.OldestAgeSeconds * float64(time.Second))
	return domainScouting.FreshnessRequiredHulls(snap.MarketCount, cycle, sla, age)
}

// desiredHulls applies release PACING on top of computeTarget so the fleet neither flaps at
// the SLA line nor thrashes the shared satellite pool. A raise or a fresh declaration lands
// immediately (freshness is the priority). Shedding a surplus (target < current) is tiered:
//   - a TELEMETRY-STARVED oversized post, or a TRUSTED post COMFORTABLY under its SLA (age
//     below the release-slack line), sheds ONE probe immediately — the starved post's age
//     cannot hold it (sp-iupr bug 1), the comfortable post has the margin to spare (the
//     original sp-orgp hysteresis);
//   - a TRUSTED post in the WARM band (under the SLA but past the slack line) whose measured
//     requirement fell below its budget sheds one probe only once the surplus has been STABLE
//     for the release window (sp-iupr bug 2) — a one-cycle demand dip clears the pending
//     window and sheds nothing, so the sizer never releases a hull the next tick re-buys.
//
// Every shed is one step, floored at the measured requirement (never below what the post
// needs), and lands as a resize-DOWN the scout reconciler un-mans — returning the hull to the
// shared pool where the frontier coordinator can claim it, never sold or retired.
func (h *RunMarketFreshnessSizerCoordinatorHandler) desiredHulls(key string, current int, fullyManned bool, snap domainScouting.SystemFreshnessSnapshot, sla, cycle time.Duration, cfg sizerConfig) int {
	target, starved := computeTarget(snap, sla, cycle, cfg, current, fullyManned)

	if current == 0 || target >= current {
		h.clearReleasePending(key) // declaring, raising, or holding — no surplus to debounce.
		return target
	}

	// Surplus (target < current). Comfortably-fresh trusted posts and starved posts shed now.
	slackSeconds := sla.Seconds() * float64(cfg.ReleaseSlackPercent) / 100
	if starved || snap.OldestAgeSeconds < slackSeconds {
		h.clearReleasePending(key)
		return stepDownToward(current, target)
	}

	// Warm surplus: shed one probe only after it has held for the stable window (debounced).
	if h.releasePendingElapsed(key) < cfg.ReleaseStableWindow {
		return current // pending, not yet stable — hold this tick.
	}
	h.markReleasePending(key) // reset the window so warm sheds pace at one probe per window.
	return stepDownToward(current, target)
}

// stepDownToward sheds exactly one probe, never below the target (the measured requirement).
func stepDownToward(current, target int) int {
	stepDown := current - 1
	if stepDown < target {
		stepDown = target
	}
	return stepDown
}

// releaseKey scopes the warm-surplus debounce per player and system (matching the scout
// reconciler's driftKey shape) so the singleton handler tracks each post independently.
func releaseKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// releasePendingElapsed records the FIRST tick a warm post's surplus was seen and returns how
// long it has been pending. A key already tracked keeps its original timestamp — the window
// accumulates across ticks until the shed fires or the surplus resolves (clearReleasePending).
func (h *RunMarketFreshnessSizerCoordinatorHandler) releasePendingElapsed(key string) time.Duration {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	if h.releasePendingSince == nil {
		h.releasePendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.releasePendingSince[key]
	if !ok {
		h.releasePendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// markReleasePending (re)starts a post's stable window at now — called right after a warm
// shed so the next shed must re-earn the full window (paces releases at one probe per window).
func (h *RunMarketFreshnessSizerCoordinatorHandler) markReleasePending(key string) {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	if h.releasePendingSince == nil {
		h.releasePendingSince = make(map[string]time.Time)
	}
	h.releasePendingSince[key] = h.clock.Now()
}

// clearReleasePending forgets a post's pending window — called the moment its surplus
// resolves (target rose back to the budget, or it shed by the immediate path), so a later
// dip below the budget starts a FRESH window rather than inheriting a stale one.
func (h *RunMarketFreshnessSizerCoordinatorHandler) clearReleasePending(key string) {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	delete(h.releasePendingSince, key)
}

// applyPost reconciles the desired-state post for one market-bearing system: declare (new),
// promote (a sweep_once that turned out to hold markets), or resize (an existing standing
// post). Resizes prefer the narrow hull-update seam so live manning is preserved.
func (h *RunMarketFreshnessSizerCoordinatorHandler) applyPost(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, existing *domainScouting.ScoutPost, system string, desired int, sla time.Duration) {
	logger := common.LoggerFromContext(ctx)

	if existing == nil {
		post := &domainScouting.ScoutPost{
			PlayerID:        cmd.PlayerID.Value(),
			SystemSymbol:    system,
			FreshnessTarget: sla,
			Kind:            domainScouting.PostKindStanding,
			Hulls:           desired,
			CreatedAt:       h.clock.Now(),
		}
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to declare standing freshness post %s: %v", system, err), nil)
			return
		}
		logger.Log("INFO", fmt.Sprintf("Declared standing freshness post %s sized to %d probes (SLA %s) — reconciler will man and partition", system, desired, sla), map[string]interface{}{
			"action": "freshness_post_declared", "system_symbol": system, "hulls": desired,
		})
		return
	}

	if existing.Kind != domainScouting.PostKindStanding {
		// PROMOTION: a sweep_once post whose system turned out to hold markets becomes a
		// standing freshness post, sized to the model, with its manning preserved.
		existing.Kind = domainScouting.PostKindStanding
		existing.Hulls = desired
		existing.FreshnessTarget = sla
		if err := h.postRepo.Upsert(ctx, existing); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to promote %s to a standing freshness post: %v", system, err), nil)
			return
		}
		logger.Log("INFO", fmt.Sprintf("Promoted %s from sweep_once to a standing freshness post sized to %d probes", system, desired), map[string]interface{}{
			"action": "freshness_post_promoted", "system_symbol": system, "hulls": desired,
		})
		return
	}

	if existing.HullBudget() == desired && existing.FreshnessTarget == sla {
		return // stable — nothing to do.
	}

	// RESIZE. Prefer the narrow hull-update seam (manning-preserving, clobber-free) when the
	// SLA is unchanged; a SLA change needs the full row so it goes through Upsert.
	if h.hullUpdater != nil && existing.FreshnessTarget == sla {
		if err := h.hullUpdater.UpdateHulls(ctx, cmd.PlayerID.Value(), system, desired); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to resize freshness post %s to %d: %v", system, desired, err), nil)
			return
		}
	} else {
		existing.Hulls = desired
		existing.FreshnessTarget = sla
		if err := h.postRepo.Upsert(ctx, existing); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to resize freshness post %s to %d: %v", system, desired, err), nil)
			return
		}
	}
	logger.Log("INFO", fmt.Sprintf("Resized freshness post %s to %d probes (SLA %s)", system, desired, sla), map[string]interface{}{
		"action": "freshness_post_resized", "system_symbol": system, "hulls": desired,
	})
}

// retireMarketlessPosts removes every STANDING post whose system dropped out of the census
// (its markets retired), freeing its probes back to the pool. sweep_once posts are left to
// the frontier coordinator. Returns the count retired.
func (h *RunMarketFreshnessSizerCoordinatorHandler) retireMarketlessPosts(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, posts []*domainScouting.ScoutPost, marketBearing map[string]bool) int {
	if cmd.DryRun {
		return 0
	}
	// FAIL-SAFE (the enumerate-the-rejected-class lesson): never mass-retire on an EMPTY
	// census. A cold start, an era gap, or a transient read that surfaced zero market-bearing
	// systems would otherwise remove EVERY standing post in one tick — a fleet-killer. With
	// no census to compare against, retire nothing and wait for it to repopulate.
	if len(marketBearing) == 0 {
		return 0
	}
	logger := common.LoggerFromContext(ctx)
	retired := 0
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding || marketBearing[post.SystemSymbol] {
			continue
		}
		if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to retire marketless freshness post %s: %v", post.SystemSymbol, err), nil)
			continue
		}
		retired++
		logger.Log("INFO", fmt.Sprintf("Retired freshness post %s — its markets are gone, probes freed to the pool", post.SystemSymbol), map[string]interface{}{
			"action": "freshness_post_retired", "system_symbol": post.SystemSymbol,
		})
	}
	return retired
}

// scoutSupply counts the scout-probe SUPPLY: every scout-type hull the player owns that is
// available to scouting (undedicated or scout-tagged), in ANY nav state — idle, in-flight,
// or manning. Counting in-flight/manning probes as supply is what stops the coordinator
// over-buying while a probe it already owns is en route to a slot (the sp-njwy lesson).
func (h *RunMarketFreshnessSizerCoordinatorHandler) scoutSupply(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) (int, error) {
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list fleet: %w", err)
	}
	n := 0
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != freshnessScoutFleetTag {
			continue
		}
		n++
	}
	return n, nil
}

// resolveCycleSeconds picks the per-market cycle for a system: its own MEASURED cycle when
// it has cleared the sample floor, else the fleet-wide median of trusted measurements, else
// the seed default. This keeps the cycle EMPIRICAL (never a bare constant) while degrading
// gracefully before telemetry exists. A system's own measurement is DAMPENED toward the fleet-
// wide median (sp-iupr issue 3c): per-system cycle telemetry is noisy, so shrinking each
// reading toward the pooled robust estimate makes equal-market systems converge on the same
// target instead of diverging on noise. A single trusted system (median == own) or a 0%
// dampening is a no-op, so this never perturbs the single-system or launch-frozen paths.
func resolveCycleSeconds(snap domainScouting.SystemFreshnessSnapshot, globalCycleSeconds float64, cfg sizerConfig) time.Duration {
	seconds := cfg.SeedCycle.Seconds()
	switch {
	case snap.CycleSamples >= cfg.MinCycleSamples && snap.MeasuredCycleSeconds > 0:
		seconds = domainScouting.DampenedCycleSeconds(snap.MeasuredCycleSeconds, globalCycleSeconds, cfg.CycleDampeningPercent)
	case globalCycleSeconds > 0:
		seconds = globalCycleSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

// aggregateMeasuredCycleSeconds is the fleet-wide median of the per-system measured cycles
// that cleared the sample floor — the fallback for a market-bearing system that does not yet
// have enough samples of its own. 0 ⇒ no system has a trusted measurement yet.
func aggregateMeasuredCycleSeconds(snapshots []domainScouting.SystemFreshnessSnapshot, minSamples int) float64 {
	var trusted []float64
	for _, snap := range snapshots {
		if snap.CycleSamples >= minSamples && snap.MeasuredCycleSeconds > 0 {
			trusted = append(trusted, snap.MeasuredCycleSeconds)
		}
	}
	if len(trusted) == 0 {
		return 0
	}
	sort.Float64s(trusted)
	mid := len(trusted) / 2
	if len(trusted)%2 == 1 {
		return trusted[mid]
	}
	return (trusted[mid-1] + trusted[mid]) / 2
}

func indexPostsBySystem(posts []*domainScouting.ScoutPost) map[string]*domainScouting.ScoutPost {
	index := make(map[string]*domainScouting.ScoutPost, len(posts))
	for _, post := range posts {
		index[post.SystemSymbol] = post
	}
	return index
}
