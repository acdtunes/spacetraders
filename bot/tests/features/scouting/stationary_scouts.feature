Feature: Stationary Scout Load Balancing
  As a scout coordinator
  I want to balance workload between touring and stationary scouts
  So that no scout is overwhelmed with markets to monitor

  Background:
    Given a scout coordinator managing multiple scouts
    And markets requiring periodic price updates

  @xfail
  Scenario: Balance markets between touring and stationary scouts
    Given 20 markets in the system
    And 2 touring scouts available
    And 3 stationary scouts available
    When assigning markets to scouts
    Then touring scouts should visit clustered markets
    And stationary scouts should monitor isolated markets
    And workload should be balanced across all scouts

  @xfail
  Scenario: Prevent stationary scout overload
    Given 15 markets to monitor
    And 1 touring scout
    And 1 stationary scout
    When assigning markets
    Then stationary scout should not be assigned more than 5 markets
    And touring scout should cover remaining markets
    And imbalance should not exceed 3:1 ratio

  @xfail
  Scenario: Stationary scout imbalance fix
    Given initial assignment with 10 markets to stationary scout
    And 2 markets to touring scout
    When rebalancing scout assignments
    Then stationary scout should be assigned fewer markets
    And touring scout should visit more markets
    And workload should be more evenly distributed
