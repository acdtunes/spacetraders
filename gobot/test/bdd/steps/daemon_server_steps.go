package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/grpc"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/cucumber/godog"
	grpcLib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// daemonServerContext holds state for daemon server BDD tests
type daemonServerContext struct {
	// Server state
	server     *grpc.DaemonServer
	socketPath string
	startErr   error

	// Client state
	grpcClient pb.DaemonServiceClient
	grpcConn   *grpcLib.ClientConn

	// Test infrastructure
	mediator *mockDaemonMediator
	logRepo  *mockContainerLogRepo

	// Response tracking
	lastResponse interface{}
	startTime    time.Time
	responseTime time.Duration

	// Container tracking
	containerIDs   []string
	containerCount int
}

// mockDaemonMediator implements common.Mediator for daemon server testing
type mockDaemonMediator struct {
	common.Mediator
}

func (m *mockDaemonMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	// For daemon server tests, we just need to not error on command execution
	// The actual command logic is tested elsewhere
	return nil, nil
}

func (m *mockDaemonMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

// mockContainerLogRepo implements persistence.ContainerLogRepository for testing
type mockContainerLogRepo struct {
	logs []string
}

func (m *mockContainerLogRepo) Log(ctx context.Context, containerID string, playerID int, message, level string) error {
	m.logs = append(m.logs, fmt.Sprintf("[%s] %s: %s", level, containerID, message))
	return nil
}

func (m *mockContainerLogRepo) GetLogs(ctx context.Context, containerID string, playerID int, limit int, level *string, since *time.Time) ([]persistence.ContainerLogEntry, error) {
	return []persistence.ContainerLogEntry{}, nil
}

func (m *mockContainerLogRepo) GetLogsWithOffset(ctx context.Context, containerID string, playerID int, limit, offset int, level *string, since *time.Time) ([]persistence.ContainerLogEntry, error) {
	return []persistence.ContainerLogEntry{}, nil
}

func (ctx *daemonServerContext) reset() {
	// Clean up any existing server
	if ctx.server != nil {
		ctx.stopDaemonServer()
	}

	// Clean up socket if it exists
	if ctx.socketPath != "" {
		os.RemoveAll(ctx.socketPath)
	}

	// Reset state
	ctx.server = nil
	ctx.socketPath = ""
	ctx.startErr = nil
	ctx.grpcClient = nil
	ctx.grpcConn = nil
	ctx.mediator = &mockDaemonMediator{}
	ctx.logRepo = &mockContainerLogRepo{logs: []string{}}
	ctx.lastResponse = nil
	ctx.startTime = time.Time{}
	ctx.responseTime = 0
	ctx.containerIDs = []string{}
	ctx.containerCount = 0
}

// InitializeDaemonServerScenario registers step definitions for daemon server tests
func InitializeDaemonServerScenario(sc *godog.ScenarioContext) {
	sCtx := &daemonServerContext{}

	// Given steps - Setup
	sc.Step(`^the daemon server is not running$`, sCtx.theDaemonServerIsNotRunning)
	sc.Step(`^a stale Unix socket exists at "([^"]*)"$`, sCtx.aStaleUnixSocketExistsAt)
	sc.Step(`^the daemon server is running on socket "([^"]*)"$`, sCtx.theDaemonServerIsRunningOnSocket)
	sc.Step(`^a gRPC client is connected$`, sCtx.aGRPCClientIsConnected)
	sc.Step(`^a ship "([^"]*)" exists for player (\d+)$`, sCtx.aShipExistsForPlayer)
	sc.Step(`^ships exist for player (\d+):$`, sCtx.shipsExistForPlayer)
	sc.Step(`^(\d+) containers are running in the background$`, sCtx.containersAreRunningInBackground)
	sc.Step(`^a container is running that will complete in (\d+) seconds$`, sCtx.aContainerIsRunningThatWillCompleteInSeconds)
	sc.Step(`^a container is running that will take (\d+) seconds to complete$`, sCtx.aContainerIsRunningThatWillTakeSecondsToComplete)
	sc.Step(`^(\d+) containers are running in the background$`, sCtx.fiveContainersAreRunningInBackground)
	sc.Step(`^the database has (\d+) active connections from the daemon$`, sCtx.theDatabaseHasActiveConnectionsFromDaemon)
	sc.Step(`^(\d+) ships are assigned to running containers$`, sCtx.shipsAreAssignedToRunningContainers)
	sc.Step(`^(\d+) containers have been executed and completed$`, sCtx.containersHaveBeenExecutedAndCompleted)
	sc.Step(`^a container is running in the background$`, sCtx.aContainerIsRunningInBackground)
	sc.Step(`^(\d+) containers are running that will fail with errors$`, sCtx.containersAreRunningThatWillFailWithErrors)

	// When steps - Actions
	sc.Step(`^I start the daemon server on socket "([^"]*)"$`, sCtx.iStartTheDaemonServerOnSocket)
	sc.Step(`^I attempt to start the daemon server on invalid socket "([^"]*)"$`, sCtx.iAttemptToStartTheDaemonServerOnInvalidSocket)
	sc.Step(`^a gRPC client connects to the Unix socket$`, sCtx.aGRPCClientConnectsToTheUnixSocket)
	sc.Step(`^the client sends a HealthCheck request$`, sCtx.theClientSendsAHealthCheckRequest)
	sc.Step(`^the client sends a NavigateShip request for ship "([^"]*)" to "([^"]*)"$`, sCtx.theClientSendsNavigateShipRequest)
	sc.Step(`^the client disconnects after receiving the container_id$`, sCtx.theClientDisconnectsAfterReceivingContainerID)
	sc.Step(`^the client sends NavigateShip requests for all (\d+) ships$`, sCtx.theClientSendsNavigateShipRequestsForAllShips)
	sc.Step(`^I send SIGTERM signal to the daemon$`, sCtx.iSendSIGTERMSignalToTheDaemon)
	sc.Step(`^I send SIGINT signal to the daemon$`, sCtx.iSendSIGINTSignalToTheDaemon)
	sc.Step(`^I send another SIGTERM signal (\d+) second later$`, sCtx.iSendAnotherSIGTERMSignalSecondLater)
	sc.Step(`^the (\d+)-second timeout expires$`, sCtx.theSecondTimeoutExpires)
	sc.Step(`^the daemon completes shutdown$`, sCtx.theDaemonCompletesShutdown)

	// Then steps - Assertions
	sc.Step(`^the daemon server should be running$`, sCtx.theDaemonServerShouldBeRunning)
	sc.Step(`^the Unix socket should exist at "([^"]*)"$`, sCtx.theUnixSocketShouldExistAt)
	sc.Step(`^the socket permissions should be (\d+)$`, sCtx.theSocketPermissionsShouldBe)
	sc.Step(`^the daemon server should start successfully$`, sCtx.theDaemonServerShouldStartSuccessfully)
	sc.Step(`^the socket should be active$`, sCtx.theSocketShouldBeActive)
	sc.Step(`^the daemon startup should fail$`, sCtx.theDaemonStartupShouldFail)
	sc.Step(`^the error should mention "([^"]*)"$`, sCtx.theErrorShouldMention)
	sc.Step(`^the connection should be accepted$`, sCtx.theConnectionShouldBeAccepted)
	sc.Step(`^the client should receive a valid gRPC server response$`, sCtx.theClientShouldReceiveValidGRPCServerResponse)
	sc.Step(`^the response should have status "([^"]*)"$`, sCtx.theResponseShouldHaveStatus)
	sc.Step(`^the response should include version "([^"]*)"$`, sCtx.theResponseShouldIncludeVersion)
	sc.Step(`^the response should include active_containers count$`, sCtx.theResponseShouldIncludeActiveContainersCount)
	sc.Step(`^the response should include a container_id$`, sCtx.theResponseShouldIncludeContainerID)
	sc.Step(`^the container should be registered in the daemon$`, sCtx.theContainerShouldBeRegisteredInDaemon)
	sc.Step(`^a container should be created with type "([^"]*)"$`, sCtx.aContainerShouldBeCreatedWithType)
	sc.Step(`^the container metadata should include ship_symbol "([^"]*)"$`, sCtx.theContainerMetadataShouldIncludeShipSymbol)
	sc.Step(`^the container metadata should include destination "([^"]*)"$`, sCtx.theContainerMetadataShouldIncludeDestination)
	sc.Step(`^the container should have player_id (\d+)$`, sCtx.theContainerShouldHavePlayerID)
	sc.Step(`^the response should return within (\d+) milliseconds$`, sCtx.theResponseShouldReturnWithinMilliseconds)
	sc.Step(`^the container should not be in COMPLETED status yet$`, sCtx.theContainerShouldNotBeInCompletedStatusYet)
	sc.Step(`^the container should continue executing in the background$`, sCtx.theContainerShouldContinueExecutingInBackground)
	sc.Step(`^the container status should eventually transition to "([^"]*)"$`, sCtx.theContainerStatusShouldEventuallyTransitionTo)
	sc.Step(`^(\d+) containers should be created$`, sCtx.containersShouldBeCreated)
	sc.Step(`^all containers should be registered in the daemon$`, sCtx.allContainersShouldBeRegisteredInDaemon)
	sc.Step(`^each container should have a unique container_id$`, sCtx.eachContainerShouldHaveUniqueContainerID)
	sc.Step(`^the daemon should initiate graceful shutdown$`, sCtx.theDaemonShouldInitiateGracefulShutdown)
	sc.Step(`^the daemon should stop accepting new connections$`, sCtx.theDaemonShouldStopAcceptingNewConnections)
	sc.Step(`^the daemon should wait for running containers to finish$`, sCtx.theDaemonShouldWaitForRunningContainersToFinish)
	sc.Step(`^the daemon should wait for the container to complete$`, sCtx.theDaemonShouldWaitForContainerToComplete)
	sc.Step(`^the container should finish successfully$`, sCtx.theContainerShouldFinishSuccessfully)
	sc.Step(`^the daemon should shut down gracefully$`, sCtx.theDaemonShouldShutDownGracefully)
	sc.Step(`^the daemon should wait up to (\d+) seconds$`, sCtx.theDaemonShouldWaitUpToSeconds)
	sc.Step(`^the daemon should force shutdown after (\d+) seconds$`, sCtx.theDaemonShouldForceShutdownAfterSeconds)
	sc.Step(`^a warning should be logged about containers not stopping within timeout$`, sCtx.aWarningShouldBeLoggedAboutContainersNotStoppingWithinTimeout)
	sc.Step(`^the daemon should stop all running containers$`, sCtx.theDaemonShouldStopAllRunningContainers)
	sc.Step(`^all containers should transition to STOPPED status$`, sCtx.allContainersShouldTransitionToStoppedStatus)
	sc.Step(`^all database connections should be closed$`, sCtx.allDatabaseConnectionsShouldBeClosed)
	sc.Step(`^the connection pool should be released$`, sCtx.theConnectionPoolShouldBeReleased)
	sc.Step(`^all ship assignments should be released$`, sCtx.allShipAssignmentsShouldBeReleased)
	sc.Step(`^the ship assignment release_reason should be "([^"]*)"$`, sCtx.theShipAssignmentReleaseReasonShouldBe)
	sc.Step(`^no ships should remain locked after shutdown$`, sCtx.noShipsShouldRemainLockedAfterShutdown)
	sc.Step(`^the Unix socket at "([^"]*)" should be removed$`, sCtx.theUnixSocketAtShouldBeRemoved)
	sc.Step(`^the socket file should not exist$`, sCtx.theSocketFileShouldNotExist)
	sc.Step(`^all goroutines spawned by containers should terminate$`, sCtx.allGoroutinesSpawnedByContainersShouldTerminate)
	sc.Step(`^no goroutine leaks should be present$`, sCtx.noGoroutineLeaksShouldBePresent)
	sc.Step(`^the daemon process should exit cleanly$`, sCtx.theDaemonProcessShouldExitCleanly)
	sc.Step(`^the daemon should shut down within (\d+) seconds$`, sCtx.theDaemonShouldShutDownWithinSeconds)
	sc.Step(`^the daemon should continue graceful shutdown$`, sCtx.theDaemonShouldContinueGracefulShutdown)
	sc.Step(`^the daemon should not panic or exit immediately$`, sCtx.theDaemonShouldNotPanicOrExitImmediately)
	sc.Step(`^the container should still be allowed to finish$`, sCtx.theContainerShouldStillBeAllowedToFinish)
	sc.Step(`^the daemon should wait for containers to fail$`, sCtx.theDaemonShouldWaitForContainersToFail)
	sc.Step(`^the daemon should complete shutdown successfully$`, sCtx.theDaemonShouldCompleteShutdownSuccessfully)
	sc.Step(`^the failed containers should be marked as FAILED$`, sCtx.theFailedContainersShouldBeMarkedAsFailed)

	// Before hook
	sc.Before(func(gCtx context.Context, sc *godog.Scenario) (context.Context, error) {
		sCtx.reset()
		return gCtx, nil
	})

	// After hook - cleanup
	sc.After(func(gCtx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		sCtx.reset()
		return gCtx, nil
	})
}

// ============================================================================
// Given Steps - Setup
// ============================================================================

func (ctx *daemonServerContext) theDaemonServerIsNotRunning() error {
	// Server is already nil from reset
	return nil
}

func (ctx *daemonServerContext) aStaleUnixSocketExistsAt(socketPath string) error {
	ctx.socketPath = socketPath
	// Create a stale socket file
	return os.WriteFile(socketPath, []byte{}, 0600)
}

func (ctx *daemonServerContext) theDaemonServerIsRunningOnSocket(socketPath string) error {
	return ctx.iStartTheDaemonServerOnSocket(socketPath)
}

func (ctx *daemonServerContext) aGRPCClientIsConnected() error {
	return ctx.aGRPCClientConnectsToTheUnixSocket()
}

func (ctx *daemonServerContext) aShipExistsForPlayer(shipSymbol string, playerID int) error {
	// For daemon server tests, we don't need to actually create ships
	// The mediator mock will handle any ship-related queries
	return nil
}

func (ctx *daemonServerContext) shipsExistForPlayer(playerID int, ships *godog.Table) error {
	// For daemon server tests, we don't need to actually create ships
	return nil
}

func (ctx *daemonServerContext) containersAreRunningInBackground(count int) error {
	// TODO: Actually create containers in the daemon
	return godog.ErrPending
}

func (ctx *daemonServerContext) aContainerIsRunningThatWillCompleteInSeconds(seconds int) error {
	// TODO: Create a container with timed completion
	return godog.ErrPending
}

func (ctx *daemonServerContext) aContainerIsRunningThatWillTakeSecondsToComplete(seconds int) error {
	// TODO: Create a long-running container
	return godog.ErrPending
}

func (ctx *daemonServerContext) fiveContainersAreRunningInBackground(count int) error {
	// TODO: Create multiple containers
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDatabaseHasActiveConnectionsFromDaemon(count int) error {
	// TODO: Track database connections
	return godog.ErrPending
}

func (ctx *daemonServerContext) shipsAreAssignedToRunningContainers(count int) error {
	// TODO: Create ship assignments
	return godog.ErrPending
}

func (ctx *daemonServerContext) containersHaveBeenExecutedAndCompleted(count int) error {
	// TODO: Execute and complete containers
	return godog.ErrPending
}

func (ctx *daemonServerContext) aContainerIsRunningInBackground() error {
	// TODO: Create a running container
	return godog.ErrPending
}

func (ctx *daemonServerContext) containersAreRunningThatWillFailWithErrors(count int) error {
	// TODO: Create failing containers
	return godog.ErrPending
}

// ============================================================================
// When Steps - Actions
// ============================================================================

func (ctx *daemonServerContext) iStartTheDaemonServerOnSocket(socketPath string) error {
	ctx.socketPath = socketPath

	// Ensure temp directory exists
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		ctx.startErr = err
		return nil
	}

	// Create daemon server
	server, err := grpc.NewDaemonServer(ctx.mediator, ctx.logRepo, socketPath)
	if err != nil {
		ctx.startErr = err
		return nil
	}

	ctx.server = server
	ctx.startErr = nil

	// Start server in background
	go func() {
		server.Start()
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	return nil
}

func (ctx *daemonServerContext) iAttemptToStartTheDaemonServerOnInvalidSocket(socketPath string) error {
	return ctx.iStartTheDaemonServerOnSocket(socketPath)
}

func (ctx *daemonServerContext) aGRPCClientConnectsToTheUnixSocket() error {
	if ctx.socketPath == "" {
		return fmt.Errorf("no socket path configured")
	}

	// Connect to Unix socket
	conn, err := grpcLib.Dial(
		"unix://"+ctx.socketPath,
		grpcLib.WithTransportCredentials(insecure.NewCredentials()),
		grpcLib.WithBlock(),
		grpcLib.WithTimeout(2*time.Second),
	)
	if err != nil {
		return err
	}

	ctx.grpcConn = conn
	ctx.grpcClient = pb.NewDaemonServiceClient(conn)
	return nil
}

func (ctx *daemonServerContext) theClientSendsAHealthCheckRequest() error {
	ctx.startTime = time.Now()
	resp, err := ctx.grpcClient.HealthCheck(context.Background(), &pb.HealthCheckRequest{})
	ctx.responseTime = time.Since(ctx.startTime)
	if err != nil {
		return err
	}
	ctx.lastResponse = resp
	return nil
}

func (ctx *daemonServerContext) theClientSendsNavigateShipRequest(shipSymbol, destination string) error {
	ctx.startTime = time.Now()
	resp, err := ctx.grpcClient.NavigateShip(context.Background(), &pb.NavigateShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    1,
	})
	ctx.responseTime = time.Since(ctx.startTime)
	if err != nil {
		return err
	}
	ctx.lastResponse = resp
	if resp != nil && resp.ContainerId != "" {
		ctx.containerIDs = append(ctx.containerIDs, resp.ContainerId)
	}
	return nil
}

func (ctx *daemonServerContext) theClientDisconnectsAfterReceivingContainerID() error {
	if ctx.grpcConn != nil {
		return ctx.grpcConn.Close()
	}
	return nil
}

func (ctx *daemonServerContext) theClientSendsNavigateShipRequestsForAllShips(count int) error {
	for i := 1; i <= count; i++ {
		shipSymbol := fmt.Sprintf("TEST-SHIP-%d", i)
		if err := ctx.theClientSendsNavigateShipRequest(shipSymbol, "X1-TEST-DEST"); err != nil {
			return err
		}
	}
	return nil
}

func (ctx *daemonServerContext) iSendSIGTERMSignalToTheDaemon() error {
	// TODO: Send actual SIGTERM signal
	return godog.ErrPending
}

func (ctx *daemonServerContext) iSendSIGINTSignalToTheDaemon() error {
	// TODO: Send actual SIGINT signal
	return godog.ErrPending
}

func (ctx *daemonServerContext) iSendAnotherSIGTERMSignalSecondLater(seconds int) error {
	// TODO: Send second SIGTERM after delay
	return godog.ErrPending
}

func (ctx *daemonServerContext) theSecondTimeoutExpires(seconds int) error {
	// TODO: Wait for timeout
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonCompletesShutdown() error {
	// TODO: Wait for daemon shutdown
	return godog.ErrPending
}

func (ctx *daemonServerContext) stopDaemonServer() {
	if ctx.grpcConn != nil {
		ctx.grpcConn.Close()
		ctx.grpcConn = nil
	}
	// Note: Actual daemon server shutdown would require signal handling
	if ctx.socketPath != "" {
		os.RemoveAll(ctx.socketPath)
	}
}

// ============================================================================
// Then Steps - Assertions
// ============================================================================

func (ctx *daemonServerContext) theDaemonServerShouldBeRunning() error {
	if ctx.server == nil {
		return fmt.Errorf("daemon server is not running")
	}
	return nil
}

func (ctx *daemonServerContext) theUnixSocketShouldExistAt(socketPath string) error {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return fmt.Errorf("Unix socket does not exist at %s", socketPath)
	}
	return nil
}

func (ctx *daemonServerContext) theSocketPermissionsShouldBe(expectedPerms int) error {
	info, err := os.Stat(ctx.socketPath)
	if err != nil {
		return err
	}
	actualPerms := int(info.Mode().Perm())
	if actualPerms != expectedPerms {
		return fmt.Errorf("expected socket permissions %o, got %o", expectedPerms, actualPerms)
	}
	return nil
}

func (ctx *daemonServerContext) theDaemonServerShouldStartSuccessfully() error {
	if ctx.startErr != nil {
		return fmt.Errorf("daemon server failed to start: %v", ctx.startErr)
	}
	return nil
}

func (ctx *daemonServerContext) theSocketShouldBeActive() error {
	// Try to connect to verify socket is active
	return ctx.aGRPCClientConnectsToTheUnixSocket()
}

func (ctx *daemonServerContext) theDaemonStartupShouldFail() error {
	if ctx.startErr == nil {
		return fmt.Errorf("expected daemon startup to fail, but it succeeded")
	}
	return nil
}

func (ctx *daemonServerContext) theErrorShouldMention(expectedMessage string) error {
	if ctx.startErr == nil {
		return fmt.Errorf("no error occurred")
	}
	if !strings.Contains(ctx.startErr.Error(), expectedMessage) {
		return fmt.Errorf("expected error to mention '%s', got: %v", expectedMessage, ctx.startErr)
	}
	return nil
}

func (ctx *daemonServerContext) theConnectionShouldBeAccepted() error {
	if ctx.grpcClient == nil {
		return fmt.Errorf("gRPC client not connected")
	}
	return nil
}

func (ctx *daemonServerContext) theClientShouldReceiveValidGRPCServerResponse() error {
	// Try a health check
	return ctx.theClientSendsAHealthCheckRequest()
}

func (ctx *daemonServerContext) theResponseShouldHaveStatus(expectedStatus string) error {
	if resp, ok := ctx.lastResponse.(*pb.HealthCheckResponse); ok {
		if resp.Status != expectedStatus {
			return fmt.Errorf("expected status '%s', got '%s'", expectedStatus, resp.Status)
		}
		return nil
	}
	if resp, ok := ctx.lastResponse.(*pb.NavigateShipResponse); ok {
		if resp.Status != expectedStatus {
			return fmt.Errorf("expected status '%s', got '%s'", expectedStatus, resp.Status)
		}
		return nil
	}
	return fmt.Errorf("unexpected response type: %T", ctx.lastResponse)
}

func (ctx *daemonServerContext) theResponseShouldIncludeVersion(expectedVersion string) error {
	resp, ok := ctx.lastResponse.(*pb.HealthCheckResponse)
	if !ok {
		return fmt.Errorf("response is not a HealthCheckResponse")
	}
	if resp.Version != expectedVersion {
		return fmt.Errorf("expected version '%s', got '%s'", expectedVersion, resp.Version)
	}
	return nil
}

func (ctx *daemonServerContext) theResponseShouldIncludeActiveContainersCount() error {
	resp, ok := ctx.lastResponse.(*pb.HealthCheckResponse)
	if !ok {
		return fmt.Errorf("response is not a HealthCheckResponse")
	}
	// Just verify the field exists (can be 0 for empty daemon)
	_ = resp.ActiveContainers
	return nil
}

func (ctx *daemonServerContext) theResponseShouldIncludeContainerID() error {
	resp, ok := ctx.lastResponse.(*pb.NavigateShipResponse)
	if !ok {
		return fmt.Errorf("response is not a NavigateShipResponse")
	}
	if resp.ContainerId == "" {
		return fmt.Errorf("response does not include container_id")
	}
	return nil
}

func (ctx *daemonServerContext) theContainerShouldBeRegisteredInDaemon() error {
	// TODO: Query daemon for container registration
	return godog.ErrPending
}

func (ctx *daemonServerContext) aContainerShouldBeCreatedWithType(containerType string) error {
	// TODO: Verify container type
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerMetadataShouldIncludeShipSymbol(shipSymbol string) error {
	// TODO: Verify container metadata
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerMetadataShouldIncludeDestination(destination string) error {
	// TODO: Verify container metadata
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerShouldHavePlayerID(playerID int) error {
	// TODO: Verify container player ID
	return godog.ErrPending
}

func (ctx *daemonServerContext) theResponseShouldReturnWithinMilliseconds(maxMillis int) error {
	maxDuration := time.Duration(maxMillis) * time.Millisecond
	if ctx.responseTime > maxDuration {
		return fmt.Errorf("response took %v, expected within %v", ctx.responseTime, maxDuration)
	}
	return nil
}

func (ctx *daemonServerContext) theContainerShouldNotBeInCompletedStatusYet() error {
	// TODO: Query container status
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerShouldContinueExecutingInBackground() error {
	// TODO: Verify background execution
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerStatusShouldEventuallyTransitionTo(status string) error {
	// TODO: Poll container status
	return godog.ErrPending
}

func (ctx *daemonServerContext) containersShouldBeCreated(count int) error {
	if len(ctx.containerIDs) != count {
		return fmt.Errorf("expected %d containers, got %d", count, len(ctx.containerIDs))
	}
	return nil
}

func (ctx *daemonServerContext) allContainersShouldBeRegisteredInDaemon() error {
	// TODO: Verify all containers registered
	return godog.ErrPending
}

func (ctx *daemonServerContext) eachContainerShouldHaveUniqueContainerID() error {
	seen := make(map[string]bool)
	for _, id := range ctx.containerIDs {
		if seen[id] {
			return fmt.Errorf("duplicate container ID: %s", id)
		}
		seen[id] = true
	}
	return nil
}

// All shutdown-related assertions are pending (require signal handling implementation)
func (ctx *daemonServerContext) theDaemonShouldInitiateGracefulShutdown() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldStopAcceptingNewConnections() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldWaitForRunningContainersToFinish() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldWaitForContainerToComplete() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerShouldFinishSuccessfully() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldShutDownGracefully() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldWaitUpToSeconds(seconds int) error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldForceShutdownAfterSeconds(seconds int) error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) aWarningShouldBeLoggedAboutContainersNotStoppingWithinTimeout() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldStopAllRunningContainers() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) allContainersShouldTransitionToStoppedStatus() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) allDatabaseConnectionsShouldBeClosed() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theConnectionPoolShouldBeReleased() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) allShipAssignmentsShouldBeReleased() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theShipAssignmentReleaseReasonShouldBe(reason string) error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) noShipsShouldRemainLockedAfterShutdown() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theUnixSocketAtShouldBeRemoved(socketPath string) error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theSocketFileShouldNotExist() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) allGoroutinesSpawnedByContainersShouldTerminate() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) noGoroutineLeaksShouldBePresent() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonProcessShouldExitCleanly() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldShutDownWithinSeconds(seconds int) error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldContinueGracefulShutdown() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldNotPanicOrExitImmediately() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theContainerShouldStillBeAllowedToFinish() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldWaitForContainersToFail() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theDaemonShouldCompleteShutdownSuccessfully() error {
	return godog.ErrPending
}

func (ctx *daemonServerContext) theFailedContainersShouldBeMarkedAsFailed() error {
	return godog.ErrPending
}
