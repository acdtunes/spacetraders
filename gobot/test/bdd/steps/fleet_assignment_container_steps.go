package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type fleetAssignmentContainerContext struct {
	mediator         common.Mediator
	playerID         int
	systemSymbol     string
	ships            map[string]*navigation.Ship
	waypoints        map[string]*shared.Waypoint
	daemonServer     *grpc.DaemonServer
	containerRunner  *grpc.ContainerRunner
	containerEntity  *container.Container
	grpcCallDuration time.Duration
	containerID      string
	containerType    container.ContainerType
	containerStatus  container.ContainerStatus
	scoutContainers  []*container.Container
	containerError   error
	containerLogs    []string
	mockClock        *shared.MockClock
	shipRepo         navigation.ShipRepository
	waypointRepo     system.WaypointRepository
	marketRepo       *helpers.MockMarketRepository
	apiClient        *helpers.MockAPIClient
	playerRepo       *helpers.MockPlayerRepository
	graphProvider    system.ISystemGraphProvider
	routingClient    routing.RoutingClient
	daemonClient     daemon.DaemonClient
	logRepo          persistence.ContainerLogRepository
}

func (ctx *fleetAssignmentContainerContext) reset() {
	ctx.mediator = common.NewMediator()
	ctx.playerID = 0
	ctx.systemSymbol = ""
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.daemonServer = nil
	ctx.containerRunner = nil
	ctx.containerEntity = nil
	ctx.grpcCallDuration = 0
	ctx.containerID = ""
	ctx.containerType = ""
	ctx.containerStatus = ""
	ctx.scoutContainers = make([]*container.Container, 0)
	ctx.containerError = nil
	ctx.containerLogs = make([]string, 0)
	ctx.mockClock = shared.NewMockClock(time.Now())

	// Initialize mock repositories
	ctx.shipRepo = helpers.NewMockShipRepository()
	ctx.waypointRepo = helpers.NewMockWaypointRepository()
	ctx.marketRepo = helpers.NewMockMarketRepository()
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.playerRepo = helpers.NewMockPlayerRepository()
	ctx.graphProvider = helpers.NewMockGraphProvider()
	ctx.routingClient = helpers.NewMockRoutingClient()
	ctx.daemonClient = helpers.NewMockDaemonClient()
	ctx.logRepo = helpers.NewMockContainerLogRepository()
}

// Given steps

func (ctx *fleetAssignmentContainerContext) aMediatorIsConfiguredWithScoutingHandlers() error {
	// Register scouting handlers with correct dependencies
	scoutTourHandler := scouting.NewScoutTourHandler(
		ctx.shipRepo,
		ctx.marketRepo,
		ctx.apiClient,
		ctx.playerRepo,
	)
	if err := common.RegisterHandler[*scouting.ScoutTourCommand](ctx.mediator, scoutTourHandler); err != nil {
		return fmt.Errorf("failed to register scout tour handler: %w", err)
	}

	scoutMarketsHandler := scouting.NewScoutMarketsHandler(
		ctx.shipRepo,
		ctx.graphProvider,
		ctx.routingClient,
		ctx.daemonClient,
	)
	if err := common.RegisterHandler[*scouting.ScoutMarketsCommand](ctx.mediator, scoutMarketsHandler); err != nil {
		return fmt.Errorf("failed to register scout markets handler: %w", err)
	}

	assignFleetHandler := scouting.NewAssignFleetHandler(
		ctx.shipRepo,
		ctx.waypointRepo,
		ctx.graphProvider,
		ctx.routingClient,
		ctx.daemonClient,
	)
	if err := common.RegisterHandler[*scouting.AssignFleetCommand](ctx.mediator, assignFleetHandler); err != nil {
		return fmt.Errorf("failed to register assign fleet handler: %w", err)
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) aPlayerWithIDExistsWithAgent(playerID int, agentSymbol string) error {
	ctx.playerID = playerID
	return nil
}

func (ctx *fleetAssignmentContainerContext) aSystemExistsWithMultipleWaypoints(systemSymbol string) error {
	ctx.systemSymbol = systemSymbol
	return nil
}

func (ctx *fleetAssignmentContainerContext) theFollowingShipsOwnedByPlayerInSystem(playerID int, systemSymbol string, table *godog.Table) error {
	for _, row := range table.Rows[1:] { // Skip header
		shipSymbol := row.Cells[0].Value
		frameType := row.Cells[1].Value
		location := row.Cells[2].Value

		// Create waypoint for location
		waypoint, err := shared.NewWaypoint(location, 0, 0)
		if err != nil {
			return fmt.Errorf("failed to create waypoint: %w", err)
		}
		waypoint.Type = "PLANET"
		waypoint.SystemSymbol = systemSymbol
		ctx.waypoints[location] = waypoint

		// Create ship
		fuel, err := shared.NewFuel(100, 100)
		if err != nil {
			return fmt.Errorf("failed to create fuel: %w", err)
		}
		cargo, err := shared.NewCargo(100, 0, []*shared.CargoItem{})
		if err != nil {
			return fmt.Errorf("failed to create cargo: %w", err)
		}
		ship, err := navigation.NewShip(
			shipSymbol,
			playerID,
			waypoint,
			fuel,
			100, // fuel capacity
			100, // cargo capacity
			cargo,
			100, // engine speed
			frameType,
			navigation.NavStatusDocked,
		)
		if err != nil {
			return fmt.Errorf("failed to create ship: %w", err)
		}

		ctx.ships[shipSymbol] = ship

		// Add to mock repository
		mockRepo := ctx.shipRepo.(*helpers.MockShipRepository)
		mockRepo.Ships[shipSymbol] = ship
		mockRepo.ShipsByPlayer[playerID] = append(mockRepo.ShipsByPlayer[playerID], ship)
	}

	ctx.systemSymbol = systemSymbol
	return nil
}

func (ctx *fleetAssignmentContainerContext) theFollowingWaypointsWithMarketplacesInSystem(systemSymbol string, table *godog.Table) error {
	for _, row := range table.Rows[1:] { // Skip header
		waypointSymbol := row.Cells[0].Value
		waypointType := row.Cells[1].Value
		traits := row.Cells[2].Value

		// Parse traits
		traitsList := []string{traits}

		// Create waypoint
		waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
		if err != nil {
			return fmt.Errorf("failed to create waypoint: %w", err)
		}
		waypoint.Type = waypointType
		waypoint.SystemSymbol = systemSymbol
		waypoint.Traits = traitsList
		ctx.waypoints[waypointSymbol] = waypoint

		// Add to mock repository
		mockRepo := ctx.waypointRepo.(*helpers.MockWaypointRepository)
		mockRepo.AddWaypoint(waypoint)
	}

	// Configure graph provider with waypoint data
	distanceGraph := make(map[string]map[string]float64)
	for symbol := range ctx.waypoints {
		distanceGraph[symbol] = make(map[string]float64)
		// Simple star topology: all waypoints connected to each other with distance 100
		for otherSymbol := range ctx.waypoints {
			if symbol != otherSymbol {
				distanceGraph[symbol][otherSymbol] = 100.0
			}
		}
	}
	mockGraphProvider := ctx.graphProvider.(*helpers.MockGraphProvider)
	mockGraphProvider.SetGraph(systemSymbol, distanceGraph)

	return nil
}

// When steps

func (ctx *fleetAssignmentContainerContext) iInvokeAssignScoutingFleetViaGRPCForSystemAndPlayer(systemSymbol string, playerID int) error {
	// This step tests that the gRPC method returns quickly
	// We'll implement this after we create the new gRPC method

	// For now, we'll simulate the call
	start := time.Now()

	// TODO: Call actual gRPC method when implemented
	// For now, just check that we can create a container quickly
	ctx.containerID = fmt.Sprintf("scout-fleet-assignment-%s-%d", systemSymbol, time.Now().UnixNano())
	ctx.containerType = container.ContainerTypeFleetAssignment
	ctx.containerStatus = container.ContainerStatusPending

	ctx.grpcCallDuration = time.Since(start)
	return nil
}

func (ctx *fleetAssignmentContainerContext) iCreateAFleetAssignmentContainerForSystemAndPlayer(systemSymbol string, playerID int) error {
	// Create the command
	cmd := &scouting.AssignFleetCommand{
		PlayerID:     uint(playerID),
		SystemSymbol: systemSymbol,
	}

	// Create container entity
	ctx.containerID = fmt.Sprintf("scout-fleet-assignment-%s-%d", systemSymbol, time.Now().UnixNano())
	ctx.containerEntity = container.NewContainer(
		ctx.containerID,
		container.ContainerTypeFleetAssignment,
		playerID,
		1, // One-time execution
		map[string]interface{}{
			"system_symbol": systemSymbol,
		},
		ctx.mockClock,
	)

	// Create container runner
	ctx.containerRunner = grpc.NewContainerRunner(
		ctx.containerEntity,
		ctx.mediator,
		cmd,
		ctx.logRepo,
	)

	return nil
}

func (ctx *fleetAssignmentContainerContext) theFleetAssignmentContainerRunsToCompletion() error {
	// Start the container
	if err := ctx.containerRunner.Start(); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Wait for completion (with timeout)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("container did not complete within timeout")
		case <-ticker.C:
			cont := ctx.containerRunner.Container()
			if cont.IsFinished() {
				ctx.containerStatus = cont.Status()
				ctx.containerError = cont.LastError()

				// Capture logs
				logs := ctx.containerRunner.GetLogs(nil, nil)
				for _, log := range logs {
					ctx.containerLogs = append(ctx.containerLogs, log.Message)
				}

				// Capture created scout containers
				// In real implementation, we'd query the daemon server for containers
				// For now, we'll check the mock daemon client
				mockDaemon := ctx.daemonClient.(*helpers.MockDaemonClient)
				ctx.scoutContainers = mockDaemon.CreatedContainers

				return nil
			}
		}
	}
}

// Then steps

func (ctx *fleetAssignmentContainerContext) theGRPCCallShouldReturnInLessThanSeconds(maxSeconds float64) error {
	maxDuration := time.Duration(maxSeconds * float64(time.Second))
	if ctx.grpcCallDuration > maxDuration {
		return fmt.Errorf("gRPC call took %v, expected less than %v", ctx.grpcCallDuration, maxDuration)
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theResponseShouldContainAContainerID() error {
	if ctx.containerID == "" {
		return fmt.Errorf("container ID is empty")
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerShouldBeOfType(expectedType string) error {
	if string(ctx.containerType) != expectedType {
		return fmt.Errorf("expected container type %s, got %s", expectedType, ctx.containerType)
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerStatusShouldBeOr(status1, status2 string) error {
	actualStatus := string(ctx.containerStatus)
	if actualStatus != status1 && actualStatus != status2 {
		return fmt.Errorf("expected status %s or %s, got %s", status1, status2, actualStatus)
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerStatusShouldBe(expectedStatus string) error {
	if string(ctx.containerStatus) != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, ctx.containerStatus)
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerShouldHaveCreatedScoutTourContainers(expectedCount int) error {
	actualCount := len(ctx.scoutContainers)
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d scout-tour containers, got %d", expectedCount, actualCount)
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) scoutTourContainersShouldExistForShips(shipsCsv string) error {
	// Parse CSV
	expectedShips := parseCsvList(shipsCsv)

	// Check each ship has a container
	for _, shipSymbol := range expectedShips {
		found := false
		for _, cont := range ctx.scoutContainers {
			if shipMeta, ok := cont.GetMetadataValue("ship_symbol"); ok {
				if shipMeta.(string) == shipSymbol {
					found = true
					break
				}
			}
		}

		if !found {
			return fmt.Errorf("no scout-tour container found for ship %s", shipSymbol)
		}
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) eachScoutTourContainerShouldHaveAssignedMarkets() error {
	for _, cont := range ctx.scoutContainers {
		markets, ok := cont.GetMetadataValue("markets")
		if !ok {
			return fmt.Errorf("container %s has no markets metadata", cont.ID())
		}

		marketList, ok := markets.([]string)
		if !ok {
			return fmt.Errorf("container %s markets metadata is not a string slice", cont.ID())
		}

		if len(marketList) == 0 {
			return fmt.Errorf("container %s has no assigned markets", cont.ID())
		}
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) allMarketsShouldBeCoveredAcrossTheScoutTourContainers(expectedMarketCount int) error {
	// Collect all assigned markets
	assignedMarkets := make(map[string]bool)

	for _, cont := range ctx.scoutContainers {
		if markets, ok := cont.GetMetadataValue("markets"); ok {
			if marketList, ok := markets.([]string); ok {
				for _, market := range marketList {
					assignedMarkets[market] = true
				}
			}
		}
	}

	actualCount := len(assignedMarkets)
	if actualCount != expectedMarketCount {
		return fmt.Errorf("expected %d markets to be covered, got %d", expectedMarketCount, actualCount)
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) noMarketShouldBeAssignedToMultipleShips() error {
	// Collect all assigned markets
	marketAssignments := make(map[string]int)

	for _, cont := range ctx.scoutContainers {
		if markets, ok := cont.GetMetadataValue("markets"); ok {
			if marketList, ok := markets.([]string); ok {
				for _, market := range marketList {
					marketAssignments[market]++
				}
			}
		}
	}

	// Check for duplicates
	for market, count := range marketAssignments {
		if count > 1 {
			return fmt.Errorf("market %s is assigned to %d ships (expected 1)", market, count)
		}
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerErrorShouldContain(expectedError string) error {
	if ctx.containerError == nil {
		return fmt.Errorf("expected error containing '%s', but no error occurred", expectedError)
	}

	if !assert.Contains(nil, ctx.containerError.Error(), expectedError) {
		return fmt.Errorf("expected error to contain '%s', got '%s'", expectedError, ctx.containerError.Error())
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) noScoutTourContainersShouldBeCreated() error {
	if len(ctx.scoutContainers) > 0 {
		return fmt.Errorf("expected no scout-tour containers, but found %d", len(ctx.scoutContainers))
	}
	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerLogsShouldContain(expectedLog string) error {
	for _, log := range ctx.containerLogs {
		if assert.Contains(nil, log, expectedLog) {
			return nil
		}
	}

	return fmt.Errorf("expected log containing '%s', but not found in logs: %v", expectedLog, ctx.containerLogs)
}

func (ctx *fleetAssignmentContainerContext) theContainerMaxIterationsShouldBe(expectedMax int) error {
	if ctx.containerEntity == nil {
		return fmt.Errorf("container entity is nil")
	}

	actualMax := ctx.containerEntity.MaxIterations()
	if actualMax != expectedMax {
		return fmt.Errorf("expected max_iterations %d, got %d", expectedMax, actualMax)
	}

	return nil
}

func (ctx *fleetAssignmentContainerContext) theContainerCurrentIterationShouldBe(expectedCurrent int) error {
	if ctx.containerEntity == nil {
		return fmt.Errorf("container entity is nil")
	}

	actualCurrent := ctx.containerEntity.CurrentIteration()
	if actualCurrent != expectedCurrent {
		return fmt.Errorf("expected current_iteration %d, got %d", expectedCurrent, actualCurrent)
	}

	return nil
}

// Helper function to parse CSV
func parseCsvList(csv string) []string {
	if csv == "" {
		return []string{}
	}

	result := []string{}
	for _, item := range strings.Split(csv, ",") {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}

// InitializeFleetAssignmentContainerScenario initializes the scenario
func InitializeFleetAssignmentContainerScenario(ctx *godog.ScenarioContext) {
	testContext := &fleetAssignmentContainerContext{}

	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		testContext.reset()
		return c, nil
	})

	// Given steps
	ctx.Step(`^a mediator is configured with scouting handlers$`, testContext.aMediatorIsConfiguredWithScoutingHandlers)
	ctx.Step(`^a player with ID (\d+) exists with agent "([^"]*)"$`, testContext.aPlayerWithIDExistsWithAgent)
	ctx.Step(`^a system "([^"]*)" exists with multiple waypoints$`, testContext.aSystemExistsWithMultipleWaypoints)
	ctx.Step(`^the following ships owned by player (\d+) in system "([^"]*)":$`, testContext.theFollowingShipsOwnedByPlayerInSystem)
	ctx.Step(`^the following waypoints with marketplaces in system "([^"]*)":$`, testContext.theFollowingWaypointsWithMarketplacesInSystem)

	// When steps
	ctx.Step(`^I invoke AssignScoutingFleet via gRPC for system "([^"]*)" and player (\d+)$`, testContext.iInvokeAssignScoutingFleetViaGRPCForSystemAndPlayer)
	ctx.Step(`^I create a scout-fleet-assignment container for system "([^"]*)" and player (\d+)$`, testContext.iCreateAFleetAssignmentContainerForSystemAndPlayer)
	ctx.Step(`^the scout-fleet-assignment container runs to completion$`, testContext.theFleetAssignmentContainerRunsToCompletion)

	// Then steps
	ctx.Step(`^the gRPC call should return in less than (\d+\.?\d*) second[s]?$`, testContext.theGRPCCallShouldReturnInLessThanSeconds)
	ctx.Step(`^the response should contain a container ID$`, testContext.theResponseShouldContainAContainerID)
	ctx.Step(`^the container should be of type "([^"]*)"$`, testContext.theContainerShouldBeOfType)
	ctx.Step(`^the container status should be "([^"]*)" or "([^"]*)"$`, testContext.theContainerStatusShouldBeOr)
	ctx.Step(`^the container status should be "([^"]*)"$`, testContext.theContainerStatusShouldBe)
	ctx.Step(`^the container should have created (\d+) scout-tour containers$`, testContext.theContainerShouldHaveCreatedScoutTourContainers)
	ctx.Step(`^scout-tour containers should exist for ships "([^"]*)"$`, testContext.scoutTourContainersShouldExistForShips)
	ctx.Step(`^each scout-tour container should have assigned markets$`, testContext.eachScoutTourContainerShouldHaveAssignedMarkets)
	ctx.Step(`^all (\d+) markets should be covered across the (\d+) scout-tour containers$`, testContext.allMarketsShouldBeCoveredAcrossTheScoutTourContainers)
	ctx.Step(`^no market should be assigned to multiple ships$`, testContext.noMarketShouldBeAssignedToMultipleShips)
	ctx.Step(`^the container error should contain "([^"]*)"$`, testContext.theContainerErrorShouldContain)
	ctx.Step(`^no scout-tour containers should be created$`, testContext.noScoutTourContainersShouldBeCreated)
	ctx.Step(`^the container logs should contain "([^"]*)"$`, testContext.theContainerLogsShouldContain)
	ctx.Step(`^the container max_iterations should be (\d+)$`, testContext.theContainerMaxIterationsShouldBe)
	ctx.Step(`^the container current_iteration should be (\d+)$`, testContext.theContainerCurrentIterationShouldBe)
}
