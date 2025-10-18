Feature: Circuit Breaker validation

  Scenario Outline: Validate Circuit Breaker module <module>
    When I execute the "circuit_breaker" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
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

