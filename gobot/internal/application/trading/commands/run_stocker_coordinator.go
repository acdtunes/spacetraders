package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

const (
	// stockerStarvationLimit bounds how many CONSECUTIVE nothing-to-stock passes a
	// continuous run (--iterations -1) tolerates before it exits HONESTLY (the
	// container completes). Mirrors tourStarvationLimit: one empty pass can be a
	// transient (stale market read, momentary treasury dip); several in a row means
	// the warehouse is filled to target, or nothing eligible is affordable/fresh, and
	// there is nothing left to do. The captain relaunches when contracts drain it.
	stockerStarvationLimit = 3
	// stockerMinerTopN is how many ranked candidate rows the pick pulls from the Lane A
	// miner before applying its own eligibility/whitelist/space/ceiling filters.
	// Generous (mirrors the tour assembler's depositCandidateMinerTopN) so a blocklist
	// or allowlist can never starve the pick.
	stockerMinerTopN = 50
	// defaultStockerStandingTick is the STANDING park cadence between at-target re-checks
	// when TickSeconds is unset — the same 30-60s band as the construction drain's tick
	// (sp-382j). Overridable per-launch via TickSeconds (RULINGS #5).
	defaultStockerStandingTick = 30 * time.Second
)

// exitReason* enumerates why the stocker loop stopped (observability, mirrors the
// tour coordinator's ExitReason).
const (
	// stockerExitIterations: a finite --iterations budget was consumed.
	stockerExitIterations = "iterations_exhausted"
	// stockerExitStarvation: stockerStarvationLimit consecutive passes found nothing
	// to stock — the warehouse is at target / nothing eligible fits. An HONEST completion.
	stockerExitStarvation = "starvation"
	// stockerExitStanding: a STANDING run is parked at target, waiting for contracts to
	// drain the warehouse back below target. NON-terminal — the standing loop only exits
	// resumable on ctx-cancel (stop/shutdown/restart); this is set purely for observability
	// on the in-flight response so a reader can see the loop is alive-but-parked, not done.
	stockerExitStanding = "standing_parked"
)

// RunStockerCoordinatorCommand is a captain-directed, guarded STOCKER LOOP (sp-zdwg):
// a dedicated hull that fills a home warehouse the tours rationally won't (sp-dchv
// proved deposit legs lose to direct sells at every re-plan — correct economics; the
// stocker dedicates capacity instead of distorting tour objectives). Each round-trip it
// (1) reads the warehouse's supported stock list + current per-good inventory and the
// Lane A demand miner's per-good target/savings/cheapest-foreign-market, (2) picks the
// most-needed good (highest savings/u × units-short) that clears every money guard,
// (3) buys it at the cheapest foreign market (live-verified at the dock, fail-closed),
// (4) hauls home and deposits into the warehouse, and (5) repeats.
//
// Iterations makes it a CONTINUOUS engine (mirrors sp-m5kv): -1 = fill until nothing
// is left to stock (starvation), N>0 = N productive round-trips, 0 = the one-round-trip
// default. The coordinator owns this loop internally (CoordinatorOwnsIterations); the
// container runs Handle() once.
//
// RULINGS: every buy is live-verified and fail-closed against the capital ceiling (10%
// live treasury), the per-leg budget, and the working-capital reserve (#4); the hull is
// dedicated/claimed by the container runner (#7); the whole lifecycle is resumable — a
// hull that restarts laden deposits before buying more (#2); every knob is a flag/config
// (#5). #14 does not bind — the trade engine crosses systems by design.
type RunStockerCoordinatorCommand struct {
	ShipSymbol        string
	WarehouseWaypoint string // the home warehouse waypoint to deposit into (its system is the demand anchor)
	PlayerID          int
	ContainerID       string
	AgentSymbol       string
	// BudgetPerLeg caps a single buy leg's spend in credits; 0 → no explicit per-leg cap
	// (the capital ceiling + working-capital reserve still bound every buy).
	BudgetPerLeg int
	// WorkingCapitalReserve is the hard spend floor (the standing 50k, RULINGS #4/#5);
	// 0 → defaultWorkingCapitalReserve. Matches tour-run's per-run reserve knob.
	WorkingCapitalReserve int64
	// Iterations is the round-trip budget: -1 = CONTINUOUS (fill until nothing left to
	// stock), N>0 = exactly N productive round-trips, 0 = the one-round-trip default.
	Iterations int
	// MaxMarketAgeMinutes is the freshness discipline on the miner's cheapest-foreign
	// ask: a candidate whose foreign market's cached price is older than this is skipped
	// at pick (fail-closed — do not haul to a stale market). 0 → the standing 75-minute cap.
	MaxMarketAgeMinutes int
	// TargetPerGood overrides the per-good fill target; 0 → use the miner's measured
	// DemandUnits (never speculative, RULINGS #6). A positive value stocks every good to
	// this absolute level.
	TargetPerGood int
	// Standing turns the stocker into a STANDING refill coordinator (sp-k1ka): instead of
	// COMPLETING once the warehouse reaches target (the starvation exit a finite/continuous
	// run takes), it PARKS a tick and re-checks, re-staging a stock run automatically the
	// moment contracts drain the warehouse back below target — with NO manual relaunch. It
	// never completes while a fillable gap exists; it exits only on stop/shutdown (ctx
	// cancel, resumable) and is re-adopted STANDING on the next boot from its persisted
	// config (RULINGS #2). Every fail-closed money guard (capital ceiling, reserve floor,
	// freshness) is UNCHANGED — a guard-blocked pass simply PARKS (fail-closed) instead of
	// killing the loop (RULINGS #4). Standing implies continuous fill semantics.
	Standing bool
	// TickSeconds is the STANDING park cadence between at-target re-checks; 0 → the default
	// 30s (same band as the construction drain). Parametrized per RULINGS #5. Ignored when
	// Standing is false.
	TickSeconds int
	// RefillHysteresis is the minimum units-short a good must be before the stocker
	// re-stages it — target-hysteresis that stops a STANDING loop thrashing on a 1-unit gap
	// (RULINGS #5). 0 → the default 1 (re-stage on any shortfall, the historical behavior);
	// a positive value raises the re-stage threshold. Applied to the need-rank in pick().
	RefillHysteresis int
}

// RunStockerCoordinatorResponse reports the realised stocking economics and — via
// CompletionOutcome — whether the run honestly completed. A run that ends holding cargo
// it bought this run but never deposited is a stranded veto (the runner terminalizes
// FAILED via the honest-completion contract, mirroring the tour).
type RunStockerCoordinatorResponse struct {
	ShipSymbol        string
	WarehouseWaypoint string

	// RoundTripsCompleted counts productive round-trips (a pass that deposited >=1 unit).
	// UnitsDeposited is the run's total deposited units; TotalSpent the credits spent on
	// buys (capital booked at the buy — deposits book no revenue). GoodsStocked is the
	// distinct-good count. ExitReason (a stockerExit* constant) explains why the loop stopped.
	RoundTripsCompleted int
	UnitsDeposited      int
	TotalSpent          int64
	GoodsStocked        int
	ExitReason          string
	Completed           bool

	// CargoStranded is the honest-completion veto (mirrors sp-m5kv / sp-7yej invariant 2):
	// the run ended with the hull still laden with undeposited cargo (its one job is to
	// deposit). Threaded through CompletionOutcome (nil Go error) — the next run's first
	// move is deposit-first.
	CargoStranded       bool
	CargoStrandedReason string

	Error string
}

// CompletionOutcome implements common.CompletionReporter: a stranded stocker vetoes the
// runner's success=true (terminalized FAILED with the strand as its signature).
func (r *RunStockerCoordinatorResponse) CompletionOutcome() (bool, string) {
	if r.CargoStranded {
		return false, r.CargoStrandedReason
	}
	return true, ""
}

// Compile-time pin: the stocker response participates in the honest-completion contract.
var _ common.CompletionReporter = (*RunStockerCoordinatorResponse)(nil)

// RunStockerCoordinatorHandler runs the dedicated warehouse-filling loop. It composes
// the proven RunTradeRouteCoordinatorHandler primitives (travel — multi-jump/jump-safe,
// dock, purchaseWithCeiling — the sp-9mkf live-ask verify, spendFloorBreached, loadShip)
// rather than re-implementing them, so it inherits every fix those legs carry, and adds
// the need-ranked pick, the capital ceiling, the warehouse deposit protocol, and the
// stranded-cargo veto.
type RunStockerCoordinatorHandler struct {
	legs               *RunTradeRouteCoordinatorHandler
	mediator           common.Mediator
	marketRepo         market.MarketRepository
	apiClient          domainPorts.APIClient
	clock              shared.Clock
	storageCoordinator storage.StorageCoordinator
	warehouseFinder    tradingsvc.WarehouseOperationFinder
	demandMiner        tradingsvc.DepositDemandMiner
	config             tradingsvc.DepositCandidateConfig
	ceilingPct         int
	// waypointRepo resolves source/warehouse waypoint COORDINATES for the sp-9274 distance-aware
	// residual buy-leg in the auto-cap knapsack. Cache-only (no API fetch-through), so the
	// per-pass re-solve costs no API spend; a nil repo (or an uncached waypoint) FAILS OPEN to the
	// coarse in/cross-system residual (RULINGS #1) — the pre-sp-9274 behavior.
	waypointRepo system.WaypointRepository

	// noReachableSource de-dups the sp-yuq9 "every ranked candidate is gate-unreachable"
	// verdict so a hull whose need-rank keeps landing on unreachable-only markets (a
	// scouted-but-unroutable market like X1-PB12 staying artificially "cheapest" forever)
	// logs ONCE per ship per distinct state, not once per pass — the same per-hull
	// state-change de-dup discipline as the tour coordinator's depositParked (sp-13tl) and
	// the ikx1 backoff. Keyed by ship symbol; the value is the last emitted
	// "<unreachable>/<total>" signature. Guarded by noReachableSourceMu because the handler
	// is a SHARED singleton dispatched concurrently across every stocker hull.
	noReachableSourceMu sync.Mutex
	noReachableSource   map[string]string

	// Warehouse auto-cap optimizer (sp-5n7v). capParams are the analyst-owned tunables
	// (RULINGS #5), injected by the daemon via SetWarehouseCapParams (zero-value defaults
	// otherwise). capState carries per-warehouse EWMA + last-selected targets across passes
	// so the buffered good-set is STICKY (EWMA damps a one-tick spike; the held-good bonus is
	// the hysteresis dead-band). It is an in-memory optimization keyed by warehouse waypoint —
	// the targets are re-derivable from persisted contract history + live Σ hull capacity every
	// pass (RULINGS #2), so a daemon restart simply re-seeds the smoothing from the raw
	// observation. Guarded because the handler is a SHARED singleton dispatched per hull.
	capParams  tradingsvc.WarehouseCapParams
	capStateMu sync.Mutex
	capState   map[string]*warehouseCapState

	// Stocking instrumentation (sp-j6uz): a driven port that records each CONFIRMED
	// stocker→warehouse deposit as a structured economic event so downstream analysis can
	// measure depot stock-IN throughput/coverage (the stock-IN mirror of the kqxe withdrawal
	// stream). Optional — a nil recorder disables emission so existing tests and any caller
	// that has not wired it are byte-identical (additive, fail-open). The event's DepositedAt
	// is stamped with the handler's own clock (h.clock, guaranteed non-nil).
	stockingRecorder storage.StockingRecorder
}

// warehouseCapState is one warehouse's carried optimizer state between passes: the EWMA
// smoothed demand and the last-selected per-good targets (the incumbent set the hysteresis
// dead-band protects).
type warehouseCapState struct {
	smoothed map[string]float64
	targets  map[string]int
}

// NewRunStockerCoordinatorHandler wires the stocker with the same driven ports as the
// trade-route circuit (so buys/navigation resolve to the daemon's exact command
// handlers) plus the storage subsystem (deposit protocol + warehouse reads), the
// warehouse-op finder, and the Lane A demand miner. cfg carries the pre-positioning
// economics (min-recurrence/min-savings/allow-block, from cfg.Contract.PrePositioning);
// ceilingPct is the capital-ceiling percent of live treasury (default 10). A nil clock
// defaults to RealClock inside the delegated handler.
func NewRunStockerCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
	storageCoordinator storage.StorageCoordinator,
	warehouseFinder tradingsvc.WarehouseOperationFinder,
	demandMiner tradingsvc.DepositDemandMiner,
	cfg tradingsvc.DepositCandidateConfig,
	ceilingPct int,
	waypointRepo system.WaypointRepository,
) *RunStockerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunStockerCoordinatorHandler{
		legs:               NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		mediator:           mediator,
		marketRepo:         marketRepo,
		apiClient:          apiClient,
		clock:              clock,
		storageCoordinator: storageCoordinator,
		warehouseFinder:    warehouseFinder,
		demandMiner:        demandMiner,
		config:             cfg,
		ceilingPct:         ceilingPct,
		waypointRepo:       waypointRepo,
		noReachableSource:  make(map[string]string),
		capState:           make(map[string]*warehouseCapState),
	}
}

// SetWarehouseCapParams injects the analyst-owned auto-cap tunables (EWMA half-life,
// value-formula weights, hysteresis margin, cold-start threshold, cross-system residual —
// RULINGS #5). The daemon calls this at wiring time; unset, the optimizer uses its documented
// defaults. Mirrors SetGateGraph/SetEventSubscriber's optional-injection shape.
func (h *RunStockerCoordinatorHandler) SetWarehouseCapParams(p tradingsvc.WarehouseCapParams) {
	h.capParams = p
}

// resolveWarehouseCaps runs the auto-cap knapsack for the co-located warehouse group and
// returns the per-good target_units to enforce this pass, or nil to defer to the pre-existing
// per-good target (measured demand / TargetPerGood override). It STANDS ASIDE (nil) on a cold
// start — too little demand history to trust computed caps — so a thin-data run degrades to
// the proven behavior rather than churning on noise; the warehouse's own cold-start caps
// (populated at StartWarehouse) still bound it. Capacity is Σ REAL hull cargo_capacity across
// the group (never assume-80). EWMA + hysteresis state is carried per warehouse waypoint.
func (h *RunStockerCoordinatorHandler) resolveWarehouseCaps(ctx context.Context, homeSystem, waypoint string, group []*storage.StorageOperation, rows []persistence.DemandCandidate) map[string]int {
	capacity := tradingsvc.TotalCapacity(h.storageCoordinator, group)
	if capacity <= 0 {
		return nil
	}

	h.capStateMu.Lock()
	st := h.capState[waypoint]
	if st == nil {
		st = &warehouseCapState{}
	}
	prior, current := st.smoothed, st.targets
	h.capStateMu.Unlock()

	plan := tradingsvc.PlanWarehouseCaps(rows, capacity, homeSystem, waypoint, h.waypointCoords(ctx), prior, current, h.capParams)

	// Persist the advanced EWMA + selection for the next pass's stickiness. Only adopt the
	// computed targets as the incumbent set when it was a real (non-cold-start) solve.
	h.capStateMu.Lock()
	next := &warehouseCapState{smoothed: plan.Smoothed, targets: st.targets}
	if !plan.ColdStart {
		next.targets = plan.Targets
	}
	h.capState[waypoint] = next
	h.capStateMu.Unlock()

	if plan.ColdStart {
		return nil // defer to the pre-existing per-good target on thin history
	}
	return plan.Targets
}

// waypointCoords builds the sp-9274 coordinate lookup the auto-cap knapsack uses to turn the
// residual buy-leg into a real dist(warehouse, source). It is a CACHE-ONLY read (waypointRepo,
// never an API fetch-through) so the per-pass re-solve costs no API spend; a nil repo, an
// unresolvable waypoint, or a TTL-expired cache row returns ok=false and the optimizer FAILS OPEN
// to the coarse in/cross-system residual (RULINGS #1). A nil repo yields a nil lookup, degrading
// the solve to the pre-sp-9274 binary proxy byte-for-byte.
func (h *RunStockerCoordinatorHandler) waypointCoords(ctx context.Context) tradingsvc.WaypointCoordsLookup {
	if h.waypointRepo == nil {
		return nil
	}
	return func(waypoint string) (float64, float64, bool) {
		wp, err := h.waypointRepo.FindBySymbol(ctx, waypoint, shared.ExtractSystemSymbol(waypoint))
		if err != nil || wp == nil {
			return 0, 0, false
		}
		return wp.X, wp.Y, true
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver into the delegated movement
// handler (so travel crosses multi-hop gaps to reach a foreign market and haul home).
// Mirrors the arb/tour coordinator's injection.
func (h *RunStockerCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetEventSubscriber wires the ship-arrival event bus into the delegated movement
// handler so the resume path waits out a hull re-adopted mid-transit before moving
// (sp-8l3o) instead of 4214'ing and burning the restart budget. Mirrors arb/tour.
func (h *RunStockerCoordinatorHandler) SetEventSubscriber(subscriber navigation.ShipEventSubscriber) {
	h.legs.SetEventSubscriber(subscriber)
}

// SetStockingRecorder wires the stock-IN deposit-event recorder (sp-j6uz): on each CONFIRMED
// stocker→warehouse deposit the handler emits a structured storage.StockingEvent (good,
// units, warehouse, source market, hauler, player, timestamp) so downstream analysis can
// measure depot stock-IN throughput/coverage. A nil recorder is a no-op, so the daemon may
// forward the wiring unconditionally. Mirrors SetGateGraph/SetWarehouseCapParams's
// optional-injection shape; the event is stamped with the handler's own clock.
func (h *RunStockerCoordinatorHandler) SetStockingRecorder(recorder storage.StockingRecorder) {
	h.stockingRecorder = recorder
}

// Handle executes the stocker loop. A stranded-cargo veto returns a nil Go error (the
// veto is threaded through CompletionOutcome); an operational failure mid-run returns
// the underlying error so the runner can retry (a retry resumes deposit-first from the
// current hold — cargo-aware, never a blind re-buy).
func (h *RunStockerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunStockerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	response := &RunStockerCoordinatorResponse{ShipSymbol: cmd.ShipSymbol, WarehouseWaypoint: cmd.WarehouseWaypoint}
	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}
	if !response.CargoStranded {
		response.Completed = true
	}
	return response, nil
}

func (h *RunStockerCoordinatorHandler) execute(ctx context.Context, cmd *RunStockerCoordinatorCommand, response *RunStockerCoordinatorResponse) error {
	logger := common.LoggerFromContext(ctx)

	if cmd.WarehouseWaypoint == "" {
		return fmt.Errorf("stocker requires a warehouse waypoint")
	}
	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.demandMiner == nil {
		return fmt.Errorf("stocker subsystem unwired (storageCoordinator/warehouseFinder/demandMiner)")
	}

	// Stamp every ledger row this run's buy legs write with operation_type "stocker" so
	// pre-positioning spend lands under the stocker op, not the default trade baseline
	// (mirrors how the tour tags its writes at the boundary).
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "stocker"))

	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = int64(defaultWorkingCapitalReserve)
	}
	maxAge := maxListingAge
	if cmd.MaxMarketAgeMinutes > 0 {
		maxAge = time.Duration(cmd.MaxMarketAgeMinutes) * time.Minute
	}

	// Iteration budget: 0 → the one-round-trip default; -1 → continuous until nothing is
	// left to stock; N>0 → exactly N productive round-trips. STANDING (sp-k1ka) forces
	// continuous fill semantics AND replaces the starvation COMPLETION with a park-and-recheck
	// (never completes while a fillable gap can reopen).
	iterations := cmd.Iterations
	if iterations == 0 {
		iterations = 1
	}
	standing := cmd.Standing
	continuous := standing || iterations < 0
	tick := h.standingTick(cmd)

	depositedGoods := map[string]bool{}

	noProgressStreak := 0
	for continuous || response.RoundTripsCompleted < iterations {
		// A stop/shutdown cancels ctx. Exit RESUMABLE at the round-trip boundary by
		// returning the ctx error, which the runner routes through its ctx.Err() path
		// (re-adopted at next boot) — never let a cancel be misread as starvation and
		// COMPLETE a -1/standing container (the sp-ovkn trap).
		if err := ctx.Err(); err != nil {
			return err
		}

		productive, terr := h.runOneRoundTrip(ctx, cmd, response, depositedGoods, reserve, maxAge)
		if terr != nil {
			if standing {
				// A STANDING refill is self-sustaining (RULINGS #2, mirroring the construction
				// drain that swallows a failed tick): a transient nav/dock/market failure must
				// not terminalize the loop. Log, PARK, re-tick — the next tick resumes
				// deposit-first from the hull's live cargo, so no bought cargo is lost.
				logger.Log("WARNING", fmt.Sprintf("Stocker (standing): round-trip failed - parking %s then retrying: %v", tick, terr), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "warehouse": cmd.WarehouseWaypoint, "error": terr.Error(),
				})
				if perr := h.parkTick(ctx, tick); perr != nil {
					return perr
				}
				continue
			}
			return terr
		}
		if !productive {
			if standing {
				// STANDING NEVER completes on an empty pass: the warehouse is at target (or
				// nothing is affordable/fresh/reachable — every money guard already failed
				// CLOSED inside pick, RULINGS #4). PARK a tick and re-check; the moment
				// contracts drain the warehouse back below target the next tick re-stages a
				// stock run automatically, with NO manual relaunch (sp-k1ka).
				response.ExitReason = stockerExitStanding
				if perr := h.parkTick(ctx, tick); perr != nil {
					return perr
				}
				continue
			}
			noProgressStreak++
			if noProgressStreak >= stockerStarvationLimit {
				response.ExitReason = stockerExitStarvation
				logger.Log("INFO", fmt.Sprintf("Stocker stopping - nothing left to stock (%d consecutive empty passes) after %d round-trip(s)", noProgressStreak, response.RoundTripsCompleted), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "warehouse": cmd.WarehouseWaypoint, "round_trips": response.RoundTripsCompleted,
				})
				break
			}
			continue
		}
		noProgressStreak = 0
		response.RoundTripsCompleted++
	}
	if response.ExitReason == "" {
		response.ExitReason = stockerExitIterations
	}
	response.GoodsStocked = len(depositedGoods)

	// Honest-completion check (FINAL exit only): a hull ending laden with undeposited
	// cargo failed at its one job — a stranded veto terminalizes the container FAILED
	// (mirrors sp-m5kv invariant 2). The next run's first move is deposit-first.
	if reason, stranded := h.strandedReason(ctx, cmd); stranded {
		response.CargoStranded = true
		response.CargoStrandedReason = reason
		logger.Log("ERROR", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol})
		return nil
	}

	logger.Log("INFO", "Stocker run complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "warehouse": cmd.WarehouseWaypoint,
		"round_trips": response.RoundTripsCompleted, "units_deposited": response.UnitsDeposited,
		"goods_stocked": response.GoodsStocked, "spent": response.TotalSpent, "exit_reason": response.ExitReason,
	})
	return nil
}

// runOneRoundTrip runs ONE round-trip from the hull's CURRENT position: if the hull is
// laden from a prior interrupted round-trip it deposits first (resume-safe, RULINGS #2);
// otherwise it picks the most-needed good, buys it live-verified, hauls home, and
// deposits. Returns productive=true when >=1 unit was deposited, and a non-nil error only
// on an operational failure the runner should retry (a retry resumes deposit-first).
func (h *RunStockerCoordinatorHandler) runOneRoundTrip(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	response *RunStockerCoordinatorResponse,
	depositedGoods map[string]bool,
	reserve int64,
	maxAge time.Duration,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// The co-located warehouse group at the deposit waypoint is required to stock
	// (sp-5q2c: one OR MORE running warehouses whose capacity sums). None
	// running/never-running → an empty pass (the starvation streak exits honestly
	// after K of these).
	group := h.warehousesAt(ctx, cmd.PlayerID, cmd.WarehouseWaypoint)
	if len(group) == 0 {
		logger.Log("WARNING", fmt.Sprintf("Stocker: no running warehouse at %s - nothing to stock this pass", cmd.WarehouseWaypoint), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint,
		})
		return false, nil
	}

	// Resume-safe first move (RULINGS #2 / stranded-veto): a hull laden from a prior
	// interrupted round-trip deposits before buying more (never a blind re-buy — the
	// cargo is physically aboard, so the honest next move is to deliver it).
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	if heldUnits(ship) > 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: hull %s laden on start - depositing held cargo before buying (resume-safe)", cmd.ShipSymbol), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint,
		})
		// Resume deposit: the aboard cargo was bought in a PRIOR run, so its source market is
		// unknown here ("") — the stock-IN analog of a non-contract draw's empty contract id.
		deposited, derr := h.haulAndDeposit(ctx, cmd, group, response, depositedGoods, "")
		if derr != nil {
			return false, derr
		}
		return deposited > 0, nil
	}

	// PICK the most-needed good (need-ranked, every money guard fail-closed).
	pick, ok := h.pick(ctx, cmd, group, reserve, maxAge)
	if !ok {
		return false, nil // nothing to stock this pass — verdict already logged in pick
	}

	// BUY at the cheapest foreign market (multi-jump travel, live-verified, reserve-guarded).
	bought, berr := h.buy(ctx, cmd, pick, response, reserve)
	if berr != nil {
		return false, berr
	}
	if bought <= 0 {
		return false, nil // buy aborted (ceiling/floor/no-units) — empty pass
	}

	// HAUL HOME + DEPOSIT. The just-bought cargo's source is the picked foreign market,
	// threaded onto each stock-IN event (sp-j6uz) for source-provenance analysis.
	deposited, derr := h.haulAndDeposit(ctx, cmd, group, response, depositedGoods, pick.ForeignMarket)
	if derr != nil {
		return false, derr
	}
	return deposited > 0, nil
}

// stockerPick is one round-trip's chosen good: what to buy, where, and how much.
type stockerPick struct {
	Good           string
	ForeignMarket  string
	ForeignAsk     int
	HomeAsk        int
	SavingsPerUnit int
	UnitsShort     int
	Units          int // the haul size (capped by hold / space / ceiling / per-leg budget)
}

// pick chooses the most-needed good to stock: the stock-eligible miner candidate with
// the highest (savings/u × units-short) that the warehouse buffers, clears the
// min-savings floor, is FRESH (75-min discipline), and whose haul fits the tightest of
// hold space, warehouse free space, the capital ceiling, and the per-leg budget. Returns
// ok=false (with a single verdict line) when nothing survives — an honest empty pass.
// Every money guard fails CLOSED (RULINGS #4): an unreadable balance stocks nothing.
func (h *RunStockerCoordinatorHandler) pick(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	reserve int64,
	maxAge time.Duration,
) (stockerPick, bool) {
	logger := common.LoggerFromContext(ctx)

	// Capital ceiling (10% of live treasury, junior to the reserve). Unreadable balance →
	// stock nothing (fail closed, RULINGS #4).
	ceiling, known := h.capitalCeiling(ctx, reserve)
	if !known {
		logger.Log("WARNING", "Stocker: capital ceiling unreadable (live balance) - nothing to stock this pass (fail closed, RULINGS #4)", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
		})
		return stockerPick{}, false
	}
	if ceiling <= 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: capital ceiling is 0 (treasury at/below reserve %d) - nothing to stock this pass", reserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "reserve": reserve,
		})
		return stockerPick{}, false
	}

	// AGGREGATE warehouse free space across the co-located group (sp-5q2c: light-12 +
	// heavy-4B sum). Full — and an empty pass — only when EVERY member is full.
	freeSpace := tradingsvc.TotalFreeSpace(h.storageCoordinator, group)
	if freeSpace <= 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: warehouse group at %s full (0 aggregate free space across %d op(s)) - nothing to stock this pass", cmd.WarehouseWaypoint, len(group)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "group_size": len(group),
		})
		return stockerPick{}, false
	}

	// Hull hold capacity bounds the haul (the hull is empty here — laden hulls take the
	// resume-deposit path before pick).
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Stocker: could not load hull %s for pick: %v", cmd.ShipSymbol, err), map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "error": err.Error()})
		return stockerPick{}, false
	}
	hold := ship.AvailableCargoSpace()
	if hold <= 0 {
		return stockerPick{}, false
	}
	currentSystem := ship.CurrentLocation().SystemSymbol

	homeSystem := shared.ExtractSystemSymbol(cmd.WarehouseWaypoint)
	rows, err := h.demandMiner.Mine(ctx, homeSystem, cmd.PlayerID, nil, persistence.DemandMinerOptions{
		MinRecurrence: h.config.MinRecurrence, TopN: stockerMinerTopN, BuyLegSavingsPerUnit: h.config.BuyLegSavingsPerUnit,
	})
	if err != nil {
		logger.Log("WARNING", "Stocker: demand mining failed - nothing to stock this pass: "+err.Error(), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "home_system": homeSystem, "error": err.Error(),
		})
		return stockerPick{}, false
	}

	minSavings := h.config.MinSavingsPerUnit
	if minSavings <= 0 {
		minSavings = 1
	}
	allow := stringSet(h.config.Allowlist)
	block := stringSet(h.config.Blocklist)
	now := h.clock.Now()

	// Auto-cap knapsack (sp-5n7v): per-good target_units from live demand × residual-buy-leg
	// over Σ REAL hull capacity, re-solved every pass (RULINGS #2 re-derivable; "re-solved as
	// demand/fleet change"). A nil result means STAND ASIDE — cold start (thin history), an
	// explicit TargetPerGood override, or zero capacity — and the pre-existing per-good target
	// governs. When present, capTargets is authoritative: a good absent from it (e.g. a
	// central/hub-covered in-system good the optimizer refuses) gets target 0 and is skipped,
	// so no single good can crowd out the far/orphan goods the buffer exists to hold.
	var capTargets map[string]int
	if cmd.TargetPerGood <= 0 {
		capTargets = h.resolveWarehouseCaps(ctx, homeSystem, cmd.WarehouseWaypoint, group, rows)
	}

	var best stockerPick
	bestValue := 0
	eligible, afterFilters, unreachable := 0, 0, 0

	// Rows arrive stock-eligible-first, ranked by total projected savings; the stocker
	// re-ranks the survivors by (savings/u × units-short) — the most-needed-by-value good.
	for _, r := range rows {
		if !r.StockEligible {
			continue // known both asks AND savings > 0 (no speculative stocking, RULINGS #6)
		}
		if r.ProjectedSavingsPerUnit < minSavings || r.ForeignAsk <= 0 || r.HomeAsk <= 0 {
			continue
		}
		eligible++
		if len(allow) > 0 && !allow[r.Good] {
			continue
		}
		if block[r.Good] {
			continue
		}
		// Only stock goods the group actually BUFFERS: a good no co-located member
		// supports would strand (no contract worker could withdraw it). Fail closed.
		if !tradingsvc.AnySupportsGood(group, r.Good) {
			continue
		}

		target := r.DemandUnits
		if cmd.TargetPerGood > 0 {
			target = cmd.TargetPerGood
		} else if capTargets != nil {
			// The auto-cap knapsack has spoken: hold each good to its computed target_units
			// (0 => not buffered => skipped by the units-short guard below).
			target = capTargets[r.Good]
		}
		// Net the target against AGGREGATE on-hand across the group (sp-5q2c) so a
		// sibling warehouse's stock is never invisible — the stocker stops buying once
		// the COMBINED inventory reaches target, not once any single hull does.
		unitsShort := target - tradingsvc.TotalCargoAvailable(h.storageCoordinator, group, r.Good)
		// Target-hysteresis (RULINGS #5): re-stage only once the shortfall reaches the
		// hysteresis floor, so a STANDING loop does not thrash on a 1-unit gap. Default 1 →
		// re-stage on any shortfall (unitsShort <= 0 excluded), the historical behavior.
		refillFloor := cmd.RefillHysteresis
		if refillFloor < 1 {
			refillFloor = 1
		}
		if unitsShort < refillFloor {
			continue // at/over target, or within the hysteresis band — nothing to re-stage
		}

		// Reachability (sp-yuq9): an unreachable-cheapest foreign market must never win
		// the need-rank — feeding it to buy()'s travel() unchecked crash-loops the hull
		// identically on every relaunch (TORWIND-38 repeatedly picked X1-PB12 from
		// X1-KA42, gate-unreachable within 5 jumps, while scout posts kept its ask
		// artificially "cheapest" forever). Consults the SAME gate graph and jump bound
		// travel()'s own jumpPath() uses — never a second reachability notion.
		if !h.foreignMarketReachable(ctx, currentSystem, r.ForeignMarket, cmd.PlayerID) {
			unreachable++
			continue
		}

		// Freshness (75-min discipline): a stale/gone foreign price is not a trustworthy
		// pick — skip rather than haul to it (the buy still live-verifies at the dock).
		if !h.foreignMarketFresh(ctx, r.ForeignMarket, r.Good, cmd.PlayerID, now, maxAge) {
			continue
		}
		afterFilters++

		// Cap the haul at the tightest of units-short, hold space, warehouse free space,
		// the capital ceiling (credits / ask), and the per-leg budget (credits / ask).
		units := min(unitsShort, hold)
		units = min(units, freeSpace)
		units = min(units, int(ceiling/int64(r.ForeignAsk)))
		if cmd.BudgetPerLeg > 0 {
			units = min(units, cmd.BudgetPerLeg/r.ForeignAsk)
		}
		if units <= 0 {
			continue // ceiling/budget/space exhausted for this good
		}

		value := r.ProjectedSavingsPerUnit * unitsShort
		if value > bestValue {
			bestValue = value
			best = stockerPick{
				Good:           r.Good,
				ForeignMarket:  r.ForeignMarket,
				ForeignAsk:     r.ForeignAsk,
				HomeAsk:        r.HomeAsk,
				SavingsPerUnit: r.ProjectedSavingsPerUnit,
				UnitsShort:     unitsShort,
				Units:          units,
			}
		}
	}

	if bestValue <= 0 {
		// unreachable>0 with nothing else survivable is the sp-yuq9 "no reachable source"
		// verdict — de-duped per hull (state-change only) so a hull parked on an
		// unreachable-only need-rank logs ONCE, not once per pass. Any OTHER empty-pass
		// cause (at target / unaffordable / stale / unsupported, unreachable==0) clears
		// the remembered state so a LATER genuine no-reachable-source park re-logs fresh.
		if unreachable > 0 {
			h.recordNoReachableSource(ctx, cmd.ShipSymbol, unreachable, len(rows))
		} else {
			h.clearNoReachableSource(cmd.ShipSymbol)
		}
		logger.Log("INFO", fmt.Sprintf(
			"Stocker verdict: nothing to stock — [warehouse=%s(group %d) free=%d ceiling=%d funnel: miner_rows=%d eligible=%d unreachable=%d after_filters=%d] (at target / unreachable / unaffordable / stale / unsupported)",
			cmd.WarehouseWaypoint, len(group), freeSpace, ceiling, len(rows), eligible, unreachable, afterFilters), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "group_size": len(group), "free_space": freeSpace,
			"ceiling": ceiling, "miner_rows": len(rows), "eligible": eligible, "unreachable": unreachable, "after_filters": afterFilters,
		})
		return stockerPick{}, false
	}

	// A productive pick: forget any prior no-reachable-source park (a later recurrence
	// re-logs as a state change, mirrors the tour's clearDepositParked on the
	// capital-available path).
	h.clearNoReachableSource(cmd.ShipSymbol)
	logger.Log("INFO", fmt.Sprintf(
		"stocking %s: %du short, buy@%s %d/u (savings %d/u), value %d, hauling %du (ceiling %d, free %d)",
		best.Good, best.UnitsShort, best.ForeignMarket, best.ForeignAsk, best.SavingsPerUnit, bestValue, best.Units, ceiling, freeSpace), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": best.Good, "units_short": best.UnitsShort,
		"foreign_market": best.ForeignMarket, "foreign_ask": best.ForeignAsk, "savings_per_unit": best.SavingsPerUnit,
		"value": bestValue, "haul_units": best.Units, "ceiling": ceiling, "free_space": freeSpace,
	})
	return best, true
}

// buy travels to the picked foreign market (multi-jump, jump-safe), docks, re-checks the
// working-capital floor against the live hold, and buys under the sp-9mkf per-tranche
// live-ask ceiling (fail-closed): the ask is re-verified at the dock and the remainder
// aborted if it ladders past the miner's foreign ask + tolerance, BEFORE overspending.
// Returns the units bought (0 on any guarded skip), and a non-nil error only on an
// operational failure.
func (h *RunStockerCoordinatorHandler) buy(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	pick stockerPick,
	response *RunStockerCoordinatorResponse,
	reserve int64,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	ship, err = h.legs.travel(ctx, ship, pick.ForeignMarket, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("travel to market %s failed: %w", pick.ForeignMarket, err)
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return 0, fmt.Errorf("dock at market %s failed: %w", pick.ForeignMarket, err)
	}

	// Re-size against the live hold post-dock.
	ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	units := pick.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if units <= 0 {
		return 0, nil
	}

	// Working-capital spend floor (RULINGS #4), reusing the delegated guard: never drop
	// live treasury below the reserve. Fails closed on any live-read failure.
	projectedCost := units * pick.ForeignAsk
	if h.legs.spendFloorBreached(ctx, projectedCost, int(reserve), &RunTradeRouteCoordinatorResponse{}) {
		logger.Log("WARNING", fmt.Sprintf("Stocker: buy of %d %s @ %d would breach working-capital floor %d - skipping", units, pick.Good, pick.ForeignAsk, reserve), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "units": units, "ask": pick.ForeignAsk, "reserve": reserve,
		})
		return 0, nil
	}

	// sp-9mkf live-verify: arm the per-tranche ceiling at the miner's foreign ask plus
	// tolerance, so a laddered/stale live ask aborts the remainder fail-closed before spend.
	maxAskPerUnit := pick.ForeignAsk + pick.ForeignAsk*tourPriceTolerancePct/100
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, pick.Good, units, cmd.PlayerID, maxAskPerUnit)
	if err != nil {
		return 0, fmt.Errorf("purchase of %d %s at %s failed: %w", units, pick.Good, pick.ForeignMarket, err)
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("WARNING", fmt.Sprintf("Stocker: buy ceiling aborted %s at %s (live ask %d > ceiling %d) - skipping this pass",
			pick.Good, pick.ForeignMarket, buyResp.CeilingObservedAsk, maxAskPerUnit), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
		})
		return 0, nil
	}
	response.TotalSpent += int64(buyResp.TotalCost)
	logger.Log("INFO", fmt.Sprintf("Stocker: bought %d %s at %s (cost %d, live-verified)", buyResp.UnitsAdded, pick.Good, pick.ForeignMarket, buyResp.TotalCost), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": pick.Good, "units": buyResp.UnitsAdded, "cost": buyResp.TotalCost, "market": pick.ForeignMarket,
	})
	return buyResp.UnitsAdded, nil
}

// haulAndDeposit travels the hull home to the warehouse waypoint (multi-jump, jump-safe),
// docks, and deposits every held good the warehouse supports via the Lane B protocol
// (ReserveSpaceForDeposit → TransferCargo → ConfirmDeposit). A good the warehouse does
// not support is left aboard (it will report stranded at the final exit). Returns the
// total units deposited. source is the market the just-bought cargo came from, threaded onto
// each emitted stock-IN event (sp-j6uz); it is "" on the resume path, where the aboard cargo
// was bought in a prior run and its provenance is unknown.
func (h *RunStockerCoordinatorHandler) haulAndDeposit(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	group []*storage.StorageOperation,
	response *RunStockerCoordinatorResponse,
	depositedGoods map[string]bool,
	source string,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	ship, err = h.legs.travel(ctx, ship, cmd.WarehouseWaypoint, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("travel to warehouse %s failed: %w", cmd.WarehouseWaypoint, err)
	}
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		return 0, fmt.Errorf("dock at warehouse %s failed: %w", cmd.WarehouseWaypoint, err)
	}

	// Reload post-dock and snapshot the held goods in deterministic order.
	ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return 0, err
	}
	cargo := ship.Cargo()
	if cargo == nil {
		return 0, nil
	}
	heldByGood := map[string]int{}
	var goods []string
	for _, item := range cargo.Inventory {
		if item.Units > 0 {
			if _, seen := heldByGood[item.Symbol]; !seen {
				goods = append(goods, item.Symbol)
			}
			heldByGood[item.Symbol] += item.Units
		}
	}
	sort.Strings(goods)

	total := 0
	for _, good := range goods {
		// Deposit the good into the co-located group, spilling from the newest member
		// with space into the next as each fills (sp-5q2c additive capacity). The
		// remainder is held aboard ONLY when the WHOLE group is full or no member
		// supports the good — that is the sole "warehouse full" condition now.
		remaining := heldByGood[good]
		for remaining > 0 {
			dst := tradingsvc.SelectDepositWarehouse(h.storageCoordinator, group, good)
			if dst == nil {
				logger.Log("WARNING", fmt.Sprintf("Stocker: no co-located warehouse at %s can accept %d %s (all full or unsupported) - held aboard (reports stranded if undeposited at exit)", cmd.WarehouseWaypoint, remaining, good), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "warehouse_waypoint": cmd.WarehouseWaypoint, "good": good, "units": remaining, "group_size": len(group),
				})
				break
			}
			deposited, derr := h.depositGood(ctx, cmd, dst, good, remaining, response, source)
			if derr != nil {
				return total, derr
			}
			if deposited <= 0 {
				break // race: space vanished between select and reserve — hold the rest
			}
			remaining -= deposited
			total += deposited
			depositedGoods[good] = true
		}
	}
	return total, nil
}

// depositGood deposits units of good into the warehouse using the gas-proven protocol:
// ReserveSpaceForDeposit → TransferCargo (API) → ConfirmDeposit, releasing the
// reservation on transfer failure. It books ZERO revenue — the capital was already sunk
// at the buy, so a deposit is a pure inventory move (no ledger transaction row). A
// warehouse with no space leaves the cargo aboard (returns 0) so it reports stranded at
// the final exit rather than being silently dropped.
func (h *RunStockerCoordinatorHandler) depositGood(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	op *storage.StorageOperation,
	good string,
	units int,
	response *RunStockerCoordinatorResponse,
	source string,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	storageShip, reserved, ok := h.storageCoordinator.ReserveSpaceForDeposit(op.ID(), units)
	if !ok || storageShip == nil {
		logger.Log("WARNING", fmt.Sprintf("Stocker: warehouse %s has no space for %d %s - held aboard (reports stranded if undeposited at exit)", op.ID(), units, good), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(), "good": good, "units": units,
		})
		return 0, nil
	}
	if reserved < units {
		units = reserved
	}

	if _, terr := h.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
		FromShip:   cmd.ShipSymbol,
		ToShip:     storageShip.ShipSymbol(),
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
	}); terr != nil {
		h.storageCoordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
		return 0, fmt.Errorf("deposit transfer of %d %s to warehouse hull %s failed: %w", units, good, storageShip.ShipSymbol(), terr)
	}
	h.storageCoordinator.ConfirmDeposit(storageShip.ShipSymbol(), good, units)

	// Emit the deposit as a structured stock-IN event (sp-j6uz) now that the transfer has
	// physically moved (TransferCargo) and committed (ConfirmDeposit) — on the ACTUAL confirmed
	// deposit, never on intent. This is the stock-IN mirror of kqxe's withdrawal event, read
	// downstream to measure depot throughput/coverage and (differenced against draws) current
	// fill. Additive + fail-open: a nil recorder is a no-op and a record error is swallowed.
	h.recordStocking(ctx, storage.StockingEvent{
		Good:              good,
		Units:             units,
		WarehouseWaypoint: storageShip.WaypointSymbol(),
		SourceWaypoint:    source,
		Ship:              cmd.ShipSymbol,
		PlayerID:          cmd.PlayerID,
	})

	response.UnitsDeposited += units
	logger.Log("INFO", fmt.Sprintf("Stocker: deposited %d %s into warehouse %s (no revenue, capital booked at buy)", units, good, storageShip.WaypointSymbol()), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": good, "units": units, "warehouse": op.ID(),
		"storage_ship": storageShip.ShipSymbol(), "operation_type": "warehouse_deposit",
	})
	return units, nil
}

// recordStocking emits one stocker→warehouse deposit event (sp-j6uz) on the actual CONFIRMED
// deposit, stamping it with the handler's clock. It is additive instrumentation mirroring
// kqxe's recordWithdrawal: a nil recorder is a no-op, and a persistence error is logged and
// swallowed so telemetry can never fail a deposit whose goods are already physically in the
// warehouse (fail-open, RULINGS #1).
func (h *RunStockerCoordinatorHandler) recordStocking(ctx context.Context, event storage.StockingEvent) {
	if h.stockingRecorder == nil {
		return
	}
	event.DepositedAt = h.clock.Now()
	if err := h.stockingRecorder.Record(ctx, event); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", "Stocking event record failed (deposit succeeded; telemetry only)", map[string]interface{}{
			"ship_symbol":  event.Ship,
			"trade_symbol": event.Good,
			"units":        event.Units,
			"warehouse":    event.WarehouseWaypoint,
			"error":        err.Error(),
		})
	}
}

// warehousesAt returns ALL RUNNING warehouse operations parked at waypoint — the
// co-located additive-capacity group (sp-5q2c: e.g. light-12's 80 slots + heavy-4B's
// 225 at E42, whose capacity and stock sum). Empty when none is running there (fail
// closed — the caller treats the pass as empty). A stale sp-3lj5 zombie row (a
// container stopped without its storage_operations row terminalized) is included but
// contributes 0 free space and 0 stock to every aggregate and is never chosen as a
// deposit target, so aggregation composes with the newest-wins zombie fix.
func (h *RunStockerCoordinatorHandler) warehousesAt(ctx context.Context, playerID int, waypoint string) []*storage.StorageOperation {
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	return tradingsvc.RunningWarehousesAtWaypoint(ops, waypoint)
}

// warehouseAt returns the newest RUNNING warehouse operation at waypoint (the group's
// deposit anchor), or nil if none is running there. The stocking flow aggregates the
// whole co-located group (warehousesAt); this anchor pick is retained for the sp-3lj5
// regression, where a stale zombie row sits alongside its live replacement at the same
// waypoint — the newest-wins resolution ensures the anchor is the live operation, and
// the group aggregation independently ensures the zombie's 0-capacity never makes the
// warehouse look full.
func (h *RunStockerCoordinatorHandler) warehouseAt(ctx context.Context, playerID int, waypoint string) *storage.StorageOperation {
	return tradingsvc.SelectNewestRunningWarehouse(h.warehousesAt(ctx, playerID, waypoint))
}

// capitalCeiling resolves the pre-positioning capital ceiling: ceilingPct (default 10)
// percent of LIVE treasury, held JUNIOR to the working-capital reserve. Returns
// known=false when the live balance is UNREADABLE — the pick then stocks nothing (fail
// closed, RULINGS #4). Mirrors the tour's depositCapitalCeiling verbatim.
func (h *RunStockerCoordinatorHandler) capitalCeiling(ctx context.Context, reserve int64) (int64, bool) {
	if h.apiClient == nil {
		return 0, false
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, false
	}
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, false
	}
	pct := int64(h.ceilingPct)
	if pct <= 0 {
		pct = defaultDepositCeilingPct
	}
	ceiling := int64(agent.Credits) * pct / 100
	if avail := int64(agent.Credits) - reserve; avail < ceiling {
		ceiling = avail // junior to the working-capital reserve
	}
	if ceiling < 0 {
		ceiling = 0
	}
	return ceiling, true
}

// foreignMarketFresh reports whether the miner's cheapest-foreign market for good is a
// trustworthy pick: the cached market row must still sell the good and be no older than
// maxAge (the 75-min discipline). A zero timestamp is "unknown age" and treated as fresh
// (matches tour_snapshot). An unreadable/gone market fails CLOSED (not fresh) so the
// stocker never hauls to a market it cannot confirm.
func (h *RunStockerCoordinatorHandler) foreignMarketFresh(ctx context.Context, waypoint, good string, playerID int, now time.Time, maxAge time.Duration) bool {
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil || mkt == nil {
		return false
	}
	if mkt.FindGood(good) == nil {
		return false
	}
	observed := mkt.LastUpdated()
	if observed.IsZero() {
		return true
	}
	return now.Sub(observed) <= maxAge
}

// foreignMarketReachable reports whether waypoint's system has a jump-gate route from
// the hull's CURRENT system, within the SAME bound buy()'s travel() itself enforces
// (sp-yuq9). Before this filter existed, the need-rank picked the cheapest foreign
// market across EVERY scouted market_data row with no reachability check at all, then
// handed it to travel() unchecked — TORWIND-38 repeatedly selected X1-PB12 as cheapest
// from X1-KA42, but PB12 has no jump-gate route within 5 jumps, so every relaunch
// crash-looped identically ("travel to market X1-PB12-C55F failed: no jump-gate route
// from X1-KA42 to X1-PB12 within 5 jumps") because scout posts kept PB12's ask fresh
// forever. This consults h.legs.gateGraphResolver() — the IDENTICAL cached GateGraph
// instance (gategraph.Service, bounded to MaxJumpPath) that travel()'s own jumpPath()
// uses — so there is exactly ONE notion of reachability in the codebase, never a second
// one invented here. The check is DB-only (Routable -> Path -> the cached fetch-through
// adjacency, sp-ikx1): no per-candidate live API call.
//
//   - Same system → trivially reachable, no consult needed (also keeps every
//     single-system pick() test passing unmodified with no gate graph wired).
//   - No gate graph wired (h.legs.gateGraphResolver() == nil) → fails OPEN, mirroring
//     jumpPath's own legacy single-hop fallback, so every caller that never wires one
//     (nearly all existing tests) is byte-for-byte unaffected.
//   - A wired graph that cannot resolve routability (a store/fetch error, NOT a
//     definitive unroutable verdict) fails CLOSED — an unverifiable route is no more
//     trustworthy than the unreadable market foreignMarketFresh already refuses.
//   - Otherwise, returns the graph's own Routable verdict directly.
func (h *RunStockerCoordinatorHandler) foreignMarketReachable(ctx context.Context, currentSystem, waypoint string, playerID int) bool {
	destSystem := shared.ExtractSystemSymbol(waypoint)
	if currentSystem == destSystem {
		return true
	}
	gateGraph := h.legs.gateGraphResolver()
	if gateGraph == nil {
		return true
	}
	routable, err := gateGraph.Routable(ctx, currentSystem, destSystem, playerID)
	if err != nil {
		return false
	}
	return routable
}

// recordNoReachableSource emits the sp-yuq9 "no reachable source" verdict for a hull at
// most ONCE per distinct (unreachable/total) signature: a hull whose need-rank keeps
// landing on unreachable-only candidates parks QUIETLY across hundreds of re-plans,
// logging one line, not one per pass (the same discipline that stopped the ikx1/13tl
// spam). A genuine state change — the unreachable count moves, or the hull later finds a
// reachable pick and clearNoReachableSource forgets the signature — re-emits.
// Concurrency-safe: the stocker handler is a shared singleton dispatched for every
// stocking hull at once.
func (h *RunStockerCoordinatorHandler) recordNoReachableSource(ctx context.Context, shipSymbol string, unreachable, totalRows int) {
	sig := fmt.Sprintf("%d/%d", unreachable, totalRows)
	h.noReachableSourceMu.Lock()
	if h.noReachableSource[shipSymbol] == sig {
		h.noReachableSourceMu.Unlock()
		return
	}
	h.noReachableSource[shipSymbol] = sig
	h.noReachableSourceMu.Unlock()
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Stocker parked: no reachable source (%d/%d ranked candidate(s) gate-unreachable from the hull's current system) - parking quietly rather than repeating an unreachable pick",
		unreachable, totalRows), map[string]interface{}{
		"ship_symbol": shipSymbol, "reason": "no_reachable_source", "unreachable_candidates": unreachable, "total_candidates": totalRows,
	})
}

// clearNoReachableSource forgets a hull's last no-reachable-source verdict so that,
// should the need-rank fall back into an unreachable-only state later, it logs afresh —
// the "or on state change" half of the once-per-hull discipline.
func (h *RunStockerCoordinatorHandler) clearNoReachableSource(shipSymbol string) {
	h.noReachableSourceMu.Lock()
	delete(h.noReachableSource, shipSymbol)
	h.noReachableSourceMu.Unlock()
}

// strandedReason reports whether the hull is ending laden with undeposited cargo — an
// honest-completion veto (the stocker's one job is to deposit, so ending with a load is a
// failure, whatever its provenance). It reads the PHYSICAL hull cargo (not a bought-this-run
// tally) so a hull that restarts laden and cannot deposit — warehouse full/gone — reports
// FAILED and the next run retries deposit-first. The message names each good, its units,
// and the hull's current location so the strand is greppable and hand-recoverable. A load
// that cannot be read does NOT false-veto (fail open on the read).
func (h *RunStockerCoordinatorHandler) strandedReason(ctx context.Context, cmd *RunStockerCoordinatorCommand) (string, bool) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return "", false
	}
	c := ship.Cargo()
	if c == nil {
		return "", false
	}
	var parts []string
	for _, item := range c.Inventory {
		if item.Units > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.Units, item.Symbol))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	return fmt.Sprintf("stranded cargo: %s still aboard at %s (undeposited) - reporting failure", strings.Join(parts, ", "), ship.CurrentLocation().Symbol), true
}

// standingTick resolves the STANDING park cadence between at-target re-checks: TickSeconds
// when set (RULINGS #5), else the default 30s. Only consulted on the standing path.
func (h *RunStockerCoordinatorHandler) standingTick(cmd *RunStockerCoordinatorCommand) time.Duration {
	if cmd.TickSeconds > 0 {
		return time.Duration(cmd.TickSeconds) * time.Second
	}
	return defaultStockerStandingTick
}

// parkTick blocks for the standing cadence, returning early with the context error if a
// stop/shutdown cancels ctx first — so a Stop never has to wait the full tick out (the
// standing loop's ONLY sleep). It races the injected clock's Sleep — instant under the test
// MockClock, a real wait in production — against ctx.Done, mirroring the container runner's
// sleepOrCancel. The detached sleeper goroutine outlives an early return by at most one tick
// before exiting, so it cannot leak.
func (h *RunStockerCoordinatorHandler) parkTick(ctx context.Context, tick time.Duration) error {
	slept := make(chan struct{})
	go func() {
		h.clock.Sleep(tick)
		close(slept)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-slept:
		return nil
	}
}

// heldUnits reports the total units of cargo aboard (0 when the hold is empty) — the
// laden check the resume-safe first move reads to know it must deposit before buying.
func heldUnits(ship *navigation.Ship) int {
	c := ship.Cargo()
	if c == nil {
		return 0
	}
	total := 0
	for _, item := range c.Inventory {
		total += item.Units
	}
	return total
}

// stringSet builds a lookup set from a slice (nil for an empty slice, so an empty
// allowlist reads as "no restriction"). Local to the stocker; mirrors the services
// package's toSet without crossing the package boundary.
func stringSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}
