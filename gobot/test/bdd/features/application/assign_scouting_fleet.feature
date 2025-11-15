Feature: Assign Scouting Fleet
  As a space trader
  I want to automatically assign all probe and satellite ships to market scouting operations
  So that I can efficiently gather market data across all non-fuel-station marketplaces

  Background:
    Given a mediator is configured with scouting handlers
    And a player with ID 1 exists with agent "TEST-AGENT"
    And a system "X1-TEST" exists with multiple waypoints

  Scenario: Assign all probe ships to scout markets excluding fuel stations
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
      | HAULER-1   | FRAME_HEAVY    | X1-TEST-C1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
      | X1-TEST-C1   | GAS_GIANT       | FUEL_STATION      |
      | X1-TEST-D1   | ASTEROID        | MARKETPLACE       |
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should assign 2 ships to scouting
    And ship "PROBE-1" should be assigned to scout markets
    And ship "PROBE-2" should be assigned to scout markets
    And ship "HAULER-1" should not be assigned
    And the markets assigned should exclude "X1-TEST-C1"
    And the markets assigned should include "X1-TEST-A1"
    And the markets assigned should include "X1-TEST-B1"
    And the markets assigned should include "X1-TEST-D1"

  Scenario: Assign probe and drone ships together
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol      | frame_type      | location    |
      | PROBE-1     | FRAME_PROBE     | X1-TEST-A1  |
      | DRONE-1     | FRAME_DRONE     | X1-TEST-B1  |
      | EXPLORER-1  | FRAME_EXPLORER  | X1-TEST-C1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should assign 2 ships to scouting
    And ship "PROBE-1" should be assigned to scout markets
    And ship "DRONE-1" should be assigned to scout markets
    And ship "EXPLORER-1" should not be assigned

  Scenario: No probe or satellite ships available
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | HAULER-1   | FRAME_HEAVY    | X1-TEST-A1  |
      | MINER-1    | FRAME_MINER    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should fail with error "no probe or satellite ships found"
    And no ships should be assigned to scouting

  Scenario: Only fuel stations available (no markets to scout)
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | GAS_GIANT       | FUEL_STATION      |
      | X1-TEST-B1   | GAS_GIANT       | FUEL_STATION      |
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should fail with error "no non-fuel-station marketplaces found"
    And no ships should be assigned to scouting

  Scenario: Reuse existing scout containers (idempotency)
    Given the following ships owned by player 1 in system "X1-TEST":
      | symbol     | frame_type     | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-TEST-B1  |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
      | X1-TEST-B1   | MOON            | MARKETPLACE       |
    And ship "PROBE-1" already has a running scout-tour container
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should assign 2 ships to scouting
    And ship "PROBE-1" container should be reused
    And ship "PROBE-2" should get a new scout-tour container
    And 1 container should be marked as reused

  Scenario: Filter ships to only those in the specified system
    Given the following ships owned by player 1:
      | symbol     | frame_type     | system      | location    |
      | PROBE-1    | FRAME_PROBE    | X1-TEST     | X1-TEST-A1  |
      | PROBE-2    | FRAME_PROBE    | X1-OTHER    | X1-OTHER-Z1 |
    And the following waypoints with marketplaces in system "X1-TEST":
      | symbol       | type            | traits            |
      | X1-TEST-A1   | PLANET          | MARKETPLACE       |
    When I execute the assign scouting fleet command with:
      | player_id     | 1        |
      | system_symbol | X1-TEST  |
    Then the command should assign 1 ship to scouting
    And ship "PROBE-1" should be assigned to scout markets
    And ship "PROBE-2" should not be assigned
