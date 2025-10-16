from pytest_bdd import scenarios

pytest_plugins = [
    "tests.bdd.steps.common.regression_runner_steps",
    "tests.test_balance_tour_times_bug",
    "tests.test_partition_overlap_bug",
    "tests.test_scout_coordinator_bugs_real_world",
    "tests.test_scout_coordinator_partitioning",
    "tests.test_scout_markets_list_fix",
    "tests.test_tour_time_imbalance_bug",
    "tests.unit.core.test_scout_coordinator_core",
    "tests.unit.operations.test_scout_coordination_operation"
]

scenarios("../../features/scout/legacy_regressions.feature")
