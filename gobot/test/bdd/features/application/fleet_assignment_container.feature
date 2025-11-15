Feature: Fleet Assignment Container
  As a space trader
  I want fleet assignment to run asynchronously in a container
  So that the CLI returns immediately without blocking for VRP optimization

  Background:
    Given a mediator is configured with scouting handlers
    And a player with ID 1 exists with agent "TEST-AGENT"
    And a system "X1-TEST" exists with multiple waypoints

  Scenario: CLI returns immediately with scout-fleet-assignment container ID
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
      | X1-TEST-D1   | ASTEROID        | MARKETPLACE       |
    When I invoke AssignScoutingFleet via gRPC for system "X1-TEST" and player 1
    Then the gRPC call should return in less than 1 second
    And the response should contain a container ID
    And the container should be of type "SCOUT_FLEET_ASSIGNMENT"
    And the container status should be "PENDING" or "RUNNING"

  Scenario: Scout-fleet-assignment container completes successfully
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
      | X1-TEST-D1   | ASTEROID        | MARKETPLACE       |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    And the scout-fleet-assignment container runs to completion
    Then the container status should be "COMPLETED"
    And the container should have created 2 scout-tour containers
    And scout-tour containers should exist for ships "PROBE-1" and "PROBE-2"

  Scenario: Scout-fleet-assignment container creates scout-tour containers with VRP assignments
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
      | X1-TEST-D1   | ASTEROID        | MARKETPLACE       |
      | X1-TEST-E1   | MOON            | MARKETPLACE       |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    And the scout-fleet-assignment container runs to completion
    Then each scout-tour container should have assigned markets
    And all 4 markets should be covered across the 2 scout-tour containers
    And no market should be assigned to multiple ships

  Scenario: Scout-fleet-assignment container fails when no probe ships exist
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | HAULER-1   | FRAME_HEAVY    | X1-TEST-A1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    And the scout-fleet-assignment container runs to completion
    Then the container status should be "FAILED"
    And the container error should contain "no probe or satellite ships found"
    And no scout-tour containers should be created

  Scenario: Scout-fleet-assignment container fails when no markets exist
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | GAS_GIANT       | FUEL_STATION      |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    And the scout-fleet-assignment container runs to completion
    Then the container status should be "FAILED"
    And the container error should contain "no non-fuel-station marketplaces found"
    And no scout-tour containers should be created

  Scenario: Scout-fleet-assignment container logs progress
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    And the scout-fleet-assignment container runs to completion
    Then the container logs should contain "Container started"
    And the container logs should contain "Discovered 2 probe/satellite ships"
    And the container logs should contain "Discovered 2 marketplaces"
    And the container logs should contain "Running VRP optimization"
    And the container logs should contain "Created scout-tour container"
    And the container logs should contain "Container completed successfully"

  Scenario: Scout-fleet-assignment container is one-time execution (not iterative)
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
    When I create a scout-fleet-assignment container for system "X1-TEST" and player 1
    Then the container max_iterations should be 1
    And the container current_iteration should be 0
    When the scout-fleet-assignment container runs to completion
    Then the container current_iteration should be 1
    And the container status should be "COMPLETED"
