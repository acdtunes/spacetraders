Feature: Circuit Breaker with Partial Cargo
  As a contract operator
  I want the circuit breaker to handle partial cargo correctly
  So that collected resources aren't wasted when mining fails

  Background:
    Given a ship at asteroid "X1-TEST-B8" with 40 cargo capacity
    And a contract requiring 65 ALUMINUM_ORE

  Scenario: Circuit breaker triggers with enough partial cargo
    Given ship has 65 units of ALUMINUM_ORE in cargo
    When targeted mining fails with circuit breaker after collecting 0 units
    Then mining should be marked as success
    And should proceed to delivery without buying
    And should deliver 65 units

  Scenario: Circuit breaker triggers with partial cargo and no alternatives
    Given ship has 22 units of ALUMINUM_ORE in cargo
    When targeted mining fails with circuit breaker after collecting 22 units
    And no alternative asteroids are available
    And no buy_from market is specified
    Then operation should fail with partial cargo message
    And should report having 22 of 65 units

  Scenario: Circuit breaker triggers with partial cargo and successful alternative
    Given ship has 0 units of ALUMINUM_ORE in cargo
    When targeted mining fails with circuit breaker after collecting 22 units
    And alternative asteroid "X1-TEST-C5" is available
    And alternative mining succeeds collecting 43 units
    Then mining should be marked as success
    And should have collected 65 total units
    And should proceed to delivery

  Scenario: Circuit breaker triggers with partial cargo and alternative also fails
    Given ship has 0 units of ALUMINUM_ORE in cargo
    When targeted mining fails with circuit breaker after collecting 22 units
    And alternative asteroid "X1-TEST-C5" is available
    And alternative mining fails with circuit breaker after collecting 30 units
    And buy_from market "X1-TEST-B7" is specified
    Then should fall back to buying 13 remaining units
    And should have collected 52 total units from mining

  Scenario: Alternative asteroid gets correct remaining amount
    Given ship has 0 units of ALUMINUM_ORE in cargo
    And ship has 18 cargo units used by other materials
    When targeted mining fails with circuit breaker after collecting 15 units
    And alternative asteroid "X1-TEST-C5" is available
    Then alternative should be asked to mine 22 units
    And not the original 65 units

  Scenario: Circuit breaker with partial cargo and buying fallback
    Given ship has 10 units of ALUMINUM_ORE in cargo
    When targeted mining fails with circuit breaker after collecting 30 units
    And no alternative asteroids are available
    And buy_from market "X1-TEST-B7" is specified
    Then should fall back to buying 25 remaining units
    And total should be 65 units after buying
