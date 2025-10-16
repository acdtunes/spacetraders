Feature: Mining regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | mining cycle aborts on failed navigation | tests.unit.operations.test_mining_cycle | regression_mining_cycle_aborts_on_failed_navigation |
      | mining cycle execute success | tests.unit.operations.test_mining_cycle | regression_mining_cycle_execute_success |
      | find alternative asteroids | tests.unit.operations.test_mining_operation | regression_find_alternative_asteroids |
      | mining operation fails without ship status | tests.unit.operations.test_mining_operation | regression_mining_operation_fails_without_ship_status |
      | mining operation route validation failure | tests.unit.operations.test_mining_operation | regression_mining_operation_route_validation_failure |
      | mining operation success path | tests.unit.operations.test_mining_operation | regression_mining_operation_success_path |
      | targeted mining circuit breaker triggers | tests.unit.operations.test_mining_operation | regression_targeted_mining_circuit_breaker_triggers |
      | targeted mining navigation failure | tests.unit.operations.test_mining_operation | regression_targeted_mining_navigation_failure |
      | targeted mining success | tests.unit.operations.test_mining_operation | regression_targeted_mining_success |
