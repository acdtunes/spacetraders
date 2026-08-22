package grpc

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

const (
	// commandRole is the flagship's registration role — its system is the cold-start home system.
	commandRole = "COMMAND"
	// marketplaceTrait / shipyardTrait are the waypoint traits the home-tour + price reads filter on.
	marketplaceTrait = "MARKETPLACE"
	shipyardTrait    = "SHIPYARD"
	// bootstrapMarketFreshnessMin bounds how old a market's data may be and still count as
	// "covered" for the heartbeat coverage reading. Generous (24h) because markets are actively scouted
	// during bootstrap, so coverage measures BREADTH (how many marketplaces have data), not
	// staleness. A tighter freshness window is a later refinement.
	bootstrapMarketFreshnessMin = 24 * 60
	// contractFleetTag is the dedicated-fleet tag the contract coordinator's dedicated pool selects on
	// (matches the contract package's dedicatedFleetContract). A hauler carrying it is adopted as a
	// contract worker (and puts the pool in exclusive mode, dropping the untagged frigate); the frigate
	// retire clears it. The income window (1h) is the trailing span the realized-$/hr read averages.
	contractFleetTag = "contract"
	// tradeFleetTag is the dedicated-fleet tag the standing trade-fleet coordinator selects on (matches the
	// trading package's tradeFleet and the autosizer's trade count). obs.TradeHullCount counts hulls carrying
	// it — the observable "trade-seeded" signal that drives the trade-seed + the scaler
	// delay-launch. The bootstrap trade-seed (BuyAndDedicate) is what stamps a bought hull with this tag.
	tradeFleetTag = "trade"
	// warehouseFleetTag / stockerFleetTag are the dedicated-fleet tags the contract auto-scaler stamps on the
	// DEPOT half of the contract fleet — the central far-source warehouse hulls and the stocker
	// (container_ops_depot_launch.go). obs.ContractDepotHullCount counts hulls carrying either, mirroring how
	// obs.Haulers/TradeHullCount count by DedicatedFleet() tag; the delivery Haulers + this depot count are
	// the FULL contract fleet the sp-gm7r GATE-entry bar measures against the scaler's target.
	warehouseFleetTag     = "warehouse"
	stockerFleetTag       = "stocker"
	bootstrapIncomeWindow = time.Hour
)

// NewBootstrapCoordinatorHandler assembles the bootstrap reconciler (sp-3nbe M4), wiring every
// concrete port to the daemon's live collaborators. LIVE BY DEFAULT once first-launched; recovery
// -adopted on restart. server drives the scout-all-markets assignment; apiClient reads treasury;
// shipRepo backs the phantom-cache refresh + fleet observation; med runs the price-check + buy;
// waypointRepo + marketRepo back the market-coverage read.
func NewBootstrapCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	marketRepo *persistence.MarketRepositoryAdapter,
) *bootstrapCmd.RunBootstrapCoordinatorHandler {
	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapRefresher{shipRepo: shipRepo})
	h.SetWorldObserver(&bootstrapObserver{
		api: apiClient, shipRepo: shipRepo, waypointRepo: waypointRepo, marketRepo: marketRepo,
		med: med, containerRepo: server.containerRepo, server: server,
		eraRepo: persistence.NewEraRepository(server.db),
		// The ramp places hulls on the SAME fixed delivery slots the contract coordinator homes them to —
		// one slot set for every positioning consumer, resolved from stationary home-system geometry.
		placement: NewContractStandbyPlacementProvider(shipRepo, waypointRepo, marketRepo),
	})
	// One acquirer instance drives both the probe buy and (embedded) the hauler price-check
	// + buy — the yard price-scan + batch-purchase plumbing is asset-agnostic (parameterised by shipType).
	// savedYards is where a yard's ask outlives the process that read it: the same era-scoped shipyard
	// inventory every scout scan writes, so a cold yard is still weighed against real evidence after a
	// daemon restart (RULINGS #2).
	acq := &bootstrapAcquirer{
		med: med, shipRepo: shipRepo, waypointRepo: waypointRepo,
		savedYards: persistence.NewShipyardInventoryRepository(server.db),
	}
	h.SetProbeAcquirer(acq)
	h.SetHaulerAcquirer(&bootstrapHaulerAcquirer{bootstrapAcquirer: acq})
	// The cold-start shipyard-readability scanner. On a fresh universe nothing has visited the home
	// shipyard, so its live (presence-gated) price is unreadable and the buy fails closed forever; this
	// flies a hull to the yard so the next tick's live PriceCheck reads. Same deps as the acquirer
	// (mediator navigate + ship/waypoint repos) — builds nothing new.
	// The cold-start market tour: bootstrap starts it through the operator's own verb.
	h.SetHomeTourStarter(&bootstrapHomeTourStarter{server: server})
	h.SetShipyardScanner(&bootstrapShipyardScanner{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo})
	h.SetFrigateRetirer(&bootstrapFrigateRetirer{shipRepo: shipRepo})
	h.SetContractRunner(&bootstrapContractRunner{server: server})
	// The frigate contract-loop primitive. Bootstrap drives only its STOP half, to clear a loop container
	// an earlier deploy left holding the frigate's claim.
	h.SetFrigateContractLoopStarter(&bootstrapFrigateContractLoop{server: server})
	h.SetMetricsSink(&bootstrapMetricsSink{})
	// The per-tick live-config reader makes every bootstrap knob honor
	// `spacetraders tune --operation bootstrap` on the next reconcile with no restart. Reads the
	// same persisted config column the tune verb writes (ContainerConfigReader).
	h.SetLiveConfigReader(NewContainerConfigReader(server.containerRepo))

	// GATE-phase collaborators (Slice 3): construction start, the manufacturing-executor ensure/bounce,
	// the repurpose-to-manufacturing re-tag, the gate-worker buy, and the COMPLETE hand-off — each a thin
	// wrapper over an existing daemon capability (build nothing new).
	h.SetConstructionManager(&bootstrapConstructionManager{server: server})
	h.SetManufacturingController(&bootstrapManufacturingController{server: server})
	h.SetWorkerRepurposer(&bootstrapWorkerRepurposer{shipRepo: shipRepo})
	// Un-dedicate surplus idle gate workers → idle pool so the contract scaler adopts them (zero buys).
	h.SetGateSurplusReleaser(&bootstrapGateSurplusReleaser{shipRepo: shipRepo})
	h.SetGateWorkerAcquirer(&bootstrapGateWorkerAcquirer{bootstrapAcquirer: acq, shipRepo: shipRepo})
	h.SetHandoffLauncher(&bootstrapHandoffLauncher{server: server})
	h.SetPendingScalingReservationPublisher(persistence.NewPendingScalingReservationRepository(server.db))
	return h
}

// bootstrapMetricsSink adapts to the global bootstrap collector: pure observation, nil-safe.
type bootstrapMetricsSink struct{}

func (m *bootstrapMetricsSink) RecordPhase(phase string, playerID string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordPhase(phase, playerID)
	}
}

func (m *bootstrapMetricsSink) RecordProbePurchased(playerID string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordProbePurchased(playerID)
	}
}

func (m *bootstrapMetricsSink) RecordHaulerPurchased(playerID string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordHaulerPurchased(playerID)
	}
}

func (m *bootstrapMetricsSink) RecordConstructionPct(pct float64, playerID string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordConstructionPct(pct, playerID)
	}
}
