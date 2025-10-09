#!/usr/bin/env python3
"""
Test to reproduce A2 partition overlap bug in X1-GH18 system

The bug: When partitioning 28 markets across 4 ships using geographic slicing,
waypoint A2 appears in BOTH Scout-2's partition AND Scout-5's partition.

Expected: Each market appears in exactly ONE partition (disjoint sets)
Actual: A2 appears in multiple partitions
"""

import pytest
from spacetraders_bot.core.market_partitioning import MarketPartitioner


def test_geographic_partition_disjoint_sets():
    """
    Test that geographic partitioning creates truly disjoint sets

    This reproduces the X1-GH18 bug where A2 was assigned to multiple partitions.
    """
    # Simplified X1-GH18 scenario: 28 markets, 4 ships
    # Using realistic coordinates that trigger the boundary condition bug

    graph = {
        'waypoints': {
            'X1-GH18-A1': {'x': -100, 'y': 0, 'traits': ['MARKETPLACE']},
            'X1-GH18-A2': {'x': 0, 'y': 0, 'traits': ['MARKETPLACE']},  # The problematic waypoint
            'X1-GH18-A3': {'x': 50, 'y': 10, 'traits': ['MARKETPLACE']},
            'X1-GH18-A4': {'x': 60, 'y': 15, 'traits': ['MARKETPLACE']},
            'X1-GH18-B6': {'x': 100, 'y': 20, 'traits': ['MARKETPLACE']},
            'X1-GH18-B7': {'x': 150, 'y': 30, 'traits': ['MARKETPLACE']},
            'X1-GH18-C43': {'x': 200, 'y': 40, 'traits': ['MARKETPLACE']},
            'X1-GH18-C44': {'x': 250, 'y': 50, 'traits': ['MARKETPLACE']},
            'X1-GH18-D45': {'x': 300, 'y': 60, 'traits': ['MARKETPLACE']},
            'X1-GH18-D46': {'x': 350, 'y': 70, 'traits': ['MARKETPLACE']},
            'X1-GH18-E47': {'x': 400, 'y': 80, 'traits': ['MARKETPLACE']},
            'X1-GH18-E48': {'x': 450, 'y': 90, 'traits': ['MARKETPLACE']},
            'X1-GH18-E49': {'x': 500, 'y': 100, 'traits': ['MARKETPLACE']},
            'X1-GH18-F50': {'x': 550, 'y': 110, 'traits': ['MARKETPLACE']},
            'X1-GH18-F51': {'x': 600, 'y': 120, 'traits': ['MARKETPLACE']},
            'X1-GH18-G52': {'x': 650, 'y': 130, 'traits': ['MARKETPLACE']},
            'X1-GH18-G53': {'x': 700, 'y': 140, 'traits': ['MARKETPLACE']},
            'X1-GH18-H55': {'x': 750, 'y': 150, 'traits': ['MARKETPLACE']},
            'X1-GH18-H56': {'x': 800, 'y': 160, 'traits': ['MARKETPLACE']},
            'X1-GH18-H57': {'x': 850, 'y': 170, 'traits': ['MARKETPLACE']},
            'X1-GH18-H58': {'x': 900, 'y': 180, 'traits': ['MARKETPLACE']},
            'X1-GH18-I59': {'x': 950, 'y': 190, 'traits': ['MARKETPLACE']},
            'X1-GH18-I60': {'x': 1000, 'y': 200, 'traits': ['MARKETPLACE']},
            'X1-GH18-J61': {'x': 1050, 'y': 210, 'traits': ['MARKETPLACE']},
            'X1-GH18-J62': {'x': 1100, 'y': 220, 'traits': ['MARKETPLACE']},
            'X1-GH18-K95': {'x': 1150, 'y': 230, 'traits': ['MARKETPLACE']},
            'X1-GH18-EZ5E': {'x': -50, 'y': -10, 'traits': ['MARKETPLACE']},  # Close to A2
            'X1-GH18-Z99': {'x': 1200, 'y': 240, 'traits': ['MARKETPLACE']},
        }
    }

    markets = list(graph['waypoints'].keys())
    ships = ['SILMARETH-2', 'SILMARETH-3', 'SILMARETH-4', 'SILMARETH-5']

    # Create partitioner
    partitioner = MarketPartitioner(
        graph=graph,
        markets=markets,
        ships=ships
    )

    # Get geographic partitions
    result = partitioner.partition("geographic")
    partitions = result.partitions

    # Print partitions for debugging
    print("\n=== GEOGRAPHIC PARTITIONS ===")
    for ship, ship_markets in sorted(partitions.items()):
        print(f"{ship}: {len(ship_markets)} markets - {ship_markets}")

    # CRITICAL CHECK: Verify partitions are disjoint
    all_markets = []
    for ship, ship_markets in partitions.items():
        all_markets.extend(ship_markets)

    # Count occurrences of each market
    market_counts = {}
    for market in all_markets:
        market_counts[market] = market_counts.get(market, 0) + 1

    # Find duplicates
    duplicates = {market: count for market, count in market_counts.items() if count > 1}

    # Print diagnostic info if test fails
    if duplicates:
        print("\n=== PARTITION OVERLAP DETECTED ===")
        for market, count in duplicates.items():
            print(f"Market {market} appears in {count} partitions:")
            for ship, ship_markets in partitions.items():
                if market in ship_markets:
                    print(f"  - {ship}: position {ship_markets.index(market)+1}/{len(ship_markets)}")

    # ASSERTION: No market should appear more than once
    assert len(duplicates) == 0, \
        f"Geographic partitioning created overlapping partitions! Duplicates: {duplicates}"


def test_geographic_partition_boundary_case():
    """
    Test the specific boundary condition that causes the bug

    When a waypoint falls exactly on the boundary between geographic slices,
    it should be assigned to ONE slice, not multiple.
    """
    # Create a minimal test case with a market exactly on a slice boundary
    graph = {
        'waypoints': {
            # Markets spread from x=0 to x=300
            'M1': {'x': 0, 'y': 0, 'traits': ['MARKETPLACE']},      # First slice
            'M2': {'x': 100, 'y': 0, 'traits': ['MARKETPLACE']},    # Boundary (should be in ONE slice only)
            'M3': {'x': 200, 'y': 0, 'traits': ['MARKETPLACE']},    # Second slice
            'M4': {'x': 300, 'y': 0, 'traits': ['MARKETPLACE']},    # Third slice
        }
    }

    markets = ['M1', 'M2', 'M3', 'M4']
    ships = ['SHIP-1', 'SHIP-2', 'SHIP-3']

    partitioner = MarketPartitioner(
        graph=graph,
        markets=markets,
        ships=ships
    )

    result = partitioner.partition("geographic")
    partitions = result.partitions

    # Check for overlaps
    all_markets = []
    for ship_markets in partitions.values():
        all_markets.extend(ship_markets)

    market_counts = {}
    for market in all_markets:
        market_counts[market] = market_counts.get(market, 0) + 1

    duplicates = {m: c for m, c in market_counts.items() if c > 1}

    # Print diagnostic
    if duplicates:
        print(f"\nBoundary case failed! Duplicates: {duplicates}")
        print("Partitions:")
        for ship, ship_markets in partitions.items():
            print(f"  {ship}: {ship_markets}")

    assert len(duplicates) == 0, \
        f"Boundary markets assigned to multiple slices: {duplicates}"


if __name__ == '__main__':
    # Run tests
    pytest.main([__file__, '-v'])
