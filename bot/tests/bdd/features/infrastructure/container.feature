Feature: Dependency Injection Container
  As a configuration system
  I want to provide singleton instances of dependencies
  So that the application can use dependency injection

  Background:
    Given the container is reset

  # Database Factory
  Scenario: Get database returns instance
    When I get the database instance
    Then the database should not be null
    And the database should have a connection attribute

  Scenario: Get database returns singleton
    When I get the database instance twice
    Then both database instances should be the same

  Scenario: Get database creates new instance after reset
    Given I get the database instance
    When I reset the container
    And I get the database instance again
    Then the new database instance should be different

  # Player Repository Factory
  Scenario: Get player repository returns instance
    When I get the player repository instance
    Then the player repository should not be null

  Scenario: Get player repository returns singleton
    When I get the player repository instance twice
    Then both player repository instances should be the same

  Scenario: Get player repository creates new instance after reset
    Given I get the player repository instance
    When I reset the container
    And I get the player repository instance again
    Then the new player repository instance should be different

  # API Client Factory (Player-based)
  Scenario: Get API client for player returns instance
    Given a player with id 1 and token "test-token-123" exists
    When I get the API client for the player
    Then the API client should not be null
    And the API client token should match the player token

  Scenario: Get API client for player raises error for nonexistent player
    Given player 999 does not exist
    When I attempt to get the API client for the nonexistent player
    Then the call should fail with ValueError
    And the error message should mention "not found"

  Scenario: Get API client for player creates new instance each time
    Given a player with id 1 and token "test-token-123" exists
    When I get the API client for the player twice
    Then both API client instances should be different

  # Ship Repository Factory
  Scenario: Get ship repository returns instance
    When I get the ship repository instance
    Then the ship repository should not be null

  Scenario: Get ship repository returns singleton
    When I get the ship repository instance twice
    Then both ship repository instances should be the same

  # Route Repository Factory
  Scenario: Get route repository returns instance
    When I get the route repository instance
    Then the route repository should not be null

  Scenario: Get route repository returns singleton
    When I get the route repository instance twice
    Then both route repository instances should be the same

  # Graph Builder Factory (Player-based)
  Scenario: Get graph builder for player returns instance
    Given a player with id 1 and token "test-token-123" exists
    When I get the graph builder for the player
    Then the graph builder should not be null

  Scenario: Get graph builder for player creates new instance each time
    Given a player with id 1 and token "test-token-123" exists
    When I get the graph builder for the player twice
    Then both graph builder instances should be different

  # Graph Provider Factory (Player-based)
  Scenario: Get graph provider for player returns instance
    Given a player with id 1 and token "test-token-123" exists
    When I get the graph provider for the player
    Then the graph provider should not be null

  Scenario: Get graph provider for player creates new instance each time
    Given a player with id 1 and token "test-token-123" exists
    When I get the graph provider for the player twice
    Then both graph provider instances should be different

  # Routing Engine Factory
  Scenario: Get routing engine returns instance
    When I get the routing engine instance
    Then the routing engine should not be null

  Scenario: Get routing engine returns singleton
    When I get the routing engine instance twice
    Then both routing engine instances should be the same

  # Mediator Factory
  Scenario: Get mediator returns instance
    When I get the mediator instance
    Then the mediator should not be null

  Scenario: Get mediator returns singleton
    When I get the mediator instance twice
    Then both mediator instances should be the same

  Scenario: Mediator has behaviors registered
    When I get the mediator instance
    Then the mediator should have behaviors or handlers attribute

  # Reset Container
  Scenario: Reset container resets all singletons
    Given I get all singleton instances
    When I reset the container
    And I get all singleton instances again
    Then all new instances should be different from original instances

  Scenario: Reset container can be called multiple times
    When I reset the container 3 times
    Then no errors should occur

  Scenario: Reset container allows fresh configuration
    Given I get the mediator instance
    When I reset the container
    And I get the mediator instance again
    Then both mediators should be valid but different

  # Dependency Injection
  Scenario: All repositories created successfully
    When I create all repository instances
    Then all repositories should be created without errors
