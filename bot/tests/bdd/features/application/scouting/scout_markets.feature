Feature: Scout Markets Command
  Scout markets with VRP-optimized fleet distribution

  Background:
    Given a player with ID 1
    And ships in system "X1-TEST":
      | ship_symbol   | waypoint    | status  |
      | TEST-SCOUT-1  | X1-TEST-A1  | DOCKED  |
      | TEST-SCOUT-2  | X1-TEST-B2  | DOCKED  |

  Scenario: Scout markets partitions across 2 ships using VRP
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
      | X1-TEST-C3 |
    Then scout markets should complete successfully
    And 2 ship assignments should be returned
    And each market should be assigned to exactly one ship

  Scenario: Scout markets with single ship skips VRP optimization
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
    Then scout markets should complete successfully
    And 1 ship assignments should be returned

  Scenario: Scout markets partitions 12 markets evenly across 4 ships
    Given ships in system "X1-GZ7":
      | ship_symbol       | waypoint    | status  |
      | CHROMESAMURAI-2   | X1-GZ7-A1   | DOCKED  |
      | CHROMESAMURAI-3   | X1-GZ7-A2   | DOCKED  |
      | CHROMESAMURAI-5   | X1-GZ7-C47  | DOCKED  |
      | CHROMESAMURAI-6   | X1-GZ7-E53  | DOCKED  |
    When I execute scout markets for system "X1-GZ7" with ships:
      | CHROMESAMURAI-2 |
      | CHROMESAMURAI-3 |
      | CHROMESAMURAI-5 |
      | CHROMESAMURAI-6 |
    And markets:
      | X1-GZ7-A1  |
      | X1-GZ7-A2  |
      | X1-GZ7-A3  |
      | X1-GZ7-A4  |
      | X1-GZ7-B6  |
      | X1-GZ7-B7  |
      | X1-GZ7-C47 |
      | X1-GZ7-C48 |
      | X1-GZ7-D49 |
      | X1-GZ7-D50 |
      | X1-GZ7-E53 |
      | X1-GZ7-E54 |
    Then scout markets should complete successfully
    And 4 ship assignments should be returned
    And each market should be assigned to exactly one ship
    And each ship should have at least one market assigned
