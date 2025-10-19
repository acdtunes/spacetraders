Feature: Scout Coordinator Market Partitioning
  As a scout coordinator managing multiple scout ships
  I want to partition markets into disjoint tours
  So that no market is visited by multiple scouts

  Background:
    Given a scout coordinator with multiple ships
    And a system with multiple markets
    And a complete graph of market waypoints

  @xfail
  Scenario: Disjoint partitions with common start location
    Given all scout ships start at the same waypoint
    And 8 markets distributed across the system
    When I partition markets among 2 scouts
    Then each market should appear in exactly one partition
    And partitions should not overlap
    And no scout should visit another scout's markets

  @xfail
  Scenario: Partition balance preserves disjoint property
    Given unbalanced initial market partitions
    When I rebalance partitions to equalize tour times
    Then partitions should remain disjoint
    And each market should still appear in exactly one partition
    And tour time variance should be reduced

  @xfail
  Scenario: Centroid-based start location selection
    Given market partitions for multiple scouts
    When calculating optimal start locations for each scout
    Then each scout should start near the centroid of their partition
    And start locations should minimize initial travel time
    And partitions should remain disjoint

  @xfail
  Scenario: Partition overlap detection
    Given market assignments for multiple scouts
    When validating partition disjointness
    Then any overlapping markets should be detected
    And overlap violations should be reported
    And correction should reassign overlapping markets
