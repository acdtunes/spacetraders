Feature: Contract Transaction Limit Handling
  As a contract fulfillment operator
  I want to handle market transaction limits when purchasing resources
  So that large purchases are automatically split into multiple batches

  Background:
    Given a ship "SILMARETH-1" with 40 cargo capacity
    And the ship is at market "X1-GH18-E48" (docked)
    And the ship has 400 fuel

  @xfail
  Scenario: Split purchase when exceeding transaction limit
    Given a contract requiring 22 units of POLYNUCLEOTIDES
    And the market has a 20 unit per-transaction limit for POLYNUCLEOTIDES
    And the market purchase price is 100 credits per unit
    When I fulfill the contract
    Then the first purchase attempt (22 units) should fail with error 4604
    And the system should retry with 20 units (success)
    And the system should purchase remaining 2 units (success)
    And there should be at least 3 purchase calls total
    And the contract should be fulfilled successfully

  @xfail
  Scenario: Handle transaction limit from API error message
    Given a contract requiring 64 units of LIQUID_HYDROGEN
    And the market transaction limit is unknown initially
    And attempting to buy 64 units returns error 4604
    And the error message indicates "limit of 20 units per transaction"
    When I fulfill the contract
    Then the system should parse the limit from the error message
    And subsequent purchases should respect the 20 unit limit
    And the contract should be fulfilled successfully
