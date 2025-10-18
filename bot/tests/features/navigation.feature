Feature: Navigation validation

  Scenario Outline: Validate Navigation module <module>
    When I execute the "navigation" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_balance_tour_times_bug.py |
    | test_checkpoint_resume_cruise_preference.py |
    | test_checkpoint_resume_simple.py |
    | test_drift_mode_short_trip_bug.py |
    | test_prefer_cruise_fix.py |
    | test_probe_satellite_navigation.py |
    | test_safety_margin_cruise_selection_bug.py |
    | test_smart_navigator_checkpoint_bug.py |
    | test_smart_navigator_stale_checkpoint.py |

