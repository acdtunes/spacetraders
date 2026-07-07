package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// defaultMaxVisits bounds the circuit loop so a lane whose bid never decays (or a
// mispriced fake) can never spin forever. The bid-floor discipline is the real
// stop; this is only a safety rail.
const defaultMaxVisits = 50

// tradeRouteCommandType is the command_type recorded on the trade-route container
// row. It is deliberately NOT registered in the daemon's command factory, so even
// if a row were ever left RUNNING it could not be rebuilt by restart recovery —
// but the row is created PENDING (see claimShip), which recovery skips outright.
const tradeRouteCommandType = "trade_route"

// ContainerRepository is the minimal container-persistence port the trade-route
// coordinator needs. This coordinator is CLI-driven (sp-s7c2), not launched by the
// daemon's container runner, so nothing else creates its container row — it must
// insert its own FK anchor before claiming a ship (satisfying the composite
// ships.(container_id, player_id) -> containers.(id, player_id) constraint
// fk_ships_container) and drop that row on release. Mirrors the same port the
// balancing coordinator owns for the identical reason.
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *container.Container, commandType string) error
	Remove(ctx context.Context, containerID string, playerID int) error
}

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
	// AbortReason explains why a SELECTED lane (Good set) flew fewer visits than the
	// margin would allow — a navigate/dock/buy/sell leg failed mid-circuit. It exists
	// because three successive zero-visit bugs (r3cl, sh6w, sp-2sam) each needed a live
	// re-run to discover WHY the loop stopped: the failing leg's reason was logged but
	// never surfaced to the caller, so the printed result was a bare 'Visits: 0'. With
	// this, the next occurrence is self-diagnosing. Empty on a clean margin-death stop.
	AbortReason string
}

// RunTradeRouteCoordinatorHandler runs a pure-arbitrage circuit on a single idle
// hull: it claims the named ship, ranks lanes from cache (trading.RankSpreads),
// selects the deepest lane that clears the bid-floor discipline
// (trading.FirstDisciplinedLane — so it never picks a top-capped lane the executor
// would refuse), then flies it in disciplined tranches — ≤18u/visit, and only while
// the destination bid clears basis+1000 (trading.MarginAlive) — looping until the
// margin dies, then releases the ship.
//
// It reuses the same driven ports as the fabrication coordinators (mediator for
// navigate/dock/purchase/sell, ship + market repositories, clock), so ship
// movement and trades go through the exact command handlers the daemon uses.
type RunTradeRouteCoordinatorHandler struct {
	mediator        common.Mediator
	shipRepo        navigation.ShipRepository
	marketRepo      market.MarketRepository
	containerRepo   ContainerRepository
	clock           shared.Clock
	marketRefresher MarketRefresher // optional; nil disables the live stale-ask guard
}

// NewRunTradeRouteCoordinatorHandler wires the coordinator. Following the sibling
// coordinators' convention (main.go: "nil = use RealClock"), a nil clock is
// substituted with a RealClock so the claim path never dereferences a nil clock. A
// nil marketRefresher disables the live stale-ask guard (the circuit still runs on
// the ranked basis); the CLI injects a real MarketScanner so the guard is active.
func NewRunTradeRouteCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	containerRepo ContainerRepository,
	clock shared.Clock,
	marketRefresher MarketRefresher,
) *RunTradeRouteCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTradeRouteCoordinatorHandler{
		mediator:        mediator,
		shipRepo:        shipRepo,
		marketRepo:      marketRepo,
		containerRepo:   containerRepo,
		clock:           clock,
		marketRefresher: marketRefresher,
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

	// Step 1: claim the named idle hull. Idle-gap hulls (a contract-pool hauler
	// between contracts, a factory hauler between tasks) are the free capacity this
	// engine exploits, so we only ever take a genuinely idle ship — never steal one
	// mid-task.
	ship, err := h.claimShip(ctx, cmd.ShipSymbol, containerID, playerID)
	if err != nil {
		return err
	}
	defer h.releaseShip(ctx, ship, containerID, playerID)

	// Step 2: rank lanes from cache and pick the deepest that clears the floor.
	lanes, err := h.scanLanes(ctx, cmd.SystemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to scan arbitrage lanes: %w", err)
	}
	if len(lanes) == 0 {
		logger.Log("INFO", "No profitable arbitrage lane in cache - releasing ship", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"system":      cmd.SystemSymbol,
		})
		return nil
	}

	// The scan ranks lanes by volume-capped spread and deliberately keeps sub-floor
	// lanes visible (it is an observation tool). The executor, however, refuses any
	// lane whose per-unit spread is below MinBidMargin (runCircuit's MarginAlive gate)
	// — so the top capped-spread lane can be one that flies ZERO visits. Select the
	// DEEPEST lane that actually clears the discipline floor, so a selected lane always
	// flies >=1 visit instead of a silent zero-visit run (sp-sh6w).
	lane, ok := trading.FirstDisciplinedLane(lanes)
	if !ok {
		response.NoDisciplinedLane = true
		response.BestSubFloorSpread = bestSpreadPerUnit(lanes)
		logger.Log("INFO", "No lane clears the discipline floor - releasing ship without trading", map[string]interface{}{
			"ship_symbol":           cmd.ShipSymbol,
			"system":                cmd.SystemSymbol,
			"floor":                 trading.MinBidMargin,
			"best_sub_floor_spread": response.BestSubFloorSpread,
			"ranked_lane_count":     len(lanes),
		})
		return nil
	}
	response.Good = lane.Good
	response.SourceWaypoint = lane.SourceWaypoint
	response.DestWaypoint = lane.DestWaypoint

	logger.Log("INFO", "Selected top disciplined arbitrage lane", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"good":          lane.Good,
		"source":        lane.SourceWaypoint,
		"dest":          lane.DestWaypoint,
		"spread_per_u":  lane.SpreadPerUnit,
		"volume_cap":    lane.VolumeCap,
		"capped_spread": lane.CappedSpread,
	})

	// Step 3: run the circuit until the margin dies.
	h.runCircuit(ctx, cmd, lane, ship, response)

	response.NetProfit = response.TotalRevenue - response.TotalCost
	logger.Log("INFO", "Trade-route circuit complete", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"good":          lane.Good,
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
func (h *RunTradeRouteCoordinatorHandler) runCircuit(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID
	maxVisits := cmd.MaxVisits
	if maxVisits <= 0 {
		maxVisits = defaultMaxVisits
	}

	held := 0
	for i := 0; i < maxVisits; i++ {
		// Re-observe both ends: basis (source ask we pay) and the live dest bid.
		srcGood, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Source market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Destination market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.DestWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return
		}

		basis := srcGood.SellPrice()       // ask: what we PAY buying from the source
		destBid := dstGood.PurchasePrice() // bid: what we RECEIVE selling to the dest

		// Bid-floor discipline: the edge is gone once the dest bid stops clearing
		// basis+1000. Stop here rather than grind the spread to nothing.
		if !trading.MarginAlive(destBid, basis) {
			logger.Log("INFO", "Margin dead - stopping circuit at the bid-floor", map[string]interface{}{
				"good": lane.Good, "dest_bid": destBid, "basis": basis, "floor": basis + trading.MinBidMargin,
			})
			return
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
			return
		}

		// Leg 1: buy a tranche at the source (exporter).
		if err := h.navigate(ctx, ship, lane.SourceWaypoint, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("navigation to source %s failed: %v", lane.SourceWaypoint, err)
			logger.Log("WARNING", "Navigation to source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at source %s failed: %v", lane.SourceWaypoint, err)
			logger.Log("WARNING", "Dock at source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}

		// Live-verify the ranked basis before the FIRST buy (hazard b): the lane was
		// ranked from a market cache that can be many minutes stale. Now that the hull
		// is docked at the source (the API returns live prices only with a ship present),
		// re-read the source ask and abort if it has run away from the basis the lane
		// was ranked on — buying on a stale basis has realised a large loss (a -196k
		// precedent). Only the first visit re-verifies; later visits already re-observe.
		if i == 0 && h.staleAskAborts(ctx, lane, playerID, response) {
			return
		}

		buyResp, err := h.purchase(ctx, ship.ShipSymbol(), lane.Good, buyUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("purchase of %d %s at source %s failed: %v", buyUnits, lane.Good, lane.SourceWaypoint, err)
			logger.Log("WARNING", "Purchase failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}
		held += buyResp.UnitsAdded
		response.TotalCost += buyResp.TotalCost

		// Leg 2: sell what we hold at the destination (importer).
		if err := h.navigate(ctx, ship, lane.DestWaypoint, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("navigation to destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			logger.Log("WARNING", "Navigation to destination failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			logger.Log("WARNING", "Dock at destination failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return
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
			return
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, sellUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("sell of %d %s at destination %s failed (cargo aboard): %v", sellUnits, lane.Good, lane.DestWaypoint, err)
			logger.Log("WARNING", "Sell failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		response.Visits++
	}

	logger.Log("INFO", "Trade-route hit the max-visit safety bound", map[string]interface{}{
		"good": lane.Good, "max_visits": maxVisits,
	})
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

// claimShip loads the named hull and, only if it is genuinely idle, assigns it to
// this trade-route container. A non-idle ship is refused rather than stolen from
// whatever coordinator currently owns it.
//
// Because this coordinator is CLI-driven, nothing else creates its container row,
// so claimShip must insert that row BEFORE it assigns the ship to it: the composite
// FK ships.(container_id, player_id) -> containers.(id, player_id) rejects a claim
// that references a container that does not yet exist (the 23503 that killed the
// first live run). The daemon coordinators avoid this only because their container
// runner persists the row before assigning ships; here we mirror that ordering.
//
// The row is created PENDING and never RUNNING, so era-scoped restart recovery
// (which resurrects only RUNNING/INTERRUPTED containers) can never adopt a leftover
// as a zombie; releaseShip removes it outright on every exit path.
func (h *RunTradeRouteCoordinatorHandler) claimShip(
	ctx context.Context,
	shipSymbol string,
	containerID string,
	playerID int,
) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - trade-route only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	// Persist the container row first so the ship claim below satisfies the FK. Only
	// reached once the ship is confirmed idle, so a refused claim never creates a row.
	tradeContainer := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1, // one trade-route operation; never a restartable daemon loop
		nil,
		map[string]interface{}{"ship_symbol": shipSymbol},
		h.clock,
	)
	if err := h.containerRepo.Add(ctx, tradeContainer, tradeRouteCommandType); err != nil {
		return nil, fmt.Errorf("failed to persist trade-route container for ship %s: %w", shipSymbol, err)
	}

	if err := ship.AssignToContainer(containerID, h.clock); err != nil {
		_ = h.containerRepo.Remove(ctx, containerID, playerID) // don't leak the anchor row
		return nil, fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
	}
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		_ = h.containerRepo.Remove(ctx, containerID, playerID) // claim save failed → drop the row
		return nil, fmt.Errorf("failed to persist claim of ship %s: %w", shipSymbol, err)
	}
	return ship, nil
}

// releaseShip returns the hull to the idle pool so the next coordinator (or
// another trade-route) can pick it up, then removes this run's container row. Order
// matters for the FK: the ship's container_id is cleared (ForceRelease + Save)
// before the container row is deleted. Both steps are best-effort — a failed save
// is logged, not fatal, since the run is already over — but removing the PENDING
// row here is what keeps restart recovery from ever seeing it. Even if the process
// dies before this runs, the row is only PENDING, which recovery skips.
func (h *RunTradeRouteCoordinatorHandler) releaseShip(ctx context.Context, ship *navigation.Ship, containerID string, playerID int) {
	logger := common.LoggerFromContext(ctx)
	ship.ForceRelease("trade_route_complete", h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", "Failed to release trade-route ship", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(), "error": err.Error(),
		})
	}
	if err := h.containerRepo.Remove(ctx, containerID, playerID); err != nil {
		logger.Log("WARNING", "Failed to remove trade-route container", map[string]interface{}{
			"container_id": containerID, "error": err.Error(),
		})
	}
}

// scanLanes builds cross-market listings for the system from cache and ranks
// them, reusing the same trading.RankSpreads core the `market spreads` verb uses.
func (h *RunTradeRouteCoordinatorHandler) scanLanes(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]trading.ArbitrageLane, error) {
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

	return trading.RankSpreads(listings), nil
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

func (h *RunTradeRouteCoordinatorHandler) dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	_, err := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(playerID),
	})
	return err
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
