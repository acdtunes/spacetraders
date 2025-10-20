Feature: Mining module core classes
  As a developer
  I want to test the refactored mining classes
  So that I can ensure each component works independently

  Background:
    Given a mining test environment

  # MiningCycle Tests - Core scenarios
  Scenario: MiningCycle executes complete cycle
    Given a mining cycle
    And ship is configured for mining
    When I execute mining cycle 1
    Then cycle should succeed
    And all cycle steps should execute

  Scenario: MiningCycle handles navigation failure
    Given a mining cycle
    And navigation will fail to destination
    When I execute mining cycle 1
    Then cycle should fail with navigation error

  # Mining helpers
  Scenario: mine_until_cargo_full fills cargo
    Given a mining context
    And ship has empty cargo
    When I mine until cargo is full
    Then cargo should be at capacity
    And extraction count should be positive

  Scenario: sell_cargo generates revenue
    Given a mining context
    And ship has full cargo
    When I sell all cargo
    Then revenue should be positive
    And cargo should be empty

  # TargetedMiningSession Tests
  Scenario: TargetedMiningSession collects target resource
    Given a targeted session for "IRON_ORE"
    And asteroid yields target resource
    When I run targeted session
    Then session should succeed
    And target units should be collected

  Scenario: TargetedMiningSession trips circuit breaker
    Given a targeted session for "IRON_ORE"
    And asteroid yields wrong resource
    And breaker limit is 5
    When I run targeted session
    Then session should fail
    And reason should mention breaker

  # MiningOperationExecutor Tests
  Scenario: Executor setup succeeds with valid config
    Given a mining executor
    And ship exists with adequate fuel
    And routes are validated
    When I setup executor
    Then setup should succeed
    And all components should initialize

  Scenario: Executor handles missing ship
    Given a mining executor
    And ship does not exist
    When I setup executor
    Then setup should fail
    And error should be logged

  Scenario: Executor validates routes before execution
    Given a mining executor
    And ship has low fuel
    When I validate routes
    Then validation should fail
    And error should mention fuel

  Scenario: Executor resumes from checkpoint
    Given a mining executor
    And checkpoint exists with 3 cycles
    When I setup executor
    Then executor should resume from cycle 4
    And stats should reflect checkpoint

  # find_alternative_asteroids
  Scenario: find_alternative_asteroids locates matches
    Given an API client
    And system has 5 asteroids with correct traits
    When I find alternatives for "IRON_ORE"
    Then alternatives list should have 5 asteroids
    And current asteroid should be excluded

  Scenario: find_alternative_asteroids filters stripped
    Given an API client
    And system has asteroids including stripped
    When I find alternatives for "IRON_ORE"
    Then stripped asteroids should be excluded
