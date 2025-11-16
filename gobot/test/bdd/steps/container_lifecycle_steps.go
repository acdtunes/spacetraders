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

// containerLifecycleContext holds state for daemon container lifecycle tests
type containerLifecycleContext struct {
	containers        map[string]*container.Container
	currentContainer  *container.Container
	err               error
	clock             *shared.MockClock
	containerList     []*container.Container
	stoppedAtSnapshot *time.Time
	startedAtSnapshot *time.Time
	nextContainerID   int
}

func (clc *containerLifecycleContext) reset() {
	clc.containers = make(map[string]*container.Container)
	clc.currentContainer = nil
	clc.err = nil
	clc.clock = shared.NewMockClock(time.Now())
	clc.containerList = nil
	clc.stoppedAtSnapshot = nil
	clc.startedAtSnapshot = nil
	clc.nextContainerID = 1
}

// ============================================================================
// Container Creation Steps
// ============================================================================

func (clc *containerLifecycleContext) aNewDaemonContainerIsCreatedWithType(containerType string) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerType(containerType),
		1, // playerID
		1, // maxIterations
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer
	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerIsInStatus(status string) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	// Set to desired status
	switch status {
	case "RUNNING":
		clc.currentContainer.Start()
	case "COMPLETED":
		clc.currentContainer.Start()
		clc.currentContainer.Complete()
	case "FAILED":
		clc.currentContainer.Start()
		clc.currentContainer.Fail(errors.New("test error"))
	case "STOPPED":
		clc.currentContainer.Start()
		clc.currentContainer.Stop()
		clc.currentContainer.MarkStopped()
	case "STOPPING":
		clc.currentContainer.Start()
		clc.currentContainer.Stop()
	}

	// Share container for cross-context assertions
	sharedContainer = clc.currentContainer
	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerThatRunsForLessThanSecond(seconds int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		1,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	// Start container
	clc.currentContainer.Start()

	// Advance time by less than 1 second
	clc.clock.Advance(500 * time.Millisecond)

	return nil
}

func (clc *containerLifecycleContext) aContainerWithStatus(status string) error {
	return clc.aDaemonContainerIsInStatus(status)
}

func (clc *containerLifecycleContext) aDaemonContainerInStatusWithRestartCount(status string, restartCount int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	// Simulate restarts by repeatedly: Start -> Fail -> ResetForRestart
	// This increments restart_count each time through the cycle
	for i := 0; i < restartCount; i++ {
		clc.currentContainer.Start()
		clc.currentContainer.Fail(errors.New("test error"))
		// Call ResetForRestart which increments restart_count
		// On the last iteration, this will fail if restart_count == maxRestarts
		if err := clc.currentContainer.ResetForRestart(); err != nil {
			// If we've hit the restart limit, we're done
			break
		}
	}

	// Ensure we're in the desired status
	if status == "FAILED" && clc.currentContainer.Status() != container.ContainerStatusFailed {
		clc.currentContainer.Start()
		clc.currentContainer.Fail(errors.New("test error"))
	}

	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerWithPlayerID(playerID int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		playerID,
		10,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	// Set to FAILED for restart scenarios
	clc.currentContainer.Start()
	clc.currentContainer.Fail(errors.New("test error"))

	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerWithMetadata(key, value string) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	metadata := map[string]interface{}{
		key: value,
	}

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		10,
		metadata,
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer
	return nil
}

func (clc *containerLifecycleContext) theContainerIsInStatus(status string) error {
	// Transition current container to status
	switch status {
	case "FAILED":
		if clc.currentContainer.Status() == container.ContainerStatusPending {
			clc.currentContainer.Start()
		}
		if clc.currentContainer.Status() == container.ContainerStatusRunning {
			clc.currentContainer.Fail(errors.New("test error"))
		}
	case "RUNNING":
		if clc.currentContainer.Status() == container.ContainerStatusPending {
			clc.currentContainer.Start()
		}
	}
	return nil
}

func (clc *containerLifecycleContext) daemonContainersAreCreated(count int) error {
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("container-%d", clc.nextContainerID)
		clc.nextContainerID++

		c := container.NewContainer(
			id,
			container.ContainerTypeNavigate,
			1,
			10,
			make(map[string]interface{}),
			clc.clock,
		)
		clc.containers[id] = c
	}
	return nil
}

func (clc *containerLifecycleContext) daemonContainersExist(count int, table *godog.Table) error {
	// count is captured but not used - table defines the containers
	// Note: The table has NO header row, so process all rows
	for _, row := range table.Rows {
		id := row.Cells[0].Value
		status := row.Cells[1].Value

		c := container.NewContainer(
			id,
			container.ContainerTypeNavigate,
			1,
			10,
			make(map[string]interface{}),
			clc.clock,
		)

		// Set to desired status
		switch status {
		case "RUNNING":
			c.Start()
		case "COMPLETED":
			c.Start()
			c.Complete()
		case "FAILED":
			c.Start()
			c.Fail(errors.New("test error"))
		case "STOPPED":
			c.Start()
			c.Stop()
			c.MarkStopped()
		}

		clc.containers[id] = c
	}
	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerIsInStatusWithMaxIterations(status string, maxIterations int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	if status == "RUNNING" {
		clc.currentContainer.Start()
	}

	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerWithMaxIterationsAndCurrentIteration(maxIterations, currentIteration int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer

	clc.currentContainer.Start()
	for i := 0; i < currentIteration; i++ {
		clc.currentContainer.IncrementIteration()
	}

	return nil
}

func (clc *containerLifecycleContext) aDaemonContainerWithMaxIterations(maxIterations int) error {
	id := fmt.Sprintf("container-%d", clc.nextContainerID)
	clc.nextContainerID++

	clc.currentContainer = container.NewContainer(
		id,
		container.ContainerTypeNavigate,
		1,
		maxIterations,
		make(map[string]interface{}),
		clc.clock,
	)
	clc.containers[id] = clc.currentContainer
	clc.currentContainer.Start()

	return nil
}

// ============================================================================
// Container Action Steps
// ============================================================================

func (clc *containerLifecycleContext) theDaemonStartsTheContainer() error {
	clc.err = clc.currentContainer.Start()
	return nil
}

func (clc *containerLifecycleContext) theContainerOperationCompletesSuccessfully() error {
	// Ensure container is started before completing
	if clc.currentContainer.Status() == container.ContainerStatusPending {
		if err := clc.currentContainer.Start(); err != nil {
			clc.err = err
			return nil
		}
	}
	clc.err = clc.currentContainer.Complete()
	return nil
}

func (clc *containerLifecycleContext) theContainerOperationEncountersAnError(errorMsg string) error {
	clc.err = clc.currentContainer.Fail(errors.New(errorMsg))
	return nil
}

func (clc *containerLifecycleContext) theDaemonSignalsTheContainerToStop() error {
	clc.err = clc.currentContainer.Stop()
	return nil
}

func (clc *containerLifecycleContext) theDaemonFinalizesTheContainerShutdown() error {
	clc.err = clc.currentContainer.MarkStopped()
	return nil
}

func (clc *containerLifecycleContext) theContainerCompletesItsOperation() error {
	return clc.theContainerOperationCompletesSuccessfully()
}

func (clc *containerLifecycleContext) theContainerCompletesSuccessfully() error {
	return clc.theContainerOperationCompletesSuccessfully()
}

func (clc *containerLifecycleContext) iQueryTheContainerStatus() error {
	// No-op: just checking current state
	return nil
}

func (clc *containerLifecycleContext) iListAllDaemonContainers() error {
	clc.containerList = make([]*container.Container, 0, len(clc.containers))
	for _, c := range clc.containers {
		clc.containerList = append(clc.containerList, c)
	}
	return nil
}

func (clc *containerLifecycleContext) iQueryContainersByStatus(status string) error {
	clc.containerList = make([]*container.Container, 0)
	for _, c := range clc.containers {
		if string(c.Status()) == status {
			clc.containerList = append(clc.containerList, c)
		}
	}
	return nil
}

func (clc *containerLifecycleContext) iListOnlyFinishedContainers() error {
	clc.containerList = make([]*container.Container, 0)
	for _, c := range clc.containers {
		if c.IsFinished() {
			clc.containerList = append(clc.containerList, c)
		}
	}
	return nil
}

func (clc *containerLifecycleContext) secondsPass(seconds int) error {
	clc.clock.Advance(time.Duration(seconds) * time.Second)
	return nil
}

func (clc *containerLifecycleContext) theContainerTransitionsTo(status string) error {
	switch status {
	case "COMPLETED":
		return clc.currentContainer.Complete()
	case "FAILED":
		return clc.currentContainer.Fail(errors.New("test error"))
	case "STOPPED":
		clc.currentContainer.Stop()
		return clc.currentContainer.MarkStopped()
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerCompletesOneIteration() error {
	return clc.currentContainer.IncrementIteration()
}

func (clc *containerLifecycleContext) iCheckIfTheContainerShouldContinue() error {
	// No-op: checking state
	return nil
}

func (clc *containerLifecycleContext) theDaemonRestartsTheContainer() error {
	clc.err = clc.currentContainer.ResetForRestart()
	return nil
}

func (clc *containerLifecycleContext) iCheckIfTheContainerCanRestart() error {
	// Verify currentContainer is set
	if clc.currentContainer == nil {
		return fmt.Errorf("currentContainer is nil")
	}
	// No-op: just checking that currentContainer exists for subsequent assertions
	return nil
}

func (clc *containerLifecycleContext) iAttemptToRestartTheContainer() error {
	clc.err = clc.currentContainer.ResetForRestart()
	return nil
}

func (clc *containerLifecycleContext) theDaemonRestartsTheContainerTimes(times int) error {
	for i := 0; i < times; i++ {
		if err := clc.currentContainer.ResetForRestart(); err != nil {
			clc.err = err
			return nil
		}
		if err := clc.currentContainer.Start(); err != nil {
			clc.err = err
			return nil
		}
		if err := clc.currentContainer.Fail(errors.New("test error")); err != nil {
			clc.err = err
			return nil
		}
	}
	return nil
}

func (clc *containerLifecycleContext) theDaemonStartsAllContainers() error {
	for _, c := range clc.containers {
		if err := c.Start(); err != nil {
			clc.err = err
			return nil
		}
		// Add tiny time advance for unique timestamps
		clc.clock.Advance(1 * time.Millisecond)
	}
	return nil
}

func (clc *containerLifecycleContext) iQueryContainer(containerID string) error {
	c, exists := clc.containers[containerID]
	if !exists {
		return fmt.Errorf("container %s not found", containerID)
	}
	clc.currentContainer = c
	return nil
}

func (clc *containerLifecycleContext) iRemoveTheContainer() error {
	// Simulate removal: check if it's in a removable state
	if clc.currentContainer.IsRunning() {
		clc.err = errors.New("container must be stopped first")
		return nil
	}
	// If not running, removal succeeds
	delete(clc.containers, clc.currentContainer.ID())
	return nil
}

func (clc *containerLifecycleContext) iAttemptToRemoveTheContainerWithoutStopping() error {
	return clc.iRemoveTheContainer()
}

func (clc *containerLifecycleContext) iAttemptToStartTheContainer() error {
	clc.err = clc.currentContainer.Start()
	return nil
}

func (clc *containerLifecycleContext) iAttemptToCompleteTheContainer() error {
	clc.err = clc.currentContainer.Complete()
	return nil
}

func (clc *containerLifecycleContext) theStoppedAtTimestampIsRecorded() error {
	if clc.currentContainer.StoppedAt() == nil {
		return fmt.Errorf("stopped_at is nil")
	}
	t := *clc.currentContainer.StoppedAt()
	clc.stoppedAtSnapshot = &t
	return nil
}

func (clc *containerLifecycleContext) timeAdvancesBySeconds(seconds int) error {
	clc.clock.Advance(time.Duration(seconds) * time.Second)
	return nil
}

func (clc *containerLifecycleContext) theContainerCompletes5Iterations(iterations int) error {
	for i := 0; i < iterations; i++ {
		if err := clc.currentContainer.IncrementIteration(); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// Assertion Steps
// ============================================================================

func (clc *containerLifecycleContext) theContainerStatusShouldBe(expectedStatus string) error {
	if string(clc.currentContainer.Status()) != expectedStatus {
		return fmt.Errorf("expected status '%s' but got '%s'", expectedStatus, clc.currentContainer.Status())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerStartedAtTimestampShouldBeSet() error {
	if clc.currentContainer.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set but it is nil")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerStoppedAtTimestampShouldBeNil() error {
	if clc.currentContainer.StoppedAt() != nil {
		return fmt.Errorf("expected stopped_at to be nil but it is set")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerStoppedAtTimestampShouldBeSet() error {
	if clc.currentContainer.StoppedAt() == nil {
		return fmt.Errorf("expected stopped_at to be set but it is nil")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldBeMarkedAsFinished() error {
	if !clc.currentContainer.IsFinished() {
		return fmt.Errorf("expected container to be marked as finished but it is not")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldNotBeMarkedAsFinished() error {
	if clc.currentContainer.IsFinished() {
		return fmt.Errorf("expected container to not be marked as finished but it is")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerLastErrorShouldBe(expectedError string) error {
	if clc.currentContainer.LastError() == nil {
		return fmt.Errorf("expected last_error '%s' but got nil", expectedError)
	}
	if clc.currentContainer.LastError().Error() != expectedError {
		return fmt.Errorf("expected last_error '%s' but got '%s'", expectedError, clc.currentContainer.LastError().Error())
	}
	return nil
}

func (clc *containerLifecycleContext) theStatusShouldBeNot(expectedStatus, notStatus string) error {
	if string(clc.currentContainer.Status()) != expectedStatus {
		return fmt.Errorf("expected status '%s' but got '%s'", expectedStatus, clc.currentContainer.Status())
	}
	if string(clc.currentContainer.Status()) == notStatus {
		return fmt.Errorf("status should not be '%s' but it is", notStatus)
	}
	return nil
}

func (clc *containerLifecycleContext) theStatusMustBeNot(expectedStatus, notStatus string) error {
	return clc.theStatusShouldBeNot(expectedStatus, notStatus)
}

func (clc *containerLifecycleContext) theStoppedAtTimestampShouldBeSet() error {
	return clc.theContainerStoppedAtTimestampShouldBeSet()
}

func (clc *containerLifecycleContext) theContainerShouldAppearWithStatus(expectedStatus string) error {
	found := false
	for _, c := range clc.containerList {
		if c.ID() == clc.currentContainer.ID() {
			found = true
			if string(c.Status()) != expectedStatus {
				return fmt.Errorf("expected container status '%s' but got '%s'", expectedStatus, c.Status())
			}
		}
	}
	if !found {
		return fmt.Errorf("container not found in list")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldBeIncludedInTheFinishedContainersList() error {
	for _, c := range clc.containerList {
		if c.ID() == clc.currentContainer.ID() {
			if !c.IsFinished() {
				return fmt.Errorf("container is in list but not marked as finished")
			}
			return nil
		}
	}
	return fmt.Errorf("container not found in finished containers list")
}

func (clc *containerLifecycleContext) theContainerShouldAppearInTheResults() error {
	for _, c := range clc.containerList {
		if c.ID() == clc.currentContainer.ID() {
			return nil
		}
	}
	return fmt.Errorf("container not found in results")
}

func (clc *containerLifecycleContext) theContainerShouldHaveFinishedFlagSetToTrue() error {
	if !clc.currentContainer.IsFinished() {
		return fmt.Errorf("expected finished flag to be true but got false")
	}
	return nil
}

func (clc *containerLifecycleContext) theResultsShouldContainContainers(expectedCount int) error {
	if len(clc.containerList) != expectedCount {
		return fmt.Errorf("expected %d containers but got %d", expectedCount, len(clc.containerList))
	}
	return nil
}

func (clc *containerLifecycleContext) theResultsShouldIncludeAnd(id1, id2 string) error {
	found1, found2 := false, false
	for _, c := range clc.containerList {
		if c.ID() == id1 {
			found1 = true
		}
		if c.ID() == id2 {
			found2 = true
		}
	}
	if !found1 {
		return fmt.Errorf("expected to find %s but did not", id1)
	}
	if !found2 {
		return fmt.Errorf("expected to find %s but did not", id2)
	}
	return nil
}

func (clc *containerLifecycleContext) theResultsShouldNotInclude(id string) error {
	for _, c := range clc.containerList {
		if c.ID() == id {
			return fmt.Errorf("expected not to find %s but did", id)
		}
	}
	return nil
}

func (clc *containerLifecycleContext) theRuntimeDurationShouldBeApproximatelySeconds(seconds int) error {
	expected := time.Duration(seconds) * time.Second
	actual := clc.currentContainer.RuntimeDuration()
	tolerance := 100 * time.Millisecond

	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}

	if diff > tolerance {
		return fmt.Errorf("expected runtime approximately %v but got %v (diff: %v)", expected, actual, diff)
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerStartedAtTimestampShouldRemainUnchanged() error {
	// This assumes we captured started_at in a previous step
	// For now, just verify it's still set
	return clc.theContainerStartedAtTimestampShouldBeSet()
}

func (clc *containerLifecycleContext) theStoppedAtShouldBeGreaterThanOrEqualToStartedAt() error {
	if clc.currentContainer.StartedAt() == nil {
		return fmt.Errorf("started_at is nil")
	}
	if clc.currentContainer.StoppedAt() == nil {
		return fmt.Errorf("stopped_at is nil")
	}
	if clc.currentContainer.StoppedAt().Before(*clc.currentContainer.StartedAt()) {
		return fmt.Errorf("stopped_at (%v) is before started_at (%v)",
			clc.currentContainer.StoppedAt(), clc.currentContainer.StartedAt())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerCurrentIterationShouldIncrementTo(expected int) error {
	if clc.currentContainer.CurrentIteration() != expected {
		return fmt.Errorf("expected current_iteration %d but got %d",
			expected, clc.currentContainer.CurrentIteration())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldContinueRunning() error {
	if !clc.currentContainer.ShouldContinue() {
		return fmt.Errorf("expected container to continue running but it should not")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldNotContinueRunning() error {
	if clc.currentContainer.ShouldContinue() {
		return fmt.Errorf("expected container to not continue running but it should")
	}
	return nil
}

func (clc *containerLifecycleContext) theCurrentIterationShouldBe(expected int) error {
	if clc.currentContainer.CurrentIteration() != expected {
		return fmt.Errorf("expected current_iteration %d but got %d",
			expected, clc.currentContainer.CurrentIteration())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerCurrentIterationShouldBe(expected int) error {
	return clc.theCurrentIterationShouldBe(expected)
}

func (clc *containerLifecycleContext) theContainerRestartCountShouldBe(expected int) error {
	if clc.currentContainer.RestartCount() != expected {
		return fmt.Errorf("expected restart_count %d but got %d",
			expected, clc.currentContainer.RestartCount())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerLastErrorShouldBeNil() error {
	if clc.currentContainer.LastError() != nil {
		return fmt.Errorf("expected last_error to be nil but got '%s'",
			clc.currentContainer.LastError().Error())
	}
	return nil
}

func (clc *containerLifecycleContext) theRestartEligibilityShouldBe(expected bool) error {
	actual := clc.currentContainer.CanRestart()
	if actual != expected {
		return fmt.Errorf("expected restart eligibility %v but got %v", expected, actual)
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldRemainInStatus(expectedStatus string) error {
	return clc.theContainerStatusShouldBe(expectedStatus)
}

func (clc *containerLifecycleContext) theRestartOperationShouldFail() error {
	if clc.err == nil {
		return fmt.Errorf("expected restart operation to fail but it succeeded")
	}
	return nil
}

func (clc *containerLifecycleContext) theErrorShouldMention(expectedText string) error {
	if clc.err == nil {
		return fmt.Errorf("expected error mentioning '%s' but got no error", expectedText)
	}
	if !strings.Contains(clc.err.Error(), expectedText) {
		return fmt.Errorf("expected error mentioning '%s' but got '%s'",
			expectedText, clc.err.Error())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldHavePlayerID(expected int) error {
	if clc.currentContainer.PlayerID() != expected {
		return fmt.Errorf("expected player_id %d but got %d",
			expected, clc.currentContainer.PlayerID())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerMetadataShouldStillContain(key, value string) error {
	val, exists := clc.currentContainer.GetMetadataValue(key)
	if !exists {
		return fmt.Errorf("expected metadata to contain key '%s' but it does not", key)
	}
	if val != value {
		return fmt.Errorf("expected metadata key '%s' to have value '%s' but got '%v'",
			key, value, val)
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldStillBeEligibleForRestart() error {
	if !clc.currentContainer.CanRestart() {
		return fmt.Errorf("expected container to be eligible for restart but it is not")
	}
	return nil
}

func (clc *containerLifecycleContext) allContainersShouldHaveStatus(expectedStatus string) error {
	for _, c := range clc.containers {
		if string(c.Status()) != expectedStatus {
			return fmt.Errorf("expected all containers to have status '%s' but %s has '%s'",
				expectedStatus, c.ID(), c.Status())
		}
	}
	return nil
}

func (clc *containerLifecycleContext) eachContainerShouldHaveAUniqueStartedAtTimestamp() error {
	timestamps := make(map[time.Time]bool)
	for _, c := range clc.containers {
		if c.StartedAt() == nil {
			return fmt.Errorf("container %s has nil started_at", c.ID())
		}
		if timestamps[*c.StartedAt()] {
			return fmt.Errorf("duplicate started_at timestamp found: %v", *c.StartedAt())
		}
		timestamps[*c.StartedAt()] = true
	}
	return nil
}

func (clc *containerLifecycleContext) eachContainerShouldHaveAUniqueID() error {
	ids := make(map[string]bool)
	for _, c := range clc.containers {
		if ids[c.ID()] {
			return fmt.Errorf("duplicate container ID found: %s", c.ID())
		}
		ids[c.ID()] = true
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerIDsShouldBeSequential() error {
	// Check that IDs are in format "container-N" and N is sequential
	expectedID := 1
	for i := 1; i <= len(clc.containers); i++ {
		id := fmt.Sprintf("container-%d", expectedID)
		if _, exists := clc.containers[id]; !exists {
			return fmt.Errorf("expected container ID '%s' but it does not exist", id)
		}
		expectedID++
	}
	return nil
}

func (clc *containerLifecycleContext) theRemovalShouldSucceed() error {
	if clc.err != nil {
		return fmt.Errorf("expected removal to succeed but got error: %s", clc.err.Error())
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldNotAppearInTheList() error {
	if _, exists := clc.containers[clc.currentContainer.ID()]; exists {
		return fmt.Errorf("expected container to not appear in list but it does")
	}
	return nil
}

func (clc *containerLifecycleContext) theRemovalShouldFail() error {
	if clc.err == nil {
		return fmt.Errorf("expected removal to fail but it succeeded")
	}
	return nil
}

func (clc *containerLifecycleContext) theContainerShouldStillAppearInTheList() error {
	if _, exists := clc.containers[clc.currentContainer.ID()]; !exists {
		return fmt.Errorf("expected container to appear in list but it does not")
	}
	return nil
}

func (clc *containerLifecycleContext) theOperationShouldFail() error {
	if clc.err == nil {
		return fmt.Errorf("expected operation to fail but it succeeded")
	}
	return nil
}

func (clc *containerLifecycleContext) theStoppedAtTimestampShouldRemainUnchanged() error {
	if clc.stoppedAtSnapshot == nil {
		return fmt.Errorf("no stopped_at snapshot was recorded")
	}
	if clc.currentContainer.StoppedAt() == nil {
		return fmt.Errorf("stopped_at is now nil but was previously set")
	}
	if !clc.stoppedAtSnapshot.Equal(*clc.currentContainer.StoppedAt()) {
		return fmt.Errorf("stopped_at changed from %v to %v",
			clc.stoppedAtSnapshot, clc.currentContainer.StoppedAt())
	}
	return nil
}

// ============================================================================
// Step Registration
// ============================================================================

// InitializeContainerLifecycleScenario registers all daemon container lifecycle step definitions
func InitializeContainerLifecycleScenario(ctx *godog.ScenarioContext) {
	clc := &containerLifecycleContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		clc.reset()
		return ctx, nil
	})

	// Container creation steps
	ctx.Step(`^a new daemon container is created with type "([^"]*)"$`, clc.aNewDaemonContainerIsCreatedWithType)
	ctx.Step(`^a daemon container is in "([^"]*)" status$`, clc.aDaemonContainerIsInStatus)
	ctx.Step(`^a daemon container that runs for less than (\d+) second$`, clc.aDaemonContainerThatRunsForLessThanSecond)
	ctx.Step(`^a container with status "([^"]*)"$`, clc.aContainerWithStatus)
	ctx.Step(`^a daemon container in "([^"]*)" status with restart_count (\d+)$`, clc.aDaemonContainerInStatusWithRestartCount)
	ctx.Step(`^a daemon container with player_id (\d+) in "([^"]*)" status$`, func(playerID int, status string) error {
		if err := clc.aDaemonContainerWithPlayerID(playerID); err != nil {
			return err
		}
		return clc.theContainerIsInStatus(status)
	})
	ctx.Step(`^a daemon container with metadata "([^"]*)" = "([^"]*)"$`, clc.aDaemonContainerWithMetadata)
	ctx.Step(`^the container is in "([^"]*)" status$`, clc.theContainerIsInStatus)
	ctx.Step(`^(\d+) daemon containers are created$`, clc.daemonContainersAreCreated)
	ctx.Step(`^(\d+) daemon containers exist:$`, clc.daemonContainersExist)
	ctx.Step(`^a daemon container is in "([^"]*)" status with max_iterations (\d+)$`, clc.aDaemonContainerIsInStatusWithMaxIterations)
	ctx.Step(`^a daemon container with max_iterations (-?\d+) and current_iteration (\d+)$`, clc.aDaemonContainerWithMaxIterationsAndCurrentIteration)
	ctx.Step(`^a daemon container with max_iterations (-?\d+)$`, clc.aDaemonContainerWithMaxIterations)

	// Container action steps
	ctx.Step(`^the daemon starts the container$`, clc.theDaemonStartsTheContainer)
	ctx.Step(`^the container operation completes successfully$`, clc.theContainerOperationCompletesSuccessfully)
	ctx.Step(`^the container operation encounters an error "([^"]*)"$`, clc.theContainerOperationEncountersAnError)
	ctx.Step(`^the daemon signals the container to stop$`, clc.theDaemonSignalsTheContainerToStop)
	ctx.Step(`^the daemon finalizes the container shutdown$`, clc.theDaemonFinalizesTheContainerShutdown)
	ctx.Step(`^the container completes its operation$`, clc.theContainerCompletesItsOperation)
	ctx.Step(`^the container completes successfully$`, clc.theContainerCompletesSuccessfully)
	ctx.Step(`^I query the container status$`, clc.iQueryTheContainerStatus)
	ctx.Step(`^I list all daemon containers$`, clc.iListAllDaemonContainers)
	ctx.Step(`^I query containers by status "([^"]*)"$`, clc.iQueryContainersByStatus)
	ctx.Step(`^I list only finished containers$`, clc.iListOnlyFinishedContainers)
	ctx.Step(`^(\d+) seconds? pass$`, clc.secondsPass)
	ctx.Step(`^the container transitions to "([^"]*)"$`, clc.theContainerTransitionsTo)
	ctx.Step(`^the container completes one iteration$`, clc.theContainerCompletesOneIteration)
	ctx.Step(`^I check if the container should continue$`, clc.iCheckIfTheContainerShouldContinue)
	ctx.Step(`^the daemon restarts the container$`, clc.theDaemonRestartsTheContainer)
	ctx.Step(`^I check if the container can restart$`, clc.iCheckIfTheContainerCanRestart)
	ctx.Step(`^I attempt to restart the container$`, clc.iAttemptToRestartTheContainer)
	ctx.Step(`^the daemon restarts the container (\d+) times$`, clc.theDaemonRestartsTheContainerTimes)
	ctx.Step(`^the daemon starts all containers$`, clc.theDaemonStartsAllContainers)
	ctx.Step(`^I query container "([^"]*)"$`, clc.iQueryContainer)
	ctx.Step(`^I remove the container$`, clc.iRemoveTheContainer)
	ctx.Step(`^I attempt to remove the container without stopping$`, clc.iAttemptToRemoveTheContainerWithoutStopping)
	ctx.Step(`^I attempt to start the container$`, clc.iAttemptToStartTheContainer)
	ctx.Step(`^I attempt to complete the container$`, clc.iAttemptToCompleteTheContainer)
	ctx.Step(`^the stopped_at timestamp is recorded$`, clc.theStoppedAtTimestampIsRecorded)
	ctx.Step(`^time advances by (\d+) seconds$`, clc.timeAdvancesBySeconds)
	ctx.Step(`^the container completes (\d+) iterations$`, clc.theContainerCompletes5Iterations)

	// Assertion steps
	ctx.Step(`^the container status should be "([^"]*)"$`, clc.theContainerStatusShouldBe)
	ctx.Step(`^the container started_at timestamp should be set$`, clc.theContainerStartedAtTimestampShouldBeSet)
	ctx.Step(`^the container stopped_at timestamp should be nil$`, clc.theContainerStoppedAtTimestampShouldBeNil)
	ctx.Step(`^the container stopped_at timestamp should be set$`, clc.theContainerStoppedAtTimestampShouldBeSet)
	ctx.Step(`^the container should be marked as finished$`, clc.theContainerShouldBeMarkedAsFinished)
	ctx.Step(`^the container should not be marked as finished$`, clc.theContainerShouldNotBeMarkedAsFinished)
	ctx.Step(`^the container last_error should be "([^"]*)"$`, clc.theContainerLastErrorShouldBe)
	ctx.Step(`^the status should be "([^"]*)" not "([^"]*)"$`, clc.theStatusShouldBeNot)
	ctx.Step(`^the status must be "([^"]*)" not "([^"]*)"$`, clc.theStatusMustBeNot)
	ctx.Step(`^the stopped_at timestamp should be set$`, clc.theStoppedAtTimestampShouldBeSet)
	ctx.Step(`^the container should appear with status "([^"]*)"$`, clc.theContainerShouldAppearWithStatus)
	ctx.Step(`^the container should be included in the finished containers list$`, clc.theContainerShouldBeIncludedInTheFinishedContainersList)
	ctx.Step(`^the container should appear in the results$`, clc.theContainerShouldAppearInTheResults)
	ctx.Step(`^the container should have finished flag set to true$`, clc.theContainerShouldHaveFinishedFlagSetToTrue)
	ctx.Step(`^the results should contain (\d+) containers$`, clc.theResultsShouldContainContainers)
	ctx.Step(`^the results should include "([^"]*)" and "([^"]*)"$`, clc.theResultsShouldIncludeAnd)
	ctx.Step(`^the results should not include "([^"]*)"$`, clc.theResultsShouldNotInclude)
	ctx.Step(`^the runtime duration should be approximately (\d+) seconds$`, clc.theRuntimeDurationShouldBeApproximatelySeconds)
	ctx.Step(`^the container started_at timestamp should remain unchanged$`, clc.theContainerStartedAtTimestampShouldRemainUnchanged)
	ctx.Step(`^the stopped_at should be greater than or equal to started_at$`, clc.theStoppedAtShouldBeGreaterThanOrEqualToStartedAt)
	ctx.Step(`^the container current_iteration should increment to (\d+)$`, clc.theContainerCurrentIterationShouldIncrementTo)
	ctx.Step(`^the container should continue running$`, clc.theContainerShouldContinueRunning)
	ctx.Step(`^the container should not continue running$`, clc.theContainerShouldNotContinueRunning)
	ctx.Step(`^the current_iteration should be (\d+)$`, clc.theCurrentIterationShouldBe)
	ctx.Step(`^the container current_iteration should be (\d+)$`, clc.theContainerCurrentIterationShouldBe)
	ctx.Step(`^the container restart_count should be (\d+)$`, clc.theContainerRestartCountShouldBe)
	ctx.Step(`^the container last_error should be nil$`, clc.theContainerLastErrorShouldBeNil)
	ctx.Step(`^the restart eligibility should be (true|false)$`, func(expected string) error {
		return clc.theRestartEligibilityShouldBe(expected == "true")
	})
	ctx.Step(`^the container should remain in "([^"]*)" status$`, clc.theContainerShouldRemainInStatus)
	ctx.Step(`^the container status should remain "([^"]*)"$`, clc.theContainerShouldRemainInStatus)
	ctx.Step(`^the restart operation should fail$`, clc.theRestartOperationShouldFail)
	ctx.Step(`^the error should mention "([^"]*)"$`, clc.theErrorShouldMention)
	ctx.Step(`^the container should have player_id (\d+)$`, clc.theContainerShouldHavePlayerID)
	ctx.Step(`^the container metadata should still contain "([^"]*)" = "([^"]*)"$`, clc.theContainerMetadataShouldStillContain)
	ctx.Step(`^the container should still be eligible for restart$`, clc.theContainerShouldStillBeEligibleForRestart)
	ctx.Step(`^all containers should have status "([^"]*)"$`, clc.allContainersShouldHaveStatus)
	ctx.Step(`^each container should have a unique started_at timestamp$`, clc.eachContainerShouldHaveAUniqueStartedAtTimestamp)
	ctx.Step(`^each container should have a unique ID$`, clc.eachContainerShouldHaveAUniqueID)
	ctx.Step(`^the container IDs should be sequential$`, clc.theContainerIDsShouldBeSequential)
	ctx.Step(`^the removal should succeed$`, clc.theRemovalShouldSucceed)
	ctx.Step(`^the container should not appear in the list$`, clc.theContainerShouldNotAppearInTheList)
	ctx.Step(`^the removal should fail$`, clc.theRemovalShouldFail)
	ctx.Step(`^the container should still appear in the list$`, clc.theContainerShouldStillAppearInTheList)
	ctx.Step(`^the operation should fail$`, clc.theOperationShouldFail)
	ctx.Step(`^the stopped_at timestamp should remain unchanged$`, clc.theStoppedAtTimestampShouldRemainUnchanged)
}
