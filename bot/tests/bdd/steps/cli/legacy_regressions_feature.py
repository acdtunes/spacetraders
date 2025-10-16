from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.unit.cli.test_main_cli"
]

scenarios("../../features/cli/legacy_regressions.feature")
