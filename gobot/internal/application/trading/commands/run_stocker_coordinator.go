package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
)

// exitReason* enumerates why the stocker loop stopped (observability, mirrors the
// tour coordinator's ExitReason).
const (
	// stockerExitIterations: a finite --iterations budget was consumed.
	stockerExitIterations = "iterations_exhausted"
	// stockerExitStarvation: stockerStarvationLimit consecutive passes found nothing
	// to stock — the warehouse is at target / nothing eligible fits. An HONEST completion.
	stockerExitStarvation = "starvation"
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
	// left to stock; N>0 → exactly N productive round-trips.
	iterations := cmd.Iterations
	if iterations == 0 {
		iterations = 1
	}
	continuous := iterations < 0

	depositedGoods := map[string]bool{}

	noProgressStreak := 0
	for continuous || response.RoundTripsCompleted < iterations {
		// A stop/shutdown cancels ctx. Exit RESUMABLE at the round-trip boundary by
		// returning the ctx error, which the runner routes through its ctx.Err() path
		// (re-adopted at next boot) — never let a cancel be misread as starvation and
		// COMPLETE a -1 container (the sp-ovkn trap).
		if err := ctx.Err(); err != nil {
			return err
		}

		productive, terr := h.runOneRoundTrip(ctx, cmd, response, depositedGoods, reserve, maxAge)
		if terr != nil {
			return terr
		}
		if !productive {
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

	// The warehouse op at the deposit waypoint is required to stock. Gone/never-running →
	// an empty pass (the starvation streak exits honestly after K of these).
	op := h.warehouseAt(ctx, cmd.PlayerID, cmd.WarehouseWaypoint)
	if op == nil {
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
			"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(),
		})
		deposited, derr := h.haulAndDeposit(ctx, cmd, op, response, depositedGoods)
		if derr != nil {
			return false, derr
		}
		return deposited > 0, nil
	}

	// PICK the most-needed good (need-ranked, every money guard fail-closed).
	pick, ok := h.pick(ctx, cmd, op, reserve, maxAge)
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

	// HAUL HOME + DEPOSIT.
	deposited, derr := h.haulAndDeposit(ctx, cmd, op, response, depositedGoods)
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
	op *storage.StorageOperation,
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

	// Warehouse free space (shared across the op's storage ships).
	freeSpace := 0
	for _, s := range h.storageCoordinator.GetStorageShipsForOperation(op.ID()) {
		freeSpace += s.AvailableSpace()
	}
	if freeSpace <= 0 {
		logger.Log("INFO", fmt.Sprintf("Stocker: warehouse %s full (0 free space) - nothing to stock this pass", op.ID()), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(),
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

	homeSystem := shared.ExtractSystemSymbol(cmd.WarehouseWaypoint)
	rows, err := h.demandMiner.Mine(ctx, homeSystem, cmd.PlayerID, nil, persistence.DemandMinerOptions{
		MinRecurrence: h.config.MinRecurrence, TopN: stockerMinerTopN,
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

	var best stockerPick
	bestValue := 0
	eligible, afterFilters := 0, 0

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
		// Only stock goods the warehouse actually BUFFERS: a good it does not support
		// would strand (no contract worker could withdraw it). Fail closed.
		if !op.SupportsGood(r.Good) {
			continue
		}

		target := r.DemandUnits
		if cmd.TargetPerGood > 0 {
			target = cmd.TargetPerGood
		}
		unitsShort := target - h.storageCoordinator.GetTotalCargoAvailable(op.ID(), r.Good)
		if unitsShort <= 0 {
			continue // already at/over the fill target
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
		logger.Log("INFO", fmt.Sprintf(
			"Stocker verdict: nothing to stock — [warehouse=%s free=%d ceiling=%d funnel: miner_rows=%d eligible=%d after_filters=%d] (at target / unaffordable / stale / unsupported)",
			op.ID(), freeSpace, ceiling, len(rows), eligible, afterFilters), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(), "free_space": freeSpace,
			"ceiling": ceiling, "miner_rows": len(rows), "eligible": eligible, "after_filters": afterFilters,
		})
		return stockerPick{}, false
	}

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
// total units deposited.
func (h *RunStockerCoordinatorHandler) haulAndDeposit(
	ctx context.Context,
	cmd *RunStockerCoordinatorCommand,
	op *storage.StorageOperation,
	response *RunStockerCoordinatorResponse,
	depositedGoods map[string]bool,
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
		units := heldByGood[good]
		if !op.SupportsGood(good) {
			logger.Log("WARNING", fmt.Sprintf("Stocker: warehouse %s does not support %s - %d units held aboard (will report stranded)", op.ID(), good, units), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol, "warehouse": op.ID(), "good": good, "units": units,
			})
			continue
		}
		deposited, derr := h.depositGood(ctx, cmd, op, good, units, response)
		if derr != nil {
			return total, derr
		}
		if deposited > 0 {
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

	response.UnitsDeposited += units
	logger.Log("INFO", fmt.Sprintf("Stocker: deposited %d %s into warehouse %s (no revenue, capital booked at buy)", units, good, storageShip.WaypointSymbol()), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": good, "units": units, "warehouse": op.ID(),
		"storage_ship": storageShip.ShipSymbol(), "operation_type": "warehouse_deposit",
	})
	return units, nil
}

// warehouseAt returns the RUNNING warehouse operation parked at waypoint (the deposit
// anchor), or nil if none is running there. Mirrors the tour's warehouseAt. When more
// than one RUNNING op matches (sp-3lj5: a container stopped without its
// storage_operations row being terminalized, leaving a stale "zombie" row alongside
// its live replacement at the same waypoint), resolves deterministically to the
// newest via tradingsvc.SelectNewestRunningWarehouse and logs the collision - a naive
// first-match pick can silently select the dead operation, which always reads back
// zero free space and makes a live warehouse look full.
func (h *RunStockerCoordinatorHandler) warehouseAt(ctx context.Context, playerID int, waypoint string) *storage.StorageOperation {
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	var matches []*storage.StorageOperation
	for _, op := range ops {
		if op.OperationType() == storage.OperationTypeWarehouse && op.WaypointSymbol() == waypoint {
			matches = append(matches, op)
		}
	}
	if len(matches) > 1 {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Stocker: %d RUNNING warehouse operations at %s - resolving to the newest (sp-3lj5 zombie-row collision)",
			len(matches), waypoint), map[string]interface{}{
			"warehouse_waypoint": waypoint, "collision_count": len(matches),
		})
	}
	return tradingsvc.SelectNewestRunningWarehouse(matches)
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
