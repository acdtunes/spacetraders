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
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
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

	// defaultProximalYardHopPenalty is the price/distance tradeoff for demand-proximal probe
	// buying (sp-hej4): the credits of price premium the buyer accepts to spawn the probe ONE
	// gate-hop closer to the target post, so a nearer-but-pricier probe-yard beats a far-cheaper
	// one iff the per-hop saving clears this. ~one probe's price (probes run ~25–55k), so
	// proximity dominates the typical yard spread by default — the "buy NEAREST the post" policy;
	// raise it toward absolute proximity or lower it toward the cheapest reachable yard. Mirrors
	// probebuy.DefaultHopPenaltyCredits (the freshness sizer's untuned value).
	defaultProximalYardHopPenalty = probebuy.DefaultHopPenaltyCredits

	// defaultProbeSiblingPriceMargin is the sp-iqv2 supply-depletion / load-balance margin: once the
	// hop-penalty-preferred probe-yard's scanned price exceeds the cheapest reachable sibling by more
	// than this, the buy spreads to that sibling instead of spiraling one market to 4x (the live home
	// hub 20k→86k). It bounds the premium proximity may pay. Mirrors probebuy.DefaultSiblingPriceMarginCredits.
	defaultProbeSiblingPriceMargin = probebuy.DefaultSiblingPriceMarginCredits

	// defaultMaxProbePrice is the sp-3u5d per-unit probe PRICE CEILING (credits) — the max the frontier
	// will pay for ONE probe. It is the BACKSTOP for the deepest-frontier tail whose ONLY reachable yard
	// is a depleted deep one: with no cheaper reachable sibling for probe_sibling_price_margin to spread
	// to, price spirals to 210-235k with nothing to stop it (max_spend_per_cycle is a blunt trailing-window
	// budget that also blocks the cheap near buys). When the FINAL chosen quote (after sibling-spread)
	// exceeds this, the buy DEFERS — the post stays dark and retries next cycle. 0 (the default) = DISABLED,
	// byte-identical to pre-sp-3u5d and the governance gate; unlike the hop/sibling knobs it takes NO <=0
	// default fallback, so 0 is the real "off" value (mirrors reserved_freshness_floor).
	defaultMaxProbePrice = 0
	// Depth-vs-breadth balance (sp-rjgr). Pure BFS scores hop-1 above hop-2 EVERY time, so with
	// scout throughput < ring width the near ring never drains and no probe ever reaches the depth
	// a heavy-freighter yard needs. The depth slice reserves a fraction of frontier capacity for
	// PATHFINDERS that pick the deepest-reachable virgin (ignoring score) along distinct corridors.
	//
	// The BREADTH fraction is the primary knob (its complement is the depth fraction): expressing
	// the split as breadth-percent lets 100 mean "pure BFS, 0% depth" — a real value the tune
	// mechanism's 0-means-revert-to-default could not otherwise carry. 65 ⇒ 65/35 breadth/depth.
	defaultBreadthFractionPercent = 65 // ⇒ 35% depth
	// defaultMaxDepthPathfinders caps concurrent depth posts so depth never starves breadth's
	// market coverage even under a heavy objective bias.
	defaultMaxDepthPathfinders = 3
	// defaultMaxDepthHops bounds how deep a pathfinder targets (and the depth scan horizon). Kept
	// within the expendable-probe reposition reach ([scouting] max_reposition_jumps, default 12) so
	// a declared deep post is actually man-able by a relay.
	defaultMaxDepthHops = 8
	// defaultObjectiveBiasPercent is the percentage points added to the depth fraction while the
	// deep-resource objective is UNMET (heavy shortfall > 0 AND no heavy yard known) — punch
	// outward until a yard is found, then relax back to the baseline split.
	defaultObjectiveBiasPercent = 40

	// defaultReservedFreshnessFloor is the sp-iopd SYMMETRIC freshness floor: N idle probes the
	// frontier treats as UNAVAILABLE (reserved for the market-freshness sizer, sp-orgp). The
	// frontier discounts them from the idle supply it counts toward covering its OWN coverage
	// demand, so an aggressive frontier (heavy objective bias, many depth pathfinders) GROWS the
	// pool with a guarded buy rather than cannibalizing scanning below a relaxed baseline. It is
	// the mirror of the freshness sizer's reserved_frontier_floor; the pair keeps neither
	// coordinator able to starve the other. 0 (the default) is EXACT pre-sp-iopd behavior — the
	// floor is OFF until deployed, opt-in via `tune reserved_freshness_floor <N>` (a probe count
	// for the MVP; the full allocator sizes it to hold known markets under a RELAXED SLA).
	defaultReservedFreshnessFloor = 0

	// defaultScanOnly is the sp-jide scan_only knob's default: 0 (OFF) = today's behavior,
	// BYTE-IDENTICAL. When tuned to 1 the coordinator DECOUPLES scanning the discovered-market
	// backlog from expanding to virgin — it declares NO depth pathfinder, buys NO probe, and
	// restricts its sweep-once declarations to the FULL charted-but-unscanned MARKET backlog (every
	// system with MARKETPLACE waypoints but zero player market_data), draining it to zero then
	// idling. Like reserved_freshness_floor, 0 IS the default here, so resolveConfig applies NO
	// <=0 fallback for it (a `tune scan_only 0` must revert to OFF, not read as "unset → default").
	defaultScanOnly = 0

	// rankingLogLimit bounds how many ranked queue entries are logged per cycle so the
	// reposition-style ranking log (pin #1) stays readable on a large frontier.
	rankingLogLimit = 10

	// --- Off-gate explorer demand + target selection (sp-k645, slice B) -------------------
	// These knobs drive the OFF-GATE DEMAND SIGNAL and warp TARGET SELECTION only; nothing
	// here warps or buys (that is slice C). All four are live-tunable (FrontierTunableDefaults)
	// under the FRONTIER container type. Appended as a self-contained block so the sp-iopd
	// union-rebase stays trivial.
	//
	// defaultOffGateQueueExhaustionCycles is N: how many CONSECUTIVE cycles the gate-reachable
	// expansion queue must be empty (no new ring opening) before trigger (a) — the "virgin set
	// exhausted" signal — fires. Debounced so a one-cycle dip (a ring momentarily drained but
	// about to reopen) never raises demand.
	defaultOffGateQueueExhaustionCycles = 5
	// defaultOffGateWarpRangeFuel bounds a single warp leg's fuel: an off-gate system whose
	// nearest-edge leg costs more than this is out of range and excluded from selection. ~one
	// explorer tank (CRUISE fuel ≈ inter-system distance); refined in slice C once the explorer
	// hull's real capacity is known.
	defaultOffGateWarpRangeFuel = 400
	// defaultOffGateValueWeight and defaultOffGateFuelWeight weight the target-ranking score
	// (score = value_weight*explorationValue − fuel_weight*warpFuel): value favors promising-type
	// unexplored systems, fuel penalizes warp distance from the frontier edge. The default 10-vs-1
	// makes proximity the tiebreak among equal-value systems while a promising type can still
	// outrank a nearer barren one.
	defaultOffGateValueWeight = 10
	defaultOffGateFuelWeight  = 1
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
	// QuoteProbe returns the price of the demand-proximal reachable probe and the yard that
	// sells it (sp-hej4: the yard nearest target, or the home yard when target is empty). An
	// error (no yard, unpriceable) fails the purchase closed.
	QuoteProbe(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (price int, yard string, err error)
	// BuyProbe purchases one probe at the target-selected yard, refusing any fill whose price
	// exceeds maxBudget. It returns the actual price paid and the new hull's symbol.
	BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int, target probebuy.ProbeTarget) (price int, shipSymbol string, err error)
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

	// BranchRoot is the hop-1 ancestor system on the BFS path from the anchor set to this
	// candidate — its CORRIDOR identity on the jump-gate graph (sp-rjgr). A hop-1 system is
	// its own root; a deeper system inherits the hop-1 system it was first reached through;
	// an anchor (hop 0) has none (""). It is the depth slice's "bearing": two deep virgins
	// with DIFFERENT BranchRoots lie down different corridors, so fanning pathfinders across
	// distinct BranchRoots stops the depth drive betting the whole outward push on one
	// direction (a heavy yard could be any way out). Gate topology — not Euclidean position —
	// is the meaningful notion of direction here: adjacent gates can be far apart in space.
	BranchRoot string
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

	// ProximalYardHopPenalty is the demand-proximal probe-buy tradeoff (sp-hej4): credits of
	// price premium accepted per gate-hop closer to the target post's system. <= 0 → default.
	ProximalYardHopPenalty int
	// ProbeSiblingPriceMargin is the sp-iqv2 supply-depletion / load-balance margin: the buy spreads
	// off a yard once a cheaper reachable sibling undercuts it by more than this. <= 0 → default.
	ProbeSiblingPriceMargin int
	// MaxProbePrice is the sp-3u5d per-unit probe price ceiling (credits): the buy DEFERS when the final
	// chosen quote (after sibling-spread) exceeds it. 0 = DISABLED (byte-identical to today); no <=0
	// default fallback — 0 is the real "off" value (the governance gate).
	MaxProbePrice int
	// Depth-vs-breadth balance knobs (sp-rjgr), all live-tunable (FrontierTunableDefaults).
	BreadthFractionPercent int // breadth share; depth = 100 - this. 100 ⇒ pure BFS.
	MaxDepthPathfinders    int // cap on concurrent depth posts
	MaxDepthHops           int // depth scan horizon + per-pathfinder max target depth
	ObjectiveBiasPercent   int // points added to the depth fraction while the heavy-yard objective is unmet

	// Off-gate explorer demand knobs (sp-k645, slice B), all live-tunable (FrontierTunableDefaults).
	OffGateQueueExhaustionCycles int // consecutive empty-queue cycles before off-gate demand fires (trigger a)
	OffGateWarpRangeFuel         int // max warp fuel a single explorer leg may cost (target range bound)
	OffGateValueWeight           int // weight on exploration value in target ranking
	OffGateFuelWeight            int // weight on warp fuel (distance) in target ranking
	// ReservedFreshnessFloor (sp-iopd) is the count of idle probes the frontier treats as
	// reserved for the freshness sizer: it discounts them from the idle supply that covers its
	// own demand, so it never cannibalizes scanning's baseline. 0 → the floor is off (pre-sp-iopd
	// behavior). Live-tunable (FrontierTunableDefaults).
	ReservedFreshnessFloor int

	// DiscoveryShare (sp-pvw3) is the 0-100 percent of each cycle's post-declaration capacity spent
	// CHARTING VIRGIN systems; the complement (100 - share) drains the dark-market backlog. 100 =
	// pure discovery (== old scan_only=0); 0 = pure backlog-scan (== old scan_only=1); 50 = a
	// balanced concurrent split. <= 0 (or unset) folds through the deprecated ScanOnly alias, else
	// the documented default. Live-tunable (FrontierTunableDefaults).
	DiscoveryShare int

	// ScanOnly (sp-jide) is the DEPRECATED binary predecessor of DiscoveryShare, kept as a read-only
	// alias so an operator who tuned it still gets the equivalent share (1 ↔ share 0, 0 ↔ the
	// default). resolveConfig folds it into DiscoveryShare; nothing else reads it. Prefer
	// discovery_share.
	ScanOnly int
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

	// darkScanner (sp-jide) enumerates the FULL charted-but-unscanned MARKET backlog the scan_only
	// mode sweeps — every system with MARKETPLACE waypoints and zero player market_data, unbounded
	// by gate hops (unlike scanner's expansion-frontier BFS). Optional-injection via
	// SetDarkMarketScanner; nil (or scan_only=0) leaves the coordinator byte-identical to pre-sp-jide.
	darkScanner DarkMarketScanner

	// objective is the optional deep-resource (heavy-yard) signal the depth slice biases on
	// (sp-rjgr §4). Nil ⇒ the split runs on its baseline fraction with no objective shift.
	objective DepthObjectiveReader

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

	// Off-gate explorer demand signal (sp-k645, slice B). offGateSelector ranks the warp
	// target; shipyardCoverage guards trigger (b); offGate holds the cross-tick empty-queue
	// streak + the latest per-player signal slice C reads via OffGateDemand. All optional:
	// a nil selector makes the whole hook a no-op (byte-identical to pre-slice-B). Wired and
	// evaluated in off_gate_demand.go.
	offGateSelector  OffGateTargetSelector
	shipyardCoverage ShipyardCoverageReader
	offGate          *offGateDemandTracker

	// Explorer buy+dispatch seam (sp-a3yn, slice C). offGateSink mirrors each tick's off-gate signal
	// out to the fleet autosizer's explorer demand provider (the cross-coordinator BRIDGE — the buy
	// side); explorerDispatch warps a bought+dedicated idle explorer to the selected off-gate target
	// via slice-A ExecuteWarpRoute (the dispatch side). BOTH optional-injection: nil sink / nil
	// dispatch make their hooks no-ops, so the coordinator is byte-identical to pre-slice-C when
	// unwired. Wired + driven in off_gate_dispatch.go.
	offGateSink      OffGateDemandSink
	explorerDispatch ExplorerDispatchPort
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
		"max_spend_per_cycle":        defaultMaxSpendPerCycle,
		"purchase_cooldown_secs":     int(defaultPurchaseCooldown / time.Second),
		"max_probe_fleet":            defaultMaxProbeFleet,
		"proximal_yard_hop_penalty":  defaultProximalYardHopPenalty,
		"probe_sibling_price_margin": defaultProbeSiblingPriceMargin,
		// sp-3u5d per-unit probe price ceiling — the BACKSTOP for reachability-bound deep yards.
		"max_probe_price": defaultMaxProbePrice,
		// Depth-vs-breadth balance (sp-rjgr) — retunable live with no restart.
		"breadth_fraction_percent": defaultBreadthFractionPercent,
		"max_depth_pathfinders":    defaultMaxDepthPathfinders,
		"max_depth_hops":           defaultMaxDepthHops,
		"objective_bias_percent":   defaultObjectiveBiasPercent,
		// Off-gate explorer demand + target selection (sp-k645, slice B).
		"off_gate_queue_exhaustion_cycles": defaultOffGateQueueExhaustionCycles,
		"off_gate_warp_range_fuel":         defaultOffGateWarpRangeFuel,
		"off_gate_value_weight":            defaultOffGateValueWeight,
		"off_gate_fuel_weight":             defaultOffGateFuelWeight,
		"reserved_freshness_floor":         defaultReservedFreshnessFloor,
		// Discovery/scan budget split (sp-pvw3): declare both discovery and dark-market scan posts
		// each cycle, split by this ratio (100 = pure discovery, 0 = pure scan, 50 = balanced).
		"discovery_share": defaultDiscoveryShare,
		// scan_only (sp-jide) is the DEPRECATED binary alias, superseded by discovery_share. Kept so
		// a persisted scan_only value still resolves to the equivalent share; prefer discovery_share.
		"scan_only": defaultScanOnly,
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
	ProximalYardHopPenalty   int
	ProbeSiblingPriceMargin  int
	MaxProbePrice            int
	BreadthFractionPercent   int
	MaxDepthPathfinders      int
	MaxDepthHops             int
	ObjectiveBiasPercent     int
	// Off-gate explorer demand + target selection (sp-k645, slice B).
	OffGateQueueExhaustionCycles int
	OffGateWarpRangeFuel         int
	OffGateValueWeight           int
	OffGateFuelWeight            int
	ReservedFreshnessFloor       int
	// DiscoveryShare (sp-pvw3) is the effective 0-100 discovery/scan split for this tick, already
	// folded from the discovery_share knob and the deprecated scan_only alias by resolveConfig.
	DiscoveryShare int
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
		ProximalYardHopPenalty:   cmd.ProximalYardHopPenalty,
		ProbeSiblingPriceMargin:  cmd.ProbeSiblingPriceMargin,
		MaxProbePrice:            cmd.MaxProbePrice,
		BreadthFractionPercent:   cmd.BreadthFractionPercent,
		MaxDepthPathfinders:      cmd.MaxDepthPathfinders,
		MaxDepthHops:             cmd.MaxDepthHops,
		ObjectiveBiasPercent:     cmd.ObjectiveBiasPercent,

		OffGateQueueExhaustionCycles: cmd.OffGateQueueExhaustionCycles,
		OffGateWarpRangeFuel:         cmd.OffGateWarpRangeFuel,
		OffGateValueWeight:           cmd.OffGateValueWeight,
		OffGateFuelWeight:            cmd.OffGateFuelWeight,
		ReservedFreshnessFloor:       cmd.ReservedFreshnessFloor,
	}
	// sp-pvw3: seed the raw discovery_share knob and the deprecated scan_only alias from the launch
	// command; a live snapshot (below) overrides both. They fold to one effective share after.
	discoveryShare := cmd.DiscoveryShare
	scanOnly := cmd.ScanOnly
	if live != nil {
		c.MaxProbeFleet = live.PositiveIntOrZero("max_probe_fleet")
		c.MaxSpendPerCycle = live.PositiveIntOrZero("max_spend_per_cycle")
		c.PurchaseCooldown = time.Duration(live.PositiveIntOrZero("purchase_cooldown_secs")) * time.Second
		c.ProximalYardHopPenalty = live.PositiveIntOrZero("proximal_yard_hop_penalty")
		c.ProbeSiblingPriceMargin = live.PositiveIntOrZero("probe_sibling_price_margin")
		// sp-3u5d probe price ceiling: live-authoritative. Absent/zeroed ⇒ 0 (ceiling OFF), the
		// documented default — NO <=0 fallback, since 0 IS the default here (the governance gate).
		c.MaxProbePrice = live.PositiveIntOrZero("max_probe_price")
		c.BreadthFractionPercent = live.PositiveIntOrZero("breadth_fraction_percent")
		c.MaxDepthPathfinders = live.PositiveIntOrZero("max_depth_pathfinders")
		c.MaxDepthHops = live.PositiveIntOrZero("max_depth_hops")
		c.ObjectiveBiasPercent = live.PositiveIntOrZero("objective_bias_percent")
		c.OffGateQueueExhaustionCycles = live.PositiveIntOrZero("off_gate_queue_exhaustion_cycles")
		c.OffGateWarpRangeFuel = live.PositiveIntOrZero("off_gate_warp_range_fuel")
		c.OffGateValueWeight = live.PositiveIntOrZero("off_gate_value_weight")
		c.OffGateFuelWeight = live.PositiveIntOrZero("off_gate_fuel_weight")
		// sp-iopd reserved freshness floor: live-authoritative. Absent/zeroed ⇒ 0 (floor OFF),
		// the documented default — no <=0 fallback, since 0 IS the default here.
		c.ReservedFreshnessFloor = live.PositiveIntOrZero("reserved_freshness_floor")
		// sp-pvw3 discovery_share + the deprecated scan_only alias: live-authoritative. A present
		// snapshot governs, so a `tune discovery_share <N>` (or a legacy `tune scan_only …`) lands
		// next tick with no restart.
		discoveryShare = live.PositiveIntOrZero("discovery_share")
		scanOnly = live.PositiveIntOrZero("scan_only")
	}
	// Fold the knob + deprecated alias into one effective share in [0,100] (the default when unset).
	c.DiscoveryShare = resolveDiscoveryShare(discoveryShare, scanOnly)
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
	if c.ProximalYardHopPenalty <= 0 {
		c.ProximalYardHopPenalty = defaultProximalYardHopPenalty
	}
	if c.ProbeSiblingPriceMargin <= 0 {
		c.ProbeSiblingPriceMargin = defaultProbeSiblingPriceMargin
	}
	if c.BreadthFractionPercent <= 0 {
		c.BreadthFractionPercent = defaultBreadthFractionPercent
	}
	if c.BreadthFractionPercent > 100 {
		c.BreadthFractionPercent = 100 // clamp: breadth is a percent (depth = 100 - breadth)
	}
	if c.MaxDepthPathfinders <= 0 {
		c.MaxDepthPathfinders = defaultMaxDepthPathfinders
	}
	if c.MaxDepthHops <= 0 {
		c.MaxDepthHops = defaultMaxDepthHops
	}
	if c.ObjectiveBiasPercent <= 0 {
		c.ObjectiveBiasPercent = defaultObjectiveBiasPercent
	}
	if c.OffGateQueueExhaustionCycles <= 0 {
		c.OffGateQueueExhaustionCycles = defaultOffGateQueueExhaustionCycles
	}
	if c.OffGateWarpRangeFuel <= 0 {
		c.OffGateWarpRangeFuel = defaultOffGateWarpRangeFuel
	}
	if c.OffGateValueWeight <= 0 {
		c.OffGateValueWeight = defaultOffGateValueWeight
	}
	if c.OffGateFuelWeight <= 0 {
		c.OffGateFuelWeight = defaultOffGateFuelWeight
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

	// sp-pvw3 DISCOVERY/SCAN SPLIT (replacing the sp-jide binary scan_only): each cycle declares BOTH
	// discovery posts (chart virgin) AND scan posts (drain the dark-market backlog), dividing the
	// per-cycle post-declaration capacity by discovery_share with GRACEFUL DEGRADATION. The plan
	// consults each side's backlog LAZILY, so a pure-discovery cycle (share 100) never touches the
	// dark scanner and a pure-scan cycle (share 0) never touches the expansion scanner — the extremes
	// keep the old scan_only call profile.
	plan := h.planCapacitySplit(ctx, cmd, cfg, posts)
	// pureScan (backlog-only served) suppresses probe buys and the off-gate explorer hooks exactly as
	// the old scan_only=1: draining discovered markets rides idle relays, it never grows the fleet.
	pureScan := plan.scanBudget > 0 && plan.discoveryBudget == 0

	openSlots := countUnmannedSlots(posts)
	frontierPosts := countSweepOncePosts(posts)

	// SUPPLY: idle, undedicated (or scout-tagged) satellites the reconciler can relay.
	available, err := h.availableProbes(ctx, cmd)
	if err != nil {
		return err
	}
	availableCount := len(available)

	declared := ""
	if !pureScan {
		// OFF-GATE DEMAND (sp-k645) + EXPLORER DISPATCH (sp-a3yn): discovery-side signals. They run
		// whenever discovery is not fully suppressed — including a dry cycle, so queue exhaustion is
		// still detected — over the plan's already-ranked queue (no re-scan).
		h.evaluateOffGateDemand(ctx, cmd, cfg, len(plan.discoveryQueue))
		h.dispatchOffGateExplorer(ctx, cmd)
	}
	if plan.discoveryBudget > 0 {
		// DEPLOY: declare the top uncovered frontier system as a sweep-once post, bounded by the
		// in-flight cap so declaration never outruns what the fleet can man (pin #3).
		declared = h.declareBreadthHead(ctx, cmd, cfg, posts, plan.discoveryQueue, frontierPosts)
		if declared != "" {
			openSlots++ // the fresh post adds one unmanned slot to this cycle's demand
		}
		// DEPTH slice (sp-rjgr): pathfinders punching OUTWARD along distinct corridors, additive to
		// the breadth head and bounded by the SAME in-flight cap. Its would-be posts add to demand.
		openSlots += h.dispatchDepthPathfinders(ctx, cmd, cfg, posts, declared, frontierPosts)
	}

	// PURCHASE: buy one probe iff the fleet is short of open manning demand and every guard passes —
	// never in a pure-scan cycle (draining the backlog spends nothing, == old scan_only=1). The
	// demand-proximal target (sp-hej4) is the just-declared post, else the first pre-existing open slot.
	purchaseReason := "no purchase: pure backlog-scan cycle spends nothing (== deprecated scan_only=1)"
	if !pureScan {
		target := declared
		if target == "" {
			target = firstUnmannedSlotSystem(posts)
		}
		purchaseReason = h.decideAndMaybeBuy(ctx, cmd, cfg, openSlots, availableCount, target)
	}

	// SCAN: drain the dark-market backlog, bounded by this cycle's scan budget (skipped when the split
	// reserves nothing for scanning — the pure-discovery case, == old scan_only=0).
	scanDeclared := 0
	if plan.scanBudget > 0 {
		scanDeclared = h.declareScanSweeps(ctx, cmd, cfg, plan.scanBacklog, plan.scanBudget)
	}

	action := purchaseReason
	if declared != "" {
		action = fmt.Sprintf("declared %s; %s", declared, purchaseReason)
	}
	logger.Log("INFO", fmt.Sprintf("Frontier cycle (discovery_share %d%% → %d discovery / %d scan budget): %d open slots, %d discovery queue, %d dark backlog, %d idle probes, %d posts in flight — %s; %d dark sweep(s) declared", cfg.DiscoveryShare, plan.discoveryBudget, plan.scanBudget, openSlots, len(plan.discoveryQueue), len(plan.scanBacklog), availableCount, frontierPosts, action, scanDeclared), map[string]interface{}{
		"action":           "frontier_expansion_cycle",
		"discovery_share":  cfg.DiscoveryShare,
		"discovery_budget": plan.discoveryBudget,
		"scan_budget":      plan.scanBudget,
		"open_slots":       openSlots,
		"discovery_queue":  len(plan.discoveryQueue),
		"dark_backlog":     len(plan.scanBacklog),
		"idle_probes":      availableCount,
		"frontier_posts":   frontierPosts,
		"scan_declared":    scanDeclared,
		"dry_run":          cmd.DryRun,
		"outcome":          action,
	})
	return nil
}

// declareBreadthHead declares the top-ranked uncovered frontier system from the discovery queue as a
// single-hull sweep-once post (the breadth head), bounded by the in-flight cap so declaration never
// outruns what the fleet can man (pin #3). It returns the declared system ("" when the queue is empty,
// the cap is reached, the head already has a post, or the write failed). Extracted verbatim from the
// pre-sp-pvw3 inline DEPLOY block so the discovery-side behavior is unchanged when the split activates it.
func (h *RunFrontierExpansionCoordinatorHandler) declareBreadthHead(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
	queue []queueEntry,
	frontierPosts int,
) string {
	logger := common.LoggerFromContext(ctx)
	if len(queue) == 0 || frontierPosts >= cfg.MaxFrontierPostsInFlight {
		return ""
	}
	head := queue[0]
	if hasPost(posts, head.SystemSymbol) {
		return ""
	}
	if cmd.DryRun {
		logger.Log("INFO", fmt.Sprintf("DRY-RUN: would declare sweep-once frontier post %s (score %d, %d markets, %d hops, virgin=%v)", head.SystemSymbol, head.Score, head.KnownMarkets, head.Hops, head.Virgin), map[string]interface{}{
			"action":        "frontier_declare_dryrun",
			"system_symbol": head.SystemSymbol,
		})
		return head.SystemSymbol
	}
	if err := h.declareSweepOncePost(ctx, cmd, cfg, head.SystemSymbol); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to declare frontier sweep-once post %s: %v", head.SystemSymbol, err), nil)
		return ""
	}
	logger.Log("INFO", fmt.Sprintf("Declared frontier sweep-once post %s (score %d, %d markets, %d hops, virgin=%v) — reconciler will relay a probe", head.SystemSymbol, head.Score, head.KnownMarkets, head.Hops, head.Virgin), map[string]interface{}{
		"action":        "frontier_post_declared",
		"system_symbol": head.SystemSymbol,
		"score":         head.Score,
		"markets":       head.KnownMarkets,
		"hops":          head.Hops,
		"virgin":        head.Virgin,
	})
	return head.SystemSymbol
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
	targetSystem string,
) string {
	logger := common.LoggerFromContext(ctx)
	// The demand-proximal yard hint handed to the quote+buy (sp-hej4). SELECTION only — every
	// guard below is unchanged and gates the buy on the quoted price of the selected yard.
	target := probebuy.ProbeTarget{System: targetSystem, HopPenaltyCredits: cfg.ProximalYardHopPenalty, SiblingPriceMarginCredits: cfg.ProbeSiblingPriceMargin, MaxProbePriceCredits: cfg.MaxProbePrice}

	// A named target must exist: an unmanned slot on a declared post. The expansion
	// queue only becomes a target once declared into a post (above), so gating on
	// openSlots is the "a slot ... target" rule — no slot, no buy (pin #2).
	if openSlots == 0 {
		return "no purchase: no unmanned slot to serve"
	}

	// SYMMETRIC FRESHNESS FLOOR (sp-iopd): the frontier DISCOUNTS reserved_freshness_floor idle
	// probes as reserved for the freshness sizer (sp-orgp) — it will not count them toward covering
	// its OWN coverage demand, so an aggressive frontier GROWS the pool with a guarded buy rather
	// than cannibalizing scanning below a relaxed baseline. floor 0 (default) leaves
	// effectiveAvailable == availableCount, i.e. exact pre-sp-iopd behavior. The reconciler still
	// relays whichever idle probes it chooses; this governs only whether the frontier BUYS — the
	// coordinator never claims a hull (RULINGS #7).
	effectiveAvailable := availableCount - cfg.ReservedFreshnessFloor
	if effectiveAvailable < 0 {
		effectiveAvailable = 0
	}

	// Fleet short? If the frontier's (floor-discounted) idle probes already cover the open demand,
	// the reconciler will relay them — buying while an idle probe can serve the target is the bug (pin #2).
	if effectiveAvailable >= openSlots {
		return fmt.Sprintf("no purchase: supply covers demand (%d idle − %d reserved-freshness = %d >= %d open slots)", availableCount, cfg.ReservedFreshnessFloor, effectiveAvailable, openSlots)
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
	price, yard, err := h.purchaser.QuoteProbe(ctx, cmd.PlayerID, target)
	if err != nil {
		return fmt.Sprintf("no purchase: probe unpriceable (fail-closed): %v", err)
	}

	// Per-unit price ceiling (sp-3u5d): the BACKSTOP for the deepest-frontier tail whose ONLY reachable
	// yard is a depleted deep one. QuoteProbe has already run the sibling-spread, so `price` is the
	// FINAL cheapest reachable yard's price; when no cheaper sibling exists it can spiral to 210-235k.
	// If the ceiling is set (>0) and that final price exceeds it, DEFER — leave the post dark and retry
	// next cycle (price may recover or a nearer yard become reachable). A normal no-op like the spend
	// cap: never spends, never errors, never strands the loop. Ceiling 0 = DISABLED (byte-identical to
	// pre-sp-3u5d). Placed before the dry-run branch so a dry-run reports the deferral, not a "would buy".
	if cfg.MaxProbePrice > 0 && price > cfg.MaxProbePrice {
		return fmt.Sprintf("no purchase: probe price %d exceeds ceiling %d at yard %s (deferred, retry next cycle)", price, cfg.MaxProbePrice, yard)
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

	paid, sym, err := h.purchaser.BuyProbe(ctx, cmd.PlayerID, treasuryCap, target)
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

// firstUnmannedSlotSystem returns the system of the first post carrying an OPEN slot (no hull,
// no relay in flight) — the demand-proximal buy target when no fresh post was declared this cycle
// (sp-hej4). It mirrors countUnmannedSlots' open-slot predicate; "" when every slot is served.
func firstUnmannedSlotSystem(posts []*domainScouting.ScoutPost) string {
	for _, post := range posts {
		for _, slot := range post.Slots() {
			if slot.AssignedHull() == "" && slot.RepositionContainerID() == "" {
				return post.SystemSymbol
			}
		}
	}
	return ""
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
