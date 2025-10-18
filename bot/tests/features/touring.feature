Feature: Touring validation

  Scenario Outline: Validate Touring module <module>
    When I execute the "touring" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_cache_validation_fuel_station.py |
    | test_tour_cache_persistence.py |
    | test_tour_cache_scout_assignment_bug.py |
    | test_tour_cache_visualizer_consistency.py |
    | test_tour_optimization_quality.py |
    | test_tour_time_imbalance_bug.py |
    | test_visualizer_coordinates.py |

