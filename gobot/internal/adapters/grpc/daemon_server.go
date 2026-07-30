package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

// MetricsCollector defines the interface for metrics collection
type MetricsCollector interface {
	Start(ctx context.Context)
	Stop()
	RecordContainerCompletion(containerInfo metrics.ContainerInfo)
	RecordContainerRestart(containerInfo metrics.ContainerInfo)
	RecordContainerIteration(containerInfo metrics.ContainerInfo)
}

// DaemonServer implements the gRPC daemon service
// Handles CLI requests and orchestrates background container operations
type DaemonServer struct {
	mediator       common.Mediator
	listener       net.Listener
	db             *gorm.DB // Database for creating repositories on demand
	logRepo        persistence.ContainerLogRepository
	containerRepo  *persistence.ContainerRepositoryGORM
	waypointRepo   *persistence.GormWaypointRepository
	shipRepo       navigation.ShipRepository
	playerRepo     player.PlayerRepository
	frontierStatus frontierStatusProvider // sp-pvw3 frontier-status query; injected post-construction, nil until wired
	routingClient  routing.RoutingClient
	apiClient      domainPorts.APIClient
	clock          shared.Clock

	// storageRecovery re-seeds the in-memory StorageCoordinator's per-good cargo
	// availability from live ship state on boot (sp-o477). The coordinator is
	// populated only by live deposits, so it starts EMPTY on restart; without this
	// standing warehouse stock is invisible to contract inventory-first sourcing.
	// Injected post-construction via SetStorageRecovery because the shared storage
	// coordinator + operation-repo singletons are built after NewDaemonServer runs.
	// nil disables recovery (fail-open — boot never depends on it).
	storageRecovery *storageApp.StorageRecoveryService

	// depotNavigateOverride, when non-nil, replaces NavigateShip for the depot element hull
	// repositioning positionDepotElementHull performs (sp-3l64) — the delivery-hull hub and the
	// source-hub waypoint (the two roles with no standing coordinator to park their own hull).
	// Nil in production → the real NavigateShip (which spawns a navigate container). Injected in
	// tests so the atomic claim-release + reposition decision is unit-tested against the real ship
	// repo WITHOUT spawning a navigate goroutine that would race the assertions. Same
	// post-construction test-seam convention as storageRecovery.
	depotNavigateOverride func(ctx context.Context, shipSymbol, destination string, playerID int) (string, error)

	// depotSinkOverride, when non-nil, replaces *DaemonServer as the depotCoordinatorSink the
	// element-add / reload positioning routes each launch through (sp-3l64). Nil in production →
	// s itself (the real StartWarehouse / StartStocker / navigate path). Injected in tests so the
	// AddDepotElement → per-role positioning WIRING is proven against a spy sink WITHOUT spawning
	// coordinator goroutines. Same post-construction test-seam convention as storageRecovery.
	depotSinkOverride depotCoordinatorSink

	// depotLiveContractSystemsOverride, when non-nil, replaces the DB-backed live-contract lookup the
	// boot depot-launch guard consults (sp-udgc): the set of destination SYSTEMS the player's active
	// (accepted, not-fulfilled) contracts deliver to. Nil in production →
	// FindActiveContracts(persistence.NewGormContractRepository(s.db)); injected in tests to drive the
	// decommissioned-vs-live launch decision without a live contracts table. Same post-construction
	// test-seam convention as depotSinkOverride.
	depotLiveContractSystemsOverride func(ctx context.Context, playerID int) (map[string]bool, error)

	// gateGraph is the cross-system reachability signal the depot stocker hull viability precondition
	// consults (sp-fihvy, RULINGS #14): the stocker is an intra-system role, so its hull must be in,
	// or gate-reachable to, the warehouse's home system before it is (re)claimed. It uses the SAME
	// Routable notion foreignMarketReachable relies on — never a second reachability mechanism.
	// Injected post-construction via SetGateGraph because gategraph.Service is built after
	// NewDaemonServer runs (main.go). A nil gateGraph fails open (byte-identical to pre-sp-fihvy: no
	// signal, no eviction), mirroring the storageRecovery/depotSinkOverride convention.
	gateGraph depotHomeRouter

	// Ship state scheduler (timer-based state transitions)
	shipStateScheduler *ShipStateScheduler

	// Ship resync scheduler (sp-p1ci): periodic full-fleet re-sync of ship
	// state from the API into the DB, so local state cannot drift vs live API
	// truth between the event-driven updates. Launched under supervision in
	// Start; halted by runCtx cancellation on shutdown.
	shipResyncScheduler *ShipResyncScheduler

	// Duty-cycle KPI sampler (sp-51ti captain amendment): ship-hours
	// EARNING/day per hull.
	dutyCycleSampler *metrics.DutyCycleSampler

	// Container orchestration
	containers   map[string]*ContainerRunner
	containersMu sync.RWMutex

	// Container spec registry - single source of truth for command construction
	containerSpecs map[string]ContainerSpec

	// Pending worker commands cache - stores commands with channels before start
	pendingWorkerCommands   map[string]interface{}
	pendingWorkerCommandsMu sync.RWMutex

	// Metrics
	metricsServer                 *http.Server
	metricsConfig                 *config.MetricsConfig
	containerMetricsCollector     MetricsCollector
	financialMetricsCollector     *metrics.FinancialMetricsCollector
	commandMetricsCollector       *metrics.CommandMetricsCollector
	marketMetricsCollector        *metrics.MarketMetricsCollector
	manufacturingMetricsCollector *metrics.ManufacturingMetricsCollector
	absorptionMetricsCollector    *metrics.AbsorptionMetricsCollector
	tourMetricsCollector          *metrics.TourMetricsCollector
	scoutMetricsCollector         *metrics.ScoutMetricsCollector

	// Read-only active-flow feed (flows): in-memory registry served at
	// GET /api/flows on the metrics mux. RULINGS #4 — exposure only, no
	// decision code reads it.
	flowRegistry *flowfeed.Registry

	// contractConfig carries the idle-arb harvest knobs (sp-1z2h / sp-uohe)
	// from config.yaml. ContractFleetCoordinator injects them into the
	// coordinator container's launch config, so a captain tunes the harvest —
	// including the money-guard blacklist — by editing config and restarting,
	// no code redeploy.
	contractConfig config.ContractConfig

	// tradeFleetConfig carries the trade-fleet coordinator knobs (sp-1278) from
	// config.yaml. TradeFleetCoordinator injects them into the coordinator
	// container's launch config on every build (creation + restart recovery via
	// resolveTradeFleetConfig), so a captain retunes the standing relaunch loop —
	// enabled/cooldown/max-concurrent/per-tour caps — by editing config and
	// restarting, no code redeploy.
	tradeFleetConfig config.TradeFleetConfig

	// workerRebalancerConfig carries the worker-rebalancer coordinator knobs (sp-f5pr)
	// from config.yaml. WorkerRebalancerCoordinator injects them into the coordinator
	// container's launch config on every build (creation + restart recovery via
	// resolveWorkerRebalancerConfig), so a captain retunes the standing ferry loop —
	// enabled/vacancy-clock/source-floor/cooldown/caps — by editing config and
	// restarting, no code redeploy.
	workerRebalancerConfig config.WorkerRebalancerConfig

	// scoutingConfig carries the scouting subsystem's tour-start phase jitter ceiling
	// (sp-x8i5) from config.yaml. ScoutTour and ScoutPostCoordinator resolve it into
	// their container's launch config on every build (creation + restart recovery via
	// resolveScoutingConfig), so a captain retunes the jitter ceiling by editing
	// config and restarting, no code redeploy.
	scoutingConfig config.ScoutingConfig

	// sensingConfig carries the probe-sensing coordinator's config.yaml-authoritative
	// knobs (the goods whitelist — a string the int-only tune registry cannot carry).
	// Injected into the probe_sensing_coordinator launch config on every build
	// (resolveSensingConfig), creation and restart recovery alike, so config.yaml is
	// the whitelist's single source of truth.
	sensingConfig config.SensingConfig

	// fleetAutosizerConfig carries the fleet capacity autosizer's knobs (sp-1txd) from
	// config.yaml. The fleet_autosizer coordinator resolves it into its container's launch
	// config on every build (creation + restart recovery via resolveFleetAutosizerConfig), so
	// a captain retunes the sizing/buying behaviour by editing config and restarting, no code
	// redeploy.
	fleetAutosizerConfig config.FleetAutosizerConfig

	// bootstrapConfig carries the captain bootstrap coordinator's knobs (sp-3nbe) from
	// config.yaml. The bootstrap coordinator resolves it into its container's launch config on
	// every build (creation + restart recovery via resolveBootstrapConfig), so a captain retunes
	// the cold-start behaviour by editing config and restarting, no code redeploy.
	bootstrapConfig config.BootstrapConfig

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}

	// Supervised background components (sp-i01z). runCtx is the daemon
	// lifetime context: canceled first thing in handleShutdown so supervised
	// loops (sweeper) wind down in parallel with the container drain.
	runCtx    context.Context
	runCancel context.CancelFunc
	sup       *supervise.Supervisor
}

// DutyCycleSampleInterval is how often the duty-cycle sampler snapshots
// every hull's earning/idle status (sp-51ti captain amendment). Matches
// ShipStateScheduler's SweeperInterval cadence — a well-understood DB load
// pattern already proven safe at this frequency — and gives 1440
// samples/day/hull, ample resolution for an hours/day KPI.
const DutyCycleSampleInterval = 60 * time.Second

// NewDaemonServer creates a new daemon server instance
// shipEventPublisher is the event bus for ship state change notifications.
// Pass the ShipEventBus created in main.go - it implements both publisher and subscriber interfaces.
func NewDaemonServer(
	mediator common.Mediator,
	db *gorm.DB,
	logRepo persistence.ContainerLogRepository,
	containerRepo *persistence.ContainerRepositoryGORM,
	waypointRepo *persistence.GormWaypointRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	routingClient routing.RoutingClient,
	apiClient domainPorts.APIClient,
	socketPath string,
	metricsConfig *config.MetricsConfig,
	contractConfig config.ContractConfig,
	tradeFleetConfig config.TradeFleetConfig,
	workerRebalancerConfig config.WorkerRebalancerConfig,
	scoutingConfig config.ScoutingConfig,
	sensingConfig config.SensingConfig,
	fleetAutosizerConfig config.FleetAutosizerConfig,
	bootstrapConfig config.BootstrapConfig,
	resyncConfig config.ResyncConfig,
	shipEventPublisher navigation.ShipEventPublisher,
) (*DaemonServer, error) {
	// Remove existing socket file if present
	if err := os.RemoveAll(socketPath); err != nil {
		return nil, fmt.Errorf("failed to remove existing socket: %w", err)
	}

	// Create Unix domain socket listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create unix socket listener: %w", err)
	}

	// Set socket permissions (owner only)
	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	clock := shared.NewRealClock()
	shipStateScheduler := NewShipStateScheduler(shipRepo, clock, shipEventPublisher)

	// Wire the global API request-budget tracker (sp-51ti) unconditionally.
	// Unlike the Prometheus collectors below, this is a lightweight in-memory
	// rolling tracker the CLI/gRPC health read depends on directly — it must
	// not be gated behind the optional metricsConfig.Enabled flag. The API
	// client (constructed by the caller, e.g. cmd/spacetraders-daemon) picks
	// this up automatically via SpaceTradersClient.getBudgetTracker()'s
	// fallback to the global, the same pattern getMetricsCollector() uses.
	metrics.SetGlobalAPIBudgetTracker(metrics.NewAPIBudgetTracker(api.RateLimitPerSecond, clock))

	// Wire arrival scheduler to ship repository so navigation triggers arrival timers
	if concreteRepo, ok := shipRepo.(interface {
		SetArrivalScheduler(navigation.ArrivalScheduler)
	}); ok {
		concreteRepo.SetArrivalScheduler(shipStateScheduler)
	}

	// Wire the position re-anchor observer: a sync that writes a position CONTRADICTING
	// the row it replaced is first-hand proof a completed move was never persisted, and
	// it must reach the captain outbox and Prometheus rather than being corrected in
	// silence (ship_position_reanchor_observer.go). Unconditional — this is the signal
	// that hands the next occurrence its own evidence, and an alarm behind a condition
	// is the failure it exists to end.
	if concreteRepo, ok := shipRepo.(interface {
		SetPositionReanchorObserver(api.PositionReanchorObserver)
	}); ok {
		concreteRepo.SetPositionReanchorObserver(shipPositionReanchorObserver{})
	}

	server := &DaemonServer{
		mediator:               mediator,
		db:                     db,
		logRepo:                logRepo,
		containerRepo:          containerRepo,
		waypointRepo:           waypointRepo,
		shipRepo:               shipRepo,
		playerRepo:             playerRepo,
		routingClient:          routingClient,
		apiClient:              apiClient,
		clock:                  clock,
		shipStateScheduler:     shipStateScheduler,
		listener:               listener,
		containers:             make(map[string]*ContainerRunner),
		containerSpecs:         make(map[string]ContainerSpec),
		pendingWorkerCommands:  make(map[string]interface{}),
		metricsConfig:          metricsConfig,
		contractConfig:         contractConfig,
		tradeFleetConfig:       tradeFleetConfig,
		workerRebalancerConfig: workerRebalancerConfig,
		scoutingConfig:         scoutingConfig,
		sensingConfig:          sensingConfig,
		fleetAutosizerConfig:   fleetAutosizerConfig,
		bootstrapConfig:        bootstrapConfig,
		shutdownChan:           make(chan os.Signal, 1),
		done:                   make(chan struct{}),
	}

	// Periodic full-fleet ship resync (sp-p1ci): re-syncs every player's ships
	// from the API into the DB on a jittered ~hourly cadence, reusing the same
	// syncAllShips core the startup sync runs. Config-driven with sane defaults
	// (1h +/-10min); launched under supervision in Start.
	server.shipResyncScheduler = NewShipResyncScheduler(
		server.syncAllShips,
		resyncConfig.ResolvedInterval(),
		resyncConfig.ResolvedJitter(),
	)

	// Create container info getter function. Hoisted above the
	// metricsConfig.Enabled block (sp-51ti) because the duty-cycle sampler
	// wired below needs it unconditionally — the same reasoning as the API
	// budget tracker above: both are lightweight in-memory trackers the
	// CLI/gRPC health read depends on directly, not optional Prometheus
	// scrape targets.
	getContainers := func() map[string]metrics.ContainerInfo {
		server.containersMu.RLock()
		defer server.containersMu.RUnlock()

		containerInfoMap := make(map[string]metrics.ContainerInfo)
		for id, runner := range server.containers {
			containerInfoMap[id] = runner.Container()
		}
		return containerInfoMap
	}

	// Wire the global duty-cycle KPI sampler (sp-51ti captain amendment):
	// ship-hours EARNING/day per hull. Each tick asks the ship-assignment
	// repository which hulls are actively assigned to a container, for
	// every player currently running at least one container (player-ID
	// discovery mirrors FinancialMetricsCollector's getContainers-based
	// approach). A captain-reserved hull (sp-i1ku) has an empty ContainerID
	// just like a genuinely idle one, so it correctly reads as non-earning
	// with no special-casing needed.
	shipAssignmentRepo := persistence.NewShipAssignmentRepository(db)
	dutyCycleSampler := metrics.NewDutyCycleSampler(func(ctx context.Context) ([]metrics.ShipEarningStatus, error) {
		playerIDs := map[int]bool{}
		for _, c := range getContainers() {
			playerIDs[c.PlayerID()] = true
		}

		var statuses []metrics.ShipEarningStatus
		for playerID := range playerIDs {
			infos, err := shipAssignmentRepo.ListActive(ctx, playerID)
			if err != nil {
				// Best-effort per player: one player's DB hiccup shouldn't
				// blank the whole tick for every other player.
				continue
			}
			for _, info := range infos {
				statuses = append(statuses, metrics.ShipEarningStatus{
					Hull:    info.ShipSymbol,
					Earning: info.ContainerID != "",
				})
			}
		}
		return statuses, nil
	}, DutyCycleSampleInterval)
	metrics.SetGlobalDutyCycleSampler(dutyCycleSampler)
	server.dutyCycleSampler = dutyCycleSampler

	// Read-only active-flow feed: constructed unconditionally so trading
	// executors always have a publish target (the HTTP route is only served
	// when metrics are enabled). RULINGS #4: exposure only — no decision code
	// reads this, and a missed publish can never touch the trade path.
	flowRegistry := newFlowRegistry(server)
	flowfeed.SetGlobal(flowRegistry)
	server.flowRegistry = flowRegistry

	// Initialize metrics collector if enabled
	if metricsConfig != nil && metricsConfig.Enabled {
		// Initialize the Prometheus registry
		metrics.InitRegistry()

		// Daemon build-info / process-start gauges (sp-wrh84): make a stale second
		// daemon detectable from a scrape (which commit / when it started).
		buildInfoCollector := metrics.NewDaemonBuildInfoCollector()
		if err := buildInfoCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register daemon build-info collector: %w", err)
		}
		buildInfoCollector.Record(buildinfo.Get().Commit, time.Now())

		// Create container metrics collector
		collector := metrics.NewContainerMetricsCollector(getContainers, shipRepo)
		if err := collector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register container metrics collector: %w", err)
		}
		server.containerMetricsCollector = collector

		// Set global collector for metrics recording
		metrics.SetGlobalCollector(collector)

		// Create navigation metrics collector
		navCollector := metrics.NewNavigationMetricsCollector()
		if err := navCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register navigation metrics collector: %w", err)
		}

		// Set global navigation collector for metrics recording
		metrics.SetGlobalNavigationCollector(navCollector)

		// Create financial metrics collector
		finCollector := metrics.NewFinancialMetricsCollector(mediator, playerRepo, getContainers)
		if err := finCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register financial metrics collector: %w", err)
		}

		// Set global financial collector for metrics recording
		metrics.SetGlobalFinancialCollector(finCollector)

		// Store reference for lifecycle management
		server.financialMetricsCollector = finCollector

		// Create command metrics collector
		cmdCollector := metrics.NewCommandMetricsCollector()
		if err := cmdCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register command metrics collector: %w", err)
		}
		server.commandMetricsCollector = cmdCollector

		// Register Prometheus middleware with mediator
		// This wraps all command/query executions to record metrics
		mediator.RegisterMiddleware(metrics.PrometheusMiddleware(cmdCollector))

		// Create API metrics collector
		apiCollector := metrics.NewAPIMetricsCollector()
		if err := apiCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register API metrics collector: %w", err)
		}

		// Set global API collector for API client to use
		metrics.SetGlobalAPICollector(apiCollector)

		// Create market metrics collector
		marketCollector := metrics.NewMarketMetricsCollector(db)
		if err := marketCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register market metrics collector: %w", err)
		}

		// Set global market collector for MarketScanner to use
		metrics.SetGlobalMarketCollector(marketCollector)

		// Store reference for lifecycle management
		server.marketMetricsCollector = marketCollector

		// Create manufacturing metrics collector
		mfgCollector := metrics.NewManufacturingMetricsCollector(db)
		if err := mfgCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register manufacturing metrics collector: %w", err)
		}

		// Set global manufacturing collector
		metrics.SetGlobalManufacturingCollector(mfgCollector)

		// Store reference for lifecycle management
		server.manufacturingMetricsCollector = mfgCollector

		// Create absorption burn-in collector (sp-8cz9): the tour coordinator emits the
		// cap-binding + ladder-incident counters through the global set here. Event-driven
		// (no polling goroutine), so registration + the global wire is the whole lifecycle.
		absorptionCollector := metrics.NewAbsorptionMetricsCollector()
		if err := absorptionCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register absorption metrics collector: %w", err)
		}

		// Set global absorption collector for the tour coordinator to record through
		metrics.SetGlobalAbsorptionCollector(absorptionCollector)

		// Store reference for lifecycle management
		server.absorptionMetricsCollector = absorptionCollector

		// Create tour instrumentation collector (sp-fbih): the tour coordinator emits the
		// reposition/margins-death/reserve-floor/exit/duration/resolved-cap series through the
		// global set here. Event-driven (no polling goroutine), so registration + the global
		// wire is the whole lifecycle, mirroring the absorption collector above.
		tourCollector := metrics.NewTourMetricsCollector()
		if err := tourCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register tour metrics collector: %w", err)
		}

		// Set global tour collector for the tour coordinator to record through
		metrics.SetGlobalTourCollector(tourCollector)

		// Store reference for lifecycle management
		server.tourMetricsCollector = tourCollector

		// Create tour-staleness collector (sp-k7q5 layer 2): the tour planner's two
		// staleness drop sites emit tour_lanes_stale_excluded_total through the global
		// set here. Event-driven (no polling goroutine), so registration + the global
		// wire is the whole lifecycle, mirroring the absorption collector above.
		tourStalenessCollector := metrics.NewTourStalenessMetricsCollector()
		if err := tourStalenessCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register tour staleness metrics collector: %w", err)
		}
		metrics.SetGlobalTourStalenessCollector(tourStalenessCollector)

		// Create scout freshness collector (sp-dp92 P7): the scout post coordinator's
		// reconcile sweep SETS this gauge directly on its own ticker — event-driven like
		// absorption above, no polling goroutine, so registration + the global wire is the
		// whole lifecycle here too.
		scoutCollector := metrics.NewScoutMetricsCollector()
		if err := scoutCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register scout metrics collector: %w", err)
		}

		// Set global scout collector for the scout post coordinator to record through
		metrics.SetGlobalScoutCollector(scoutCollector)

		// Store reference for lifecycle management
		server.scoutMetricsCollector = scoutCollector

		// Parked-probe sensing collector (sp-k6v8z): the sensing coordinator's reconcile
		// SETS all three gauges directly each tick — the pacer rate it just handed the scan
		// rotation, the staleness percentiles of the parked fleet, and the placement census
		// — so like the scout collector above this is event-driven and registration plus the
		// global wire is the whole lifecycle. The coordinator reaches it through an adapter
		// that resolves this global LAZILY per call, because handler wiring runs before this
		// constructor does.
		parkedSensingCollector := metrics.NewParkedSensingMetricsCollector()
		if err := parkedSensingCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register parked-sensing metrics collector: %w", err)
		}
		metrics.SetGlobalParkedSensingCollector(parkedSensingCollector)

		// Create fleet-health collector (sp-686e): the tour coordinator's reposition exit
		// path emits the stranded-hull counter (fleet_hull_stranded_total) through the global
		// set here — the StrandedHull alert's source. Event-driven (no polling goroutine), so
		// registration + the global wire is the whole lifecycle, mirroring the absorption
		// collector above; no per-collector lifecycle state to retain.
		fleetHealthCollector := metrics.NewFleetHealthMetricsCollector()
		if err := fleetHealthCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register fleet-health metrics collector: %w", err)
		}
		metrics.SetGlobalFleetHealthCollector(fleetHealthCollector)

		// Create chain-P&L collector (sp-rh2z): the goods_factory coordinator's kill-switch
		// emits the realized-P&L/hr gauge and the kill-episode counter through the global set
		// here — the realization-side chain accounting, and the ChainPnLKill alert's source.
		// Event-driven (no polling goroutine), mirroring the fleet-health collector above.
		chainPnLCollector := metrics.NewChainPnLMetricsCollector()
		if err := chainPnLCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register chain-P&L metrics collector: %w", err)
		}
		metrics.SetGlobalChainPnLCollector(chainPnLCollector)

		// Create input-pause collector (sp-r5a6): the goods_factory coordinator's input-poison
		// anti-cycle emits the pause-episode counter through the global set here — the INPUT side
		// of the self-pruning portfolio (the chain-P&L kill counter above is the OUTPUT side).
		// Event-driven (no polling goroutine), mirroring the collectors above.
		chainInputPauseCollector := metrics.NewChainInputPauseMetricsCollector()
		if err := chainInputPauseCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register chain input-pause metrics collector: %w", err)
		}
		metrics.SetGlobalChainInputPauseCollector(chainInputPauseCollector)

		// Create export-rest collector (sp-xdk6): the goods_factory coordinator's export-ask-subsidy
		// rest signal emits the rest-episode counter through the global set here — the OUTPUT-LADDER
		// side of the self-pruning portfolio (the input-pause counter above is the input side, the
		// chain-P&L kill counter the realized-P&L side). Event-driven (no polling goroutine).
		chainExportRestCollector := metrics.NewChainExportRestMetricsCollector()
		if err := chainExportRestCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register chain export-rest metrics collector: %w", err)
		}
		metrics.SetGlobalChainExportRestCollector(chainExportRestCollector)

		// Create fleet-autosizer collector (sp-1txd): the autosizer's ACT path emits its
		// purchase / guard-blocked / demand / zero-effect-alarm series through the global set here —
		// the observability for the standing coordinator that sizes the hull pool and auto-buys.
		fleetAutosizerCollector := metrics.NewFleetAutosizerMetricsCollector()
		if err := fleetAutosizerCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register fleet-autosizer metrics collector: %w", err)
		}
		metrics.SetGlobalFleetAutosizerCollector(fleetAutosizerCollector)

		// Create captain-bootstrap collector (sp-3nbe): the bootstrap reconciler emits its
		// derived-phase gauge + probe-purchase counter through the global set here — the
		// observability for the standing coordinator that drives a cold agent to the jump gate.
		bootstrapCollector := metrics.NewBootstrapMetricsCollector()
		if err := bootstrapCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register bootstrap metrics collector: %w", err)
		}
		metrics.SetGlobalBootstrapCollector(bootstrapCollector)

		// Coordinator-stall collector: the Prometheus half of the stall-escalation layer
		// (internal/application/health/stall.go). Coordinators report a three-way tick verdict
		// — PROGRESS / IDLE / BLOCKED(reason) — and a block sustained on one reason for
		// StallEscalationTicks consecutive ticks lands here as an escalation counter plus a
		// live streak gauge, beside the captain_events row. Event-driven (no polling
		// goroutine), so registration + the global wire is the whole lifecycle; the
		// coordinators reach it through a port that resolves this global LAZILY per call,
		// because handler wiring runs before this constructor does.
		stallCollector := metrics.NewStallMetricsCollector()
		if err := stallCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register coordinator-stall metrics collector: %w", err)
		}
		metrics.SetGlobalStallCollector(stallCollector)

		// Opportunity-relocator collector (sp-j1i49): the counters that make the relocator's behaviour
		// a RATE rather than an anecdote. It shipped emitting nothing, so a relocator losing every
		// decision to the claim race was indistinguishable from one with nothing to do. Same lifecycle
		// and the same lazy-global reasoning as the stall collector above — the relocator handler is
		// wired well before this constructor runs. A RELOCATOR-SPECIFIC series, never the tour's: this
		// package already records that two hull-relocating engines keep separate series "so the two
		// engines' telemetry never conflate".
		relocatorCollector := metrics.NewRelocatorMetricsCollector()
		if err := relocatorCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register opportunity-relocator metrics collector: %w", err)
		}
		metrics.SetGlobalRelocatorCollector(relocatorCollector)

		// Ship-position re-anchor collector: the Prometheus half of the lost-write signal.
		// Same lifecycle and same lazy-global reasoning as the stall collector above — the
		// ship repository is wired well before this constructor runs.
		reanchorCollector := metrics.NewPositionReanchorCollector()
		if err := reanchorCollector.Register(); err != nil {
			listener.Close()
			return nil, fmt.Errorf("failed to register ship-position re-anchor metrics collector: %w", err)
		}
		metrics.SetGlobalPositionReanchorCollector(reanchorCollector)
	}

	// Register container specs for launch and recovery
	server.registerContainerSpecs()

	// Setup signal handling
	signal.Notify(server.shutdownChan, os.Interrupt, syscall.SIGTERM)

	return server, nil
}

// Start begins serving gRPC requests
func (s *DaemonServer) Start() error {
	fmt.Printf("Daemon server listening on unix socket: %s\n", s.listener.Addr().String())

	// Supervised-component wiring (sp-i01z). The captain recorder was
	// installed by main before Start; a nil recorder just means no events.
	s.runCtx, s.runCancel = context.WithCancel(context.Background())
	bootCtx, bootCancel := context.WithTimeout(s.runCtx, 10*time.Second)
	s.sup = supervise.New(
		currentCaptainEventRecorder(),
		s.primaryPlayerID(bootCtx),
		s.clock,
		supervise.WithOnRestart(metrics.RecordDaemonComponentRestart),
	)
	bootCancel()

	// Release all zombie assignments from previous daemon runs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.shipRepo != nil {
		eraRepo := persistence.NewEraRepository(s.db)
		openEra, err := eraRepo.FindOpenEra(ctx)
		if err != nil {
			fmt.Printf("Warning: Failed to resolve open era for zombie assignment release: %v\n", err)
		} else if openEra == nil {
			fmt.Println("No open era - skipping zombie assignment release")
		} else {
			count, err := s.shipRepo.ReleaseAllActive(ctx, shared.MustNewPlayerID(openEra.PlayerID), "daemon_restart")
			if err != nil {
				fmt.Printf("Warning: Failed to release zombie assignments: %v\n", err)
			} else if count > 0 {
				fmt.Printf("Released %d zombie ship assignment(s) on daemon startup\n", count)
			}
		}
	}

	// Reset orphaned ASSIGNED manufacturing tasks to READY
	// This fixes tasks stuck in ASSIGNED state from failed worker container creation
	if err := s.resetOrphanedManufacturingTasks(); err != nil {
		fmt.Printf("Warning: Failed to reset orphaned manufacturing tasks: %v\n", err)
	}

	// Sync all ships from API to database (database becomes source of truth after this)
	if err := s.syncAllShipsOnStartup(); err != nil {
		fmt.Printf("Warning: Ship startup sync failed: %v\n", err)
		// Continue - we can still operate with stale data
	}

	// Schedule timers for pending arrivals and cooldowns
	if s.shipStateScheduler != nil {
		scheduleCtx, scheduleCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scheduleCancel()
		if err := s.shipStateScheduler.ScheduleAllPending(scheduleCtx); err != nil {
			fmt.Printf("Warning: Failed to schedule pending state transitions: %v\n", err)
		}
		// Start background sweeper under supervision (sp-i01z): restarts
		// with backoff on crash, escalates a crash loop to the captain.
		s.sup.Go(s.runCtx, "ship-state-sweeper", s.shipStateScheduler.RunSweeper)
	}

	// Start the periodic full-fleet ship resync under supervision (sp-p1ci):
	// keeps DB ship state from drifting vs the live API between the
	// event-driven updates. Like the sweeper, it restarts with backoff on
	// crash and winds down on runCtx cancellation at shutdown.
	if s.shipResyncScheduler != nil {
		s.sup.Go(s.runCtx, "ship-resync", s.shipResyncScheduler.Run)
	}

	// Start the duty-cycle KPI sampler (sp-51ti). Unconditional, like the
	// ship state scheduler above — not gated behind metricsConfig.Enabled.
	if s.dutyCycleSampler != nil {
		s.dutyCycleSampler.Start()
	}

	// Start metrics server if enabled. A bind failure is FATAL (sp-wrh84 root
	// cause B): a taken metrics port means another daemon already holds it — abort
	// instead of running headless (no scrape, no alerting) beside a stale writer.
	if err := s.startMetricsServerOrFail(); err != nil {
		return err
	}
	if s.metricsConfig != nil && s.metricsConfig.Enabled {
		// Start container metrics collector
		if s.containerMetricsCollector != nil {
			s.containerMetricsCollector.Start(context.Background())
		}

		// Start financial metrics collector
		if s.financialMetricsCollector != nil {
			s.financialMetricsCollector.Start(context.Background())
		}

		// Start market metrics collector
		if s.marketMetricsCollector != nil {
			s.marketMetricsCollector.Start(context.Background())
		}

		// Start manufacturing metrics collector
		if s.manufacturingMetricsCollector != nil {
			s.manufacturingMetricsCollector.Start(context.Background())
		}
	}

	// Recover RUNNING containers from previous daemon instance
	// This runs in the background to avoid blocking daemon startup.
	// Guard, not sup.Go (sp-i01z): recovery re-adopts containers and is NOT
	// safely re-runnable — a restart could double-adopt. One attempt, loudly
	// logged, panic-isolated; error behavior identical to today.
	go supervise.Guard("container-recovery", func() {
		recoveryCtx, recoveryCancel := context.WithTimeout(s.runCtx, 30*time.Second)
		defer recoveryCancel()

		if err := s.RecoverRunningContainers(recoveryCtx); err != nil {
			fmt.Printf("Warning: Container recovery failed: %v\n", err)
		}

		// Re-seed storage cargo availability from live ship state (sp-o477). MUST
		// run AFTER RecoverRunningContainers so the storage operations exist and
		// FindRunning returns them. The in-memory StorageCoordinator starts EMPTY
		// on restart, so without this the contract inventory-first path sees 0 and
		// market-buys goods already standing in the warehouse. Idempotent (skips an
		// already-registered ship) and fail-open (per-player/per-ship errors are
		// logged and skipped) — RULINGS #1/#2, read-only re-registration (#3).
		s.recoverStorageOperations(recoveryCtx)

		s.launchBootStandingAfterRecovery()
	})

	// Start shutdown handler
	go s.handleShutdown()

	// Create gRPC server
	grpcServer := grpc.NewServer()

	// Create and register service implementation
	serviceImpl := newDaemonServiceImpl(s)
	pb.RegisterDaemonServiceServer(grpcServer, serviceImpl)

	// Start serving in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(s.listener); err != nil {
			errChan <- fmt.Errorf("gRPC server error: %w", err)
		}
	}()

	// Wait for shutdown signal or error
	select {
	case err := <-errChan:
		return err
	case <-s.done:
		// Graceful shutdown
		fmt.Println("Initiating graceful shutdown of gRPC server...")
		grpcServer.GracefulStop()
		return nil
	}
}

// startMetricsServer starts the HTTP server for Prometheus metrics
// registerFlowsRoute mounts the read-only GET /api/flows handler on the metrics
// mux, beside /metrics (same localhost trust boundary; no auth change).
func registerFlowsRoute(mux *http.ServeMux, reg *flowfeed.Registry) {
	mux.Handle("/api/flows", flowfeed.NewFlowsHandler(reg))
}

// newFlowRegistry builds the daemon's flow registry with its live source wired.
//
// The live source is what makes the feed honest across a restart (sp-2uvec):
// published snapshots die with the process and executors only re-publish at their
// next plan adoption or leg arrival, so without it every hull is invisible for as
// long as it takes to adopt a plan — tens of minutes while repositioning or
// replanning, which is how the feed came to report 5 of 13 running tours. This
// wiring is load-bearing, not decoration: a registry built without it
// under-reports the fleet. It lives in its own function so a test can build the
// registry the daemon actually builds.
func newFlowRegistry(s *DaemonServer) *flowfeed.Registry {
	reg := flowfeed.New()
	reg.SetLiveSource(s.liveTradingRuns)
	return reg
}

// liveTradingRuns enumerates the trading containers that are RUNNING right now,
// off the SAME in-memory runner map ListContainers reads — so the flow feed can
// never disagree with `spacetraders container list`, which is the source that was
// right when the feed was wrong (sp-2uvec: feed 5, container list 13).
//
// The program is read off the command's concrete type rather than launch
// metadata: the command is what the runner actually executes, and restart
// recovery rebuilds it from persisted config, so this survives a restart with no
// separate re-registration step (RULINGS #2). Non-trading containers publish no
// flows and are skipped.
func (s *DaemonServer) liveTradingRuns() []flowfeed.LiveRun {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	runs := make([]flowfeed.LiveRun, 0, len(s.containers))
	for id, runner := range s.containers {
		if runner == nil {
			continue
		}
		cont := runner.Container()
		if cont == nil || !cont.IsRunning() {
			continue
		}
		switch cmd := runner.Command().(type) {
		case *tradingCmd.RunTourCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramTour, Ship: cmd.ShipSymbol, Closed: cmd.ClosedTours,
			})
		case *tradingCmd.RunTradeRouteCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramTradeRoute, Ship: cmd.ShipSymbol,
			})
		case *tradingCmd.RunArbCoordinatorCommand:
			runs = append(runs, flowfeed.LiveRun{
				ContainerID: id, Program: flowfeed.ProgramArb, Ship: cmd.ShipSymbol,
			})
		}
	}
	return runs
}

// startMetricsServerOrFail starts the Prometheus metrics server when enabled and
// returns a FATAL error if the bind fails. A taken metrics port means another
// daemon instance is already live on this host: the daemon must abort rather than
// run headless beside a stale writer (sp-wrh84 root cause B). Disabled metrics is
// a no-op success.
func (s *DaemonServer) startMetricsServerOrFail() error {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return nil
	}
	if err := s.startMetricsServer(); err != nil {
		return fmt.Errorf("metrics server failed to start (another daemon may already hold %s:%d): %w",
			s.metricsConfig.Host, s.metricsConfig.Port, err)
	}
	fmt.Printf("Metrics server listening on %s:%d%s\n",
		s.metricsConfig.Host, s.metricsConfig.Port, s.metricsConfig.Path)
	return nil
}

func (s *DaemonServer) startMetricsServer() error {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return nil
	}

	// Create HTTP mux for metrics endpoint
	mux := http.NewServeMux()
	mux.Handle(s.metricsConfig.Path, promhttp.HandlerFor(
		metrics.GetRegistry(),
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))
	registerFlowsRoute(mux, s.flowRegistry)

	// Create listener FIRST to verify port is available before returning success
	addr := fmt.Sprintf("%s:%d", s.metricsConfig.Host, s.metricsConfig.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind metrics server to %s: %w", addr, err)
	}

	// Create HTTP server
	s.metricsServer = &http.Server{
		Handler: mux,
	}

	// Start server in goroutine using the already-bound listener
	go func() {
		if err := s.metricsServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Metrics server error: %v\n", err)
		}
	}()

	return nil
}

// stopMetricsServer gracefully stops the HTTP metrics server
func (s *DaemonServer) stopMetricsServer() {
	if s.metricsServer == nil {
		return
	}

	// Stop metrics collectors first
	if s.containerMetricsCollector != nil {
		s.containerMetricsCollector.Stop()
	}
	if s.financialMetricsCollector != nil {
		s.financialMetricsCollector.Stop()
	}
	if s.marketMetricsCollector != nil {
		s.marketMetricsCollector.Stop()
	}

	// Shutdown HTTP server with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.metricsServer.Shutdown(ctx); err != nil {
		fmt.Printf("Error shutting down metrics server: %v\n", err)
	}
}

// handleShutdown manages graceful shutdown
// GracefulShutdownTimeout is the maximum time to wait for containers to finish
const GracefulShutdownTimeout = 30 * time.Second

func (s *DaemonServer) handleShutdown() {
	<-s.shutdownChan
	fmt.Println("\nShutdown signal received, initiating graceful shutdown...")

	// Cancel supervised components first (sp-i01z) so the sweeper stops
	// scheduling new writes while containers drain.
	if s.runCancel != nil {
		s.runCancel()
	}

	// Stop ship state scheduler (cancels timers and stops background sweeper)
	if s.shipStateScheduler != nil {
		s.shipStateScheduler.Stop()
	}

	// Stop the duty-cycle KPI sampler (sp-51ti)
	if s.dutyCycleSampler != nil {
		s.dutyCycleSampler.Stop()
	}

	// BUG FIX #5: Graceful shutdown with timeout
	// Give containers time to complete their current operation before force-interrupting
	s.gracefulShutdownWithTimeout(GracefulShutdownTimeout)

	// Stop metrics server and collector
	s.stopMetricsServer()

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	// Join supervised components — they exit promptly on runCtx cancel.
	if s.sup != nil {
		s.sup.Wait()
	}

	close(s.done)
}

// primaryPlayerID resolves the player that daemon-scoped captain events are
// attributed to: the open era's player (the same identity the zombie-release
// block at Start uses), falling back to the first player row, else 0.
func (s *DaemonServer) primaryPlayerID(ctx context.Context) int {
	if s.db != nil {
		eraRepo := persistence.NewEraRepository(s.db)
		if openEra, err := eraRepo.FindOpenEra(ctx); err == nil && openEra != nil {
			return openEra.PlayerID
		}
	}
	if s.playerRepo != nil {
		if players, err := s.playerRepo.ListAll(ctx); err == nil && len(players) > 0 {
			return players[0].ID.Value()
		}
	}
	return 0
}

// syncAllShipsOnStartup syncs the live (open-era) player's ships from the API
// into the database at daemon boot. After this sync, the database becomes the
// source of truth for ship state. Thin wrapper over the shared syncAllShips
// core (sp-p1ci), which the periodic ShipResyncScheduler also drives.
func (s *DaemonServer) syncAllShipsOnStartup() error {
	return s.syncAllShips(context.Background())
}

// syncAllShips re-syncs the live (open-era) player's ships from the API into
// the DB. It is the shared core called at startup AND on every periodic resync
// tick (sp-p1ci). The write path (SyncAllFromAPI) preserves the daemon-owned
// dedicated_fleet tag per ship (sp-bi75/sp-90a3), so a repeated hourly resync
// cannot clobber a `fleet assign` pin. The parent ctx bounds the sync (canceled
// at shutdown) under a 60s timeout.
//
// sp-ig6x: sync ONLY s.primaryPlayerID — the open era's player — NOT every
// player row. A universe reset leaves dead prior-era rows behind (empty or
// reset-date-mismatched tokens); the old playerRepo.ListAll loop synced them
// too, and because every player shared this ONE 60s deadline, each dead row's
// 401 burned the budget so the live player's ships never landed fresh —
// fleet-wide synced_at froze 12h+. primaryPlayerID is the canonical open-era
// resolver every other boot-scoped path already uses (ensureBootStandingCoordinators,
// depot registry). On a normal single-player era it resolves the only player, so
// the observable outcome is unchanged — this only STOPS syncing dead rows.
func (s *DaemonServer) syncAllShips(parent context.Context) error {
	if s.shipRepo == nil || s.playerRepo == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	pid := s.primaryPlayerID(ctx)
	if pid == 0 {
		fmt.Println("No open-era player found - skipping ship resync")
		return nil
	}

	playerID, err := shared.NewPlayerID(pid)
	if err != nil {
		return fmt.Errorf("resolve primary player id %d: %w", pid, err)
	}

	// Agent symbol for the log line only; the expensive API round-trip is
	// SyncAllFromAPI below, not this single-row lookup. Fall back to the numeric
	// id if the row can't be read — the sync still runs.
	agentLabel := fmt.Sprintf("player_id=%d", pid)
	if p, err := s.playerRepo.FindByID(ctx, playerID); err == nil && p != nil {
		agentLabel = p.AgentSymbol
	}

	count, err := s.shipRepo.SyncAllFromAPI(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to sync ships for player %s: %w", agentLabel, err)
	}

	fmt.Printf("Synced %d ship(s) for player %s\n", count, agentLabel)
	fmt.Printf("Ship sync complete: %d total ship(s) synced across 1 player(s)\n", count)
	return nil
}

// resetOrphanedManufacturingTasks resets ASSIGNED manufacturing tasks on daemon startup.
// A task can get stuck in ASSIGNED status because:
// 1. AssignTaskAtomically succeeds (task.assigned_ship is set)
// 2. But PersistManufacturingTaskWorkerContainer or shipRepo.Save fails
// 3. Rollback errors are ignored, leaving task ASSIGNED with no worker container
//
// This cleanup runs on daemon startup to reset any such orphaned tasks.
func (s *DaemonServer) resetOrphanedManufacturingTasks() error {
	if s.db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Reset all ASSIGNED tasks to READY and clear their assigned_ship
	// This allows them to be picked up by a manufacturing coordinator when it starts
	result := s.db.WithContext(ctx).
		Table("manufacturing_tasks").
		Where("status = ?", "ASSIGNED").
		Updates(map[string]interface{}{
			"status":        "READY",
			"assigned_ship": nil,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to reset orphaned manufacturing tasks: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		fmt.Printf("Reset %d orphaned ASSIGNED manufacturing task(s) to READY on daemon startup\n", result.RowsAffected)
	}

	return nil
}

// gracefulShutdownWithTimeout waits for containers to complete or times out
// BUG FIX #5: This prevents context cancellation cascades that corrupt state
func (s *DaemonServer) gracefulShutdownWithTimeout(timeout time.Duration) {
	s.containersMu.RLock()
	containerCount := len(s.containers)
	s.containersMu.RUnlock()

	if containerCount == 0 {
		fmt.Println("No running containers to stop")
		return
	}

	fmt.Printf("Waiting up to %s for %d container(s) to complete current operations...\n",
		timeout, containerCount)

	// Create a done channel to track when containers finish
	allDone := make(chan struct{})

	go func() {
		// Wait for all containers to finish their done channels
		s.containersMu.RLock()
		runners := make([]*ContainerRunner, 0, len(s.containers))
		for _, runner := range s.containers {
			runners = append(runners, runner)
		}
		s.containersMu.RUnlock()

		// Signal each container to stop (sets stopping flag, doesn't cancel context yet)
		for _, runner := range runners {
			// Try graceful stop first - this sets the stopping flag
			runner.mu.Lock()
			_ = runner.containerEntity.Stop()
			runner.mu.Unlock()
		}

		// Wait for each container's done channel
		for _, runner := range runners {
			select {
			case <-runner.done:
				// Container finished gracefully
			case <-time.After(timeout):
				// This container took too long - will be force-interrupted
			}
		}
		close(allDone)
	}()

	// Wait for graceful completion or timeout
	select {
	case <-allDone:
		fmt.Println("All containers completed gracefully")
	case <-time.After(timeout):
		fmt.Printf("Graceful shutdown timeout (%s) exceeded, force-interrupting remaining containers...\n", timeout)
		// Force-interrupt any remaining containers
		s.interruptAllContainers()
	}
}

// retiredCommandTypes are container command types deliberately removed from the registry by a
// retirement. A persisted RUNNING/INTERRUPTED row of one of these types has no builder, so
// generic recovery would mark it "recovery_failed" and log it as an unexplained loss. The
// recovery loop skips these explicitly — marking the row terminated and exempting it from the
// loss summary — so a post-retirement daemon boot recovers cleanly with no
// unknown-command-type errors. Data-driven: a future retirement adds one line here, with its
// attribution.
var retiredCommandTypes = map[string]bool{
	// The factory-ops retirement (sp-hoj8u): goods-factory, factory-siting,
	// worker-rebalancer, and the vestigial manufacturing coordinator.
	"goods_factory_coordinator":     true,
	"siting_coordinator":            true,
	"worker_rebalancer_coordinator": true,
	"manufacturing_coordinator":     true,
	// The probe-sensing retirement: the market-freshness sizer and the frontier
	// expansion coordinator are superseded by probe_sensing_coordinator. Their
	// rows are marked terminated on the first post-retirement boot instead of
	// alarming as unexplained losses; the registry refuses to build them either way.
	"market_freshness_sizer_coordinator": true,
	"frontier_expansion_coordinator":     true,
	// The probe-buyer retirement (Admiral 2026-07-28): a second engine buying into the same probe
	// fleet the sensing drain already supplies. It spent 245,316 credits on 9 hulls in five minutes
	// on the live fleet before it was stopped by hand. Deleted outright, not disabled.
	"probe_buyer_coordinator": true,
}

// RecoverRunningContainers recovers containers that were RUNNING or INTERRUPTED when daemon stopped
// INTERRUPTED = graceful shutdown (daemon called interruptAllContainers)
// RUNNING = ungraceful shutdown (kill -9, crash) - backwards compatibility
func (s *DaemonServer) RecoverRunningContainers(ctx context.Context) error {
	// Query database for INTERRUPTED containers (graceful shutdown)
	interruptedContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusInterrupted, nil)
	if err != nil {
		return fmt.Errorf("failed to list INTERRUPTED containers: %w", err)
	}

	// Query database for RUNNING containers (ungraceful shutdown - backwards compatibility)
	runningContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, nil)
	if err != nil {
		return fmt.Errorf("failed to list RUNNING containers: %w", err)
	}

	// Combine both lists
	allContainers := append(interruptedContainers, runningContainers...)

	if len(allContainers) == 0 {
		fmt.Println("No containers to recover")
		return nil
	}

	fmt.Printf("Recovering %d container(s) from previous daemon instance (%d INTERRUPTED, %d RUNNING)...\n",
		len(allContainers), len(interruptedContainers), len(runningContainers))

	// sp-njpu: scope recovery to the current open era's player. After a universe
	// reset / era close, containers belonging to a prior era's player must NOT be
	// re-instantiated against the reset universe (cross-era zombies). Mirrors the
	// open-era scoping of ReleaseAllActive on daemon startup (sp-s7b7). A nil openEra
	// means every era is closed, so nothing is live. A resolution error aborts
	// recovery without touching any container so the next restart can retry.
	openEra, err := persistence.NewEraRepository(s.db).FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to resolve open era for container recovery: %w", err)
	}

	// sp-tit8: track the outcome of every candidate so the pass can diff the
	// expected-running set (the INTERRUPTED+RUNNING rows loaded above) against
	// what actually ended running, and announce anything that silently fell out —
	// silence is the enemy, so any expected-but-missing hull that is NOT a
	// by-design skip becomes a loud, interrupt-class captain event (see
	// collectAndAnnounceLostContainers).
	recovered := make(map[string]bool)          // ended the pass as a running container
	exempt := make(map[string]bool)             // deliberately not running (respawn / dead-era) — no alarm
	failReason := make(map[string]recoveryLoss) // explicitly failed, with a captured reason
	coordinatorSkipCount := 0
	deadEraCount := 0

	for _, containerModel := range allContainers {
		// sp-njpu: skip any container whose player is not the open-era player. This
		// runs before the worker-adoption checks so an entire dead-era subtree
		// (coordinators AND their workers) stays down instead of being resurrected.
		// Dead-era is a deliberate universe-reset skip, not a loss — exempt from the
		// diff so a reset does not fire a storm of false lost-events (sp-tit8).
		if openEra == nil || containerModel.PlayerID != openEra.PlayerID {
			s.markContainerDeadEra(ctx, containerModel, openEra)
			exempt[containerModel.ID] = true
			deadEraCount++
			continue
		}

		// Parse container config from JSON
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			fmt.Printf("Container %s: Failed to parse config JSON, marking as FAILED: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "invalid_config", fmt.Sprintf("JSON parse error: %v", err))
			failReason[containerModel.ID] = recoveryLoss{
				id: containerModel.ID, commandType: containerModel.CommandType,
				playerID: containerModel.PlayerID,
				reason:   fmt.Sprintf("invalid_config: %v", err),
			}
			continue
		}

		// Skip worker containers — managed by their parent coordinator, not recovered
		// independently. Detected 3 ways: 1) coordinator_id in config 2) ParentContainerID
		// field 3) known worker command types. markWorkerInterrupted marks them FAILED but
		// deliberately does NOT release ship assignments (the coordinator resets those on
		// its own recovery; releasing here would break SELL tasks holding cargo).
		// sp-tit8: a worker is respawned by its coordinator by design, so it is NOT
		// expected to end this pass running — exempt from the loss diff (a lost-event here
		// would false-alarm on every restart) and counted separately, not as a failure.
		if coordinatorID, hasCoordinator := config["coordinator_id"].(string); hasCoordinator && coordinatorID != "" {
			fmt.Printf("Container %s: Skipping recovery (worker container managed by coordinator %s)\n", containerModel.ID, coordinatorID)
			s.markWorkerInterrupted(ctx, containerModel, coordinatorID)
			exempt[containerModel.ID] = true
			coordinatorSkipCount++
			continue
		}
		if containerModel.ParentContainerID != nil && *containerModel.ParentContainerID != "" {
			fmt.Printf("Container %s: Skipping recovery (worker container managed by parent %s)\n", containerModel.ID, *containerModel.ParentContainerID)
			s.markWorkerInterrupted(ctx, containerModel, *containerModel.ParentContainerID)
			exempt[containerModel.ID] = true
			coordinatorSkipCount++
			continue
		}
		// Skip known worker container types that should be managed by their parent coordinator
		// These containers will be re-spawned by the coordinator after it recovers
		if spec, hasSpec := s.containerSpecs[containerModel.CommandType]; hasSpec && spec.IsWorker {
			fmt.Printf("Container %s: Skipping recovery (worker container type '%s' managed by coordinator)\n", containerModel.ID, containerModel.CommandType)
			s.markWorkerInterrupted(ctx, containerModel, "")
			exempt[containerModel.ID] = true
			coordinatorSkipCount++
			continue
		}

		// A persisted row of a RETIRED command type has no builder. Skip it cleanly — mark it
		// terminated with a clear reason and exempt it from the loss summary — rather than
		// letting generic recovery report a spurious "recovery_failed". Which retirement
		// removed the type is documented on retiredCommandTypes itself.
		if retiredCommandTypes[containerModel.CommandType] {
			fmt.Printf("Container %s: Skipping recovery (retired command type '%s')\n", containerModel.ID, containerModel.CommandType)
			s.markContainerFailed(ctx, containerModel, "retired_command_type", fmt.Sprintf("command type '%s' retired", containerModel.CommandType))
			exempt[containerModel.ID] = true
			continue
		}

		// Recover using generic recovery with command factory
		if err := s.recoverContainer(ctx, containerModel, config); err != nil {
			fmt.Printf("Container %s: Recovery failed: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "recovery_failed", err.Error())
			failReason[containerModel.ID] = recoveryLoss{
				id: containerModel.ID, commandType: containerModel.CommandType,
				playerID: containerModel.PlayerID,
				reason:   fmt.Sprintf("recovery_failed: %v", err),
			}
		} else {
			recovered[containerModel.ID] = true
		}
	}

	// sp-tit8: diff expected-vs-recovered and announce every candidate that
	// neither ended running nor was a by-design skip. The summary NAMES each
	// loss so an operator never has to guess which container "N failed" was.
	lost := s.collectAndAnnounceLostContainers(allContainers, recovered, exempt, failReason)

	fmt.Printf("Container recovery complete: %d recovered, %d lost%s, %d coordinator-managed skipped, %d dead-era skipped\n",
		len(recovered), len(lost), formatLostSummary(lost), coordinatorSkipCount, deadEraCount)
	return nil
}

// SetStorageRecovery injects the storage recovery service invoked on boot to
// re-seed the in-memory StorageCoordinator from live ship state (sp-o477). Wired
// from main.go AFTER the shared storage coordinator + operation-repo singletons
// are constructed (they do not exist when NewDaemonServer runs), mirroring the
// codebase's other post-construction setters. A nil service is tolerated —
// recoverStorageOperations no-ops.
func (s *DaemonServer) SetStorageRecovery(svc *storageApp.StorageRecoveryService) {
	s.storageRecovery = svc
}

// SetGateGraph injects the cross-system gate-graph reachability service the depot element hull
// viability precondition consults (sp-fihvy stocker; generalized to every role by sp-fis8y). Wired
// from main.go AFTER gategraph.Service is constructed (it does not exist when NewDaemonServer
// runs), mirroring SetStorageRecovery. A nil service is tolerated — depotElementHullViable fails
// open, byte-identical to pre-sp-fihvy.
func (s *DaemonServer) SetGateGraph(g depotHomeRouter) {
	s.gateGraph = g
}

// recoverStorageOperations re-seeds the in-memory StorageCoordinator's per-good
// cargo availability from live ship state after a daemon restart (sp-o477).
//
// The coordinator is populated only by live deposits, so on restart it starts
// EMPTY: standing warehouse/manufacturing stock becomes invisible to
// GetTotalCargoAvailable, which blinds contract inventory-first sourcing into
// market-buying goods that are physically present in the warehouse. The injected
// StorageRecoveryService reloads each RUNNING storage operation and reconstructs
// its ships' cargo from the Ship API, re-registering them with the SAME shared
// coordinator the contract StorageInventoryFinder reads.
//
// Called on every boot AFTER RecoverRunningContainers so the operations exist.
// RULINGS #1/#2: idempotent (the service skips an already-registered ship, so a
// concurrent warehouse-worker registration never double-counts) and fail-open (a
// per-player DB error is logged and skipped; per-ship API errors are handled
// inside the service — neither aborts boot nor empties good state). RULING #3:
// read-only re-registration, no ship state is mutated.
func (s *DaemonServer) recoverStorageOperations(ctx context.Context) {
	if s.storageRecovery == nil || s.playerRepo == nil {
		return
	}
	players, err := s.playerRepo.ListAll(ctx)
	if err != nil {
		fmt.Printf("Warning: storage recovery skipped — failed to list players: %v\n", err)
		return
	}
	for _, p := range players {
		if _, err := s.storageRecovery.RecoverStorageOperations(ctx, p.ID.Value(), p.Token); err != nil {
			// Fail-open (RULINGS #1/#2): one player's recovery failure never aborts
			// boot or the remaining players' recovery.
			fmt.Printf("Warning: storage recovery failed for player %s: %v\n", p.AgentSymbol, err)
		}
	}
}

// recoveryLoss identifies a container that was expected to be RUNNING after boot
// recovery but is not — carrying the id, type, and why for the loud captain event
// and the named summary line (sp-tit8).
type recoveryLoss struct {
	id          string
	commandType string
	playerID    int
	reason      string
}

// collectAndAnnounceLostContainers diffs the loaded candidate set against what
// ended running (or was a by-design skip) and, for every container that fell
// out, records an interrupt-class captain.EventContainerLost (which wakes the
// captain via the watchkeeper) and logs a loud, greppable line naming it. This
// is the sp-tit8 guarantee: a container that terminalizes unseen is impossible —
// whatever the cause (recovery error, or a candidate that fell through every
// branch uncategorized), the hull announces itself. Explicitly-failed candidates
// carry their captured reason; an uncategorized one is reported as an unexpected
// terminal (a guard against a future refactor adding a silent `continue`).
func (s *DaemonServer) collectAndAnnounceLostContainers(
	candidates []*persistence.ContainerModel,
	recovered, exempt map[string]bool,
	failReason map[string]recoveryLoss,
) []recoveryLoss {
	var lost []recoveryLoss
	for _, cm := range candidates {
		if recovered[cm.ID] || exempt[cm.ID] {
			continue
		}
		loss, ok := failReason[cm.ID]
		if !ok {
			loss = recoveryLoss{
				id: cm.ID, commandType: cm.CommandType, playerID: cm.PlayerID,
				reason: "unexpected_terminal: recovery pass did not account for this container",
			}
		}
		lost = append(lost, loss)
	}
	for _, loss := range lost {
		fmt.Printf("CONTAINER LOST at recovery: %s [%s] — %s\n", loss.id, loss.commandType, loss.reason)
		recordCaptainEvent(captain.EventContainerLost, loss.id, loss.playerID, map[string]any{
			"container_id":   loss.id,
			"container_type": loss.commandType,
			"reason":         loss.reason,
		})
	}
	return lost
}

// formatLostSummary renders the named-failure detail appended to the recovery
// summary line, so "N lost" is never anonymous (sp-tit8). Empty when nothing
// was lost.
func formatLostSummary(lost []recoveryLoss) string {
	if len(lost) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lost))
	for _, loss := range lost {
		parts = append(parts, fmt.Sprintf("%s [%s]: %s", loss.id, loss.commandType, loss.reason))
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// markContainerDeadEra marks a container FAILED because it belongs to a player whose
// era is closed / the universe was reset (sp-njpu). It is NOT re-instantiated: reviving
// it would burn API calls against a dead token on a reset map. Ship assignments are
// left untouched — they belong to the reset universe, and the startup zombie-release
// path (ReleaseAllActive) deliberately scopes only to the open-era player.
func (s *DaemonServer) markContainerDeadEra(ctx context.Context, containerModel *persistence.ContainerModel, openEra *persistence.EraModel) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	livePlayer := "none (no open era)"
	if openEra != nil {
		livePlayer = fmt.Sprintf("player %d, era %q", openEra.PlayerID, openEra.Name)
	}
	detail := fmt.Sprintf("dead_era: container player %d is not the open-era player (%s); universe reset — not recovered",
		containerModel.PlayerID, livePlayer)
	fmt.Printf("Container %s: Skipping recovery (%s)\n", containerModel.ID, detail)

	exitCode := 1
	now := time.Now()
	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		detail,
	); err != nil {
		fmt.Printf("Warning: Failed to mark container %s as dead-era: %v\n", containerModel.ID, err)
	}
}

// recoverContainer is the generic container recovery function
// Uses the command factory registry to recreate any container type
// Adding new container types only requires registering a new factory - NO changes needed here!
func (s *DaemonServer) recoverContainer(ctx context.Context, containerModel *persistence.ContainerModel, config map[string]interface{}) error {
	// Build command from config via the container spec registry
	cmd, err := s.buildCommandForType(containerModel.CommandType, config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Extract ship symbol for assignment (if present)
	shipSymbol, hasShip := config["ship_symbol"].(string)
	if hasShip {
		// Re-assign ship using Ship aggregate pattern
		playerID := shared.MustNewPlayerID(containerModel.PlayerID)
		// Re-assign on the FRESH row: a plain Save that loses the version race has
		// its ownership columns taken from the row, so a claim persisted that way
		// would silently not stick. Re-claiming a container's own hull is a no-op.
		if _, _, err := s.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if sh.IsAssigned() && sh.ContainerID() == containerModel.ID {
					return false, nil
				}
				if err := sh.AssignToContainer(containerModel.ID, s.clock); err != nil {
					return false, err
				}
				return true, nil
			}); err != nil {
			return fmt.Errorf("failed to reassign ship %s: %w", shipSymbol, err)
		}
	}

	// Extract iterations from config. Runner-loop types persist their budget
	// under "iterations", except goods_factory_coordinator which persists it
	// under "max_iterations" (see StartGoodsFactory) — check both so a recovered
	// factory resumes with its actual budget instead of silently collapsing to
	// the single-iteration default (sp-perx).
	//
	// COORDINATOR-OWNED types are pinned to 1 regardless of config (sp-7yej
	// invariant 3): their command's handler owns the whole run internally
	// (scout_tour's tour count, trade_route's visit budget), so feeding the
	// config budget to the CONTAINER as well would double-loop it on recovery —
	// a rebuilt scout_tour with iterations=3 would fly 3 runner iterations × 3
	// tours each. The config value still reaches the command through the
	// factory builder, which is the loop that actually owns it.
	iterations := 1 // Default
	if spec, ok := s.containerSpecs[containerModel.CommandType]; ok && spec.CoordinatorOwnsIterations {
		// pinned: one runner iteration wraps the whole coordinator-owned run
	} else if iter, ok := config["iterations"].(float64); ok {
		iterations = int(iter)
	} else if iter, ok := config["max_iterations"].(float64); ok {
		iterations = int(iter)
	}

	// Recreate container entity
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		iterations,
		containerModel.ParentContainerID, // Restore parent-child relationship
		config,
		nil, // Use default RealClock for production
	)

	// Restore restart count from database
	for i := 0; i < containerModel.RestartCount; i++ {
		containerEntity.IncrementRestartCount()
	}

	s.startContainerRunner(containerEntity, cmd, containerModel.ID, "Recovered container")

	shipInfo := ""
	if hasShip {
		shipInfo = fmt.Sprintf(" for ship %s", shipSymbol)
	}
	fmt.Printf("Recovered container %s (%s%s)\n", containerModel.ID, containerModel.CommandType, shipInfo)
	return nil
}

// recoveryBookkeepingContext detaches a recovery terminalization from the recovery
// pass's own budget. The pass runs under a bounded context; when that budget expires
// mid-pass the adoption fails AND — on the shared context — so does every write that
// marks the container FAILED and releases its hull, leaving a RUNNING row with a
// claimed hull and no runner behind it. Bookkeeping about a container that is already
// not running must outlive the budget that stopped it, or recovery resurrects a corpse
// that `container list` reports as healthy indefinitely.
func recoveryBookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), dbOperationTimeout)
}

// markContainerFailed marks a container as FAILED in the database
func (s *DaemonServer) markContainerFailed(ctx context.Context, containerModel *persistence.ContainerModel, reason string, details string) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("%s: %s", reason, details),
	); err != nil {
		fmt.Printf("Warning: Failed to mark container %s as FAILED: %v\n", containerModel.ID, err)
	}

	// Release ship assignments for this failed container
	// This prevents orphaned assignments when containers fail during recovery
	playerID := shared.MustNewPlayerID(containerModel.PlayerID)
	assignedShips, err := s.shipRepo.FindByContainer(ctx, containerModel.ID, playerID)
	if err != nil {
		fmt.Printf("Warning: Failed to find ships for container %s: %v\n", containerModel.ID, err)
	} else {
		for _, ship := range assignedShips {
			shipSymbol := ship.ShipSymbol()
			// Release under CAS-retry (sp-wa7c): re-apply ForceRelease on the FRESH row
			// so a concurrent writer's cargo/nav update survives instead of being
			// last-write-wins clobbered by the FindByContainer snapshot. Skip unless the
			// hull is still on THIS failed container (a concurrent release or re-claim ->
			// changed=false), so a hull that moved on is not released out from under its
			// new owner.
			if _, _, err := s.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
				func(sh *navigation.Ship) (bool, error) {
					if !sh.IsAssigned() || sh.ContainerID() != containerModel.ID {
						return false, nil
					}
					sh.ForceRelease(reason, s.clock)
					return true, nil
				}); err != nil {
				fmt.Printf("Warning: Failed to release ship %s for container %s: %v\n", shipSymbol, containerModel.ID, err)
			}
		}
	}
}

// markWorkerInterrupted marks a worker container as interrupted during daemon restart.
// Unlike markContainerFailed, this does NOT release ship assignments.
// The coordinator's recoverState() will handle the ship assignments when it resets tasks.
// This is critical for SELL tasks where the ship still has cargo that needs to be sold.
func (s *DaemonServer) markWorkerInterrupted(ctx context.Context, containerModel *persistence.ContainerModel, coordinatorID string) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("worker_interrupted: Worker interrupted by daemon restart (coordinator: %s). Ship assignments preserved for task recovery.", coordinatorID),
	); err != nil {
		fmt.Printf("Warning: Failed to mark worker %s as interrupted: %v\n", containerModel.ID, err)
	}
	// NOTE: Intentionally NOT releasing ship assignments here.
	// The coordinator will handle this when it resets the task from EXECUTING to READY.
}

// ListContainers returns all registered containers
func (s *DaemonServer) ListContainers(playerID *int, status *string) []*container.Container {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	containers := make([]*container.Container, 0, len(s.containers))

	// Parse comma-separated status filter into map for O(1) lookup
	var allowedStatuses map[string]bool
	if status != nil && *status != "" {
		allowedStatuses = make(map[string]bool)
		statuses := strings.Split(*status, ",")
		for _, s := range statuses {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				allowedStatuses[trimmed] = true
			}
		}
	}

	for _, runner := range s.containers {
		cont := runner.Container()

		// Apply filters
		if playerID != nil && cont.PlayerID() != *playerID {
			continue
		}

		// Filter by status (if filter provided)
		if allowedStatuses != nil {
			if !allowedStatuses[string(cont.Status())] {
				continue
			}
		}

		containers = append(containers, cont)
	}

	return containers
}

// GetContainer retrieves a specific container
func (s *DaemonServer) GetContainer(containerID string) (*container.Container, error) {
	s.containersMu.RLock()
	defer s.containersMu.RUnlock()

	runner, exists := s.containers[containerID]
	if !exists {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	return runner.Container(), nil
}

// PersistedContainerConfig returns a container's config JSON as persisted in the
// database — Store A, the source of truth that live config mutations write
// (UpdateContainerConfig, e.g. a `fleet hub add|remove`). The in-memory container
// entity's Metadata() is frozen at launch by NewContainer and does NOT reflect a
// live mutation until a daemon restart rebuilds the entity, so the `container get`
// read path must source the displayed config here rather than serialize the launch
// snapshot (sp-aoy2). found is false when no row exists for the (id, playerID) pair.
func (s *DaemonServer) PersistedContainerConfig(ctx context.Context, containerID string, playerID int) (config string, found bool, err error) {
	model, err := s.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return "", false, fmt.Errorf("failed to read persisted container config: %w", err)
	}
	if model == nil {
		return "", false, nil
	}
	return model.Config, true, nil
}

// StopContainer stops a running container and all its child containers
func (s *DaemonServer) StopContainer(containerID string) error {
	s.containersMu.RLock()
	runner, exists := s.containers[containerID]
	s.containersMu.RUnlock()

	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}

	// Get playerID from the container
	playerID := runner.containerEntity.PlayerID()

	// Find and stop all child containers first (depth-first)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	childContainers, err := s.containerRepo.FindChildContainers(ctx, containerID, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to find child containers for %s: %v\n", containerID, err)
	} else {
		for _, child := range childContainers {
			// Only stop RUNNING or PENDING children
			if child.Status != "RUNNING" && child.Status != "PENDING" {
				continue
			}

			// Try to stop in-memory runner if exists
			s.containersMu.RLock()
			childRunner, childExists := s.containers[child.ID]
			s.containersMu.RUnlock()

			if childExists {
				fmt.Printf("Stopping child container: %s\n", child.ID)
				if err := childRunner.Stop(); err != nil {
					fmt.Printf("Warning: failed to stop child container %s: %v\n", child.ID, err)
				}
			} else {
				// Child not in memory (orphaned) - update DB directly
				fmt.Printf("Marking orphaned child container as stopped: %s\n", child.ID)
				now := time.Now()
				exitCode := 0
				if err := s.containerRepo.UpdateStatus(ctx, child.ID, playerID, container.ContainerStatusStopped, &now, &exitCode, "parent stopped"); err != nil {
					fmt.Printf("Warning: failed to update orphaned child container %s: %v\n", child.ID, err)
				}
			}
		}
	}

	// Now stop the parent container
	stopErr := runner.Stop()

	// sp-86yb: a gas coordinator's storage_operations row must be terminalized
	// alongside its container. Left at RUNNING, every manufacturing coordinator
	// keeps discovering an "active" storage source at a now-dead coordinator and
	// spawns STORAGE_ACQUIRE_DELIVER tasks against ships that are no longer there
	// - the recurring storage wedge. ctx is still live here (unlike the stopped
	// container's own cancelled ctx), so this write isn't racing shutdown.
	//
	// sp-3lj5: a warehouse container's storage_operations row needs the identical
	// terminalization, for the identical reason. Left un-terminalized, the stale
	// "zombie" RUNNING row keeps surfacing alongside its live replacement at the
	// same waypoint - the stocker/tour warehouse lookup can resolve to the dead
	// operation (whose registered storage ships are gone, so it always reads back
	// zero free space) and wrongly declare a warehouse with real free space full.
	// operationID == containerID for both container types (see
	// command_factory_registry.go's buildWarehouseCommand / gas-coordinator
	// equivalent), so the single call below covers both.
	if runner.containerEntity.Type() == container.ContainerTypeGasCoordinator ||
		runner.containerEntity.Type() == container.ContainerTypeWarehouse {
		s.terminalizeStorageOperation(ctx, containerID)
	}

	return stopErr
}

// terminalizeStorageOperation moves a gas coordinator's or warehouse's
// storage_operations row to a terminal status when its container is stopped
// (sp-86yb gas coordinators; sp-3lj5 extends this to warehouses). No-ops if
// there's no matching row, or it already reached a terminal status (idempotent -
// never clobbers e.g. an already-COMPLETED row back to STOPPED).
func (s *DaemonServer) terminalizeStorageOperation(ctx context.Context, operationID string) {
	if s.db == nil {
		return
	}

	storageOpRepo := persistence.NewStorageOperationRepository(s.db, s.clock)
	op, err := storageOpRepo.FindByID(ctx, operationID)
	if err != nil {
		fmt.Printf("Warning: failed to load storage operation %s for terminalization: %v\n", operationID, err)
		return
	}
	if op == nil || op.IsFinished() {
		return
	}

	if err := op.Stop(); err != nil {
		fmt.Printf("Warning: failed to transition storage operation %s to stopped: %v\n", operationID, err)
		return
	}

	if err := storageOpRepo.Update(ctx, op); err != nil {
		fmt.Printf("Warning: failed to persist stopped storage operation %s: %v\n", operationID, err)
	}
}

// DeleteContainer deletes a container from the database
// This is for cleanup of PENDING containers that were never started
func (s *DaemonServer) DeleteContainer(ctx context.Context, containerID string, playerID int) error {
	// Remove from in-memory map if exists (shouldn't be there for PENDING containers)
	s.containersMu.Lock()
	delete(s.containers, containerID)
	s.containersMu.Unlock()

	// Delete from database
	if err := s.containerRepo.Remove(ctx, containerID, playerID); err != nil {
		return fmt.Errorf("failed to delete container from database: %w", err)
	}

	return nil
}

// Container registration

func (s *DaemonServer) registerContainer(containerID string, runner *ContainerRunner) {
	s.containersMu.Lock()
	defer s.containersMu.Unlock()
	s.containers[containerID] = runner
}

// interruptAllContainers interrupts all container goroutines and marks them as INTERRUPTED
// Allows containers to be recovered on daemon restart
func (s *DaemonServer) interruptAllContainers() {
	s.containersMu.Lock()
	runners := make([]*ContainerRunner, 0, len(s.containers))
	for _, runner := range s.containers {
		runners = append(runners, runner)
	}
	s.containersMu.Unlock()

	fmt.Printf("Interrupting %d running container(s) (will be recovered on restart)...\n", len(runners))

	// Cancel all container contexts to stop goroutines
	for _, runner := range runners {
		runner.cancelFunc() // Stop goroutine execution
	}

	// Wait briefly for goroutines to exit
	time.Sleep(1 * time.Second)

	// Mark all containers as INTERRUPTED in database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, runner := range runners {
		// Only mark as INTERRUPTED if container is RUNNING
		// Skip containers that are already in terminal states (STOPPED, COMPLETED, FAILED)
		currentStatus := runner.containerEntity.Status()
		if currentStatus != container.ContainerStatusRunning {
			fmt.Printf("Skipping container %s (status: %s, not RUNNING)\n", runner.containerEntity.ID(), currentStatus)
			continue
		}

		now := time.Now()
		if err := s.containerRepo.UpdateStatus(
			ctx,
			runner.containerEntity.ID(),
			runner.containerEntity.PlayerID(),
			container.ContainerStatusInterrupted,
			&now,              // stoppedAt - when daemon interrupted
			nil,               // exitCode - nil for interruption
			"daemon_shutdown", // exitReason
		); err != nil {
			fmt.Printf("Warning: Failed to mark container %s as INTERRUPTED: %v\n", runner.containerEntity.ID(), err)
		}
	}

	fmt.Println("All containers interrupted and marked as INTERRUPTED in database")
}
