Feature: Scout Tour Wait Optimization
  Optimize wait time between iterations based on tour type

  Background:
    Given a player with ID 1 exists
    And a ship "TEST-SCOUT-1" exists at "X1-TEST-A1" for player 1

  Scenario: Stationary scout waits 60 seconds between iterations
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
    When I execute a scout tour iteration with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the tour should wait 60 seconds before next iteration

  Scenario: Touring scout does not wait between iterations
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
      | X1-TEST-B2   |
      | X1-TEST-C3   |
    When I execute a scout tour iteration with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the tour should not wait before next iteration

  Scenario: Two-market tour does not wait
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
      | X1-TEST-B2   |
    When I execute a scout tour iteration with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the tour should not wait before next iteration
