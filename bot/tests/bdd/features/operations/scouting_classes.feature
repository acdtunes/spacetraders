Feature: Scouting module classes
  As a developer
  I want each scouting class to be independently testable
  So that I can ensure the refactored architecture works correctly

  Background:
    Given a scouting test environment

  # MarketDataService Tests
  Scenario: MarketDataService collects market data successfully
    Given a market data service
    And market "X1-TEST-A1" has 5 trade goods
    When I collect market data for "X1-TEST-A1"
    Then market data should be returned
    And market data should contain 5 trade goods

  Scenario: MarketDataService handles API failure
    Given a market data service
    And market "X1-FAIL-A1" API returns error
    When I collect market data for "X1-FAIL-A1"
    Then market data should be None

  Scenario: MarketDataService updates database with correct field mapping
    Given a market data service
    And trade good "IRON_ORE" has API purchasePrice 150 and sellPrice 100
    When I update database with trade good data
    Then database should have sell_price 150
    And database should have purchase_price 100

  Scenario: MarketDataService collects and updates in one call
    Given a market data service
    And market "X1-TEST-B2" has 3 trade goods
    When I collect and update for "X1-TEST-B2"
    Then 3 goods should be updated in database

  # StationaryScoutMode Tests
  Scenario: StationaryScoutMode navigates once and polls
    Given a stationary scout mode
    And ship "SHIP-1" is at "X1-TEST-A1"
    And market "X1-TEST-B2" has trade goods
    When I execute stationary mode for "X1-TEST-B2" non-continuous
    Then ship should navigate to "X1-TEST-B2"
    And ship should dock at market
    And market should be polled 1 time
    And result should be successful

  Scenario: StationaryScoutMode handles navigation failure
    Given a stationary scout mode
    And ship "SHIP-1" is at "X1-TEST-A1"
    And navigation to "X1-FAIL-B2" will fail
    When I execute stationary mode for "X1-FAIL-B2" non-continuous
    Then result should be failure
    And error message should mention navigation failed

  Scenario: StationaryScoutMode polls continuously until stopped
    Given a stationary scout mode
    And ship "SHIP-1" is at "X1-TEST-A1"
    And market "X1-TEST-A1" has trade goods
    When I execute stationary mode for "X1-TEST-A1" continuous with 3 polls
    Then market should be polled 3 times
    And result should be successful

  # TourScoutMode Tests
  Scenario: TourScoutMode plans tour successfully
    Given a tour scout mode
    And markets "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3" exist
    When I plan tour from "X1-TEST-A1"
    Then tour should be returned
    And tour should have 2 legs

  Scenario: TourScoutMode handles tour planning failure
    Given a tour scout mode
    And tour planning will fail
    When I plan tour from "X1-TEST-A1"
    Then tour should be None

  Scenario: TourScoutMode executes tour and collects data
    Given a tour scout mode
    And planned tour has 3 waypoints
    And each waypoint has 5 trade goods
    When I execute planned tour
    Then 3 markets should be visited
    And 15 goods should be updated
    And result should be successful

  Scenario: TourScoutMode saves tour to file
    Given a tour scout mode
    And planned tour exists
    When I save tour to "routes/test_tour.json"
    Then tour file should be created
    And tour file should contain JSON data

  # ScoutMarketsExecutor Tests
  Scenario: ScoutMarketsExecutor setup succeeds
    Given a scout markets executor
    And system "X1-TEST" has graph in database
    And ship "SHIP-1" exists
    When I call setup
    Then setup should return True
    And ship controller should be initialized
    And navigator should be initialized
    And market service should be initialized

  Scenario: ScoutMarketsExecutor handles graph build failure
    Given a scout markets executor
    And system "X1-FAIL" has no graph
    And graph building will fail
    When I call setup
    Then setup should return False

  Scenario: ScoutMarketsExecutor handles ship data failure
    Given a scout markets executor
    And system "X1-TEST" has graph in database
    And ship "SHIP-FAIL" does not exist
    When I call setup
    Then setup should return False

  Scenario: ScoutMarketsExecutor determines partitioned markets
    Given a scout markets executor with partitioned markets "X1-TEST-A1,X1-TEST-B2"
    When I determine markets
    Then tour start should be "X1-TEST-A1"
    And market stops should be "X1-TEST-A1,X1-TEST-B2"

  Scenario: ScoutMarketsExecutor determines auto-discover markets
    Given a scout markets executor
    And system "X1-TEST" has 5 markets
    And ship "SHIP-1" is at "X1-TEST-M1"
    When I determine markets
    Then tour start should be "X1-TEST-M1"
    And market stops should exclude current location
    And market stops should have 4 markets

  Scenario: ScoutMarketsExecutor runs stationary mode
    Given a scout markets executor
    And markets list is "X1-TEST-B2"
    When I run single tour
    Then stationary mode should execute
    And result should be True

  Scenario: ScoutMarketsExecutor runs tour mode
    Given a scout markets executor
    And markets list is "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3"
    When I run single tour
    Then tour mode should execute
    And result should be True
