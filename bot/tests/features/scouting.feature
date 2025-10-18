Feature: Scouting validation

  Scenario Outline: Validate Scouting module <module>
    When I execute the "scouting" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_j53_market_exclusion_bug.py |
    | test_partition_overlap_bug.py |
    | test_scout_coordinator_bugs_real_world.py |
    | test_scout_coordinator_exclude_markets.py |
    | test_scout_coordinator_exclude_markets_cache_bug.py |
    | test_scout_coordinator_market_dropping_bug.py |
    | test_scout_coordinator_partitioning.py |
    | test_scout_market_price_mapping_bug.py |
    | test_scout_markets_list_fix.py |
    | test_scout_tour_visualization.py |
    | test_stationary_scout_imbalance_fix.py |

