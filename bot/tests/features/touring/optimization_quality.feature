Feature: Tour Optimization Quality
  As an OR-Tools TSP solver
  I want to produce optimal tours without edge crossings
  So that scouts follow efficient routes

  Background:
    Given a ship with standard configuration
    And OR-Tools TSP solver

  @xfail
  Scenario: Simple 3x3 grid produces no crossings
    Given a 3x3 grid graph with 9 waypoints
    And long timeout (30 seconds) for optimization
    When I optimize a tour visiting 8 waypoints
    Then the tour should have zero edge crossings
    And tour should follow spiral or perimeter pattern

  @xfail
  Scenario: Short timeout may produce suboptimal tour
    Given a 3x3 grid graph with 9 waypoints
    And short timeout (5 seconds) for optimization
    When I optimize a tour visiting 8 waypoints
    Then the tour may have some edge crossings
    And solution quality depends on solver luck

  @xfail
  Scenario: Longer timeout produces better solution
    Given a 3x3 grid graph with 9 waypoints
    When I optimize tour with 5 second timeout
    And I optimize same tour with 30 second timeout
    Then 30-second solution should have equal or fewer crossings
    And 30-second solution should have equal or shorter distance

  @xfail
  Scenario: Large grid (25 waypoints) optimizes without crossings
    Given a 5x5 grid graph with 25 waypoints
    And production timeout (30 seconds)
    When I optimize a tour visiting 23 waypoints plus start
    Then the tour should have zero edge crossings
    And tour should complete within timeout

  @xfail
  Scenario: Production scenario with 23 waypoints (manual validation)
    Given real X1-VH85 graph data with DRAGONSPYRE-3 markets
    And 23 waypoints to visit
    When I optimize tour with proper timeout
    Then tour should have minimal or zero crossings
