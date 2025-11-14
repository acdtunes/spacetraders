package bdd

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/test/bdd/steps"
	"github.com/cucumber/godog"
)

func TestContainerLogging(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			steps.InitializeContainerLoggingScenario(sc)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features/daemon/container_logging.feature"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run container logging feature tests")
	}
}
