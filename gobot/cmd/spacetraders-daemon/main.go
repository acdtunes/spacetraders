package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractQuery "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	gasQuery "github.com/andrescamacho/spacetraders-go/internal/application/gas/queries"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	tradingServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTactics "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQuery "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	systemQuery "github.com/andrescamacho/spacetraders-go/internal/application/system/queries"
	tradeRouteCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/pidfile"
)

func main() {
	// Parse command-line flags
	forceFlag := flag.Bool("force", false, "Kill any existing daemon and start a new one")
	flag.Parse()

	fmt.Println("SpaceTraders Daemon v0.1.0")
	fmt.Println("==========================")
	// Build stamp: makes the live binary's commit greppable in daemon.log so a
	// deploy can assert the fresh build is actually running (sp-898q, retires L42).
	fmt.Println(buildinfo.Get().Banner("spacetraders-daemon"))

	// Load configuration
	fmt.Println("Loading configuration...")
	cfg := config.MustLoadConfig("") // Empty string = search default paths

	// Acquire PID file lock to prevent multiple instances
	fmt.Printf("Acquiring PID file lock: %s\n", cfg.Daemon.PIDFile)
	pf := pidfile.New(cfg.Daemon.PIDFile)

	// Try to acquire the lock
	err := pf.Acquire()
	if err != nil {
		if *forceFlag {
			// Force mode: kill existing daemon and try again
			fmt.Println("Force mode enabled - attempting to kill existing daemon...")
			if killErr := pf.KillExisting(); killErr != nil {
				log.Fatalf("Failed to kill existing daemon: %v", killErr)
			}
			fmt.Println("Existing daemon killed")

			// Try to acquire lock again
			if err := pf.Acquire(); err != nil {
				log.Fatalf("Failed to acquire PID file lock after killing existing daemon: %v", err)
			}
		} else {
			log.Fatalf("Failed to acquire PID file lock: %v\nUse --force to kill the existing daemon", err)
		}
	}

	defer func() {
		if err := pf.Release(); err != nil {
			log.Printf("Warning: failed to release PID file: %v", err)
		}
	}()
	fmt.Println("PID file lock acquired")

	// Initialize application
	if err := run(cfg); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func run(cfg *config.Config) error {
	// 1. Setup database connection
	fmt.Printf("Connecting to %s database...\n", cfg.Database.Type)

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer database.Close(db)
	fmt.Println("Database connected")

	// Reconcile schema on startup: models are the source of truth, and
	// AutoMigrate is additive (creates missing tables/columns/indexes, never
	// destructive). This closes the gap where a merged model change passed
	// tests (which AutoMigrate the in-memory SQLite) but broke production
	// Postgres for lack of a hand-written migration (the 2026-07-03 reserved-
	// column P0). Non-fatal: a healthy earner must not be blocked by a
	// migration quirk — log loudly and continue.
	if err := database.AutoMigrate(db); err != nil {
		fmt.Printf("WARNING: schema AutoMigrate failed (continuing on existing schema): %v\n", err)
	} else {
		fmt.Println("Schema reconciled (AutoMigrate)")
	}

	// 2. Initialize waypoint converter (needed for repositories)
	waypointConverter := api.NewWaypointConverter()
	fmt.Println("Waypoint converter initialized")

	// 3. Initialize repositories
	playerRepo := persistence.NewGormPlayerRepository(db)
	waypointRepo := persistence.NewGormWaypointRepository(db)
	systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
	containerLogRepo := persistence.NewGormContainerLogRepository(db, nil) // nil = use RealClock in production
	containerRepo := persistence.NewContainerRepository(db)
	marketRepo := persistence.NewMarketRepository(db)
	marketRepoAdapter := persistence.NewMarketRepositoryAdapter(marketRepo) // Adapter for domain market.MarketRepository interface
	goodsFactoryRepo := persistence.NewGormGoodsFactoryRepository(db)
	contractRepo := persistence.NewGormContractRepository(db)
	tradingMarketRepo := persistence.NewMarketRepositoryAdapter(marketRepo)
	transactionRepo := persistence.NewGormTransactionRepository(db)
	priceHistoryRepo := persistence.NewGormMarketPriceHistoryRepository(db)

	// 4. Initialize API client
	apiClient := api.NewSpaceTradersClient()
	fmt.Println("API client initialized")

	// 4. Initialize ship repository (adapts API responses to domain entities)
	// Note: Will be updated after waypointProvider is created
	var shipRepo navigation.ShipRepository // Declare here, initialize after waypointProvider
	fmt.Println("Ship repository will be initialized after waypoint provider")

	// 5. Initialize routing client
	// Use real gRPC client if routing address is configured, otherwise use mock
	var routingClient domainRouting.RoutingClient
	if cfg.Routing.Address != "" {
		fmt.Printf("Connecting to routing service at %s...\n", cfg.Routing.Address)
		grpcClient, err := routing.NewGRPCRoutingClient(cfg.Routing.Address)
		if err != nil {
			return fmt.Errorf("failed to connect to routing service: %w", err)
		}
		routingClient = grpcClient
		fmt.Println("Routing client initialized (gRPC OR-Tools service)")
	} else {
		routingClient = routing.NewMockRoutingClient()
		fmt.Println("Routing client initialized (mock - configure routing.address to use real service)")
	}

	// 6. Initialize graph builder
	graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
	fmt.Println("Graph builder initialized")

	// 6.5. Initialize unified graph service (replaces SystemGraphProvider + WaypointProvider)
	// This single service provides both graph and waypoint access with consistent caching
	graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
	fmt.Println("Graph service initialized (unified graph and waypoint access)")

	// Now initialize ship repository with graph service (implements IWaypointProvider)
	// Pass db connection for hybrid API+DB operation (ship data from API, assignment from DB)
	shipRepo = api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil) // nil = use RealClock
	fmt.Println("Ship repository initialized")

	// 7. Initialize mediator (CQRS dispatcher)
	med := common.NewMediator()

	// 7a. Register middleware (must be done before registering handlers)
	med.RegisterMiddleware(common.PlayerTokenMiddleware(playerRepo))

	// 8. Register command handlers
	// Register atomic command handlers (used by RouteExecutor)
	orbitHandler := shipTactics.NewOrbitShipHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.OrbitShipCommand](med, orbitHandler); err != nil {
		return fmt.Errorf("failed to register OrbitShip handler: %w", err)
	}

	dockHandler := shipTactics.NewDockShipHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.DockShipCommand](med, dockHandler); err != nil {
		return fmt.Errorf("failed to register DockShip handler: %w", err)
	}

	refuelHandler := shipTactics.NewRefuelShipHandler(shipRepo, playerRepo, apiClient, med)
	if err := mediator.RegisterHandler[*shipTypes.RefuelShipCommand](med, refuelHandler); err != nil {
		return fmt.Errorf("failed to register RefuelShip handler: %w", err)
	}

	setFlightModeHandler := shipNav.NewSetFlightModeHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.SetFlightModeCommand](med, setFlightModeHandler); err != nil {
		return fmt.Errorf("failed to register SetFlightMode handler: %w", err)
	}

	navigateDirectHandler := shipNav.NewNavigateDirectHandler(shipRepo, waypointRepo)
	if err := mediator.RegisterHandler[*shipTypes.NavigateDirectCommand](med, navigateDirectHandler); err != nil {
		return fmt.Errorf("failed to register NavigateDirect handler: %w", err)
	}

	// Create extracted services for NavigateRouteHandler
	waypointEnricher := ship.NewWaypointEnricher(waypointRepo)
	routePlanner := ship.NewRoutePlanner(routingClient)

	// Market scanner for automatic market data collection during navigation
	marketScanner := ship.NewMarketScanner(apiClient, marketRepo, playerRepo, priceHistoryRepo)

	// Ship event bus for pub/sub of ship state changes (arrival, cooldown, etc.)
	// Used by ShipStateScheduler (publisher) and RouteExecutor (subscriber)
	shipEventBus := ship.NewShipEventBus()
	fmt.Println("Ship event bus initialized")

	captainEventRepo := persistence.NewGormCaptainEventRepository(db)
	// Burst-group retry-storm event types at emission so one incident is one
	// event in the captain's attention budget, not one per retry (sp-kb61). Raw
	// per-retry rows still land in the container logs. container.crashed is
	// intentionally excluded: it stays one-row-per-death for detectCrashLoops.
	captainRecorder := watchkeeper.NewBurstGroupingRecorder(
		captainEventRepo, watchkeeper.DefaultBurstWindow, captain.EventWorkflowFailed)
	grpc.SetCaptainEventRecorder(captainRecorder)
	grpc.SetDefaultWorkerEventPublisher(shipEventBus)
	fmt.Println("Captain event outbox initialized")

	// Deploy-completed signal (sp-ess3): there is no distinct Go merge-deploy
	// path in this codebase, so a fresh boot running a different commit than
	// the last recorded deploy.completed IS the honest deploy signal the
	// crash-loop-resumes-on-deploy doctrine keys on. Best-effort bead id from
	// HEAD; a failure here is logged and never blocks the daemon boot.
	if err := watchkeeper.RecordDeployIfChanged(
		context.Background(), captainEventRepo, cfg.Captain.PlayerID,
		buildinfo.Get(), watchkeeper.BeadIDFromHEAD(".")); err != nil {
		fmt.Printf("watchkeeper: deploy.completed check failed (continuing): %v\n", err)
	}

	routeExecutor := ship.NewRouteExecutor(shipRepo, med, nil, marketScanner, nil, waypointRepo, shipEventBus) // nil = use RealClock and default refuel strategy

	// NavigateRoute handler (now uses extracted services)
	navigateRouteHandler := shipNav.NewNavigateRouteHandler(
		shipRepo,
		graphService,
		waypointEnricher,
		routePlanner,
		routeExecutor,
	)
	if err := mediator.RegisterHandler[*shipNav.NavigateRouteCommand](med, navigateRouteHandler); err != nil {
		return fmt.Errorf("failed to register NavigateRoute handler: %w", err)
	}

	// Jump handler (sp-n0x7: was never registered, so dispatching
	// JumpShipCommand always failed with "no handler registered")
	jumpShipHandler := shipNav.NewJumpShipHandler(shipRepo, playerRepo, apiClient, med, containerRepo, api.NewConstructionSiteRepository(apiClient, playerRepo), nil) // constructionRepo enables the at-complete-gate driveless-jump check; nil clock = RealClock
	if err := mediator.RegisterHandler[*shipNav.JumpShipCommand](med, jumpShipHandler); err != nil {
		return fmt.Errorf("failed to register JumpShip handler: %w", err)
	}

	// Market scouting handlers
	scoutTourHandler := scoutingCmd.NewScoutTourHandler(shipRepo, med, marketScanner)
	if err := mediator.RegisterHandler[*scoutingCmd.ScoutTourCommand](med, scoutTourHandler); err != nil {
		return fmt.Errorf("failed to register ScoutTour handler: %w", err)
	}

	getMarketHandler := scoutingQuery.NewGetMarketDataHandler(marketRepo)
	if err := mediator.RegisterHandler[*scoutingQuery.GetMarketDataQuery](med, getMarketHandler); err != nil {
		return fmt.Errorf("failed to register GetMarketData handler: %w", err)
	}

	listMarketsHandler := scoutingQuery.NewListMarketDataHandler(marketRepo)
	if err := mediator.RegisterHandler[*scoutingQuery.ListMarketDataQuery](med, listMarketsHandler); err != nil {
		return fmt.Errorf("failed to register ListMarketData handler: %w", err)
	}

	// Player query handlers
	getPlayerHandler := playerQuery.NewGetPlayerHandler(playerRepo, apiClient)
	if err := mediator.RegisterHandler[*playerQuery.GetPlayerQuery](med, getPlayerHandler); err != nil {
		return fmt.Errorf("failed to register GetPlayer handler: %w", err)
	}

	// Ship query handlers
	listShipsHandler := shipQuery.NewListShipsHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.ListShipsQuery](med, listShipsHandler); err != nil {
		return fmt.Errorf("failed to register ListShips handler: %w", err)
	}

	getShipHandler := shipQuery.NewGetShipHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.GetShipQuery](med, getShipHandler); err != nil {
		return fmt.Errorf("failed to register GetShip handler: %w", err)
	}

	// containerRepo satisfies ContainerStatusReader so refresh can reconcile a
	// stale claim left by a dead trade-route CLI runner (sp-vjwb); nil clock =
	// RealClock.
	refreshShipHandler := shipQuery.NewRefreshShipHandler(shipRepo, playerRepo, containerRepo, nil)
	if err := mediator.RegisterHandler[*shipQuery.RefreshShipQuery](med, refreshShipHandler); err != nil {
		return fmt.Errorf("failed to register RefreshShip handler: %w", err)
	}

	// Jump-gate discovery query handlers. FindNearestJumpGate was already used
	// internally by JumpShipHandler but, like JumpShipCommand itself before
	// sp-n0x7, had never been registered with the mediator - dispatching it
	// directly always failed with "no handler registered". GetJumpGateConnections
	// is new (sp-wlev): it backs the multi-system trade-route's neighbor-system
	// discovery.
	findNearestJumpGateHandler := shipQuery.NewFindNearestJumpGateHandler(shipRepo, graphService, playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.FindNearestJumpGateQuery](med, findNearestJumpGateHandler); err != nil {
		return fmt.Errorf("failed to register FindNearestJumpGate handler: %w", err)
	}

	getJumpGateConnectionsHandler := shipQuery.NewGetJumpGateConnectionsHandler(graphService, apiClient, playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.GetJumpGateConnectionsQuery](med, getJumpGateConnectionsHandler); err != nil {
		return fmt.Errorf("failed to register GetJumpGateConnections handler: %w", err)
	}

	// Captain-reservation command handlers: reserve/release a hull for the
	// captain's direct manual use, hiding it from coordinator discovery
	// (sp-i1ku).
	reserveShipHandler := shipAssignment.NewReserveShipHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.ReserveShipCommand](med, reserveShipHandler); err != nil {
		return fmt.Errorf("failed to register ReserveShip handler: %w", err)
	}

	releaseShipHandler := shipAssignment.NewReleaseShipHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.ReleaseShipCommand](med, releaseShipHandler); err != nil {
		return fmt.Errorf("failed to register ReleaseShip handler: %w", err)
	}

	// Fleet-dedication command + query: the single write path for the
	// dedicated_fleet tag and the fleet listing behind `fleet list` (sp-l7h2).
	// The contract coordinator's startup reconciliation of --dedicated-ships
	// routes through the same command.
	assignShipFleetHandler := shipAssignment.NewAssignShipFleetHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.AssignShipFleetCommand](med, assignShipFleetHandler); err != nil {
		return fmt.Errorf("failed to register AssignShipFleet handler: %w", err)
	}

	listFleetsHandler := shipQuery.NewListFleetsHandler(shipRepo, playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.ListFleetsQuery](med, listFleetsHandler); err != nil {
		return fmt.Errorf("failed to register ListFleets handler: %w", err)
	}

	// Waypoint discovery query handlers (graphService implements both the
	// system-graph and single-waypoint provider interfaces).
	listWaypointsHandler := systemQuery.NewListWaypointsHandler(graphService, playerRepo)
	if err := mediator.RegisterHandler[*systemQuery.ListWaypointsQuery](med, listWaypointsHandler); err != nil {
		return fmt.Errorf("failed to register ListWaypoints handler: %w", err)
	}

	getWaypointHandler := systemQuery.NewGetWaypointHandler(graphService, playerRepo)
	if err := mediator.RegisterHandler[*systemQuery.GetWaypointQuery](med, getWaypointHandler); err != nil {
		return fmt.Errorf("failed to register GetWaypoint handler: %w", err)
	}

	// Shipyard handlers
	getShipyardListingsHandler := shipyardQuery.NewGetShipyardListingsHandler(apiClient, playerRepo)
	if err := mediator.RegisterHandler[*shipyardQuery.GetShipyardListingsQuery](med, getShipyardListingsHandler); err != nil {
		return fmt.Errorf("failed to register GetShipyardListings handler: %w", err)
	}

	purchaseShipHandler := shipyardCmd.NewPurchaseShipHandler(shipRepo, playerRepo, waypointRepo, graphService, apiClient, med)
	if err := mediator.RegisterHandler[*shipyardCmd.PurchaseShipCommand](med, purchaseShipHandler); err != nil {
		return fmt.Errorf("failed to register PurchaseShip handler: %w", err)
	}

	batchPurchaseShipsHandler := shipyardCmd.NewBatchPurchaseShipsHandler(playerRepo, med, apiClient)
	if err := mediator.RegisterHandler[*shipyardCmd.BatchPurchaseShipsCommand](med, batchPurchaseShipsHandler); err != nil {
		return fmt.Errorf("failed to register BatchPurchaseShips handler: %w", err)
	}

	// Cargo handlers (pass marketScanner to refresh market data after transactions)
	purchaseCargoHandler := shipCargo.NewPurchaseCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, med, marketScanner)
	if err := mediator.RegisterHandler[*shipCargo.PurchaseCargoCommand](med, purchaseCargoHandler); err != nil {
		return fmt.Errorf("failed to register PurchaseCargo handler: %w", err)
	}

	jettisonCargoHandler := shipCargo.NewJettisonCargoHandler(shipRepo, playerRepo, apiClient)
	if err := mediator.RegisterHandler[*shipCargo.JettisonCargoCommand](med, jettisonCargoHandler); err != nil {
		return fmt.Errorf("failed to register JettisonCargo handler: %w", err)
	}

	// Ledger handlers
	playerResolver := common.NewPlayerResolver(playerRepo)
	recordTransactionHandler := ledgerCmd.NewRecordTransactionHandler(transactionRepo, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*ledgerCmd.RecordTransactionCommand](med, recordTransactionHandler); err != nil {
		return fmt.Errorf("failed to register RecordTransaction handler: %w", err)
	}

	getTransactionsHandler := ledgerQuery.NewGetTransactionsHandler(transactionRepo, playerResolver)
	if err := mediator.RegisterHandler[*ledgerQuery.GetTransactionsQuery](med, getTransactionsHandler); err != nil {
		return fmt.Errorf("failed to register GetTransactions handler: %w", err)
	}

	getProfitLossHandler := ledgerQuery.NewGetProfitLossHandler(transactionRepo)
	if err := mediator.RegisterHandler[*ledgerQuery.GetProfitLossQuery](med, getProfitLossHandler); err != nil {
		return fmt.Errorf("failed to register GetProfitLoss handler: %w", err)
	}

	getCashFlowHandler := ledgerQuery.NewGetCashFlowHandler(transactionRepo)
	if err := mediator.RegisterHandler[*ledgerQuery.GetCashFlowQuery](med, getCashFlowHandler); err != nil {
		return fmt.Errorf("failed to register GetCashFlow handler: %w", err)
	}

	// Contract handlers
	negotiateContractHandler := contractCmd.NewNegotiateContractHandler(contractRepo, shipRepo, playerRepo, apiClient)
	if err := mediator.RegisterHandler[*contractCmd.NegotiateContractCommand](med, negotiateContractHandler); err != nil {
		return fmt.Errorf("failed to register NegotiateContract handler: %w", err)
	}

	acceptContractHandler := contractCmd.NewAcceptContractHandler(contractRepo, playerRepo, apiClient, med)
	if err := mediator.RegisterHandler[*contractCmd.AcceptContractCommand](med, acceptContractHandler); err != nil {
		return fmt.Errorf("failed to register AcceptContract handler: %w", err)
	}

	deliverContractHandler := contractCmd.NewDeliverContractHandler(contractRepo, apiClient, playerRepo)
	if err := mediator.RegisterHandler[*contractCmd.DeliverContractCommand](med, deliverContractHandler); err != nil {
		return fmt.Errorf("failed to register DeliverContract handler: %w", err)
	}

	fulfillContractHandler := contractCmd.NewFulfillContractHandler(contractRepo, playerRepo, apiClient, med)
	if err := mediator.RegisterHandler[*contractCmd.FulfillContractCommand](med, fulfillContractHandler); err != nil {
		return fmt.Errorf("failed to register FulfillContract handler: %w", err)
	}

	evaluateContractProfitabilityHandler := contractQuery.NewEvaluateContractProfitabilityHandler(shipRepo, tradingMarketRepo)
	if err := mediator.RegisterHandler[*contractQuery.EvaluateContractProfitabilityQuery](med, evaluateContractProfitabilityHandler); err != nil {
		return fmt.Errorf("failed to register EvaluateContractProfitability handler: %w", err)
	}

	contractWorkflowHandler := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil)
	if err := mediator.RegisterHandler[*contractCmd.RunWorkflowCommand](med, contractWorkflowHandler); err != nil {
		return fmt.Errorf("failed to register ContractWorkflow handler: %w", err)
	}

	rebalanceFleetHandler := contractCmd.NewRebalanceContractFleetHandler(med, shipRepo, graphService, marketRepo, waypointConverter)
	if err := mediator.RegisterHandler[*contractCmd.RebalanceContractFleetCommand](med, rebalanceFleetHandler); err != nil {
		return fmt.Errorf("failed to register RebalanceContractFleet handler: %w", err)
	}

	balanceShipHandler := contractCmd.NewBalanceShipPositionHandler(med, shipRepo, containerRepo, graphService, marketRepo, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*contractCmd.BalanceShipPositionCommand](med, balanceShipHandler); err != nil {
		return fmt.Errorf("failed to register BalanceShipPosition handler: %w", err)
	}

	homeShipHandler := contractCmd.NewHomeShipHandler(med, shipRepo, graphService) // sp-snmb: dedicated fleet homing
	if err := mediator.RegisterHandler[*contractCmd.HomeShipCommand](med, homeShipHandler); err != nil {
		return fmt.Errorf("failed to register HomeShip handler: %w", err)
	}

	sellCargoHandler := shipCargo.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, med, marketScanner)
	if err := mediator.RegisterHandler[*shipCargo.SellCargoCommand](med, sellCargoHandler); err != nil {
		return fmt.Errorf("failed to register SellCargo handler: %w", err)
	}

	// 7. Initialize daemon server
	socketPath := cfg.Daemon.SocketPath
	fmt.Printf("Starting daemon server on: %s\n", socketPath)

	// Ensure socket directory exists
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, waypointRepo, shipRepo, playerRepo, routingClient, goodsFactoryRepo, apiClient, socketPath, &cfg.Metrics, shipEventBus)
	if err != nil {
		return fmt.Errorf("failed to create daemon server: %w", err)
	}

	// Now that daemon server is created, register handlers that need daemonClient
	// This avoids circular dependency (handler can call daemon server methods directly)
	daemonClientLocal := grpc.NewDaemonClientLocal(daemonServer)

	scoutMarketsHandler := scoutingCmd.NewScoutMarketsHandler(shipRepo, graphService, routingClient, daemonClientLocal, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*scoutingCmd.ScoutMarketsCommand](med, scoutMarketsHandler); err != nil {
		return fmt.Errorf("failed to register ScoutMarkets handler: %w", err)
	}

	contractFleetCoordinatorHandler := contractCmd.NewRunFleetCoordinatorHandler(med, shipRepo, contractRepo, tradingMarketRepo, daemonClientLocal, graphService, waypointConverter, containerRepo, nil, captainEventRepo)
	contractFleetCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// Idle-gap arb (sp-1z2h): the coordinator's dispatcher launches its
	// one-shot legs through the daemon server (claim-first, recovery-safe).
	contractFleetCoordinatorHandler.SetIdleArbLauncher(daemonServer)
	if err := mediator.RegisterHandler[*contractCmd.RunFleetCoordinatorCommand](med, contractFleetCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ContractFleetCoordinator handler: %w", err)
	}

	// Register AssignScoutingFleet handler (depends on daemonClientLocal)
	assignScoutingFleetHandler := scoutingCmd.NewAssignScoutingFleetHandler(
		shipRepo,
		waypointRepo,
		graphService,
		routingClient,
		daemonClientLocal,
		nil, // nil = use RealClock
	)
	if err := mediator.RegisterHandler[*scoutingCmd.AssignScoutingFleetCommand](med, assignScoutingFleetHandler); err != nil {
		return fmt.Errorf("failed to register AssignScoutingFleet handler: %w", err)
	}

	// Register GoodsFactoryCoordinator handler (depends on daemonClientLocal)
	// Create goods factory services using the domain market repository adapter
	goodsMarketLocator := goodsServices.NewMarketLocator(marketRepoAdapter, waypointRepo, playerRepo, apiClient)
	goodsResolver := goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepoAdapter)

	factoryCoordinatorHandler := goodsCmd.NewRunFactoryCoordinatorHandler(
		med, shipRepo, marketRepoAdapter, goodsResolver, goodsMarketLocator, nil, // nil = use RealClock
		apiClient, // sp-9aoc: live treasury for the factory input-buy working-capital spend floor
	)
	// sp-w3he: HARD cross-container concurrent spend cap. The per-buy floor (sp-9aoc) above is
	// per-container, so N factory containers can each clear it inside their own check->buy window
	// and collectively breach the reserve. This DB-backed reservation ledger (shared across all
	// factory containers) serializes their in-flight input spend and closes that race.
	factoryCoordinatorHandler.SetSpendLedger(persistence.NewSpendReservationLedger(db))
	if err := mediator.RegisterHandler[*goodsCmd.RunFactoryCoordinatorCommand](med, factoryCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register GoodsFactoryCoordinator handler: %w", err)
	}

	// Trade-route coordinator (sp-zewt): a single-hull pure-arbitrage circuit that runs
	// as a recovery-safe daemon container. Registered in the daemon mediator so its
	// NavigateRouteCommand legs resolve to the RouteExecutor-backed handler (orbit →
	// refuel → NavigateDirect → arrival events) instead of the CLI runner's hand-rolled
	// in-process nav — subsuming the 2sam/sj7p patches. marketScanner drives the live
	// stale-ask guard (2sam hazard b). DaemonServer.StartTradeRoute launches the container.
	tradeRouteCoordinatorHandler := tradeRouteCmd.NewRunTradeRouteCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
	)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeRouteCoordinatorCommand](med, tradeRouteCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeRouteCoordinator handler: %w", err)
	}

	// Arb-run coordinator (sp-p4ua): a one-shot, captain-directed, guarded arbitrage run
	// (buy@source → cross-gate → sell@dest, ONCE, capped + floor-guarded). Wired with the
	// same ports as trade-route so its buy/sell/navigate legs resolve to the identical
	// daemon handlers (RouteExecutor-backed travel); marketScanner drives the pre-buy
	// live source-market refresh and apiClient the working-capital spend floor.
	// DaemonServer.StartArbRun launches the container.
	arbCoordinatorHandler := tradeRouteCmd.NewRunArbCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
	)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunArbCoordinatorCommand](med, arbCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ArbCoordinator handler: %w", err)
	}

	// Gas extraction handlers (depend on daemonClientLocal and storageCoordinator)
	// NOTE: Storage coordinator is created below (after manufacturing setup) and passed here.
	// We'll register these handlers after storage coordinator is created.

	siphonResourcesHandler := gasCmd.NewSiphonResourcesHandler(shipRepo, playerRepo, apiClient, shipEventBus)
	if err := mediator.RegisterHandler[*gasCmd.SiphonResourcesCommand](med, siphonResourcesHandler); err != nil {
		return fmt.Errorf("failed to register SiphonResources handler: %w", err)
	}

	transferCargoHandler := gasCmd.NewTransferCargoHandler(shipRepo, apiClient)
	if err := mediator.RegisterHandler[*gasCmd.TransferCargoCommand](med, transferCargoHandler); err != nil {
		return fmt.Errorf("failed to register TransferCargo handler: %w", err)
	}

	findFactoryForGasHandler := gasQuery.NewFindFactoryForGasHandler(tradingMarketRepo)
	if err := mediator.RegisterHandler[*gasQuery.FindFactoryForGasQuery](med, findFactoryForGasHandler); err != nil {
		return fmt.Errorf("failed to register FindFactoryForGas handler: %w", err)
	}

	// Manufacturing handlers (depends on daemonClientLocal)
	// Create manufacturing repositories (pipeline repo needed by demand finder)
	manufacturingPipelineRepo := persistence.NewGormManufacturingPipelineRepository(db)
	manufacturingTaskRepo := persistence.NewGormManufacturingTaskRepository(db)
	manufacturingFactoryStateRepo := persistence.NewGormManufacturingFactoryStateRepository(db)
	storageOperationRepo := persistence.NewStorageOperationRepository(db, nil) // nil = use RealClock

	// Create demand finder for manufacturing opportunities
	// Pipeline repo is used to filter out goods that already have active pipelines
	manufacturingDemandFinder := tradingServices.NewManufacturingDemandFinder(
		tradingMarketRepo, graphService, goods.ExportToImportMap, goodsResolver, manufacturingPipelineRepo,
	)

	// Create collection opportunity finder for COLLECT_SELL pipelines
	// Finds factories with HIGH/ABUNDANT supply to collect from
	// Also finds storage-based opportunities (e.g., HYDROCARBON from gas siphoning)
	collectionOpportunityFinder := tradingServices.NewCollectionOpportunityFinder(
		tradingMarketRepo, manufacturingPipelineRepo,
	).WithStorageRepo(storageOperationRepo)

	// Create task queue (in-memory with DB backing)
	taskQueue := tradingServices.NewTaskQueue()

	// Create factory state tracker
	factoryTracker := manufacturing.NewFactoryStateTracker()

	// Create pipeline planner
	// Note: storageOperationRepo enables STORAGE_ACQUIRE_DELIVER tasks for gas goods.
	// containerRepo gates those tasks on the storage coordinator's container actually
	// being alive, not just its storage_operations row status (sp-86yb defense-in-depth).
	pipelinePlanner := tradingServices.NewPipelinePlanner(goodsMarketLocator, storageOperationRepo, containerRepo)

	// Manufacturing task worker services - using strategy pattern for task execution
	mfgNavigator := mfgServices.NewManufacturingNavigator(med, shipRepo)
	mfgPurchaser := mfgServices.NewManufacturingPurchaser(med, shipRepo, tradingMarketRepo)
	mfgSeller := mfgServices.NewManufacturingSeller(med, shipRepo)

	// Create task executors using strategy pattern
	taskExecutorRegistry := mfgServices.NewTaskExecutorRegistry()
	taskExecutorRegistry.Register(mfgServices.NewAcquireDeliverExecutor(mfgNavigator, mfgPurchaser, mfgSeller))
	taskExecutorRegistry.Register(mfgServices.NewCollectSellExecutor(mfgNavigator, mfgPurchaser, mfgSeller))
	taskExecutorRegistry.Register(mfgServices.NewLiquidateExecutor(mfgNavigator, mfgSeller))

	// Create storage coordinator for STORAGE_ACQUIRE_DELIVER tasks
	// This enables manufacturing pipelines to acquire cargo from storage ships
	storageCoordinator := storageApp.NewInMemoryStorageCoordinator()
	// Register storage executor and enable storage support on COLLECT_SELL executor
	mfgServices.RegisterStorageExecutor(taskExecutorRegistry, mfgNavigator, mfgPurchaser, mfgSeller, storageCoordinator, apiClient, shipRepo)

	// Register DELIVER_TO_CONSTRUCTION executor so construction pipeline tasks can execute
	constructionSiteRepo := api.NewConstructionSiteRepository(apiClient, playerRepo)
	mfgServices.RegisterConstructionExecutor(taskExecutorRegistry, mfgNavigator, mfgPurchaser, constructionSiteRepo, manufacturingPipelineRepo, manufacturingTaskRepo)

	// Gas extraction handlers (now that storage coordinator is available)
	// Transport is handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks
	gasCoordinatorHandler := gasCmd.NewRunGasCoordinatorHandler(
		med, shipRepo, storageOperationRepo, daemonClientLocal, waypointRepo, storageCoordinator, nil, // nil = use RealClock
	)
	if err := mediator.RegisterHandler[*gasCmd.RunGasCoordinatorCommand](med, gasCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register RunGasCoordinator handler: %w", err)
	}

	gasSiphonWorkerHandler := gasCmd.NewRunSiphonWorkerHandler(med, shipRepo, storageCoordinator, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*gasCmd.RunSiphonWorkerCommand](med, gasSiphonWorkerHandler); err != nil {
		return fmt.Errorf("failed to register RunSiphonWorker handler: %w", err)
	}

	gasStorageShipWorkerHandler := gasCmd.NewRunStorageShipWorkerHandler(med, shipRepo, storageCoordinator)
	if err := mediator.RegisterHandler[*gasCmd.RunStorageShipWorkerCommand](med, gasStorageShipWorkerHandler); err != nil {
		return fmt.Errorf("failed to register RunStorageShipWorker handler: %w", err)
	}

	// Create storage recovery service for daemon restart resilience
	// This recovers storage ship cargo state from API when daemon restarts
	storageRecoveryService := storageApp.NewStorageRecoveryService(
		storageOperationRepo,
		apiClient,
		storageCoordinator,
	)

	// Manufacturing task worker handler
	manufacturingTaskWorkerHandler := tradingCmd.NewRunManufacturingTaskWorkerHandler(
		taskExecutorRegistry,
		manufacturingTaskRepo,
	)
	if err := mediator.RegisterHandler[*tradingCmd.RunManufacturingTaskWorkerCommand](med, manufacturingTaskWorkerHandler); err != nil {
		return fmt.Errorf("failed to register RunManufacturingTaskWorker handler: %w", err)
	}

	// Parallel manufacturing coordinator handler
	// Note: SupplyMonitor is created at runtime in the Handle method (needs playerID)
	parallelManufacturingCoordinatorHandler := tradingCmd.NewRunParallelManufacturingCoordinatorHandler(
		manufacturingDemandFinder,
		collectionOpportunityFinder, // For COLLECT_SELL pipeline discovery
		pipelinePlanner,
		taskQueue,
		factoryTracker,
		shipRepo,
		manufacturingPipelineRepo,
		manufacturingTaskRepo,
		manufacturingFactoryStateRepo,
		tradingMarketRepo, // For SupplyMonitor creation
		containerRepo,     // For cleaning up orphaned PENDING containers
		med,
		daemonClientLocal, // For spawning worker containers
		nil,               // Use default RealClock
		graphService,      // WaypointProvider for task source location lookups
	)
	// Wire the ship event bus so the coordinator can subscribe to worker/task
	// events and the supply monitor can publish TasksBecameReady events.
	// Without this the handler nil-derefs the event bus on its goroutine and
	// crashes the whole daemon (mirrors the contract coordinator wiring above).
	parallelManufacturingCoordinatorHandler.SetEventSubscriber(shipEventBus)
	parallelManufacturingCoordinatorHandler.SetEventPublisher(shipEventBus)
	// Enable storage ship recovery on daemon restart
	parallelManufacturingCoordinatorHandler.SetStorageRecoveryService(storageRecoveryService)
	// Enable STORAGE_ACQUIRE_DELIVER task creation for goods produced by storage operations
	parallelManufacturingCoordinatorHandler.SetStorageOperationRepository(storageOperationRepo)
	// Gate SupplyMonitor's ongoing storage-source lookups on coordinator liveness too
	// (sp-86yb defense-in-depth; pipelinePlanner's own check was wired at construction).
	parallelManufacturingCoordinatorHandler.SetContainerStatusReader(containerRepo)
	if err := mediator.RegisterHandler[*tradingCmd.RunParallelManufacturingCoordinatorCommand](med, parallelManufacturingCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register RunParallelManufacturingCoordinator handler: %w", err)
	}

	fmt.Println("\n✓ Daemon is ready to accept connections")
	fmt.Println("Press Ctrl+C to stop")

	// Start serving (blocks until shutdown)
	if err := daemonServer.Start(); err != nil {
		return fmt.Errorf("daemon server error: %w", err)
	}

	fmt.Println("\nDaemon stopped")
	return nil
}
