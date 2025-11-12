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
			Paths:    []string{"features"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

func InitializeScenario(sc *godog.ScenarioContext) {
	// Register all step definitions
	steps.InitializeShipScenario(sc)
	steps.InitializeRouteScenario(sc)
	steps.InitializeContainerScenario(sc)
	steps.InitializeValueObjectScenarios(sc)
	steps.InitializeNavigateShipHandlerScenario(sc)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
