Feature: Ship Balancer
  As a contract coordinator
  I want to intelligently balance ship positions across markets
  So that idle haulers are evenly distributed for efficient contract assignment

  Background:
    Given the following markets exist:
      | symbol      | x    | y    |
      | MARKET-A    | 0    | 0    |
      | MARKET-B    | 100  | 0    |
      | MARKET-C    | 0    | 100  |
      | MARKET-D    | 100  | 100  |

  # Global Assignment Tracking Algorithm Tests
  # Score = (assigned_ships × 100) + (distance × 0.1)
  # Lower score is better
  # Goal: Even distribution (1 ship per market ideal, then 2 per market, etc.)

  Scenario: Select empty market over occupied market
    Given ship "HAULER-1" is at location (80, 90)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
      | HAULER-3    | 0    | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A has 2 ships (score = 200 + distance)
    # MARKET-B/C/D have 0 ships (score = 0 + distance)
    # MARKET-D is closest to HAULER-1 at (80,90): distance = 22.4
    Then the selected market should be "MARKET-D"
    And there should be 0 assigned ships at the target market

  Scenario: Distance tiebreaker when all markets are empty
    Given ship "HAULER-1" is at location (25, 0)
    And no other idle haulers exist
    When I calculate the optimal balancing position for "HAULER-1"
    # All markets have 0 ships, so picks nearest
    # MARKET-A is at 25 units, MARKET-B at 75 units
    Then the selected market should be "MARKET-A"
    And the distance to target should be 25.0

  Scenario: Avoid market with existing ships
    Given ship "HAULER-1" is at location (50, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 100  | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-B (100, 0) has 1 ship: score = 100 + 50 = 150
    # MARKET-A (0, 0) has 0 ships: score = 0 + 50 = 5
    # Even though both are equidistant, MARKET-A wins (no ships)
    Then the selected market should be "MARKET-A"
    And there should be 0 assigned ships at the target market

  Scenario: Even distribution across multiple markets
    Given ship "HAULER-1" is at location (50, 50)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
      | HAULER-3    | 100  | 100  |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A has 1 ship: score = 100 + 70.7 ≈ 170.7
    # MARKET-B has 0 ships: score = 0 + 70.7 ≈ 7.07
    # MARKET-C has 0 ships: score = 0 + 70.7 ≈ 7.07
    # MARKET-D has 1 ship: score = 100 + 70.7 ≈ 170.7
    # Picks B or C (both empty and equidistant)
    Then the selected market should not be "MARKET-A"
    And the selected market should not be "MARKET-D"

  Scenario: Heavy penalty for second ship to same market
    Given ship "HAULER-1" is at location (10, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A (0, 0) has 1 ship: score = 100 + 10 = 110
    # MARKET-B (100, 0) has 0 ships: score = 0 + 90 = 9
    # Even though MARKET-B is 9x farther, it wins (penalty for MARKET-A)
    Then the selected market should be "MARKET-B"

  Scenario: Select nearest empty market when multiple options exist
    Given ship "HAULER-1" is at location (110, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A (0, 0) has 1 ship: score = 100 + 110 = 210
    # MARKET-B (100, 0) has 0 ships: score = 0 + 10 = 1
    # MARKET-C (0, 100) has 0 ships: score = 0 + 141.4 = 14.14
    # MARKET-D (100, 100) has 0 ships: score = 0 + 100 = 10
    Then the selected market should be "MARKET-B"
    And the distance to target should be 10.0

  Scenario: All markets occupied - select least crowded
    Given ship "HAULER-1" is at location (50, 50)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
      | HAULER-3    | 0    | 0    |
      | HAULER-4    | 100  | 0    |
      | HAULER-5    | 0    | 100  |
      | HAULER-6    | 0    | 100  |
      | HAULER-7    | 0    | 100  |
      | HAULER-8    | 100  | 100  |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A has 2 ships: score = 200 + 70.7 ≈ 270.7
    # MARKET-B has 1 ship: score = 100 + 70.7 ≈ 170.7
    # MARKET-C has 3 ships: score = 300 + 70.7 ≈ 370.7
    # MARKET-D has 1 ship: score = 100 + 70.7 ≈ 170.7
    # Picks B or D (both have 1 ship, equidistant)
    Then the distance to target should be less than 100.0

  # TODO: Fix error scenario test setup
  # Scenario: Error when no markets available
  #   Given ship "HAULER-1" is at location (0, 0)
  #   And no markets exist
  #   When I attempt to calculate the optimal balancing position for "HAULER-1"
  #   Then the operation should fail with error "no markets available for balancing"

  # TODO: Fix error scenario test setup
  # Scenario: Error when ship is nil
  #   Given no ship exists
  #   And markets exist
  #   When I attempt to calculate the optimal balancing position for a nil ship
  #   Then the operation should fail with error "ship cannot be nil"
