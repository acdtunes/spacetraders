Feature: Contract Profitability Evaluation with Real Market Prices
  As a contract evaluation system
  I want to use real market prices instead of estimates
  So that I don't accept massively unprofitable contracts

  Background:
    Given a ship with 40 cargo capacity
    And a database with real market prices

  @xfail
  Scenario: Reject contract with expensive resource (MEDICINE)
    Given MEDICINE costs 5,200 credits per unit at market
    And a contract requiring 40 units of MEDICINE
    And contract payment is 50,000 credits (5,000 accepted + 45,000 fulfilled)
    When I evaluate the contract profitability
    Then real cost should be 208,000 credits (40 × 5,200)
    And net profit should be -158,000 credits (massive loss)
    And the contract should be REJECTED
    And price source should be "market data"

  @xfail
  Scenario: Reject tiny payment contract (LIQUID_HYDROGEN)
    Given LIQUID_HYDROGEN costs 1,500 credits per unit at market
    And a contract requiring 64 units of LIQUID_HYDROGEN
    And contract payment is 2,627 credits total
    When I evaluate the contract profitability
    Then real cost should be 96,000 credits (64 × 1,500)
    And net profit should be approximately -93,373 credits
    And the contract should be REJECTED

  @xfail
  Scenario: Accept profitable contract with cheap resource
    Given LIQUID_HYDROGEN costs 1,500 credits per unit at market
    And a contract requiring 50 units of LIQUID_HYDROGEN
    And contract payment is 194,256 credits (19,425 + 174,831)
    When I evaluate the contract profitability
    Then real cost should be 75,200 credits (50 × 1,500 + fuel)
    And net profit should be approximately 119,056 credits
    And ROI should be approximately 158%
    And the contract should be ACCEPTED

  @xfail
  Scenario: Conservative estimate when no market data available
    Given no market price data is available for UNKNOWN_EXPENSIVE_GOOD
    And a contract requiring 50 units of UNKNOWN_EXPENSIVE_GOOD
    And contract payment is 100,000 credits
    When I evaluate the contract profitability
    Then estimated cost should use 5,000 credits per unit (conservative)
    And total cost should be 250,200 credits (50 × 5,000 + fuel)
    And net profit should be -150,200 credits
    And the contract should be REJECTED
    And price source should be "estimated (conservative)"

  Scenario: Already fulfilled contract
    Given a contract requiring 50 units of IRON_ORE
    And 50 units are already fulfilled
    When I evaluate the contract profitability
    Then the contract should be REJECTED
    And reason should be "already fulfilled"

  Scenario: No delivery requirements
    Given a contract with no delivery requirements
    When I evaluate the contract profitability
    Then the contract should be REJECTED
    And reason should be "No delivery requirements"

  Scenario: Partially fulfilled contract
    Given a contract requiring 50 units of IRON_ORE
    And 30 units are already fulfilled
    And IRON_ORE costs 150 credits per unit
    And contract payment is 110,000 credits
    When I evaluate the contract profitability
    Then units remaining should be 20
    And trips should be 1 (20 units / 40 capacity)
    And the evaluation should account for only remaining units
