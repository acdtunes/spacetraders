from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_intermediate_refuel_bug",
    "tests.test_prefer_cruise_fix",
    "tests.test_probe_satellite_navigation",
    "tests.test_refuel_step_execution_bug",
    "tests.test_refuel_via_intermediate",
    "tests.test_routing_critical_bugs_fix",
    "tests.test_safety_margin_cruise_selection_bug",
    "tests.test_tour_optimization_quality",
    "tests.test_tour_cache_persistence",
    "tests.unit.core.test_ortools_routing",
    "tests.unit.core.test_route_optimizer",
    "tests.unit.core.test_smart_navigator_unit",
    "tests.unit.core.test_tour_optimizer",
    "tests.unit.operations.test_navigation_operation",
    "tests.unit.operations.test_routing_operation"
]

scenarios("../../features/navigation/legacy_regressions.feature")
