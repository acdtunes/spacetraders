Feature: Refuel Ship Command
  As a ship operator
  I want to refuel my ships at stations
  So that they can continue their missions

  Background:
    Given the refuel ship command handler is initialized

  # Happy Path - Successful Refueling

  Scenario: Refuel ship to full capacity
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the ship should have 100 current fuel
    And 50 units of fuel should be added
    And the cost should be 100 credits
    And the API refuel should be called for ship "TEST-SHIP-1"

  Scenario: Refuel ship with specific amount (currently refuels to full)
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1 with 30 units
    Then the refuel should succeed
    And the ship should have 100 current fuel
    And 50 units of fuel should be added

  Scenario: Refuel ship already at full capacity
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 100 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the ship should have 100 current fuel
    And 0 units of fuel should be added

  # Error Conditions

  Scenario: Refuel non-existent ship
    When I execute refuel command for ship "NONEXISTENT" and player 1
    Then the command should fail with ShipNotFoundError
    And the error message should contain "NONEXISTENT"
    And the error message should contain "player 1"

  Scenario: Refuel ship in orbit auto-docks
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is in orbit at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the ship should be docked
    And fuel should be added

  Scenario: Refuel ship in transit fails
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is in transit
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the command should fail with InvalidNavStatusError

  Scenario: Refuel at location without fuel fails
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint does not have fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the command should fail with ValueError
    And the error message should contain "fuel"

  Scenario: Refuel ship belonging to different player fails
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 2
    Then the command should fail with ShipNotFoundError

  # Persistence

  Scenario: Refuel persists ship state to repository
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the ship in the repository should have 100 current fuel

  # Response Structure

  Scenario: Refuel response contains cost information
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    And the API will return refuel cost of 250 credits
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the cost should be 250 credits

  Scenario: Refuel response when API does not return cost
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    And the API will not return cost information
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the cost should be None

  # Edge Cases

  Scenario: Refuel preserves other ship properties
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 50 current fuel and 100 capacity
    And the ship has cargo capacity 40 and cargo units 0
    And the ship has engine speed 30
    When I execute refuel command for ship "TEST-SHIP-1" and player 1
    Then the refuel should succeed
    And the ship symbol should be "TEST-SHIP-1"
    And the ship player id should be 1
    And the ship should be docked
    And the ship cargo capacity should be 40
    And the ship cargo units should be 0
    And the ship engine speed should be 30

  Scenario: Refuel with specific units caps at capacity
    Given a ship "TEST-SHIP-1" owned by player 1
    And the ship is docked at waypoint "X1-TEST-AB12"
    And the waypoint has fuel available
    And the ship has 90 current fuel and 100 capacity
    When I execute refuel command for ship "TEST-SHIP-1" and player 1 with 50 units
    Then the refuel should succeed
    And the ship should have 100 current fuel
