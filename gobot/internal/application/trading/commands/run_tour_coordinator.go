package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
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
	// maxTourHops / maxTourSystems bound the planner's search (spec: ≤6 hops,
	// ≤2 gate-adjacent systems). The planner enforces the system cap; the executor
	// caps hops in the constraint it sends.
	maxTourHops    = 6
	maxTourSystems = 2
	// defaultModelArtifactPath is where the checked-in market-model artifact lives
	// relative to the daemon's working directory (repo root). The executor reads
	// fit_version + era from it at launch to bind the planner to the exact model —
	// an unreadable artifact fails OPEN to single-lane (RULINGS #4: never guess a
	// version), never a phantom trade.
	defaultModelArtifactPath = "gobot/services/routing-service/model_artifacts/market_model.json"
	// tourDefaultMaxSpendTreasuryPct sizes the default cumulative spend cap when the
	// captain leaves --max-spend at 0: 25% of live treasury (RULINGS #6). With
	// --iterations -1 this is re-resolved against LIVE treasury at EACH tour's plan
	// (an explicit --max-spend stays constant per tour); see execute's loop.
	tourDefaultMaxSpendTreasuryPct = 25
	// tourStarvationLimit bounds how many CONSECUTIVE no-progress tours (planner
	// returns no profitable tour, or a feasible plan executes zero trades) a
	// continuous run (--iterations -1) tolerates before it calls margins dead and
	// exits HONESTLY (the container completes). Mirrors the trade-route circuit
	// loop's noProgressStarvationLimit: one no-plan can be a transient live-recheck
	// miss, several in a row means the system has nothing left worth touring. A
	// no-plan on the VERY FIRST tour (nothing earned yet) is the existing fail-open
	// "tour unavailable" instead, so the single-lane fallback stands.
	tourStarvationLimit = 3
	// defaultDepositCeilingPct is the pre-positioning capital ceiling as a percent
	// of live treasury when the captain leaves capital_ceiling_pct at 0 (sp-dchv
	// Lane C). Junior to the working-capital reserve; an unreadable balance yields
	// ZERO candidates (fail closed, RULINGS #4).
	defaultDepositCeilingPct = 10
)

// exitReason* enumerates why the continuous tour loop stopped, surfaced on the
// response for observability (mirrors the trade-route coordinator's ExitReason).
const (
	// tourExitIterations: a finite --iterations budget was consumed (every tour flew).
	tourExitIterations = "iterations_exhausted"
	// tourExitStarvation: tourStarvationLimit consecutive tours found no profitable
	// tour (or flew zero trades) — margins died. An HONEST completion.
	tourExitStarvation = "starvation"
	// tourExitUnavailable: the very first tour found no plan and nothing was earned —
	// the fail-open no-op (single-lane fallback stands).
	tourExitUnavailable = "tour_unavailable"
)

// RunTourCoordinatorCommand is a captain-directed, guarded multi-hop trade-tour run
// (sp-1ek0): plan a depth-aware tour for THIS hull, fly it leg by leg with prices
// re-verified live at every dock, re-plan at most ReplanLimit times when reality
// drifts past tolerance. The route is dynamically planned, so honest completion is a
// response VETO (not a Go error) — a re-run cannot resume a planner-chosen route.
//
// Iterations makes it a CONTINUOUS engine (sp-m5kv): on manifest completion it
// re-plans from the hull's CURRENT position + live market and flies the next tour
// with no captain in the loop, turning capital velocity from captain-cadence into
// engine-cadence. See Iterations for the loop semantics.
type RunTourCoordinatorCommand struct {
	ShipSymbol  string
	PlayerID    int
	MaxHops     int   // 0 → maxTourHops
	MaxSpend    int64 // 0 → 25% of live treasury (re-resolved per tour when Iterations != 0/1)
	MinMargin   int
	ReplanLimit int // 0 → tourMaxReplansDefault (PER TOUR)
	// Iterations is the tour count (sp-m5kv), unifying the container iteration
	// semantics (registry invariant 3): -1 = CONTINUOUS (tour, re-plan from the new
	// position, tour again — until margins die/starvation/stop), N>0 = exactly N
	// tours, 0 = the one-tour default (the original one-shot behavior, so every
	// pre-sp-m5kv caller and test is byte-for-byte unchanged). The coordinator owns
	// this loop internally (CoordinatorOwnsIterations); the container runs Handle()
	// once.
	Iterations            int
	AgentSymbol           string
	ContainerID           string // the tour id; groups this run's telemetry legs
	WorkingCapitalReserve int64  // 0 → defaultWorkingCapitalReserve
	// ModelArtifactPath overrides defaultModelArtifactPath (tests point it at a temp
	// artifact); empty → the default repo-relative path.
	ModelArtifactPath string
}

// RunTourCoordinatorResponse reports the realised tour economics and — via
// CompletionOutcome — whether the run honestly completed. Three terminal shapes:
// a completed tour (Completed), a fail-open no-op (TourUnavailable, a clean
// completion — planner down/infeasible or model artifact unreadable, single-lane
// fallback stands), and a stranded-cargo veto (CargoStranded → the runner
// terminalizes FAILED via the honest-completion contract).
type RunTourCoordinatorResponse struct {
	ShipSymbol   string
	TourID       string
	LegsPlanned  int
	LegsExecuted int
	Replans      int
	TotalSpent   int64
	TotalRevenue int64
	NetProfit    int64
	ModelVersion string
	Completed    bool

	// ToursCompleted counts how many tours flew >=1 trade this run (sp-m5kv). 1 for
	// the one-shot default; >1 for a continuous (--iterations) run. TradesExecuted is
	// the run's total executed buy+sell tranches (the per-tour progress signal the
	// starvation guard reads). ExitReason (a tourExit* constant) explains why a
	// continuous loop stopped; empty on the one-shot path.
	ToursCompleted int
	TradesExecuted int
	ExitReason     string

	// TourUnavailable marks a fail-open exit: no trading happened, the single-lane
	// fallback remains. A CLEAN completion (not a failure), never a phantom trade.
	TourUnavailable       bool
	TourUnavailableReason string

	// CargoStranded is the honest-completion veto (sp-7yej invariant 2): the tour
	// ended holding cargo it bought this run. Threaded through CompletionOutcome
	// (nil Go error), NOT arb's non-nil-error shape — a dynamically-planned tour
	// cannot be resumed by a re-run, which would trade AROUND the strand.
	CargoStranded       bool
	CargoStrandedReason string

	Error string
}

// CompletionOutcome implements common.CompletionReporter: a stranded tour vetoes
// the runner's success=true (terminalized FAILED with the strand as its signature).
// A fail-open "tour unavailable" is an honest clean completion (nothing half-done).
func (r *RunTourCoordinatorResponse) CompletionOutcome() (bool, string) {
	if r.CargoStranded {
		return false, r.CargoStrandedReason
	}
	return true, ""
}

// Compile-time pin: the tour response participates in the honest-completion contract.
var _ common.CompletionReporter = (*RunTourCoordinatorResponse)(nil)

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
	// apiClient live-reads treasury for the default 25% max-spend; nil → no default
	// cap (the per-buy working-capital floor still guards).
	apiClient domainPorts.APIClient
	// modelArtifactPath is the daemon-configured (absolute) path to the market-model
	// artifact this coordinator reads at launch, injected from cfg.Routing.ModelArtifactPath
	// (sp-wj0h). Empty → the repo-relative defaultModelArtifactPath fallback. A per-run
	// cmd.ModelArtifactPath (tests) still wins over this.
	modelArtifactPath string

	// mediator dispatches the cargo TransferCargoCommand for haul-to-storage deposit
	// legs (sp-dchv Lane C). Same mediator the delegated legs use.
	mediator common.Mediator
	// Pre-positioning deposit dependencies (sp-dchv Lane C), all optional and
	// injected via SetPrePositioning AFTER the storage subsystem is wired (main.go).
	// When any is nil or prePositioning.Enabled is false, no deposit legs are
	// offered or executed and the tour behaves exactly as pre-sp-dchv.
	storageCoordinator storage.StorageCoordinator
	warehouseFinder    tradingsvc.WarehouseOperationFinder
	demandMiner        tradingsvc.DepositDemandMiner
	prePositioning     tradingsvc.DepositCandidateConfig
	depositCeilingPct  int
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
		legs:         NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		marketRepo:   marketRepo,
		waypointRepo: waypointRepo,
		telemetry:    telemetry,
		planner:      planner,
		clock:        clock,
		apiClient:    apiClient,
		mediator:     mediator,
	}
}

// SetPrePositioning wires the optional haul-to-storage deposit subsystem (sp-dchv
// Lane C): the shared storage coordinator (deposit protocol + warehouse space
// reads), the warehouse-op finder, the Lane A demand miner, the resolved config,
// and the capital-ceiling percent. Called from main.go AFTER the storage subsystem
// is constructed (the tour coordinator is wired earlier). Left unset, no deposit
// legs are ever offered or executed — the tour plans and flies pure arb.
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

// SetGateGraph wires the multi-jump gate-graph resolver into the delegated movement
// handler (so travel crosses multi-hop gaps and cross-gate tours fly). Mirrors the
// arb coordinator's injection.
func (h *RunTourCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetModelArtifactPath injects the daemon-configured (absolute) market-model artifact
// path this coordinator reads at launch (sp-wj0h: resolved from cfg.Routing.ModelArtifactPath
// so it is cwd-independent). Left unset, the coordinator falls back to the repo-relative
// defaultModelArtifactPath. Mirrors the SetGateGraph optional-injection idiom.
func (h *RunTourCoordinatorHandler) SetModelArtifactPath(path string) {
	h.modelArtifactPath = path
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

func (h *RunTourCoordinatorHandler) execute(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse) error {
	logger := common.LoggerFromContext(ctx)

	// Stamp every ledger row this run's buy/sell legs write with operation_type=
	// "tour" (sp-lgnh). The delegated cargo-tx path reads this operation context
	// off ctx and persists opCtx.NormalizedOperationType() ("tour_run" → "tour");
	// without it, tour trades land under the default and contaminate the very
	// single-lane baseline the graduation gate measures the tour against (the
	// baseline filters operation_type <> 'tour'). Mirrors how every coordinator
	// tags its writes at the boundary (run_trade_route_coordinator.go's "trade_route").
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "tour_run"))

	// Bind the model version from the checked-in artifact (RULINGS #4: unreadable →
	// fail OPEN to single-lane, never guess a version). Path precedence (sp-wj0h): an
	// explicit per-run cmd.ModelArtifactPath (tests) → the daemon-configured absolute
	// path (production, cwd-independent) → the repo-relative constant (pure-env fallback).
	artifactPath := cmd.ModelArtifactPath
	if artifactPath == "" {
		artifactPath = h.modelArtifactPath
	}
	if artifactPath == "" {
		artifactPath = defaultModelArtifactPath
	}
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

	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = int64(defaultWorkingCapitalReserve)
	}
	maxHops := cmd.MaxHops
	if maxHops <= 0 || maxHops > maxTourHops {
		maxHops = maxTourHops
	}
	replanLimit := cmd.ReplanLimit
	if replanLimit <= 0 {
		replanLimit = tourMaxReplansDefault
	}

	// Iteration budget (sp-m5kv): 0 → the one-tour default (the original one-shot,
	// so every pre-sp-m5kv caller/test is unchanged); -1 → continuous until margins
	// die; N>0 → exactly N tours.
	iterations := cmd.Iterations
	if iterations == 0 {
		iterations = 1
	}
	continuous := iterations < 0

	// netBought is CUMULATIVE across every tour this run: the honest-completion
	// stranded veto (sp-7yej invariant 2) is checked ONCE, at the final exit. A tour
	// ending with held cargo is NOT stranded mid-run — the next tour re-plans from the
	// hull's current cargo and (sp-m5kv part 2) the solver sells it as launch
	// inventory. Only cargo BOUGHT this run and never sold survives to veto the final
	// completion; pre-held cargo (never in netBought) drives it negative, so
	// liquidating the captain's pre-existing load is a bonus, never a false veto.
	netBought := map[string]int{}

	// The budget counts PRODUCTIVE tours (ToursCompleted), not attempts: "N tours"
	// means N tours actually flown, so a transient no-plan mid-run is retried (bounded
	// by the starvation streak) rather than silently burning a tour slot.
	noProgressStreak := 0
	for continuous || response.ToursCompleted < iterations {
		// A stop/shutdown cancels ctx (interruptAllContainers escalates the STOPPING
		// flag to a ctx cancel). Exit RESUMABLE at the tour boundary by returning the
		// ctx error, which the runner routes through its ctx.Err() path (re-adopted at
		// next boot) — never let a cancel be misread as a swallowed planner no-plan and,
		// via the starvation streak, COMPLETE a -1 container (the sp-ovkn trap: a
		// COMPLETED row is dropped from the recovery set and the hull is lost).
		if err := ctx.Err(); err != nil {
			return err
		}

		// RULINGS #6: an explicit --max-spend is a constant per-tour cap; --max-spend
		// 0/omitted re-resolves 25% of LIVE treasury at EACH tour's plan, so a
		// continuous run sizes each tour to the treasury it has grown into. The per-buy
		// working-capital floor guards every spend regardless.
		tourMaxSpend := cmd.MaxSpend
		if tourMaxSpend == 0 {
			tourMaxSpend = h.defaultMaxSpend(ctx)
		}

		tradesBefore := response.TradesExecuted
		feasible, reason, terr := h.runOneTour(ctx, cmd, response, netBought, maxHops, tourMaxSpend, reserve, replanLimit, modelVersion)
		if terr != nil {
			return terr
		}

		if !feasible {
			// Nothing earned yet AND no plan → the fail-open no-op (single-lane
			// fallback stands): the original one-shot behavior, preserved exactly.
			if response.ToursCompleted == 0 {
				response.TourUnavailable = true
				response.TourUnavailableReason = reason
				response.ExitReason = tourExitUnavailable
				logger.Log("INFO", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "model": modelVersion})
				return nil
			}
			// Already earned: a no-plan from the current position is margin-death.
			// Confirm tourStarvationLimit in a row, then exit HONEST (container completes).
			noProgressStreak++
			if noProgressStreak >= tourStarvationLimit {
				response.ExitReason = tourExitStarvation
				logger.Log("INFO", fmt.Sprintf("Continuous tour stopping - margins died (%d consecutive tours found no profitable plan) after %d productive tour(s)", noProgressStreak, response.ToursCompleted), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted, "reason": reason,
				})
				break
			}
			continue
		}

		// A feasible plan that flew ZERO trades (every leg degraded, re-plans
		// exhausted) is also no-progress — bound how many in a row a -1 loop tolerates
		// so a persistently-unexecutable plan can't spin forever (mirrors the
		// trade-route zero-visit starvation).
		if response.TradesExecuted == tradesBefore {
			noProgressStreak++
			if noProgressStreak >= tourStarvationLimit {
				response.ExitReason = tourExitStarvation
				logger.Log("INFO", fmt.Sprintf("Continuous tour stopping - %d consecutive tours flew zero trades after %d productive tour(s)", noProgressStreak, response.ToursCompleted), map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
				})
				break
			}
			continue
		}

		noProgressStreak = 0
		response.ToursCompleted++
	}
	if response.ExitReason == "" {
		response.ExitReason = tourExitIterations
	}

	// Honest-completion check (FINAL exit only, sp-m5kv boundary): any cargo bought
	// this run and still aboard after the whole loop is a stranded veto — the
	// container is terminalized FAILED (sp-7yej invariant 2). A mid-run held load is
	// deliberately NOT checked here; it was carried forward to the next tour's plan.
	if reason, stranded := h.strandedReason(ctx, cmd, netBought); stranded {
		response.CargoStranded = true
		response.CargoStrandedReason = reason
		logger.Log("ERROR", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol})
		return nil
	}

	response.NetProfit = response.TotalRevenue - response.TotalSpent
	logger.Log("INFO", "Tour run complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted, "exit_reason": response.ExitReason,
		"legs_executed": response.LegsExecuted, "trades_executed": response.TradesExecuted, "replans": response.Replans,
		"spent": response.TotalSpent, "revenue": response.TotalRevenue, "net": response.NetProfit,
	})
	return nil
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
	maxHops int,
	maxSpend, reserve int64,
	replanLimit int,
	modelVersion string,
) (bool, string, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, "", err
	}

	plan, err := h.plan(ctx, ship, maxHops, maxSpend, reserve, cmd, modelVersion)
	if err != nil {
		return false, fmt.Sprintf("tour unavailable: planner error: %v", err), nil
	}
	if !plan.Feasible {
		return false, fmt.Sprintf("tour unavailable: %s", plan.InfeasibleReason), nil
	}
	response.LegsPlanned += len(plan.Legs)
	// Honest projection split (sp-bc27 + sp-dchv Lane C): projected profit is the
	// TOTAL that ranked this tour; fresh cash profit, held-cargo liquidation revenue,
	// and synthetic haul-to-storage DEPOSIT value are reported apart so a laden-hull
	// or pre-positioning plan's margin is not read as pure fresh-trade profit.
	// Fresh cash = total - liquidation - deposit_value (liquidation has no
	// acquisition cost; a deposit books no cash — its value is future contract
	// savings, not revenue).
	freshProfit := plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
	logger.Log("INFO", fmt.Sprintf("Tour planned: %d legs, projected profit %d (fresh %d, liquidation %d, deposit %d) (model %s)", len(plan.Legs), plan.ProjectedProfit, freshProfit, plan.HeldLiquidation, plan.DepositValue, modelVersion), map[string]interface{}{
		"legs": len(plan.Legs), "projected_profit": plan.ProjectedProfit,
		"projected_fresh_profit": freshProfit, "projected_held_liquidation": plan.HeldLiquidation,
		"projected_deposit_value": plan.DepositValue,
		"cph":                     plan.ProjectedCreditsPerHour, "model": modelVersion,
	})

	// Execute plan legs; on degradation, re-plan from current position/cargo (bounded
	// by replanLimit PER TOUR).
	replansLeft := replanLimit
	var cumulativeSpend int64
	for {
		degraded, execErr := h.executePlan(ctx, cmd, plan, response, netBought, &cumulativeSpend, maxSpend, reserve)
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
		budget := remainingSpend(maxSpend, cumulativeSpend)
		plan, err = h.plan(ctx, ship, maxHops, budget, reserve, cmd, modelVersion)
		if err != nil || !plan.Feasible {
			logger.Log("INFO", "Re-plan produced no feasible tour - stopping", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
			})
			break
		}
	}
	return true, "", nil
}

// executePlan flies the legs of a single plan. It returns degraded=true when a
// leg's live prices moved past tolerance (the caller re-plans), and a non-nil error
// only on an operational failure the runner should retry. An unroutable leg (gate
// graph drift) is treated as degradation, not a hard failure.
func (h *RunTourCoordinatorHandler) executePlan(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	plan *routing.TourPlan,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	for legIdx, leg := range plan.Legs {
		ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return false, err
		}
		ship, err = h.legs.travel(ctx, ship, leg.Waypoint, cmd.PlayerID)
		if err != nil {
			if errors.Is(err, gategraph.ErrUnroutable) {
				logger.Log("WARNING", fmt.Sprintf("Leg %d to %s unroutable (gate-graph drift) - degrading to re-plan: %v", legIdx, leg.Waypoint, err), map[string]interface{}{
					"leg": legIdx, "waypoint": leg.Waypoint, "error": err.Error(),
				})
				return true, nil
			}
			return false, fmt.Errorf("travel to leg %d (%s) failed: %w", legIdx, leg.Waypoint, err)
		}
		if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
			return false, fmt.Errorf("dock at leg %d (%s) failed: %w", legIdx, leg.Waypoint, err)
		}

		legDegraded := false
		// Sells before buys (errata): a leg that fills the hold both ways must free
		// space before spending it, and sell tranches are ordered price-ascending.
		for _, trade := range sellsBeforeBuys(leg.Trades) {
			executed, terr := h.executeTrade(ctx, cmd, leg, legIdx, trade, response, netBought, cumulativeSpend, maxSpend, reserve)
			if terr != nil {
				return false, terr
			}
			if !executed {
				legDegraded = true // a skipped trade degrades the leg but a still-good sibling trade may proceed
			}
		}
		response.LegsExecuted++
		if legDegraded {
			return true, nil
		}
	}
	return false, nil
}

// executeTrade live-re-verifies one trade against the plan and, if within tolerance,
// dispatches it. Returns executed=false (a skip) when the live price has degraded past
// tourPriceTolerancePct or cannot be read — the caller degrades the leg and re-plans.
func (h *RunTourCoordinatorHandler) executeTrade(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// sp-dchv Lane C: a DEPOSIT tranche is a haul-to-storage transfer, not a market
	// trade — there is no live market bid to re-verify (its value is the synthetic
	// bid). Route it straight to the warehouse deposit path, BYPASSING the
	// live-price observe + tolerance gate the market trades below run.
	if trade.IsDeposit {
		return h.executeDeposit(ctx, cmd, leg, legIdx, trade, response, netBought)
	}

	live, oerr := h.legs.observeGood(ctx, leg.Waypoint, trade.Good, cmd.PlayerID)
	if oerr != nil {
		logger.Log("WARNING", fmt.Sprintf("No live price for %s at %s - skipping (will re-plan): %v", trade.Good, leg.Waypoint, oerr), map[string]interface{}{
			"good": trade.Good, "waypoint": leg.Waypoint, "error": oerr.Error(),
		})
		return false, nil
	}
	planned := trade.ExpectedUnitPrice
	if planned <= 0 {
		return false, nil
	}
	livePrice := live.PurchasePrice() // sell: what the market pays us
	if trade.IsBuy {
		livePrice = live.SellPrice() // buy: what we pay
	}
	degradationPct := math.Abs(float64(livePrice-planned)) / float64(planned) * 100
	if degradationPct > tourPriceTolerancePct {
		logger.Log("WARNING", fmt.Sprintf("Leg %d %s %s: live %d vs planned %d = %.1f%% moved (> %d%%) - skipping, will re-plan",
			legIdx, tradeSide(trade), trade.Good, livePrice, planned, degradationPct, tourPriceTolerancePct), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live": livePrice, "planned": planned, "degradation_pct": degradationPct,
		})
		return false, nil
	}

	if trade.IsBuy {
		return h.executeBuy(ctx, cmd, leg, legIdx, trade, live, response, netBought, cumulativeSpend, maxSpend, reserve)
	}
	return h.executeSell(ctx, cmd, leg, legIdx, trade, live, response, netBought)
}

func (h *RunTourCoordinatorHandler) executeBuy(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	live *market.TradeGood,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	liveAsk := live.SellPrice()
	if liveAsk <= 0 {
		return false, nil
	}
	units := trade.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv // each transaction ≤ tradeVolume
	}
	if maxSpend > 0 {
		remaining := maxSpend - *cumulativeSpend
		if remaining <= 0 {
			logger.Log("WARNING", "Cumulative tour spend cap reached - skipping buy", map[string]interface{}{
				"good": trade.Good, "cap": maxSpend, "spent": *cumulativeSpend,
			})
			return false, nil
		}
		if affordable := int(remaining / int64(liveAsk)); affordable < units {
			units = affordable
		}
	}
	if units <= 0 {
		return false, nil
	}

	// Working-capital spend floor (RULINGS #4), reusing the delegated guard.
	projectedCost := units * liveAsk
	if h.legs.spendFloorBreached(ctx, projectedCost, int(reserve), &RunTradeRouteCoordinatorResponse{}) {
		logger.Log("WARNING", fmt.Sprintf("Buy of %d %s @ %d would breach working-capital floor %d - skipping", units, trade.Good, liveAsk, reserve), map[string]interface{}{
			"good": trade.Good, "units": units, "ask": liveAsk, "reserve": reserve,
		})
		return false, nil
	}

	plannedAt := h.clock.Now()
	// sp-9mkf: arm the per-tranche buy ceiling at the plan's tolerated ask — the planned
	// basis plus the same tourPriceTolerancePct the leg-level gate above applied. That
	// gate checked only the first live read; this bounds the intra-buy ladder a
	// multi-tranche purchase walks up itself (the D39 stale-ask class), aborting the
	// remainder once a sub-tranche prices past the plan's tolerance.
	planned := trade.ExpectedUnitPrice
	maxAskPerUnit := planned + planned*tourPriceTolerancePct/100
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID, maxAskPerUnit)
	if err != nil {
		return false, fmt.Errorf("purchase of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: buy ceiling aborted %s at %s (live ask %d > ceiling %d) - skipping, will re-plan",
			legIdx, trade.Good, leg.Waypoint, buyResp.CeilingObservedAsk, maxAskPerUnit), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
		})
		return false, nil
	}
	*cumulativeSpend += int64(buyResp.TotalCost)
	response.TotalSpent += int64(buyResp.TotalCost)
	response.TradesExecuted++
	netBought[trade.Good] += buyResp.UnitsAdded
	h.recordLeg(ctx, cmd, leg, legIdx, trade, buyResp.UnitsAdded, realizedUnitPrice(buyResp.TotalCost, buyResp.UnitsAdded), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: bought %d %s at %s (cost %d)", legIdx, buyResp.UnitsAdded, trade.Good, leg.Waypoint, buyResp.TotalCost), nil)
	return true, nil
}

func (h *RunTourCoordinatorHandler) executeSell(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	live *market.TradeGood,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv
	}
	if units <= 0 {
		return false, nil // nothing to sell here (cargo already gone) — not a degrade
	}

	plannedAt := h.clock.Now()
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("sell of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	response.TotalRevenue += int64(sellResp.TotalRevenue)
	response.TradesExecuted++
	netBought[trade.Good] -= sellResp.UnitsSold
	h.recordLeg(ctx, cmd, leg, legIdx, trade, sellResp.UnitsSold, realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: sold %d %s at %s (revenue %d)", legIdx, sellResp.UnitsSold, trade.Good, leg.Waypoint, sellResp.TotalRevenue), nil)
	return true, nil
}

// executeDeposit deposits a haul-to-storage tranche into the home warehouse
// (sp-dchv Lane C) using the gas-proven protocol: ReserveSpaceForDeposit →
// TransferCargo (API) → ConfirmDeposit, releasing the reservation on transfer
// failure. It runs NO live-price re-verify (the value is the synthetic bid, not a
// market price) and books ZERO revenue — a deposit is an inventory transfer, not a
// sale, so no ledger transaction row is written (recordLeg is deliberately NOT
// called) and realized P&L is not inflated; the synthetic savings value is logged
// for observability only.
//
// Honest-completion composure (RULINGS #1 / sp-7yej): a successful deposit
// decrements netBought (the good LEFT the hull into inventory — not stranded). A
// deposit that cannot complete (no warehouse, warehouse full/gone) returns a SKIP
// (executed=false) so the leg degrades and the tour re-plans; the un-deposited
// cargo is then carried as held cargo and the next plan liquidates it at market
// (m5kv) rather than stranding it. An API transfer failure returns an error the
// runner retries (it re-plans cargo-aware from the current hold).
func (h *RunTourCoordinatorHandler) executeDeposit(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.mediator == nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: deposit of %s planned but storage subsystem unwired - degrading to re-plan (held cargo will liquidate)", legIdx, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	op := h.warehouseAt(ctx, cmd.PlayerID, leg.Waypoint)
	if op == nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: no running warehouse at %s for %s deposit - degrading to re-plan (held cargo will liquidate)", legIdx, leg.Waypoint, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if units <= 0 {
		return false, nil // nothing to deposit (cargo already gone) — not a degrade
	}

	// Reserve space atomically, then transfer, then confirm (Lane B / siphon protocol).
	storageShip, reserved, ok := h.storageCoordinator.ReserveSpaceForDeposit(op.ID(), units)
	if !ok || storageShip == nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: warehouse %s has no space for %d %s - degrading to re-plan (held cargo will liquidate at market)", legIdx, op.ID(), units, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "units": units, "warehouse": op.ID(),
		})
		return false, nil // full → degrade → next plan liquidates the held cargo (m5kv)
	}
	if reserved < units {
		units = reserved
	}

	if _, terr := h.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
		FromShip:   cmd.ShipSymbol,
		ToShip:     storageShip.ShipSymbol(),
		GoodSymbol: trade.Good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
	}); terr != nil {
		h.storageCoordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
		return false, fmt.Errorf("deposit transfer of %d %s to warehouse hull %s failed: %w", units, trade.Good, storageShip.ShipSymbol(), terr)
	}
	h.storageCoordinator.ConfirmDeposit(storageShip.ShipSymbol(), trade.Good, units)

	response.TradesExecuted++
	netBought[trade.Good] -= units // left the hull into inventory — not stranded
	savingsValue := units * trade.ExpectedUnitPrice
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: deposited %d %s into warehouse %s (savings value %d, no revenue)", legIdx, units, trade.Good, storageShip.WaypointSymbol(), savingsValue), map[string]interface{}{
		"leg": legIdx, "good": trade.Good, "units": units, "warehouse": op.ID(),
		"storage_ship": storageShip.ShipSymbol(), "savings_value": savingsValue,
		"operation_type": "warehouse_deposit",
	})
	return true, nil
}

// warehouseAt returns the RUNNING warehouse operation parked at waypoint (the
// storage anchor the planner routed a deposit leg to), or nil if none is running
// there (warehouse stopped/gone since plan time — the caller degrades).
func (h *RunTourCoordinatorHandler) warehouseAt(ctx context.Context, playerID int, waypoint string) *storage.StorageOperation {
	if h.warehouseFinder == nil {
		return nil
	}
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	for _, op := range ops {
		if op.OperationType() == storage.OperationTypeWarehouse && op.WaypointSymbol() == waypoint {
			return op
		}
	}
	return nil
}

// plan assembles the market snapshot + era-scoped coordinates over the tour graph
// (home system + fresh gate neighbors) and calls the depth-aware planner. The
// constraint carries the resolved model version so the solver fails closed on a
// mismatch rather than silently using a stale model.
func (h *RunTourCoordinatorHandler) plan(
	ctx context.Context,
	ship *navigation.Ship,
	maxHops int,
	maxSpend, reserve int64,
	cmd *RunTourCoordinatorCommand,
	modelVersion string,
) (*routing.TourPlan, error) {
	allowedSystems := h.tourSystems(ctx, ship, cmd.PlayerID)
	snapshot, waypoints, err := tradingsvc.BuildTourSnapshot(ctx, h.marketRepo, h.waypointRepo, allowedSystems, cmd.PlayerID, h.clock.Now(), maxListingAge)
	if err != nil {
		return nil, err
	}
	// sp-dchv Lane C: assemble haul-to-storage deposit candidates for the planner to
	// price against arb sells. Empty when pre-positioning is off, no warehouse is in
	// the tour graph, or the capital ceiling is unreadable (fail closed) — the tour
	// then plans pure arb, unchanged.
	deposits := h.depositCandidates(ctx, allowedSystems, cmd.PlayerID, reserve)
	cons := routing.TourConstraints{
		MaxHops:               maxHops,
		MinMarginPerUnit:      cmd.MinMargin,
		MaxSnapshotAgeMinutes: int(maxListingAge.Minutes()),
		MaxSpend:              maxSpend,
		WorkingCapitalReserve: reserve,
		AllowedSystems:        allowedSystems,
		ExpectedModelVersion:  modelVersion,
	}
	return h.planner.OptimizeTradeTour(ctx, snapshot, waypoints, h.tourShipState(ship), cons, deposits)
}

// depositCandidates assembles the haul-to-storage deposit sinks for the planner
// (sp-dchv Lane C), resolving the pre-positioning capital ceiling from live
// treasury first. Returns nil (no deposit legs) when pre-positioning is disabled,
// the storage subsystem is unwired, or the live balance is unreadable — all of
// which leave the tour to plan pure arb.
func (h *RunTourCoordinatorHandler) depositCandidates(ctx context.Context, allowedSystems []string, playerID int, reserve int64) []routing.TourDepositCandidate {
	if !h.prePositioning.Enabled {
		return nil // deliberate off-switch — not a silent zero, no verdict
	}
	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.demandMiner == nil {
		// Enabled but a dependency is unwired: a WIRING BUG, not an off-switch —
		// make it LOUD (sp-dchv observability) rather than silently planning pure arb.
		common.LoggerFromContext(ctx).Log("WARNING",
			"Pre-positioning verdict: 0 deposit candidate(s) — enabled but subsystem unwired (storageCoordinator/warehouseFinder/demandMiner nil)",
			map[string]interface{}{
				"storage_coordinator_wired": h.storageCoordinator != nil,
				"warehouse_finder_wired":    h.warehouseFinder != nil,
				"demand_miner_wired":        h.demandMiner != nil,
			})
		return nil
	}
	ceiling, known := h.depositCapitalCeiling(ctx, reserve)
	return tradingsvc.BuildDepositCandidates(
		ctx, h.demandMiner, h.warehouseFinder, h.storageCoordinator,
		allowedSystems, playerID, ceiling, known, h.prePositioning,
	)
}

// depositCapitalCeiling resolves the pre-positioning capital ceiling:
// depositCeilingPct (default 10) percent of LIVE treasury, held JUNIOR to the
// working-capital reserve (never tie up capital that would breach it). Returns
// known=false when the live balance is UNREADABLE — the assembler then offers no
// candidates (fail closed, RULINGS #4: money guards never spend on an unreadable
// balance). The foreign buys the deposits fund still pass the per-buy
// working-capital floor and the cumulative max-spend cap at execution; this
// ceiling is the pre-positioning-specific budget layered on top.
func (h *RunTourCoordinatorHandler) depositCapitalCeiling(ctx context.Context, reserve int64) (int64, bool) {
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
	pct := int64(h.depositCeilingPct)
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

// tourSystems is the default tour graph: the hull's current system plus every system
// one gate hop away with fresh market data (the planner scopes each tour to
// maxTourSystems=2 within this allowed set). Neighbor discovery fails open to
// home-only.
func (h *RunTourCoordinatorHandler) tourSystems(ctx context.Context, ship *navigation.Ship, playerID int) []string {
	home := ship.CurrentLocation().SystemSymbol
	systems := []string{home}
	seen := map[string]bool{home: true}
	for _, n := range h.legs.neighborSystems(ctx, home, playerID) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		systems = append(systems, n)
	}
	return systems
}

func (h *RunTourCoordinatorHandler) tourShipState(ship *navigation.Ship) routing.TourShipState {
	cargo := map[string]int{}
	if c := ship.Cargo(); c != nil {
		for _, item := range c.Inventory {
			cargo[item.Symbol] = item.Units
		}
	}
	fuelCurrent, fuelCapacity := 0, ship.FuelCapacity()
	if f := ship.Fuel(); f != nil {
		fuelCurrent, fuelCapacity = f.Current, f.Capacity
	}
	return routing.TourShipState{
		ShipSymbol:      ship.ShipSymbol(),
		CurrentWaypoint: ship.CurrentLocation().Symbol,
		CurrentSystem:   ship.CurrentLocation().SystemSymbol,
		HoldCapacity:    ship.CargoCapacity(),
		FuelCurrent:     fuelCurrent,
		FuelCapacity:    fuelCapacity,
		EngineSpeed:     ship.EngineSpeed(),
		Cargo:           cargo,
	}
}

// defaultMaxSpend resolves the 25%-of-treasury cap (RULINGS #6) when --max-spend is 0.
// No apiClient / no token / read failure → 0 (no explicit cumulative cap; the per-buy
// working-capital floor still guards every spend).
func (h *RunTourCoordinatorHandler) defaultMaxSpend(ctx context.Context) int64 {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return 0
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0
	}
	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0
	}
	spendCap := int64(agent.Credits) * tourDefaultMaxSpendTreasuryPct / 100
	logger.Log("INFO", fmt.Sprintf("Default tour max-spend = %d (25%% of live treasury %d)", spendCap, agent.Credits), map[string]interface{}{
		"max_spend": spendCap, "treasury": agent.Credits,
	})
	return spendCap
}

// strandedReason reports whether any good the tour bought is still aboard (net
// bought minus sold > 0) — an honest-completion veto. The message names each good,
// its stranded units, and the hull's current location so the strand is greppable
// and hand-recoverable.
func (h *RunTourCoordinatorHandler) strandedReason(ctx context.Context, cmd *RunTourCoordinatorCommand, netBought map[string]int) (string, bool) {
	var parts []string
	for good, net := range netBought {
		if net > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", net, good))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	loc := "unknown"
	if ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); err == nil {
		loc = ship.CurrentLocation().Symbol
	}
	return fmt.Sprintf("stranded cargo: %s still aboard at %s (tour-bought, unsold) - reporting failure", strings.Join(parts, ", "), loc), true
}

func (h *RunTourCoordinatorHandler) recordLeg(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	realizedUnits, realizedUnitPrice int,
	plannedAt time.Time,
) {
	if h.telemetry == nil {
		return
	}
	err := h.telemetry.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID:            cmd.ContainerID,
		ShipSymbol:        cmd.ShipSymbol,
		LegIndex:          legIdx,
		Waypoint:          leg.Waypoint,
		Good:              trade.Good,
		IsBuy:             trade.IsBuy,
		PlannedUnits:      trade.Units,
		RealizedUnits:     realizedUnits,
		PlannedUnitPrice:  trade.ExpectedUnitPrice,
		RealizedUnitPrice: realizedUnitPrice,
		PlannedAt:         plannedAt,
		RealizedAt:        h.clock.Now(),
		PlayerID:          cmd.PlayerID,
	})
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to record tour leg telemetry: %v", err), map[string]interface{}{
			"tour": cmd.ContainerID, "leg": legIdx, "good": trade.Good, "error": err.Error(),
		})
	}
}

// readTourModelVersion reads "<fit_version>@<era>" from the checked-in artifact so the
// constraint binds the planner to the exact fitted model (spec: mismatch → the solver
// fails closed). Any read/parse failure surfaces as an error the caller fails open on.
func readTourModelVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read model artifact: %w", err)
	}
	var art struct {
		FitVersion int    `json:"fit_version"`
		Era        string `json:"era"`
	}
	if err := json.Unmarshal(data, &art); err != nil {
		return "", fmt.Errorf("parse model artifact: %w", err)
	}
	if art.Era == "" {
		return "", fmt.Errorf("model artifact missing era")
	}
	return fmt.Sprintf("%d@%s", art.FitVersion, art.Era), nil
}

// sellsBeforeBuys reorders a leg's trades so every sell precedes every buy, preserving
// relative order within each side (the planner emits them this way; the executor
// enforces it so the hold is freed before it is refilled).
func sellsBeforeBuys(trades []routing.TourTrade) []routing.TourTrade {
	out := make([]routing.TourTrade, 0, len(trades))
	for _, t := range trades {
		if !t.IsBuy {
			out = append(out, t)
		}
	}
	for _, t := range trades {
		if t.IsBuy {
			out = append(out, t)
		}
	}
	return out
}

func remainingSpend(maxSpend, spent int64) int64 {
	if maxSpend <= 0 {
		return 0 // no explicit cap
	}
	if r := maxSpend - spent; r > 0 {
		return r
	}
	return 0
}

func realizedUnitPrice(total, units int) int {
	if units <= 0 {
		return 0
	}
	return total / units
}

func tradeSide(t routing.TourTrade) string {
	if t.IsBuy {
		return "buy"
	}
	return "sell"
}
