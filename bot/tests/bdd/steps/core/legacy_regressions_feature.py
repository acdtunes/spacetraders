from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_market_data_module",
    "tests.unit.core.test_api_client",
    "tests.unit.core.test_daemon_manager"
]

scenarios("../../features/core/legacy_regressions.feature")
