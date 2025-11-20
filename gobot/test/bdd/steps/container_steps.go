package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type containerContext struct {
	container          *container.Container
	assignment         *container.ShipAssignment
	assignmentManager  *container.ShipAssignmentManager
	err                error
	boolResult         bool
	intResult          int
	durationResult     time.Duration
	metadataValue      interface{}
	metadataExists     bool
	clock              *shared.MockClock
	existingContainers map[string]bool
}

func (cc *containerContext) reset() {
	cc.container = nil
	cc.assignment = nil
	cc.assignmentManager = nil
	cc.err = nil
	cc.boolResult = false
	cc.intResult = 0
	cc.durationResult = 0
	cc.metadataValue = nil
	cc.metadataExists = false
	cc.clock = shared.NewMockClock(time.Now())
	cc.existingContainers = make(map[string]bool)
}

// Container Creation Steps

func (cc *containerContext) iCreateAContainerWithIDTypePlayerMaxIterations(
	id string, containerType string, playerID int, maxIterations int,
) error {
	ct := container.ContainerType(containerType)
	cc.container = container.NewContainer(id, ct, playerID, maxIterations, nil, cc.clock)
	return nil
}

func (cc *containerContext) iCreateAContainerWithIDTypePlayerMaxIterationsMetadata(
	id string, containerType string, playerID int, maxIterations int, table *godog.Table,
) error {
	metadata := make(map[string]interface{})
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header row
		}
		key := row.Cells[0].Value
		value := row.Cells[1].Value
		metadata[key] = value
	}

	ct := container.ContainerType(containerType)
	cc.container = container.NewContainer(id, ct, playerID, maxIterations, metadata, cc.clock)
	return nil
}

// State Setup Steps

func (cc *containerContext) aContainerInState(status string) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	switch status {
	case "PENDING":
		// Already in pending state
	case "RUNNING":
		return cc.container.Start()
	case "COMPLETED":
		_ = cc.container.Start()
		return cc.container.Complete()
	case "FAILED":
		_ = cc.container.Start()
		return cc.container.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		// Stop from PENDING goes directly to STOPPED
		return cc.container.Stop()
	case "STOPPING":
		_ = cc.container.Start()
		return cc.container.Stop()
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (cc *containerContext) aContainerInStateWithRestartCount(status string, restartCount int) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	// Manually set restart count by cycling through restart attempts
	for i := 0; i < restartCount; i++ {
		cc.container.IncrementRestartCount()
	}

	switch status {
	case "FAILED":
		_ = cc.container.Start()
		return cc.container.Fail(fmt.Errorf("test error"))
	case "RUNNING":
		return cc.container.Start()
	case "PENDING":
		// Already pending
	case "COMPLETED":
		_ = cc.container.Start()
		return cc.container.Complete()
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (cc *containerContext) aContainerInStateAtIteration(status string, iteration int) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	// For PENDING state with iteration, can't advance iteration (must be RUNNING to increment)
	if status == "PENDING" {
		// Just create the container in PENDING state
		// Note: iteration parameter is ignored for PENDING state
		return nil
	}

	// Start and advance to desired iteration
	_ = cc.container.Start()
	for i := 0; i < iteration; i++ {
		_ = cc.container.IncrementIteration()
	}

	// Transition to target state (if not running)
	switch status {
	case "RUNNING":
		// Already running
	case "COMPLETED":
		return cc.container.Complete()
	case "STOPPED":
		// Stop requires two-phase: STOPPING -> STOPPED
		_ = cc.container.Stop()
		return cc.container.MarkStopped()
	case "FAILED":
		return cc.container.Fail(fmt.Errorf("test error"))
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (cc *containerContext) aContainerWithMaxIterationsAtIteration(maxIterations, currentIteration int) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		maxIterations,
		nil,
		cc.clock,
	)

	// Start the container and advance to desired iteration
	if currentIteration > 0 {
		_ = cc.container.Start()
		for i := 0; i < currentIteration; i++ {
			_ = cc.container.IncrementIteration()
		}
	} else {
		// For iteration 0, just start to allow ShouldContinue to work
		_ = cc.container.Start()
	}

	return nil
}

func (cc *containerContext) aContainerWithIDInStateWithRestartCount(id string, status string, restartCount int) error {
	cc.container = container.NewContainer(
		id,
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	// Set restart count
	for i := 0; i < restartCount; i++ {
		cc.container.IncrementRestartCount()
	}

	// Transition to target state
	switch status {
	case "FAILED":
		_ = cc.container.Start()
		return cc.container.Fail(fmt.Errorf("test error"))
	case "RUNNING":
		return cc.container.Start()
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

// Metadata Setup Steps

func (cc *containerContext) aContainerWithNoMetadata() error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)
	return nil
}

func (cc *containerContext) aContainerWithMetadata(table *godog.Table) error {
	metadata := make(map[string]interface{})
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		key := row.Cells[0].Value
		value := row.Cells[1].Value
		metadata[key] = value
	}

	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		metadata,
		cc.clock,
	)
	return nil
}

// Time Setup Steps

func (cc *containerContext) aContainerStartedMinutesAgoInState(minutes int, status string) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	// Start the container
	_ = cc.container.Start()

	// Advance clock to simulate elapsed time
	cc.clock.Advance(time.Duration(minutes) * time.Minute)

	// Transition to target state if needed
	switch status {
	case "RUNNING":
		// Already running
	case "COMPLETED", "FAILED", "STOPPED":
		return fmt.Errorf("use different step for finished containers")
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (cc *containerContext) aContainerThatRanForMinutesAndIsNow(minutes int, status string) error {
	cc.container = container.NewContainer(
		"test-container",
		container.ContainerTypeMining,
		1,
		10,
		nil,
		cc.clock,
	)

	// Start the container
	_ = cc.container.Start()

	// Advance clock to simulate runtime
	cc.clock.Advance(time.Duration(minutes) * time.Minute)

	// Transition to final state
	switch status {
	case "COMPLETED":
		return cc.container.Complete()
	case "FAILED":
		return cc.container.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		return cc.container.Stop()
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

// Action Steps

func (cc *containerContext) iStartTheContainer() error {
	cc.err = cc.container.Start()
	return nil
}

func (cc *containerContext) iAttemptToStartTheContainer() error {
	cc.err = cc.container.Start()
	return nil
}

func (cc *containerContext) iCompleteTheContainer() error {
	cc.err = cc.container.Complete()
	return nil
}

func (cc *containerContext) iAttemptToCompleteTheContainer() error {
	cc.err = cc.container.Complete()
	return nil
}

func (cc *containerContext) iFailTheContainerWithError(errorMsg string) error {
	cc.err = cc.container.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (cc *containerContext) iAttemptToFailTheContainerWithError(errorMsg string) error {
	cc.err = cc.container.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (cc *containerContext) iStopTheContainer() error {
	cc.err = cc.container.Stop()
	return nil
}

func (cc *containerContext) iAttemptToStopTheContainer() error {
	cc.err = cc.container.Stop()
	return nil
}

func (cc *containerContext) iMarkTheContainerAsStopped() error {
	cc.err = cc.container.MarkStopped()
	return nil
}

func (cc *containerContext) iAttemptToMarkTheContainerAsStopped() error {
	cc.err = cc.container.MarkStopped()
	return nil
}

func (cc *containerContext) iIncrementTheContainerIteration() error {
	cc.err = cc.container.IncrementIteration()
	return nil
}

func (cc *containerContext) iAttemptToIncrementTheContainerIteration() error {
	cc.err = cc.container.IncrementIteration()
	return nil
}

func (cc *containerContext) iCheckIfContainerShouldContinue() error {
	cc.boolResult = cc.container.ShouldContinue()
	return nil
}

func (cc *containerContext) iCheckIfContainerCanRestart() error {
	cc.boolResult = cc.container.CanRestart()
	return nil
}

func (cc *containerContext) iResetContainerForRestart() error {
	cc.err = cc.container.ResetForRestart()
	return nil
}

func (cc *containerContext) iAttemptToResetContainerForRestart() error {
	cc.err = cc.container.ResetForRestart()
	return nil
}

func (cc *containerContext) iUpdateContainerMetadataWith(table *godog.Table) error {
	updates := make(map[string]interface{})
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		key := row.Cells[0].Value
		value := row.Cells[1].Value
		updates[key] = value
	}

	cc.container.UpdateMetadata(updates)
	return nil
}

func (cc *containerContext) iGetContainerMetadataValueForKey(key string) error {
	cc.metadataValue, cc.metadataExists = cc.container.GetMetadataValue(key)
	return nil
}

func (cc *containerContext) iCalculateContainerRuntimeDuration() error {
	cc.durationResult = cc.container.RuntimeDuration()
	return nil
}

func (cc *containerContext) iCheckIfContainerIsRunning() error {
	cc.boolResult = cc.container.IsRunning()
	return nil
}

func (cc *containerContext) iCheckIfContainerIsFinished() error {
	cc.boolResult = cc.container.IsFinished()
	return nil
}

func (cc *containerContext) iCheckIfContainerIsStopping() error {
	cc.boolResult = cc.container.IsStopping()
	return nil
}

func (cc *containerContext) whenIIncrementTheContainerIteration() error {
	return cc.iIncrementTheContainerIteration()
}

// Assertion Steps

func (cc *containerContext) theContainerStatusShouldBe(expectedStatus string) error {
	actualStatus := string(cc.container.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (cc *containerContext) theContainerCurrentIterationShouldBe(expected int) error {
	actual := cc.container.CurrentIteration()
	if actual != expected {
		return fmt.Errorf("expected current iteration %d, got %d", expected, actual)
	}
	return nil
}

func (cc *containerContext) theContainerRestartCountShouldBe(expected int) error {
	actual := cc.container.RestartCount()
	if actual != expected {
		return fmt.Errorf("expected restart count %d, got %d", expected, actual)
	}
	return nil
}

func (cc *containerContext) theContainerMaxIterationsShouldBe(expected int) error {
	actual := cc.container.MaxIterations()
	if actual != expected {
		return fmt.Errorf("expected max iterations %d, got %d", expected, actual)
	}
	return nil
}

func (cc *containerContext) theContainerStartedAtShouldBeNil() error {
	if cc.container.StartedAt() != nil {
		return fmt.Errorf("expected started_at to be nil, but it was set")
	}
	return nil
}

func (cc *containerContext) theContainerStartedAtShouldNotBeNil() error {
	if cc.container.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set, but it was nil")
	}
	return nil
}

func (cc *containerContext) theContainerStoppedAtShouldBeNil() error {
	if cc.container.StoppedAt() != nil {
		return fmt.Errorf("expected stopped_at to be nil, but it was set")
	}
	return nil
}

func (cc *containerContext) theContainerStoppedAtShouldNotBeNil() error {
	if cc.container.StoppedAt() == nil {
		return fmt.Errorf("expected stopped_at to be set, but it was nil")
	}
	return nil
}

func (cc *containerContext) theContainerLastErrorShouldBe(expectedError string) error {
	if cc.container.LastError() == nil {
		return fmt.Errorf("expected error '%s', got nil", expectedError)
	}
	actual := cc.container.LastError().Error()
	if actual != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, actual)
	}
	return nil
}

func (cc *containerContext) theContainerLastErrorShouldBeNil() error {
	if cc.container.LastError() != nil {
		return fmt.Errorf("expected error to be nil, got '%s'", cc.container.LastError().Error())
	}
	return nil
}

func (cc *containerContext) theContainerMetadataShouldContainWithValue(key, expectedValue string) error {
	value, exists := cc.container.GetMetadataValue(key)
	if !exists {
		return fmt.Errorf("metadata key '%s' does not exist", key)
	}
	if value != expectedValue {
		return fmt.Errorf("expected metadata '%s' = '%s', got '%s'", key, expectedValue, value)
	}
	return nil
}

func (cc *containerContext) theMetadataValueShouldBe(expectedValue string) error {
	if cc.metadataValue != expectedValue {
		return fmt.Errorf("expected metadata value '%s', got '%v'", expectedValue, cc.metadataValue)
	}
	return nil
}

func (cc *containerContext) theMetadataKeyShouldExist() error {
	if !cc.metadataExists {
		return fmt.Errorf("expected metadata key to exist, but it does not")
	}
	return nil
}

func (cc *containerContext) theMetadataKeyShouldNotExist() error {
	if cc.metadataExists {
		return fmt.Errorf("expected metadata key to not exist, but it does")
	}
	return nil
}

func (cc *containerContext) theContainerIDShouldBe(expected string) error {
	actual := cc.container.ID()
	if actual != expected {
		return fmt.Errorf("expected container ID '%s', got '%s'", expected, actual)
	}
	return nil
}

func (cc *containerContext) theContainerTypeShouldBe(expected string) error {
	actual := string(cc.container.Type())
	if actual != expected {
		return fmt.Errorf("expected container type '%s', got '%s'", expected, actual)
	}
	return nil
}

func (cc *containerContext) theContainerPlayerIDShouldBe(expected int) error {
	actual := cc.container.PlayerID()
	if actual != expected {
		return fmt.Errorf("expected player ID %d, got %d", expected, actual)
	}
	return nil
}

// Container-specific boolean assertions to avoid conflicts with value_object_steps

func (cc *containerContext) theContainerShouldContinue() error {
	if !cc.boolResult {
		return fmt.Errorf("expected container to continue, but it should not")
	}
	return nil
}

func (cc *containerContext) theContainerShouldNotContinue() error {
	if cc.boolResult {
		return fmt.Errorf("expected container to not continue, but it should")
	}
	return nil
}

func (cc *containerContext) theContainerCanRestart() error {
	if !cc.boolResult {
		return fmt.Errorf("expected container to be able to restart, but it cannot")
	}
	return nil
}

func (cc *containerContext) theContainerCannotRestart() error {
	if cc.boolResult {
		return fmt.Errorf("expected container to not be able to restart, but it can")
	}
	return nil
}

func (cc *containerContext) theContainerIsRunning() error {
	if !cc.boolResult {
		return fmt.Errorf("expected container to be running, but it is not")
	}
	return nil
}

func (cc *containerContext) theContainerIsNotRunning() error {
	if cc.boolResult {
		return fmt.Errorf("expected container to not be running, but it is")
	}
	return nil
}

func (cc *containerContext) theContainerIsFinished() error {
	if !cc.boolResult {
		return fmt.Errorf("expected container to be finished, but it is not")
	}
	return nil
}

func (cc *containerContext) theContainerIsNotFinished() error {
	if cc.boolResult {
		return fmt.Errorf("expected container to not be finished, but it is")
	}
	return nil
}

func (cc *containerContext) theContainerIsStopping() error {
	if !cc.boolResult {
		return fmt.Errorf("expected container to be stopping, but it is not")
	}
	return nil
}

func (cc *containerContext) theContainerIsNotStopping() error {
	if cc.boolResult {
		return fmt.Errorf("expected container to not be stopping, but it is")
	}
	return nil
}

func (cc *containerContext) theDurationShouldBeSeconds(expectedSeconds int) error {
	expected := time.Duration(expectedSeconds) * time.Second
	if cc.durationResult != expected {
		return fmt.Errorf("expected duration %v, got %v", expected, cc.durationResult)
	}
	return nil
}

func (cc *containerContext) theDurationShouldBeApproximatelySeconds(expectedSeconds int) error {
	expected := time.Duration(expectedSeconds) * time.Second
	tolerance := 1 * time.Second // Allow 1 second tolerance

	diff := cc.durationResult - expected
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		return fmt.Errorf("expected duration ~%v, got %v (diff: %v)", expected, cc.durationResult, diff)
	}
	return nil
}

func (cc *containerContext) theContainerOperationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error '%s', but operation succeeded", expectedError)
	}
	actualError := cc.err.Error()
	if actualError != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, actualError)
	}
	return nil
}

// Register container steps with godog
func RegisterContainerSteps(ctx *godog.ScenarioContext) {
	cc := &containerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		cc.reset()
		return ctx, nil
	})

	// Creation steps
	ctx.Step(`^I create a container with id "([^"]*)", type "([^"]*)", player (\d+), max_iterations (-?\d+)$`,
		cc.iCreateAContainerWithIDTypePlayerMaxIterations)
	ctx.Step(`^I create a container with id "([^"]*)", type "([^"]*)", player (\d+), max_iterations (-?\d+), metadata:$`,
		cc.iCreateAContainerWithIDTypePlayerMaxIterationsMetadata)

	// State setup steps
	ctx.Step(`^a container in "([^"]*)" state$`, cc.aContainerInState)
	ctx.Step(`^a container in "([^"]*)" state with restart_count (\d+)$`, cc.aContainerInStateWithRestartCount)
	ctx.Step(`^a container in "([^"]*)" state at iteration (\d+)$`, cc.aContainerInStateAtIteration)
	ctx.Step(`^a container with max_iterations (-?\d+) at iteration (\d+)$`, cc.aContainerWithMaxIterationsAtIteration)
	ctx.Step(`^a container with id "([^"]*)" in "([^"]*)" state with restart_count (\d+)$`, cc.aContainerWithIDInStateWithRestartCount)
	ctx.Step(`^a container with no metadata$`, cc.aContainerWithNoMetadata)
	ctx.Step(`^a container with metadata:$`, cc.aContainerWithMetadata)
	ctx.Step(`^a container started (\d+) minutes ago in "([^"]*)" state$`, cc.aContainerStartedMinutesAgoInState)
	ctx.Step(`^a container that ran for (\d+) minutes and is now "([^"]*)"$`, cc.aContainerThatRanForMinutesAndIsNow)

	// Action steps
	ctx.Step(`^I start the container$`, cc.iStartTheContainer)
	ctx.Step(`^I attempt to start the container$`, cc.iAttemptToStartTheContainer)
	ctx.Step(`^I complete the container$`, cc.iCompleteTheContainer)
	ctx.Step(`^I attempt to complete the container$`, cc.iAttemptToCompleteTheContainer)
	ctx.Step(`^I fail the container with error "([^"]*)"$`, cc.iFailTheContainerWithError)
	ctx.Step(`^I attempt to fail the container with error "([^"]*)"$`, cc.iAttemptToFailTheContainerWithError)
	ctx.Step(`^I stop the container$`, cc.iStopTheContainer)
	ctx.Step(`^I attempt to stop the container$`, cc.iAttemptToStopTheContainer)
	ctx.Step(`^I mark the container as stopped$`, cc.iMarkTheContainerAsStopped)
	ctx.Step(`^I attempt to mark the container as stopped$`, cc.iAttemptToMarkTheContainerAsStopped)
	ctx.Step(`^I increment the container iteration$`, cc.iIncrementTheContainerIteration)
	ctx.Step(`^I attempt to increment the container iteration$`, cc.iAttemptToIncrementTheContainerIteration)
	ctx.Step(`^I check if container should continue$`, cc.iCheckIfContainerShouldContinue)
	ctx.Step(`^when I increment the container iteration$`, cc.whenIIncrementTheContainerIteration)
	ctx.Step(`^I check if container can restart$`, cc.iCheckIfContainerCanRestart)
	ctx.Step(`^I reset container for restart$`, cc.iResetContainerForRestart)
	ctx.Step(`^I attempt to reset container for restart$`, cc.iAttemptToResetContainerForRestart)
	ctx.Step(`^I update container metadata with:$`, cc.iUpdateContainerMetadataWith)
	ctx.Step(`^I get container metadata value for key "([^"]*)"$`, cc.iGetContainerMetadataValueForKey)
	ctx.Step(`^I calculate container runtime duration$`, cc.iCalculateContainerRuntimeDuration)
	ctx.Step(`^I check if container is running$`, cc.iCheckIfContainerIsRunning)
	ctx.Step(`^I check if container is finished$`, cc.iCheckIfContainerIsFinished)
	ctx.Step(`^I check if container is stopping$`, cc.iCheckIfContainerIsStopping)

	// Assertion steps
	ctx.Step(`^the container status should be "([^"]*)"$`, cc.theContainerStatusShouldBe)
	ctx.Step(`^the container current iteration should be (\d+)$`, cc.theContainerCurrentIterationShouldBe)
	ctx.Step(`^the container restart count should be (\d+)$`, cc.theContainerRestartCountShouldBe)
	ctx.Step(`^the container max iterations should be (-?\d+)$`, cc.theContainerMaxIterationsShouldBe)
	ctx.Step(`^the container started_at should be nil$`, cc.theContainerStartedAtShouldBeNil)
	ctx.Step(`^the container started_at should not be nil$`, cc.theContainerStartedAtShouldNotBeNil)
	ctx.Step(`^the container stopped_at should be nil$`, cc.theContainerStoppedAtShouldBeNil)
	ctx.Step(`^the container stopped_at should not be nil$`, cc.theContainerStoppedAtShouldNotBeNil)
	ctx.Step(`^the container last_error should be "([^"]*)"$`, cc.theContainerLastErrorShouldBe)
	ctx.Step(`^the container last_error should be nil$`, cc.theContainerLastErrorShouldBeNil)
	ctx.Step(`^the container metadata should contain "([^"]*)" with value "([^"]*)"$`, cc.theContainerMetadataShouldContainWithValue)
	ctx.Step(`^the metadata value should be "([^"]*)"$`, cc.theMetadataValueShouldBe)
	ctx.Step(`^the metadata key should exist$`, cc.theMetadataKeyShouldExist)
	ctx.Step(`^the metadata key should not exist$`, cc.theMetadataKeyShouldNotExist)
	ctx.Step(`^the container id should be "([^"]*)"$`, cc.theContainerIDShouldBe)
	ctx.Step(`^the container type should be "([^"]*)"$`, cc.theContainerTypeShouldBe)
	ctx.Step(`^the container player_id should be (\d+)$`, cc.theContainerPlayerIDShouldBe)
	ctx.Step(`^the duration should be (\d+) seconds$`, cc.theDurationShouldBeSeconds)
	ctx.Step(`^the duration should be approximately (\d+) seconds$`, cc.theDurationShouldBeApproximatelySeconds)
	ctx.Step(`^the container operation should fail with error "([^"]*)"$`, cc.theContainerOperationShouldFailWithError)

	// Container-specific boolean assertions
	ctx.Step(`^the container should continue$`, cc.theContainerShouldContinue)
	ctx.Step(`^the container should not continue$`, cc.theContainerShouldNotContinue)
	ctx.Step(`^the container can restart$`, cc.theContainerCanRestart)
	ctx.Step(`^the container cannot restart$`, cc.theContainerCannotRestart)
	ctx.Step(`^the container is running$`, cc.theContainerIsRunning)
	ctx.Step(`^the container is not running$`, cc.theContainerIsNotRunning)
	ctx.Step(`^the container is finished$`, cc.theContainerIsFinished)
	ctx.Step(`^the container is not finished$`, cc.theContainerIsNotFinished)
	ctx.Step(`^the container is stopping$`, cc.theContainerIsStopping)
	ctx.Step(`^the container is not stopping$`, cc.theContainerIsNotStopping)
}
