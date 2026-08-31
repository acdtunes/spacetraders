package grpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
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
	mediator      common.Mediator
	listener      net.Listener
	db            *gorm.DB // Database for creating repositories on demand
	logRepo       persistence.ContainerLogRepository
	containerRepo *persistence.ContainerRepositoryGORM
	waypointRepo  *persistence.GormWaypointRepository
	shipRepo      navigation.ShipRepository
	playerRepo    player.PlayerRepository
	routingClient routing.RoutingClient
	apiClient     domainPorts.APIClient
	clock         shared.Clock

	// storageRecovery re-seeds the in-memory StorageCoordinator's per-good cargo
	// availability from live ship state on boot (sp-o477). The coordinator is
	// populated only by live deposits, so it starts EMPTY on restart; without this
	// standing warehouse stock is invisible to contract inventory-first sourcing.
	// Injected post-construction via SetStorageRecovery because the shared storage
	// coordinator + operation-repo singletons are built after NewDaemonServer runs.
	// nil disables recovery (fail-open — boot never depends on it).
	storageRecovery *storageApp.StorageRecoveryService

	// yardScanner is the fleet's metered shipyard reader, handed to the per-call
	// MarketLocator StartConstructionPipeline builds so its hull search draws on the
	// same one shipyard-read allowance as every other reader.
	yardScanner *ship.ShipyardScanner

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
	// NewDaemonServer runs (main.go). A nil gateGraph fails open (byte-identical to before: no
	// signal, no eviction), mirroring the storageRecovery/depotSinkOverride convention.
	gateGraph depotHomeRouter

	// phaseGate is the shared EXPANSION reader the market-tour verbs refuse past (Admiral
	// 2026-08-08). Injected post-construction from main.go, like gateGraph. nil fails CLOSED —
	// see refuseTourOutsideBootstrap.
	phaseGate bootstrapPhaseGate

	// Ship state scheduler (timer-based state transitions)
	shipStateScheduler *ShipStateScheduler

	// Ship resync scheduler: periodic full-fleet re-sync of ship
	// state from the API into the DB, so local state cannot drift vs live API
	// truth between the event-driven updates. Launched under supervision in
	// Start; halted by runCtx cancellation on shutdown.
	shipResyncScheduler *ShipResyncScheduler

	// Unreadable-hull repair sweep: works the open episodes of hulls the API will not
	// serialise, confirming the fault against each hull's sub-resources before writing fuel
	// to clear it. Launched under supervision in Start; halted by runCtx cancellation.
	hullRepairScheduler *HullRepairScheduler

	// Container retention sweep (sp-72gmi): deletes terminal container rows older than the
	// retention window, so the table cannot grow without bound. Launched under supervision in
	// Start; halted by runCtx cancellation on shutdown.
	containerRetentionScheduler *ContainerRetentionScheduler

	// Container LOG retention sweep (sp-p1jo4): deletes container_logs rows past their level's
	// retention window in bounded batches, so the highest-volume table in the database cannot
	// grow without bound. Launched under supervision in Start; halted by runCtx cancellation on
	// shutdown. Nil when the operator has explicitly disabled the sweep.
	containerLogRetentionScheduler *ContainerLogRetentionScheduler

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

	// contractConfig carries the idle-arb harvest knobs
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

	// workerRebalancerConfig carries the worker-rebalancer coordinator knobs
	// from config.yaml. WorkerRebalancerCoordinator injects them into the coordinator
	// container's launch config on every build (creation + restart recovery via
	// resolveWorkerRebalancerConfig), so a captain retunes the standing ferry loop —
	// enabled/vacancy-clock/source-floor/cooldown/caps — by editing config and
	// restarting, no code redeploy.
	workerRebalancerConfig config.WorkerRebalancerConfig

	// scoutingConfig carries the scouting subsystem's tour-start phase jitter ceiling
	// from config.yaml. ScoutTour and ScoutPostCoordinator resolve it into
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

	// bootstrapConfig carries the captain bootstrap coordinator's knobs (sp-3nbe) from
	// config.yaml. The bootstrap coordinator resolves it into its container's launch config on
	// every build (creation + restart recovery via resolveBootstrapConfig), so a captain retunes
	// the cold-start behaviour by editing config and restarting, no code redeploy.
	bootstrapConfig config.BootstrapConfig

	// Shutdown coordination
	shutdownChan chan os.Signal
	done         chan struct{}

	// Supervised background components. runCtx is the daemon
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
	bootstrapConfig config.BootstrapConfig,
	resyncConfig config.ResyncConfig,
	containerLogRetentionConfig config.ContainerLogRetentionConfig,
	shipEventPublisher navigation.ShipEventPublisher,
) (*DaemonServer, error) {
	listener, err := listenOnDaemonSocket(socketPath)
	if err != nil {
		return nil, err
	}

	clock := shared.NewRealClock()
	shipStateScheduler := NewShipStateScheduler(shipRepo, clock, shipEventPublisher)

	// Not gated behind metricsConfig.Enabled: this is a lightweight in-memory tracker the
	// CLI/gRPC health read depends on directly, picked up via the API client's global fallback.
	metrics.SetGlobalAPIBudgetTracker(metrics.NewAPIBudgetTracker(api.RateLimitPerSecond, clock))

	wireShipRepositoryObservers(shipRepo, shipStateScheduler)

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
		bootstrapConfig:        bootstrapConfig,
		shutdownChan:           make(chan os.Signal, 1),
		done:                   make(chan struct{}),
	}

	// Periodic full-fleet ship resync: re-syncs every player's ships
	// from the API into the DB on a jittered ~hourly cadence, reusing the same
	// syncAllShips core the startup sync runs. Config-driven with sane defaults
	// (1h +/-10min); launched under supervision in Start.
	server.shipResyncScheduler = NewShipResyncScheduler(
		server.syncAllShips,
		resyncConfig.ResolvedInterval(),
		resyncConfig.ResolvedJitter(),
	)

	// Unreadable-hull repair: the fault appears with no operator present and never clears
	// on its own, so the sweep is standing and unconditional. It costs no API call while no
	// hull is unreadable.
	server.hullRepairScheduler = NewHullRepairScheduler(server.sweepUnreadableHulls, 0)

	// Container retention (sp-72gmi): the containers table had no bound at all, and the
	// sp-20eyn crash loop put 34,279 FAILED rows in it in a day. Sweeps at start and daily
	// thereafter; only terminal rows past the window are ever touched.
	server.containerRetentionScheduler = NewContainerRetentionScheduler(containerRepo)

	// Container LOG retention (sp-p1jo4): container_logs had no bound either, and it grows
	// ~400x faster than the containers table — 3,938,300 rows over 18 days, 1,591MB of a
	// 2,510MB database, all of it duplicating what daemon.log already holds on disk. Sweeps at
	// start and daily thereafter, in bounded batches, and never touches a row inside its
	// level's window. Built over db directly (the "repositories on demand" pattern) because the
	// injected logRepo is the write-side interface, not the concrete repository the sweep needs.
	server.containerLogRetentionScheduler = NewContainerLogRetentionScheduler(
		persistence.NewGormContainerLogRepository(db, nil),
		containerLogRetentionConfig,
	)

	// Hoisted above the metricsConfig.Enabled block: the duty-cycle sampler needs it
	// unconditionally, so it cannot live inside the optional Prometheus wiring.
	getContainers := func() map[string]metrics.ContainerInfo {
		server.containersMu.RLock()
		defer server.containersMu.RUnlock()

		containerInfoMap := make(map[string]metrics.ContainerInfo)
		for id, runner := range server.containers {
			containerInfoMap[id] = runner.Container()
		}
		return containerInfoMap
	}

	dutyCycleSampler := newDutyCycleSampler(db, getContainers)
	metrics.SetGlobalDutyCycleSampler(dutyCycleSampler)
	server.dutyCycleSampler = dutyCycleSampler

	// Read-only active-flow feed: constructed unconditionally so trading
	// executors always have a publish target (the HTTP route is only served
	// when metrics are enabled). RULINGS #4: exposure only — no decision code
	// reads this, and a missed publish can never touch the trade path.
	flowRegistry := newFlowRegistry(server)
	flowfeed.SetGlobal(flowRegistry)
	server.flowRegistry = flowRegistry

	if metricsConfig != nil && metricsConfig.Enabled {
		if err := server.registerMetricsCollectors(getContainers); err != nil {
			listener.Close()
			return nil, err
		}
	}

	server.registerContainerSpecs()

	signal.Notify(server.shutdownChan, os.Interrupt, syscall.SIGTERM)

	return server, nil
}

func listenOnDaemonSocket(socketPath string) (net.Listener, error) {
	if err := os.RemoveAll(socketPath); err != nil {
		return nil, fmt.Errorf("failed to remove existing socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create unix socket listener: %w", err)
	}

	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}
	return listener, nil
}

// wireShipRepositoryObservers hands the repository its optional navigation hooks. The
// re-anchor observer is unconditional: a contradicting position must not pass in silence.
func wireShipRepositoryObservers(shipRepo navigation.ShipRepository, arrivals navigation.ArrivalScheduler) {
	if concreteRepo, ok := shipRepo.(interface {
		SetArrivalScheduler(navigation.ArrivalScheduler)
	}); ok {
		concreteRepo.SetArrivalScheduler(arrivals)
	}

	if concreteRepo, ok := shipRepo.(interface {
		SetPositionReanchorObserver(api.PositionReanchorObserver)
	}); ok {
		concreteRepo.SetPositionReanchorObserver(shipPositionReanchorObserver{})
	}
}

// newDutyCycleSampler samples ship-hours EARNING/day per hull. A captain-reserved hull has
// an empty ContainerID just like a genuinely idle one, so it reads as non-earning unaided.
func newDutyCycleSampler(db *gorm.DB, getContainers func() map[string]metrics.ContainerInfo) *metrics.DutyCycleSampler {
	shipAssignmentRepo := persistence.NewShipAssignmentRepository(db)
	return metrics.NewDutyCycleSampler(func(ctx context.Context) ([]metrics.ShipEarningStatus, error) {
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
}

// registerCollector registers c with the shared Prometheus registry, labelling a
// failure for the caller.
func registerCollector[T interface{ Register() error }](c T, label string) (T, error) {
	if err := c.Register(); err != nil {
		var zero T
		return zero, fmt.Errorf("failed to register %s: %w", label, err)
	}
	return c, nil
}

// registerMetricsCollectors wires every Prometheus collector. The coordinators recording
// through them resolve each global lazily, because handler wiring runs before this does.
func (s *DaemonServer) registerMetricsCollectors(getContainers func() map[string]metrics.ContainerInfo) error {
	metrics.InitRegistry()

	// Build-info / process-start gauges make a stale second daemon detectable from a scrape.
	buildInfoCollector, err := registerCollector(metrics.NewDaemonBuildInfoCollector(), "daemon build-info collector")
	if err != nil {
		return err
	}
	buildInfoCollector.Record(buildinfo.Get().Commit, time.Now())

	containerCollector, err := registerCollector(metrics.NewContainerMetricsCollector(getContainers, s.shipRepo), "container metrics collector")
	if err != nil {
		return err
	}
	s.containerMetricsCollector = containerCollector
	metrics.SetGlobalCollector(containerCollector)

	navCollector, err := registerCollector(metrics.NewNavigationMetricsCollector(), "navigation metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalNavigationCollector(navCollector)

	finCollector, err := registerCollector(metrics.NewFinancialMetricsCollector(s.playerRepo, getContainers), "financial metrics collector")
	if err != nil {
		return err
	}
	s.financialMetricsCollector = finCollector
	metrics.SetGlobalFinancialCollector(finCollector)

	cmdCollector, err := registerCollector(metrics.NewCommandMetricsCollector(), "command metrics collector")
	if err != nil {
		return err
	}
	s.commandMetricsCollector = cmdCollector
	s.mediator.RegisterMiddleware(metrics.PrometheusMiddleware(cmdCollector))

	apiCollector, err := registerCollector(metrics.NewAPIMetricsCollector(), "API metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalAPICollector(apiCollector)

	marketCollector, err := registerCollector(metrics.NewMarketMetricsCollector(s.db), "market metrics collector")
	if err != nil {
		return err
	}
	s.marketMetricsCollector = marketCollector
	metrics.SetGlobalMarketCollector(marketCollector)

	mfgCollector, err := registerCollector(metrics.NewManufacturingMetricsCollector(s.db), "manufacturing metrics collector")
	if err != nil {
		return err
	}
	s.manufacturingMetricsCollector = mfgCollector
	metrics.SetGlobalManufacturingCollector(mfgCollector)

	// Aggregate-headroom denials on the shared cross-operation spend cap. A buy
	// refused for its OWN cost is already legible in the coordinator log; one refused because
	// the COMBINED in-flight spend would breach was previously recoverable only by noticing
	// that several PURCHASE_CARGO rows shared a balance_after.
	spendCapCollector, err := registerCollector(metrics.NewSpendCapMetricsCollector(), "spend cap metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalSpendCapCollector(spendCapCollector)

	gateCommitmentCollector, err := registerCollector(metrics.NewGateCommitmentMetricsCollector(), "gate commitment metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalGateCommitmentCollector(gateCommitmentCollector)

	absorptionCollector, err := registerCollector(metrics.NewAbsorptionMetricsCollector(), "absorption metrics collector")
	if err != nil {
		return err
	}
	s.absorptionMetricsCollector = absorptionCollector
	metrics.SetGlobalAbsorptionCollector(absorptionCollector)

	// The ledger/live/error split of every money-guard treasury read: without it a fleet
	// whose ledger is always stale is indistinguishable from a change that never shipped.
	treasuryReadCollector, err := registerCollector(metrics.NewTreasuryReadMetricsCollector(), "treasury read metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalTreasuryReadCollector(treasuryReadCollector)

	tourCollector, err := registerCollector(metrics.NewTourMetricsCollector(), "tour metrics collector")
	if err != nil {
		return err
	}
	s.tourMetricsCollector = tourCollector
	metrics.SetGlobalTourCollector(tourCollector)

	tourStalenessCollector, err := registerCollector(metrics.NewTourStalenessMetricsCollector(), "tour staleness metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalTourStalenessCollector(tourStalenessCollector)

	scoutCollector, err := registerCollector(metrics.NewScoutMetricsCollector(), "scout metrics collector")
	if err != nil {
		return err
	}
	s.scoutMetricsCollector = scoutCollector
	metrics.SetGlobalScoutCollector(scoutCollector)

	parkedSensingCollector, err := registerCollector(metrics.NewParkedSensingMetricsCollector(), "parked-sensing metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalParkedSensingCollector(parkedSensingCollector)

	// Whether the routing service SOLVED each fleet partition or fell back to its own
	// round-robin. Unwired, a solver that stops solving passes for a working one.
	fleetPartitionCollector, err := registerCollector(metrics.NewFleetPartitionMetricsCollector(), "fleet-partition metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalFleetPartitionCollector(fleetPartitionCollector)

	// The market and shipyard scan allowances. Unpublished, both run armed while signalling
	// nothing — which is how shipyard reads reached 3.2x their configured allowance unseen.
	scanBudgetCollector, err := registerCollector(metrics.NewScanBudgetMetricsCollector(), "scan-budget metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalScanBudgetCollector(scanBudgetCollector)

	// The scan-dedup A/B test's saved-calls counter. Same unpublished-collector trap as
	// scan-budget above: an unwired global leaves the recorder a silent, nil-safe no-op.
	scanDedupCollector, err := registerCollector(metrics.NewScanDedupMetricsCollector(), "scan-dedup metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalScanDedupCollector(scanDedupCollector)

	fleetHealthCollector, err := registerCollector(metrics.NewFleetHealthMetricsCollector(), "fleet-health metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalFleetHealthCollector(fleetHealthCollector)

	// Realized-P&L side of the goods_factory self-pruning portfolio; the two below are its
	// input and output-ladder sides.
	chainPnLCollector, err := registerCollector(metrics.NewChainPnLMetricsCollector(), "chain-P&L metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalChainPnLCollector(chainPnLCollector)

	chainInputPauseCollector, err := registerCollector(metrics.NewChainInputPauseMetricsCollector(), "chain input-pause metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalChainInputPauseCollector(chainInputPauseCollector)

	chainExportRestCollector, err := registerCollector(metrics.NewChainExportRestMetricsCollector(), "chain export-rest metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalChainExportRestCollector(chainExportRestCollector)

	fleetGrowthCollector, err := registerCollector(metrics.NewFleetGrowthMetricsCollector(), "fleet-growth metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalFleetGrowthCollector(fleetGrowthCollector)

	bootstrapCollector, err := registerCollector(metrics.NewBootstrapMetricsCollector(), "bootstrap metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalBootstrapCollector(bootstrapCollector)

	// Prometheus half of the stall-escalation layer (internal/application/health/stall.go).
	stallCollector, err := registerCollector(metrics.NewStallMetricsCollector(), "coordinator-stall metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalStallCollector(stallCollector)

	// Relocator-specific, never the tour's: the two hull-relocating engines keep separate
	// series so their telemetry never conflates.
	relocatorCollector, err := registerCollector(metrics.NewRelocatorMetricsCollector(), "opportunity-relocator metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalRelocatorCollector(relocatorCollector)

	reanchorCollector, err := registerCollector(metrics.NewPositionReanchorCollector(), "ship-position re-anchor metrics collector")
	if err != nil {
		return err
	}
	metrics.SetGlobalPositionReanchorCollector(reanchorCollector)

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

// SetYardScanner injects the fleet's metered shipyard reader.
//
// StartConstructionPipeline builds its own per-call MarketLocator, and that
// locator's hull search must draw on the SAME one shipyard-read allowance as every
// other reader — otherwise the budget would hold everywhere except on a path that
// constructs its collaborator fresh, which is precisely how the four original
// bypasses came about. Wired from main.go after the scanner is constructed.
func (s *DaemonServer) SetYardScanner(sc *ship.ShipyardScanner) {
	s.yardScanner = sc
}

// SetGateGraph injects the cross-system gate-graph reachability service the depot element hull
// viability precondition consults (sp-fihvy stocker; generalized to every role by sp-fis8y). Wired
// from main.go AFTER gategraph.Service is constructed (it does not exist when NewDaemonServer
// runs), mirroring SetStorageRecovery. A nil service is tolerated — depotElementHullViable fails
// open, byte-identical to before.
func (s *DaemonServer) SetGateGraph(g depotHomeRouter) {
	s.gateGraph = g
}
