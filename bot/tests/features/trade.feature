Feature: Trade validation

  Scenario Outline: Validate Trade module <module>
    When I execute the "trade" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_cargo_cleanup_market_search_bug.py |
    | test_cargo_cleanup_sell_destination_bug.py |
    | test_cargo_overflow_execution_bug.py |
    | test_circuit_breaker_buy_only_segment.py |
    | test_circuit_breaker_buy_spike_simple.py |
    | test_circuit_breaker_cargo_cleanup.py |
    | test_circuit_breaker_price_spike_profitability_bug.py |
    | test_circuit_breaker_profitability.py |
    | test_circuit_breaker_selective_salvage.py |
    | test_circuit_breaker_selective_salvage_simple.py |
    | test_circuit_breaker_smart_skip.py |
    | test_circuit_breaker_stale_sell_price.py |
    | test_circuit_breaker_wrong_market_bug.py |
    | test_find_planned_sell_destination_helper.py |
    | test_fleet_trade_optimizer.py |
    | test_market_data_module.py |
    | test_market_freshness_visibility_bug.py |
    | test_multileg_cargo_overflow_bug.py |
    | test_multileg_route_planning_bug.py |
    | test_multileg_trader_action_placement.py |
    | test_multileg_trader_price_extraction.py |
    | test_orphaned_buy_validation.py |
    | test_price_degradation_model.py |
    | test_price_impact_model.py |
    | test_price_validation_circuit_breaker.py |
    | test_purchase_ship_low_fuel_navigation.py |
    | test_route_planning_residual_cargo_bug.py |
    | test_stale_market_data_route_planning.py |
    | test_trade_plan_stale_market_data_bug.py |
    | test_trade_plan_wrong_price_field.py |
    | test_trade_route_profit_calculation_bugs.py |

