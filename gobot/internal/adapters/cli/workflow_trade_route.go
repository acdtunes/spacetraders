package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/application/setup"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	domainNav "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// daemonNavHandler adapts NavigateRouteCommand onto the daemon's existing
// NavigateShip RPC and blocks until the ship arrives.
//
// Ship movement is daemon-owned in this architecture (routing, refuel planning
// and event-driven arrival all live in the daemon), so the CLI-driven
// trade-route delegates each navigation leg to the daemon rather than rebuilding
// that runtime in-process. NavigateShip returns as soon as the leg is dispatched,
// so this handler polls the (API-backed) ship repository until the hull is no
// longer IN_TRANSIT before letting the coordinator dock and trade.
type daemonNavHandler struct {
	client       *DaemonClient
	shipRepo     domainNav.ShipRepository
	playerID     int
	agentSymbol  string
	pollInterval time.Duration
	timeout      time.Duration
}

func (h *daemonNavHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	navReq, ok := request.(*navCmd.NavigateRouteCommand)
	if !ok {
		return nil, fmt.Errorf("daemonNavHandler: invalid request type %T", request)
	}

	// Already parked at the destination? Nothing to do.
	if ship, err := h.shipRepo.FindBySymbol(ctx, navReq.ShipSymbol, shared.MustNewPlayerID(h.playerID)); err == nil &&
		ship != nil && ship.NavStatus() != domainNav.NavStatusInTransit &&
		ship.CurrentLocation() != nil && ship.CurrentLocation().Symbol == navReq.Destination {
		return &navCmd.NavigateRouteResponse{Status: "already_at_destination", CurrentLocation: navReq.Destination}, nil
	}

	if _, err := h.client.NavigateShip(ctx, navReq.ShipSymbol, navReq.Destination, h.playerID, h.agentSymbol); err != nil {
		return nil, fmt.Errorf("daemon navigation of %s to %s failed: %w", navReq.ShipSymbol, navReq.Destination, err)
	}

	if err := h.waitForArrival(ctx, navReq.ShipSymbol, navReq.Destination); err != nil {
		return nil, err
	}
	return &navCmd.NavigateRouteResponse{Status: "completed", CurrentLocation: navReq.Destination}, nil
}

func (h *daemonNavHandler) waitForArrival(ctx context.Context, shipSymbol, destination string) error {
	deadline := time.Now().Add(h.timeout)
	for {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(h.playerID))
		if err == nil && ship != nil && ship.NavStatus() != domainNav.NavStatusInTransit {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to arrive at %s", h.timeout, shipSymbol, destination)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(h.pollInterval):
		}
	}
}

// newWorkflowTradeRouteCommand creates the workflow trade-route subcommand.
func newWorkflowTradeRouteCommand() *cobra.Command {
	var (
		shipSymbol   string
		systemSymbol string
		maxVisits    int
	)

	cmd := &cobra.Command{
		Use:   "trade-route",
		Short: "Fly one idle hull through the top-ranked arbitrage circuit",
		Long: `Claim a single idle hull and run the top-ranked pure-arbitrage circuit in a
system under trade-analyst discipline: buy at the exporter, sell at the importer,
in tranches of at most 18 units per visit, and keep looping only while the
destination bid clears basis+1000 (the acquisition cost plus the bid-floor). The
circuit stops the moment the margin dies and the ship is released back to idle.

This complements the mfg coordinator, which only trades its own fabrication
targets: trade-route exploits the standing buy-export/sell-import spreads nobody
else works, using idle-gap hulls (a contract-pool hauler between contracts, a
factory hauler between tasks) as free capacity.

Execution model: the trade legs (buy/sell) run in-process against the API; the
navigation legs are delegated to the running daemon (ship movement is
daemon-owned). Run this only on a genuinely idle hull the daemon is not actively
flying. The daemon must be running.

Examples:
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --agent ENDURANCE
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --max-visits 20 --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Build the in-process dependency graph (mirrors `ship sell`/`ship purchase`).
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
			graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
			graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
			shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil)
			marketRepo := persistence.NewMarketRepository(db)
			transactionRepo := persistence.NewGormTransactionRepository(db)
			playerResolver := player.NewPlayerResolver(playerRepo)

			ctx := context.Background()

			var resolvedPlayerID int
			var playerToken string
			if playerIdent.PlayerID > 0 {
				resolvedPlayerID = playerIdent.PlayerID
				p, err := playerRepo.FindByID(ctx, shared.MustNewPlayerID(resolvedPlayerID))
				if err != nil {
					return fmt.Errorf("failed to load player: %w", err)
				}
				playerToken = p.Token
			} else {
				p, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve player from agent symbol: %w", err)
				}
				resolvedPlayerID = p.ID.Value()
				playerToken = p.Token
			}
			ctx = auth.WithPlayerToken(ctx, playerToken)

			// Mediator: ledger handlers from the registry, plus the in-process trade
			// and dock handlers, plus a daemon-backed navigation handler.
			registry := setup.NewHandlerRegistry(transactionRepo, playerResolver, nil, nil, nil, nil, nil, nil, nil)
			m, err := registry.CreateConfiguredMediator()
			if err != nil {
				return fmt.Errorf("failed to create mediator: %w", err)
			}

			purchaseHandler := shipCargo.NewPurchaseCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, m, nil)
			sellHandler := shipCargo.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, m, nil)
			dockHandler := tactics.NewDockShipHandler(shipRepo)
			if err := mediator.RegisterHandler[*shipCargo.PurchaseCargoCommand](m, purchaseHandler); err != nil {
				return fmt.Errorf("failed to register purchase handler: %w", err)
			}
			if err := mediator.RegisterHandler[*shipCargo.SellCargoCommand](m, sellHandler); err != nil {
				return fmt.Errorf("failed to register sell handler: %w", err)
			}
			if err := mediator.RegisterHandler[*shipTypes.DockShipCommand](m, dockHandler); err != nil {
				return fmt.Errorf("failed to register dock handler: %w", err)
			}

			daemonClient, err := connectDaemon()
			if err != nil {
				return fmt.Errorf("trade-route needs the daemon running for ship movement: %w", err)
			}
			defer daemonClient.Close()

			navHandler := &daemonNavHandler{
				client:       daemonClient,
				shipRepo:     shipRepo,
				playerID:     resolvedPlayerID,
				agentSymbol:  playerIdent.AgentSymbol,
				pollInterval: 3 * time.Second,
				timeout:      5 * time.Minute,
			}
			if err := mediator.RegisterHandler[*navCmd.NavigateRouteCommand](m, navHandler); err != nil {
				return fmt.Errorf("failed to register navigation handler: %w", err)
			}

			coordinator := tradingCmd.NewRunTradeRouteCoordinatorHandler(m, shipRepo, marketRepo, nil)

			fmt.Printf("Running trade-route for %s in %s (max %d visits)...\n\n", shipSymbol, systemSymbol, maxVisits)

			resp, err := coordinator.Handle(ctx, &tradingCmd.RunTradeRouteCoordinatorCommand{
				ShipSymbol:   shipSymbol,
				SystemSymbol: systemSymbol,
				PlayerID:     resolvedPlayerID,
				MaxVisits:    maxVisits,
			})
			if err != nil {
				return fmt.Errorf("trade-route failed: %w", err)
			}

			result, ok := resp.(*tradingCmd.RunTradeRouteCoordinatorResponse)
			if !ok {
				return fmt.Errorf("unexpected response type %T", resp)
			}

			printTradeRouteResult(result)
			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Idle hull to fly the circuit (required)")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "System to scan for arbitrage lanes (required)")
	cmd.Flags().IntVar(&maxVisits, "max-visits", 0, "Safety bound on circuit visits (0 = default 50)")

	return cmd
}

func printTradeRouteResult(result *tradingCmd.RunTradeRouteCoordinatorResponse) {
	if result.Good == "" {
		fmt.Printf("No profitable arbitrage lane in cached markets - %s released, nothing traded.\n", result.ShipSymbol)
		return
	}

	fmt.Println("=== Trade-route complete ===")
	fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
	fmt.Printf("  Good:          %s\n", result.Good)
	fmt.Printf("  Circuit:       %s (buy) -> %s (sell)\n", result.SourceWaypoint, result.DestWaypoint)
	fmt.Printf("  Visits:        %d\n", result.Visits)
	fmt.Printf("  Units traded:  %d\n", result.UnitsTraded)
	fmt.Printf("  Total cost:    %d\n", result.TotalCost)
	fmt.Printf("  Total revenue: %d\n", result.TotalRevenue)
	fmt.Printf("  Net (pre-fuel): %d\n", result.NetProfit)
	fmt.Println("\nNote: net is revenue minus acquisition cost; fuel is billed separately by the daemon.")
}
