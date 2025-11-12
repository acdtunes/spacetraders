Feature: Navigate Ship Handler - Business Rules Testing
  As a SpaceTraders bot
  I want to execute navigation commands with proper business rules
  So that ships navigate safely with optimal refueling and state management

  # ============================================================================
  # Caching and Enrichment Business Rules
  # ============================================================================

  @caching
  Scenario: Load graph from database cache when available
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has a cached graph with 10 waypoints
    And waypoint "X1-GZ7-A1" has trait "MARKETPLACE" in waypoints table
    And waypoint "X1-GZ7-B1" has no fuel station trait
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 100 fuel
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then the graph should be loaded from database cache
    And waypoints should be enriched with has_fuel trait data
    And navigation should succeed

  @caching
  Scenario: Build graph from API when cache is empty
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-NEW" has no cached graph
    And the API will return 5 waypoints for system "X1-NEW"
    And ship "SCOUT-1" is at "X1-NEW-A1"
    When I navigate "SCOUT-1" to "X1-NEW-B1"
    Then the API should be called to list waypoints
    And the graph should be saved to system_graphs table
    And waypoints should be saved to waypoints table
    And the graph should be enriched with trait data
    And navigation should succeed

  @caching @enrichment
  Scenario: Merge graph structure with waypoint trait data
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has cached graph structure only
    And waypoint "X1-GZ7-A1" has trait "MARKETPLACE" in waypoints table
    And waypoint "X1-GZ7-B1" has no traits in waypoints table
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 100 fuel
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then waypoint "X1-GZ7-A1" should be enriched with has_fuel true
    And waypoint "X1-GZ7-B1" should be enriched with has_fuel false

  # ============================================================================
  # Validation Business Rules
  # ============================================================================

  @validation
  Scenario: Reject navigation with empty waypoint cache
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has zero waypoints in cache
    And ship "SCOUT-1" is at "X1-GZ7-A1"
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then the command should fail with error "waypoint cache is empty"
    And the error should mention "Please sync waypoints from API first"

  @validation
  Scenario: Reject navigation when ship location missing from cache
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has 5 waypoints cached
    But waypoint "X1-GZ7-A1" is NOT in the cache
    And ship "SCOUT-1" reports location "X1-GZ7-A1"
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then the command should fail with error "Ship location is missing from waypoint cache"

  @validation
  Scenario: Reject navigation when destination missing from cache
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoint "X1-GZ7-A1" cached
    But waypoint "X1-GZ7-B1" is NOT cached
    And ship "SCOUT-1" is at "X1-GZ7-A1"
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then the command should fail with error "Destination waypoint is missing from waypoint cache"

  @validation
  Scenario: Route not found - provide detailed statistics
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has 20 waypoints with 3 fuel stations
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 50 fuel out of 100 capacity
    And destination "X1-GZ7-UNREACHABLE" exists but routing engine finds no path
    When I navigate "SCOUT-1" to "X1-GZ7-UNREACHABLE"
    Then the error should include "No route found"
    And the error should mention "20 waypoints cached"
    And the error should mention "3 fuel stations"
    And the error should mention "Ship fuel: 50/100"
    And the error should mention "unreachable or require multi-hop refueling"

  # ============================================================================
  # Idempotency Business Rules
  # ============================================================================

  @idempotency
  Scenario: Ship already at destination - return success immediately
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" is at "X1-GZ7-A1"
    When I navigate "SCOUT-1" to "X1-GZ7-A1"
    Then the response status should be "already_at_destination"
    And no navigate API calls should be made
    And the route should have 0 segments
    And the route status should be "COMPLETED"
    And current location should be "X1-GZ7-A1"

  @idempotency @in-transit
  Scenario: Wait for previous command completion before starting new navigation
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" is IN_TRANSIT to "X1-GZ7-B1" arriving in 30 seconds
    When I navigate "SCOUT-1" to "X1-GZ7-C1"
    Then the handler should detect IN_TRANSIT state
    And the handler should wait for arrival
    And ship state should be re-synced after arrival
    And then navigation to "X1-GZ7-C1" should begin

  # ============================================================================
  # Refueling Business Rules - 90% Opportunistic Rule
  # ============================================================================

  @refueling @90-percent-rule
  Scenario: Opportunistically refuel when arriving at fuel station with less than 90% fuel
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-B1" is a fuel station
    And ship "SCOUT-1" has 100 fuel capacity
    And ship "SCOUT-1" starts at "X1-GZ7-A1" with 100 fuel
    And navigation to "X1-GZ7-B1" consumes 50 fuel
    And the routing engine plans direct route without planned refuel
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then ship should arrive at "X1-GZ7-B1" with 50% fuel
    And opportunistic refuel should trigger
    And ship should dock at "X1-GZ7-B1"
    And ship should refuel to 100/100
    And ship should orbit after refuel

  @refueling @90-percent-rule
  Scenario: Skip opportunistic refuel when fuel is at or above 90%
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-B1" is a fuel station
    And ship "SCOUT-1" has 100 fuel capacity
    And ship arrives at fuel station "X1-GZ7-B1" with 95 fuel
    When the navigation segment completes
    Then opportunistic refuel should NOT trigger
    And ship should remain in orbit

  @refueling @90-percent-rule
  Scenario: Skip opportunistic refuel at non-fuel-station
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-C1" has no fuel station
    And ship "SCOUT-1" arrives at "X1-GZ7-C1" with 30% fuel
    When the navigation segment completes
    Then opportunistic refuel should NOT trigger

  @refueling @90-percent-rule
  Scenario: Skip opportunistic refuel when segment already requires refuel
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-B1" is a fuel station
    And ship "SCOUT-1" arrives at fuel station "X1-GZ7-B1" with 50% fuel
    And the segment has requires_refuel set to true
    When the navigation segment completes
    Then opportunistic refuel should NOT trigger
    And only the planned refuel should execute

  # ============================================================================
  # Pre-Departure Refuel Business Rules - Prevent DRIFT Mode
  # ============================================================================

  @refueling @pre-departure
  Scenario: Refuel before departure when DRIFT mode planned at fuel station
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" is a fuel station
    And ship "SCOUT-1" is at fuel station "X1-GZ7-A1" with 50% fuel
    And routing engine plans DRIFT mode for next segment
    And current location has fuel available
    When navigation begins for the segment
    Then pre-departure refuel should trigger
    And ship should refuel before departing
    And log should mention "Preventing DRIFT mode"

  @refueling @pre-departure
  Scenario: Skip pre-departure refuel when using CRUISE mode
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" is a fuel station
    And ship "SCOUT-1" is at fuel station "X1-GZ7-A1" with 50% fuel
    And routing engine plans CRUISE mode for next segment
    When navigation begins for the segment
    Then pre-departure refuel should NOT trigger

  @refueling @pre-departure
  Scenario: Skip pre-departure refuel when fuel is at or above 90%
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" is a fuel station
    And ship "SCOUT-1" is at fuel station "X1-GZ7-A1" with 95% fuel
    And routing engine plans DRIFT mode for next segment
    When navigation begins for the segment
    Then pre-departure refuel should NOT trigger

  @refueling @pre-departure
  Scenario: Skip pre-departure refuel when not at fuel station
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" has no fuel station
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 50% fuel
    And routing engine plans DRIFT mode for next segment
    When navigation begins for the segment
    Then pre-departure refuel should NOT trigger

  # ============================================================================
  # Refuel Before Departure Business Rules
  # ============================================================================

  @refueling @refuel-before-departure
  Scenario: Refuel at start location when route requires it
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" is a fuel station
    And ship "SCOUT-1" is at fuel station "X1-GZ7-A1" with low fuel
    And route has refuel_before_departure set to true
    When route execution begins
    Then ship should dock at "X1-GZ7-A1"
    And ship should refuel to full capacity
    And ship should orbit after refuel
    And then first segment should execute

  # ============================================================================
  # Flight Mode Business Rules
  # ============================================================================

  @flight-mode
  Scenario: Set flight mode before each navigation segment
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 100 fuel
    And routing engine plans BURN mode for segment 1
    And routing engine plans CRUISE mode for segment 2
    When segment 1 executes
    Then flight mode should be set to "BURN" before navigate API call
    When segment 2 executes
    Then flight mode should be set to "CRUISE" before navigate API call

  # ============================================================================
  # Wait for Arrival Business Rules
  # ============================================================================

  @arrival @timing
  Scenario: Wait for ship to arrive after navigation
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" navigates from "X1-GZ7-A1" to "X1-GZ7-B1"
    And API returns segment travel time of 45 seconds
    And current time is "2025-11-12T12:00:00Z"
    When navigation API is called
    Then the handler should calculate 45 seconds wait time
    And the handler should sleep for 48 seconds
    And ship state should be re-fetched after sleep
    And ship Arrive method should be called if status is IN_TRANSIT

  @arrival @timing
  Scenario: Handle arrival time in the past - ship already arrived
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" navigates to destination
    And API returns travel time in the past
    When navigation executes
    Then wait time should be 0 seconds
    And no sleep should occur
    And ship state should still be re-fetched

  # ============================================================================
  # Auto-Sync Business Rules
  # ============================================================================

  @state-sync
  Scenario: Re-sync ship state after every API call
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" executes a route with dock refuel orbit navigate
    When dock API is called
    Then ship state should be re-fetched immediately
    When refuel API is called
    Then ship state should be re-fetched immediately
    When orbit API is called
    Then ship state should be re-fetched immediately
    When navigate API is called
    Then ship state should be re-fetched immediately

  # ============================================================================
  # Error Handling Business Rules
  # ============================================================================

  @error-handling
  Scenario: Mark route as failed when navigation fails
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" starts navigation to "X1-GZ7-B1"
    But navigate API returns error "Insufficient fuel"
    When the error occurs during execution
    Then route status should be set to FAILED
    And route FailRoute method should be called with error message
    And the error should propagate to caller

  @error-handling
  Scenario: Handle dock failure during refuel sequence
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" attempts to refuel at "X1-GZ7-B1"
    But dock API returns error "Ship is already docked"
    When the error occurs
    Then the error should be returned to handler
    And route should be marked as FAILED

  # ============================================================================
  # Complete Multi-Segment Business Rules
  # ============================================================================

  @multi-segment @end-to-end
  Scenario: Execute multi-segment route with planned refueling
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" has 50 fuel capacity
    And ship "SCOUT-1" starts at "X1-GZ7-A1" with 50 fuel
    And routing engine plans route with segments:
      | From       | To        | Fuel Cost | Requires Refuel |
      | X1-GZ7-A1  | X1-GZ7-B1 | 30        | false           |
      | X1-GZ7-B1  | X1-GZ7-C1 | 40        | true            |
      | X1-GZ7-C1  | X1-GZ7-D1 | 35        | false           |
    When I navigate "SCOUT-1" to "X1-GZ7-D1"
    Then segment 1 should execute from A1 to B1
    And ship should have 20 fuel remaining at B1
    And segment 2 should execute from B1 to C1
    And ship should refuel at C1 because of planned refuel
    And ship should have 50 fuel after refuel at C1
    And segment 3 should execute from C1 to D1
    And ship should arrive at D1
    And route status should be COMPLETED

  @multi-segment @end-to-end
  Scenario: Execute route with opportunistic and planned refueling
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" has 100 fuel capacity
    And ship "SCOUT-1" starts at "X1-GZ7-A1" with 100 fuel
    And routing engine plans route with segments:
      | From       | To        | Fuel Cost | Requires Refuel | Waypoint Has Fuel |
      | X1-GZ7-A1  | X1-GZ7-B1 | 60        | false           | true              |
      | X1-GZ7-B1  | X1-GZ7-C1 | 30        | false           | false             |
      | X1-GZ7-C1  | X1-GZ7-D1 | 45        | true            | true              |
      | X1-GZ7-D1  | X1-GZ7-E1 | 35        | false           | false             |
    When I navigate "SCOUT-1" to "X1-GZ7-E1"
    Then segment 1 should execute to B1
    And ship should have 40% fuel at B1
    And opportunistic refuel should trigger at B1
    And segment 2 should execute to C1
    And no refuel at C1 because no fuel station
    And segment 3 should execute to D1
    And planned refuel should execute at D1
    And segment 4 should execute to E1
    And route status should be COMPLETED

  # ============================================================================
  # State Machine Verification
  # ============================================================================

  @state-machine
  Scenario: Ship transitions through states during navigation
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" is DOCKED at "X1-GZ7-A1"
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then ship should be IN_ORBIT before navigation
    And ship should be IN_TRANSIT after navigate API call
    And ship should be IN_ORBIT after arrival
    And final status should be IN_ORBIT at "X1-GZ7-B1"

  @state-machine
  Scenario: Refuel sequence follows dock orbit pattern
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-B1" is a fuel station
    And ship "SCOUT-1" is IN_ORBIT at "X1-GZ7-B1" with low fuel
    And route requires refuel at "X1-GZ7-B1"
    When refuel sequence executes
    Then ship should transition to DOCKED
    And refuel API should be called
    And ship should transition back to IN_ORBIT
    And fuel should be at 100%

  # ============================================================================
  # Edge Cases
  # ============================================================================

  @edge-case
  Scenario: Ship with zero fuel capacity navigates without refueling
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "PROBE-1" has 0 fuel capacity
    And ship "PROBE-1" is at "X1-GZ7-A1"
    When I navigate "PROBE-1" to "X1-GZ7-B1"
    Then navigation should succeed
    And no refuel checks should execute
    And no fuel consumption should occur

  @edge-case
  Scenario: Single waypoint system - already at destination
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-SOLO" has only 1 waypoint cached
    And ship "SCOUT-1" is at "X1-SOLO-A1"
    When I navigate "SCOUT-1" to "X1-SOLO-A1"
    Then the response status should be "already_at_destination"
    And route should have 0 segments
    And no API calls should be made

  @edge-case
  Scenario: Route with only refuel before departure
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And waypoint "X1-GZ7-A1" is a fuel station
    And ship "SCOUT-1" is at "X1-GZ7-A1" with 10% fuel
    And routing engine returns route with refuel_before_departure true
    And route has 1 segment from A1 to B1
    When I navigate "SCOUT-1" to "X1-GZ7-B1"
    Then ship should refuel before starting journey
    And ship should have 100% fuel before departure
    And then segment to B1 should execute

  # ============================================================================
  # Performance and Timing
  # ============================================================================

  @timing
  Scenario: Calculate correct wait time with 3 second buffer
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" navigates with segment travel time 120 seconds
    When navigation API completes
    Then handler should calculate wait time as 120 seconds
    And handler should add 3 second buffer
    And total sleep time should be 123 seconds

  @timing
  Scenario: Zero wait time for instant arrival
    Given an in-memory database is initialized
    And a player "TEST-PLAYER" exists with token "test-token"
    And system "X1-GZ7" has waypoints cached
    And ship "SCOUT-1" navigates with segment travel time 0 seconds
    When navigation API completes
    Then handler should calculate wait time as 0 seconds
    And no sleep should occur
    And ship state should still be re-synced
