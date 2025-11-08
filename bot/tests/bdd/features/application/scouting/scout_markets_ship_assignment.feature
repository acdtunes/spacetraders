Feature: Scout Markets Ship Assignment Isolation
  Verify that each container is assigned to the correct ship

  Background:
    Given a player with ID 1

  Scenario: Multiple scout containers each control their assigned ship
    Given ships in system "X1-TEST":
      | ship_symbol   | waypoint    | status  |
      | ENDURANCE-2   | X1-TEST-A1  | DOCKED  |
      | ENDURANCE-3   | X1-TEST-B2  | DOCKED  |
      | ENDURANCE-4   | X1-TEST-C3  | DOCKED  |
    When I execute scout markets for system "X1-TEST" with ships:
      | ENDURANCE-2 |
      | ENDURANCE-3 |
      | ENDURANCE-4 |
    And markets:
      | X1-TEST-A1 |
      | X1-TEST-B2 |
      | X1-TEST-C3 |
    Then scout markets should complete successfully
    And 3 containers should be created
    And container 0 should be assigned to ship "ENDURANCE-2"
    And container 1 should be assigned to ship "ENDURANCE-3"
    And container 2 should be assigned to ship "ENDURANCE-4"
    And each container's ship matches its assigned markets owner
    And all container configs are independent objects
