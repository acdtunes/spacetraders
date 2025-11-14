Feature: Scout Markets Command - Fleet Orchestration
  Deploy multiple ships to scout markets using VRP optimization.
  This command orchestrates fleet distribution across markets, reuses existing containers,
  and creates new containers only when needed (idempotent design).

  Background:
    Given a scout markets test database
    And a scout markets player with ID 1 and agent "TESTBOT"
    And the scout markets player has token "test-token-123"

  Scenario: Deploy single ship to scout all markets
    Given I have a ship "SCOUT-1" at waypoint "X1-TEST-A1" in system "X1-TEST"
    And the system "X1-TEST" has markets at "X1-TEST-A1,X1-TEST-B2"
    When I execute ScoutMarketsCommand with ships "SCOUT-1" and markets "X1-TEST-A1,X1-TEST-B2" in system "X1-TEST"
    Then the scout markets command should succeed
    And 1 scout container should be created
    And ship "SCOUT-1" should be assigned 2 markets

  Scenario: Deploy multiple ships with VRP optimization
    Given I have a ship "SCOUT-1" at waypoint "X1-TEST-A1" in system "X1-TEST"
    And I have a ship "SCOUT-2" at waypoint "X1-TEST-B2" in system "X1-TEST"
    And the system "X1-TEST" has markets at "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3,X1-TEST-D4"
    And the routing client will partition markets using VRP
    When I execute ScoutMarketsCommand with ships "SCOUT-1,SCOUT-2" and markets "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3,X1-TEST-D4" in system "X1-TEST"
    Then the scout markets command should succeed
    And 2 scout containers should be created
    And markets should be distributed across ships

  Scenario: Idempotent container reuse - ship already has active container
    Given I have a ship "SCOUT-1" at waypoint "X1-TEST-A1" in system "X1-TEST"
    And ship "SCOUT-1" has an active container "scout-tour-scout-1-abc12345"
    And the system "X1-TEST" has markets at "X1-TEST-A1,X1-TEST-B2"
    When I execute ScoutMarketsCommand with ships "SCOUT-1" and markets "X1-TEST-A1,X1-TEST-B2" in system "X1-TEST"
    Then the scout markets command should succeed
    And 0 new scout containers should be created
    And 1 scout container should be reused
    And the reused container is "scout-tour-scout-1-abc12345"

  Scenario: Partial container reuse - some ships have containers
    Given I have a ship "SCOUT-1" at waypoint "X1-TEST-A1" in system "X1-TEST"
    And I have a ship "SCOUT-2" at waypoint "X1-TEST-B2" in system "X1-TEST"
    And ship "SCOUT-1" has an active container "scout-tour-scout-1-abc12345"
    And the system "X1-TEST" has markets at "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3"
    When I execute ScoutMarketsCommand with ships "SCOUT-1,SCOUT-2" and markets "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3" in system "X1-TEST"
    Then the scout markets command should succeed
    And 1 new scout container should be created
    And 1 scout container should be reused
    And ship "SCOUT-2" should have a new scout container

  Scenario: Early return when all ships already have containers
    Given I have a ship "SCOUT-1" at waypoint "X1-TEST-A1" in system "X1-TEST"
    And I have a ship "SCOUT-2" at waypoint "X1-TEST-B2" in system "X1-TEST"
    And ship "SCOUT-1" has an active container "scout-tour-scout-1-abc12345"
    And ship "SCOUT-2" has an active container "scout-tour-scout-2-def67890"
    And the system "X1-TEST" has markets at "X1-TEST-A1,X1-TEST-B2"
    When I execute ScoutMarketsCommand with ships "SCOUT-1,SCOUT-2" and markets "X1-TEST-A1,X1-TEST-B2" in system "X1-TEST"
    Then the scout markets command should succeed
    And 0 new scout containers should be created
    And 2 scout containers should be reused
    And all requested ships have containers
