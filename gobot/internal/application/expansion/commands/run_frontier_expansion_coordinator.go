// Package commands holds the frontier expansion coordinator (sp-8w89): a standing
// daemon coordinator that CLOSES the manual frontier loop. Today a human measures
// coverage gaps, buys probes (RULINGS #6), and declares posts/sweeps; the engine's
// scout-post reconciler then mans them. This coordinator does the MEASURING, BUYING,
// and DECLARING — while ALL movement and manning stay with the existing machinery
// (the scout-post reconciler, the jump relays, and nn0y virgin discovery).
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
	// never weakened") — unlike the fleet CAP, which raises or lowers how much the
	// coordinator may expand but can never let a single buy breach 25%.
	maxTreasuryFractionPercent = 25

	// Config defaults (RULINGS #5: every operational value is a flag/config key, filled
	// here only when the launch config leaves it unset). Documented in the dispatch report.
	defaultTickSeconds              = 60
	defaultMaxProbeFleet            = 40               // total satellite cap (current fleet + headroom)
	defaultExpansionMaxHops         = 3                // gate-graph reach for the expansion queue
	defaultMaxFrontierPostsInFlight = 5                // outstanding sweep-once posts cap (bounds runaway declaration)
	defaultFrontierFreshness        = 60 * time.Minute // sweep-once post freshness target

	// Ranking weights (RULINGS #5). Score = markets*known − hops*penalty + virginBonus.
	defaultWeightKnownMarket = 10
	defaultWeightHopPenalty  = 5
	defaultWeightVirginBonus = 15

	// hopPenaltyCredits + siblingPriceMarginCredits are the INTERNAL probe-sourcing consts
	// (sp-tlekc §2D — no longer operator knobs). hopPenaltyCredits is the demand-proximal
	// price/distance tradeoff: the credits of price premium accepted per gate-hop closer to the
	// target post, so a nearer-but-pricier yard beats a far-cheaper one iff the per-hop saving
	// clears it. siblingPriceMarginCredits is the supply-depletion / load-balance margin:
	// once the proximal yard's scanned price exceeds the cheapest reachable sibling by more than
	// this, the buy spreads to the sibling instead of spiraling one market to 4x. They
	// mirror the freshness sizer's probebuy.Default* values, keeping the two coordinators' sourcing
	// identical; the sourcing MECHANISM is unchanged, only the operator KNOBS were removed.
	hopPenaltyCredits         = probebuy.DefaultHopPenaltyCredits
	siblingPriceMarginCredits = probebuy.DefaultSiblingPriceMarginCredits

	// maxProbePrice is the IMMUTABLE per-unit probe PRICE CEILING (sp-tlekc §2C — no longer an
	// operator knob). It is the anti-OVERPAY backstop for the deepest-frontier tail whose ONLY
	// reachable yard is a depleted deep one: with no cheaper reachable sibling for the sibling-spread
	// to fall to, the live price spirals to 210-235k. When the FINAL chosen quote (after the
	// sibling-spread) exceeds this, the buy DEFERS — the post stays dark and retries next cycle. It
	// is NOT a solvency guard (solvency is the 25% rule + the fleet cap + the 50k working-capital
	// floor); it caps only per-unit OVERPAY, so an immutable const is MORE RULINGS-clean than a
	// tunable — it can never be tuned OFF (into a spiral) or UP. 100000 is ~2x the ~55k baseline
	// probe top, well under the spiral.
	maxProbePrice = 100000

	// defaultReservedFreshnessFloor is the sp-iopd SYMMETRIC freshness floor: N idle probes the
	// frontier treats as UNAVAILABLE (reserved for the market-freshness sizer). The frontier
	// discounts them from the idle supply it counts toward covering its OWN coverage demand, so an
	// aggressive frontier GROWS the pool with a guarded buy rather than cannibalizing scanning below
	// a relaxed baseline. 0 (the default) is the floor OFF. It is the ONE retained expert guard —
	// half of the two-coordinator mirror with the freshness sizer's reserved_frontier_floor (retired
	// in Phase 4). Live-tunable (FrontierTunableDefaults).
	defaultReservedFreshnessFloor = 0

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
	// QuoteProbe returns the price of the demand-proximal reachable probe and the yard that
	// sells it (the yard nearest target, or the home yard when target is empty). An
	// error (no yard, unpriceable) fails the purchase closed.
	QuoteProbe(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (price int, yard string, err error)
	// BuyProbe purchases one probe at the target-selected yard, refusing any fill whose price
	// exceeds maxBudget. It returns the actual price paid and the new hull's symbol.
	BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int, target probebuy.ProbeTarget) (price int, shipSymbol string, err error)
}

// ProbeBuyerPositioner breaks the "probe unpriceable" stall (sp-255rz). QuoteProbe fails closed when
// no idle undedicated hull sits at a probe-selling shipyard AND no priced yard is reachable from the
// deep target — so the coordinator can never price a probe and fleet growth halts forever once the
// fleet drifts from yards. On that stall this positioner LOCATES the nearest reachable scanned
// probe-yard from an eligible hull's OWN system and RELAYS that hull there, so the NEXT tick's
// presence-gated live PriceCheck reads and the in-place buy clears. It mirrors the home-yard
// ShipyardScanner idiom for the frontier. It NEVER buys and NEVER weakens a money guard (RULINGS #4)
// — positioning only makes the price READABLE; if pricing still fails (e.g. over the ceiling) the buy
// stays fail-closed. It NEVER poaches a dedicated hull (RULINGS #7 — only an idle, undedicated,
// non-transit hull is eligible). Idempotent + best-effort (RULINGS #2): dispatched=false when a hull
// is already at / en route to a yard, no reachable probe-yard is known, or no eligible hull is free.
// Unset (nil) → the coordinator stays fail-closed byte-identical to before.
type ProbeBuyerPositioner interface {
	PositionProbeBuyer(ctx context.Context, playerID shared.PlayerID) (dispatched bool, err error)
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
	// undiscovered). Scanned supplies that missing distinction: a Scanned system
	// with KnownMarkets==0 is genuinely marketless and buildExpansionQueue DROPS it (its
	// markets were looked for and none exist — re-scouting it every cycle is waste); a
	// !Scanned system stays a scout target. The scanner derives it from the waypoint
	// catalog — a persisted non-gate waypoint proves a real sweep (adapters.go).
	Scanned bool

	// BranchRoot is the hop-1 ancestor system on the BFS path from the anchor set to this
	// candidate — its CORRIDOR identity on the jump-gate graph. A hop-1 system is
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

	MaxProbeFleet            int // total satellite cap — the sole probe-fleet throttle (sp-tlekc)
	ExpansionMaxHops         int // gate-graph reach for the queue
	MaxFrontierPostsInFlight int // outstanding sweep-once posts cap
	FrontierFreshnessSecs    int // declared sweep-once freshness target

	WeightKnownMarket int
	WeightHopPenalty  int
	WeightVirginBonus int

	// ReservedFreshnessFloor (sp-iopd) is the count of idle probes the frontier treats as
	// reserved for the freshness sizer: it discounts them from the idle supply that covers its
	// own demand, so it never cannibalizes scanning's baseline. 0 → the floor is off. Live-tunable.
	ReservedFreshnessFloor int

	// DiscoveryShare (sp-pvw3) is the LEGACY discovery/scan key, retained ONLY as a read-through
	// migration alias for the sp-tlekc rename to DiscoverScanBalance: a persisted discovery_share
	// still resolves to the equivalent share until it is re-tuned as discover_scan_balance. Nothing
	// writes this key any more; prefer DiscoverScanBalance.
	DiscoveryShare int

	// DiscoverScanBalance (sp-tlekc) is THE operator dial for the discovery/scan split (0-100):
	// percent of each cycle's post-declaration capacity spent CHARTING VIRGIN systems; the rest
	// drains the dark-market backlog. Authoritative when set (> 0), else the legacy DiscoveryShare
	// alias, else the documented default. Live-tunable (FrontierTunableDefaults).
	DiscoverScanBalance int

	// ReachMode (sp-tlekc) is THE operator dial for the frontier's reach: 1=shallow, 2=balanced
	// (the live snapshot, the default), 3=deep. It composes the depth/breadth balance, the reuse
	// relay, the off-gate explorer signal, and the reap timeout from ONE coherent preset
	// (applyReachPreset), couplings honored by construction. Live-tunable (FrontierTunableDefaults).
	ReachMode int
}

// ProbeReuseTarget is the reuse-relay request: relay the nearest EXISTING probe to
// System, bounded by MaxHops (reach measured from the probe, not the buyer home), borrowing only
// off a system whose trade-value is below ValueCeiling. It mirrors probebuy.ProbeTarget — the
// caller bundles its tunables into the target so the port stays a single narrow call.
type ProbeReuseTarget struct {
	System       string
	MaxHops      int
	ValueCeiling int
}

// ProbeReuseRelayer relays an EXISTING probe from the charted frontier edge to a target virgin,
// the reuse-before-buy path that fixes the deep-frontier deadlock: instead of buying a
// probe at an unreachable deep yard and relaying a BUYER there, it hops a probe already parked at
// the edge (~1 gate) onto the target. Relay-reach is anchored to the nearest usable probe, so
// "unreachable within 5 jumps of the buyer home" becomes "one hop from the edge". A nil relayer
// (unwired, or reuse disarmed) leaves the coordinator byte-identical to the buy-only path.
type ProbeReuseRelayer interface {
	// RelayNearestProbe relays the nearest reusable probe to target.System within target.MaxHops,
	// borrowing only off a system whose trade-value is below target.ValueCeiling. It returns
	// (shipSymbol, true, nil) on a committed relay; ("", false, nil) when no reusable probe is
	// within reach / under the ceiling (the coordinator then falls back to buying); a non-nil
	// error fails the staffing closed for this target (the coordinator logs and does not buy blind).
	RelayNearestProbe(ctx context.Context, playerID shared.PlayerID, target ProbeReuseTarget) (shipSymbol string, ok bool, err error)
}

// FrontierNeighborReader answers the snowball walk: the uncharted gate-neighbors of a
// system the frontier has CHARTED. A still-virgin system has no persisted gate edges, so it yields
// none — the reader self-gates the walk to genuinely-charted systems. A nil reader (unwired, or
// snowball disarmed) makes the walk a no-op.
type FrontierNeighborReader interface {
	// UnchartedNeighbors returns systemSymbol's gate-adjacent systems that are NOT yet charted —
	// the next ring a probe walks onto after charting systemSymbol.
	UnchartedNeighbors(ctx context.Context, playerID int, systemSymbol string) ([]string, error)
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

	// probePositioner breaks the "probe unpriceable" stall (sp-255rz): when QuoteProbe fails
	// closed it relays an eligible idle undedicated hull to a reachable probe-yard so the next
	// tick's live price reads. Optional-injection; nil keeps the previous fail-closed behavior
	// byte-identical (never buys, never poaches — RULINGS #4/#7).
	probePositioner ProbeBuyerPositioner

	// darkScanner enumerates the FULL charted-but-unscanned MARKET backlog the scan_only
	// mode sweeps — every system with MARKETPLACE waypoints and zero player market_data, unbounded
	// by gate hops (unlike scanner's expansion-frontier BFS). Optional-injection via
	// SetDarkMarketScanner; nil (or scan_only=0) leaves the coordinator byte-identical to before.
	darkScanner DarkMarketScanner

	// objective is the optional deep-resource (heavy-yard) signal the depth slice biases on
	// (sp-rjgr §4). Nil ⇒ the split runs on its baseline fraction with no objective shift.
	objective DepthObjectiveReader

	// captainEvents emits the coordinator error-loop event (rollout sp-6wxq)
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

	// Off-gate explorer demand signal (slice B). offGateSelector ranks the warp
	// target; shipyardCoverage guards trigger (b); offGate holds the cross-tick empty-queue
	// streak + the latest per-player signal slice C reads via OffGateDemand. All optional:
	// a nil selector makes the whole hook a no-op (byte-identical to pre-slice-B). Wired and
	// evaluated in off_gate_demand.go.
	offGateSelector  OffGateTargetSelector
	shipyardCoverage ShipyardCoverageReader
	offGate          *offGateDemandTracker

	// Explorer buy+dispatch seam (slice C). offGateSink mirrors each tick's off-gate signal
	// out to the fleet autosizer's explorer demand provider (the cross-coordinator BRIDGE — the buy
	// side); explorerDispatch warps a bought+dedicated idle explorer to the selected off-gate target
	// via slice-A ExecuteWarpRoute (the dispatch side). BOTH optional-injection: nil sink / nil
	// dispatch make their hooks no-ops, so the coordinator is byte-identical to pre-slice-C when
	// unwired. Wired + driven in off_gate_dispatch.go.
	offGateSink      OffGateDemandSink
	explorerDispatch ExplorerDispatchPort

	// Reuse-before-buy. reuseRelayer hops an EXISTING edge probe onto a target virgin
	// (the deadlock fix); neighborReader answers the snowball walk. BOTH optional-injection: a nil
	// relayer/reader — or the DEFAULT-SAFE knobs (reuse/snowball OFF) — makes their hooks no-ops, so
	// the coordinator is byte-identical to the buy-only path until armed next era. Wired in main.go.
	reuseRelayer   ProbeReuseRelayer
	neighborReader FrontierNeighborReader
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

// SetProbeBuyerPositioner wires the "probe unpriceable" stall breaker (sp-255rz): on a fail-closed
// quote it positions an eligible idle undedicated hull at a reachable probe-yard so pricing resumes
// next tick. Leaving it unset keeps the coordinator fail-closed byte-identical (never poaches,
// never buys — RULINGS #4/#7).
func (h *RunFrontierExpansionCoordinatorHandler) SetProbeBuyerPositioner(p ProbeBuyerPositioner) {
	h.probePositioner = p
}

// SetExpansionScanner wires the frontier enumerator. Leaving it unset disables the
// expansion queue; the coordinator then serves only unmanned-slot demand.
func (h *RunFrontierExpansionCoordinatorHandler) SetExpansionScanner(s ExpansionScanner) {
	h.scanner = s
}

// SetEventRecorder wires the captain outbox the coordinator emits its error-loop
// event through. Optional-injection like the other setters: without it
// the streak monitor still tracks and logs, it just cannot escalate to a captain
// event (nil-safe, see health.RecordErrorLoop).
func (h *RunFrontierExpansionCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetLiveConfigReader wires the per-tick live-config snapshot source (sp-vwek), making
// the tunable knobs (FrontierTunableDefaults) honor `spacetraders tune` on the next
// tick. Leaving it unset keeps every knob launch-frozen (the previous behavior).
func (h *RunFrontierExpansionCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// SetProbeReuseRelayer wires the reuse-before-buy edge-probe relayer. Leaving it unset
// — or leaving probe_reuse_enabled at its default 0 — keeps the coordinator on the buy-only path,
// byte-identical to today.
func (h *RunFrontierExpansionCoordinatorHandler) SetProbeReuseRelayer(r ProbeReuseRelayer) {
	h.reuseRelayer = r
}

// SetFrontierNeighborReader wires the snowball walk's uncharted-neighbor source. Leaving
// it unset — or leaving snowball_neighbors at its default 0 — makes the walk a no-op.
func (h *RunFrontierExpansionCoordinatorHandler) SetFrontierNeighborReader(r FrontierNeighborReader) {
	h.neighborReader = r
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
	// observable: once the streak crosses DefaultStreakThreshold it emits a
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

// FrontierTunableDefaults maps every LIVE-tunable frontier-expansion knob to
// its documented default — the value that applies when neither the live container
// config nor the launch command carries a positive one. The daemon's tune bounds
// registry reads THIS map, so the defaults-of-record stay in this file next to the
// consts they mirror. The map's KEY SET is also the contract for which keys
// resolveConfig live-overlays.
func FrontierTunableDefaults() map[string]int {
	return map[string]int{
		// sp-tlekc final operator surface — EXACTLY four knobs. The throttle (max_probe_fleet), the
		// two composing dials (reach_mode, discover_scan_balance), and the one retained expert guard
		// (reserved_freshness_floor, retired in Phase 4). Everything else — the price ceiling, the 50k
		// floor, the hop-penalty/sibling-margin sourcing terms, the 25% rule — is an internal const.
		"max_probe_fleet":          defaultMaxProbeFleet,
		"reach_mode":               defaultReachMode,
		"discover_scan_balance":    defaultDiscoveryShare,
		"reserved_freshness_floor": defaultReservedFreshnessFloor,
	}
}

// frontierConfig is the launch command with every default resolved, so the reconcile logic never
// repeats the "<= 0 → default" fallback (RULINGS #5). The reach/yard/price fields are still read by
// the engine verbatim — the MECHANISM is byte-for-byte unchanged (sp-tlekc) — but they are now
// populated from the reach_mode PRESET and the internal sourcing consts, not from operator knobs.
type frontierConfig struct {
	MaxProbeFleet            int
	ExpansionMaxHops         int
	MaxFrontierPostsInFlight int
	FrontierFreshness        time.Duration
	WeightKnownMarket        int
	WeightHopPenalty         int
	WeightVirginBonus        int
	// Probe-sourcing + anti-overpay ceiling — internal consts now (sp-tlekc §2C/§2D), not knobs.
	ProximalYardHopPenalty  int
	ProbeSiblingPriceMargin int
	MaxProbePrice           int
	// Depth/breadth + off-gate — populated by the reach_mode preset (applyReachPreset).
	BreadthFractionPercent       int
	MaxDepthPathfinders          int
	MaxDepthHops                 int
	ObjectiveBiasPercent         int
	OffGateQueueExhaustionCycles int
	OffGateWarpRangeFuel         int
	OffGateValueWeight           int
	OffGateFuelWeight            int
	ReservedFreshnessFloor       int
	// DiscoveryShare is the effective 0-100 discovery/scan split for this tick, folded from the
	// discover_scan_balance dial (+ the legacy discovery_share migration alias) by resolveConfig.
	DiscoveryShare int
	// Reuse-before-buy + reap — populated by the reach_mode preset. The flags land as bools, the
	// timeout as a Duration.
	ProbeReuseEnabled   bool
	EdgeRelayMaxHops    int
	ReuseValueCeiling   int
	SnowballNeighbors   bool
	PostInflightTimeout time.Duration
}

// resolveConfig resolves one tick's effective config. live is the tick-start snapshot of the
// container's persisted config column (nil when unwired/unreadable). For the four TUNABLE knobs
// (FrontierTunableDefaults) a non-nil snapshot is AUTHORITATIVE: a positive value is the live value
// (the launch verb wrote its values into the same column, so untuned knobs still read their launch
// values here) and an absent/zeroed key means the documented default — the `tune <key> 0` revert.
// The probe-sourcing terms and the price ceiling are now INTERNAL CONSTS (sp-tlekc §2C/§2D); the
// reach/depth/off-gate/reuse/reap values come wholly from the reach_mode PRESET (applyReachPreset).
func resolveConfig(cmd *RunFrontierExpansionCoordinatorCommand, live liveconfig.Snapshot) frontierConfig {
	c := frontierConfig{
		MaxProbeFleet:            cmd.MaxProbeFleet,
		ExpansionMaxHops:         cmd.ExpansionMaxHops,
		MaxFrontierPostsInFlight: cmd.MaxFrontierPostsInFlight,
		FrontierFreshness:        time.Duration(cmd.FrontierFreshnessSecs) * time.Second,
		WeightKnownMarket:        cmd.WeightKnownMarket,
		WeightHopPenalty:         cmd.WeightHopPenalty,
		WeightVirginBonus:        cmd.WeightVirginBonus,
		ReservedFreshnessFloor:   cmd.ReservedFreshnessFloor,
		// Probe sourcing + anti-overpay ceiling are IMMUTABLE internal consts now — the sourcing
		// mechanism is byte-for-byte unchanged, only the operator knobs were removed (sp-tlekc §2C/§2D).
		ProximalYardHopPenalty:  hopPenaltyCredits,
		ProbeSiblingPriceMargin: siblingPriceMarginCredits,
		MaxProbePrice:           maxProbePrice,
	}
	// sp-tlekc final dials: reach_mode (preset selector) + discover_scan_balance (discovery_share
	// promoted 1:1). Seeded from the launch command; a live snapshot overrides. legacyDiscoveryShare
	// is the read-through migration alias for the discovery_share → discover_scan_balance rename, so a
	// coordinator with a persisted discovery_share tune keeps its split until it is re-tuned.
	discoverScanBalance := cmd.DiscoverScanBalance
	legacyDiscoveryShare := cmd.DiscoveryShare
	reachMode := cmd.ReachMode
	if live != nil {
		c.MaxProbeFleet = live.PositiveIntOrZero("max_probe_fleet")
		// sp-iopd reserved freshness floor: live-authoritative. Absent/zeroed ⇒ 0 (floor OFF),
		// the documented default — no <=0 fallback, since 0 IS the default here.
		c.ReservedFreshnessFloor = live.PositiveIntOrZero("reserved_freshness_floor")
		discoverScanBalance = live.PositiveIntOrZero("discover_scan_balance")
		legacyDiscoveryShare = live.PositiveIntOrZero("discovery_share")
		reachMode = live.PositiveIntOrZero("reach_mode")
	}
	// Fold the dial (+ the legacy discovery_share migration alias) into one effective share in
	// [0,100] (the default when unset). discover_scan_balance is authoritative; then the legacy key.
	c.DiscoveryShare = resolveDiscoveryShare(discoverScanBalance, legacyDiscoveryShare)
	if c.MaxProbeFleet <= 0 {
		c.MaxProbeFleet = defaultMaxProbeFleet
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
	// sp-tlekc reach_mode PRESET: applied LAST and ALWAYS. It composes the depth/breadth balance,
	// the reuse relay, the off-gate signal, and the reap timeout from ONE coherent preset —
	// couplings honored by construction (§2B). An unset dial resolves to defaultReachMode (balanced,
	// the live snapshot).
	if reachMode <= 0 {
		reachMode = defaultReachMode
	}
	applyReachPreset(&c, reachMode)
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
// re-derives everything from persisted state (posts, ships), so it never double-declares
// (Upsert is keyed by system). It resolves the tick's config from the live-config snapshot
// taken here — a knob tuned mid-tick lands next tick — then delegates to reconcile.
func (h *RunFrontierExpansionCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) error {
	return h.reconcile(ctx, cmd, resolveConfig(cmd, h.liveConfigSnapshot(ctx, cmd)))
}

// reconcile is one reconcile pass against an ALREADY-RESOLVED config — the seam that separates
// config resolution (reach_mode preset, dial folding, the internal sourcing/price consts) from the
// engine, so the engine's behavior can be driven directly from a frontierConfig. The reach/depth/
// reuse/off-gate MECHANISM is byte-for-byte unchanged (sp-tlekc); only its inputs' provenance moved.
func (h *RunFrontierExpansionCoordinatorHandler) reconcile(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig) error {
	logger := common.LoggerFromContext(ctx)

	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}

	// NO-TIMEOUT DEADLOCK FIX: reap wedged in-flight posts BEFORE measuring demand, so a
	// freed slot opens declaration capacity THIS cycle rather than staying jammed at the cap. It is
	// independent of reuse and a no-op on the default (timeout 0). Survivors drive the rest of the tick.
	posts = h.abandonStalePosts(ctx, cmd, cfg, posts)

	// sp-pvw3 DISCOVERY/SCAN SPLIT (replacing the binary scan_only): each cycle declares BOTH
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
		// OFF-GATE DEMAND + EXPLORER DISPATCH: discovery-side signals. They run
		// whenever discovery is not fully suppressed — including a dry cycle, so queue exhaustion is
		// still detected — over the plan's already-ranked queue (no re-scan).
		h.evaluateOffGateDemand(ctx, cmd, cfg, len(plan.discoveryQueue))
		h.dispatchOffGateExplorer(ctx, cmd)
		// SNOWBALL: walk outward from CHARTED frontier posts — a relayed probe charts a
		// virgin S, then S's uncharted neighbors become the next targets. Additive to the breadth
		// head and bounded by the SAME in-flight cap; its declarations count toward this cycle's
		// demand. Runs on any discovery-capable (non-pure-scan) cycle, so the walk continues even
		// when the ranked expansion queue is momentarily empty. No-op on the default (disarmed).
		snowballed := h.snowballFromChartedPosts(ctx, cmd, cfg, posts, frontierPosts)
		frontierPosts += snowballed
		openSlots += snowballed
	}
	if plan.discoveryBudget > 0 {
		// DEPLOY: declare the top uncovered frontier system as a sweep-once post, bounded by the
		// in-flight cap so declaration never outruns what the fleet can man (pin #3).
		declared = h.declareBreadthHead(ctx, cmd, cfg, posts, plan.discoveryQueue, frontierPosts)
		if declared != "" {
			openSlots++ // the fresh post adds one unmanned slot to this cycle's demand
		}
		// DEPTH slice: pathfinders punching OUTWARD along distinct corridors, additive to
		// the breadth head and bounded by the SAME in-flight cap. Its would-be posts add to demand.
		openSlots += h.dispatchDepthPathfinders(ctx, cmd, cfg, posts, declared, frontierPosts)
	}

	// PURCHASE: buy one probe iff the fleet is short of open manning demand and every guard passes —
	// never in a pure-scan cycle (draining the backlog spends nothing, == old scan_only=1). The
	// demand-proximal target is the just-declared post, else the first pre-existing open slot.
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

// abandonStalePosts is the NO-TIMEOUT deadlock fix: it REMOVES every unmanned, relay-free
// in-flight sweep-once post older than cfg.PostInflightTimeout, returning the survivors. Today
// nothing reaps a post the reconciler cannot man — at the deep edge no yard/buyer is reachable — so
// the in-flight cap jams permanently at 5/5 and discovery stalls at 0. Reaping the wedged posts frees
// their slots so declaration rotates and RETRIES targets, turning the permanent jam into a queue. It
// is INDEPENDENT of reuse (gated only on the timeout knob), so the safety net applies whether or not
// reuse is armed. Timeout 0 (the default) reaps nothing — byte-identical to today. A post being
// SERVED (manned, or a relay airborne) and a STANDING freshness post are never reaped; only a
// genuinely wedged frontier sweep-once qualifies. Dry-run reports the reap but removes nothing.
func (h *RunFrontierExpansionCoordinatorHandler) abandonStalePosts(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
) []*domainScouting.ScoutPost {
	if cfg.PostInflightTimeout <= 0 {
		return posts // disabled — no post is ever reaped, byte-identical to today
	}
	logger := common.LoggerFromContext(ctx)
	now := h.clock.Now()
	survivors := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if !isStaleInFlightPost(post, now, cfg.PostInflightTimeout) {
			survivors = append(survivors, post)
			continue
		}
		age := now.Sub(post.CreatedAt).Round(time.Second)
		if cmd.DryRun {
			logger.Log("INFO", fmt.Sprintf("DRY-RUN: would abandon wedged in-flight frontier post %s (unmanned %s, past %s timeout) to free its slot", post.SystemSymbol, age, cfg.PostInflightTimeout), map[string]interface{}{
				"action":        "frontier_abandon_dryrun",
				"system_symbol": post.SystemSymbol,
			})
			survivors = append(survivors, post) // dry-run acts on nothing
			continue
		}
		if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to abandon wedged frontier post %s: %v", post.SystemSymbol, err), nil)
			survivors = append(survivors, post) // could not remove — keep it in this cycle's view
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Abandoned wedged in-flight frontier post %s (unmanned %s, past %s timeout) — slot freed, target will be retried", post.SystemSymbol, age, cfg.PostInflightTimeout), map[string]interface{}{
			"action":        "frontier_post_abandoned",
			"system_symbol": post.SystemSymbol,
			"age_seconds":   int(now.Sub(post.CreatedAt).Seconds()),
		})
	}
	return survivors
}

// isStaleInFlightPost reports whether post is a genuinely-wedged frontier target: a SWEEP-ONCE post
// (never a standing freshness post) whose EVERY slot is open — no hull AND no relay airborne (not
// being served) — and whose age exceeds the timeout.
func isStaleInFlightPost(post *domainScouting.ScoutPost, now time.Time, timeout time.Duration) bool {
	if post.Kind != domainScouting.PostKindSweepOnce {
		return false
	}
	if !postFullyUnmanned(post) {
		return false
	}
	return now.Sub(post.CreatedAt) > timeout
}

// postFullyUnmanned reports whether every one of post's slots is open (no hull AND no relay in
// flight) — the same open-slot predicate countUnmannedSlots uses, so a post being served by a relay
// is never treated as wedged.
func postFullyUnmanned(post *domainScouting.ScoutPost) bool {
	for _, slot := range post.Slots() {
		if slot.AssignedHull() != "" || slot.RepositionContainerID() != "" {
			return false
		}
	}
	return true
}

// declareBreadthHead declares the top-ranked uncovered frontier system from the discovery queue as a
// single-hull sweep-once post (the breadth head), bounded by the in-flight cap so declaration never
// outruns what the fleet can man (pin #3). It returns the declared system ("" when the queue is empty,
// the cap is reached, the head already has a post, or the write failed). Extracted verbatim from the
// previous inline DEPLOY block so the discovery-side behavior is unchanged when the split activates it.
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

// snowballFromChartedPosts is the walk-outward: after a relayed probe CHARTS a frontier
// virgin S, S's uncharted gate-neighbors become the next targets, so ONE probe walks the frontier
// outward instead of the ranked queue backfilling inward. For each CHARTED frontier (sweep-once)
// post it declares sweep-once posts for that system's uncharted neighbors, deduped against covered
// systems and bounded by the in-flight cap. The neighbor reader SELF-GATES — a still-virgin system
// has no persisted gate edges and yields no neighbors — so iterating every frontier post is safe:
// only genuinely-charted systems snowball. Returns the number of posts declared (0 in dry-run, which
// logs intent only). Armed + wired only; disarmed (the default) or unwired is a pure no-op.
func (h *RunFrontierExpansionCoordinatorHandler) snowballFromChartedPosts(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
	frontierPosts int,
) int {
	if !cfg.SnowballNeighbors || h.neighborReader == nil {
		return 0
	}
	logger := common.LoggerFromContext(ctx)
	covered := postSystemSet(posts)
	declared := 0
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindSweepOnce {
			continue // walk outward from FRONTIER posts — the virgins a probe just charted
		}
		neighbors, err := h.neighborReader.UnchartedNeighbors(ctx, cmd.PlayerID.Value(), post.SystemSymbol)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Snowball neighbor read for %s failed: %v", post.SystemSymbol, err), nil)
			continue
		}
		for _, neighbor := range neighbors {
			if frontierPosts+declared >= cfg.MaxFrontierPostsInFlight {
				return declared // in-flight cap reached — the walk never floods past breadth's allowance
			}
			if covered[neighbor] {
				continue // already has a post (dedup)
			}
			covered[neighbor] = true
			if cmd.DryRun {
				logger.Log("INFO", fmt.Sprintf("DRY-RUN: would snowball frontier post %s (uncharted neighbor of charted %s)", neighbor, post.SystemSymbol), map[string]interface{}{
					"action":        "frontier_snowball_dryrun",
					"system_symbol": neighbor,
					"from_system":   post.SystemSymbol,
				})
				continue // dry-run acts on nothing (declares no post, adds no demand)
			}
			if err := h.declareSweepOncePost(ctx, cmd, cfg, neighbor); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to snowball frontier post %s: %v", neighbor, err), nil)
				continue
			}
			declared++
			logger.Log("INFO", fmt.Sprintf("Snowball declared frontier post %s — uncharted neighbor of charted %s, walking the frontier outward", neighbor, post.SystemSymbol), map[string]interface{}{
				"action":        "frontier_snowball_declared",
				"system_symbol": neighbor,
				"from_system":   post.SystemSymbol,
			})
		}
	}
	return declared
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
	// The demand-proximal yard hint handed to the quote+buy. SELECTION only — every
	// guard below is unchanged and gates the buy on the quoted price of the selected yard.
	target := probebuy.ProbeTarget{System: targetSystem, HopPenaltyCredits: cfg.ProximalYardHopPenalty, SiblingPriceMarginCredits: cfg.ProbeSiblingPriceMargin, MaxProbePriceCredits: cfg.MaxProbePrice, ClaimOwnerContainerID: cmd.ContainerID}

	// A named target must exist: an unmanned slot on a declared post. The expansion
	// queue only becomes a target once declared into a post (above), so gating on
	// openSlots is the "a slot ... target" rule — no slot, no buy (pin #2).
	if openSlots == 0 {
		return "no purchase: no unmanned slot to serve"
	}

	// SYMMETRIC FRESHNESS FLOOR (sp-iopd): the frontier DISCOUNTS reserved_freshness_floor idle
	// probes as reserved for the freshness sizer — it will not count them toward covering
	// its OWN coverage demand, so an aggressive frontier GROWS the pool with a guarded buy rather
	// than cannibalizing scanning below a relaxed baseline. floor 0 (default) leaves
	// effectiveAvailable == availableCount, i.e. exact previous behavior. The reconciler still
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

	// REUSE-BEFORE-BUY: the fleet is short of open manning demand. Before GROWING it with
	// a buy — which deadlocks at the deep edge where no yard/buyer is reachable ("no jump-gate route
	// from VB74 to BK75 within 5 jumps") — try to RELAY an EXISTING probe from the charted frontier
	// edge onto the target, anchoring reach to the nearest usable probe (~1 hop) rather than the far
	// buyer home. It is best-effort BEFORE the buy: a committed relay ends the cycle (0 purchases);
	// no reusable probe within reach / under the ceiling falls straight through to the unchanged buy
	// path. Armed + wired only — disarmed (the default) or unwired skips the whole block, so the
	// coordinator is byte-identical to today's buy-only behavior until armed next era.
	if cfg.ProbeReuseEnabled && h.reuseRelayer != nil {
		if reason, done := h.maybeReuseExistingProbe(ctx, cmd, cfg, targetSystem, openSlots); done {
			return reason
		}
	}

	// Fleet cap (RULINGS #5 ceiling): never grow the satellite fleet past the cap.
	satCount, err := h.satelliteCount(ctx, cmd)
	if err != nil {
		return fmt.Sprintf("no purchase: fleet count unreadable (fail-closed): %v", err)
	}
	if satCount >= cfg.MaxProbeFleet {
		return fmt.Sprintf("no purchase: fleet cap reached (%d/%d satellites)", satCount, cfg.MaxProbeFleet)
	}

	// sp-tlekc §2A: the two ledger-derived RATE GOVERNORS (a per-buy cooldown + a per-window spend
	// cap) are REMOVED — proven redundant. Probe buys stay bounded WITHOUT them by four surviving
	// bounds: ONE buy per tick (structural — decideAndMaybeBuy runs once per ReconcileOnce, 60s tick),
	// the FLEET CAP above (total ≤ fleet × price, a bounded one-time capital cost), the 25% per-buy
	// rule + the 50k working-capital floor below, and the daemon API limiter. The frontier no longer
	// reads the PURCHASE_SHIP ledger for cooldown/spend.

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
		// STALL BREAKER (sp-255rz): the quote failed closed because no hull sits at a probe-selling
		// yard and none is reachable-priced from the deep target. Position an eligible idle
		// undedicated hull at a reachable probe-yard so NEXT tick's live PriceCheck reads (mirrors
		// the sp-hh0h home-yard idiom). It never buys, never poaches (RULINGS #7), never weakens the
		// guard (RULINGS #4) — pricing still gates the buy once readable.
		return h.positionProbeBuyerOnStall(ctx, cmd, err)
	}

	// IMMUTABLE per-unit price ceiling (sp-tlekc §2C): the anti-OVERPAY backstop for the
	// deepest-frontier tail whose ONLY reachable yard is a depleted deep one. QuoteProbe has already
	// run the sibling-spread, so `price` is the FINAL cheapest reachable yard's price; when no cheaper
	// sibling exists it can spiral to 210-235k. If that final price exceeds the ceiling, DEFER — leave
	// the post dark and retry next cycle (price may recover or a nearer yard become reachable). A
	// normal no-op like a covered slot: never spends, never errors, never strands the loop. The
	// ceiling is now an IMMUTABLE const (maxProbePrice, cfg.MaxProbePrice) — always on, un-tunable, so
	// it can never be weakened OFF into a spiral (RULINGS #5).
	if price > cfg.MaxProbePrice {
		return fmt.Sprintf("no purchase: probe price %d exceeds ceiling %d at yard %s (deferred, retry next cycle)", price, cfg.MaxProbePrice, yard)
	}

	// 25% rule (RULINGS #6): price must be at most 25% of live treasury. Integer form
	// price*100 > credits*25 avoids float rounding and never weakens the guard.
	if price*100 > credits*maxTreasuryFractionPercent {
		return fmt.Sprintf("no purchase: probe price %d exceeds %d%% of treasury %d", price, maxTreasuryFractionPercent, credits)
	}

	// WORKING-CAPITAL FLOOR (sp-tlekc §2E, RULINGS #4/#5): the frontier finally sits behind the
	// standing 50k working-capital floor that tour/factory/trade/outfitting all enforce — a buy must
	// leave AT LEAST the immutable reserve spendable (credits − price ≥ floor). It is the SHARED,
	// IMMUTABLE surface (common.ImmutableReserveFloor = 50k, flat — sp-05glh scrapped the prior
	// proportional-of-treasury computation); there is NO config/tune seam, so it can never be
	// weakened (RULINGS #5). It fails CLOSED on the treasury already read above (an unreadable
	// treasury never reaches here). This ADDS a real solvency bound to REPLACE the two deleted rate
	// governors and closes the §2A cross-coordinator low-treasury residual — added even as knobs are
	// removed.
	const floor = common.ImmutableReserveFloor
	if int64(credits)-int64(price) < floor {
		return fmt.Sprintf("no purchase: working-capital floor (treasury %d − price %d = %d < floor %d)", credits, price, credits-price, floor)
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

// positionProbeBuyerOnStall runs the sp-255rz stall breaker when QuoteProbe fails closed: position an
// eligible idle undedicated hull at a reachable probe-selling yard so NEXT tick's presence-gated live
// price reads and the in-place buy clears. Best-effort + nil-safe: an UNSET positioner, a DRY-RUN, a
// positioning error, or a graceful no-op (no reachable yard / no eligible hull) all just report the
// unchanged stall this cycle — byte-identical to previous when unwired. It NEVER buys, NEVER
// poaches a dedicated hull (RULINGS #7), and NEVER weakens the price guard (RULINGS #4): a positioned
// hull only makes the price READABLE; the buy still runs every guard next tick.
func (h *RunFrontierExpansionCoordinatorHandler) positionProbeBuyerOnStall(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, quoteErr error) string {
	stall := fmt.Sprintf("no purchase: probe unpriceable (fail-closed): %v", quoteErr)
	// Positioning moves a real hull, so it is suppressed under dry-run exactly like the buy; an unset
	// positioner leaves the previous behavior untouched.
	if h.probePositioner == nil || cmd.DryRun {
		return stall
	}
	logger := common.LoggerFromContext(ctx)
	dispatched, err := h.probePositioner.PositionProbeBuyer(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Frontier probe-buyer positioning failed (stall persists): %v", err), map[string]interface{}{
			"action": "frontier_probe_position_failed",
			"error":  err.Error(),
		})
		return stall
	}
	if !dispatched {
		return stall // no reachable yard / no eligible undedicated hull this cycle — retry next tick
	}
	logger.Log("INFO", "Frontier probe unpriceable — positioned an idle undedicated hull at a probe-selling yard; the live price reads next tick (sp-255rz stall breaker)", map[string]interface{}{
		"action": "frontier_probe_buyer_positioned",
	})
	return "no purchase: probe unpriceable — positioned a buyer hull at a probe-selling yard, pricing resumes next tick (sp-255rz)"
}

// maybeReuseExistingProbe runs the reuse-before-buy relay for targetSystem. It returns
// (reason, done): done=true ends the staffing decision this cycle (a committed relay — 0 purchases
// — or the dry-run intent); done=false tells decideAndMaybeBuy to fall through to the unchanged buy
// path (no reusable probe within reach / under the ceiling, or a relay error — never buy blind on a
// relay failure, but never strand the target either). Only reached when reuse is armed AND a
// relayer is wired, so it is a pure no-op path on the default byte-identical configuration.
func (h *RunFrontierExpansionCoordinatorHandler) maybeReuseExistingProbe(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	targetSystem string,
	openSlots int,
) (string, bool) {
	logger := common.LoggerFromContext(ctx)
	if cmd.DryRun {
		logger.Log("INFO", fmt.Sprintf("DRY-RUN: would relay an existing edge probe to %s within %d hops (value ceiling %d) to serve %d unmanned slot(s)", targetSystem, cfg.EdgeRelayMaxHops, cfg.ReuseValueCeiling, openSlots), map[string]interface{}{
			"action":        "frontier_reuse_dryrun",
			"system_symbol": targetSystem,
			"max_hops":      cfg.EdgeRelayMaxHops,
			"value_ceiling": cfg.ReuseValueCeiling,
		})
		return fmt.Sprintf("would reuse an existing probe for %s (dry-run)", targetSystem), true
	}

	target := ProbeReuseTarget{System: targetSystem, MaxHops: cfg.EdgeRelayMaxHops, ValueCeiling: cfg.ReuseValueCeiling}
	sym, ok, err := h.reuseRelayer.RelayNearestProbe(ctx, cmd.PlayerID, target)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Frontier probe reuse relay for %s failed (falling back to buy): %v", targetSystem, err), map[string]interface{}{
			"action":        "frontier_reuse_failed",
			"system_symbol": targetSystem,
			"error":         err.Error(),
		})
		return "", false // fall through to the buy path
	}
	if !ok {
		return "", false // no reusable probe within reach / under the ceiling — fall through to buy
	}
	logger.Log("INFO", fmt.Sprintf("Frontier reused existing probe %s to serve %s (0 purchases) — relayed from the charted edge, not a deep-yard buy", sym, targetSystem), map[string]interface{}{
		"action":        "frontier_probe_reused",
		"ship_symbol":   sym,
		"system_symbol": targetSystem,
		"open_slots":    openSlots,
	})
	return fmt.Sprintf("reused probe %s to %s (0 purchases)", sym, targetSystem), true
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
			// An occupied/anchor system (hop 0 — the HQ or a system the fleet
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
			// A scanned-and-genuinely-marketless system is unserviceable — DROP it.
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
