package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

// HealthMonitorContext holds state for health monitor BDD tests
type HealthMonitorContext struct {
	healthMonitor                *daemon.HealthMonitor
	clock                        *shared.MockClock
	recoveryFailed               bool
	failedRecoveryCount          int
	totalRecoveryAttempts        int
	successfulRecoveries         int
	healthCheckSkippedDueToCooldown bool
}

func InitializeHealthMonitorContext(ctx *godog.ScenarioContext) {
	hmc := &HealthMonitorContext{}

	ctx.Step(`^the clock is mocked$`, hmc.theClockIsMocked)
	ctx.Step(`^a health monitor exists with check interval (\d+) seconds and recovery timeout (\d+) seconds$`, hmc.aHealthMonitorExists)

	// Recovery tracking steps
	ctx.Step(`^(\d+) recoveries failed$`, hmc.recoveriesFailed)
	ctx.Step(`^the recovery should be marked as failed$`, hmc.theRecoveryShouldBeMarkedAsFailed)
	ctx.Step(`^the ship has already failed recovery (\d+) times$`, hmc.theShipHasAlreadyFailedRecoveryTimes)
	ctx.Step(`^the health check should be skipped due to cooldown$`, hmc.theHealthCheckShouldBeSkippedDueToCooldown)
	ctx.Step(`^the failed recovery count should be (\d+)$`, hmc.theFailedRecoveryCountShouldBe)

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		hmc.healthMonitor = nil
		hmc.clock = nil
		hmc.recoveryFailed = false
		hmc.failedRecoveryCount = 0
		hmc.totalRecoveryAttempts = 0
		hmc.successfulRecoveries = 0
		hmc.healthCheckSkippedDueToCooldown = false
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

// Recovery tracking step implementations
func (hmc *HealthMonitorContext) recoveriesFailed(count int) error {
	hmc.failedRecoveryCount = count
	hmc.totalRecoveryAttempts += count
	return nil
}

func (hmc *HealthMonitorContext) theRecoveryShouldBeMarkedAsFailed() error {
	if !hmc.recoveryFailed {
		return fmt.Errorf("expected recovery to be marked as failed, but it was not")
	}
	return nil
}

func (hmc *HealthMonitorContext) theShipHasAlreadyFailedRecoveryTimes(count int) error {
	hmc.failedRecoveryCount = count
	return nil
}

func (hmc *HealthMonitorContext) theHealthCheckShouldBeSkippedDueToCooldown() error {
	if !hmc.healthCheckSkippedDueToCooldown {
		return fmt.Errorf("expected health check to be skipped due to cooldown, but it was not")
	}
	return nil
}

func (hmc *HealthMonitorContext) theFailedRecoveryCountShouldBe(expected int) error {
	if hmc.failedRecoveryCount != expected {
		return fmt.Errorf("expected failed recovery count to be %d, but got %d", expected, hmc.failedRecoveryCount)
	}
	return nil
}
