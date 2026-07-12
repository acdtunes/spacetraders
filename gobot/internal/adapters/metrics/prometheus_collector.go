package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Namespace for all metrics
	namespace = "spacetraders"
	// Subsystem for daemon metrics
	subsystem = "daemon"
)

var (
	// Registry is the global Prometheus registry for all metrics
	Registry *prometheus.Registry

	// globalCollector is the singleton container metrics collector
	// Set by SetGlobalCollector() when metrics are enabled
	globalCollector MetricsRecorder

	// globalNavigationCollector is the singleton navigation metrics collector
	// Set by SetGlobalNavigationCollector() when metrics are enabled
	globalNavigationCollector NavigationMetricsRecorder

	// globalFinancialCollector is the singleton financial metrics collector
	// Set by SetGlobalFinancialCollector() when metrics are enabled
	globalFinancialCollector FinancialMetricsRecorder

	// globalAPICollector is the singleton API metrics collector
	// Set by SetGlobalAPICollector() when metrics are enabled
	globalAPICollector *APIMetricsCollector

	// globalMarketCollector is the singleton market metrics collector
	// Set by SetGlobalMarketCollector() when metrics are enabled
	globalMarketCollector *MarketMetricsCollector

	// globalManufacturingCollector is the singleton manufacturing metrics collector
	// Set by SetGlobalManufacturingCollector() when metrics are enabled
	globalManufacturingCollector *ManufacturingMetricsCollector

	// globalAbsorptionCollector is the singleton absorption burn-in collector (sp-8cz9).
	// Set by SetGlobalAbsorptionCollector() when metrics are enabled; the tour
	// coordinator emits the cap-binding + ladder-incident counters through it.
	globalAbsorptionCollector *AbsorptionMetricsCollector

	// globalTourCollector is the singleton tour instrumentation collector (sp-fbih).
	// Set by SetGlobalTourCollector() when metrics are enabled; the tour coordinator emits
	// the reposition/margins-death/reserve-floor/exit/duration/resolved-cap series through it.
	globalTourCollector *TourMetricsCollector

	// globalTourStalenessCollector is the singleton planner staleness-exclusion
	// collector (sp-k7q5 layer 2). Set by SetGlobalTourStalenessCollector() when
	// metrics are enabled; the tour planner's two staleness drop sites emit the
	// tour_lanes_stale_excluded_total counter through it.
	globalTourStalenessCollector *TourStalenessMetricsCollector

	// globalScoutCollector is the singleton scout metrics collector (sp-dp92 P7).
	// Set by SetGlobalScoutCollector() when metrics are enabled; the scout post
	// coordinator's reconcile sweep emits the market-freshness gauge through it.
	globalScoutCollector *ScoutMetricsCollector

	// globalFleetHealthCollector is the singleton fleet-health collector (sp-686e).
	// Set by SetGlobalFleetHealthCollector() when metrics are enabled; the tour
	// coordinator's reposition exit path emits the stranded-hull counter through it.
	globalFleetHealthCollector *FleetHealthMetricsCollector

	// globalChainPnLCollector is the singleton chain-P&L collector (sp-rh2z). Set by
	// SetGlobalChainPnLCollector() when metrics are enabled; the goods_factory coordinator's
	// kill-switch emits the realized-P&L/hr gauge and the kill-episode counter through it.
	globalChainPnLCollector *ChainPnLMetricsCollector

	// globalChainInputPauseCollector is the singleton input-poison anti-cycle collector
	// (sp-r5a6). Set by SetGlobalChainInputPauseCollector() when metrics are enabled; the
	// goods_factory coordinator emits the input-pause episode counter through it (the INPUT
	// side of the self-pruning portfolio, alongside the chain-P&L kill counter above).
	globalChainInputPauseCollector *ChainInputPauseMetricsCollector

	// globalChainExportRestCollector is the singleton export-ask-subsidy rest collector
	// (sp-xdk6). Set by SetGlobalChainExportRestCollector() when metrics are enabled; the
	// goods_factory coordinator emits the export-rest episode counter through it (the
	// OUTPUT-LADDER side of the self-pruning portfolio, alongside the input-pause and
	// chain-P&L kill counters above).
	globalChainExportRestCollector *ChainExportRestMetricsCollector

	// globalFleetAutosizerCollector is the singleton fleet-autosizer collector (sp-1txd). Set by
	// SetGlobalFleetAutosizerCollector() when metrics are enabled; the autosizer's ACT path emits
	// its purchase/blocked/demand/zero-effect series through the package Record funcs below.
	globalFleetAutosizerCollector *FleetAutosizerMetricsCollector

	// globalBootstrapCollector is the singleton captain-bootstrap collector (sp-3nbe). Set by
	// SetGlobalBootstrapCollector() when metrics are enabled; the bootstrap reconciler emits its
	// derived-phase gauge + probe-purchase counter through it.
	globalBootstrapCollector *BootstrapMetricsCollector

	// globalSitingCollector is the singleton factory-siting collector (sp-vdld). Set by
	// SetGlobalSitingCollector() when metrics are enabled; the siting coordinator's ACT and
	// EMIT steps increment the launch/retire/scout-demand counters through it.
	globalSitingCollector *SitingMetricsCollector

	// globalAPIBudgetTracker is the singleton API request-budget tracker
	// (sp-51ti). Set by SetGlobalAPIBudgetTracker() at daemon startup; the API
	// client falls back to it when no per-instance tracker was injected, the
	// same pattern getMetricsCollector() uses for globalAPICollector.
	globalAPIBudgetTracker *APIBudgetTracker

	// globalDutyCycleSampler is the singleton duty-cycle KPI sampler
	// (sp-51ti captain amendment). Set by SetGlobalDutyCycleSampler() at
	// daemon startup so a future CLI/gRPC read can reach it without a direct
	// reference to the daemon's internal sampler instance.
	globalDutyCycleSampler *DutyCycleSampler
)

// MetricsRecorder defines the interface for recording container metrics events
// This interface is used by domain/application code to record metrics
type MetricsRecorder interface {
	RecordContainerCompletion(containerInfo ContainerInfo)
	RecordContainerRestart(containerInfo ContainerInfo)
	RecordContainerIteration(containerInfo ContainerInfo)
	RecordContainerExit(containerInfo ContainerInfo)
}

// ShipWriteConflictRecorder is implemented by collectors that track the
// sp-60ff ship-row version tripwire. Separate single-method interface so
// existing MetricsRecorder implementations keep compiling.
type ShipWriteConflictRecorder interface {
	RecordShipVersionConflict()
}

// NavigationMetricsRecorder defines the interface for recording navigation metrics
type NavigationMetricsRecorder interface {
	RecordRouteCompletion(playerID int, status navigation.RouteStatus, duration float64, distance int, fuelConsumed int)
	RecordSegmentCompletion(playerID int, distance int, fuelRequired int)
	RecordFuelPurchase(playerID int, waypoint string, units int)
	RecordFuelConsumption(playerID int, flightMode shared.FlightMode, units int)
}

// FinancialMetricsRecorder defines the interface for recording financial metrics
type FinancialMetricsRecorder interface {
	RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int)
	RecordTrade(playerID int, goodSymbol string, buyPrice int, sellPrice int, quantity int)
}

// InitRegistry initializes the Prometheus registry
// Should be called once at application startup if metrics are enabled
func InitRegistry() {
	Registry = prometheus.NewRegistry()
}

// GetRegistry returns the global Prometheus registry
// Returns nil if metrics are not initialized
func GetRegistry() *prometheus.Registry {
	return Registry
}

// IsEnabled returns true if metrics collection is enabled
func IsEnabled() bool {
	return Registry != nil
}

// SetGlobalCollector sets the global metrics collector
// This should be called after the collector is created and started
func SetGlobalCollector(collector MetricsRecorder) {
	globalCollector = collector
}

// RecordContainerCompletion records a container completion event globally
func RecordContainerCompletion(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerCompletion(containerInfo)
	}
}

// RecordContainerRestart records a container restart event globally
func RecordContainerRestart(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerRestart(containerInfo)
	}
}

// RecordContainerIteration records a container iteration completion globally
func RecordContainerIteration(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerIteration(containerInfo)
	}
}

// RecordContainerExit records a container terminal exit event globally (sp-dp92 P9).
func RecordContainerExit(containerInfo ContainerInfo) {
	if globalCollector != nil {
		globalCollector.RecordContainerExit(containerInfo)
	}
}

// RecordShipVersionConflict records a ship save whose row version moved past
// the entity's loaded version (a concurrent-writer clobber, sp-60ff). No-op
// when metrics are disabled or the global collector doesn't implement the
// recorder, so a metrics miss never touches the save path (RULINGS #4).
func RecordShipVersionConflict() {
	if globalCollector == nil {
		return
	}
	if rec, ok := globalCollector.(ShipWriteConflictRecorder); ok {
		rec.RecordShipVersionConflict()
	}
}

// DaemonComponentRecorder is implemented by collectors that track supervised
// daemon background components (sp-i01z). A separate single-method interface
// (instead of widening MetricsRecorder) so existing MetricsRecorder
// implementations and test fakes keep compiling.
type DaemonComponentRecorder interface {
	RecordDaemonComponentRestart(component string)
}

// RecordDaemonComponentRestart records a supervised daemon component restart
// globally. Wired into supervise.WithOnRestart at daemon boot.
func RecordDaemonComponentRestart(component string) {
	if globalCollector == nil {
		return
	}
	if rec, ok := globalCollector.(DaemonComponentRecorder); ok {
		rec.RecordDaemonComponentRestart(component)
	}
}

// SetGlobalNavigationCollector sets the global navigation metrics collector
func SetGlobalNavigationCollector(collector NavigationMetricsRecorder) {
	globalNavigationCollector = collector
}

// RecordRouteCompletion records a route completion event globally
func RecordRouteCompletion(playerID int, status navigation.RouteStatus, duration float64, distance int, fuelConsumed int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordRouteCompletion(playerID, status, duration, distance, fuelConsumed)
	}
}

// RecordSegmentCompletion records a route segment completion globally
func RecordSegmentCompletion(playerID int, distance int, fuelRequired int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordSegmentCompletion(playerID, distance, fuelRequired)
	}
}

// RecordFuelPurchase records a fuel purchase event globally
func RecordFuelPurchase(playerID int, waypoint string, units int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordFuelPurchase(playerID, waypoint, units)
	}
}

// RecordFuelConsumption records fuel consumption globally
func RecordFuelConsumption(playerID int, flightMode shared.FlightMode, units int) {
	if globalNavigationCollector != nil {
		globalNavigationCollector.RecordFuelConsumption(playerID, flightMode, units)
	}
}

// SetGlobalFinancialCollector sets the global financial metrics collector
func SetGlobalFinancialCollector(collector FinancialMetricsRecorder) {
	globalFinancialCollector = collector
}

// RecordTransaction records a transaction event globally
func RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTransaction(playerID, agentSymbol, transactionType, category, amount, creditsBalance)
	}
}

// RecordTrade records trade profitability metrics globally
func RecordTrade(playerID int, goodSymbol string, buyPrice int, sellPrice int, quantity int) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTrade(playerID, goodSymbol, buyPrice, sellPrice, quantity)
	}
}

// SetGlobalAPICollector sets the global API metrics collector
func SetGlobalAPICollector(collector *APIMetricsCollector) {
	globalAPICollector = collector
}

// GetGlobalAPICollector returns the global API metrics collector
// Returns nil if metrics are not enabled
func GetGlobalAPICollector() *APIMetricsCollector {
	return globalAPICollector
}

// SetGlobalMarketCollector sets the global market metrics collector
func SetGlobalMarketCollector(collector *MarketMetricsCollector) {
	globalMarketCollector = collector
}

// GetGlobalMarketCollector returns the global market metrics collector
// Returns nil if metrics are not enabled
func GetGlobalMarketCollector() *MarketMetricsCollector {
	return globalMarketCollector
}

// SetGlobalManufacturingCollector sets the global manufacturing metrics collector
func SetGlobalManufacturingCollector(collector *ManufacturingMetricsCollector) {
	globalManufacturingCollector = collector
}

// GetGlobalManufacturingCollector returns the global manufacturing metrics collector
// Returns nil if metrics are not enabled
func GetGlobalManufacturingCollector() *ManufacturingMetricsCollector {
	return globalManufacturingCollector
}

// RecordManufacturingPipelineCompletion records a pipeline completion event globally
func RecordManufacturingPipelineCompletion(playerID int, productGood, status string, duration time.Duration, profit int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordPipelineCompletion(playerID, productGood, status, duration, profit)
	}
}

// RecordManufacturingTaskCompletion records a task completion event globally
func RecordManufacturingTaskCompletion(playerID int, taskType, status string, duration time.Duration) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskCompletion(playerID, taskType, status, duration)
	}
}

// RecordManufacturingTaskRetry records a task retry event globally
func RecordManufacturingTaskRetry(playerID int, taskType string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskRetry(playerID, taskType)
	}
}

// RecordManufacturingSupplyTransition records a supply level change event globally
func RecordManufacturingSupplyTransition(playerID int, good, fromLevel, toLevel string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordSupplyTransition(playerID, good, fromLevel, toLevel)
	}
}

// RecordManufacturingFactoryCycle records a factory production cycle completion globally
func RecordManufacturingFactoryCycle(playerID int, factorySymbol, outputGood string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordFactoryCycle(playerID, factorySymbol, outputGood)
	}
}

// RecordManufacturingCost records a manufacturing cost globally
func RecordManufacturingCost(playerID int, costType string, amount int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordCost(playerID, costType, amount)
	}
}

// RecordManufacturingRevenue records manufacturing revenue globally
func RecordManufacturingRevenue(playerID int, amount int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordRevenue(playerID, amount)
	}
}

// RecordManufacturingTaskAssignment records a task assignment event globally
func RecordManufacturingTaskAssignment(playerID int, taskType string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskAssignment(playerID, taskType)
	}
}

// SetGlobalAbsorptionCollector sets the global absorption burn-in collector (sp-8cz9).
func SetGlobalAbsorptionCollector(collector *AbsorptionMetricsCollector) {
	globalAbsorptionCollector = collector
}

// GetGlobalAbsorptionCollector returns the global absorption burn-in collector.
// Returns nil if metrics are not enabled.
func GetGlobalAbsorptionCollector() *AbsorptionMetricsCollector {
	return globalAbsorptionCollector
}

// RecordAbsorptionCapBinding records one accepted-plan cap-binding classification
// globally (sp-8cz9 P1). No-op when metrics are disabled, so a metrics miss never
// touches the trade path (RULINGS #4).
func RecordAbsorptionCapBinding(playerID int, side, outcome string) {
	if globalAbsorptionCollector != nil {
		globalAbsorptionCollector.RecordCapBinding(playerID, side, outcome)
	}
}

// RecordAbsorptionLadderIncident records one cross-plan ladder incident globally
// (sp-8cz9 P2). No-op when metrics are disabled.
func RecordAbsorptionLadderIncident(playerID int, goodSymbol string) {
	if globalAbsorptionCollector != nil {
		globalAbsorptionCollector.RecordLadderIncident(playerID, goodSymbol)
	}
}

// SetGlobalTourCollector sets the global tour instrumentation collector (sp-fbih).
func SetGlobalTourCollector(collector *TourMetricsCollector) {
	globalTourCollector = collector
}

// GetGlobalTourCollector returns the global tour instrumentation collector.
// Returns nil if metrics are not enabled.
func GetGlobalTourCollector() *TourMetricsCollector {
	return globalTourCollector
}

// RecordTourReposition records one margins-death reposition evaluation globally
// (sp-fbih P3). No-op when metrics are disabled, so a metrics miss never touches the
// trade path (RULINGS #4).
func RecordTourReposition(playerID int, outcome string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordReposition(playerID, outcome)
	}
}

// RecordTourMarginsDeath records one confirmed 3-strike ground tap-out globally
// (sp-fbih P4). No-op when metrics are disabled.
func RecordTourMarginsDeath(playerID int) {
	if globalTourCollector != nil {
		globalTourCollector.RecordMarginsDeath(playerID)
	}
}

// RecordTourReserveFloorEngagement records one buy-time working-capital floor engagement
// globally (sp-fbih P5). No-op when metrics are disabled.
func RecordTourReserveFloorEngagement(playerID int, action string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordReserveFloorEngagement(playerID, action)
	}
}

// RecordTourExit records one tour-run terminal completion by exit reason globally
// (sp-fbih P11). No-op when metrics are disabled.
func RecordTourExit(playerID int, reason string) {
	if globalTourCollector != nil {
		globalTourCollector.RecordExit(playerID, reason)
	}
}

// RecordTourJumpLoaded records one committed margins-death reposition jump globally by
// whether it carried a look-back manifest (sp-ed4i). No-op when metrics are disabled, so
// a metrics miss never touches the trade path (RULINGS #4).
func RecordTourJumpLoaded(playerID int, loaded bool) {
	if globalTourCollector != nil {
		globalTourCollector.RecordJumpLoaded(playerID, loaded)
	}
}

// ObserveTourDuration observes one tour-run wall-time (seconds) globally at honest
// completion (sp-fbih P12). No-op when metrics are disabled.
func ObserveTourDuration(playerID int, seconds float64) {
	if globalTourCollector != nil {
		globalTourCollector.ObserveDuration(playerID, seconds)
	}
}

// SetTourResolvedMaxSpend records the dynamic per-tour spend cap globally as just resolved
// (sp-fbih P13). No-op when metrics are disabled.
func SetTourResolvedMaxSpend(playerID int, maxSpend int64) {
	if globalTourCollector != nil {
		globalTourCollector.SetResolvedMaxSpend(playerID, maxSpend)
	}
}

// SetTourFactoryGoodAcquisitionCost records the per-unit price a tour paid to
// acquire a factory good (source=stock|market) — the C1 (sp-64je) T2 acceptance
// series. No-op until the global tour collector is wired.
func SetTourFactoryGoodAcquisitionCost(playerID int, good, source string, unitPrice float64) {
	if globalTourCollector != nil {
		globalTourCollector.SetFactoryGoodAcquisitionCost(playerID, good, source, unitPrice)
	}
}

// ObserveTourPlanRate observes one tour plan's credits/hour globally (sp-1wp8),
// phase="projected" at plan-accept or phase="realized" at completion. No-op when
// metrics are disabled, so a metrics miss never touches the trade path (RULINGS #4).
func ObserveTourPlanRate(playerID int, phase string, creditsPerHour float64) {
	if globalTourCollector != nil {
		globalTourCollector.ObservePlanRate(playerID, phase, creditsPerHour)
	}
}

// SetGlobalTourStalenessCollector sets the global planner staleness-exclusion
// collector (sp-k7q5 layer 2).
func SetGlobalTourStalenessCollector(collector *TourStalenessMetricsCollector) {
	globalTourStalenessCollector = collector
}

// RecordTourLanesStaleExcluded records `count` tour lanes dropped for staleness in
// `system` globally (sp-k7q5 layer 2). No-op when metrics are disabled, so a metrics
// miss never touches the tour planning path (RULINGS #4).
func RecordTourLanesStaleExcluded(playerID int, system string, count int) {
	if globalTourStalenessCollector != nil {
		globalTourStalenessCollector.RecordStaleExcluded(playerID, system, count)
	}
}

// RecordTourCandidateDropped records `count` profitable lanes dropped from tour candidate
// assembly for `reason` globally (sp-mtvg). No-op when metrics are disabled, so a metrics
// miss never touches the tour planning path (RULINGS #4).
func RecordTourCandidateDropped(playerID int, reason string, count int) {
	if globalTourStalenessCollector != nil {
		globalTourStalenessCollector.RecordCandidateDropped(playerID, reason, count)
	}
}

// RecordAbsorptionConsultVerdict records one consult-apply verdict globally (sp-dp92
// P6). engine distinguishes the emitting engine ("idle_arb"|"trade_route"); verdict
// uses each engine's own native vocabulary (idle_arb: skip_reserved|pass; trade_route:
// clear|shadow|reserved-depth|unreadable). No-op when metrics are disabled (RULINGS #4).
func RecordAbsorptionConsultVerdict(playerID int, verdict, engine string) {
	if globalAbsorptionCollector != nil {
		globalAbsorptionCollector.RecordConsultVerdict(playerID, verdict, engine)
	}
}

// SetGlobalScoutCollector sets the global scout metrics collector (sp-dp92 P7).
func SetGlobalScoutCollector(collector *ScoutMetricsCollector) {
	globalScoutCollector = collector
}

// GetGlobalScoutCollector returns the global scout metrics collector.
// Returns nil if metrics are not enabled.
func GetGlobalScoutCollector() *ScoutMetricsCollector {
	return globalScoutCollector
}

// RecordScoutFreshness sets the scout market-freshness gauge for one (player, system)
// globally (sp-dp92 P7). No-op when metrics are disabled (RULINGS #4).
func RecordScoutFreshness(playerID int, system string, ageSeconds float64) {
	if globalScoutCollector != nil {
		globalScoutCollector.RecordFreshness(playerID, system, ageSeconds)
	}
}

// SetGlobalFleetHealthCollector sets the global fleet-health collector (sp-686e).
func SetGlobalFleetHealthCollector(collector *FleetHealthMetricsCollector) {
	globalFleetHealthCollector = collector
}

// GetGlobalFleetHealthCollector returns the global fleet-health collector.
// Returns nil if metrics are not enabled.
func GetGlobalFleetHealthCollector() *FleetHealthMetricsCollector {
	return globalFleetHealthCollector
}

// RecordHullStranded records one stranded-hull episode for a (ship, system) globally
// (sp-686e). No-op when metrics are disabled, so a metrics miss never touches the
// reposition/tour path (RULINGS #4).
func RecordHullStranded(ship, system string) {
	if globalFleetHealthCollector != nil {
		globalFleetHealthCollector.RecordHullStranded(ship, system)
	}
}

// SetGlobalChainPnLCollector sets the global chain-P&L collector (sp-rh2z). Pass nil to
// clear it (e.g. in test cleanup).
func SetGlobalChainPnLCollector(collector *ChainPnLMetricsCollector) {
	globalChainPnLCollector = collector
}

// GetGlobalChainPnLCollector returns the global chain-P&L collector.
// Returns nil if metrics are not enabled.
func GetGlobalChainPnLCollector() *ChainPnLMetricsCollector {
	return globalChainPnLCollector
}

// RecordChainPnLRealizedPerHour sets a chain's realized-P&L/hr gauge globally (sp-rh2z).
// No-op when metrics are disabled, so a metrics miss never touches the kill-check path
// (RULINGS #4).
func RecordChainPnLRealizedPerHour(good string, perHour float64) {
	if globalChainPnLCollector != nil {
		globalChainPnLCollector.RecordRealizedPerHour(good, perHour)
	}
}

// RecordChainPnLKill increments a chain's kill-episode counter globally (sp-rh2z). No-op
// when metrics are disabled (RULINGS #4).
func RecordChainPnLKill(good string) {
	if globalChainPnLCollector != nil {
		globalChainPnLCollector.RecordKill(good)
	}
}

// SetGlobalChainInputPauseCollector sets the global input-pause collector (sp-r5a6). Pass nil
// to clear it (e.g. in test cleanup).
func SetGlobalChainInputPauseCollector(collector *ChainInputPauseMetricsCollector) {
	globalChainInputPauseCollector = collector
}

// GetGlobalChainInputPauseCollector returns the global input-pause collector.
// Returns nil if metrics are not enabled.
func GetGlobalChainInputPauseCollector() *ChainInputPauseMetricsCollector {
	return globalChainInputPauseCollector
}

// RecordChainInputPause increments a chain's input-pause-episode counter globally (sp-r5a6).
// No-op when metrics are disabled, so a metrics miss never touches the pause-check path
// (RULINGS #4).
func RecordChainInputPause(good string) {
	if globalChainInputPauseCollector != nil {
		globalChainInputPauseCollector.RecordPause(good)
	}
}

// SetGlobalChainExportRestCollector sets the global export-ask-subsidy rest collector (sp-xdk6).
// Pass nil to clear it (e.g. in test cleanup).
func SetGlobalChainExportRestCollector(collector *ChainExportRestMetricsCollector) {
	globalChainExportRestCollector = collector
}

// GetGlobalChainExportRestCollector returns the global export-rest collector.
// Returns nil if metrics are not enabled.
func GetGlobalChainExportRestCollector() *ChainExportRestMetricsCollector {
	return globalChainExportRestCollector
}

// RecordChainExportRest increments a chain's export-rest-episode counter globally (sp-xdk6).
// No-op when metrics are disabled, so a metrics miss never touches the rest-check path
// (RULINGS #4).
func RecordChainExportRest(good string) {
	if globalChainExportRestCollector != nil {
		globalChainExportRestCollector.RecordRest(good)
	}
}

// SetGlobalSitingCollector sets the global factory-siting collector (sp-vdld). Pass nil to
// clear it (e.g. in test cleanup).
func SetGlobalSitingCollector(collector *SitingMetricsCollector) {
	globalSitingCollector = collector
}

// GetGlobalSitingCollector returns the global factory-siting collector.
func GetGlobalSitingCollector() *SitingMetricsCollector {
	return globalSitingCollector
}

// RecordSitingLaunch increments the siting launch counter for a (good, system) chain globally
// (sp-vdld). No-op when metrics are disabled, so a metrics miss never touches the ACT path
// (RULINGS #4).
func RecordSitingLaunch(good, system string) {
	if globalSitingCollector != nil {
		globalSitingCollector.RecordLaunch(good, system)
	}
}

// RecordSitingRetire increments the siting retire counter for a (good, system) chain globally
// (sp-vdld). No-op when metrics are disabled (RULINGS #4).
func RecordSitingRetire(good, system string) {
	if globalSitingCollector != nil {
		globalSitingCollector.RecordRetire(good, system)
	}
}

// RecordSitingScoutDemand increments the siting scout-demand counter for a system globally
// (sp-vdld). No-op when metrics are disabled (RULINGS #4).
func RecordSitingScoutDemand(system string) {
	if globalSitingCollector != nil {
		globalSitingCollector.RecordScoutDemand(system)
	}
}

// SetGlobalFleetAutosizerCollector sets the global fleet-autosizer collector (sp-1txd). Pass nil
// to clear it (e.g. in test cleanup).
func SetGlobalFleetAutosizerCollector(collector *FleetAutosizerMetricsCollector) {
	globalFleetAutosizerCollector = collector
}

// GetGlobalFleetAutosizerCollector returns the global fleet-autosizer collector.
func GetGlobalFleetAutosizerCollector() *FleetAutosizerMetricsCollector {
	return globalFleetAutosizerCollector
}

// SetGlobalBootstrapCollector sets the global captain-bootstrap collector (sp-3nbe). Pass nil to
// clear it (e.g. in test cleanup).
func SetGlobalBootstrapCollector(collector *BootstrapMetricsCollector) {
	globalBootstrapCollector = collector
}

// GetGlobalBootstrapCollector returns the global captain-bootstrap collector.
func GetGlobalBootstrapCollector() *BootstrapMetricsCollector {
	return globalBootstrapCollector
}

// RecordAutosizerPurchase increments the autosizer purchase counter for a class globally
// (sp-1txd). No-op when metrics are disabled, so a metrics miss never touches the buy path.
func RecordAutosizerPurchase(class string) {
	if globalFleetAutosizerCollector != nil {
		globalFleetAutosizerCollector.RecordPurchase(class)
	}
}

// RecordAutosizerBlocked increments the autosizer blocked counter for a (class, guard) globally
// (sp-1txd). No-op when metrics are disabled.
func RecordAutosizerBlocked(class, guard string) {
	if globalFleetAutosizerCollector != nil {
		globalFleetAutosizerCollector.RecordBlocked(class, guard)
	}
}

// RecordAutosizerDemand sets the autosizer demand/current gauges for a class globally (sp-1txd).
// No-op when metrics are disabled.
func RecordAutosizerDemand(class string, demand, current int) {
	if globalFleetAutosizerCollector != nil {
		globalFleetAutosizerCollector.RecordDemand(class, demand, current)
	}
}

// RecordAutosizerZeroEffectAlarm increments the autosizer zero-effect alarm counter globally
// (sp-1txd). No-op when metrics are disabled.
func RecordAutosizerZeroEffectAlarm() {
	if globalFleetAutosizerCollector != nil {
		globalFleetAutosizerCollector.RecordZeroEffectAlarm()
	}
}

// SetGlobalAPIBudgetTracker sets the global API request-budget tracker
// (sp-51ti). Pass nil to clear it (e.g. in test cleanup).
func SetGlobalAPIBudgetTracker(tracker *APIBudgetTracker) {
	globalAPIBudgetTracker = tracker
}

// GetGlobalAPIBudgetTracker returns the global API request-budget tracker.
// Returns nil if it was never set.
func GetGlobalAPIBudgetTracker() *APIBudgetTracker {
	return globalAPIBudgetTracker
}

// SetGlobalDutyCycleSampler sets the global duty-cycle KPI sampler
// (sp-51ti). Pass nil to clear it (e.g. in test cleanup).
func SetGlobalDutyCycleSampler(sampler *DutyCycleSampler) {
	globalDutyCycleSampler = sampler
}

// GetGlobalDutyCycleSampler returns the global duty-cycle KPI sampler.
// Returns nil if it was never set.
func GetGlobalDutyCycleSampler() *DutyCycleSampler {
	return globalDutyCycleSampler
}
