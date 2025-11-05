Feature: Daemon Server Lifecycle
  As an autonomous bot
  I need a daemon server to run containers in the background
  So that operations can execute asynchronously

  Scenario: Start daemon server successfully
    Given the daemon server is not running
    When I start the daemon server
    Then the daemon server should be running
    And the Unix socket should exist at "var/daemon.sock"

  Scenario: Daemon server accepts client connections
    Given the daemon server is running
    When a client connects to the Unix socket
    Then the connection should be accepted
    And the client can send JSON-RPC requests

  Scenario: Stop daemon server gracefully
    Given the daemon server is running
    When I send a stop signal to the daemon
    Then the daemon should stop gracefully
    And the Unix socket should be cleaned up
