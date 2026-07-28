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
	metricsAdapter "github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	parkedSensingAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractQuery "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
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
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	probeBuyerFleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/probebuyerfleet/commands"
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
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/daemonlock"
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

	// Single-writer guard (sp-wrh84): take a Postgres SESSION advisory lock for
	// this player BEFORE recovering any containers, so two daemons can never write
	// the same player's game state — even past a PID-file/socket mismatch or a
	// manual --force. The lock auto-releases when the pinned connection closes at
	// shutdown (or drops on crash). SQLite (tests/local) is already a single-writer
	// store, so the guard is Postgres-only.
	if cfg.Database.Type == "postgres" {
		sqlDB, err := db.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying db for advisory lock: %w", err)
		}
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Second)
		playerLock, err := daemonlock.NewPostgresPlayerLock(lockCtx, sqlDB)
		if err != nil {
			lockCancel()
			return fmt.Errorf("failed to create daemon advisory lock: %w", err)
		}
		if err := daemonlock.AcquireExclusive(lockCtx, playerLock, cfg.Captain.PlayerID); err != nil {
			lockCancel()
			_ = playerLock.Close()
			return err
		}
		lockCancel()
		// Hold the lock (pinned connection) for the whole daemon lifetime; the
		// deferred Close releases it at shutdown (LIFO: runs before database.Close).
		defer func() { _ = playerLock.Close() }()
		fmt.Printf("Daemon advisory lock acquired for player %d\n", cfg.Captain.PlayerID)
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
	// The limiter-pressure EWMA half-life the probe-sensing coordinator sheds scanning
	// against (RULINGS #5 — an operational tuning number, not a rebuild). 0/unset -> the
	// client's built-in 30s default; a persisted sensing-container tune overrides at rebuild.
	apiClient.SetLimiterPressureHalfLife(time.Duration(cfg.Daemon.LimiterPressureHalfLifeSeconds) * time.Second)
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
		time.Duration(cfg.Scouting.ShipyardRescanTTLSeconds)*time.Second,
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
	// The same recent-scan window the trade coordinators stamp, so the fleet keeps
	// one definition of "already scanned recently enough" rather than two that drift.
	scoutTourHandler.SetScanDedupWindow(cfg.TradeImpact.ResolvedScanMaxAge())
	// The scout-post dormancy bit the tour parks on: without this reader every tour
	// scans unconditionally and the probe-sensing coordinator's pressure rotation is
	// inert fleet-wide. The repo is shared with the scout-post/probe-sensing wiring below.
	scoutPostRepo := persistence.NewGormScoutPostRepository(db)
	scoutTourHandler.SetDormancyReader(scoutPostRepo)
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

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, waypointRepo, shipRepo, playerRepo, routingClient, apiClient, socketPath, &cfg.Metrics, cfg.Contract, cfg.TradeFleet, cfg.WorkerRebalancer, cfg.Scouting, cfg.Sensing, cfg.FleetAutosizer, cfg.Bootstrap, cfg.ShipResync, shipEventBus)
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
	// Live per-park DEMAND weights (sp-5rakx/sp-bu6ma, epic sp-9le3x C2c): the coordinator
	// homes each idle hull to its FIXED placement slot, and auto-resolves the standby set from the
	// ≤6 fixed placement slots when the `fleet hub` set is empty (fixes the pile-up). Backed by the
	// SAME home-system role lookup + TopDeliverySlots selection the contract auto-scaler buys against
	// (marketRepo, waypointRepo, shipRepo) — ONE slot set so the two positioning consumers place hulls
	// identically. A READ, never a config write (RULINGS #3); coord-deduped to distinct LOCATIONS.
	contractFleetCoordinatorHandler.SetStandbyPlacementProvider(grpc.NewContractStandbyPlacementProvider(shipRepo, waypointRepo, marketRepo))
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
	// table (scoutPostRepo, constructed with the scout-tour wiring above) and waypoint
	// repo are read directly; the container repo supplies tour liveness
	// (ListByStatusSimple), daemonClientLocal spawns/stops tour workers.
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
	// sp-m3122 liveness watchdog: read each running tour's last real-progress time and kill+relaunch
	// any RUNNING-but-hung tour (the daemon serves both ports over the containers/logs it single-writes),
	// plus promptly release absorption reservations of dead containers on restart / after a kill.
	tradeFleetCoordinatorHandler.SetTourLiveness(daemonServer)
	tradeFleetCoordinatorHandler.SetTourStopper(daemonServer)
	tradeFleetCoordinatorHandler.SetAbsorptionReclaimer(grpc.NewDeadContainerAbsorptionReclaimer(absorptionLedger))
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
	// sp-duxru: the SAME shared construction-site read the planner and the delivery terminal use, so
	// each tick reconciles the pipeline's delivered counters against the server before sizing buys.
	// Unwired, those counters can only drift BEHIND (they are written after the server already
	// accepted a supply) and the drain over-sources material the gate no longer needs.
	constructionCoordinatorHandler.SetConstructionSiteSource(api.NewConstructionSiteRepository(apiClient, playerRepo))
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
	// A gate-set refresh re-reads EVERY connected gate's build state, and a set expires
	// as a whole — so one neighbour still under construction drags its healthy siblings
	// onto the short window and re-confirms verdicts that cannot have changed (gate
	// construction is monotone). This probe answers those from the same era-scoped,
	// freshness-bounded row the routing cache already trusts; every uncertain case still
	// goes live. Scoped to the gate graph, the only consumer of the per-gate read.
	// A jump asks two topology questions the router has already answered and stored:
	// which gate waypoint the hop leaves for, and whether the source gate is built.
	// Attached here, where the store exists; the handler keeps its live reads for
	// anything the store does not hold.
	jumpShipHandler.SetJumpTopologyStore(gateEdgeRepo)
	gateProbeClient := api.NewGateConstructionProbe(apiClient, gateEdgeRepo)
	gateGraphService := gategraph.NewService(
		gateEdgeRepo, gateProbeClient, graphService, playerRepo,
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
	// sp-fihvy: wire the SAME gate-graph reachability service into the daemon server so the depot
	// stocker hull viability precondition can consult it (Routable) — never a second reachability
	// mechanism. Post-construction (gateGraphService is built after NewDaemonServer runs), mirroring
	// SetStorageRecovery below.
	daemonServer.SetGateGraph(gateGraphService)

	// Off-gate warp support (sp-0xd0, slice A): attach the warp-execute +
	// chart-on-arrival capability to the route executor now that gateGraphService
	// exists (WithWarpSupport mutates the same *RouteExecutor the nav handlers
	// already hold, so no re-wiring is needed). The charter reuses the SAME gate
	// graph, market scanner, and shipyard scanner the gate-nav path uses, plus the
	// graph provider as its waypoint source. Its callers are the frontier explorer
	// dispatcher and the `ship warp` verb wired just below.
	// The onward-viability reader answers the one strand question the API does not:
	// whether a system a warp lands in can be LEFT again. It reuses the SAME
	// fetch-through waypoint source (so an uncharted destination answers truthfully
	// instead of reading as empty) and the same gate-edge store the routing BFS reads.
	warpWaypointSource := ship.NewGraphWaypointSource(graphService)
	routeExecutor.WithWarpSupport(
		ship.NewAPIWarpNavigator(apiClient),
		ship.NewWarpSystemCharter(
			gateGraphService,
			warpWaypointSource,
			marketScanner,
			shipyardScanner,
		),
		ship.NewSystemEscapeReader(warpWaypointSource, gateEdgeRepo),
	)

	// The `ship warp` verb: the operator entry point to the warp executor just wired
	// above. Registered here (not beside the other ship verbs) because warp support
	// only exists from this point on. Its handler adds no warp logic — it resolves the
	// hull and the destination waypoint, then hands the leg to ExecuteWarpLeg, so every
	// warp runs behind the executor's fail-closed drive/strand guards. Destinations
	// resolve through the SAME fetch-through waypoint source the charter uses, which is
	// what lets an operator warp to a system the fleet has never charted.
	warpShipHandler := shipNav.NewWarpShipHandler(routeExecutor, shipRepo, warpWaypointSource)
	if err := mediator.RegisterHandler[*shipNav.WarpShipCommand](med, warpShipHandler); err != nil {
		return fmt.Errorf("failed to register WarpShip handler: %w", err)
	}

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
	// sp-42ow: the ReachableYardFinder is the heavy branch's yard-price FALLBACK — scout-scanned
	// yards ranked by stored-gate-graph hops then price. Signal-only: with no scan data the price
	// guard fails closed exactly as before, and every other guard still gates the buy.
	// The cross-coordinator off-gate demand bridge the FLEET autosizer's explorer BUY path
	// reads (sp-a3yn). Its only writer was the retired frontier coordinator, so the bridge is
	// currently always empty and the explorer path dormant — retained because the autosizer
	// registers its demand provider at construction, and a future off-gate emitter (the
	// probe-sensing discovery pass is the natural candidate) reconnects the write side here.
	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()

	fleetAutosizerHandler := grpc.NewFleetAutosizerCoordinatorHandler(
		daemonServer, apiClient, shipRepo, med, persistence.NewGormChainPnLRepository(db), waypointRepo, captainEventRepo,
		marketRepo, persistence.NewTourTelemetryRepository(db),
		shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService),
		explorerOffGateBridge, // sp-a3yn: explorer demand provider reads off-gate demand through this bridge
		shipyardInventoryRepo, // sp-fwk8z: cheapest KNOWN PRICED heavy yard — the reservation's price term
	)
	if err := mediator.RegisterHandler[*fleetCmd.RunFleetAutosizerCoordinatorCommand](med, fleetAutosizerHandler); err != nil {
		return fmt.Errorf("failed to register FleetAutosizerCoordinator handler: %w", err)
	}

	// Dedicated contract auto-scaler: the standing coordinator that ramps a FIXED, EXCLUSIVE contract
	// fleet to a live-tunable ceiling behind the 200000-credit cushion. Its concrete ports — the NOVEL
	// RoleResolver (home-system geometry + market roles), the treasury/yard-price REUSE of the autosizer
	// idioms, the "contract"-fleet counter, and the buy+dedicate+home Purchaser (the kept autosizer buy
	// primitive + the demand-ranked homing consumer) — are assembled inside
	// grpc.NewContractScalerCoordinatorHandler. Registering the handler changes NO live behaviour by itself
	// — it merely makes the coordinator available; the bootstrap coordinator launches this scaler during its
	// DATA/INCOME cold-start window (unconditional, sp-1cbxz).
	contractScalerHandler := grpc.NewContractScalerCoordinatorHandler(
		daemonServer, apiClient, shipRepo, med, waypointRepo, marketRepo,
		shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService),
		gateGraphService, // sp-fihvy: home-scoped depot stocker reclaim (Routable) — same graph, no new mechanism
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
		daemonServer, apiClient, shipRepo, med, waypointRepo, marketRepoAdapter,
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
	// sp-t5sh5: arm the activity-conditioned ranker freshness caps for the undirected
	// auto-scan. Absent [trading] config → the fitted armed defaults; a captain retunes
	// per activity from config.yaml + restart (RULINGS #5).
	tradeRouteCoordinatorHandler.SetRankerAgeCaps(cfg.Trading.RankerAgeCapMinutes.Resolved())
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

	// Reachable probe-yard finder for demand-proximal probe buys (sp-hej4): given a target
	// system it selects the scout-scanned probe-yard NEAREST it (fewest gate hops, arbitrated
	// against price) instead of always the home yard. Reads the SAME sp-42ow shipyard-inventory
	// scans + stored gate graph the heavy-yard fallback uses; a sparse/empty scan store fails
	// OPEN to the home yard. Lands the probe undedicated for the scout reconciler to relay.
	// Shared by the probe-sensing coordinator and the probe-buyer fleet below.
	probeYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)

	// EXPANSION phase gate, shared by the two coordinators that only belong to the
	// gate-built steady-state era: probe SENSING (demand-driven sizing has no trading
	// footprint to size against during cold start) and probe BUYING (sp-f3mcc, Admiral
	// 2026-07-24 — never during DATA/INCOME/GATE, where it drained the contract
	// working-capital band). ONE reader so the two can never disagree about which era it
	// is. It re-derives the phase from the live world (ships → home system → jump-gate
	// construction site, the same signal bootstrap's derivePhase reads) because the phase
	// is never persisted and bootstrap exits after its hand-off. Fail-closed.
	expansionPhase := expansionAdapters.NewBootstrapExpansionPhaseReader(
		shipRepo, waypointRepo, api.NewConstructionSiteRepository(apiClient, playerRepo),
	)

	// Parked-probe sensing coordinator: the fleet's ONE standing sensing engine. Its model
	// is PARKED probes — a hull is bought for a WAYPOINT, flown there once, and then stands
	// still scanning forever — so steady-state sensing costs navigation nothing and the only
	// recurring spend is the scans, paced fleet-wide by a single rotation against whatever
	// rate-limiter headroom the rest of the fleet leaves. It owns no algorithm: each tick it
	// composes the five engines in internal/application/parkedsensing (screen → buy queue →
	// placements → expansion → scan rotation) over the durable sensing ledger, and on its
	// FIRST tick it cuts over from the retired touring model (screening the known map offline,
	// retiring every scout post but home, adopting the orphaned probes as spares).
	//
	// expansionPhase is the era gate that holds the whole tick inert — cutover included —
	// until the home jump gate is built; before then bootstrap provisions probes and the
	// scout-post coordinator mans them. marketRepo serves the cutover's offline census only.
	probeSensingHandler := scoutingCmd.NewRunProbeSensingCoordinatorHandler(
		marketRepo, scoutPostRepo, shipRepo, apiClient.LimiterPressure(), expansionPhase, nil, // nil = use RealClock
	)
	// The engine's outbound surface, wired as ONE unit: a half-wired engine is a wedge
	// rather than a degraded mode (it would plan placements forever and fill none), so the
	// coordinator checks the bundle is complete and holds the tick fail-closed if it is not.
	// Every adapter here is thin — the money guards, the purchase machinery, the movement
	// verbs and the market scanner are all reused unmodified.
	//
	// Built PER PLAYER, like constructionActivatorFactory above: two of the reads sit in
	// player-scoped tables while their port signatures carry no player (the shipyard
	// inventory behind ListProbeYards, and the catalog sweep stamp behind CatalogKnown), so
	// the player has to be bound into the adapter — and this handler is a registered
	// singleton serving every player's ticks. The factory result is memoised per player.
	sensingLedgerPort := parkedSensingAdapters.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	sensingMarketGoods := parkedSensingAdapters.NewMarketGoodsPort(db)
	probeSensingHandler.SetEnginePortsFactory(func(sensingPlayerID int) scoutingCmd.SensingEnginePorts {
		// One catalog adapter instance serves the screen, the buy queue's yard lookup and
		// expansion's uncharted walk, so the three can never disagree about what is in a
		// system. DB-only by contract — ListProbeYards especially, whose locality the
		// drain's free-skip accounting depends on.
		catalog := parkedSensingAdapters.NewWaypointCatalogPort(waypointRepo, db, sensingPlayerID)
		return scoutingCmd.SensingEnginePorts{
			Ledger:    sensingLedgerPort,
			Waypoints: catalog,
			Uncharted: catalog,
			// The market cache: what a market deals in, how deep it is, and the
			// two-sided quotes the spread weighting reads (columns CROSSED — see
			// MarketPrices, where an uncrossed wiring fails silently).
			MarketGoods: sensingMarketGoods,
			SpreadOf:    sensingMarketGoods,
			// The screen's only genuine API spend: the goods CATALOGUE of a charted
			// market no hull has visited, which survives a presence-less GET.
			RemoteMarket: parkedSensingAdapters.NewRemoteMarketPort(apiClient, playerRepo),
			// Money: the same live-treasury reader every other guard uses, and the
			// trading fleet's measured cargo outflow, which is what makes the probe
			// buy floor dynamic rather than a fixed number.
			Treasury:   parkedSensingAdapters.NewTreasuryPort(expansionAdapters.NewTreasuryReader(apiClient)),
			CargoSpend: parkedSensingAdapters.NewCargoSpendPort(transactionRepo),
			Purchaser:  parkedSensingAdapters.NewProbePurchasePort(med, shipRepo),
			Ships:      parkedSensingAdapters.NewShipPositionPort(db),
			Fleet:      parkedSensingAdapters.NewFleetTagPort(shipRepo),
			Mover:      parkedSensingAdapters.NewMoverPort(med),
			// Per-system stored gate adjacency — never the whole-map read, and never a
			// fetch-through resolver.
			Gates:    parkedSensingAdapters.NewGateNeighbourPort(gateEdgeRepo),
			SeedShip: parkedSensingAdapters.NewSeedCommandPort(med, apiClient, playerRepo, waypointRepo, marketScanner),
			Scan:     parkedSensingAdapters.NewScanRunnerPort(marketScanner),
			Home:     parkedSensingAdapters.NewHomeSystemPort(db),
			// The heavy reservation: probe buying stands down while treasury accumulates
			// toward the next heavy, and resumes the moment it lands. heavy_cap is read
			// from the fleet autosizer's OWN persisted config — one dial, one enforcer —
			// and an absent autosizer simply reserves nothing.
			HeavyReserve: parkedSensingAdapters.NewHeavyReservePort(
				parkedSensingAdapters.NewShipRepoCensus(shipRepo),
				persistence.NewShipyardInventoryRepository(db),
				parkedSensingAdapters.NewAutosizerCapPort(db),
			),
			// The budget the whole model is sized against: sensing is the RESIDUAL
			// consumer, so it reads how much of the ceiling everyone else is using.
			Budget: parkedSensingAdapters.NewBudgetRatePort(metricsAdapter.GetGlobalAPIBudgetTracker(), api.RateLimitPerSecond),
		}
	})
	// Per-tick live view of the persisted config, so `tune --operation sensing` takes
	// effect on the NEXT reconcile rather than at the next rebuild (mirrors probeBuyer).
	probeSensingHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	probeSensingHandler.SetEventRecorder(captainEventRepo) // emit coordinator error-loop events on reconcile streak breach
	// Resolves the collector lazily per call: the metrics collectors are installed by
	// NewDaemonServer, which runs after this wiring, so a captured reference would be nil.
	probeSensingHandler.SetMetricsRecorder(parkedSensingAdapters.NewMetricsPort())
	if err := mediator.RegisterHandler[*scoutingCmd.RunProbeSensingCoordinatorCommand](med, probeSensingHandler); err != nil {
		return fmt.Errorf("failed to register ProbeSensingCoordinator handler: %w", err)
	}

	// Probe-buyer-fleet coordinator (sp-f082y): the standing coordinator that maintains K dedicated
	// (dedicated_fleet="probe-buyer") buyer hulls stationed at probe-yards so the probe fleet keeps
	// GROWING when freshness/scout demand outruns supply and no idle undedicated hull is left to buy
	// through — the catch-22 the frontier/freshness buy path deadlocks on. It orchestrates the SAME
	// machinery, forking nothing: the shared GuardedProbeBuyer guard stack (25% treasury +
	// working-capital floor + fleet cap + price ceiling) drives the shared ProbePurchaser
	// .resolveInPlaceBuy, and the shared ProbeBuyerPositioner stations/rotates — both wired
	// ownFleet="probe-buyer" so they select the dedicated buyers and claim them under their own fleet
	// operation (every other coordinator keeps skipping those hulls). Recruitment tags the nearest
	// reachable idle undedicated satellite through the single AssignFleet write path. Boot-standing +
	// ARMED (daemon_boot_standing.go). PurchaseCooldown=0 + a large spend window so the fleet grows as
	// fast as the reused money guards allow (K buys/tick); the fleet CAP is the binding growth bound,
	// enforced authoritatively by the coordinator's own live cap gate (the buyer's internal cap is a
	// redundant backstop). The shared 50k working-capital floor is injected via ReserveFloor.
	probeBuyerPurchaser := expansionAdapters.NewProbePurchaser(med, shipRepo, probeYardFinder, transactionRepo, nil).
		SetOwnFleet(probeBuyerFleetCmd.ProbeBuyerFleet)
	probeBuyerPositioner := expansionAdapters.NewProbeBuyerPositioner(med, shipRepo, probeYardFinder).
		SetOwnFleet(probeBuyerFleetCmd.ProbeBuyerFleet)
	probeBuyerGuardedBuyer := probebuy.NewGuardedProbeBuyer(
		expansionAdapters.NewTreasuryReader(apiClient),
		probeBuyerPurchaser,
		transactionRepo,
		nil, // RealClock
		probebuy.Config{
			MaxProbeFleet:    probeBuyerFleetCmd.DefaultProbeBuyerMaxFleet, // redundant backstop; the coordinator's live cap gate binds first
			MaxSpendPerCycle: 1_000_000_000,                                // effectively non-binding: the cap + 25% + floor are the real bounds
			PurchaseCooldown: 0,                                            // no pacing — grow as fast as the guards allow
			SpendWindow:      time.Hour,
			ReserveFloor:     common.ImmutableReserveFloor, // the shared 50k working-capital floor (RULINGS #4)
		},
	)
	// sp-f3mcc EXPANSION phase gate (Admiral 2026-07-24): the coordinator is INERT outside the
	// bootstrap-derived EXPANSION phase — probes are bought only once the home jump gate is BUILT
	// (sp-feiy7), never during DATA/INCOME/GATE where the sp-f082y buyer drained ~500k of the
	// contract working-capital band on staging. Shares the one expansionPhase reader wired above.
	probeBuyerHandler := probeBuyerFleetCmd.NewRunProbeBuyerFleetCoordinatorHandler(
		shipRepo, probeBuyerGuardedBuyer, probeBuyerPositioner, probeYardFinder, expansionPhase, nil, // nil = RealClock
	)
	probeBuyerHandler.SetLiveConfigReader(grpc.NewContainerConfigReader(containerRepo))
	if err := mediator.RegisterHandler[*probeBuyerFleetCmd.RunProbeBuyerFleetCoordinatorCommand](med, probeBuyerHandler); err != nil {
		return fmt.Errorf("failed to register ProbeBuyerFleetCoordinator handler: %w", err)
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

	// Long-haul arb engine (sp-mepj): the out-of-horizon single-good arb engine that captures the
	// exotic lanes the 1-gate-hop discovery horizon hides from BOTH the tour solver and the arb
	// scanner (proven by sp-mtvg). It REUSES the daemon's already-built collaborators (design
	// REUSE INVARIANT): the arb handler above as its per-leg executor, the trade-route
	// coordinator's cross-gate reposition (RepositionToWaypoint), the shared gate graph, the GORM
	// market repo's global-best sink/source scanners, the ONE shared price-impact model, and the
	// sp-m3122 liveness watchdog. ARMED on deploy but INERT until an operator tags a hull
	// `fleet add --operation long-haul --ship X` (an empty long-haul fleet → an empty idle bucket
	// → zero launches). No feature flag (Admiral standing order).
	const (
		longHaulMaxDataAge           = time.Hour // a lane priced from an observation older than this cannot win
		longHaulMinSpreadFloor       = 100       // coarse per-unit pre-filter; realized economics do the ranking
		longHaulMarginalFloorCredits = 0.0       // take the full argmax tranche (marginal spread ≥ 0); realized-net>0 filters losers
	)
	// The worker handler is a singleton the mediator dispatches per launched worker container;
	// each coordinator tick launches one worker per idle long-haul hull via DaemonServer.LaunchLongHaul
	// (FK-safe, recovery-safe, operation="long-haul"). debt is nil: the exotic cross-system lanes the
	// engine works are not the same-system lanes the trade fleet hammers, so shared cooldown debt is
	// ~zero and nil-safe; the era-3 buy/sell impact coefficients still drive optimal-volume.
	longHaulWorkerHandler := tradeRouteCmd.NewLongHaulArbWorkerHandler(
		shipRepo,
		marketRepo,
		gateGraphService,
		arbCoordinatorHandler,        // reused one-shot arb leg executor
		tradeRouteCoordinatorHandler, // shared cross-gate reposition
		grpc.NewLongHaulTreasuryReader(apiClient),
		cfg.TradeImpact.ResolvedBuyImpact(),
		cfg.TradeImpact.ResolvedSellImpact(),
		nil, // debt lookup: nil-safe (see above)
		nil, // RealClock
		longHaulMaxDataAge,
		longHaulMinSpreadFloor,
		longHaulMarginalFloorCredits,
	)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunLongHaulArbCommand](med, longHaulWorkerHandler); err != nil {
		return fmt.Errorf("failed to register LongHaulArb worker handler: %w", err)
	}

	// The standing fleet coordinator: each tick it launches a worker on every idle
	// long-haul-tagged hull and runs the SHARED sp-m3122 liveness watchdog — the SAME daemon
	// launcher/liveness/stopper/absorption-reclaim ports the trade-fleet coordinator wires
	// (~main.go trade-fleet block), never a new set. Registering it makes an armed-launch or
	// restart-recovered coordinator runnable; DaemonServer.LongHaulArbCoordinator (the
	// `workflow long-haul-coordinator` arm) launches the container.
	longHaulCoordinatorHandler := tradeRouteCmd.NewLongHaulArbFleetCoordinatorHandler(shipRepo, nil) // nil = RealClock
	longHaulCoordinatorHandler.SetLongHaulLauncher(daemonServer)
	// sp-tg11c — long-haul liveness watchdog RE-ENABLED (2026-07-24). It was disabled in
	// bba4cba3 because the shared sp-m3122 watchdog false-killed workers mid multi-hop
	// reposition — a long jump cooldown read as ">=12m no progress". sp-39hjn fixed that at the
	// SHARED watchdog: relaunchHungContainers now skips IsOnCooldown hulls, and the cooldown
	// wait emits a periodic heartbeat so the liveness signal advances. Re-wiring both ports
	// restores genuine hang-detection for long-haul (a truly-stuck worker with NO active
	// cooldown is killed+relaunched) without the false-kill. The reposition storm that also
	// stalled these hulls is gone too (sp-0o9ub plan-cheap-verify + sp-if4lx pathfind deadline).
	longHaulCoordinatorHandler.SetTourLiveness(daemonServer)
	longHaulCoordinatorHandler.SetTourStopper(daemonServer)
	longHaulCoordinatorHandler.SetAbsorptionReclaimer(grpc.NewDeadContainerAbsorptionReclaimer(absorptionLedger))
	if err := mediator.RegisterHandler[*tradeRouteCmd.LongHaulArbFleetCoordinatorCommand](med, longHaulCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register LongHaulArbFleetCoordinator handler: %w", err)
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
	// sp-t5sh5: arm the SAME activity-conditioned freshness caps for the tour snapshot
	// builder, so the tour path and the lane ranker drop stale rows against one
	// config-resolved table (defined once).
	tourCoordinatorHandler.SetRankerAgeCaps(cfg.Trading.RankerAgeCapMinutes.Resolved())
	// sp-tgll8 item 2: arm the "FRESH" clause on the firm-sink buy gate — at buy execution the
	// gate re-reads each held sink's LIVE market_data and refuses on stale data (older than
	// this). Ships ARMED at the 75-min default (matching maxListingAge); [trade_fleet].
	// sink_freshness_max_minutes retunes it, restart to apply. Byte-identical for fresh sinks.
	tourCoordinatorHandler.SetSinkFreshness(cfg.TradeFleet.ResolvedSinkFreshnessMaxAge())
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
