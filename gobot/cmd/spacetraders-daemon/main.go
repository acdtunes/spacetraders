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
	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	expansionAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/expansion"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractQuery "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
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
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
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
	// sp-01wc: wire the CAS-retry knob (live by default; cas_retry_disabled reverts
	// ship saves to sp-60ff last-write-wins). Setter injection keeps the 4
	// NewShipRepository call sites untouched.
	shipRepoImpl.SetCASRetryPolicy(cfg.Daemon.MaxCASRetries, cfg.Daemon.CASRetryDisabled)
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
	if err := watchkeeper.RecordDeployIfChanged(
		context.Background(), captainEventRepo, cfg.Captain.PlayerID,
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

	// Jump handler (sp-n0x7: was never registered, so dispatching
	// JumpShipCommand always failed with "no handler registered")
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

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, waypointRepo, shipRepo, playerRepo, routingClient, goodsFactoryRepo, apiClient, socketPath, &cfg.Metrics, cfg.Contract, cfg.TradeFleet, cfg.WorkerRebalancer, cfg.Manufacturing, cfg.Scouting, cfg.FleetAutosizer, cfg.Bootstrap, cfg.CapacityReconciler, cfg.ShipResync, shipEventBus)
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
	contractFleetCoordinatorHandler.SetAbsorptionLedger(absorptionLedger, cfg.Absorption.IdleArbConsultDisabled, cfg.Absorption.PlannedTTLSlack)
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
	tradeFleetCoordinatorHandler.SetEventRecorder(captainEventRepo) // sp-6wxq: emit coordinator error-loop events on reconcile streak breach
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeFleetCoordinatorCommand](med, tradeFleetCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeFleetCoordinator handler: %w", err)
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
	// sp-iv65: wire the trailing-median source for the ladder-chase input price ceiling. The
	// priceHistoryRepo (already built above for the market scanner) reads the sell_price series
	// buyGood checks each input ask against; left unset the ceiling is fail-open.
	factoryCoordinatorHandler.SetPriceHistoryReader(priceHistoryRepo)
	// sp-rh2z: wire the DB-backed realized-P&L ledger the chain kill-switch judges (per-good
	// factory buys/sells + tour realized net + refuel pool over the rolling window). Left unset
	// the kill-switch is fail-open (disabled) — the optional-port contract; the daemon turns it
	// on by injecting the real reader here.
	factoryCoordinatorHandler.SetChainPnLReader(persistence.NewGormChainPnLRepository(db))
	// sp-ev0n: live per-op worker cap. The coordinator resolves its concurrent-hull cap
	// from its own container config every production pass, so a `goods factory workers`
	// change converges the fan-out with no restart — the factory mirror of the contract
	// coordinator's live standby-station provider (sp-jcke). The value persists across a
	// restart (worker_cap is not a config.yaml-reinjected key, RULINGS #2).
	factoryCoordinatorHandler.SetWorkerCapProvider(grpc.NewFactoryWorkerCapConfigProvider(containerRepo))
	if err := mediator.RegisterHandler[*goodsCmd.RunFactoryCoordinatorCommand](med, factoryCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register GoodsFactoryCoordinator handler: %w", err)
	}

	// Register the standing construction-supply drain (sp-382j): the coordinator that rebuilds
	// gate-construction EXECUTION post-sp-jav2 — a THIN drain on the SHARED ProductionExecutor
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
	// Wire an EXPLICIT real clock (sp-vh1s): sp-vh1s made PollForProduction and the gate output-buy
	// throughput-pacing window clock-driven. Unlike the goods-factory path (which defaults nil→RealClock
	// inside NewRunFactoryCoordinatorHandler before building its executor), the construction drain builds
	// its producer directly here, so it must supply the clock itself — otherwise the unified gate-fill
	// nil-panicked on every construction tick at e.clock.Now(). The constructor also defaults a nil clock
	// (defense in depth), but the pacing needs a real, monotonic clock wired on the live gate path.
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo, marketRepoAdapter, goodsMarketLocator, shared.NewRealClock(), apiClient)
	constructionExecutor.SetConstructionRepo(api.NewConstructionSiteRepository(apiClient, playerRepo))
	// The activator is the SURVIVING SupplyMonitor (sp-jav2 kept the subpackage): NO new
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

	// Register the standing factory-SITING coordinator (sp-vdld): the standing "brain" that
	// automates factory discovery, placement, and capacity planning. Each slow tick it SCANs
	// candidate (good,system) sites (export-site hard gate + in-system input eligibility +
	// freshness), SCOREs them by branchPL × tour-alignment − competition − staleness, MAINTAINs
	// the top-K portfolio (K = floor(haulers / workers_per_chain), C3), ACTs by launching missing
	// chains THROUGH the guard stack (StartGoodsFactory iterations=-1, so the child runs 2dv4 +
	// a5j7 + C2 + r5a6 on its own passes) and retiring fallen ones with hysteresis, then EMITs
	// scout-demand for stale-but-promising sites on the captain proposal channel. It reuses the
	// SAME resolver/locator/guard the goods-factory coordinator holds, so it prices chains exactly
	// as the launch path does. LIVE BY DEFAULT (Admiral: no dark-shipping); every weight/cap
	// resolves live from config.yaml [manufacturing.siting]. Standing coordinators are CLI/gRPC
	// first-launched then recovery-adopted — registering the handler makes a launched or recovered
	// container runnable, but nothing auto-starts on daemon boot.
	// The concrete port adapters (scanner data source, chain projector via the ChainMarginGuard,
	// portfolio controller over StartGoodsFactory/StopGoodsFactory, HAULER worker counter, and
	// scout-demand emitter) are assembled inside grpc.NewSitingCoordinatorHandler from the SAME
	// resolver/locator/repos the goods-factory path uses. The tour-alignment provider is left
	// unset there for now (the C1 stock-draw signal has no persisted read path yet and no
	// tour_leg_telemetry throughput reader exists), so scoring ranks on branchPL alone — the
	// documented monotonic proxy — until that seam lands (sp-vdld).
	sitingCoordinatorHandler := grpc.NewSitingCoordinatorHandler(
		daemonServer, goodsResolver, goodsMarketLocator, marketRepo, marketRepoAdapter, shipRepo, captainEventRepo,
	)
	if err := mediator.RegisterHandler[*goodsCmd.RunSitingCoordinatorCommand](med, sitingCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register SitingCoordinator handler: %w", err)
	}

	// Register the standing contract-HUB placement coordinator (sp-q2zq): the demand-side
	// SIBLING of the factory-siting coordinator above. Each slow tick it SCANs candidate hubs
	// (the cheapest in-system EXPORT/EXCHANGE source for each recent-contract good — single
	// system, RULINGS #14), SCOREs them by the MARGINAL payment-weighted buy-leg each eliminates
	// (greedy facility-location over an EWMA-smoothed demand signal, so a central cluster
	// self-limits and outliers score high), and PLACEs each new / idle-unhomed contract hauler at
	// argmax marginal, growing the hub portfolio with the fleet. Phase 1 is placement-only: it
	// never re-homes an already-homed hull (zero thrash) and is idle-only (never strands a hull
	// mid-contract). LIVE BY DEFAULT (Admiral: no dark-shipping); every knob resolves live from
	// the launch config (RULINGS #5). Like the siting sibling, registering the handler makes a
	// launched or recovered container runnable — nothing auto-starts on daemon boot.
	// The concrete port adapters (candidate scan over the market repo, demand EWMA source over the
	// recent-contracts projection, hauler-home source over the ship repo, and the home ASSIGNER
	// that persists to the contract coordinator's standby-station set via the daemon single-writer
	// `fleet hub` path — RULINGS #2/#3) are assembled inside grpc.NewContractHubCoordinatorHandler
	// from the SAME contract/market/waypoint/ship repos the contract path already uses.
	contractHubCoordinatorHandler := grpc.NewContractHubCoordinatorHandler(
		daemonServer, contractRepo, marketRepoAdapter, waypointRepo, shipRepo,
	)
	if err := mediator.RegisterHandler[*contractCmd.RunContractHubCoordinatorCommand](med, contractHubCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ContractHubCoordinator handler: %w", err)
	}

	// sp-7gr2: the persisted, fetch-through gate-graph resolver. travel() BFS-walks
	// it to cross a multi-hop gap (KA42→PA3→UQ16→JP61 — the single-edge assumption
	// that crashed a laden frigate at the home gate), and the arb pre-buy guard
	// route-checks a cross-system sell leg through it BEFORE spending. Shared by
	// the trade-route circuit, the one-shot arb, and (sp-42ow) the autosizer's
	// reachable-yard ranking so they all see one cache/graph. Constructed here,
	// ahead of the autosizer wiring that consumes it.
	// Captured so the sp-ywh1 gate-reconcile widening can read backoff markers straight from
	// the SAME store the gate graph routes over (one cache/graph, era-scoped) — see
	// scoutPostCoordinatorHandler.SetUnreadableGateProvider below.
	gateEdgeRepo := persistence.NewGormGateEdgeRepository(db)
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
	// st-x00 (re-scoped by st-5le): the capacity reconciler's CAPITAL tier emits its
	// tier-4 contract-delivery hull demand into sp-1txd's SINGLE guarded buy path via
	// this bridge — NOT a second guard/budget stack. One object: the reconciler's
	// GOVERN emitter writes it (capacity.CapitalDemandSink, wired below), the autosizer
	// reads it as a registered demand provider. DORMANT — sp-1txd's classDisabled skips
	// the unknown contract_delivery class ("unknown class: never act") until an arming
	// lane wires it in — so registering it changes no live behaviour and nothing
	// auto-buys. sp-1txd's absolute fleet ceiling + per-tick cap stay authoritative over
	// the COMBINED post-buy fleet count across every provider, this one included.
	contractDeliveryDemand := capacityAdapters.NewContractDeliveryDemandBridge()

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
	fleetAutosizerHandler.AddDemandProvider(contractDeliveryDemand) // st-x00: contract-delivery capital demand → sp-1txd buy path (dormant until armed)
	if err := mediator.RegisterHandler[*fleetCmd.RunFleetAutosizerCoordinatorCommand](med, fleetAutosizerHandler); err != nil {
		return fmt.Errorf("failed to register FleetAutosizerCoordinator handler: %w", err)
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

	// Trade-route coordinator (sp-zewt): a single-hull pure-arbitrage circuit that runs
	// as a recovery-safe daemon container. Registered in the daemon mediator so its
	// NavigateRouteCommand legs resolve to the RouteExecutor-backed handler (orbit →
	// refuel → NavigateDirect → arrival events) instead of the CLI runner's hand-rolled
	// in-process nav — subsuming the 2sam/sj7p patches. marketScanner drives the live
	// stale-ask guard (2sam hazard b). DaemonServer.StartTradeRoute launches the container.
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
	// sp-3vg8: now that the shared stored-adjacency gate graph exists (built just above), wire
	// the siting scorer's worker-reachability signal. The provider reuses the fleet's idle-worker
	// locator + RepositionPath (no reinvented routing), so vdld deprioritizes far-cluster chains it
	// cannot man (C81/GS93) instead of launching them workerless. The penalty weight is live by
	// default (siting_weight_worker_reachability → 1.0); the Analyst tunes it from config.yaml.
	sitingCoordinatorHandler.SetWorkerReachabilityProvider(
		grpc.NewSitingWorkerReachabilityProvider(shipRepo, gateGraphService),
	)
	// sp-8l3o: the shared ship-arrival event bus lets travel() wait out a hull
	// re-adopted mid-transit before any movement (jump/navigate) instead of 4214'ing
	// and burning the container restart budget on a routine arrival.
	tradeRouteCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// sp-78ai L4: read-only absorption consult (trade-analyst Q1: "circuits write
	// nothing") — scanLanes excludes a lane whose sell side is shadowed or whose
	// reserved depth can't absorb a circuit tranche. Shares the SAME ledger instance
	// L2 (idle-arb) writes to, above; TradeRouteConsultDisabled is the independent
	// operator kill-switch for this read path only.
	tradeRouteCoordinatorHandler.SetAbsorptionLedger(absorptionLedger, cfg.Absorption.TradeRouteConsultDisabled)
	// sp-tl68: wire the era-3 price-impact coefficients + the shared cooldown ledger into
	// lane ranking. scanLanes now ranks on the EFFECTIVE spread (snapshot less the
	// self-compression this hull's volume would cause + the live shared cooldown debt), and
	// runCircuit accrues each completed leg's debt back to the shared ledger. The operator
	// kill-switch [trade_impact].disabled leaves the handler on the inert (snapshot) model.
	if !cfg.TradeImpact.Disabled {
		tradeRouteCoordinatorHandler.SetLaneImpactModel(
			cfg.TradeImpact.ResolvedBuyImpact(),
			cfg.TradeImpact.ResolvedSellImpact(),
			laneCooldownLedger,
		)
	}
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeRouteCoordinatorCommand](med, tradeRouteCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeRouteCoordinator handler: %w", err)
	}

	// sp-9l4p: teach the shared navigate handler to route a CROSS-SYSTEM destination
	// through the trade coordinator's gate-crossing travel machinery (RepositionToWaypoint)
	// instead of fail-closing ("waypoint <dest> not found in cache for system <current>").
	// The intra-system route planner cannot cross systems, so a bare cross-system navigate
	// used to loud-fail — the live -90k copper-contract stall and the ceiling on the
	// frontier depth campaign's edge probe-buys (an idle hull navigated from home to a
	// frontier shipyard). One shared-seam wire fixes BOTH victims; it mutates the already
	// registered handler in place so every NavigateRouteCommand dispatch sees it. Additive
	// and inert until here — same-system navigation is untouched, and without this wire the
	// handler keeps its exact pre-fix fail-closed behaviour.
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
	// leave the warning off (pre-k7q5 behavior).
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
	// age breaching the post's freshness target without advancing. nil disables the watchdog
	// (pre-5les behavior); it never affects manning when unwired.
	scoutPostCoordinatorHandler.SetSystemFreshnessReader(marketRepo)
	// sp-5les: the watchdog's manning_stall_* knobs are live-tunable — snapshot this
	// container's own persisted config each tick (the SAME reader the freshness sizer uses) so a
	// `spacetraders tune scoutpost ...` lands on the next tick with no restart.
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

	// Worker-rebalancer coordinator (sp-f5pr): the standing coordinator that ferries idle
	// undedicated light-haulers cross-system to worker-starved factory systems so a factory
	// posting "No in-system worker" self-heals without captain hand-holding. It derives ALL
	// state from ship + container rows (zero new persisted state, restart-safe), claims
	// nothing directly (each ferry claims its own hull under operation="worker_ferry", never
	// poaching a pinned/reserved hull), and reuses the trade-route coordinator's multi-jump
	// travel() via the ferry worker below. The container-query adapter reads RUNNING factory
	// + worker_ferry rows; waypointRepo supplies ferry destinations. Tuning is resolved live
	// from config.yaml [worker_rebalancer].
	workerRebalancerCoordinatorHandler := tradeRouteCmd.NewRunWorkerRebalancerCoordinatorHandler(
		shipRepo,
		daemonClientLocal,
		grpc.NewWorkerRebalancerContainerQuery(containerRepo),
		waypointRepo,
		nil, // nil = use RealClock
	)
	// Shares the SAME persisted gate graph as the trade circuit / scout relays (one
	// cache/graph) to rank the nearest idle light by jump hops. nil would disable ferrying
	// (fail-closed park).
	workerRebalancerCoordinatorHandler.SetGateGraph(gateGraphService)
	workerRebalancerCoordinatorHandler.SetEventRecorder(captainEventRepo) // sp-6wxq: emit coordinator error-loop events on reconcile streak breach
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunWorkerRebalancerCoordinatorCommand](med, workerRebalancerCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register WorkerRebalancerCoordinator handler: %w", err)
	}
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
	// The expansion queue's frontier enumerator: one BFS over the SAME persisted gate graph
	// the trade circuit and scout relays share, annotated with market-data counts and a
	// swept/never-scanned flag from the waypoint catalog (sp-gb7h — so a genuinely-barren
	// scanned system stops being re-scouted). nil would leave the coordinator serving only
	// unmanned-slot demand.
	frontierExpansionHandler.SetExpansionScanner(expansionAdapters.NewExpansionScanner(
		gateGraphService, marketRepoAdapter, shipRepo, playerRepo, waypointRepo,
	))
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
	freshnessSizerHandler.SetEventRecorder(captainEventRepo) // emit coordinator error-loop events on reconcile streak breach
	// sp-vwek: per-tick live-config snapshots — the motivating retune surface (the
	// cooldown/spend knobs hand-edited on 2026-07-15 are now `tune`-able live).
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

	// Capacity reconciler (epic st-7zk, foundation st-fyr): the standing declarative
	// reconciler that drives the contract-delivery machine's actual capacity topology toward
	// a computed desired topology (SENSE → PLAN → DIFF → GOVERN → CONVERGE), maximizing
	// per-hull-sustained $/hr, capex-paced. REGISTRATION ONLY — the coordinator is
	// deliberately NOT boot-standing-armed (deploy-inert): it runs only when explicitly
	// started via `workflow capacity-reconciler`, then survives restarts through the
	// persisted-container recovery idiom. The foundation wiring is the provably-inert NoOp
	// chain (empty desired topology → zero actions); each epic lane replaces exactly one
	// component here (SENSE st-7ee, planner st-hlw, differ st-zr0, actuator st-5ig, governor
	// st-x00, proposals st-0h8 — see internal/domain/capacity/CONTRACTS.md). The
	// captain/DISABLED kill switch is the watchkeeper Workspace over the SAME workspace dir
	// the supervisor polls, re-read at the top of every tick.
	capacityReconcilerHandler := capacityCmd.NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, capacityAdapters.NewSensor(db, expansionAdapters.NewTreasuryReader(apiClient)), capacity.NewHeuristicPlanner()), // st-7ee SENSE + st-hlw PLAN
		capacity.NewLadderDiffer(),                       // st-zr0 DIFF — cheapest-lever-first gap closure
		capacity.NewCapexEmitter(contractDeliveryDemand), // st-x00 GOVERN — thin emitter: cheap tiers → Approved, capital tier → sp-1txd demand path (no second guard stack)
		// st-5ig CONVERGE actuator (cheap tiers 1-3): each verb drives its EXISTING
		// primitive; ExecuteCapital is fail-closed (st-0h8 owns the capital path).
		capacityAdapters.NewActuator(
			capacityAdapters.NewMediatorReassigner(med),                                         // tier-1 reassign -> fleet-assign command
			capacityAdapters.NewMediatorRepositioner(med),                                       // tier-2 reposition -> navigate command
			capacityAdapters.NewWorkerRebalanceEnsurer(containerRepo, daemonServer, playerRepo), // tier-2 workers -> ensure the sp-f5pr rebalancer runs
			capacityAdapters.NewWarehouseBufferConfigurator(db, shared.NewRealClock()),          // tier-3 buffer -> depot supported-goods whitelist writer
			capacityAdapters.NewTokenPlayerResolver(apiClient, playerRepo),                      // reconciling player from the ambient auth token
		),
		// st-0h8 CONVERGE proposal channel (tier-4 capital): files a capex proposal
		// for human approval as a DEFERRED captain nudge carrying the full ROI
		// evidence (reusing the EventScoutPostProposal outbox idiom). Filing spends
		// NOTHING — capital executes only later via the approval-execution path
		// (ProposalApprovalExecutor), past the invariant-4 gate — so this swap is
		// deploy-inert (the contract_delivery capital class stays dormant; the
		// arming lane st-cpc drives approvals).
		capacityAdapters.NewProposalChannel(captainEventRepo, shared.NewRealClock()),
		watchkeeper.NewWorkspace(cfg.Captain.WorkspaceDir),
		nil, // nil = use RealClock
	)
	capacityReconcilerHandler.SetEventRecorder(captainEventRepo) // emit coordinator error-loop events on reconcile streak breach
	if err := mediator.RegisterHandler[*capacityCmd.RunCapacityReconcilerCoordinatorCommand](med, capacityReconcilerHandler); err != nil {
		return fmt.Errorf("failed to register CapacityReconcilerCoordinator handler: %w", err)
	}

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
	// sp-7gr2: same gate graph — enables multi-jump travel AND the routability-check-
	// before-spend guard that would have refused the JP61 buy at the source instead of
	// crashing laden at the home gate.
	arbCoordinatorHandler.SetGateGraph(gateGraphService)
	arbCoordinatorHandler.SetChartGateOnArrival(chartGateOnArrival) // sp-bcsu: chart cross-system arrivals
	// sp-8l3o: wait out a mid-transit re-adoption before the resume path's jump — the
	// exact incident (arb-run-TORWIND-21 re-adopted mid in-system hop, jumped, 4214'd,
	// then rode out the 5s/30s/120s restart backoff to self-heal, consuming the whole
	// MaxRestartAttempts budget on a routine arrival).
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
	// sp-wj0h: inject the config-resolved ABSOLUTE artifact path so the executor reads
	// the market model regardless of the daemon's cwd (the launchd daemon's cwd is not
	// the repo root, which DOA'd the first tour on the old cwd-relative constant).
	tourCoordinatorHandler.SetModelArtifactPath(cfg.Routing.ModelArtifactPath)
	// sp-zhii: durably record an in-flight margins-death reposition (its target
	// system+waypoint) into the container config so a restart-rebuilt resume completes the
	// jump toward the same ground instead of re-planning at an intermediate hop (RULINGS #2).
	tourCoordinatorHandler.SetRepositionPersister(grpc.NewTourRepositionConfigPersister(containerRepo))
	// sp-78ai L3: wire the SAME absorption ledger the idle-arb/arb engines use so the
	// tour reserves its planned tranches (fleet-wide A-cap), nets outstanding depth into
	// each plan, and converts sold sinks into recovery shadows — the flagship writer/reader
	// of the cross-engine coordination. TourConsultDisabled is the operator escape hatch;
	// the shared PlannedTTLSlack sizes reservation lifetimes.
	tourCoordinatorHandler.SetAbsorptionLedger(absorptionLedger, cfg.Absorption.TourConsultDisabled, cfg.Absorption.PlannedTTLSlack)
	tourCoordinatorHandler.SetEventRecorder(captainEventRepo) // sp-6wxq: emit coordinator error-loop event when the dynamic-budget resolve stays unreadable
	// sp-v34b: stamp the tour-scan load policy so the shared arrival + post-trade scans
	// SAMPLE the deliberate price-impact instrumentation (the top API consumer, ~80% of
	// API) instead of scanning every market around every trade. Resolved from [trade_impact]
	// config (scan_max_age_seconds / impact_sample_rate; restart to apply — the same
	// refit-per-era path the model's coefficients already use). scan_sampling_disabled
	// reverts to pre-sp-v34b full-scan behavior.
	if scanPolicy, on := cfg.TradeImpact.ResolvedScanPolicy(); on {
		tourCoordinatorHandler.SetScanPolicy(scanPolicy)
	}
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

	// sp-jav2 X2: the parallel task-style manufacturing coordinator and its task worker were
	// retired. The survivor goods_factory_coordinator (registered above) is the sole factory
	// coordinator design.

	fmt.Println("\n✓ Daemon is ready to accept connections")
	fmt.Println("Press Ctrl+C to stop")

	// Start serving (blocks until shutdown)
	if err := daemonServer.Start(); err != nil {
		return fmt.Errorf("daemon server error: %w", err)
	}

	fmt.Println("\nDaemon stopped")
	return nil
}
