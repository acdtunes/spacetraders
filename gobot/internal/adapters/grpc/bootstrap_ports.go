package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file wires the captain bootstrap coordinator's concrete ports (sp-3nbe) to the daemon's live
// collaborators, mirroring fleet_autosizer_ports.go. The application-layer reconciler
// (bootstrapCmd.RunBootstrapCoordinatorHandler) owns the sequencing/gating/staging/recovery logic
// and is unit-tested against fakes; these adapters are the thin, real-service edges — reusing
// SyncAllFromAPI (the phantom-cache guard), GetAgent (treasury), shipyard list + BatchPurchaseShips
// (price-check + buy), and AssignScoutingFleet (scout-all-markets). It BUILDS nothing new.

const (
	// commandRole is the flagship's registration role — its system is the cold-start home system.
	commandRole = "COMMAND"
	// marketplaceTrait / shipyardTrait are the waypoint traits the coverage + price reads filter on.
	marketplaceTrait = "MARKETPLACE"
	shipyardTrait    = "SHIPYARD"
	// bootstrapMarketFreshnessMin bounds how old a market's data may be and still count as
	// "covered" for the DATA coverage bar. Generous (24h) because markets are actively scouted
	// during bootstrap, so coverage measures BREADTH (how many marketplaces have data), not
	// staleness. A tighter freshness window is a later refinement.
	bootstrapMarketFreshnessMin = 24 * 60
)

// NewBootstrapCoordinatorHandler assembles the bootstrap reconciler (sp-3nbe M4), wiring every
// concrete port to the daemon's live collaborators. LIVE BY DEFAULT once first-launched; recovery
// -adopted on restart. server drives the scout-all-markets assignment; apiClient reads treasury;
// shipRepo backs the phantom-cache refresh + fleet observation; med runs the price-check + buy;
// waypointRepo + marketRepo back the market-coverage read.
func NewBootstrapCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	marketRepo *persistence.MarketRepositoryAdapter,
) *bootstrapCmd.RunBootstrapCoordinatorHandler {
	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapRefresher{shipRepo: shipRepo})
	h.SetWorldObserver(&bootstrapObserver{api: apiClient, shipRepo: shipRepo, waypointRepo: waypointRepo, marketRepo: marketRepo})
	h.SetProbeAcquirer(&bootstrapAcquirer{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo})
	h.SetScoutAssigner(&bootstrapScouter{server: server})
	h.SetMetricsSink(&bootstrapMetricsSink{})
	return h
}

// --- ship refresher (phantom-cache guard, captain L47) ---

type bootstrapRefresher struct{ shipRepo navigation.ShipRepository }

func (r *bootstrapRefresher) RefreshFleet(ctx context.Context, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	_, err = r.shipRepo.SyncAllFromAPI(ctx, pid)
	return err
}

// --- world observer (fleet counts + coverage + treasury + home system) ---

type bootstrapObserver struct {
	api          agentReader
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
	marketRepo   *persistence.MarketRepositoryAdapter
}

func (o *bootstrapObserver) Observe(ctx context.Context, playerID int) (bootstrapCmd.Observation, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.Observation{Readable: false, Reason: fmt.Sprintf("bad player id: %v", err)}, nil
	}
	ships, err := o.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.Observation{}, err // infra fault → tick skip (logged by the reconciler)
	}

	obs := bootstrapCmd.Observation{}
	commandHome, anyHome := "", ""
	for _, s := range ships {
		if s.IsScoutType() {
			obs.ProbeCount++
			// A dispatched (non-idle) probe is scouting; a fresh probe idle at the yard is not yet.
			if !s.IsIdle() {
				obs.ProbesScouting++
			}
		}
		if s.IsIdle() {
			obs.HasIdlePurchaser = true
		}
		if loc := s.CurrentLocation(); loc != nil {
			sys := shared.ExtractSystemSymbol(loc.Symbol)
			if anyHome == "" {
				anyHome = sys
			}
			if s.Role() == commandRole {
				commandHome = sys
			}
		}
	}
	obs.HomeSystem = commandHome
	if obs.HomeSystem == "" {
		obs.HomeSystem = anyHome
	}

	// Treasury — the capital-gate input. No token / unreadable agent ⇒ fail closed (no action).
	token, terr := common.PlayerTokenFromContext(ctx)
	if terr != nil {
		obs.Reason = "no player token in context"
		return obs, nil
	}
	agent, aerr := o.api.GetAgent(ctx, token)
	if aerr != nil || agent == nil {
		obs.Reason = fmt.Sprintf("agent credits unreadable: %v", aerr)
		return obs, nil
	}
	obs.Treasury = int64(agent.Credits)

	// Coverage — home-system marketplaces total vs those with (fresh) market data. A read miss on
	// either leaves that count 0, which reads as uncovered (stay in DATA) rather than falsely exiting.
	if obs.HomeSystem != "" {
		if wps, werr := o.waypointRepo.ListBySystemWithTrait(ctx, obs.HomeSystem, marketplaceTrait); werr == nil {
			obs.MarketsTotal = len(wps)
		}
		if mkts, merr := o.marketRepo.ListMarketsInSystem(ctx, uint(playerID), obs.HomeSystem, bootstrapMarketFreshnessMin); merr == nil {
			obs.MarketsCovered = len(mkts)
		}
	}

	obs.Readable = true
	return obs, nil
}

// --- probe acquirer (shipyard list price-check + BatchPurchaseShips) ---

type bootstrapAcquirer struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
}

// PriceCheck finds the cheapest priced listing for shipType at a SHIPYARD-trait waypoint in a system
// where the player operates. readable=false (capital gate fails closed) when no priced listing is
// found.
func (a *bootstrapAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, "", false, nil
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, "", false, nil
	}
	systems := map[string]struct{}{}
	for _, s := range ships {
		if loc := s.CurrentLocation(); loc != nil {
			systems[shared.ExtractSystemSymbol(loc.Symbol)] = struct{}{}
		}
	}
	var cheapest int64
	var cheapestYard string
	for system := range systems {
		waypoints, werr := a.waypointRepo.ListBySystemWithTrait(ctx, system, shipyardTrait)
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil {
				continue
			}
			price, ok := a.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
		}
	}
	if cheapestYard == "" {
		return 0, "", false, nil
	}
	return cheapest, cheapestYard, true, nil
}

func (a *bootstrapAcquirer) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid}
	resp, err := a.med.Send(ctx, q)
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

// Buy purchases ONE shipType at yard through the money-integrity batch path (which navigates an idle
// hull to the yard and enforces the sp-e7je type guard). Probes are scouts — no dedicated-fleet tag.
func (a *bootstrapAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	purchaser := ""
	for _, s := range ships {
		if s.IsIdle() {
			purchaser = s.ShipSymbol()
			break
		}
	}
	if purchaser == "" {
		return bootstrapCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
	}

	resp, err := a.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             shipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     yard,
	})
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return bootstrapCmd.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]
	return bootstrapCmd.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost)}, nil
}

// --- scout assigner (reuse the VRP scout-all-markets fleet assignment) ---

type bootstrapScouter struct{ server *DaemonServer }

func (s *bootstrapScouter) AssignAllMarkets(ctx context.Context, playerID int, system string) error {
	_, err := s.server.AssignScoutingFleet(ctx, system, playerID)
	return err
}

// --- metrics sink (adapts to the global bootstrap collector; pure observation, nil-safe) ---

type bootstrapMetricsSink struct{}

func (m *bootstrapMetricsSink) RecordPhase(phase string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordPhase(phase)
	}
}

func (m *bootstrapMetricsSink) RecordProbePurchased() {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordProbePurchased()
	}
}
