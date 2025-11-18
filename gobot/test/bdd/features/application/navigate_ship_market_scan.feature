Feature: Navigate Ship Market Scanning
  As a SpaceTraders bot
  I want ships to automatically scan marketplaces during navigation
  So that market data is collected without manual intervention

  # ============================================================================
  # Automatic Market Scanning
  # ============================================================================

  @market_scan
  Scenario: Scan market at destination when ship arrives at marketplace
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-TEST" has a simple graph with 2 waypoints
    And waypoint "X1-TEST-A1" has no traits
    And waypoint "X1-TEST-B1" has trait "MARKETPLACE"
    And ship "SCOUT-1" is at "X1-TEST-A1" with 100 fuel
    And market scanner is enabled for route executor
    And the API will return market data for "X1-TEST-B1" with 3 trade goods
    When I navigate "SCOUT-1" to "X1-TEST-B1"
    Then navigation should succeed
    And market data should be saved for waypoint "X1-TEST-B1"
    And market data should have 3 trade goods for "X1-TEST-B1"

  @market_scan
  Scenario: Scan market at intermediate refueling stop that is a marketplace
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-TEST" has a graph with 3 waypoints in a line
    And waypoint "X1-TEST-A1" has no traits
    And waypoint "X1-TEST-B1" has traits "MARKETPLACE" and "FUEL_STATION"
    And waypoint "X1-TEST-C1" has no traits
    And ship "SCOUT-1" is at "X1-TEST-A1" with 50 fuel
    And market scanner is enabled for route executor
    And the API will return market data for "X1-TEST-B1" with 2 trade goods
    And routing service plans route with refuel stop at "X1-TEST-B1"
    When I navigate "SCOUT-1" to "X1-TEST-C1"
    Then navigation should succeed
    And market data should be saved for waypoint "X1-TEST-B1"
    And market data should have 2 trade goods for "X1-TEST-B1"

  @market_scan
  Scenario: Do not scan market at non-marketplace waypoints
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-TEST" has a simple graph with 2 waypoints
    And waypoint "X1-TEST-A1" has no traits
    And waypoint "X1-TEST-B1" has trait "FUEL_STATION"
    And ship "SCOUT-1" is at "X1-TEST-A1" with 100 fuel
    And market scanner is enabled for route executor
    When I navigate "SCOUT-1" to "X1-TEST-B1"
    Then navigation should succeed
    And no market data should be saved

  @market_scan @error_handling
  Scenario: Navigation succeeds even if market scan fails
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-TEST" has a simple graph with 2 waypoints
    And waypoint "X1-TEST-A1" has no traits
    And waypoint "X1-TEST-B1" has trait "MARKETPLACE"
    And ship "SCOUT-1" is at "X1-TEST-A1" with 100 fuel
    And market scanner is enabled for route executor
    And the market scan API will fail with error "market temporarily unavailable"
    When I navigate "SCOUT-1" to "X1-TEST-B1"
    Then navigation should succeed
    And ship should be at "X1-TEST-B1"
    And no market data should be saved

  @market_scan
  Scenario: Market scanner disabled - no scanning occurs
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-TEST" has a simple graph with 2 waypoints
    And waypoint "X1-TEST-A1" has no traits
    And waypoint "X1-TEST-B1" has trait "MARKETPLACE"
    And ship "SCOUT-1" is at "X1-TEST-A1" with 100 fuel
    And market scanner is disabled for route executor
    When I navigate "SCOUT-1" to "X1-TEST-B1"
    Then navigation should succeed
    And no market data should be saved
