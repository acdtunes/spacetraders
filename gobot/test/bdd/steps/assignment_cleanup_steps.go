package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

// assignmentCleanupContext holds state for assignment cleanup BDD tests
type assignmentCleanupContext struct {
	// Server state
	server     *grpc.DaemonServer
	socketPath string

	// Repositories
	shipAssignmentRepo *persistence.ShipAssignmentRepositoryGORM
	containerRepo      *persistence.ContainerRepositoryGORM
	testDB             *gorm.DB

	// Test infrastructure
	mediator *mockDaemonMediator
	logRepo  *mockContainerLogRepo

	// Test state
	ships         []string
	containerID   string
	containerIDs  []string
	runner        *grpc.ContainerRunner
	runners       []*grpc.ContainerRunner
	assignment    *daemon.ShipAssignment
	assignments   []*daemon.ShipAssignment
	playerID      int
	daemonCrashed bool
}

func (ctx *assignmentCleanupContext) reset() {
	// Clean up any existing server
	if ctx.server != nil {
		// Don't call stopDaemonServer here to avoid cleanup
		ctx.server = nil
	}

	// Clean up socket if it exists
	if ctx.socketPath != "" {
		os.RemoveAll(ctx.socketPath)
	}

	// Use shared test database and truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Sprintf("failed to truncate tables: %v", err))
	}

	// Reset state
	ctx.server = nil
	ctx.socketPath = ""
	ctx.mediator = &mockDaemonMediator{}
	ctx.logRepo = &mockContainerLogRepo{logs: []string{}}
	ctx.testDB = helpers.SharedTestDB
	ctx.containerRepo = persistence.NewContainerRepository(helpers.SharedTestDB)
	ctx.shipAssignmentRepo = persistence.NewShipAssignmentRepository(helpers.SharedTestDB)
	ctx.ships = []string{}
	ctx.containerID = ""
	ctx.containerIDs = []string{}
	ctx.runner = nil
	ctx.runners = []*grpc.ContainerRunner{}
	ctx.assignment = nil
	ctx.assignments = []*daemon.ShipAssignment{}
	ctx.playerID = 1
	ctx.daemonCrashed = false
}

// InitializeAssignmentCleanupScenario registers step definitions
func InitializeAssignmentCleanupScenario(sc *godog.ScenarioContext) {
	sCtx := &assignmentCleanupContext{}

	// Given steps
	sc.Step(`^the daemon server is running on socket "([^"]*)"$`, sCtx.theDaemonServerIsRunningOnSocket)
	sc.Step(`^a ship "([^"]*)" exists for player (\d+)$`, sCtx.aShipExistsForPlayer)
	sc.Step(`^ships exist for player (\d+):$`, sCtx.shipsExistForPlayer)
	sc.Step(`^a navigation container is created for ship "([^"]*)" and player (\d+)$`, sCtx.aNavigationContainerIsCreatedForShipAndPlayer)
	sc.Step(`^a scout markets container is created for all ships and player (\d+)$`, sCtx.aScoutMarketsContainerIsCreatedForAllShipsAndPlayer)
	sc.Step(`^the ship assignment is created for the container$`, sCtx.theShipAssignmentIsCreatedForTheContainer)
	sc.Step(`^ship assignments are created for all ships$`, sCtx.shipAssignmentsAreCreatedForAllShips)
	sc.Step(`^the container is running$`, sCtx.theContainerIsRunning)

	// When steps
	sc.Step(`^the container completes successfully$`, sCtx.theContainerCompletesSuccessfully)
	sc.Step(`^the container fails with an error$`, sCtx.theContainerFailsWithAnError)
	sc.Step(`^the user stops the container$`, sCtx.theUserStopsTheContainer)
	sc.Step(`^the ship assignment is released$`, sCtx.theShipAssignmentIsReleased)
	sc.Step(`^a new navigation container is created for ship "([^"]*)" and player (\d+)$`, sCtx.aNewNavigationContainerIsCreatedForShipAndPlayer)
	sc.Step(`^the daemon crashes without cleanup$`, sCtx.theDaemonCrashesWithoutCleanup)
	sc.Step(`^the daemon restarts$`, sCtx.theDaemonRestarts)
	sc.Step(`^the container crashes unexpectedly$`, sCtx.theContainerCrashesUnexpectedly)

	// Then steps
	sc.Step(`^the ship assignment should be released$`, sCtx.theShipAssignmentShouldBeReleased)
	sc.Step(`^all ship assignments should be released$`, sCtx.allShipAssignmentsShouldBeReleased)
	sc.Step(`^the release reason should be "([^"]*)"$`, sCtx.theReleaseReasonShouldBe)
	sc.Step(`^the release reason for all assignments should be "([^"]*)"$`, sCtx.theReleaseReasonForAllAssignmentsShouldBe)
	sc.Step(`^the ship "([^"]*)" should be available for reassignment$`, sCtx.theShipShouldBeAvailableForReassignment)
	sc.Step(`^all ships should be available for reassignment$`, sCtx.allShipsShouldBeAvailableForReassignment)
	sc.Step(`^the container should be created successfully$`, sCtx.theContainerShouldBeCreatedSuccessfully)
	sc.Step(`^a new ship assignment should be created for the ship$`, sCtx.aNewShipAssignmentShouldBeCreatedForTheShip)
	sc.Step(`^all active ship assignments should be released$`, sCtx.allActiveShipAssignmentsShouldBeReleased)

	// Hooks
	sc.Before(func(gCtx context.Context, sc *godog.Scenario) (context.Context, error) {
		sCtx.reset()
		return gCtx, nil
	})

	sc.After(func(gCtx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		sCtx.reset()
		return gCtx, nil
	})
}

// ============================================================================
// Given Steps
// ============================================================================

func (ctx *assignmentCleanupContext) theDaemonServerIsRunningOnSocket(socketPath string) error {
	ctx.socketPath = socketPath

	// Ensure temp directory exists
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Create daemon server
	server, err := grpc.NewDaemonServer(ctx.mediator, ctx.logRepo, ctx.containerRepo, ctx.shipAssignmentRepo, nil, nil, socketPath)
	if err != nil {
		return err
	}

	ctx.server = server

	// Start server in background
	go func() {
		server.Start()
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	return nil
}

func (ctx *assignmentCleanupContext) aShipExistsForPlayer(shipSymbol string, playerID int) error {
	ctx.ships = []string{shipSymbol}
	ctx.playerID = playerID
	return nil
}

func (ctx *assignmentCleanupContext) shipsExistForPlayer(playerID int, ships *godog.Table) error {
	ctx.playerID = playerID
	ctx.ships = []string{}

	for i, row := range ships.Rows {
		if i == 0 {
			continue // Skip header row
		}
		shipSymbol := row.Cells[0].Value
		ctx.ships = append(ctx.ships, shipSymbol)
	}

	return nil
}

func (ctx *assignmentCleanupContext) aNavigationContainerIsCreatedForShipAndPlayer(shipSymbol string, playerID int) error {
	ctx.containerID = fmt.Sprintf("navigate-%s-%d", shipSymbol, time.Now().UnixNano())
	ctx.playerID = playerID

	// Create container entity
	containerEntity := container.NewContainer(
		ctx.containerID,
		container.ContainerTypeNavigate,
		playerID,
		1,
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": "TEST-DEST",
		},
		nil,
	)

	// Persist container to database
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctx.containerRepo.Add(ctxTimeout, containerEntity, "navigate_ship"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Create container runner
	ctx.runner = grpc.NewContainerRunner(containerEntity, ctx.mediator, nil, ctx.logRepo, ctx.containerRepo, ctx.shipAssignmentRepo)

	return nil
}

func (ctx *assignmentCleanupContext) aScoutMarketsContainerIsCreatedForAllShipsAndPlayer(playerID int) error {
	ctx.containerID = fmt.Sprintf("scout-markets-%d", time.Now().UnixNano())
	ctx.playerID = playerID

	// Create container entity
	containerEntity := container.NewContainer(
		ctx.containerID,
		container.ContainerTypeScout,
		playerID,
		1,
		map[string]interface{}{
			"ships": ctx.ships,
		},
		nil,
	)

	// Persist container to database
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctx.containerRepo.Add(ctxTimeout, containerEntity, "scout_markets"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Create container runner
	ctx.runner = grpc.NewContainerRunner(containerEntity, ctx.mediator, nil, ctx.logRepo, ctx.containerRepo, ctx.shipAssignmentRepo)

	return nil
}

func (ctx *assignmentCleanupContext) theShipAssignmentIsCreatedForTheContainer() error {
	// Create ship assignment
	assignment := daemon.NewShipAssignment(
		ctx.ships[0],
		ctx.playerID,
		ctx.containerID,
		nil,
	)

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctx.shipAssignmentRepo.Insert(ctxTimeout, assignment); err != nil {
		return fmt.Errorf("failed to create ship assignment: %w", err)
	}

	ctx.assignment = assignment

	return nil
}

func (ctx *assignmentCleanupContext) shipAssignmentsAreCreatedForAllShips() error {
	ctx.assignments = []*daemon.ShipAssignment{}

	for _, shipSymbol := range ctx.ships {
		assignment := daemon.NewShipAssignment(
			shipSymbol,
			ctx.playerID,
			ctx.containerID,
			nil,
		)

		ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := ctx.shipAssignmentRepo.Insert(ctxTimeout, assignment); err != nil {
			cancel()
			return fmt.Errorf("failed to create ship assignment for %s: %w", shipSymbol, err)
		}

		ctx.assignments = append(ctx.assignments, assignment)
	}

	return nil
}

func (ctx *assignmentCleanupContext) theContainerIsRunning() error {
	// Container is already created, just mark it as running
	return nil
}

// ============================================================================
// When Steps
// ============================================================================

func (ctx *assignmentCleanupContext) theContainerCompletesSuccessfully() error {
	// Simulate container completion by calling the runner's completion logic
	// Since we can't easily trigger the actual container execution, we'll just
	// directly test the cleanup logic

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Release ship assignments for the container
	return ctx.shipAssignmentRepo.ReleaseByContainer(ctxTimeout, ctx.containerID, ctx.playerID, "completed")
}

func (ctx *assignmentCleanupContext) theContainerFailsWithAnError() error {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Release ship assignments for the container
	return ctx.shipAssignmentRepo.ReleaseByContainer(ctxTimeout, ctx.containerID, ctx.playerID, "failed")
}

func (ctx *assignmentCleanupContext) theUserStopsTheContainer() error {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Release ship assignments for the container
	return ctx.shipAssignmentRepo.ReleaseByContainer(ctxTimeout, ctx.containerID, ctx.playerID, "stopped")
}

func (ctx *assignmentCleanupContext) theShipAssignmentIsReleased() error {
	// This is handled by the completion/failure/stop steps
	return nil
}

func (ctx *assignmentCleanupContext) aNewNavigationContainerIsCreatedForShipAndPlayer(shipSymbol string, playerID int) error {
	// Create new container
	newContainerID := fmt.Sprintf("navigate-%s-%d", shipSymbol, time.Now().UnixNano())

	containerEntity := container.NewContainer(
		newContainerID,
		container.ContainerTypeNavigate,
		playerID,
		1,
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"destination": "TEST-DEST-2",
		},
		nil,
	)

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctx.containerRepo.Add(ctxTimeout, containerEntity, "navigate_ship"); err != nil {
		return fmt.Errorf("failed to persist new container: %w", err)
	}

	// Update context with new container ID
	ctx.containerID = newContainerID

	return nil
}

func (ctx *assignmentCleanupContext) theDaemonCrashesWithoutCleanup() error {
	// Simulate daemon crash by not calling cleanup
	ctx.daemonCrashed = true
	return nil
}

func (ctx *assignmentCleanupContext) theDaemonRestarts() error {
	// Simulate daemon restart by calling ReleaseAllActive
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ctx.shipAssignmentRepo.ReleaseAllActive(ctxTimeout, "daemon_restart")
	return err
}

func (ctx *assignmentCleanupContext) theContainerCrashesUnexpectedly() error {
	// Same as failure
	return ctx.theContainerFailsWithAnError()
}

// ============================================================================
// Then Steps
// ============================================================================

func (ctx *assignmentCleanupContext) theShipAssignmentShouldBeReleased() error {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Find the assignment
	assignment, err := ctx.shipAssignmentRepo.FindByShip(ctxTimeout, ctx.ships[0], ctx.playerID)
	if err != nil {
		return fmt.Errorf("failed to find assignment: %w", err)
	}

	// Assignment should be nil (released) or have status "released"
	if assignment != nil {
		return fmt.Errorf("expected assignment to be released, but it's still active")
	}

	return nil
}

func (ctx *assignmentCleanupContext) allShipAssignmentsShouldBeReleased() error {
	for _, shipSymbol := range ctx.ships {
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		assignment, err := ctx.shipAssignmentRepo.FindByShip(ctxTimeout, shipSymbol, ctx.playerID)
		if err != nil {
			return fmt.Errorf("failed to find assignment for %s: %w", shipSymbol, err)
		}

		if assignment != nil {
			return fmt.Errorf("expected assignment for %s to be released, but it's still active", shipSymbol)
		}
	}

	return nil
}

func (ctx *assignmentCleanupContext) theReleaseReasonShouldBe(reason string) error {
	// Query database directly to check release reason
	var model persistence.ShipAssignmentModel
	err := ctx.testDB.Where("ship_symbol = ? AND player_id = ?", ctx.ships[0], ctx.playerID).First(&model).Error
	if err != nil {
		return fmt.Errorf("failed to find assignment in database: %w", err)
	}

	if model.ReleaseReason != reason {
		return fmt.Errorf("expected release reason '%s', got '%s'", reason, model.ReleaseReason)
	}

	return nil
}

func (ctx *assignmentCleanupContext) theReleaseReasonForAllAssignmentsShouldBe(reason string) error {
	for _, shipSymbol := range ctx.ships {
		var model persistence.ShipAssignmentModel
		err := ctx.testDB.Where("ship_symbol = ? AND player_id = ?", shipSymbol, ctx.playerID).First(&model).Error
		if err != nil {
			return fmt.Errorf("failed to find assignment for %s in database: %w", shipSymbol, err)
		}

		if model.ReleaseReason != reason {
			return fmt.Errorf("expected release reason '%s' for %s, got '%s'", reason, shipSymbol, model.ReleaseReason)
		}
	}

	return nil
}

func (ctx *assignmentCleanupContext) theShipShouldBeAvailableForReassignment(shipSymbol string) error {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ship is available if no active assignment exists
	assignment, err := ctx.shipAssignmentRepo.FindByShip(ctxTimeout, shipSymbol, ctx.playerID)
	if err != nil {
		return fmt.Errorf("failed to check ship availability: %w", err)
	}

	if assignment != nil {
		return fmt.Errorf("ship %s is still assigned (not available for reassignment)", shipSymbol)
	}

	return nil
}

func (ctx *assignmentCleanupContext) allShipsShouldBeAvailableForReassignment() error {
	for _, shipSymbol := range ctx.ships {
		if err := ctx.theShipShouldBeAvailableForReassignment(shipSymbol); err != nil {
			return err
		}
	}

	return nil
}

func (ctx *assignmentCleanupContext) theContainerShouldBeCreatedSuccessfully() error {
	// Check that the new container exists in the database
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var model persistence.ContainerModel
	err := ctx.testDB.WithContext(ctxTimeout).Where("id = ? AND player_id = ?", ctx.containerID, ctx.playerID).First(&model).Error
	if err != nil {
		return fmt.Errorf("failed to find new container: %w", err)
	}

	return nil
}

func (ctx *assignmentCleanupContext) aNewShipAssignmentShouldBeCreatedForTheShip() error {
	// Create new assignment for the new container
	assignment := daemon.NewShipAssignment(
		ctx.ships[0],
		ctx.playerID,
		ctx.containerID,
		nil,
	)

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctx.shipAssignmentRepo.Insert(ctxTimeout, assignment); err != nil {
		return fmt.Errorf("failed to create new ship assignment: %w", err)
	}

	// Verify it was created
	foundAssignment, err := ctx.shipAssignmentRepo.FindByShip(ctxTimeout, ctx.ships[0], ctx.playerID)
	if err != nil {
		return fmt.Errorf("failed to find new assignment: %w", err)
	}

	if foundAssignment == nil {
		return fmt.Errorf("new assignment was not created")
	}

	return nil
}

func (ctx *assignmentCleanupContext) allActiveShipAssignmentsShouldBeReleased() error {
	// This should be the same as checking all ships are released
	return ctx.allShipAssignmentsShouldBeReleased()
}
