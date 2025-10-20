Feature: Navigator Integration in Contract Operations
  As a contract fulfillment system
  I want to properly pass navigator to all sub-operations
  So that navigation to markets doesn't crash with AttributeError

  Background:
    Given a ship "STARGAZER-1" with 40 cargo capacity at "X1-JB26-A2"
    And a SmartNavigator for system "X1-JB26"

  @xfail
  Scenario: Navigator passed to purchase operation
    Given a contract requiring 26 units of CLOTHING at "X1-JB26-A1"
    And market "X1-JB26-K88" sells CLOTHING at 1,225 credits per unit
    And the market is 100 units away
    When I fulfill the contract with navigator passed explicitly
    Then the navigator should be used for route execution to market
    And the purchase should succeed at "X1-JB26-K88"
    And the contract should be fulfilled

  @xfail
  Scenario: Navigator initialized internally when not provided
    Given a contract requiring 26 units of CLOTHING at "X1-JB26-A1"
    And market "X1-JB26-K88" sells CLOTHING at 1,225 credits per unit
    When I fulfill the contract without passing navigator parameter
    Then a navigator should be initialized internally
    And navigation to market should succeed
    And the contract should be fulfilled without crashes

  @xfail
  Scenario: Navigator crash without proper initialization (bug reproduction)
    Given a contract requiring 26 units of CLOTHING at "X1-JB26-A1"
    And market discovery finds "X1-JB26-K88" after retries
    And _acquire_initial_resources is called without navigator parameter
    When the system tries to navigate to the market
    Then it would crash with AttributeError (if bug not fixed)
    But with the fix, navigator is properly initialized
    And navigation succeeds
