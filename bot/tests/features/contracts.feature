Feature: Contracts validation

  Scenario Outline: Validate Contracts module <module>
    When I execute the "contracts" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_batch_contract_operations.py |
    | test_contract_fulfillment_crash_bug.py |
    | test_contract_pagination_bug.py |
    | test_contract_price_polling.py |
    | test_contract_profitability_real_prices.py |
    | test_contract_transaction_limit_bug.py |

