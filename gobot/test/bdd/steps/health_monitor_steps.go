package steps

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

// HealthMonitorContext holds state for health monitor BDD tests
type HealthMonitorContext struct {
	healthMonitor *daemon.HealthMonitor
	clock         *shared.MockClock
}

func InitializeHealthMonitorContext(ctx *godog.ScenarioContext) {
	hmc := &HealthMonitorContext{}
	
	ctx.Step(`^the clock is mocked$`, hmc.theClockIsMocked)
	ctx.Step(`^a health monitor exists with check interval (\d+) seconds and recovery timeout (\d+) seconds$`, hmc.aHealthMonitorExists)
	
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		hmc.healthMonitor = nil
		hmc.clock = nil
		return ctx, nil
	})
}

func (hmc *HealthMonitorContext) theClockIsMocked() error {
	hmc.clock = shared.NewMockClock(time.Now())
	return nil
}

func (hmc *HealthMonitorContext) aHealthMonitorExists(checkInterval, recoveryTimeout int) error {
	hmc.healthMonitor = daemon.NewHealthMonitor(
		time.Duration(checkInterval)*time.Second,
		time.Duration(recoveryTimeout)*time.Second,
		hmc.clock,
	)
	return nil
}
