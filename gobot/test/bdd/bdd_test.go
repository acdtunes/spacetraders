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
			Paths:    []string{"features/domain", "features/utils"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

func InitializeScenario(sc *godog.ScenarioContext) {
	// Register step definitions for the sample ship entity test
	// NOTE: ValueObjectScenarios registered FIRST so its step definitions take precedence
	// for shared steps like "the result should be true/false"
	// Container steps registered BEFORE ship steps to handle container-specific error assertions
	steps.InitializeValueObjectScenarios(sc)
	steps.RegisterContainerSteps(sc)
	steps.InitializeShipScenario(sc)
	steps.InitializeContainerIDSteps(sc)
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
