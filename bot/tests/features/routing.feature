Feature: Routing validation

  Scenario Outline: Validate Routing module <module>
    When I execute the "routing" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_fixed_route_coordinate_bug.py |
    | test_mincostflow_branching_bug.py |
    | test_ortools_crossing_edges_bug.py |
    | test_ortools_disjunction_penalty_too_low.py |
    | test_ortools_duplicate_waypoint_bug.py |
    | test_ortools_fallback_dijkstra.py |
    | test_ortools_market_drop_bug_real_data.py |
    | test_ortools_min_cost_flow_cycle_bug.py |
    | test_ortools_mining_steps.py |
    | test_ortools_orbital_branching_bug.py |
    | test_ortools_orbital_jitter.py |
    | test_ortools_partitioner_deduplication_unit.py |
    | test_ortools_real_coordinates.py |
    | test_ortools_router_fast_fuel_aware_routing.py |
    | test_ortools_router_hang_bug.py |
    | test_ortools_router_initialization_performance.py |
    | test_ortools_router_mincostflow_hang.py |
    | test_routing_critical_bugs_fix.py |

