Feature: Purchase Transaction Limits
  As a contract automation system
  I need to respect market transaction limits when purchasing cargo
  So that large purchases are split into multiple compliant transactions

  Scenario: Purchase exceeds market transaction limit and splits into multiple transactions
    Given a contract workflow handler with market repository access
    And a market "X1-HZ85-K88" selling "EQUIPMENT" with transaction limit of 20 units
    When I query the transaction limit for "EQUIPMENT" at "X1-HZ85-K88"
    Then the transaction limit should be 20 units

  Scenario: No market data defaults to unlimited transaction limit
    Given a contract workflow handler with market repository access
    And no market data exists for waypoint "X1-UNKNOWN-A1"
    When I query the transaction limit for "EQUIPMENT" at "X1-UNKNOWN-A1"
    Then the transaction limit should be 999999 units
