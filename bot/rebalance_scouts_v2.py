#!/usr/bin/env python3
"""
Scout Rebalancing Script v2 for X1-GH18

Uses greedy tour-time-aware assignment to create balanced partitions.
"""

import json
import sys
from typing import Dict, List, Tuple, Set
import math

# All 26 unique markets from log analysis
ALL_MARKETS = [
    "X1-GH18-A1", "X1-GH18-A2", "X1-GH18-A4", "X1-GH18-B6", "X1-GH18-B7",
    "X1-GH18-C43", "X1-GH18-C44", "X1-GH18-D45", "X1-GH18-D46",
    "X1-GH18-E47", "X1-GH18-E48", "X1-GH18-E49", "X1-GH18-EZ5E",
    "X1-GH18-F50", "X1-GH18-F51", "X1-GH18-G52", "X1-GH18-G53",
    "X1-GH18-H55", "X1-GH18-H56", "X1-GH18-H57", "X1-GH18-H58",
    "X1-GH18-I59", "X1-GH18-I60", "X1-GH18-J61", "X1-GH18-J62",
    "X1-GH18-K95"
]

# Ship list
SHIPS = [
    "SILMARETH-2", "SILMARETH-3", "SILMARETH-4", "SILMARETH-5",
    "SILMARETH-6", "SILMARETH-7", "SILMARETH-8", "SILMARETH-9",
    "SILMARETH-A", "SILMARETH-B", "SILMARETH-C"
]

def get_market_coordinates(graph_data: Dict) -> Dict[str, Tuple[int, int]]:
    """Extract market coordinates from graph data"""
    coords = {}
    waypoints = graph_data.get('waypoints', {})

    for market in ALL_MARKETS:
        wp = waypoints.get(market)
        if wp:
            coords[market] = (wp['x'], wp['y'])

    return coords

def distance(p1: Tuple[int, int], p2: Tuple[int, int]) -> float:
    """Euclidean distance between two points"""
    return math.sqrt((p2[0] - p1[0])**2 + (p2[1] - p1[1])**2)

def estimate_tour_time_with_market(current_markets: List[str], new_market: str,
                                   coords: Dict[str, Tuple[int, int]]) -> float:
    """
    Estimate tour time if we add new_market to current_markets

    Uses nearest-neighbor approximation
    """
    all_markets = current_markets + [new_market]

    if len(all_markets) == 1:
        return 22.0  # Just overhead

    market_coords = [coords[m] for m in all_markets if m in coords]

    if len(market_coords) <= 1:
        return 22.0

    # Nearest neighbor tour
    visited = set()
    current = 0
    visited.add(0)
    total_distance = 0.0

    for _ in range(len(market_coords) - 1):
        nearest = None
        nearest_dist = float('inf')

        for i in range(len(market_coords)):
            if i not in visited:
                d = distance(market_coords[current], market_coords[i])
                if d < nearest_dist:
                    nearest_dist = d
                    nearest = i

        if nearest is not None:
            total_distance += nearest_dist
            visited.add(nearest)
            current = nearest

    # Return to start
    total_distance += distance(market_coords[current], market_coords[0])

    # SpaceTraders formula
    travel_time = round((total_distance * 26) / 9)
    overhead = len(all_markets) * 22

    return travel_time + overhead

def greedy_partition_by_tour_time(coords: Dict[str, Tuple[int, int]],
                                  num_ships: int) -> List[List[str]]:
    """
    Greedy partitioning: assign each market to scout with shortest tour time

    This ensures balanced tour times from the start.
    """
    print(f"\nGreedy partitioning by tour time...")

    partitions = [[] for _ in range(num_ships)]
    tour_times = [0.0] * num_ships
    unassigned = set(ALL_MARKETS)

    # Sort markets by X coordinate (spatial locality helps)
    sorted_markets = sorted(ALL_MARKETS, key=lambda m: coords[m][0] if m in coords else 0)

    iteration = 0
    while unassigned and iteration < 100:
        iteration += 1

        # Find scout with minimum tour time
        scout_idx = min(range(num_ships), key=lambda i: tour_times[i])

        # Find unassigned market that minimizes incremental tour time
        best_market = None
        best_incremental_time = float('inf')

        for market in sorted_markets:
            if market not in unassigned:
                continue

            # Calculate incremental time
            current_time = tour_times[scout_idx]
            new_time = estimate_tour_time_with_market(partitions[scout_idx], market, coords)
            incremental = new_time - current_time

            if incremental < best_incremental_time:
                best_incremental_time = incremental
                best_market = market

        if best_market is None:
            break

        # Assign market
        partitions[scout_idx].append(best_market)
        # Recalculate tour time for this scout
        tour_times[scout_idx] = estimate_tour_time_with_market(partitions[scout_idx][:-1], best_market, coords)
        unassigned.remove(best_market)

        if iteration % 5 == 0:
            print(f"  Assigned {len(ALL_MARKETS) - len(unassigned)}/{len(ALL_MARKETS)} markets...")

    print(f"✅ Greedy assignment complete: {len(ALL_MARKETS) - len(unassigned)} markets assigned")

    return partitions

def validate_partitions(partitions: Dict[str, List[str]]) -> bool:
    """
    Validate that partitions are disjoint (no overlaps)
    """
    all_markets = []
    for markets in partitions.values():
        all_markets.extend(markets)

    # Check for duplicates
    if len(all_markets) != len(set(all_markets)):
        print("\n❌ VALIDATION FAILED: Overlapping markets detected!")

        from collections import Counter
        counts = Counter(all_markets)
        duplicates = [m for m, c in counts.items() if c > 1]

        for dup in duplicates:
            ships_with_dup = [ship for ship, markets in partitions.items() if dup in markets]
            print(f"   {dup} assigned to: {', '.join(ships_with_dup)}")

        return False

    # Check all markets covered
    if set(all_markets) != set(ALL_MARKETS):
        missing = set(ALL_MARKETS) - set(all_markets)
        print(f"\n⚠️  WARNING: Missing markets: {missing}")
        return False

    print("\n✅ VALIDATION PASSED: No overlaps detected!")
    return True

def calculate_tour_stats(partitions: Dict[str, List[str]], coords: Dict[str, Tuple[int, int]]):
    """Calculate and print tour statistics"""
    tour_times = []

    for ship, markets in partitions.items():
        tour_time = estimate_tour_time_with_market(markets[:-1], markets[-1], coords) if markets else 0.0
        tour_times.append(tour_time)

    if not tour_times:
        return

    min_time = min(tour_times) / 60
    max_time = max(tour_times) / 60
    avg_time = sum(tour_times) / len(tour_times) / 60
    variance = max(abs(t - sum(tour_times)/len(tour_times)) / (sum(tour_times)/len(tour_times)) for t in tour_times)

    print(f"\nTour Time Statistics:")
    print(f"  Min: {min_time:.1f} min")
    print(f"  Max: {max_time:.1f} min")
    print(f"  Avg: {avg_time:.1f} min")
    print(f"  Variance: {variance*100:.1f}%")
    print(f"  Range: {max_time/min_time:.1f}x" if min_time > 0 else "  Range: N/A")

def main():
    # Load graph data
    print("Loading graph data...")
    graph_file = "/Users/andres.camacho/Development/Personal/spacetradersV2/bot/var/data/graphs/X1-GH18_graph.json"

    try:
        with open(graph_file, 'r') as f:
            graph_data = json.load(f)
    except FileNotFoundError:
        print(f"❌ Graph file not found: {graph_file}")
        return 1

    coords = get_market_coordinates(graph_data)
    print(f"✅ Loaded coordinates for {len(coords)} markets")

    # Greedy partition
    partition_lists = greedy_partition_by_tour_time(coords, len(SHIPS))

    # Convert to dict
    partitions = {}
    for i, ship in enumerate(SHIPS):
        partitions[ship] = sorted(partition_lists[i])

    # Validate
    if not validate_partitions(partitions):
        return 1

    # Calculate stats
    calculate_tour_stats(partitions, coords)

    # Print final assignments
    print("\n" + "="*70)
    print("FINAL BALANCED TOUR ASSIGNMENTS")
    print("="*70)

    for ship in SHIPS:
        markets = partitions[ship]
        tour_time = estimate_tour_time_with_market(markets[:-1], markets[-1], coords) if len(markets) > 0 else 0.0
        time_min = tour_time / 60

        print(f"{ship}: {len(markets):2d} markets, {time_min:6.1f} min")
        print(f"  Markets: {', '.join(markets)}")

    print("="*70 + "\n")

    # Save config
    output_file = "/Users/andres.camacho/Development/Personal/spacetradersV2/bot/config/agents/scout_partitions_X1-GH18.json"

    config = {
        "system": "X1-GH18",
        "ships": SHIPS,
        "partitions": partitions,
        "algorithm": "2opt",
        "generated_by": "rebalance_scouts_v2.py (greedy tour-time)",
        "validation": "strict_disjoint",
        "timestamp": "2025-10-09T11:30:00Z"
    }

    with open(output_file, 'w') as f:
        json.dump(config, f, indent=2)

    print(f"💾 Saved partitions to: {output_file}\n")

    return 0

if __name__ == '__main__':
    sys.exit(main())
