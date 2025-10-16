Feature: Cli regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | cli assignments list | tests.unit.cli.test_main_cli | regression_cli_assignments_list |
      | cli assignments missing action | tests.unit.cli.test_main_cli | regression_cli_assignments_missing_action |
      | cli daemon start | tests.unit.cli.test_main_cli | regression_cli_daemon_start |
      | cli dispatches graph build | tests.unit.cli.test_main_cli | regression_cli_dispatches_graph_build |
      | cli route plan | tests.unit.cli.test_main_cli | regression_cli_route_plan |
      | cli scout coordinator status | tests.unit.cli.test_main_cli | regression_cli_scout_coordinator_status |
