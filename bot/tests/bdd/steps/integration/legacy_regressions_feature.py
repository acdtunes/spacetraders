from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_component_interactions_simple",
    "tests.test_mcp_market_tools"
]

scenarios("../../features/integration/legacy_regressions.feature")
