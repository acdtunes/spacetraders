package bdd

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/test/bdd/steps"
	"github.com/cucumber/godog"
)

func TestContainerLifecycle(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			steps.InitializeContainerLifecycleScenario(sc)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features/daemon/container_lifecycle.feature"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run container lifecycle tests")
	}
}
