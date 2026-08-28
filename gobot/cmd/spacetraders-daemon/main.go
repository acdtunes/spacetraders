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
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractQuery "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	gasQuery "github.com/andrescamacho/spacetraders-go/internal/application/gas/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	playerCmd "github.com/andrescamacho/spacetraders-go/internal/application/player/commands"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipOutfit "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/outfitting"
	shipScrap "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/scrap"
	shipTactics "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/strategies"
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
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/pidfile"
	"gorm.io/gorm"
)

const constructionActivatorPollInterval = time.Minute

func main() {
	forceFlag := flag.Bool("force", false, "Kill any existing daemon and start a new one")
	flag.Parse()

	fmt.Println("SpaceTraders Daemon v0.1.0")
	fmt.Println("==========================")
	// Build stamp: makes the live binary's commit greppable in daemon.log so a
	// deploy can assert the fresh build is actually running.
	fmt.Println(buildinfo.Get().Banner("spacetraders-daemon"))

	fmt.Println("Loading configuration...")
	cfg := config.MustLoadConfig("") // Empty string = search default paths

	// Acquire PID file lock to prevent multiple instances
	fmt.Printf("Acquiring PID file lock: %s\n", cfg.Daemon.PIDFile)
	pf := pidfile.New(cfg.Daemon.PIDFile)
	acquirePIDLockOrExit(pf, *forceFlag)

	defer func() {
		if err := pf.Release(); err != nil {
			log.Printf("Warning: failed to release PID file: %v", err)
		}
	}()
	fmt.Println("PID file lock acquired")

	if err := run(cfg); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

// daemonRepos is the shared, one-instance-each repository set every registration group wires from.
type daemonRepos struct {
	med               mediator.Mediator
	apiClient         *api.SpaceTradersClient
	shipRepo          navigation.ShipRepository
	playerRepo        *persistence.GormPlayerRepository
	waypointRepo      *persistence.GormWaypointRepository
	containerRepo     *persistence.ContainerRepositoryGORM
	graphService      *graph.GraphService
	marketRepo        *persistence.MarketRepositoryGORM
	marketRepoAdapter *persistence.MarketRepositoryAdapter
	contractRepo      *persistence.GormContractRepository
	transactionRepo   *persistence.GormTransactionRepository
}

// The atomic verbs RouteExecutor dispatches leg by leg.
func (r daemonRepos) registerShipTacticHandlers() error {
	orbitHandler := shipTactics.NewOrbitShipHandler(r.shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.OrbitShipCommand](r.med, orbitHandler); err != nil {
		return fmt.Errorf("failed to register OrbitShip handler: %w", err)
	}

	dockHandler := shipTactics.NewDockShipHandler(r.shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.DockShipCommand](r.med, dockHandler); err != nil {
		return fmt.Errorf("failed to register DockShip handler: %w", err)
	}

	refuelHandler := shipTactics.NewRefuelShipHandler(r.shipRepo, r.playerRepo, r.apiClient, r.med)
	if err := mediator.RegisterHandler[*shipTypes.RefuelShipCommand](r.med, refuelHandler); err != nil {
		return fmt.Errorf("failed to register RefuelShip handler: %w", err)
	}

	setFlightModeHandler := shipNav.NewSetFlightModeHandler(r.shipRepo)
	if err := mediator.RegisterHandler[*shipTypes.SetFlightModeCommand](r.med, setFlightModeHandler); err != nil {
		return fmt.Errorf("failed to register SetFlightMode handler: %w", err)
	}

	navigateDirectHandler := shipNav.NewNavigateDirectHandler(r.shipRepo, r.waypointRepo)
	if err := mediator.RegisterHandler[*shipTypes.NavigateDirectCommand](r.med, navigateDirectHandler); err != nil {
		return fmt.Errorf("failed to register NavigateDirect handler: %w", err)
	}
	return nil
}

func (r daemonRepos) registerMarketAndPlayerQueryHandlers() error {
	getMarketHandler := scoutingQuery.NewGetMarketDataHandler(r.marketRepo)
	if err := mediator.RegisterHandler[*scoutingQuery.GetMarketDataQuery](r.med, getMarketHandler); err != nil {
		return fmt.Errorf("failed to register GetMarketData handler: %w", err)
	}

	listMarketsHandler := scoutingQuery.NewListMarketDataHandler(r.marketRepo)
	if err := mediator.RegisterHandler[*scoutingQuery.ListMarketDataQuery](r.med, listMarketsHandler); err != nil {
		return fmt.Errorf("failed to register ListMarketData handler: %w", err)
	}

	getPlayerHandler := playerQuery.NewGetPlayerHandler(r.playerRepo, r.apiClient)
	if err := mediator.RegisterHandler[*playerQuery.GetPlayerQuery](r.med, getPlayerHandler); err != nil {
		return fmt.Errorf("failed to register GetPlayer handler: %w", err)
	}

	// Player identity sync. Era registration seeds players.metadata.headquarters; this handler
	// is what REPAIRS it, and the parked-sensing cutover reads it ahead of expansion on every
	// tick. Leave it unregistered and a row whose key is missing or stale stays broken forever,
	// with the whole sensing reconcile aborting every 30s. The daemon's boot hook dispatches it
	// per player; registering it here is what makes that dispatch resolve instead of failing
	// with "no handler registered for type".
	syncPlayerHandler := playerCmd.NewSyncPlayerHandler(r.playerRepo, r.apiClient)
	if err := mediator.RegisterHandler[*playerCmd.SyncPlayerCommand](r.med, syncPlayerHandler); err != nil {
		return fmt.Errorf("failed to register SyncPlayer handler: %w", err)
	}
	return nil
}

func (r daemonRepos) registerShipQueryHandlers() error {
	listShipsHandler := shipQuery.NewListShipsHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.ListShipsQuery](r.med, listShipsHandler); err != nil {
		return fmt.Errorf("failed to register ListShips handler: %w", err)
	}

	getShipHandler := shipQuery.NewGetShipHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.GetShipQuery](r.med, getShipHandler); err != nil {
		return fmt.Errorf("failed to register GetShip handler: %w", err)
	}

	// r.containerRepo satisfies ContainerStatusReader so refresh can reconcile a
	// stale claim left by a dead trade-route CLI runner; nil clock =
	// RealClock.
	refreshShipHandler := shipQuery.NewRefreshShipHandler(r.shipRepo, r.playerRepo, r.containerRepo, nil)
	if err := mediator.RegisterHandler[*shipQuery.RefreshShipQuery](r.med, refreshShipHandler); err != nil {
		return fmt.Errorf("failed to register RefreshShip handler: %w", err)
	}

	// Jump-gate discovery query handlers. GetJumpGateConnections backs the
	// multi-system trade-route's neighbor-system discovery.
	findNearestJumpGateHandler := shipQuery.NewFindNearestJumpGateHandler(r.shipRepo, r.graphService, r.playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.FindNearestJumpGateQuery](r.med, findNearestJumpGateHandler); err != nil {
		return fmt.Errorf("failed to register FindNearestJumpGate handler: %w", err)
	}

	getJumpGateConnectionsHandler := shipQuery.NewGetJumpGateConnectionsHandler(r.graphService, r.apiClient, r.playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.GetJumpGateConnectionsQuery](r.med, getJumpGateConnectionsHandler); err != nil {
		return fmt.Errorf("failed to register GetJumpGateConnections handler: %w", err)
	}
	return nil
}

// The reserve and assign handlers are returned because both later take an
// orphaned-container reaper, which only exists once the daemon server does.
func (r daemonRepos) registerFleetAssignmentHandlers() (*shipAssignment.ReserveShipHandler, *shipAssignment.AssignShipFleetHandler, error) {
	// Captain-reservation command handlers: reserve/release a hull for the
	// captain's direct manual use, hiding it from coordinator discovery
	// (sp-i1ku).
	reserveShipHandler := shipAssignment.NewReserveShipHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.ReserveShipCommand](r.med, reserveShipHandler); err != nil {
		return nil, nil, fmt.Errorf("failed to register ReserveShip handler: %w", err)
	}

	releaseShipHandler := shipAssignment.NewReleaseShipHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.ReleaseShipCommand](r.med, releaseShipHandler); err != nil {
		return nil, nil, fmt.Errorf("failed to register ReleaseShip handler: %w", err)
	}

	// Fleet-dedication command + query: the single write path for the
	// dedicated_fleet tag and the fleet listing behind `fleet list`.
	// The contract coordinator's startup reconciliation of --dedicated-ships
	// routes through the same command.
	assignShipFleetHandler := shipAssignment.NewAssignShipFleetHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.AssignShipFleetCommand](r.med, assignShipFleetHandler); err != nil {
		return nil, nil, fmt.Errorf("failed to register AssignShipFleet handler: %w", err)
	}

	// Retirement: the standing per-hull withdrawal mark the trade fleet coordinator's
	// relaunch gate reads once a retiring hull's hold is empty.
	retireShipHandler := shipAssignment.NewRetireShipHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipAssignment.RetireShipCommand](r.med, retireShipHandler); err != nil {
		return nil, nil, fmt.Errorf("failed to register RetireShip handler: %w", err)
	}

	listFleetsHandler := shipQuery.NewListFleetsHandler(r.shipRepo, r.playerRepo)
	if err := mediator.RegisterHandler[*shipQuery.ListFleetsQuery](r.med, listFleetsHandler); err != nil {
		return nil, nil, fmt.Errorf("failed to register ListFleets handler: %w", err)
	}
	return reserveShipHandler, assignShipFleetHandler, nil
}

// graphService implements both the system-graph and single-waypoint provider interfaces.
func (r daemonRepos) registerWaypointQueryHandlers() error {
	listWaypointsHandler := systemQuery.NewListWaypointsHandler(r.graphService, r.playerRepo)
	if err := mediator.RegisterHandler[*systemQuery.ListWaypointsQuery](r.med, listWaypointsHandler); err != nil {
		return fmt.Errorf("failed to register ListWaypoints handler: %w", err)
	}

	getWaypointHandler := systemQuery.NewGetWaypointHandler(r.graphService, r.playerRepo)
	if err := mediator.RegisterHandler[*systemQuery.GetWaypointQuery](r.med, getWaypointHandler); err != nil {
		return fmt.Errorf("failed to register GetWaypoint handler: %w", err)
	}
	return nil
}

func (r daemonRepos) registerLedgerHandlers() error {
	playerResolver := common.NewPlayerResolver(r.playerRepo)
	recordTransactionHandler := ledgerCmd.NewRecordTransactionHandler(r.transactionRepo, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*ledgerCmd.RecordTransactionCommand](r.med, recordTransactionHandler); err != nil {
		return fmt.Errorf("failed to register RecordTransaction handler: %w", err)
	}

	getTransactionsHandler := ledgerQuery.NewGetTransactionsHandler(r.transactionRepo, playerResolver)
	if err := mediator.RegisterHandler[*ledgerQuery.GetTransactionsQuery](r.med, getTransactionsHandler); err != nil {
		return fmt.Errorf("failed to register GetTransactions handler: %w", err)
	}

	getProfitLossHandler := ledgerQuery.NewGetProfitLossHandler(r.transactionRepo)
	if err := mediator.RegisterHandler[*ledgerQuery.GetProfitLossQuery](r.med, getProfitLossHandler); err != nil {
		return fmt.Errorf("failed to register GetProfitLoss handler: %w", err)
	}

	getCashFlowHandler := ledgerQuery.NewGetCashFlowHandler(r.transactionRepo)
	if err := mediator.RegisterHandler[*ledgerQuery.GetCashFlowQuery](r.med, getCashFlowHandler); err != nil {
		return fmt.Errorf("failed to register GetCashFlow handler: %w", err)
	}
	return nil
}

func (r daemonRepos) registerContractLifecycleHandlers() error {
	negotiateContractHandler := contractCmd.NewNegotiateContractHandler(r.contractRepo, r.shipRepo, r.playerRepo, r.apiClient)
	if err := mediator.RegisterHandler[*contractCmd.NegotiateContractCommand](r.med, negotiateContractHandler); err != nil {
		return fmt.Errorf("failed to register NegotiateContract handler: %w", err)
	}

	acceptContractHandler := contractCmd.NewAcceptContractHandler(r.contractRepo, r.playerRepo, r.apiClient, r.med)
	if err := mediator.RegisterHandler[*contractCmd.AcceptContractCommand](r.med, acceptContractHandler); err != nil {
		return fmt.Errorf("failed to register AcceptContract handler: %w", err)
	}

	deliverContractHandler := contractCmd.NewDeliverContractHandler(r.contractRepo, r.apiClient, r.playerRepo)
	if err := mediator.RegisterHandler[*contractCmd.DeliverContractCommand](r.med, deliverContractHandler); err != nil {
		return fmt.Errorf("failed to register DeliverContract handler: %w", err)
	}

	fulfillContractHandler := contractCmd.NewFulfillContractHandler(r.contractRepo, r.playerRepo, r.apiClient, r.med)
	if err := mediator.RegisterHandler[*contractCmd.FulfillContractCommand](r.med, fulfillContractHandler); err != nil {
		return fmt.Errorf("failed to register FulfillContract handler: %w", err)
	}

	evaluateContractProfitabilityHandler := contractQuery.NewEvaluateContractProfitabilityHandler(r.shipRepo, r.marketRepoAdapter)
	if err := mediator.RegisterHandler[*contractQuery.EvaluateContractProfitabilityQuery](r.med, evaluateContractProfitabilityHandler); err != nil {
		return fmt.Errorf("failed to register EvaluateContractProfitability handler: %w", err)
	}
	return nil
}

func (r daemonRepos) registerGasExtractionHandlers(shipEventBus *ship.ShipEventBus) error {
	siphonResourcesHandler := gasCmd.NewSiphonResourcesHandler(r.shipRepo, r.playerRepo, r.apiClient, shipEventBus)
	if err := mediator.RegisterHandler[*gasCmd.SiphonResourcesCommand](r.med, siphonResourcesHandler); err != nil {
		return fmt.Errorf("failed to register SiphonResources handler: %w", err)
	}

	transferCargoHandler := gasCmd.NewTransferCargoHandler(r.shipRepo, r.apiClient)
	if err := mediator.RegisterHandler[*gasCmd.TransferCargoCommand](r.med, transferCargoHandler); err != nil {
		return fmt.Errorf("failed to register TransferCargo handler: %w", err)
	}

	findFactoryForGasHandler := gasQuery.NewFindFactoryForGasHandler(r.marketRepoAdapter)
	if err := mediator.RegisterHandler[*gasQuery.FindFactoryForGasQuery](r.med, findFactoryForGasHandler); err != nil {
		return fmt.Errorf("failed to register FindFactoryForGas handler: %w", err)
	}
	return nil
}

// sensingWiring is the parked-sensing engine's collaborator set, composed per player into its ports.
type sensingWiring struct {
	ledger       *parkedSensingAdapters.LedgerPort
	marketGoods  *parkedSensingAdapters.MarketGoodsPort
	unpricedPool *parkedSensingAdapters.UnpricedPoolPort
	remoteMarket *parkedSensingAdapters.RemoteMarketPort

	db              *gorm.DB
	med             mediator.Mediator
	apiClient       *api.SpaceTradersClient
	shipRepo        navigation.ShipRepository
	playerRepo      *persistence.GormPlayerRepository
	waypointRepo    *persistence.GormWaypointRepository
	transactionRepo *persistence.GormTransactionRepository

	gateEdgeRepo     *persistence.GormGateEdgeRepository
	gateGraphService *gategraph.Service
	marketScanner    *ship.MarketScanner
	routingClient    domainRouting.RoutingClient

	shipyardScanner       *ship.ShipyardScanner
	shipyardInventoryRepo *persistence.ShipyardInventoryRepositoryGORM
	yardBudget            *ship.YardScanBudget
}

// The engine's outbound surface, wired as ONE unit: a half-wired engine is a wedge
// rather than a degraded mode (it would plan placements forever and fill none), so the
// coordinator checks the bundle is complete and holds the tick fail-closed if it is not.
// Every adapter here is thin — the money guards, the purchase machinery, the movement
// verbs and the market scanner are all reused unmodified.
//
// Built PER PLAYER, like constructionActivatorFactory: two of the reads sit in
// player-scoped tables while their port signatures carry no player (the shipyard
// inventory behind ListProbeYards, and the catalog sweep stamp behind CatalogKnown), so
// the player has to be bound into the adapter — and the handler is a registered
// singleton serving every player's ticks. The factory result is memoised per player.
//
// heavyTargetFinder and unservedLaneReader are explicit parameters rather than fields: both are
// SHARED with the fleet-growth coordinator, and the composition-root pin tests require each
// consumer be handed the one instance by name. A field would let a second one be constructed here
// without the wiring reading any differently.
// sensingHighWaterPort and sensingLanePort narrow a concrete collaborator to the ONE port the wave
// may judge, and drop a MISSING one to a genuine nil interface: a typed nil is NOT nil to an
// interface, so an unwired repository would sail past the port's own fail-closed guard and panic
// mid-tick. Named functions rather than inline conditionals for the same reason
// growthHighWaterPort is — substituting a point read must be a visible edit to a documented
// contract, not a plausible-looking one-word change.
func sensingHighWaterPort(txns *persistence.GormTransactionRepository) ledger.TreasuryHighWaterReader {
	if txns == nil {
		return nil
	}
	return txns
}

func sensingLanePort(lanes *tradingQueries.UnservedLaneReader) parkedSensingAdapters.UnservedLaneCounter {
	if lanes == nil {
		return nil
	}
	return lanes
}

func (s sensingWiring) enginePorts(
	heavyTargetFinder *shipyardQuery.HeavyTargetFinder,
	unservedLaneReader *tradingQueries.UnservedLaneReader,
	sensingPlayerID int,
) scoutingCmd.SensingEnginePorts {
	// One catalog adapter instance serves the screen, the buy queue's yard lookup and
	// expansion's uncharted walk, so the three can never disagree about what is in a
	// system. DB-only by contract — ListProbeYards especially, whose locality the
	// drain's free-skip accounting depends on.
	catalog := parkedSensingAdapters.NewWaypointCatalogPort(s.waypointRepo, s.db, sensingPlayerID)
	// One gate-adjacency adapter serves expansion's frontier walk AND the
	// placement mover's cross-system gate walk, so the two can never disagree
	// about which systems border which — the mover walks toward a placement
	// over the same edges expansion used to decide the placement was reachable.
	gateNeighbours := parkedSensingAdapters.NewGateNeighbourPort(s.gateEdgeRepo)
	// ONE read of the heavy buyer's container row, serving both knobs the wave depends on: the cap
	// the reservation is sized within, and the switch that says whether a buyer exists at all. They
	// are one question, and splitting them is how a later change answers it twice.
	heavyBuyerCaps := parkedSensingAdapters.NewHeavyBuyerCapPort(s.db)
	return scoutingCmd.SensingEnginePorts{
		Ledger:    s.ledger,
		Waypoints: catalog,
		Uncharted: catalog,
		// The fleet's one VRP, shared with the scout reset.
		Partitioner: s.routingClient,
		// The same catalog instance again: it owns the shipyard_inventory reads,
		// so the yard lookup and the listing memo read one store.
		ListingMemo: catalog,
		ProbeAsks:   catalog,
		// And once more for the shipyard blind spot: the set difference between
		// the charted SHIPYARD-trait waypoints and the ones already carrying a
		// stored reading. Same instance, so "which yards exist" and "which yards
		// have we read" are answered from one place.
		YardCatalog: catalog,
		// The two halves of a shipyard read, wired as ONE scanner behind two
		// budget tags. The free half learns what a yard SELLS with no hull
		// anywhere near it (`shipTypes` survives a presence-less GET, exactly like
		// the market catalogue above); the scan half rides a parked probe's turn
		// and additionally carries the PRICES, which only a hull at the counter
		// can see. Both persist through the fleet's one shipyard scanner, so the
		// heavy-yard milestone fires from either.
		YardRead: parkedSensingAdapters.NewYardCatalogPort(s.shipyardScanner, s.playerRepo),
		YardScan: parkedSensingAdapters.NewYardScanPort(s.shipyardScanner, s.playerRepo),
		// The third half of the shipyard problem, and the one neither reader can
		// solve: a counter's PRICES never appear in any response until a hull of
		// ours is standing on it. This is the SAME budget instance
		// every shipyard reader draws from, handed over as a demand source — its
		// top weight tier is precisely the set of yards it keeps ranking first and
		// keeps failing to price, and here that tier becomes a request to send a
		// hull rather than another read that comes back without a number.
		//
		// Passed DIRECTLY rather than through an adapter because there is nothing
		// to adapt: the budget already speaks the port's two methods, and a
		// wrapper would only create a second place for the reposition allowance to
		// be accidentally re-created per player.
		YardPresence: s.yardBudget,
		// The market cache: what a market deals in, how deep it is, and the
		// two-sided quotes the spread weighting reads (columns CROSSED — see
		// MarketPrices, where an uncrossed wiring fails silently).
		MarketGoods: s.marketGoods,
		SpreadOf:    s.marketGoods,
		// The screen's only genuine API spend: the goods CATALOGUE of a charted
		// market no hull has visited, which survives a presence-less GET. It is
		// CHARGED to the fleet market-scan budget but never gated by it — there is
		// no store to serve a cache gap from (sp-ntgfj).
		RemoteMarket: s.remoteMarket,
		// Money: the same live-treasury reader every other guard uses, and the
		// trading fleet's measured cargo outflow, which is what makes the probe
		// buy floor dynamic rather than a fixed number.
		Treasury:   parkedSensingAdapters.NewTreasuryPort(expansionAdapters.NewTreasuryReader(s.apiClient)),
		CargoSpend: parkedSensingAdapters.NewCargoSpendPort(s.transactionRepo),
		// The purchaser PERSISTS every shipyard listing set its quote reads, which
		// is what populates the memo above — without the writer the memo can never
		// learn and every dead yard is re-quoted forever.
		Purchaser: parkedSensingAdapters.NewProbePurchasePort(s.med, s.shipRepo, s.shipyardInventoryRepo),
		Ships:     parkedSensingAdapters.NewShipPositionPort(s.db),
		Fleet:     parkedSensingAdapters.NewFleetTagPort(s.shipRepo),
		Mover:     parkedSensingAdapters.NewMoverPort(s.med, gateNeighbours),
		// Per-system stored gate adjacency — never the whole-map read, and never a
		// fetch-through resolver.
		Gates: gateNeighbours,
		// The DELIBERATE fetch-through gate read, over the SAME gategraph service the
		// `spacetraders system gates --system X1-…` CLI verb drives and every router already
		// resolves through — so there is one live gate fetcher in the codebase, one persistence
		// path, and one negative-result backoff for a gate the API refuses.
		//
		// It is a SECOND port beside Gates rather than a widening of it: Gates is asked of every
		// known system on every tick and must stay a pure store read, while this is asked of a
		// bounded, ordered handful (MaxGateReads) of systems the store has already said it cannot
		// answer for. Without it the engine could only learn a system's adjacency by flying a
		// probe to it, which left the fleet sealed inside a 57-system pocket whose one built,
		// passable exit nobody had ever read.
		GateRead: parkedSensingAdapters.NewGateReadPort(s.gateGraphService),
		// The same stored adjacency the placement mover walks: a seed's target
		// may now be up to MaxWalkRings hops out, so the crossing resolves its
		// next system from the gate graph rather than jumping at the errand's
		// final system, which the API rejects when it is not connected.
		SeedShip: parkedSensingAdapters.NewSeedCommandPort(s.med, s.apiClient, s.playerRepo, s.waypointRepo, s.marketScanner, gateNeighbours),
		Scan:     parkedSensingAdapters.NewScanRunnerPort(s.marketScanner),
		Home:     parkedSensingAdapters.NewHomeSystemPort(s.db),
		// The sensing surge's work list: charted systems we hold no price for
		// (sp-zvywu). Same instance for every player — see its construction above.
		UnpricedPool: s.unpricedPool,
		// THE WAVE: probe buying pauses while the treasury climbs toward a heavy hull's
		// ask. It is the SECOND consumer of the predicate the fleet-growth coordinator
		// spends on, and every fact behind it is shared with that coordinator by
		// INSTANCE rather than by agreement — the same heavy target (so the two cannot
		// save toward different yards), the same capacity-short signal, ONE container
		// read serving both the cap and the buyer's master switch, and the ledger's
		// PEAK-over-window reader. A point read in that last slot makes the regime a
		// function of where in a trade cycle the tick landed, and every test of the
		// predicate still passes.
		//
		// Every read behind it is LOCAL, which is what lets the drain consult it ahead of
		// its cheapest-first gate order without pricing a tick that buys nothing.
		Wave: parkedSensingAdapters.NewWavePort(
			heavyBuyerCaps,
			parkedSensingAdapters.NewHeavyReservePort(
				parkedSensingAdapters.NewShipRepoCensus(s.shipRepo),
				heavyTargetFinder,
				heavyBuyerCaps,
			),
			sensingLanePort(unservedLaneReader),
			sensingHighWaterPort(s.transactionRepo),
			nil,
		),
		// The budget the whole model is sized against: sensing is the RESIDUAL
		// consumer, so it reads how much of the ceiling everyone else is using.
		Budget: parkedSensingAdapters.NewBudgetRatePort(metricsAdapter.GetGlobalAPIBudgetTracker(), api.RateLimitPerSecond),
	}
}

func run(cfg *config.Config) error {
	db, err := openDatabase(&cfg.Database)
	if err != nil {
		return err
	}
	defer database.Close(db)
	reconcileSchema(db)

	// Single-writer guard (sp-wrh84): take a Postgres SESSION advisory lock for
	// this player BEFORE recovering any containers, so two daemons can never write
	// the same player's game state — even past a PID-file/socket mismatch or a
	// manual --force. The lock auto-releases when the pinned connection closes at
	// shutdown (or drops on crash). SQLite (tests/local) is already a single-writer
	// store, so the guard is Postgres-only.
	if cfg.Database.Type == "postgres" {
		playerLock, err := acquirePlayerAdvisoryLock(db, cfg.Captain.PlayerID)
		if err != nil {
			return err
		}
		// Hold the lock (pinned connection) for the whole daemon lifetime; the
		// deferred Close releases it at shutdown (LIFO: runs before database.Close).
		defer func() { _ = playerLock.Close() }()
		fmt.Printf("Daemon advisory lock acquired for player %d\n", cfg.Captain.PlayerID)
	}

	// Initialize waypoint converter. Its sole consumer is the contract fleet
	// coordinator, constructed far below; no repository takes it.
	waypointConverter := api.NewWaypointConverter()
	fmt.Println("Waypoint converter initialized")

	// Initialize repositories
	playerRepo := persistence.NewGormPlayerRepository(db)
	waypointRepo := persistence.NewGormWaypointRepository(db)
	systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
	containerLogRepo := persistence.NewGormContainerLogRepository(db, nil) // nil = use RealClock in production
	containerRepo := persistence.NewContainerRepository(db)
	marketRepo := persistence.NewMarketRepository(db)
	marketRepoAdapter := persistence.NewMarketRepositoryAdapter(marketRepo)
	contractRepo := persistence.NewGormContractRepository(db)
	transactionRepo := persistence.NewGormTransactionRepository(db)
	priceHistoryRepo := persistence.NewGormMarketPriceHistoryRepository(db)

	apiClient := api.NewSpaceTradersClient()
	// Cache Get Agent (the #2 API consumer) with a short TTL. Every
	// GetAgent caller shares this one client, so the money guards and monitors all
	// benefit at once; safety comes from invalidating on every credit-decreasing
	// call inside the client. 0/unset -> the client's built-in 15s default.
	apiClient.SetAgentCacheTTL(time.Duration(cfg.Daemon.AgentCacheTTLSeconds) * time.Second)
	// The limiter-pressure EWMA half-life the probe-sensing coordinator sheds scanning
	// against (RULINGS #5 — an operational tuning number, not a rebuild). 0/unset -> the
	// client's built-in 30s default; a persisted sensing-container tune overrides at rebuild.
	apiClient.SetLimiterPressureHalfLife(time.Duration(cfg.Daemon.LimiterPressureHalfLifeSeconds) * time.Second)
	apiClient.SetFleetIsolationAbortStreak(cfg.Daemon.FleetIsolationAbortStreak)
	fmt.Println("API client initialized")

	// Declared as the interface up here and assigned below, once graphService —
	// the IWaypointProvider it is built over — exists.
	var shipRepo navigation.ShipRepository
	fmt.Println("Ship repository will be initialized after waypoint provider")

	routingClient, err := newRoutingClient(cfg.Routing.Address)
	if err != nil {
		return err
	}

	// In-process and stateless, so concurrent callers plan concurrently.
	routeFinder := domainRouting.NewFuelStatePlanner()

	graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
	fmt.Println("Graph builder initialized")

	// Initialize unified graph service (replaces SystemGraphProvider + WaypointProvider)
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

	// Initialize mediator (CQRS dispatcher)
	med := common.NewMediator()

	// Register middleware (must be done before registering handlers)
	med.RegisterMiddleware(common.PlayerTokenMiddleware(playerRepo))

	repos := daemonRepos{
		med:               med,
		apiClient:         apiClient,
		shipRepo:          shipRepo,
		playerRepo:        playerRepo,
		waypointRepo:      waypointRepo,
		containerRepo:     containerRepo,
		graphService:      graphService,
		marketRepo:        marketRepo,
		marketRepoAdapter: marketRepoAdapter,
		contractRepo:      contractRepo,
		transactionRepo:   transactionRepo,
	}

	if err := repos.registerShipTacticHandlers(); err != nil {
		return err
	}

	// Create extracted services for NavigateRouteHandler
	waypointEnricher := ship.NewWaypointEnricher(waypointRepo)
	routePlanner := ship.NewRoutePlanner(routeFinder)

	// Market scanner for automatic market data collection during navigation.
	marketScanner := newMarketScanner(cfg.MarketScan, apiClient, marketRepo, playerRepo, priceHistoryRepo)

	// Ship event bus for pub/sub of ship state changes (arrival, cooldown, etc.)
	// Used by ShipStateScheduler (publisher) and RouteExecutor (subscriber)
	shipEventBus := ship.NewShipEventBus()
	fmt.Println("Ship event bus initialized")

	captainEventRepo := persistence.NewGormCaptainEventRepository(db)
	// Burst-group retry-storm event types at emission so one incident is one
	// event in the captain's attention budget, not one per retry. Raw
	// per-retry rows still land in the container logs. container.crashed is
	// intentionally excluded: it stays one-row-per-death for detectCrashLoops.
	captainRecorder := watchkeeper.NewBurstGroupingRecorder(
		captainEventRepo, watchkeeper.DefaultBurstWindow, captain.EventWorkflowFailed)
	grpc.SetCaptainEventRecorder(captainRecorder)
	grpc.SetDefaultWorkerEventPublisher(shipEventBus)
	fmt.Println("Captain event outbox initialized")

	// Deploy-completed signal: there is no distinct Go merge-deploy
	// path in this codebase, so a fresh boot running a different commit than
	// the last recorded deploy.completed IS the honest deploy signal the
	// crash-loop-resumes-on-deploy doctrine keys on. Best-effort bead id from
	// HEAD; a failure here is logged and never blocks the daemon boot.
	//
	// Guard the emit behind a player-exists check. captain_events.player_id
	// FKs players.id, so on first boot against a fresh DB (no player row yet) the
	// insert violated fk_captain_events_player (23503). The signal is re-evaluated
	// every boot, so skipping the player-less boot loses nothing.
	if err := recordDeployIfPlayerExists(
		context.Background(), playerRepo, captainEventRepo, cfg.Captain.PlayerID,
		buildinfo.Get(), watchkeeper.BeadIDFromHEAD(".")); err != nil {
		fmt.Printf("watchkeeper: deploy.completed check failed (continuing): %v\n", err)
	}

	// Shipyard scanner: piggybacks a shipyard-inventory scan on the scout
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
	shipyardScanner, yardBudget := newShipyardScanner(
		cfg.ShipyardScan, cfg.Scouting, apiClient, shipyardInventoryRepo, waypointRepo, captainEventRepo,
	)

	// From [refuel] rather than a code literal; printed because the floor silently
	// overrides a knob set below it, and an operator must see which value won.
	refuelStrategy := strategies.NewConservativeRefuelStrategy(cfg.Refuel.Threshold)
	fmt.Printf("Refuel threshold: %.2f of tank capacity (configured %.2f, floor %.2f)\n",
		strategies.ResolveRefuelThreshold(cfg.Refuel.Threshold), cfg.Refuel.Threshold, strategies.MinRefuelThreshold)
	routeExecutor := ship.NewRouteExecutor(shipRepo, med, nil, marketScanner, shipyardScanner, refuelStrategy, waypointRepo, shipEventBus) // nil clock = RealClock

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

	constructionSiteRepo := api.NewConstructionSiteRepository(apiClient, playerRepo)

	jumpShipHandler := shipNav.NewJumpShipHandler(shipRepo, playerRepo, apiClient, med, containerRepo, constructionSiteRepo, nil) // constructionRepo enables the at-complete-gate driveless-jump check; nil clock = RealClock
	if err := mediator.RegisterHandler[*shipNav.JumpShipCommand](med, jumpShipHandler); err != nil {
		return fmt.Errorf("failed to register JumpShip handler: %w", err)
	}

	// Ship outfitting handlers: install/remove/list modules. One
	// handler backs all three commands. The op atomically claims the hull
	// (RULING #3/#7) and gates the modification fee on the working-capital
	// reserve (RULING #4).
	outfittingHandler := shipOutfit.NewOutfittingHandler(shipRepo, playerRepo, apiClient, shipyardScanner, containerRepo, med, nil) // nil clock = RealClock
	if err := mediator.RegisterHandler[*shipOutfit.InstallModuleCommand](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register InstallModule handler: %w", err)
	}
	if err := mediator.RegisterHandler[*shipOutfit.RemoveModuleCommand](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register RemoveModule handler: %w", err)
	}
	if err := mediator.RegisterHandler[*shipOutfit.ListShipModulesQuery](med, outfittingHandler); err != nil {
		return fmt.Errorf("failed to register ListShipModules handler: %w", err)
	}

	// Ship scrap handler: sells a hull for credits and retires it. Claims the hull
	// (RULING #3/#7) and refuses while it still holds cargo (RULING #4).
	scrapHandler := shipScrap.NewScrapShipHandler(shipRepo, playerRepo, apiClient, containerRepo, med, nil) // nil clock = RealClock
	if err := mediator.RegisterHandler[*shipScrap.ScrapShipCommand](med, scrapHandler); err != nil {
		return fmt.Errorf("failed to register ScrapShip handler: %w", err)
	}

	// Market scouting handlers (shipyardScanner constructed above, next to the
	// route executor it now also feeds — sp-42ow emit-path fix)
	scoutTourHandler := scoutingCmd.NewScoutTourHandler(shipRepo, med, marketScanner, shipyardScanner, nil) // nil clock = RealClock
	// The same recent-scan window the trade coordinators stamp, so the fleet keeps
	// one definition of "already scanned recently enough" rather than two that drift.
	scoutTourHandler.SetScanDedupWindow(cfg.TradeImpact.ResolvedScanMaxAge())
	// The posts table survives only so leftover rows can be listed and cleared; the sensing
	// cutover is its last reader.
	scoutPostRepo := persistence.NewGormScoutPostRepository(db)
	if err := mediator.RegisterHandler[*scoutingCmd.ScoutTourCommand](med, scoutTourHandler); err != nil {
		return fmt.Errorf("failed to register ScoutTour handler: %w", err)
	}

	if err := repos.registerMarketAndPlayerQueryHandlers(); err != nil {
		return err
	}

	if err := repos.registerShipQueryHandlers(); err != nil {
		return err
	}

	reserveShipHandler, assignShipFleetHandler, err := repos.registerFleetAssignmentHandlers()
	if err != nil {
		return err
	}

	if err := repos.registerWaypointQueryHandlers(); err != nil {
		return err
	}

	// Shipyard handlers
	getShipyardListingsHandler := shipyardQuery.NewGetShipyardListingsHandler(shipyardScanner, playerRepo)
	if err := mediator.RegisterHandler[*shipyardQuery.GetShipyardListingsQuery](med, getShipyardListingsHandler); err != nil {
		return fmt.Errorf("failed to register GetShipyardListings handler: %w", err)
	}

	purchaseShipHandler := shipyardCmd.NewPurchaseShipHandler(shipRepo, playerRepo, waypointRepo, graphService, apiClient, med, shipyardScanner)
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

	if err := repos.registerLedgerHandlers(); err != nil {
		return err
	}

	if err := repos.registerContractLifecycleHandlers(); err != nil {
		return err
	}

	// ContractWorkflow handler is constructed AFTER the storage coordinator +
	// warehouse (sp-dchv Lane B/D) so it can be wired with inventory-first
	// sourcing — see "Inventory-first contract sourcing" below.

	balanceShipHandler := contractCmd.NewBalanceShipPositionHandler(med, shipRepo, containerRepo, graphService, marketRepo, nil) // nil = use RealClock
	if err := mediator.RegisterHandler[*contractCmd.BalanceShipPositionCommand](med, balanceShipHandler); err != nil {
		return fmt.Errorf("failed to register BalanceShipPosition handler: %w", err)
	}

	homeShipHandler := contractCmd.NewHomeShipHandler(med, shipRepo, graphService) // Dedicated fleet homing
	if err := mediator.RegisterHandler[*contractCmd.HomeShipCommand](med, homeShipHandler); err != nil {
		return fmt.Errorf("failed to register HomeShip handler: %w", err)
	}

	sellCargoHandler := shipCargo.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, med, marketScanner)
	if err := mediator.RegisterHandler[*shipCargo.SellCargoCommand](med, sellCargoHandler); err != nil {
		return fmt.Errorf("failed to register SellCargo handler: %w", err)
	}

	// Initialize daemon server
	socketPath := cfg.Daemon.SocketPath
	fmt.Printf("Starting daemon server on: %s\n", socketPath)

	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	daemonServer, err := grpc.NewDaemonServer(med, db, containerLogRepo, containerRepo, waypointRepo, shipRepo, playerRepo, routingClient, apiClient, socketPath, &cfg.Metrics, cfg.Contract, cfg.TradeFleet, cfg.WorkerRebalancer, cfg.Scouting, cfg.Sensing, cfg.Bootstrap, cfg.ShipResync, cfg.ContainerLogRetention, shipEventBus)
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
	absorptionReclaimer := grpc.NewDeadContainerAbsorptionReclaimer(absorptionLedger)

	// ONE shared, decaying, per-lane compression ledger for the whole fleet. Every
	// trade-route/arb/tour/stocker leg Accrues its compression debt to it and every lane
	// rank Debt-reads it, so once the fleet hammers a lane it stays down-weighted for ~tau
	// (hours) and hulls rotate to fresh lanes. Coefficients are era-3 config (refit per
	// era); an absent [trade_impact] section resolves to the analyst's era-3 fit.
	laneCooldownLedger := domainTrading.NewLaneCooldownLedger(
		cfg.TradeImpact.ResolvedBuyImpact(),
		cfg.TradeImpact.ResolvedSellImpact(),
		cfg.TradeImpact.ResolvedCooldownTau(),
	)
	// Activity-conditioned decay — ARMED. A market under STRONG activity sheds a price move several
	// times faster than one under RESTRICTED, so a single constant holds a recovered source yielded
	// far longer than it needs. Activity is an OBSERVABLE and is only ever READ here: it does not
	// track our buying, so nothing in the guard may assume a trade can induce a tier. A market the
	// cache cannot answer for keeps the slow rate.
	laneCooldownLedger.SetActivityResolver(func(waypoint, good string) (string, bool) {
		mkt, err := marketRepo.GetMarketData(context.Background(), waypoint, cfg.Captain.PlayerID)
		if err != nil || mkt == nil {
			return "", false
		}
		g := mkt.FindGood(good)
		if g == nil || g.Activity() == nil {
			return "", false
		}
		return *g.Activity(), true
	})

	// The ledger is in-memory, so a restart would forget how much the fleet has just taken out of
	// each market — permissive amnesia in a value a spend guard now reads (RULINGS #2 names cooldown
	// clocks). Replay it from the purchase rows, which already record every drain durably.
	//
	// HERE, before any coordinator is handed the ledger to accrue into: Rebuild refuses a key that
	// already carries debt, so a replay running after live accrual would silently restore nothing.
	// It cannot fail the boot — see replayLaneCooldown.
	replayLaneCooldown(
		context.Background(), laneCooldownLedger, transactionRepo, marketRepo, playerRepo,
		cfg.Captain.PlayerID, cfg.TradeImpact.ResolvedCooldownTau(), time.Now(),
	)

	contractFleetCoordinatorHandler := contractCmd.NewRunFleetCoordinatorHandler(med, shipRepo, contractRepo, marketRepoAdapter, daemonClientLocal, graphService, waypointConverter, containerRepo, nil, captainEventRepo)
	contractFleetCoordinatorHandler.SetEventSubscriber(shipEventBus)
	// First-boot seed marker (sp-86vb): persist "the --dedicated-ships seed has
	// been applied" into the coordinator's own container config after first boot,
	// so a daemon restart does NOT replay the stale seed over live fleet state and
	// a `fleet remove` survives the restart (RULINGS #2).
	contractFleetCoordinatorHandler.SetDedicatedFleetSeedMarker(grpc.NewDedicatedFleetSeedConfigPersister(containerRepo))
	// Live standby-station ("hub") set: the coordinator resolves its hub
	// set from its own container config every discovery pass, so a `fleet hub
	// add|remove` on the running coordinator is honored with no restart — the
	// operation-level mirror of the live dedicated-fleet tag read.
	contractFleetCoordinatorHandler.SetStandbyStationProvider(grpc.NewStandbyStationConfigProvider(containerRepo))
	// Live per-park DEMAND weights (sp-5rakx/sp-bu6ma, epic sp-9le3x C2c): the coordinator
	// homes each idle hull to its FIXED placement slot, and auto-resolves the standby set from the
	// ≤6 fixed placement slots when the `fleet hub` set is empty (fixes the pile-up). Backed by the
	// SAME home-system role lookup + TopDeliverySlots selection the contract auto-scaler buys against
	// (marketRepo, waypointRepo, shipRepo) — ONE slot set so the two positioning consumers place hulls
	// identically. A READ, never a config write (RULINGS #3); coord-deduped to distinct LOCATIONS.
	contractFleetCoordinatorHandler.SetStandbyPlacementProvider(grpc.NewContractStandbyPlacementProvider(shipRepo, waypointRepo, marketRepo))
	// Idle-gap arb: the coordinator's dispatcher launches its
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
	// A nil clock is fail-open here too (EstimateAll guards e.clock == nil); production always wires the real one.
	contractFleetCoordinatorHandler.SetRouteETAEstimator(appContract.NewRouteETAEstimator(routeFinder, shared.NewRealClock(), cfg.Contract.RouteETABudget()))
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

	// Register the standing trade-fleet coordinator: it watches every 'trade'-dedicated hull
	// and relaunches a continuous tour on any hull parked by an honest tour exit, after a
	// per-hull cooldown. It claims nothing itself; each tour it spawns claims its own hull
	// under operation="trade" through the daemon server (SetTourLauncher), the SAME
	// StartTourRun path `workflow tour-run` uses. Tuning is resolved live from config.yaml
	// [trade_fleet].
	// Every path that SEVERS a live work-claim must also reap the container that
	// lost the hull. Breaking the claim frees the hull atomically and correctly, but does
	// nothing to the container flying it — which keeps navigating, buying and selling on a
	// hull it no longer owns while the coordinator, reading ownership from the hull's single
	// assignment row, launches a SECOND container onto it. The orphan's RUNNING row then
	// survives until a daemon restart's recovery sweep fails it (measured: 4.0h and 2.9h).
	// Wired post-construction because both handlers are registered with the mediator above,
	// before NewDaemonServer exists.
	assignShipFleetHandler.SetOrphanedContainerReaper(daemonServer) // `fleet unassign`
	reserveShipHandler.SetOrphanedContainerReaper(daemonServer)     // `ship reserve --force`

	tradeFleetCoordinatorHandler := tradeRouteCmd.NewRunTradeFleetCoordinatorHandler(shipRepo, nil) // nil = use RealClock
	tradeFleetCoordinatorHandler.SetTourLauncher(daemonServer)
	tradeFleetCoordinatorHandler.SetEventRecorder(captainEventRepo) // Emit coordinator error-loop events on reconcile streak breach
	// sp-m3122 liveness watchdog: read each running tour's last real-progress time and kill+relaunch
	// any RUNNING-but-hung tour (the daemon serves both ports over the containers/logs it single-writes),
	// plus promptly release absorption reservations of dead containers on restart / after a kill.
	tradeFleetCoordinatorHandler.SetTourLiveness(daemonServer)
	tradeFleetCoordinatorHandler.SetTourStopper(daemonServer)
	tradeFleetCoordinatorHandler.SetAbsorptionReclaimer(absorptionReclaimer)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTradeFleetCoordinatorCommand](med, tradeFleetCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TradeFleetCoordinator handler: %w", err)
	}

	// Production services for the construction-supply drain, their only consumer. Built here
	// rather than inside it so the executor, the activator and the coordinator below all hold
	// the SAME locator/resolver singletons.
	goodsMarketLocator := goodsServices.NewMarketLocator(marketRepoAdapter, waypointRepo, playerRepo, apiClient)
	goodsMarketLocator.SetYardSource(shipyardScanner)
	goodsResolver := goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepoAdapter)

	// Register the standing construction-supply drain: the coordinator that rebuilds
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
	// sp-muq66: the daemon's ONE treasury reader. `Get Agent` measured 0.167 req/s — 8.3% of
	// the 2.00 req/s ceiling — and did NOT fall under request coalescing, because the money
	// guards' reads are invalidation-driven (every buy/sell/refuel/jump empties the agent
	// cache) rather than concurrent duplicates. This reader answers from the transaction
	// ledger, which already carries the same balance to the credit, and falls back to the
	// coalesced live read when the newest row is older than the 30s freshness bound. Shared
	// by the tour coordinator, the trade-route circuit, the fleet-growth coordinator, the contract
	// scaler and — since sp-45s6f — the stocker, the one-shot arb and BOTH factory spend
	// guards, so every money guard reads treasury one way. Built HERE, above its first
	// consumer, rather than beside the growth wiring further down: the construction executor is
	// the earliest guard that needs it. DELIBERATELY NOT wired into bootstrap (cold start has
	// a legitimately empty ledger and stays live-first) nor into the captain's own credits
	// reader (separate process, wake gate not a money guard).
	ledgerTreasury := grpc.NewLedgerTreasuryReader(db, apiClient)

	// sp-9bacx: wired here, not beside the handler above, which is registered before this exists.
	tradeFleetCoordinatorHandler.SetTreasuryReader(ledgerTreasury)

	// sp-ps2oc: THE CROSS-OPERATION CONCURRENT SPEND CAP. One ledger, shared by every operation
	// that draws on the treasury — the construction executor below and the contract source-buy
	// further down. It must be ONE instance: two ledgers are two budgets, and the aggregate
	// breach reappears one level up.
	//
	// This wiring is the sp-ps2oc fix. sp-w3he built the cap and wired it onto
	// factoryCoordinatorHandler (4ee47ef0); sp-hoj8u retired the goods-factory operation and
	// deleted that handler (712b6f66), taking the ONLY production call to SetSpendLedger with
	// it. The guard, its interface, its repository and all of its tests survived — only the
	// wiring died, so reserveConcurrentSpendOrPark returned at its nil-ledger fail-open branch
	// on every gate buy. Three construction_supply buys then landed inside 68ms, each clearing
	// the per-buy floor and together taking treasury 75k BELOW the reserve, which deadlocked
	// every income path at once. spend_ledger_wiring_test.go pins this call so an unrelated
	// refactor cannot silently delete it again.
	concurrentSpendCap := persistence.NewSpendReservationLedger(db)

	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo, marketRepoAdapter, goodsMarketLocator, shared.NewRealClock(), apiClient)
	constructionExecutor.SetConstructionRepo(constructionSiteRepo)
	constructionExecutor.SetSpendLedger(concurrentSpendCap)
	// BOTH factory money guards (the per-buy spend floor and the cross-container
	// concurrent-spend cap) read treasury through the shared ledger-backed reader instead of
	// calling Get Agent before every input tranche. Unconditional — no config key, no arming.
	constructionExecutor.SetTreasuryReader(ledgerTreasury)
	// Per-operation capital budget: ONE sensor, shared by BOTH spend guards (the
	// construction executor below and the tour coordinator further down), so their two views of
	// the trade/construction split are always resolved from the same source and can never each
	// conclude "the other side is idle" and both take 100%. Wired UNCONDITIONALLY — there is no
	// config key and no arming step; the budget is live the moment the daemon boots.
	// The construction side is sensed as DEMAND, not liveness: the drain keeps ticking
	// after its gate is filled, and reserving 40% of deployable capital for a finished bill funds
	// nothing. The pipeline repo supplies the bill; without it the sensor degrades conservatively
	// to the old liveness-only reading, so a wiring slip can never hand trade the whole treasury.
	capitalWorkSensor := common.NewEngineCapitalWorkSensor(containerRepo).
		WithConstructionDemand(goodsServices.NewConstructionDemandReader(constructionPipelineRepo))
	constructionExecutor.SetCapitalWorkSensor(capitalWorkSensor)
	constructionExecutor.SetPendingScalingReservation(persistence.NewPendingScalingReservationRepository(db))
	// The rescue-buy validator's trailing-median source (sp-f5lki). It was NEVER WIRED: the repo
	// was built at the top of this function and handed only to the market scanner, so
	// trailingMedianAsk returned ok=false on every call and rescueSource parked EVERY rescue buy
	// for the life of the process — while logging "no trailing median at %s", which reads as absent
	// market data rather than an absent collaborator. Four sibling setters land on this same
	// receiver in this same block; this one was skipped.
	//
	// It ARMS a guard rather than relaxing one: the rescue cap (ask <= multiplier x trailing
	// median) can now actually be evaluated. A buy over the cap is still refused, and no samples in
	// the window still parks (RULINGS #4) — what changes is that the guard reaches its judgement
	// instead of failing closed on a missing reader. Pinned by executor_guard_wiring_test.go.
	constructionExecutor.SetPriceHistoryReader(priceHistoryRepo)
	// The activator is the SURVIVING SupplyMonitor: NO new
	// activation logic. Built per-player because it bakes in the playerID; the poll-loop-only
	// collaborators (factory tracker/state, sell distributor, storage, container reader, event
	// publisher) are left nil — construction activation uses only task/pipeline/queue/market.
	constructionActivatorFactory := func(pid int) goodsCmd.ConstructionActivator {
		return goodsServices.NewSupplyMonitor(
			marketRepoAdapter, nil, nil, constructionPipelineRepo, goodsServices.NewTaskQueue(),
			constructionTaskRepo, nil, goodsMarketLocator, nil, nil, nil, constructionActivatorPollInterval, pid,
		)
	}
	constructionCoordinatorHandler := goodsCmd.NewRunConstructionCoordinatorHandler(
		constructionTaskRepo, constructionPipelineRepo, shipRepo, constructionExecutor, constructionActivatorFactory, nil, // nil = use RealClock
	)
	// DI the SAME resolver singleton the goods-factory path holds so the construction drain
	// builds the FULL scarcity-gated dependency tree for a FABRICATE material (produce scarce
	// intermediates that have a factory, buy abundant ones) instead of the flat one-level node —
	// bounded by the pipeline's SupplyChainDepth + the resolver's cycle guard, config-reversible.
	constructionCoordinatorHandler.SetTreeResolver(goodsResolver)
	// The SAME shared construction-site read the planner and the delivery terminal use, so
	// each tick reconciles the pipeline's delivered counters against the server before sizing buys.
	// Unwired, those counters can only drift BEHIND (they are written after the server already
	// accepted a supply) and the drain over-sources material the gate no longer needs.
	constructionCoordinatorHandler.SetConstructionSiteSource(constructionSiteRepo)
	// The gate DELIVERY fleet: phase 1's role-based topology resolves this era's terminal factory
	// (the waypoint that EXPORTS each gate material — never a hardcoded symbol), and the executor
	// buys there with every money guard unchanged. The handler holds ONE buy policy for the process,
	// so the supply-anchored pause hysteresis survives across legs. Optional collaborator: unwired,
	// the drain behaves exactly as before.
	// ONE topology object serves BOTH fleets. It is a stateless pair of immutable fields over the
	// SHARED goodsMarketLocator, so a second instance could not diverge — but binding it to a name
	// keeps the composition root honest about the two fleets reading one map, and the two setters
	// take DIFFERENT views of it (GateTopologyResolver asks only where a good is exported;
	// GateFactoryTopology also answers the recipe seam).
	gateTopology := goodsServices.NewGateTopology(goodsMarketLocator, goods.ExportToImportMap)
	constructionCoordinatorHandler.SetGateDelivery(
		gateTopology,
		constructionExecutor,
	)
	// The gate FACTORY fleet — ARMED. Wired unconditionally, alongside the delivery fleet: no flag,
	// no config key, no arm seam. The nil-guard inside SetGateFactory is the drain's
	// optional-collaborator pattern for its own fixtures, and this call is what makes that
	// distinction true rather than merely claimed.
	//
	// The SAME topology answers the recipe seam the recursive feed walk needs (IsRaw/Inputs — never
	// goods.GetRequiredInputs, which returns a recipe for every ore and would send the walk into the
	// cyclic part of the map), and the SAME executor both performs the pinned input buy through the
	// one shared tranche loop and feeds the factory by selling into its import listing. One spend
	// primitive, one set of money guards, for both fleets.
	//
	// This call also arms the pause-driven role reallocation: reallocateGateRoles returns before
	// doing anything unless BOTH legs are wired on this one handler.
	constructionCoordinatorHandler.SetGateFactory(
		gateTopology,
		constructionExecutor,
		constructionExecutor,
	)
	// Source pacing — ARMED, and sharing the trade engine's ledger rather than keeping a second
	// one. The feeding leg is the largest buyer of some exports in the system, so its own volume is
	// a first-class part of the fleet's compression picture; a private ledger would let the two
	// engines drain one market while each saw only half of it.
	constructionCoordinatorHandler.SetSourceCooldown(laneCooldownLedger)
	if err := mediator.RegisterHandler[*goodsCmd.RunConstructionCoordinatorCommand](med, constructionCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register ConstructionCoordinator handler: %w", err)
	}

	// There is deliberately no contract-hub PLACEMENT coordinator here: where idle contract
	// haulers stage is owned by the scaler's ResolveRoles (home-system geometry + market roles)
	// plus C1's demand-ranked homing via the fleet coordinator, whose shared `fleet hub`
	// standby-station providers are wired above.

	// The persisted, fetch-through gate-graph resolver. travel() BFS-walks it to
	// cross a multi-hop gap, and the arb pre-buy guard route-checks a cross-system
	// sell leg through it BEFORE spending. Shared by the trade-route circuit, the
	// one-shot arb, and the reachable-yard ranking so they all see one
	// cache/graph. Constructed here, ahead of the growth wiring that consumes it.
	// The healthy-edge freshness window is the configured topology-cache TTL
	// ([routing] gate_cache_ttl, 24h default) — the per-tick lane/reposition neighbor scan
	// hits this cache instead of re-reading gate topology live.
	gateEdgeRepo := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(cfg.Routing.GateCacheTTL))
	// A jump asks two topology questions the router has already answered and stored:
	// which gate waypoint the hop leaves for, and whether the source gate is built.
	// Attached here, where the store exists; the handler keeps its live reads for
	// anything the store does not hold.
	jumpShipHandler.SetJumpTopologyStore(gateEdgeRepo)
	gateGraphService := newGateGraphService(cfg.Routing, gateEdgeRepo, apiClient, graphService, playerRepo, med)
	// sp-fihvy: wire the SAME gate-graph reachability service into the daemon server so the depot
	// stocker hull viability precondition can consult it (Routable) — never a second reachability
	// mechanism. Post-construction (gateGraphService is built after NewDaemonServer runs), mirroring
	// SetStorageRecovery below.
	daemonServer.SetGateGraph(gateGraphService)
	// StartConstructionPipeline builds its own MarketLocator per call; give it the
	// shared shipyard reader so its hull search draws on the fleet's one
	// shipyard-read allowance rather than reaching the API unmetered.
	daemonServer.SetYardScanner(shipyardScanner)

	circuits := circuitWiring{
		cfg:              cfg,
		db:               db,
		containerRepo:    containerRepo,
		transactionRepo:  transactionRepo,
		marketRepo:       marketRepo,
		captainEventRepo: captainEventRepo,
		marketScanner:    marketScanner,
		shipEventBus:     shipEventBus,

		gateGraph:         gateGraphService,
		treasury:          ledgerTreasury,
		absorption:        absorptionLedger,
		laneCooldown:      laneCooldownLedger,
		capitalWorkSensor: capitalWorkSensor,

		chartGateOnArrival: defaultOn(cfg.Routing.ChartGateOnArrival),
	}

	// Off-gate warp support: attach the warp-execute +
	// chart-on-arrival capability to the route executor now that gateGraphService
	// exists (WithWarpSupport mutates the same *RouteExecutor the nav handlers
	// already hold, so no re-wiring is needed). The charter reuses the SAME gate
	// graph, market scanner, and shipyard scanner the gate-nav path uses, plus the
	// graph provider as its waypoint source. Its caller is the `ship warp` verb wired just
	// below.
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

	// The ReachableYardFinder is the heavy branch's yard-price FALLBACK — scout-scanned
	// yards ranked by stored-gate-graph hops then price. Signal-only: with no scan data the price
	// guard fails closed exactly as before, and every other guard still gates the buy.
	reachableYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)

	// THE SHARED HEAVY TARGET (sp-fwk8z). ONE instance, two consumers: the fleet-growth coordinator
	// (which SPENDS the accumulation) and the sensing buy-floor (which WITHHOLDS it) both read the heavy
	// target through this, so they can never end up saving toward different yards. The reservation's
	// arithmetic already has a single definition in common.HeavyReserve; this gives its price term
	// one too. Constructed here, at the composition root, precisely so a second one is conspicuous.
	heavyTargetFinder := shipyardQuery.NewHeavyTargetFinder(
		shipyardInventoryRepo, // availability — HasAnyOfTypes, the read with NO price predicate
		reachableYardFinder,   // the buy path's own priced rank
		shipRepo,              // reach is measured from the systems the fleet actually holds
		nil,                   // nil ⇒ the documented heavy classes
	)

	// THE SHARED CAPACITY-SHORT SIGNAL. ONE instance, two consumers: the fleet-growth coordinator
	// (which SPENDS on it) and the sensing wave gate (which PAUSES on it). Two readers of one
	// quantity is how the spender and the withholder end up disagreeing about whether the fleet is
	// capacity-short. Constructed here, at the composition root, precisely so a second one is
	// conspicuous.
	// Same freshness table as the trade ranker, or the census counts lanes the ranker already dropped.
	profitableLaneCensus := tradingQueries.NewProfitableLaneReader(marketRepo, gateGraphService)
	profitableLaneCensus.SetRankerAgeCaps(cfg.Trading.RankerAgeCapMinutes.Resolved())
	profitableLaneCensus.SetStalenessDiscount(cfg.Trading.StalenessDiscount.Resolved())
	unservedLaneReader := tradingQueries.NewUnservedLaneReader(shipRepo, profitableLaneCensus)
	unservedLaneReader.SetSaturation(cfg.Trading.TradeSaturation.Resolved())

	// The fleet-growth coordinator: the fleet's ONLY heavy buyer. It drives the shared buy-path port
	// set — treasury, API utilization, the shipyard price walk, the heavy census and target,
	// the pricing errand, the buy+dedicate purchaser — and adds the three reads the wave and the
	// working-capital term are derived from. The transaction repository serves BOTH ledger reads
	// over the one shared trailing window: the demonstrated-capacity peak and the cargo outflow.
	fleetGrowthHandler := grpc.NewFleetGrowthCoordinatorHandler(
		daemonServer, apiClient, ledgerTreasury, shipRepo, med, waypointRepo, captainEventRepo,
		reachableYardFinder, heavyTargetFinder, unservedLaneReader, transactionRepo,
	)
	if err := mediator.RegisterHandler[*fleetCmd.RunFleetGrowthCoordinatorCommand](med, fleetGrowthHandler); err != nil {
		return fmt.Errorf("failed to register FleetGrowthCoordinator handler: %w", err)
	}

	// Dedicated contract auto-scaler: the standing coordinator that ramps a FIXED, EXCLUSIVE contract
	// fleet to a live-tunable ceiling behind the 200000-credit cushion. Its concrete ports — the NOVEL
	// RoleResolver (home-system geometry + market roles), the treasury/yard-price REUSE of the shared
	// buy-path idioms, the "contract"-fleet counter, and the buy+dedicate+home Purchaser (the shared buy
	// primitive + the demand-ranked homing consumer) — are assembled inside
	// grpc.NewContractScalerCoordinatorHandler. Registering the handler changes NO live behaviour by itself
	// — it merely makes the coordinator available; the bootstrap coordinator launches this scaler during its
	// DATA/INCOME cold-start window (unconditional).
	contractScalerHandler := grpc.NewContractScalerCoordinatorHandler(
		daemonServer, apiClient, ledgerTreasury, shipRepo, med, waypointRepo, marketRepo,
		reachableYardFinder,
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
	circuits.configureTradeRouteCoordinator(tradeRouteCoordinatorHandler)
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

	containerConfigReader := grpc.NewContainerConfigReader(containerRepo)

	// Wire the `ship route` verb — a thin operator-facing cross-system
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

	// The worker-rebalancer coordinator was retired with the factory ops: it ferried idle
	// light-haulers to worker-starved FACTORY systems, which no longer exist. The worker_ferry
	// primitive it drove is retained (below) for the daemon's persist/start dispatch + container recovery.
	// The ferry worker reuses the trade-route coordinator's RepositionToWaypoint (the SAME
	// multi-jump travel() the arb/trade circuits use).
	workerFerryHandler := tradeRouteCmd.NewWorkerFerryHandler(tradeRouteCoordinatorHandler)
	if err := mediator.RegisterHandler[*tradeRouteCmd.WorkerFerryCommand](med, workerFerryHandler); err != nil {
		return fmt.Errorf("failed to register WorkerFerry handler: %w", err)
	}

	// Cargo-liquidation worker: the contract fleet coordinator's one-shot
	// self-clearing leg for a parked-with-cargo hull. It reuses the existing
	// navigate/dock/sell/jettison commands (via med) plus the ship and market repos —
	// no new ship I/O — to sell a strand at the best in-system bid, jettison only as a
	// last resort below a configured floor, and hold otherwise.
	cargoLiquidationHandler := liquidation.NewLiquidateCargoHandler(shipRepo, marketRepo, med)
	if err := mediator.RegisterHandler[*liquidation.LiquidateCargoCommand](med, cargoLiquidationHandler); err != nil {
		return fmt.Errorf("failed to register CargoLiquidation handler: %w", err)
	}

	// EXPANSION phase gate, shared by the two coordinators that only belong to the
	// gate-built steady-state era: probe SENSING (demand-driven sizing has no trading
	// footprint to size against during cold start) and probe BUYING (sp-f3mcc, Admiral
	// 2026-07-24 — never during DATA/INCOME/GATE, where it drained the contract
	// working-capital band). ONE reader so the two can never disagree about which era it
	// is. It re-derives the phase from the live world (ships → home system → jump-gate
	// construction site, the same signal bootstrap's derivePhase reads) because the phase
	// is never persisted and bootstrap exits after its hand-off. Fail-closed.
	expansionPhase := expansionAdapters.NewBootstrapExpansionPhaseReader(
		shipRepo, waypointRepo, constructionSiteRepo,
	)

	// Parked-probe sensing coordinator: the fleet's ONE standing sensing engine. Its model
	// is PARKED probes — a hull is bought for a WAYPOINT, flown there once, and then stands
	// still scanning forever — so steady-state sensing costs navigation nothing and the only
	// recurring spend is the scans, paced fleet-wide by a single rotation against whatever
	// rate-limiter headroom the rest of the fleet leaves. It owns no algorithm: each tick it
	// composes the five engines in internal/application/parkedsensing (screen → buy queue →
	// placements → expansion → scan rotation) over the durable sensing ledger, and on its FIRST tick
	// it cuts over from the touring model (screening the known map offline, adopting orphaned probes).
	//
	// expansionPhase is the era gate that holds the whole tick inert — cutover included — until the
	// home jump gate is built; before then bootstrap provisions probes and the scout-post
	// coordinator tours them, retiring itself at the same boundary this gate opens on.
	// The market-tour era rule (Admiral 2026-08-08), both halves off ONE reader. The tour
	// verbs refuse once expansionPhase reads EXPANSION, and the sensing coordinator — whose
	// whole tick begins at that same edge — stops any tour still flying and returns its probe.
	// Sharing the reader is what makes "tours before the gate, parked sensing after" a single
	// boundary rather than two that can drift apart.
	daemonServer.SetBootstrapPhaseGate(expansionPhase)

	probeSensingHandler := scoutingCmd.NewRunProbeSensingCoordinatorHandler(
		marketRepo, scoutPostRepo, shipRepo, apiClient.LimiterPressure(), expansionPhase, nil, // nil = use RealClock
	)
	sensingLedgerPort := parkedSensingAdapters.NewLedgerPort(persistence.NewSensingLedgerRepository(db))
	sensingMarketGoods := parkedSensingAdapters.NewMarketGoodsPort(db)
	// The sensing surge's pool read (sp-zvywu): the era-scoped set difference between
	// the charted systems and the ones we already hold prices for. Player-agnostic
	// instance — its method carries the player, because the priced half is
	// player-partitioned while the charted half is shared — so one instance serves
	// every player's ticks. Wired UNCONDITIONALLY and checked by wired(): a
	// nil-tolerated pool read is what a dormant feature looks like, and this ships
	// driving the live tick.
	sensingUnpricedPool := parkedSensingAdapters.NewUnpricedPoolPort(db)

	// The screen's catalogue gap fill is the one market API read that cannot pass
	// through MarketScanner, so it is charged to the same allowance directly
	// (sp-ntgfj) — metered for attribution, never deniable.
	remoteMarketPort := parkedSensingAdapters.NewRemoteMarketPort(apiClient, playerRepo)
	remoteMarketPort.SetScanBudget(marketScanner.ScanBudget())
	sensing := sensingWiring{
		ledger:       sensingLedgerPort,
		marketGoods:  sensingMarketGoods,
		unpricedPool: sensingUnpricedPool,
		remoteMarket: remoteMarketPort,

		db:              db,
		med:             med,
		apiClient:       apiClient,
		shipRepo:        shipRepo,
		playerRepo:      playerRepo,
		waypointRepo:    waypointRepo,
		transactionRepo: transactionRepo,

		gateEdgeRepo:     gateEdgeRepo,
		gateGraphService: gateGraphService,
		marketScanner:    marketScanner,
		routingClient:    routingClient,

		shipyardScanner:       shipyardScanner,
		shipyardInventoryRepo: shipyardInventoryRepo,
		yardBudget:            yardBudget,
	}
	probeSensingHandler.SetEnginePortsFactory(func(sensingPlayerID int) scoutingCmd.SensingEnginePorts {
		return sensing.enginePorts(heavyTargetFinder, unservedLaneReader, sensingPlayerID)
	})
	// Per-tick live view of the persisted config, so `tune --operation sensing` takes
	// effect on the NEXT reconcile rather than at the next rebuild (mirrors probeBuyer).
	// The graduation sweep's arm into the container registry: it stops any market tour still
	// flying past the gate and returns its probe to the pool this engine buys from.
	probeSensingHandler.SetLegacyTourSweeper(grpc.NewLegacyTourSweeper(daemonServer))
	probeSensingHandler.SetLiveConfigReader(containerConfigReader)
	probeSensingHandler.SetEventRecorder(captainEventRepo) // emit coordinator error-loop events on reconcile streak breach
	// Resolves the collector lazily per call: the metrics collectors are installed by
	// NewDaemonServer, which runs after this wiring, so a captured reference would be nil.
	probeSensingHandler.SetMetricsRecorder(parkedSensingAdapters.NewMetricsPort())
	// Stall escalation: the sensing tick and its off-gate/expansion pass each report
	// PROGRESS / IDLE / BLOCKED(reason) every tick, and a block sustained on one reason for
	// health.StallEscalationTicks consecutive ticks raises a coordinator.stalled captain event
	// beside a Prometheus escalation counter. These are the two passes that reported "0
	// discovered" for hours while a 33-system region sat behind one unread jump gate. The seam
	// is write-only by type, so no sensing decision can read the streak (RULINGS #2).
	probeSensingHandler.SetStallObserver(health.NewStallEscalator(metricsAdapter.NewStallMetricsPort(), captainEventRepo))
	if err := mediator.RegisterHandler[*scoutingCmd.RunProbeSensingCoordinatorCommand](med, probeSensingHandler); err != nil {
		return fmt.Errorf("failed to register ProbeSensingCoordinator handler: %w", err)
	}

	// The probe-buyer-fleet coordinator was RETIRED and DELETED here (Admiral 2026-07-28).
	// The probe-sensing coordinator owns probe supply: its drain buys what its own placements need,
	// behind a floor and a cap, and reuses hulls it already owns first. A second engine buying into
	// the same fleet could only double-spend, and did — 245,316 credits on 9 hulls in five minutes.

	// The dedicated contract scaler (registered above) is the ONE contract-fleet capacity owner;
	// there is deliberately no capacity-reconciler stack beside it (no SENSE/PLAN/DIFF/GOVERN/
	// CONVERGE, no gate-shortfall reader, no depot launcher, no graduation gate), and with the
	// jump gate COMPLETE the gate-depot demand machinery it fed would be dead weight anyway.
	// Nothing boot-standing or restart-recovering depends on that stack (RULINGS #2).

	tourTelemetryRepo := persistence.NewTourTelemetryRepository(db)

	// Auto-outfit coordinator (sp-buyd): the standing guarded auto-outfit coordinator — the
	// module analogue of the growth coordinator's hull-buying. Each tick it measures per-hull cargo
	// saturation from tour_leg_telemetry, catalogs available modules off the market cache,
	// and installs the highest-marginal-value (hull, module) upgrade behind a fail-closed
	// money/ceiling/cap guard stack. REGISTRATION ONLY — the coordinator is deliberately NOT
	// boot-standing-armed (deploy-inert): it runs only when explicitly started via
	// `workflow auto-outfit`, then survives restarts through the persisted-container recovery
	// idiom. Live-tunable via `tune --operation autooutfit`.
	autoOutfitHandler := grpc.NewAutoOutfitCoordinatorHandler(
		apiClient, shipRepo, tourTelemetryRepo, marketRepo, med, captainEventRepo, containerRepo,
	)
	if err := mediator.RegisterHandler[*autooutfitCmd.RunAutoOutfitCoordinatorCommand](med, autoOutfitHandler); err != nil {
		return fmt.Errorf("failed to register AutoOutfitCoordinator handler: %w", err)
	}

	// Arb-run coordinator: a one-shot, captain-directed, guarded arbitrage run
	// (buy@source → cross-gate → sell@dest, ONCE, capped + floor-guarded). Wired with the
	// same ports as trade-route so its buy/sell/navigate legs resolve to the identical
	// daemon handlers (RouteExecutor-backed travel); marketScanner drives the pre-buy
	// live source-market refresh and apiClient the working-capital spend floor.
	// DaemonServer.StartArbRun launches the container.
	arbCoordinatorHandler := tradeRouteCmd.NewRunArbCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
	)
	circuits.configureArbCoordinator(arbCoordinatorHandler)
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
	// Arm the long-haul sink-depth consult (sp-kw2em). Its predecessor setter had ZERO call
	// sites for its entire lifetime, so the clamp was consumed, unit-tested, and never once
	// applied to a real buy. absorptionLedger is the SAME instance every other engine consults,
	// so long-haul now sees their in-flight units against a shared sink instead of sizing as
	// though it were the only trader on the lane.
	longHaulWorkerHandler.SetAbsorptionLedger(absorptionLedger)

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
	// stalled these hulls is gone too (plan-cheap-verify + pathfind deadline).
	longHaulCoordinatorHandler.SetTourLiveness(daemonServer)
	longHaulCoordinatorHandler.SetTourStopper(daemonServer)
	longHaulCoordinatorHandler.SetAbsorptionReclaimer(absorptionReclaimer)
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
		med, shipRepo, marketRepo, waypointRepo, tourTelemetryRepo,
		routingClient, marketScanner, nil, apiClient,
	)
	marketFreshness := circuits.configureTourCoordinator(tourCoordinatorHandler)
	tourCoordinatorHandler.SetPurchaseObligationReader(transactionRepo)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunTourCoordinatorCommand](med, tourCoordinatorHandler); err != nil {
		return fmt.Errorf("failed to register TourCoordinator handler: %w", err)
	}

	// Opportunity relocator (sp-zvywu Part 2): the standing reconciler that ranks every (trade hull,
	// reachable region) pair by relocation NPV and moves the best-valued hulls onto better-earning
	// ground. It is the rate-floor rescue's trigger INVERTED — that one rescues a hull that is rotting,
	// this one chases upside a perfectly-profitable hull would otherwise never leave for (measured
	// 2026-07-30, only 116 of 1,183 charted systems carry any market data, so most reachable ground goes
	// unworked). It NEVER SPENDS: hulls move through the same travel primitive the two existing
	// relocation triggers use, and no money guard is read or relaxed (RULINGS #4).
	//
	// The REGION OBSERVER is tourCoordinatorHandler itself (ObserveRegions): pricing a candidate ground
	// means running the tour solver's pre-flight, and a second implementation of that is how a third
	// relocation trigger starts disagreeing with the other two about what a ground is worth. Everything
	// else is a thin adapter in opportunity_relocator_ports.go. The travel actuator rides
	// tradeRouteCoordinatorHandler's RepositionToWaypointWithinJumps — the SAME stored-adjacency
	// movement primitive the margins-death and rate-floor relocations commit their jumps through
	// (a relocation is a MOVEMENT of the hull, not a commitment of money).
	//
	// Wired UNCONDITIONALLY: no config key, no default-off, no arming step. Registering the handler is
	// what makes a launched or restart-recovered relocator container runnable; the operator stop is the
	// SHARED reposition_disabled kill-switch, which halts all three relocation triggers at once.
	opportunityRelocatorHandler := tradeRouteCmd.NewRunOpportunityRelocatorHandler(
		grpc.NewRelocatorFleetObserver(shipRepo, containerRepo),
		tourCoordinatorHandler,
		grpc.NewRelocatorTelemetryObserver(tourTelemetryRepo),
		grpc.NewRelocatorEraHorizon(),
		grpc.NewRelocatorActuator(tradeRouteCoordinatorHandler),
		grpc.NewRelocationIntentConfigStore(containerRepo),
		nil, // travel-hop model: nil → the ARMED fitted affine model (SetTravelHopModel is the refit seam)
		nil, // clock: nil → the real clock
	)
	// The SAME activity-conditioned freshness table the tour snapshot builder and the lane ranker drop
	// stale rows against, so the relocator excludes a stale region on one config-resolved definition
	// rather than a fourth copy of it.
	opportunityRelocatorHandler.SetRankerAgeCaps(cfg.Trading.RankerAgeCapMinutes.Resolved())
	// Stall escalation: the relocator reports PROGRESS / IDLE / BLOCKED(reason) once per
	// tick, and a block sustained on one reason for health.StallEscalationTicks consecutive ticks raises
	// a coordinator.stalled captain event beside a Prometheus escalation counter.
	//
	// It shipped without this, which is why the claim race could eat 3 of its first 4 decisions in
	// silence: a relocator losing every decision and one with nothing to do both produce a quiet log,
	// so "is it working?" had to be answered by hand-joining daemon.log against the containers table.
	// BLOCKED here means a relocation was LICENSED and could not be carried out — never merely that
	// nothing was worth moving for, which is the common and correct case on a settled fleet and must
	// stay silent forever. Write-only by type, so no relocation decision can read the streak
	// (RULINGS #2).
	opportunityRelocatorHandler.SetStallObserver(health.NewStallEscalator(metricsAdapter.NewStallMetricsPort(), captainEventRepo))
	// Counters: per-tick verdict, per-hull decision, and the per-reason skip counts the tick
	// always computed and always discarded. A RELOCATOR-SPECIFIC series, not the tour's — two
	// hull-relocating engines already keep separate ones "so the two engines' telemetry never conflate"
	// (adapters/metrics/tour_metrics.go), and this reconciler's reason vocabulary has no overlap with
	// the tour's success|failed|no_candidate anyway. Write-only by type, so no relocation decision can
	// read a counter.
	opportunityRelocatorHandler.SetMetricsSink(metricsAdapter.NewRelocatorMetricsPort())
	// Re-levels the NPV's travel model each tick to the measured per-hop jump toll (sp-80mha).
	circuits.configureOpportunityRelocator(opportunityRelocatorHandler)
	if err := mediator.RegisterHandler[*tradeRouteCmd.RunOpportunityRelocatorCommand](med, opportunityRelocatorHandler); err != nil {
		return fmt.Errorf("failed to register OpportunityRelocator handler: %w", err)
	}

	if err := repos.registerGasExtractionHandlers(shipEventBus); err != nil {
		return err
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

	// Warehouse-first construction sourcing: the construction drain WITHDRAWS a gate
	// material from an in-system depot warehouse before buying it at market, so a depot stocker is
	// the sole buyer→warehouse and construction never double-buys the same units (RULINGS #4). It
	// reuses the SAME shared finder + coordinator the contract path uses (one warehouse-query brain,
	// not a divergent parallel one) and the construction executor as the warehouse-leg navigator.
	// The same StorageRecoveryService that repopulates the coordinator on restart makes this
	// restart-safe (RULINGS #2). Byte-identical when no depot warehouse holds the material — so it is
	// arm-safe to deploy before the reconciler half emits gate-depot demand.
	constructionCoordinatorHandler.SetInventorySource(contractInventoryFinder, storageCoordinator, apiClient, constructionExecutor)

	// The in-memory storage coordinator is populated only by live
	// deposits, so on daemon restart it starts EMPTY and the inventory-first path
	// wired just above sees 0 available — contracts market-buy goods already
	// standing in the warehouse. Wire the StorageRecoveryService into daemon boot
	// so it reloads each running storage operation's ships from the API and
	// re-registers them with THIS SAME shared coordinator + operation repo (the
	// exact singletons the finder above reads — not a second instance). Invoked in
	// DaemonServer.Start AFTER container recovery; idempotent + fail-open.
	daemonServer.SetStorageRecovery(storageApp.NewStorageRecoveryService(storageOperationRepo, apiClient, storageCoordinator))

	// Emit a structured event on each warehouse→hauler buffer draw so
	// warehouse ROI (buffer hit-rate, served-from-buffer, contract-leg-avoided) is
	// measurable. The GORM recorder persists to warehouse_withdrawals; nil clock =
	// RealClock. Additive/fail-open — a record error never fails the draw.
	// apiClient is the server-truth contract reader (sp-20eyn): every workflow pass reconciles
	// the local delivery counts against GET /my/contracts/{id} before planning anything, so a
	// row that lagged behind a delivery the server already accepted cannot drive a second
	// delivery of the same load — and a contract already delivered in full is fulfilled instead
	// of dispatching a hull at it.
	contractWorkflowHandler := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, apiClient, nil,
		contractCmd.WithInventorySourcing(contractInventoryFinder, storageCoordinator, apiClient),
		contractCmd.WithWithdrawalRecording(persistence.NewWithdrawalEventRepository(db), nil),
		// The SAME cap the construction executor holds (sp-ps2oc acceptance 4): construction
		// and contract draw on one treasury, so a contract source-buy must serialise against
		// an in-flight construction_supply buy, not merely against other contract buys. Each
		// operation still checks that one in-flight total against ITS OWN floor, preserving
		// the deliberate contract-exclusive 50k-150k band.
		contractCmd.WithConcurrentSpendCap(concurrentSpendCap))
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
	prePositioning := cfg.Contract.PrePositioning
	demandMiner := persistence.NewDemandMiner(db)
	depositCandidates := tradingSvc.DepositCandidateConfig{
		Enabled:              prePositioning.Enabled,
		TopN:                 prePositioning.TopN,
		MinRecurrence:        prePositioning.MinRecurrence,
		MinSavingsPerUnit:    prePositioning.MinSavingsPerUnit,
		BuyLegSavingsPerUnit: prePositioning.BuyLegSavingsPerUnit,
		Allowlist:            prePositioning.Allowlist,
		Blocklist:            prePositioning.Blocklist,
	}
	tourCoordinatorHandler.SetPrePositioning(
		storageCoordinator,
		storageOperationRepo,
		demandMiner,
		depositCandidates,
		prePositioning.CapitalCeilingPct,
	)

	// Stocker coordinator: a dedicated hull that fills the home warehouse the
	// tours rationally won't (sp-dchv — deposit legs lose to direct sells at every re-plan;
	// the stocker dedicates capacity instead of distorting tour objectives). Wired with the
	// same ports as tour/arb/trade-route (so its buy/navigate legs resolve to the identical
	// RouteExecutor-backed daemon handlers, and it inherits the shared gate graph for
	// multi-jump travel + the arrival event bus for the resume-safe in-transit wait) PLUS
	// the shared storage coordinator (deposit protocol + warehouse reads), the warehouse-op
	// finder (storageOperationRepo), and the Lane A demand miner (over the same db). The
	// pre-positioning economics (min-recurrence/min-savings/allow-block/ceiling-pct) come
	// from the same cfg.Contract.PrePositioning the tour reads; the stocker is launched
	// explicitly (a dedicated hull), so it runs its economics regardless of prePositioning.Enabled (the
	// tour's opportunistic-deposit switch). DaemonServer.StartStocker launches the container.
	stockerCoordinatorHandler := tradeRouteCmd.NewRunStockerCoordinatorHandler(
		med, shipRepo, marketRepo, marketScanner, nil, apiClient,
		storageCoordinator, storageOperationRepo, demandMiner,
		depositCandidates,
		prePositioning.CapitalCeilingPct,
		waypointRepo, // Cache-only coords for the distance-aware residual buy-leg (fail-open)
	)
	circuits.configureStockerCoordinator(stockerCoordinatorHandler, marketFreshness)
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
