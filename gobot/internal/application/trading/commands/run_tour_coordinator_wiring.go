package commands

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// outOfHorizonSinkScanner reads the global best sell destination per good (across ALL
// systems), the seam the tour coordinator uses to SEE sinks its 1-gate-hop snapshot
// cannot. The concrete *persistence.MarketRepositoryGORM satisfies it; the daemon
// injects it via SetOutOfHorizonSinkScanner. Kept as a narrow local port (not a method
// on the wide market.MarketRepository interface) so no mock/test double outside this
// diagnostic is disturbed.
type outOfHorizonSinkScanner interface {
	BestSinksAcrossSystems(ctx context.Context, goods []string, playerID int, maxAge time.Duration, now time.Time) (map[string]market.GlobalSinkResult, error)
}

// NewRunTourCoordinatorHandler wires the tour coordinator with the same driven ports
// as the trade-route circuit (so buys/sells/navigation resolve to the daemon's exact
// command handlers) plus the market-model planner, waypoint repository (era-scoped
// coordinates), and telemetry repository. A nil clock defaults to RealClock.
func NewRunTourCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	waypointRepo system.WaypointRepository,
	telemetry trading.TourTelemetryRepository,
	planner routing.RoutingClient,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *RunTourCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTourCoordinatorHandler{
		legs:                       NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		marketRepo:                 marketRepo,
		waypointRepo:               waypointRepo,
		telemetry:                  telemetry,
		planner:                    planner,
		clock:                      clock,
		apiClient:                  apiClient,
		mediator:                   mediator,
		depositParked:              make(map[string]string),
		strandedStreak:             make(map[string]*strandedHullState),
		repositionTieSweep:         make(map[string]*repositionTieState),
		purchaseObligation:         make(map[string]map[string]int),
		obligationSeeded:           make(map[int]bool),
		rateFloorLastRelocation:    make(map[string]time.Time),
		pendingRelocationsBySystem: make(map[string]int),
		contendedHolderLogAt:       make(map[string]time.Time),
		recentSells:                make(map[string]map[marketGood]time.Time),
	}
}

// SetPrePositioning wires the optional haul-to-storage deposit subsystem: the shared
// storage coordinator (deposit protocol + warehouse space reads), the warehouse-op
// finder, the demand miner, the resolved config, and the capital-ceiling percent.
// Called from main.go AFTER the storage subsystem is constructed (the tour
// coordinator is wired earlier). Left unset, no deposit legs are ever offered or
// executed — the tour plans and flies pure arb.
func (h *RunTourCoordinatorHandler) SetPrePositioning(
	coordinator storage.StorageCoordinator,
	warehouses tradingsvc.WarehouseOperationFinder,
	miner tradingsvc.DepositDemandMiner,
	cfg tradingsvc.DepositCandidateConfig,
	capitalCeilingPct int,
) {
	h.storageCoordinator = coordinator
	h.warehouseFinder = warehouses
	h.demandMiner = miner
	h.prePositioning = cfg
	h.depositCeilingPct = capitalCeilingPct
}

// SetOutOfHorizonSinkScanner wires the global best-sink reader that backs the
// out-of-horizon lane diagnostic. The daemon injects the concrete market repo; left
// unset the diagnostic no-ops (RULINGS #4). Optional-port pattern, like the setters
// below.
func (h *RunTourCoordinatorHandler) SetOutOfHorizonSinkScanner(s outOfHorizonSinkScanner) {
	h.sinkScanner = s
}

// SetGateGraph wires the multi-jump gate-graph resolver into the delegated movement
// handler (so travel crosses multi-hop gaps and cross-gate tours fly). Mirrors the
// arb coordinator's injection.
func (h *RunTourCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetTreasuryReader wires the ledger-backed treasury reader (sp-muq66) into BOTH this
// coordinator's own money reads and the movement legs' working-capital guard, so the tour
// path reads treasury one way rather than two. Left unset, both keep the direct live read.
func (h *RunTourCoordinatorHandler) SetTreasuryReader(r TreasuryReader) {
	h.treasury = r
	h.legs.SetTreasuryReader(r)
}

// SetGateFeeReader injects the ledger-backed per-departure-gate fee table.
// Unset (every existing test) leaves gateFees nil, which plans byte-identically to today.
func (h *RunTourCoordinatorHandler) SetGateFeeReader(r GateFeeReader) {
	h.gateFees = r
}

// SetJumpTollReader injects the sample-backed per-gate-hop travel estimator (sp-3x143).
// Unset (every existing test) leaves jumpTolls nil, which plans byte-identically to today.
func (h *RunTourCoordinatorHandler) SetJumpTollReader(r JumpTollReader) {
	h.jumpTolls = r
}

// SetJumpTollRecorder forwards the measured-hop recorder into the MOVEMENT LEGS, which are
// what fly the crossings. The legs are this handler's OWN instance, not the daemon's
// separately wired circuit handler, so a setter that stopped here would leave the fleet's
// largest source of jumps unmeasured. Nil is a no-op. Mirrors SetTreasuryReader.
func (h *RunTourCoordinatorHandler) SetJumpTollRecorder(r trading.JumpTollRepository) {
	h.legs.SetJumpTollRecorder(r)
}

// SetChartGateOnArrival propagates the chart-on-gate-arrival knob to the movement
// legs, so this coordinator's cross-gate tour arrivals chart the gate they land on too.
// Mirrors the SetGateGraph delegation.
func (h *RunTourCoordinatorHandler) SetChartGateOnArrival(enabled bool) {
	h.legs.SetChartGateOnArrival(enabled)
}

// SetScanDedupAllowlist mirrors the SetGateGraph delegation. Left unset (nil), every
// leg's bracket stays disabled.
func (h *RunTourCoordinatorHandler) SetScanDedupAllowlist(a ScanDedupAllowlist) {
	h.legs.SetScanDedupAllowlist(a)
}

// SetScanPolicy wires the tour-scan load policy (recent-scan freshness gate +
// impact-sample rate, resolved from cfg.TradeImpact on restart). Stamped onto ctx at run
// start so the shared arrival + post-trade scans throttle the deliberate price-impact
// instrumentation. Left unset, the coordinator stamps no policy and every scan runs
// unsampled (deploy-safe). Mirrors the SetGateGraph optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetScanPolicy(policy shared.ScanPolicy) {
	h.scanPolicy = policy
	h.scanPolicySet = true
}

// SetRankerAgeCaps wires the activity-conditioned listing freshness table
// into the tour snapshot builder, the same optional-injection idiom as SetScanPolicy.
// The daemon injects cfg.Trading.RankerAgeCapMinutes.Resolved() so the tour path and
// the lane ranker drop stale rows against the SAME config-resolved caps; left unset
// the zero-value table still builds on the fitted armed defaults, so every existing
// test is unaffected.
func (h *RunTourCoordinatorHandler) SetRankerAgeCaps(caps trading.RankerAgeCaps) {
	h.rankerAgeCaps = caps
}

// SetCargoBlocklist injects the tour cargo good-blocklist from
// cfg.TradeFleet.CargoBlocklist at daemon boot — the same global-config → handler-setter
// injection SetPrePositioning/SetScanPolicy use. An empty/absent list leaves the handler's
// set nil, so planForState's filter is a no-op and the tour plans over the full good
// universe exactly as before (byte-identical). Arming the noise-goods filter is an
// explicit config edit (recommended value: FUEL, ALUMINUM, PLASTICS) + daemon restart.
func (h *RunTourCoordinatorHandler) SetCargoBlocklist(goods []string) {
	h.cargoBlocklist = stringSet(goods)
}

// SetConstructionCargoBlocklist injects the construction-conditional cargo blocklist at boot,
// the same mechanism SetCargoBlocklist uses. Empty/absent is byte-identical.
func (h *RunTourCoordinatorHandler) SetConstructionCargoBlocklist(goods []string) {
	h.constructionCargoBlocklist = stringSet(goods)
}

// SetSinkFreshness arms the sp-tgll8 item-2 "FRESH" clause on the firm-sink buy gate with
// the maximum age a downstream sink's cached market_data may carry at buy time. The daemon
// injects cfg.TradeFleet.ResolvedSinkFreshnessMaxAge() at boot — the same global-config →
// handler-setter idiom SetRankerAgeCaps/SetCargoBlocklist use, re-read on every restart. A
// non-positive age leaves the clause INERT (the gate behaves exactly as sp-pcxju), so every
// existing ledger-wired tour test that never calls this is byte-identical.
func (h *RunTourCoordinatorHandler) SetSinkFreshness(maxAge time.Duration) {
	h.sinkFreshnessMaxAge = maxAge
}

// SetCapitalWorkSensor wires the per-operation capital budget's hasWork sensor (sp-ftqgp). The
// daemon calls this UNCONDITIONALLY at boot — there is no config key, no default-off and no
// arming step between the sensor and a live budget. Leaving it unset is the test path only, and
// keeps the 25%-of-treasury dynamic cap as the sole cumulative bound.
func (h *RunTourCoordinatorHandler) SetCapitalWorkSensor(sensor common.CapitalWorkSensor) {
	h.workSensor = sensor
}

// SetModelArtifactPath injects the daemon-configured (absolute) market-model artifact
// path this coordinator reads at launch (resolved from cfg.Routing.ModelArtifactPath so
// it is cwd-independent). Left unset, the coordinator falls back to the repo-relative
// defaultModelArtifactPath. Mirrors the SetGateGraph optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetModelArtifactPath(path string) {
	h.modelArtifactPath = path
}

// RepositionEpisode is the durable slice of a margins-death reposition: the destination
// the hull is jumping to. It is persisted into the container config the instant the
// jump is committed and cleared (InProgress=false) once it lands, so a daemon restart
// mid-jump (RULINGS #2) resumes toward the SAME ground through the shared
// cooldown-riding travel machinery rather than re-planning at whatever intermediate hop
// it was re-adopted on.
type RepositionEpisode struct {
	InProgress     bool
	TargetSystem   string
	TargetWaypoint string
}

// RepositionStatePersister durably records an in-flight reposition's destination (keyed
// by container) so a restart-rebuilt run resumes the jump instead of re-planning at an
// intermediate position (RULINGS #2). The daemon backs this with the container config —
// the same map the recovery rebuild reads (buildTourCoordinatorCommand's reposition_*
// keys). Mirrors the arb ArbCostPersister contract: a returned error is advisory
// (persistence durability, never a spend/movement guard), so the caller logs and
// continues.
type RepositionStatePersister interface {
	PersistRepositionState(ctx context.Context, containerID string, playerID int, episode RepositionEpisode) error
}

// SetRepositionPersister wires the durable reposition-state store so a margins-death
// reposition survives a daemon restart mid-jump (RULINGS #2). Left unset (nil), a
// restart mid-jump re-plans at the hull's current position rather than resuming the
// reposition, exactly as if the feature carried no persistence (fail-open). Mirrors the
// arb SetCostPersister optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetRepositionPersister(p RepositionStatePersister) {
	h.repositionPersister = p
}

// SetPurchaseObligationReader wires the ledger-backed rebuild of the per-hull tour-purchase
// obligation, so a hull holding cargo it bought before a daemon restart is still known to owe
// it afterwards and cannot be released as a clean completion (RULINGS #2). Read once per
// player on the first run that needs it; nothing is written back, so the ledger stays the
// single record of what was bought. Left unset (every test that does not wire it), a restart
// starts the map empty exactly as before — the veto simply cannot see pre-restart purchases,
// which is the behaviour this reader replaces and never worse than it.
func (h *RunTourCoordinatorHandler) SetPurchaseObligationReader(r ledger.OutstandingPurchaseReader) {
	h.obligationReader = r
}

// SetEventRecorder wires the captain outbox the coordinator emits its error-loop event
// through. Optional-injection like the other setters: without it the streak monitor
// still tracks and logs, it just cannot escalate to a captain event (nil-safe, see
// health.RecordErrorLoop).
func (h *RunTourCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}
