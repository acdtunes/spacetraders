Feature: FlightMode Value Object
  As a SpaceTraders navigation system
  I want to manage flight modes with their characteristics
  So that I can calculate fuel costs and travel times accurately

  # FlightMode.String() - String representation
  Scenario: CRUISE mode string representation
    When I get the string representation of CRUISE flight mode
    Then the flight mode string should be "CRUISE"

  Scenario: DRIFT mode string representation
    When I get the string representation of DRIFT flight mode
    Then the flight mode string should be "DRIFT"

  Scenario: BURN mode string representation
    When I get the string representation of BURN flight mode
    Then the flight mode string should be "BURN"

  Scenario: STEALTH mode string representation
    When I get the string representation of STEALTH flight mode
    Then the flight mode string should be "STEALTH"

  # IsValidFlightModeName() - Name validation
  Scenario: Valid flight mode name CRUISE
    When I check if "CRUISE" is a valid flight mode name
    Then the name should be valid

  Scenario: Valid flight mode name DRIFT
    When I check if "DRIFT" is a valid flight mode name
    Then the name should be valid

  Scenario: Valid flight mode name BURN
    When I check if "BURN" is a valid flight mode name
    Then the name should be valid

  Scenario: Valid flight mode name STEALTH
    When I check if "STEALTH" is a valid flight mode name
    Then the name should be valid

  Scenario: Invalid flight mode name INVALID
    When I check if "INVALID" is a valid flight mode name
    Then the name should not be valid

  Scenario: Invalid flight mode name empty string
    When I check if "" is a valid flight mode name
    Then the name should not be valid

  Scenario: Invalid flight mode name lowercase
    When I check if "cruise" is a valid flight mode name
    Then the name should not be valid

  # ParseFlightMode() - Parse mode from string
  Scenario: Parse CRUISE mode from string
    When I parse flight mode from string "CRUISE"
    Then parsing should succeed
    And the parsed flight mode should be CRUISE

  Scenario: Parse DRIFT mode from string
    When I parse flight mode from string "DRIFT"
    Then parsing should succeed
    And the parsed flight mode should be DRIFT

  Scenario: Parse BURN mode from string
    When I parse flight mode from string "BURN"
    Then parsing should succeed
    And the parsed flight mode should be BURN

  Scenario: Parse STEALTH mode from string
    When I parse flight mode from string "STEALTH"
    Then parsing should succeed
    And the parsed flight mode should be STEALTH

  Scenario: Parse invalid flight mode returns error
    When I parse flight mode from string "INVALID"
    Then parsing should fail with error "invalid flight mode: INVALID"

  Scenario: Parse empty string returns error
    When I parse flight mode from string ""
    Then parsing should fail with error "invalid flight mode: "

  Scenario: Parse lowercase mode returns error
    When I parse flight mode from string "cruise"
    Then parsing should fail with error "invalid flight mode: cruise"
