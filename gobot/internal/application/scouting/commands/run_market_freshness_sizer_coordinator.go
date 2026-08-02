// Package commands: the market-freshness auto-sizer. A standing daemon
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
//   - the empirical (value-weighted) P90 market age is the CLOSED-LOOP ground truth (sp-r57g,
//     superseding the tail-dominated MAX): a system whose P90 breaches its SLA has its demand
//     RAISED beyond the static model, a comfortably-fresh one is allowed to RELEASE a probe
//     (hysteresis prevents flapping), and the stale tail beyond the percentile is TOLERATED;
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
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// freshnessScoutFleetTag is the dedication tag scouting hulls carry. An idle/manning
	// satellite counts toward supply only when it is undedicated or already scout-tagged —
	// the same first-line poach filter the reconciler and frontier coordinator use.
	freshnessScoutFleetTag = "scout"

	// Config defaults (RULINGS #5: every operational value is a flag/config key, filled
	// here only when the launch config leaves it unset).
	defaultSizerTickSeconds = 60
	defaultSLASeconds       = 3600 // 1h freshness SLA (the global fallback when a system carries no activity signal)
	// Per-activity freshness SLAs: the freshness SLA is a FUNCTION OF market ACTIVITY
	// STATE, not one global value. A WEAK market tolerates far staler prices than a STRONG one, so a
	// single global SLA over-serves the weak and under-serves the tight. The sizer partitions each
	// system's markets by activity, sizes each cohort against ITS OWN SLA (ceil(cohortMarkets × cycle
	// / activitySLA)), and SUMS the per-cohort probe needs. Defaults were chosen ~0.75× the trade-
	// ranker's age caps (documented here — NOT a runtime dependency on sp-t5sh5's cap table). Defined
	// ONCE here (RULINGS #5), surfaced through SizerTunableDefaults, and live-tunable via
	// sla_seconds_{weak,restricted,growing,strong}. An unknown/null activity is sized at the
	// RESTRICTED default; a system whose EVERY market lacks an activity signal falls back to the
	// single global defaultSLASeconds (byte-identical to pre-sp-j4kjv).
	defaultSLAWeakSeconds       = 21600 // 360 min — weak markets move slowly, tolerate the stalest prices
	defaultSLARestrictedSeconds = 8100  // 135 min — also the unknown/null default
	defaultSLAGrowingSeconds    = 2700  // 45 min
	defaultSLAStrongSeconds     = 1320  // 22 min — strong markets move fastest, need the freshest prices
	defaultSeedCycleSeconds     = 180   // seeded per-market cycle until telemetry exists
	defaultMinCycleSamples      = 3     // min consecutive-interval samples to trust a measured cycle
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
	// defaultTargetPercentile is the sp-r57g PERCENTILE-age target: a system breaches iff its
	// MEASURED P90 market age exceeds the SLA, NOT iff its MAX does. This SUPERSEDES sp-tor9's
	// max-age premise (the tail-dominated OldestAgeSeconds) — the max is unachievable for big
	// systems and mis-allocated probes to the stalest 1-10% while arb thrived on the fresh bulk.
	// 90 explicitly TOLERATES the stale tail (DA78: P90≈62 vs max≈167), bounding big-system demand
	// to the achievable P90. The closed-loop measurement + proportional CircuitRequiredHulls
	// response machinery is REUSED from sp-tor9 verbatim; only the metric feeding it changed.
	// 100 recovers the exact pre-sp-r57g max-age behavior (the live rollback lever).
	defaultTargetPercentile = 90
	// value_weighted is an int-mode knob (the tune registry stores ints, and 0 means "revert to
	// default", so a plain 0/1 bool cannot express an OFF that survives a revert). 2 = ON (the
	// value-weighted percentile, the documented default when a weight source exists), 1 = OFF
	// (a plain count percentile). Absent/0 resolves to the launch value, then to defaultValueWeighted.
	valueWeightedModeOff = 1
	valueWeightedModeOn  = 2
	// defaultValueWeighted is ON: the census carries a per-market Σ(trade_volume × price) weight,
	// so the percentile is value-weighted by default — a high-value stale market pulls the
	// percentile up (arb core stays tight) while a low-traffic straggler stays in the tolerated
	// tail. `tune value_weighted 1` disables it live (falls back to a plain count percentile).
	defaultValueWeighted = true
	// defaultDemandHalfLifeSeconds is the EWMA half-life of the realized-sink-demand
	// weight: a SELL leg this many seconds old counts half as much as one just realized. 3h matches
	// the lane-decay priors — long enough that a steady arb lane holds a stable weight, short enough
	// that a lane the fleet abandons decays off within a shift so its markets stop winning scarce
	// probes. Tunable-only via demand_ewma_half_life_secs (no launch field, like the sp-j4kjv
	// per-activity SLAs); `tune demand_ewma_half_life_secs 0` reverts to this default.
	defaultDemandHalfLifeSeconds = 10800
	// demandWindowHalfLives bounds the telemetry READ window at this many half-lives — after 6 a
	// sale retains 2^-6 (~1.5%) of its weight, negligible, so reading further back only costs I/O.
	// It is a derived constant, not a knob: the half-life is the single tuning surface (RULINGS #5).
	demandWindowHalfLives      = 6
	defaultReleaseSlackPercent = 60 // release a feedback probe only below this % of the SLA (hysteresis)
	// defaultReleaseStableWindowSecs is how long a WARM post's measured surplus (desired <
	// current, under the SLA but past the slack line) must hold before one probe is shed to
	// the pool (sp-iupr bug 2). It debounces the shed so a one-cycle demand dip never releases
	// a hull the next tick re-buys; the buy cooldown is the second half of that anti-thrash.
	defaultReleaseStableWindowSecs = 300
	defaultSizerMaxProbeFleet      = 40              // total satellite cap
	defaultSizerMaxSpend           = 500000          // max probe spend within the trailing spend window (Admiral 2026-07-15: ~17 probes/hr ramp; 25%-treasury + fleet cap still bind)
	defaultSizerCooldown           = 1 * time.Minute // Admiral 2026-07-15: fast ramp; spend window + treasury/fleet caps still bound total buys
	defaultSizerSpendWindow        = 1 * time.Hour
	// defaultReservedFrontierFloor is the sp-iopd MVP reserved FRONTIER floor: N probes the
	// freshness sizer treats as UNAVAILABLE. The sizer holds its AGGREGATE demand against
	// (supply − N) and RELEASES the surplus down to that reduced effective pool, so the
	// frontier expansion coordinator (sp-8w89/sp-rjgr) keeps its depth probes no matter how
	// high freshness demand climbs — breaking the self-defeating loop where depth discovers
	// markets, freshness eats the probes, and depth starves. The per-system computeTarget is
	// UNCHANGED; the floor is a separate aggregate ceiling, applied via the same resize-DOWN
	// release seam sp-iupr uses (the freed hulls land undedicated in the shared pool the
	// frontier claims — never sold or retired). 0 (the default) is EXACT pre-sp-iopd behavior:
	// the floor is OFF until deployed, opt-in via `tune reserved_frontier_floor <N>` (recommended
	// production value ~ the frontier's max_depth_pathfinders, e.g. 6). The FULL global probe
	// allocator (one owner, total demand = freshness+frontier, a single global cap replacing the
	// two double-drawing caps, priority grants + relaxed-SLA graceful degradation, and the
	// count-only-OWN-probes fix so freed hulls actually reach depth) is the deferred follow-up;
	// this static floor is the surgical de-risk.
	defaultReservedFrontierFloor = 0
	// defaultHoldUnscannedMarketPosts is the sp-u8jc/sp-gucu bootstrap-catch-22 fix flag
	// (int-mode, 0 = OFF = today's retire-as-gone behavior, byte-identical). The freshness
	// census keys "markets" on SCANNED market_data rows, so a CHARTED dense hub (its waypoints
	// carry the MARKETPLACE trait) that has never been scanned reads as 0 markets and its
	// standing post is retired "its markets are gone" — the probe never goes, so the system
	// never gets its first scan, so it stays dark forever (the depth-wall root the sp-u8jc relay
	// and the probe-buyer are armed to break but cannot while the post keeps being retired). 1 ⇒
	// a charted-with-marketplaces-but-unscanned system is treated as NEEDING AN INITIAL SCAN: its
	// post is HELD (not retired) and it counts as one-probe initial-scan demand, so the coordinator
	// mans it (in-system idle, sp-u8jc cross-system relay, or a probe buy), the probe scans it, and
	// it joins the normal freshness rotation. Genuinely empty systems (no marketplace waypoints)
	// still retire. Live-tunable (SizerTunableDefaults); requires the daemon to have wired the
	// charted-marketplace reader (SetChartedMarketplaceReader).
	defaultHoldUnscannedMarketPosts = 0
	// SENSING SCOPE. The API budget is server-capped and cannot be raised, while the charted map
	// grows without bound — so sensing every market-bearing system spends a fixed budget over an
	// ever-larger set and per-system freshness decays as the map grows. These three bound the set
	// the sizer sizes against: the fleet's operating footprint plus a fixed discovery allowance.
	//
	// defaultScanFootprintRetentionSecs is how long a system stays in the footprint after its last
	// realized trade. It is deliberately LONGER than any freshness SLA and longer than a market
	// takes to recover: the fleet stops trading a lane precisely BECAUSE it crushed it, so a
	// shorter retention would evict a crushed market exactly while it reverts, and it could never
	// return — nothing would scan it, so nothing would trade it. 24h clears the ~8-9h full
	// reversion and the 12-24h dead window of a crushed lane with margin.
	defaultScanFootprintRetentionSecs = 86400
	// defaultScanDiscoveryAllowance is how many OUT-of-footprint systems stay under a standing
	// watch so a narrowed scope can still grow — the price paid to keep options. Each slot is ONE
	// probe on the richest untraded system, so the whole allowance costs exactly this many probes.
	// Slots rotate on success: a discovery system the fleet trades enters the footprint, is sized
	// by the full model, and frees its slot. Roving discovery over UNCHARTED space stays the
	// frontier coordinator's job.
	defaultScanDiscoveryAllowance = 8
	// defaultScanDiscoverySLASeconds is the freshness target stamped on a discovery post. It is
	// deliberately loose: a one-probe watch on a system the fleet does not trade cannot hold a
	// trading SLA, and a post stamped with an unreachable target reads as permanently breaching —
	// which would raise its demand and put the scout reconciler's manning watchdog on its tour.
	defaultScanDiscoverySLASeconds = 21600
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

// ChartedMarketplaceReader reports, per system, how many CHARTED marketplace waypoints it holds
// (a non-fuel waypoint carrying the MARKETPLACE trait) — regardless of whether the player has
// scanned those markets' prices yet. It is the "has a marketplace" signal the sp-u8jc/sp-gucu
// hold-unscanned-posts guard keys on: a system present here but ABSENT from the SCANNED freshness
// census (SystemsFreshness, which counts only waypoints that already have market_data) is charted-
// but-unscanned — it NEEDS an initial scan, NOT retirement. It REUSES the sp-gucu/sp-pvw3 census
// primitive the dark-market scanner is built on (MarketRepositoryGORM.ChartedMarketSystemCounts:
// era-scoped, fuel-excluded, MARKETPLACE-trait-filtered) so charting and this signal share one
// notion of "a market" and cannot drift — no new query. Optional-injection: nil (unwired) keeps
// the retire-as-gone behavior byte-identical, and the hold_unscanned_market_posts knob gates it
// even when wired.
type ChartedMarketplaceReader interface {
	ChartedMarketSystemCounts(ctx context.Context) (map[string]int, error)
}

// TourTelemetryReader is the REUSED tour-telemetry read: the same
// trading.TourTelemetryRepository.ListByPlayer the auto-outfit coordinator reads, scoped to the
// player and a rolling window. The freshness sizer folds its SELL legs into a per-sink realized-
// demand weight that supersedes the intrinsic Σ(trade_volume × price) census weight as the PRIMARY
// input to the value-weighted freshness percentile (a market never traded keeps its intrinsic
// prior). Optional-injection: nil (unwired) leaves every market on its intrinsic weight.
// Structurally satisfied by *persistence.TourTelemetryRepositoryGORM
// (the read is REUSED, not forked — the repository enforces the player_id row filter).
type TourTelemetryReader interface {
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
}

// RunMarketFreshnessSizerCoordinatorCommand launches the standing coordinator for a player.
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

	SLASeconds            int            // default freshness SLA in seconds
	SystemSLAOverrides    map[string]int // per-system SLA override (seconds)
	SeedCycleSeconds      int            // seeded per-market cycle until telemetry exists
	MinCycleSamples       int            // min samples to trust a measured cycle
	WorstCycleSeconds     int            // worst-plausible per-market cycle bounding the market-count clamp (sp-iupr issue 3b)
	CycleDampeningPercent int            // shrinkage % of a system's own cycle toward the fleet median (sp-iupr issue 3c)
	MaxProbesPerSystem    int            // per-system hull cap
	BreachResponsePercent int            // aggressiveness of the circuit-observed breach response (sp-tor9); 100 = exact measured circuit
	// TargetPercentile (sp-r57g) is the age percentile the sizer sizes against (default 90): a
	// system breaches iff its MEASURED P90 market age exceeds the SLA, not iff its MAX does. 100
	// recovers the pre-sp-r57g max-age behavior. Live-tunable (SizerTunableDefaults).
	TargetPercentile int
	// ValueWeightedMode (sp-r57g) toggles value-weighting of the percentile: valueWeightedModeOn
	// (2, the default) weights by per-market Σ(trade_volume × price); valueWeightedModeOff (1) is a
	// plain count percentile. 0 (unset) resolves to the default ON. Live-tunable (SizerTunableDefaults).
	ValueWeightedMode       int
	ReleaseSlackPercent     int // release hysteresis: shed a probe only below this % of the SLA
	ReleaseStableWindowSecs int // a warm surplus must hold this long before one probe is shed

	// ReservedFrontierFloor (sp-iopd) is the count of probes the sizer treats as reserved for
	// the frontier: it holds its aggregate demand against (supply − this) and releases the
	// surplus. 0 → the floor is off (pre-sp-iopd behavior). Live-tunable (SizerTunableDefaults).
	ReservedFrontierFloor int

	// HoldUnscannedMarketPosts (sp-u8jc/sp-gucu) is the int-mode bootstrap-catch-22 flag: >0 ⇒
	// a charted-with-marketplaces-but-unscanned system is HELD (its post is not retired) and
	// counted as one-probe initial-scan demand; 0 (default) ⇒ today's retire-as-gone behavior,
	// byte-identical. Live-tunable (SizerTunableDefaults). Inert unless the daemon also wired the
	// charted-marketplace reader (SetChartedMarketplaceReader).
	HoldUnscannedMarketPosts int

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

	// chartedMarketplaceReader is the optional "has a marketplace" signal the sp-u8jc/sp-gucu
	// hold-unscanned-posts guard reads. nil (unwired) keeps the retire-as-gone behavior byte-
	// identical; even wired it is inert until the hold_unscanned_market_posts knob is armed.
	chartedMarketplaceReader ChartedMarketplaceReader

	// tourTelemetry is the optional REUSED tour-telemetry read: the sizer folds its
	// SELL legs into a per-sink realized-demand weight that supersedes the intrinsic census weight
	// in the value-weighted percentile. nil (unwired) or no telemetry ⇒ every market keeps its
	// intrinsic weight.
	tourTelemetry TourTelemetryReader

	// captainEvents emits the coordinator error-loop event when a reconcile pass fails with
	// the identical error for DefaultStreakThreshold consecutive ticks. Optional-injection.
	captainEvents captain.EventRecorder

	// liveConfig snapshots the container's OWN persisted config at each tick start,
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

// SetChartedMarketplaceReader wires the "has a marketplace" signal the sp-u8jc/sp-gucu
// hold-unscanned-posts guard reads. Leaving it unset keeps the retire-as-gone behavior
// byte-identical; even wired it is inert until hold_unscanned_market_posts is armed.
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetChartedMarketplaceReader(r ChartedMarketplaceReader) {
	h.chartedMarketplaceReader = r
}

// SetTourTelemetryReader wires the REUSED tour-telemetry read that drives the demand
// weight. Leaving it unset keeps every market on its intrinsic census weight.
// The demand weight is applied only when value-weighting is on (mode 2).
func (h *RunMarketFreshnessSizerCoordinatorHandler) SetTourTelemetryReader(r TourTelemetryReader) {
	h.tourTelemetry = r
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

// ReconcileOnce is one reconcile pass — the unit the tests drive directly. It MEASURES the
// per-system freshness demand, SIZES each market-bearing system's standing post, and BUYS
// one probe when the aggregate demand outruns supply and every guard passes. The tick runs
// entirely on the live-config snapshot taken here; a knob tuned mid-tick lands next tick.
func (h *RunMarketFreshnessSizerCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) error {
	cfg := resolveSizerConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	snapshots, err := h.freshnessReader.SystemsFreshness(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to read system freshness: %w", err)
	}
	// ONE telemetry read serves both the demand weight and the sensing scope.
	now := h.clock.Now()
	legs, tradeEvidence := h.readTradeLegs(ctx, cmd, cfg, now)

	// Override each market's intrinsic census weight with its realized SELL demand — the
	// PRIMARY weight source for the value-weighted freshness percentile so the fleet holds SLA on
	// the sinks it earns through and lets zero-demand markets breach. A never-traded market keeps
	// its intrinsic prior; a no-op (byte-identical) when unwired, value-weighting off, or no demand.
	h.applyDemandWeights(cfg, snapshots, legs, now)

	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	postBySystem := indexPostsBySystem(posts)
	// The fleet-wide cycle median stays measured over the WHOLE census, not the scope: it is a
	// noise-dampening statistic and a wider sample is a better one.
	globalCycle := aggregateMeasuredCycleSeconds(snapshots, cfg.MinCycleSamples)

	// SUPPLY is read up front: the sp-iopd reserved frontier floor holds the sizer's AGGREGATE
	// demand against (supply − floor), so the cap needs the pool count before the posts are sized.
	// The same pass yields the systems the fleet's non-scout hulls occupy, which anchor the scope.
	fleet, err := h.readFleet(ctx, cmd)
	if err != nil {
		return err
	}
	supply := fleet.supply

	// SENSING SCOPE: the API budget is fixed while the charted map is not, so the sizer sizes
	// against the systems the fleet OPERATES in plus a bounded discovery allowance, rather than
	// every market-bearing system. This bounds the INPUT SET only — every sizing decision inside
	// the scope runs the unchanged model, so the same probes over fewer systems buy proportionally
	// more freshness where the fleet actually earns.
	scope := buildScanScope(snapshots, legs, tradeEvidence, fleet.occupied, cfg, now)

	// CHARTED-MARKETPLACE signal: system → charted marketplace-waypoint count, independent of whether
	// prices were scanned yet. Read whenever the reader is wired (nil ⇒ every lookup zero, so an
	// unwired reader — tests/minimal boots — stays byte-identical on the scanned count). Two consumers:
	// the cold-start SIZING numerator below (a correctness default, always on) and, only when
	// hold_unscanned_market_posts is armed, the hold/retire + initial-scan-demand path. A read failure
	// aborts THIS tick (fail-safe: never retire a charted post or size off a partial view); the
	// error-streak monitor handles a persistent gap.
	chartedMarketplace, err := h.chartedMarketplaceSystems(ctx)
	if err != nil {
		return fmt.Errorf("failed to read charted marketplace systems: %w", err)
	}
	// hold_unscanned_market_posts gates ONLY the hold/retire + initial-scan-demand consumer, so that
	// behavior stays byte-identical when off; the sizing numerator consumes the signal unconditionally.
	var holdCharted map[string]int
	if cfg.HoldUnscannedMarketPosts {
		holdCharted = chartedMarketplace
	}

	marketBearing := make(map[string]bool, len(snapshots))
	// desiredBySystem holds each market-bearing system's target from the UNCHANGED
	// computeTarget/desiredHulls pipeline (model → clamp → circuit → per-system release pacing).
	// The reserved frontier floor may release from this AGGREGATE below (a separate ceiling), but
	// it never changes how a per-system target is computed.
	desiredBySystem := make(map[string]int, len(snapshots))
	totalDemand := 0
	for _, snap := range snapshots {
		if snap.MarketCount <= 0 {
			continue
		}
		marketBearing[snap.SystemSymbol] = true
		if !scope.Includes(snap.SystemSymbol) {
			continue // outside the sensing scope — no demand, no post write; released below.
		}
		// A DISCOVERY slot is a cheap standing watch on a system the fleet does not trade: one
		// probe against the relaxed discovery target, never the full SLA model. Sizing it like a
		// traded system is what made sensing scale with the map in the first place.
		if scope.IsDiscovery(snap.SystemSymbol) {
			desiredBySystem[snap.SystemSymbol] = 1
			totalDemand++
			continue
		}
		desired := h.sizeTradedSystem(cmd.PlayerID.Value(), snap, postBySystem[snap.SystemSymbol], chartedMarketplace[snap.SystemSymbol], globalCycle, cfg)
		desiredBySystem[snap.SystemSymbol] = desired
		totalDemand += desired
	}

	// RESERVED FRONTIER FLOOR (sp-iopd): hold the aggregate against (supply − floor), RELEASING the
	// surplus down to that reduced effective pool so the frontier keeps its reserved probes no
	// matter how high freshness demand climbs (breaking the depth→freshness→starve loop). Gated on
	// floor>0 (default 0 = exact pre-sp-iopd behavior) AND a pool bigger than the floor — you
	// cannot reserve more probes than the pool holds, so a pool ≤ the floor degrades to raw sizing
	// rather than starving freshness to nothing (the full allocator does relaxed-SLA graceful
	// degradation instead). The release lands per-post below through the SAME resize-DOWN seam
	// sp-iupr uses, so freed hulls return undedicated to the shared pool the frontier claims.
	if cfg.ReservedFrontierFloor > 0 && supply > cfg.ReservedFrontierFloor && totalDemand > supply-cfg.ReservedFrontierFloor {
		releaseAggregateToPool(desiredBySystem, supply-cfg.ReservedFrontierFloor)
		totalDemand = sumDesired(desiredBySystem)
	}

	// INITIAL-SCAN DEMAND (sp-u8jc/sp-gucu): each charted-but-unscanned system that carries a
	// standing post adds ONE probe of demand so the aggregate buy provisions capacity to man it for
	// its first scan (the reconciler mans it via in-system idle, the sp-u8jc cross-system relay, or
	// this buy); once scanned it lands market_data and enters the census-sized rotation above. Added
	// AFTER the reserved-frontier-floor recompute (which resets totalDemand from the per-system map)
	// so the floor never wipes it — this is NEW buy-demand, not a per-system holding the floor caps.
	// Empty holdCharted (hold_unscanned off / reader unwired) ⇒ 0 ⇒ byte-identical.
	totalDemand += initialScanDemand(posts, marketBearing, holdCharted, scope)

	neediestSystem := h.applySizedPosts(ctx, cmd, cfg, snapshots, scope, desiredBySystem, postBySystem)

	// AUTO-RESIZE / RELEASE: a standing post whose system dropped out of the census (its
	// markets retired) is removed, freeing its probes back to the pool. A charted-but-unscanned
	// system (marketplace waypoints present, no market_data yet) is HELD when armed — it needs an
	// initial scan, not retirement (sp-u8jc/sp-gucu).
	retired := h.retireMarketlessPosts(ctx, cmd, posts, marketBearing, holdCharted, scope)

	// AGGREGATE BUY: one guarded probe buy when total freshness demand outruns supply. With the
	// reserved frontier floor engaged, totalDemand is already ≤ (supply − floor) < supply, so the
	// sizer never buys into the reserved probes — it holds strictly against the reduced pool.
	buyer := probebuy.NewGuardedProbeBuyer(h.treasury, h.purchaser, h.ledgerRepo, h.clock, cfg.Buy)
	// Demand-proximal buy hint: spawn the probe at the yard nearest the neediest system.
	// The sizer has no per-hop tuning knob of its own, so it applies the shared default penalty
	// (proximity-first) and the shared sp-iqv2 supply-depletion margin so a repeated aggregate buy
	// spreads across sibling yards instead of spiraling one market to 4x. An empty neediestSystem is
	// the home-yard path — unchanged.
	target := probebuy.ProbeTarget{System: neediestSystem, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits, ClaimOwnerContainerID: cmd.ContainerID}
	outcome := buyer.MaybeBuy(ctx, cmd.PlayerID, totalDemand, supply, cmd.DryRun, target)

	logSizerCycle(ctx, cmd.DryRun, len(marketBearing), totalDemand, supply, retired, outcome)
	return nil
}

func (h *RunMarketFreshnessSizerCoordinatorHandler) sizeTradedSystem(playerID int, snap domainScouting.SystemFreshnessSnapshot, existing *domainScouting.ScoutPost, chartedMarkets int, globalCycle float64, cfg sizerConfig) int {
	sla := cfg.slaFor(snap.SystemSymbol)
	cycle := resolveCycleSeconds(snap, globalCycle, cfg)
	sz := systemSizing{snap: snap, sla: sla}
	if existing != nil {
		sz.current = existing.HullBudget()
		sz.fullyManned = existing.IsFullyManned() // sp-iupr issue 3a: gates the empirical-age sanity floor.
	}
	// COLD-START SCALE-UP: while the initial circuit is incomplete the census MarketCount is only
	// the SCANNED subset, so a big post sizes to one hull and never grows. Size against the full
	// CHARTED marketplace count so it reaches its true RequiredHulls at once (the coordinator then
	// mans + VRP-partitions the idle probes). The measured per-market cycle is taken from that SAME
	// partial subset — during a cold start it is a burst-and-idle artifact that deflates below the
	// true per-market hop, so sizing off it yields RequiredHulls=1 even at the full charted count.
	// Size off the fixed SEED cycle instead, matching the scout-post coordinator's undersized-warning
	// avgHop, so the two coordinators' hull demand agrees. Age/percentile telemetry stays on the
	// scanned reality (you can only measure a market you have scanned). Once scanning catches up
	// (charted ≤ scanned) neither override fires and the measured-cycle loop resumes.
	if chartedMarkets > snap.MarketCount {
		sz.snap.MarketCount = chartedMarkets
		cycle = cfg.SeedCycle
	}
	sz.cycle = cycle
	return h.desiredHulls(releaseKey(playerID, snap.SystemSymbol), sz, cfg)
}

// applySizedPosts writes each in-scope system's post at its sized manning level and returns
// the market-bearing system with the LARGEST unmet probe gap — the demand-proximal buy TARGET.
// The aggregate buy lands one undedicated probe for the reconciler to relay; naming the
// neediest system lets the purchaser spawn it at the probe-yard NEAREST that system. Empty
// (no positive gap) is the home-yard path.
func (h *RunMarketFreshnessSizerCoordinatorHandler) applySizedPosts(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, cfg sizerConfig, snapshots []domainScouting.SystemFreshnessSnapshot, scope domainScouting.ScanScope, desiredBySystem map[string]int, postBySystem map[string]*domainScouting.ScoutPost) string {
	neediestSystem := ""
	neediestGap := 0
	for _, snap := range snapshots {
		if snap.MarketCount <= 0 || !scope.Includes(snap.SystemSymbol) {
			continue
		}
		desired := desiredBySystem[snap.SystemSymbol]
		existing := postBySystem[snap.SystemSymbol]
		current := 0
		if existing != nil {
			current = existing.HullBudget()
		}
		if gap := desired - current; gap > neediestGap {
			neediestGap = gap
			neediestSystem = snap.SystemSymbol
		}
		// MANNING FLOOR: the home post carries a permanent MinHulls floor (probe_target)
		// so the freshsizer never sizes it below the probes bootstrap bought for the home scan. The
		// floor raises only the size WRITTEN to the post; `desired` (the buy-demand summed into
		// totalDemand, and the neediest-gap above) stays UN-floored, so the guarded buyer never
		// double-buys against bootstrap's single-buyer probe purchase. Every non-home post has
		// MinHulls 0, so FloorHulls is a no-op and non-home sizing is byte-identical.
		manning := desired
		if existing != nil {
			manning = existing.FloorHulls(desired)
		}
		// A discovery post carries the RELAXED watch target: a one-probe post stamped with a
		// trading SLA it can never meet reads as permanently breaching, which would raise its
		// demand and put the scout reconciler's manning watchdog on its tour.
		postSLA := cfg.slaFor(snap.SystemSymbol)
		if scope.IsDiscovery(snap.SystemSymbol) {
			postSLA = cfg.DiscoverySLA
		}
		if !cmd.DryRun {
			h.applyPost(ctx, cmd, existing, snap.SystemSymbol, manning, postSLA)
		}
	}
	return neediestSystem
}

func logSizerCycle(ctx context.Context, dryRun bool, systems, totalDemand, supply, retired int, outcome probebuy.Outcome) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Freshness sizer cycle: %d market-bearing systems, aggregate demand %d probes, supply %d, %d retired — %s", systems, totalDemand, supply, retired, outcome.Reason), map[string]interface{}{
		"action":           "freshness_sizer_cycle",
		"systems":          systems,
		"aggregate_demand": totalDemand,
		"supply":           supply,
		"retired":          retired,
		"dry_run":          dryRun,
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
}

// applyDemandWeights overrides each market's intrinsic census weight with its realized
// SELL demand — the PRIMARY weight source for the value-weighted freshness percentile — leaving a
// never-traded market on its intrinsic prior. Because it only rewrites the WEIGHT the percentile
// consumes, the whole sizing pipeline (WeightedPercentileAgeSeconds, the per-activity static base,
// the release/hysteresis math, the SLA) is untouched: this changes WHICH markets win scarce
// freshness, not the caps or the sizing math (orthogonal to sp-t5sh5 and sp-j4kjv).
//
// It is a no-op (byte-identical to intrinsic weighting) when value-weighting is off (mode 1, the
// weight is ignored) or there is no realized demand — legs is empty when the telemetry reader is
// unwired or its read failed, so an unreadable read falls open to intrinsic rather than aborting.
func (h *RunMarketFreshnessSizerCoordinatorHandler) applyDemandWeights(cfg sizerConfig, snapshots []domainScouting.SystemFreshnessSnapshot, legs []trading.TourLegTelemetry, now time.Time) {
	if !cfg.ValueWeighted {
		return
	}
	// The shared read spans the WIDER of the demand and footprint windows; the demand weight keeps
	// its own narrower window so a longer footprint retention cannot perturb the weighting.
	demandSince := now.Add(-cfg.DemandHalfLife * demandWindowHalfLives)
	inWindow := make([]trading.TourLegTelemetry, 0, len(legs))
	for _, leg := range legs {
		if !leg.PlannedAt.Before(demandSince) {
			inWindow = append(inWindow, leg)
		}
	}
	demand := domainScouting.DemandWeightsBySink(toSinkSales(inWindow, now), cfg.DemandHalfLife.Seconds())
	if len(demand) == 0 {
		return // cold start / no realized demand — every market keeps its intrinsic prior.
	}
	for i := range snapshots {
		for j := range snapshots[i].Markets {
			if weight, ok := demand[snapshots[i].Markets[j].Waypoint]; ok && weight > 0 {
				snapshots[i].Markets[j].Weight = weight
			}
		}
	}
}

// readTradeLegs pulls ONE tick's realized trade telemetry, spanning the WIDER of the demand-weight
// and footprint-retention windows so a single read serves both consumers (each applies its own
// window downstream).
//
// The bool reports whether the tick has TRADE EVIDENCE at all — false when the reader is unwired
// or its read failed, as distinct from a successful read that found no trades. The scope must not
// narrow without it: hull presence alone would drop every system the fleet trades in but happens
// to hold no hull in right now, so a transient read failure would mass-release posts.
func (h *RunMarketFreshnessSizerCoordinatorHandler) readTradeLegs(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, cfg sizerConfig, now time.Time) ([]trading.TourLegTelemetry, bool) {
	if h.tourTelemetry == nil {
		return nil, false
	}
	window := cfg.DemandHalfLife * demandWindowHalfLives
	if cfg.FootprintRetention > window {
		window = cfg.FootprintRetention
	}
	legs, err := h.tourTelemetry.ListByPlayer(ctx, cmd.PlayerID.Value(), now.Add(-window))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Tour telemetry unreadable — demand weighting falls back to intrinsic and the scan scope stays un-narrowed this tick: %v", err), nil)
		return nil, false
	}
	return legs, true
}

// chartedMarketplaceSystems reads the "has a marketplace" signal for this tick: system → charted
// marketplace-waypoint count. It returns nil (⇒ every lookup is zero ⇒ both consumers byte-identical)
// only when the reader is unwired; otherwise it always reads, because cold-start SIZING consumes the
// signal unconditionally (a correctness default) and the hold/retire consumer gates on its own knob
// downstream. A read error is returned to ReconcileOnce, which aborts the tick rather than act on a
// partial view (fail-safe: never retire a charted post or size off an incomplete census).
func (h *RunMarketFreshnessSizerCoordinatorHandler) chartedMarketplaceSystems(ctx context.Context) (map[string]int, error) {
	if h.chartedMarketplaceReader == nil {
		return nil, nil
	}
	return h.chartedMarketplaceReader.ChartedMarketSystemCounts(ctx)
}

// fleetView is one pass over the fleet: the scout-probe SUPPLY and the systems the fleet's
// NON-scout hulls occupy.
type fleetView struct {
	// supply is every scout-type hull available to scouting (undedicated or scout-tagged), in ANY
	// nav state — idle, in-flight, or manning. Counting in-flight/manning probes as supply is what
	// stops the coordinator over-buying while a probe it already owns is en route to a slot.
	supply int
	// occupied is the systems the fleet's NON-scout hulls sit in — where it works rather than
	// where it looks. It anchors contract hubs, the gate construction site, and factory feed
	// systems into the sensing footprint, none of which need have produced a tour leg.
	//
	// Probes are excluded deliberately: they are the sensor, not a reason to sense. Counting them
	// would let every scanned system justify its own scanning and the scope could never narrow.
	occupied map[string]bool
}

// readFleet derives the scout supply and the occupied-system set in a single fleet read.
func (h *RunMarketFreshnessSizerCoordinatorHandler) readFleet(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) (fleetView, error) {
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return fleetView{}, fmt.Errorf("failed to list fleet: %w", err)
	}
	view := fleetView{occupied: make(map[string]bool)}
	for _, ship := range ships {
		if !ship.IsScoutType() {
			// An IN_TRANSIT hull reports its DESTINATION here, so a hauler en route anchors the
			// system it is about to work in — which is exactly when fresh prices there start mattering.
			if loc := ship.CurrentLocation(); loc != nil && loc.Symbol != "" {
				view.occupied[shared.ExtractSystemSymbol(loc.Symbol)] = true
			}
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != freshnessScoutFleetTag {
			continue
		}
		view.supply++
	}
	return view, nil
}
