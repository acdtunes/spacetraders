package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
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

// RunStockerCoordinatorCommand is a captain-directed, guarded STOCKER LOOP:
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
// (#5). #14: the GENERIC stocker crosses systems by design (does not bind); the CONTRACT DEPOT
// sets HomeSystemOnly (sp-k2xav) to confine sourcing to the home system, under which #14 DOES bind.
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
	// HomeSystemOnly confines source-market selection to the WAREHOUSE's OWN system (bead
	// sp-k2xav, RULINGS #14): the contract-depot stocker sources the fixed far-source goods from
	// the home system's own export/exchange waypoints ONLY, NEVER cross-gating to a cheaper
	// foreign market. Set by launchDepotStocker for the contract depot; false (the default) leaves
	// the GENERIC stocker's cross-system economics unchanged. Threaded into the demand miner's
	// DemandMinerOptions.HomeSystemOnly in pick(). Persisted in the launch config so it survives
	// recovery (RULINGS #2).
	HomeSystemOnly bool
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
	legs       *RunTradeRouteCoordinatorHandler
	mediator   common.Mediator
	marketRepo market.MarketRepository
	apiClient  domainPorts.APIClient
	// treasury is the LEDGER-backed treasury reader (sp-muq66) the stocker's capital
	// ceiling reads through instead of calling Get Agent on every pick. nil —
	// every existing test — leaves the direct apiClient read in place, byte-identical; the
	// daemon injects the shared reader via SetTreasuryReader at boot, with no config gate
	// between. Wired or not, an unreadable balance still stocks nothing (fail closed).
	treasury           TreasuryReader
	clock              shared.Clock
	storageCoordinator storage.StorageCoordinator
	warehouseFinder    tradingsvc.WarehouseOperationFinder
	demandMiner        tradingsvc.DepositDemandMiner
	config             tradingsvc.DepositCandidateConfig
	ceilingPct         int
	// waypointRepo resolves source/warehouse waypoint COORDINATES for the distance-aware
	// residual buy-leg in the auto-cap knapsack. Cache-only (no API fetch-through), so the
	// per-pass re-solve costs no API spend; a nil repo (or an uncached waypoint) FAILS OPEN to the
	// coarse in/cross-system residual (RULINGS #1) — the previous behavior.
	waypointRepo system.WaypointRepository

	// freshness derives the stocker's market-freshness cap from the LIVE scan rotation
	// rather than a minute count written into the source (sp-k4z5b) — the same resolver
	// the tour handler holds, so the two paths never disagree about what "stale" means.
	// nil leaves the cap at marketDataAgeFloor, byte-identical.
	freshness *MarketFreshness

	// noReachableSource de-dups the sp-yuq9 "every ranked candidate is gate-unreachable"
	// verdict so a hull whose need-rank keeps landing on unreachable-only markets (a
	// scouted-but-unroutable market like X1-PB12 staying artificially "cheapest" forever)
	// logs ONCE per ship per distinct state, not once per pass — the same per-hull
	// state-change de-dup discipline as the tour coordinator's depositParked and
	// the ikx1 backoff. Keyed by ship symbol; the value is the last emitted
	// "<unreachable>/<total>" signature. Guarded by noReachableSourceMu because the handler
	// is a SHARED singleton dispatched concurrently across every stocker hull.
	noReachableSourceMu sync.Mutex
	noReachableSource   map[string]string

	// Warehouse auto-cap optimizer. capParams are the analyst-owned tunables
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

	// Stocking instrumentation: a driven port that records each CONFIRMED
	// stocker→warehouse deposit as a structured economic event so downstream analysis can
	// measure depot stock-IN throughput/coverage (the stock-IN mirror of the kqxe withdrawal
	// stream). Optional — a nil recorder disables emission so existing tests and any caller
	// that has not wired it are byte-identical (additive, fail-open). The event's DepositedAt
	// is stamped with the handler's own clock (h.clock, guaranteed non-nil).
	stockingRecorder storage.StockingRecorder
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

	// Stamp every ledger row this run's buy legs write with operation_type "contract" (via the
	// contract coordinator's own "contract_workflow" raw type — shared.NormalizedOperationType
	// maps it to "contract", identically to delivery_executor.go:119) so pre-positioning spend
	// lands in the SAME bucket as the contract REVENUE it enables. Contract Profit (panel 109,
	// filtering operation_type='contract') then NETS the pre-stock input cost against contract
	// revenue instead of overstating it while a standalone 'stocker' line shows as pure loss
	// (sp-a0y4). This is the ledger-attribution tag ONLY; it is DISTINCT from the stocker's
	// fleet-dedication / ClaimShip identity (container_ops_stocker.go operationStocker="stocker"),
	// which stays "stocker" so hull ownership survives restarts.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "contract_workflow"))

	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = int64(defaultWorkingCapitalReserve)
	}
	maxAge := h.listingMaxAge(ctx, cmd.PlayerID)
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
		// COMPLETE a -1/standing container (the trap).
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
	// (one OR MORE running warehouses whose capacity sums). None
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
	// threaded onto each stock-IN event for source-provenance analysis.
	deposited, derr := h.haulAndDeposit(ctx, cmd, group, response, depositedGoods, pick.ForeignMarket)
	if derr != nil {
		return false, derr
	}
	return deposited > 0, nil
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
