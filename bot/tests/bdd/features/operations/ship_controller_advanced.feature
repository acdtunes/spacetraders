Feature: Ship Controller Advanced Operations
  As a bot operator
  I want comprehensive error handling and edge case coverage
  So that my ship operations are robust and reliable

  Background:
    Given the SpaceTraders API is mocked
    And a waypoint "X1-TEST-A1" exists at (0, 0) with traits ["MARKETPLACE"]
    And a waypoint "X1-TEST-B2" exists at (100, 0) with traits ["ASTEROID_BASE"]
    And a waypoint "X1-TEST-C3" exists at (300, 0) with traits ["COMMON_METAL_DEPOSITS"]

  # ============================================================================
  # Error Handling - API Failures
  # ============================================================================

  Scenario: Get status returns None - location check
    Given a ship "ERROR-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the API will fail on next get_ship call
    When I get the ship location
    Then the result should be None

  Scenario: Get status returns None - refuel fails early
    Given a ship "ERROR-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 100/400 fuel
    And the API will fail on next get_ship call
    When I refuel the ship
    Then refuel should return False

  Scenario: Get status returns None - navigation fails early
    Given a ship "ERROR-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the API will fail on next get_ship call
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should return False

  # ============================================================================
  # Failed Operations - API Errors
  # ============================================================================

  Scenario: Dock operation fails due to API error
    Given a ship "FAIL-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the API will fail on next dock call
    When I dock the ship
    Then dock should return False

  Scenario: Orbit operation fails due to API error
    Given a ship "FAIL-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    And the API will fail on next orbit call
    When I orbit the ship
    Then orbit should return False

  Scenario: Refuel operation fails due to API error
    Given a ship "FAIL-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 100/400 fuel
    And the API will fail on next refuel call
    When I refuel the ship
    Then refuel should return False

  # ============================================================================
  # Auto-Dock for Refueling
  # ============================================================================

  Scenario: Refuel auto-docks when ship is in orbit
    Given a ship "AUTO-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 100/400 fuel
    And the agent has 10000 credits
    When I refuel the ship
    Then the ship should be DOCKED
    And the ship should have 400/400 fuel

  Scenario: Refuel auto-dock fails prevents refueling
    Given a ship "AUTO-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 100/400 fuel
    And the API will fail on next dock call
    When I refuel the ship
    Then refuel should return False

  # ============================================================================
  # IN_TRANSIT State Handling
  # ============================================================================

  Scenario: Navigation waits for arrival when already in transit to destination
    Given a ship "TRANSIT-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship is in transit to "X1-TEST-B2" arriving in 1 seconds
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should return True
    And the ship should be at "X1-TEST-B2"

  Scenario: Navigation waits for different destination then navigates
    Given a ship "TRANSIT-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship is in transit to "X1-TEST-B2" arriving in 1 seconds
    When I navigate the ship to "X1-TEST-C3"
    Then navigation should return True
    And the ship should be at "X1-TEST-C3"

  Scenario: Navigate to current location returns immediately
    Given a ship "STAY-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    When I navigate the ship to "X1-TEST-A1"
    Then navigation should return True
    And the ship should be at "X1-TEST-A1"

  # ============================================================================
  # Auto-Refuel and Orbit Before Navigation
  # ============================================================================

  Scenario: Navigate with auto-refuel when fuel low
    Given a ship "LOW-FUEL" at "X1-TEST-A1" is DOCKED
    And the ship has 50/400 fuel
    And the agent has 10000 credits
    When I navigate the ship to "X1-TEST-B2" with auto-refuel
    Then the ship should be at "X1-TEST-B2"
    And navigation should return True

  Scenario: Navigate auto-refuel fails prevents navigation
    Given a ship "LOW-FUEL" at "X1-TEST-A1" is DOCKED
    And the ship has 50/400 fuel
    And the API will fail on next refuel call
    When I navigate the ship to "X1-TEST-B2" with auto-refuel
    Then navigation should return False

  Scenario: Navigate auto-orbits when ship is docked
    Given a ship "DOCKED-NAV" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    When I navigate the ship to "X1-TEST-B2"
    Then the ship should be at "X1-TEST-B2"
    And navigation should return True

  Scenario: Navigate auto-orbit fails prevents navigation
    Given a ship "DOCKED-NAV" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    And the API will fail on next orbit call
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should return False

  Scenario: Flight mode setting fails prevents navigation
    Given a ship "MODE-FAIL" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the API will fail on next patch call
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should return False

  # ============================================================================
  # Wait Operations with Edge Cases
  # ============================================================================

  Scenario: Wait for arrival with zero seconds does nothing
    Given a ship "INSTANT-SHIP" at "X1-TEST-A1" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship is in transit to "X1-TEST-B2" arriving in 0 seconds
    When I navigate the ship to "X1-TEST-B2"
    Then navigation should return True
    And the ship should be at "X1-TEST-B2"

  Scenario: Wait for cooldown after extraction
    Given a ship "MINER-WAIT" at "X1-TEST-C3" is IN_ORBIT
    And the ship has 400/400 fuel
    And the ship has 0/40 cargo units
    When I extract resources
    Then the extraction should succeed
    When I wait for the extraction cooldown
    Then the cooldown should be complete

  Scenario: Wait for cooldown with zero seconds does nothing
    Given a ship "NO-COOLDOWN" at "X1-TEST-C3" is IN_ORBIT
    And the ship has 400/400 fuel
    When I wait for cooldown of 0 seconds
    Then the wait should complete immediately

  # ============================================================================
  # Sell All Operations
  # ============================================================================

  Scenario: Sell all cargo with multiple items
    Given a ship "TRADER-SHIP" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 15}, {"symbol": "COPPER_ORE", "units": 10}, {"symbol": "ICE_WATER", "units": 5}]
    And the agent has 50000 credits
    When I sell all cargo
    Then all cargo should be sold
    And the ship cargo should have 0 units
    And the total revenue should be greater than 0

  Scenario: Sell all when cargo is empty
    Given a ship "EMPTY-TRADER" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: []
    When I sell all cargo
    Then the revenue should be 0
    And the ship cargo should have 0 units

  Scenario: Sell all with API error on one item continues
    Given a ship "PARTIAL-FAIL" at "X1-TEST-A1" is DOCKED
    And the ship has 400/400 fuel
    And the ship has cargo: [{"symbol": "IRON_ORE", "units": 10}, {"symbol": "COPPER_ORE", "units": 10}]
    And the agent has 50000 credits
    And the API will fail on first sell call
    When I sell all cargo
    Then some cargo should remain
    And the revenue should be greater than 0
