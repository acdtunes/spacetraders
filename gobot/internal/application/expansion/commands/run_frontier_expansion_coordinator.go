// Package commands holds the frontier expansion coordinator (sp-8w89): a standing
// daemon coordinator that CLOSES the manual frontier loop. Today a human measures
// coverage gaps, buys probes (RULINGS #6), and declares posts/sweeps; the engine's
// scout-post reconciler then mans them. This coordinator does the MEASURING, BUYING,
// and DECLARING — while ALL movement and manning stay with the existing machinery
// (the scout-post reconciler, the sp-s232 jump relays, and nn0y virgin discovery).
//
// It NEVER moves a probe and NEVER claims a hull (RULINGS #7). Its only two actions
// per cycle are:
//   - DECLARE a sweep-once scout post for the top-ranked uncovered frontier system,
//     through the SAME repository seam the `scout posts add` RPC writes (the domain
//     comment on scouting.PostKind confirms: "the scan queue ... is exactly the set
//     of unmanned sweep-once posts"). The reconciler then relays a probe there.
//   - PURCHASE one probe when coverage demand outstrips the idle-probe supply and
//     every money guard passes (RULINGS #4/#6). The bought probe lands UNDEDICATED
//     in the general pool; the reconciler claims and relays it.
//
// The loop is idempotent and restart-safe (RULINGS #2): every decision is re-derived
// from persisted state each tick (posts, ship rows, the transactions ledger), so a
// daemon restart mid-cycle re-derives the cooldown from the ledger and never
// double-buys. The coordinator persists NO new state of its own.
package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// probeShipType is the SpaceTraders purchase type for a scout/satellite hull.
	// There is no SHIP_SATELLITE type; a purchased SHIP_PROBE reports role SATELLITE
	// and satisfies navigation.Ship.IsScoutType() (the reconciler's manning filter).
	probeShipType = "SHIP_PROBE"

	// scoutFleetTag is the dedication tag the scout-post reconciler claims manning
	// hulls under. An idle satellite counts toward available supply only when it is
	// undedicated or already tagged for scouting — the same first-line poach filter
	// the reconciler uses (RULINGS #7). Kept in sync with the scouting package's
	// scoutPostFleet constant by value; a cross-package mismatch would make the
	// coordinator miscount supply, never poach (ClaimShip is the atomic guard).
	scoutFleetTag = "scout"

	// maxTreasuryFractionPercent is the RULINGS #6 hard per-hull ceiling: a probe is
	// bought only when its price is at most ~25% of LIVE treasury. It is a deliberate
	// NON-tunable constant (RULINGS #5's hard-floor exception, RULINGS #4 "guards are
	// never weakened") — unlike the spend/fleet CEILINGS below, which raise or lower
	// how much the coordinator may expand but can never let a single buy breach 25%.
	maxTreasuryFractionPercent = 25

	// Config defaults (RULINGS #5: every operational value is a flag/config key, filled
	// here only when the launch config leaves it unset). Documented in the dispatch report.
	defaultTickSeconds              = 60
	defaultMaxProbeFleet            = 40               // total satellite cap (current fleet + headroom)
	defaultMaxSpendPerCycle         = 100000           // max probe spend within the trailing spend window
	defaultPurchaseCooldown         = 10 * time.Minute // min wall-clock between probe buys
	defaultSpendWindow              = 1 * time.Hour    // trailing window the spend cap sums over
	defaultExpansionMaxHops         = 3                // gate-graph reach for the expansion queue
	defaultMaxFrontierPostsInFlight = 5                // outstanding sweep-once posts cap (bounds runaway declaration)
	defaultFrontierFreshness        = 60 * time.Minute // sweep-once post freshness target

	// Ranking weights (RULINGS #5). Score = markets*known − hops*penalty + virginBonus.
	defaultWeightKnownMarket = 10
	defaultWeightHopPenalty  = 5
	defaultWeightVirginBonus = 15

	// rankingLogLimit bounds how many ranked queue entries are logged per cycle so the
	// reposition-style ranking log (pin #1) stays readable on a large frontier.
	rankingLogLimit = 10
)

// TreasuryReader live-reads the player's treasury for the 25% money guard (RULINGS
// #6). It is deliberately fail-closed at the call site: a nil reader or a read error
// means the coordinator CANNOT verify the guard and therefore does NOT spend (RULINGS
// #4 — "cannot read the live balance ... do not spend"). The daemon wires the real
// api-backed reader; tests inject a stub.
type TreasuryReader interface {
	// LiveCredits returns the player's current treasury balance. Any error means the
	// balance is unreadable and the caller must fail closed (no purchase).
	LiveCredits(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// ProbePurchaser prices and buys ONE probe through the existing purchase_ship
// machinery (the daemon-side PurchaseShip mediator path — RULINGS #3, the daemon is
// the single writer). The coordinator quotes first to run its own money guards, then
// buys with the 25% ceiling as the hard MaxBudget. The bought probe lands undedicated
// in the pool; the coordinator claims nothing (RULINGS #7). A nil purchaser or any
// error fails closed.
type ProbePurchaser interface {
	// QuoteProbe returns the price of the cheapest reachable probe and the yard that
	// sells it. An error (no yard, unpriceable) fails the purchase closed.
	QuoteProbe(ctx context.Context, playerID shared.PlayerID) (price int, yard string, err error)
	// BuyProbe purchases one probe, refusing any fill whose price exceeds maxBudget.
	// It returns the actual price paid and the new hull's symbol.
	BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int) (price int, shipSymbol string, err error)
}

// ExpansionCandidate is one gate-reachable system the scanner surfaces for the queue:
// its hop distance from the nearest anchor (home/trade system), its count of known
// marketplace waypoints, and whether it is charted at all. A virgin candidate
// (Charted == false, surfaced because a charted neighbor gates to it) has zero known
// markets — nn0y discovers its waypoints on the relay's arrival.
type ExpansionCandidate struct {
	SystemSymbol string
	Hops         int
	KnownMarkets int
	Charted      bool

	// Scanned reports whether the system's FULL waypoint set has been SWEPT (persisted),
	// as distinct from Charted (merely gate-reachable). market_data rows exist ONLY for
	// systems that HAVE a market, so KnownMarkets==0 alone cannot tell a genuinely-barren
	// system (swept, no marketplace anywhere) from a never-scanned one (markets simply
	// undiscovered). Scanned supplies that missing distinction (sp-gb7h): a Scanned system
	// with KnownMarkets==0 is genuinely marketless and buildExpansionQueue DROPS it (its
	// markets were looked for and none exist — re-scouting it every cycle is waste); a
	// !Scanned system stays a scout target. The scanner derives it from the waypoint
	// catalog — a persisted non-gate waypoint proves a real sweep (adapters.go).
	Scanned bool
}

// ExpansionScanner enumerates the frontier the coordinator ranks. It hides the whole
// infra join behind one call: a multi-source BFS over the persisted gate adjacency
// from the anchor set (HQ + running-container systems), annotated with market-data
// counts and waypoint-presence (charted) flags. Optional: a nil scanner disables the
// expansion queue entirely, leaving the coordinator to serve only unmanned-slot demand
// on already-declared posts.
type ExpansionScanner interface {
	// ExpansionCandidates returns every gate-reachable system within maxHops of the
	// anchor set (charted reachability, virgin edge-targets included), each annotated.
	ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]ExpansionCandidate, error)
}

// FleetReader is the narrow slice of navigation.ShipRepository the coordinator reads:
// idle hulls (available probe SUPPLY) and the whole fleet (satellite COUNT for the
// fleet cap). Read-only — the coordinator never writes ship state (RULINGS #3/#7).
type FleetReader interface {
	FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// RunFrontierExpansionCoordinatorCommand launches the standing coordinator for a
// player (sp-8w89). Like the scout-post and trade-route coordinators it runs an
// infinite reconcile loop inside a single Handle() call; the container wraps it.
//
// All knobs are launch-config keys (RULINGS #5); <= 0 (or the zero value) falls back
// to the documented default, so the CLI can pass only what it wants to override.
type RunFrontierExpansionCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int

	// DryRun evaluates every decision and logs what it WOULD do, but declares no post
	// and buys no probe (pin #7 — the captain watches a cycle before arming it).
	DryRun bool

	MaxProbeFleet            int // total satellite cap
	MaxSpendPerCycle         int // max probe spend within the trailing spend window
	PurchaseCooldownSecs     int // min seconds between probe buys
	SpendWindowSecs          int // trailing window (seconds) the spend cap sums over
	ExpansionMaxHops         int // gate-graph reach for the queue
	MaxFrontierPostsInFlight int // outstanding sweep-once posts cap
	FrontierFreshnessSecs    int // declared sweep-once freshness target

	WeightKnownMarket int
	WeightHopPenalty  int
	WeightVirginBonus int
}

// RunFrontierExpansionCoordinatorResponse reports reconcile progress. Because the loop
// is infinite it is only observed on context cancellation (shutdown).
type RunFrontierExpansionCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunFrontierExpansionCoordinatorHandler reconciles coverage demand against probe
// supply every tick. It is a registered singleton (one instance serves every player's
// ticks), so it holds no per-player mutable state — every decision is derived fresh
// from the injected repositories each pass (RULINGS #2).
type RunFrontierExpansionCoordinatorHandler struct {
	postRepo   domainScouting.ScoutPostRepository
	fleetRepo  FleetReader
	ledgerRepo ledger.TransactionRepository
	clock      shared.Clock

	// treasury, purchaser, and scanner are optional collaborators wired via setters
	// (the codebase's optional-injection idiom). A nil treasury or purchaser fails the
	// PURCHASE path closed (no spend); a nil scanner disables the expansion QUEUE
	// (slot demand still drives purchases). Declaring posts and counting slots need
	// none of them, so the coordinator is always at least partially useful.
	treasury  TreasuryReader
	purchaser ProbePurchaser
	scanner   ExpansionScanner

	// captainEvents emits the coordinator error-loop event (sp-e2l1, rollout sp-6wxq)
	// when a reconcile pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — the silent-stuck class becomes an interrupt-visible captain
	// event instead of ERROR lines nothing reads. Optional-injection via
	// SetEventRecorder, nil-safe like the contract coordinator's captainEvents.
	captainEvents captain.EventRecorder

	// liveConfig snapshots the container's OWN persisted config at each tick start
	// (sp-vwek/sp-0z7f), so a `spacetraders tune` of a spend/cooldown/cap knob takes
	// effect on the NEXT tick with no restart. Optional-injection: nil keeps the
	// launch-frozen behavior byte-identical.
	liveConfig liveconfig.Reader
}

// NewRunFrontierExpansionCoordinatorHandler wires the coordinator. clock defaults to
// the real clock when nil (production). The treasury reader, probe purchaser, and
// expansion scanner are optional and injected separately (SetTreasuryReader,
// SetProbePurchaser, SetExpansionScanner).
func NewRunFrontierExpansionCoordinatorHandler(
	postRepo domainScouting.ScoutPostRepository,
	fleetRepo FleetReader,
	ledgerRepo ledger.TransactionRepository,
	clock shared.Clock,
) *RunFrontierExpansionCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunFrontierExpansionCoordinatorHandler{
		postRepo:   postRepo,
		fleetRepo:  fleetRepo,
		ledgerRepo: ledgerRepo,
		clock:      clock,
	}
}

// SetTreasuryReader wires the live-treasury source for the 25% guard (RULINGS #6).
// Leaving it unset keeps the PURCHASE path fail-closed (no spend without a treasury read).
func (h *RunFrontierExpansionCoordinatorHandler) SetTreasuryReader(t TreasuryReader) {
	h.treasury = t
}

// SetProbePurchaser wires the price-and-buy port over the existing purchase_ship
// machinery. Leaving it unset keeps the PURCHASE path fail-closed (nothing to buy through).
func (h *RunFrontierExpansionCoordinatorHandler) SetProbePurchaser(p ProbePurchaser) {
	h.purchaser = p
}

// SetExpansionScanner wires the frontier enumerator. Leaving it unset disables the
// expansion queue; the coordinator then serves only unmanned-slot demand.
func (h *RunFrontierExpansionCoordinatorHandler) SetExpansionScanner(s ExpansionScanner) {
	h.scanner = s
}

// SetEventRecorder wires the captain outbox the coordinator emits its error-loop
// event through (sp-6wxq). Optional-injection like the other setters: without it
// the streak monitor still tracks and logs, it just cannot escalate to a captain
// event (nil-safe, see health.RecordErrorLoop).
func (h *RunFrontierExpansionCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetLiveConfigReader wires the per-tick live-config snapshot source (sp-vwek), making
// the tunable knobs (FrontierTunableDefaults) honor `spacetraders tune` on the next
// tick. Leaving it unset keeps every knob launch-frozen (the pre-sp-vwek behavior).
func (h *RunFrontierExpansionCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunFrontierExpansionCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFrontierExpansionCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultTickSeconds * time.Second
	}

	result := &RunFrontierExpansionCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Frontier expansion coordinator starting (tick %s, dry_run=%v)", tick, cmd.DryRun), map[string]interface{}{
		"action":       "frontier_expansion_start",
		"container_id": cmd.ContainerID,
		"dry_run":      cmd.DryRun,
	})

	// errMon makes a reconcile pass that fails with the identical error every tick
	// observable (sp-e2l1): once the streak crosses DefaultStreakThreshold it emits a
	// captain event instead of just another ERROR line. One per Handle invocation so
	// the streak persists across ticks; noteReconcile keeps ReconcileOnce — the tested
	// unit — unchanged.
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
			logger.Log("ERROR", fmt.Sprintf("Frontier expansion reconcile failed: %v", err), nil)
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

// FrontierTunableDefaults maps every LIVE-tunable frontier-expansion knob (sp-0z7f) to
// its documented default — the value that applies when neither the live container
// config nor the launch command carries a positive one. The daemon's tune bounds
// registry reads THIS map, so the defaults-of-record stay in this file next to the
// consts they mirror. The map's KEY SET is also the contract for which keys
// resolveConfig live-overlays.
func FrontierTunableDefaults() map[string]int {
	return map[string]int{
		"max_spend_per_cycle":    defaultMaxSpendPerCycle,
		"purchase_cooldown_secs": int(defaultPurchaseCooldown / time.Second),
		"max_probe_fleet":        defaultMaxProbeFleet,
	}
}

// frontierConfig is the launch command with every default resolved, so the reconcile
// logic never repeats the "<= 0 → default" fallback (RULINGS #5).
type frontierConfig struct {
	MaxProbeFleet            int
	MaxSpendPerCycle         int
	PurchaseCooldown         time.Duration
	SpendWindow              time.Duration
	ExpansionMaxHops         int
	MaxFrontierPostsInFlight int
	FrontierFreshness        time.Duration
	WeightKnownMarket        int
	WeightHopPenalty         int
	WeightVirginBonus        int
}

// resolveConfig resolves one tick's effective config. live is the tick-start snapshot
// of the container's persisted config column (nil when unwired/unreadable). For the
// TUNABLE knobs (FrontierTunableDefaults) a non-nil snapshot is AUTHORITATIVE: a
// positive value is the live value (the launch verb wrote its values into the same
// column, so untuned knobs still read their launch values here), and an absent/zeroed
// key means the documented default — the `tune <key> 0` revert. Only when there is NO
// snapshot does the launch command fill those knobs (fail-safe launch behavior). The
// non-tunable knobs always resolve from the launch command, unchanged.
func resolveConfig(cmd *RunFrontierExpansionCoordinatorCommand, live liveconfig.Snapshot) frontierConfig {
	c := frontierConfig{
		MaxProbeFleet:            cmd.MaxProbeFleet,
		MaxSpendPerCycle:         cmd.MaxSpendPerCycle,
		PurchaseCooldown:         time.Duration(cmd.PurchaseCooldownSecs) * time.Second,
		SpendWindow:              time.Duration(cmd.SpendWindowSecs) * time.Second,
		ExpansionMaxHops:         cmd.ExpansionMaxHops,
		MaxFrontierPostsInFlight: cmd.MaxFrontierPostsInFlight,
		FrontierFreshness:        time.Duration(cmd.FrontierFreshnessSecs) * time.Second,
		WeightKnownMarket:        cmd.WeightKnownMarket,
		WeightHopPenalty:         cmd.WeightHopPenalty,
		WeightVirginBonus:        cmd.WeightVirginBonus,
	}
	if live != nil {
		c.MaxProbeFleet = live.PositiveIntOrZero("max_probe_fleet")
		c.MaxSpendPerCycle = live.PositiveIntOrZero("max_spend_per_cycle")
		c.PurchaseCooldown = time.Duration(live.PositiveIntOrZero("purchase_cooldown_secs")) * time.Second
	}
	if c.MaxProbeFleet <= 0 {
		c.MaxProbeFleet = defaultMaxProbeFleet
	}
	if c.MaxSpendPerCycle <= 0 {
		c.MaxSpendPerCycle = defaultMaxSpendPerCycle
	}
	if c.PurchaseCooldown <= 0 {
		c.PurchaseCooldown = defaultPurchaseCooldown
	}
	if c.SpendWindow <= 0 {
		c.SpendWindow = defaultSpendWindow
	}
	if c.ExpansionMaxHops <= 0 {
		c.ExpansionMaxHops = defaultExpansionMaxHops
	}
	if c.MaxFrontierPostsInFlight <= 0 {
		c.MaxFrontierPostsInFlight = defaultMaxFrontierPostsInFlight
	}
	if c.FrontierFreshness <= 0 {
		c.FrontierFreshness = defaultFrontierFreshness
	}
	if c.WeightKnownMarket <= 0 {
		c.WeightKnownMarket = defaultWeightKnownMarket
	}
	if c.WeightHopPenalty <= 0 {
		c.WeightHopPenalty = defaultWeightHopPenalty
	}
	if c.WeightVirginBonus <= 0 {
		c.WeightVirginBonus = defaultWeightVirginBonus
	}
	return c
}

// queueEntry is one ranked expansion target: an uncovered gate-reachable system worth
// a frontier post, with the score that ordered it.
type queueEntry struct {
	SystemSymbol string
	Hops         int
	KnownMarkets int
	Virgin       bool
	Score        int
}

// noteReconcile records one reconcile pass at the "reconcile" streak checkpoint
// (sp-6wxq): a nil err is a success that resets the streak; a non-nil err that
// repeats identically for DefaultStreakThreshold consecutive passes crosses and
// emits the coordinator error-loop captain event. Edge-triggered and nil-safe on
// the recorder (health.RecordErrorLoop). Per-decision failures inside ReconcileOnce
// (a probe purchase, a single scan) are logged WARNING and swallowed there, so only
// a pass-level error — the genuine silent-stuck signal — is tracked here.
func (h *RunFrontierExpansionCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// liveConfigSnapshot takes the tick's live-config snapshot (sp-vwek). A nil reader
// (not wired — tests, minimal boots) or a read error yields nil, which resolveConfig
// treats as "run this tick entirely on the launch command" — the fail-safe launch
// behavior, never a half-applied config. The read is logged, not fatal: a transient
// DB gap must not kill the reconcile loop.
func (h *RunFrontierExpansionCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) liveconfig.Snapshot {
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

// ReconcileOnce is one reconcile pass — the unit the tests drive directly (Handle just
// calls it on a timer). It MEASURES demand, DECLARES the top frontier target, and BUYS
// one probe when the fleet is short and every guard passes. It is idempotent: a restart
// re-derives everything from persisted state (posts, ships, ledger), so it never
// double-declares (Upsert is keyed by system) or double-buys (the cooldown reads the
// ledger, not memory). The tick runs entirely on the live-config snapshot taken here;
// a knob tuned mid-tick lands next tick.
func (h *RunFrontierExpansionCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)
	cfg := resolveConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}

	openSlots := countUnmannedSlots(posts)
	frontierPosts := countSweepOncePosts(posts)

	// SUPPLY: idle, undedicated (or scout-tagged) satellites the reconciler can relay.
	available, err := h.availableProbes(ctx, cmd)
	if err != nil {
		return err
	}
	availableCount := len(available)

	// QUEUE: rank the uncovered gate-reachable frontier (logs the ranking, pin #1).
	queue := h.buildExpansionQueue(ctx, cmd, cfg, posts)

	// DEPLOY: declare the top uncovered frontier system as a sweep-once post, bounded by
	// the in-flight cap so declaration never outruns what the fleet can man (pin #3).
	declared := ""
	if len(queue) > 0 && frontierPosts < cfg.MaxFrontierPostsInFlight {
		head := queue[0]
		if !hasPost(posts, head.SystemSymbol) {
			if cmd.DryRun {
				logger.Log("INFO", fmt.Sprintf("DRY-RUN: would declare sweep-once frontier post %s (score %d, %d markets, %d hops, virgin=%v)", head.SystemSymbol, head.Score, head.KnownMarkets, head.Hops, head.Virgin), map[string]interface{}{
					"action":        "frontier_declare_dryrun",
					"system_symbol": head.SystemSymbol,
				})
				declared = head.SystemSymbol
			} else if err := h.declareSweepOncePost(ctx, cmd, cfg, head.SystemSymbol); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to declare frontier sweep-once post %s: %v", head.SystemSymbol, err), nil)
			} else {
				declared = head.SystemSymbol
				logger.Log("INFO", fmt.Sprintf("Declared frontier sweep-once post %s (score %d, %d markets, %d hops, virgin=%v) — reconciler will relay a probe", head.SystemSymbol, head.Score, head.KnownMarkets, head.Hops, head.Virgin), map[string]interface{}{
					"action":        "frontier_post_declared",
					"system_symbol": head.SystemSymbol,
					"score":         head.Score,
					"markets":       head.KnownMarkets,
					"hops":          head.Hops,
					"virgin":        head.Virgin,
				})
			}
		}
	}
	// A declared (or, in dry-run, would-be-declared) sweep-once post adds one unmanned
	// slot to this cycle's demand, so the purchase decision — and the dry-run preview —
	// reflects the fresh coverage need the reconciler must now serve.
	if declared != "" {
		openSlots++
	}

	// PURCHASE: buy one probe iff the fleet is short of the open manning demand and every
	// guard passes. decideAndMaybeBuy returns the human reason for the per-cycle summary.
	purchaseReason := h.decideAndMaybeBuy(ctx, cmd, cfg, openSlots, availableCount)

	action := purchaseReason
	if declared != "" {
		action = fmt.Sprintf("declared %s; %s", declared, purchaseReason)
	}
	logger.Log("INFO", fmt.Sprintf("Frontier expansion cycle: demand %d open slots + %d queue, supply %d idle probes, %d frontier posts in flight — %s", openSlots, len(queue), availableCount, frontierPosts, action), map[string]interface{}{
		"action":         "frontier_expansion_cycle",
		"open_slots":     openSlots,
		"queue_len":      len(queue),
		"idle_probes":    availableCount,
		"frontier_posts": frontierPosts,
		"dry_run":        cmd.DryRun,
		"outcome":        action,
	})
	return nil
}

// decideAndMaybeBuy runs the fail-closed purchase gate stack and, when every gate
// passes, buys exactly one probe (or, in dry-run, logs the intent). It returns a short
// human reason for the per-cycle summary — either "bought ..." / "would buy ..." or
// "no purchase: <why>". The gate ORDER is cheapest-first: the checks that need no I/O
// (target, supply, dry-run intent) precede the ledger/treasury/API reads, so a
// no-purchase cycle rarely touches the network.
func (h *RunFrontierExpansionCoordinatorHandler) decideAndMaybeBuy(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	openSlots, availableCount int,
) string {
	logger := common.LoggerFromContext(ctx)

	// A named target must exist: an unmanned slot on a declared post. The expansion
	// queue only becomes a target once declared into a post (above), so gating on
	// openSlots is the "a slot ... target" rule — no slot, no buy (pin #2).
	if openSlots == 0 {
		return "no purchase: no unmanned slot to serve"
	}

	// Fleet short? If idle probes already cover the open demand, the reconciler will
	// relay them — buying while an idle probe can serve the target is the bug (pin #2).
	if availableCount >= openSlots {
		return fmt.Sprintf("no purchase: supply covers demand (%d idle probes >= %d open slots)", availableCount, openSlots)
	}

	// Fleet cap (RULINGS #5 ceiling): never grow the satellite fleet past the cap.
	satCount, err := h.satelliteCount(ctx, cmd)
	if err != nil {
		return fmt.Sprintf("no purchase: fleet count unreadable (fail-closed): %v", err)
	}
	if satCount >= cfg.MaxProbeFleet {
		return fmt.Sprintf("no purchase: fleet cap reached (%d/%d satellites)", satCount, cfg.MaxProbeFleet)
	}

	// Cooldown (ledger-derived, restart-safe): at most one probe buy per cooldown. Read
	// from the persisted PURCHASE_SHIP ledger, NOT memory, so a restart mid-cycle sees a
	// just-bought probe and never double-buys (RULINGS #2).
	last, hadLast, err := h.lastProbePurchase(ctx, cmd)
	if err != nil {
		return fmt.Sprintf("no purchase: purchase ledger unreadable (fail-closed): %v", err)
	}
	if hadLast {
		if elapsed := h.clock.Now().Sub(last); elapsed < cfg.PurchaseCooldown {
			return fmt.Sprintf("no purchase: cooldown active (%s since last probe, need %s)", elapsed.Round(time.Second), cfg.PurchaseCooldown)
		}
	}

	// Treasury (RULINGS #4/#6): cannot read the live balance → do not spend.
	if h.treasury == nil {
		return "no purchase: no treasury reader wired (fail-closed)"
	}
	credits, err := h.treasury.LiveCredits(ctx, cmd.PlayerID)
	if err != nil {
		return fmt.Sprintf("no purchase: treasury unreadable (fail-closed): %v", err)
	}

	// Price quote (RULINGS #4): cannot price the hull → do not spend.
	if h.purchaser == nil {
		return "no purchase: no purchaser wired (fail-closed)"
	}
	price, yard, err := h.purchaser.QuoteProbe(ctx, cmd.PlayerID)
	if err != nil {
		return fmt.Sprintf("no purchase: probe unpriceable (fail-closed): %v", err)
	}

	// 25% rule (RULINGS #6): price must be at most 25% of live treasury. Integer form
	// price*100 > credits*25 avoids float rounding and never weakens the guard.
	if price*100 > credits*maxTreasuryFractionPercent {
		return fmt.Sprintf("no purchase: probe price %d exceeds %d%% of treasury %d", price, maxTreasuryFractionPercent, credits)
	}

	// Per-window spend cap (RULINGS #5 ceiling, ledger-derived): probe spend already
	// booked in the trailing cooldown window plus this price must clear the cap.
	windowSpend, err := h.probeSpendSince(ctx, cmd, h.clock.Now().Add(-cfg.SpendWindow))
	if err != nil {
		return fmt.Sprintf("no purchase: spend ledger unreadable (fail-closed): %v", err)
	}
	if windowSpend+price > cfg.MaxSpendPerCycle {
		return fmt.Sprintf("no purchase: spend cap (window %d + price %d > %d)", windowSpend, price, cfg.MaxSpendPerCycle)
	}

	// Every guard passed. The hard MaxBudget handed to the buy is the 25% treasury
	// ceiling — a slight price move up to (never past) the line still fills (RULINGS #6).
	treasuryCap := credits * maxTreasuryFractionPercent / 100

	if cmd.DryRun {
		logger.Log("INFO", fmt.Sprintf("DRY-RUN: would buy probe at %s for ~%d (treasury %d, cap %d) to serve %d unmanned slot(s)", yard, price, credits, treasuryCap, openSlots), map[string]interface{}{
			"action":       "frontier_purchase_dryrun",
			"yard":         yard,
			"quoted_price": price,
			"treasury":     credits,
			"open_slots":   openSlots,
			"treasury_cap": treasuryCap,
		})
		return fmt.Sprintf("would buy probe at %s for ~%d (dry-run)", yard, price)
	}

	paid, sym, err := h.purchaser.BuyProbe(ctx, cmd.PlayerID, treasuryCap)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Frontier probe purchase failed at %s (budget %d): %v", yard, treasuryCap, err), map[string]interface{}{
			"action": "frontier_purchase_failed",
			"yard":   yard,
			"error":  err.Error(),
		})
		return fmt.Sprintf("no purchase: buy failed (fail-closed): %v", err)
	}

	// One loud line per purchase (pin #2/#6). The capex dashboard already surfaces the
	// SHIP_INVESTMENTS ledger row the purchase machinery wrote.
	logger.Log("INFO", fmt.Sprintf("Frontier probe purchased: %s for %d at %s (treasury %d, serving %d unmanned slot(s)) — landed undedicated, reconciler will relay", sym, paid, yard, credits, openSlots), map[string]interface{}{
		"action":      "frontier_probe_purchased",
		"ship_symbol": sym,
		"price":       paid,
		"yard":        yard,
		"treasury":    credits,
		"open_slots":  openSlots,
	})
	return fmt.Sprintf("bought probe %s for %d at %s", sym, paid, yard)
}

// buildExpansionQueue ranks the uncovered gate-reachable frontier and logs the ranking
// (pin #1, reposition-style: system, score, chosen). A system is a candidate when it is
// NOT already covered by a declared post AND either has known markets to keep fresh or
// is a reachable virgin system (nn0y charts it on the relay's arrival). A charted system
// with no markets and no post is unserviceable — skipped. Returns nil (no queue) when no
// scanner is wired or the scan fails, so the coordinator degrades to slot-demand only.
func (h *RunFrontierExpansionCoordinatorHandler) buildExpansionQueue(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
) []queueEntry {
	logger := common.LoggerFromContext(ctx)
	if h.scanner == nil {
		return nil
	}

	candidates, err := h.scanner.ExpansionCandidates(ctx, cmd.PlayerID.Value(), cfg.ExpansionMaxHops)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Frontier expansion scan failed: %v — serving slot demand only this cycle", err), nil)
		return nil
	}

	covered := postSystemSet(posts)
	var entries []queueEntry
	for _, c := range candidates {
		if covered[c.SystemSymbol] {
			continue // already has a declared post
		}
		if c.Hops == 0 {
			// sp-njwy: an occupied/anchor system (hop 0 — the HQ or a system the fleet
			// already sits in) is NOT a frontier. Auto-declaring it as a sweep-once post
			// spins up a LOCAL in-system sweep tour that absorbs every freshly-bought probe
			// before the scout reconciler can relay it to a genuinely virgin CROSS-SYSTEM
			// post (the starvation that left all virgin posts unmanned), and its
			// in-system-coverable slot spuriously inflates buy demand (the over-buy). Skip
			// it: expansion targets systems we do NOT yet occupy, so fresh probes stay idle
			// and claimable for the gate-jump relay.
			continue
		}
		if c.Scanned && c.KnownMarkets == 0 {
			// sp-gb7h: a scanned-and-genuinely-marketless system is unserviceable — DROP it.
			// Its full waypoint set WAS swept (Scanned) and holds no marketplace anywhere
			// (KnownMarkets==0), so its markets were looked for and none exist. Re-declaring
			// it only re-scouts a barren system every cycle (declare → sweep-once → no market
			// found → post retired → re-declare), the waste sp-dc50's gap-2 shortcut left
			// behind when it removed the charted-marketless skip ENTIRELY. Note this keys on
			// SWEEP knowledge (Scanned), NOT gate-edge presence (Charted) — the exact
			// conflation that froze the frontier: a !Scanned system (never swept) below is
			// still KEPT as a scout target, since a persisted gate edge means we can REACH the
			// system, not that its markets were ever scanned (sp-dc50 gap 2). A system with
			// known markets (KnownMarkets>0) is never dropped here regardless of Scanned.
			continue
		}
		virgin := !c.Charted
		// sp-dc50 gap 2: do NOT skip a charted-but-0-market system as "nothing to scan". A
		// persisted gate edge (Charted=true) means we can REACH the system, not that its MARKETS
		// were ever scanned — it may still hold a marketplace and shipyard (even the heavy-freighter
		// yard the expansion is hunting). The old skip keyed on gate-edge presence, not market
		// knowledge, so it dropped every hop-2+ system the BFS reached over a charted gate but had
		// never scanned — the frontier froze at the pre-charted boundary and the queue emptied. A
		// reachable, uncovered system with no known markets simply stays an unscanned scout target.
		score := c.KnownMarkets*cfg.WeightKnownMarket - c.Hops*cfg.WeightHopPenalty
		if virgin {
			score += cfg.WeightVirginBonus
		}
		entries = append(entries, queueEntry{
			SystemSymbol: c.SystemSymbol,
			Hops:         c.Hops,
			KnownMarkets: c.KnownMarkets,
			Virgin:       virgin,
			Score:        score,
		})
	}

	// Highest score first; deterministic system-symbol tiebreak so the head (chosen) and
	// the logged ranking are stable across ticks and testable.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].SystemSymbol < entries[j].SystemSymbol
	})

	for i, e := range entries {
		if i >= rankingLogLimit {
			break
		}
		logger.Log("INFO", fmt.Sprintf("Frontier queue #%d: %s score=%d (markets=%d, hops=%d, virgin=%v)%s", i+1, e.SystemSymbol, e.Score, e.KnownMarkets, e.Hops, e.Virgin, chosenMarker(i)), map[string]interface{}{
			"action":        "frontier_expansion_ranking",
			"rank":          i + 1,
			"system_symbol": e.SystemSymbol,
			"score":         e.Score,
			"markets":       e.KnownMarkets,
			"hops":          e.Hops,
			"virgin":        e.Virgin,
			"chosen":        i == 0,
		})
	}
	return entries
}

func chosenMarker(i int) string {
	if i == 0 {
		return " [chosen]"
	}
	return ""
}

// declareSweepOncePost writes a single-hull sweep-once post for system through the SAME
// repository seam the `scout posts add` RPC uses (scouting.ScoutPostRepository.Upsert),
// keyed by (player, system) so a re-declare is idempotent. The caller declares only when
// no post exists for the system, so there is no live manning to preserve.
func (h *RunFrontierExpansionCoordinatorHandler) declareSweepOncePost(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	systemSymbol string,
) error {
	post := &domainScouting.ScoutPost{
		PlayerID:        cmd.PlayerID.Value(),
		SystemSymbol:    systemSymbol,
		FreshnessTarget: cfg.FrontierFreshness,
		Kind:            domainScouting.PostKindSweepOnce,
		Hulls:           1,
		CreatedAt:       h.clock.Now(),
	}
	return h.postRepo.Upsert(ctx, post)
}

// availableProbes returns the idle satellites the reconciler could relay to a slot:
// idle, scout-type, and not dedicated to some OTHER fleet — the same first-line poach
// filter the scout reconciler uses (RULINGS #7). The coordinator only COUNTS them; it
// never claims one.
func (h *RunFrontierExpansionCoordinatorHandler) availableProbes(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) ([]*navigation.Ship, error) {
	ships, err := h.fleetRepo.FindIdleByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find idle ships: %w", err)
	}
	var sats []*navigation.Ship
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != scoutFleetTag {
			continue
		}
		sats = append(sats, ship)
	}
	return sats, nil
}

// satelliteCount is the total number of scout-type hulls the player owns — the figure
// the fleet cap gates on (RULINGS #6, "never buy hulls speculatively" past the cap).
func (h *RunFrontierExpansionCoordinatorHandler) satelliteCount(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (int, error) {
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list fleet: %w", err)
	}
	n := 0
	for _, ship := range ships {
		if ship.IsScoutType() {
			n++
		}
	}
	return n, nil
}

// lastProbePurchase returns the timestamp of the most recent SHIP_PROBE purchase for
// the player, derived from the persisted transactions ledger (RULINGS #2: the cooldown
// clock survives a restart because it is READ from the ledger, not held in memory). It
// scans recent PURCHASE_SHIP rows and matches the first whose metadata ship_type is a
// probe — scoping the cooldown to probe buys from ANY source (coordinator or a manual
// captain buy), which is the conservative reading: if the fleet just grew a probe,
// pause and let the reconciler deploy it before buying more.
func (h *RunFrontierExpansionCoordinatorHandler) lastProbePurchase(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (time.Time, bool, error) {
	ps := ledger.TransactionTypePurchaseShip
	txns, err := h.ledgerRepo.FindByPlayer(ctx, cmd.PlayerID, ledger.QueryOptions{
		TransactionType: &ps,
		OrderBy:         "timestamp DESC",
		Limit:           50,
	})
	if err != nil {
		return time.Time{}, false, err
	}
	for _, t := range txns {
		if isProbePurchase(t) {
			return t.Timestamp(), true, nil
		}
	}
	return time.Time{}, false, nil
}

// probeSpendSince sums probe purchase spend booked since `since`, derived from the
// ledger (RULINGS #2/#5: the per-window spend cap is re-derived from persisted state
// each tick). Amounts are stored negative (expenses), so spend is the negated sum.
func (h *RunFrontierExpansionCoordinatorHandler) probeSpendSince(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, since time.Time) (int, error) {
	ps := ledger.TransactionTypePurchaseShip
	txns, err := h.ledgerRepo.FindByPlayer(ctx, cmd.PlayerID, ledger.QueryOptions{
		TransactionType: &ps,
		StartDate:       &since,
		Limit:           500,
	})
	if err != nil {
		return 0, err
	}
	sum := 0
	for _, t := range txns {
		if isProbePurchase(t) {
			sum += -t.Amount()
		}
	}
	return sum, nil
}

// isProbePurchase reports whether a PURCHASE_SHIP transaction bought a probe, read from
// the metadata ship_type the purchase machinery stamps.
func isProbePurchase(t *ledger.Transaction) bool {
	st, _ := t.Metadata()["ship_type"].(string)
	return st == probeShipType
}

// countUnmannedSlots counts manning slots with no hull AND no relay in flight across all
// posts — the OPEN coverage demand. A slot with a reposition relay airborne is already
// being served by an existing probe, so it is NOT demand (never triggers a buy).
func countUnmannedSlots(posts []*domainScouting.ScoutPost) int {
	n := 0
	for _, post := range posts {
		for _, slot := range post.Slots() {
			if slot.AssignedHull() == "" && slot.RepositionContainerID() == "" {
				n++
			}
		}
	}
	return n
}

// countSweepOncePosts counts outstanding frontier (sweep-once) posts — the in-flight
// expansions the declaration cap bounds.
func countSweepOncePosts(posts []*domainScouting.ScoutPost) int {
	n := 0
	for _, post := range posts {
		if post.Kind == domainScouting.PostKindSweepOnce {
			n++
		}
	}
	return n
}

func hasPost(posts []*domainScouting.ScoutPost, systemSymbol string) bool {
	for _, post := range posts {
		if post.SystemSymbol == systemSymbol {
			return true
		}
	}
	return false
}

func postSystemSet(posts []*domainScouting.ScoutPost) map[string]bool {
	set := make(map[string]bool, len(posts))
	for _, post := range posts {
		set[post.SystemSymbol] = true
	}
	return set
}
