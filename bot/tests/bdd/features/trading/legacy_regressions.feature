Feature: Trading regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | cargo blocks future segments | tests.test_circuit_breaker_smart_skip | regression_cargo_blocks_future_segments |
      | cargo does not block when space available | tests.test_circuit_breaker_smart_skip | regression_cargo_does_not_block_when_space_available |
      | dependency detection cargo dependency | tests.test_circuit_breaker_smart_skip | regression_dependency_detection_cargo_dependency |
      | dependency detection credit dependency | tests.test_circuit_breaker_smart_skip | regression_dependency_detection_credit_dependency |
      | dependency detection independence | tests.test_circuit_breaker_smart_skip | regression_dependency_detection_independence |
      | example scenario from spec smart skip vs abort all | tests.test_circuit_breaker_smart_skip | regression_example_scenario_from_spec_smart_skip_vs_abort_all |
      | should not skip when all depend on failed | tests.test_circuit_breaker_smart_skip | regression_should_not_skip_when_all_depend_on_failed |
      | should not skip when remaining profit too low | tests.test_circuit_breaker_smart_skip | regression_should_not_skip_when_remaining_profit_too_low |
      | should skip segment with independents remaining | tests.test_circuit_breaker_smart_skip | regression_should_skip_segment_with_independents_remaining |
      | cargo flow tracking with net zero segment | tests.test_dependency_analysis_cargo_flow_bug | regression_cargo_flow_tracking_with_net_zero_segment |
      | cargo flow tracking with partial sells | tests.test_dependency_analysis_cargo_flow_bug | regression_cargo_flow_tracking_with_partial_sells |
      | should skip segment abort when source fails | tests.test_dependency_analysis_cargo_flow_bug | regression_should_skip_segment_abort_when_source_fails |
      | calculate distance with coordinate dicts succeeds | tests.test_fixed_route_coordinate_bug | regression_calculate_distance_with_coordinate_dicts_succeeds |
      | calculate distance with waypoint symbols fails | tests.test_fixed_route_coordinate_bug | regression_calculate_distance_with_waypoint_symbols_fails |
      | create fixed route coordinate lookup | tests.test_fixed_route_coordinate_bug | regression_create_fixed_route_coordinate_lookup |
      | intermediate segment has both sell and buy | tests.test_multileg_route_planning_bug | regression_intermediate_segment_has_both_sell_and_buy |
      | route planner creates actions for starting waypoint | tests.test_multileg_route_planning_bug | regression_route_planner_creates_actions_for_starting_waypoint |
      | multileg route action placement simple 4 leg | tests.test_multileg_trader_action_placement | regression_multileg_route_action_placement_simple_4_leg |
      | multileg route correct action placement | tests.test_multileg_trader_action_placement | regression_multileg_route_correct_action_placement |
      | route planner assigns buy actions to start waypoint | tests.test_multileg_trader_action_placement | regression_route_planner_assigns_buy_actions_to_start_waypoint |
      | greedy planner does not create orphaned buys | tests.test_orphaned_buy_validation | regression_greedy_planner_does_not_create_orphaned_buys |
      | route validation accepts complete routes | tests.test_orphaned_buy_validation | regression_route_validation_accepts_complete_routes |
      | route validation rejects orphaned buy actions | tests.test_orphaned_buy_validation | regression_route_validation_rejects_orphaned_buy_actions |
      | TestSellAllTypeConsistency.multileg trader defensive handling dict return | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_multileg_trader_defensive_handling_dict_return |
      | TestSellAllTypeConsistency.multileg trader defensive handling int return | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_multileg_trader_defensive_handling_int_return |
      | TestSellAllTypeConsistency.multileg trader defensive handling none return | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_multileg_trader_defensive_handling_none_return |
      | TestSellAllTypeConsistency.multileg trader handles sell all int return | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_multileg_trader_handles_sell_all_int_return |
      | TestSellAllTypeConsistency.sell all actual implementation consistency | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_sell_all_actual_implementation_consistency |
      | TestSellAllTypeConsistency.sell all returns int as documented | tests.unit.test_sell_all_type_consistency | TestSellAllTypeConsistency.regression_sell_all_returns_int_as_documented |
      | circuit breaker continues after recovery integration | tests.bdd.steps.trading.test_circuit_breaker_continue_after_recovery_steps | regression_circuit_breaker_continues_after_recovery_integration |
      | circuit breaker partial cargo integration | tests.bdd.steps.trading.test_circuit_breaker_partial_cargo_steps | regression_circuit_breaker_partial_cargo_integration |
