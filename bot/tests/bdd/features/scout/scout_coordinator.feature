Feature: Scout Coordinator - Multi-ship Market Scouting

  Background:
    Given a mock API client
    And a test token "test-token-123"

  # ===== INITIALIZATION & GRAPH LOADING =====

  Scenario: Load existing graph from file
    Given a system "X1-TEST" with graph file
    When I initialize a scout coordinator for system "X1-TEST" with ships "SHIP-1,SHIP-2"
    Then the coordinator should load the graph from file
    And the coordinator should extract markets from the graph
    And markets should include "X1-TEST-M1,X1-TEST-M2,X1-TEST-M3"

  Scenario: Build new graph when file does not exist
    Given a system "X1-TEST" without graph file
    And the API has waypoints for system "X1-TEST"
    When I initialize a scout coordinator for system "X1-TEST" with ships "SHIP-1"
    Then the coordinator should build a new graph
    And the graph should be saved to file
    And markets should be extracted from the built graph

  Scenario: Fail when graph cannot be built
    Given a system "X1-TEST" without graph file
    And the API returns empty waypoints for system "X1-TEST"
    When I attempt to initialize a scout coordinator for system "X1-TEST" with ships "SHIP-1"
    Then initialization should fail with error "Failed to build graph"

  Scenario: Extract markets from graph
    Given a system graph with 5 waypoints including 3 markets
    When I extract markets from the graph
    Then I should get 3 markets
    And each market should have MARKETPLACE trait

  # ===== MARKET PARTITIONING =====

  Scenario: Single ship gets all markets
    Given a system "X1-TEST" with 10 markets
    And a scout coordinator with 1 ship
    When markets are partitioned
    Then ship "SHIP-1" should be assigned all 10 markets

  Scenario: Two ships partition markets geographically
    Given a system "X1-TEST" with markets spread horizontally
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then markets should be split into 2 groups
    And each ship should get approximately half the markets

  Scenario: Vertical partitioning for wide systems
    Given a system with markets at coordinates:
      | symbol      | x   | y   |
      | X1-TEST-M1  | 0   | 50  |
      | X1-TEST-M2  | 100 | 50  |
      | X1-TEST-M3  | 200 | 50  |
      | X1-TEST-M4  | 300 | 50  |
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then markets should be split vertically by X coordinate
    And ship "SHIP-1" should get markets with x < 150
    And ship "SHIP-2" should get markets with x >= 150

  Scenario: Horizontal partitioning for tall systems
    Given a system with markets at coordinates:
      | symbol      | x   | y   |
      | X1-TEST-M1  | 50  | 0   |
      | X1-TEST-M2  | 50  | 100 |
      | X1-TEST-M3  | 50  | 200 |
      | X1-TEST-M4  | 50  | 300 |
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then markets should be split horizontally by Y coordinate
    And ship "SHIP-1" should get markets with y < 150
    And ship "SHIP-2" should get markets with y >= 150

  Scenario: Three ships partition markets evenly
    Given a system with 9 markets spread evenly
    And a scout coordinator with 3 ships
    When markets are partitioned
    Then each ship should get 3 markets
    And partitions should not overlap

  Scenario: Uneven market distribution
    Given a system with 10 markets spread horizontally
    And a scout coordinator with 3 ships
    When markets are partitioned
    Then ship "SHIP-1" should get 3 markets
    And ship "SHIP-2" should get 3 markets
    And ship "SHIP-3" should get 4 markets
    And all markets should be assigned

  Scenario: Empty markets list
    Given a system with 0 markets
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then each ship should get 0 markets
    And no assignments should be created

  Scenario: Handle markets with identical positions
    Given a system with markets at same coordinates
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then all markets should be assigned
    And partitioning should not fail

  # ===== SUBTOUR OPTIMIZATION =====

  Scenario: Optimize subtour with 2opt algorithm
    Given a ship "SHIP-1" at "X1-TEST-M1"
    And markets "X1-TEST-M1,X1-TEST-M2,X1-TEST-M3,X1-TEST-M4"
    And algorithm "2opt"
    When I optimize subtour for "SHIP-1"
    Then the tour should visit all 4 markets
    And the tour should return to start
    And the tour should be optimized with 2opt

  Scenario: Optimize subtour with greedy algorithm
    Given a ship "SHIP-1" at "X1-TEST-M1"
    And markets "X1-TEST-M1,X1-TEST-M2,X1-TEST-M3"
    And algorithm "greedy"
    When I optimize subtour for "SHIP-1"
    Then the tour should visit all 3 markets
    And the tour should return to start
    And the tour should use greedy nearest neighbor

  Scenario: Optimize empty subtour
    Given a ship "SHIP-1" at "X1-TEST-M1"
    And markets ""
    When I optimize subtour for "SHIP-1"
    Then the optimization should return None

  Scenario: Handle missing ship data during optimization
    Given a ship "SHIP-INVALID" that does not exist
    And markets "X1-TEST-M1,X1-TEST-M2"
    When I optimize subtour for "SHIP-INVALID"
    Then the optimization should fail
    And the optimization should return None

  Scenario: Return to start for continuous loop
    Given a ship "SHIP-1" at "X1-TEST-M1"
    And markets "X1-TEST-M1,X1-TEST-M2,X1-TEST-M3"
    When I optimize subtour for "SHIP-1"
    Then the tour should end at "X1-TEST-M1"
    And the tour should be continuous loop

  # ===== DAEMON MANAGEMENT =====

  Scenario: Start scout daemon for ship
    Given a scout coordinator with ship "SHIP-1"
    And markets "X1-TEST-M1,X1-TEST-M2"
    When I start scout daemon for "SHIP-1"
    Then the daemon should start successfully
    And the daemon ID should be "scout-1"
    And the daemon command should include "scout-markets"
    And the daemon command should include "--continuous"

  Scenario: Start daemons for multiple ships
    Given a scout coordinator with ships "SHIP-1,SHIP-2"
    And partitioned markets
    When I partition and start all scouts
    Then daemon "scout-1" should be running
    And daemon "scout-2" should be running
    And each ship should have unique daemon ID

  Scenario: Monitor running daemons
    Given scout daemons are running
    When I monitor daemons for 60 seconds
    Then all daemons should remain running

  Scenario: Auto-restart failed daemon
    Given a running scout daemon "scout-1"
    When the daemon stops unexpectedly
    And the monitor checks daemon status
    Then the daemon should be restarted automatically
    And the new daemon should use same markets

  Scenario: Handle daemon start failure
    Given a scout coordinator with ship "SHIP-1"
    And the daemon manager will fail to start
    When I start scout daemon for "SHIP-1"
    Then the daemon start should fail
    And no assignment should be created

  Scenario: Stop all daemons on shutdown
    Given running scout daemons for ships "SHIP-1,SHIP-2"
    When I stop all scouts
    Then all daemons should be stopped
    And no daemons should be running

  # ===== RECONFIGURATION =====

  Scenario: Detect reconfigure signal from config file
    Given a running scout coordinator
    And a config file with reconfigure flag set
    When the monitor checks for reconfiguration
    Then reconfiguration should be detected
    And the reconfigure handler should be called

  Scenario: Add ships gracefully
    Given a running scout coordinator with ships "SHIP-1"
    And markets are partitioned for 1 ship
    When I add ship "SHIP-2" via config file
    And reconfiguration is triggered
    Then markets should be repartitioned for 2 ships
    And daemon "scout-1" should be running
    And daemon "scout-2" should be running

  Scenario: Remove ships gracefully
    Given a running scout coordinator with ships "SHIP-1,SHIP-2"
    And markets are partitioned for 2 ships
    When I remove ship "SHIP-2" via config file
    And reconfiguration is triggered
    Then daemon "scout-2" should be stopped
    And markets should be repartitioned for 1 ship
    And ship "SHIP-1" should get all markets

  Scenario: No-op when no changes detected
    Given a running scout coordinator with ships "SHIP-1,SHIP-2"
    And a config file with same ships
    When reconfiguration is triggered
    Then the reconfigure should be skipped
    And the reconfigure flag should be cleared
    And no daemons should be restarted

  Scenario: Wait for tours to complete before reconfiguring
    Given a running scout coordinator with long tours
    When reconfiguration is requested
    Then the wait should timeout after 300 seconds if tours do not complete

  Scenario: Handle reconfiguration timeout
    Given a running scout coordinator with infinite tours
    When reconfiguration is requested
    And tours do not complete within timeout
    Then the timeout warning should be logged
    And reconfiguration should proceed anyway

  # ===== CONFIGURATION PERSISTENCE =====

  Scenario: Save configuration to file
    Given a scout coordinator with ships "SHIP-1,SHIP-2"
    And algorithm "2opt"
    When I save the configuration
    Then the config file should be created
    And the config should include system "X1-TEST"
    And the config should include ships "SHIP-1,SHIP-2"
    And the config should include algorithm "2opt"
    And the reconfigure flag should be false

  Scenario: Load configuration from file
    Given a config file exists with ships "SHIP-1,SHIP-2,SHIP-3"
    When I load the configuration
    Then the loaded ships should be "SHIP-1,SHIP-2,SHIP-3"
    And the reconfigure flag should be available

  Scenario: Update reconfigure flag in config
    Given a config file exists
    When I set the reconfigure flag to true
    Then the config file should have reconfigure=true
    And the next check should detect reconfiguration

  Scenario: Create config directory if missing
    Given no config directory exists
    When I save the configuration
    Then the config directory should be created
    And the config file should be saved successfully

  # ===== SIGNAL HANDLING =====

  Scenario: Handle SIGTERM gracefully
    Given a running scout coordinator
    When SIGTERM is received
    Then the running flag should be set to false
    And monitoring should stop
    And a shutdown message should be printed

  Scenario: Handle SIGINT gracefully
    Given a running scout coordinator
    When SIGINT is received
    Then the running flag should be set to false
    And monitoring should stop gracefully

  # ===== EDGE CASES =====

  Scenario: Single market in system
    Given a system with 1 market
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then ship "SHIP-1" should get 1 market
    And ship "SHIP-2" should get 0 markets

  Scenario: More ships than markets
    Given a system with 2 markets
    And a scout coordinator with 5 ships
    When markets are partitioned
    Then 2 ships should get 1 market each
    And 3 ships should get 0 markets

  Scenario: Very small coordinate range
    Given a system with markets in 1x1 area
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then partitioning should not divide by zero
    And all markets should be assigned

  Scenario: Negative coordinates
    Given a system with markets at negative coordinates
    And a scout coordinator with 2 ships
    When markets are partitioned
    Then partitioning should handle negative values correctly
    And all markets should be assigned
