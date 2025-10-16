from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_batch_contract_operations",
    "tests.test_cargo_cleanup_market_search_bug",
    "tests.test_circuit_breaker_cargo_cleanup",
    "tests.test_contract_transaction_limit_bug",
    "tests.unit.operations.test_analysis_operation",
    "tests.unit.operations.test_assignment_operations",
    "tests.unit.operations.test_assignments_operation",
    "tests.unit.operations.test_captain_logging",
    "tests.unit.operations.test_common",
    "tests.unit.operations.test_contract_resource_strategy",
    "tests.unit.operations.test_control_primitives",
    "tests.unit.operations.test_daemon_operation",
    "tests.unit.operations.test_fleet_operation"
]

scenarios("../../features/operations/legacy_regressions.feature")
