Feature: Flight Mode Value Object
  As a navigation system
  I want to calculate fuel costs and travel times for different flight modes
  So that I can optimize ship navigation

  # ============================================================================
  # Flight Mode Fuel Cost Tests
  # ============================================================================

  Scenario: Calculate cruise mode fuel cost
    When I calculate fuel cost for CRUISE mode with distance 100.0
    Then the fuel cost should be 100

  Scenario: Calculate drift mode fuel cost
    When I calculate fuel cost for DRIFT mode with distance 100.0
    Then the fuel cost should be 1

  Scenario: Calculate burn mode fuel cost
    When I calculate fuel cost for BURN mode with distance 100.0
    Then the fuel cost should be 200

  Scenario: Calculate stealth mode fuel cost
    When I calculate fuel cost for STEALTH mode with distance 100.0
    Then the fuel cost should be 100

  Scenario: Fuel cost for zero distance is zero
    When I calculate fuel cost for CRUISE mode with distance 0.0
    Then the fuel cost should be 0

  Scenario: Minimum fuel cost is 1 for very small distances
    When I calculate fuel cost for CRUISE mode with distance 0.1
    Then the fuel cost should be 1

  Scenario: Drift mode has minimal fuel consumption
    When I calculate fuel cost for DRIFT mode with distance 1000.0
    Then the fuel cost should be 3

  # ============================================================================
  # Flight Mode Travel Time Tests
  # ============================================================================

  Scenario: Calculate cruise mode travel time
    When I calculate travel time for CRUISE mode with distance 100.0 and speed 30
    Then the travel time should be 103 seconds

  Scenario: Calculate drift mode travel time
    When I calculate travel time for DRIFT mode with distance 100.0 and speed 30
    Then the travel time should be 86 seconds

  Scenario: Calculate burn mode travel time
    When I calculate travel time for BURN mode with distance 100.0 and speed 30
    Then the travel time should be 50 seconds

  Scenario: Calculate stealth mode travel time
    When I calculate travel time for STEALTH mode with distance 100.0 and speed 30
    Then the travel time should be 166 seconds

  Scenario: Travel time for zero distance is zero
    When I calculate travel time for CRUISE mode with distance 0.0 and speed 30
    Then the travel time should be 0 seconds

  Scenario: Minimum travel time is 1 for very small distances
    When I calculate travel time for CRUISE mode with distance 0.1 and speed 30
    Then the travel time should be 1 seconds

  Scenario: Higher speed reduces travel time
    When I calculate travel time for CRUISE mode with distance 100.0 and speed 60
    Then the travel time should be 51 seconds

  # ============================================================================
  # Optimal Flight Mode Selection Tests
  # ============================================================================

  Scenario: Select burn mode with high fuel and safety margin
    When I select optimal flight mode with current fuel 100, cost 50, safety margin 4
    Then the selected mode should be BURN

  Scenario: Select cruise mode with medium fuel
    When I select optimal flight mode with current fuel 54, cost 50, safety margin 4
    Then the selected mode should be CRUISE

  Scenario: Select drift mode with low fuel
    When I select optimal flight mode with current fuel 53, cost 50, safety margin 4
    Then the selected mode should be DRIFT

  Scenario: Select burn with exact fuel for burn plus safety
    When I select optimal flight mode with current fuel 104, cost 50, safety margin 4
    Then the selected mode should be BURN

  Scenario: Select cruise when burn would exceed safety
    When I select optimal flight mode with current fuel 103, cost 50, safety margin 4
    Then the selected mode should be CRUISE

  Scenario: Select drift when cruise would exceed safety
    When I select optimal flight mode with current fuel 53, cost 50, safety margin 4
    Then the selected mode should be DRIFT

  Scenario: Always select fastest mode that maintains safety margin
    When I select optimal flight mode with current fuel 200, cost 50, safety margin 4
    Then the selected mode should be BURN

  # ============================================================================
  # Flight Mode Name Tests
  # ============================================================================

  Scenario: Get cruise mode name
    When I get the name of CRUISE mode
    Then the mode name should be "CRUISE"

  Scenario: Get drift mode name
    When I get the name of DRIFT mode
    Then the mode name should be "DRIFT"

  Scenario: Get burn mode name
    When I get the name of BURN mode
    Then the mode name should be "BURN"

  Scenario: Get stealth mode name
    When I get the name of STEALTH mode
    Then the mode name should be "STEALTH"
