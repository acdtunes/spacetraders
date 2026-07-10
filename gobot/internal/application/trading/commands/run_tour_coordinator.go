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
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	// captain leaves --max-spend at 0: 25% of live treasury at launch (RULINGS #6).
	tourDefaultMaxSpendTreasuryPct = 25
)

// RunTourCoordinatorCommand is a ONE-SHOT, captain-directed, guarded multi-hop
// trade-tour run (sp-1ek0): plan a depth-aware tour for THIS hull, fly it leg by
// leg with prices re-verified live at every dock, re-plan at most ReplanLimit times
// when reality drifts past tolerance, and stop. Like the arb one-shot it never
// loops or auto-selects a lane beyond the planner's answer; unlike it, the route is
// dynamically planned, so honest completion is a response VETO (not a Go error) —
// a re-run cannot resume a planner-chosen route.
type RunTourCoordinatorCommand struct {
	ShipSymbol            string
	PlayerID              int
	MaxHops               int   // 0 → maxTourHops
	MaxSpend              int64 // 0 → 25% of live treasury at launch
	MinMargin             int
	ReplanLimit           int // 0 → tourMaxReplansDefault
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
	}
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
	replansLeft := cmd.ReplanLimit
	if replansLeft <= 0 {
		replansLeft = tourMaxReplansDefault
	}
	maxSpend := cmd.MaxSpend
	if maxSpend == 0 {
		maxSpend = h.defaultMaxSpend(ctx) // 0 → no explicit cap (floor still guards)
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}

	plan, err := h.plan(ctx, ship, maxHops, maxSpend, reserve, cmd, modelVersion)
	if err != nil {
		response.TourUnavailable = true
		response.TourUnavailableReason = fmt.Sprintf("tour unavailable: planner error: %v", err)
		logger.Log("WARNING", response.TourUnavailableReason, map[string]interface{}{"error": err.Error()})
		return nil
	}
	if !plan.Feasible {
		response.TourUnavailable = true
		response.TourUnavailableReason = fmt.Sprintf("tour unavailable: %s", plan.InfeasibleReason)
		logger.Log("INFO", response.TourUnavailableReason, map[string]interface{}{
			"reason": plan.InfeasibleReason, "model": modelVersion,
		})
		return nil
	}
	response.LegsPlanned = len(plan.Legs)
	logger.Log("INFO", fmt.Sprintf("Tour planned: %d legs, projected profit %d (model %s)", len(plan.Legs), plan.ProjectedProfit, modelVersion), map[string]interface{}{
		"legs": len(plan.Legs), "projected_profit": plan.ProjectedProfit, "cph": plan.ProjectedCreditsPerHour, "model": modelVersion,
	})

	// Execute plan legs; on degradation, re-plan from current position/cargo (bounded).
	netBought := map[string]int{}
	var cumulativeSpend int64
	for {
		degraded, execErr := h.executePlan(ctx, cmd, plan, response, netBought, &cumulativeSpend, maxSpend, reserve)
		if execErr != nil {
			return execErr
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
			return err
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

	// Honest-completion check: any tour-bought cargo still aboard is a stranded veto.
	if reason, stranded := h.strandedReason(ctx, cmd, netBought); stranded {
		response.CargoStranded = true
		response.CargoStrandedReason = reason
		logger.Log("ERROR", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol})
		return nil
	}

	response.NetProfit = response.TotalRevenue - response.TotalSpent
	logger.Log("INFO", "Tour complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "legs_executed": response.LegsExecuted, "replans": response.Replans,
		"spent": response.TotalSpent, "revenue": response.TotalRevenue, "net": response.NetProfit,
	})
	return nil
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
	buyResp, err := h.legs.purchase(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("purchase of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	*cumulativeSpend += int64(buyResp.TotalCost)
	response.TotalSpent += int64(buyResp.TotalCost)
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
	netBought[trade.Good] -= sellResp.UnitsSold
	h.recordLeg(ctx, cmd, leg, legIdx, trade, sellResp.UnitsSold, realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: sold %d %s at %s (revenue %d)", legIdx, sellResp.UnitsSold, trade.Good, leg.Waypoint, sellResp.TotalRevenue), nil)
	return true, nil
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
	cons := routing.TourConstraints{
		MaxHops:               maxHops,
		MinMarginPerUnit:      cmd.MinMargin,
		MaxSnapshotAgeMinutes: int(maxListingAge.Minutes()),
		MaxSpend:              maxSpend,
		WorkingCapitalReserve: reserve,
		AllowedSystems:        allowedSystems,
		ExpectedModelVersion:  modelVersion,
	}
	return h.planner.OptimizeTradeTour(ctx, snapshot, waypoints, h.tourShipState(ship), cons)
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
