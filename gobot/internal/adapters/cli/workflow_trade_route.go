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
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	domainNav "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// inProcessNavHandler executes each trade-route navigation leg IN PROCESS on the
// hull the coordinator has already claimed, then polls until it arrives.
//
// Why not delegate to the daemon: the coordinator claims the hull into its own
// trade-route container. Routing a leg through the daemon's NavigateShip RPC spawns
// a CHILD navigate container that tries to RE-CLAIM the same hull, and the daemon
// rejects the double-claim ("ship X is already assigned to container trade-route-…").
// The navigate leg then errors and the circuit flies zero visits — the sp-2sam
// self-collision seen live in daemon.log. Instead we move the already-claimed hull
// with the atomic NavigateDirect command, which assigns NO container (mirroring how
// the mfg task worker and balance_ship_position move a hull their parent already
// owns), so there is nothing to collide with. NavigateDirect returns as soon as the
// hop is dispatched, so we poll the API-backed ship repo until the hull is no longer
// IN_TRANSIT before letting the coordinator dock and trade. An idle claimed hull is
// excluded from the daemon's scheduler, so moving it in process is single-writer-safe
// and no longer requires the daemon to be running.
type inProcessNavHandler struct {
	mediator     common.Mediator
	shipRepo     domainNav.ShipRepository
	playerID     int
	pollInterval time.Duration
	timeout      time.Duration
}

func (h *inProcessNavHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	navReq, ok := request.(*navCmd.NavigateRouteCommand)
	if !ok {
		return nil, fmt.Errorf("inProcessNavHandler: invalid request type %T", request)
	}

	// The preceding buy leaves the hull DOCKED (cargo transactions require docked), and
	// the API rejects a navigate from a docked hull with 4236 (not in orbit) — the live
	// sp-sj7p failure. Orbit first so the hull departs. OrbitShip is idempotent: an
	// already-in-orbit hull (a later leg that arrived and did not dock) short-circuits to
	// already_in_orbit with no API call or error (tactics.OrbitShipHandler / Ship.EnsureInOrbit),
	// so it is safe to dispatch unconditionally — mirroring RouteExecutor.ensureShipInOrbit,
	// which orbits before every departure.
	if _, err := h.mediator.Send(ctx, &shipTypes.OrbitShipCommand{
		ShipSymbol: navReq.ShipSymbol,
		PlayerID:   navReq.PlayerID,
	}); err != nil {
		return nil, fmt.Errorf("in-process orbit of %s before navigate failed: %w", navReq.ShipSymbol, err)
	}

	// Move the ALREADY-CLAIMED hull directly. NavigateDirect assigns no container, so
	// it cannot self-collide with the parent trade-route claim, and it short-circuits
	// cleanly when the hull is already at the destination.
	if _, err := h.mediator.Send(ctx, &shipTypes.NavigateDirectCommand{
		ShipSymbol:  navReq.ShipSymbol,
		Destination: navReq.Destination,
		PlayerID:    navReq.PlayerID,
	}); err != nil {
		return nil, fmt.Errorf("in-process navigation of %s to %s failed: %w", navReq.ShipSymbol, navReq.Destination, err)
	}

	if err := h.waitForArrival(ctx, navReq.ShipSymbol, navReq.Destination); err != nil {
		return nil, err
	}
	return &navCmd.NavigateRouteResponse{Status: "completed", CurrentLocation: navReq.Destination}, nil
}

func (h *inProcessNavHandler) waitForArrival(ctx context.Context, shipSymbol, destination string) error {
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

Execution model: the whole circuit runs in-process against the API - trade legs
(buy/sell) and navigation legs alike. Each leg moves the hull the run has already
claimed with a direct navigate (no re-claiming child container), so it never
self-collides with its own claim. Run this only on a genuinely idle hull; the
claim excludes it from the daemon's scheduler, so the daemon need not be running.

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
			orbitHandler := tactics.NewOrbitShipHandler(shipRepo)
			if err := mediator.RegisterHandler[*shipCargo.PurchaseCargoCommand](m, purchaseHandler); err != nil {
				return fmt.Errorf("failed to register purchase handler: %w", err)
			}
			if err := mediator.RegisterHandler[*shipCargo.SellCargoCommand](m, sellHandler); err != nil {
				return fmt.Errorf("failed to register sell handler: %w", err)
			}
			if err := mediator.RegisterHandler[*shipTypes.DockShipCommand](m, dockHandler); err != nil {
				return fmt.Errorf("failed to register dock handler: %w", err)
			}
			// The in-process nav leg orbits the hull (DOCKED from the preceding buy)
			// before navigating; register the orbit handler so that dispatch resolves.
			if err := mediator.RegisterHandler[*shipTypes.OrbitShipCommand](m, orbitHandler); err != nil {
				return fmt.Errorf("failed to register orbit handler: %w", err)
			}

			// Register the atomic direct-navigate handler and route every NavigateRoute
			// leg through it IN PROCESS: this moves the hull the coordinator already
			// claimed without spawning a re-claiming child navigate container (the
			// sp-2sam self-collision that failed leg 1 and flew zero visits). Ship
			// movement no longer needs the daemon RPC.
			navigateDirectHandler := navCmd.NewNavigateDirectHandler(shipRepo, waypointRepo)
			if err := mediator.RegisterHandler[*shipTypes.NavigateDirectCommand](m, navigateDirectHandler); err != nil {
				return fmt.Errorf("failed to register navigate-direct handler: %w", err)
			}

			navHandler := &inProcessNavHandler{
				mediator:     m,
				shipRepo:     shipRepo,
				playerID:     resolvedPlayerID,
				pollInterval: 3 * time.Second,
				timeout:      5 * time.Minute,
			}
			if err := mediator.RegisterHandler[*navCmd.NavigateRouteCommand](m, navHandler); err != nil {
				return fmt.Errorf("failed to register navigation handler: %w", err)
			}

			containerRepo := persistence.NewContainerRepository(db)

			// Market scanner lets the coordinator live-verify the source ask before the
			// first buy (stale-ask guard, sp-2sam hazard b): the lane is ranked from a
			// cache that can be minutes stale, and a moved basis has realised large
			// losses. Wired here at the composition root so the guard is active on the
			// live path (unit tests pass nil to disable it).
			marketScanner := ship.NewMarketScanner(apiClient, marketRepo, playerRepo, nil)
			coordinator := tradingCmd.NewRunTradeRouteCoordinatorHandler(m, shipRepo, marketRepo, containerRepo, nil, marketScanner)

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
	if result.StaleAskAbort {
		fmt.Printf("Source ask moved %d -> %d (beyond %d%%) since the lane was ranked - %s released, nothing bought "+
			"(stale basis; aborted before the first buy to avoid a bad fill).\n",
			result.RankedSourceAsk, result.LiveSourceAsk, trading.StaleAskMovePercent, result.ShipSymbol)
		return
	}
	if result.NoDisciplinedLane {
		fmt.Printf("No lane clears the discipline floor (best standing spread %d/u < floor %d/u) - %s released, nothing traded.\n",
			result.BestSubFloorSpread, trading.MinBidMargin, result.ShipSymbol)
		return
	}
	if result.Good == "" {
		fmt.Printf("No profitable arbitrage lane in cached markets - %s released, nothing traded.\n", result.ShipSymbol)
		return
	}

	fmt.Println("=== Trade-route complete ===")
	if result.AbortReason != "" {
		// A selected lane that stopped short of margin-death — surface WHY here rather
		// than leave a bare 'Visits: 0' to be diagnosed by a live re-run (sp-2sam).
		fmt.Printf("  Aborted:       circuit stopped early — %s\n", result.AbortReason)
	}
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
