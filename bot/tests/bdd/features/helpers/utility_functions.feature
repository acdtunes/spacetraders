Feature: Utility Functions
  As a bot system
  I want utility functions for calculations
  So that I can perform common operations

  Scenario: Calculate distance between waypoints
    Given two waypoints at (0, 0) and (3, 4)
    When I calculate the distance
    Then the result should be 5

  Scenario: Calculate distance to same waypoint
    Given two waypoints at (100, 100) and (100, 100)
    When I calculate the distance
    Then the result should be 0

  Scenario: Calculate horizontal distance
    Given two waypoints at (0, 50) and (100, 50)
    When I calculate the distance
    Then the result should be 100

  Scenario: Calculate vertical distance
    Given two waypoints at (50, 0) and (50, 100)
    When I calculate the distance
    Then the result should be 100

  Scenario: Calculate distance with negative coordinates
    Given two waypoints at (-50, -50) and (50, 50)
    When I calculate the distance
    Then the result should be approximately 141.42

  Scenario: Calculate arrival wait time for future arrival
    Given an arrival time 60 seconds in the future
    When I calculate the wait time
    Then the wait time should be approximately 60 seconds

  Scenario: Calculate arrival wait time for past arrival
    Given an arrival time 60 seconds in the past
    When I calculate the wait time
    Then the wait time should be 0 seconds

  Scenario: Calculate arrival wait time for current time
    Given an arrival time at current time
    When I calculate the wait time
    Then the wait time should be less than 5 seconds

  Scenario: Parse standard waypoint symbol
    Given a waypoint symbol "X1-HU87-A1"
    When I parse the waypoint symbol
    Then the system should be "X1-HU87"
    And the waypoint should be "X1-HU87-A1"

  Scenario: Parse waypoint with different sector
    Given a waypoint symbol "Z9-XYZ-M2"
    When I parse the waypoint symbol
    Then the system should be "Z9-XYZ"
    And the waypoint should be "Z9-XYZ-M2"

  Scenario: Select CRUISE mode with high fuel
    Given a ship with 400/400 fuel
    And a distance of 100 units
    And return trip is required
    When I select the flight mode
    Then the mode should be "CRUISE"

  Scenario: Select DRIFT mode with low fuel
    Given a ship with 50/400 fuel
    And a distance of 100 units
    And return trip is not required
    When I select the flight mode
    Then the mode should be "DRIFT"

  Scenario: Select DRIFT mode for long distance with limited fuel
    Given a ship with 100/400 fuel
    And a distance of 500 units
    And return trip is not required
    When I select the flight mode
    Then the mode should be "DRIFT"

  Scenario: Select DRIFT mode with critical fuel
    Given a ship with 10/400 fuel
    And a distance of 50 units
    And return trip is not required
    When I select the flight mode
    Then the mode should be "DRIFT"

  Scenario: Select CRUISE mode with zero distance
    Given a ship with 400/400 fuel
    And a distance of 0 units
    And return trip is required
    When I select the flight mode
    Then the mode should be "CRUISE"

  Scenario: Select DRIFT mode with zero fuel
    Given a ship with 0/400 fuel
    And a distance of 100 units
    And return trip is required
    When I select the flight mode
    Then the mode should be "DRIFT"

  Scenario: Timestamp returns formatted string
    When I generate a timestamp
    Then the timestamp should contain a colon
    And the timestamp should be a string

  Scenario: Timestamp contains time components
    When I generate a timestamp
    Then the timestamp should contain time components

  Scenario: Format credits with thousand separators
    When I format 1000000 credits
    Then the formatted credits should be "1,000,000"

  Scenario: Format small credits amount
    When I format 150 credits
    Then the formatted credits should be "150"

  Scenario: Generate ISO timestamp
    When I generate an ISO timestamp
    Then the ISO timestamp should contain "T"
    And the ISO timestamp should be a string

  Scenario: Map IRON_ORE to deposit type
    When I map resource "IRON_ORE" to deposit type
    Then the deposit types should include "COMMON_METAL_DEPOSITS"

  Scenario: Map GOLD_ORE to deposit type
    When I map resource "GOLD_ORE" to deposit type
    Then the deposit types should include "PRECIOUS_METAL_DEPOSITS"

  Scenario: Map unknown resource to default deposit type
    When I map resource "UNKNOWN_RESOURCE" to deposit type
    Then the deposit types should include "COMMON_METAL_DEPOSITS"

  Scenario: Calculate profit for profitable trade
    Given a buy price of 100 credits per unit
    And a sell price of 150 credits per unit
    And 20 units to trade
    And a distance of 50 units
    When I calculate the profit
    Then the gross profit should be 1000
    And the net profit should be greater than 900
    And the ROI should be greater than 40

  Scenario: Calculate profit for unprofitable trade
    Given a buy price of 150 credits per unit
    And a sell price of 100 credits per unit
    And 10 units to trade
    And a distance of 0 units
    When I calculate the profit
    Then the gross profit should be -500
    And the net profit should be -500
