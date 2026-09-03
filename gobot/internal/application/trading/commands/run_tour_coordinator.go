package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// tourPriceTolerancePct is the live-vs-planned price gate: a trade whose live
	// price has moved more than this from the planner's projection is skipped and
	// triggers a re-plan (matches the graduation-gate ±15% metric).
	tourPriceTolerancePct = 15
	// tourMaxReplansDefault bounds re-plans per tour when the captain leaves
	// --replan-limit at 0.
	tourMaxReplansDefault = 2
	// maxTourHops bounds the planner's search (spec: ≤6 hops); the executor caps hops in
	// the constraint it sends. The per-tour distinct-system cap rides cmd.MaxTourSystems ->
	// TourConstraints.MaxTourSystems to the Python solver, which clamps it to [2,
	// MAX_HOPS_DEFAULT] and falls back to its own MAX_TOUR_SYSTEMS default (2) when unset —
	// this side enforces no distinct-system bound of its own.
	maxTourHops = 6
	// unreachableLaneReason labels the drop counter: a good with a cheap source
	// IN the tour graph but its best sink in a system OUTSIDE it (>1 gate hop away), so
	// source and sink never co-occur in one snapshot and the solver can never plan the
	// lane. This is the "exotic good-level blind spot" the diagnostic makes loud.
	unreachableLaneReason = "counterparty_system_unreachable"
	// unreachableLaneMinSpreadPerUnit gates the diagnostic to materially profitable
	// lanes: the observed exotic misses run 14k–37k/u (LASER_RIFLES 14,078; HOLOGRAPHICS
	// ~19,800; QUANTUM_DRIVES ~37,000), so a 5k floor captures that class while filtering
	// routine sub-5k cross-map spreads that would only add noise. A tuning knob, not a
	// trade gate — it only decides what the observation counts.
	unreachableLaneMinSpreadPerUnit = 5000
	// unreachableLaneLogTopN caps how many of the richest dropped lanes are named in the
	// log line per plan (the counter still aggregates ALL of them); mirrors the solver's
	// TOP_REJECTED_N observability parity so the log can't spam.
	unreachableLaneLogTopN = 3
	// defaultModelArtifactPath is where the checked-in market-model artifact lives
	// relative to the daemon's working directory (repo root). The executor reads
	// fit_version + era from it at launch to bind the planner to the exact model —
	// an unreadable artifact fails OPEN to single-lane (RULINGS #4: never guess a
	// version), never a phantom trade.
	defaultModelArtifactPath = "gobot/services/routing-service/model_artifacts/market_model.json"
	// tourStarvationLimit bounds how many CONSECUTIVE no-progress tours (planner
	// returns no profitable tour, or a feasible plan executes zero trades) a
	// continuous run (--iterations -1) tolerates before it calls margins dead and
	// exits HONESTLY (the container completes). Mirrors the trade-route circuit
	// loop's noProgressStarvationLimit: one no-plan can be a transient live-recheck
	// miss, several in a row means the system has nothing left worth touring. A
	// no-plan on the VERY FIRST tour (nothing earned yet) is the existing fail-open
	// "tour unavailable" instead, so the single-lane fallback stands.
	tourStarvationLimit = 3
	// defaultDepositCeilingPct is the DEDICATED STOCKER hull's capital-ceiling fallback
	// when its launch config leaves capital_ceiling_pct at 0 — a stocker is a hull the
	// captain EXPLICITLY launches to fill the warehouse, so a default is its intended
	// behavior. The OPPORTUNISTIC tour path does NOT apply this default: a 0/absent tour
	// ceiling PARKS pre-positioning rather than turning money movement on with no
	// analyst-ruled number (RULINGS #5). Junior to the working-capital reserve; an
	// unreadable balance yields ZERO candidates (fail closed, RULINGS #4).
	defaultDepositCeilingPct = 10
	// tourTreasuryRetryBackoff is the interruptible pause a CONTINUOUS (--iterations -1)
	// dynamic-cap (--max-spend 0) tour waits before RE-TRYING when the live treasury read
	// fails at re-resolution time. RULINGS #4: an unreadable balance fails CLOSED (never
	// spend, never fall back to unlimited/stale) — but failing closed must PAUSE and
	// RETRY, not silently end the -1 loop, or a transient shared-agent read blip
	// completes the container after a single iteration. Mirrors liquidationRetryBackoff's
	// cadence; clock-injected so tests are instant and a Stop/shutdown never waits it out.
	tourTreasuryRetryBackoff = 20 * time.Second
)

// exitReason* enumerates why the continuous tour loop stopped, surfaced on the
// response for observability (mirrors the trade-route coordinator's ExitReason).
const (
	// tourExitIterations: a finite --iterations budget was consumed (every tour flew).
	tourExitIterations = "iterations_exhausted"
	// tourExitStarvation: tourStarvationLimit consecutive tours found no profitable
	// tour (or flew zero trades) — margins died. An HONEST completion.
	tourExitStarvation = "starvation"
	// tourExitCapitalDenied: tourStarvationLimit tours found a profitable plan but a money
	// guard refused the spend. The margin was there, the cash was not — deliberately NOT
	// starvation, since nothing about the ground died. An HONEST completion.
	tourExitCapitalDenied = "capital_denied"
	// tourExitUnavailable: the very first tour found no plan and nothing was earned —
	// the fail-open no-op (single-lane fallback stands).
	tourExitUnavailable = "tour_unavailable"
	// tourExitPlannerInternalError: the routing-service returned a structured
	// internal_error (an exception it caught, not a transport failure). A real planner
	// OUTAGE — vetoes the container FAILED, never a clean fail-open.
	tourExitPlannerInternalError = "planner_internal_error"
	// tourExitRetired: the operator marked the hull retiring and a boundary found its hold
	// EMPTY, so the run stands it down instead of planning it more work. An HONEST completion.
	tourExitRetired = "retired"
	// tourExitRetiredHolding: the operator marked the hull retiring and the disposal ladder ran
	// out of rungs with cargo still aboard — nothing within reach bids for it. The run stands the
	// hull down rather than looping on a load it cannot sell. An HONEST completion; the residue
	// is named in the exit log so it can be cleared by hand before scrapping.
	tourExitRetiredHolding = "retired_holding"
)

// plannerInternalErrorMarker is the prefix the routing-service stamps on any
// exception it catches inside OptimizeTradeTour (handlers/tour_handler.py:
// infeasible_reason = "internal_error: <exc>"). It uniquely distinguishes a
// planner OUTAGE (the service is up but threw) from both a gRPC transport failure
// (planAndReserve stamps "planner error:") and a legitimate infeasibility
// ("no_profitable_tour", "no_fresh_market_data", …). Kept in lockstep with the
// Python handler's literal.
const plannerInternalErrorMarker = "internal_error:"

// isPlannerInternalError reports whether an infeasible/unavailable reason carries the
// routing-service's structured internal_error marker. Such a reason is a planner
// outage, not a legitimate no-tour verdict, and must surface as a FAILURE.
func isPlannerInternalError(reason string) bool {
	return strings.Contains(reason, plannerInternalErrorMarker)
}

// RunTourCoordinatorHandler runs the one-shot guarded tour. It composes the proven
// RunTradeRouteCoordinatorHandler primitives (travel — multi-jump, dock, purchase,
// sell, observeGood, loadShip, spendFloorBreached) rather than re-implementing them,
// so it inherits every fix those legs carry, and adds the planner call, per-leg live
// re-verification, bounded re-planning, telemetry, and the stranded-cargo veto.
type RunTourCoordinatorHandler struct {
	legs         *RunTradeRouteCoordinatorHandler
	marketRepo   market.MarketRepository
	waypointRepo system.WaypointRepository
	telemetry    trading.TourTelemetryRepository
	planner      routing.RoutingClient
	clock        shared.Clock
	// apiClient live-reads treasury for the default (--max-spend 0) capital budget; nil →
	// no default cap (the per-buy working-capital floor still guards).
	apiClient domainPorts.APIClient
	// treasury is the LEDGER-backed treasury reader (sp-muq66) the tour's money reads —
	// the dynamic max-spend budget and the pre-positioning capital ceiling — go
	// through instead of calling Get Agent every time. nil (every existing test) keeps the
	// direct apiClient read, byte-identical; the daemon injects the shared reader via
	// SetTreasuryReader at boot with no config gate. An unreadable treasury still fails
	// CLOSED either way.
	treasury TreasuryReader
	// gateFees is the ledger-backed per-DEPARTURE-SYSTEM jump-fee table the
	// solver prices crossings against. nil (every existing test, and any daemon that does
	// not wire it) => no table on the wire => every crossing prices at the solver's flat
	// charge, byte-identical to today. Injected via SetGateFeeReader at boot.
	gateFees GateFeeReader
	// jumpTolls estimates what one gate hop currently costs in wall-clock seconds, which the
	// solver prices the MARGINAL term of a crossing at. nil, or a fleet with too few measured
	// hops => 0 on the wire => the solver keeps its fitted default, byte-identical to today.
	jumpTolls JumpTollReader
	// apiSaturation reads how hard the shared request budget is binding; nil, or a fleet
	// with headroom => 0 on the wire => selection on credits/hour.
	apiSaturation APISaturationReader
	// ownTradeRecency reports when the fleet itself last traded in each system, which the
	// reposition pre-rank charges a bounded haircut for. DERIVED from the ledger and never
	// persisted (RULINGS #2): the transactions table already dates every buy and sell, so a
	// second stored copy could only drift from it. nil (every existing test, and any daemon
	// that does not wire it) => no candidate is stamped => the pre-rank orders exactly as before.
	ownTradeRecency OwnTradeRecencyReader
	// modelArtifactPath is the daemon-configured (absolute) path to the market-model
	// artifact this coordinator reads at launch, injected from cfg.Routing.ModelArtifactPath.
	// Empty → the repo-relative defaultModelArtifactPath fallback. A per-run
	// cmd.ModelArtifactPath (tests) still wins over this.
	modelArtifactPath string

	// scanPolicy is the tour-scan load policy stamped onto ctx at run start so the
	// shared scan path (arrival scan + post-trade impact scan) SAMPLES the deliberate
	// price-impact instrumentation instead of scanning every market around every trade.
	// scanPolicySet gates the stamp: unset (the default for every test and any daemon
	// that does not wire it) stamps NOTHING and every scan runs unsampled. Injected via
	// SetScanPolicy from cfg.TradeImpact at boot.
	scanPolicy    shared.ScanPolicy
	scanPolicySet bool

	// rankerAgeCaps is the BACKSTOP horizon the tour snapshot builder drops stale market
	// rows against (sp-t5sh5), each good measured against its OWN activity's cap. The
	// daemon injects the config-resolved table via SetRankerAgeCaps; the zero value is
	// SAFE (RankerAgeCaps.For fills the fitted armed defaults per activity), so an unwired
	// handler — every existing test — builds the snapshot on the fit rather than a cap of 0.
	rankerAgeCaps trading.RankerAgeCaps

	// stalenessDiscount prices what a quote's AGE costs INSIDE that backstop: the snapshot
	// emits prices already marked toward the adverse side, so the solver ranks a stale
	// board at a haircut. The zero value is SAFE and ARMED (the fitted curve at face value).
	stalenessDiscount trading.StalenessDiscount

	// sinkFreshnessMaxAge is the sp-tgll8 item-2 "FRESH" clause on the firm-sink buy gate:
	// at buy execution the gate re-reads each held sell-sink's LIVE cached market_data and
	// refuses the buy when that row is older than this (the sink is no longer a trustworthy
	// FRESH sink). The daemon injects cfg.TradeFleet.ResolvedSinkFreshnessMaxAge() via
	// SetSinkFreshness at boot (mirrors SetRankerAgeCaps/SetCargoBlocklist). The zero value
	// (setter never called — every existing test) leaves the freshness clause INERT, so the
	// gate behaves exactly as sp-pcxju; production ships it ARMED at the conservative default.
	sinkFreshnessMaxAge time.Duration

	// freshness derives every market-freshness cap on the tour path from the LIVE scan
	// rotation instead of a minute count written into the source (sp-k4z5b), and carries
	// the operator's live floors from `tune --operation tour`. nil — every existing test —
	// leaves each cap at the floor it was already using, so an unwired handler is
	// byte-identical. The daemon injects it via SetMarketFreshness at boot.
	freshness *MarketFreshness

	// workSensor backs the per-operation capital budget: it answers whether the CONSTRUCTION
	// side is live, which is what sizes trade's share of deployable capital. It feeds the
	// budget's GRACEFUL DEGRADATION only — the budget itself always binds. Neither a nil
	// sensor nor a read error may fail OPEN: both are answered conservatively (construction
	// assumed live, trade takes only its proportional share). The daemon wires the
	// container-backed sensor unconditionally via SetCapitalWorkSensor, with no config gate
	// between. See tradeCapitalBudget.
	workSensor common.CapitalWorkSensor

	// repositionPersister durably records an in-flight margins-death reposition (its
	// target system+waypoint) into the container config so a daemon restart mid-jump
	// resumes toward the SAME ground (RULINGS #2). Optional; nil disables persistence
	// (a restart mid-jump then re-plans at the hull's current position rather than
	// resuming the reposition — fail-open, matching the sibling optional-port
	// contract). The daemon injects a container-config-backed persister via
	// SetRepositionPersister.
	repositionPersister RepositionStatePersister

	// tourLegPersister durably records the SELL leg a hull is flying (sink + goods) into the
	// container config, so a restart mid-leg finishes that discharge before re-planning rather
	// than making a laden hull wait out a planner round trip first (RULINGS #2). Optional; nil
	// disables persistence and a restart re-plans at the hull's current position — fail-open,
	// matching the sibling optional-port contract. The daemon injects a container-config-backed
	// persister via SetTourLegPersister.
	tourLegPersister TourLegStatePersister

	// offerPersister durably records the sp-e8d92 relocation OFFER into this container's own config, so
	// the relocator can take a hull at its tour boundary instead of losing the race for the 2.6% of time
	// a hull is idle. Optional and nil-safe: unset, no offer is ever written and the fleet tours exactly
	// as it does today.
	offerPersister RelocationOfferPersister
	// offerPollInterval overrides how often the sp-e8d92 hold re-reads the hull's position. Zero uses
	// the production constant; a test sets it small so the real waiting loop can be exercised at speed
	// instead of being replaced by something that does not.
	offerPollInterval time.Duration

	// mvt holds the MVT trade-loop readers (claim registry, ledger depth reader, transition
	// recorder) injected via SetMVTPorts; unwired (every existing test) the branch is inert and
	// the shadow logger is silent. mvtHulls is the per-hull in-memory loop state, guarded by
	// mvtMu because the handler is a SHARED singleton dispatched concurrently for every touring
	// hull (the strandedStreak discipline); mvtFleet caches each player's fleet-wide stats on
	// the specialist cadence.
	mvt      mvtPorts
	mvtMu    sync.Mutex
	mvtHulls map[string]*mvtHullState
	mvtFleet map[int]*mvtFleetCache

	// mediator dispatches the cargo TransferCargoCommand for haul-to-storage deposit
	// legs. Same mediator the delegated legs use.
	mediator common.Mediator
	// Pre-positioning deposit dependencies, all optional and injected via
	// SetPrePositioning AFTER the storage subsystem is wired (main.go). When any is
	// nil or prePositioning.Enabled is false, no deposit legs are offered or
	// executed and the tour behaves as pure arb.
	storageCoordinator storage.StorageCoordinator
	warehouseFinder    tradingsvc.WarehouseOperationFinder
	demandMiner        tradingsvc.DepositDemandMiner
	prePositioning     tradingsvc.DepositCandidateConfig
	depositCeilingPct  int

	// cargoBlocklist names goods the tour planner must NEVER trade as cargo:
	// the economy-analyst's sub-70-cr/u noise goods (FUEL/ALUMINUM/PLASTICS) whose per-leg
	// dock+dwell tempo cost exceeds the cargo value. Filtered out of the market snapshot in
	// planForState BEFORE it reaches the solver, so a blocklisted good is unselectable as
	// either a buy source or a sell sink. A set (nil/empty ⇒ no filtering ⇒ byte-identical),
	// injected via SetCargoBlocklist from cfg.TradeFleet.CargoBlocklist at boot (mirrors the
	// contract pre_positioning.blocklist mechanism). This is FUEL-as-tradeable-CARGO only —
	// ship refueling (RefuelShipHandler → API RefuelShip) never consults the tour snapshot.
	cargoBlocklist map[string]bool

	// constructionCargoBlocklist names goods the tour must NOT trade ONLY while a construction
	// pipeline is actively executing with unfilled material (h.workSensor.ConstructionHasWork)
	// — unlike cargoBlocklist, which is unconditional. effectiveCargoBlocklist unions the two
	// per call; nil/empty is byte-identical.
	constructionCargoBlocklist map[string]bool

	// depositParked de-dups the pre-positioning parked/dormant verdict so a hull whose
	// deposits are parked — no ceiling configured, treasury at/below the reserve, or an
	// unreadable balance — logs ONCE per container per distinct state, not once per
	// re-plan. Keyed by container id (ship-symbol fallback); the value is the last
	// emitted "<level>|<reason>" signature. Guarded by depositParkedMu because the
	// handler is a SHARED singleton dispatched concurrently for every touring hull.
	depositParkedMu sync.Mutex
	depositParked   map[string]string

	// strandedStreak counts CONSECUTIVE origin-level empty reposition discoveries per hull:
	// a hull whose origin has no durable gate adjacency AND a gate-inaccessible live probe
	// finds BOTH discovery paths empty and can never self-reposition, so it silently
	// relaunch-loops until a human notices. When the streak crosses the configured threshold
	// (default 3) the coordinator emits ONE WARN + the fleet_hull_stranded_total counter so
	// the watch is paged. Any successful discovery resets the hull's streak. Keyed by ship
	// symbol (globally unique, agent-scoped); the value tracks the accruing system + count +
	// whether this episode already paged, so the page fires once per episode, not per launch.
	// Guarded by strandedMu because the handler is a SHARED singleton dispatched concurrently
	// for every touring hull — the same per-hull state-change de-dup discipline as
	// depositParked. In-memory only: a daemon restart resets every hull's streak (acceptable —
	// a genuinely stranded hull re-accrues its streak within N relaunches).
	strandedMu     sync.Mutex
	strandedStreak map[string]*strandedHullState

	// repositionTieSweep records where each hull's next TIED reposition slice starts. When the
	// pre-rank scores every reachable candidate the same, the top-K cut is decided by the sort's
	// stable symbol tie-break alone — a deterministic alphabet, so a hull that failed re-prices an
	// identical slice on every later attempt from that ground and never explores the rest of its
	// reach. The sweep advances by the candidates each attempt consumed, so consecutive attempts
	// cover disjoint windows. Keyed by ship symbol; reset when the hull's origin changes (a new
	// ground is a fresh reach). Guarded by repositionTieMu because the handler is a SHARED singleton
	// dispatched concurrently for every touring hull — the same per-hull discipline as
	// strandedStreak. In-memory only: a restart begins the sweep again, which is harmless.
	repositionTieMu    sync.Mutex
	repositionTieSweep map[string]*repositionTieState

	// explorePriced orders the grounds the solver has pre-flighted, so the exploration slot can
	// spend its one call on the one the fleet has gone longest without pricing. Keyed by SYSTEM,
	// not by hull: what a ground is worth is a property of the ground, so a sweep one hull
	// advances is one every hull inherits — which is what stops a fleet each re-asking the same
	// ranking's top few. Guarded by exploreMu (the repositionTieSweep discipline); in-memory only.
	exploreMu     sync.Mutex
	explorePriced map[string]uint64
	exploreSeq    uint64

	// purchaseObligation records how many units of each good a hull bought under tour
	// operation and has NOT yet discharged (sold, deposited or liquidated). The
	// honest-completion veto reads it at every terminal exit, so a tour can never release a
	// hull still holding cargo it bought. Keyed by SHIP symbol, not container id, because the
	// obligation must outlive the container that incurred it: the runner restarts an
	// interrupted iteration — and the fleet relaunches an idle hull — with a brand-new
	// in-memory run, and a purchase forgotten across that boundary is exactly how a full hold
	// reached a success=true exit. Guarded by purchaseObligationMu because the handler is a
	// SHARED singleton dispatched concurrently for every touring hull (the same per-hull
	// discipline as strandedStreak). Dropped the moment the hull's hold discharges, so it can
	// never wedge a healthy hull. The map is in-memory, but it is not the record: a daemon
	// restart is routine and the CARGO outlives it, so the first read after a bounce rebuilds
	// the fleet's obligations from the ledger (obligationReader) before anything can exit on
	// an empty one (RULINGS #2).
	purchaseObligationMu sync.Mutex
	purchaseObligation   map[string]map[string]int

	// obligationReader rebuilds the purchase obligations above from the transactions ledger,
	// once per player per daemon lifetime, on the first run that needs them. DERIVED, never
	// persisted: the ledger already records every buy's hull, good, units and operation, so a
	// second stored copy could only drift from it. Optional and nil-safe — unset (every test
	// that does not wire it) the map simply starts empty after a restart, which is the
	// pre-reload behaviour and never worse. obligationSeeded tracks which players are already
	// rebuilt; obligationSeedMu is its own lock, always taken BEFORE purchaseObligationMu.
	obligationReader ledger.OutstandingPurchaseReader
	obligationSeedMu sync.Mutex
	obligationSeeded map[int]bool

	// rateFloorLastRelocation records the last rate-floor relocation time per hull for the
	// dwell window: a hull that relocated within reposition_rate_floor_dwell_minutes is not
	// a rate-floor candidate again, so it cannot hop-scotch across successive productive
	// tours. Keyed by ship symbol; guarded by rateFloorMu because the handler is a SHARED
	// singleton dispatched concurrently for every touring hull (the same per-hull discipline
	// as strandedStreak / depositParked). In-memory only: a daemon restart resets the timer
	// (acceptable — dwell is a soft anti-thrash cadence cap, not a correctness invariant).
	rateFloorMu             sync.Mutex
	rateFloorLastRelocation map[string]time.Time

	// pendingRelocationsBySystem counts rate-floor relocations currently IN FLIGHT toward each
	// destination system (atomic anti-herd). A relocation increments its target at the
	// commit-decision (just BEFORE the jump) and decrements it on landing (defer, after the
	// synchronous RepositionToWaypointWithinJumps returns). excludeHerdedSystems ADDS this
	// pending count to the LANDED count, so a concurrent evaluator sees in-flight movers and
	// respects the per-system cap — the landed count alone lags a full multi-hop flight, which
	// would let every under-earner read the richest frontier system as under-cap, pile in,
	// dilute, fall under-floor, and migrate as a bunch. Guarded by pendingMu; empty when the
	// rate-floor trigger never commits (so the herd check is byte-identical when the trigger is
	// off). In-memory only (a restart resets it — acceptable; it only bounds a live concurrent
	// cohort within one daemon lifetime).
	pendingMu                  sync.Mutex
	pendingRelocationsBySystem map[string]int

	// recentSells is the per-hull sell history the same-market rebuy guard plans against:
	// ship symbol -> (market, good) -> when that hull last sold there. Guarded by
	// recentSellsMu because the handler is a SHARED singleton dispatched concurrently for
	// every touring hull. See run_tour_coordinator_rebuy.go for why it is in-memory.
	recentSellsMu sync.Mutex
	recentSells   map[string]map[marketGood]time.Time

	// --- Cross-engine absorption coordination ---
	// absorptionLedger, when wired via SetAbsorptionLedger, makes the tour a ledger
	// WRITER (reserve planned tranches at plan-accept, convert to recovery shadows at
	// sale, release on re-plan/exit) AND a READER (net outstanding depth into each plan
	// so the solver plans AROUND sinks other containers occupy). Nil (tests that don't
	// wire it) → no netting, no reservations: the tour plans and flies exactly as before.
	absorptionLedger absorption.Ledger
	// tourPlannedTTLSlack pads a plan's projected round-trip TTL (backstop to the sweep +
	// dead-container reclaim). 0 → defaultTourPlannedTTLSlack.
	tourPlannedTTLSlack time.Duration
	// contendedHolderLogAt de-dups the sp-cddfs enriched "could not reserve tour depth"
	// refusal log so a persistently-contended lane — retried every plan cycle, bounded
	// only by planner RTT — logs its holder attribution ONCE per tourContendedHolderLogCooldown
	// PER CONTAINER instead of flooding daemon.log on every retry. Keyed by container id;
	// guarded by its own mutex because the handler is a SHARED singleton dispatched
	// concurrently for every touring hull (the same per-hull discipline as
	// strandedStreak/depositParked). In-memory only: a daemon restart clears every hull's
	// cooldown, which is harmless (worst case, one extra enriched log after a restart).
	contendedHolderLogMu sync.Mutex
	contendedHolderLogAt map[string]time.Time
	// planGates holds one gate per contention domain (a player's system), so the netting
	// read and the reservation that follows it are one critical section for every planner
	// that could pick the same sink: concurrent planners otherwise all net against the same
	// pre-reservation snapshot and converge on one sink. planSlots bounds how many of a
	// player's planners hold gates at once. This handler is a single instance shared by
	// every tour container, so both are instance-level (see the plangate file).
	planGateMu sync.Mutex
	planGates  map[planDomain]chan struct{}
	planSlots  map[int]chan struct{}
	// planGateWait bounds the queue wait; 0 → tourPlanGateWait.
	planGateWait time.Duration
	// planConcurrency bounds concurrent planners per player; 0 → defaultTourPlanConcurrency.
	planConcurrency int
	// recoveryHalfLives caches the fitted per-tier recovery half-lives (minutes) read
	// from the model artifact ONCE, for the report-only projected_recovery_burden metric
	// (Q3). Immutable after the first load; the handler is shared across concurrent tour
	// runs, so it is loaded under recoveryOnce and never mutated per-run.
	recoveryHalfLives map[string]float64
	recoveryOnce      sync.Once

	// sinkScanner backs the out-of-horizon lane diagnostic: after building the in-scope
	// snapshot, the coordinator asks it for each in-scope-sourced good's best sink ACROSS
	// ALL SYSTEMS, and counts+logs the lanes whose best sink lies beyond the 1-gate-hop
	// tour graph — the "exotic good-level blind spot" made loud. Optional and nil-safe:
	// unset (tests, or metrics-disabled builds) → the diagnostic no-ops and the tour plans
	// exactly as before (RULINGS #4 — observation never gates the trade path).
	sinkScanner outOfHorizonSinkScanner

	// captainEvents emits the coordinator error-loop event when the continuous loop's
	// dynamic-budget resolve fails with the same cause (live treasury unreadable) for
	// DefaultStreakThreshold consecutive iterations — the one unbounded in-loop
	// silent-retry in this otherwise worker-shaped coordinator (fail-closed pause+
	// continue). Optional-injection via SetEventRecorder, nil-safe like the contract
	// coordinator's captainEvents.
	captainEvents captain.EventRecorder
}

// Handle executes the one-shot tour. A fail-open no-op and a stranded-cargo veto both
// return a nil Go error (the veto is threaded through CompletionOutcome); an
// operational failure mid-tour returns the underlying error so the runner can retry
// (a retry re-plans from current position/cargo — cargo-aware, never a blind re-buy).
func (h *RunTourCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTourCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	response := &RunTourCoordinatorResponse{ShipSymbol: cmd.ShipSymbol, TourID: cmd.ContainerID}
	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}
	if !response.TourUnavailable && !response.CargoStranded {
		response.Completed = true
	}
	return response, nil
}

// execute runs the hull's continuous tour loop: resolve the run's budget, reserve and
// model version, resume any in-flight reposition, then plan and fly tours until a
// terminal condition ends the run.
//
// The named return err is load-bearing: the tour-exit metrics defer and the
// purchase-obligation epilogue below both branch on it. Binding err with := inside a
// nested scope shadows it and silently breaks the "observe only on honest completion"
// contract — give an inner error a distinct name (the file's verr/rerr convention) and
// assign to err explicitly.
func (h *RunTourCoordinatorHandler) execute(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse) (err error) {
	logger := common.LoggerFromContext(ctx)

	// Observe the tour_run's terminal outcome exactly once, and ONLY on an HONEST
	// completion (err == nil). A resumable exit — ctx-cancel on shutdown, a fail-closed
	// treasury pause, a travel error mid-reposition — returns non-nil and sets no
	// ExitReason; the container is re-adopted and runs again, so counting an exit or
	// observing a truncated duration there would double-count one logical run. Every
	// err==nil return sets ExitReason first (unavailable/starvation/iterations), so the
	// counter and the histogram move together. Pure observation after the loop has already
	// decided (RULINGS #4); a metrics miss cannot alter the outcome.
	tourStart := h.clock.Now()
	defer func() {
		if err != nil || response.ExitReason == "" {
			return
		}
		metrics.RecordTourExit(cmd.PlayerID, response.ExitReason)
		metrics.ObserveTourDuration(cmd.PlayerID, h.clock.Now().Sub(tourStart).Seconds())
	}()

	// Stamp every ledger row this run's buy/sell legs write with operation_type=
	// "tour". The delegated cargo-tx path reads this operation context off ctx and
	// persists opCtx.NormalizedOperationType() ("tour_run" → "tour"); without it,
	// tour trades land under the default and contaminate the single-lane baseline
	// the graduation gate measures the tour against (the baseline filters
	// operation_type <> 'tour'). Mirrors how every coordinator tags its writes at
	// the boundary (run_trade_route_coordinator.go's "trade_route").
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, tourRawOperationType))

	// Stamp the tour-scan load policy so the shared arrival + post-trade scans SAMPLE
	// the deliberate price-impact instrumentation (the top API consumer) rather than
	// scanning every market around every trade. It rides the SAME ctx the operation
	// context above already threads to the delegated travel + cargo legs, so the
	// arrival scan (RouteExecutor) and the post-trade scan (cargo transactions) both
	// see it. Unset (tests that don't wire it) → no stamp → full-scan behavior.
	if h.scanPolicySet {
		ctx = shared.WithScanPolicy(ctx, h.scanPolicy)
	}

	// Release this container's PLANNED reservations on EVERY exit path (clean
	// completion, error, ctx-cancel) so a finished tour stops occupying sink/ask depth
	// other engines net against. Converted EXECUTED shadows are LEFT (real recovery
	// still decaying); a ctx-cancelled exit that cannot run the delete leaves the rows
	// to the ledger's TTL sweep + dead-container reclaim (the belt-and-suspenders
	// cleanup).
	defer h.releaseTourReservations(ctx, cmd)

	// netBought is the hull's OUTSTANDING tour-purchase obligation: units bought under tour
	// operation minus what has since left the hold. It is adopted from — and handed back to —
	// the per-hull carry, because a purchase the run cannot discharge must not be forgotten by
	// the restart that interrupted it. Cumulative across every tour this run: a tour ending
	// with held cargo is NOT stranded mid-run, since the next tour re-plans from the hull's
	// current cargo and the solver sells it as launch inventory. Only cargo BOUGHT and never
	// discharged survives to veto the completion; cargo the tour never bought is never in here,
	// so a hull handed a foreign load is never falsely vetoed.
	netBought := h.adoptPurchaseObligation(ctx, cmd.ShipSymbol, cmd.PlayerID)

	// The honest-completion epilogue. Deferred so EVERY exit funnels through it — a fail-open
	// "tour unavailable", a planner outage, margins-death, an unreadable model artifact — and
	// a future exit condition cannot bypass the laden check the way the previous exits did. A
	// resumable (non-nil error) exit claims nothing: the runner retries it, so only the carry
	// is written back and no terminal verdict is reached.
	defer func() {
		defer h.retainPurchaseObligation(cmd.ShipSymbol, netBought)
		if err != nil {
			return
		}
		// Enforce the hold invariant before the verdict is read: a hull is never parked
		// holding cargo THIS RUN bought while a bid in its current system will still take
		// it. The veto below then reports only the strands nothing would buy.
		h.liquidateStrandBeforeExit(ctx, cmd, response, netBought)
		h.vetoLadenExit(ctx, cmd, response, netBought)
	}()

	// Bind the model version from the checked-in artifact (RULINGS #4: unreadable →
	// fail OPEN to single-lane, never guess a version).
	artifactPath := h.resolveModelArtifactPath(cmd)
	modelVersion, err := readTourModelVersion(artifactPath)
	if err != nil {
		response.TourUnavailable = true
		response.TourUnavailableReason = fmt.Sprintf("tour unavailable: model artifact unreadable (%s): %v", artifactPath, err)
		response.ExitReason = tourExitUnavailable
		logger.Log("WARNING", response.TourUnavailableReason, map[string]interface{}{
			"artifact": artifactPath, "error": err.Error(),
		})
		return nil
	}
	response.ModelVersion = modelVersion

	reserve := h.resolveWorkingCapitalReserve(cmd, logger)
	maxHops, replanLimit := resolveTourHopBudget(cmd)
	budget := tourPlanBudget{maxHops: maxHops, reserve: reserve, modelVersion: modelVersion}

	// One line per container naming the resolved fleet-wide sink cap: the knob must move with
	// the solver's tranche cap, and a mismatch nothing prints is one nobody can read back.
	logger.Log("INFO", fmt.Sprintf("Tour absorption sink cap: acap_tranches=%d tranches of trade_volume", cmd.aCapTranches()), map[string]interface{}{
		"action": "tour_acap_tranches", "ship_symbol": cmd.ShipSymbol,
		"container_id": cmd.ContainerID, "acap_tranches": cmd.aCapTranches(),
	})

	// Iteration budget: 0 → the one-tour default (the original one-shot); -1 →
	// continuous until margins die; N>0 → exactly N tours.
	iterations := cmd.Iterations
	if iterations == 0 {
		iterations = 1
	}
	continuous := iterations < 0

	// The budget counts PRODUCTIVE tours (ToursCompleted), not attempts: "N tours"
	// means N tours actually flown, so a transient no-plan mid-run is retried (bounded
	// by the starvation streak) rather than silently burning a tour slot.
	noProgressStreak := 0

	// capitalDeniedStreak counts the zero-trade tours a MONEY GUARD caused since the last
	// productive one, kept apart from noProgressStreak because the two demand opposite
	// responses: a dead margin means leave, denied capital means wait — the treasury crosses
	// the working-capital floor constantly while contracts and hulls are being paid for, and
	// the ground is still rich on the other side of the dip.
	capitalDeniedStreak := 0

	// episode tracks the current margins-death reposition: whether this run has already
	// spent its ONE reposition since the last productive tour, and the systems involved
	// (for the honest "margins died at X, repositioned to Y, died there too" exit). A
	// productive tour clears it — a fresh ground earned means a LATER death may rotate
	// again (grounds are renewable flows), which is the whole point; the one-per-episode
	// bound only stops hop-scotching WITHOUT trading in between.
	var episode repositionEpisode

	// sp-e8d92 FIRST REFUSAL. relocationOfferUntil is the deadline this run wrote at its last boundary;
	// the loop top waits it out before planning again, so the relocator gets the hull BEFORE the tour
	// re-anchors it locally. Seeded from the container config so a restart mid-offer honours the SAME
	// deadline rather than opening a fresh one (RULINGS #2) — an offer that renewed itself across
	// restarts is precisely the unexpiring hold that would strand a trade hull.
	relocationOfferUntil := cmd.RelocationOfferUntil

	// A hull already stuck-laden at container START inherited that load across a restart or a
	// veto relaunch. Until it trades again its rescue is a buyer, not a fresh ground.
	launchedLaden := false

	// retirementJumps counts the reach hops the retirement disposal ladder has spent this run
	// (see retirementReachJumpLimit); never refunded, so one run can never hunt. Per-run and
	// deliberately not persisted: the jump itself IS persisted mid-flight (RULINGS #2) so a
	// restart lands the hull where it was going, and a relaunch — which the fleet coordinator
	// paces — re-earns the budget against a hold that only ever gets smaller.
	retirementJumps := 0

	// strandDisposalJumps is the same budget for the margins-death pre-release drain, kept apart
	// because a marked hull breaks out at the boundary above and never reaches that rung.
	strandDisposalJumps := 0

	if continuous {
		resumed, rerr := h.resumeInFlightReposition(ctx, cmd, logger)
		if rerr != nil {
			return rerr
		}
		episode = resumed
		// A hull re-adopted MID-JUMP has left the ground its leg was flown on, so the jump
		// destination wins and the abandoned leg is dropped: flying back across a gate to a
		// sink the hull is no longer near is the one outcome worse than re-planning.
		if !episode.repositioned {
			if lerr := h.resumeInFlightTourLeg(ctx, cmd, response, netBought); lerr != nil {
				return lerr
			}
		}
		// Asked AFTER the resume: a hull that just discharged the leg it was flying is no
		// longer stuck-laden, and promoting the offload rung for it would rank fresh grounds
		// against a hold that no longer exists.
		launchedLaden = h.launchedStuckLaden(ctx, cmd)
		if cmd.MVTLoop {
			if err := h.mvtRecover(ctx, cmd, response, &episode, netBought, h.mvtBootBudget(ctx, cmd, budget)); err != nil {
				return err
			}
		}
	}

	// budgetMon makes a continuous run that can never re-resolve its dynamic budget
	// observable: the fail-closed pause+continue below is the one UNBOUNDED silent
	// retry in this otherwise worker-shaped coordinator — a treasury source wired but
	// unreadable every iteration loops WARNING+backoff forever. Once the streak
	// crosses, it emits a captain event; a readable resolve resets it. Created once
	// per execute (one continuous run) so the streak persists across the loop's
	// iterations.
	budgetMon := health.NewMonitor(health.DefaultStreakThreshold)

	for continuous || response.ToursCompleted < iterations {
		// A stop/shutdown cancels ctx (interruptAllContainers escalates the STOPPING
		// flag to a ctx cancel). Exit RESUMABLE at the tour boundary by returning the
		// ctx error, which the runner routes through its ctx.Err() path (re-adopted at
		// next boot) — never let a cancel be misread as a swallowed planner no-plan and,
		// via the starvation streak, COMPLETE a -1 container (a COMPLETED row is dropped
		// from the recovery set and the hull is lost).
		if err := ctx.Err(); err != nil {
			return err
		}

		// RETIREMENT. A hull the operator marked never plans another tour — here, before any
		// ground is chosen for it. Empty, it stands down ready to scrap; laden, it descends the
		// sell-only disposal ladder (dispose here, else reach one sink, else stand down naming
		// the residue) until the hold is gone. Read fresh each pass so a hull marked mid-run
		// notices without a restart, and ahead of the relocation hold below so a hull leaving
		// service is never parked waiting for an offer.
		//
		// TERMINATION: a pass that sold anything strictly shrinks a finite hold; a pass that sold
		// nothing spends one of this run's retirementReachJumpLimit jumps, never refunded; a pass
		// that can do neither ends the run. So the ladder is bounded by hold size plus two hops
		// and cannot ping-pong between systems on a load it keeps failing to clear.
		if retiring, drained := h.retirementState(ctx, cmd); retiring {
			if drained {
				h.standDownRetiring(ctx, cmd, response, logger)
				break
			}
			sales, derr := h.disposalPass(ctx, cmd, response, netBought, retirementDisposalKind)
			response.RetirementDisposalSales += sales
			if derr != nil {
				return derr
			}
			if sales > 0 {
				continue
			}
			if retirementJumps < retirementReachJumpLimit {
				// nil episode: a hull leaving service is never planned another ground, so it has
				// no reposition episode for the hop to spend.
				reached, rerr := h.reachDisposalSink(ctx, cmd, response, nil, retirementDisposalKind)
				if rerr != nil {
					return rerr
				}
				if reached {
					retirementJumps++
					continue
				}
			}
			h.standDownRetiringHolding(ctx, cmd, response, logger)
			break
		}

		// FIRST REFUSAL (sp-e8d92): if this hull was offered to the relocator at the last boundary, wait
		// the offer out before planning here again. This is the whole mechanism — the fleet occupies 23
		// of 373 tradeable systems because the instant a hull is free its tour re-plans it locally, and
		// the relocator only ever sees the 2.6% of time a hull is idle. Holding removes the race instead
		// of trying to win it faster.
		//
		// BOUNDED BY CONSTRUCTION: holdForRelocationOffer ends at the deadline whatever the config says,
		// and returns immediately on a cancelled context. A taken offer clears and the loop simply plans
		// at the hull's NEW ground; a lapsed one backs off and plans locally exactly as today.
		if relocationOfferStands(relocationOfferUntil, h.clock.Now()) {
			if ship, serr := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); serr == nil && ship != nil && ship.CurrentLocation() != nil {
				h.holdForRelocationOffer(ctx, cmd, relocationOfferUntil, ship.CurrentLocation().SystemSymbol)
			}
			relocationOfferUntil = time.Time{}
		}

		tourMaxSpend, unresolved, serr := h.resolveTourSpendCap(ctx, cmd, response, budgetMon, reserve, logger)
		if serr != nil {
			return serr
		}
		if unresolved {
			continue
		}
		tourBudget := budget.withMaxSpend(tourMaxSpend)

		tradesBefore := response.TradesExecuted
		deniedBefore := response.CapitalDeniedBuys
		feasible, reason, terr := h.runOneTour(ctx, cmd, response, netBought, tourBudget, replanLimit)
		if terr != nil {
			return terr
		}

		// The plan stopped itself at a drained LEG boundary on the same mark. End the run here
		// rather than falling into the streak/rescue ladder, which would rank fresh ground for
		// a hull that is leaving service. A plan cut short having traded still flew its tour.
		if response.RetirementStandDown {
			if feasible && response.TradesExecuted > tradesBefore {
				response.ToursCompleted++
			}
			h.standDownRetiring(ctx, cmd, response, logger)
			break
		}

		// The plan discharged under a mark set MID-FLIGHT and the hull is still laden. Go
		// straight back to the boundary, where the disposal ladder takes over: everything below
		// ranks fresh ground, offers the hull for relocation or waits out a treasury dip, and
		// none of that belongs to a hull leaving service. A plan that traded still flew its tour.
		if response.RetirementDischarging {
			if feasible && response.TradesExecuted > tradesBefore {
				response.ToursCompleted++
			}
			response.RetirementDischarging = false
			continue
		}

		// A planner-returned internal_error is a routing-service OUTAGE (an exception it
		// caught and returned as a structured feasible=false), NOT a legitimate "no
		// profitable tour". Terminalize the container FAILED via the honest-completion
		// veto so a live outage is surfaced LOUDLY instead of masked as a clean "tour
		// unavailable" success. Checked BEFORE the fail-open/starvation classification
		// below so it wins in BOTH the one-shot and continuous paths, and regardless of
		// how many tours already flew. Transport errors ("planner error:") and genuine
		// infeasibility still fail open below (single-lane fallback stands).
		if !feasible && isPlannerInternalError(reason) {
			response.PlannerInternalError = true
			response.PlannerInternalErrorReason = reason
			response.ExitReason = tourExitPlannerInternalError
			logger.Log("ERROR", reason, map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "model": modelVersion,
			})
			return nil
		}

		// A PRODUCTIVE tour (feasible AND flew >=1 trade) resets the starvation streak and
		// ENDS any reposition episode: a fresh ground earned, so a later death may rotate
		// again (the one-per-episode bound only prevents hop-scotching WITHOUT trading in
		// between).
		if feasible && response.TradesExecuted > tradesBefore {
			noProgressStreak = 0
			capitalDeniedStreak = 0
			response.ToursCompleted++
			episode = repositionEpisode{}
			// Trading again: whatever it carries now is this run's own doing.
			launchedLaden = false
			if continuous {
				offerUntil, perr := h.afterProductiveTour(ctx, cmd, response, netBought, tourBudget)
				if perr != nil {
					return perr
				}
				relocationOfferUntil = offerUntil
			}
			continue
		}

		// No progress this tour. On the VERY FIRST tour with no plan, nothing was earned.
		// A finite/one-shot run (iterations != -1) fails open here — the single-lane
		// fallback stands, the original one-shot behavior preserved exactly. A CONTINUOUS
		// (-1) run does NOT: a recovered engine re-enters at ToursCompleted==0 having LOST
		// its pre-restart productive standing across the daemon boundary, and dying on ONE
		// drained-ground plan (bypassing the rank-and-reposition rescue) would strand hulls
		// that were productive before the restart on ground the pre-restart cohort had
		// drained. So a continuous run falls THROUGH to the streak, letting iteration-1
		// infeasibility accumulate toward the SAME reposition rescue as margins-death rather
		// than completing the container; a genuinely dead neighbourhood still exits honestly
		// below (no candidate clears the floor). The unreadable-treasury PAUSE never reaches
		// here (it `continue`s above, before runOneTour), so it is untouched.
		if !feasible && response.ToursCompleted == 0 && !continuous {
			response.TourUnavailable = true
			response.TourUnavailableReason = reason
			response.ExitReason = tourExitUnavailable
			logger.Log("INFO", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "model": modelVersion})
			return nil
		}

		// This tour flew zero trades because a MONEY GUARD refused its spend, not because the
		// ground had nothing to offer. The starvation breaker exists to detect dead margins;
		// a treasury dip is the opposite diagnosis and gets the opposite response — wait, do
		// not leave. Feeding it to the breaker parks a hull the market would still pay, and
		// the relaunch re-plans the same trade and is killed the same way. The pause mirrors
		// the unreadable-treasury guard trip above (which likewise leaves the streak alone);
		// it is bounded so a treasury that never recovers still ends the run honestly, and
		// the exit epilogue clears the hold on the way out.
		// The same diagnosis one step EARLIER in the chain: a budget re-resolved from live
		// treasury that came out at zero (the deployable pool is empty — treasury at or below the
		// working-capital reserve) refuses every plan before the market is priced, so this tour's
		// zero trades say nothing about the ground. Left in the starvation streak it reads as
		// margin-death and parks the hull — on a treasury dip that moves every minute, and after
		// ranking candidate grounds it equally cannot buy at. Scoped to the DYNAMIC budget, which
		// re-resolves each pass and can therefore recover; an explicit --max-spend at or below its
		// reserve is two operator constants that waiting cannot change, so it exits as before.
		budgetDenied := cmd.MaxSpend == 0 && h.budgetDeniesEverySpend(cmd, tourBudget)
		if response.CapitalDeniedBuys > deniedBefore || budgetDenied {
			deniedCause := "a money guard refused its buys"
			if budgetDenied {
				deniedCause = fmt.Sprintf("the resolved spend budget left no headroom (max-spend %d)", tourBudget.maxSpend)
			}
			capitalDeniedStreak++
			if capitalDeniedStreak < tourStarvationLimit {
				logger.Log("WARNING", fmt.Sprintf("Tour denied capital - %s (%d since the last productive tour) - this is not a margin verdict, pausing %ds before re-planning", deniedCause, capitalDeniedStreak, int(tourTreasuryRetryBackoff.Seconds())), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
					"capital_denied_streak": capitalDeniedStreak, "backoff_seconds": int(tourTreasuryRetryBackoff.Seconds()),
				})
				if werr := h.legs.sleepInterruptibly(ctx, tourTreasuryRetryBackoff); werr != nil {
					return werr
				}
				continue
			}
			response.ExitReason = tourExitCapitalDenied
			response.ExitDetail = fmt.Sprintf("denied capital - %s (%d consecutive tours) after %d productive tour(s)", deniedCause, capitalDeniedStreak, response.ToursCompleted)
			logger.Log("INFO", "Tour stopping - "+response.ExitDetail, map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
				"capital_denied_streak": capitalDeniedStreak,
			})
			break
		}

		// Already earned but this tour made no progress (no plan, or a feasible plan that
		// flew zero trades — every leg degraded, re-plans exhausted). Bound how many in a
		// row a -1 loop tolerates so a transient miss is retried but a genuinely dead
		// ground is caught (mirrors the trade-route zero-visit starvation).
		noProgressStreak++
		starvationDetail := fmt.Sprintf("margins died (%d consecutive tours found no profitable plan) after %d productive tour(s)", noProgressStreak, response.ToursCompleted)
		if feasible {
			starvationDetail = fmt.Sprintf("%d consecutive tours flew zero trades after %d productive tour(s)", noProgressStreak, response.ToursCompleted)
		}
		if noProgressStreak < tourStarvationLimit {
			// Breathing out the streak assumes the ground may simply be between cycles. On a
			// system a stack of trade hulls is already working that assumption is wrong, so
			// skip straight to the rescue rather than re-price what the stack has drained. A
			// rescue that declines leaves the streak running, so an uncrowded ground — and a
			// crowded one with nowhere better to be — behaves exactly as before.
			if !cmd.MVTLoop {
				dispersed, derr := h.maybeDisperseFromCrowdedGround(ctx, cmd, response, &episode, netBought, tourBudget, continuous, feasible)
				if derr != nil {
					return derr
				}
				if dispersed {
					noProgressStreak = 0
				}
			}
			continue
		}

		// The ground just tapped out (3-strike confirmed). Counted HERE, before the
		// reposition attempt, so it measures the ground rich->tapped cadence whether or not a
		// reposition then rescues the run — distinct from tour_exit_total{reason=starvation},
		// which fires only when a tap-out becomes the final honest exit. A productive tour
		// resets the streak, so this counts once per margins-death episode.
		metrics.RecordTourMarginsDeath(cmd.PlayerID)

		// Scoped to CONTINUOUS (-1) runs — a finite/one-shot run already fail-opened above
		// on iteration-1 infeasibility and never reaches here with no plan.
		if continuous {
			rescued, rerr := h.rescueStarvedGround(ctx, cmd, response, &episode, netBought, tourBudget, launchedLaden)
			if rerr != nil {
				return rerr
			}
			if rescued {
				noProgressStreak = 0
				continue
			}

			// LAST RUNG BEFORE RELEASE (sp-b9alf). Every value-seeking rescue has declined, so
			// this run is about to release the hull — and a hold this system will not bid for is
			// stranded the moment it does, vetoing the completion and costing a container failure
			// plus a fast-fail park before the relaunch tries the same ground again. Descend the
			// sp-58zaj disposal ladder instead: sell here, else reach one system that bids.
			//
			// A hull that got anything out of it keeps TOURING — an ordinary hull that just
			// cleared its hold is ready to work, and after a reach it stands where the markets
			// are — so it is treated exactly as the rescues above. Bounded the same way: the
			// ladder's jumps are per-run and never refunded, and once the hold is clear it
			// declines on a ship read. A ladder that cannot clear the hold changes nothing and
			// the run exits below still holding.
			progressed, derr := h.disposeStrandedHold(ctx, cmd, response, &episode, netBought, &strandDisposalJumps)
			if derr != nil {
				return derr
			}
			if progressed {
				noProgressStreak = 0
				continue
			}
		}

		h.recordStarvationExit(cmd, response, episode, starvationDetail, reason, logger)
		break
	}
	if response.ExitReason == "" {
		response.ExitReason = tourExitIterations
	}

	// The honest-completion veto runs in the deferred epilogue, which every exit shares —
	// including the ones that return before this point.
	response.NetProfit = response.TotalRevenue - response.TotalSpent
	logger.Log("INFO", "Tour run complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted, "exit_reason": response.ExitReason,
		"legs_executed": response.LegsExecuted, "trades_executed": response.TradesExecuted, "replans": response.Replans,
		"repositions": response.Repositions, "resumed_legs": response.ResumedLegs,
		"spent": response.TotalSpent, "revenue": response.TotalRevenue, "net": response.NetProfit,
	})
	return nil
}

func (h *RunTourCoordinatorHandler) resolveModelArtifactPath(cmd *RunTourCoordinatorCommand) string {
	// Precedence: an explicit per-run path (tests) → the daemon-configured absolute path
	// (production, cwd-independent) → the repo-relative constant (pure-env fallback).
	if cmd.ModelArtifactPath != "" {
		return cmd.ModelArtifactPath
	}
	if h.modelArtifactPath != "" {
		return h.modelArtifactPath
	}
	return defaultModelArtifactPath
}

// resumeInFlightReposition returns the episode the restart inherited: an empty one when no
// jump was in flight.
func (h *RunTourCoordinatorHandler) resumeInFlightReposition(ctx context.Context, cmd *RunTourCoordinatorCommand, logger common.ContainerLogger) (repositionEpisode, error) {
	if !cmd.RepositionInProgress || cmd.RepositionTargetWaypoint == "" {
		return repositionEpisode{}, nil
	}
	// RULINGS #2 restart-resume: a continuous run re-adopted mid-jump (the reposition was
	// in flight when the daemon restarted) resumes toward the SAME destination through the
	// shared cooldown-riding travel machinery, then clears the persisted flag — so the
	// hull lands on the ground it was rotating to rather than re-planning at whatever
	// intermediate hop it was re-adopted on. It counts as the episode's spent reposition
	// so a fresh 3-strike at the destination exits honestly instead of hop-scotching
	// across the restart boundary.
	logger.Log("INFO", fmt.Sprintf("Reposition resume: re-adopted mid-jump toward %s (%s) after a restart - completing the jump before re-planning (RULINGS #2)", cmd.RepositionTargetSystem, cmd.RepositionTargetWaypoint), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "target_system": cmd.RepositionTargetSystem, "target_waypoint": cmd.RepositionTargetWaypoint,
	})
	// The resume rides the SAME stored-adjacency bounded resolver as the fresh jump
	// (resolveRepositionJumpBound), so a restart mid-jump toward an unreadable-gate ground
	// completes over the persisted topology instead of re-hitting the strict Path fail-close.
	if rerr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, cmd.RepositionTargetWaypoint, cmd.PlayerID, resolveRepositionJumpBound(cmd.RepositionJumpBound)); rerr != nil {
		return repositionEpisode{}, rerr // resumable — the persisted in-progress flag stays set so a re-restart retries the resume
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	// rescues 1 so the MVT loop's cap still binds across the restart: the flight just resumed
	// IS the episode's first rescue, not a free one.
	return repositionEpisode{repositioned: true, rescues: 1, toSystem: cmd.RepositionTargetSystem}, nil
}

// afterProductiveTour returns the relocation-offer deadline the loop must honour before it
// plans here again. Continuous runs only: a finite run flies exactly the tours it was asked for.
func (h *RunTourCoordinatorHandler) afterProductiveTour(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, netBought map[string]int, budget tourPlanBudget) (time.Time, error) {
	if !cmd.MVTLoop {
		h.mvtShadow(ctx, cmd)
	}
	if cmd.MVTLoop {
		return time.Time{}, h.mvtAfterTour(ctx, cmd, response, netBought, budget)
	}
	// Rate-floor early-reposition (DEFAULT-OFF): a hull that just flew a
	// PRODUCTIVE-but-mediocre tour (well below the fleet-median realized rate) never
	// margin-dies, so the margins-death reposition never rescues it. When armed, evaluate a
	// relocation to a meaningfully better reachable ground before touring here again. A
	// non-nil error is a resumable travel failure (persisted in-flight destination resumes
	// on restart); every stay path returns nil and simply keeps touring.
	if cmd.RepositionRateFloorEnabled {
		if rerr := h.maybeRepositionRateFloor(ctx, cmd, response, netBought, budget); rerr != nil {
			return time.Time{}, rerr
		}
	}
	// FIRST REFUSAL (sp-e8d92): offer this hull to the relocator before touring here again — but
	// only when it SHARES its system, because a hull alone in its system is already spreading and
	// stalling it would cost a window of earning for nothing. Fail-open throughout — no persister,
	// an unreadable fleet, or a failed write all yield no offer and today's behaviour.
	if ship, serr := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); serr == nil && ship != nil && ship.CurrentLocation() != nil {
		return h.maybeOfferForRelocation(ctx, cmd, ship.CurrentLocation().SystemSymbol), nil
	}
	return time.Time{}, nil
}

// launchedStuckLaden reports whether the hull came up already stuck-laden — the restart signature,
// since a hull only becomes laden mid-run by buying. An unreadable hull keeps the established order.
func (h *RunTourCoordinatorHandler) launchedStuckLaden(ctx context.Context, cmd *RunTourCoordinatorCommand) bool {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false
	}
	return isLadenForOffload(ship.CargoUnits(), ship.CargoCapacity())
}

// rescueStarvedGround is the three-tier ladder a margins-dead hull descends: rotate to a
// fresh ground, else offload held cargo at a reachable sink, else distress-sell where it sits.
//
// launchedLaden PROMOTES the offload rung: the rotation ranks grounds on fresh profit priced
// against a CLEARED hold (planAtCandidate), which a hull arriving still full cannot earn. It
// still falls through to rotation when no reachable sink will take the load.
func (h *RunTourCoordinatorHandler) rescueStarvedGround(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, episode *repositionEpisode, netBought map[string]int, budget tourPlanBudget, launchedLaden bool) (bool, error) {
	if launchedLaden {
		offloaded, oerr := h.maybeOffloadHeldCargo(ctx, cmd, response, episode)
		if oerr != nil {
			return false, oerr
		}
		if offloaded {
			return true, nil
		}
	}
	// Margins confirmed dead. Before exiting, try to ROTATE the hull to a fresh
	// renewable ground: rank jump-reachable systems by expected tour margin, jump to
	// the best one that clears the reposition floor, and let the loop re-plan there.
	// This also fires at ToursCompleted==0, so a recovered continuous engine that
	// re-entered with a lost productive count and 3-struck on iteration-1 infeasibility
	// rotates off the drained ground instead of dying on it.
	if cmd.MVTLoop {
		return h.mvtClaimAndTravel(ctx, cmd, response, episode, netBought, mvtReasonEmpty, budget)
	}
	repositioned, rerr := h.maybeReposition(ctx, cmd, response, episode, netBought, budget)
	if rerr != nil {
		return false, rerr
	}
	if !repositioned && !launchedLaden {
		// sp-2v69u: fresh arb found nothing worth the jump. A LADEN hull is not
		// actually stuck — it may still be worth jumping toward the best reachable
		// sink for the cargo it is already holding (margin-exempt cash recovery).
		// Fallback only: never engages while maybeReposition itself finds a plan.
		// Skipped under launchedLaden: the promoted rung above already declined.
		offloaded, oerr := h.maybeOffloadHeldCargo(ctx, cmd, response, episode)
		if oerr != nil {
			return false, oerr
		}
		repositioned = offloaded
	}
	if !repositioned {
		// sp-2v69u TERTIARY: neither a fresh-arb jump nor a held-cargo offload could
		// rescue this laden hull — there is no profitable fresh tour anywhere reachable
		// AND no reachable OTHER-system sink for its load. LAST RESORT: sell the held
		// cargo at the best AVAILABLE bid in the hull's CURRENT system (a ground the
		// offload never evaluates), even below the profit floor — the cargo is sunk cost,
		// so recovering partial capital and freeing the hull beats churning full forever.
		// Sell-side cash recovery only (RULINGS #4 buy guards untouched); shares the same
		// kill-switch + one-action-per-episode budget, so it fires at most once per stuck
		// episode. Fallback only: never engages while a fresh plan or an offload sink exists.
		liquidated, lerr := h.maybeDistressLiquidate(ctx, cmd, response, episode, netBought)
		if lerr != nil {
			return false, lerr
		}
		repositioned = liquidated
	}
	return repositioned, nil
}

func (h *RunTourCoordinatorHandler) recordStarvationExit(cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, episode repositionEpisode, starvationDetail, reason string, logger common.ContainerLogger) {
	// No ground was worth the jump (or reposition is off/already spent this episode) —
	// exit HONEST (the container completes). The detail NAMES BOTH systems when a
	// reposition was already spent this episode (RULINGS: name origin and destination).
	response.ExitReason = tourExitStarvation
	response.ExitDetail = starvationExitDetail(episode, starvationDetail)
	// Append the LAST tour's concrete reason (e.g. a solver "reserve_exceeds_budget"
	// verdict) to the MESSAGE TEXT, not just metadata — ContainerRunner.Log only
	// prints "message" to stdout, so a reason left solely in the metadata map is
	// invisible to `container logs`. Without this, a starved budget and genuine
	// market death both read as the same generic "margins died" line.
	stopMsg := "Continuous tour stopping - " + response.ExitDetail
	if reason != "" {
		stopMsg += " (last: " + reason + ")"
	}
	logger.Log("INFO", stopMsg, map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
		"repositions": response.Repositions, "reason": reason,
	})
}

// runOneTour plans and flies ONE tour from the hull's CURRENT position and cargo,
// accumulating economics into response and cargo bought into netBought (cumulative
// across the run). It returns feasible=false with a fail-open reason when the planner
// found no profitable tour (the caller decides fail-open vs margin-death), and a
// non-nil error only on an operational failure the runner should retry (a retry
// re-plans from current position/cargo — cargo-aware, never a blind re-buy). This is
// the per-tour body the continuous loop repeats; the original one-shot run is exactly
// one call of it.
func (h *RunTourCoordinatorHandler) runOneTour(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	budget tourPlanBudget,
	replanLimit int,
) (bool, string, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, "", err
	}

	// Plan a depth-netted tour AND conditionally reserve its tranches all-or-nothing. A
	// reservation breach (another container claimed a sink between the netting snapshot
	// and the reserve) is a normal re-plan, NOT a failure — planAndReserve retries
	// against fresh ledger state, and only a persistent contention exits infeasible.
	plan, shadowSinks, reason, feasible, err := h.planAndReserve(ctx, cmd, ship, budget)
	if err != nil {
		return false, "", err
	}
	if !feasible {
		return false, reason, nil
	}
	// Publish the adopted tour plan to the read-only flow feed (fire-and-forget; a
	// missed publish never touches the trade path — RULINGS #4).
	flowfeed.Publish(buildTourFlow(cmd, plan, -1, time.Time{}, time.Time{}, shipCargoItems(ship), time.Now().UTC()))
	response.LegsPlanned += len(plan.Legs)
	// Honest projection split: projected profit is the TOTAL that ranked this tour;
	// fresh cash profit, held-cargo liquidation revenue, and synthetic haul-to-storage
	// DEPOSIT value are reported apart so a laden-hull or pre-positioning plan's margin
	// is not read as pure fresh-trade profit. Fresh cash = total - liquidation -
	// deposit_value (liquidation has no acquisition cost; a deposit books no cash — its
	// value is future contract savings, not revenue).
	freshProfit := plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
	logger.Log("INFO", fmt.Sprintf("Tour planned: %d legs, projected profit %d (fresh %d, liquidation %d, deposit %d) (model %s)", len(plan.Legs), plan.ProjectedProfit, freshProfit, plan.HeldLiquidation, plan.DepositValue, budget.modelVersion), map[string]interface{}{
		"legs": len(plan.Legs), "projected_profit": plan.ProjectedProfit,
		"projected_fresh_profit": freshProfit, "projected_held_liquidation": plan.HeldLiquidation,
		"projected_deposit_value": plan.DepositValue,
		"cph":                     plan.ProjectedCreditsPerHour, "model": budget.modelVersion,
	})
	// Pair every accepted plan's PROJECTED rate with a REALIZED rate at the tour's
	// honest completion, so ranking quality is measurable (a systematic
	// projected≫realized gap means the estimator flatters plans). Projected = the
	// solver's own cph, observed ONCE per tour for the plan that won selection
	// (intra-tour replans are recovery, not selection — they emit nothing, keeping
	// the projected/realized samples paired 1:1). Realized is observed at the
	// success return below: cash profit booked this tour over its actual wall-clock,
	// covering execution AND any replans. Error exits observe nothing — the runner
	// re-adopts and re-runs the tour, and a truncated observation would double-count
	// one logical tour (the same discipline as the exit-reason metrics).
	metrics.ObserveTourPlanRate(cmd.PlayerID, "projected", plan.ProjectedCreditsPerHour)
	acceptedAt := h.clock.Now()
	spentBefore, revenueBefore := response.TotalSpent, response.TotalRevenue

	// Execute plan legs; on degradation, re-plan from current position/cargo (bounded
	// by replanLimit PER TOUR).
	replansLeft := replanLimit
	var cumulativeSpend int64
	run := tourPlanRun{
		cmd: cmd, response: response, netBought: netBought,
		cumulativeSpend: &cumulativeSpend, maxSpend: budget.maxSpend, reserve: budget.reserve,
		sellFloorSpent: map[string]bool{}, // one refusal per good, spanning this tour's re-plans
		acapTranches:   cmd.aCapTranches(),
	}
	for {
		degraded, execErr := h.executePlan(ctx, run, plan, shadowSinks)
		if execErr != nil {
			return false, "", execErr
		}
		if !degraded {
			break
		}
		if replansLeft <= 0 {
			logger.Log("INFO", "Tour re-plan budget exhausted - stopping (any unsold tour cargo will report as stranded)", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
			})
			break
		}
		replansLeft--
		response.Replans++
		ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return false, "", err
		}
		replanSpend := remainingSpend(budget.maxSpend, cumulativeSpend)
		// Re-plan releases this container's prior PLANNED rows and reserves the new plan
		// fresh (planAndReserve), so the replacement plan never double-counts the old
		// one's holds and converted recovery shadows persist.
		var replanFeasible bool
		var replanReason string
		plan, shadowSinks, replanReason, replanFeasible, err = h.planAndReserve(ctx, cmd, ship, budget.withMaxSpend(replanSpend))
		if err != nil {
			return false, "", err
		}
		if !replanFeasible {
			// NAME the cause in the message text, not just metadata — ContainerRunner.Log
			// (container_runner.go) only prints "message" to stdout, so a reason left in the
			// metadata map alone is invisible to `container logs`.
			logger.Log("INFO", "Re-plan produced no feasible tour - stopping: "+replanReason, map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "reason": replanReason,
			})
			break
		}
	}
	// Cash-true audit: the phase="realized" observation is ALREADY cash-true
	// and needed NO change. The profit here is BOOKED cash — revenue minus spend accumulated from the
	// actual buy/sell API responses (response.TotalRevenue/TotalSpent), which INCLUDE the look-back
	// manifest buys (run_tour_coordinator_lookback.go increments TotalSpent). The sp-rd21 bug dropped
	// TELEMETRY legs, never these ledger-backed accumulators, so this histogram was never inflated.
	if rate, ok := realizedRatePerHour(
		(response.TotalRevenue-revenueBefore)-(response.TotalSpent-spentBefore),
		h.clock.Now().Sub(acceptedAt).Seconds()); ok {
		metrics.ObserveTourPlanRate(cmd.PlayerID, "realized", rate)
	}
	return true, "", nil
}

// realizedRatePerHour converts a tour's booked cash profit and elapsed wall-clock into
// credits/hour for the realized-rate observation. ok=false on a non-positive elapsed (a
// frozen test clock, or clock skew) — no honest rate exists there, and a divide-by-zero
// must never reach the histogram. Profit may be negative (a losing tour is a real
// observation; it lands in the histogram's le=0 bucket).
func realizedRatePerHour(profit int64, elapsedSeconds float64) (float64, bool) {
	if elapsedSeconds <= 0 {
		return 0, false
	}
	return float64(profit) / (elapsedSeconds / secondsPerHour), true
}

// executePlan flies the legs of a single plan. It returns degraded=true when a
// leg's live prices moved past tolerance (the caller re-plans), and a non-nil error
// only on an operational failure the runner should retry. An unroutable leg (gate
// graph drift) is treated as degradation, not a hard failure.
func (h *RunTourCoordinatorHandler) executePlan(
	ctx context.Context,
	run tourPlanRun,
	plan *routing.TourPlan,
	shadowSinks map[shadowSinkKey]bool,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)
	// Per-good sink dispositions for the firm-sink buy gate (sp-pcxju), folded once from
	// the plan: which goods have a market sink (gated on firm reserved depth) vs a
	// warehouse deposit (exempt — a guaranteed sink).
	run = run.forPlan(plan, shadowSinks)
	cmd := run.cmd

	// discharging marks the plan's economics void but its ROUTE still worth flying: a leg has
	// degraded, and a later leg can still sell cargo the hull is holding. Realising owned
	// inventory only ever adds credits, so "this plan is broken" is never a reason to carry it
	// home. Remaining legs run sells only — a fresh buy would re-open the exposure the broken
	// plan was supposed to close.
	discharging := false

	// retiring marks the hull as leaving service: the operator's mark landed on it, so every
	// remaining BUY tranche of this plan is dropped while its sells still fly. Kept apart from
	// discharging because the two mean different things about the PLAN — discharging says the
	// plan's economics are void and the caller must re-plan, while a retiring hull needs no
	// replacement plan at all; the boundary's disposal ladder takes it from here.
	retiring := false

	for legIdx, leg := range plan.Legs {
		ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return false, err
		}
		// Retirement binds BETWEEN legs, never mid-leg. A marked hull whose hold is empty right
		// here flies no further leg and the caller ends the run. A laden one flies on — that
		// flight is how a retirement drains instead of stranding — but DISCHARGING: the sells
		// this plan queued still run, its remaining BUY tranches are dropped. A hull leaving
		// service must never execute another purchase, including one already planned, or its
		// hold refills as fast as it drains.
		if ship.IsRetiring() {
			if ship.CargoUnits() == 0 {
				run.response.RetirementStandDown = true
				return false, nil
			}
			if !retiring {
				retiring = true
				run.response.RetirementDischarging = true
				logger.Log("INFO", fmt.Sprintf("Retirement discharge - %s was marked retiring mid-plan; the rest of this plan flies SELLS ONLY (%d leg(s) remaining, every queued buy dropped)", cmd.ShipSymbol, len(plan.Legs)-legIdx), map[string]interface{}{
					"action": "tour_retirement_discharge", "ship_symbol": cmd.ShipSymbol,
					"container_id": cmd.ContainerID, "leg": legIdx, "legs_remaining": len(plan.Legs) - legIdx,
				})
			}
		}
		// Record the leg the instant it is chosen and BEFORE the hull moves, so a daemon
		// restart mid-flight finds the sink this cargo was already going to (RULINGS #2). A
		// leg that discharges nothing the hull holds writes the CLEAR, so a leg already flown
		// can never be resumed a second time.
		h.persistTourLeg(ctx, cmd, legSellState(leg, h.tourShipState(ship).Cargo))
		// True leg-start stamp: travel() blocks through arrival, so this is the only
		// place the real departure time exists. It anchors the visualizer's
		// schedule-drift glyph (drift = arrivesAt − (departedAt + travelSeconds)).
		legDepartedAt := time.Now().UTC()
		// Captured before travel(); cleared after the leg's first buy consumes it
		// (below), since a leg may carry several.
		legDedupBracket := h.legs.startScanDedupBracket(ctx, cmd.ShipSymbol, cmd.PlayerID)
		// A fully guarded leg gets its live read from the guard moments after arrival, so
		// the arrival scan duplicates it. TRAVEL ctx only — the trade ctx is untouched.
		// The same verdict rides run into the leg's buys, whose cuts then size off the
		// guard ceiling rather than a cached ask no arrival scan refreshed.
		run.legScanDeferred = tourLegDefersArrivalScan(leg, discharging || retiring)
		travelCtx := ctx
		if run.legScanDeferred {
			travelCtx = shared.WithArrivalScanDeferred(ctx, leg.Waypoint)
		}
		ship, err = h.legs.travel(travelCtx, ship, leg.Waypoint, cmd.PlayerID)
		if err != nil {
			if errors.Is(err, gategraph.ErrUnroutable) {
				logger.Log("WARNING", fmt.Sprintf("Leg %d to %s unroutable (gate-graph drift) - degrading to re-plan: %v", legIdx, leg.Waypoint, err), map[string]interface{}{
					"leg": legIdx, "waypoint": leg.Waypoint, "error": err.Error(),
				})
				return true, nil
			}
			return false, fmt.Errorf("travel to leg %d (%s) failed: %w", legIdx, leg.Waypoint, err)
		}
		// Publish the just-flown leg to the read-only flow feed (RULINGS #4). travel()
		// has blocked through arrival, so arrivesAt is the ACTUAL arrival: nav's
		// arrival time when it survived, else publish-now (we arrived moments ago —
		// the arrival path clears ship.ArrivalTime(), and a zero arrivesAt would
		// zero the drift glyph). Position truth stays with the visualizer nav join.
		publishedAt := time.Now().UTC()
		arrivesAt := publishedAt
		if at := ship.ArrivalTime(); at != nil && !at.IsZero() {
			arrivesAt = *at
		}
		flowfeed.Publish(buildTourFlow(cmd, plan, legIdx, legDepartedAt, arrivesAt, shipCargoItems(ship), publishedAt))
		legDedupBracket = h.legs.confirmScanDedupArrival(legDedupBracket)

		legDegraded := false
		// Accumulate realized units per (good, side) at THIS leg, so each pool's recovery
		// shadow is converted ONCE with the full move (across its price-tiered
		// tranches), not once per tranche. Nil when no ledger is wired.
		legFlows := h.newLegFlows()
		// This leg's own sink depth, which arms the sell floor past the declared cap. Kept
		// apart from legFlows so an unwired absorption ledger cannot disarm a money guard.
		run.legSold = map[string]int{}
		// Sells before buys (errata): a leg that fills the hold both ways must free
		// space before spending it, and sell tranches are ordered price-ascending.
		for _, trade := range legTradesToFly(leg.Trades, discharging || retiring) {
			executed, terr := h.executeTrade(ctx, run, leg, legIdx, trade, legFlows, legDedupBracket)
			if trade.IsBuy {
				legDedupBracket = scanDedupBracket{} // exhausted once offered to one buy
			}
			if terr != nil {
				return false, terr
			}
			if !executed {
				legDegraded = true // a skipped trade degrades the leg but a still-good sibling trade may proceed
			}
		}
		// Convert this leg's pools to recovery shadows (per pool as legs complete, design
		// §2) — even on a degraded leg, so the tranches that DID trade shadow their move.
		h.convertLegShadows(ctx, cmd, leg.Waypoint, legFlows)
		run.response.LegsExecuted++
		// A hull leaving service flies no leg that cannot discharge its hold: its buys are
		// dropped, so a remaining buy-only leg is a wasted hop on a clock that is running out.
		// Ending the plan here is NOT a degradation — it needs no replacement plan, because the
		// boundary's disposal ladder takes the hull from here.
		if retiring && !legDegraded && !discharging {
			if !h.legsCanDischargeHold(ctx, cmd, plan.Legs[legIdx+1:]) {
				return false, nil
			}
			continue
		}
		if !legDegraded && !discharging {
			continue
		}
		discharging = true
		// Re-asked after every discharge leg: the moment nothing ahead can take what is
		// still aboard, stop and let the caller re-plan from here.
		if !h.legsCanDischargeHold(ctx, cmd, plan.Legs[legIdx+1:]) {
			return true, nil
		}
	}
	return discharging, nil
}
