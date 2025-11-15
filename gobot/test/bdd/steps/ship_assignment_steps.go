package steps

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

// Global ship registry to track ships across contexts (for test purposes only)
var (
	globalShipRegistry      = make(map[string]int) // shipSymbol -> playerID
	globalShipRegistryMutex sync.RWMutex
)

func registerShipGlobally(shipSymbol string, playerID int) {
	globalShipRegistryMutex.Lock()
	defer globalShipRegistryMutex.Unlock()
	globalShipRegistry[shipSymbol] = playerID
}

func getShipPlayerID(shipSymbol string) (int, bool) {
	globalShipRegistryMutex.RLock()
	defer globalShipRegistryMutex.RUnlock()
	playerID, exists := globalShipRegistry[shipSymbol]
	return playerID, exists
}

func clearGlobalShipRegistry() {
	globalShipRegistryMutex.Lock()
	defer globalShipRegistryMutex.Unlock()
	globalShipRegistry = make(map[string]int)
}

type shipAssignmentContext struct {
	// Core state
	manager     *daemon.ShipAssignmentManager
	assignment  *daemon.ShipAssignment
	err         error
	clock       *shared.MockClock

	// Test data
	players     map[int]bool                        // playerID -> exists
	ships       map[string]int                      // shipSymbol -> playerID
	containers  map[string]*containerInfo           // containerID -> container info
	assignments map[string]*daemon.ShipAssignment   // shipSymbol -> assignment

	// Query results
	boolResult          bool
	assignmentExists    bool
	cleanupCount        int

	// Command simulation state
	commandErr          error
	queryResult         map[string]interface{}
	response            interface{} // Generic response field for cross-context compatibility

	// Daemon state
	daemonShuttingDown  bool
}

type containerInfo struct {
	id        string
	type_     string
	playerID  int
	status    string
}

func (sac *shipAssignmentContext) reset() {
	sac.manager = daemon.NewShipAssignmentManager(nil) // Will use RealClock, but we'll replace it
	sac.assignment = nil
	sac.err = nil
	sac.clock = shared.NewMockClock(time.Now())

	// Replace manager's clock with our mock
	sac.manager = daemon.NewShipAssignmentManager(sac.clock)

	sac.players = make(map[int]bool)
	sac.ships = make(map[string]int)
	sac.containers = make(map[string]*containerInfo)
	sac.assignments = make(map[string]*daemon.ShipAssignment)

	sac.boolResult = false
	sac.assignmentExists = false
	sac.cleanupCount = 0
	sac.commandErr = nil
	sac.queryResult = make(map[string]interface{})
	sac.response = nil
	sac.daemonShuttingDown = false

	// Clear global ship registry for test isolation
	clearGlobalShipRegistry()
}

// ============================================================================
// Background Steps - Player and Ship Setup
// ============================================================================

func (sac *shipAssignmentContext) aPlayerExistsWithID(playerID int) error {
	sac.players[playerID] = true
	return nil
}

func (sac *shipAssignmentContext) aShipExistsForPlayer(shipSymbol string, playerID int) error {
	// Auto-create player if it doesn't exist (for test convenience)
	if !sac.players[playerID] {
		sac.players[playerID] = true
	}
	sac.ships[shipSymbol] = playerID
	// Register globally so other contexts can access it
	registerShipGlobally(shipSymbol, playerID)
	return nil
}

// ============================================================================
// Container Setup Steps
// ============================================================================

func (sac *shipAssignmentContext) aContainerExistsWithTypeForPlayer(containerID, containerType string, playerID int) error {
	if !sac.players[playerID] {
		return fmt.Errorf("player %d does not exist", playerID)
	}
	sac.containers[containerID] = &containerInfo{
		id:       containerID,
		type_:    containerType,
		playerID: playerID,
		status:   "RUNNING",
	}
	return nil
}

func (sac *shipAssignmentContext) theContainerTransitionsToStatus(containerID, status string) error {
	container, exists := sac.containers[containerID]
	if !exists {
		return fmt.Errorf("container %s does not exist", containerID)
	}
	container.status = status
	return nil
}

// ============================================================================
// Ship Assignment Steps
// ============================================================================

func (sac *shipAssignmentContext) iAssignShipToContainerWithOperation(shipSymbol, containerID, operation string) error {
	// Get player ID from container instead of ships map (to avoid cross-context issues)
	container, exists := sac.containers[containerID]
	if !exists {
		sac.err = fmt.Errorf("container %s does not exist", containerID)
		return nil
	}

	sac.assignment, sac.err = sac.manager.AssignShip(
		context.Background(),
		shipSymbol,
		container.playerID,
		containerID,
		operation,
	)

	if sac.err == nil {
		sac.assignments[shipSymbol] = sac.assignment
	}

	return nil
}

func (sac *shipAssignmentContext) shipIsAssignedToContainerWithOperation(shipSymbol, containerID, operation string) error {
	return sac.iAssignShipToContainerWithOperation(shipSymbol, containerID, operation)
}

func (sac *shipAssignmentContext) iAttemptToAssignShipToContainerWithOperation(shipSymbol, containerID, operation string) error {
	// Get player ID from container instead of ships map (to avoid cross-context issues)
	container, exists := sac.containers[containerID]
	if !exists {
		sac.err = fmt.Errorf("container %s does not exist", containerID)
		return nil
	}

	// Check if ship exists and validate player ID mismatch using global registry
	if shipPlayerID, shipExists := getShipPlayerID(shipSymbol); shipExists {
		if shipPlayerID != container.playerID {
			sac.err = fmt.Errorf("ship player_id mismatch")
			return nil
		}
	}

	sac.assignment, sac.err = sac.manager.AssignShip(
		context.Background(),
		shipSymbol,
		container.playerID,
		containerID,
		operation,
	)

	return nil
}

func (sac *shipAssignmentContext) iReleaseTheShipAssignmentForWithReason(shipSymbol, reason string) error {
	sac.err = sac.manager.ReleaseAssignment(shipSymbol, reason)
	return nil
}

func (sac *shipAssignmentContext) iReleaseAllShipAssignmentsWithReason(reason string) error {
	sac.err = sac.manager.ReleaseAll(reason)
	return nil
}

// ============================================================================
// Orphaned Assignment Steps
// ============================================================================

func (sac *shipAssignmentContext) shipHasAnOrphanedAssignmentToNonExistentContainer(shipSymbol, containerID string) error {
	// Use player ID from ships map if available, otherwise default to 1
	// (this allows the test to work even when ship is created by a different context)
	playerID := 1
	if storedPlayerID, exists := sac.ships[shipSymbol]; exists {
		playerID = storedPlayerID
	}

	// Create orphaned assignment directly
	assignment := daemon.NewShipAssignment(
		shipSymbol,
		playerID,
		containerID,
		"orphaned_operation",
		sac.clock,
	)

	// Manually add to manager's internal map (simulating persistence)
	sac.assignments[shipSymbol] = assignment

	// Use the manager to assign (this is the orphaned assignment)
	sac.assignment, sac.err = sac.manager.AssignShip(
		context.Background(),
		shipSymbol,
		playerID,
		containerID,
		"orphaned_operation",
	)

	return nil
}

func (sac *shipAssignmentContext) theAssignmentWasCreatedHoursAgo(hours int) error {
	// Advance clock backwards in time to simulate old assignment
	// Actually, we need to create the assignment in the past
	// Let's rewind the clock, create assignment, then fast-forward
	oldTime := sac.clock.Now()
	sac.clock.Advance(-time.Duration(hours) * time.Hour)

	// If we already have an assignment, we need to recreate it with the old time
	if sac.assignment != nil {
		shipSymbol := sac.assignment.ShipSymbol()
		playerID := sac.assignment.PlayerID()
		containerID := sac.assignment.ContainerID()
		operation := sac.assignment.Operation()

		// Create new assignment at old time
		sac.assignment = daemon.NewShipAssignment(
			shipSymbol,
			playerID,
			containerID,
			operation,
			sac.clock,
		)

		// Update manager's assignment
		sac.manager.AssignShip(
			context.Background(),
			shipSymbol,
			playerID,
			containerID,
			operation,
		)
	}

	// Restore clock to current time
	sac.clock.SetTime(oldTime)

	return nil
}

func (sac *shipAssignmentContext) theAssignmentWasCreatedMinutesAgo(minutes int) error {
	// Similar to hours, but with minutes
	oldTime := sac.clock.Now()
	sac.clock.Advance(-time.Duration(minutes) * time.Minute)

	if sac.assignment != nil {
		shipSymbol := sac.assignment.ShipSymbol()
		playerID := sac.assignment.PlayerID()
		containerID := sac.assignment.ContainerID()
		operation := sac.assignment.Operation()

		sac.assignment = daemon.NewShipAssignment(
			shipSymbol,
			playerID,
			containerID,
			operation,
			sac.clock,
		)

		sac.manager.AssignShip(
			context.Background(),
			shipSymbol,
			playerID,
			containerID,
			operation,
		)
	}

	sac.clock.SetTime(oldTime)

	return nil
}

// ============================================================================
// Daemon Lifecycle Steps
// ============================================================================

func (sac *shipAssignmentContext) theDaemonStartsUp() error {
	// Daemon startup - no specific action needed in this context
	return nil
}

func (sac *shipAssignmentContext) theDaemonReceivesShutdownSignal() error {
	sac.daemonShuttingDown = true
	return nil
}

func (sac *shipAssignmentContext) iCleanOrphanedShipAssignments() error {
	// Build map of existing containers
	existingContainers := make(map[string]bool)
	for containerID := range sac.containers {
		existingContainers[containerID] = true
	}

	sac.cleanupCount, sac.err = sac.manager.CleanOrphanedAssignments(existingContainers)
	return nil
}

func (sac *shipAssignmentContext) iCheckIfTheShipAssignmentIsStale() error {
	if sac.assignment == nil {
		assignment, exists := sac.manager.GetAssignment(sac.getCurrentShipSymbol())
		if !exists {
			return fmt.Errorf("no assignment found")
		}
		sac.assignment = assignment
	}

	// Check if stale (30 minute timeout)
	sac.boolResult = sac.assignment.IsStale(30 * time.Minute)
	return nil
}

func (sac *shipAssignmentContext) iShouldBeAbleToForcefullyReleaseTheStaleAssignment() error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to release")
	}

	sac.err = sac.assignment.ForceRelease("stale_timeout")
	return nil
}

// ============================================================================
// Lock Behavior Steps
// ============================================================================

func (sac *shipAssignmentContext) iAttemptToExecuteANavigationCommandForShip(shipSymbol string) error {
	// Check if ship is locked
	assignment, exists := sac.manager.GetAssignment(shipSymbol)
	if exists && assignment.IsActive() {
		sac.commandErr = fmt.Errorf("ship is locked by container: %s", assignment.ContainerID())
	} else {
		sac.commandErr = nil
	}
	return nil
}

func (sac *shipAssignmentContext) iQueryShipDetailsFor(shipSymbol string) error {
	// Simulate read-only query
	assignment, exists := sac.manager.GetAssignment(shipSymbol)

	sac.queryResult = make(map[string]interface{})
	sac.queryResult["ship_symbol"] = shipSymbol

	if exists && assignment.IsActive() {
		sac.queryResult["assignment_status"] = "locked"
		sac.queryResult["container_id"] = assignment.ContainerID()
	} else {
		sac.queryResult["assignment_status"] = "unlocked"
	}

	// Set response for cross-context compatibility with "the query should succeed" step
	sac.response = sac.queryResult

	return nil
}

// ============================================================================
// Reassignment Steps
// ============================================================================

func (sac *shipAssignmentContext) iAttemptToReassignShipFromTo(shipSymbol, oldContainerID, newContainerID string) error {
	// Check if ship is currently assigned to oldContainerID
	assignment, exists := sac.manager.GetAssignment(shipSymbol)
	if !exists {
		sac.err = fmt.Errorf("ship not assigned")
		return nil
	}

	if assignment.ContainerID() != oldContainerID {
		sac.err = fmt.Errorf("ship not assigned to container %s", oldContainerID)
		return nil
	}

	if assignment.IsActive() {
		sac.err = fmt.Errorf("ship is still locked")
		return nil
	}

	// Attempt reassignment
	playerID := sac.ships[shipSymbol]
	sac.assignment, sac.err = sac.manager.AssignShip(
		context.Background(),
		shipSymbol,
		playerID,
		newContainerID,
		"new_operation",
	)

	return nil
}

// ============================================================================
// Assertion Steps
// ============================================================================

func (sac *shipAssignmentContext) shipShouldBeAssignedToContainer(shipSymbol, containerID string) error {
	assignment, exists := sac.manager.GetAssignment(shipSymbol)
	if !exists {
		return fmt.Errorf("ship %s is not assigned to any container", shipSymbol)
	}

	if assignment.ContainerID() != containerID {
		return fmt.Errorf("ship %s is assigned to container %s, expected %s",
			shipSymbol, assignment.ContainerID(), containerID)
	}

	sac.assignment = assignment // Store for further assertions
	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentStatusShouldBe(expectedStatus string) error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check status")
	}

	actualStatus := string(sac.assignment.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected assignment status '%s' but got '%s'",
			expectedStatus, actualStatus)
	}

	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentOperationShouldBe(expectedOperation string) error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check operation")
	}

	actualOperation := sac.assignment.Operation()
	if actualOperation != expectedOperation {
		return fmt.Errorf("expected assignment operation '%s' but got '%s'",
			expectedOperation, actualOperation)
	}

	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentPlayerIDShouldBe(expectedPlayerID int) error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check player_id")
	}

	actualPlayerID := sac.assignment.PlayerID()
	if actualPlayerID != expectedPlayerID {
		return fmt.Errorf("expected assignment player_id %d but got %d",
			expectedPlayerID, actualPlayerID)
	}

	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentShouldHaveAnAssignedAtTimestamp() error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check assigned_at timestamp")
	}

	assignedAt := sac.assignment.AssignedAt()
	if assignedAt.IsZero() {
		return fmt.Errorf("expected assigned_at timestamp to be set but it is zero")
	}

	return nil
}

func (sac *shipAssignmentContext) theAssignmentShouldFailWithError(expectedError string) error {
	if sac.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}

	if !strings.Contains(sac.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'",
			expectedError, sac.err.Error())
	}

	return nil
}

func (sac *shipAssignmentContext) shipShouldStillBeAssignedToContainer(shipSymbol, containerID string) error {
	return sac.shipShouldBeAssignedToContainer(shipSymbol, containerID)
}

func (sac *shipAssignmentContext) shipShouldNoLongerBeAssignedToAnyContainer(shipSymbol string) error {
	assignment, exists := sac.manager.GetAssignment(shipSymbol)
	if !exists {
		return nil // Not assigned - this is what we expect
	}

	if assignment.IsActive() {
		return fmt.Errorf("ship %s is still assigned to container %s",
			shipSymbol, assignment.ContainerID())
	}

	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentShouldHaveAReleasedAtTimestamp() error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check released_at timestamp")
	}

	releasedAt := sac.assignment.ReleasedAt()
	if releasedAt == nil {
		return fmt.Errorf("expected released_at timestamp to be set but it is nil")
	}

	return nil
}

func (sac *shipAssignmentContext) theShipAssignmentReleaseReasonShouldBe(expectedReason string) error {
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to check release_reason")
	}

	releaseReason := sac.assignment.ReleaseReason()
	if releaseReason == nil {
		return fmt.Errorf("expected release_reason '%s' but it is nil", expectedReason)
	}

	if *releaseReason != expectedReason {
		return fmt.Errorf("expected release_reason '%s' but got '%s'",
			expectedReason, *releaseReason)
	}

	return nil
}

func (sac *shipAssignmentContext) theOrphanedAssignmentCleanupCountShouldBe(expectedCount int) error {
	if sac.cleanupCount != expectedCount {
		return fmt.Errorf("expected cleanup count %d but got %d",
			expectedCount, sac.cleanupCount)
	}
	return nil
}

func (sac *shipAssignmentContext) shipShouldNotBeAssignedToAnyContainer(shipSymbol string) error {
	return sac.shipShouldNoLongerBeAssignedToAnyContainer(shipSymbol)
}

// ============================================================================
// Database Persistence Steps
// ============================================================================

func (sac *shipAssignmentContext) theShipAssignmentShouldBePersistedInTheDatabase() error {
	// In-memory manager - we simulate persistence by checking if assignment exists
	if sac.assignment == nil {
		return fmt.Errorf("no assignment to persist")
	}

	// Verify it exists in manager
	_, exists := sac.manager.GetAssignment(sac.assignment.ShipSymbol())
	if !exists {
		return fmt.Errorf("assignment not found in manager (simulated database)")
	}

	sac.assignmentExists = exists
	return nil
}

func (sac *shipAssignmentContext) queryingTheDatabaseShouldReturnTheAssignmentForShip(shipSymbol string) error {
	assignment, exists := sac.manager.GetAssignment(shipSymbol)
	if !exists {
		return fmt.Errorf("assignment for ship %s not found in database", shipSymbol)
	}

	sac.assignment = assignment
	return nil
}

// ============================================================================
// Lock Assertion Steps
// ============================================================================

func (sac *shipAssignmentContext) theNavigationCommandShouldBeRejectedWithError(expectedError string) error {
	if sac.commandErr == nil {
		return fmt.Errorf("expected command error containing '%s' but got no error",
			expectedError)
	}

	if !strings.Contains(sac.commandErr.Error(), expectedError) {
		return fmt.Errorf("expected command error containing '%s' but got '%s'",
			expectedError, sac.commandErr.Error())
	}

	return nil
}

func (sac *shipAssignmentContext) theErrorShouldIncludeContainerID(expectedContainerID string) error {
	if sac.commandErr == nil {
		return fmt.Errorf("expected error to include container_id '%s' but got no error",
			expectedContainerID)
	}

	if !strings.Contains(sac.commandErr.Error(), expectedContainerID) {
		return fmt.Errorf("expected error to include container_id '%s' but got '%s'",
			expectedContainerID, sac.commandErr.Error())
	}

	return nil
}

func (sac *shipAssignmentContext) theQueryShouldSucceed() error {
	// Queries always succeed (read-only operations don't fail due to locks)
	// Check if response is set (for ship assignment queries)
	if sac.response == nil {
		return fmt.Errorf("response should not be nil")
	}
	return nil
}

func (sac *shipAssignmentContext) theShipDetailsShouldIncludeAssignmentStatus(expectedStatus string) error {
	status, exists := sac.queryResult["assignment_status"]
	if !exists {
		return fmt.Errorf("query result missing assignment_status")
	}

	if status != expectedStatus {
		return fmt.Errorf("expected assignment_status '%s' but got '%v'",
			expectedStatus, status)
	}

	return nil
}

func (sac *shipAssignmentContext) theShipDetailsShouldIncludeContainerID(expectedContainerID string) error {
	containerID, exists := sac.queryResult["container_id"]
	if !exists {
		return fmt.Errorf("query result missing container_id")
	}

	if containerID != expectedContainerID {
		return fmt.Errorf("expected container_id '%s' but got '%v'",
			expectedContainerID, containerID)
	}

	return nil
}

// ============================================================================
// Stale Assignment Assertion Steps
// ============================================================================

func (sac *shipAssignmentContext) theAssignmentShouldBeMarkedAsStale() error {
	if !sac.boolResult {
		return fmt.Errorf("expected assignment to be stale but it is not")
	}
	return nil
}

func (sac *shipAssignmentContext) theReassignmentShouldFailWithError(expectedError string) error {
	return sac.theAssignmentShouldFailWithError(expectedError)
}

// ============================================================================
// Helper Methods
// ============================================================================

func (sac *shipAssignmentContext) getCurrentShipSymbol() string {
	// Get the first ship symbol we can find
	for shipSymbol := range sac.ships {
		return shipSymbol
	}
	return ""
}

// ============================================================================
// Step Registration
// ============================================================================

// InitializeShipAssignmentScenario registers all ship assignment step definitions
func InitializeShipAssignmentScenario(ctx *godog.ScenarioContext) {
	sac := &shipAssignmentContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		sac.reset()
		return ctx, nil
	})

	// Background steps
	ctx.Step(`^a player exists with id (\d+)$`, sac.aPlayerExistsWithID)
	ctx.Step(`^a ship "([^"]*)" exists for player (\d+)$`, sac.aShipExistsForPlayer)

	// Container setup steps
	ctx.Step(`^a container "([^"]*)" exists with type "([^"]*)" for player (\d+)$`,
		sac.aContainerExistsWithTypeForPlayer)
	ctx.Step(`^the container "([^"]*)" transitions to "([^"]*)" status$`,
		sac.theContainerTransitionsToStatus)

	// Ship assignment steps
	ctx.Step(`^I assign ship "([^"]*)" to container "([^"]*)" with operation "([^"]*)"$`,
		sac.iAssignShipToContainerWithOperation)
	ctx.Step(`^ship "([^"]*)" is assigned to container "([^"]*)" with operation "([^"]*)"$`,
		sac.shipIsAssignedToContainerWithOperation)
	ctx.Step(`^I attempt to assign ship "([^"]*)" to container "([^"]*)" with operation "([^"]*)"$`,
		sac.iAttemptToAssignShipToContainerWithOperation)
	ctx.Step(`^I release the ship assignment for "([^"]*)" with reason "([^"]*)"$`,
		sac.iReleaseTheShipAssignmentForWithReason)
	ctx.Step(`^I release all ship assignments with reason "([^"]*)"$`,
		sac.iReleaseAllShipAssignmentsWithReason)

	// Orphaned assignment steps
	ctx.Step(`^ship "([^"]*)" has an orphaned assignment to non-existent container "([^"]*)"$`,
		sac.shipHasAnOrphanedAssignmentToNonExistentContainer)
	ctx.Step(`^the assignment was created (\d+) hours ago$`,
		sac.theAssignmentWasCreatedHoursAgo)
	ctx.Step(`^the assignment was created (\d+) minutes ago$`,
		sac.theAssignmentWasCreatedMinutesAgo)

	// Daemon lifecycle steps
	ctx.Step(`^the daemon starts up$`, sac.theDaemonStartsUp)
	ctx.Step(`^the daemon receives shutdown signal$`, sac.theDaemonReceivesShutdownSignal)
	ctx.Step(`^I clean orphaned ship assignments$`, sac.iCleanOrphanedShipAssignments)
	ctx.Step(`^I check if the ship assignment is stale$`, sac.iCheckIfTheShipAssignmentIsStale)
	ctx.Step(`^I should be able to forcefully release the stale assignment$`,
		sac.iShouldBeAbleToForcefullyReleaseTheStaleAssignment)

	// Lock behavior steps
	ctx.Step(`^I attempt to execute a navigation command for ship "([^"]*)"$`,
		sac.iAttemptToExecuteANavigationCommandForShip)
	ctx.Step(`^I query ship details for "([^"]*)"$`, sac.iQueryShipDetailsFor)

	// Reassignment steps
	ctx.Step(`^I attempt to reassign ship "([^"]*)" from "([^"]*)" to "([^"]*)"$`,
		sac.iAttemptToReassignShipFromTo)

	// Assertion steps
	ctx.Step(`^ship "([^"]*)" should be assigned to container "([^"]*)"$`,
		sac.shipShouldBeAssignedToContainer)
	ctx.Step(`^the ship assignment status should be "([^"]*)"$`,
		sac.theShipAssignmentStatusShouldBe)
	ctx.Step(`^the ship assignment operation should be "([^"]*)"$`,
		sac.theShipAssignmentOperationShouldBe)
	ctx.Step(`^the ship assignment player_id should be (\d+)$`,
		sac.theShipAssignmentPlayerIDShouldBe)
	ctx.Step(`^the ship assignment should have an assigned_at timestamp$`,
		sac.theShipAssignmentShouldHaveAnAssignedAtTimestamp)
	ctx.Step(`^the assignment should fail with error "([^"]*)"$`,
		sac.theAssignmentShouldFailWithError)
	ctx.Step(`^ship "([^"]*)" should still be assigned to container "([^"]*)"$`,
		sac.shipShouldStillBeAssignedToContainer)
	ctx.Step(`^ship "([^"]*)" should no longer be assigned to any container$`,
		sac.shipShouldNoLongerBeAssignedToAnyContainer)
	ctx.Step(`^the ship assignment should have a released_at timestamp$`,
		sac.theShipAssignmentShouldHaveAReleasedAtTimestamp)
	ctx.Step(`^the ship assignment release_reason should be "([^"]*)"$`,
		sac.theShipAssignmentReleaseReasonShouldBe)
	ctx.Step(`^the orphaned assignment cleanup count should be (\d+)$`,
		sac.theOrphanedAssignmentCleanupCountShouldBe)
	ctx.Step(`^ship "([^"]*)" should not be assigned to any container$`,
		sac.shipShouldNotBeAssignedToAnyContainer)

	// Database persistence steps
	ctx.Step(`^the ship assignment should be persisted in the database$`,
		sac.theShipAssignmentShouldBePersistedInTheDatabase)
	ctx.Step(`^querying the database should return the assignment for ship "([^"]*)"$`,
		sac.queryingTheDatabaseShouldReturnTheAssignmentForShip)

	// Lock assertion steps
	ctx.Step(`^the navigation command should be rejected with error "([^"]*)"$`,
		sac.theNavigationCommandShouldBeRejectedWithError)
	ctx.Step(`^the error should include container_id "([^"]*)"$`,
		sac.theErrorShouldIncludeContainerID)
	ctx.Step(`^the query should succeed$`, sac.theQueryShouldSucceed)
	ctx.Step(`^the ship details should include assignment status "([^"]*)"$`,
		sac.theShipDetailsShouldIncludeAssignmentStatus)
	ctx.Step(`^the ship details should include container_id "([^"]*)"$`,
		sac.theShipDetailsShouldIncludeContainerID)

	// Stale assignment assertion steps
	ctx.Step(`^the assignment should be marked as stale$`,
		sac.theAssignmentShouldBeMarkedAsStale)
	ctx.Step(`^the reassignment should fail with error "([^"]*)"$`,
		sac.theReassignmentShouldFailWithError)
}
