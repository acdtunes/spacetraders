Feature: Scout regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | geographic partition boundary case | tests.test_partition_overlap_bug | regression_geographic_partition_boundary_case |
      | geographic partition disjoint sets | tests.test_partition_overlap_bug | regression_geographic_partition_disjoint_sets |
      | centroid based start location | tests.test_scout_coordinator_partitioning | regression_centroid_based_start_location |
      | disjoint partitions with common start location | tests.test_scout_coordinator_partitioning | regression_disjoint_partitions_with_common_start_location |
      | partition balance preserves disjoint property | tests.test_scout_coordinator_partitioning | regression_partition_balance_preserves_disjoint_property |
      | disjoint tours with multiple ships | tests.test_scout_markets_list_fix | regression_disjoint_tours_with_multiple_ships |
      | tour plan integration with markets list | tests.test_scout_markets_list_fix | regression_tour_plan_integration_with_markets_list |
      | tour starts from first assigned market | tests.test_scout_markets_list_fix | regression_tour_starts_from_first_assigned_market |
      | balance tour times missing ship data | tests.unit.core.test_scout_coordinator_core | regression_balance_tour_times_missing_ship_data |
      | balance tour times no boundary market | tests.unit.core.test_scout_coordinator_core | regression_balance_tour_times_no_boundary_market |
      | balance tour times reallocates | tests.unit.core.test_scout_coordinator_core | regression_balance_tour_times_reallocates |
      | balance tour times tsp path | tests.unit.core.test_scout_coordinator_core | regression_balance_tour_times_tsp_path |
      | check reconfigure signal invalid json | tests.unit.core.test_scout_coordinator_core | regression_check_reconfigure_signal_invalid_json |
      | coordinator builds graph when missing | tests.unit.core.test_scout_coordinator_core | regression_coordinator_builds_graph_when_missing |
      | coordinator uses cached graph | tests.unit.core.test_scout_coordinator_core | regression_coordinator_uses_cached_graph |
      | estimate partition tour time | tests.unit.core.test_scout_coordinator_core | regression_estimate_partition_tour_time |
      | handle reconfiguration no changes | tests.unit.core.test_scout_coordinator_core | regression_handle_reconfiguration_no_changes |
      | handle reconfiguration updates ships | tests.unit.core.test_scout_coordinator_core | regression_handle_reconfiguration_updates_ships |
      | monitor and restart | tests.unit.core.test_scout_coordinator_core | regression_monitor_and_restart |
      | monitor cycle restarts daemon | tests.unit.core.test_scout_coordinator_core | regression_monitor_cycle_restarts_daemon |
      | monitor cycle triggers reconfigure | tests.unit.core.test_scout_coordinator_core | regression_monitor_cycle_triggers_reconfigure |
      | optimize subtour uses two opt | tests.unit.core.test_scout_coordinator_core | regression_optimize_subtour_uses_two_opt |
      | partition and start records assignments | tests.unit.core.test_scout_coordinator_core | regression_partition_and_start_records_assignments |
      | partition markets geographic even distribution | tests.unit.core.test_scout_coordinator_core | regression_partition_markets_geographic_even_distribution |
      | partition markets geographic horizontal | tests.unit.core.test_scout_coordinator_core | regression_partition_markets_geographic_horizontal |
      | partition markets geographic vertical | tests.unit.core.test_scout_coordinator_core | regression_partition_markets_geographic_vertical |
      | partition markets greedy assigns all | tests.unit.core.test_scout_coordinator_core | regression_partition_markets_greedy_assigns_all |
      | partition markets kmeans | tests.unit.core.test_scout_coordinator_core | regression_partition_markets_kmeans |
      | restart daemon for missing assignment | tests.unit.core.test_scout_coordinator_core | regression_restart_daemon_for_missing_assignment |
      | save config and check reconfigure | tests.unit.core.test_scout_coordinator_core | regression_save_config_and_check_reconfigure |
      | start scout daemon available | tests.unit.core.test_scout_coordinator_core | regression_start_scout_daemon_available |
      | start scout daemon existing assignment | tests.unit.core.test_scout_coordinator_core | regression_start_scout_daemon_existing_assignment |
      | start scout daemon start failure | tests.unit.core.test_scout_coordinator_core | regression_start_scout_daemon_start_failure |
      | stop all | tests.unit.core.test_scout_coordinator_core | regression_stop_all |
      | wait for tours complete success | tests.unit.core.test_scout_coordinator_core | regression_wait_for_tours_complete_success |
      | wait for tours complete timeout | tests.unit.core.test_scout_coordinator_core | regression_wait_for_tours_complete_timeout |
      | coordinator add ship operation rejects duplicate | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_add_ship_operation_rejects_duplicate |
      | coordinator add ship operation updates config | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_add_ship_operation_updates_config |
      | coordinator remove ship operation prevents last ship | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_remove_ship_operation_prevents_last_ship |
      | coordinator remove ship operation updates config | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_remove_ship_operation_updates_config |
      | coordinator start operation happy path | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_start_operation_happy_path |
      | coordinator start operation player missing | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_start_operation_player_missing |
      | coordinator status operation reports state | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_status_operation_reports_state |
      | coordinator stop operation handles missing config | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_stop_operation_handles_missing_config |
      | coordinator stop operation stops daemons | tests.unit.operations.test_scout_coordination_operation | regression_coordinator_stop_operation_stops_daemons |
