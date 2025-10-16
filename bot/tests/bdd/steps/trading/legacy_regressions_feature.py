from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_circuit_breaker_smart_skip",
    "tests.test_dependency_analysis_cargo_flow_bug",
    "tests.test_fixed_route_coordinate_bug",
    "tests.test_multileg_route_planning_bug",
    "tests.test_multileg_trader_action_placement",
    "tests.test_orphaned_buy_validation",
    "tests.unit.test_sell_all_type_consistency",
    "tests.bdd.steps.trading.test_circuit_breaker_continue_after_recovery_steps",
    "tests.bdd.steps.trading.test_circuit_breaker_partial_cargo_steps"
]

scenarios("../../features/trading/legacy_regressions.feature")
