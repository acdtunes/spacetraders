package bdd

import (
	"os"
	"testing"

	"github.com/andrescamacho/spacetraders-go/test/bdd/steps"
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
	// NOTE: ContainerLifecycleScenario registered BEFORE ContainerScenario so lifecycle steps take precedence
	// The lifecycle steps have the same wording but operate on containerLifecycleContext with currentContainer
	steps.InitializeContainerLifecycleScenario(sc)
	steps.InitializeContainerScenario(sc)
	steps.InitializeShipScenario(sc)
	steps.InitializeRouteScenario(sc)
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

	// Scouting application layer scenarios
	steps.InitializeGetMarketDataScenario(sc)
	steps.InitializeListMarketDataScenario(sc)
	steps.InitializeScoutTourScenario(sc)
	steps.InitializeScoutMarketsScenario(sc)

	// Infrastructure layer scenarios
	steps.InitializeWaypointCacheScenario(sc) // Re-enabled
	steps.InitializeDatabaseRetryScenario(sc)

	// Daemon layer scenarios
	// steps.InitializeDaemonPlayerResolutionScenario(sc) // Temporarily disabled - incomplete
	// steps.InitializeDaemonServerScenario(sc) // Temporarily disabled - compilation errors
	// steps.InitializeShipAssignmentScenario(sc) // Temporarily disabled - compilation errors
	steps.InitializeContainerLoggingScenario(sc) // Re-enabled - testing
	steps.InitializeHealthMonitorContext(sc)     // Re-enabled

	// Register NavigationUtils scenario (temporarily disabled - file backed up)
	// // steps.InitializeNavigationUtilsScenario(sc) // Temporarily disabled
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
