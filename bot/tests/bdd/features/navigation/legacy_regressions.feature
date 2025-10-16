Feature: Navigation regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | direct drift when no intermediate stations exist | tests.test_intermediate_refuel_bug | regression_direct_drift_when_no_intermediate_stations_exist |
      | should find intermediate refuel station instead of drift | tests.test_intermediate_refuel_bug | regression_should_find_intermediate_refuel_station_instead_of_drift |
      | prefer cruise avoids drift | tests.test_prefer_cruise_fix | regression_prefer_cruise_avoids_drift |
      | prefer cruise emergency drift | tests.test_prefer_cruise_fix | regression_prefer_cruise_emergency_drift |
      | normal ship with zero fuel fails health validation | tests.test_probe_satellite_navigation | regression_normal_ship_with_zero_fuel_fails_health_validation |
      | probe routing uses cruise mode | tests.test_probe_satellite_navigation | regression_probe_routing_uses_cruise_mode |
      | probe routing works for multi hop | tests.test_probe_satellite_navigation | regression_probe_routing_works_for_multi_hop |
      | probe ship passes health validation | tests.test_probe_satellite_navigation | regression_probe_ship_passes_health_validation |
      | probe ship role variations | tests.test_probe_satellite_navigation | regression_probe_ship_role_variations |
      | smart navigator plan route for probe | tests.test_probe_satellite_navigation | regression_smart_navigator_plan_route_for_probe |
      | smart navigator validates probe route | tests.test_probe_satellite_navigation | regression_smart_navigator_validates_probe_route |
      | drift final approach | tests.test_refuel_step_execution_bug | regression_drift_final_approach |
      | refuel step execution | tests.test_refuel_step_execution_bug | regression_refuel_step_execution |
      | refuel step in route plan | tests.test_refuel_step_execution_bug | regression_refuel_step_in_route_plan |
      | direct route without intermediate | tests.test_refuel_via_intermediate | regression_direct_route_without_intermediate |
      | refuel at intermediate waypoint | tests.test_refuel_via_intermediate | regression_refuel_at_intermediate_waypoint |
      | TestRoutingCriticalBugs.bug1 iteration limit insufficient for long paths | tests.test_routing_critical_bugs_fix | TestRoutingCriticalBugs.regression_bug1_iteration_limit_insufficient_for_long_paths |
      | TestRoutingCriticalBugs.bug2 misleading insufficient fuel error | tests.test_routing_critical_bugs_fix | TestRoutingCriticalBugs.regression_bug2_misleading_insufficient_fuel_error |
      | TestRoutingCriticalBugs.bug3 contract market ignores distance | tests.test_routing_critical_bugs_fix | TestRoutingCriticalBugs.regression_bug3_contract_market_ignores_distance |
      | TestRoutingCriticalBugs.integration all bugs together | tests.test_routing_critical_bugs_fix | TestRoutingCriticalBugs.regression_integration_all_bugs_together |
      | legitimate drift still allowed | tests.test_safety_margin_cruise_selection_bug | regression_legitimate_drift_still_allowed |
      | safety margin bug 382 unit cruise | tests.test_safety_margin_cruise_selection_bug | regression_safety_margin_bug_382_unit_cruise |
      | safety margin bug boundary case | tests.test_safety_margin_cruise_selection_bug | regression_safety_margin_bug_boundary_case |
      | TestProductionScenario.dragonspyre 23 waypoint tour | tests.test_tour_optimization_quality | TestProductionScenario.regression_dragonspyre_23_waypoint_tour |
      | TestProductionScenario.large grid tour quality | tests.test_tour_optimization_quality | TestProductionScenario.regression_large_grid_tour_quality |
      | TestTourOptimizationQuality.long timeout produces better tour | tests.test_tour_optimization_quality | TestTourOptimizationQuality.regression_long_timeout_produces_better_tour |
      | TestTourOptimizationQuality.short timeout may produce suboptimal tour | tests.test_tour_optimization_quality | TestTourOptimizationQuality.regression_short_timeout_may_produce_suboptimal_tour |
      | TestTourOptimizationQuality.simple grid tour no crossings | tests.test_tour_optimization_quality | TestTourOptimizationQuality.regression_simple_grid_tour_no_crossings |
      | wal checkpoint persists data immediately | tests.test_tour_cache_persistence | regression_wal_checkpoint_persists_data_immediately |
      | without checkpoint data may be lost | tests.test_tour_cache_persistence | regression_without_checkpoint_data_may_be_lost |
      | tour cache with return to start persists | tests.test_tour_cache_persistence | regression_tour_cache_with_return_to_start_persists |
      | multiple tours persist with checkpoints | tests.test_tour_cache_persistence | regression_multiple_tours_persist_with_checkpoints |
      | tour cache immediately queryable same connection | tests.test_tour_cache_persistence | regression_tour_cache_immediately_queryable_same_connection |
      | checkpoint performance is acceptable | tests.test_tour_cache_persistence | regression_checkpoint_performance_is_acceptable |
      | ortools router probe fast path | tests.unit.core.test_ortools_routing | regression_ortools_router_probe_fast_path |
      | ortools router selects drift when cruise impossible | tests.unit.core.test_ortools_routing | regression_ortools_router_selects_drift_when_cruise_impossible |
      | routing config validation | tests.unit.core.test_ortools_routing | regression_routing_config_validation |
      | routing validator detects deviation | tests.unit.core.test_ortools_routing | regression_routing_validator_detects_deviation |
      | routing validator resumes on success | tests.unit.core.test_ortools_routing | regression_routing_validator_resumes_on_success |
      | emergency drift to fuel station | tests.unit.core.test_route_optimizer | regression_emergency_drift_to_fuel_station |
      | find optimal route cruise | tests.unit.core.test_route_optimizer | regression_find_optimal_route_cruise |
      | prefer drift when allowed | tests.unit.core.test_route_optimizer | regression_prefer_drift_when_allowed |
      | ensure graph builds when missing | tests.unit.core.test_smart_navigator_unit | regression_ensure_graph_builds_when_missing |
      | ensure graph loads from json | tests.unit.core.test_smart_navigator_unit | regression_ensure_graph_loads_from_json |
      | validate ship health branches | tests.unit.core.test_smart_navigator_unit | regression_validate_ship_health_branches |
      | build tour from invalid order | tests.unit.core.test_tour_optimizer | regression_build_tour_from_invalid_order |
      | plan tour with cache | tests.unit.core.test_tour_optimizer | regression_plan_tour_with_cache |
      | navigate operation missing player | tests.unit.operations.test_navigation_operation | regression_navigate_operation_missing_player |
      | navigate operation success | tests.unit.operations.test_navigation_operation | regression_navigate_operation_success |
      | navigate ship cross system fails | tests.unit.operations.test_navigation_operation | regression_navigate_ship_cross_system_fails |
      | navigate ship same location | tests.unit.operations.test_navigation_operation | regression_navigate_ship_same_location |
      | navigate ship success | tests.unit.operations.test_navigation_operation | regression_navigate_ship_success |
      | navigate ship validation failure | tests.unit.operations.test_navigation_operation | regression_navigate_ship_validation_failure |
      | graph build operation failure | tests.unit.operations.test_routing_operation | regression_graph_build_operation_failure |
      | graph build operation success | tests.unit.operations.test_routing_operation | regression_graph_build_operation_success |
      | route plan operation builds graph when missing | tests.unit.operations.test_routing_operation | regression_route_plan_operation_builds_graph_when_missing |
      | route plan operation graph build failure | tests.unit.operations.test_routing_operation | regression_route_plan_operation_graph_build_failure |
      | route plan operation includes refuel | tests.unit.operations.test_routing_operation | regression_route_plan_operation_includes_refuel |
      | route plan operation missing ship | tests.unit.operations.test_routing_operation | regression_route_plan_operation_missing_ship |
      | route plan operation no route | tests.unit.operations.test_routing_operation | regression_route_plan_operation_no_route |
      | route plan operation success | tests.unit.operations.test_routing_operation | regression_route_plan_operation_success |
      | route plan operation writes output | tests.unit.operations.test_routing_operation | regression_route_plan_operation_writes_output |
      | scout markets operation graph build failure | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_graph_build_failure |
      | scout markets operation missing ship | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_missing_ship |
      | scout markets operation no markets | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_no_markets |
      | scout markets operation only current location | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_only_current_location |
      | scout markets operation single tour | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_single_tour |
      | scout markets operation tour failure | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_tour_failure |
      | scout markets operation unknown algorithm | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_unknown_algorithm |
      | scout markets operation writes output | tests.unit.operations.test_routing_operation | regression_scout_markets_operation_writes_output |
