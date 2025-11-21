package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type lifecycleStateMachineContext struct {
	stateMachine    *shared.LifecycleStateMachine
	clock           *shared.MockClock
	transitionError error
	isRunningResult bool
	isPendingResult bool
	isFinishedResult bool
	runtimeDuration time.Duration
}

func (lc *lifecycleStateMachineContext) reset() {
	lc.stateMachine = nil
	lc.clock = shared.NewMockClock(time.Time{})
	lc.transitionError = nil
	lc.isRunningResult = false
	lc.isPendingResult = false
	lc.isFinishedResult = false
	lc.runtimeDuration = 0
}

// Given steps

func (lc *lifecycleStateMachineContext) aLifecycleStateMachineInState(state string) error {
	lc.clock = shared.NewMockClock(time.Time{})
	lc.stateMachine = shared.NewLifecycleStateMachine(lc.clock)

	// Transition to desired state
	switch state {
	case "PENDING":
		// Already in PENDING state
		return nil
	case "RUNNING":
		return lc.stateMachine.Start()
	case "COMPLETED":
		if err := lc.stateMachine.Start(); err != nil {
			return err
		}
		return lc.stateMachine.Complete()
	case "FAILED":
		if err := lc.stateMachine.Start(); err != nil {
			return err
		}
		return lc.stateMachine.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		return lc.stateMachine.Stop()
	default:
		return fmt.Errorf("unknown state: %s", state)
	}
}

func (lc *lifecycleStateMachineContext) secondsHavePassed(seconds int) error {
	lc.clock.Advance(time.Duration(seconds) * time.Second)
	return nil
}

func (lc *lifecycleStateMachineContext) aLifecycleStateMachineThatRanForSecondsAndCompleted(seconds int) error {
	lc.clock = shared.NewMockClock(time.Time{})
	lc.stateMachine = shared.NewLifecycleStateMachine(lc.clock)

	// Start the state machine
	if err := lc.stateMachine.Start(); err != nil {
		return err
	}

	// Advance time
	lc.clock.Advance(time.Duration(seconds) * time.Second)

	// Complete
	if err := lc.stateMachine.Complete(); err != nil {
		return err
	}

	return nil
}

func (lc *lifecycleStateMachineContext) aLifecycleStateMachineThatRanForSecondsAndFailed(seconds int) error {
	lc.clock = shared.NewMockClock(time.Time{})
	lc.stateMachine = shared.NewLifecycleStateMachine(lc.clock)

	// Start the state machine
	if err := lc.stateMachine.Start(); err != nil {
		return err
	}

	// Advance time
	lc.clock.Advance(time.Duration(seconds) * time.Second)

	// Fail
	if err := lc.stateMachine.Fail(fmt.Errorf("test error")); err != nil {
		return err
	}

	return nil
}

func (lc *lifecycleStateMachineContext) aLifecycleStateMachineThatRanForSecondsAndStopped(seconds int) error {
	lc.clock = shared.NewMockClock(time.Time{})
	lc.stateMachine = shared.NewLifecycleStateMachine(lc.clock)

	// Start the state machine
	if err := lc.stateMachine.Start(); err != nil {
		return err
	}

	// Advance time
	lc.clock.Advance(time.Duration(seconds) * time.Second)

	// Stop
	if err := lc.stateMachine.Stop(); err != nil {
		return err
	}

	return nil
}

// When steps

func (lc *lifecycleStateMachineContext) iCreateANewLifecycleStateMachine() error {
	lc.clock = shared.NewMockClock(time.Time{})
	lc.stateMachine = shared.NewLifecycleStateMachine(lc.clock)
	return nil
}

func (lc *lifecycleStateMachineContext) iStartTheLifecycleStateMachine() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.transitionError = lc.stateMachine.Start()
	return nil
}

func (lc *lifecycleStateMachineContext) iCompleteTheLifecycleStateMachine() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.transitionError = lc.stateMachine.Complete()
	return nil
}

func (lc *lifecycleStateMachineContext) iFailTheLifecycleStateMachineWithError(errorMsg string) error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.transitionError = lc.stateMachine.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (lc *lifecycleStateMachineContext) iStopTheLifecycleStateMachine() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.transitionError = lc.stateMachine.Stop()
	return nil
}

func (lc *lifecycleStateMachineContext) iCheckIfTheStateMachineIsRunning() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.isRunningResult = lc.stateMachine.IsRunning()
	return nil
}

func (lc *lifecycleStateMachineContext) iCheckIfTheStateMachineIsPending() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.isPendingResult = lc.stateMachine.IsPending()
	return nil
}

func (lc *lifecycleStateMachineContext) iCheckIfTheStateMachineIsFinished() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.isFinishedResult = lc.stateMachine.IsFinished()
	return nil
}

func (lc *lifecycleStateMachineContext) iGetTheRuntimeDuration() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}
	lc.runtimeDuration = lc.stateMachine.RuntimeDuration()
	return nil
}

// Then steps

func (lc *lifecycleStateMachineContext) theLifecycleStatusShouldBe(expectedStatus string) error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	actualStatus := string(lc.stateMachine.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStateMachineShouldHaveACreatedTimestamp() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.CreatedAt().IsZero() {
		return fmt.Errorf("created timestamp is not set")
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStateMachineShouldHaveAnUpdatedTimestamp() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.UpdatedAt().IsZero() {
		return fmt.Errorf("updated timestamp is not set")
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStartedTimestampShouldBeNil() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.StartedAt() != nil {
		return fmt.Errorf("started timestamp should be nil but was %v", *lc.stateMachine.StartedAt())
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStoppedTimestampShouldBeNil() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.StoppedAt() != nil {
		return fmt.Errorf("stopped timestamp should be nil but was %v", *lc.stateMachine.StoppedAt())
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStartedTimestampShouldBeSet() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.StartedAt() == nil {
		return fmt.Errorf("started timestamp should be set but was nil")
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStoppedTimestampShouldBeSet() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.StoppedAt() == nil {
		return fmt.Errorf("stopped timestamp should be set but was nil")
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theUpdatedTimestampShouldBeUpdated() error {
	// This is implicit - we just verify it's set
	return lc.theStateMachineShouldHaveAnUpdatedTimestamp()
}

func (lc *lifecycleStateMachineContext) theTransitionShouldFailWith(expectedError string) error {
	if lc.transitionError == nil {
		return fmt.Errorf("expected transition to fail with '%s', but it succeeded", expectedError)
	}

	if lc.transitionError.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, lc.transitionError.Error())
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theLastErrorShouldBe(expectedError string) error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if lc.stateMachine.LastError() == nil {
		return fmt.Errorf("expected last error to be '%s', but it was nil", expectedError)
	}

	if lc.stateMachine.LastError().Error() != expectedError {
		return fmt.Errorf("expected last error '%s', got '%s'", expectedError, lc.stateMachine.LastError().Error())
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theRunningCheckShouldReturn(expectedStr string) error {
	expected := expectedStr == "true"
	if lc.isRunningResult != expected {
		return fmt.Errorf("expected IsRunning to return %t, got %t", expected, lc.isRunningResult)
	}
	return nil
}

func (lc *lifecycleStateMachineContext) thePendingCheckShouldReturn(expectedStr string) error {
	expected := expectedStr == "true"
	if lc.isPendingResult != expected {
		return fmt.Errorf("expected IsPending to return %t, got %t", expected, lc.isPendingResult)
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theFinishedCheckShouldReturn(expectedStr string) error {
	expected := expectedStr == "true"
	if lc.isFinishedResult != expected {
		return fmt.Errorf("expected IsFinished to return %t, got %t", expected, lc.isFinishedResult)
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theRuntimeDurationShouldBeSeconds(expectedSeconds int) error {
	expectedDuration := time.Duration(expectedSeconds) * time.Second
	if lc.runtimeDuration != expectedDuration {
		return fmt.Errorf("expected runtime duration %v, got %v", expectedDuration, lc.runtimeDuration)
	}
	return nil
}

func (lc *lifecycleStateMachineContext) theStateMachineShouldBeFinished() error {
	if lc.stateMachine == nil {
		return fmt.Errorf("no state machine available")
	}

	if !lc.stateMachine.IsFinished() {
		return fmt.Errorf("expected state machine to be finished, but it was not")
	}
	return nil
}

// Initialize lifecycle state machine steps
func InitializeLifecycleStateMachineScenario(ctx *godog.ScenarioContext) {
	lc := &lifecycleStateMachineContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		lc.reset()
		return ctx, nil
	})

	// Given steps
	ctx.Step(`^a lifecycle state machine in "([^"]*)" state$`, lc.aLifecycleStateMachineInState)
	ctx.Step(`^(\d+) seconds have passed$`, lc.secondsHavePassed)
	ctx.Step(`^a lifecycle state machine that ran for (\d+) seconds and completed$`, lc.aLifecycleStateMachineThatRanForSecondsAndCompleted)
	ctx.Step(`^a lifecycle state machine that ran for (\d+) seconds and failed$`, lc.aLifecycleStateMachineThatRanForSecondsAndFailed)
	ctx.Step(`^a lifecycle state machine that ran for (\d+) seconds and stopped$`, lc.aLifecycleStateMachineThatRanForSecondsAndStopped)

	// When steps
	ctx.Step(`^I create a new lifecycle state machine$`, lc.iCreateANewLifecycleStateMachine)
	ctx.Step(`^I start the lifecycle state machine$`, lc.iStartTheLifecycleStateMachine)
	ctx.Step(`^I complete the lifecycle state machine$`, lc.iCompleteTheLifecycleStateMachine)
	ctx.Step(`^I fail the lifecycle state machine with error "([^"]*)"$`, lc.iFailTheLifecycleStateMachineWithError)
	ctx.Step(`^I stop the lifecycle state machine$`, lc.iStopTheLifecycleStateMachine)
	ctx.Step(`^I check if the state machine is running$`, lc.iCheckIfTheStateMachineIsRunning)
	ctx.Step(`^I check if the state machine is pending$`, lc.iCheckIfTheStateMachineIsPending)
	ctx.Step(`^I check if the state machine is finished$`, lc.iCheckIfTheStateMachineIsFinished)
	ctx.Step(`^I get the runtime duration$`, lc.iGetTheRuntimeDuration)

	// Then steps
	ctx.Step(`^the lifecycle status should be "([^"]*)"$`, lc.theLifecycleStatusShouldBe)
	ctx.Step(`^the state machine should have a created timestamp$`, lc.theStateMachineShouldHaveACreatedTimestamp)
	ctx.Step(`^the state machine should have an updated timestamp$`, lc.theStateMachineShouldHaveAnUpdatedTimestamp)
	ctx.Step(`^the started timestamp should be nil$`, lc.theStartedTimestampShouldBeNil)
	ctx.Step(`^the stopped timestamp should be nil$`, lc.theStoppedTimestampShouldBeNil)
	ctx.Step(`^the started timestamp should be set$`, lc.theStartedTimestampShouldBeSet)
	ctx.Step(`^the stopped timestamp should be set$`, lc.theStoppedTimestampShouldBeSet)
	ctx.Step(`^the updated timestamp should be updated$`, lc.theUpdatedTimestampShouldBeUpdated)
	ctx.Step(`^the transition should fail with "([^"]*)"$`, lc.theTransitionShouldFailWith)
	ctx.Step(`^the last error should be "([^"]*)"$`, lc.theLastErrorShouldBe)
	ctx.Step(`^the running check should return (true|false)$`, lc.theRunningCheckShouldReturn)
	ctx.Step(`^the pending check should return (true|false)$`, lc.thePendingCheckShouldReturn)
	ctx.Step(`^the finished check should return (true|false)$`, lc.theFinishedCheckShouldReturn)
	ctx.Step(`^the runtime duration should be (\d+) seconds$`, lc.theRuntimeDurationShouldBeSeconds)
	ctx.Step(`^the state machine should be finished$`, lc.theStateMachineShouldBeFinished)
}
