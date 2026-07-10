package contract

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Absorption-ledger integration constants (sp-78ai L2). The engine tag attributes a
// row's origin for telemetry and dead-container reclaim; the TTL knobs bound a
// PLANNED hold whose container dies without releasing (dead-container reclaim is the
// primary cleanup, these are the backstop).
const (
	absorptionEngineIdleArb = "idle-arb"
	// defaultAbsorptionPlannedTTLSlack pads a leg's projected round-trip so a healthy
	// in-flight hold never expires early; minAbsorptionPlannedTTL floors it for very
	// short legs. Both are backstops to the dead-container reclaim (RULINGS #5: the
	// slack is a wired config, these are its defaults).
	defaultAbsorptionPlannedTTLSlack = 15 * time.Minute
	minAbsorptionPlannedTTL          = 30 * time.Minute
)

// Idle-gap arbitrage (sp-1z2h). The contract fleet's dedicated hulls sit
// homed+unclaimed 89% of wall-time (sp-5bmq: 5.9 active hull-hours of 54);
// the pipeline is SERIAL (one contract worker ever), so at most one hull is
// contract-active while the rest park at their hub standby stations. This
// dispatcher harvests that idle time with hub-local ONE-SHOT arb legs run
// through the proven guarded arb-run machinery (sp-p4ua), dispatched by the
// CONTRACT coordinator itself — l7h2 exclusivity stays intact because the arb
// containers claim with the contract fleet's own identity, through the same
// atomic operation-checked ClaimShip every contract worker uses.
//
// WHY NO RECALLABLE LEASE: the brief allowed either a lease-with-instant-recall
// primitive or "an arbing hull isn't counted as available and the claim takes
// the next hull". The second is strictly smaller and gives a hard bound, so it
// wins. The RESERVE rule makes it airtight:
//
//   - The dispatcher never claims a hull while claimable idle hulls ≤
//     ReserveHulls, and it RECOUNTS from the repository before EVERY claim.
//   - Contract claims are serial (one worker at a time), and every contract
//     completion releases a hull back to idle.
//   - Therefore a contract claim always finds ≥ReserveHulls unclaimed, homed
//     hulls: added claim latency from arb = ZERO. The only idle-reducing event
//     inside the recount→claim race window is the coordinator's own claim —
//     which by definition already succeeded, un-delayed. If that race lands,
//     the pool transiently dips below reserve with NO waiting claim, and
//     refills within one hub-local leg (≤ ~8 min at DefaultIdleArbHubRadius)
//     — well inside the ~18 min claim cadence. A recall primitive would add
//     persisted lease state (RULINGS #2), a recall protocol, and mid-leg
//     cargo cleanup to improve on a bound that is already zero.
//
// HUB-LOCAL is physical, not advisory: the leg's BuyAt is the hull's CURRENT
// waypoint (its hub) — the arb run's location guard refuses to buy anywhere
// else — and SellAt must sit within HubRadius in the same system, so a hull is
// never more than one short hop from home. Guards are inherited wholesale from
// the arb run: min-margin (live re-read, fail-closed), max-spend, the
// non-tunable working-capital floor, stranded-cargo=failure. Nothing here
// weakens them (RULINGS #4); this file only DECIDES lanes, it never spends.
//
// The dispatcher itself holds no persisted state: every tick recomputes from
// live discovery, and the launched containers are ordinary recovery-safe
// arb_run rows (rebuilt or cleanly released on daemon restart — RULINGS #2/#3).

// IdleArbConfig parametrizes the dispatcher (RULINGS #5: operational values
// are config, not constants — these flow from the coordinator's persisted
// launch config, with the defaults below when unset).
type IdleArbConfig struct {
	// ReserveHulls is the number of idle dedicated hulls the dispatcher must
	// always leave unclaimed for instant contract claims. The serial pipeline
	// needs at most one hull at a time, so 1 preserves the zero-latency bound.
	ReserveHulls int
	// HubRadius is the maximum in-system distance (distance units) from the
	// hull's current waypoint to the leg's sell market. Bounds both leg
	// duration and how far a hull can drift from its hub. This is the OUTER
	// hub-local filter; LeashRadius (below) is the tighter money-guard leash.
	HubRadius float64
	// LeashRadius (sp-uohe) is the formal money-guard leash: the maximum
	// distance (distance units) from the home hub a leg's sell market may sit.
	// Legs naturally max ~52u, so 80 formalizes that boundary with headroom;
	// tighter than HubRadius, it is the binding radius in practice. A candidate
	// beyond it is skipped (leash counter), never dispatched.
	LeashRadius float64
	// MaxLegDuration (sp-uohe) caps a leg's projected one-way flight time to
	// the sell market (CRUISE estimate from the hull's engine speed). It bites
	// where LeashRadius does not: a slow hull whose in-radius leg still
	// projects longer than this is skipped (leash counter).
	MaxLegDuration time.Duration
	// MaxSpendPerLeg caps each leg's buy (the arb run's --max-spend guard).
	MaxSpendPerLeg int
	// MinMarginPerUnit is the absolute per-unit floor handed to the arb run's
	// margin gate (which re-reads live prices and fails closed).
	MinMarginPerUnit int
	// MarginVerifyFraction (sp-uohe) is the RELATIVE per-unit floor: a leg's
	// effective MinMargin is raised to ceil(MarginVerifyFraction × quoted
	// margin), so the arb run's existing live-verify gate aborts fail-closed
	// unless the live margin holds ≥ this fraction of the cached quote. This is
	// the −234k fix: it gives the gate teeth the flat MinMarginPerUnit=1 floor
	// never had (which tolerated a near-total collapse from the quote). 0.80 =
	// tolerate at most a 20% margin slip between quote and live.
	MarginVerifyFraction float64
	// Blacklist (sp-uohe) is the config-driven excluded-goods list checked at
	// dispatch: a leg is never dispatched on a listed good. Nil → the package
	// default (ELECTRONICS); an explicit empty list disables the blacklist.
	// The captain flips a good back by editing config and restarting (no code
	// redeploy). RULINGS #5.
	Blacklist []string
	// StandbyStations (sp-8bpr) are the operator's standby waypoint symbols — the
	// SAME --standby-stations set the contract coordinator's contract-handoff
	// homing uses (l7h2 Phase 3). The post-leg re-home (rehomeDriftedHulls) sends
	// any idle dedicated hull NOT sitting at one of these back to its balanced
	// standby station, so an arb leg that ends off-station doesn't dead-idle at
	// the sell waypoint. Empty (or a nil homer) disables re-homing entirely,
	// mirroring HomeShipCommand's own "empty stations = no relocation" contract.
	// RULINGS #5: the tunable is the station set, already an operator flag.
	StandbyStations []string
	// Interval is the dispatch tick.
	Interval time.Duration
	// RecoveryHold (sp-lbbm) is the lane mutex's post-termination hold: after a
	// leg on a (good, sink) lane terminates, the dispatcher keeps that lane closed
	// for this long before another hull may work it, so back-to-back passes never
	// re-dump a sink the last leg just depressed. In-flight legs block their lane
	// regardless of this value; it only spaces SEQUENTIAL legs on one sink. See
	// laneMutex for why a flat hold (not the routing service's recovery model) is
	// the honest v1 and how it cites the model's half-lives.
	RecoveryHold time.Duration
}

// Idle-arb defaults. Sizing notes: HubRadius 250 is the loose outer hub-local
// filter; LeashRadius 80 is the tight money-guard leash (legs naturally max
// ~52u, sp-5bmq, so 80 formalizes that boundary with headroom) and the
// 8-minute cap catches slow hulls the radius alone would not; spend 100k/leg ×
// ≤5 concurrent legs bounds exposure at ~500k against a multi-million treasury,
// before the arb run's own working-capital floor (non-tunable, sp-bp6f) even
// engages. MinMargin 1 is the ABSOLUTE floor; the capital protection is the
// RELATIVE MarginVerifyFraction (0.80): sp-uohe autopsy — a flat floor of 1 let
// the arb run's live-verify gate pass legs whose quoted margin had collapsed to
// +1/unit, and selling ~52u of volatile ELECTRONICS into that razor cushion
// realized −234k. The 80%-of-quote floor gives the gate the teeth to abort
// those pre-buy.
const (
	DefaultIdleArbReserveHulls         = 1
	DefaultIdleArbHubRadius            = 250.0
	DefaultIdleArbLeashRadius          = 80.0
	DefaultIdleArbMaxLegDuration       = 8 * time.Minute
	DefaultIdleArbMaxSpend             = 100_000
	DefaultIdleArbMinMargin            = 1
	DefaultIdleArbMarginVerifyFraction = 0.80
	DefaultIdleArbInterval             = 90 * time.Second
	// DefaultIdleArbRecoveryHold (sp-lbbm) is the lane mutex's post-termination
	// hold. 20min is deliberately shorter than any modelled recovery half-life
	// (market_model.json: 180min GROWING … 413min RESTRICTED, ~1074min baseline) —
	// it does not claim full recovery, only that a sink is not re-dumped
	// back-to-back. The in-flight lane block and the per-tranche sell floor carry
	// the rest of the defense; a captain wanting the fuller modelled hold raises
	// the config knob with no code change.
	DefaultIdleArbRecoveryHold = 20 * time.Minute
)

// DefaultIdleArbBlacklist is the initial excluded-goods list (sp-uohe): the
// −234k bleed was on ELECTRONICS. A nil IdleArbConfig.Blacklist takes this;
// an explicit empty list disables the blacklist entirely.
var DefaultIdleArbBlacklist = []string{"ELECTRONICS"}

// WithDefaults fills zero-valued fields with the package defaults.
func (c IdleArbConfig) WithDefaults() IdleArbConfig {
	if c.ReserveHulls <= 0 {
		c.ReserveHulls = DefaultIdleArbReserveHulls
	}
	if c.HubRadius <= 0 {
		c.HubRadius = DefaultIdleArbHubRadius
	}
	if c.LeashRadius <= 0 {
		c.LeashRadius = DefaultIdleArbLeashRadius
	}
	if c.MaxLegDuration <= 0 {
		c.MaxLegDuration = DefaultIdleArbMaxLegDuration
	}
	if c.MaxSpendPerLeg <= 0 {
		c.MaxSpendPerLeg = DefaultIdleArbMaxSpend
	}
	if c.MinMarginPerUnit <= 0 {
		c.MinMarginPerUnit = DefaultIdleArbMinMargin
	}
	if c.MarginVerifyFraction <= 0 {
		c.MarginVerifyFraction = DefaultIdleArbMarginVerifyFraction
	}
	// nil → default blacklist; an explicit empty (non-nil) list is preserved so
	// a config whitelist-flip genuinely disables the blacklist without code.
	if c.Blacklist == nil {
		c.Blacklist = DefaultIdleArbBlacklist
	}
	if c.Interval <= 0 {
		c.Interval = DefaultIdleArbInterval
	}
	if c.RecoveryHold <= 0 {
		c.RecoveryHold = DefaultIdleArbRecoveryHold
	}
	return c
}

// IdleArbSpec is one hub-local leg the dispatcher wants flown: the arb-run
// launch parameters plus the claim identity (Operation) the container must use
// so the atomic ClaimShip dedication check passes for the contract fleet's own
// hulls — and keeps rejecting everyone else's.
type IdleArbSpec struct {
	ShipSymbol string
	Good       string
	BuyAt      string // the hull's CURRENT waypoint (arb location guard enforces this)
	SellAt     string
	MaxSpend   int
	MinMargin  int
	PlayerID   int
	Operation  string // claim identity, e.g. "contract" (l7h2)
	// SellFloorFraction (sp-lbbm) arms the arb run's per-tranche sell floor: each
	// sell tranche aborts the remainder when the LIVE bid falls below this fraction
	// of the quoted bid. It reuses the SAME 80% knob the buy-side live-verify uses
	// (cfg.MarginVerifyFraction), so a captain retune moves both floors together.
	// 0 → the arb run's own default (defaultArbSellFloorFraction).
	SellFloorFraction float64
}

// IdleArbLauncher starts one recovery-safe, guarded one-shot arb container and
// confirms the hull is CLAIMED (atomically, operation-checked) before
// returning. Implemented by the daemon server; the dispatcher stays a pure
// decision loop (RULINGS #3: new operations are daemon containers, and the
// daemon remains the single writer of ship state).
type IdleArbLauncher interface {
	LaunchIdleArb(ctx context.Context, spec IdleArbSpec) (containerID string, err error)
}

// ShipHomer re-homes ONE idle dedicated hull to its balanced standby station
// through the EXISTING HomeShipCommand path (sp-d3kd / l7h2 Phase 3) — never a
// parallel homing algorithm (RULINGS #7). A narrow port, implemented by the
// coordinator over the mediator and faked trivially in tests, that keeps the
// dispatcher a pure decision loop.
//
// The implementation dispatches the homing FIRE-AND-FORGET: HomeShipCommand
// navigates synchronously and blocks until the hull arrives (navigate_route
// executes the whole route), so a blocking call would stall an entire dispatch
// tick for a full flight. HomeShip therefore returns as soon as the home is
// DISPATCHED, not when the hull lands — exactly as the coordinator's own
// contract-handoff homing goroutine behaves. The hull is marked in-transit
// within a hop, so the next discovery pass excludes it (FindIdleShipsByFleet
// skips in-transit hulls); a returned error means the home could not even be
// dispatched, and the dispatcher leaves the hull for the next pass.
type ShipHomer interface {
	HomeShip(ctx context.Context, shipSymbol string) error
}

// ContractGoodsProvider lists the delivery goods of the player's OPEN contracts
// (sp-uohe guard 3) so the dispatcher never dispatches an arb leg on a good we
// are actively sourcing for a contract — the idle harvest must never compete
// with, or bid up, our own contract sourcing. A narrow port (not the full
// ContractRepository) keeps the dispatcher testable with a trivial fake.
type ContractGoodsProvider interface {
	// OpenContractGoods returns the set of trade symbols under the player's
	// active contracts. An error is fatal to a dispatch pass (fail-closed): the
	// dispatcher would rather skip a tick than risk sourcing-competition it
	// cannot rule out.
	OpenContractGoods(ctx context.Context, playerID int) (map[string]struct{}, error)
}

// activeContractGoods adapts the domain ContractRepository to
// ContractGoodsProvider by reading every active contract's delivery symbols.
type activeContractGoods struct {
	repo domainContract.ContractRepository
}

// NewActiveContractGoods wires the default provider over the contract repo.
func NewActiveContractGoods(repo domainContract.ContractRepository) ContractGoodsProvider {
	return activeContractGoods{repo: repo}
}

func (a activeContractGoods) OpenContractGoods(ctx context.Context, playerID int) (map[string]struct{}, error) {
	contracts, err := a.repo.FindActiveContracts(ctx, playerID)
	if err != nil {
		return nil, err
	}
	goods := make(map[string]struct{})
	for _, c := range contracts {
		for _, delivery := range c.Terms().Deliveries {
			goods[delivery.TradeSymbol] = struct{}{}
		}
	}
	return goods, nil
}

// IdleArbLane is a scored hub-local lane candidate.
type IdleArbLane struct {
	Good          string
	SellAt        string
	MarginPerUnit int
	Distance      float64
	SourceAsk     int
	DestBid       int
}

// IdleArbDispatcher runs the idle-gap harvest for one coordinator's dedicated
// fleet.
type IdleArbDispatcher struct {
	shipRepo        navigation.ShipRepository
	marketRepo      market.MarketRepository
	graphProvider   system.ISystemGraphProvider
	launcher        IdleArbLauncher
	homer           ShipHomer // sp-8bpr: post-leg re-homing (nil → re-home off)
	contractGoods   ContractGoodsProvider
	clock           shared.Clock
	playerID        shared.PlayerID
	fleet           string
	cfg             IdleArbConfig
	blacklist       map[string]struct{} // upper-cased cfg.Blacklist, built once
	standbyStations map[string]struct{} // sp-8bpr: cfg.StandbyStations as a set, for the at-home filter
	lanes           *laneMutex          // sp-lbbm: one hull per (good, sink) per recovery window

	// sp-78ai L2: the cross-engine absorption ledger. nil → integration inert (the
	// same optional-port contract the other guards use). When wired, the dispatcher
	// CONSULTS it once per pass (skip:reserved) and RECORDS each launched leg's sell
	// side so tours and other dispatchers see this leg's in-flight absorption — the
	// lane mutex + flat hold above STAY ARMED IN PARALLEL (belt to this suspenders;
	// L5 retires them after burn-in). consultDisabled is the kill-switch: when set,
	// the consult (skip:reserved) is suppressed but recording continues, so the
	// ledger still populates for other engines while an operator diagnoses it.
	ledger          absorption.Ledger
	consultDisabled bool
	plannedTTLSlack time.Duration
	skipReserved    int // legs skipped: sink reserved/recovering in the absorption ledger

	// Observability counters (sp-uohe guard 5). In-memory and reset on restart
	// by design: they measure THIS process's harvest rate, not operational
	// state — a restart legitimately restarts the window. The operational state
	// (claims, reservations, container rows) is persisted by the existing
	// mechanisms (RULINGS #2), untouched here. DispatchOnce is called serially
	// (Run's single goroutine), so these need no locking.
	startTime        time.Time
	attempts         int // legs launch-attempted
	launched         int // legs successfully launched
	skipBlacklist    int // legs skipped: good on the blacklist
	skipContractGood int // legs skipped: good under an open contract
	skipLeash        int // legs skipped: only profit was beyond the leash/leg-time
	skipLaneHeld     int // sp-lbbm: legs skipped: best lane held by a live/recovering leg
	rehomed          int // sp-8bpr: hulls re-homed post-leg (cumulative)
}

// NewIdleArbDispatcher wires a dispatcher for the given dedicated fleet. A nil
// contractGoods provider leaves the contract-good exclusion (guard 3) inert —
// the same optional-port contract the other guards use for missing wiring.
func NewIdleArbDispatcher(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	graphProvider system.ISystemGraphProvider,
	launcher IdleArbLauncher,
	homer ShipHomer,
	contractGoods ContractGoodsProvider,
	clock shared.Clock,
	playerID shared.PlayerID,
	fleet string,
	cfg IdleArbConfig,
) *IdleArbDispatcher {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	cfg = cfg.WithDefaults()
	// Pre-build the blacklist lookup once, upper-cased so a config typo in case
	// still matches the API's upper-case good symbols.
	blacklist := make(map[string]struct{}, len(cfg.Blacklist))
	for _, g := range cfg.Blacklist {
		blacklist[strings.ToUpper(strings.TrimSpace(g))] = struct{}{}
	}
	// Pre-build the standby-station lookup once (sp-8bpr): the at-home filter in
	// rehomeDriftedHulls asks "is this hull's waypoint a standby station?" per
	// hull per tick. Waypoint symbols are case-exact, so no case-folding here.
	standbyStations := make(map[string]struct{}, len(cfg.StandbyStations))
	for _, s := range cfg.StandbyStations {
		if s = strings.TrimSpace(s); s != "" {
			standbyStations[s] = struct{}{}
		}
	}
	return &IdleArbDispatcher{
		shipRepo:        shipRepo,
		marketRepo:      marketRepo,
		graphProvider:   graphProvider,
		launcher:        launcher,
		homer:           homer,
		contractGoods:   contractGoods,
		clock:           clock,
		playerID:        playerID,
		fleet:           fleet,
		cfg:             cfg,
		blacklist:       blacklist,
		standbyStations: standbyStations,
		lanes:           newLaneMutex(clock, cfg.RecoveryHold),
		startTime:       clock.Now(),
	}
}

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L2), the
// optional-port idiom the other dispatcher dependencies use. A nil ledger leaves the
// consult and the launch-record inert (pre-L2 behavior). consultDisabled is the
// consult kill-switch (recording continues so the ledger still serves other
// engines); plannedTTLSlack pads a recorded leg's projected round-trip TTL (0 → the
// package default).
func (d *IdleArbDispatcher) SetAbsorptionLedger(ledger absorption.Ledger, consultDisabled bool, plannedTTLSlack time.Duration) {
	d.ledger = ledger
	d.consultDisabled = consultDisabled
	if plannedTTLSlack <= 0 {
		plannedTTLSlack = defaultAbsorptionPlannedTTLSlack
	}
	d.plannedTTLSlack = plannedTTLSlack
}

// absorptionConsult is one pass's batched read of the ledger plus its fail-closed
// state. Built once per DispatchOnce (design §2: one outstanding query per pass) and
// threaded to every candidate — the within-pass collision is the lane mutex's job,
// the cross-pass/cross-engine collision is this.
type absorptionConsult struct {
	active     bool // ledger wired AND consult not killed
	unreadable bool // the read failed → fail closed
	pools      map[absorption.LaneKey]absorption.KeyOccupancy
}

// reserved reports whether a (good, sink) sell is blocked by the ledger: an in-flight
// PLANNED leg on the key, or a recovering EXECUTED shadow still above its floor
// (Outstanding already drops sub-floor shadows). Fail-closed: an unreadable ledger
// blocks EVERY candidate — never dispatch blind into depth another engine may have
// reserved or just crushed (RULINGS #4). Same structural hole as sp-i8vx's in-flight
// exposure finding, closed here from the market-absorption side.
func (c absorptionConsult) reserved(good, sink string) bool {
	if !c.active {
		return false
	}
	if c.unreadable {
		return true
	}
	occ := c.pools[absorption.LaneKey{Waypoint: sink, Good: good, Side: absorption.SideSell}]
	return occ.PlannedUnits > 0 || occ.RecoveringResidual > 0
}

// readAbsorption performs the once-per-pass consult read. Inert (never blocks) when
// the ledger is unwired or the consult is killed; fail-closed (blocks all) when the
// read errors.
func (d *IdleArbDispatcher) readAbsorption(ctx context.Context) absorptionConsult {
	if d.ledger == nil || d.consultDisabled {
		return absorptionConsult{}
	}
	pools, err := d.ledger.Outstanding(ctx, d.playerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb absorption consult: ledger read failed, declining all candidates this pass (fail-closed): %v", err), nil)
		return absorptionConsult{active: true, unreadable: true}
	}
	return absorptionConsult{active: true, pools: pools}
}

// recordAbsorption publishes a just-launched leg's sell-side occupancy to the ledger
// so tours and other dispatchers consult it (sp-78ai L2). Called at the SAME seam the
// lane mutex is marked (noteLaunch) — the leg has committed, so this is a fail-open
// RECORD, not a gate: a write failure loses cross-engine visibility (the armed mutex
// and the arb run's sell floor still guard the leg) but never strands the launched
// leg. units is the full hull hold (worst case); the arb container's convert-at-sale
// corrects it to realized units.
func (d *IdleArbDispatcher) recordAbsorption(ctx context.Context, hull *navigation.Ship, lane *IdleArbLane, containerID string) {
	if d.ledger == nil {
		return
	}
	legSeconds := shared.FlightModeCruise.TravelTime(lane.Distance, hull.EngineSpeed())
	ttl := 2*time.Duration(legSeconds)*time.Second + d.plannedTTLSlack
	if ttl < minAbsorptionPlannedTTL {
		ttl = minAbsorptionPlannedTTL
	}
	entry := absorption.ReserveEntry{
		Waypoint:    lane.SellAt,
		Good:        lane.Good,
		Side:        absorption.SideSell,
		Units:       hull.AvailableCargoSpace(),
		QuotedPrice: lane.DestBid,
		TTL:         ttl,
	}
	if _, err := d.ledger.RecordPlanned(ctx, d.playerID.Value(), containerID, absorptionEngineIdleArb, entry); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb absorption record: could not record leg %s on %s/%s (leg flies; mutex + sell floor still guard it): %v",
			containerID, lane.SellAt, lane.Good, err), nil)
	}
}

// isBlacklisted reports whether good is on the configured excluded list.
func (d *IdleArbDispatcher) isBlacklisted(good string) bool {
	_, ok := d.blacklist[strings.ToUpper(good)]
	return ok
}

// idleArbSkipReason names why a hull's would-be leg was refused at dispatch, so
// the skip counters (guard 5) can attribute pressure by cause.
type idleArbSkipReason int

const (
	skipNone idleArbSkipReason = iota
	skipReasonBlacklist
	skipReasonContractGood
	skipReasonLeash
	skipReasonLaneHeld
	// skipReasonReserved (sp-78ai L2): the (good, sink) is occupied in the
	// cross-engine absorption ledger — a PLANNED leg (any engine) is in flight there,
	// or a recovering EXECUTED shadow still blocks above its floor. This is the
	// lane-mutex's guarantee generalized CROSS-ENGINE and across a restart: a tour or
	// another dispatcher's leg the in-memory mutex cannot see is caught here.
	skipReasonReserved
)

// String names the skip reason for the per-candidate verdict line (sp-nw9v). It
// mirrors the reason names the harvest-summary counters already use, so a
// candidate's "skipped:<reason>" reads consistently with the cumulative totals.
func (r idleArbSkipReason) String() string {
	switch r {
	case skipReasonBlacklist:
		return "blacklist"
	case skipReasonContractGood:
		return "contract-good"
	case skipReasonLeash:
		return "leash"
	case skipReasonLaneHeld:
		return "lane-held"
	case skipReasonReserved:
		return "reserved"
	default:
		return "none"
	}
}

// idleArbMinMargin (sp-uohe guard 1) is the effective per-unit floor a leg
// hands the arb run's live-verify gate: the tighter of the absolute floor and
// the relative one (ceil(fraction × quoted margin)). Passing THIS as the run's
// MinMargin makes the run's existing live-refresh + fail-closed abort reject a
// leg whose live margin has slipped below the fraction of its quote — the
// −234k hole was the dispatcher handing this gate a flat floor of 1.
func idleArbMinMargin(cfg IdleArbConfig, quotedMargin int) int {
	relative := int(math.Ceil(cfg.MarginVerifyFraction * float64(quotedMargin)))
	if relative > cfg.MinMarginPerUnit {
		return relative
	}
	return cfg.MinMarginPerUnit
}

// Run ticks DispatchOnce every Interval until ctx is cancelled. Started as a
// goroutine by the fleet coordinator's Handle; the coordinator's own context
// bounds its life, so a stopped coordinator stops the harvest with it.
func (d *IdleArbDispatcher) Run(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf(
		"Idle-gap arb dispatcher running: fleet %q, reserve %d hull(s), hub radius %.0f, leash radius %.0f, max leg %s, max spend %d/leg, min margin %d/unit, tick %s",
		d.fleet, d.cfg.ReserveHulls, d.cfg.HubRadius, d.cfg.LeashRadius, d.cfg.MaxLegDuration, d.cfg.MaxSpendPerLeg, d.cfg.MinMarginPerUnit, d.cfg.Interval,
	), nil)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.cfg.Interval):
		}
		d.DispatchOnce(ctx)
	}
}

// DispatchOnce runs one dispatch pass and returns how many legs it launched.
// Exported so the zero-missed-claims simulation can drive it deterministically.
func (d *IdleArbDispatcher) DispatchOnce(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	launched := 0
	passSkips := 0 // dispatch-time guard skips THIS pass (guard-5 summary trigger)

	// POST-LEG RE-HOMING (sp-8bpr): send every idle dedicated hull that finished
	// off-station back to its balanced standby station FIRST — before the arb
	// loop, and before the guard-3 contract-goods read below (which is arb-only
	// and fail-closed) so a contract-read failure never skips the re-home. This
	// returns the hulls homed THIS pass; the arb loop excludes them so a hull is
	// never re-arbed from a drift position the same tick it is being sent home
	// (chained legs would drift past the leash, which is measured from the hull's
	// CURRENT waypoint). Inert when re-homing is off (nil homer / no stations).
	homingThisPass := d.rehomeDriftedHulls(ctx)
	rehomedThisPass := len(homingThisPass)

	// LANE MUTEX reconcile (sp-lbbm): observe which of the legs this dispatcher
	// launched have terminated, so their (good, sink) lanes can begin the recovery
	// hold and eventually free. A terminated leg is one whose hull no longer
	// carries the leg's container id (released to idle, or re-claimed by a
	// contract) — the same live fleet state the reserve accounting reads. A read
	// failure skips reconcile (lanes stay held — the safe direction: never free a
	// lane we cannot confirm terminated), and terminations are picked up next pass.
	if shipContainerIDs, ok := d.fleetShipContainerIDs(ctx); ok {
		d.lanes.reconcile(shipContainerIDs)
	}

	// Emit the harvest summary (guard 5) on every return path of a pass that did
	// something, so the captain's acceptance can read the attempt rate, the
	// per-reason skip pressure, and the re-home count from message text.
	defer func() { d.logHarvestSummary(ctx, launched, passSkips, rehomedThisPass) }()

	// Guard 3 dependency: the goods under the player's OPEN contracts. Read ONCE
	// per pass (not per hull) and fail CLOSED — a contract-read failure skips the
	// whole tick rather than risk dispatching a leg that competes with our own
	// sourcing. A nil provider leaves the exclusion inert (empty set).
	openGoods := map[string]struct{}{}
	if d.contractGoods != nil {
		g, err := d.contractGoods.OpenContractGoods(ctx, d.playerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Idle-arb dispatch: could not read open-contract goods, skipping pass (fail-closed): %v", err), nil)
			return launched
		}
		openGoods = g
	}

	// sp-78ai L2: one batched absorption-ledger read per pass (design §2). The
	// consult skips candidates whose (good, sink) another engine has reserved in
	// flight or just crushed (a recovering shadow above its floor) — the cross-engine
	// generalization the in-memory lane mutex cannot see. Fail-closed: an unreadable
	// ledger declines every candidate this pass rather than dispatch blind.
	consult := d.readAbsorption(ctx)

	// tried tracks hulls already handled this pass (launched, or skipped for
	// want of a lane) so the recount loop below always terminates. A skipped
	// hull stays idle and keeps padding the reserve — conservative.
	tried := map[string]bool{}

	for {
		// RECOUNT before every claim: the reserve check must see the
		// repository's current truth, not this pass's opening snapshot —
		// this is what shrinks the race window with the coordinator's own
		// claims to the recount→claim gap.
		idleShips, _, err := FindIdleShipsByFleet(ctx, d.playerID, d.shipRepo, d.fleet)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Idle-arb dispatch: idle discovery failed, skipping pass: %v", err), nil)
			return launched
		}

		var candidates []*navigation.Ship
		for _, s := range idleShips {
			// A hull sent home this pass (sp-8bpr) is off-limits to arb: it is
			// being repositioned to standby, and its in-transit mark can lag the
			// homer's fire-and-forget return, so exclude it explicitly rather than
			// trust the recount to have caught it yet.
			if !tried[s.ShipSymbol()] && !homingThisPass[s.ShipSymbol()] {
				candidates = append(candidates, s)
			}
		}

		// The reserve is judged against ALL idle hulls (tried-but-skipped ones
		// still count — they are genuinely claimable by a contract), but only
		// untried hulls are dispatchable. Hulls launched earlier this pass are
		// already claimed by their containers, so the recount above has
		// excluded them — no separate subtraction needed.
		if len(idleShips) <= d.cfg.ReserveHulls || len(candidates) == 0 {
			return launched
		}

		hull := candidates[0]
		tried[hull.ShipSymbol()] = true

		lane, skip := d.pickHubLocalLane(ctx, hull, openGoods, consult)
		if lane == nil {
			// A guard refused this hull's only profitable lane → attribute the
			// skip by cause (guard 5). skipNone means there simply was no
			// profitable local lane, i.e. idle-for-lack-of-opportunity, not a
			// guard skip.
			if d.recordSkip(skip) {
				passSkips++
			}
			continue
		}

		// Guard 1 (the −234k fix): hand the arb run's live-verify gate the
		// RELATIVE floor ceil(fraction × quoted margin), not the flat absolute
		// floor. The run re-reads live prices and fails closed, so a leg whose
		// live margin has collapsed below that fraction of its quote aborts
		// pre-buy (zero spend) instead of buying on a razor cushion.
		spec := IdleArbSpec{
			ShipSymbol: hull.ShipSymbol(),
			Good:       lane.Good,
			BuyAt:      hull.CurrentLocation().Symbol,
			SellAt:     lane.SellAt,
			MaxSpend:   d.cfg.MaxSpendPerLeg,
			MinMargin:  idleArbMinMargin(d.cfg, lane.MarginPerUnit),
			PlayerID:   d.playerID.Value(),
			Operation:  d.fleet,
			// sp-lbbm: arm the arb run's per-tranche sell floor with the SAME
			// 80%-of-quote knob the buy-side live-verify uses (cfg.MarginVerifyFraction).
			SellFloorFraction: d.cfg.MarginVerifyFraction,
		}
		d.attempts++
		containerID, err := d.launcher.LaunchIdleArb(ctx, spec)
		if err != nil {
			// Losing the claim race (the coordinator took this hull for a
			// contract between recount and claim) is the system WORKING —
			// contract claims outrank arb. Log and move on.
			logger.Log("INFO", fmt.Sprintf(
				"Idle-arb dispatch: launch for %s declined (%v) - hull skipped this pass", hull.ShipSymbol(), err), nil)
			continue
		}
		launched++
		d.launched++
		// LANE MUTEX (sp-lbbm): mark this (good, sink) held the instant the leg
		// launches, so a later candidate THIS pass that would pick the same sink is
		// skipped:lane-held (within-pass dedupe), and the next pass holds it until
		// the leg terminates + the recovery window elapses (cross-pass).
		d.lanes.noteLaunch(laneKey{good: lane.Good, sink: lane.SellAt}, hull.ShipSymbol(), containerID)
		// sp-78ai L2: publish this leg's sell-side absorption to the cross-engine
		// ledger at the same seam the mutex is marked, so a tour or another dispatcher
		// consults it. Fail-open record (the leg has committed) — see recordAbsorption.
		d.recordAbsorption(ctx, hull, lane, containerID)

		logger.Log("INFO", fmt.Sprintf(
			"Idle-gap arb leg launched: %s flies %s %s->%s (quoted margin %d/unit = bid %d - ask %d, live-verify floor %d/unit, distance %.0f, max spend %d) in container %s",
			hull.ShipSymbol(), lane.Good, spec.BuyAt, lane.SellAt,
			lane.MarginPerUnit, lane.DestBid, lane.SourceAsk, spec.MinMargin, lane.Distance, spec.MaxSpend, containerID,
		), map[string]interface{}{
			"action":       "idle_arb_launched",
			"ship_symbol":  hull.ShipSymbol(),
			"good":         lane.Good,
			"buy_at":       spec.BuyAt,
			"sell_at":      lane.SellAt,
			"margin":       lane.MarginPerUnit,
			"distance":     lane.Distance,
			"container_id": containerID,
		})
	}
}

// fleetShipContainerIDs returns the dedicated fleet's live ship→container map
// (symbol → current container id, "" when idle/unassigned) — the input the lane
// mutex reconciles its launched legs against (sp-lbbm). It reads the same
// repository the reserve recount does. ok is false on a read failure, so the
// caller skips reconcile and leaves lane holds untouched rather than free a lane
// it cannot confirm terminated.
func (d *IdleArbDispatcher) fleetShipContainerIDs(ctx context.Context) (map[string]string, bool) {
	ships, err := d.shipRepo.FindAllByPlayer(ctx, d.playerID)
	if err != nil {
		return nil, false
	}
	out := make(map[string]string, len(ships))
	for _, s := range ships {
		if s.DedicatedFleet() != d.fleet {
			continue
		}
		out[s.ShipSymbol()] = s.ContainerID()
	}
	return out, true
}

// rehomeDriftedHulls (sp-8bpr) sends every idle dedicated hull that is NOT
// sitting at one of the configured standby stations back to its balanced standby
// station via the EXISTING HomeShipCommand (l7h2 Phase 3), and returns the set
// of hulls homed this pass so the caller keeps them out of the arb loop.
//
// THE GAP IT CLOSES: an idle-arb leg (sp-1z2h) ends with the hull idle at the
// SELL waypoint — within a hop of its hub, but off-station. Nothing repositions
// it until a contract-handoff homing or another balance pass happens to catch
// it, so it dead-idles at a random market. This pass re-homes it directly, which
// also keeps the hub-local leash honest: the leash (sp-uohe) is measured from
// the hull's CURRENT waypoint, so a hull left at a drift position could chain
// legs arbitrarily far from home; returning it to standby between legs makes
// "leash-from-current" equal "leash-from-hub" again.
//
// WHY ONLY OFF-STATION HULLS: a hull already parked at ANY standby station is
// left alone. Re-firing HomeShipCommand on it would chase the balancer's
// least-occupied target and shuffle home hulls station-to-station every tick
// (churn); the balancer only needs to run when a hull is actually being brought
// back. Claimed and in-transit hulls never appear here — FindIdleShipsByFleet
// already excludes them — so an active contract claim or an in-flight leg is
// never disturbed (RULINGS #7); and if a contract claim lands on a hull the
// instant it finishes homing, that claim wins, exactly as it does for the
// coordinator's own contract-handoff homing (homing never holds a claim).
//
// Best-effort and inert when re-homing is off (nil homer or no standby stations
// configured — matching HomeShipCommand's own "empty stations disables
// relocation" contract), so the harvest behaves exactly as before when the
// operator has not configured standby stations.
func (d *IdleArbDispatcher) rehomeDriftedHulls(ctx context.Context) map[string]bool {
	homed := map[string]bool{}
	if d.homer == nil || len(d.standbyStations) == 0 {
		return homed
	}

	logger := common.LoggerFromContext(ctx)

	idleShips, _, err := FindIdleShipsByFleet(ctx, d.playerID, d.shipRepo, d.fleet)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb re-home: idle discovery failed, skipping re-home this pass: %v", err), nil)
		return homed
	}

	for _, hull := range idleShips {
		loc := hull.CurrentLocation()
		if loc == nil {
			continue
		}
		if _, atStandby := d.standbyStations[loc.Symbol]; atStandby {
			continue // already home — re-firing would chase balance and churn
		}
		if err := d.homer.HomeShip(ctx, hull.ShipSymbol()); err != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Idle-arb re-home: could not dispatch homing for %s from %s: %v", hull.ShipSymbol(), loc.Symbol, err), nil)
			continue
		}
		homed[hull.ShipSymbol()] = true
		d.rehomed++
		logger.Log("INFO", fmt.Sprintf(
			"Idle-arb re-home: %s idle off-station at %s - homing to balanced standby", hull.ShipSymbol(), loc.Symbol),
			map[string]interface{}{
				"action":      "idle_arb_rehome",
				"ship_symbol": hull.ShipSymbol(),
				"from":        loc.Symbol,
			})
	}

	return homed
}

// pickHubLocalLane scores every (good, sell-market) pair reachable from the
// hull's CURRENT waypoint and returns the best positive-margin lane that PASSES
// every dispatch-time guard, together with a skip reason. Prices are the scanned
// cache — deliberately: the arb run itself live-refreshes the source and
// re-gates the margin fail-closed (now against the tighter relative floor) before
// any credit moves, so a stale pick here costs at worst a wasted (refused) leg.
//
// The return distinguishes three outcomes for the skip counters (guard 5):
//   - a lane + skipNone: fly it.
//   - nil + a guard reason: a profitable lane existed but EVERY candidate was
//     refused by a guard (blacklist / open-contract good / leash) — a
//     skipped-by-guard leg, attributed to the reason of the best refused lane.
//   - nil + skipNone: no profitable local lane at all — idle for lack of
//     opportunity, not a guard skip.
func (d *IdleArbDispatcher) pickHubLocalLane(ctx context.Context, hull *navigation.Ship, excludedContractGoods map[string]struct{}, consult absorptionConsult) (*IdleArbLane, idleArbSkipReason) {
	logger := common.LoggerFromContext(ctx)
	origin := hull.CurrentLocation()
	if origin == nil {
		return nil, skipNone
	}

	hubMarket, err := d.marketRepo.GetMarketData(ctx, origin.Symbol, d.playerID.Value())
	if err != nil || hubMarket == nil || hubMarket.GoodsCount() == 0 {
		return nil, skipNone // the hub standby station isn't a scanned market — nothing to fly
	}

	systemSymbol := shared.ExtractSystemSymbol(origin.Symbol)
	graphResult, err := d.graphProvider.GetGraph(ctx, systemSymbol, false, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: no system graph for %s: %v", systemSymbol, err), nil)
		return nil, skipNone
	}

	marketWaypoints, err := d.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: market listing failed for %s: %v", systemSymbol, err), nil)
		return nil, skipNone
	}

	// bestAllowed is the best lane passing every guard; bestExcluded is the best
	// profitable lane a guard refused (with its reason). If nothing passes but a
	// profitable lane was refused, the reason of bestExcluded is what skipped the
	// leg — a fair attribution because bestAllowed==nil means ALL profitable
	// candidates were refused, so the best one's reason is representative.
	var bestAllowed *IdleArbLane
	bestAllowedScore := 0
	var bestExcluded *IdleArbLane
	bestExcludedScore := 0
	bestExcludedReason := skipNone

	for _, wp := range marketWaypoints {
		if wp == origin.Symbol {
			continue
		}
		coord, ok := graphResult.Graph.Waypoints[wp]
		if !ok {
			continue
		}
		distance := origin.DistanceTo(coord)
		if distance > d.cfg.HubRadius {
			continue // hub-LOCAL outer bound: the hull must stay a short hop from home
		}

		destMarket, err := d.marketRepo.GetMarketData(ctx, wp, d.playerID.Value())
		if err != nil || destMarket == nil {
			continue
		}

		for _, hubGood := range hubMarket.TradeGoods() {
			ask := hubGood.SellPrice() // what the hull pays at the hub
			if ask <= 0 {
				continue
			}
			destGood := destMarket.FindGood(hubGood.Symbol())
			if destGood == nil {
				continue
			}
			// sp-9mkf (Bug 3): never sell into an EXPORT market's bid — an exporter's bid
			// is a low sellback price, not a real import sink. A valid sink is IMPORT or
			// EXCHANGE; unknown trade type is left eligible (fail-open, matching the
			// manufacturing sell_market_distributor reference filter).
			if destGood.TradeType() == market.TradeTypeExport {
				continue
			}
			bid := destGood.PurchasePrice() // what the hull receives at the destination
			margin := bid - ask
			if margin < d.cfg.MinMarginPerUnit {
				continue
			}
			// Expected-gross score: per-unit margin × the tranche the leg can
			// actually carry/afford, so a fat-margin good the spend cap allows
			// two of doesn't outrank a solid lane with real volume.
			units := hull.AvailableCargoSpace()
			if affordable := d.cfg.MaxSpendPerLeg / ask; affordable < units {
				units = affordable
			}
			if units <= 0 {
				continue
			}
			score := margin * units
			lane := &IdleArbLane{
				Good:          hubGood.Symbol(),
				SellAt:        wp,
				MarginPerUnit: margin,
				Distance:      distance,
				SourceAsk:     ask,
				DestBid:       bid,
			}

			reason := d.laneSkipReason(hubGood.Symbol(), wp, distance, excludedContractGoods, hull.EngineSpeed(), consult)

			// Per-candidate verdict logging (sp-nw9v): emit one terse line for
			// every positive-margin candidate with the COMPUTED distance the leash
			// check used, the two endpoints (with coordinates) it measured between,
			// the quoted margin, the buy/sell market read ages, and the verdict.
			// This is the candidate list an all-pairs analyst scan is diffed
			// against: a masked mis-pick (a wrong distance/endpoint pair, a stale
			// cache row, an over-broad exclusion) is visible here per lane. Log
			// only — it never alters the pick, a threshold, or a guard (RULINGS #4).
			d.logCandidate(ctx, hull, lane, origin, coord, hubMarket, destMarket, reason)

			if reason != skipNone {
				if bestExcluded == nil || score > bestExcludedScore {
					bestExcluded = lane
					bestExcludedScore = score
					bestExcludedReason = reason
				}
				continue
			}
			if bestAllowed == nil || score > bestAllowedScore {
				bestAllowed = lane
				bestAllowedScore = score
			}
		}
	}

	if bestAllowed != nil {
		return bestAllowed, skipNone
	}
	if bestExcluded != nil {
		return nil, bestExcludedReason
	}
	return nil, skipNone
}

// logCandidate emits one terse per-candidate verdict line in MESSAGE TEXT
// (sp-nw9v observability; the CLI renderer drops metadata maps). It carries every
// value the leash decision turned on so a masked mis-pick is impossible to hide:
// the good, the buy/sell waypoints WITH the coordinates the distance was measured
// between, that computed distance against the live leash/hub radii, the projected
// CRUISE leg-time against the cap, the quoted margin (bid−ask), the buy/sell
// market read ages, and the verdict (eligible, or skipped:<reason>). This is the
// surface an all-pairs analyst scan is diffed against. It is LOG-ONLY: it reads
// no new state and changes no pick, threshold, or guard (RULINGS #4).
func (d *IdleArbDispatcher) logCandidate(ctx context.Context, hull *navigation.Ship, lane *IdleArbLane, buy, sell *shared.Waypoint, buyMarket, sellMarket *market.Market, reason idleArbSkipReason) {
	verdict := "eligible"
	if reason != skipNone {
		verdict = "skipped:" + reason.String()
		// sp-lbbm: for a lane-held verdict, name the holding hull and (once its leg
		// has terminated) when the lane frees, so a collision the mutex prevented is
		// legible in the same candidate line the analyst scan is diffed against.
		if reason == skipReasonLaneHeld {
			if holder, freesAt, flying := d.lanes.describe(laneKey{good: lane.Good, sink: lane.SellAt}); holder != "" {
				if flying {
					verdict += fmt.Sprintf(" (%s flying)", holder)
				} else {
					verdict += fmt.Sprintf(" (%s dumped, frees ~%s)", holder, freesAt.Format("15:04:05"))
				}
			}
		}
	}
	now := d.clock.Now()
	legSeconds := shared.FlightModeCruise.TravelTime(lane.Distance, hull.EngineSpeed())
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf(
		"Idle-arb candidate: %s %s buy@%s(%.0f,%.0f) -> sell@%s(%.0f,%.0f) dist %.0fu (leash %.0f, hub %.0f), leg %ds (cap %s), margin %d/u (bid %d - ask %d), age buy %s/sell %s, verdict %s",
		hull.ShipSymbol(), lane.Good,
		buy.Symbol, buy.X, buy.Y, sell.Symbol, sell.X, sell.Y,
		lane.Distance, d.cfg.LeashRadius, d.cfg.HubRadius,
		legSeconds, d.cfg.MaxLegDuration,
		lane.MarginPerUnit, lane.DestBid, lane.SourceAsk,
		now.Sub(buyMarket.LastUpdated()).Round(time.Second), now.Sub(sellMarket.LastUpdated()).Round(time.Second),
		verdict,
	), nil)
}

// laneSkipReason applies the dispatch-time exclusions to one (good, market)
// candidate and returns the FIRST reason it is refused, or skipNone if it may
// fly. Order: blacklist (guard 4) → open-contract good (guard 3) → leash (guard
// 2: the LeashRadius bound, then the projected CRUISE leg-time from the hull's
// engine speed against MaxLegDuration). None weakens the pre-existing HubRadius
// filter; each only tightens (RULINGS #4).
func (d *IdleArbDispatcher) laneSkipReason(good, sink string, distance float64, excludedContractGoods map[string]struct{}, engineSpeed int, consult absorptionConsult) idleArbSkipReason {
	if d.isBlacklisted(good) {
		return skipReasonBlacklist
	}
	if _, ok := excludedContractGoods[good]; ok {
		return skipReasonContractGood
	}
	if distance > d.cfg.LeashRadius {
		return skipReasonLeash
	}
	legSeconds := shared.FlightModeCruise.TravelTime(distance, engineSpeed)
	if time.Duration(legSeconds)*time.Second > d.cfg.MaxLegDuration {
		return skipReasonLeash
	}
	// LANE MUTEX (sp-lbbm): a (good, sink) already worked by a live or still-
	// recovering leg — including one launched earlier THIS pass — is held. Checked
	// before the ledger consult so a sink THIS dispatcher already holds keeps
	// reporting lane-held (existing behavior), and because pickHubLocalLane scans ALL
	// (good, sink) pairs and keeps the best NON-skipped one, a held best lane makes
	// the hull fall back to its next-best unheld sink rather than collide.
	if d.lanes.held(laneKey{good: good, sink: sink}) {
		return skipReasonLaneHeld
	}
	// ABSORPTION LEDGER (sp-78ai L2): a sink the in-memory mutex does NOT hold but
	// another engine has reserved in flight, or a recovering shadow still blocks — the
	// cross-engine / cross-restart generalization of the mutex. Same structural hole
	// as sp-i8vx (in-flight exposure), closed here from the market-absorption side.
	if consult.reserved(good, sink) {
		return skipReasonReserved
	}
	return skipNone
}

// recordSkip increments the cumulative counter for a dispatch-time guard skip
// (guard 5) and reports whether it was one. skipNone is not a skip — the hull
// simply had no profitable local lane this tick.
func (d *IdleArbDispatcher) recordSkip(reason idleArbSkipReason) bool {
	switch reason {
	case skipReasonBlacklist:
		d.skipBlacklist++
	case skipReasonContractGood:
		d.skipContractGood++
	case skipReasonLeash:
		d.skipLeash++
	case skipReasonLaneHeld:
		d.skipLaneHeld++
	case skipReasonReserved:
		d.skipReserved++
	default:
		return false
	}
	return true
}

// logHarvestSummary emits the guard-5 observability line in MESSAGE TEXT (not a
// metadata map — the CLI renderer drops metadata), carrying the attempt rate and
// the cumulative per-reason skip counts the captain's acceptance and the
// fleet-sizing rule read. Margin-aborts are a POST-launch refusal the arb run
// logs per-leg in its own message text ("... aborting before buy"); they are not
// summed here because the dispatcher's launch is fire-and-forget and never
// observes the run's outcome. Emitted only when the pass did something, to keep
// idle ticks quiet.
func (d *IdleArbDispatcher) logHarvestSummary(ctx context.Context, launchedThisPass, skipsThisPass, rehomedThisPass int) {
	if launchedThisPass == 0 && skipsThisPass == 0 && rehomedThisPass == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	rate := 0.0
	if elapsed := d.clock.Now().Sub(d.startTime).Hours(); elapsed > 0 {
		rate = float64(d.attempts) / elapsed
	}
	logger.Log("INFO", fmt.Sprintf(
		"Idle-arb harvest: %d leg(s) launched this pass; %d hull(s) re-homed this pass; %d attempt(s) total at %.1f/hr; "+
			"skipped legs - blacklist %d, contract-good %d, leash %d, lane-held %d, reserved %d; re-homed %d total (cumulative; margin-aborts logged per-leg by the arb run)",
		launchedThisPass, rehomedThisPass, d.attempts, rate,
		d.skipBlacklist, d.skipContractGood, d.skipLeash, d.skipLaneHeld, d.skipReserved, d.rehomed,
	), nil)
}
