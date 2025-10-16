from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.unit.operations.test_mining_cycle",
    "tests.unit.operations.test_mining_operation"
]

scenarios("../../features/mining/legacy_regressions.feature")
