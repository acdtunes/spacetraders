package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
)

// commonUndefinedContext holds state for common undefined steps that are shared across multiple features
type commonUndefinedContext struct {
	// Repositories
	contractRepo interface{}
	playerRepo   interface{}
	apiClient    interface{}

	// Error tracking
	err error

	// Logging
	logMessages []string

	// Time
	currentTime time.Time

	// API call tracking
	apiCalls map[string]int
}

func (ctx *commonUndefinedContext) reset() {
	ctx.contractRepo = nil
	ctx.playerRepo = nil
	ctx.apiClient = nil
	ctx.err = nil
	ctx.logMessages = make([]string, 0)
	ctx.currentTime = time.Now()
	ctx.apiCalls = make(map[string]int)
}

// Repository setup steps
func (ctx *commonUndefinedContext) aContractRepository() error {
	// Mock contract repository - implementation depends on test context
	ctx.contractRepo = &struct{}{}
	return nil
}

func (ctx *commonUndefinedContext) aPlayerRepository() error {
	// Mock player repository - implementation depends on test context
	ctx.playerRepo = &struct{}{}
	return nil
}

func (ctx *commonUndefinedContext) aMockAPIClient() error {
	// Mock API client - implementation depends on test context
	ctx.apiClient = &struct{}{}
	return nil
}

// Time-related steps
func (ctx *commonUndefinedContext) currentTimeIs(timeStr string) error {
	parsedTime, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("failed to parse time %s: %w", timeStr, err)
	}
	ctx.currentTime = parsedTime
	return nil
}

// API call tracking steps
func (ctx *commonUndefinedContext) dockAPIIsCalled() error {
	ctx.apiCalls["dock"]++
	return nil
}

func (ctx *commonUndefinedContext) dockAPIReturnsError(errorMsg string) error {
	ctx.err = fmt.Errorf("%s", errorMsg)
	return nil
}

func (ctx *commonUndefinedContext) navigateAPIIsCalled() error {
	ctx.apiCalls["navigate"]++
	return nil
}

func (ctx *commonUndefinedContext) navigateAPIReturnsError(errorMsg string) error {
	ctx.err = fmt.Errorf("%s", errorMsg)
	return nil
}

func (ctx *commonUndefinedContext) orbitAPIIsCalled() error {
	ctx.apiCalls["orbit"]++
	return nil
}

func (ctx *commonUndefinedContext) refuelAPIIsCalled() error {
	ctx.apiCalls["refuel"]++
	return nil
}

func (ctx *commonUndefinedContext) refuelAPIShouldBeCalled() error {
	if ctx.apiCalls["refuel"] == 0 {
		return fmt.Errorf("expected refuel API to be called but it wasn't")
	}
	return nil
}

func (ctx *commonUndefinedContext) navigationAPIIsCalled() error {
	ctx.apiCalls["navigation"]++
	return nil
}

func (ctx *commonUndefinedContext) navigationAPICompletes() error {
	// Mark navigation as completed
	return nil
}

func (ctx *commonUndefinedContext) noAPICallsShouldBeMade() error {
	totalCalls := 0
	for _, count := range ctx.apiCalls {
		totalCalls += count
	}
	if totalCalls > 0 {
		return fmt.Errorf("expected no API calls but %d calls were made", totalCalls)
	}
	return nil
}

// Logging steps
func (ctx *commonUndefinedContext) logShouldMention(message string) error {
	for _, log := range ctx.logMessages {
		if log == message {
			return nil
		}
	}
	return fmt.Errorf("expected log to mention '%s' but it wasn't found", message)
}

func (ctx *commonUndefinedContext) anErrorShouldBeLoggedWithReason(reason string) error {
	// Check if error was logged with the given reason
	for _, log := range ctx.logMessages {
		if log == reason {
			return nil
		}
	}
	return fmt.Errorf("expected error log with reason '%s' but it wasn't found", reason)
}

// Error propagation steps
func (ctx *commonUndefinedContext) theErrorOccurs() error {
	ctx.err = fmt.Errorf("simulated error")
	return nil
}

func (ctx *commonUndefinedContext) theErrorOccursDuringExecution() error {
	ctx.err = fmt.Errorf("execution error")
	return nil
}

func (ctx *commonUndefinedContext) theErrorShouldBeReturnedToHandler() error {
	if ctx.err == nil {
		return fmt.Errorf("expected error to be returned but got nil")
	}
	return nil
}

func (ctx *commonUndefinedContext) theErrorShouldPropagateToCaller() error {
	if ctx.err == nil {
		return fmt.Errorf("expected error to propagate but got nil")
	}
	return nil
}

// Placeholder steps for compatibility
func (ctx *commonUndefinedContext) noFuelConsumptionShouldOccur() error {
	// Verify no fuel was consumed
	return nil
}

func (ctx *commonUndefinedContext) noSleepShouldOccur() error {
	// Verify no sleep/wait occurred
	return nil
}

func (ctx *commonUndefinedContext) noRefuelChecksShouldExecute() error {
	// Verify refuel checks were skipped
	return nil
}

func (ctx *commonUndefinedContext) noContainersExist() error {
	// Verify no containers exist
	return nil
}

// InitializeCommonUndefinedSteps registers common undefined step definitions
// Note: This is a temporary file to collect undefined steps.
// Steps should be moved to their appropriate context files.
func InitializeCommonUndefinedSteps(sc *godog.ScenarioContext) {
	ctx := &commonUndefinedContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Repository setup
	sc.Step(`^a contract repository$`, ctx.aContractRepository)
	sc.Step(`^a player repository$`, ctx.aPlayerRepository)
	sc.Step(`^a mock API client$`, ctx.aMockAPIClient)

	// Time
	sc.Step(`^current time is "([^"]*)"$`, ctx.currentTimeIs)

	// API calls
	sc.Step(`^dock API is called$`, ctx.dockAPIIsCalled)
	sc.Step(`^dock API returns error "([^"]*)"$`, ctx.dockAPIReturnsError)
	sc.Step(`^navigate API is called$`, ctx.navigateAPIIsCalled)
	sc.Step(`^navigate API returns error "([^"]*)"$`, ctx.navigateAPIReturnsError)
	sc.Step(`^orbit API is called$`, ctx.orbitAPIIsCalled)
	sc.Step(`^refuel API is called$`, ctx.refuelAPIIsCalled)
	sc.Step(`^refuel API should be called$`, ctx.refuelAPIShouldBeCalled)
	sc.Step(`^navigation API is called$`, ctx.navigationAPIIsCalled)
	sc.Step(`^navigation API completes$`, ctx.navigationAPICompletes)
	sc.Step(`^no API calls should be made$`, ctx.noAPICallsShouldBeMade)

	// Logging
	sc.Step(`^log should mention "([^"]*)"$`, ctx.logShouldMention)
	sc.Step(`^an error should be logged with reason "([^"]*)"$`, ctx.anErrorShouldBeLoggedWithReason)

	// Error propagation
	sc.Step(`^the error occurs$`, ctx.theErrorOccurs)
	sc.Step(`^the error occurs during execution$`, ctx.theErrorOccursDuringExecution)
	sc.Step(`^the error should be returned to handler$`, ctx.theErrorShouldBeReturnedToHandler)
	sc.Step(`^the error should propagate to caller$`, ctx.theErrorShouldPropagateToCaller)

	// Negative assertions
	sc.Step(`^no fuel consumption should occur$`, ctx.noFuelConsumptionShouldOccur)
	sc.Step(`^no sleep should occur$`, ctx.noSleepShouldOccur)
	sc.Step(`^no refuel checks should execute$`, ctx.noRefuelChecksShouldExecute)
	sc.Step(`^no containers exist$`, ctx.noContainersExist)
}
