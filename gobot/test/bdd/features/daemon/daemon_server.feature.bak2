Feature: Daemon Server Lifecycle
  As a SpaceTraders bot operator
  I need a daemon server to manage background operations
  So that CLI commands can execute asynchronously with proper lifecycle management

  # ============================================================================
  # Server Startup and Socket Management
  # ============================================================================

  Scenario: Daemon server starts and listens on Unix socket
    Given the daemon server is not running
    When I start the daemon server on socket "/tmp/test-daemon.sock"
    Then the daemon server should be running
    And the Unix socket should exist at "/tmp/test-daemon.sock"
    And the socket permissions should be 0600

  Scenario: Daemon server removes existing socket on startup
    Given a stale Unix socket exists at "/tmp/test-daemon.sock"
    When I start the daemon server on socket "/tmp/test-daemon.sock"
    Then the daemon server should start successfully
    And the Unix socket should exist at "/tmp/test-daemon.sock"
    And the socket should be active

  Scenario: Daemon server fails to start if socket path is invalid
    Given the daemon server is not running
    When I attempt to start the daemon server on invalid socket "/nonexistent/path/daemon.sock"
    Then the daemon startup should fail
    And the error should mention "failed to create unix socket listener"

  # ============================================================================
  # gRPC Connection Handling
  # ============================================================================

  Scenario: Daemon server accepts gRPC client connections
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    When a gRPC client connects to the Unix socket
    Then the connection should be accepted
    And the client should receive a valid gRPC server response

  Scenario: Daemon server handles HealthCheck request
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    When the client sends a HealthCheck request
    Then the response should have status "ok"
    And the response should include version "0.1.0"
    And the response should include active_containers count

  # ============================================================================
  # Request Handling and Container Creation
  # ============================================================================

  Scenario: Daemon server handles NavigateShip request
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    And a ship "TEST-SHIP-1" exists for player 1
    When the client sends a NavigateShip request for ship "TEST-SHIP-1" to "X1-TEST-DEST"
    Then the response should include a container_id
    And the response should have status "PENDING"
    And the container should be registered in the daemon

  Scenario: Daemon server creates container for navigation operation
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    When the client sends a NavigateShip request for ship "TEST-SHIP-1" to "X1-TEST-DEST"
    Then a container should be created with type "NAVIGATE"
    And the container metadata should include ship_symbol "TEST-SHIP-1"
    And the container metadata should include destination "X1-TEST-DEST"
    And the container should have player_id 1

  Scenario: Daemon server returns container ID immediately
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    When the client sends a NavigateShip request for ship "TEST-SHIP-1" to "X1-TEST-DEST"
    Then the response should return within 100 milliseconds
    And the response should include a container_id
    And the container should not be in COMPLETED status yet

  # ============================================================================
  # Background Operation Execution
  # ============================================================================

  Scenario: Daemon server continues operation in background
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    When the client sends a NavigateShip request for ship "TEST-SHIP-1" to "X1-TEST-DEST"
    And the client disconnects after receiving the container_id
    Then the container should continue executing in the background
    And the container status should eventually transition to "RUNNING"

  Scenario: Daemon server tracks multiple concurrent operations
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a gRPC client is connected
    And ships exist for player 1:
      | TEST-SHIP-1 |
      | TEST-SHIP-2 |
      | TEST-SHIP-3 |
    When the client sends NavigateShip requests for all 3 ships
    Then 3 containers should be created
    And all containers should be registered in the daemon
    And each container should have a unique container_id

  # ============================================================================
  # Graceful Shutdown Behavior
  # ============================================================================

  Scenario: Daemon server initiates graceful shutdown on SIGTERM
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And 2 containers are running in the background
    When I send SIGTERM signal to the daemon
    Then the daemon should initiate graceful shutdown
    And the daemon should stop accepting new connections
    And the daemon should wait for running containers to finish

  Scenario: Daemon server waits for containers during shutdown (within timeout)
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a container is running that will complete in 5 seconds
    When I send SIGTERM signal to the daemon
    Then the daemon should wait for the container to complete
    And the container should finish successfully
    And the daemon should shut down gracefully

  Scenario: Daemon server enforces 30-second shutdown timeout
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a container is running that will take 60 seconds to complete
    When I send SIGTERM signal to the daemon
    Then the daemon should wait up to 30 seconds
    And the daemon should force shutdown after 30 seconds
    And a warning should be logged about containers not stopping within timeout

  Scenario: Daemon server stops all containers on forceful shutdown
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And 5 containers are running in the background
    When I send SIGTERM signal to the daemon
    And the 30-second timeout expires
    Then the daemon should stop all running containers
    And all containers should transition to STOPPED status

  # ============================================================================
  # Resource Cleanup on Shutdown
  # ============================================================================

  Scenario: Daemon server closes database connections on shutdown
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And the database has 10 active connections from the daemon
    When I send SIGTERM signal to the daemon
    And the daemon completes shutdown
    Then all database connections should be closed
    And the connection pool should be released

  Scenario: Daemon server releases all ship assignments on shutdown
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And 3 ships are assigned to running containers
    When I send SIGTERM signal to the daemon
    And the daemon completes shutdown
    Then all ship assignments should be released
    And the ship assignment release_reason should be "daemon_shutdown"
    And no ships should remain locked after shutdown

  Scenario: Daemon server removes Unix socket on shutdown
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    When I send SIGTERM signal to the daemon
    And the daemon completes shutdown
    Then the Unix socket at "/tmp/test-daemon.sock" should be removed
    And the socket file should not exist

  Scenario: Daemon server prevents memory leaks during shutdown
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And 100 containers have been executed and completed
    When I send SIGTERM signal to the daemon
    And the daemon completes shutdown
    Then all goroutines spawned by containers should terminate
    And no goroutine leaks should be present
    And the daemon process should exit cleanly

  # ============================================================================
  # Shutdown Edge Cases
  # ============================================================================

  Scenario: Daemon server handles SIGINT signal (Ctrl+C)
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    When I send SIGINT signal to the daemon
    Then the daemon should initiate graceful shutdown
    And the daemon should shut down within 30 seconds

  Scenario: Daemon server handles repeated shutdown signals gracefully
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And a container is running in the background
    When I send SIGTERM signal to the daemon
    And I send another SIGTERM signal 1 second later
    Then the daemon should continue graceful shutdown
    And the daemon should not panic or exit immediately
    And the container should still be allowed to finish

  Scenario: Daemon server completes shutdown even with failing containers
    Given the daemon server is running on socket "/tmp/test-daemon.sock"
    And 2 containers are running that will fail with errors
    When I send SIGTERM signal to the daemon
    Then the daemon should wait for containers to fail
    And the daemon should complete shutdown successfully
    And the failed containers should be marked as FAILED
