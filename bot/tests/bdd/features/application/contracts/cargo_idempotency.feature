Feature: Contract Workflow Cargo Idempotency

  Background:
    Given a player with id 1 and 100000 credits
    And a ship "TEST-1" for player 1 at waypoint "X1-TEST-A1"
    And ship "TEST-1" has 40 cargo capacity
    And a mock contract requiring 30 units of "IRON_ORE" to deliver to "X1-TEST-B1"
    And waypoint "X1-TEST-A1" sells "IRON_ORE" at 100 credits per unit

  Scenario: Ship already has required cargo - skips purchase
    Given ship "TEST-1" has 30 units of "IRON_ORE" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should skip navigation to seller market
    And the workflow should skip cargo purchase
    And the workflow should deliver cargo directly
    And the contract should be fulfilled

  Scenario: Ship has wrong cargo - jettisons then purchases
    Given ship "TEST-1" has 20 units of "COPPER" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should jettison 20 units of "COPPER"
    And the workflow should navigate to seller market "X1-TEST-A1"
    And the workflow should purchase 30 units of "IRON_ORE"
    And the contract should be fulfilled

  Scenario: Ship has partial required cargo - purchases remainder
    Given ship "TEST-1" has 10 units of "IRON_ORE" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should not jettison any cargo
    And the workflow should navigate to seller market "X1-TEST-A1"
    And the workflow should purchase 20 units of "IRON_ORE"
    And the contract should be fulfilled

  Scenario: Ship has required cargo plus wrong cargo
    Given ship "TEST-1" has 30 units of "IRON_ORE" in cargo
    And ship "TEST-1" has 5 units of "COPPER" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should jettison 5 units of "COPPER"
    And the workflow should skip navigation to seller market
    And the workflow should skip cargo purchase
    And the contract should be fulfilled

  Scenario: Empty ship - baseline behavior
    Given ship "TEST-1" has empty cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should navigate to seller market "X1-TEST-A1"
    And the workflow should purchase 30 units of "IRON_ORE"
    And the contract should be fulfilled

  Scenario: Ship with exact cargo mix - jettisons and purchases
    Given ship "TEST-1" has 15 units of "IRON_ORE" in cargo
    And ship "TEST-1" has 10 units of "COPPER" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should jettison 10 units of "COPPER"
    And the workflow should navigate to seller market "X1-TEST-A1"
    And the workflow should purchase 15 units of "IRON_ORE"
    And the contract should be fulfilled

  Scenario: Ship is FULL with required cargo but insufficient total - delivers first
    Given a mock contract requiring 75 units of "IRON_ORE" to deliver to "X1-TEST-B1"
    And ship "TEST-1" has 40 units of "IRON_ORE" in cargo
    When I execute contract batch workflow for 1 iteration
    Then the workflow should deliver 40 units of "IRON_ORE"
    And the workflow should not purchase any cargo
