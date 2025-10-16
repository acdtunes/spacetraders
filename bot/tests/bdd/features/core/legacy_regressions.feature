Feature: Core regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | find markets buying orders by sell price | tests.test_market_data_module | regression_find_markets_buying_orders_by_sell_price |
      | find markets selling filters by system and supply | tests.test_market_data_module | regression_find_markets_selling_filters_by_system_and_supply |
      | get waypoint good and summary | tests.test_market_data_module | regression_get_waypoint_good_and_summary |
      | get waypoint goods returns all goods | tests.test_market_data_module | regression_get_waypoint_goods_returns_all_goods |
      | recent updates and stale queries | tests.test_market_data_module | regression_recent_updates_and_stale_queries |
      | api result success and failure helpers | tests.unit.core.test_api_client | regression_api_result_success_and_failure_helpers |
      | rate limit retries until success | tests.unit.core.test_api_client | regression_rate_limit_retries_until_success |
      | request result client error preserves payload | tests.unit.core.test_api_client | regression_request_result_client_error_preserves_payload |
      | request result success | tests.unit.core.test_api_client | regression_request_result_success |
      | request returns none for server failure | tests.unit.core.test_api_client | regression_request_returns_none_for_server_failure |
      | request wraps result for client error | tests.unit.core.test_api_client | regression_request_wraps_result_for_client_error |
      | cleanup stopped removes entries | tests.unit.core.test_daemon_manager | regression_cleanup_stopped_removes_entries |
      | fetch process none | tests.unit.core.test_daemon_manager | regression_fetch_process_none |
      | is running updates crashed | tests.unit.core.test_daemon_manager | regression_is_running_updates_crashed |
      | list all sorted | tests.unit.core.test_daemon_manager | regression_list_all_sorted |
      | start records daemon | tests.unit.core.test_daemon_manager | regression_start_records_daemon |
      | start returns false if running | tests.unit.core.test_daemon_manager | regression_start_returns_false_if_running |
      | status handles missing process | tests.unit.core.test_daemon_manager | regression_status_handles_missing_process |
      | status reports metrics | tests.unit.core.test_daemon_manager | regression_status_reports_metrics |
      | stop handles generic exception | tests.unit.core.test_daemon_manager | regression_stop_handles_generic_exception |
      | stop handles missing process | tests.unit.core.test_daemon_manager | regression_stop_handles_missing_process |
      | stop handles process disappearing | tests.unit.core.test_daemon_manager | regression_stop_handles_process_disappearing |
      | stop updates database | tests.unit.core.test_daemon_manager | regression_stop_updates_database |
      | tail logs missing file | tests.unit.core.test_daemon_manager | regression_tail_logs_missing_file |
      | tail logs reads file | tests.unit.core.test_daemon_manager | regression_tail_logs_reads_file |
