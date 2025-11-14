Feature: Navigation Utilities
  As a navigation system
  I need helper functions to extract system symbols and calculate arrival times
  So that I can process waypoint symbols and time-based navigation

  Background:
    Given the navigation utilities are available

  Scenario: Extract system symbol from valid waypoint symbol
    When I extract the system symbol from waypoint "X1-ABC123-AB12"
    Then the extracted system symbol should be "X1-ABC123"

  Scenario: Extract system symbol from waypoint with multiple hyphens
    When I extract the system symbol from waypoint "X1-GZ7-F5"
    Then the extracted system symbol should be "X1-GZ7"

  Scenario: Extract system symbol from waypoint without hyphen
    When I extract the system symbol from waypoint "NOSYSTEM"
    Then the extracted system symbol should be "NOSYSTEM"

  Scenario: Extract system symbol from single segment waypoint
    When I extract the system symbol from waypoint "X1"
    Then the extracted system symbol should be "X1"

  Scenario: Calculate wait time for future arrival
    Given the current time is "2024-01-01T12:00:00Z"
    When I calculate wait time for arrival at "2024-01-01T12:05:30Z"
    Then the wait time should be 330 seconds

  Scenario: Calculate wait time for past arrival returns zero
    Given the current time is "2024-01-01T12:00:00Z"
    When I calculate wait time for arrival at "2024-01-01T11:50:00Z"
    Then the wait time should be 0 seconds

  Scenario: Calculate wait time handles Z suffix
    Given the current time is "2024-01-01T12:00:00Z"
    When I calculate wait time for arrival at "2024-01-01T12:03:00Z"
    Then the wait time should be 180 seconds

  Scenario: Calculate wait time handles +00:00 suffix
    Given the current time is "2024-01-01T12:00:00Z"
    When I calculate wait time for arrival at "2024-01-01T12:02:00+00:00"
    Then the wait time should be 120 seconds

  Scenario: Calculate wait time for invalid time string returns zero
    When I calculate wait time for arrival at "invalid-time-format"
    Then the wait time should be 0 seconds
    And a warning should be logged about parsing failure
