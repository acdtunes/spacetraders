package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// defaultMaxVisits bounds the circuit loop so a lane whose bid never decays (or a
// mispriced fake) can never spin forever. The bid-floor discipline is the real
// stop; this is only a safety rail.
const defaultMaxVisits = 50

// tradeRouteDockRetryLimit bounds how many times h.dock resyncs the ship from the
// API and re-issues the dock while waiting for the nav-cache race to clear (the
// arrival event flipping a stale IN_TRANSIT to IN_ORBIT). Bounded so a genuinely
// undockable ship can never spin forever — it aborts the circuit cleanly with the
// verbatim cause instead (sp-ynuf, mirroring the goods-factory dock race sp-n7yp).
const tradeRouteDockRetryLimit = 3

// defaultMaxCircuits bounds the OUTER loop (sp-wlev scope amendment): how many
// distinct lanes a single run will commit to before stopping regardless of
// margin. The bid-floor discipline and starvation detection are the real stops;
// this is only a safety rail against a persistently-wrong ranking cycling
// forever.
const defaultMaxCircuits = 20

// noProgressStarvationLimit bounds how many consecutive circuits may commit to
// a lane and fly zero visits before the run calls it starvation and stops. One
// zero-visit circuit can be a transient live-recheck miss; several in a row
// means the system has nothing left worth absorbing a hull into.
const noProgressStarvationLimit = 3

// defaultWorkingCapitalReserve is the fallback hard spend floor (sp-bp6f): a
// circuit must not execute a buy that would drop LIVE treasury below this
// line. Sized to the exact level the 2026-07-09 incident called "danger" —
// treasury bottomed at 43,041 and briefly went negative (-30,537) before the
// captain intervened — so a circuit now stops BEFORE crossing back into that
// zone instead of the failure being discovered after the fact. Overridable
// per-run via RunTradeRouteCoordinatorCommand.WorkingCapitalReserve (0 → this
// default) and via the daemon's "working_capital_reserve" launch-config key
// (mirroring max_visits' own "0 → coordinator default" convention), so the
// captain can raise or lower the floor operationally — e.g. to the current
// contract+factory working-capital need the incident notes called out —
// without a redeploy.
const defaultWorkingCapitalReserve = 50000

// negativeMarginAbortVisits bounds how many visits a circuit's OWN realized
// margin (this circuit's sell revenue minus its buy cost, reset to zero every
// fresh lane commitment) may run negative before the circuit aborts (sp-bp6f
// fix #2). This is the exact incident shape: repeated tranches walk the
// source ask up while the destination bid gets crushed, so the STALE ranked
// spread still looks positive long after the circuit's actual fills have
// turned loss-making. Small enough to catch the pattern within a circuit's
// early visits before it compounds; not so small that a single noisy fill
// (e.g. one visit's fees) trips it.
const negativeMarginAbortVisits = 3

// exitReason* enumerates why the outer circuit loop stopped (sp-wlev: circuits
// must loop until a margin-exit or starvation-exit, never one-and-done — a
// hull that flies one lane and idles wastes duty cycle, the 20x gap this
// feature targets).
const (
	// exitReasonMarginExhausted: a fresh re-scan found nothing that clears the
	// discipline floor — every lane in the system (and its jump-gate neighbors)
	// is currently sub-floor.
	exitReasonMarginExhausted = "margin_exhausted"
	// exitReasonStarvation: repeated re-scans kept committing to a lane that flew
	// zero visits — the system has stopped absorbing new circuits.
	exitReasonStarvation = "starvation"
	// exitReasonMaxCircuits: the outer safety bound (defaultMaxCircuits) tripped
	// while the run was still productive.
	exitReasonMaxCircuits = "max_circuits"
	// exitReasonError: a navigate/dock/buy/sell leg failed mid-circuit; see
	// AbortReason for the verbatim cause.
	exitReasonError = "error"
	// exitReasonStaleAsk: the live source ask moved beyond tolerance of the
	// ranked basis just before the first buy; see RankedSourceAsk/LiveSourceAsk.
	exitReasonStaleAsk = "stale_ask"
	// exitReasonNoLanes: the market cache had nothing to rank at all.
	exitReasonNoLanes = "no_lanes"
	// exitReasonSpendFloor: a circuit was about to buy a tranche that would drop
	// live treasury below the working-capital reserve floor (sp-bp6f); see
	// SpendFloorAbort/TreasuryAtAbort/ReserveFloor.
	exitReasonSpendFloor = "spend_floor"
	// exitReasonNegativeMargin: a circuit's own realized margin ran negative for
	// negativeMarginAbortVisits consecutive visits - the lane is losing money on
	// its actual recent fills, not just a stale ranked spread (sp-bp6f); see
	// NegativeMarginAbort/RealizedCircuitMargin.
	exitReasonNegativeMargin = "negative_margin"
)

// MarketRefresher live-refreshes one waypoint's market from the API into the cache.
// The coordinator uses it once, before the first buy, to re-read the source ask live
// and abort if it has run away from the stale basis the lane was ranked on (hazard b,
// sp-2sam). Kept as a narrow port (not an import of the ship package) to avoid a cycle
// — the CLI composition root injects the concrete MarketScanner. A nil refresher
// disables the guard, so callers that cannot scan (e.g. tests) simply skip it.
type MarketRefresher interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

// RunTradeRouteCoordinatorCommand asks the coordinator to fly one idle hull
// through the top-ranked arbitrage circuit in a system until the margin dies.
type RunTradeRouteCoordinatorCommand struct {
	ShipSymbol   string
	SystemSymbol string
	PlayerID     int
	ContainerID  string
	MaxVisits    int // 0 → defaultMaxVisits
	// WorkingCapitalReserve is the hard spend floor (sp-bp6f): 0 → defaultWorkingCapitalReserve.
	WorkingCapitalReserve int
}

// RunTradeRouteCoordinatorResponse reports the realised circuit economics. Net
// profit is revenue − acquisition cost; fuel is a live cost outside this ledger.
type RunTradeRouteCoordinatorResponse struct {
	ShipSymbol     string
	Good           string
	SourceWaypoint string
	DestWaypoint   string
	Visits         int
	UnitsTraded    int
	TotalCost      int
	TotalRevenue   int
	NetProfit      int
	Completed      bool
	Error          string
	// NoDisciplinedLane is set when profitable lanes were ranked but NONE cleared the
	// bid-floor discipline (trading.MinBidMargin), so the circuit flew nothing by
	// design. It distinguishes a disciplined "nothing worth flying" from "no lane at
	// all" — both leave Good=="" and Visits==0 — so the caller reports the reason
	// instead of a silent zero-visit success (sp-sh6w).
	NoDisciplinedLane bool
	// BestSubFloorSpread is the highest per-unit spread among the ranked lanes when
	// NoDisciplinedLane is set: how close the best standing lane came to the floor.
	BestSubFloorSpread int
	// StaleAskAbort is set when a live re-read of the source ask (taken at the source
	// before the first buy) had moved beyond trading.StaleAskMovePercent from the basis
	// the lane was ranked on. The lane's ranked spread was stale, so the run aborted
	// before buying rather than risk a bad fill (sp-2sam hazard b, a -196k precedent).
	// A selected lane that aborts this way is NOT a silent zero — it reports WHY.
	StaleAskAbort bool
	// RankedSourceAsk and LiveSourceAsk are the basis the lane was ranked on and the
	// live ask read at the source, populated when StaleAskAbort is set.
	RankedSourceAsk int
	LiveSourceAsk   int
	// SpendFloorAbort is set when a circuit was about to buy a tranche that would
	// drop live treasury below the working-capital reserve floor (sp-bp6f) — the
	// buy was skipped and the circuit stopped BEFORE spending past the line,
	// rather than the breach being discovered after the fact. TreasuryAtAbort and
	// ReserveFloor are the live credits observed and the reserve in effect; a
	// fail-closed abort (the live treasury read itself failed) leaves
	// TreasuryAtAbort at zero since no live figure was actually obtained.
	SpendFloorAbort bool
	TreasuryAtAbort int
	ReserveFloor    int
	// NegativeMarginAbort is set when THIS circuit's own realized margin (its
	// sell revenue minus its buy cost, not the cross-run cumulative totals) ran
	// negative for negativeMarginAbortVisits consecutive visits — the lane is
	// actively losing money on its recent fills (sp-bp6f fix #2).
	// RealizedCircuitMargin is that circuit-local net margin when it was set.
	NegativeMarginAbort   bool
	RealizedCircuitMargin int
	// AbortReason explains why a SELECTED lane (Good set) flew fewer visits than the
	// margin would allow — a navigate/dock/buy/sell leg failed mid-circuit. It exists
	// because three successive zero-visit bugs (r3cl, sh6w, sp-2sam) each needed a live
	// re-run to discover WHY the loop stopped: the failing leg's reason was logged but
	// never surfaced to the caller, so the printed result was a bare 'Visits: 0'. With
	// this, the next occurrence is self-diagnosing. Empty on a clean margin-death stop.
	AbortReason string
	// ExitReason explains why the OUTER circuit loop stopped (sp-wlev scope
	// amendment: circuits loop until a margin-exit or starvation-exit, never
	// one-and-done). See the exitReason* constants for the full set.
	ExitReason string
	// Circuits counts how many distinct lanes the outer loop committed to and
	// attempted this run, regardless of whether each one was productive.
	Circuits int
}

// RunTradeRouteCoordinatorHandler runs a pure-arbitrage circuit on a single hull:
// it loads the named ship (already claimed for it by the daemon container runner via
// the ship_symbol metadata, so release-on-death is the runner's job — sp-zewt), ranks
// lanes from cache (trading.RankSpreads), selects the deepest lane that clears the
// bid-floor discipline (trading.FirstDisciplinedLane — so it never picks a top-capped
// lane the executor would refuse), then flies it in disciplined tranches — ≤18u/visit,
// and only while the destination bid clears basis+1000 (trading.MarginAlive) — looping
// until the margin dies.
//
// It reuses the same driven ports as the fabrication coordinators (mediator for
// navigate/dock/purchase/sell, ship + market repositories), so ship movement and
// trades go through the exact command handlers the daemon uses — in the daemon that
// means NavigateRouteCommand resolves to the RouteExecutor-backed handler (orbit →
// refuel → NavigateDirect → arrival events), which is why running as a container
// subsumes the CLI runner's hand-rolled in-process nav and its patches (sp-2sam
// self-collision, sp-sj7p orbit-before-nav): the container never spawns a re-claiming
// child navigate.
type RunTradeRouteCoordinatorHandler struct {
	mediator        common.Mediator
	shipRepo        navigation.ShipRepository
	marketRepo      market.MarketRepository
	marketRefresher MarketRefresher // optional; nil disables the live stale-ask guard
	clock           shared.Clock    // used only for the cross-system jump-cooldown wait (sp-wlev)
	// apiClient is used only to live-read treasury for the working-capital spend
	// floor (sp-bp6f). Optional; nil disables the guard entirely (fails OPEN,
	// mirroring marketRefresher's own optional-port contract) rather than
	// defaulting to a real client the way clock defaults to RealClock — a caller
	// that cannot supply one (e.g. most tests) simply runs without the guard.
	apiClient domainPorts.APIClient
}

// NewRunTradeRouteCoordinatorHandler wires the coordinator. It does not own a
// container repository: the daemon container runner claims and releases the
// hull through the normal container lifecycle (ship_symbol metadata → createShipAssignments
// on start, releaseShipAssignments on every terminal path), so this handler only reads
// ship/market state and flies the circuit. A nil marketRefresher disables the live
// stale-ask guard (the circuit still runs on the ranked basis); the daemon injects a
// real MarketScanner so the guard is active. A nil clock defaults to shared.RealClock;
// tests inject a shared.MockClock so the cross-system jump-cooldown wait (sp-wlev) is
// instant instead of a real sleep. A nil apiClient disables the working-capital
// spend-floor guard (sp-bp6f) — the circuit runs without live-checking treasury
// before each buy; the daemon injects the real APIClient so the guard is active in
// production, the same fail-open contract marketRefresher already uses.
func NewRunTradeRouteCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *RunTradeRouteCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTradeRouteCoordinatorHandler{
		mediator:        mediator,
		shipRepo:        shipRepo,
		marketRepo:      marketRepo,
		marketRefresher: marketRefresher,
		clock:           clock,
		apiClient:       apiClient,
	}
}

// Handle executes the trade-route command.
func (h *RunTradeRouteCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTradeRouteCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunTradeRouteCoordinatorResponse{ShipSymbol: cmd.ShipSymbol}

	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}

	response.Completed = true
	return response, nil
}

func (h *RunTradeRouteCoordinatorHandler) execute(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	response *RunTradeRouteCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID

	containerID := cmd.ContainerID
	if containerID == "" {
		containerID = utils.GenerateContainerID("trade-route", cmd.ShipSymbol)
	}
	// Link buy/sell ledger transactions to this trade-route operation.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(containerID, "trade_route"))

	// Step 1: load the hull the container runner already claimed for this circuit. The
	// idle-gap discipline (only take a genuinely idle hull, never steal one mid-task)
	// is enforced at the container-start boundary (DaemonServer.StartTradeRoute checks
	// IsIdle before persisting the container); by the time this runs the runner has
	// assigned the hull to this container via the ship_symbol metadata, and it will be
	// force-released on every terminal path (completion, crash, cancel) — so this
	// handler neither claims nor releases (sp-zewt retires the vjwb orphan-on-death).
	ship, err := h.loadShip(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return err
	}

	// Step 2+3: loop the circuit — sp-wlev scope amendment. A hull that flies one
	// lane to its bid-floor and then idles wastes duty cycle (the 20x gap this
	// feature targets is DUTY CYCLE, not per-trade economics), so re-rank lanes
	// from fresh cache after every circuit and keep committing to whichever lane
	// still clears the discipline floor until margins collapse everywhere, the
	// system stops absorbing new circuits (starvation), a leg errors, a stale ask
	// aborts, or the safety bound trips.
	noProgressStreak := 0
	exitReason := ""

	for circuitNum := 0; circuitNum < defaultMaxCircuits; circuitNum++ {
		lanes, err := h.scanLanes(ctx, cmd.SystemSymbol, playerID, ship.CargoCapacity())
		if err != nil {
			return fmt.Errorf("failed to scan arbitrage lanes: %w", err)
		}
		if len(lanes) == 0 {
			exitReason = exitReasonNoLanes
			logger.Log("INFO", "No profitable arbitrage lane in cache - releasing ship", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"system":      cmd.SystemSymbol,
			})
			break
		}

		// The scan ranks lanes by volume-capped spread and deliberately keeps sub-floor
		// lanes visible (it is an observation tool). The executor, however, refuses any
		// lane whose per-unit spread is below MinBidMargin (runCircuit's MarginAlive gate)
		// — so the top capped-spread lane can be one that flies ZERO visits. Select the
		// DEEPEST lane that actually clears the discipline floor, so a selected lane always
		// flies >=1 visit instead of a silent zero-visit run (sp-sh6w).
		lane, ok := trading.FirstDisciplinedLane(lanes)
		if !ok {
			exitReason = exitReasonMarginExhausted
			// Only report "nothing to fly" if this run never flew anything at all — a
			// later re-scan finding nothing (after earlier circuits DID trade) is a clean
			// margin-exhausted stop, not the "no lane at all" case (sp-wlev).
			if response.Good == "" {
				response.NoDisciplinedLane = true
				response.BestSubFloorSpread = bestSpreadPerUnit(lanes)
			}
			logger.Log("INFO", "No lane clears the discipline floor - releasing ship without trading", map[string]interface{}{
				"ship_symbol":           cmd.ShipSymbol,
				"system":                cmd.SystemSymbol,
				"floor":                 trading.MinBidMargin,
				"best_sub_floor_spread": bestSpreadPerUnit(lanes),
				"ranked_lane_count":     len(lanes),
				"circuits_flown":        response.Circuits,
			})
			break
		}
		response.Good = lane.Good
		response.SourceWaypoint = lane.SourceWaypoint
		response.DestWaypoint = lane.DestWaypoint
		response.Circuits++

		// sp-q1ca: this line used to print no structured payload — the captain could
		// not tell which lane a daemon picked, or whether cross-system lanes were even
		// scanned, without inferring it from nav destinations. laneLogPayload carries
		// the SELECTED lane's full identity (both endpoints' waypoint+system, margin,
		// cross-system flag); laneLogCandidates attaches the top-ranked shortlist so a
		// penalized-but-present cross-system lane (see rankLanesWithGatePenalty) is
		// verifiable in the log even when a home lane wins the selection.
		selectionPayload := laneLogPayload(lane)
		selectionPayload["ship_symbol"] = cmd.ShipSymbol
		selectionPayload["circuit"] = response.Circuits
		selectionPayload["candidates"] = laneLogCandidates(lanes)
		logger.Log("INFO", "Selected top disciplined arbitrage lane", selectionPayload)

		visitsBefore := response.Visits
		ship = h.runCircuit(ctx, cmd, lane, ship, response)

		if response.AbortReason != "" {
			exitReason = exitReasonError
			break
		}
		if response.StaleAskAbort {
			exitReason = exitReasonStaleAsk
			break
		}
		if response.SpendFloorAbort {
			exitReason = exitReasonSpendFloor
			break
		}
		if response.NegativeMarginAbort {
			exitReason = exitReasonNegativeMargin
			break
		}
		if response.Visits == visitsBefore {
			// The ranked lane flew zero visits (e.g. the live re-check killed it
			// immediately). Bound how many consecutive commitments can go nowhere
			// before calling it starvation, so a persistently-wrong ranking can't
			// spin forever re-selecting the same dead lane.
			noProgressStreak++
			if noProgressStreak >= noProgressStarvationLimit {
				exitReason = exitReasonStarvation
				break
			}
			continue
		}
		noProgressStreak = 0
	}
	if exitReason == "" {
		exitReason = exitReasonMaxCircuits
	}
	response.ExitReason = exitReason

	response.NetProfit = response.TotalRevenue - response.TotalCost
	logger.Log("INFO", "Trade-route run complete", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"good":          response.Good,
		"circuits":      response.Circuits,
		"exit_reason":   response.ExitReason,
		"visits":        response.Visits,
		"units_traded":  response.UnitsTraded,
		"total_cost":    response.TotalCost,
		"total_revenue": response.TotalRevenue,
		"net_profit":    response.NetProfit,
	})
	return nil
}

// runCircuit flies disciplined tranches of the lane until the destination bid
// falls below basis+1000, tradable volume dries up, or the safety bound trips.
// Each visit re-observes both markets so a decaying importer bid ends the loop.
// It returns the ship pointer current as of wherever the circuit ended: travel()
// may return a freshly-reloaded pointer after a cross-system jump, so the outer
// loop (sp-wlev) must carry this forward into its next circuit rather than reuse
// its own now-stale reference.
func (h *RunTradeRouteCoordinatorHandler) runCircuit(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
) *navigation.Ship {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID
	maxVisits := cmd.MaxVisits
	if maxVisits <= 0 {
		maxVisits = defaultMaxVisits
	}
	reserve := cmd.WorkingCapitalReserve
	if reserve <= 0 {
		reserve = defaultWorkingCapitalReserve
	}

	held := 0
	circuitNetMargin := 0 // this circuit's own sell revenue minus its own buy cost (sp-bp6f fix #2)
	for i := 0; i < maxVisits; i++ {
		// Re-observe both ends: basis (source ask we pay) and the live dest bid.
		srcGood, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Source market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Destination market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.DestWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship
		}

		basis := srcGood.SellPrice()       // ask: what we PAY buying from the source
		destBid := dstGood.PurchasePrice() // bid: what we RECEIVE selling to the dest

		// Bid-floor discipline: the edge is gone once the dest bid stops clearing
		// basis+1000. Stop here rather than grind the spread to nothing.
		if !trading.MarginAlive(destBid, basis) {
			logger.Log("INFO", "Margin dead - stopping circuit at the bid-floor", map[string]interface{}{
				"good": lane.Good, "dest_bid": destBid, "basis": basis, "floor": basis + trading.MinBidMargin,
			})
			return ship
		}

		// Size the tranche to the hull's AVAILABLE hold, not its total capacity: an idle
		// hull is not necessarily empty (a factory hauler benched mid-task, a pool hull
		// with leftover cargo), and the buy executor refuses any tranche larger than
		// AvailableCargoSpace. Sizing by CargoCapacity would overshoot the free hold on a
		// non-empty hull and get the buy rejected — a distinct zero-visit path from the
		// sp-2sam root cause (the navigate self-collision, fixed in the CLI runner), hardened
		// here so a residual-cargo hull still flies. AvailableCargoSpace already nets out the
		// residual cargo; held is this run's own bought-not-yet-sold units on top.
		cargoSpace := ship.AvailableCargoSpace() - held
		buyUnits := trading.VisitTranche(srcGood.TradeVolume(), cargoSpace)
		if buyUnits <= 0 {
			logger.Log("INFO", "No tranche to buy (volume or hold exhausted) - stopping circuit", map[string]interface{}{
				"good": lane.Good, "source_volume": srcGood.TradeVolume(), "cargo_space": cargoSpace,
			})
			return ship
		}

		// Working-capital spend floor (sp-bp6f): refuse the buy BEFORE traveling,
		// docking, or purchasing if it would drop live treasury below the reserve.
		// Must run here, ahead of Leg 1 committing to anything — a circuit that
		// already docked and spent before checking has defeated the floor's whole
		// purpose. Uses basis (this visit's live source ask, re-observed above),
		// not lane.SourceAsk (the STALE ranked basis), so the projected cost
		// reflects what the circuit is actually about to pay right now.
		if h.spendFloorBreached(ctx, buyUnits*basis, reserve, response) {
			return ship
		}

		// Leg 1: buy a tranche at the source (exporter). A cross-system lane
		// jumps instead of navigating (sp-wlev); travel reloads the ship
		// afterward so this pointer reflects its post-jump state.
		ship, err = h.travel(ctx, ship, lane.SourceWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to source %s failed: %v", lane.SourceWaypoint, err)
			logger.Log("WARNING", "Travel to source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at source %s failed: %v", lane.SourceWaypoint, err)
			// Put the verbatim cause in the MESSAGE — the container-log renderer drops the
			// metadata map, so a cause hidden in {"error": ...} never reaches an operator
			// (sp-ynuf defect 1, the sp-iqyq class). A blind dock failure now names itself.
			logger.Log("WARNING", fmt.Sprintf("Dock at source %s failed: %v - ending circuit", lane.SourceWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship
		}

		// Live-verify the ranked basis before the FIRST buy (hazard b): the lane was
		// ranked from a market cache that can be many minutes stale. Now that the hull
		// is docked at the source (the API returns live prices only with a ship present),
		// re-read the source ask and abort if it has run away from the basis the lane
		// was ranked on — buying on a stale basis has realised a large loss (a -196k
		// precedent). Only the first visit re-verifies; later visits already re-observe.
		if i == 0 && h.staleAskAborts(ctx, lane, playerID, response) {
			return ship
		}

		buyResp, err := h.purchase(ctx, ship.ShipSymbol(), lane.Good, buyUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("purchase of %d %s at source %s failed: %v", buyUnits, lane.Good, lane.SourceWaypoint, err)
			logger.Log("WARNING", "Purchase failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship
		}
		held += buyResp.UnitsAdded
		response.TotalCost += buyResp.TotalCost
		circuitNetMargin -= buyResp.TotalCost

		// Leg 2: sell what we hold at the destination (importer). A
		// cross-system lane jumps instead of navigating (sp-wlev).
		ship, err = h.travel(ctx, ship, lane.DestWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			logger.Log("WARNING", "Travel to destination failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return ship
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			// Verbatim cause in the MESSAGE (not the dropped metadata field) so a blind
			// dock-at-destination failure names itself — same defect-1 fix as the source leg.
			logger.Log("WARNING", fmt.Sprintf("Dock at destination %s failed (cargo aboard): %v - ending circuit", lane.DestWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship
		}
		sellUnits := trading.VisitTranche(dstGood.TradeVolume(), held)
		if sellUnits <= 0 {
			// The importer has no tradable volume this tick while we hold cargo: not a
			// clean margin-death, so surface it rather than return silently (the one
			// early-return that used to vanish without a trace).
			response.AbortReason = fmt.Sprintf("destination %s has no sellable volume for %s while holding %d units", lane.DestWaypoint, lane.Good, held)
			logger.Log("INFO", "No sellable tranche at destination (importer volume exhausted) - ending circuit with cargo aboard", map[string]interface{}{
				"good": lane.Good, "dest_volume": dstGood.TradeVolume(), "held": held,
			})
			return ship
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, sellUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("sell of %d %s at destination %s failed (cargo aboard): %v", sellUnits, lane.Good, lane.DestWaypoint, err)
			logger.Log("WARNING", "Sell failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return ship
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		circuitNetMargin += sellResp.TotalRevenue
		response.Visits++

		// Per-circuit negative-margin abort (sp-bp6f fix #2): this circuit's own
		// realized fills - not the stale ranked spread - are what matter once
		// trading has actually started. Gate on i+1 (visit count) so one noisy
		// early fill can't trip it; negativeMarginAbortVisits consecutive visits
		// of net-negative realized margin means the lane itself has turned
		// loss-making, e.g. the incident's repeated tranches walking the source
		// ask up while the destination bid gets crushed.
		if i+1 >= negativeMarginAbortVisits && circuitNetMargin < 0 {
			response.NegativeMarginAbort = true
			response.RealizedCircuitMargin = circuitNetMargin
			logger.Log("WARNING", "Circuit realized margin ran negative - aborting before it compounds", map[string]interface{}{
				"good": lane.Good, "visits": response.Visits, "realized_margin": circuitNetMargin,
			})
			return ship
		}
	}

	logger.Log("INFO", "Trade-route hit the max-visit safety bound", map[string]interface{}{
		"good": lane.Good, "max_visits": maxVisits,
	})
	return ship
}

// spendFloorBreached live-checks whether spending projectedCost right now would
// drop treasury below reserve (sp-bp6f), setting the abort fields on response
// and returning true if so — the caller must not proceed with the buy.
//
// Fails OPEN when no apiClient is wired (h.apiClient == nil): the guard is
// simply unavailable, the same optional-port contract staleAskAborts already
// uses for marketRefresher, so callers that cannot supply a live API client
// (most tests) run without it rather than being forced to fake one.
//
// Fails CLOSED on every live-read failure (an unresolvable player token, or the
// GetAgent call itself erroring): unlike staleAskAborts' infrastructure-gap
// tolerance, a guard whose entire job is stopping the treasury from going
// negative must never let a buy through just because it went blind. An API
// hiccup here aborts the circuit instead of silently trading past the floor.
func (h *RunTradeRouteCoordinatorHandler) spendFloorBreached(
	ctx context.Context,
	projectedCost int,
	reserve int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return false
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Could not resolve player token for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", "Could not read live treasury for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if agentData.Credits-projectedCost < reserve {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting circuit before spending", map[string]interface{}{
			"treasury": agentData.Credits, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = agentData.Credits
		response.ReserveFloor = reserve
		return true
	}

	return false
}

// staleAskAborts live-verifies the source ask before the first buy and reports
// whether the circuit must abort because the ask has moved beyond
// trading.StaleAskMovePercent from the basis the lane was ranked on (hazard b). The
// lane is ranked from a cache that can be many minutes stale; executing on a moved
// basis has realised large losses. It refreshes the source market from the API (the
// hull is docked there, so the API returns live prices), re-reads the ask, and
// compares it to lane.SourceAsk.
//
// Fail-open on infrastructure gaps, fail-closed only on a CONFIRMED move: with no
// refresher wired, or when the refresh/read itself fails, it proceeds on the ranked
// basis (a transient scan hiccup must not strand an otherwise-good circuit). Only a
// live ask that is actually present AND beyond tolerance aborts the run.
func (h *RunTradeRouteCoordinatorHandler) staleAskAborts(
	ctx context.Context,
	lane trading.ArbitrageLane,
	playerID int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.marketRefresher == nil {
		return false
	}

	if err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID), lane.SourceWaypoint); err != nil {
		logger.Log("WARNING", "Could not refresh source market to live-verify basis - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveSrc, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
	if err != nil {
		logger.Log("WARNING", "Could not read live source ask after refresh - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveAsk := liveSrc.SellPrice()
	if trading.AskMovedBeyondTolerance(liveAsk, lane.SourceAsk) {
		response.StaleAskAbort = true
		response.RankedSourceAsk = lane.SourceAsk
		response.LiveSourceAsk = liveAsk
		logger.Log("WARNING", "Source ask moved beyond tolerance since the lane was ranked - aborting circuit before first buy", map[string]interface{}{
			"good": lane.Good, "source": lane.SourceWaypoint,
			"ranked_ask": lane.SourceAsk, "live_ask": liveAsk, "tolerance_pct": trading.StaleAskMovePercent,
		})
		return true
	}

	logger.Log("INFO", "Live-verified source ask within tolerance - proceeding with the circuit", map[string]interface{}{
		"good": lane.Good, "source": lane.SourceWaypoint, "ranked_ask": lane.SourceAsk, "live_ask": liveAsk,
	})
	return false
}

// loadShip loads the hull the daemon container runner already claimed for this
// circuit. It does NOT claim or release: the runner owns the assignment lifecycle
// (createShipAssignments on start via the container's ship_symbol metadata,
// releaseShipAssignments on completion/crash/cancel), so the hull is force-released
// on every terminal path without this handler touching it (sp-zewt). The idle-gap
// discipline — only fly a genuinely idle hull, never steal one — is enforced ahead of
// time at DaemonServer.StartTradeRoute, before the container is persisted.
func (h *RunTradeRouteCoordinatorHandler) loadShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	return ship, nil
}

// scanLanes builds cross-market listings for the system from cache, plus (sp-wlev)
// every system one jump-gate hop away, and ranks them all in a single pass so
// gate-crossing lanes can surface alongside home-system ones. Aggregating BEFORE
// ranking (rather than ranking each system separately) is what lets a good that
// only exports in one system and only imports in another form a lane at all —
// trading.RankSpreads pairs listings purely by good and waypoint, indifferent to
// which system either side is in. Cross-system candidates are then penalized via
// rankLanesWithGatePenalty to reflect the jump+cooldown time cost a raw per-unit
// spread can't see.
//
// Neighbor discovery is fail-open: a system with no jump gate, or a discovery
// query that errors, simply contributes no extra listings — never an aborted
// scan. One hop only (no recursive multi-hop chase): out of scope for sp-wlev.
//
// Multi-daemon lane-dedupe semantics (sp-q1ca, confirmed): scanLanes has NO
// awareness of other concurrently-running trade-route daemons or their active
// circuits — there is no registry of in-flight lanes and no query of what any
// other hull is doing. Two daemons started at the same instant against the same
// system WILL rank identically and both select the same top lane; there is no
// explicit dedupe. Divergence observed in practice (e.g. one hull landing on a
// different lane than another started moments later) is an EMERGENT side effect
// of the shared market cache, not deliberate coordination: after a hull finishes
// a buy/sell batch, the handler issues one extra live GET of that waypoint's
// market and overwrites the cached rows (see cargo_transaction.go's
// refreshMarketData -> MarketScanner.ScanAndSaveMarket -> UpsertMarketData),
// synchronously before the command returns. h.collectSystemListings reads that
// same cache with a plain uncached query, so a daemon that scans shortly AFTER
// another hull has already traded into a lane's destination sees that lane's
// decayed bid and naturally re-ranks it lower — no locking, no CAS, last writer
// wins. Two daemons racing to scan at the SAME moment (before either has traded)
// can still legitimately pick the same lane; only a bid-floor "margin died" stop
// on one of them or a later rescan resolves the collision, not the ranker itself.
func (h *RunTradeRouteCoordinatorHandler) scanLanes(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	shipCapacity int,
) ([]trading.ArbitrageLane, error) {
	listings, err := h.collectSystemListings(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	for _, neighbor := range h.neighborSystems(ctx, systemSymbol, playerID) {
		neighborListings, err := h.collectSystemListings(ctx, neighbor, playerID)
		if err != nil {
			continue // fail-open: an unreadable neighbor system just yields fewer lanes
		}
		listings = append(listings, neighborListings...)
	}

	// Hold-vs-absorption weighting (sp-pnx0) and the cross-system jump-gate
	// penalty are folded into ONE scoring pass inside rankLanesWithGatePenalty,
	// not chained as two sequential re-rankings: both are "recompute-from-
	// scratch" rankers that derive their score purely from each lane's own
	// fields, ignoring input order, so composing them as funcB(funcA(lanes))
	// would let funcB silently discard funcA's reordering. Start from the
	// plain trading.RankSpreads order (not RankSpreadsForHold) since hold-fit
	// weighting is applied here via shipCapacity instead.
	return rankLanesWithGatePenalty(trading.RankSpreads(listings), shipCapacity), nil
}

// collectSystemListings reads every cached market in one system into flat
// GoodListing rows, the shared building block scanLanes aggregates across the
// home system and its jump-gate neighbors before ranking.
func (h *RunTradeRouteCoordinatorHandler) collectSystemListings(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]trading.GoodListing, error) {
	waypoints, err := h.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list markets in %s: %w", systemSymbol, err)
	}

	var listings []trading.GoodListing
	for _, wp := range waypoints {
		mkt, err := h.marketRepo.GetMarketData(ctx, wp, playerID)
		if err != nil || mkt == nil {
			continue // an unreadable market simply doesn't contribute lanes
		}
		for _, g := range mkt.TradeGoods() {
			listings = append(listings, trading.GoodListing{
				Good:      g.Symbol(),
				Waypoint:  mkt.WaypointSymbol(),
				TradeType: string(g.TradeType()),
				Bid:       g.PurchasePrice(), // market BUY column = what we receive selling TO it
				Ask:       g.SellPrice(),     // market SELL column = what we pay buying FROM it
				Supply:    derefString(g.Supply()),
				Activity:  derefString(g.Activity()),
				Volume:    g.TradeVolume(),
			})
		}
	}
	return listings, nil
}

// neighborSystems returns the systems one jump directly away from systemSymbol's
// own jump gate, via the already-registered GetJumpGateConnectionsQuery. Any
// failure (no gate in the system, an API error, no player context) fails open to
// an empty neighbor set rather than surfacing an error — a multi-system trade
// route degrades to a home-system-only one, it never aborts the scan.
func (h *RunTradeRouteCoordinatorHandler) neighborSystems(ctx context.Context, systemSymbol string, playerID int) []string {
	resp, err := h.mediator.Send(ctx, &shipQuery.GetJumpGateConnectionsQuery{
		SystemSymbol: systemSymbol,
		PlayerID:     &playerID,
	})
	if err != nil {
		return nil
	}
	conn, ok := resp.(*shipQuery.GetJumpGateConnectionsResponse)
	if !ok || conn == nil {
		return nil
	}
	return conn.ConnectedSystems
}

// observeGood re-reads a single good's live cached row at a waypoint so the loop
// can watch the destination bid decay as the importer fills.
func (h *RunTradeRouteCoordinatorHandler) observeGood(
	ctx context.Context,
	waypoint, good string,
	playerID int,
) (*market.TradeGood, error) {
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil {
		return nil, err
	}
	if mkt == nil {
		return nil, fmt.Errorf("no cached market at %s", waypoint)
	}
	g := mkt.FindGood(good)
	if g == nil {
		return nil, fmt.Errorf("%s no longer trades %s", waypoint, good)
	}
	return g, nil
}

func (h *RunTradeRouteCoordinatorHandler) navigate(ctx context.Context, ship *navigation.Ship, destination string, playerID int) error {
	_, err := h.mediator.Send(ctx, &navCmd.NavigateRouteCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	})
	return err
}

// dock docks the hull at its current waypoint, surviving the nav-cache race the goods
// factory hit (sp-n7yp): right after arrival the ship's cached nav_status can still
// read IN_TRANSIT, so the domain EnsureDocked rejects the dock ("cannot dock while in
// transit"). Rather than fail — and strand the circuit at zero visits — it reconciles
// the hull against the live API (SyncShipFromAPI clears the stale IN_TRANSIT once the
// arrival has actually landed) and retries, bounded by tradeRouteDockRetryLimit so a
// genuinely-undockable hull can never spin forever.
//
// Every attempt is dispatched by SHIP SYMBOL (nil Ship), never the coordinator's
// cached hull: passing the cached ship makes LoadShip return the stale IN_TRANSIT
// snapshot and the resync a no-op (the exact subtlety sp-n7yp's dockAndConfirm
// documents) — by symbol the handler reloads the freshly-synced nav_status each try.
// A dock that keeps failing returns its cause VERBATIM so the caller aborts the
// circuit self-diagnosingly instead of swallowing it (sp-ynuf).
func (h *RunTradeRouteCoordinatorHandler) dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	logger := common.LoggerFromContext(ctx)
	pid := shared.MustNewPlayerID(playerID)
	shipSymbol := ship.ShipSymbol()

	var lastErr error
	for attempt := 0; attempt <= tradeRouteDockRetryLimit; attempt++ {
		_, err := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol, // nil Ship: force a fresh reload of the true persisted nav_status
			PlayerID:   pid,
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == tradeRouteDockRetryLimit {
			break
		}
		// Most likely the nav-cache race: the arrival event has not yet flipped the
		// cached IN_TRANSIT to IN_ORBIT. Reconcile against the live API to refresh
		// nav_status, then retry. Bounded, so a genuine failure still surfaces below.
		logger.Log("WARNING", fmt.Sprintf("Dock of %s failed (attempt %d/%d): %v - resyncing from API and retrying", shipSymbol, attempt+1, tradeRouteDockRetryLimit+1, err), map[string]interface{}{
			"ship_symbol": shipSymbol, "attempt": attempt + 1, "error": err.Error(),
		})
		if _, serr := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, pid); serr != nil {
			return fmt.Errorf("dock of %s failed (%v); resync from API also failed: %w", shipSymbol, err, serr)
		}
	}
	return fmt.Errorf("dock of %s still failing after %d resync retries: %w", shipSymbol, tradeRouteDockRetryLimit, lastErr)
}

func (h *RunTradeRouteCoordinatorHandler) purchase(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.PurchaseCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(playerID),
	})
	if err != nil {
		return nil, err
	}
	pr, ok := resp.(*shipCargo.PurchaseCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected purchase response type %T", resp)
	}
	return pr, nil
}

func (h *RunTradeRouteCoordinatorHandler) sell(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.SellCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(playerID),
	})
	if err != nil {
		return nil, err
	}
	sr, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected sell response type %T", resp)
	}
	return sr, nil
}

// bestSpreadPerUnit returns the highest per-unit spread among ranked lanes, used to
// report how far the best standing lane fell short of the discipline floor when none
// cleared it — so a no-trade run always reports WHY, never a silent zero. Lanes are
// ranked by CAPPED spread, so the deepest per-unit spread is not necessarily lanes[0].
func bestSpreadPerUnit(lanes []trading.ArbitrageLane) int {
	best := 0
	for _, l := range lanes {
		if l.SpreadPerUnit > best {
			best = l.SpreadPerUnit
		}
	}
	return best
}

// derefString flattens an optional supply/activity pointer to its value or "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// laneCandidateLogLimit bounds how many top-ranked candidates are attached to the
// lane-selection log line (sp-q1ca): enough to show cross-system candidates were
// actually scanned and ranked even when a penalized home lane wins the selection,
// without flooding the log with the full ranked set on a system with many goods.
const laneCandidateLogLimit = 5

// laneLogCandidates summarizes up to laneCandidateLogLimit top-ranked lanes (in
// their already-penalized rank order) into loggable payloads, so the lane-selection
// log line makes cross-system scanning VERIFIABLE rather than inferred (sp-q1ca):
// an operator can see the full ranked shortlist — including any cross-system
// candidates that lost to a penalized home lane — not just the one that won.
func laneLogCandidates(lanes []trading.ArbitrageLane) []map[string]interface{} {
	limit := laneCandidateLogLimit
	if len(lanes) < limit {
		limit = len(lanes)
	}
	candidates := make([]map[string]interface{}, 0, limit)
	for _, l := range lanes[:limit] {
		candidates = append(candidates, laneLogPayload(l))
	}
	return candidates
}

// laneLogPayload flattens one lane into the structured fields the captain needs to
// verify lane selection without inferring it from nav destinations (sp-q1ca): the
// good, both endpoints (waypoint + system), the per-unit margin, and whether the
// lane crosses a system boundary (source system != destination system).
func laneLogPayload(l trading.ArbitrageLane) map[string]interface{} {
	sourceSystem := shared.ExtractSystemSymbol(l.SourceWaypoint)
	destSystem := shared.ExtractSystemSymbol(l.DestWaypoint)
	return map[string]interface{}{
		"good":          l.Good,
		"source":        l.SourceWaypoint,
		"source_system": sourceSystem,
		"dest":          l.DestWaypoint,
		"dest_system":   destSystem,
		"cross_system":  sourceSystem != destSystem,
		"spread_per_u":  l.SpreadPerUnit,
		"volume_cap":    l.VolumeCap,
		"capped_spread": l.CappedSpread,
	}
}
