Feature: Operations regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | TestBatchContractOperation.batch accept all contracts regardless of profitability | tests.test_batch_contract_operations | TestBatchContractOperation.regression_batch_accept_all_contracts_regardless_of_profitability |
      | TestBatchContractOperation.batch all fulfillments fail | tests.test_batch_contract_operations | TestBatchContractOperation.regression_batch_all_fulfillments_fail |
      | TestBatchContractOperation.batch all profitable contracts | tests.test_batch_contract_operations | TestBatchContractOperation.regression_batch_all_profitable_contracts |
      | TestBatchContractOperation.batch handle fulfillment failure | tests.test_batch_contract_operations | TestBatchContractOperation.regression_batch_handle_fulfillment_failure |
      | TestBatchContractOperation.batch handle negotiation failure | tests.test_batch_contract_operations | TestBatchContractOperation.regression_batch_handle_negotiation_failure |
      | TestBatchContractSequentialExecution.always accept contracts no profitability filter | tests.test_batch_contract_operations | TestBatchContractSequentialExecution.regression_always_accept_contracts_no_profitability_filter |
      | TestBatchContractSequentialExecution.sequential execution prevents error 4511 | tests.test_batch_contract_operations | TestBatchContractSequentialExecution.regression_sequential_execution_prevents_error_4511 |
      | TestContractProfitabilityEvaluation.already fulfilled contract | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_already_fulfilled_contract |
      | TestContractProfitabilityEvaluation.no delivery requirements | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_no_delivery_requirements |
      | TestContractProfitabilityEvaluation.partially fulfilled contract | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_partially_fulfilled_contract |
      | TestContractProfitabilityEvaluation.profitable contract | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_profitable_contract |
      | TestContractProfitabilityEvaluation.unprofitable low profit contract | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_unprofitable_low_profit_contract |
      | TestContractProfitabilityEvaluation.unprofitable low roi contract | tests.test_batch_contract_operations | TestContractProfitabilityEvaluation.regression_unprofitable_low_roi_contract |
      | TestSingleContractBackwardCompatibility.single contract mode requires contract id | tests.test_batch_contract_operations | TestSingleContractBackwardCompatibility.regression_single_contract_mode_requires_contract_id |
      | TestCargoCleanupMarketSearchFix.cleanup finds nearby market and navigates | tests.test_cargo_cleanup_market_search_bug | TestCargoCleanupMarketSearchFix.regression_cleanup_finds_nearby_market_and_navigates |
      | TestCargoCleanupMarketSearchFix.cleanup sells at current market when compatible | tests.test_cargo_cleanup_market_search_bug | TestCargoCleanupMarketSearchFix.regression_cleanup_sells_at_current_market_when_compatible |
      | TestCargoCleanupMarketSearchFix.cleanup skips unsellable goods gracefully | tests.test_cargo_cleanup_market_search_bug | TestCargoCleanupMarketSearchFix.regression_cleanup_skips_unsellable_goods_gracefully |
      | circuit breaker sells stranded cargo on buy price spike | tests.test_circuit_breaker_cargo_cleanup | regression_circuit_breaker_sells_stranded_cargo_on_buy_price_spike |
      | circuit breaker sells stranded cargo on segment unprofitable | tests.test_circuit_breaker_cargo_cleanup | regression_circuit_breaker_sells_stranded_cargo_on_segment_unprofitable |
      | circuit breaker sells stranded cargo on sell price crash | tests.test_circuit_breaker_cargo_cleanup | regression_circuit_breaker_sells_stranded_cargo_on_sell_price_crash |
      | purchase splits when exceeding transaction limit | tests.test_contract_transaction_limit_bug | regression_purchase_splits_when_exceeding_transaction_limit |
      | utilities operation distance | tests.unit.operations.test_analysis_operation | regression_utilities_operation_distance |
      | utilities operation find fuel missing data | tests.unit.operations.test_analysis_operation | regression_utilities_operation_find_fuel_missing_data |
      | utilities operation find fuel success | tests.unit.operations.test_analysis_operation | regression_utilities_operation_find_fuel_success |
      | utilities operation find mining | tests.unit.operations.test_analysis_operation | regression_utilities_operation_find_mining |
      | utilities operation unknown util type | tests.unit.operations.test_analysis_operation | regression_utilities_operation_unknown_util_type |
      | assignment assign operation failure | tests.unit.operations.test_assignment_operations | regression_assignment_assign_operation_failure |
      | assignment assign operation success | tests.unit.operations.test_assignment_operations | regression_assignment_assign_operation_success |
      | assignment available operation available | tests.unit.operations.test_assignment_operations | regression_assignment_available_operation_available |
      | assignment available operation unavailable | tests.unit.operations.test_assignment_operations | regression_assignment_available_operation_unavailable |
      | assignment find operation none available | tests.unit.operations.test_assignment_operations | regression_assignment_find_operation_none_available |
      | assignment find operation with requirements | tests.unit.operations.test_assignment_operations | regression_assignment_find_operation_with_requirements |
      | assignment list operation no assignments | tests.unit.operations.test_assignment_operations | regression_assignment_list_operation_no_assignments |
      | assignment list operation requires player id | tests.unit.operations.test_assignment_operations | regression_assignment_list_operation_requires_player_id |
      | assignment list operation with assignments | tests.unit.operations.test_assignment_operations | regression_assignment_list_operation_with_assignments |
      | assignment reassign operation failure | tests.unit.operations.test_assignment_operations | regression_assignment_reassign_operation_failure |
      | assignment reassign operation requires ships | tests.unit.operations.test_assignment_operations | regression_assignment_reassign_operation_requires_ships |
      | assignment reassign operation success | tests.unit.operations.test_assignment_operations | regression_assignment_reassign_operation_success |
      | assignment release operation | tests.unit.operations.test_assignment_operations | regression_assignment_release_operation |
      | assignment sync operation | tests.unit.operations.test_assignment_operations | regression_assignment_sync_operation |
      | assignment assign operation | tests.unit.operations.test_assignments_operation | regression_assignment_assign_operation |
      | assignment assign operation failure | tests.unit.operations.test_assignments_operation | regression_assignment_assign_operation_failure |
      | assignment available operation success | tests.unit.operations.test_assignments_operation | regression_assignment_available_operation_success |
      | assignment available unavailable | tests.unit.operations.test_assignments_operation | regression_assignment_available_unavailable |
      | assignment find available | tests.unit.operations.test_assignments_operation | regression_assignment_find_available |
      | assignment find no available | tests.unit.operations.test_assignments_operation | regression_assignment_find_no_available |
      | assignment list no assignments | tests.unit.operations.test_assignments_operation | regression_assignment_list_no_assignments |
      | assignment list prints assignments | tests.unit.operations.test_assignments_operation | regression_assignment_list_prints_assignments |
      | assignment list requires player id | tests.unit.operations.test_assignments_operation | regression_assignment_list_requires_player_id |
      | assignment reassign operation failure | tests.unit.operations.test_assignments_operation | regression_assignment_reassign_operation_failure |
      | assignment reassign operation no ships | tests.unit.operations.test_assignments_operation | regression_assignment_reassign_operation_no_ships |
      | assignment reassign operation success | tests.unit.operations.test_assignments_operation | regression_assignment_reassign_operation_success |
      | assignment release operation success | tests.unit.operations.test_assignments_operation | regression_assignment_release_operation_success |
      | assignment status details | tests.unit.operations.test_assignments_operation | regression_assignment_status_details |
      | assignment status unknown | tests.unit.operations.test_assignments_operation | regression_assignment_status_unknown |
      | assignment sync operation | tests.unit.operations.test_assignments_operation | regression_assignment_sync_operation |
      | append to log raises after circuit breaker trip | tests.unit.operations.test_captain_logging | regression_append_to_log_raises_after_circuit_breaker_trip |
      | append to log uses circuit breaker | tests.unit.operations.test_captain_logging | regression_append_to_log_uses_circuit_breaker |
      | captain log operation actions | tests.unit.operations.test_captain_logging | regression_captain_log_operation_actions |
      | initialize log creates file | tests.unit.operations.test_captain_logging | regression_initialize_log_creates_file |
      | log entry operation completed sections | tests.unit.operations.test_captain_logging | regression_log_entry_operation_completed_sections |
      | log entry requires narrative | tests.unit.operations.test_captain_logging | regression_log_entry_requires_narrative |
      | log entry ignores scout operations | tests.unit.operations.test_captain_logging | regression_log_entry_ignores_scout_operations |
      | log entry updates session | tests.unit.operations.test_captain_logging | regression_log_entry_updates_session |
      | search and report | tests.unit.operations.test_captain_logging | regression_search_and_report |
      | session end saves archive | tests.unit.operations.test_captain_logging | regression_session_end_saves_archive |
      | session start records state | tests.unit.operations.test_captain_logging | regression_session_start_records_state |
      | get api client missing player | tests.unit.operations.test_common | regression_get_api_client_missing_player |
      | get api client returns client | tests.unit.operations.test_common | regression_get_api_client_returns_client |
      | get captain logger missing player | tests.unit.operations.test_common | regression_get_captain_logger_missing_player |
      | get captain logger returns cached instance | tests.unit.operations.test_common | regression_get_captain_logger_returns_cached_instance |
      | get operator name | tests.unit.operations.test_common | regression_get_operator_name |
      | humanize duration | tests.unit.operations.test_common | regression_humanize_duration |
      | log captain event handles none | tests.unit.operations.test_common | regression_log_captain_event_handles_none |
      | log captain event invokes writer | tests.unit.operations.test_common | regression_log_captain_event_invokes_writer |
      | setup logging creates file | tests.unit.operations.test_common | regression_setup_logging_creates_file |
      | strategy discovers market and updates preference | tests.unit.operations.test_contract_resource_strategy | regression_strategy_discovers_market_and_updates_preference |
      | strategy respects preferred market | tests.unit.operations.test_contract_resource_strategy | regression_strategy_respects_preferred_market |
      | strategy times out after retries | tests.unit.operations.test_contract_resource_strategy | regression_strategy_times_out_after_retries |
      | circuit breaker records failures and trips | tests.unit.operations.test_control_primitives | regression_circuit_breaker_records_failures_and_trips |
      | circuit breaker resets on success | tests.unit.operations.test_control_primitives | regression_circuit_breaker_resets_on_success |
      | daemon cleanup operation | tests.unit.operations.test_daemon_operation | regression_daemon_cleanup_operation |
      | daemon logs operation | tests.unit.operations.test_daemon_operation | regression_daemon_logs_operation |
      | daemon start operation ship already assigned | tests.unit.operations.test_daemon_operation | regression_daemon_start_operation_ship_already_assigned |
      | daemon start operation success | tests.unit.operations.test_daemon_operation | regression_daemon_start_operation_success |
      | daemon status operation list | tests.unit.operations.test_daemon_operation | regression_daemon_status_operation_list |
      | daemon status operation single | tests.unit.operations.test_daemon_operation | regression_daemon_status_operation_single |
      | daemon stop operation releases assignment | tests.unit.operations.test_daemon_operation | regression_daemon_stop_operation_releases_assignment |
      | monitor operation single iteration | tests.unit.operations.test_fleet_operation | regression_monitor_operation_single_iteration |
      | status operation lists all ships | tests.unit.operations.test_fleet_operation | regression_status_operation_lists_all_ships |
