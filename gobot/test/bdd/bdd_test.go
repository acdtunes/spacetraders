package bdd

import (
	"os"
	"testing"

	"github.com/andrescamacho/spacetraders-go/test/bdd/steps"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
	"github.com/cucumber/godog"
)

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features/domain", "features/application", "features/adapters", "features/infrastructure", "features/daemon"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

func InitializeScenario(sc *godog.ScenarioContext) {
	// Register all step definitions
	// NOTE: ValueObjectScenarios registered FIRST so its step definitions take precedence
	// for shared steps like "the result should be true/false"
	steps.InitializeValueObjectScenarios(sc)
	// NOTE: Route scenario registered early to ensure its step definitions take precedence
	// for shared steps like "the operation should fail with error" in route tests
	steps.InitializeRouteScenario(sc)
	steps.InitializeShipScenario(sc)
	// NOTE: ContainerLifecycleScenario registered BEFORE ContainerScenario so lifecycle steps take precedence
	// The lifecycle steps have the same wording but operate on containerLifecycleContext with currentContainer
	steps.InitializeContainerLifecycleScenario(sc)
	steps.InitializeContainerScenario(sc)
	// Market scouting domain scenarios
	// NOTE: MarketScenario (trading) registered FIRST to take precedence for simpler patterns
	// TradeGoodSteps and ScoutingMarketSteps check sharedErr for cross-context error assertions
	steps.InitializeMarketScenario(sc)      // Trading market - register first for simpler patterns and error steps
	steps.InitializeTradeGoodSteps(sc)      // Trade good value object - uses sharedErr for error assertions
	steps.InitializeScoutingMarketSteps(sc) // Scouting market - register after for more specific patterns
	// NOTE: NegotiateContractScenario registered BEFORE ContractScenario so negotiate contract assertions take precedence
	steps.InitializeNegotiateContractScenario(sc)
	steps.InitializeContractScenario(sc)
	steps.InitializeAcceptContractScenario(sc) // Re-enabled
	steps.InitializeFulfillContractScenario(sc) // Re-enabled
	steps.InitializeNavigateShipHandlerScenario(sc) // Re-enabled

	// Register ShipOperationsScenario (dock, orbit, set flight mode) BEFORE NavigateToWaypointScenario
	// so its step definitions take precedence for dock_ship.feature, orbit_ship.feature, and set_flight_mode.feature
	steps.InitializeShipOperationsScenario(sc)
	steps.InitializeNavigateToWaypointScenario(sc) // Re-enabled
	steps.InitializeRefuelShipScenario(sc)
	// Adapter layer scenarios
	// NOTE: MarketRepositoryScenario registered BEFORE TransactionLimitScenario
	// to ensure its "a player with ID" step takes precedence for market_repository.feature
	steps.InitializeMarketRepositoryScenario(sc)

	// NOTE: TransactionLimitScenario registered BEFORE PurchaseCargoScenario
	// so transaction limit step definitions take precedence for purchase_cargo_transaction_limits.feature
	steps.InitializeTransactionLimitScenario(sc) // Re-enabled
	steps.InitializePurchaseCargoScenario(sc)
	steps.InitializeSellCargoScenario(sc)
	steps.InitializeJettisonCargoScenario(sc)
	steps.InitializeDeliverContractScenario(sc) // Re-enabled
	steps.InitializeRoutePlannerScenario(sc)
	steps.InitializeRouteExecutorScenario(sc) // Re-enabled
	steps.InitializeEvaluateContractProfitabilityScenario(sc) // Re-enabled
	steps.InitializeBatchContractWorkflowScenario(sc)

	// Daemon layer scenarios (registered before scouting to avoid step collisions)
	// NOTE: Infrastructure scenarios registered BEFORE entity scenarios so that infrastructure
	// steps take precedence for features/daemon/* (first registration wins in godog, not last)
	steps.InitializeShipAssignmentScenario(sc) // Infrastructure layer ship assignment tests - FIRST to take precedence
	steps.InitializeHealthMonitorContext(sc)     // Infrastructure layer health monitor tests - FIRST to take precedence
	steps.InitializeDaemonEntityScenarios(sc)   // Domain-layer daemon entity tests (ShipAssignment, HealthMonitor) - AFTER infrastructure
	steps.InitializeDaemonPlayerResolutionScenario(sc) // Re-enabled
	steps.InitializeDaemonServerScenario(sc) // Re-enabled - core functionality implemented, complex scenarios marked pending
	steps.InitializeContainerLoggingScenario(sc) // Re-enabled - testing

	// Scouting application layer scenarios
	steps.InitializeGetMarketDataScenario(sc)
	steps.InitializeListMarketDataScenario(sc)
	steps.InitializeScoutTourScenario(sc)
	steps.InitializeScoutMarketsScenario(sc)

	// Infrastructure layer scenarios
	steps.InitializeWaypointCacheScenario(sc) // Re-enabled

	// Register NavigationUtils scenario
	steps.InitializeNavigationUtilsScenario(sc) // Re-enabled

	// Register common undefined steps (temporary implementations)
	steps.InitializeCommonUndefinedSteps(sc)
	steps.InitializeRouteNavigationUndefinedSteps(sc)
	steps.InitializeHealthShipContainerUndefinedSteps(sc)
	steps.InitializeScoutingMiscUndefinedSteps(sc)

	// API Adapter layer scenarios (circuit breaker, retry logic, rate limiting)
	steps.InitializeAPIAdapterSteps(sc) // PLACEHOLDER - Step definitions not yet implemented
}

func TestMain(m *testing.M) {
	// Initialize shared test database for all integration tests
	// This reduces test time from ~24s to ~9s by avoiding per-scenario DB creation
	if err := helpers.InitializeSharedTestDB(); err != nil {
		panic("Failed to initialize shared test database: " + err.Error())
	}
	defer helpers.CloseSharedTestDB()

	// Run tests
	os.Exit(m.Run())
}
