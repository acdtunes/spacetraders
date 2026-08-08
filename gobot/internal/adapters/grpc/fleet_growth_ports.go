package grpc

import (
	"context"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// THE DEMONSTRATED-CAPACITY PORT IS THE LEDGER'S NARROW SIDE PORT, and these two lines are what
// hold that. The ruled property — that the regime is a function of the fleet rather than of where
// in a trade cycle the tick landed — is carried by nothing but WHICH port lands in this field: a
// live-balance read wired here flaps exactly as the live-treasury form did, and every test of the
// predicate still passes. The peak-over-window contract itself is proven where it is implemented.
var (
	_ fleetCmd.TreasuryHighWaterReader = (ledger.TreasuryHighWaterReader)(nil)
	_ ledger.TreasuryHighWaterReader   = (*persistence.GormTransactionRepository)(nil)
)

// growthHighWaterPort narrows the transaction repository to the ONE port the reachability clause
// may judge. It is a named seam rather than an inline argument so that substituting a point read
// here is a visible edit to a documented contract instead of a plausible-looking one-word change.
func growthHighWaterPort(txns *persistence.GormTransactionRepository) ledger.TreasuryHighWaterReader {
	return txns
}

// NewFleetGrowthCoordinatorHandler assembles the fleet-growth coordinator, wiring every concrete
// port to the daemon's live collaborators.
//
// IT REUSES THE AUTOSIZER'S PORTS RATHER THAN RE-IMPLEMENTING THEM: the treasury read, the
// API-utilization read, the shipyard price walk, the heavy census, the heavy-target read, the
// heavy-yard catalogue, the pricing errand, the buy+dedicate purchaser and the captain notice are
// all the same types the autosizer drives. A second implementation of any of them is a second
// money-guard opinion, which is the drift the shared guard stack exists to prevent.
//
// The unserved-lane reader is passed IN rather than constructed here: it is the one instance the
// sensing wave gate also reads, and two readers of one quantity is how the spender and the
// withholder end up disagreeing about whether the fleet is capacity-short.
func NewFleetGrowthCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	ledgerTreasury *persistence.LedgerTreasury,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	eventStore captain.EventStore,
	scannedYards scannedYardRanker,
	heavyYards heavyYardInventory,
	lanes *tradingQueries.UnservedLaneReader,
	txns *persistence.GormTransactionRepository,
) *fleetCmd.RunFleetGrowthCoordinatorHandler {
	h := fleetCmd.NewRunFleetGrowthCoordinatorHandler(nil)

	h.SetTreasuryReader(&fleetTreasuryReader{api: apiClient, ledger: ledgerTreasury})
	// The tracker is RESOLVED PER READ, not captured here: passing the resolver itself — the
	// function, uncalled — is what makes the wiring order stop mattering.
	h.SetAPIUtilizationReader(&fleetAPIUtilReader{resolve: globalAPIBudgetReporter})
	// The concrete waypoint repo is assigned only when non-nil: a typed-nil pointer inside the
	// interface field would defeat the reader's nil guard with a runtime panic instead.
	yardPriceReader := &fleetYardPriceReader{med: med, shipRepo: shipRepo, scannedYards: scannedYards}
	if waypointRepo != nil {
		yardPriceReader.waypointRepo = waypointRepo
	}
	h.SetYardPriceReader(yardPriceReader)

	// The heavy-hull census and the heavy-target read: together they derive the heavy reservation
	// each tick and feed the heavy_cap guard. BOTH fail closed when unwired, so an unwired census
	// STOPS heavy buying — hence the loud WARN rather than a silent skip.
	if counter, ok := shipRepo.(heavyHullCounter); ok {
		h.SetHeavyCensusReader(&fleetHeavyCensus{counter: counter})
	} else {
		log.Printf("WARNING: fleet growth heavy census UNWIRED — the ship repository does not implement CountHeavyHulls, so the heavy_cap guard fails closed and NO heavy will be bought")
	}
	if heavyYards != nil {
		h.SetHeavyYardReader(&fleetHeavyYardReader{yards: heavyYards})
	} else {
		log.Printf("WARNING: fleet growth heavy-yard reader UNWIRED — no heavy target can be priced, so the wave can never turn HEAVY and no heavy is ever bought")
	}
	// The heavy-yard PRICING ERRAND. A shipyard prices its hulls only while a ship stands there, so
	// a heavy yard discovered without presence carries an availability-only row and can never feed
	// the reservation. Unwired ⇒ no errand, and such a yard stays unpriced forever.
	if scannedYards != nil {
		if ranker, ok := scannedYards.(heavyYardRanker); ok {
			h.SetHeavyYardCatalogReader(&fleetHeavyYardCatalog{ranker: ranker, shipRepo: shipRepo})
			// The errand draws its carrier from the PARKED SENSING pool, which it SHARES with the
			// scout coordinator — so it must also see which probes a live scout post already owns.
			// The constructor takes the server and reads the roster off its connection, which is
			// what keeps that read from being something this line can omit.
			h.SetHeavyPricingErrandPort(newHeavyYardPricingErrand(server, med, shipRepo))
		} else {
			log.Printf("WARNING: fleet growth heavy-yard pricing errand UNWIRED — the yard ranker cannot list availability-only rows, so a known-but-unpriced heavy yard is never priced and no heavy reservation can form")
		}
	}

	// THE CAPACITY-SHORT SIGNAL, shared with the sensing wave gate. Unwired ⇒ the wave is PROBE and
	// nothing is bought, which is the release direction and spends nothing.
	if lanes != nil {
		h.SetUnservedLaneReader(lanes)
	} else {
		log.Printf("WARNING: fleet growth unserved-lane reader UNWIRED — the capacity-short signal is blind, so the wave is permanently PROBE and no heavy will be bought")
	}
	h.SetTradeHullCounter(&growthTradeHullCounter{shipRepo: shipRepo})

	if txns != nil {
		// The two ledger observations, over the ONE shared trailing window.
		h.SetTreasuryHighWaterReader(growthHighWaterPort(txns))
		h.SetCargoOutflowReader(parkedsensing.NewCargoSpendPort(txns))
	} else {
		log.Printf("WARNING: fleet growth ledger reads UNWIRED — demonstrated capacity is unreadable (the wave is permanently PROBE) and the working-capital term fails closed, so no heavy will be bought")
	}

	// growth_enabled and heavy_cap are the live-tunable knobs (Pattern-C hot reload).
	h.SetGrowthConfigReader(NewContainerConfigReader(server.containerRepo))
	h.SetPurchaser(&fleetHullPurchaser{med: med, shipRepo: shipRepo})
	h.SetPurchaseNotifier(&fleetPurchaseNotifier{store: eventStore})
	h.SetMetricsSink(&growthMetricsSink{})
	// Stall escalation: a coordinator that refuses every tick must not look identical to one with
	// nothing to do. Write-only by type — the streak it accumulates is unreadable by any growth
	// decision (RULINGS #2).
	h.SetStallObserver(health.NewStallEscalator(metrics.NewStallMetricsPort(), eventStore))
	return h
}

// growthTradeHullCounter counts the trade-DEDICATED pool. It is deliberately NOT the broad heavy
// census: the working-capital hold-fill term asks what the TRADING fleet is about to spend, and a
// heavy tagged elsewhere is not flying a lane.
type growthTradeHullCounter struct {
	shipRepo navigation.ShipRepository
}

func (c *growthTradeHullCounter) TradeHulls(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, c.shipRepo, playerID, func(sh *navigation.Ship) bool {
		return sh.DedicatedFleet() == growthTradeFleetTag
	})
}

// growthTradeFleetTag is the dedication a hull carries when it belongs to the trade-tour pool.
const growthTradeFleetTag = "trade"

// growthMetricsSink adapts the coordinator's MetricsSink to the global collector's Record funcs.
type growthMetricsSink struct{}

func (m *growthMetricsSink) RecordWave(playerID string, wave common.Wave, reason common.WaveProbeReason) {
	metrics.RecordFleetGrowthWave(playerID, metrics.WaveReaderGrowth, wave == common.WaveHeavy, string(reason))
}

func (m *growthMetricsSink) RecordGrowthEnabled(playerID string, enabled bool) {
	metrics.RecordGrowthEnabled(playerID, enabled)
}

func (m *growthMetricsSink) RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int) {
	metrics.RecordGrowthHeavyReserve(playerID, reserve, target, owned, capacity)
}

func (m *growthMetricsSink) RecordWorkingCapital(playerID string, terms fleetgrowth.WorkingCapitalTerms) {
	metrics.RecordGrowthWorkingCapital(playerID, terms)
}

func (m *growthMetricsSink) RecordDemand(class fleetCmd.HullClass, demand, current int) {
	metrics.RecordGrowthDemand(string(class), demand, current)
}

func (m *growthMetricsSink) RecordPurchase(class fleetCmd.HullClass) {
	metrics.RecordGrowthPurchase(string(class))
}

func (m *growthMetricsSink) RecordBlocked(class fleetCmd.HullClass, guard fleetCmd.GuardName) {
	metrics.RecordGrowthBlocked(string(class), string(guard))
}

func (m *growthMetricsSink) RecordZeroEffectAlarm() {
	metrics.RecordGrowthZeroEffectAlarm()
}

func (m *growthMetricsSink) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	metrics.ObserveGrowthHeavyPricePremium(playerID, paid, cheapestKnown)
}
