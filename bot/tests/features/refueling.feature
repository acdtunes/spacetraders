Feature: Refueling validation

  Scenario Outline: Validate Refueling module <module>
    When I execute the "refueling" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_fuel_station_in_scout_tour_bug.py |
    | test_fuel_station_scout_exclusion_bug.py |
    | test_intermediate_refuel_bug.py |
    | test_refuel_step_execution_bug.py |
    | test_refuel_via_intermediate.py |

