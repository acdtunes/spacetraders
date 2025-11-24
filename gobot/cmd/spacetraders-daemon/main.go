package main

import (
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
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/goods/commands"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	miningCmd "github.com/andrescamacho/spacetraders-go/internal/application/mining/commands"
	miningQuery "github.com/andrescamacho/spacetraders-go/internal/application/mining/queries"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQuery "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	tradingQuery "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	tradingServices "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
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
	shipAssignmentRepo := persistence.NewShipAssignmentRepository(db)
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
	shipRepo = api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService)
	fmt.Println("Ship repository initialized")

	// 7. Initialize mediator (CQRS dispatcher)
	med := common.NewMediator()

	// 7a. Register middleware (must be done before registering handlers)
	med.RegisterMiddleware(common.PlayerTokenMiddleware(playerRepo))

	// 8. Register command handlers
	// Register atomic command handlers (used by RouteExecutor)
	orbitHandler := shipCmd.NewOrbitShipHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.OrbitShipCommand](med, orbitHandler); err != nil {
		return fmt.Errorf("failed to register OrbitShip handler: %w", err)
	}

	dockHandler := shipCmd.NewDockShipHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.DockShipCommand](med, dockHandler); err != nil {
		return fmt.Errorf("failed to register DockShip handler: %w", err)
	}

	refuelHandler := shipCmd.NewRefuelShipHandler(shipRepo, playerRepo, apiClient, med)
	if err := mediator.RegisterHandler[*shipTypes.RefuelShipCommand](med, refuelHandler); err != nil {
		return fmt.Errorf("failed to register RefuelShip handler: %w", err)
	}

	setFlightModeHandler := shipCmd.NewSetFlightModeHandler(shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.SetFlightModeCommand](med, setFlightModeHandler); err != nil {
		return fmt.Errorf("failed to register SetFlightMode handler: %w", err)
	}

	navigateDirectHandler := shipCmd.NewNavigateDirectHandler(shipRepo, waypointRepo)
	if err := mediator.RegisterHandler[*shipTypes.NavigateDirectCommand](med, navigateDirectHandler); err != nil {
		return fmt.Errorf("failed to register NavigateDirect handler: %w", err)
	}

	// Create extracted services for NavigateRouteHandler
	waypointEnricher := ship.NewWaypointEnricher(waypointRepo)
	routePlanner := ship.NewRoutePlanner(routingClient)

	// Market scanner for automatic market data collection during navigation
	marketScanner := ship.NewMarketScanner(apiClient, marketRepo, playerRepo, priceHistoryRepo)

	routeExecutor := ship.NewRouteExecutor(shipRepo, med, nil, marketScanner, nil) // nil = use RealClock and default refuel strategy

	// NavigateRoute handler (now uses extracted services)
	navigateRouteHandler := shipCmd.NewNavigateRouteHandler(
		shipRepo,
		graphService,
		waypointEnricher,
		routePlanner,
		routeExecutor,
	)
	if err := mediator.RegisterHandler[*shipCmd.NavigateRouteCommand](med, navigateRouteHandler); err != nil {
		return fmt.Errorf("failed to register NavigateRoute handler: %w", err)
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

	// Shipyard handlers
	getShipyardListingsHandler := shipyardQuery.NewGetShipyardListingsHandler(apiClient, playerRepo)
	if err := mediator.RegisterHandler[*shipyardQuery.GetShipyardListingsQuery](med, getShipyardListingsHandler); err != nil {
		return fmt.Errorf("failed to register GetShipyardListings handler: %w", err)
	}

	purchaseShipHandler := shipyardCmd.NewPurchaseShipHandler(shipRepo, playerRepo, waypointRepo, graphService, apiClient, med, shipAssignmentRepo)
	if err := mediator.RegisterHandler[*shipyardCmd.PurchaseShipCommand](med, purchaseShipHandler); err != nil {
		return fmt.Errorf("failed to register PurchaseShip handler: %w", err)
	}

	batchPurchaseShipsHandler := shipyardCmd.NewBatchPurchaseShipsHandler(playerRepo, med, apiClient)
	if err := mediator.RegisterHandler[*shipyardCmd.BatchPurchaseShipsCommand](med, batchPurchaseShipsHandler); err != nil {
		return fmt.Errorf("failed to register BatchPurchaseShips handler: %w", err)
	}

	// Cargo handlers
	purchaseCargoHandler := shipCmd.NewPurchaseCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, med)
	if err := mediator.RegisterHandler[*shipCmd.PurchaseCargoCommand](med, purchaseCargoHandler); err != nil {
		return fmt.Errorf("failed to register PurchaseCargo handler: %w", err)
	}

	jettisonCargoHandler := shipCmd.NewJettisonCargoHandler(shipRepo, playerRepo, apiClient)
	if err := mediator.RegisterHandler[*shipCmd.JettisonCargoCommand](med, jettisonCargoHandler); err != nil {
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

	contractWorkflowHandler := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, shipAssignmentRepo)
	if err := mediator.RegisterHandler[*contractCmd.RunWorkflowCommand](med, contractWorkflowHandler); err != nil {
		return fmt.Errorf("failed to register ContractWorkflow handler: %w", err)
	}

	rebalanceFleetHandler := contractCmd.NewRebalanceContractFleetHandler(med, shipRepo, shipAssignmentRepo, graphService, marketRepo, waypointConverter)
	if err := mediator.RegisterHandler[*contractCmd.RebalanceContractFleetCommand](med, rebalanceFleetHandler); err != nil {
		return fmt.Errorf("failed to register RebalanceContractFleet handler: %w", err)
	}

	balanceShipHandler := contractCmd.NewBalanceShipPositionHandler(med, shipRepo, shipAssignmentRepo, containerRepo, graphService, marketRepo)
	if err := mediator.RegisterHandler[*contractCmd.BalanceShipPositionCommand](med, balanceShipHandler); err != nil {
		return fmt.Errorf("failed to register BalanceShipPosition handler: %w", err)
	}

	// Mining handlers
	extractResourcesHandler := miningCmd.NewExtractResourcesHandler(shipRepo, playerRepo, apiClient)
	if err := mediator.RegisterHandler[*miningCmd.ExtractResourcesCommand](med, extractResourcesHandler); err != nil {
		return fmt.Errorf("failed to register ExtractResources handler: %w", err)
	}

	transferCargoHandler := miningCmd.NewTransferCargoHandler(shipRepo, playerRepo, apiClient)
	if err := mediator.RegisterHandler[*miningCmd.TransferCargoCommand](med, transferCargoHandler); err != nil {
		return fmt.Errorf("failed to register TransferCargo handler: %w", err)
	}

	evaluateCargoValueHandler := miningQuery.NewEvaluateCargoValueHandler(tradingMarketRepo)
	if err := mediator.RegisterHandler[*miningQuery.EvaluateCargoValueQuery](med, evaluateCargoValueHandler); err != nil {
		return fmt.Errorf("failed to register EvaluateCargoValue handler: %w", err)
	}

	miningWorkerHandler := miningCmd.NewRunWorkerHandler(med, shipRepo, shipAssignmentRepo, nil)
	if err := mediator.RegisterHandler[*miningCmd.RunWorkerCommand](med, miningWorkerHandler); err != nil {
		return fmt.Errorf("failed to register MiningWorker handler: %w", err)
	}

	transportWorkerHandler := miningCmd.NewRunTransportWorkerHandler(med, shipRepo, shipAssignmentRepo, graphService)
	if err := mediator.RegisterHandler[*miningCmd.RunTransportWorkerCommand](med, transportWorkerHandler); err != nil {
		return fmt.Errorf("failed to register TransportWorker handler: %w", err)
	}

	// Tour selling handler
	tourSellingHandler := tradingCmd.NewRunTourSellingHandler(med, shipRepo, tradingMarketRepo, routingClient, graphService)
	if err := mediator.RegisterHandler[*tradingCmd.RunTourSellingCommand](med, tourSellingHandler); err != nil {
		return fmt.Errorf("failed to register TourSelling handler: %w", err)
	}

	sellCargoHandler := shipCmd.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, med)
	if err := mediator.RegisterHandler[*shipCmd.SellCargoCommand](med, sellCargoHandler); err != nil {
		return fmt.Errorf("failed to register SellCargo handler: %w", err)
	}

	// Arbitrage trading handlers
	analyzer := trading.NewArbitrageAnalyzer()
	opportunityFinder := tradingServices.NewArbitrageOpportunityFinder(tradingMarketRepo, graphService, analyzer)
	arbitrageExecutionLogRepo := persistence.NewGormArbitrageExecutionLogRepository(db)
	arbitrageExecutor := tradingServices.NewArbitrageExecutor(med, shipRepo, arbitrageExecutionLogRepo)

	findArbitrageOpportunitiesHandler := tradingQuery.NewFindArbitrageOpportunitiesHandler(opportunityFinder)
	if err := mediator.RegisterHandler[*tradingQuery.FindArbitrageOpportunitiesQuery](med, findArbitrageOpportunitiesHandler); err != nil {
		return fmt.Errorf("failed to register FindArbitrageOpportunities handler: %w", err)
	}

	runArbitrageWorkerHandler := tradingCmd.NewRunArbitrageWorkerHandler(arbitrageExecutor, shipRepo, tradingMarketRepo, med)
	if err := mediator.RegisterHandler[*tradingCmd.RunArbitrageWorkerCommand](med, runArbitrageWorkerHandler); err != nil {
		return fmt.Errorf("failed to register RunArbitrageWorker handler: %w", err)
	}

	// 7. Initialize daemon server
	socketPath := cfg.Daemon.SocketPath
	fmt.Printf("Starting daemon server on: %s\n", socketPath)

	// Ensure socket directory exists
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, shipAssignmentRepo, waypointRepo, shipRepo, playerRepo, routingClient, goodsFactoryRepo, socketPath, &cfg.Metrics)
	if err != nil {
		return fmt.Errorf("failed to create daemon server: %w", err)
	}

	// Now that daemon server is created, register handlers that need daemonClient
	// This avoids circular dependency (handler can call daemon server methods directly)
	daemonClientLocal := grpc.NewDaemonClientLocal(daemonServer)

	scoutMarketsHandler := scoutingCmd.NewScoutMarketsHandler(shipRepo, graphService, routingClient, daemonClientLocal, shipAssignmentRepo)
	if err := mediator.RegisterHandler[*scoutingCmd.ScoutMarketsCommand](med, scoutMarketsHandler); err != nil {
		return fmt.Errorf("failed to register ScoutMarkets handler: %w", err)
	}

	contractFleetCoordinatorHandler := contractCmd.NewRunFleetCoordinatorHandler(med, shipRepo, contractRepo, tradingMarketRepo, shipAssignmentRepo, daemonClientLocal, graphService, waypointConverter, containerRepo, nil)
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
		shipAssignmentRepo,
	)
	if err := mediator.RegisterHandler[*scoutingCmd.AssignScoutingFleetCommand](med, assignScoutingFleetHandler); err != nil {
		return fmt.Errorf("failed to register AssignScoutingFleet handler: %w", err)
	}

	// Mining operation repository
	miningOperationRepo := persistence.NewMiningOperationRepository(db)

	// Register MiningCoordinator handler (depends on daemonClientLocal)
	miningCoordinatorHandler := miningCmd.NewRunCoordinatorHandler(
		med, shipRepo, miningOperationRepo, shipAssignmentRepo, daemonClientLocal,
		routingClient, routePlanner, graphService, tradingMarketRepo, waypointRepo,
	)
	if err := mediator.RegisterHandler[*miningCmd.RunCoordinatorCommand](med, miningCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register MiningCoordinator handler: %w", err)
	}

	// Register GoodsFactoryCoordinator handler (depends on daemonClientLocal)
	// Create goods factory services using the domain market repository adapter
	goodsMarketLocator := goodsServices.NewMarketLocator(marketRepoAdapter, waypointRepo, playerRepo, apiClient)
	goodsResolver := goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepoAdapter)

	factoryCoordinatorHandler := goodsCmd.NewRunFactoryCoordinatorHandler(
		med, shipRepo, marketRepoAdapter, shipAssignmentRepo, goodsResolver, goodsMarketLocator, nil, // nil = use RealClock
	)
	if err := mediator.RegisterHandler[*goodsCmd.RunFactoryCoordinatorCommand](med, factoryCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register GoodsFactoryCoordinator handler: %w", err)
	}

	// Arbitrage coordinator handler (depends on daemonClientLocal)
	arbitrageCoordinatorHandler := tradingCmd.NewRunArbitrageCoordinatorHandler(
		opportunityFinder, shipRepo, shipAssignmentRepo, containerRepo, daemonClientLocal, med, nil, // nil = use RealClock
	)
	if err := mediator.RegisterHandler[*tradingCmd.RunArbitrageCoordinatorCommand](med, arbitrageCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register RunArbitrageCoordinator handler: %w", err)
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
