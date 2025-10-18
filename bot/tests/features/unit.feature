Feature: Unit validation

  Scenario Outline: Validate unit test module <module>
    When I execute the "unit" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | cli/test_main_cli.py |
    | core/test_api_client.py |
    | core/test_daemon_manager.py |
    | core/test_ortools_routing.py |
    | core/test_route_optimizer.py |
    | core/test_scout_coordinator_core.py |
    | core/test_smart_navigator_unit.py |
    | core/test_tour_optimizer.py |
    | helpers/test_paths.py |
    | operations/test_analysis_operation.py |
    | operations/test_assignment_operations.py |
    | operations/test_assignments_operation.py |
    | operations/test_captain_logging.py |
    | operations/test_common.py |
    | operations/test_contract_resource_strategy.py |
    | operations/test_control_primitives.py |
    | operations/test_daemon_operation.py |
    | operations/test_fleet_operation.py |
    | operations/test_mining_cycle.py |
    | operations/test_mining_operation.py |
    | operations/test_navigation_operation.py |
    | operations/test_routing_operation.py |
    | operations/test_scout_coordination_operation.py |
    | test_sell_all_type_consistency.py |
