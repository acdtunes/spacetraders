Feature: Navigation Checkpoint Resume
  As a smart navigator recovering from interruption
  I want to resume navigation from checkpoints correctly
  So that ships continue their routes with proper flight mode selection

  Background:
    Given a smart navigator with checkpoint capability
    And ships mid-route that may be interrupted

  @xfail
  Scenario: Checkpoint resume uses CRUISE when navigating to refuel
    Given a ship at B14 that needs to refuel at B7
    And route was planned with CRUISE mode
    And checkpoint was saved during route execution
    When navigation resumes from checkpoint
    Then flight mode should be CRUISE (not DRIFT)
    And ship should navigate to refuel waypoint efficiently

  @xfail
  Scenario: Checkpoint resume respects original flight mode plan
    Given a route was planned with specific flight modes
    And checkpoint contains the planned route
    When navigation resumes from checkpoint
    Then flight mode should match the original route plan
    And mode should NOT be re-selected based on current fuel

  @xfail
  Scenario: Checkpoint resume with insufficient fuel uses DRIFT
    Given a ship with very low fuel at checkpoint
    And refuel waypoint is nearby
    And insufficient fuel for CRUISE
    When navigation resumes from checkpoint
    Then DRIFT mode should be used to reach fuel station
    And this is an acceptable emergency scenario

  @xfail
  Scenario: Normal navigation path uses CRUISE correctly
    Given a ship navigating without checkpoint resume
    And fuel is adequate for CRUISE
    When flight mode is selected
    Then CRUISE should be chosen for efficiency
    And selection logic should be consistent

  @xfail
  Scenario: Stale checkpoint with location mismatch is detected
    Given a checkpoint exists for a ship
    And ship's actual location differs from checkpoint location
    When attempting to resume from checkpoint
    Then checkpoint should be rejected as stale
    And navigation should re-plan from actual location

  @xfail
  Scenario: Stale checkpoint with destination mismatch is detected
    Given a checkpoint exists for a ship
    And current destination differs from checkpoint destination
    When attempting to resume from checkpoint
    Then checkpoint should be rejected as stale
    And new route should be planned for current destination

  @xfail
  Scenario: Valid checkpoint with matching location is accepted
    Given a checkpoint exists for a ship
    And ship location matches checkpoint location
    And destination matches checkpoint destination
    When attempting to resume from checkpoint
    Then checkpoint should be accepted as valid
    And navigation should resume from checkpoint step

  @xfail
  Scenario: Checkpoint resume prefers CRUISE over DRIFT
    Given a checkpoint with a refuel step
    And ship has adequate fuel for CRUISE to refuel waypoint
    When resuming navigation from checkpoint
    Then CRUISE mode should be preferred
    And DRIFT should only be used if fuel is insufficient
