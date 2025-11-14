package bdd

import (
	"testing"

	"github.com/cucumber/godog"
	"github.com/andrescamacho/spacetraders-go/test/bdd/steps"
)

func TestShipOperations(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			steps.InitializeShipOperationsScenario(ctx)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features/application/dock_ship.feature", "features/application/orbit_ship.feature"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}
