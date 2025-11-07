Feature: Scout Markets Idempotency
  Scout markets should be idempotent and reuse existing containers to prevent duplicates

  Background:
    Given a player with ID 1
    And ships in system "X1-TEST":
      | ship_symbol   | waypoint    | status  |
      | TEST-SCOUT-1  | X1-TEST-A1  | DOCKED  |
      | TEST-SCOUT-2  | X1-TEST-B2  | DOCKED  |
      | TEST-SCOUT-3  | X1-TEST-C3  | DOCKED  |

  Scenario: Scout markets reuses existing STARTING containers
    Given existing scout containers:
      | container_id                 | ship_symbol   | status    |
      | scout-tour-test-scout-1-abc1 | TEST-SCOUT-1  | STARTING  |
      | scout-tour-test-scout-2-abc2 | TEST-SCOUT-2  | STARTING  |
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
    Then scout markets should complete successfully
    And 2 container IDs should be returned
    And no new containers should be created
    And container "scout-tour-test-scout-1-abc1" should be reused
    And container "scout-tour-test-scout-2-abc2" should be reused

  Scenario: Scout markets reuses existing RUNNING containers
    Given existing scout containers:
      | container_id                 | ship_symbol   | status   |
      | scout-tour-test-scout-1-def1 | TEST-SCOUT-1  | RUNNING  |
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
    And markets:
      | X1-TEST-A1 |
    Then scout markets should complete successfully
    And 1 container IDs should be returned
    And no new containers should be created
    And container "scout-tour-test-scout-1-def1" should be reused

  Scenario: Scout markets creates new containers when ships have no active containers
    Given existing scout containers:
      | container_id                 | ship_symbol   | status   |
      | scout-tour-test-scout-1-old1 | TEST-SCOUT-1  | STOPPED  |
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
    Then scout markets should complete successfully
    And 2 container IDs should be returned
    And 2 new containers should be created
    And container "scout-tour-test-scout-1-old1" should not be reused

  Scenario: Scout markets handles mixed scenario - some ships with active containers, some without
    Given existing scout containers:
      | container_id                 | ship_symbol   | status    |
      | scout-tour-test-scout-1-ghi1 | TEST-SCOUT-1  | RUNNING   |
      | scout-tour-test-scout-3-old3 | TEST-SCOUT-3  | STOPPED   |
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
      | TEST-SCOUT-3 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
      | X1-TEST-C3 |
    Then scout markets should complete successfully
    And 3 container IDs should be returned
    And 2 new containers should be created
    And container "scout-tour-test-scout-1-ghi1" should be reused
    And container for "TEST-SCOUT-2" should be newly created
    And container for "TEST-SCOUT-3" should be newly created

  Scenario: Scout markets prevents race condition - concurrent calls for same ships
    When I execute scout markets concurrently 3 times for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
    Then all scout market calls should complete successfully
    And exactly 2 unique containers should exist
    And no duplicate containers should exist for "TEST-SCOUT-1"
    And no duplicate containers should exist for "TEST-SCOUT-2"

  Scenario: Scout markets retry after timeout reuses containers
    Given existing scout containers:
      | container_id                 | ship_symbol   | status    |
      | scout-tour-test-scout-1-jkl1 | TEST-SCOUT-1  | STARTING  |
      | scout-tour-test-scout-2-jkl2 | TEST-SCOUT-2  | STARTING  |
    When I execute scout markets for system "X1-TEST" with ships:
      | TEST-SCOUT-1 |
      | TEST-SCOUT-2 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
    And the first call times out after 5 minutes
    And I retry scout markets with the same parameters
    Then the retry should complete successfully
    And 2 container IDs should be returned
    And no duplicate containers should be created
    And container "scout-tour-test-scout-1-jkl1" should be reused
    And container "scout-tour-test-scout-2-jkl2" should be reused
