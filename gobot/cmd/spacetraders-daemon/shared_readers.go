package main

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

const routingReadinessProbeTimeout = 2 * time.Second

func newRoutingClient(address string) (domainRouting.RoutingClient, error) {
	if address == "" {
		client := routing.NewMockRoutingClient()
		fmt.Println("Routing client initialized (mock - configure routing.address to use real service)")
		return client, nil
	}

	fmt.Printf("Connecting to routing service at %s...\n", address)
	grpcClient, err := routing.NewGRPCRoutingClient(address)
	if err != nil {
		return nil, fmt.Errorf("failed to create routing client: %w", err)
	}
	// Boot-time reachability probe: the daemon does NOT depend on the
	// routing service being up — the lazy gRPC conn reconnects on its own — but
	// operators should see routing state at startup. Bounded and non-fatal either way.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), routingReadinessProbeTimeout)
	if probeErr := grpcClient.WaitForReady(probeCtx); probeErr != nil {
		fmt.Printf("Routing service UNREACHABLE at boot (%s) — continuing, will reconnect (route planning degraded until it returns)\n", address)
	} else {
		fmt.Printf("Routing service reachable at %s\n", address)
	}
	probeCancel()
	fmt.Println("Routing client initialized (gRPC OR-Tools service)")
	return grpcClient, nil
}

// ONE scanner instance, shared by every coordinator container, which is what
// makes its market-scan budget a fleet budget rather than a per-container one
// that would multiply by the container count. The budget is enforced
// by construction; this only replaces the built-in default with the configured
// rate and value clamp.
func newMarketScanner(
	cfg config.MarketScanConfig,
	apiClient *api.SpaceTradersClient,
	marketRepo *persistence.MarketRepositoryGORM,
	playerRepo *persistence.GormPlayerRepository,
	priceHistoryRepo *persistence.GormMarketPriceHistoryRepository,
) *ship.MarketScanner {
	scanner := ship.NewMarketScanner(apiClient, marketRepo, playerRepo, priceHistoryRepo)
	scanner.SetScanBudget(ship.NewScanBudget(
		cfg.ResolvedBudgetReqPerSec(),
		cfg.ResolvedValueClampR(),
	))
	fmt.Printf("Market-scan budget: %.3f req/s shared by every market reader, value clamp %dx\n",
		cfg.ResolvedBudgetReqPerSec(), cfg.ResolvedValueClampR())
	return scanner
}

// The returned budget is the fleet's ONE shipyard-read allowance, shared by every
// reader. Config rather than a container tunable for the same reason the market budget
// is: a per-container allowance would multiply by the container count and stop
// being a budget. The budget is enforced by construction; this only replaces the
// built-in default with the configured rate and demand clamp, and wires the
// charted-yard counter that is its denominator.
func newShipyardScanner(
	scanCfg config.ShipyardScanConfig,
	scoutCfg config.ScoutingConfig,
	apiClient *api.SpaceTradersClient,
	inventoryRepo *persistence.ShipyardInventoryRepositoryGORM,
	waypointRepo *persistence.GormWaypointRepository,
	events *persistence.GormCaptainEventRepository,
) (*ship.ShipyardScanner, *ship.YardScanBudget) {
	heavyShipTypes := domainShipyard.NewHeavyShipTypeSet(scoutCfg.HeavyShipTypes)
	scanner := ship.NewShipyardScanner(
		apiClient, inventoryRepo, waypointRepo, events,
		heavyShipTypes,
		time.Duration(scoutCfg.ShipyardRescanTTLSeconds)*time.Second,
	)
	budget := ship.NewYardScanBudget(
		scanCfg.ResolvedBudgetReqPerSec(),
		scanCfg.ResolvedValueClampR(),
		heavyShipTypes,
	)
	budget.SetChartedYardCounter(waypointRepo)
	scanner.SetScanBudget(budget)
	fmt.Printf("Shipyard-read budget: %.3f req/s shared by every shipyard reader, demand clamp %dx\n",
		scanCfg.ResolvedBudgetReqPerSec(), scanCfg.ResolvedValueClampR())
	return scanner, budget
}

func newGateGraphService(
	cfg config.RoutingConfig,
	gateEdgeRepo *persistence.GormGateEdgeRepository,
	apiClient *api.SpaceTradersClient,
	graphService *graph.GraphService,
	playerRepo *persistence.GormPlayerRepository,
) *gategraph.Service {
	// A gate-set refresh re-reads EVERY connected gate's build state, and a set expires
	// as a whole — so one neighbour still under construction drags its healthy siblings
	// onto the short window and re-confirms verdicts that cannot have changed (gate
	// construction is monotone). This probe answers those from the same era-scoped,
	// freshness-bounded row the routing cache already trusts; every uncertain case still
	// goes live. Scoped to the gate graph, the only consumer of the per-gate read.
	gateProbeClient := api.NewGateConstructionProbe(apiClient, gateEdgeRepo)
	return gategraph.NewService(
		gateEdgeRepo, gateProbeClient, graphService, playerRepo,
		// Back off re-probing an unreadable jump gate (5m→30m→2h) instead of
		// re-fetching it every reconcile tick — the negative-result backoff is persisted
		// on the gate_edges row so a restart resumes it rather than re-storming the API.
		gategraph.WithBackoff(gategraph.BackoffSchedule{
			Initial:    cfg.GateBackoff.Initial,
			Multiplier: cfg.GateBackoff.Multiplier,
			Max:        cfg.GateBackoff.Max,
		}),
		// Skip the guaranteed-400 live GetJumpGate on an uncharted origin gate
		// (default ON; an explicit [routing] skip_uncharted_gate_fetch:false restores probe-
		// then-backoff). A nil switch defaults ON, matching SetDefaults.
		gategraph.WithSkipUnchartedFetch(defaultOn(cfg.SkipUnchartedGateFetch)),
	)
}

func defaultOn(switchValue *bool) bool {
	return switchValue == nil || *switchValue
}
