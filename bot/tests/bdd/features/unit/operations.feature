Feature: Operations Layer Components
  Tests for operation modules: mining, assignments, contracts, fleet, navigation

  # Mining Operation Tests
  Scenario: Mining operation fails without ship status
    Given a mining operation setup
    And a ship controller that returns None for status
    When I execute mining operation
    Then the operation should fail with exit code 1
    And a critical error should be logged

  Scenario: Mining operation fails on route validation
    Given a mining operation setup
    And a ship with valid status
    And route validation fails with "No path"
    When I execute mining operation
    Then the operation should fail with exit code 1
    And a critical error should be logged

  Scenario: Targeted mining succeeds after wrong cargo
    Given a ship targeting "ALUMINUM_ORE"
    And initial extractions yield wrong cargo then correct cargo
    When I execute targeted mining for 5 units
    Then mining should succeed
    And 5 units should be mined
    And wrong cargo should be jettisoned

  Scenario: Mining fails if navigation to asteroid fails
    Given a ship with valid configuration
    And navigator that always fails navigation
    When I execute targeted mining
    Then mining should fail
    And reason should be "Navigation to asteroid failed"

  Scenario: Mining operation completes full success path
    Given a mining operation setup
    And valid ship, navigator, and controller
    When I execute mining operation for 1 cycle
    Then operation should succeed with exit code 0
    And ship should navigate to asteroid and market
    And revenue should be recorded
    And operation should be marked complete

  Scenario: Find alternative asteroids excludes stripped
    Given a system with multiple asteroids
    And some asteroids are stripped
    When I search for alternative asteroids
    Then stripped asteroids should be excluded
    And valid asteroids should be included

  # Assignment Operations Tests
  Scenario: List all ship assignments
    Given multiple ships with assignments
    When I list assignments
    Then all assigned ships should be shown

  Scenario: Release ship assignment
    Given an assigned ship
    When I release the assignment
    Then ship should become unassigned

  Scenario: Find available ships by criteria
    Given ships with various cargo capacities
    When I search for ships with cargo minimum 40
    Then only ships meeting criteria should be returned

  # Contract Operations Tests
  Scenario: Evaluate contract profitability
    Given a contract with payment terms
    And resource costs
    When I evaluate contract
    Then net profit should be calculated correctly
    And ROI should be computed

  Scenario: Negotiate contract terms
    Given contract negotiation parameters
    When I negotiate contract
    Then terms should be within acceptable range

  # Fleet Operations Tests
  Scenario: Display fleet status summary
    Given multiple ships in various states
    When I query fleet status
    Then status summary should show all ships
    And states should be accurate

  # Navigation Operations Tests
  Scenario: Navigate ship with SmartNavigator
    Given a ship at starting waypoint
    And a valid destination
    When I execute navigation operation
    Then ship should reach destination
    And fuel should be managed automatically

  # Captain Logging Tests
  Scenario: Initialize captain log
    Given a new agent
    When I initialize captain log
    Then log file should be created
    And header should contain agent info

  Scenario: Start logging session
    Given an initialized captain log
    When I start a session with objective
    Then session state should be saved
    And session ID should be generated

  Scenario: Log operation entry
    Given an active session
    When I log an operation started event
    Then event should be appended to log
    And session operations should be updated

  Scenario: Log entry requires narrative for OPERATION_COMPLETED
    Given an active session
    When I log OPERATION_COMPLETED without narrative
    Then entry should be skipped
    And warning should be shown

  Scenario: Scout operations are ignored in logging
    Given an active session
    When I log a scout operation event
    Then entry should be skipped
    And info message should be shown

  Scenario: Session end archives session
    Given an active session with operations
    When I end the session
    Then session should be archived to JSON
    And current session should be cleared
    And net profit should be calculated

  Scenario: Search logs by tag and timeframe
    Given a captain log with entries
    And entries tagged with "mining"
    When I search logs for tag "mining" within 2 hours
    Then matching entries should be returned

  Scenario: Generate executive report
    Given archived sessions with profit data
    When I generate 24-hour executive report
    Then report should show total profit
    And performance metrics should be included

  # Daemon Operations Tests
  Scenario: Start daemon process
    Given daemon configuration
    When I start daemon
    Then process should spawn
    And PID should be recorded

  Scenario: Stop daemon gracefully
    Given a running daemon
    When I stop daemon
    Then process should terminate cleanly

  # Routing Operations Tests
  Scenario: Build system graph
    Given waypoints in a system
    When I build graph
    Then all waypoints should be nodes
    And edges should have distances

  Scenario: Plan route between waypoints
    Given a system graph
    And start and end waypoints
    When I plan route
    Then route should be calculated
    And fuel requirements should be estimated

  # Control Primitives Tests
  Scenario: Operation controller manages lifecycle
    Given an operation controller
    When I start operation
    Then state should be "running"

  Scenario: Operation can be paused
    Given a running operation
    When I pause operation
    Then state should be "paused"

  Scenario: Operation can be canceled
    Given a running operation
    When I cancel operation
    Then state should be "canceled"

  # Common Utilities Tests
  Scenario: Setup logging creates log file
    Given logging configuration
    When I setup logging
    Then log file should be created

  Scenario: Humanize duration formats time
    Given a duration in seconds
    When I humanize the duration
    Then output should be readable format

  # Analysis Operations Tests
  Scenario: Analyze ship capabilities
    Given a ship with specific modules
    When I analyze capabilities
    Then report should show cargo, fuel, and range
    And module details should be included

  # Scout Coordination Tests
  Scenario: Coordinate multi-ship market survey
    Given multiple scout ships
    And market waypoints to survey
    When I coordinate survey
    Then ships should be assigned to markets
    And coordination state should be saved

  # Helpers Tests
  Scenario: Captain logs root creates directories
    Given a captain logs configuration
    When I get captain logs root for agent
    Then root directory should be created
    And sessions subdirectory should be created
    And executive reports subdirectory should be created

  # Type Consistency Tests
  Scenario: Sell all maintains type consistency
    Given a ship with cargo
    When I execute sell_all
    Then return value should be consistent type
    And response should be properly formatted
