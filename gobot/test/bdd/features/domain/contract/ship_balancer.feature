Feature: Ship Balancer
  As a contract coordinator
  I want to intelligently balance ship positions across markets
  So that idle haulers are optimally distributed for efficient contract assignment

  Background:
    Given the following markets exist:
      | symbol      | x    | y    |
      | MARKET-A    | 0    | 0    |
      | MARKET-B    | 100  | 0    |
      | MARKET-C    | 0    | 100  |
      | MARKET-D    | 100  | 100  |

  # Distance + Coverage Score Algorithm Tests
  # Score = (nearby_haulers × 10) + (distance × 0.1)
  # Lower score is better

  Scenario: Select market with no nearby haulers (highest priority)
    Given ship "HAULER-1" is at location (50, 50)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 10   | 0    |
      | HAULER-3    | 10   | 5    |
      | HAULER-4    | 90   | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    Then the selected market should be "MARKET-D"
    And the balancing score should be approximately 7.1
    And there should be 0 nearby haulers at the target market

  Scenario: Prefer market with fewer nearby haulers over closer market
    Given ship "HAULER-1" is at location (0, 50)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 5    | 45   |
      | HAULER-3    | 5    | 55   |
      | HAULER-4    | 10   | 50   |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A is closest (50 units) but has 3 nearby haulers (score = 35)
    # MARKET-C has 3 nearby haulers and is same distance (score = 35)
    # MARKET-B and MARKET-D have 0 nearby haulers
    # MARKET-B is closer (111.8 units, score = 11.18)
    # MARKET-D is farther (141.4 units, score = 14.14)
    Then the selected market should be "MARKET-B"
    And there should be 0 nearby haulers at the target market

  Scenario: Select nearest market when coverage is equal
    Given ship "HAULER-1" is at location (25, 0)
    And no other idle haulers exist
    When I calculate the optimal balancing position for "HAULER-1"
    Then the selected market should be "MARKET-A"
    And the distance to target should be 25.0

  Scenario: Distance tiebreaker with equal coverage
    Given ship "HAULER-1" is at location (25, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 200  | 0    |
      | HAULER-3    | 0    | 200  |
      | HAULER-4    | 100  | 200  |
    When I calculate the optimal balancing position for "HAULER-1"
    # All markets have 0 nearby haulers (all haulers >100 units away)
    # Ship equidistant to MARKET-A and MARKET-B, picks first evaluated
    Then the selected market should be "MARKET-A"
    And the distance to target should be 25.0

  Scenario: Heavy coverage penalty outweighs distance savings
    Given ship "HAULER-1" is at location (0, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 5    | 5    |
      | HAULER-3    | 10   | 10   |
      | HAULER-4    | 15   | 15   |
    When I calculate the optimal balancing position for "HAULER-1"
    # MARKET-A is closest but has 3 nearby haulers (score = 30)
    # MARKET-B/C/D have 0 nearby haulers (scores ~10-14)
    Then the selected market should not be "MARKET-A"

  Scenario: Multiple haulers within proximity radius
    Given ship "HAULER-1" is at location (0, 0)
    And the following idle haulers exist within 100 units of MARKET-A:
      | symbol      | x    | y    |
      | HAULER-2    | 20   | 0    |
      | HAULER-3    | 40   | 0    |
      | HAULER-4    | 60   | 0    |
      | HAULER-5    | 80   | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    Then the selected market should not be "MARKET-A"
    And MARKET-A should have 4 nearby haulers

  Scenario: Ship at exact market location
    Given ship "HAULER-1" is at location (0, 0)
    And no other idle haulers exist
    When I calculate the optimal balancing position for "HAULER-1"
    Then the selected market should be "MARKET-A"
    And the distance to target should be 0.0

  Scenario: All markets have equal coverage
    Given ship "HAULER-1" is at location (50, 50)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 0    | 0    |
      | HAULER-3    | 100  | 0    |
      | HAULER-4    | 0    | 100  |
      | HAULER-5    | 100  | 100  |
    When I calculate the optimal balancing position for "HAULER-1"
    # All markets have 1 nearby hauler, so picks closest
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

  Scenario: Proximity radius boundary (100 units)
    Given ship "HAULER-1" is at location (0, 0)
    And the following idle haulers exist:
      | symbol      | x    | y    |
      | HAULER-2    | 99   | 0    |
      | HAULER-3    | 101  | 0    |
    When I calculate the optimal balancing position for "HAULER-1"
    And I check haulers within 100 units of MARKET-A
    # HAULER-2 is within radius (99 < 100), HAULER-3 is outside (101 > 100)
    Then there should be 1 nearby hauler at "MARKET-A"
