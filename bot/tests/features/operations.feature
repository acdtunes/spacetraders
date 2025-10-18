Feature: Operations validation

  Scenario Outline: Validate Operations module <module>
    When I execute the "operations" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_api_retry_limit_bug.py |
    | test_mcp_market_tools.py |
    | test_mcp_multileg_trade_parameters.py |

