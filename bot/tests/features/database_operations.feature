Feature: Database Operations
  As a database administrator
  I want to manage players, markets, transactions, and graphs
  So that I can store and retrieve game data efficiently

  Background:
    Given the database is initialized with a temporary path

  Scenario: Create and retrieve player
    When I create a player with agent symbol "CMDR_AC_2025" and token "test-token-123"
    Then the player should exist in the database
    When I get the player by agent symbol "CMDR_AC_2025"
    Then the player's agent symbol should be "CMDR_AC_2025"
    And the player's token should be "test-token-123"

  Scenario: Update player activity timestamp
    Given a player "CMDR_AC_2025" exists
    When I get the player by agent symbol "CMDR_AC_2025"
    And I update the player's activity timestamp
    Then the player's last_active should be recent

  Scenario: List all players
    Given a player "CMDR_AC_2025" exists
    And a player "TRADER_01" exists
    And a player "MINER_02" exists
    When I list all players
    Then I should see 3 players
    And the players list should include "CMDR_AC_2025"
    And the players list should include "TRADER_01"
    And the players list should include "MINER_02"

  Scenario: Record market transaction for player
    Given a player "CMDR_AC_2025" exists
    When I record a SELL transaction for player "CMDR_AC_2025" ship "SHIP-1" at "X1-HU87-A1" good "IRON_ORE" units 50 price 70 total 3500
    Then the transaction should be recorded
    And I should be able to retrieve 1 transaction for player "CMDR_AC_2025"

  Scenario: Update market data shared across players
    Given a player "CMDR_AC_2025" exists
    And a player "TRADER_01" exists
    When player "CMDR_AC_2025" updates market data for waypoint "X1-HU87-A1" and good "IRON_ORE" supply "MODERATE" activity "STRONG" buy 60 sell 70 volume 100
    Then the market data should be visible to player "TRADER_01"
    And the market data should show purchase_price 60
    And the market data should show sell_price 70

  Scenario: Get market data with filters
    Given a player "CMDR_AC_2025" exists
    And market data exists for waypoint "X1-HU87-A1" with goods "IRON_ORE,COPPER_ORE,ALUMINUM_ORE"
    When I get market data for waypoint "X1-HU87-A1"
    Then I should see 3 goods in the market data
    When I get market data for waypoint "X1-HU87-A1" and good "IRON_ORE"
    Then I should see 1 good in the market data
    And the good should be "IRON_ORE"

  Scenario: Query transaction history with filters
    Given a player "CMDR_AC_2025" exists
    And transaction 1 exists: player "CMDR_AC_2025" ship "SHIP-1" at "X1-HU87-A1" good "IRON_ORE" SELL 50 units
    And transaction 2 exists: player "CMDR_AC_2025" ship "SHIP-1" at "X1-HU87-A1" good "COPPER_ORE" SELL 30 units
    And transaction 3 exists: player "CMDR_AC_2025" ship "SHIP-2" at "X1-HU87-B2" good "IRON_ORE" BUY 20 units
    And transaction 4 exists: player "CMDR_AC_2025" ship "SHIP-1" at "X1-HU87-A1" good "ALUMINUM_ORE" SELL 40 units
    When I get transactions for player "CMDR_AC_2025" filtered by ship "SHIP-1"
    Then I should see 3 transactions
    When I get transactions for player "CMDR_AC_2025" filtered by waypoint "X1-HU87-A1"
    Then I should see 3 transactions
    When I get transactions for player "CMDR_AC_2025" filtered by good "IRON_ORE"
    Then I should see 2 transactions
    When I get transactions for player "CMDR_AC_2025" with no filters
    Then I should see 4 transactions

  Scenario: Save and retrieve graph edges
    Given a system graph "X1-HU87" with 3 waypoints and 2 edges
    When I retrieve the system graph for "X1-HU87"
    Then the graph should have 3 waypoints
    And the graph should have 2 edges

  Scenario: List systems with graphs
    Given a system graph exists for "X1-HU87"
    And a system graph exists for "X1-JB59"
    And a system graph exists for "X1-MM38"
    When I list all systems with graphs
    Then I should see 3 systems
    And the systems list should include "X1-HU87"
    And the systems list should include "X1-JB59"
    And the systems list should include "X1-MM38"

  Scenario: Find fuel stations in system
    Given a system graph exists for "X1-HU87" with fuel waypoints "X1-HU87-A1,X1-HU87-C3"
    When I find fuel stations in system "X1-HU87"
    Then I should see 2 fuel stations
    And the fuel stations should include "X1-HU87-A1"
    And the fuel stations should include "X1-HU87-C3"

  Scenario: Multi-player transaction isolation
    Given a player "CMDR_AC_2025" exists
    And a player "TRADER_01" exists
    When I record a SELL transaction for player "CMDR_AC_2025" ship "SHIP-1" at "X1-HU87-A1" good "IRON_ORE" units 50 price 70 total 3500
    And I record a BUY transaction for player "TRADER_01" ship "SHIP-A" at "X1-HU87-B2" good "COPPER_ORE" units 30 price 65 total 1950
    Then player "CMDR_AC_2025" should have 1 transaction
    And player "TRADER_01" should have 1 transaction
    And player "CMDR_AC_2025" transactions should not include "TRADER_01" transactions

  Scenario: Create player with metadata
    When I create a player "CMDR_AC_2025" with metadata {"faction": "COSMIC", "starting_system": "X1-HU87"}
    Then the player should exist
    And the player metadata should include "faction" as "COSMIC"
    And the player metadata should include "starting_system" as "X1-HU87"

  Scenario: Get player by ID
    Given a player "CMDR_AC_2025" exists
    When I get the player by agent symbol "CMDR_AC_2025"
    Then I should have the player's ID
    When I get the player by that ID
    Then the player's agent symbol should be "CMDR_AC_2025"

  Scenario: List ship assignments filtered by status
    Given a player "CMDR_AC_2025" exists
    And ship "SHIP-1" is assigned to "trading_operator" for player "CMDR_AC_2025"
    And ship "SHIP-2" is idle for player "CMDR_AC_2025"
    And ship "SHIP-3" is assigned to "mining_operator" for player "CMDR_AC_2025"
    When I list ship assignments for player "CMDR_AC_2025" with status "active"
    Then I should see 2 ship assignments
    When I list ship assignments for player "CMDR_AC_2025" with status "idle"
    Then I should see 1 ship assignment

  Scenario: Find available ships for player
    Given a player "CMDR_AC_2025" exists
    And ship "SHIP-1" is assigned to "trading_operator" for player "CMDR_AC_2025"
    And ship "SHIP-2" is idle for player "CMDR_AC_2025"
    And ship "SHIP-3" is idle for player "CMDR_AC_2025"
    When I find available ships for player "CMDR_AC_2025"
    Then I should see 2 available ships
    And the available ships should include "SHIP-2"
    And the available ships should include "SHIP-3"

  Scenario: Reassign ship that is already assigned
    Given a player "CMDR_AC_2025" exists
    And ship "SHIP-1" is assigned to "trading_operator" for player "CMDR_AC_2025"
    When I try to assign "SHIP-1" to "mining_operator" for player "CMDR_AC_2025"
    Then the assignment should fail
    And the ship should still be assigned to "trading_operator"

  Scenario: Get ship assignment with metadata
    Given a player "CMDR_AC_2025" exists
    When I assign ship "SHIP-1" to "trading_operator" for player "CMDR_AC_2025" with metadata {"route": "TRADE_ROUTE_1", "duration": 3600}
    Then the ship assignment should have metadata key "route" with value "TRADE_ROUTE_1"
    And the ship assignment should have metadata key "duration" with value 3600

  Scenario: Get nonexistent player by ID
    When I try to get player by ID 999
    Then the result should be None

  Scenario: Create daemon for player
    Given a player "CMDR_AC_2025" exists
    When I create daemon "mining-daemon-1" for player "CMDR_AC_2025" with PID 12345 command "python3 miner.py"
    Then the daemon should be created successfully
    And I should be able to retrieve daemon "mining-daemon-1" for player "CMDR_AC_2025"

  Scenario: Update daemon status
    Given a player "CMDR_AC_2025" exists
    And daemon "test-daemon" exists for player "CMDR_AC_2025"
    When I update daemon "test-daemon" status to "stopped" for player "CMDR_AC_2025"
    Then daemon "test-daemon" should have status "stopped"

  Scenario: List daemons filtered by status
    Given a player "CMDR_AC_2025" exists
    And daemon "daemon-1" with status "running" exists for player "CMDR_AC_2025"
    And daemon "daemon-2" with status "running" exists for player "CMDR_AC_2025"
    And daemon "daemon-3" with status "stopped" exists for player "CMDR_AC_2025"
    When I list daemons for player "CMDR_AC_2025" with status "running"
    Then I should see 2 daemons

  Scenario: Delete daemon
    Given a player "CMDR_AC_2025" exists
    And daemon "temp-daemon" exists for player "CMDR_AC_2025"
    When I delete daemon "temp-daemon" for player "CMDR_AC_2025"
    Then the daemon should be deleted successfully
