Feature: Set Flight Mode Command
  As a SpaceTraders bot
  I want to change ship flight modes
  So that I can optimize travel time and fuel consumption

  Background:
    Given a ship operations test player 1 exists with agent "TEST-AGENT" and token "test-token-123"

  Scenario: Successfully set flight mode to CRUISE
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "CRUISE"
    Then the flight mode command should succeed with status "success"
    And the current flight mode should be "CRUISE"

  Scenario: Successfully set flight mode to DRIFT
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "DRIFT"
    Then the flight mode command should succeed with status "success"
    And the current flight mode should be "DRIFT"

  Scenario: Successfully set flight mode to BURN
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "BURN"
    Then the flight mode command should succeed with status "success"
    And the current flight mode should be "BURN"

  Scenario: Successfully set flight mode to STEALTH
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "STEALTH"
    Then the flight mode command should succeed with status "success"
    And the current flight mode should be "STEALTH"

  Scenario: Ship not found
    When I execute SetFlightModeCommand for ship "NONEXISTENT" and player 1 with mode "CRUISE"
    Then the flight mode command should fail with error "ship not found"

  Scenario: Invalid flight mode
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "INVALID"
    Then the flight mode command should fail with error "invalid flight mode: UNKNOWN"

  Scenario: Cannot set flight mode for ship belonging to different player
    Given a ship "SHIP-1" for player 2 at "X1-A1" with status "IN_ORBIT"
    When I execute SetFlightModeCommand for ship "SHIP-1" and player 1 with mode "CRUISE"
    Then the flight mode command should fail with error "ship not found"
