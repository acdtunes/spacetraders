Feature: Ship controller utilities
  Scenario: Dock waits for in-transit arrival before docking
    Given an in-transit ship at waypoint "ROUTE-DEST"
    When the ship controller docks the ship
    Then docking succeeds

  Scenario: Refuel skips when tank is already full
    Given a docked ship with full fuel
    When a refuel request is made
    Then no refuel API call is issued

  Scenario: Refuel while undocked forces docking and posts units
    Given an orbiting ship with low fuel
    When a refuel request is made for 50 units
    Then a refuel API call is issued with 50 units

  Scenario: Navigation waits for arrival when already en route to the same destination
    Given a ship already in transit to "TARGET"
    When navigation is requested to "TARGET"
    Then navigation waits for arrival only

  Scenario: Navigation performs full flight sequence
    Given a docked ship needing navigation to "TARGET"
    When navigation is requested with auto refuel
    Then the ship patches the desired flight mode
    And the ship posts a navigate command

  Scenario: Cooldown waits emit progress updates
    Given a ship controller waiting for cooldown
    When a cooldown of 3 seconds is requested
    Then the wait timer sleeps for progress intervals
