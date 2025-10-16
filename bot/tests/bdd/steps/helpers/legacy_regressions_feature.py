from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.unit.helpers.test_paths"
]

scenarios("../../features/helpers/legacy_regressions.feature")
