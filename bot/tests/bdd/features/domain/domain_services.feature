Feature: Domain Services for Navigation
  As a ship operator
  I want domain services to help with flight decisions
  So that I can optimize fuel usage, select flight modes, and validate routes

  Background:
    Given the domain services are initialized

  # FlightModeSelector - Speed-First Selection with Absolute Safety Margin (11 tests)
  # Business Rule: ALWAYS prioritize speed (BURN > CRUISE > DRIFT)
  # Use absolute safety margin of 4 fuel units (not percentage)

  Scenario: Select BURN when fuel is high
    Given a ship with fuel at 80 percent
    And a distance of 30 units
    When I select flight mode based on fuel
    Then BURN mode should be selected

  Scenario: Select CRUISE when fuel is medium
    Given a ship with 50 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode based on fuel
    Then CRUISE mode should be selected

  Scenario: Select DRIFT when fuel is low
    Given a ship with 10 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode based on fuel
    Then DRIFT mode should be selected

  Scenario: Select BURN with full fuel
    Given a ship with 100 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode based on fuel
    Then BURN mode should be selected

  Scenario: Select CRUISE when insufficient fuel for BURN
    Given a ship with 63 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode based on fuel
    Then CRUISE mode should be selected

  Scenario: Select DRIFT when only enough fuel for DRIFT
    Given a ship with 13 current fuel and 100 capacity
    And a distance of 10 units
    When I select flight mode based on fuel
    Then DRIFT mode should be selected

  Scenario: Select BURN for distance with sufficient fuel
    Given a ship with 100 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode for distance
    Then BURN mode should be selected

  Scenario: Select DRIFT for distance with insufficient fuel
    Given a ship with 10 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode for distance
    Then DRIFT mode should be selected

  Scenario: Select CRUISE when return trip required and BURN not possible
    Given a ship with 70 current fuel and 100 capacity
    And a distance of 30 units
    And return trip is required
    When I select flight mode for distance with return
    Then CRUISE mode should be selected

  Scenario: Select DRIFT when insufficient fuel for all faster modes
    Given a ship with 10 current fuel and 100 capacity
    And a distance of 50 units
    When I select flight mode for distance
    Then DRIFT mode should be selected

  Scenario: Prefer BURN over CRUISE when fuel sufficient
    Given a ship with 100 current fuel and 100 capacity
    And a distance of 30 units
    When I select flight mode for distance
    Then BURN mode should be selected

  # RefuelPlanner - Refuel Decision with Absolute Safety Margin (10 tests)
  # Business Rule: Refuel when fuel < 4 units OR insufficient for next leg in BURN mode

  Scenario: Should refuel when fuel below safety margin
    Given a ship with 3 current fuel and 100 capacity
    And the ship is at a marketplace
    When I check if ship should refuel
    Then refuel should be recommended

  Scenario: Should not refuel when above safety margin with no distance
    Given a ship with 50 current fuel and 100 capacity
    And the ship is at a marketplace
    When I check if ship should refuel
    Then refuel should not be recommended

  Scenario: Should not refuel when not at marketplace
    Given a ship with 2 current fuel and 100 capacity
    And the ship is not at a marketplace
    When I check if ship should refuel
    Then refuel should not be recommended

  Scenario: Should refuel when insufficient for next leg in BURN mode
    Given a ship with 50 current fuel and 100 capacity
    And the ship is at a marketplace
    And a distance of 30 units
    When I check if ship should refuel
    Then refuel should be recommended

  Scenario: Should not refuel when sufficient for next leg in BURN mode
    Given a ship with 100 current fuel and 100 capacity
    And the ship is at a marketplace
    And a distance of 30 units
    When I check if ship should refuel
    Then refuel should not be recommended

  Scenario: Need refuel stop when cannot reach destination
    Given a ship with 50 current fuel and 100 capacity
    And a distance to destination of 200 units
    And a distance to refuel point of 30 units
    And flight mode is CRUISE
    When I check if refuel stop is needed
    Then refuel stop should be needed

  Scenario: No refuel stop when can reach destination
    Given a ship with 100 current fuel and 100 capacity
    And a distance to destination of 50 units
    And a distance to refuel point of 30 units
    And flight mode is CRUISE
    When I check if refuel stop is needed
    Then refuel stop should not be needed

  Scenario: Refuel stop includes safety margin
    Given a ship with 55 current fuel and 100 capacity
    And a distance to destination of 50 units
    And a distance to refuel point of 10 units
    And flight mode is CRUISE
    When I check if refuel stop is needed
    Then refuel stop should not be needed due to safety margin

  Scenario: Cannot reach refuel point returns false
    Given a ship with 10 current fuel and 100 capacity
    And a distance to destination of 200 units
    And a distance to refuel point of 50 units
    And flight mode is CRUISE
    When I check if refuel stop is needed
    Then refuel stop should not be needed because cannot reach refuel point

  Scenario: Refuel stop calculation works with DRIFT mode
    Given a ship with 10 current fuel and 100 capacity
    And a distance to destination of 1000 units
    And a distance to refuel point of 100 units
    And flight mode is DRIFT
    When I check if refuel stop is needed
    Then refuel stop should not be needed

  # RouteValidator - Segment Connection (13 tests)
  Scenario: Validate connected route segments
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And waypoint C at coordinates 200, 0
    And a route segment from A to B with 100 distance
    And a route segment from B to C with 100 distance
    When I validate segments are connected
    Then segments should be valid

  Scenario: Reject disconnected route segments
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And waypoint C at coordinates 200, 0
    And a route segment from A to B with 100 distance
    And a route segment from A to C with 200 distance
    When I validate segments are connected
    Then segments should not be valid

  Scenario: Validate single route segment
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And a route segment from A to B with 100 distance
    When I validate segments are connected
    Then segments should be valid

  Scenario: Validate empty route segments
    Given no route segments
    When I validate segments are connected
    Then segments should be valid

  Scenario: Validate fuel capacity sufficient for all segments
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And a route segment from A to B requiring 50 fuel
    And a route segment from A to B requiring 60 fuel
    And ship fuel capacity is 100
    When I validate fuel capacity
    Then fuel capacity should be sufficient

  Scenario: Reject when segment exceeds fuel capacity
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And a route segment from A to B requiring 150 fuel
    And ship fuel capacity is 100
    When I validate fuel capacity
    Then fuel capacity should not be sufficient

  Scenario: Validate segment at exact fuel capacity limit
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And a route segment from A to B requiring 100 fuel
    And ship fuel capacity is 100
    When I validate fuel capacity
    Then fuel capacity should be sufficient

  Scenario: Validate empty segments for fuel capacity
    Given no route segments
    And ship fuel capacity is 100
    When I validate fuel capacity
    Then fuel capacity should be sufficient

  Scenario: Check maximum segment fuel requirement
    Given waypoint A at coordinates 0, 0
    And waypoint B at coordinates 100, 0
    And a route segment from A to B requiring 40 fuel
    And a route segment from A to B requiring 150 fuel
    And ship fuel capacity is 100
    When I validate fuel capacity
    Then fuel capacity should not be sufficient

  # Service Constants (2 tests)
  # New business rules use absolute safety margin, not percentages

  Scenario: FlightModeSelector has correct safety margin
    When I check the FlightModeSelector safety margin
    Then the safety margin should be 4 units

  Scenario: RefuelPlanner has correct safety margin
    When I check the RefuelPlanner safety margin
    Then the safety margin should be 4 units
