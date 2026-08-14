package main

import (
	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ship "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradeRouteCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"gorm.io/gorm"
)

// circuitWiring is the one-instance-each collaborator set the four gate-crossing trade engines share.
type circuitWiring struct {
	cfg              *config.Config
	db               *gorm.DB
	containerRepo    *persistence.ContainerRepositoryGORM
	transactionRepo  *persistence.GormTransactionRepository
	marketRepo       *persistence.MarketRepositoryGORM
	captainEventRepo *persistence.GormCaptainEventRepository
	marketScanner    *ship.MarketScanner
	shipEventBus     *ship.ShipEventBus

	gateGraph         *gategraph.Service
	treasury          *persistence.LedgerTreasury
	absorption        *persistence.AbsorptionLedgerGORM
	laneCooldown      *domainTrading.LaneCooldownLedger
	capitalWorkSensor *common.EngineCapitalWorkSensor

	// Charts every jump gate a hull lands on, the one moment its outbound edges are
	// readable, so a market-swept frontier system never strands hulls on empty
	// gate_edges. Default ON ([routing] chart_gate_on_arrival); shared across every
	// cross-system engine below.
	chartGateOnArrival bool
}

func (w circuitWiring) configureTradeRouteCoordinator(h *tradeRouteCmd.RunTradeRouteCoordinatorHandler) {
	h.SetGateGraph(w.gateGraph)
	// The working-capital spend floor reads treasury through the shared ledger-backed
	// reader; an unreadable treasury still aborts the circuit (fail-closed).
	h.SetTreasuryReader(w.treasury)
	h.SetChartGateOnArrival(w.chartGateOnArrival)
	// Lets travel() wait out a hull re-adopted mid-transit before any movement,
	// instead of erroring on a routine arrival.
	h.SetEventSubscriber(w.shipEventBus)
	// Read-only absorption consult: scanLanes excludes a lane whose sell side is
	// shadowed or whose reserved depth can't absorb a circuit tranche.
	h.SetAbsorptionLedger(w.absorption)
	// Ranks on the effective spread: snapshot less this hull's own self-compression
	// plus the live shared cooldown debt; runCircuit accrues each leg's debt back.
	h.SetLaneImpactModel(
		w.cfg.TradeImpact.ResolvedBuyImpact(),
		w.cfg.TradeImpact.ResolvedSellImpact(),
		w.laneCooldown,
	)
	// Activity-conditioned ranker freshness caps for the undirected auto-scan.
	h.SetRankerAgeCaps(w.cfg.Trading.RankerAgeCapMinutes.Resolved())
	// The scan-dedup A/B test's live, operator-settable allowlist. Defaults empty
	// (every ship disarmed, unchanged behavior); an operator arms a hull via
	// `fleet scan-dedup add`, no daemon restart.
	h.SetScanDedupAllowlist(persistence.NewScanDedupAllowlistGORM(w.db))
}

func (w circuitWiring) configureArbCoordinator(h *tradeRouteCmd.RunArbCoordinatorHandler) {
	h.SetGateGraph(w.gateGraph) // multi-jump travel + the routability-check-before-spend guard
	h.SetChartGateOnArrival(w.chartGateOnArrival)
	h.SetTreasuryReader(w.treasury)
	// Waits out a mid-transit re-adoption before the resume path's jump.
	h.SetEventSubscriber(w.shipEventBus)
	// Durably records a fresh buy's cost so a restart-rebuilt resume reports honest P&L.
	h.SetCostPersister(grpc.NewArbCostConfigPersister(w.containerRepo))
	// Converts a PLANNED absorption hold into an EXECUTED recovery shadow at sale
	// completion (shared ledger instance).
	h.SetAbsorptionLedger(w.absorption)
}

func (w circuitWiring) configureTourCoordinator(h *tradeRouteCmd.RunTourCoordinatorHandler) *tradeRouteCmd.MarketFreshness {
	h.SetGateGraph(w.gateGraph)
	h.SetTreasuryReader(w.treasury)
	// Prices each crossing's first hop from the gate it actually departs (learned from
	// the ledger's recorded jumps) rather than a fleet-wide constant; an empty/unreadable
	// ledger prices exactly as before.
	h.SetGateFeeReader(
		tradeRouteCmd.NewLedgerGateFeeReader(w.transactionRepo, nil), // nil clock = RealClock
	)
	h.SetChartGateOnArrival(w.chartGateOnArrival)
	// Lets the tour see profitable exotic lanes whose sink sits beyond the tour graph's
	// own hop horizon.
	h.SetOutOfHorizonSinkScanner(w.marketRepo)
	// Absolute artifact path: the launchd daemon's cwd is not the repo root.
	h.SetModelArtifactPath(w.cfg.Routing.ModelArtifactPath)
	tourRepositionPersister := grpc.NewTourRepositionConfigPersister(w.containerRepo)
	// Durably records an in-flight margins-death reposition so a restart-rebuilt resume
	// continues toward the same ground instead of re-planning mid-hop.
	h.SetRepositionPersister(tourRepositionPersister)
	// Records the relocation offer a tour writes at its boundary, letting the relocator
	// claim an idle hull before the tour re-anchors it locally; unwired, no offer is
	// written and the fleet tours exactly as today.
	h.SetRelocationOfferPersister(tourRepositionPersister)
	// Shared absorption ledger: the tour reserves its planned tranches, nets outstanding
	// depth into each plan, and converts sold sinks into recovery shadows.
	h.SetAbsorptionLedger(w.absorption, w.cfg.Absorption.PlannedTTLSlack)
	h.SetEventRecorder(w.captainEventRepo) // coordinator error-loop event when the dynamic-budget resolve stays unreadable
	// Blocks low-value noise goods from tour cargo selection; absent/empty is byte-identical.
	h.SetCargoBlocklist(w.cfg.TradeFleet.CargoBlocklist)
	// Samples the deliberate price-impact instrumentation instead of scanning every
	// market around every trade.
	h.SetScanPolicy(w.cfg.TradeImpact.ResolvedScanPolicy())
	// Same activity-conditioned freshness caps as the lane ranker, one config-resolved table.
	h.SetRankerAgeCaps(w.cfg.Trading.RankerAgeCapMinutes.Resolved())
	// Arms the firm-sink buy gate's FRESH clause: at execution it refuses a held sink
	// whose live market_data is older than this floor.
	h.SetSinkFreshness(w.cfg.TradeFleet.ResolvedSinkFreshnessMaxAge())
	// Derives every market-freshness cap on the trade path from the live scan rotation
	// rather than a fixed minute count, so the cap tracks the charted map instead of
	// discarding rows a growing map made merely due. Floors stay live-tunable with no
	// restart.
	marketFreshness := tradeRouteCmd.NewMarketFreshness(
		w.marketScanner.ScanBudget(),
		grpc.NewCoordinatorConfigReader(w.containerRepo, string(domainContainer.ContainerTypeTradeFleetCoordinator)),
		nil, // nil clock = RealClock
	)
	h.SetMarketFreshness(marketFreshness)
	// Trade's share of the per-operation capital budget; degrades gracefully to the
	// whole pool whenever the construction drain is not running.
	h.SetCapitalWorkSensor(w.capitalWorkSensor)
	return marketFreshness
}

func (w circuitWiring) configureStockerCoordinator(
	h *tradeRouteCmd.RunStockerCoordinatorHandler,
	marketFreshness *tradeRouteCmd.MarketFreshness,
) {
	h.SetGateGraph(w.gateGraph)
	h.SetChartGateOnArrival(w.chartGateOnArrival)
	h.SetTreasuryReader(w.treasury)
	h.SetEventSubscriber(w.shipEventBus)
	// Emits a structured stock-IN event on each confirmed deposit for depot
	// throughput/coverage analysis; fail-open, never blocks a deposit.
	h.SetStockingRecorder(persistence.NewStockingEventRepository(w.db))
	// Shares the tour's rotation-derived freshness cap for refill sourcing rather than
	// carrying a second copy that could drift.
	h.SetMarketFreshness(marketFreshness)
}
