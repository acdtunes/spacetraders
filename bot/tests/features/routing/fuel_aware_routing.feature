Feature: Fuel-Aware Routing
  As a navigation system
  I want route planning to accurately calculate fuel requirements
  So that ships never run out of fuel or report false fuel shortages

  Background:
    Given SmartNavigator is configured with production settings
    And fuel calculations use correct formulas:
      | mode   | fuel_per_unit |
      | CRUISE | 1.0           |
      | DRIFT  | 0.003         |

  # =========================================================================
  # Critical Bug #1: A* Iteration Limit
  # =========================================================================

  Scenario: Long-distance routes should not fail due to iteration limit
    Given the real X1-GH18 system graph from production
    And ship "SILMARETH-1" at waypoint "X1-GH18-H57"
    And ship has 400/400 fuel
    And destination "X1-GH18-J62" is 762 units away
    When I plan a route using A* pathfinding
    Then the route should be found successfully
    And the route should not fail with "iteration limit exceeded"
    Because 762-unit paths are feasible with DRIFT mode

  Scenario: A* max_iterations should accommodate long paths
    Given a system graph with 95 waypoints
    And ship at starting waypoint
    And destination is 700+ units away via complex path
    When I attempt route planning with default max_iterations
    Then the route should be found
    And iteration count should be logged
    And max_iterations should be at least 50000 for production systems

  Scenario: Complex graphs with many waypoints require higher iteration limits
    Given a system with 100+ waypoints
    And multiple fuel stations creating branching paths
    And ship must navigate 500+ unit distance
    When route planning is attempted
    Then A* should complete within iteration limit
    And route should include optimal refuel stops
    And error messages should not blame fuel if iterations exceeded

  # =========================================================================
  # Critical Bug #2: Misleading "Insufficient Fuel" Errors
  # =========================================================================

  Scenario: Route planner should not report "insufficient fuel" when fuel is adequate
    Given ship "SILMARETH-1" with 400 fuel at "X1-GH18-H57"
    And destination "X1-GH18-J62" is 762 units away
    And DRIFT fuel requirement is ~2.3 fuel (762 * 0.003)
    When route validation fails for any reason
    Then error message should NOT mention "insufficient fuel"
    Because ship has 400 fuel which is 173x the requirement

  Scenario: Distinguish between fuel constraints and pathfinding failures
    Given a ship with ample fuel for the journey
    And a complex route that exceeds A* iteration limit
    When route planning fails
    Then error should indicate "route planning limit exceeded" or similar
    And error should NOT mention "insufficient fuel even with DRIFT"
    And logs should clarify the actual failure reason

  Scenario: Accurate error reporting for genuine fuel shortages
    Given ship with 50 fuel at waypoint "X1-TEST-A1"
    And destination "X1-TEST-Z99" requires 200 fuel in CRUISE mode
    And destination requires 75 fuel in DRIFT mode
    When route validation is performed
    Then error should accurately report "insufficient fuel"
    And error should specify required fuel amount
    And error should suggest DRIFT mode or refuel stops

  # =========================================================================
  # Fuel Calculation Accuracy
  # =========================================================================

  Scenario: DRIFT mode fuel calculation for 762-unit journey
    Given distance of 762 units
    When I calculate DRIFT mode fuel cost
    Then fuel required should be approximately 2.3 units (762 * 0.003)
    And calculation should account for rounding
    And minimum fuel should be at least 1 (DRIFT mode minimum)

  Scenario: CRUISE mode fuel calculation for 762-unit journey
    Given distance of 762 units
    When I calculate CRUISE mode fuel cost
    Then fuel required should be approximately 762 units (762 * 1.0)
    And ship with 400 fuel cannot complete journey without refuel
    And route planner should automatically select DRIFT or insert refuel

  Scenario: Round-trip fuel calculation with safety margin
    Given a mining route from "X1-TEST-M1" to asteroid "X1-TEST-A5"
    And one-way distance is 200 units
    When I calculate round-trip fuel requirement
    Then fuel needed should be (200 * 2 * 1.1) = 440 units with 10% margin
    And ship with 400 fuel should be flagged as insufficient for CRUISE
    But ship with 400 fuel is adequate for DRIFT round-trip

  Scenario: Fuel-aware mode selection based on current fuel level
    Given a ship with varying fuel levels
    And a 100-unit journey
    When I determine optimal flight mode:
      | current_fuel | fuel_capacity | recommended_mode | reason                          |
      | 350          | 400           | CRUISE           | >75% fuel, fast travel preferred|
      | 250          | 400           | DRIFT            | <75% fuel, conserve fuel        |
      | 80           | 400           | DRIFT            | <25% fuel, emergency mode       |
    Then flight mode should be selected correctly
    And fuel safety margin should be maintained

  # =========================================================================
  # Refuel Stop Insertion
  # =========================================================================

  Scenario: SmartNavigator inserts refuel stops for long CRUISE journeys
    Given ship with 400 fuel at "X1-TEST-A1"
    And destination "X1-TEST-Z99" is 600 units away
    And fuel station "X1-TEST-M50" is at 300-unit midpoint
    When I execute route with SmartNavigator
    Then route should automatically include refuel stop at "X1-TEST-M50"
    And ship should reach destination with safe fuel margin
    And total fuel consumed should account for CRUISE mode

  Scenario: DRIFT mode avoids refuel stops for long journeys
    Given ship with 100 fuel at "X1-TEST-A1"
    And destination "X1-TEST-Z99" is 600 units away (requires ~1.8 fuel in DRIFT)
    When I execute route with SmartNavigator
    Then route should NOT insert refuel stops
    And flight mode should be DRIFT
    And ship should arrive with 98+ fuel remaining

  Scenario: Multiple refuel stops for very long journeys
    Given ship with 400 fuel at "X1-TEST-A1"
    And destination "X1-TEST-Z99" is 1500 units away
    And fuel stations exist at 400u, 800u, and 1200u waypoints
    When I plan route in CRUISE mode
    Then route should include multiple refuel stops
    And fuel level should never drop below 10% capacity
    And route should minimize total refueling time

  Scenario: Emergency refuel when fuel drops below minimum threshold
    Given ship with 150 fuel at "X1-TEST-A1"
    And destination is 400 units away
    And current fuel <25% of capacity (emergency threshold)
    When route is planned
    Then route should prioritize nearest fuel station first
    And route should use DRIFT mode exclusively
    And warning should be logged about low fuel condition

  # =========================================================================
  # Zero-Distance Navigation (Orbital Waypoints)
  # =========================================================================

  Scenario: Orbital waypoints have zero fuel cost
    Given planet "X1-TEST-A1" at coordinates (10, 20)
    And moon "X1-TEST-A2" orbiting "X1-TEST-A1" at same coordinates
    When I navigate from "X1-TEST-A1" to "X1-TEST-A2"
    Then distance should be 0 units
    And fuel cost should be 0
    And navigation should be instant

  Scenario: Route planner leverages orbital waypoints for fuel efficiency
    Given planet "X1-TEST-A1" with fuel station at (10, 20)
    And moon "X1-TEST-A2" with marketplace at (10, 20)
    And both share orbital parent
    When I plan a mining route using both waypoints
    Then route should recognize 0-distance transition
    And fuel calculations should not penalize orbital hops
    And route should prefer orbital waypoints when available

  # =========================================================================
  # Integration: Fuel + Market Selection
  # =========================================================================

  Scenario: Contract market selection should consider navigation fuel cost
    Given contract requires delivery to "X1-GH18-J62"
    And market "X1-GH18-H57" sells resource at 1000 cr/unit (762u from delivery)
    And market "X1-GH18-B7" sells resource at 1200 cr/unit (100u from delivery)
    When contract operation selects purchase market
    Then fuel cost should be factored into decision
    And total cost should be (purchase_price * units) + (fuel_cost * 2 trips)
    And nearby market "B7" should be preferred despite higher unit price
    Because fuel cost savings offset 200 cr/unit price difference

  Scenario: Distance-aware market selection for contract fulfillment
    Given a contract with delivery waypoint "X1-TEST-D5"
    And available markets sorted by price:
      | market      | price | distance_to_delivery |
      | X1-TEST-M1  | 800   | 50                   |
      | X1-TEST-M2  | 750   | 300                  |
      | X1-TEST-M3  | 700   | 750                  |
    When contract operation selects market
    Then selection should use cost function: price + (fuel_cost * distance)
    And market "X1-TEST-M1" should be selected
    Because total cost is lower when fuel is factored in

  # =========================================================================
  # Validation and Safety Checks
  # =========================================================================

  Scenario: Route validation catches impossible journeys
    Given ship with 50 fuel
    And destination requires 200 fuel (no refuel stations available)
    When route validation is performed
    Then validation should return False
    And reason should clearly state "insufficient fuel"
    And reason should specify required fuel (200) vs available (50)
    And route execution should be blocked

  Scenario: Route validation ensures minimum fuel margin
    Given ship with 100 fuel
    And destination requires exactly 95 fuel in DRIFT
    When route validation checks safety margin
    Then validation should enforce 10% fuel margin minimum
    And route should either insert refuel stop or use slower mode
    And ship should not be allowed to arrive with <5% fuel

  Scenario: Fuel recalculation after cargo changes
    Given ship with 40 cargo capacity
    And initially empty cargo
    And planned route requires 150 fuel
    When ship loads 40 units of cargo
    Then fuel calculations should remain constant
    Because SpaceTraders fuel cost is independent of cargo weight
    And route should still be valid
