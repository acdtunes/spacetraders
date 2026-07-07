package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
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
}

// RunTradeRouteCoordinatorHandler runs a pure-arbitrage circuit on a single idle
// hull: it claims the named ship, ranks lanes from cache (trading.RankSpreads),
// then flies the top lane in disciplined tranches — ≤18u/visit, and only while
// the destination bid clears basis+1000 (trading.MarginAlive) — looping until the
// margin dies, then releases the ship.
//
// It reuses the same driven ports as the fabrication coordinators (mediator for
// navigate/dock/purchase/sell, ship + market repositories, clock), so ship
// movement and trades go through the exact command handlers the daemon uses.
type RunTradeRouteCoordinatorHandler struct {
	mediator   common.Mediator
	shipRepo   navigation.ShipRepository
	marketRepo market.MarketRepository
	clock      shared.Clock
}

// NewRunTradeRouteCoordinatorHandler wires the coordinator. Following the sibling
// coordinators' convention (main.go: "nil = use RealClock"), a nil clock is
// substituted with a RealClock so the claim path never dereferences a nil clock.
func NewRunTradeRouteCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	clock shared.Clock,
) *RunTradeRouteCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTradeRouteCoordinatorHandler{
		mediator:   mediator,
		shipRepo:   shipRepo,
		marketRepo: marketRepo,
		clock:      clock,
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
	defer h.releaseShip(ctx, ship)

	// Step 2: rank lanes from cache and pick the deepest.
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
	lane := lanes[0]
	response.Good = lane.Good
	response.SourceWaypoint = lane.SourceWaypoint
	response.DestWaypoint = lane.DestWaypoint

	logger.Log("INFO", "Selected top arbitrage lane", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"good":           lane.Good,
		"source":         lane.SourceWaypoint,
		"dest":           lane.DestWaypoint,
		"spread_per_u":   lane.SpreadPerUnit,
		"volume_cap":     lane.VolumeCap,
		"capped_spread":  lane.CappedSpread,
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

		basis := srcGood.SellPrice()      // ask: what we PAY buying from the source
		destBid := dstGood.PurchasePrice() // bid: what we RECEIVE selling to the dest

		// Bid-floor discipline: the edge is gone once the dest bid stops clearing
		// basis+1000. Stop here rather than grind the spread to nothing.
		if !trading.MarginAlive(destBid, basis) {
			logger.Log("INFO", "Margin dead - stopping circuit at the bid-floor", map[string]interface{}{
				"good": lane.Good, "dest_bid": destBid, "basis": basis, "floor": basis + trading.MinBidMargin,
			})
			return
		}

		cargoSpace := ship.CargoCapacity() - held
		buyUnits := trading.VisitTranche(srcGood.TradeVolume(), cargoSpace)
		if buyUnits <= 0 {
			logger.Log("INFO", "No tranche to buy (volume or hold exhausted) - stopping circuit", map[string]interface{}{
				"good": lane.Good, "source_volume": srcGood.TradeVolume(), "cargo_space": cargoSpace,
			})
			return
		}

		// Leg 1: buy a tranche at the source (exporter).
		if err := h.navigate(ctx, ship, lane.SourceWaypoint, playerID); err != nil {
			logger.Log("WARNING", "Navigation to source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			logger.Log("WARNING", "Dock at source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}
		buyResp, err := h.purchase(ctx, ship.ShipSymbol(), lane.Good, buyUnits, playerID)
		if err != nil {
			logger.Log("WARNING", "Purchase failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return
		}
		held += buyResp.UnitsAdded
		response.TotalCost += buyResp.TotalCost

		// Leg 2: sell what we hold at the destination (importer).
		if err := h.navigate(ctx, ship, lane.DestWaypoint, playerID); err != nil {
			logger.Log("WARNING", "Navigation to destination failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			logger.Log("WARNING", "Dock at destination failed - ending circuit with cargo aboard", map[string]interface{}{"error": err.Error()})
			return
		}
		sellUnits := trading.VisitTranche(dstGood.TradeVolume(), held)
		if sellUnits <= 0 {
			return
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, sellUnits, playerID)
		if err != nil {
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

// claimShip loads the named hull and, only if it is genuinely idle, assigns it to
// this trade-route container. A non-idle ship is refused rather than stolen from
// whatever coordinator currently owns it.
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
	if err := ship.AssignToContainer(containerID, h.clock); err != nil {
		return nil, fmt.Errorf("failed to claim ship %s: %w", shipSymbol, err)
	}
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		return nil, fmt.Errorf("failed to persist claim of ship %s: %w", shipSymbol, err)
	}
	return ship, nil
}

// releaseShip returns the hull to the idle pool so the next coordinator (or
// another trade-route) can pick it up. Best-effort: a failed save is logged, not
// fatal, since the run is already over.
func (h *RunTradeRouteCoordinatorHandler) releaseShip(ctx context.Context, ship *navigation.Ship) {
	logger := common.LoggerFromContext(ctx)
	ship.ForceRelease("trade_route_complete", h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", "Failed to release trade-route ship", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(), "error": err.Error(),
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

// derefString flattens an optional supply/activity pointer to its value or "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
