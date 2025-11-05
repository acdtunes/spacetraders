Feature: Get System Graph Query
  As a navigation system
  I want to retrieve system graphs from cache or API
  So that I can perform pathfinding and route planning

  Background:
    Given the get system graph query handler is initialized

  # Query Creation Tests
  Scenario: Create query with default force_refresh
    When I create a query for system "X1"
    Then the query should have system symbol "X1"
    And the query should have force_refresh false

  Scenario: Create query with force_refresh enabled
    When I create a query for system "X1" with force_refresh true
    Then the query should have system symbol "X1"
    And the query should have force_refresh true

  Scenario: Query is immutable
    Given a query for system "X1"
    When I attempt to modify the query system symbol to "Y2"
    Then the modification should fail with AttributeError

  Scenario: Create queries for different systems
    When I create a query for system "X1"
    And I create a query for system "Y2"
    Then the first query should have system symbol "X1"
    And the second query should have system symbol "Y2"

  # Successful Graph Retrieval
  Scenario: Retrieve graph from database
    Given a system "X1" with graph data in database
    When I execute get system graph query for "X1"
    Then the result should be a GraphLoadResult
    And the graph should contain waypoints data
    And the result source should be "database"
    And the result message should be "Loaded from cache"

  Scenario: Retrieve graph from API with force refresh
    Given a system "X1" with graph data available
    When I execute get system graph query for "X1" with force_refresh true
    Then the result should be a GraphLoadResult
    And the result source should be "api"
    And the result message should be "Fetched from API"

  # Graph Data Structure
  Scenario: Graph data structure is preserved
    Given a system "X1" with 2 waypoints and 1 edge
    When I execute get system graph query for "X1"
    Then the graph should have key "waypoints"
    And the graph should have key "edges"
    And the graph should have 2 waypoints
    And the graph should have 1 edge
    And the waypoint "X1-A1" should exist
    And the waypoint "X1-B2" should exist

  Scenario: Handle empty graph
    Given a system "EMPTY-SYSTEM" with no waypoints
    When I execute get system graph query for "EMPTY-SYSTEM"
    Then the graph waypoints should be empty
    And the graph edges should be empty
    And the result message should be "No waypoints in system"

  Scenario: Retrieve graphs for different systems
    Given a system "X1" with waypoint "X1-A1"
    And a system "Y2" with waypoint "Y2-Z9"
    When I execute get system graph query for "X1"
    And I execute get system graph query for "Y2"
    Then the first result should contain waypoint "X1-A1"
    And the second result should contain waypoint "Y2-Z9"

  # Error Handling
  Scenario: Graph provider exception is propagated
    Given the graph provider will raise RuntimeError "API connection failed"
    When I attempt to execute get system graph query for "X1"
    Then the command should fail with RuntimeError
    And the error message should contain "API connection failed"

  # Parameter Passing
  Scenario: Force refresh parameter fetches from API
    Given a system "X1" with graph data available
    When I execute get system graph query for "X1" with force_refresh true
    Then the result source should be "api"

  Scenario: Without force refresh uses cache
    Given a system "X1" with graph data in database
    When I execute get system graph query for "X1"
    Then the result source should be "database"

  # Large Graph Handling
  Scenario: Handle large graph with many waypoints
    Given a system "X1" with 100 waypoints
    When I execute get system graph query for "X1"
    Then the graph should have 100 waypoints
    And the waypoint "X1-WP0" should exist
    And the waypoint "X1-WP99" should exist

  Scenario: Handle graph with complex edges
    Given a system "X1" with 3 waypoints and 3 edges
    When I execute get system graph query for "X1"
    Then the graph should have 3 edges
    And all edges should have type "TRAVEL"

  # Read-Only Behavior
  Scenario: Handler does not modify graph data
    Given a system "X1" with graph data in database
    When I execute get system graph query for "X1"
    Then the graph data should match the original
    And the graph provider should only be called for read operations

  Scenario: Result message is optional
    Given a system "X1" with graph data but no message
    When I execute get system graph query for "X1"
    Then the result message should be None

  Scenario: Consecutive queries for same system return consistent data
    Given a system "X1" with graph data in database
    When I execute get system graph query for "X1"
    And I execute get system graph query for "X1"
    Then both results should have the same graph data

  Scenario: Waypoint with all properties preserved
    Given a system "X1" with detailed waypoint "X1-DETAIL"
    When I execute get system graph query for "X1"
    Then the waypoint "X1-DETAIL" should have symbol "X1-DETAIL"
    And the waypoint "X1-DETAIL" should have x coordinate 123.45
    And the waypoint "X1-DETAIL" should have y coordinate 678.90
    And the waypoint "X1-DETAIL" should have type "ORBITAL_STATION"
    And the waypoint "X1-DETAIL" should have traits ["MARKETPLACE", "SHIPYARD", "REFUEL"]
    And the waypoint "X1-DETAIL" should have has_fuel true
    And the waypoint "X1-DETAIL" should have orbitals ["X1-PLANET-A"]
