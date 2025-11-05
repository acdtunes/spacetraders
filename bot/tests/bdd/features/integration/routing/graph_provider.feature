Feature: System Graph Provider Integration
  As a routing system
  I want to cache and provide system graphs
  So that I can avoid repeated API calls

  Background:
    Given a graph provider with mocked database and builder

  # Cache Operations
  Scenario: Load graph from database cache
    Given the database has cached graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" without forcing refresh
    Then the graph should be loaded from "database"
    And the result message should contain "database cache"
    And the database should have been queried for graph

  Scenario: Cache miss triggers API build
    Given the database has no cached graph for "SYSTEM-A"
    And the builder can build graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" without forcing refresh
    Then the graph should be loaded from "api"
    And the result message should contain "from API"
    And the builder should have been called for "SYSTEM-A"
    And the graph should have been saved to database

  Scenario: Force refresh skips cache
    Given the builder can build graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" forcing refresh
    Then the graph should be loaded from "api"
    And the database should not have been queried for graph
    And the builder should have been called for "SYSTEM-A"

  # Internal Methods - Load from Database
  Scenario: Load existing graph from database
    Given the database has cached graph for "SYSTEM-A"
    When I load from database for "SYSTEM-A"
    Then the loaded graph should match cached data
    And the database should have been queried with "SYSTEM-A"

  Scenario: Load non-existent graph from database
    Given the database has no cached graph for "NONEXISTENT"
    When I load from database for "NONEXISTENT"
    Then the loaded graph should be None

  Scenario: Database error returns None
    Given the database throws error on connection
    When I load from database for "SYSTEM-A"
    Then the loaded graph should be None

  # Internal Methods - Build from API
  Scenario: Build and save graph from API
    Given the builder can build graph for "SYSTEM-A"
    When I build from API for "SYSTEM-A"
    Then the built graph should match builder output
    And the builder should have been called for "SYSTEM-A"
    And the graph should have been saved to database

  Scenario: Builder error is propagated
    Given the builder throws error for "SYSTEM-A"
    When I build from API for "SYSTEM-A"
    Then building should fail with "Failed to build graph"
    And the error message should mention "SYSTEM-A"

  # Internal Methods - Save to Database
  Scenario: Save new graph to database
    Given a sample graph for "SYSTEM-A"
    When I save to database for "SYSTEM-A"
    Then the database should have received INSERT with UPSERT
    And the saved data should include system "SYSTEM-A"

  Scenario: Database save error is handled gracefully
    Given the database throws error on transaction
    And a sample graph for "SYSTEM-A"
    When I save to database for "SYSTEM-A"
    Then no exception should be raised

  Scenario: Save updates existing graph
    Given a sample graph for "SYSTEM-A"
    When I save to database for "SYSTEM-A"
    Then the SQL should use UPSERT pattern
    And the SQL should contain "ON CONFLICT"
    And the SQL should contain "DO UPDATE SET"

  # Integration Scenarios
  Scenario: First request builds and caches
    Given the database has no cached graph for "SYSTEM-A"
    And the builder can build graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" without forcing refresh
    Then the graph should be loaded from "api"
    And the database should have been queried for graph
    And the builder should have been called once
    And the graph should have been saved to database

  Scenario: Second request uses cache
    Given the database has cached graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" without forcing refresh
    Then the graph should be loaded from "database"
    And the database should have been queried for graph
    And the builder should not have been called

  Scenario: Force refresh rebuilds cache
    Given the builder can build updated graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" forcing refresh
    Then the graph should be loaded from "api"
    And the builder should have been called once
    And the graph should have been saved to database

  Scenario: Multiple systems cached separately
    Given the database has cached graph for "SYSTEM-A" with system name "SYSTEM-A"
    And the database has cached graph for "SYSTEM-B" with system name "SYSTEM-B"
    When I get graph for "SYSTEM-A" without forcing refresh
    And I get graph for "SYSTEM-B" without forcing refresh
    Then both graphs should have different system names
    And both graphs should be loaded from "database"

  Scenario: Database error falls back to API
    Given the database throws error on connection
    And the builder can build graph for "SYSTEM-A"
    When I get graph for "SYSTEM-A" without forcing refresh
    Then the graph should be loaded from "api"
    And the builder should have been called once

  # Cache Consistency
  Scenario: JSON serialization roundtrip preserves data
    Given a sample graph for "SYSTEM-A"
    When I save to database for "SYSTEM-A"
    Then the saved JSON should deserialize to original graph

  Scenario: Complex graph structure preserved
    Given a complex graph for "COMPLEX" with decimals and lists
    When I build from API for "COMPLEX"
    Then the built graph should match builder output
    And the saved JSON should preserve all data types
