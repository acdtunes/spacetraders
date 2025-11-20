Feature: Container ID Generation
  As a system administrator
  I want container IDs to be standardized, readable, and unique
  So that I can easily identify and track containers

  Background:
    Given the container ID generator is available

  Scenario: Generate container ID with standard ship symbol
    When I generate a container ID with operation "navigate" and ship "AGENT-SCOUT-1"
    Then the container ID should match the pattern "navigate-SCOUT-1-[a-f0-9]{8}"
    And the container ID should be shorter than 30 characters

  Scenario: Generate container ID with multi-part agent prefix
    When I generate a container ID with operation "mining-worker" and ship "MY-AGENT-MINER-2"
    Then the container ID should match the pattern "mining-worker-MINER-2-[a-f0-9]{8}"
    And the agent prefix "MY-AGENT-" should be stripped from the ship symbol

  Scenario: Generate container ID with ship symbol without agent prefix
    When I generate a container ID with operation "dock" and ship "SCOUT-1"
    Then the container ID should match the pattern "dock-SCOUT-1-[a-f0-9]{8}"
    And the ship symbol should remain unchanged

  Scenario: Generate container ID with single-part ship symbol
    When I generate a container ID with operation "refuel" and ship "SCOUT"
    Then the container ID should match the pattern "refuel-SCOUT-[a-f0-9]{8}"
    And the ship symbol should remain unchanged

  Scenario: Container IDs are unique
    When I generate 100 container IDs with operation "test" and ship "AGENT-SHIP-1"
    Then all container IDs should be unique

  Scenario: Container ID format consistency across operation types
    When I generate container IDs for the following operations and ships:
      | operation               | ship              |
      | navigate                | AGENT-SCOUT-1     |
      | dock                    | AGENT-SCOUT-1     |
      | orbit                   | AGENT-MINER-2     |
      | refuel                  | AGENT-HAULER-3    |
      | mining-worker           | AGENT-MINER-1     |
      | transport-worker        | AGENT-HAULER-1    |
      | contract-work           | AGENT-SCOUT-2     |
      | scout-tour              | AGENT-SCOUT-3     |
      | purchase_ship           | AGENT-COMMAND-1   |
      | contract_fleet_coordinator | AGENT-HAULER-2 |
    Then all container IDs should match their respective patterns
    And all container IDs should contain their operation names
    And all container IDs should have 8-character hex UUID suffixes

  Scenario: Container ID is shorter than legacy formats
    When I generate a container ID with operation "navigate" and ship "AGENT-SCOUT-1"
    Then the container ID should be at least 30% shorter than legacy format "navigate-AGENT-SCOUT-1-1234567890123456789"

  Scenario Outline: Agent prefix stripping with various formats
    When I generate a container ID with operation "test" and ship "<ship_symbol>"
    Then the container ID should match the pattern "test-<expected_ship>-[a-f0-9]{8}"

    Examples:
      | ship_symbol          | expected_ship   |
      | AGENT-SCOUT-1        | SCOUT-1         |
      | MY-AGENT-MINER-2     | MINER-2         |
      | SOME-AGENT-HAULER-3  | HAULER-3        |
      | SCOUT-1              | SCOUT-1         |
      | SINGLE               | SINGLE          |
      | A-B-C-D              | C-D             |

  Scenario: UUID suffix is exactly 8 hexadecimal characters
    When I generate a container ID with operation "test" and ship "AGENT-SHIP-1"
    Then the UUID suffix should be exactly 8 characters long
    And the UUID suffix should only contain hexadecimal characters [a-f0-9]
