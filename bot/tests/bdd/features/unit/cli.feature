Feature: CLI Command Routing
  As a bot operator
  I want CLI commands to route to the correct operation handlers
  So that I can execute operations from the command line

  Scenario: Route graph-build command to operation handler
    Given the CLI is ready to process commands
    When I run "graph-build --player-id 7 --system X1-TEST"
    Then the graph_build_operation should be called
    And the operation should receive system "X1-TEST"
    And the command should succeed with exit code 0

  Scenario: Route route-plan command to operation handler
    Given the CLI is ready to process commands
    When I run "route-plan --player-id 9 --ship SHIP-1 --system X1-OPS --start A --goal B"
    Then the route_plan_operation should be called
    And the operation should receive goal waypoint "B"
    And the command should return exit code 5

  Scenario: Route assignments list command to operation handler
    Given the CLI is ready to process commands
    When I run "assignments list --player-id 3"
    Then the assignment_list_operation should be called
    And the command should succeed with exit code 0

  Scenario: Handle missing subcommand gracefully
    Given the CLI is ready to process commands
    When I run "assignments"
    Then the command should fail with exit code 1
    And usage help should be displayed

  Scenario: Route scout-coordinator status command
    Given the CLI is ready to process commands
    When I run "scout-coordinator status --player-id 5 --system X1-TEST"
    Then the coordinator_status_operation should be called
    And the operation should receive coordinator action "status"
    And the command should succeed with exit code 0

  Scenario: Route daemon start command
    Given the CLI is ready to process commands
    When I run "daemon start --player-id 11 --ship SHIP-9 --operation mining"
    Then the daemon_start_operation should be called
    And the operation should receive daemon action "start"
    And the command should return exit code 3
