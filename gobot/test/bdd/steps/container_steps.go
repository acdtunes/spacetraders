package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type containerContext struct {
	container      *container.Container
	err            error
	boolResult     bool
	metadata       map[string]interface{}
	metadataValue  interface{}
	metadataExists bool
	duration       time.Duration
	clock          *shared.MockClock
}

func (cc *containerContext) reset() {
	cc.container = nil
	cc.err = nil
	cc.boolResult = false
	cc.metadata = make(map[string]interface{})
	cc.metadataValue = nil
	cc.metadataExists = false
	cc.duration = 0
	// Initialize with current time
	cc.clock = shared.NewMockClock(time.Now())
}

// ============================================================================
// Container Creation Steps
// ============================================================================

func (cc *containerContext) iCreateAContainerWith(table *godog.Table) error {
	var id string
	var containerType container.ContainerType
	var playerID, maxIterations int
	metadata := make(map[string]interface{})

	for _, row := range table.Rows {
		key := row.Cells[0].Value
		val := row.Cells[1].Value

		switch key {
		case "id":
			id = val
		case "type":
			containerType = container.ContainerType(val)
		case "player_id":
			fmt.Sscanf(val, "%d", &playerID)
		case "max_iterations":
			fmt.Sscanf(val, "%d", &maxIterations)
		}
	}

	cc.container = container.NewContainer(id, containerType, playerID, maxIterations, metadata, cc.clock)
	return nil
}

func (cc *containerContext) iCreateAContainerWithMaxIterations(maxIterations int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		cc.clock,
	)
	return nil
}

func (cc *containerContext) iCreateAContainerWithMetadata(table *godog.Table) error {
	metadata := make(map[string]interface{})

	for _, row := range table.Rows {
		key := row.Cells[0].Value
		val := row.Cells[1].Value
		metadata[key] = val
	}

	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		metadata,
		cc.clock,
	)
	return nil
}

func (cc *containerContext) aContainerWithMetadata(table *godog.Table) error {
	return cc.iCreateAContainerWithMetadata(table)
}

func (cc *containerContext) aContainerWithEmptyMetadata() error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)
	return nil
}

// ============================================================================
// Container Status Steps
// ============================================================================

func (cc *containerContext) aContainerInStatus(status string) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)

	// Set status based on input
	switch status {
	case "RUNNING":
		cc.container.Start()
	case "COMPLETED":
		cc.container.Start()
		cc.container.Complete()
	case "FAILED":
		cc.container.Start()
		cc.container.Fail(errors.New("test error"))
	case "STOPPED":
		cc.container.Start()
		cc.container.Stop()
		cc.container.MarkStopped()
	case "STOPPING":
		cc.container.Start()
		cc.container.Stop()
	}

	return nil
}

func (cc *containerContext) aContainerInStatusWithCurrentIteration(status string, iteration int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)

	// Set status
	switch status {
	case "RUNNING":
		cc.container.Start()
	}

	// Manually set iteration (for testing purposes, accessing through reflection or direct field access)
	// Since we don't have a setter, we'll increment to reach the target
	for i := 0; i < iteration; i++ {
		cc.container.IncrementIteration()
	}

	return nil
}

func (cc *containerContext) aContainerInStatusWithMaxIterations(status string, maxIterations int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		cc.clock,
	)

	// Set status
	switch status {
	case "RUNNING":
		cc.container.Start()
	}

	return nil
}

func (cc *containerContext) aContainerInStatusWithRestartCount(status string, restartCount int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)

	// Set status and restart count
	for i := 0; i < restartCount; i++ {
		cc.container.Start()
		cc.container.Fail(errors.New("test error"))
		cc.container.ResetForRestart()
	}

	switch status {
	case "FAILED":
		cc.container.Start()
		cc.container.Fail(errors.New("test error"))
	}

	return nil
}

func (cc *containerContext) aContainerWithMaxIterationsAndCurrentIteration(maxIterations, currentIteration int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		cc.clock,
	)

	cc.container.Start()
	for i := 0; i < currentIteration; i++ {
		cc.container.IncrementIteration()
	}

	return nil
}

// ============================================================================
// Container Action Steps
// ============================================================================

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
	cc.err = cc.container.Fail(errors.New(errorMsg))
	return nil
}

func (cc *containerContext) iAttemptToFailTheContainer() error {
	cc.err = cc.container.Fail(errors.New("test error"))
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

func (cc *containerContext) iIncrementTheIteration() error {
	cc.err = cc.container.IncrementIteration()
	return nil
}

func (cc *containerContext) iAttemptToIncrementTheIteration() error {
	cc.err = cc.container.IncrementIteration()
	return nil
}

func (cc *containerContext) iResetTheContainerForRestart() error {
	cc.err = cc.container.ResetForRestart()
	return nil
}

func (cc *containerContext) iAttemptToResetTheContainerForRestart() error {
	cc.err = cc.container.ResetForRestart()
	return nil
}

func (cc *containerContext) iUpdateMetadataWith(table *godog.Table) error {
	metadata := make(map[string]interface{})

	for _, row := range table.Rows {
		key := row.Cells[0].Value
		val := row.Cells[1].Value
		metadata[key] = val
	}

	cc.container.UpdateMetadata(metadata)
	return nil
}

func (cc *containerContext) iGetMetadataValueForKey(key string) error {
	cc.metadataValue, cc.metadataExists = cc.container.GetMetadataValue(key)
	return nil
}

// ============================================================================
// Container Query Steps
// ============================================================================

func (cc *containerContext) iCheckIfTheContainerShouldContinue() error {
	cc.boolResult = cc.container.ShouldContinue()
	return nil
}

func (cc *containerContext) iCheckIfTheContainerCanRestart() error {
	cc.boolResult = cc.container.CanRestart()
	return nil
}

func (cc *containerContext) iCheckIfTheContainerIsRunning() error {
	cc.boolResult = cc.container.IsRunning()
	return nil
}

func (cc *containerContext) iCheckIfTheContainerIsFinished() error {
	cc.boolResult = cc.container.IsFinished()
	return nil
}

func (cc *containerContext) iCheckIfTheContainerIsStopping() error {
	cc.boolResult = cc.container.IsStopping()
	return nil
}

func (cc *containerContext) iCalculateTheRuntimeDuration() error {
	cc.duration = cc.container.RuntimeDuration()
	return nil
}

// ============================================================================
// Runtime Duration Setup Steps
// ============================================================================

func (cc *containerContext) aContainerThatHasNotBeenStarted() error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)
	return nil
}

func (cc *containerContext) aContainerThatStartedSecondsAgo(seconds int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)

	// Start the container
	cc.container.Start()

	// Advance the mock clock by N seconds (NO SLEEP!)
	cc.clock.Advance(time.Duration(seconds) * time.Second)

	return nil
}

func (cc *containerContext) aContainerThatStartedSecondsAgoAndStoppedSecondsLater(startSeconds, durationSeconds int) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		cc.clock,
	)

	// Start the container
	cc.container.Start()

	// Advance the mock clock by duration seconds (NO SLEEP!)
	cc.clock.Advance(time.Duration(durationSeconds) * time.Second)

	// Stop the container
	cc.container.Stop()
	cc.container.MarkStopped()

	return nil
}

// ============================================================================
// Assertion Steps
// ============================================================================

func (cc *containerContext) theContainerShouldHaveID(id string) error {
	if cc.container.ID() != id {
		return fmt.Errorf("expected id '%s' but got '%s'", id, cc.container.ID())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveType(containerType string) error {
	if string(cc.container.Type()) != containerType {
		return fmt.Errorf("expected type '%s' but got '%s'", containerType, cc.container.Type())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHavePlayerID(playerID int) error {
	if cc.container.PlayerID() != playerID {
		return fmt.Errorf("expected player_id %d but got %d", playerID, cc.container.PlayerID())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveMaxIterations(maxIterations int) error {
	if cc.container.MaxIterations() != maxIterations {
		return fmt.Errorf("expected max_iterations %d but got %d", maxIterations, cc.container.MaxIterations())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveStatus(status string) error {
	if string(cc.container.Status()) != status {
		return fmt.Errorf("expected status '%s' but got '%s'", status, cc.container.Status())
	}
	return nil
}

func (cc *containerContext) theContainerCurrentIterationShouldBe(iteration int) error {
	if cc.container.CurrentIteration() != iteration {
		return fmt.Errorf("expected current_iteration %d but got %d", iteration, cc.container.CurrentIteration())
	}
	return nil
}

func (cc *containerContext) theContainerRestartCountShouldBe(count int) error {
	if cc.container.RestartCount() != count {
		return fmt.Errorf("expected restart_count %d but got %d", count, cc.container.RestartCount())
	}
	return nil
}

func (cc *containerContext) theContainerStartedAtShouldBeSet() error {
	if cc.container.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set but it is nil")
	}
	return nil
}

func (cc *containerContext) theContainerStoppedAtShouldBeSet() error {
	if cc.container.StoppedAt() == nil {
		return fmt.Errorf("expected stopped_at to be set but it is nil")
	}
	return nil
}

func (cc *containerContext) theContainerStoppedAtShouldBeNil() error {
	if cc.container.StoppedAt() != nil {
		return fmt.Errorf("expected stopped_at to be nil but it is set")
	}
	return nil
}

func (cc *containerContext) theContainerLastErrorShouldBe(expectedError string) error {
	if cc.container.LastError() == nil {
		return fmt.Errorf("expected last_error '%s' but got nil", expectedError)
	}
	if cc.container.LastError().Error() != expectedError {
		return fmt.Errorf("expected last_error '%s' but got '%s'", expectedError, cc.container.LastError().Error())
	}
	return nil
}

func (cc *containerContext) theContainerLastErrorShouldBeNil() error {
	if cc.container.LastError() != nil {
		return fmt.Errorf("expected last_error to be nil but got '%s'", cc.container.LastError().Error())
	}
	return nil
}

func (cc *containerContext) theContainerShouldContinueRunning() error {
	if !cc.container.ShouldContinue() {
		return fmt.Errorf("expected container to continue running but it should not")
	}
	return nil
}

func (cc *containerContext) theContainerShouldNotContinueRunning() error {
	if cc.container.ShouldContinue() {
		return fmt.Errorf("expected container to not continue running but it should")
	}
	return nil
}

func (cc *containerContext) theContainerMetadataShouldContainWithValue(key, value string) error {
	val, exists := cc.container.GetMetadataValue(key)
	if !exists {
		return fmt.Errorf("expected metadata to contain key '%s' but it does not", key)
	}
	if val != value {
		return fmt.Errorf("expected metadata key '%s' to have value '%s' but got '%v'", key, value, val)
	}
	return nil
}

func (cc *containerContext) theMetadataValueShouldBe(expectedValue string) error {
	if !cc.metadataExists {
		return fmt.Errorf("expected metadata value '%s' but key does not exist", expectedValue)
	}
	if cc.metadataValue != expectedValue {
		return fmt.Errorf("expected metadata value '%s' but got '%v'", expectedValue, cc.metadataValue)
	}
	return nil
}

func (cc *containerContext) theMetadataKeyShouldExist() error {
	if !cc.metadataExists {
		return fmt.Errorf("expected metadata key to exist but it does not")
	}
	return nil
}

func (cc *containerContext) theMetadataKeyShouldNotExist() error {
	if cc.metadataExists {
		return fmt.Errorf("expected metadata key to not exist but it does")
	}
	return nil
}

func (cc *containerContext) theRuntimeDurationShouldBeSeconds(seconds int) error {
	expected := time.Duration(seconds) * time.Second
	if cc.duration != expected {
		return fmt.Errorf("expected runtime duration %v but got %v", expected, cc.duration)
	}
	return nil
}

func (cc *containerContext) theRuntimeDurationShouldBeApproximatelySeconds(seconds int) error {
	expected := time.Duration(seconds) * time.Second
	tolerance := 2 * time.Second // Allow 2 second tolerance

	diff := cc.duration - expected
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		return fmt.Errorf("expected runtime duration approximately %v but got %v (diff: %v)", expected, cc.duration, diff)
	}
	return nil
}

func (cc *containerContext) theOperationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(cc.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, cc.err.Error())
	}
	return nil
}

// ============================================================================
// Shared Result Steps (from value_object_steps.go)
// ============================================================================

func (cc *containerContext) theResultShouldBe(expected bool) error {
	if cc.boolResult != expected {
		return fmt.Errorf("expected result %v but got %v", expected, cc.boolResult)
	}
	return nil
}

// ============================================================================
// Step Registration
// ============================================================================

// InitializeContainerScenario registers all container-related step definitions
func InitializeContainerScenario(ctx *godog.ScenarioContext) {
	cc := &containerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		cc.reset()
		return ctx, nil
	})

	// Container creation steps
	ctx.Step(`^I create a container with:$`, cc.iCreateAContainerWith)
	ctx.Step(`^I create a container with max_iterations (-?\d+)$`, cc.iCreateAContainerWithMaxIterations)
	ctx.Step(`^I create a container with metadata:$`, cc.iCreateAContainerWithMetadata)
	ctx.Step(`^a container with metadata:$`, cc.aContainerWithMetadata)
	ctx.Step(`^a container with empty metadata$`, cc.aContainerWithEmptyMetadata)

	// Container status steps
	ctx.Step(`^a container in "([^"]*)" status$`, cc.aContainerInStatus)
	ctx.Step(`^a container in "([^"]*)" status with current_iteration (\d+)$`, cc.aContainerInStatusWithCurrentIteration)
	ctx.Step(`^a container in "([^"]*)" status with max_iterations (\d+)$`, cc.aContainerInStatusWithMaxIterations)
	ctx.Step(`^a container in "([^"]*)" status with restart_count (\d+)$`, cc.aContainerInStatusWithRestartCount)
	ctx.Step(`^a container with max_iterations (-?\d+) and current_iteration (\d+)$`, cc.aContainerWithMaxIterationsAndCurrentIteration)

	// Container action steps
	ctx.Step(`^I start the container$`, cc.iStartTheContainer)
	ctx.Step(`^I attempt to start the container$`, cc.iAttemptToStartTheContainer)
	ctx.Step(`^I complete the container$`, cc.iCompleteTheContainer)
	ctx.Step(`^I attempt to complete the container$`, cc.iAttemptToCompleteTheContainer)
	ctx.Step(`^I fail the container with error "([^"]*)"$`, cc.iFailTheContainerWithError)
	ctx.Step(`^I attempt to fail the container$`, cc.iAttemptToFailTheContainer)
	ctx.Step(`^I stop the container$`, cc.iStopTheContainer)
	ctx.Step(`^I attempt to stop the container$`, cc.iAttemptToStopTheContainer)
	ctx.Step(`^I mark the container as stopped$`, cc.iMarkTheContainerAsStopped)
	ctx.Step(`^I attempt to mark the container as stopped$`, cc.iAttemptToMarkTheContainerAsStopped)
	ctx.Step(`^I increment the iteration$`, cc.iIncrementTheIteration)
	ctx.Step(`^I attempt to increment the iteration$`, cc.iAttemptToIncrementTheIteration)
	ctx.Step(`^I reset the container for restart$`, cc.iResetTheContainerForRestart)
	ctx.Step(`^I attempt to reset the container for restart$`, cc.iAttemptToResetTheContainerForRestart)
	ctx.Step(`^I update metadata with:$`, cc.iUpdateMetadataWith)
	ctx.Step(`^I get metadata value for key "([^"]*)"$`, cc.iGetMetadataValueForKey)

	// Container query steps
	ctx.Step(`^I check if the container should continue$`, cc.iCheckIfTheContainerShouldContinue)
	ctx.Step(`^I check if the container can restart$`, cc.iCheckIfTheContainerCanRestart)
	ctx.Step(`^I check if the container is running$`, cc.iCheckIfTheContainerIsRunning)
	ctx.Step(`^I check if the container is finished$`, cc.iCheckIfTheContainerIsFinished)
	ctx.Step(`^I check if the container is stopping$`, cc.iCheckIfTheContainerIsStopping)
	ctx.Step(`^I calculate the runtime duration$`, cc.iCalculateTheRuntimeDuration)

	// Runtime duration setup steps
	ctx.Step(`^a container that has not been started$`, cc.aContainerThatHasNotBeenStarted)
	ctx.Step(`^a container that started (\d+) seconds ago$`, cc.aContainerThatStartedSecondsAgo)
	ctx.Step(`^a container that started (\d+) seconds ago and stopped (\d+) seconds later$`, cc.aContainerThatStartedSecondsAgoAndStoppedSecondsLater)

	// Assertion steps
	ctx.Step(`^the container should have id "([^"]*)"$`, cc.theContainerShouldHaveID)
	ctx.Step(`^the container should have type "([^"]*)"$`, cc.theContainerShouldHaveType)
	ctx.Step(`^the container should have player_id (\d+)$`, cc.theContainerShouldHavePlayerID)
	ctx.Step(`^the container should have max_iterations (-?\d+)$`, cc.theContainerShouldHaveMaxIterations)
	ctx.Step(`^the container should have status "([^"]*)"$`, cc.theContainerShouldHaveStatus)
	ctx.Step(`^the container current_iteration should be (\d+)$`, cc.theContainerCurrentIterationShouldBe)
	ctx.Step(`^the container restart_count should be (\d+)$`, cc.theContainerRestartCountShouldBe)
	ctx.Step(`^the container started_at should be set$`, cc.theContainerStartedAtShouldBeSet)
	ctx.Step(`^the container stopped_at should be set$`, cc.theContainerStoppedAtShouldBeSet)
	ctx.Step(`^the container stopped_at should be nil$`, cc.theContainerStoppedAtShouldBeNil)
	ctx.Step(`^the container last_error should be "([^"]*)"$`, cc.theContainerLastErrorShouldBe)
	ctx.Step(`^the container last_error should be nil$`, cc.theContainerLastErrorShouldBeNil)
	ctx.Step(`^the container should continue running$`, cc.theContainerShouldContinueRunning)
	ctx.Step(`^the container should not continue running$`, cc.theContainerShouldNotContinueRunning)
	ctx.Step(`^the container metadata should contain "([^"]*)" with value "([^"]*)"$`, cc.theContainerMetadataShouldContainWithValue)
	ctx.Step(`^the metadata value should be "([^"]*)"$`, cc.theMetadataValueShouldBe)
	ctx.Step(`^the metadata key should exist$`, cc.theMetadataKeyShouldExist)
	ctx.Step(`^the metadata key should not exist$`, cc.theMetadataKeyShouldNotExist)
	ctx.Step(`^the runtime duration should be (\d+) seconds$`, cc.theRuntimeDurationShouldBeSeconds)
	ctx.Step(`^the runtime duration should be approximately (\d+) seconds$`, cc.theRuntimeDurationShouldBeApproximatelySeconds)
	ctx.Step(`^the operation should fail with error "([^"]*)"$`, cc.theOperationShouldFailWithError)

	// Shared result steps
	ctx.Step(`^the result should be (true|false)$`, func(expected string) error {
		return cc.theResultShouldBe(expected == "true")
	})
}
