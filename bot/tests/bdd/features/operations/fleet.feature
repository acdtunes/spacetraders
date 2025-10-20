Feature: Fleet operations
  As a fleet manager
  I want to check fleet status and monitor operations
  So that I can track my ships and profits

  Background:
    Given a fleet management system

  Scenario: Check status with all ships
    Given agent has 3 ships
    And agent has 50000 credits
    When I check fleet status
    Then agent summary should be displayed
    And 3 ships should be displayed

  Scenario: Check status with specific ships
    Given agent has ship "SHIP-1" at "X1-AA-B1"
    And agent has ship "SHIP-2" at "X1-AA-B2"
    And agent has ship "SHIP-3" at "X1-AA-B3"
    When I check fleet status for ships "SHIP-1,SHIP-3"
    Then 2 ships should be displayed
    And ship "SHIP-1" should be shown
    And ship "SHIP-3" should be shown

  Scenario: Display ship in transit
    Given agent has ship "SHIP-1" in transit to "X1-AA-B5"
    When I check fleet status
    Then ship "SHIP-1" should show IN_TRANSIT status
    And ETA should be displayed

  Scenario: Display docked ship
    Given agent has ship "SHIP-2" docked at "X1-AA-B7"
    When I check fleet status
    Then ship "SHIP-2" should show DOCKED status
    And location should show "X1-AA-B7"

  Scenario: Display ship fuel and cargo
    Given agent has ship "SHIP-1" with fuel 80/100
    And ship "SHIP-1" has cargo 25/40
    When I check fleet status
    Then fuel should show "80/100"
    And cargo should show "25/40"

  Scenario: Monitor fleet for 2 checks
    Given agent starts with 10000 credits
    And agent has 2 ships
    When I monitor fleet for 2 checks with 1 minute interval
    Then 2 status checks should be performed
    And profit should be calculated

  Scenario: Monitor shows credit profit
    Given agent starts with 10000 credits
    And after first check agent has 12000 credits
    And after second check agent has 15000 credits
    When I monitor fleet for 2 checks with 1 minute interval
    Then final profit should show 5000 credits

  Scenario: Monitor handles unavailable ship gracefully
    Given agent has ship "SHIP-1" at "X1-AA-B1"
    And ship "SHIP-2" is unavailable
    And agent starts with 10000 credits
    When I monitor fleet for 1 check with 1 minute interval for ships "SHIP-1,SHIP-2"
    Then ship "SHIP-1" should be displayed
    And ship "SHIP-2" should show unavailable message
