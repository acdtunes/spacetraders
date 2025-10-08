Feature: Routing operations
  Scenario: Graph build succeeds for system
    Given a routing context with a graph builder that succeeds
    When the graph build operation runs for system "X1-TEST"
    Then the graph build operation succeeds

  Scenario: Graph build fails when builder returns nothing
    Given a routing context with a graph builder that fails
    When the graph build operation runs for system "X1-TEST"
    Then the graph build operation fails

  Scenario: Route planning succeeds with existing graph
    Given a route planning context with an existing graph
    When the route plan operation builds a route from "A" to "B"
    Then the route plan operation succeeds

  Scenario: Route plan writes output file
    Given a route planning context with an existing graph
    When the route plan operation writes the route to a file
    Then the route file is created

  Scenario: Route planning builds graph when missing
    Given a route planning context without a graph but with a builder
    When the route plan operation builds a route from "A" to "B"
    Then the graph builder is invoked

  Scenario: Route planning fails without ship data
    Given a route planning context with an existing graph
    And the API returns no ship data
    When the route plan operation builds a route from "A" to "B"
    Then the route plan operation fails

  Scenario: Route planning handles refuel steps
    Given a route planning context with a refuel step route
    When the route plan operation builds a route from "A" to "B"
    Then the route output includes a refuel step

  Scenario: Route planning fails when no route is available
    Given a route planning context with a route optimizer that returns nothing
    When the route plan operation builds a route from "A" to "B"
    Then the route plan operation fails

  Scenario: Scout markets executes a single tour and records data
    Given a scouting context with two markets and available tour
    When the scout markets operation runs once
    Then the markets are visited and logged

  Scenario: Scout markets fails when graph build fails
    Given a scouting context where graph cannot be built
    When the scout markets operation runs once
    Then the scout markets operation fails

  Scenario: Scout markets fails with missing ship
    Given a scouting context with a missing ship record
    When the scout markets operation runs once
    Then the scout markets operation fails

  Scenario: Scout markets fails when no markets are discovered
    Given a scouting context with an empty graph
    When the scout markets operation runs once
    Then the scout markets operation fails

  Scenario: Scout markets skips when ship is at the only market
    Given a scouting context where the ship is at the only market
    When the scout markets operation runs once
    Then the scout markets operation exits without tours

  Scenario: Scout markets reports unknown algorithms as errors
    Given a scouting context with two markets and available tour
    When the scout markets operation runs with algorithm "unsupported"
    Then the scout markets operation fails

  Scenario: Scout markets retries when tour planning returns nothing
    Given a scouting context where the optimizer returns no tour
    When the scout markets operation runs once
    Then the scout markets operation fails

  Scenario: Scout markets writes output file when tour succeeds
    Given a scouting context with two markets and available tour
    When the scout markets operation writes the tour to a file
    Then the tour file is created
