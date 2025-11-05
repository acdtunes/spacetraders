Feature: Plan Route Query
  As a ship operator
  I want to plan optimal routes to destinations
  So that I can navigate efficiently with proper fuel and time management

  Background:
    Given the plan route query handler is initialized

  # Query Creation Tests
  Scenario: Create query with default prefer_cruise setting
    When I create a plan route query for ship "SHIP-1" to "X1-DEST" for player 1
    Then the query ship symbol should be "SHIP-1"
    And the query destination should be "X1-DEST"
    And the query player ID should be 1
    And the query prefer_cruise should be True

  Scenario: Create query with custom prefer_cruise setting
    When I create a plan route query for ship "SHIP-1" to "X1-DEST" for player 1 with prefer_cruise False
    Then the query prefer_cruise should be False

  Scenario: Query is immutable
    Given I create a plan route query for ship "SHIP-1" to "X1-DEST" for player 1
    When I attempt to modify the query ship symbol
    Then the modification should fail with AttributeError

  # Happy Path - Route Planning
  Scenario: Successfully plan route with valid ship and destination
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns a valid path from "X1-START" to "X1-DEST"
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then a route should be returned
    And the route ship symbol should be "SHIP-1"
    And the route player ID should be 1
    And the route should have 1 segment
    And the ship repository should have been queried for "SHIP-1"
    And the graph provider should have been queried for system "X1"

  # Error Conditions
  Scenario: Plan route for non-existent ship
    Given no ship "NONEXISTENT" exists for player 1
    When I attempt to plan a route for ship "NONEXISTENT" to "X1-DEST" as player 1
    Then the query should fail with ShipNotFoundError
    And the error message should contain "NONEXISTENT"
    And the error message should contain "player 1"

  Scenario: Plan route to destination in different system
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    When I attempt to plan a route for ship "SHIP-1" to "Y2-DEST" as player 1
    Then the query should fail with ValueError
    And the error message should contain "must be in same system"

  Scenario: No path found to destination
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns no path from "X1-START" to "X1-DEST"
    When I attempt to plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the query should fail with ValueError
    And the error message should contain "No valid path found"

  # Routing Engine Integration
  Scenario: Routing engine called with correct parameters
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns a valid path from "X1-START" to "X1-DEST"
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1 with prefer_cruise False
    Then the routing engine should have been called with start "X1-START"
    And the routing engine should have been called with goal "X1-DEST"
    And the routing engine should have been called with current_fuel 100
    And the routing engine should have been called with fuel_capacity 200
    And the routing engine should have been called with engine_speed 30
    And the routing engine should have been called with prefer_cruise False

  # Graph Conversion
  Scenario: Graph data is converted to Waypoint objects
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns a valid path from "X1-START" to "X1-DEST"
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the routing engine should have received a graph with waypoint "X1-START"
    And the routing engine should have received a graph with waypoint "X1-DEST"
    And the graph waypoint "X1-START" should have symbol "X1-START"
    And the graph waypoint "X1-DEST" should have symbol "X1-DEST"

  # Route Segments Creation
  Scenario: Route segments are created correctly from path steps
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns a path with distance 141.42 and fuel cost 50 and time 100
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the route should have 1 segment
    And segment 1 should have from_waypoint "X1-START"
    And segment 1 should have to_waypoint "X1-DEST"
    And segment 1 should have distance 141.42
    And segment 1 should have fuel_required 50
    And segment 1 should have travel_time 100
    And segment 1 should have flight_mode CRUISE

  # Multi-Segment Routes
  Scenario: Route with refuel stop creates multiple segments
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And waypoint "X1-REFUEL" exists in system "X1" at position (50.0, 50.0) with fuel available
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And the routing engine returns a path via "X1-REFUEL" with refuel action
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the route should have 2 segments
    And segment 1 should have to_waypoint "X1-REFUEL"
    And segment 1 should require refuel
    And segment 2 should have to_waypoint "X1-DEST"
    And segment 2 should not require refuel

  Scenario: Route with multiple waypoints but no refuel
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And waypoint "X1-MID" exists in system "X1" at position (50.0, 50.0)
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And the routing engine returns a path via "X1-MID" without refuel
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the route should have 2 segments
    And segment 1 should have from_waypoint "X1-START"
    And segment 1 should have to_waypoint "X1-MID"
    And segment 1 should not require refuel
    And segment 2 should have from_waypoint "X1-MID"
    And segment 2 should have to_waypoint "X1-DEST"
    And segment 2 should not require refuel

  # Route ID Generation
  Scenario: Route ID is generated correctly
    Given a ship "SHIP-1" exists at waypoint "X1-START" in system "X1"
    And the ship has 100 fuel with capacity 200 and engine speed 30
    And waypoint "X1-DEST" exists in system "X1" at position (100.0, 100.0)
    And waypoint "X1-START" exists in system "X1" at position (0.0, 0.0)
    And the routing engine returns a valid path from "X1-START" to "X1-DEST"
    When I plan a route for ship "SHIP-1" to "X1-DEST" as player 1
    Then the route ID should be "ROUTE-SHIP-1-X1-DEST"
