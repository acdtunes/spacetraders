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

	// Chart every jump gate a hull lands on (the one moment its outbound edges are
	// readable — a remote read with no ship present 400s) so a market-swept frontier system
	// never strands hulls on empty gate_edges. Default ON; [routing] chart_gate_on_arrival
	// (nil => on) is the reversibility switch. Wired on the SHARED trade-route instance
	// (trade circuits + scout reposition + worker ferry + route-ship) and delegated to the
	// arb/tour/stocker legs, so ALL cross-system gate arrivals chart. Best-effort +
	// idempotent: no new burst.
	chartGateOnArrival bool
}

func (w circuitWiring) configureTradeRouteCoordinator(h *tradeRouteCmd.RunTradeRouteCoordinatorHandler) {
	h.SetGateGraph(w.gateGraph)
	// sp-muq66: the circuit's working-capital spend floor reads treasury through the shared
	// ledger-backed reader instead of a live Get Agent before every buy. Unconditional — no
	// config gate. An unreadable treasury still aborts the circuit (fail-closed, RULINGS #4).
	h.SetTreasuryReader(w.treasury)
	h.SetChartGateOnArrival(w.chartGateOnArrival)
	// The shared ship-arrival event bus lets travel() wait out a hull
	// re-adopted mid-transit before any movement (jump/navigate) instead of 4214'ing
	// and burning the container restart budget on a routine arrival.
	h.SetEventSubscriber(w.shipEventBus)
	// sp-78ai L4: read-only absorption consult (trade-analyst Q1: "circuits write
	// nothing") — scanLanes excludes a lane whose sell side is shadowed or whose
	// reserved depth can't absorb a circuit tranche. Shares the SAME ledger instance
	// L2 (idle-arb) writes to.
	h.SetAbsorptionLedger(w.absorption)
	// Wire the era-3 price-impact coefficients + the shared cooldown ledger into
	// lane ranking. scanLanes now ranks on the EFFECTIVE spread (snapshot less the
	// self-compression this hull's volume would cause + the live shared cooldown debt), and
	// runCircuit accrues each completed leg's debt back to the shared ledger.
	h.SetLaneImpactModel(
		w.cfg.TradeImpact.ResolvedBuyImpact(),
		w.cfg.TradeImpact.ResolvedSellImpact(),
		w.laneCooldown,
	)
	// Arm the activity-conditioned ranker freshness caps for the undirected
	// auto-scan. Absent [trading] config → the fitted armed defaults; a captain retunes
	// per activity from config.yaml + restart (RULINGS #5).
	h.SetRankerAgeCaps(w.cfg.Trading.RankerAgeCapMinutes.Resolved())
}

func (w circuitWiring) configureArbCoordinator(h *tradeRouteCmd.RunArbCoordinatorHandler) {
	// Same gate graph: enables multi-jump travel AND the routability-check-before-spend
	// guard.
	h.SetGateGraph(w.gateGraph)
	h.SetChartGateOnArrival(w.chartGateOnArrival) // Chart cross-system arrivals
	// The spend-floor guard — and the movement legs' buy-time working-capital floor,
	// which this setter also reaches — read treasury through the shared ledger-backed reader
	// instead of calling Get Agent before every one-shot buy.
	h.SetTreasuryReader(w.treasury)
	// Wait out a mid-transit re-adoption before the resume path's jump, instead of
	// 4214'ing and burning the container restart budget on a routine arrival.
	h.SetEventSubscriber(w.shipEventBus)
	// Durably record a fresh buy's cost into the container config so a
	// restart-rebuilt resume reloads it and reports honest P&L (a resumed run skips the
	// completed buy, which otherwise leaves TotalCost=0 and over-states NetProfit).
	h.SetCostPersister(grpc.NewArbCostConfigPersister(w.containerRepo))
	// sp-78ai L2: convert an arb/idle-arb leg's PLANNED absorption hold into an
	// EXECUTED recovery shadow at sale completion (shared ledger instance).
	h.SetAbsorptionLedger(w.absorption)
}

func (w circuitWiring) configureTourCoordinator(h *tradeRouteCmd.RunTourCoordinatorHandler) *tradeRouteCmd.MarketFreshness {
	h.SetGateGraph(w.gateGraph)
	// sp-muq66: the tour's dynamic 25%-of-treasury max-spend, its pre-positioning capital
	// ceiling, and the movement legs' working-capital floor all read treasury through the
	// shared ledger-backed reader instead of a live Get Agent per check. Unconditional — no
	// config gate. An unreadable treasury still fails CLOSED exactly as before (RULINGS #4).
	h.SetTreasuryReader(w.treasury)
	// Price each crossing's first hop from the gate it actually DEPARTS, learned
	// from the ledger's own recorded jumps, instead of from one fleet-wide constant. The fee
	// is a property of the departure gate (origin explains 99.7% of the variance; the same
	// edge costs 27% more one way than the other), so the flat charge — while unbiased in
	// aggregate — carries a 15.2% error on any individual crossing, which is precisely the
	// error that orders candidates against each other. Unconditional, no config gate: an
	// empty or unreadable ledger yields no table, and no table prices exactly as before.
	h.SetGateFeeReader(
		tradeRouteCmd.NewLedgerGateFeeReader(w.transactionRepo, nil), // nil clock = RealClock
	)
	h.SetChartGateOnArrival(w.chartGateOnArrival) // Chart cross-gate tour arrivals
	// sp-mtvg: wire the global best-sink reader so the tour coordinator can SEE (and count
	// on tour_candidates_dropped_total) the profitable exotic lanes whose sink is beyond the
	// 1-gate-hop tour graph. The raw GORM repo carries BestSinksAcrossSystems; read-only.
	h.SetOutOfHorizonSinkScanner(w.marketRepo)
	// Inject the config-resolved ABSOLUTE artifact path so the executor reads the
	// market model regardless of the daemon's cwd (the launchd daemon's cwd is not the
	// repo root).
	h.SetModelArtifactPath(w.cfg.Routing.ModelArtifactPath)
	tourRepositionPersister := grpc.NewTourRepositionConfigPersister(w.containerRepo)
	// sp-zhii: durably record an in-flight margins-death reposition (its target
	// system+waypoint) into the container config so a restart-rebuilt resume completes the
	// jump toward the same ground instead of re-planning at an intermediate hop (RULINGS #2).
	h.SetRepositionPersister(tourRepositionPersister)
	// sp-e8d92 FIRST REFUSAL: the same config-backed persister also records the relocation OFFER a tour
	// writes at its boundary, which is how the relocator gets a hull BEFORE the tour re-anchors it
	// locally. The fleet occupies 23 of 373 tradeable systems because a tour is planned from wherever the
	// hull stands, so the envelope never leaves its neighbourhood; the relocator is the only thing that
	// moves a hull to new ground and it only ever sees the 2.6% of time a hull is idle. Unwired, no offer
	// is ever written and the fleet tours exactly as it does today (fail-open).
	h.SetRelocationOfferPersister(tourRepositionPersister)
	// sp-78ai L3: wire the SAME absorption ledger the idle-arb/arb engines use so the
	// tour reserves its planned tranches (fleet-wide A-cap), nets outstanding depth into
	// each plan, and converts sold sinks into recovery shadows — the flagship writer/reader
	// of the cross-engine coordination. The shared PlannedTTLSlack sizes reservation
	// lifetimes.
	h.SetAbsorptionLedger(w.absorption, w.cfg.Absorption.PlannedTTLSlack)
	h.SetEventRecorder(w.captainEventRepo) // Emit coordinator error-loop event when the dynamic-budget resolve stays unreadable
	// Inject the noise-goods cargo blocklist (FUEL/ALUMINUM/PLASTICS are sub-70-cr/u
	// tempo drag) so the tour planner never selects a listed good as cargo. Global list from
	// [trade_fleet].cargo_blocklist, mirroring the contract pre_positioning.blocklist boot
	// injection. Absent/empty ⇒ no filtering ⇒ byte-identical; arming = adding goods to
	// config.yaml + daemon restart. Cargo only — refueling never reads the tour snapshot.
	h.SetCargoBlocklist(w.cfg.TradeFleet.CargoBlocklist)
	// Stamp the tour-scan load policy so the shared arrival + post-trade scans
	// SAMPLE the deliberate price-impact instrumentation (the top API consumer, ~80% of
	// API) instead of scanning every market around every trade. Resolved from [trade_impact]
	// config (scan_max_age_seconds / impact_sample_rate; restart to apply — the same
	// refit-per-era path the model's coefficients already use).
	h.SetScanPolicy(w.cfg.TradeImpact.ResolvedScanPolicy())
	// Arm the SAME activity-conditioned freshness caps for the tour snapshot
	// builder, so the tour path and the lane ranker drop stale rows against one
	// config-resolved table (defined once).
	h.SetRankerAgeCaps(w.cfg.Trading.RankerAgeCapMinutes.Resolved())
	// sp-tgll8 item 2: arm the "FRESH" clause on the firm-sink buy gate — at buy execution the
	// gate re-reads each held sink's LIVE market_data and refuses on stale data (older than
	// this). Ships ARMED at the 75-min default. This is the BOOT floor; the effective cap is
	// max(floor, rotation bound) (sp-k4z5b) and the LIVE lever is `tune --operation tour
	// market_data_max_age_minutes`. Byte-identical for fresh sinks.
	h.SetSinkFreshness(w.cfg.TradeFleet.ResolvedSinkFreshnessMaxAge())
	// sp-k4z5b: derive every market-freshness cap on the trade path from the LIVE scan
	// rotation rather than a minute count written into the source. The scan budget is a
	// fixed req/s, so a market's scan interval is an OUTPUT of budget / markets known —
	// and when the charted map reached 4,389 markets the flat 75-minute caps started
	// discarding four fifths of it (980 fail-closed refusals in 15 minutes, ~87% of trade
	// throughput). marketScanner's budget is the SAME allowance admission is decided
	// against, so consumers now refuse exactly when the scanner has failed its own
	// anti-starvation guarantee. The floors under it are live-tunable on the trade fleet
	// coordinator's config column (`spacetraders tune --operation tour ...`) — no restart,
	// because each daemon bounce costs jump cooldowns. Wired UNCONDITIONALLY: no config
	// key, no default-off, no arming step.
	marketFreshness := tradeRouteCmd.NewMarketFreshness(
		w.marketScanner.ScanBudget(),
		grpc.NewCoordinatorConfigReader(w.containerRepo, string(domainContainer.ContainerTypeTradeFleetCoordinator)),
		nil, // nil clock = RealClock
	)
	h.SetMarketFreshness(marketFreshness)
	// The TRADE half of the per-operation capital budget, on the SAME sensor the
	// construction executor holds. The dynamic (--max-spend 0) cap is clamped to trade's share of
	// deployable capital, and graceful degradation hands trade the WHOLE pool whenever the
	// construction drain is not running — so a stopped gate never leaves capital idle. Wired
	// UNCONDITIONALLY: no config key, no default-off, no arming step.
	h.SetCapitalWorkSensor(w.capitalWorkSensor)
	return marketFreshness
}

func (w circuitWiring) configureStockerCoordinator(
	h *tradeRouteCmd.RunStockerCoordinatorHandler,
	marketFreshness *tradeRouteCmd.MarketFreshness,
) {
	h.SetGateGraph(w.gateGraph)
	h.SetChartGateOnArrival(w.chartGateOnArrival) // Chart cross-system haul arrivals
	// The capital ceiling — and the movement legs' buy-time working-capital floor,
	// which this setter also reaches — read treasury through the shared ledger-backed reader
	// instead of calling Get Agent on every stocker pick.
	h.SetTreasuryReader(w.treasury)
	h.SetEventSubscriber(w.shipEventBus)
	// Emit a structured stock-IN event on each CONFIRMED stocker→warehouse deposit so
	// downstream analysis can measure depot throughput/coverage (the stock-IN mirror of the
	// kqxe withdrawal recorder wired above). Additive + fail-open — a record error never fails
	// a deposit.
	h.SetStockingRecorder(persistence.NewStockingEventRepository(w.db))
	// sp-k4z5b: the stocker picks its refill source off the same freshListings filter the
	// tour does, so it shares the tour's rotation-derived cap and its live floor rather
	// than carrying a second copy that could drift.
	h.SetMarketFreshness(marketFreshness)
}
