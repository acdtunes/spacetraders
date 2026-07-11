package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file wires the fleet capacity autosizer's application ports (sp-1txd M6) to the concrete
// daemon collaborators — the buy-side twin of siting_ports.go. The M1–M5 coordinator logic depends
// only on narrow interfaces (fleetCmd.TreasuryReader / EraClockReader / YardPriceReader / Purchaser
// / the demand sources, etc.), tested against fakes; these are the thin bridges the daemon injects
// at boot. No business logic lives here — every method forwards to an existing client/repo.
//
// LIGHTS are FULLY LIVE: real worker count (HAULER hulls), running-chain count, chain-P&L realized
// worker rate, treasury / era-clock / yard-price / fleet-size reads, and the buy+dedicate path.
// HEAVIES are wired but FAIL-CLOSED: the unserved-lane count and realized tour-rate have no clean
// read path yet (the banked seam), so the heavy provider returns readable=false and no heavy is
// ever bought until that seam lands (the vdld TourAlignmentProvider precedent). API utilization
// likewise has no per-coordinator read path here yet, so its guard fails OPEN (the fleet ceilings
// are the hard API-budget bound). Vacancies are 0 for now (the rebalancer hub-vacancy query is a
// later enrichment; a 0 leaves the chain-derived base demand intact).

// agentReader / serverStatusReader are the narrow slices of *api.SpaceTradersClient the money
// guards need (treasury + era clock). Declared here so the ports depend on behaviour, not the
// whole client.
type agentReader interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}
type serverStatusReader interface {
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
}

// NewFleetAutosizerCoordinatorHandler assembles the autosizer handler (sp-1txd M6), wiring every
// concrete port to the daemon's live collaborators and registering the light + heavy demand
// providers. The WAREHOUSE class (sp-1j3f demand + dispatch) is registered too (sp-3yqa M6-equiv),
// wired over its concrete read-path ports; it stays DORMANT until the captain opts in with
// warehouse_hulls_enabled (the coordinator skips the class otherwise), so registering it changes
// no live behaviour.
func NewFleetAutosizerCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	chainPnL goodsServices.ChainPnLReader,
	waypointRepo *persistence.GormWaypointRepository,
	eventStore captain.EventStore,
	marketLocator *goodsServices.MarketLocator,
) *fleetCmd.RunFleetAutosizerCoordinatorHandler {
	h := fleetCmd.NewRunFleetAutosizerCoordinatorHandler(nil)

	// Demand providers.
	h.AddDemandProvider(fleetCmd.NewLightDemandProvider(&autosizerLightSources{
		shipRepo: shipRepo, server: server, chainPnL: chainPnL,
	}))
	h.AddDemandProvider(fleetCmd.NewHeavyDemandProvider(&autosizerHeavySources{shipRepo: shipRepo}))

	// Warehouse class (sp-3yqa): the concrete read-path for the sp-1j3f WarehouseDemandProvider —
	// the durable-chain portfolio (vdld chains ∩ export waypoint ∩ chain_pnl), the dedicated-hull
	// pool, and the StartWarehouse dispatch bridge. SetWarehouseProvider both registers it in the
	// demand loop AND hands the coordinator the typed handle its reconcile loop needs to run the
	// anti-stranding DISPATCH step. Opt-in (warehouse_hulls_enabled, default off) so it is dormant
	// until the captain enables it.
	h.SetWarehouseProvider(fleetCmd.NewWarehouseDemandProvider(
		newWarehousePortfolioSource(server.containerRepo, marketLocator, chainPnL),
		newWarehouseHullSource(server.containerRepo, shipRepo),
		newWarehouseDispatchBridge(server, shipRepo),
	))

	// Buy-path readers + writers.
	h.SetTreasuryReader(&autosizerTreasuryReader{api: apiClient})
	h.SetEraClockReader(&autosizerEraReader{api: apiClient})
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{})
	h.SetFleetSizeReader(&autosizerFleetSizeReader{shipRepo: shipRepo})
	h.SetYardPriceReader(&autosizerYardPriceReader{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo})
	h.SetPurchaser(&autosizerPurchaser{med: med, shipRepo: shipRepo})
	h.SetPurchaseNotifier(&autosizerNotifier{store: eventStore})
	h.SetMetricsSink(&autosizerMetricsSink{})
	return h
}

// --- treasury ---

type autosizerTreasuryReader struct{ api agentReader }

func (r *autosizerTreasuryReader) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, false, nil // no token in ctx → unreadable → the treasury guards fail closed
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil || agent == nil {
		return 0, false, nil
	}
	return int64(agent.Credits), true, nil
}

// --- era clock ---

type autosizerEraReader struct{ api serverStatusReader }

func (r *autosizerEraReader) HoursToEraEnd(ctx context.Context) (float64, bool, error) {
	status, err := r.api.GetServerStatus(ctx)
	if err != nil || status == nil || status.ServerResets.Next == "" {
		return 0, false, nil // unreadable → the era-payback guard fails closed
	}
	next, perr := time.Parse(time.RFC3339, status.ServerResets.Next)
	if perr != nil {
		return 0, false, nil
	}
	return time.Until(next).Hours(), true, nil
}

// --- API utilization (banked seam: no per-coordinator read path yet → fail OPEN) ---

type autosizerAPIUtilReader struct{}

func (r *autosizerAPIUtilReader) UtilizationPct(ctx context.Context) (float64, bool, error) {
	// No live per-coordinator utilization read path is wired yet; report unreadable so the guard
	// fails OPEN (the absolute + per-class fleet ceilings are the hard API-budget bound). Wire the
	// sp-51ti budget tracker's dual report here when a fleet-wide utilization surface lands.
	return 0, false, nil
}

// --- fleet size ---

type autosizerFleetSizeReader struct{ shipRepo navigation.ShipRepository }

func (r *autosizerFleetSizeReader) TotalHulls(ctx context.Context, playerID int) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	return len(ships), nil
}

// --- yard price (cheapest known shipyard ask for the type across the player's systems) ---

type autosizerYardPriceReader struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
}

// PriceFor finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint in a
// system where the player operates. Returns readable=false (price guard fails closed) when no priced
// listing is found. The demand-proximal preference is a later refinement (banked) — cheapest is
// returned now.
func (r *autosizerYardPriceReader) PriceFor(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	if r.waypointRepo == nil {
		return 0, 0, "", false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, 0, "", false, nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, 0, "", false, nil
	}
	// Distinct systems the player has hulls in — the shipyards we can reach.
	systems := map[string]struct{}{}
	for _, s := range ships {
		if loc := s.CurrentLocation(); loc != nil {
			systems[shared.ExtractSystemSymbol(loc.Symbol)] = struct{}{}
		}
	}
	var cheapest int64
	var cheapestYard string
	for system := range systems {
		waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil {
				continue
			}
			price, ok := r.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
		}
	}
	if cheapestYard == "" {
		return 0, 0, "", false, nil
	}
	return cheapest, cheapest, cheapestYard, true, nil
}

func (r *autosizerYardPriceReader) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid}
	resp, err := r.med.Send(ctx, q)
	if err != nil {
		return 0, false
	}
	out, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok || out == nil {
		return 0, false
	}
	if listing, found := out.Shipyard.FindListingByType(shipType); found {
		return int64(listing.PurchasePrice), true
	}
	return 0, false
}

// --- purchaser (buy 1 through the money-integrity batch path, then dedicate) ---

type autosizerPurchaser struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
}

// autosizerDedicatedFleet maps a hull class to its permanent dedicated-fleet tag. Lights get NO tag
// (a SHIP_LIGHT_HAULER is a HAULER worker the moment it is bought — being adopted by a factory chain
// is the intended outcome, not the absorption hazard); heavies and warehouse hulls MUST be tagged at
// purchase so no coordinator poaches them before they reach their role (the 3-of-5-absorbed lesson).
func autosizerDedicatedFleet(class fleetCmd.HullClass) string {
	switch class {
	case fleetCmd.HullClassHeavy:
		return "trade"
	case fleetCmd.HullClassWarehouse:
		return "warehouse"
	default:
		return ""
	}
}

func (p *autosizerPurchaser) BuyAndDedicate(ctx context.Context, order fleetCmd.BuyOrder) (fleetCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	// The purchase needs a hull to travel to and buy at the shipyard. Use an idle hull; the
	// battle-tested batch path navigates it and enforces the sp-e7je money-integrity type guard.
	ships, err := p.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	purchaser := ""
	for _, s := range ships {
		if s.IsIdle() {
			purchaser = s.ShipSymbol()
			break
		}
	}
	if purchaser == "" {
		return fleetCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
	}

	resp, err := p.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             order.ShipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     order.Yard,
	})
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return fleetCmd.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]

	// Dedicate-at-purchase: tag heavy/warehouse hulls to their fleet in the same breath so no
	// coordinator tick can adopt them first. Idempotent; lights get no tag (they ARE workers).
	dedicated := false
	if fleet := autosizerDedicatedFleet(order.Class); fleet != "" {
		if aerr := p.shipRepo.AssignFleet(ctx, bought.ShipSymbol(), fleet, pid); aerr != nil {
			return fleetCmd.BuyResult{}, fmt.Errorf("bought %s but failed to dedicate to %q: %w", bought.ShipSymbol(), fleet, aerr)
		}
		dedicated = true
	}
	return fleetCmd.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost), Dedicated: dedicated}, nil
}

// --- purchase notifier (captain event: a buy is real news) ---

type autosizerNotifier struct{ store captain.EventStore }

func (n *autosizerNotifier) NotifyPurchase(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, price int64, note string) error {
	if n.store == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"class": string(class), "ship_type": shipType, "price": price, "note": note,
	})
	return n.store.Record(ctx, &captain.Event{
		Type:     captain.EventFleetAutosizerPurchase,
		Ship:     string(class),
		PlayerID: playerID,
		Payload:  string(payload),
	})
}

// --- metrics sink (adapts the fleet MetricsSink to the global collector's Record funcs) ---

type autosizerMetricsSink struct{}

func (m *autosizerMetricsSink) RecordDemand(class fleetCmd.HullClass, demand, current int) {
	metrics.RecordAutosizerDemand(string(class), demand, current)
}
func (m *autosizerMetricsSink) RecordPurchase(class fleetCmd.HullClass) {
	metrics.RecordAutosizerPurchase(string(class))
}
func (m *autosizerMetricsSink) RecordBlocked(class fleetCmd.HullClass, guard fleetCmd.GuardName) {
	metrics.RecordAutosizerBlocked(string(class), string(guard))
}
func (m *autosizerMetricsSink) RecordZeroEffectAlarm() {
	metrics.RecordAutosizerZeroEffectAlarm()
}

// --- LIGHT demand sources ---

type autosizerLightSources struct {
	shipRepo navigation.ShipRepository
	server   *DaemonServer
	chainPnL goodsServices.ChainPnLReader
}

const autosizerHaulerRole = "HAULER"

func (s *autosizerLightSources) WorkerCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.Role() == autosizerHaulerRole })
}

func (s *autosizerLightSources) DesiredChains(ctx context.Context, playerID int) (int, error) {
	// Running standing goods_factory chains (iterations=-1) — the same portfolio the siting
	// controller enumerates. When siting is worker-limited these are the chains that need workers.
	models, err := s.server.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range models {
		if m.ContainerType != "goods_factory_coordinator" {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if json.Unmarshal([]byte(m.Config), &cfg) != nil {
				continue
			}
		}
		if iter, ok := cfg["max_iterations"].(float64); ok && iter == -1 {
			n++
		}
	}
	return n, nil
}

func (s *autosizerLightSources) Vacancies(ctx context.Context, playerID int) (int, error) {
	// The rebalancer hub-vacancy query is a later enrichment (banked). 0 leaves the chain-derived
	// base demand intact (vacancies are additive).
	return 0, nil
}

func (s *autosizerLightSources) MarginalWorkerRate(ctx context.Context, playerID int) (float64, float64, bool, bool, error) {
	if s.chainPnL == nil {
		return 0, 0, false, false, nil
	}
	const windowHours = 2.0
	raw, err := s.chainPnL.ReadRealizedPnL(ctx, playerID, time.Now().Add(-time.Duration(windowHours*float64(time.Hour))))
	if err != nil {
		return 0, 0, false, false, nil
	}
	results := goodsServices.ComputeChainPnL(raw, windowHours)
	var sum float64
	var count int
	marginal := 0.0
	haveMarginal := false
	for _, res := range results {
		if !res.HasRealization {
			continue
		}
		sum += res.NetPerHour
		count++
		if !haveMarginal || res.NetPerHour < marginal {
			marginal, haveMarginal = res.NetPerHour, true
		}
	}
	if count == 0 {
		return 0, 0, false, false, nil // pre-realization: no rate signal → guard fails the rate gate closed
	}
	return marginal, sum / float64(count), false, true, nil
}

// --- HEAVY demand sources ---

type autosizerHeavySources struct{ shipRepo navigation.ShipRepository }

func (s *autosizerHeavySources) HeavyCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.DedicatedFleet() == "trade" })
}

func (s *autosizerHeavySources) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	// BANKED SEAM: the trade solver's profitable-but-unflown lane count has no clean read path yet.
	// Report unreadable so heavies FAIL CLOSED (never wrongly bought). Wire the solver's ranked-plan
	// surface here when it lands (the vdld TourAlignmentProvider precedent).
	return 0, false, nil
}

func (s *autosizerHeavySources) FleetTourRate(ctx context.Context, playerID int) (float64, float64, bool, bool, error) {
	// BANKED SEAM: no fleet-average realized tour-rate reader is wired yet. Report unreadable; heavies
	// are already fail-closed on the lane signal above, so this does not gate any live buy.
	return 0, 0, false, false, nil
}

// --- shared helper ---

func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ships {
		if pred(s) {
			n++
		}
	}
	return n, nil
}
