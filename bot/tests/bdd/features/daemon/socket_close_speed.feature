Feature: Fast Socket Close in Daemon RPC
  As a SpaceTraders bot operator
  I need daemon RPC socket connections to close immediately after sending response
  So that MCP tools don't experience 60+ second delays

  Scenario: Socket handler closes connection in under 100ms
    Given a mock StreamWriter and StreamReader are created
    When the daemon handles a successful request
    Then the connection handler should complete in under 100ms
    And writer.close() should be called
    And writer.wait_closed() should NOT be called

  Scenario: Socket handler closes on error in under 100ms
    Given a mock StreamWriter and StreamReader are created
    When the daemon handles a request that raises an error
    Then the connection handler should complete in under 100ms
    And writer.close() should be called
    And writer.wait_closed() should NOT be called
