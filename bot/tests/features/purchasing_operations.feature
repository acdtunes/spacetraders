Feature: Ship purchasing operation
  Scenario: Purchase succeeds within budget and quantity
    Given a purchasing context with ship type "HEAVY_FREIGHTER"
    And the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    When the purchase operation runs for 2 ships with budget 3000
    Then the purchase should complete with 2 new ships

  Scenario: Insufficient funds prevent the purchase
    Given a purchasing context with ship type "HEAVY_FREIGHTER"
    And the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 2000 credits with 1000 credits available
    When the purchase operation runs for 1 ships with budget 1500
    Then the purchase should fail with an error log

  Scenario: Navigation failure aborts the purchase
    Given a purchasing context with ship type "HEAVY_FREIGHTER"
    And the purchasing ship is away from "X1-TEST-B1" and cannot navigate
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with an error log

  Scenario: API purchase failure logs an error
    Given a purchasing context with ship type "HEAVY_FREIGHTER"
    And the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard purchase call returns no data
    When the purchase operation runs for 1 ships with budget 5000
    Then the purchase should fail with an error log

  Scenario: Validation rejects non-integer quantity
    Given a purchasing context with invalid quantity "not-an-int"
    When the purchase operation validates arguments
    Then validation should fail

  Scenario: Validation rejects non-numeric budget
    Given a purchasing context with invalid budget "not-a-number"
    When the purchase operation validates arguments
    Then validation should fail

  Scenario: Missing required argument triggers validation error
    Given a purchasing context missing the argument "player_id"
    When the purchase operation validates arguments with missing data
    Then validation should fail for missing argument

  Scenario: Zero quantity fails the purchase operation
    Given a purchasing context with ship type "HEAVY_FREIGHTER"
    And the purchasing ship is already docked at "X1-TEST-B1"
    And the shipyard lists the ship for 1000 credits with 5000 credits available
    When the purchase operation runs for 0 ships with budget 5000
    Then the purchase should fail with message "Quantity must be greater than zero"
    And no purchase requests should be sent
