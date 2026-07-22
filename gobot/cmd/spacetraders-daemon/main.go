package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	expansionAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/expansion"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractQuery "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	gasQuery "github.com/andrescamacho/spacetraders-go/internal/application/gas/queries"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipOutfit "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/outfitting"
	shipTactics "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQuery "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	storageCmd "github.com/andrescamacho/spacetraders-go/internal/application/storage/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	systemQuery "github.com/andrescamacho/spacetraders-go/internal/application/system/queries"
	tradeRouteCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	tradingSvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
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
	// deploy can assert the fresh build is actually running.
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
	// destructive) — closes the gap where a merged model change passes tests
	// (which AutoMigrate the in-memory SQLite) but breaks production Postgres
	// for lack of a hand-written migration. Non-fatal: a healthy earner must
	// not be blocked by a migration quirk — log loudly and continue.
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
	contractRepo := persistence.NewGormContractRepository(db)
	tradingMarketRepo := persistence.NewMarketRepositoryAdapter(marketRepo)
	transactionRepo := persistence.NewGormTransactionRepository(db)
	priceHistoryRepo := persistence.NewGormMarketPriceHistoryRepository(db)

	// 4. Initialize API client
	apiClient := api.NewSpaceTradersClient()
	// sp-oszc: cache Get Agent (the #2 API consumer) with a short TTL. Every
	// GetAgent caller shares this one client, so the money guards and monitors all
	// benefit at once; safety comes from invalidating on every credit-decreasing
	// call inside the client. 0/unset -> the client's built-in 15s default.
	apiClient.SetAgentCacheTTL(time.Duration(cfg.Daemon.AgentCacheTTLSeconds) * time.Second)
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
			return fmt.Errorf("failed to create routing client: %w", err)
		}
		// Boot-time reachability probe (sp-g5ct): the daemon does NOT depend on the
		// routing service being up — the lazy gRPC conn reconnects on its own — but
		// operators should see routing state at startup. Bounded and non-fatal either way.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if probeErr := grpcClient.WaitForReady(probeCtx); probeErr != nil {
			fmt.Printf("Routing service UNREACHABLE at boot (%s) — continuing, will reconnect (route planning degraded until it returns)\n", cfg.Routing.Address)
		} else {
			fmt.Printf("Routing service reachable at %s\n", cfg.Routing.Address)
		}
		probeCancel()
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
	shipRepoImpl := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService, db, nil) // nil = use RealClock
	// sp-01wc: wire the CAS-retry knob (live by default). Setter injection keeps
	// the 4 NewShipRepository call sites untouched.
	shipRepoImpl.SetCASRetryPolicy(cfg.Daemon.MaxCASRetries)
	shipRepo = shipRepoImpl
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
	//
	// sp-7pri: guard the emit behind a player-exists check. captain_events.player_id
	// FKs players.id, so on first boot against a fresh DB (no player row yet) the
	// insert violated fk_captain_events_player (23503). The signal is re-evaluated
	// every boot, so skipping the player-less boot loses nothing.
	if err := recordDeployIfPlayerExists(
		context.Background(), playerRepo, captainEventRepo, cfg.Captain.PlayerID,
		buildinfo.Get(), watchkeeper.BeadIDFromHEAD(".")); err != nil {
		fmt.Printf("watchkeeper: deploy.completed check failed (continuing): %v\n", err)
	}

	// Shipyard scanner (sp-42ow): piggybacks a shipyard-inventory scan on the scout
	// tour's market visits — availability + prices persisted per (player, waypoint,
	// ship_type), era-scoped, with a one-time-per-era heavy-yard milestone event.
	// heavy_ship_types resolves from [scouting] config; empty defers to the domain
	// default {SHIP_HEAVY_FREIGHTER, SHIP_BULK_FREIGHTER}. Constructed BEFORE the
	// route executor so the SAME instance is injected there too (sp-42ow emit-path
	// fix): the standing multi-market scout tour delegates its market scan to
	// RouteExecutor.scanMarketIfPresent, so the shipyard scan must ride that same
	// route-arrival hook or a scout that visits a SHIPYARD-trait marketplace never
	// persists a shipyard_inventory row.
	shipyardInventoryRepo := persistence.NewShipyardInventoryRepository(db)
	shipyardScanner := ship.NewShipyardScanner(
		apiClient, shipyardInventoryRepo, waypointRepo, captainEventRepo,
		domainShipyard.NewHeavyShipTypeSet(cfg.Scouting.HeavyShipTypes),
	)

	routeExecutor := ship.NewRouteExecutor(shipRepo, med, nil, marketScanner, shipyardScanner, nil, waypointRepo, shipEventBus) // nil = use RealClock and default refuel strategy

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

	jumpShipHandler := shipNav.NewJumpShipHandler(shipRepo, playerRepo, apiClient, med, containerRepo, api.NewConstructionSiteRepository(apiClient, playerRepo), nil) // constructionRepo enables the at-complete-gate driveless-jump check; nil clock = RealClock
	if err := mediator.RegisterHandler[*shipNav.JumpShipCommand](med, jumpShipHandler); err != nil {
		return fmt.Errorf("failed to register JumpShip handler: %w", err)
	}

	// Ship outfitting handlers (sp-wh0t): install/remove/list modules. One
	// handler backs all three commands. The op atomically claims the hull
	// (RULING #3/#7) and gates the modification fee on the working-capital
	// reserve (RULING #4).
	outfittingHandler := shipOutfit.NewOutfittingHandler(shipRepo, playerRepo, apiClient, containerRepo, nil) // nil clock = RealClock
	if err := mediator.RegisterHandler[*shipOutfit.InstallModuleCommand](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register InstallModule handler: %w", err)
	}
	if err := mediator.RegisterHandler[*shipOutfit.RemoveModuleCommand](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register RemoveModule handler: %w", err)
	}
	if err := mediator.RegisterHandler[*shipOutfit.ListShipModulesQuery](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register ListShipModules handler: %w", err)
	}

	// Market scouting handlers (shipyardScanner constructed above, next to the
	// route executor it now also feeds — sp-42ow emit-path fix)
	scoutTourHandler := scoutingCmd.NewScoutTourHandler(shipRepo, med, marketScanner, shipyardScanner, nil) // nil clock = RealClock (sp-zixw)
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

	// Jump-gate discovery query handlers. GetJumpGateConnections backs the
	// multi-system trade-route's neighbor-system discovery.
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

	// ContractWorkflow handler is constructed AFTER the storage coordinator +
	// warehouse (sp-dchv Lane B/D) so it can be wired with inventory-first
	// sourcing — see "Inventory-first contract sourcing" below.

	// sp-1ef0: contractRepo + marketRepo (as SourceMarketFinder) + live config wire the
	// contract source pre-position hint. marketRepo satisfies both the market-discovery
	// and the cheapest-selling (availability-based) source resolution interfaces.
	rebalanceFleetHandler := contractCmd.NewRebalanceContractFleetHandler(
		med, shipRepo, graphService, marketRepo, waypointConverter,
		contractRepo, marketRepo,
		contractCmd.SourcePrepositionConfig{
			Disabled:            cfg.Contract.SourcePreposition.Disabled,
			ConfidenceThreshold: cfg.Contract.SourcePreposition.ConfidenceThreshold,
		},
	)
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

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, waypointRepo, shipRepo, playerRepo, routingClient, apiClient, socketPath, &cfg.Metrics, cfg.Contract, cfg.TradeFleet, cfg.WorkerRebalancer, cfg.Manufacturing, cfg.Scouting, cfg.FleetAutosizer, cfg.Bootstrap, cfg.ShipResync, shipEventBus)
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

	// sp-78ai L2: the cross-engine market-absorption ledger, shared by the idle-arb
	// dispatcher (consult skip:reserved + record each launched leg) and the arb
	// container (convert-at-sale). Recovery half-lives come from the SAME fitted
	// artifact the tour engine reads (cfg.Routing.ModelArtifactPath, resolved
	// absolute at load); dead-container reclaim consults the live containers table.
	absorptionLedger := persistence.NewAbsorptionLedger(
		db,
		cfg.Routing.ModelArtifactPath,
		persistence.AbsorptionLedgerConfig{
			ExecutedHardCap:     cfg.Absorption.ExecutedHardCap,
			ShadowFloorFraction: cfg.Absorption.ShadowFloorFraction,
		},
		persistence.NewContainerLiveness(db),
	)

	// sp-tl68: ONE shared, decaying, per-lane compression ledger for the whole fleet. Every
	// trade-route/arb/tour/stocker leg Accrues its compression debt to it and every lane
	// rank Debt-reads it, so once the fleet hammers a lane it stays down-weighted for ~tau
	// (hours) and hulls rotate to fresh lanes. Coefficients are era-3 config (refit per
	// era); an absent [trade_impact] section resolves to the analyst's era-3 fit.
	laneCooldownLedger := domainTrading.NewLaneCooldownLedger(
		cfg.TradeImpact.ResolvedBuyImpact(),
		cfg.TradeImpact.ResolvedSellImpact(),
		cfg.TradeImpact.ResolvedCooldownTau(),
	)

	contractFleetCoordinatorHandler := contractCmd.NewRunFleetCoordinatorHandler(med, shipRepo, contractRepo, tradingMarketRepo, daemonClientLocal, graphService, waypointConverter, containerRepo, nil, captainEventRepo)
	contractFleetCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// First-boot seed marker (sp-86vb): persist "the --dedicated-ships seed has
	// been applied" into the coordinator's own container config after first boot,
	// so a daemon restart does NOT replay the stale seed over live fleet state and
	// a `fleet remove` survives the restart (RULINGS #2).
	contractFleetCoordinatorHandler.SetDedicatedFleetSeedMarker(grpc.NewDedicatedFleetSeedConfigPersister(containerRepo))
	// Live standby-station ("hub") set (sp-jcke): the coordinator resolves its hub
	// set from its own container config every discovery pass, so a `fleet hub
	// add|remove` on the running coordinator is honored with no restart — the
	// operation-level mirror of the live dedicated-fleet tag read (sp-cmwc).
	contractFleetCoordinatorHandler.SetStandbyStationProvider(grpc.NewStandbyStationConfigProvider(containerRepo))
	// Idle-gap arb (sp-1z2h): the coordinator's dispatcher launches its
	// one-shot legs through the daemon server (claim-first, recovery-safe).
	contractFleetCoordinatorHandler.SetIdleArbLauncher(daemonServer)
	// sp-78ai L2: wire the absorption ledger into the idle-arb dispatcher (consult +
	// record), with the analyst-ruled knobs.
	contractFleetCoordinatorHandler.SetAbsorptionLedger(absorptionLedger, cfg.Absorption.PlannedTTLSlack)
	// sp-u9xa (the final seam): consume the boot-loaded contract-depot routing
	// registry. The daemon server already re-derives the LIVE registry per player from
	// the durable store (LoadDepotRegistry), so a `depot add|remove` on the running
	// daemon is honored the next pass with no restart. Fail-safe: with no depots
	// configured the registry is empty and contract routing is byte-identical to today
	// (the natural off-switch). Mirrors SetIdleArbLauncher(daemonServer) above.
	contractFleetCoordinatorHandler.SetDepotRegistryProvider(daemonServer)
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

	// Register the standing scout-post coordinator (sp-cxpq): reconciles the
	// desired-state posts table every tick — respawns dead tours, claims idle
	// satellites for unmanned posts, retires completed sweep-once posts. The posts
	// table and waypoint repo are read directly; the container repo supplies tour
	// liveness (ListByStatusSimple), daemonClientLocal spawns/stops tour workers.
	scoutPostRepo := persistence.NewGormScoutPostRepository(db)
	scoutPostCoordinatorHandler := scoutingCmd.NewRunScoutPostCoordinatorHandler(
		scoutPostRepo,
		shipRepo,
		daemonClientLocal,
		containerRepo,
		waypointRepo,
		nil, // nil = use RealClock
	)
	if err := mediator.RegisterHandler[*scoutingCmd.RunScoutPostCoordinatorCommand](med, scoutPostCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ScoutPostCoordinator handler: %w", err)
	}

	// Register the standing trade-fleet coordinator (sp-1278): it watches every
	// 'trade'-dedicated hull and relaunches a continuous tour on any hull parked by an
	// honest tour exit, after a per-hull cooldown — retiring the captain's hand-relaunch
	// loop. It claims nothing itself; each tour it spawns claims its own hull under
	// operation="trade" through the daemon server (SetTourLauncher), the SAME StartTourRun
	// path `workflow tour-run` uses. Tuning is resolved live from config.yaml [trade_fleet].
	tradeFleetCoordinatorHandler := tradeRouteCmd.NewRunTradeFleetCoordinatorHandler(shipRepo, nil) // nil = use RealClock
	tradeFleetCoordinatorHandler.SetTourLauncher(daemonServer)
	tradeFleetCoordinatorHandler.SetEventRecorder(captainEventRepo)    // sp-6wxq: emit coordinator error-loop events on reconcile streak breach
	tradeFleetCoordinatorHandler.SetActiveContainerShips(daemonServer) // sp-6asm: reaper safety signal — hulls a live/recent container touched (never reap those)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeFleetCoordinatorCommand](med, tradeFleetCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeFleetCoordinator handler: %w", err)
	}

	// Shared production services for the construction-supply drain (sp-hoj8u: the goods-factory
	// coordinator that also consumed these was retired; construction is now the sole consumer).
	goodsMarketLocator := goodsServices.NewMarketLocator(marketRepoAdapter, waypointRepo, playerRepo, apiClient)
	goodsResolver := goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepoAdapter)

	// Register the standing construction-supply drain (sp-382j): the coordinator that rebuilds
	// gate-construction EXECUTION — a THIN drain on the SHARED ProductionExecutor
	// engine (NOT a second parallel task coordinator, NOT folded into the goods factory). Each
	// tick it runs the surviving activator (PENDING->READY), polls READY DELIVER_TO_CONSTRUCTION
	// tasks from EXECUTING pipelines, claims idle in-system haulers under the shared
	// "manufacturing" identity, and delegates source+deliver to the executor. Standing
	// coordinators are CLI/gRPC/bootstrap first-launched then recovery-adopted; registering the
	// handler makes a launched or recovered container runnable (nothing auto-starts on boot).
	constructionPipelineRepo := persistence.NewGormManufacturingPipelineRepository(db)
	constructionTaskRepo := persistence.NewGormManufacturingTaskRepository(db)
	// The delivery TERMINAL rides the shared engine: ProduceGood sources the material into the
	// hauler, DeliverToConstructionSite (wired here via the construction supply API) flies it to
	// the site and supplies it — no duplicate sourcing/nav logic in the drain.
	// Wire an EXPLICIT real clock (sp-vh1s): PollForProduction is clock-driven. Unlike the goods-factory
	// path (which defaults nil→RealClock inside NewRunFactoryCoordinatorHandler before building its
	// executor), the construction drain builds its producer directly here, so it must supply the clock
	// itself — otherwise the unified gate-fill nil-panicked on every construction tick at e.clock.Now().
	// The constructor also defaults a nil clock (defense in depth).
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo, marketRepoAdapter, goodsMarketLocator, shared.NewRealClock(), apiClient)
	constructionExecutor.SetConstructionRepo(api.NewConstructionSiteRepository(apiClient, playerRepo))
	// The activator is the SURVIVING SupplyMonitor: NO new
	// activation logic. Built per-player because it bakes in the playerID; the poll-loop-only
	// collaborators (factory tracker/state, sell distributor, storage, container reader, event
	// publisher) are left nil — construction activation uses only task/pipeline/queue/market.
	constructionActivatorFactory := func(pid int) goodsCmd.ConstructionActivator {
		return goodsServices.NewSupplyMonitor(
			marketRepoAdapter, nil, nil, constructionPipelineRepo, goodsServices.NewTaskQueue(),
			constructionTaskRepo, nil, goodsMarketLocator, nil, nil, nil, time.Minute, pid,
		)
	}
	constructionCoordinatorHandler := goodsCmd.NewRunConstructionCoordinatorHandler(
		constructionTaskRepo, constructionPipelineRepo, shipRepo, constructionExecutor, constructionActivatorFactory, nil, // nil = use RealClock
	)
	// sp-yfzi: DI the SAME resolver singleton the goods-factory path holds so the construction drain
	// builds the FULL scarcity-gated dependency tree for a FABRICATE material (produce scarce
	// intermediates that have a factory, buy abundant ones) instead of the flat one-level node —
	// bounded by the pipeline's SupplyChainDepth + the resolver's cycle guard, config-reversible.
	constructionCoordinatorHandler.SetTreeResolver(goodsResolver)
	if err := mediator.RegisterHandler[*goodsCmd.RunConstructionCoordinatorCommand](med, constructionCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ConstructionCoordinator handler: %w", err)
	}

	// sp-y2ptq (epic sp-9le3x): the standalone contract-hub PLACEMENT coordinator (sp-q2zq) was DELETED — the
	// scaler's ResolveRoles (home-system geometry + market roles) plus C1's demand-ranked homing via the
	// fleet coordinator now own where idle contract haulers stage, making this brain redundant. Its four
	// ports + wiring were removed; the shared `fleet hub` standby-station store is untouched.

	// The persisted, fetch-through gate-graph resolver. travel() BFS-walks it to
	// cross a multi-hop gap, and the arb pre-buy guard route-checks a cross-system
	// sell leg through it BEFORE spending. Shared by the trade-route circuit, the
	// one-shot arb, and the autosizer's reachable-yard ranking so they all see one
	// cache/graph. Constructed here, ahead of the autosizer wiring that consumes it.
	// Captured so the sp-ywh1 gate-reconcile widening can read backoff markers straight from
	// the SAME store the gate graph routes over (one cache/graph, era-scoped) — see
	// scoutPostCoordinatorHandler.SetUnreadableGateProvider below.
	// sp-jgcache: the healthy-edge freshness window is the configured topology-cache TTL
	// ([routing] gate_cache_ttl, 24h default) — the per-tick lane/reposition neighbor scan
	// hits this cache instead of re-reading gate topology live.
	gateEdgeRepo := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(cfg.Routing.GateCacheTTL))
	// sp-jgcache: skip the guaranteed-400 live GetJumpGate on an uncharted origin gate
	// (default ON; an explicit [routing] skip_uncharted_gate_fetch:false restores probe-
	// then-backoff). A nil switch defaults ON, matching SetDefaults.
	skipUnchartedGateFetch := cfg.Routing.SkipUnchartedGateFetch == nil || *cfg.Routing.SkipUnchartedGateFetch
	gateGraphService := gategraph.NewService(
		gateEdgeRepo, apiClient, graphService, playerRepo,
		// sp-ikx1: back off re-probing an unreadable jump gate (5m→30m→2h) instead of
		// re-fetching it every reconcile tick — the negative-result backoff is persisted
		// on the gate_edges row so a restart resumes it rather than re-storming the API.
		gategraph.WithBackoff(gategraph.BackoffSchedule{
			Initial:    cfg.Routing.GateBackoff.Initial,
			Multiplier: cfg.Routing.GateBackoff.Multiplier,
			Max:        cfg.Routing.GateBackoff.Max,
		}),
		gategraph.WithSkipUnchartedFetch(skipUnchartedGateFetch),
	)

	// Off-gate warp support (sp-0xd0, slice A): attach the warp-execute +
	// chart-on-arrival capability to the route executor now that gateGraphService
	// exists (WithWarpSupport mutates the same *RouteExecutor the nav handlers
	// already hold, so no re-wiring is needed). The charter reuses the SAME gate
	// graph, market scanner, and shipyard scanner the gate-nav path uses, plus the
	// graph provider as its waypoint source. INERT until a caller (slice C's
	// explorer) invokes ExecuteWarpLeg/ExecuteWarpRoute — nothing dispatches a warp
	// yet, so this changes no live behavior.
	routeExecutor.WithWarpSupport(
		ship.NewAPIWarpNavigator(apiClient),
		ship.NewWarpSystemCharter(
			gateGraphService,
			ship.NewGraphWaypointSource(graphService),
			marketScanner,
			shipyardScanner,
		),
	)

	// Fleet capacity autosizer (sp-1txd): the buy-side twin of the siting coordinator. It sizes the
	// hull pool to demand and auto-buys hulls behind the fail-closed money-guard stack. LIVE BY
	// DEFAULT once first-launched (CLI/gRPC), recovery-adopted on restart. All concrete ports —
	// treasury/era-clock via the API client, worker/heavy/fleet counts via the ship repo, the
	// running-chain count via the daemon, the chain-P&L realized worker rate, the shipyard price
	// read, the buy+dedicate path, and the captain purchase notice — are assembled inside
	// grpc.NewFleetAutosizerCoordinatorHandler. Heavies are now LIVE (sp-4ewi): the unserved-lane
	// signal reads the profitable-lane surface off the persisted market cache (marketRepo, via the
	// read-only ProfitableLaneReader) and the realized tour-rate reads persisted tour telemetry
	// (NewTourTelemetryRepository) — both fail closed on a read failure, so the guard stack still
	// gates every heavy buy.
	// sp-3yqa: goodsMarketLocator feeds the warehouse portfolio source (resolves each durable
	// chain's in-system export waypoint — the warehouse's home). The warehouse class stays dormant
	// until warehouse_hulls_enabled, so this wiring is safe to land ahead of opt-in.
	// sp-42ow: the ReachableYardFinder is the heavy branch's yard-price FALLBACK — scout-scanned
	// yards ranked by stored-gate-graph hops then price. Signal-only: with no scan data the price
	// guard fails closed exactly as before, and every other guard still gates the buy.
	// sp-a3yn slice C: the cross-coordinator bridge carrying slice-B off-gate demand (raised in the
	// FRONTIER coordinator, wired below) to the FLEET autosizer's explorer BUY path. Created here so
	// the explorer demand provider can be registered on the autosizer handler at construction; the
	// frontier connects the WRITE side (SetOffGateDemandSink) further down. Dormant until the frontier
	// emits AND the explorer class is armed (explorer_hulls_enabled, default off) — nothing auto-buys.
	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()

	fleetAutosizerHandler := grpc.NewFleetAutosizerCoordinatorHandler(
		daemonServer, apiClient, shipRepo, med, persistence.NewGormChainPnLRepository(db), waypointRepo, captainEventRepo, goodsMarketLocator,
		marketRepo, persistence.NewTourTelemetryRepository(db),
		shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService),
		explorerOffGateBridge, // sp-a3yn: explorer demand provider reads off-gate demand through this bridge
	)
	if err := mediator.RegisterHandler[*fleetCmd.RunFleetAutosizerCoordinatorCommand](med, fleetAutosizerHandler); err != nil {
		return fmt.Errorf("failed to register FleetAutosizerCoordinator handler: %w", err)
	}

	// Dedicated contract auto-scaler: the standing coordinator that ramps a FIXED, EXCLUSIVE contract
	// fleet to a live-tunable ceiling behind the 200000-credit cushion. Its concrete ports — the NOVEL
	// RoleResolver (home-system geometry + market roles), the treasury/yard-price REUSE of the autosizer
	// idioms, the "contract"-fleet counter, and the buy+dedicate+home Purchaser (the kept autosizer buy
	// primitive + the demand-ranked homing consumer) — are assembled inside
	// grpc.NewContractScalerCoordinatorHandler. Registering it changes NO live behaviour: nothing launches
	// the coordinator until the bootstrap early-scaling arm fires (contract_scaler_early_scaling,
	// default-off), so a bare deploy is byte-identical.
	contractScalerHandler := grpc.NewContractScalerCoordinatorHandler(
		daemonServer, apiClient, shipRepo, med, waypointRepo, marketRepo,
		shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService),
	)
	if err := mediator.RegisterHandler[*contractScalerCmd.RunContractScalerCommand](med, contractScalerHandler); err != nil {
		return fmt.Errorf("failed to register ContractScalerCoordinator handler: %w", err)
	}

	// Captain bootstrap coordinator (sp-3nbe): the reconciler that drives a cold agent through the
	// cold-start arc to the jump gate. Slice 1 runs the DATA phase (probes → target, scout every
	// market). LIVE BY DEFAULT once first-launched (CLI/gRPC 'workflow bootstrap'), recovery-adopted
	// on restart. Its concrete ports — the phantom-cache ship refresh, the fleet/coverage/treasury
	// observation, the shipyard price-check + buy, and the scout-all-markets assignment — are
	// assembled inside grpc.NewBootstrapCoordinatorHandler over the daemon's live collaborators.
	// LAUNCH-GATED: registering the handler changes nothing until 'workflow bootstrap' is invoked.
	bootstrapHandler := grpc.NewBootstrapCoordinatorHandler(
		daemonServer, apiClient, shipRepo, med, waypointRepo, marketRepoAdapter, contractRepo,
	)
	if err := mediator.RegisterHandler[*bootstrapCmd.RunBootstrapCoordinatorCommand](med, bootstrapHandler); err != nil {
		return fmt.Errorf("failed to register BootstrapCoordinator handler: %w", err)
	}

	// Trade-route coordinator: a single-hull pure-arbitrage circuit that runs as a
	// recovery-safe daemon container. Registered in the daemon mediator so its
	// NavigateRouteCommand legs resolve to the RouteExecutor-backed handler (orbit →
	// refuel → NavigateDirect → arrival events) instead of hand-rolled in-process nav.
	// marketScanner drives the live stale-ask guard. DaemonServer.StartTradeRoute
	// launches the container.
	tradeRouteCoordinatorHandler := tradeRouteCmd.NewRunTradeRouteCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
	)
	tradeRouteCoordinatorHandler.SetGateGraph(gateGraphService)
	// sp-bcsu: chart every jump gate a hull lands on (the one moment its outbound edges are
	// readable — a remote read with no ship present 400s) so a market-swept frontier system
	// never strands hulls on empty gate_edges. Default ON; [routing] chart_gate_on_arrival
	// (nil => on) is the reversibility switch. Wired on this SHARED instance (trade circuits +
	// scout reposition + worker ferry + route-ship) and delegated to the arb/tour/stocker legs
	// below, so ALL cross-system gate arrivals chart. Best-effort + idempotent: no new burst.
	chartGateOnArrival := cfg.Routing.ChartGateOnArrival == nil || *cfg.Routing.ChartGateOnArrival
	tradeRouteCoordinatorHandler.SetChartGateOnArrival(chartGateOnArrival)
	// sp-8l3o: the shared ship-arrival event bus lets travel() wait out a hull
	// re-adopted mid-transit before any movement (jump/navigate) instead of 4214'ing
	// and burning the container restart budget on a routine arrival.
	tradeRouteCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// sp-78ai L4: read-only absorption consult (trade-analyst Q1: "circuits write
	// nothing") — scanLanes excludes a lane whose sell side is shadowed or whose
	// reserved depth can't absorb a circuit tranche. Shares the SAME ledger instance
	// L2 (idle-arb) writes to, above.
	tradeRouteCoordinatorHandler.SetAbsorptionLedger(absorptionLedger)
	// sp-tl68: wire the era-3 price-impact coefficients + the shared cooldown ledger into
	// lane ranking. scanLanes now ranks on the EFFECTIVE spread (snapshot less the
	// self-compression this hull's volume would cause + the live shared cooldown debt), and
	// runCircuit accrues each completed leg's debt back to the shared ledger.
	tradeRouteCoordinatorHandler.SetLaneImpactModel(
		cfg.TradeImpact.ResolvedBuyImpact(),
		cfg.TradeImpact.ResolvedSellImpact(),
		laneCooldownLedger,
	)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeRouteCoordinatorCommand](med, tradeRouteCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeRouteCoordinator handler: %w", err)
	}

	// Teach the shared navigate handler to route a CROSS-SYSTEM destination through the
	// trade coordinator's gate-crossing travel machinery (RepositionToWaypoint) instead
	// of fail-closing ("waypoint <dest> not found in cache for system <current>"). The
	// intra-system route planner cannot cross systems; this mutates the already
	// registered handler in place so every NavigateRouteCommand dispatch sees it. Additive
	// and inert until here — same-system navigation is untouched, and without this wire the
	// handler keeps its exact fail-closed behaviour.
	navigateRouteHandler.WithCrossSystemRouter(tradeRouteCoordinatorHandler)

	// sp-s232: wire the scout-post coordinator for cross-gate satellite repositioning.
	// It shares the SAME persisted gate graph as the trade circuit (one cache/graph) to
	// BFS-rank the fleet-wide nearest idle satellite for an unmanned frontier post, and
	// dispatches the relay as a scout_reposition worker whose handler REUSES the trade
	// coordinator's multi-jump travel() (RepositionToWaypoint) — no new jump logic.
	// Manning stays in-system only (the sp-qxa4 invariant); repositioning just moves the
	// hull there first. nil gate graph would leave the pre-s232 park behavior intact.
	scoutPostCoordinatorHandler.SetGateGraph(gateGraphService)
	// sp-nn0y: wire the presence-free waypoint discoverer so a reposition target with no
	// KNOWN market waypoint (a virgin frontier system) is charted via the API and serviced
	// the same tick, instead of parking forever on the s232 bootstrap chicken-and-egg. Same
	// graphService the `waypoint` verb and scout-markets planner use — one cache/graph,
	// era-scoped persistence. nil would leave the pre-nn0y park behavior intact.
	scoutPostCoordinatorHandler.SetGraphProvider(graphService)
	// sp-enry: wire the VRP fleet partitioner so a multi-probe post splits its markets into
	// N disjoint per-probe tours. Reuses the SAME routing client the scout-markets verb uses —
	// the routing service already solves the partition problem. nil would leave multi-probe
	// posts parked (fail-closed); single-hull posts never partition and are unaffected.
	scoutPostCoordinatorHandler.SetRoutingClient(routingClient)
	// sp-k7q5 layer 1: wire the captain event outbox so the coordinator warns (deferred)
	// on a standing post whose circuit math cannot meet its freshness contract — the
	// SAME store the watchkeeper reads, so the warning rides the next wake. nil would
	// leave the warning off.
	scoutPostCoordinatorHandler.SetEventStore(captainEventRepo)
	// sp-dp92 P7: wire the scout_freshness_actual_seconds gauge's data source — the SAME
	// GORM market repository the rest of the coordinator already reads through, so no
	// extra DB connection or cache. nil (the pre-dp92 default) leaves the gauge unrecorded;
	// this is pure OBSERVATION and never affects manning (RULINGS #4).
	scoutPostCoordinatorHandler.SetMarketFreshnessProvider(marketRepo)
	// sp-ywh1: wire the traffic-marker enumeration that widens the gate-reconcile sweep onto
	// MARKETLESS transit gates (uncharted systems a stale backoff marker proves traffic jumps
	// THROUGH — the residual GetJumpGate-400 source the market-scoped sweep structurally cannot
	// reach). The SAME GORM gate-edge store the gate graph routes over, so one cache/graph and
	// era scoping. nil (the pre-ywh1 default) leaves the sweep market-only; the widening also
	// self-guards on GateReconcileEnabled and is reversible live via gate_reconcile_marketless_disabled.
	scoutPostCoordinatorHandler.SetUnreadableGateProvider(gateEdgeRepo)
	// sp-5les manning watchdog: wire the SAME SystemsFreshness census the freshness sizer
	// (sp-iupr) reconciles against, so the watchdog re-mans a fully-manned-but-silent standing
	// post the sizer stopped hoarding probes for — detected via the census's worst-case market
	// age breaching the post's freshness target without advancing. nil disables the
	// watchdog; it never affects manning when unwired.
	scoutPostCoordinatorHandler.SetSystemFreshnessReader(marketRepo)
	// sp-u8jc cross-system reuse relay: wire the per-system freshsizer-demand source over the SAME
	// SystemsFreshness census (cycle/sla default to the freshness sizer's own defaults so the two
	// agree). This makes the relay ARM-able by a knob flip (scout_cross_system_relay_enabled=1);
	// while that flag is 0 (the default) the reader is read by nothing, so the coordinator is
	// byte-identical to today. Demand HONORS age-driven raises, so a breaching core system reads a
	// high demand and is never raided — only comfortably-fresh over-provisioned systems donate.
	scoutPostCoordinatorHandler.SetProbeDemandReader(scoutingCmd.NewCensusProbeDemandReader(marketRepo, 0, 0))
	// sp-5les: the watchdog's manning_stall_* knobs are live-tunable — snapshot this
	// container's own persisted config each tick (the SAME reader the freshness sizer uses) so a
	// `spacetraders tune scoutpost ...` lands on the next tick with no restart. sp-u8jc's two knobs
	// ride the same snapshot.
	scoutPostCoordinatorHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	scoutRepositionHandler := scoutingCmd.NewScoutRepositionHandler(tradeRouteCoordinatorHandler)
	if err := mediator.RegisterHandler[*scoutingCmd.ScoutRepositionCommand](med, scoutRepositionHandler); err != nil {
		return fmt.Errorf("failed to register ScoutReposition handler: %w", err)
	}

	// sp-6hjw: wire the `ship route` verb — a thin operator-facing cross-system
	// point-to-point move. Its handler REUSES the trade-route coordinator's exported
	// multi-jump travel() (RepositionToWaypoint, strict fetch-through resolver) exactly
	// as the scout_reposition worker does — no new jump logic. This closes the tooling
	// gap where a manual cross-gate hull move had to be hand-rolled from navigate-to-gate
	// + jump + navigate. Registered here because it needs the already-constructed
	// tradeRouteCoordinatorHandler as its movement port.
	routeShipHandler := shipNav.NewRouteShipHandler(tradeRouteCoordinatorHandler)
	if err := mediator.RegisterHandler[*shipNav.RouteShipCommand](med, routeShipHandler); err != nil {
		return fmt.Errorf("failed to register RouteShip handler: %w", err)
	}

	// (sp-hoj8u) The worker-rebalancer coordinator was retired with the factory ops: it ferried idle
	// light-haulers to worker-starved FACTORY systems, which no longer exist. The worker_ferry
	// primitive it drove is retained (below) for the daemon's persist/start dispatch + container recovery.
	// The ferry worker reuses the trade-route coordinator's RepositionToWaypoint (the SAME
	// multi-jump travel() the arb/trade circuits use) — twin of scoutRepositionHandler.
	workerFerryHandler := tradeRouteCmd.NewWorkerFerryHandler(tradeRouteCoordinatorHandler)
	if err := mediator.RegisterHandler[*tradeRouteCmd.WorkerFerryCommand](med, workerFerryHandler); err != nil {
		return fmt.Errorf("failed to register WorkerFerry handler: %w", err)
	}

	// Cargo-liquidation worker (sp-39oi): the contract fleet coordinator's one-shot
	// self-clearing leg for a parked-with-cargo hull. It reuses the existing
	// navigate/dock/sell/jettison commands (via med) plus the ship and market repos —
	// no new ship I/O — to sell a strand at the best in-system bid, jettison only as a
	// last resort below a configured floor, and hold otherwise.
	cargoLiquidationHandler := liquidation.NewLiquidateCargoHandler(shipRepo, marketRepo, med)
	if err := mediator.RegisterHandler[*liquidation.LiquidateCargoCommand](med, cargoLiquidationHandler); err != nil {
		return fmt.Errorf("failed to register CargoLiquidation handler: %w", err)
	}

	// Frontier expansion coordinator (sp-8w89): the standing coordinator that closes the
	// manual expansion loop — it measures coverage demand (unmanned scout-post slots +
	// a gate-ranked expansion queue), declares frontier sweep-once posts through the SAME
	// scout-post repo the reconciler mans, and buys probes under the money guards. It moves
	// and claims NOTHING; the scout-post reconciler (above) and its s232 relays do all
	// movement. shipRepo satisfies the coordinator's read-only FleetReader; transactionRepo
	// supplies the ledger-derived, restart-safe cooldown/spend (RULINGS #2).
	frontierExpansionHandler := expansionCmd.NewRunFrontierExpansionCoordinatorHandler(
		scoutPostRepo, shipRepo, transactionRepo, nil, // nil = use RealClock
	)
	// Live treasury for the 25% guard (RULINGS #6) — nil would fail-close every buy.
	frontierExpansionHandler.SetTreasuryReader(expansionAdapters.NewTreasuryReader(apiClient))
	// Price-and-buy over the existing purchase_ship machinery (RULINGS #3): DEMAND-PROXIMAL
	// (sp-hej4) — given the target post's system it spawns the probe at the scout-scanned
	// probe-yard NEAREST that system (fewest gate hops, arbitrated against price by the live
	// proximal_yard_hop_penalty knob) instead of always at the home yard, so the reconciler's
	// relay is short. The probeYardFinder reads the SAME sp-42ow shipyard-inventory scans + stored
	// gate graph the heavy-yard fallback uses; a sparse/empty scan store fails OPEN to the home
	// yard. Lands the probe undedicated for the reconciler to relay. Shared with the freshness
	// sizer below (same purchaser, same fail-open selection).
	probeYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)
	frontierExpansionHandler.SetProbePurchaser(expansionAdapters.NewProbePurchaser(med, shipRepo, probeYardFinder))
	// sp-255rz stall breaker: on a fail-closed probe quote, relay an idle undedicated hull to a
	// reachable probe-yard so the next tick's live price reads. Reuses the SAME purchaser seams
	// (mediator + ship repo + yard finder); never buys, never poaches (RULINGS #4/#7). Active on
	// deploy as a liveness/safety restoration (mirrors the sp-hh0h home-yard positioner).
	frontierExpansionHandler.SetProbeBuyerPositioner(expansionAdapters.NewProbeBuyerPositioner(med, shipRepo, probeYardFinder))
	// The expansion queue's frontier enumerator: one BFS over the SAME persisted gate graph
	// the trade circuit and scout relays share, annotated with market-data counts and a
	// swept/never-scanned flag from the waypoint catalog (sp-gb7h — so a genuinely-barren
	// scanned system stops being re-scouted). nil would leave the coordinator serving only
	// unmanned-slot demand.
	frontierExpansionHandler.SetExpansionScanner(expansionAdapters.NewExpansionScanner(
		gateGraphService, marketRepoAdapter, shipRepo, playerRepo, waypointRepo,
	))
	// sp-jide: the scan-only backlog enumerator — the FULL charted-but-unscanned MARKET set (every
	// system with MARKETPLACE waypoints but zero player market_data), unbounded by gate hops. When
	// `tune --operation frontier scan_only 1` is set the coordinator sweeps this whole discovered
	// backlog (sp-pvw3: charted markets with NO or STALE price data — the honest dark set, not just
	// never-scanned). The scan side of the discovery_share split drains it; discovery_share=100 never
	// consults it. Reads the raw market repo (charted-market counts + the player's scan ages).
	// sp-gucu: give the scanner the live standing-post SLA reader so a manned standing post scanned
	// WITHIN its own 4–10h freshness SLA is not mislabeled dark against the fixed 4h bar (the false
	// "nothing is draining" census). Systems with no manned standing post keep the fixed bar.
	darkMarketScanner := expansionAdapters.NewDarkMarketScanner(marketRepo, expansionAdapters.DefaultStaleMarketSeconds)
	darkMarketScanner.SetScoutCoverageSource(scoutPostRepo)
	frontierExpansionHandler.SetDarkMarketScanner(darkMarketScanner)
	// sp-rjgr §4: the deep-resource (heavy-yard) objective the DEPTH slice biases on — heavy
	// capacity shortfall (sp-4ewi profitable-lane surface, read-only off the market cache) AND
	// whether a heavy-freighter yard is known yet (sp-42ow shipyard inventory). While unmet the
	// split shifts toward depth to FIND the yard; once known it relaxes. Fails safe (no bias) when
	// unreadable — it moves a policy split, never a spend.
	frontierExpansionHandler.SetDepthObjectiveReader(expansionAdapters.NewDepthObjectiveReader(
		shipyardInventoryRepo, tradingQueries.NewProfitableLaneReader(marketRepo), shipRepo,
	))
	frontierExpansionHandler.SetEventRecorder(captainEventRepo) // sp-6wxq: emit coordinator error-loop events on reconcile streak breach
	// sp-vwek: per-tick live-config snapshots from the container's OWN config column,
	// so `spacetraders tune` retunes the spend/cooldown/cap knobs on the next tick —
	// no restart, no rebuild.
	frontierExpansionHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	// sp-6vep reuse-before-buy the deep frontier (DEFAULT-OFF until armed next era). The
	// ProbeReuseRelayer hops an EXISTING edge probe onto a target virgin instead of buying at an
	// unreachable deep yard: it selects the nearest scout probe within edge_relay_max_hops sitting in
	// a below-ceiling system (never cannibalizing a high-value core market — the depth-vs-freshness
	// guard) and relays it over the SAME reposition path the scout reconciler uses
	// (tradeRouteCoordinatorHandler). The FrontierNeighborScanner feeds the snowball walk — a charted
	// system's uncharted gate-neighbors. Both are inert while probe_reuse_enabled / snowball_neighbors
	// stay at their default 0, so a merge is byte-identical to today's buy-only path.
	frontierExpansionHandler.SetProbeReuseRelayer(expansionAdapters.NewProbeReuseRelayer(
		shipRepo,
		gateGraphService,
		expansionAdapters.NewMarketSystemValueReader(marketRepoAdapter),
		expansionAdapters.NewRepositionerRelayDispatcher(tradeRouteCoordinatorHandler, gateGraphService),
	))
	frontierExpansionHandler.SetFrontierNeighborReader(expansionAdapters.NewFrontierNeighborScanner(
		gateGraphService, marketRepoAdapter,
	))
	// sp-a3yn slice C: connect the off-gate BUY seam (mirror each tick's signal into the bridge the
	// autosizer's explorer provider reads) and the explorer DISPATCH seam (warp a bought+dedicated
	// idle explorer to the off-gate target via slice-A ExecuteWarpRoute; on arrival slice A charts the
	// system so growFrontierGraph resumes). Both are optional injection — a bare deploy with the
	// explorer class disarmed buys nothing, so this dispatch never fires.
	frontierExpansionHandler.SetOffGateDemandSink(explorerOffGateBridge)
	frontierExpansionHandler.SetExplorerDispatchPort(expansionAdapters.NewExplorerWarpDispatcher(
		routeExecutor, shipRepo, ship.NewGraphWaypointSource(graphService),
	))
	if err := mediator.RegisterHandler[*expansionCmd.RunFrontierExpansionCoordinatorCommand](med, frontierExpansionHandler); err != nil {
		return fmt.Errorf("failed to register FrontierExpansionCoordinator handler: %w", err)
	}
	// sp-pvw3 `frontier status`: expose the coordinator's read-only live-state query through the
	// daemon. The handler already holds every port the view needs; the daemon just resolves the
	// running container and delegates.
	daemonServer.SetFrontierStatusProvider(frontierExpansionHandler)

	// Market-freshness auto-sizer (sp-orgp): the standing coordinator that keeps EVERY
	// scanned market fresh within an SLA by auto-sizing AND auto-buying probe capacity per
	// system — the freshness analogue of the frontier coverage auto-sizer above. It measures
	// per-system demand (markets × measured scan-cycle / SLA, corrected by the empirical
	// worst-case market age), declares/resizes/retires each market-bearing system's STANDING
	// scout post through the SAME scout-post repo the reconciler mans and partitions, and
	// buys probes under the SHARED money-guard stack (probebuy.GuardedProbeBuyer). It moves
	// and claims NOTHING. marketRepo satisfies the per-system freshness census
	// (SystemsFreshness); shipRepo the read-only FleetReader; transactionRepo the
	// ledger-derived, restart-safe cooldown/spend it shares with the frontier coordinator so
	// the two never collectively over-buy.
	freshnessSizerHandler := scoutingCmd.NewRunMarketFreshnessSizerCoordinatorHandler(
		marketRepo, scoutPostRepo, shipRepo, transactionRepo, nil, // nil = use RealClock
	)
	// Live treasury for the 25% guard — nil would fail-close every buy. Reuses the frontier
	// coordinator's api-backed reader (same seam, same guard).
	freshnessSizerHandler.SetTreasuryReader(expansionAdapters.NewTreasuryReader(apiClient))
	// Price-and-buy over the existing purchase_ship machinery, landing the probe undedicated
	// for the reconciler to relay — the SAME demand-proximal purchaser the frontier coordinator
	// uses (sp-hej4): the sizer names its neediest system as the target so the probe spawns at the
	// nearest scanned probe-yard, fail-open to the home yard on sparse scan data.
	freshnessSizerHandler.SetProbePurchaser(expansionAdapters.NewProbePurchaser(med, shipRepo, probeYardFinder))
	// The narrow, manning-preserving resize seam: UpdateHulls touches only the hull column so
	// a resize cannot clobber the manning the scout reconciler wrote to the same row.
	freshnessSizerHandler.SetHullUpdater(scoutPostRepo)
	// sp-u8jc/sp-gucu bootstrap-catch-22 fix: wire the "has a marketplace" signal over the SAME
	// GORM market repo (ChartedMarketSystemCounts — the era-scoped, fuel-excluded, MARKETPLACE-
	// trait census the dark-market scanner is built on), so a CHARTED-but-unscanned dense hub is
	// HELD (not retired "markets gone") and counted as initial-scan demand once armed. This makes
	// the fix ARM-able by a knob flip (hold_unscanned_market_posts=1); while that flag is 0 (the
	// default) the reader is read by nothing, so the coordinator is byte-identical to today. It is
	// the missing piece the sp-u8jc relay + probe-buyer need: the post must survive to be manned.
	freshnessSizerHandler.SetChartedMarketplaceReader(marketRepo)
	freshnessSizerHandler.SetEventRecorder(captainEventRepo) // emit coordinator error-loop events on reconcile streak breach
	// Per-tick live-config snapshots: the cooldown/spend knobs are `tune`-able live,
	// no restart needed.
	freshnessSizerHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	if err := mediator.RegisterHandler[*scoutingCmd.RunMarketFreshnessSizerCoordinatorCommand](med, freshnessSizerHandler); err != nil {
		return fmt.Errorf("failed to register MarketFreshnessSizerCoordinator handler: %w", err)
	}

	// Shipyard-backfill sweep (sp-rhju): the standing catch-up coordinator that closes the
	// charted-but-unscanned shipyard blind spot the market-tour-only scan (sp-42ow) left behind.
	// It enumerates known-shipyard systems the depth frontier reached but no market tour toured —
	// intersecting the era-agnostic SHIPYARD-trait set (waypointRepo.ListWithTrait) with the
	// CURRENT gate-reachable frontier (a dedicated ExpansionScanner for hop depth + reachability)
	// minus the era-scoped scanned set (shipyardInventoryRepo.ScannedSystems) — and declares
	// deeper-first sweep-once posts through the SAME scout-post repo the reconciler mans, bounded
	// by min(rate knob, idle probe supply). The probe's arrival rides the sp-rhju decoupled
	// shipyard scan and a heavy hit fires the existing heavy_yard_discovered event. It moves and
	// claims NOTHING; self-quiescing once the blind spot drains. Registration + restart-recovery +
	// live-tune are wired here; a thin launch verb calls DaemonServer.ShipyardBackfillCoordinator.
	shipyardBackfillHandler := scoutingCmd.NewRunShipyardBackfillCoordinatorHandler(
		expansionAdapters.NewChartedShipyardEnumerator(
			expansionAdapters.NewExpansionScanner(gateGraphService, marketRepoAdapter, shipRepo, playerRepo, waypointRepo),
			waypointRepo,
			// reach is passed PER TICK by the coordinator (the live-tunable backfill_max_hops
			// knob, full gate graph by default) — a charted shipyard is in-graph + relay-reachable,
			// so the sweep must reach the DEEP in-graph yards, not just the shallow frontier (sp-b8lf).
		),
		shipyardInventoryRepo,
		expansionAdapters.NewIdleProbeCounter(shipRepo),
		scoutPostRepo,
		nil, // nil = use RealClock
	)
	shipyardBackfillHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	if err := mediator.RegisterHandler[*scoutingCmd.RunShipyardBackfillCoordinatorCommand](med, shipyardBackfillHandler); err != nil {
		return fmt.Errorf("failed to register ShipyardBackfillCoordinator handler: %w", err)
	}

	// sp-y2ptq (epic sp-9le3x): the capacity-reconciler contract-capacity stack was DELETED. The
	// dedicated contract scaler (registered above) replaces it as the contract-fleet capacity owner, and
	// the jump gate is COMPLETE so the gate-depot demand machinery is dead weight. All reconciler wiring
	// (SENSE/PLAN/DIFF/GOVERN/CONVERGE, the gate-shortfall reader, the depot launcher, the graduation
	// gate) is gone; nothing boot-standing or restart-recovering depends on it (RULINGS #2).

	// Auto-outfit coordinator (sp-buyd): the standing guarded auto-outfit coordinator — the
	// module analogue of the autosizer's hull-buying. Each tick it measures per-hull cargo
	// saturation from tour_leg_telemetry, catalogs available modules off the market cache,
	// and installs the highest-marginal-value (hull, module) upgrade behind a fail-closed
	// money/ceiling/cap guard stack. REGISTRATION ONLY — the coordinator is deliberately NOT
	// boot-standing-armed (deploy-inert): it runs only when explicitly started via
	// `workflow auto-outfit`, then survives restarts through the persisted-container recovery
	// idiom. Live-tunable via `tune --operation autooutfit`.
	autoOutfitHandler := grpc.NewAutoOutfitCoordinatorHandler(
		apiClient, shipRepo, persistence.NewTourTelemetryRepository(db), marketRepo, med, captainEventRepo, containerRepo,
	)
	if err := mediator.RegisterHandler[*autooutfitCmd.RunAutoOutfitCoordinatorCommand](med, autoOutfitHandler); err != nil {
		return fmt.Errorf("failed to register AutoOutfitCoordinator handler: %w", err)
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
	// Same gate graph: enables multi-jump travel AND the routability-check-before-spend
	// guard.
	arbCoordinatorHandler.SetGateGraph(gateGraphService)
	arbCoordinatorHandler.SetChartGateOnArrival(chartGateOnArrival) // sp-bcsu: chart cross-system arrivals
	// Wait out a mid-transit re-adoption before the resume path's jump, instead of
	// 4214'ing and burning the container restart budget on a routine arrival.
	arbCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// sp-dkj7: durably record a fresh buy's cost into the container config so a
	// restart-rebuilt resume reloads it and reports honest P&L (a resumed run skips the
	// completed buy, which otherwise leaves TotalCost=0 and over-states NetProfit).
	arbCoordinatorHandler.SetCostPersister(grpc.NewArbCostConfigPersister(containerRepo))
	// sp-78ai L2: convert an arb/idle-arb leg's PLANNED absorption hold into an
	// EXECUTED recovery shadow at sale completion (shared ledger instance above).
	arbCoordinatorHandler.SetAbsorptionLedger(absorptionLedger)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunArbCoordinatorCommand](med, arbCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ArbCoordinator handler: %w", err)
	}

	// Tour-run coordinator (sp-1ek0): a one-shot, captain-directed, guarded multi-hop
	// trade tour. Wired with the same ports as arb/trade-route (so its buy/sell/navigate
	// legs resolve to the identical RouteExecutor-backed daemon handlers, and it inherits
	// the shared gate graph for multi-jump travel) PLUS the depth-aware planner
	// (routingClient), the era-scoped waypoint repository (real travel-time coordinates),
	// and the tour telemetry repository (planned-vs-realized for the graduation report).
	// DaemonServer.StartTourRun launches the container.
	tourCoordinatorHandler := tradeRouteCmd.NewRunTourCoordinatorHandler(
		med, shipRepo, marketRepo, waypointRepo, persistence.NewTourTelemetryRepository(db),
		routingClient, marketScanner, nil, apiClient,
	)
	tourCoordinatorHandler.SetGateGraph(gateGraphService)
	tourCoordinatorHandler.SetChartGateOnArrival(chartGateOnArrival) // sp-bcsu: chart cross-gate tour arrivals
	// sp-mtvg: wire the global best-sink reader so the tour coordinator can SEE (and count
	// on tour_candidates_dropped_total) the profitable exotic lanes whose sink is beyond the
	// 1-gate-hop tour graph. The raw GORM repo carries BestSinksAcrossSystems; read-only.
	tourCoordinatorHandler.SetOutOfHorizonSinkScanner(marketRepo)
	// Inject the config-resolved ABSOLUTE artifact path so the executor reads the
	// market model regardless of the daemon's cwd (the launchd daemon's cwd is not the
	// repo root).
	tourCoordinatorHandler.SetModelArtifactPath(cfg.Routing.ModelArtifactPath)
	// sp-zhii: durably record an in-flight margins-death reposition (its target
	// system+waypoint) into the container config so a restart-rebuilt resume completes the
	// jump toward the same ground instead of re-planning at an intermediate hop (RULINGS #2).
	tourCoordinatorHandler.SetRepositionPersister(grpc.NewTourRepositionConfigPersister(containerRepo))
	// sp-78ai L3: wire the SAME absorption ledger the idle-arb/arb engines use so the
	// tour reserves its planned tranches (fleet-wide A-cap), nets outstanding depth into
	// each plan, and converts sold sinks into recovery shadows — the flagship writer/reader
	// of the cross-engine coordination. The shared PlannedTTLSlack sizes reservation
	// lifetimes.
	tourCoordinatorHandler.SetAbsorptionLedger(absorptionLedger, cfg.Absorption.PlannedTTLSlack)
	tourCoordinatorHandler.SetEventRecorder(captainEventRepo) // sp-6wxq: emit coordinator error-loop event when the dynamic-budget resolve stays unreadable
	// sp-o4wa: inject the noise-goods cargo blocklist (FUEL/ALUMINUM/PLASTICS are sub-70-cr/u
	// tempo drag) so the tour planner never selects a listed good as cargo. Global list from
	// [trade_fleet].cargo_blocklist, mirroring the contract pre_positioning.blocklist boot
	// injection. Absent/empty ⇒ no filtering ⇒ byte-identical; arming = adding goods to
	// config.yaml + daemon restart. Cargo only — refueling never reads the tour snapshot.
	tourCoordinatorHandler.SetCargoBlocklist(cfg.TradeFleet.CargoBlocklist)
	// sp-v34b: stamp the tour-scan load policy so the shared arrival + post-trade scans
	// SAMPLE the deliberate price-impact instrumentation (the top API consumer, ~80% of
	// API) instead of scanning every market around every trade. Resolved from [trade_impact]
	// config (scan_max_age_seconds / impact_sample_rate; restart to apply — the same
	// refit-per-era path the model's coefficients already use).
	tourCoordinatorHandler.SetScanPolicy(cfg.TradeImpact.ResolvedScanPolicy())
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTourCoordinatorCommand](med, tourCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TourCoordinator handler: %w", err)
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

	storageOperationRepo := persistence.NewStorageOperationRepository(db, nil) // nil = use RealClock

	// Create storage coordinator for STORAGE_ACQUIRE_DELIVER tasks
	// This enables manufacturing pipelines to acquire cargo from storage ships
	storageCoordinator := storageApp.NewInMemoryStorageCoordinator()
	// Durable cost-basis persistence for warehouse stock (storage infra): the storage
	// operation repo persists per-good basis out-of-band and reloads it on recovery
	// (RULINGS #2); nil-safe if omitted.
	storageCoordinator.SetCostBasisStore(storageOperationRepo)
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

	// Warehouse coordinator (sp-dchv Lane B): passive inventory buffer on a
	// dedicated hull. Shares the SAME storageCoordinator as gas + manufacturing,
	// so a warehouse hull's deposits (tour/trade legs) and withdrawals
	// (STORAGE_ACQUIRE_DELIVER executor) flow through one coordinator, and the
	// StorageRecoveryService below rebuilds its cargo on restart for free.
	warehouseHandler := storageCmd.NewRunWarehouseHandler(med, shipRepo, storageOperationRepo, storageCoordinator, nil)
	if err := mediator.RegisterHandler[*storageCmd.RunWarehouseCommand](med, warehouseHandler); err != nil {
		return fmt.Errorf("failed to register RunWarehouse handler: %w", err)
	}

	// Inventory-first contract sourcing (sp-dchv Lane D). The finder reads
	// warehouse (Lane B) stock from the SAME shared storage coordinator the
	// warehouse registers its hull with, so a contract worker withdraws a stocked
	// good in-system at zero ask before buying it, and the fleet coordinator's
	// defer gate treats that stock as free (never parks a contract inventory can
	// fulfill). Nil-safe throughout: no warehouse / no stock / any read error
	// falls through to the pre-existing market path (RULINGS #1). Withdrawal is
	// single-system (RULINGS #14) and transfers from Lane B's dedicated hull
	// without claiming it (RULINGS #7).
	contractInventoryFinder := contractServices.NewStorageInventoryFinder(storageOperationRepo, storageCoordinator)
	contractFleetCoordinatorHandler.SetInventoryFinder(contractInventoryFinder)

	// Warehouse-first construction sourcing (sp-crjla): the construction drain WITHDRAWS a gate
	// material from an in-system depot warehouse before buying it at market, so a depot stocker is
	// the sole buyer→warehouse and construction never double-buys the same units (RULINGS #4). It
	// reuses the SAME shared finder + coordinator the contract path uses (one warehouse-query brain,
	// not a divergent parallel one) and the construction executor as the warehouse-leg navigator.
	// The same StorageRecoveryService that repopulates the coordinator on restart makes this
	// restart-safe (RULINGS #2). Byte-identical when no depot warehouse holds the material — so it is
	// arm-safe to deploy before the reconciler half emits gate-depot demand.
	constructionCoordinatorHandler.SetInventorySource(contractInventoryFinder, storageCoordinator, apiClient, constructionExecutor)

	// sp-o477: the in-memory storage coordinator is populated only by live
	// deposits, so on daemon restart it starts EMPTY and the inventory-first path
	// wired just above sees 0 available — contracts market-buy goods already
	// standing in the warehouse. Wire the StorageRecoveryService into daemon boot
	// so it reloads each running storage operation's ships from the API and
	// re-registers them with THIS SAME shared coordinator + operation repo (the
	// exact singletons the finder above reads — not a second instance). Invoked in
	// DaemonServer.Start AFTER container recovery; idempotent + fail-open.
	daemonServer.SetStorageRecovery(storageApp.NewStorageRecoveryService(storageOperationRepo, apiClient, storageCoordinator))

	// sp-kqxe: emit a structured event on each warehouse→hauler buffer draw so
	// warehouse ROI (buffer hit-rate, served-from-buffer, contract-leg-avoided) is
	// measurable. The GORM recorder persists to warehouse_withdrawals; nil clock =
	// RealClock. Additive/fail-open — a record error never fails the draw.
	contractWorkflowHandler := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil,
		contractCmd.WithInventorySourcing(contractInventoryFinder, storageCoordinator, apiClient),
		contractCmd.WithWithdrawalRecording(persistence.NewWithdrawalEventRepository(db), nil))
	if err := mediator.RegisterHandler[*contractCmd.RunWorkflowCommand](med, contractWorkflowHandler); err != nil {
		return fmt.Errorf("failed to register ContractWorkflow handler: %w", err)
	}

	// Wire the tour coordinator's haul-to-storage pre-positioning subsystem (sp-dchv
	// Lane C), now that the shared storage coordinator + operation repo exist. The
	// coordinator was constructed earlier (above), so this injects the deps via a
	// setter: the Lane A demand miner (over the same db), the warehouse-op finder
	// (storageOperationRepo), and the resolved config from cfg.Contract.PrePositioning.
	// Live-config (sp-ts82 pattern): the daemon reads these knobs from config.yaml at
	// every boot, so a captain retunes by editing config.yaml and restarting. OFF
	// unless enabled AND a warehouse hull is running in the tour's home system.
	pp := cfg.Contract.PrePositioning
	tourCoordinatorHandler.SetPrePositioning(
		storageCoordinator,
		storageOperationRepo,
		persistence.NewDemandMiner(db),
		tradingSvc.DepositCandidateConfig{
			Enabled:              pp.Enabled,
			TopN:                 pp.TopN,
			MinRecurrence:        pp.MinRecurrence,
			MinSavingsPerUnit:    pp.MinSavingsPerUnit,
			BuyLegSavingsPerUnit: pp.BuyLegSavingsPerUnit,
			Allowlist:            pp.Allowlist,
			Blocklist:            pp.Blocklist,
		},
		pp.CapitalCeilingPct,
	)

	// Stocker coordinator (sp-zdwg): a dedicated hull that fills the home warehouse the
	// tours rationally won't (sp-dchv — deposit legs lose to direct sells at every re-plan;
	// the stocker dedicates capacity instead of distorting tour objectives). Wired with the
	// same ports as tour/arb/trade-route (so its buy/navigate legs resolve to the identical
	// RouteExecutor-backed daemon handlers, and it inherits the shared gate graph for
	// multi-jump travel + the arrival event bus for the resume-safe in-transit wait) PLUS
	// the shared storage coordinator (deposit protocol + warehouse reads), the warehouse-op
	// finder (storageOperationRepo), and the Lane A demand miner (over the same db). The
	// pre-positioning economics (min-recurrence/min-savings/allow-block/ceiling-pct) come
	// from the same cfg.Contract.PrePositioning the tour reads; the stocker is launched
	// explicitly (a dedicated hull), so it runs its economics regardless of pp.Enabled (the
	// tour's opportunistic-deposit switch). DaemonServer.StartStocker launches the container.
	stockerCoordinatorHandler := tradeRouteCmd.NewRunStockerCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
		storageCoordinator, storageOperationRepo, persistence.NewDemandMiner(db),
		tradingSvc.DepositCandidateConfig{
			Enabled:              pp.Enabled,
			TopN:                 pp.TopN,
			MinRecurrence:        pp.MinRecurrence,
			MinSavingsPerUnit:    pp.MinSavingsPerUnit,
			BuyLegSavingsPerUnit: pp.BuyLegSavingsPerUnit,
			Allowlist:            pp.Allowlist,
			Blocklist:            pp.Blocklist,
		},
		pp.CapitalCeilingPct,
		waypointRepo, // sp-9274: cache-only coords for the distance-aware residual buy-leg (fail-open)
	)
	stockerCoordinatorHandler.SetGateGraph(gateGraphService)
	stockerCoordinatorHandler.SetChartGateOnArrival(chartGateOnArrival) // sp-bcsu: chart cross-system haul arrivals
	stockerCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// sp-j6uz: emit a structured stock-IN event on each CONFIRMED stocker→warehouse deposit so
	// downstream analysis can measure depot throughput/coverage (the stock-IN mirror of the
	// kqxe withdrawal recorder wired above). Additive + fail-open — a record error never fails
	// a deposit.
	stockerCoordinatorHandler.SetStockingRecorder(persistence.NewStockingEventRepository(db))
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunStockerCoordinatorCommand](med, stockerCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register StockerCoordinator handler: %w", err)
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
