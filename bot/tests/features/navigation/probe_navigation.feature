Feature: Probe and Satellite Navigation
  As a probe or satellite ship type
  I want specialized navigation handling
  So that probe-specific movement constraints are respected

  Background:
    Given ships with probe or satellite type
    And specialized navigation requirements for probes

  @xfail
  Scenario: Probe navigates with type-specific constraints
    Given a probe ship at orbital position
    And probe has limited fuel capacity
    And destination is within probe range
    When planning probe navigation route
    Then route should account for probe movement constraints
    And fuel consumption should match probe specifications

  @xfail
  Scenario: Satellite maintains orbital position
    Given a satellite ship in orbit
    And satellite has positioning requirements
    When planning satellite movement
    Then satellite orbital constraints should be enforced
    And movement should maintain stable orbit

  @xfail
  Scenario: Probe fuel efficiency optimization
    Given a probe with minimal fuel capacity
    And multiple waypoints to visit
    When planning probe route
    Then route should optimize for probe fuel efficiency
    And DRIFT mode should be considered for probes
