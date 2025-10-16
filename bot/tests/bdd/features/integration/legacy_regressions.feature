Feature: Integration regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | TestCheckpointDataFlow.cancel signal changes state | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_cancel_signal_changes_state |
      | TestCheckpointDataFlow.checkpoint contains actual navigation state | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_checkpoint_contains_actual_navigation_state |
      | TestCheckpointDataFlow.checkpoint persisted to disk | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_checkpoint_persisted_to_disk |
      | TestCheckpointDataFlow.multiple checkpoints track progress | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_multiple_checkpoints_track_progress |
      | TestCheckpointDataFlow.pause signal preserves state | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_pause_signal_preserves_state |
      | TestCheckpointDataFlow.refuel checkpoint has docked state | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_refuel_checkpoint_has_docked_state |
      | TestCheckpointDataFlow.resume loads actual checkpoint data | tests.test_component_interactions_simple | TestCheckpointDataFlow.regression_resume_loads_actual_checkpoint_data |
      | TestProgressMetrics.get progress returns checkpoint count | tests.test_component_interactions_simple | TestProgressMetrics.regression_get_progress_returns_checkpoint_count |
      | market find sellers formats results | tests.test_mcp_market_tools | regression_market_find_sellers_formats_results |
      | market summarize handles missing data | tests.test_mcp_market_tools | regression_market_summarize_handles_missing_data |
      | market waypoint lists all goods | tests.test_mcp_market_tools | regression_market_waypoint_lists_all_goods |
